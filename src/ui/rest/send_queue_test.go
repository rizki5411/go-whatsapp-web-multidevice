package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// The opt-in `queue` flag has to survive both binding paths the send endpoints
// support (JSON body and multipart upload), and a queued send has to look
// different on the wire from a delivered one.

// sendQueueStubUsecase records the request and returns a canned response.
type sendQueueStubUsecase struct {
	domainSend.ISendUsecase
	textRequest  domainSend.MessageRequest
	imageRequest domainSend.ImageRequest
	response     domainSend.GenericResponse
}

func (s *sendQueueStubUsecase) SendText(_ context.Context, request domainSend.MessageRequest) (domainSend.GenericResponse, error) {
	s.textRequest = request
	return s.response, nil
}

func (s *sendQueueStubUsecase) SendImage(_ context.Context, request domainSend.ImageRequest) (domainSend.GenericResponse, error) {
	s.imageRequest = request
	return s.response, nil
}

func newSendQueueTestApp(stub *sendQueueStubUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	controller := Send{Service: stub}
	app.Post("/send/message", controller.SendText)
	app.Post("/send/image", controller.SendImage)
	return app
}

func decodeSendResponse(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload
}

func TestSendTextBindsQueueFromJSONBody(t *testing.T) {
	stub := &sendQueueStubUsecase{response: domainSend.GenericResponse{
		Status: domainSend.StatusQueued, QueueID: 42,
	}}
	app := newSendQueueTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/send/message",
		strings.NewReader(`{"phone":"628123456789","message":"queued hello","queue":true}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if !stub.textRequest.Queue {
		t.Fatal("Queue did not bind from the JSON body")
	}
	if stub.textRequest.Message != "queued hello" {
		t.Fatalf("Message = %q", stub.textRequest.Message)
	}

	payload := decodeSendResponse(t, resp.Body)
	if payload["message"] != domainSend.StatusQueued {
		t.Fatalf("message = %v, want %q", payload["message"], domainSend.StatusQueued)
	}
	results, ok := payload["results"].(map[string]any)
	if !ok {
		t.Fatalf("results = %v", payload["results"])
	}
	if results["status"] != domainSend.StatusQueued {
		t.Fatalf("results.status = %v", results["status"])
	}
	if got, ok := results["queue_id"].(float64); !ok || int64(got) != 42 {
		t.Fatalf("results.queue_id = %v, want 42", results["queue_id"])
	}
}

// The multipart path matters because the media endpoints are usually called that
// way; a missing `form` tag would silently drop the flag.
func TestSendImageBindsQueueFromMultipartForm(t *testing.T) {
	stub := &sendQueueStubUsecase{response: domainSend.GenericResponse{
		Status: domainSend.StatusQueued, QueueID: 7,
	}}
	app := newSendQueueTestApp(stub)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("phone", "628123456789"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.WriteField("caption", "with caption"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.WriteField("queue", "true"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	part, err := writer.CreateFormFile("image", "pic.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("image-bytes")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send/image", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if !stub.imageRequest.Queue {
		t.Fatal("Queue did not bind from the multipart form")
	}
	if stub.imageRequest.Image == nil {
		t.Fatal("the upload was not attached")
	}
	if stub.imageRequest.Caption != "with caption" {
		t.Fatalf("Caption = %q", stub.imageRequest.Caption)
	}
}

// The default must stay off, and a direct send's response must not grow a
// queue_id field.
func TestSendTextWithoutQueueKeepsDirectSendResponse(t *testing.T) {
	stub := &sendQueueStubUsecase{response: domainSend.GenericResponse{
		MessageID: "WA-1", Status: "Message sent to 628123456789",
	}}
	app := newSendQueueTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/send/message",
		strings.NewReader(`{"phone":"628123456789","message":"direct hello"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if stub.textRequest.Queue {
		t.Fatal("Queue must default to false when the field is absent")
	}

	results, ok := decodeSendResponse(t, resp.Body)["results"].(map[string]any)
	if !ok {
		t.Fatal("results missing")
	}
	if _, present := results["queue_id"]; present {
		t.Fatalf("queue_id must be omitted from a direct send response, got %v", results["queue_id"])
	}
	if results["message_id"] != "WA-1" {
		t.Fatalf("message_id = %v, want WA-1", results["message_id"])
	}
}

// Explicit false must behave exactly like an absent field.
func TestSendTextQueueFalseIsDirectSend(t *testing.T) {
	stub := &sendQueueStubUsecase{response: domainSend.GenericResponse{MessageID: "WA-2", Status: "sent"}}
	app := newSendQueueTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/send/message",
		strings.NewReader(`{"phone":"628123456789","message":"hi","queue":false}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if stub.textRequest.Queue {
		t.Fatal("queue:false must not enable the queue")
	}
}
