package send

import "mime/multipart"

type StickerRequest struct {
	BaseRequest
	Sticker    *multipart.FileHeader `json:"sticker" form:"sticker"`
	StickerURL *string               `json:"sticker_url" form:"sticker_url"`
	// Queue routes this send through the per-device outbound queue instead of
	// sending now. Opt-in: absent or false keeps the immediate-send behavior
	// unchanged. Not on BaseRequest on purpose, so it is only advertised on the
	// endpoints that actually honor it.
	Queue bool `json:"queue,omitempty" form:"queue"`
}
