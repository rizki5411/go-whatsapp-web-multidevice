package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// addDeviceStubUsecase implements domainDevice.IDeviceUsecase by embedding the
// interface while recording the arguments actually received by AddDevice.
type addDeviceStubUsecase struct {
	domainDevice.IDeviceUsecase
	receivedDeviceID string
	receivedWebhook  *chatstorage.DeviceWebhookConfig
	addErr           error
}

func (s *addDeviceStubUsecase) AddDevice(_ context.Context, deviceID string, webhook *chatstorage.DeviceWebhookConfig) (*domainDevice.Device, error) {
	s.receivedDeviceID = deviceID
	s.receivedWebhook = webhook
	if s.addErr != nil {
		return nil, s.addErr
	}
	return &domainDevice.Device{ID: deviceID}, nil
}

func (s *addDeviceStubUsecase) LoginDevice(_ context.Context, _ string) (domainApp.LoginResponse, error) {
	return domainApp.LoginResponse{ImagePath: "statics/qrcode/scan-qr-dev1.png"}, nil
}

func newAddDeviceTestApp(stub *addDeviceStubUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	controller := Device{Service: stub}
	app.Post("/devices", controller.AddDevice)
	app.Get("/devices/:device_id/login", controller.LoginDevice)
	return app
}

// TestAddDevice_ForwardsFullWebhookConfig verifies that POST /devices accepts the
// complete webhook configuration (url, secret, events, insecure_skip_verify) that the
// device manager UI sends, instead of silently dropping everything but webhook_url.
func TestAddDevice_ForwardsFullWebhookConfig(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	body := `{
		"device_id": "dev1",
		"webhook_url": "https://hook.example.com",
		"webhook_secret": "s3cret",
		"webhook_events": "message,message.ack",
		"webhook_insecure_skip_verify": true
	}`
	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if stub.receivedDeviceID != "dev1" {
		t.Fatalf("expected device_id dev1, got %q", stub.receivedDeviceID)
	}
	cfg := stub.receivedWebhook
	if cfg == nil {
		t.Fatal("expected webhook config to be forwarded to the usecase, got nil")
	}
	if cfg.WebhookURL == nil || *cfg.WebhookURL != "https://hook.example.com" {
		t.Fatalf("expected webhook_url to be forwarded, got %v", cfg.WebhookURL)
	}
	if cfg.WebhookSecret != "s3cret" {
		t.Fatalf("expected webhook_secret to be forwarded, got %q", cfg.WebhookSecret)
	}
	if cfg.WebhookEvents != "message,message.ack" {
		t.Fatalf("expected webhook_events to be forwarded, got %q", cfg.WebhookEvents)
	}
	if !cfg.WebhookInsecureSkipVerify {
		t.Fatal("expected webhook_insecure_skip_verify to be forwarded as true")
	}
}

// TestAddDevice_NoWebhookFields verifies that a plain device creation without any
// webhook fields passes a nil config to the usecase.
func TestAddDevice_NoWebhookFields(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"device_id":"dev2"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if stub.receivedWebhook != nil {
		t.Fatalf("expected nil webhook config when no webhook fields sent, got %+v", stub.receivedWebhook)
	}

	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if parsed.Results["id"] != "dev2" {
		t.Fatalf("expected result id dev2, got %v", parsed.Results["id"])
	}
}

// TestLoginDevice_QRLinkKeepsRequestPort verifies the QR link points back at the
// host:port the client connected to, so it stays reachable when the app is
// served on a non-default port.
func TestLoginDevice_QRLinkKeepsRequestPort(t *testing.T) {
	app := newAddDeviceTestApp(&addDeviceStubUsecase{})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "http://172.168.0.101:3000/devices/dev1/login", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	want := "http://172.168.0.101:3000/statics/qrcode/scan-qr-dev1.png"
	if parsed.Results["qr_link"] != want {
		t.Fatalf("expected qr_link %q, got %v", want, parsed.Results["qr_link"])
	}
}

// TestAddDevice_ForwardsChosenDeviceID memastikan id pilihan pemanggil benar-benar
// sampai ke usecase — bukan diam-diam dibuang lalu diganti UUID, yang akan
// membuat pemanggil percaya id yang ia ketik sedang dipakai.
func TestAddDevice_ForwardsChosenDeviceID(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"device_id":"  cs-utama  "}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if stub.receivedDeviceID != "cs-utama" {
		t.Fatalf("expected trimmed device id %q, got %q", "cs-utama", stub.receivedDeviceID)
	}
}

// Id kosong tetap sah: itu cara meminta backend membuatkan UUID-nya.
func TestAddDevice_EmptyDeviceIDStillAllowed(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"name":"CS Utama"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if stub.receivedDeviceID != "" {
		t.Fatalf("expected empty device id, got %q", stub.receivedDeviceID)
	}
}

// Bentuk id ditolak SEBELUM device dibuat: id ber-"/" mengubah path rute device
// menjadi rute yang sama sekali lain, dan penolakan yang datang setelah slot
// lahir akan meninggalkan device yang tidak diminta siapa pun.
func TestAddDevice_RejectsMalformedDeviceID(t *testing.T) {
	for _, id := range []string{"cs/utama", "cs utama", "ab", "-cs"} {
		stub := &addDeviceStubUsecase{}
		app := newAddDeviceTestApp(stub)

		req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"device_id":"`+id+`"}`))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed for %q: %v", id, err)
		}

		var parsed struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %q, got %d", id, resp.StatusCode)
		}
		if parsed.Code != "VALIDATION_ERROR" {
			t.Fatalf("expected VALIDATION_ERROR for %q, got %q", id, parsed.Code)
		}
		if stub.receivedDeviceID != "" {
			t.Fatalf("expected AddDevice not to be called for %q", id)
		}
	}
}

// Id yang sudah dipakai harus bisa DIBEDAKAN dari backend rusak: ia satu-satunya
// alasan POST /devices menolak sebuah id, dan pemanggil memperbaikinya cukup
// dengan mengetik id lain. Sebelum DEVICE_ID_TAKEN ada, ini jatuh ke 500.
func TestAddDevice_DuplicateDeviceIDAnswers409(t *testing.T) {
	stub := &addDeviceStubUsecase{addErr: pkgError.DeviceIDTaken("cs-utama")}
	app := newAddDeviceTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"device_id":"cs-utama"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	var parsed struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if parsed.Code != "DEVICE_ID_TAKEN" {
		t.Fatalf("expected code DEVICE_ID_TAKEN, got %q", parsed.Code)
	}
}
