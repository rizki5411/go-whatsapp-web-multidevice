package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
)

const testQueuePhone = "628123456789@s.whatsapp.net"

// stubQueueRepo records what the enqueue path persists.
type stubQueueRepo struct {
	mu       sync.Mutex
	enqueued []*domainMessageQueue.QueuedMessage
	nextID   int64
	err      error
}

func (r *stubQueueRepo) Enqueue(msg *domainMessageQueue.QueuedMessage) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	msg.ID = r.nextID
	r.enqueued = append(r.enqueued, msg)
	return nil
}

func (r *stubQueueRepo) last() *domainMessageQueue.QueuedMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.enqueued) == 0 {
		return nil
	}
	return r.enqueued[len(r.enqueued)-1]
}

func (r *stubQueueRepo) FetchPendingByDevice(string, time.Time, int) ([]*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}
func (r *stubQueueRepo) ClaimForSending(int64) (bool, error)     { return true, nil }
func (r *stubQueueRepo) MarkSent(int64, string, time.Time) error { return nil }
func (r *stubQueueRepo) MarkFailed(int64, string) error          { return nil }
func (r *stubQueueRepo) LastSentAt(string) (*time.Time, error)   { return nil, nil }
func (r *stubQueueRepo) CountByDeviceStatus(string) (map[string]int, error) {
	return map[string]int{}, nil
}
func (r *stubQueueRepo) ListByDevice(domainMessageQueue.ListFilter) ([]*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}
func (r *stubQueueRepo) CancelPending(string, int64) (*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}
func (r *stubQueueRepo) ResetInterruptedSending() (int64, error)  { return 0, nil }
func (r *stubQueueRepo) ListPendingMediaPaths() ([]string, error) { return nil, nil }
func (r *stubQueueRepo) DeleteDeviceQueue(string) error           { return nil }

// queueSendStub captures the request each Send* method receives, so tests can
// assert on what the dispatcher rebuilt.
type queueSendStub struct {
	domainSend.ISendUsecase
	text    *domainSend.MessageRequest
	image   *domainSend.ImageRequest
	file    *domainSend.FileRequest
	sticker *domainSend.StickerRequest
	// imageBytes is what the rebuilt upload actually contained.
	imageBytes []byte
	err        error
}

func (s *queueSendStub) SendText(_ context.Context, request domainSend.MessageRequest) (domainSend.GenericResponse, error) {
	s.text = &request
	return domainSend.GenericResponse{MessageID: "WA-TEXT"}, s.err
}

func (s *queueSendStub) SendImage(_ context.Context, request domainSend.ImageRequest) (domainSend.GenericResponse, error) {
	s.image = &request
	if request.Image != nil {
		f, err := request.Image.Open()
		if err == nil {
			s.imageBytes, _ = io.ReadAll(f)
			_ = f.Close()
		}
	}
	return domainSend.GenericResponse{MessageID: "WA-IMAGE"}, s.err
}

func (s *queueSendStub) SendFile(_ context.Context, request domainSend.FileRequest) (domainSend.GenericResponse, error) {
	s.file = &request
	return domainSend.GenericResponse{MessageID: "WA-FILE"}, s.err
}

func (s *queueSendStub) SendSticker(_ context.Context, request domainSend.StickerRequest) (domainSend.GenericResponse, error) {
	s.sticker = &request
	return domainSend.GenericResponse{MessageID: "WA-STICKER"}, s.err
}

// useTempQueueDir points config.PathMessageQueue at a temp dir for one test.
func useTempQueueDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := config.PathMessageQueue
	config.PathMessageQueue = dir
	t.Cleanup(func() { config.PathMessageQueue = original })
	return dir
}

func writeQueueFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func deviceContext(t *testing.T, deviceID string) context.Context {
	t.Helper()
	instance := whatsapp.NewDeviceInstance(deviceID, nil, nil)
	return whatsapp.ContextWithDevice(context.Background(), instance)
}

// A small file stays in memory inside ReadForm; the rebuilt header must still
// open and read back byte for byte.
func TestRehydrateMultipartFileRoundTripsSmallFile(t *testing.T) {
	dir := useTempQueueDir(t)
	content := []byte("hello queued media")
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-photo.jpg", content)

	header, cleanup, err := rehydrateMultipartFile(path, "image", "image/png")
	if err != nil {
		t.Fatalf("rehydrateMultipartFile: %v", err)
	}
	defer cleanup()

	if header.Filename != "photo.jpg" {
		t.Fatalf("Filename = %q, want the original %q", header.Filename, "photo.jpg")
	}
	if header.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", header.Size, len(content))
	}

	f, err := header.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

