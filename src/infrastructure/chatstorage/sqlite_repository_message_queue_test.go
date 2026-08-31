package chatstorage

import (
	"errors"
	"testing"
	"time"

	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
)

func enqueueTestRow(t *testing.T, repo *SQLiteRepository, deviceID, phone string) *domainMessageQueue.QueuedMessage {
	t.Helper()
	row := &domainMessageQueue.QueuedMessage{
		DeviceID:    deviceID,
		DeviceJID:   deviceID + "@s.whatsapp.net",
		MessageType: domainMessageQueue.TypeText,
		Phone:       phone,
		Payload:     `{"phone":"` + phone + `","message":"hi"}`,
	}
	if err := repo.Enqueue(row); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if row.ID == 0 {
		t.Fatal("expected the row id to be populated after insert")
	}
	return row
}

func TestMessageQueueEnqueueAndFetchOrdersByCreatedAt(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	first := enqueueTestRow(t, repo, "device-a", "628111111111")
	// created_at has second-level granularity in some drivers, so make the
	// ordering unambiguous rather than relying on insertion speed.
	first.CreatedAt = time.Now().Add(-2 * time.Minute)
	if _, err := repo.db.Exec("UPDATE message_queue SET created_at = ? WHERE id = ?", first.CreatedAt, first.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	second := enqueueTestRow(t, repo, "device-a", "628222222222")

	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("fetched %d rows, want 2", len(rows))
	}
	if rows[0].ID != first.ID || rows[1].ID != second.ID {
		t.Fatalf("order = [%d %d], want [%d %d]", rows[0].ID, rows[1].ID, first.ID, second.ID)
	}
	if rows[0].Status != domainMessageQueue.StatusPending {
		t.Fatalf("status = %q, want pending", rows[0].Status)
	}
	if rows[0].Phone != "628111111111" || rows[0].DeviceJID != "device-a@s.whatsapp.net" {
		t.Fatalf("row round-trip lost fields: %+v", rows[0])
	}
	if rows[0].SentAt != nil {
		t.Fatalf("SentAt = %v, want nil for a pending row", rows[0].SentAt)
	}
}

// The core isolation guarantee: one device's fetch never returns another's rows.
func TestMessageQueueFetchIsScopedToOneDevice(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	rowA := enqueueTestRow(t, repo, "device-a", "628111111111")
	rowB := enqueueTestRow(t, repo, "device-b", "628222222222")

	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != rowA.ID {
		t.Fatalf("device-a fetched %d rows, want only %d", len(rows), rowA.ID)
	}

	rows, err = repo.FetchPendingByDevice("device-b", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != rowB.ID {
		t.Fatalf("device-b fetched %d rows, want only %d", len(rows), rowB.ID)
	}
}

// A row scheduled for later must not be handed out early.
func TestMessageQueueFetchRespectsScheduledAt(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	row := &domainMessageQueue.QueuedMessage{
		DeviceID:    "device-a",
		MessageType: domainMessageQueue.TypeText,
		Phone:       "628111111111",
		ScheduledAt: time.Now().Add(time.Hour),
	}
	if err := repo.Enqueue(row); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("fetched %d rows, want 0 before scheduled_at", len(rows))
	}

	rows, err = repo.FetchPendingByDevice("device-a", time.Now().Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("fetched %d rows, want 1 after scheduled_at", len(rows))
	}
}

// The claim is what prevents two workers sending the same row.
func TestMessageQueueClaimForSendingIsExclusive(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")

	claimed, err := repo.ClaimForSending(row.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim should win")
	}

	claimed, err = repo.ClaimForSending(row.ID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("second claim must lose")
	}

	// A claimed row is no longer pending, so it drops out of the fetch.
	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("fetched %d rows, want 0 once claimed", len(rows))
	}
}

func TestMessageQueueMarkSentAndLastSentAt(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")

	if _, err := repo.ClaimForSending(row.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	sentAt := time.Now().Truncate(time.Second)
	if err := repo.MarkSent(row.ID, "WA-1", sentAt); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	stored, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("list: %v (rows=%d)", err, len(stored))
	}
	if stored[0].Status != domainMessageQueue.StatusSent || stored[0].MessageID != "WA-1" {
		t.Fatalf("row = %+v", stored[0])
	}
	if stored[0].SentAt == nil {
		t.Fatal("SentAt should be populated after a successful send")
	}

	last, err := repo.LastSentAt("device-a")
	if err != nil {
		t.Fatalf("last sent: %v", err)
	}
	if last == nil {
		t.Fatal("LastSentAt should return the send time")
	}
	if last.Unix() != sentAt.Unix() {
		t.Fatalf("LastSentAt = %v, want %v", last, sentAt)
	}

	// Another device has no history of its own.
	otherLast, err := repo.LastSentAt("device-b")
	if err != nil {
		t.Fatalf("last sent for device-b: %v", err)
	}
	if otherLast != nil {
		t.Fatalf("device-b LastSentAt = %v, want nil", otherLast)
	}
}

