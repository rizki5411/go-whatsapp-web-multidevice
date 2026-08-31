package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
)

type fakeMessageQueueSource struct {
	devices []messageQueueDevice
}

func (s fakeMessageQueueSource) ListMessageQueueDevices() []messageQueueDevice {
	return s.devices
}

// readyQueueDevice is a device the gate accepts, with a context builder that
// records nothing (the fake dispatcher is what assertions look at).
func readyQueueDevice(id string) messageQueueDevice {
	return messageQueueDevice{
		id:         id,
		ready:      func() bool { return true },
		withDevice: func(ctx context.Context) context.Context { return ctx },
	}
}

// fakeQueueRepo is an in-memory IMessageQueueRepository. Rows are kept per
// device so a test can prove a device's worker never reads another's queue.
type fakeQueueRepo struct {
	mu       sync.Mutex
	rows     map[string][]*domainMessageQueue.QueuedMessage
	lastSent map[string]time.Time
	sent     map[int64]string
	failed   map[int64]string
	claimErr error
	// claimFails simulates losing the claim race.
	claimFails bool
	fetchedFor []string
}

func newFakeQueueRepo() *fakeQueueRepo {
	return &fakeQueueRepo{
		rows:     map[string][]*domainMessageQueue.QueuedMessage{},
		lastSent: map[string]time.Time{},
		sent:     map[int64]string{},
		failed:   map[int64]string{},
	}
}

func (r *fakeQueueRepo) add(deviceID string, ids ...int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		r.rows[deviceID] = append(r.rows[deviceID], &domainMessageQueue.QueuedMessage{
			ID:          id,
			DeviceID:    deviceID,
			MessageType: domainMessageQueue.TypeText,
			Phone:       fmt.Sprintf("62800%d", id),
			Status:      domainMessageQueue.StatusPending,
		})
	}
}

func (r *fakeQueueRepo) Enqueue(msg *domainMessageQueue.QueuedMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[msg.DeviceID] = append(r.rows[msg.DeviceID], msg)
	return nil
}

func (r *fakeQueueRepo) FetchPendingByDevice(deviceID string, _ time.Time, limit int) ([]*domainMessageQueue.QueuedMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetchedFor = append(r.fetchedFor, deviceID)

	var due []*domainMessageQueue.QueuedMessage
	for _, row := range r.rows[deviceID] {
		if row.Status != domainMessageQueue.StatusPending {
			continue
		}
		due = append(due, row)
		if len(due) >= limit {
			break
		}
	}
	return due, nil
}

