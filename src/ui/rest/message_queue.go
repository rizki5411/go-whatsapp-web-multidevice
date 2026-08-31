package rest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Management surface for the per-device outbound send queue. Mirrors
// command_config.go: path-param device resolution and the same response
// envelope.

// MessageQueueHandler serves the queue inspection and cancellation routes.
type MessageQueueHandler struct {
	DeviceManager *whatsapp.DeviceManager
	QueueRepo     domainMessageQueue.IMessageQueueRepository
}

// InitRestMessageQueue registers the queue routes. They take the device as a
// path param and resolve it manually, so they must be registered outside
// DeviceMiddleware (which only reads header/query).
func InitRestMessageQueue(app fiber.Router, dm *whatsapp.DeviceManager, queueRepo domainMessageQueue.IMessageQueueRepository) *MessageQueueHandler {
	h := &MessageQueueHandler{DeviceManager: dm, QueueRepo: queueRepo}

	app.Get("/devices/:device_id/queue", h.ListQueue)
	app.Delete("/devices/:device_id/queue/:queue_id", h.CancelQueued)

	return h
}

func queuedMessageView(msg *domainMessageQueue.QueuedMessage) map[string]any {
	view := map[string]any{
		"id":           msg.ID,
		"device_id":    msg.DeviceID,
		"message_type": msg.MessageType,
		"phone":        msg.Phone,
		"status":       msg.Status,
		"scheduled_at": msg.ScheduledAt,
		"created_at":   msg.CreatedAt,
		"updated_at":   msg.UpdatedAt,
	}
	// Only surface the outcome fields once they carry something, so a pending
	// row reads cleanly.
	if msg.SentAt != nil {
		view["sent_at"] = msg.SentAt
	}
	if msg.MessageID != "" {
		view["message_id"] = msg.MessageID
	}
	if msg.LastError != "" {
		view["last_error"] = msg.LastError
	}
	if msg.MediaPath != "" {
		view["has_media"] = true
	}
	return view
}

// ListQueue returns one device's queue, newest status counts included.
// GET /devices/:device_id/queue?status=&limit=&offset=
func (h *MessageQueueHandler) ListQueue(c fiber.Ctx) error {
	if h.QueueRepo == nil {
		return utils.ResponseError(c, "message queue not available")
	}
	deviceID, ok := h.resolveQueueDeviceID(c)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "DEVICE_NOT_FOUND", Message: "device not found"})
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !isKnownQueueStatus(status) {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  fiber.StatusBadRequest,
			Code:    "INVALID_STATUS",
			Message: "status must be one of pending, sending, sent, failed, cancelled",
		})
	}

	limit, err := strconv.Atoi(strings.TrimSpace(c.Query("limit", "100")))
	if err != nil || limit < 0 {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{Status: fiber.StatusBadRequest, Code: "INVALID_LIMIT", Message: "limit must be a non-negative integer"})
	}
	offset, err := strconv.Atoi(strings.TrimSpace(c.Query("offset", "0")))
	if err != nil || offset < 0 {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{Status: fiber.StatusBadRequest, Code: "INVALID_OFFSET", Message: "offset must be a non-negative integer"})
	}

	rows, err := h.QueueRepo.ListByDevice(domainMessageQueue.ListFilter{
		DeviceID: deviceID,
		Status:   status,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to load queue: %v", err))
	}

	counts, err := h.QueueRepo.CountByDeviceStatus(deviceID)
	if err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to count queue: %v", err))
	}

	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		views = append(views, queuedMessageView(row))
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device message queue",
		Results: map[string]any{
			"device_id": deviceID,
			"counts":    counts,
			"messages":  views,
		},
	})
}

// CancelQueued cancels a row that has not been picked up yet.
// DELETE /devices/:device_id/queue/:queue_id
func (h *MessageQueueHandler) CancelQueued(c fiber.Ctx) error {
	if h.QueueRepo == nil {
		return utils.ResponseError(c, "message queue not available")
	}
	deviceID, ok := h.resolveQueueDeviceID(c)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "DEVICE_NOT_FOUND", Message: "device not found"})
	}

	queueID, err := strconv.ParseInt(strings.TrimSpace(c.Params("queue_id")), 10, 64)
	if err != nil || queueID <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{Status: fiber.StatusBadRequest, Code: "INVALID_QUEUE_ID", Message: "queue_id must be a positive integer"})
	}

	// Scoped to this device inside the query: ids are global to the table, so an
	// unscoped cancel would let a request naming one device mutate another's queue.
	msg, err := h.QueueRepo.CancelPending(deviceID, queueID)
	if err != nil && !errors.Is(err, domainMessageQueue.ErrQueueRowNotPending) {
		return utils.ResponseError(c, fmt.Sprintf("failed to cancel queued message: %v", err))
	}
	if msg == nil {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "QUEUE_ROW_NOT_FOUND", Message: "no queued message with that id for this device"})
	}
	if errors.Is(err, domainMessageQueue.ErrQueueRowNotPending) {
		return c.Status(fiber.StatusConflict).JSON(utils.ResponseData{
			Status:  fiber.StatusConflict,
			Code:    "QUEUE_ROW_NOT_PENDING",
			Message: fmt.Sprintf("queued message is already %s", msg.Status),
			Results: queuedMessageView(msg),
		})
	}

	// The row will never be sent now, so its durable upload is dead weight.
	whatsapp.RemoveQueuedMedia(msg.MediaPath)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Queued message cancelled",
		Results: queuedMessageView(msg),
	})
}

// resolveQueueDeviceID resolves the :device_id path param to a known device id.
// DeviceMiddleware only reads the header/query, so these routes resolve manually;
// the result is cloned because fasthttp recycles the param buffer.
func (h *MessageQueueHandler) resolveQueueDeviceID(c fiber.Ctx) (string, bool) {
	deviceID := strings.TrimSpace(c.Params("device_id"))
	if deviceID == "" || h.DeviceManager == nil {
		return "", false
	}
	_, resolvedID, err := h.DeviceManager.ResolveDevice(deviceID)
	if err != nil {
		return "", false
	}
	return strings.Clone(resolvedID), true
}

func isKnownQueueStatus(status string) bool {
	switch status {
	case domainMessageQueue.StatusPending,
		domainMessageQueue.StatusSending,
		domainMessageQueue.StatusSent,
		domainMessageQueue.StatusFailed,
		domainMessageQueue.StatusCancelled:
		return true
	default:
		return false
	}
}