// Anything over ReadForm's ~10MB in-memory budget must spill to a temp file
// rather than sit in RAM. This is the path a queued video takes.
func TestRehydrateMultipartFileSpillsLargeFileToDisk(t *testing.T) {
	dir := useTempQueueDir(t)
	content := bytes.Repeat([]byte("v"), 12<<20) // 12MB, past the 10MB budget
	content[0] = 'A'
	content[len(content)-1] = 'Z'
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-clip.mp4", content)

	header, cleanup, err := rehydrateMultipartFile(path, "video", "video/mp4")
	if err != nil {
		t.Fatalf("rehydrateMultipartFile: %v", err)
	}
	defer cleanup()

	if header.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", header.Size, len(content))
	}

	f, err := header.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	// A spilled part is backed by a real file on disk.
	if _, isFile := f.(*os.File); !isFile {
		t.Fatalf("large part is %T, want it spilled to *os.File", f)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(content) || got[0] != 'A' || got[len(got)-1] != 'Z' {
		t.Fatalf("content mismatch: len=%d first=%q last=%q", len(got), got[0], got[len(got)-1])
	}
}

// The durable queue file must survive rehydration, because the send path may
// move the copy it is handed.
func TestRehydrateMultipartFileLeavesSourceIntact(t *testing.T) {
	dir := useTempQueueDir(t)
	content := []byte("durable")
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-doc.pdf", content)

	header, cleanup, err := rehydrateMultipartFile(path, "file", "application/pdf")
	if err != nil {
		t.Fatalf("rehydrateMultipartFile: %v", err)
	}
	f, _ := header.Open()
	if f != nil {
		_ = f.Close()
	}
	cleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("source file was removed: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("source content changed to %q", got)
	}
}

func TestRehydrateMultipartFileMissingSource(t *testing.T) {
	dir := useTempQueueDir(t)
	_, _, err := rehydrateMultipartFile(filepath.Join(dir, "nope.jpg"), "image", "image/jpeg")
	if err == nil {
		t.Fatal("expected an error for a missing source file")
	}
	if !strings.Contains(err.Error(), "failed to open queued media") {
		t.Fatalf("error = %v", err)
	}
}

// Regression: the rebuilt part used to default to application/octet-stream,
// which ValidateSendImage/Sticker/Video/Audio reject outright, so every queued
// image, sticker, video and audio send failed at dispatch time.
func TestRehydrateMultipartFilePreservesContentType(t *testing.T) {
	dir := useTempQueueDir(t)
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-pic.png", []byte("png-bytes"))

	header, cleanup, err := rehydrateMultipartFile(path, "image", "image/png")
	if err != nil {
		t.Fatalf("rehydrateMultipartFile: %v", err)
	}
	defer cleanup()

	if got := header.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}

	// And the real validator has to accept the rebuilt request.
	if err := validations.ValidateSendImage(context.Background(), domainSend.ImageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Image:       header,
	}); err != nil {
		t.Fatalf("ValidateSendImage rejected the rebuilt upload: %v", err)
	}
}

// Rows queued before media_mime existed have none stored, so it is recovered
// from the extension (or by sniffing) rather than left as octet-stream.
func TestRehydrateMultipartFileRecoversMissingContentType(t *testing.T) {
	dir := useTempQueueDir(t)
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-pic.png", []byte("png-bytes"))

	header, cleanup, err := rehydrateMultipartFile(path, "image", "")
	if err != nil {
		t.Fatalf("rehydrateMultipartFile: %v", err)
	}
	defer cleanup()

	if got := header.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("recovered Content-Type = %q, want image/png", got)
	}
}

