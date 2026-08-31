package chatstorage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
)

// Per-device outbound send queue storage. Mirrors the device command config
// methods, kept in its own file so sqlite_repository.go does not grow further.
//
// *SQLiteRepository satisfies the queue contract implicitly; the queue is not
// part of IChatStorageRepository, so no delegation stubs are needed in
// infrastructure/whatsapp/chatstorage_wrapper.go.
var _ domainMessageQueue.IMessageQueueRepository = (*SQLiteRepository)(nil)

// NewMessageQueueRepository exposes the queue contract over the same *sql.DB the
// chat storage uses, so wiring needs no type assertion.
func NewMessageQueueRepository(db *sql.DB) domainMessageQueue.IMessageQueueRepository {
	return &SQLiteRepository{db: db}
}

const messageQueueColumns = `id, device_id, device_jid, message_type, phone, payload, media_path, media_mime, status, scheduled_at, sent_at, message_id, last_error, created_at, updated_at`

// maxQueueErrorLength bounds what a driver error can write into last_error.
const maxQueueErrorLength = 500

// scanQueuedMessage decodes one row and is shared by the QueryRow and rows
// paths. sent_at is nullable: it stays unset until a successful send.
func (r *SQLiteRepository) scanQueuedMessage(scanner interface{ Scan(...any) error }) (*domainMessageQueue.QueuedMessage, error) {
	msg := &domainMessageQueue.QueuedMessage{}
	var sentAt sql.NullTime
	err := scanner.Scan(
		&msg.ID, &msg.DeviceID, &msg.DeviceJID, &msg.MessageType, &msg.Phone,
		&msg.Payload, &msg.MediaPath, &msg.MediaMime, &msg.Status, &msg.ScheduledAt, &sentAt,
		&msg.MessageID, &msg.LastError, &msg.CreatedAt, &msg.UpdatedAt,
	)
	if err != nil {
		return msg, err
	}
	if sentAt.Valid {
		sent := sentAt.Time
		msg.SentAt = &sent
	}
	return msg, nil
}

// Enqueue inserts a pending row and populates msg.ID.
func (r *SQLiteRepository) Enqueue(msg *domainMessageQueue.QueuedMessage) error {
	if msg == nil || strings.TrimSpace(msg.DeviceID) == "" {
		return fmt.Errorf("queued message requires a device id")
	}
	if strings.TrimSpace(msg.MessageType) == "" {
		return fmt.Errorf("queued message requires a message type")
	}

	now := time.Now()
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	msg.UpdatedAt = now
	if msg.ScheduledAt.IsZero() {
		msg.ScheduledAt = now
	}
	if strings.TrimSpace(msg.Status) == "" {
		msg.Status = domainMessageQueue.StatusPending
	}
	if strings.TrimSpace(msg.Payload) == "" {
		msg.Payload = "{}"
	}

	res, err := r.db.Exec(`
		INSERT INTO message_queue (
			device_id, device_jid, message_type, phone, payload, media_path, media_mime,
			status, scheduled_at, message_id, last_error, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)
	`, msg.DeviceID, msg.DeviceJID, msg.MessageType, msg.Phone, msg.Payload,
		msg.MediaPath, msg.MediaMime, msg.Status, msg.ScheduledAt, msg.CreatedAt, msg.UpdatedAt)
	if err != nil {
		return err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to load new queued message id: %w", err)
	}
	msg.ID = id
	return nil
}

