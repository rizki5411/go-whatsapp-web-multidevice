package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// queueRepoSpy records the arguments the handler passes down. The cancel route
// must forward the device id, otherwise the repository cannot scope the write
// and one device could cancel another's row by guessing its id.
type queueRepoSpy struct {
	cancelDeviceID string
	cancelID       int64
	cancelResult   *domainMessageQueue.QueuedMessage
	cancelErr      error

	listFilter domainMessageQueue.ListFilter
	listResult []*domainMessageQueue.QueuedMessage
	counts     map[string]int
}

func (r *queueRepoSpy) Enqueue(*domainMessageQueue.QueuedMessage) error { return nil }
func (r *queueRepoSpy) FetchPendingByDevice(string, time.Time, int) ([]*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}
func (r *queueRepoSpy) ClaimForSending(int64) (bool, error)      { return false, nil }
func (r *queueRepoSpy) MarkSent(int64, string, time.Time) error  { return nil }
func (r *queueRepoSpy) MarkFailed(int64, string) error           { return nil }
func (r *queueRepoSpy) LastSentAt(string) (*time.Time, error)    { return nil, nil }
func (r *queueRepoSpy) ResetInterruptedSending() (int64, error)  { return 0, nil }
func (r *queueRepoSpy) ListPendingMediaPaths() ([]string, error) { return nil, nil }
func (r *queueRepoSpy) DeleteDeviceQueue(string) error           { return nil }

func (r *queueRepoSpy) ListByDevice(filter domainMessageQueue.ListFilter) ([]*domainMessageQueue.QueuedMessage, error) {
	r.listFilter = filter
	return r.listResult, nil
}

func (r *queueRepoSpy) CountByDeviceStatus(string) (map[string]int, error) {
	if r.counts == nil {
		return map[string]int{}, nil
	}
	return r.counts, nil
}

func (r *queueRepoSpy) CancelPending(deviceID string, id int64) (*domainMessageQueue.QueuedMessage, error) {
	r.cancelDeviceID = deviceID
	r.cancelID = id
	return r.cancelResult, r.cancelErr
}

// queueTestDeviceManager is a registry holding one resolvable device, since the
// queue routes resolve the :device_id path param themselves (they are registered
// outside DeviceMiddleware, which only reads header/query).
func queueTestDeviceManager(deviceID string) *whatsapp.DeviceManager {
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance(deviceID, nil, nil))
	return dm
}

func newQueueTestApp(h *MessageQueueHandler) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Get("/devices/:device_id/queue", h.ListQueue)
	app.Delete("/devices/:device_id/queue/:queue_id", h.CancelQueued)
	return app
}

func TestListQueueRejectsUnknownStatus(t *testing.T) {
	h := &MessageQueueHandler{DeviceManager: queueTestDeviceManager("dev-1"), QueueRepo: &queueRepoSpy{}}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/devices/dev-1/queue?status=bogus", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCancelQueuedRejectsBadQueueID(t *testing.T) {
	spy := &queueRepoSpy{}
	h := &MessageQueueHandler{DeviceManager: queueTestDeviceManager("dev-1"), QueueRepo: spy}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/devices/dev-1/queue/not-a-number", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if spy.cancelID != 0 {
		t.Fatal("repository must not be called for an unparseable id")
	}
}

// Regression: an earlier version cancelled the row first and only then compared
// its device id, so a request naming the wrong device returned 404 while having
// already cancelled someone else's message. The device must reach the query.
func TestCancelQueuedPassesDeviceIDToRepository(t *testing.T) {
	// Repository reports "no such row for this device".
	spy := &queueRepoSpy{cancelResult: nil}
	h := &MessageQueueHandler{DeviceManager: queueTestDeviceManager("dev-1"), QueueRepo: spy}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/devices/dev-1/queue/7", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if spy.cancelDeviceID != "dev-1" {
		t.Fatalf("cancel device id = %q, want dev-1; without it the repository cannot scope the write", spy.cancelDeviceID)
	}
	if spy.cancelID != 7 {
		t.Fatalf("cancel id = %d, want 7", spy.cancelID)
	}
}

// The listing must be scoped to the resolved device as well.
func TestListQueueScopesFilterToDevice(t *testing.T) {
	spy := &queueRepoSpy{counts: map[string]int{"pending": 2}}
	h := &MessageQueueHandler{DeviceManager: queueTestDeviceManager("dev-1"), QueueRepo: spy}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/devices/dev-1/queue?status=pending&limit=5&offset=2", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if spy.listFilter.DeviceID != "dev-1" {
		t.Fatalf("filter device id = %q, want dev-1", spy.listFilter.DeviceID)
	}
	if spy.listFilter.Status != domainMessageQueue.StatusPending {
		t.Fatalf("filter status = %q", spy.listFilter.Status)
	}
	if spy.listFilter.Limit != 5 || spy.listFilter.Offset != 2 {
		t.Fatalf("filter paging = %d/%d, want 5/2", spy.listFilter.Limit, spy.listFilter.Offset)
	}
}

// A device that is not in the registry must not reach the repository at all.
func TestCancelQueuedRejectsUnknownDevice(t *testing.T) {
	spy := &queueRepoSpy{}
	h := &MessageQueueHandler{DeviceManager: queueTestDeviceManager("dev-1"), QueueRepo: spy}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/devices/dev-unknown/queue/7", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if spy.cancelDeviceID != "" || spy.cancelID != 0 {
		t.Fatalf("repository was called with (%q, %d) for an unknown device", spy.cancelDeviceID, spy.cancelID)
	}
}

func TestListQueueRequiresQueueRepository(t *testing.T) {
	h := &MessageQueueHandler{}
	app := newQueueTestApp(h)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/devices/dev-1/queue", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["message"] != "message queue not available" {
		t.Fatalf("message = %v", payload["message"])
	}
}