func (r *fakeQueueRepo) ClaimForSending(id int64) (bool, error) {
	if r.claimErr != nil {
		return false, r.claimErr
	}
	if r.claimFails {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rows := range r.rows {
		for _, row := range rows {
			if row.ID == id {
				if row.Status != domainMessageQueue.StatusPending {
					return false, nil
				}
				row.Status = domainMessageQueue.StatusSending
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *fakeQueueRepo) MarkSent(id int64, messageID string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent[id] = messageID
	r.setStatusLocked(id, domainMessageQueue.StatusSent)
	return nil
}

func (r *fakeQueueRepo) MarkFailed(id int64, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed[id] = reason
	r.setStatusLocked(id, domainMessageQueue.StatusFailed)
	return nil
}

func (r *fakeQueueRepo) setStatusLocked(id int64, status string) {
	for _, rows := range r.rows {
		for _, row := range rows {
			if row.ID == id {
				row.Status = status
			}
		}
	}
}

func (r *fakeQueueRepo) LastSentAt(deviceID string) (*time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts, ok := r.lastSent[deviceID]; ok {
		return &ts, nil
	}
	return nil, nil
}

func (r *fakeQueueRepo) ListByDevice(domainMessageQueue.ListFilter) ([]*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}

func (r *fakeQueueRepo) CountByDeviceStatus(string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (r *fakeQueueRepo) CancelPending(string, int64) (*domainMessageQueue.QueuedMessage, error) {
	return nil, nil
}

func (r *fakeQueueRepo) ResetInterruptedSending() (int64, error) { return 0, nil }

func (r *fakeQueueRepo) ListPendingMediaPaths() ([]string, error) { return nil, nil }

func (r *fakeQueueRepo) DeleteDeviceQueue(string) error { return nil }

func (r *fakeQueueRepo) statusOf(id int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rows := range r.rows {
		for _, row := range rows {
			if row.ID == id {
				return row.Status
			}
		}
	}
	return ""
}

// recordingDispatcher captures every row it is asked to send.
type recordingDispatcher struct {
	mu   sync.Mutex
	seen []*domainMessageQueue.QueuedMessage
	err  error
	done chan struct{}
}

func newRecordingDispatcher() *recordingDispatcher {
	return &recordingDispatcher{done: make(chan struct{}, 16)}
}

func (d *recordingDispatcher) dispatch(_ context.Context, msg *domainMessageQueue.QueuedMessage) (string, error) {
	d.mu.Lock()
	d.seen = append(d.seen, msg)
	d.mu.Unlock()
	d.done <- struct{}{}
	if d.err != nil {
		return "", d.err
	}
	return fmt.Sprintf("WA-%d", msg.ID), nil
}

func (d *recordingDispatcher) calls() []*domainMessageQueue.QueuedMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*domainMessageQueue.QueuedMessage(nil), d.seen...)
}

func newTestQueueScheduler(source messageQueueDeviceSource, repo domainMessageQueue.IMessageQueueRepository, dispatch MessageQueueDispatcher) *messageQueueScheduler {
	s := newMessageQueueScheduler(source, repo, dispatch, 2*time.Minute, 5*time.Minute, time.Minute)
	// Deterministic spacing so tests assert on behavior, not on randomness.
	s.randDelay = func(min, _ time.Duration) time.Duration { return min }
	return s
}

func waitForQueueIdle(t *testing.T, scheduler *messageQueueScheduler, deviceID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		scheduler.mu.Lock()
		inFlight := scheduler.inFlight[deviceID]
		scheduler.mu.Unlock()
		if !inFlight {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for queue worker to finish")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitForDispatch(t *testing.T, dispatcher *recordingDispatcher) {
	t.Helper()
	select {
	case <-dispatcher.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for dispatch")
	}
}

// A device with a full queue must still emit only one message per cycle, which
// is what keeps the random spacing meaningful.
func TestMessageQueueSendsOnlyOneRowPerCycle(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1, 2, 3)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	if !scheduler.startIfDue(context.Background(), readyQueueDevice("device-1")) {
		t.Fatal("expected the worker to start")
	}
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")

	calls := dispatcher.calls()
	if len(calls) != 1 {
		t.Fatalf("dispatch calls = %d, want 1", len(calls))
	}
	if calls[0].ID != 1 {
		t.Fatalf("sent row %d, want the oldest (1)", calls[0].ID)
	}
	if got := repo.statusOf(1); got != domainMessageQueue.StatusSent {
		t.Fatalf("row 1 status = %q, want %q", got, domainMessageQueue.StatusSent)
	}
	if got := repo.statusOf(2); got != domainMessageQueue.StatusPending {
		t.Fatalf("row 2 status = %q, want it left pending", got)
	}
}

// Ten devices sending at once must never share a queue.
func TestMessageQueueKeepsDevicesIsolated(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-a", 1, 2)
	repo.add("device-b", 10, 11)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	for _, id := range []string{"device-a", "device-b"} {
		if !scheduler.startIfDue(context.Background(), readyQueueDevice(id)) {
			t.Fatalf("expected worker to start for %s", id)
		}
	}
	waitForDispatch(t, dispatcher)
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-a")
	waitForQueueIdle(t, scheduler, "device-b")

	byDevice := map[string][]int64{}
	for _, call := range dispatcher.calls() {
		byDevice[call.DeviceID] = append(byDevice[call.DeviceID], call.ID)
	}
	if len(byDevice["device-a"]) != 1 || byDevice["device-a"][0] != 1 {
		t.Fatalf("device-a sent %v, want [1]", byDevice["device-a"])
	}
	if len(byDevice["device-b"]) != 1 || byDevice["device-b"][0] != 10 {
		t.Fatalf("device-b sent %v, want [10]", byDevice["device-b"])
	}
}

// A device that is not connected is not a failure: its rows wait.
func TestMessageQueueSkipsDeviceNotReady(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	notReady := readyQueueDevice("device-1")
	notReady.ready = func() bool { return false }

	if scheduler.startIfDue(context.Background(), notReady) {
		t.Fatal("expected the worker not to start for a device that is not ready")
	}
	if calls := dispatcher.calls(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %d, want 0", len(calls))
	}
	if got := repo.statusOf(1); got != domainMessageQueue.StatusPending {
		t.Fatalf("row status = %q, want it left pending", got)
	}
}

// After a send the device must wait out the gap before the next one.
func TestMessageQueueEnforcesSpacingBetweenSends(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1, 2)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	now := time.Now()
	scheduler.now = func() time.Time { return now }

	device := readyQueueDevice("device-1")
	scheduler.startIfDue(context.Background(), device)
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")

	// Same instant: still inside the 2 minute minimum.
	if scheduler.startIfDue(context.Background(), device) {
		t.Fatal("expected the second send to be held back by the spacing")
	}

	// Past the gap: the next row goes out.
	now = now.Add(3 * time.Minute)
	if !scheduler.startIfDue(context.Background(), device) {
		t.Fatal("expected the second send to be released after the gap")
	}
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")

	if calls := dispatcher.calls(); len(calls) != 2 || calls[1].ID != 2 {
		t.Fatalf("calls = %v, want the second to be row 2", calls)
	}
}

// A failed send is closed out, not retried, and does not block the queue.
func TestMessageQueueMarksFailedWithoutRetry(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1, 2)
	dispatcher := newRecordingDispatcher()
	dispatcher.err = errors.New("recipient is not on whatsapp")
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	now := time.Now()
	scheduler.now = func() time.Time { return now }

	device := readyQueueDevice("device-1")
	scheduler.startIfDue(context.Background(), device)
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")

	if got := repo.statusOf(1); got != domainMessageQueue.StatusFailed {
		t.Fatalf("row 1 status = %q, want %q", got, domainMessageQueue.StatusFailed)
	}
	repo.mu.Lock()
	reason := repo.failed[1]
	repo.mu.Unlock()
	if reason != "recipient is not on whatsapp" {
		t.Fatalf("failure reason = %q", reason)
	}

	// The queue keeps moving: the next row is attempted after the gap.
	now = now.Add(6 * time.Minute)
	dispatcher.err = nil
	if !scheduler.startIfDue(context.Background(), device) {
		t.Fatal("expected the queue to continue past the failed row")
	}
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")

	if got := repo.statusOf(2); got != domainMessageQueue.StatusSent {
		t.Fatalf("row 2 status = %q, want %q", got, domainMessageQueue.StatusSent)
	}
}

// A restart must not fire off a message immediately after the previous process
// just sent one.
func TestMessageQueueSeedsSpacingFromLastSent(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1)
	now := time.Now()
	repo.lastSent["device-1"] = now.Add(-30 * time.Second)

	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)
	scheduler.now = func() time.Time { return now }

	if scheduler.startIfDue(context.Background(), readyQueueDevice("device-1")) {
		t.Fatal("expected the seeded gap to hold the first send back")
	}
	if calls := dispatcher.calls(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %d, want 0", len(calls))
	}
}