// FetchPendingByDevice returns due pending rows for one device, oldest first.
// Always scoped to a single device_id so one device's worker can never drain
// another device's queue.
func (r *SQLiteRepository) FetchPendingByDevice(deviceID string, now time.Time, limit int) ([]*domainMessageQueue.QueuedMessage, error) {
	if strings.TrimSpace(deviceID) == "" {
		return nil, fmt.Errorf("device id is required")
	}
	if limit <= 0 {
		limit = 1
	}

	rows, err := r.db.Query(`
		SELECT `+messageQueueColumns+`
		FROM message_queue
		WHERE device_id = ? AND status = ? AND scheduled_at <= ?
		ORDER BY created_at, id
		LIMIT ?
	`, deviceID, domainMessageQueue.StatusPending, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var queued []*domainMessageQueue.QueuedMessage
	for rows.Next() {
		msg, err := r.scanQueuedMessage(rows)
		if err != nil {
			return nil, err
		}
		queued = append(queued, msg)
	}
	return queued, rows.Err()
}

// ClaimForSending flips pending to sending and reports whether this caller won
// the row. The status predicate is what makes the claim atomic, so two workers
// (or two processes sharing one SQLite file) can never both send the same row.
func (r *SQLiteRepository) ClaimForSending(id int64) (bool, error) {
	res, err := r.db.Exec(`
		UPDATE message_queue
		SET status = ?, updated_at = ?
		WHERE id = ? AND status = ?
	`, domainMessageQueue.StatusSending, time.Now(), id, domainMessageQueue.StatusPending)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (r *SQLiteRepository) MarkSent(id int64, messageID string, sentAt time.Time) error {
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	_, err := r.db.Exec(`
		UPDATE message_queue
		SET status = ?, message_id = ?, sent_at = ?, last_error = '', updated_at = ?
		WHERE id = ?
	`, domainMessageQueue.StatusSent, messageID, sentAt, time.Now(), id)
	return err
}

func (r *SQLiteRepository) MarkFailed(id int64, reason string) error {
	_, err := r.db.Exec(`
		UPDATE message_queue
		SET status = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`, domainMessageQueue.StatusFailed, truncateQueueError(reason), time.Now(), id)
	return err
}

// LastSentAt returns the newest successful send for a device, or (nil, nil) when
// it has never sent through the queue.
func (r *SQLiteRepository) LastSentAt(deviceID string) (*time.Time, error) {
	if strings.TrimSpace(deviceID) == "" {
		return nil, nil
	}

	var sentAt sql.NullTime
	err := r.db.QueryRow(`
		SELECT sent_at
		FROM message_queue
		WHERE device_id = ? AND status = ? AND sent_at IS NOT NULL
		ORDER BY sent_at DESC
		LIMIT 1
	`, deviceID, domainMessageQueue.StatusSent).Scan(&sentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !sentAt.Valid {
		return nil, nil
	}
	sent := sentAt.Time
	return &sent, nil
}

func (r *SQLiteRepository) ListByDevice(filter domainMessageQueue.ListFilter) ([]*domainMessageQueue.QueuedMessage, error) {
	if strings.TrimSpace(filter.DeviceID) == "" {
		return nil, fmt.Errorf("device id is required")
	}

	query := "SELECT " + messageQueueColumns + " FROM message_queue WHERE device_id = ?"
	args := []any{filter.DeviceID}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at, id"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query += " LIMIT ?"
	args = append(args, limit)
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var queued []*domainMessageQueue.QueuedMessage
	for rows.Next() {
		msg, err := r.scanQueuedMessage(rows)
		if err != nil {
			return nil, err
		}
		queued = append(queued, msg)
	}
	return queued, rows.Err()
}

// CountByDeviceStatus returns status to row count for one device, so the listing
// endpoint can show a summary without fetching every row.
func (r *SQLiteRepository) CountByDeviceStatus(deviceID string) (map[string]int, error) {
	counts := map[string]int{}
	if strings.TrimSpace(deviceID) == "" {
		return counts, nil
	}

	rows, err := r.db.Query(
		"SELECT status, COUNT(*) FROM message_queue WHERE device_id = ? GROUP BY status", deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// CancelPending marks a pending row cancelled and returns it so the caller can
// drop its media file. Every statement is scoped to deviceID: without that, a
// request naming one device could cancel (or merely reveal) another device's row
// by id, since ids are global to the table.
func (r *SQLiteRepository) CancelPending(deviceID string, id int64) (*domainMessageQueue.QueuedMessage, error) {
	if strings.TrimSpace(deviceID) == "" {
		return nil, fmt.Errorf("device id is required")
	}

	msg, err := r.scanQueuedMessage(r.db.QueryRow(
		"SELECT "+messageQueueColumns+" FROM message_queue WHERE id = ? AND device_id = ? LIMIT 1",
		id, deviceID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if msg.Status != domainMessageQueue.StatusPending {
		return msg, domainMessageQueue.ErrQueueRowNotPending
	}

	res, err := r.db.Exec(`
		UPDATE message_queue
		SET status = ?, updated_at = ?
		WHERE id = ? AND device_id = ? AND status = ?
	`, domainMessageQueue.StatusCancelled, time.Now(), id, deviceID, domainMessageQueue.StatusPending)
	if err != nil {
		return nil, err
	}
	// Zero rows means the worker claimed the row between the read and the write.
	if affected, _ := res.RowsAffected(); affected != 1 {
		return msg, domainMessageQueue.ErrQueueRowNotPending
	}

	msg.Status = domainMessageQueue.StatusCancelled
	return msg, nil
}

// ResetInterruptedSending fails rows a crash left mid-send. Replaying them could
// duplicate a message WhatsApp already accepted, which is worse than losing one.
func (r *SQLiteRepository) ResetInterruptedSending() (int64, error) {
	res, err := r.db.Exec(`
		UPDATE message_queue
		SET status = ?, last_error = ?, updated_at = ?
		WHERE status = ?
	`, domainMessageQueue.StatusFailed, "interrupted by restart", time.Now(),
		domainMessageQueue.StatusSending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListPendingMediaPaths returns the media files still referenced by a row that
// may yet be sent, so anything else in the queue directory can be reclaimed.
func (r *SQLiteRepository) ListPendingMediaPaths() ([]string, error) {
	rows, err := r.db.Query(`
		SELECT media_path FROM message_queue
		WHERE media_path <> '' AND status IN (?, ?)
	`, domainMessageQueue.StatusPending, domainMessageQueue.StatusSending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (r *SQLiteRepository) DeleteDeviceQueue(deviceID string) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	_, err := r.db.Exec("DELETE FROM message_queue WHERE device_id = ?", deviceID)
	return err
}

func truncateQueueError(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > maxQueueErrorLength {
		return reason[:maxQueueErrorLength]
	}
	return reason
}
