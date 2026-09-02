package rest

import (
	"errors"
	"fmt"
	"strings"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Penetapan pemilik device oleh admin.
//
// Halaman ini yang membuat rollout mungkin dilakukan: saat mode multi-tenant
// pertama dinyalakan, semua device lama berstatus tak-ber-owner dan hanya
// terlihat admin, sampai pemiliknya ditetapkan di sini.

// AdminDeviceOwnerHandler menyajikan rute /admin/devices/:device_id/owner.
type AdminDeviceOwnerHandler struct {
	DeviceManager *whatsapp.DeviceManager
	Ownership     domainTenancy.IDeviceOwnership
	Users         domainTenancy.ITenancyUsecase
}

// InitRestAdminDeviceOwner mendaftarkan rute kepemilikan device.
func InitRestAdminDeviceOwner(
	app fiber.Router,
	dm *whatsapp.DeviceManager,
	ownership domainTenancy.IDeviceOwnership,
	users domainTenancy.ITenancyUsecase,
) *AdminDeviceOwnerHandler {
	h := &AdminDeviceOwnerHandler{DeviceManager: dm, Ownership: ownership, Users: users}

	admin := app.Group("/admin", middleware.RequireAdmin())
	admin.Get("/devices/:device_id/owner", h.GetOwner)
	admin.Put("/devices/:device_id/owner", h.SetOwner)
	admin.Delete("/devices/:device_id/owner", h.ClearOwner)

	return h
}

// resolveDeviceParam memvalidasi :device_id terhadap registry device.
//
// Hasilnya di-clone: saat ResolveDevice mencocokkan dengan id yang persis, ia
// mengembalikan string turunan path param, dan buffer fasthttp di belakangnya
// didaur ulang setelah request. Nilai ini dipersistensi, jadi tanpa clone ia
// akan bermutasi pada request berikutnya. Pola yang sama dipakai
// resolveConfigDeviceID di command_config.go.
func (h *AdminDeviceOwnerHandler) resolveDeviceParam(c fiber.Ctx) (string, bool) {
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

func (h *AdminDeviceOwnerHandler) notFound(c fiber.Ctx) error {
	return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
		Status:  fiber.StatusNotFound,
		Code:    "DEVICE_NOT_FOUND",
		Message: "device not found",
	})
}

// GetOwner mengembalikan pemilik satu device.
// GET /admin/devices/:device_id/owner
func (h *AdminDeviceOwnerHandler) GetOwner(c fiber.Ctx) error {
	deviceID, ok := h.resolveDeviceParam(c)
	if !ok {
		return h.notFound(c)
	}

	owner, err := h.Ownership.Owner(deviceID)
	if err != nil {
		return respondTenancyError(c, err)
	}

	result := map[string]any{"device_id": deviceID, "owner": nil}
	if owner != nil {
		result["owner"] = map[string]any{"user_id": owner.UserID}
		// Nama pemilik jauh lebih berguna daripada angka id di halaman
		// operator; kegagalan membacanya tidak boleh menggagalkan permintaan.
		if user, err := h.Users.GetUser(c.Context(), owner.UserID); err == nil && user != nil {
			result["owner"] = map[string]any{
				"user_id":      user.ID,
				"username":     user.Username,
				"display_name": user.DisplayName,
				"active":       user.Active,
			}
		}
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Pemilik device",
		Results: result,
	})
}

// SetOwner memindahkan device ke seorang user.
// PUT /admin/devices/:device_id/owner
func (h *AdminDeviceOwnerHandler) SetOwner(c fiber.Ctx) error {
	deviceID, ok := h.resolveDeviceParam(c)
	if !ok {
		return h.notFound(c)
	}

	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return utils.ResponseError(c, "Invalid request body")
	}
	if req.UserID <= 0 {
		return utils.ResponseError(c, "user_id wajib diisi")
	}

	if err := h.Ownership.Assign(deviceID, req.UserID); err != nil {
		if errors.Is(err, domainTenancy.ErrDeviceLimitReached) {
			return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
				Status:  fiber.StatusBadRequest,
				Code:    "DEVICE_LIMIT_REACHED",
				Message: err.Error(),
			})
		}
		return respondTenancyError(c, err)
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: fmt.Sprintf("Device %s ditetapkan ke user %d", deviceID, req.UserID),
		Results: map[string]any{"device_id": deviceID, "user_id": req.UserID},
	})
}

// ClearOwner melepas kepemilikan device.
// DELETE /admin/devices/:device_id/owner
func (h *AdminDeviceOwnerHandler) ClearOwner(c fiber.Ctx) error {
	deviceID, ok := h.resolveDeviceParam(c)
	if !ok {
		return h.notFound(c)
	}

	if err := h.Ownership.Release(deviceID); err != nil {
		return respondTenancyError(c, err)
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Kepemilikan dilepas; device ini kini hanya terlihat oleh admin",
		Results: map[string]any{"device_id": deviceID},
	})
}