func TestMessageQueueMarkFailedRecordsReason(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")

	if err := repo.MarkFailed(row.ID, "recipient is not on whatsapp"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	stored, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("list: %v", err)
	}
	if stored[0].Status != domainMessageQueue.StatusFailed {
		t.Fatalf("status = %q, want failed", stored[0].Status)
	}
	if stored[0].LastError != "recipient is not on whatsapp" {
		t.Fatalf("last_error = %q", stored[0].LastError)
	}
	// A failed row is never handed out again.
	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("fetched %d rows, want 0 (no retry)", len(rows))
	}
}

func TestMessageQueueCancelPending(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")

	cancelled, err := repo.CancelPending("device-a", row.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled == nil || cancelled.Status != domainMessageQueue.StatusCancelled {
		t.Fatalf("cancelled = %+v", cancelled)
	}

	// Cancelling twice reports the real state instead of pretending to succeed.
	again, err := repo.CancelPending("device-a", row.ID)
	if !errors.Is(err, domainMessageQueue.ErrQueueRowNotPending) {
		t.Fatalf("second cancel err = %v, want ErrQueueRowNotPending", err)
	}
	if again == nil || again.Status != domainMessageQueue.StatusCancelled {
		t.Fatalf("second cancel row = %+v", again)
	}

	missing, err := repo.CancelPending("device-a", 999999)
	if err != nil || missing != nil {
		t.Fatalf("cancel of a missing row = (%v, %v), want (nil, nil)", missing, err)
	}
}

// Ids are global to the table, so a cancel naming the wrong device must neither
// mutate nor reveal the row.
func TestMessageQueueCancelPendingIsScopedToTheOwningDevice(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")

	got, err := repo.CancelPending("device-b", row.ID)
	if err != nil {
		t.Fatalf("cross-device cancel err = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("cross-device cancel returned %+v, want nil", got)
	}

	stored, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("list: %v", err)
	}
	if stored[0].Status != domainMessageQueue.StatusPending {
		t.Fatalf("row status = %q, want it left pending by a cross-device cancel", stored[0].Status)
	}

	if _, err := repo.CancelPending("", row.ID); err == nil {
		t.Fatal("expected an error cancelling without a device id")
	}
}

func TestMessageQueueResetInterruptedSending(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	row := enqueueTestRow(t, repo, "device-a", "628111111111")
	survivor := enqueueTestRow(t, repo, "device-a", "628222222222")

	if _, err := repo.ClaimForSending(row.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	count, err := repo.ResetInterruptedSending()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if count != 1 {
		t.Fatalf("reset %d rows, want 1", count)
	}

	stored, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[int64]*domainMessageQueue.QueuedMessage{}
	for _, s := range stored {
		byID[s.ID] = s
	}
	if got := byID[row.ID]; got == nil || got.Status != domainMessageQueue.StatusFailed {
		t.Fatalf("interrupted row = %+v, want failed", got)
	}
	if got := byID[row.ID]; got != nil && got.LastError != "interrupted by restart" {
		t.Fatalf("last_error = %q", got.LastError)
	}
	// Pending rows are untouched: only the claimed one was mid-flight.
	if got := byID[survivor.ID]; got == nil || got.Status != domainMessageQueue.StatusPending {
		t.Fatalf("pending row = %+v, want still pending", got)
	}
}

func TestMessageQueueListFilterAndCounts(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	pending := enqueueTestRow(t, repo, "device-a", "628111111111")
	failed := enqueueTestRow(t, repo, "device-a", "628222222222")
	enqueueTestRow(t, repo, "device-b", "628333333333")

	if err := repo.MarkFailed(failed.ID, "nope"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	rows, err := repo.ListByDevice(domainMessageQueue.ListFilter{
		DeviceID: "device-a", Status: domainMessageQueue.StatusPending,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != pending.ID {
		t.Fatalf("filtered list = %d rows, want just %d", len(rows), pending.ID)
	}

	counts, err := repo.CountByDeviceStatus("device-a")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[domainMessageQueue.StatusPending] != 1 || counts[domainMessageQueue.StatusFailed] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	// device-b's row must not leak into device-a's numbers.
	if len(counts) != 2 {
		t.Fatalf("counts = %v, want only device-a statuses", counts)
	}
}

// media_mime has to survive the round trip, or the rebuilt upload is rejected by
// the send validators.
func TestMessageQueueMediaMimeRoundTrips(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	row := &domainMessageQueue.QueuedMessage{
		DeviceID: "device-a", MessageType: domainMessageQueue.TypeImage,
		Phone: "628111111111", MediaPath: "statics/senditems/queue/a.png",
		MediaMime: "image/png",
	}
	if err := repo.Enqueue(row); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	rows, err := repo.FetchPendingByDevice("device-a", time.Now(), 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("fetch: %v (rows=%d)", err, len(rows))
	}
	if rows[0].MediaMime != "image/png" {
		t.Fatalf("MediaMime = %q, want image/png", rows[0].MediaMime)
	}
	if rows[0].MediaPath != "statics/senditems/queue/a.png" {
		t.Fatalf("MediaPath = %q", rows[0].MediaPath)
	}

	// A text row keeps it empty rather than null.
	textRow := enqueueTestRow(t, repo, "device-b", "628222222222")
	stored, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-b"})
	if err != nil || len(stored) != 1 || stored[0].ID != textRow.ID {
		t.Fatalf("list: %v", err)
	}
	if stored[0].MediaMime != "" {
		t.Fatalf("text row MediaMime = %q, want empty", stored[0].MediaMime)
	}
}

func TestMessageQueueListPendingMediaPaths(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	withMedia := &domainMessageQueue.QueuedMessage{
		DeviceID: "device-a", MessageType: domainMessageQueue.TypeImage,
		Phone: "628111111111", MediaPath: "statics/senditems/queue/a.jpg",
	}
	if err := repo.Enqueue(withMedia); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	sentWithMedia := &domainMessageQueue.QueuedMessage{
		DeviceID: "device-a", MessageType: domainMessageQueue.TypeImage,
		Phone: "628222222222", MediaPath: "statics/senditems/queue/b.jpg",
	}
	if err := repo.Enqueue(sentWithMedia); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := repo.MarkSent(sentWithMedia.ID, "WA-1", time.Now()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	enqueueTestRow(t, repo, "device-a", "628444444444") // text, no media

	paths, err := repo.ListPendingMediaPaths()
	if err != nil {
		t.Fatalf("list media: %v", err)
	}
	if len(paths) != 1 || paths[0] != "statics/senditems/queue/a.jpg" {
		t.Fatalf("paths = %v, want only the unsent one", paths)
	}
}

func TestMessageQueueDeleteDeviceQueue(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	enqueueTestRow(t, repo, "device-a", "628111111111")
	keep := enqueueTestRow(t, repo, "device-b", "628222222222")

	if err := repo.DeleteDeviceQueue("device-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("device-a still has %d rows", len(rows))
	}

	rows, err = repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-b"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != keep.ID {
		t.Fatalf("device-b rows = %d, want its own row kept", len(rows))
	}
}

// Removing a device must not leave its queue behind.
func TestMessageQueueRowsRemovedWithDeviceData(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	enqueueTestRow(t, repo, "device-a", "628111111111")
	keep := enqueueTestRow(t, repo, "device-b", "628222222222")

	if err := repo.DeleteDeviceData("device-a"); err != nil {
		t.Fatalf("delete device data: %v", err)
	}

	rows, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("device-a queue survived DeleteDeviceData: %d rows", len(rows))
	}

	rows, err = repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: "device-b"})
	if err != nil || len(rows) != 1 || rows[0].ID != keep.ID {
		t.Fatalf("device-b queue was affected: %d rows (err=%v)", len(rows), err)
	}
}

func TestMessageQueueTruncateAllChatsClearsQueue(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	enqueueTestRow(t, repo, "device-a", "628111111111")
	enqueueTestRow(t, repo, "device-b", "628222222222")

	if err := repo.TruncateAllChats(); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	for _, deviceID := range []string{"device-a", "device-b"} {
		rows, err := repo.ListByDevice(domainMessageQueue.ListFilter{DeviceID: deviceID})
		if err != nil {
			t.Fatalf("list %s: %v", deviceID, err)
		}
		if len(rows) != 0 {
			t.Fatalf("%s queue survived TruncateAllChats: %d rows", deviceID, len(rows))
		}
	}
}

func TestMessageQueueEnqueueRejectsIncompleteRows(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if err := repo.Enqueue(nil); err == nil {
		t.Fatal("expected an error for a nil row")
	}
	if err := repo.Enqueue(&domainMessageQueue.QueuedMessage{MessageType: domainMessageQueue.TypeText}); err == nil {
		t.Fatal("expected an error without a device id")
	}
	if err := repo.Enqueue(&domainMessageQueue.QueuedMessage{DeviceID: "device-a"}); err == nil {
		t.Fatal("expected an error without a message type")
	}
	if _, err := repo.FetchPendingByDevice("", time.Now(), 1); err == nil {
		t.Fatal("expected an error fetching without a device id")
	}
}
