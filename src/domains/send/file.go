package send

import "mime/multipart"

type FileRequest struct {
	BaseRequest
	File           *multipart.FileHeader `json:"file" form:"file"`
	FileURL        *string               `json:"file_url" form:"file_url"`
	Caption        string                `json:"caption" form:"caption"`
	ReplyMessageID *string               `json:"reply_message_id" form:"reply_message_id"`
	// Queue routes this send through the per-device outbound queue instead of
	// sending now. Opt-in: absent or false keeps the immediate-send behavior
	// unchanged. Not on BaseRequest on purpose, so it is only advertised on the
	// endpoints that actually honor it.
	Queue bool `json:"queue,omitempty" form:"queue"`
}
