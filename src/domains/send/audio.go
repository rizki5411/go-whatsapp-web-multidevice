package send

import "mime/multipart"

type AudioRequest struct {
	BaseRequest
	Audio          *multipart.FileHeader `json:"audio" form:"audio"`
	AudioURL       *string               `json:"audio_url" form:"audio_url"`
	ReplyMessageID *string               `json:"reply_message_id" form:"reply_message_id"`
	PTT            bool                  `json:"ptt" form:"ptt"`
	// Queue routes this send through the per-device outbound queue instead of
	// sending now. Opt-in: absent or false keeps the immediate-send behavior
	// unchanged. Not on BaseRequest on purpose, so it is only advertised on the
	// endpoints that actually honor it.
	Queue bool `json:"queue,omitempty" form:"queue"`
}
