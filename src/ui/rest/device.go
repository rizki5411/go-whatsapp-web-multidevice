package rest

import (
	"fmt"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/tenantfilter"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
)

type Device struct {
	Service device.IDeviceUsecase

	// Ownership nil di mode single-tenant; setiap pemakaian di bawah menjaga
	// nil itu, sehingga perilaku lama tidak berubah.
	Ownership domainTenancy.IDeviceOwnership
}

func InitRestDevice(app fiber.Router, service device.IDeviceUsecase) Device {
	return InitRestDeviceWithOwnership(app, service, nil)
}

// InitRestDeviceWithOwnership mendaftarkan rute device beserta penjaga
// kepemilikannya. Rute ini memakai path param sehingga berada di luar
// DeviceMiddleware/DeviceOwnerGuard dan harus menjaga dirinya sendiri.
func InitRestDeviceWithOwnership(app fiber.Router, service device.IDeviceUsecase, ownership domainTenancy.IDeviceOwnership) Device {
	rest := Device{Service: service, Ownership: ownership}

	app.Get("/devices", rest.ListDevices)
	app.Post("/devices", rest.AddDevice)

	app.Get("/devices/:device_id", rest.GetDevice)
	app.Delete("/devices/:device_id", rest.RemoveDevice)

	app.Get("/devices/:device_id/login", rest.LoginDevice)
	app.Post("/devices/:device_id/login/code", rest.LoginDeviceWithCode)
	app.Post("/devices/:device_id/logout", rest.LogoutDevice)
	app.Post("/devices/:device_id/reconnect", rest.ReconnectDevice)
	app.Get("/devices/:device_id/status", rest.Status)
	app.Patch("/devices/:device_id/webhook", rest.UpdateDeviceWebhook)
	app.Get("/devices/:device_id/webhook", rest.GetDeviceWebhook)

	return rest
}

func (handler *Device) ListDevices(c fiber.Ctx) error {
	devices, err := handler.Service.ListDevices(c.Context())
	utils.PanicIfNeeded(err)

	// Inilah yang membuat dashboard gowa-ui otomatis benar: ia menampilkan apa
	// pun yang endpoint ini kembalikan, dan HTML-nya tidak bisa kita ubah.
	devices = tenantfilter.Devices(middleware.PrincipalFrom(c), handler.Ownership, devices,
		func(d device.Device) string { return d.ID })

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "List devices",
		Results: devices,
	})
}

func (handler *Device) GetDevice(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	device, err := handler.Service.GetDevice(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device info",
		Results: device,
	})
}

