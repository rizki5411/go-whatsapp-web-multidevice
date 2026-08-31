package send

type MessageRequest struct {
	BaseRequest
	Message        string   `json:"message" form:"message"`
	ReplyMessageID *string  `json:"reply_message_id" form:"reply_message_id"`
	Mentions       []string `json:"mentions,omitempty" form:"mentions"` // List of phone numbers/JIDs to mention (ghost mentions)
	// Queue routes this send through the per-device outbound queue instead of
	// sending now. Opt-in: absent or false keeps the immediate-send behavior
	// unchanged. Not on BaseRequest on purpose, so it is only advertised on the
	// endpoints that actually honor it.
	Queue bool `json:"queue,omitempty" form:"queue"`
}
