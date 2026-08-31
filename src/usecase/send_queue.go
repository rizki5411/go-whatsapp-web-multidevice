package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	fiberUtils "github.com/gofiber/utils/v2"
	"github.com/valyala/fasthttp"
)

// Opt-in send queue: the enqueue half (called from the Send* methods when a
// request carries `queue: true`) and the dispatch half (called by the background
// worker to replay a row through the very same Send* methods).
//
// The enqueue hook sits after validation but before the client lookup, so a
// malformed request still fails fast at request time while a queued one does not
// require the device to be online right now.

// enqueueSend persists one send request for later delivery.
//
// request must already have its *multipart.FileHeader field nulled out; upload is
// the original file header (nil for text and for the *_url variants) and is
// copied to a durable path under config.PathMessageQueue.
func (service serviceSend) enqueueSend(
	ctx context.Context,
	messageType string,
	phone string,
	request any,
	upload *multipart.FileHeader,
) (response domainSend.GenericResponse, err error) {
	if service.messageQueueRepo == nil {
		return response, pkgError.InternalServerError("message queue is not available")
	}

	instance, ok := whatsapp.DeviceFromContext(ctx)
	if !ok || instance == nil || strings.TrimSpace(instance.ID()) == "" {
		return response, pkgError.ValidationError("queue requires a device; pass X-Device-Id")
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return response, pkgError.InternalServerError(fmt.Sprintf("failed to encode queued request: %v", err))
	}

	mediaPath := ""
	mediaMime := ""
	if upload != nil {
		mediaPath, err = persistQueuedMedia(upload)
		if err != nil {
			return response, err
		}
		// Kept because the image/sticker/video/audio validators check it, and the
		// rebuilt part at dispatch time would otherwise default to octet-stream.
		mediaMime = upload.Header.Get("Content-Type")
	}

	queued := &domainMessageQueue.QueuedMessage{
		// Slot id, not the JID: it is what X-Device-Id resolves to and it
		// survives a logout/re-login.
		DeviceID:    instance.ID(),
		DeviceJID:   instance.JID(),
		MessageType: messageType,
		Phone:       phone,
		Payload:     string(payload),
		MediaPath:   mediaPath,
		MediaMime:   mediaMime,
		Status:      domainMessageQueue.StatusPending,
	}

	if err := service.messageQueueRepo.Enqueue(queued); err != nil {
		// Do not leave the durable copy behind if the row never landed.
		whatsapp.RemoveQueuedMedia(mediaPath)
		return response, pkgError.InternalServerError(fmt.Sprintf("failed to queue message: %v", err))
	}

	// Status is what the REST handler surfaces as the response message, so a
	// queued send is visibly different from a delivered one.
	response.Status = domainSend.StatusQueued
	response.QueueID = queued.ID
	return response, nil
}

// persistQueuedMedia copies an upload somewhere it will survive until the row is
// sent, including across a restart. config.PathSendItems is unsuitable: the send
// path deletes those files as soon as a send finishes.
func persistQueuedMedia(upload *multipart.FileHeader) (string, error) {
	if err := os.MkdirAll(config.PathMessageQueue, 0o755); err != nil {
		return "", pkgError.InternalServerError(fmt.Sprintf("failed to prepare queue media directory: %v", err))
	}

	// Keep the original extension: the audio path derives the container from it
	// and thumbnail generation needs a decodable name.
	name := fiberUtils.UUIDv4() + "-" + filepath.Base(upload.Filename)
	dst := filepath.Join(config.PathMessageQueue, name)
	if err := fasthttp.SaveMultipartFile(upload, dst); err != nil {
		return "", pkgError.InternalServerError(fmt.Sprintf("failed to store queued media: %v", err))
	}
	return dst, nil
}

// NewMessageQueueDispatcher returns the callback the background worker uses to
// deliver a queued row. It lives here because infrastructure/whatsapp cannot
// import usecase, and because replaying a row must go through the existing
// Send* methods rather than duplicate any send logic.
func NewMessageQueueDispatcher(service domainSend.ISendUsecase) whatsapp.MessageQueueDispatcher {
	return func(ctx context.Context, msg *domainMessageQueue.QueuedMessage) (string, error) {
		if service == nil {
			return "", fmt.Errorf("send service is not available")
		}
		if msg == nil {
			return "", fmt.Errorf("queued message is nil")
		}

		switch msg.MessageType {
		case domainMessageQueue.TypeText:
			var request domainSend.MessageRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued text request: %w", err)
			}
			request.Queue = false
			resp, err := service.SendText(ctx, request)
			return resp.MessageID, err

		case domainMessageQueue.TypeImage:
			var request domainSend.ImageRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued image request: %w", err)
			}
			request.Queue = false
			header, cleanup, err := rehydrateQueuedMedia(msg.MediaPath, "image", msg.MediaMime)
			if err != nil {
				return "", err
			}
			defer cleanup()
			request.Image = header
			resp, err := service.SendImage(ctx, request)
			return resp.MessageID, err

		case domainMessageQueue.TypeFile:
			var request domainSend.FileRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued file request: %w", err)
			}
			request.Queue = false
			header, cleanup, err := rehydrateQueuedMedia(msg.MediaPath, "file", msg.MediaMime)
			if err != nil {
				return "", err
			}
			defer cleanup()
			request.File = header
			resp, err := service.SendFile(ctx, request)
			return resp.MessageID, err

		case domainMessageQueue.TypeVideo:
			var request domainSend.VideoRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued video request: %w", err)
			}
			request.Queue = false
			header, cleanup, err := rehydrateQueuedMedia(msg.MediaPath, "video", msg.MediaMime)
			if err != nil {
				return "", err
			}
			defer cleanup()
			request.Video = header
			resp, err := service.SendVideo(ctx, request)
			return resp.MessageID, err

		case domainMessageQueue.TypeAudio:
			var request domainSend.AudioRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued audio request: %w", err)
			}
			request.Queue = false
			header, cleanup, err := rehydrateQueuedMedia(msg.MediaPath, "audio", msg.MediaMime)
			if err != nil {
				return "", err
			}
			defer cleanup()
			request.Audio = header
			resp, err := service.SendAudio(ctx, request)
			return resp.MessageID, err

		case domainMessageQueue.TypeSticker:
			var request domainSend.StickerRequest
			if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
				return "", fmt.Errorf("failed to decode queued sticker request: %w", err)
			}
			request.Queue = false
			header, cleanup, err := rehydrateQueuedMedia(msg.MediaPath, "sticker", msg.MediaMime)
			if err != nil {
				return "", err
			}
			defer cleanup()
			request.Sticker = header
			resp, err := service.SendSticker(ctx, request)
			return resp.MessageID, err

		default:
			return "", fmt.Errorf("unsupported queued message type %q", msg.MessageType)
		}
	}
}

// rehydrateQueuedMedia rebuilds the upload for a queued media row. An empty path
// means the request used a *_url variant instead, which round-trips inside the
// payload and is downloaded by the send path itself.
func rehydrateQueuedMedia(path, field, mimeType string) (*multipart.FileHeader, func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, func() {}, nil
	}
	return rehydrateMultipartFile(path, field, mimeType)
}