func (handler *Device) AddDevice(c fiber.Ctx) error {
	var req struct {
		// Id slot device pilihan pemanggil. KOSONG berarti "buatkan otomatis",
		// dan backend membuat UUID-nya. Bentuknya dibatasi
		// `validations.ValidateNewDeviceID` karena id ini masuk ke path URL dan
		// ke nilai header X-Device-Id, bukan cuma ke satu kolom database.
		DeviceID                  string `json:"device_id"`
		WebhookURL                string `json:"webhook_url"`
		WebhookSecret             string `json:"webhook_secret"`
		WebhookEvents             string `json:"webhook_events"`
		WebhookInsecureSkipVerify bool   `json:"webhook_insecure_skip_verify"`
	}

	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  400,
			Code:    "BAD_REQUEST",
			Message: "Invalid request body",
			Results: nil,
		})
	}

	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if err := validations.ValidateNewDeviceID(c.Context(), req.DeviceID); err != nil {
		// Diperiksa SEBELUM kuota dan sebelum device dibuat: penolakan bentuk id
		// tidak boleh sampai memakan jatah kuota atau meninggalkan slot separuh.
		utils.PanicIfNeeded(err)
	}

	var webhook *chatstorage.DeviceWebhookConfig
	if req.WebhookURL != "" || req.WebhookSecret != "" || req.WebhookEvents != "" || req.WebhookInsecureSkipVerify {
		webhook = &chatstorage.DeviceWebhookConfig{
			WebhookURL:                &req.WebhookURL,
			WebhookSecret:             req.WebhookSecret,
			WebhookEvents:             req.WebhookEvents,
			WebhookInsecureSkipVerify: req.WebhookInsecureSkipVerify,
		}
	}

	principal := middleware.PrincipalFrom(c)

	// Kuota diperiksa SEBELUM device dibuat, supaya penolakan tidak pernah
	// meninggalkan slot device yatim.
	if handler.Ownership != nil {
		if err := handler.Ownership.EnsureQuota(principal); err != nil {
			return c.Status(fiber.StatusForbidden).JSON(utils.ResponseData{
				Status:  fiber.StatusForbidden,
				Code:    "DEVICE_LIMIT_REACHED",
				Message: err.Error(),
			})
		}
	}

	device, err := handler.Service.AddDevice(c.Context(), req.DeviceID, webhook)
	utils.PanicIfNeeded(err)

	if handler.Ownership != nil {
		if err := handler.Ownership.Claim(principal, device.ID); err != nil {
			// Device sudah terbuat; melaporkan sukses akan menyembunyikan bahwa
			// ia tak-ber-owner dan karenanya hanya terlihat admin. Pola pesan
			// jujur yang sama dipakai AddDevice untuk kegagalan simpan webhook.
			return c.Status(fiber.StatusInternalServerError).JSON(utils.ResponseData{
				Status:  fiber.StatusInternalServerError,
				Code:    "DEVICE_CLAIM_FAILED",
				Message: fmt.Sprintf("device %s dibuat tetapi pemiliknya gagal dicatat (%v); device ini hanya terlihat oleh admin sampai pemiliknya ditetapkan lewat /admin/devices/%s/owner", device.ID, err, device.ID),
				Results: map[string]any{"device_id": device.ID},
			})
		}
	}

	result := map[string]any{
		"id":           device.ID,
		"display_name": device.DisplayName,
		"jid":          device.JID,
		"state":        device.State,
		"created_at":   device.CreatedAt,
	}
	if webhook != nil {
		result["webhook_url"] = req.WebhookURL
		result["webhook_secret"] = req.WebhookSecret
		result["webhook_events"] = req.WebhookEvents
		result["webhook_insecure_skip_verify"] = req.WebhookInsecureSkipVerify
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device added",
		Results: result,
	})
}

func (handler *Device) RemoveDevice(c fiber.Ctx) error {
	deviceID := c.Params("device_id")

	// Tanpa penjaga ini, operator mana pun bisa mem-purge device orang lain —
	// kebocoran paling merusak di seluruh permukaan REST, karena efeknya
	// menghapus sesi WhatsApp dan seluruh data chat device itu.
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}

	err := handler.Service.RemoveDevice(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	// Kepemilikan dilepas setelah purge berhasil. Baris owner yatim akan
	// membuat device baru yang kebetulan memakai id sama langsung dimiliki
	// pemilik lama.
	if handler.Ownership != nil {
		if err := handler.Ownership.Release(deviceID); err != nil {
			logrus.WithError(err).Warnf("[MULTITENANT] gagal melepas kepemilikan device %s setelah dihapus", deviceID)
		}
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device removed",
		Results: nil,
	})
}

func (handler *Device) LoginDevice(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	response, err := handler.Service.LoginDevice(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Login success",
		Results: map[string]any{
			"device_id":   deviceID,
			"qr_link":     fmt.Sprintf("%s://%s%s/%s", c.Scheme(), c.Host(), config.AppBasePath, response.ImagePath),
			"qr_duration": response.Duration,
		},
	})
}

func (handler *Device) LoginDeviceWithCode(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	code, err := handler.Service.LoginDeviceWithCode(c.Context(), deviceID, c.Query("phone"))
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Login with code started",
		Results: map[string]any{
			"device_id": deviceID,
			"pair_code": code,
		},
	})
}

func (handler *Device) LogoutDevice(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	err := handler.Service.LogoutDevice(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Logout requested",
		Results: nil,
	})
}