// A device with no send history is eligible right away.
func TestMessageQueueSendsImmediatelyWhenDeviceIdle(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	if !scheduler.startIfDue(context.Background(), readyQueueDevice("device-1")) {
		t.Fatal("expected an idle device to send immediately")
	}
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")
}

// Losing the claim (another process, or a cancellation) must not send.
func TestMessageQueueSkipsSendWhenClaimLost(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1)
	repo.claimFails = true
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, dispatcher.dispatch)

	scheduler.startIfDue(context.Background(), readyQueueDevice("device-1"))
	waitForQueueIdle(t, scheduler, "device-1")

	if calls := dispatcher.calls(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %d, want 0 when the claim is lost", len(calls))
	}
}

// An already-running device must not get a second concurrent worker.
func TestMessageQueueDoesNotStartTwiceForOneDevice(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-1", 1, 2)

	release := make(chan struct{})
	dispatcher := newRecordingDispatcher()
	blocking := func(ctx context.Context, msg *domainMessageQueue.QueuedMessage) (string, error) {
		<-release
		return dispatcher.dispatch(ctx, msg)
	}
	scheduler := newTestQueueScheduler(fakeMessageQueueSource{}, repo, blocking)

	device := readyQueueDevice("device-1")
	if !scheduler.startIfDue(context.Background(), device) {
		t.Fatal("expected the first worker to start")
	}
	if scheduler.startIfDue(context.Background(), device) {
		t.Fatal("expected the second start to be refused while one is in flight")
	}

	close(release)
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-1")
}

