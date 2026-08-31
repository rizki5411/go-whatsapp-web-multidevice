package messagequeue

import "time"

// IMessageQueueRepository is the persistence contract for the per-device send
// queue.
//
// Deliberately kept separate from chatstorage.IChatStorageRepository: adding
// these methods there would force a matching set of delegation stubs into
// infrastructure/whatsapp/chatstorage_wrapper.go for no benefit. The concrete
// *chatstorage.SQLiteRepository satisfies this interface implicitly.
type IMessageQueueRepository interface {
	// Enqueue inserts a pending row and populates msg.ID.
	Enqueue(msg *QueuedMessage) error

	// FetchPendingByDevice returns pending rows for one device that are due at
	// now, oldest first by created_at. Only ever scoped to a single device_id.
	FetchPendingByDevice(deviceID string, now time.Time, limit int) ([]*QueuedMessage, error)

	// ClaimForSending flips pending -> sending for one row and reports whether
	// this caller won the row. Guards against double sends.
	ClaimForSending(id int64) (bool, error)

	MarkSent(id int64, messageID string, sentAt time.Time) error
	MarkFailed(id int64, reason string) error

	// LastSentAt is the most recent successful send for a device, or nil when
	// the device has never sent through the queue. Used to seed the delay so a
	// restart does not burst.
	LastSentAt(deviceID string) (*time.Time, error)

	ListByDevice(filter ListFilter) ([]*QueuedMessage, error)
	CountByDeviceStatus(deviceID string) (map[string]int, error)

	// CancelPending marks a pending row cancelled and returns it so the caller
	// can drop its media file. Scoped to deviceID so one device can neither
	// cancel nor probe another's queue by guessing ids. Returns (nil, nil) when
	// the device has no such row, and ErrQueueRowNotPending when the row exists
	// but has moved past pending.
	CancelPending(deviceID string, id int64) (*QueuedMessage, error)

	// ResetInterruptedSending fails rows left in sending by a crash. Called once
	// at startup: replaying them could duplicate a message that WhatsApp already
	// accepted, which is worse than losing one.
	ResetInterruptedSending() (int64, error)

	// ListPendingMediaPaths backs the orphan-file sweep.
	ListPendingMediaPaths() ([]string, error)

	DeleteDeviceQueue(deviceID string) error
}
