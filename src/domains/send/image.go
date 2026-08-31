package send

import "mime/multipart"

type ImageRequest struct {
	BaseRequest
	Caption        string                `json:"caption" form:"caption"`
	ReplyMessageID *string               `json:"reply_message_id" form:"reply_message_id"`
	Image          *multipart.FileHeader `json:"image" form:"image"`
	ImageURL       *string               `json:"image_url" form:"image_url"`
	ViewOnce       bool                  `json:"view_once" form:"view_once"`
	Compress       bool                  `json:"compress"`
	HD             bool                  `json:"hd" form:"hd"`
	// Queue routes this send through the per-device outbound queue instead of
	// sending now. Opt-in: absent or false keeps the immediate-send behavior
	// unchanged. Not on BaseRequest on purpose, so it is only advertised on the
	// endpoints that actually honor it.
	Queue bool `json:"queue,omitempty" form:"queue"`
}