func (handler *Device) ReconnectDevice(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	err := handler.Service.ReconnectDevice(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Reconnect requested",
		Results: nil,
	})
}

func (handler *Device) Status(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	isConnected, isLoggedIn, err := handler.Service.GetStatus(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device status",
		Results: map[string]any{
			"device_id":    deviceID,
			"is_connected": isConnected,
			"is_logged_in": isLoggedIn,
		},
	})
}

// UpdateDeviceWebhook handles PATCH /devices/:device_id/webhook.
func (handler *Device) UpdateDeviceWebhook(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	var req struct {
		WebhookURL                *string `json:"webhook_url"`
		WebhookSecret             string  `json:"webhook_secret"`
		WebhookEvents             string  `json:"webhook_events"`
		WebhookInsecureSkipVerify bool    `json:"webhook_insecure_skip_verify"`
	}

	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  400,
			Code:    "BAD_REQUEST",
			Message: "Invalid request body",
			Results: nil,
		})
	}

	if req.WebhookURL == nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  400,
			Code:    "BAD_REQUEST",
			Message: "webhook_url is required",
			Results: nil,
		})
	}

	config := &chatstorage.DeviceWebhookConfig{
		WebhookURL:                req.WebhookURL,
		WebhookSecret:             req.WebhookSecret,
		WebhookEvents:             req.WebhookEvents,
		WebhookInsecureSkipVerify: req.WebhookInsecureSkipVerify,
	}

	err := handler.Service.SetDeviceWebhookConfig(c.Context(), deviceID, config)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device webhook updated",
		Results: map[string]any{
			"device_id":                    deviceID,
			"webhook_url":                  *req.WebhookURL,
			"webhook_secret":               req.WebhookSecret,
			"webhook_events":               req.WebhookEvents,
			"webhook_insecure_skip_verify": req.WebhookInsecureSkipVerify,
		},
	})
}

// GetDeviceWebhook handles GET /devices/:device_id/webhook.
func (handler *Device) GetDeviceWebhook(c fiber.Ctx) error {
	deviceID := c.Params("device_id")
	if !handler.canAccessDevice(c, deviceID) {
		return handler.deviceNotFound(c, deviceID)
	}
	config, err := handler.Service.GetDeviceWebhookConfig(c.Context(), deviceID)
	utils.PanicIfNeeded(err)

	webhookURL := ""
	if config != nil && config.WebhookURL != nil {
		webhookURL = *config.WebhookURL
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device webhook retrieved",
		Results: map[string]any{
			"device_id":   deviceID,
			"webhook_url": webhookURL,
			"webhook_secret": func() string {
				if config != nil {
					return config.WebhookSecret
				}
				return ""
			}(),
			"webhook_events": func() string {
				if config != nil {
					return config.WebhookEvents
				}
				return ""
			}(),
			"webhook_insecure_skip_verify": func() bool {
				if config != nil {
					return config.WebhookInsecureSkipVerify
				}
				return false
			}(),
		},
	})
}

// canAccessDevice menegakkan kepemilikan untuk rute device yang memakai path
// param, yaitu yang berada di luar DeviceMiddleware/DeviceOwnerGuard.
//
// Selalu true di mode single-tenant.
func (handler *Device) canAccessDevice(c fiber.Ctx, deviceID string) bool {
	if handler.Ownership == nil {
		return true
	}
	return handler.Ownership.CanAccess(middleware.PrincipalFrom(c), deviceID)
}

// deviceNotFound menjawab sama seperti device yang benar-benar tidak ada,
// supaya cross-tenant tidak bisa dibedakan darinya.
func (handler *Device) deviceNotFound(c fiber.Ctx, deviceID string) error {
	return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
		Status:  fiber.StatusNotFound,
		Code:    "DEVICE_NOT_FOUND",
		Message: "device not found; create a device first from /api/devices or provide a valid X-Device-Id",
		Results: map[string]string{"device_id": deviceID},
	})
}