// processDueDevices must only ever ask about devices the source reports.
func TestMessageQueueProcessDueDevicesUsesSource(t *testing.T) {
	repo := newFakeQueueRepo()
	repo.add("device-a", 1)
	dispatcher := newRecordingDispatcher()
	scheduler := newTestQueueScheduler(
		fakeMessageQueueSource{devices: []messageQueueDevice{readyQueueDevice("device-a")}},
		repo, dispatcher.dispatch)

	scheduler.processDueDevices(context.Background())
	waitForDispatch(t, dispatcher)
	waitForQueueIdle(t, scheduler, "device-a")

	repo.mu.Lock()
	fetched := append([]string(nil), repo.fetchedFor...)
	repo.mu.Unlock()
	for _, deviceID := range fetched {
		if deviceID != "device-a" {
			t.Fatalf("worker read the queue of %q, want only device-a", deviceID)
		}
	}
}

func TestRandomQueueDelayStaysWithinBounds(t *testing.T) {
	min := 2 * time.Minute
	max := 5 * time.Minute
	for i := 0; i < 200; i++ {
		got := randomQueueDelay(min, max)
		if got < min || got > max {
			t.Fatalf("randomQueueDelay() = %s, want within [%s, %s]", got, min, max)
		}
	}
	if got := randomQueueDelay(max, min); got != max {
		t.Fatalf("randomQueueDelay with max<min = %s, want %s", got, max)
	}
}

// media_path comes out of the database, so the delete must be fenced to the
// queue directory.
func TestRemoveQueuedMediaRefusesPathsOutsideQueueDir(t *testing.T) {
	baseDir := t.TempDir()
	originalPath := config.PathMessageQueue
	config.PathMessageQueue = filepath.Join(baseDir, "queue")
	t.Cleanup(func() { config.PathMessageQueue = originalPath })

	if err := os.MkdirAll(config.PathMessageQueue, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	inside := filepath.Join(config.PathMessageQueue, "keep.jpg")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	outside := filepath.Join(baseDir, "outside.jpg")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	RemoveQueuedMedia(outside)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the queue directory was deleted: %v", err)
	}

	RemoveQueuedMedia(inside)
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("file inside the queue directory was not deleted: %v", err)
	}

	// Empty path and a missing file must both be no-ops.
	RemoveQueuedMedia("")
	RemoveQueuedMedia(inside)
}
