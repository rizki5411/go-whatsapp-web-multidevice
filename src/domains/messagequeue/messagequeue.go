package messagequeue

import "time"

// Status values for a queued row.
//
// StatusSending is claimed-but-not-yet-confirmed. It exists so a row can never
// be picked up twice: the worker flips pending -> sending with a conditional
// UPDATE before dispatching, so a second worker (or a second process sharing the
// same SQLite file) loses the race and skips the row.
const (
	StatusPending   = "pending"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Message types the queue can replay. Only the personal-message endpoints that
// round-trip through JSON (plus a durable media file) are supported; link,
// location, contact and poll deliberately have no queue support.
const (
	TypeText    = "text"
	TypeImage   = "image"
	TypeFile    = "file"
	TypeVideo   = "video"
	TypeAudio   = "audio"
	TypeSticker = "sticker"
)

// QueuedMessage is one pending outbound send owned by exactly one device.
//
// Payload holds the original send request marshalled to JSON with the
// *multipart.FileHeader field nulled out; MediaPath points at the durable copy
// of the uploaded file instead (empty for text and for the *_url variants,
// whose URL round-trips inside Payload).
type QueuedMessage struct {
	ID          int64  `db:"id"`
	DeviceID    string `db:"device_id"`
	DeviceJID   string `db:"device_jid"`
	MessageType string `db:"message_type"`
	Phone       string `db:"phone"`
	Payload     string `db:"payload"`
	MediaPath   string `db:"media_path"`
	// MediaMime is the Content-Type the upload arrived with. Replayed onto the
	// rebuilt multipart part, because the send validators check it.
	MediaMime   string     `db:"media_mime"`
	Status      string     `db:"status"`
	ScheduledAt time.Time  `db:"scheduled_at"`
	SentAt      *time.Time `db:"sent_at"`
	MessageID   string     `db:"message_id"`
	LastError   string     `db:"last_error"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
}

// ListFilter scopes the management API listing. DeviceID is required; an empty
// Status means "any status".
type ListFilter struct {
	DeviceID string
	Status   string
	Limit    int
	Offset   int
}
