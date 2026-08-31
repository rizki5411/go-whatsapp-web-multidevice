package send

// StatusQueued is the Status a send returns when it was persisted to the
// per-device queue rather than delivered. Callers branch on this to tell an
// accepted-for-later from an actually-sent message.
const StatusQueued = "queued"

type GenericResponse struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	// QueueID identifies the message_queue row when Status is StatusQueued.
	// omitempty keeps the direct-send response byte-identical to before.
	QueueID int64 `json:"queue_id,omitempty"`
}