// A filename with no usable extension falls back to content sniffing.
func TestDetectQueuedMediaMIMESniffsContent(t *testing.T) {
	dir := useTempQueueDir(t)
	// Real PNG signature, no extension on the file name.
	signature := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	content := append(signature, bytes.Repeat([]byte{0}, 32)...)
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-noext", content)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	got := detectQueuedMediaMIME(path, f)
	if got != "image/png" {
		t.Fatalf("detectQueuedMediaMIME = %q, want image/png", got)
	}
	// The reader must be rewound so the caller can still stream the whole file.
	all, err := io.ReadAll(f)
	if err != nil || len(all) != len(content) {
		t.Fatalf("source not rewound: read %d of %d bytes (err=%v)", len(all), len(content), err)
	}
}

func TestOriginalQueuedFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strips uuid prefix", "abcdefabcdefabcdefabcdefabcdefabcdef-photo.jpg", "photo.jpg"},
		{"keeps plain name", "photo.jpg", "photo.jpg"},
		{"keeps name without separator", "abcdefabcdefabcdefabcdefabcdefabcdef.jpg", "abcdefabcdefabcdefabcdefabcdefabcdef.jpg"},
		{"keeps name when nothing follows the separator", "abcdefabcdefabcdefabcdefabcdefabcdef-", "abcdefabcdefabcdefabcdefabcdefabcdef-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := originalQueuedFilename(tc.in); got != tc.want {
				t.Fatalf("originalQueuedFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The payload column must round-trip every field the send path reads back.
func TestQueuedPayloadRoundTripsRequestFields(t *testing.T) {
	replyID := "3EB0ABCDEF"
	imageURL := "https://example.com/a.jpg"
	duration := 3600

	image := domainSend.ImageRequest{
		BaseRequest:    domainSend.BaseRequest{Phone: testQueuePhone, Duration: &duration, IsForwarded: true},
		Caption:        "with caption",
		ReplyMessageID: &replyID,
		ImageURL:       &imageURL,
		ViewOnce:       true,
		Compress:       true,
		HD:             true,
	}
	encoded, err := json.Marshal(image)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded domainSend.ImageRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Phone != image.Phone || decoded.Caption != image.Caption {
		t.Fatalf("phone/caption lost: %+v", decoded)
	}
	if decoded.Duration == nil || *decoded.Duration != 3600 || !decoded.IsForwarded {
		t.Fatalf("base fields lost: %+v", decoded.BaseRequest)
	}
	if decoded.ReplyMessageID == nil || *decoded.ReplyMessageID != replyID {
		t.Fatalf("reply id lost: %+v", decoded.ReplyMessageID)
	}
	if decoded.ImageURL == nil || *decoded.ImageURL != imageURL {
		t.Fatalf("image url lost: %+v", decoded.ImageURL)
	}
	if !decoded.ViewOnce || !decoded.Compress || !decoded.HD {
		t.Fatalf("bool flags lost: %+v", decoded)
	}
}

func TestEnqueueSendPersistsTextAndReturnsQueuedStatus(t *testing.T) {
	repo := &stubQueueRepo{}
	service := serviceSend{messageQueueRepo: repo}
	request := domainSend.MessageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Message:     "queued hello",
		Queue:       true,
	}

	response, err := service.enqueueSend(
		deviceContext(t, "device-1"), domainMessageQueue.TypeText, request.Phone, request, nil)
	if err != nil {
		t.Fatalf("enqueueSend: %v", err)
	}

	if response.Status != domainSend.StatusQueued {
		t.Fatalf("Status = %q, want %q", response.Status, domainSend.StatusQueued)
	}
	if response.QueueID != 1 {
		t.Fatalf("QueueID = %d, want 1", response.QueueID)
	}
	if response.MessageID != "" {
		t.Fatalf("MessageID = %q, want empty for a queued send", response.MessageID)
	}

	row := repo.last()
	if row == nil {
		t.Fatal("nothing was enqueued")
	}
	if row.DeviceID != "device-1" {
		t.Fatalf("DeviceID = %q, want the device slot id", row.DeviceID)
	}
	if row.MessageType != domainMessageQueue.TypeText || row.Phone != testQueuePhone {
		t.Fatalf("row = %+v", row)
	}
	if row.Status != domainMessageQueue.StatusPending {
		t.Fatalf("Status = %q, want %q", row.Status, domainMessageQueue.StatusPending)
	}
	if row.MediaPath != "" {
		t.Fatalf("MediaPath = %q, want empty for text", row.MediaPath)
	}
	var persisted domainSend.MessageRequest
	if err := json.Unmarshal([]byte(row.Payload), &persisted); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	if persisted.Message != "queued hello" {
		t.Fatalf("persisted message = %q", persisted.Message)
	}
}

func TestEnqueueSendRequiresADevice(t *testing.T) {
	service := serviceSend{messageQueueRepo: &stubQueueRepo{}}
	_, err := service.enqueueSend(context.Background(), domainMessageQueue.TypeText, testQueuePhone,
		domainSend.MessageRequest{}, nil)
	if err == nil {
		t.Fatal("expected an error without a device in context")
	}
	if !strings.Contains(err.Error(), "device") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnqueueSendFailsWhenQueueUnavailable(t *testing.T) {
	service := serviceSend{}
	_, err := service.enqueueSend(deviceContext(t, "device-1"), domainMessageQueue.TypeText,
		testQueuePhone, domainSend.MessageRequest{}, nil)
	if err == nil {
		t.Fatal("expected an error when the queue repository is nil")
	}
}

// SendText with queue:true must never reach the client lookup, so a device that
// is offline can still accept a queued message.
func TestSendTextWithQueueDoesNotRequireAClient(t *testing.T) {
	repo := &stubQueueRepo{}
	service := serviceSend{messageQueueRepo: repo}

	response, err := service.SendText(deviceContext(t, "device-1"), domainSend.MessageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Message:     "hold this",
		Queue:       true,
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if response.Status != domainSend.StatusQueued || response.QueueID == 0 {
		t.Fatalf("response = %+v, want a queued status with an id", response)
	}
	if repo.last() == nil {
		t.Fatal("nothing was enqueued")
	}
}

// A request that fails validation must be rejected at request time rather than
// silently queued and failed minutes later.
func TestSendTextWithQueueStillValidates(t *testing.T) {
	repo := &stubQueueRepo{}
	service := serviceSend{messageQueueRepo: repo}

	_, err := service.SendText(deviceContext(t, "device-1"), domainSend.MessageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Message:     "", // required
		Queue:       true,
	})
	if err == nil {
		t.Fatal("expected validation to reject an empty message")
	}
	if len(repo.enqueued) != 0 {
		t.Fatalf("enqueued %d rows, want 0 for an invalid request", len(repo.enqueued))
	}
}

func TestMessageQueueDispatcherReplaysTextWithQueueDisabled(t *testing.T) {
	stub := &queueSendStub{}
	dispatch := NewMessageQueueDispatcher(stub)

	payload, _ := json.Marshal(domainSend.MessageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Message:     "replayed",
		Queue:       true, // must be cleared before replay
	})

	messageID, err := dispatch(context.Background(), &domainMessageQueue.QueuedMessage{
		ID: 7, DeviceID: "device-1", MessageType: domainMessageQueue.TypeText,
		Payload: string(payload),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if messageID != "WA-TEXT" {
		t.Fatalf("messageID = %q", messageID)
	}
	if stub.text == nil {
		t.Fatal("SendText was not called")
	}
	if stub.text.Queue {
		t.Fatal("Queue must be false on replay, otherwise the row re-queues itself forever")
	}
	if stub.text.Message != "replayed" {
		t.Fatalf("message = %q", stub.text.Message)
	}
}

func TestEnqueueSendRecordsUploadContentType(t *testing.T) {
	useTempQueueDir(t)
	repo := &stubQueueRepo{}
	service := serviceSend{messageQueueRepo: repo}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="pic.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write([]byte("png-bytes")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(0)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	defer form.RemoveAll()
	upload := form.File["image"][0]

	if _, err := service.enqueueSend(deviceContext(t, "device-1"), domainMessageQueue.TypeImage,
		testQueuePhone, domainSend.ImageRequest{
			BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		}, upload); err != nil {
		t.Fatalf("enqueueSend: %v", err)
	}

	row := repo.last()
	if row == nil {
		t.Fatal("nothing was enqueued")
	}
	if row.MediaMime != "image/png" {
		t.Fatalf("MediaMime = %q, want image/png", row.MediaMime)
	}
	if row.MediaPath == "" {
		t.Fatal("MediaPath should point at the durable copy")
	}
}

func TestMessageQueueDispatcherRebuildsImageUpload(t *testing.T) {
	dir := useTempQueueDir(t)
	content := []byte("image-bytes")
	path := writeQueueFile(t, dir, "abcdefabcdefabcdefabcdefabcdefabcdef-pic.png", content)

	stub := &queueSendStub{}
	dispatch := NewMessageQueueDispatcher(stub)

	payload, _ := json.Marshal(domainSend.ImageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		Caption:     "look",
		Compress:    true,
	})

	if _, err := dispatch(context.Background(), &domainMessageQueue.QueuedMessage{
		ID: 8, DeviceID: "device-1", MessageType: domainMessageQueue.TypeImage,
		Payload: string(payload), MediaPath: path, MediaMime: "image/png",
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if stub.image == nil {
		t.Fatal("SendImage was not called")
	}
	if stub.image.Image == nil {
		t.Fatal("the rebuilt request has no upload")
	}
	if stub.image.Image.Filename != "pic.png" {
		t.Fatalf("Filename = %q, want pic.png", stub.image.Image.Filename)
	}
	if !bytes.Equal(stub.imageBytes, content) {
		t.Fatalf("upload content = %q, want %q", stub.imageBytes, content)
	}
	if got := stub.image.Image.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("rebuilt Content-Type = %q, want image/png", got)
	}
	if stub.image.Caption != "look" || !stub.image.Compress {
		t.Fatalf("request fields lost: %+v", stub.image)
	}
}

// A *_url request carries no file, so nothing should be rebuilt.
func TestMessageQueueDispatcherLeavesURLVariantWithoutUpload(t *testing.T) {
	stub := &queueSendStub{}
	dispatch := NewMessageQueueDispatcher(stub)

	imageURL := "https://example.com/a.jpg"
	payload, _ := json.Marshal(domainSend.ImageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: testQueuePhone},
		ImageURL:    &imageURL,
	})

	if _, err := dispatch(context.Background(), &domainMessageQueue.QueuedMessage{
		ID: 9, DeviceID: "device-1", MessageType: domainMessageQueue.TypeImage,
		Payload: string(payload),
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if stub.image.Image != nil {
		t.Fatal("no upload should be rebuilt when media_path is empty")
	}
	if stub.image.ImageURL == nil || *stub.image.ImageURL != imageURL {
		t.Fatalf("image url lost: %+v", stub.image.ImageURL)
	}
}

func TestMessageQueueDispatcherRejectsUnknownType(t *testing.T) {
	dispatch := NewMessageQueueDispatcher(&queueSendStub{})
	_, err := dispatch(context.Background(), &domainMessageQueue.QueuedMessage{
		ID: 10, MessageType: "poll", Payload: "{}",
	})
	if err == nil {
		t.Fatal("expected an error for an unsupported queued type")
	}
	if !strings.Contains(err.Error(), "unsupported queued message type") {
		t.Fatalf("error = %v", err)
	}
}

func TestMessageQueueDispatcherRejectsBadPayload(t *testing.T) {
	dispatch := NewMessageQueueDispatcher(&queueSendStub{})
	_, err := dispatch(context.Background(), &domainMessageQueue.QueuedMessage{
		ID: 11, MessageType: domainMessageQueue.TypeText, Payload: "{not json",
	})
	if err == nil {
		t.Fatal("expected an error for an undecodable payload")
	}
}

// persistQueuedMedia must keep the extension, which the audio and thumbnail
// paths depend on.
func TestPersistQueuedMediaKeepsExtension(t *testing.T) {
	useTempQueueDir(t)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "voice note.ogg")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("audio-bytes")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(0)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	defer form.RemoveAll()

	path, err := persistQueuedMedia(form.File["audio"][0])
	if err != nil {
		t.Fatalf("persistQueuedMedia: %v", err)
	}
	if filepath.Ext(path) != ".ogg" {
		t.Fatalf("stored path %q lost its extension", path)
	}
	if !strings.HasPrefix(path, config.PathMessageQueue) {
		t.Fatalf("stored path %q is outside %q", path, config.PathMessageQueue)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stored file unreadable: %v", err)
	}
	if string(got) != "audio-bytes" {
		t.Fatalf("stored content = %q", got)
	}

	// And it round-trips back into a usable upload.
	header, cleanup, err := rehydrateMultipartFile(path, "audio", "audio/ogg")
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	defer cleanup()
	if header.Filename != "voice note.ogg" {
		t.Fatalf("rebuilt filename = %q", header.Filename)
	}
}
