package middleware

import (
	"net/url"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// DeviceOwnerGuard menegakkan kepemilikan atas device yang sudah diresolve
// DeviceMiddleware.
//
// Dipisah sebagai middleware sendiri, BUKAN dengan mengubah DeviceMiddleware,
// supaya file upstream itu tidak tersentuh sama sekali dan sync upstream tetap
// bebas konflik. Dipasang berpasangan:
//
//	apiGroup.Group("",
//	    middleware.DeviceMiddleware(dm),
//	    middleware.DeviceOwnerGuard(dm, ownership),
//	)
//
// Lihat docs/multitenant/phase-04-device-ownership.md.

// deviceNotFound menjawab persis seperti DeviceMiddleware saat device tidak
// ada.
//
// Cross-tenant WAJIB tidak bisa dibedakan dari device yang benar-benar tidak
// ada: 403 — atau pesan yang berbeda — akan mengonfirmasi bahwa device itu
// eksis, dan itu sendiri sebuah kebocoran. String di bawah disalin apa adanya
// dari device.go; kalau upstream mengubahnya, samakan lagi.
func deviceNotFound(c fiber.Ctx, deviceID string) error {
	return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
		Status:  fiber.StatusNotFound,
		Code:    "DEVICE_NOT_FOUND",
		Message: "device not found; create a device first from /api/devices or provide a valid X-Device-Id",
		Results: map[string]string{"device_id": deviceID},
	})
}

func deviceIDRequired(c fiber.Ctx) error {
	return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
		Status:  fiber.StatusBadRequest,
		Code:    "DEVICE_ID_REQUIRED",
		Message: "device_id is required via X-Device-Id header or device_id query",
		Results: nil,
	})
}

// requestedDeviceID mengembalikan device id yang benar-benar disebut pemanggil,
// atau string kosong kalau ia tidak menyebut apa pun.
//
// Membaca ulang header dan query dengan cara yang sama seperti
// DeviceMiddleware, karena Locals sudah berisi hasil resolusi — termasuk hasil
// fallback ke device default, yang justru harus dibedakan di sini.
func requestedDeviceID(c fiber.Ctx) string {
	deviceID := strings.TrimSpace(c.Get(DeviceIDHeader))
	if decoded, err := url.QueryUnescape(deviceID); err == nil {
		deviceID = decoded
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(c.Query("device_id"))
	}
	return deviceID
}

// DeviceOwnerGuard membangun middleware penjaga kepemilikan device.
func DeviceOwnerGuard(dm *whatsapp.DeviceManager, ownership domainTenancy.IDeviceOwnership) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !config.MultiTenantEnabled || ownership == nil {
			return c.Next()
		}

		// Lolosi rute non-device yang juga dilewati DeviceMiddleware.
		path := strings.TrimSpace(c.Path())
		if path == "" || path == "/" || path == config.AppBasePath || path == config.AppBasePath+"/" {
			return c.Next()
		}

		resolvedID, _ := c.Locals("device_id").(string)
		if strings.TrimSpace(resolvedID) == "" {
			// DeviceMiddleware sudah menolak duluan; tidak ada yang dijaga.
			return c.Next()
		}

		principal := PrincipalFrom(c)

		// Pemanggil menyebut device secara eksplisit: tegakkan apa adanya.
		if requested := requestedDeviceID(c); requested != "" {
			if ownership.CanAccess(principal, resolvedID) {
				return c.Next()
			}
			return deviceNotFound(c, resolvedID)
		}

		// Pemanggil tidak menyebut device, sehingga DeviceMiddleware jatuh ke
		// DefaultDevice() — dan device default global bisa saja milik orang
		// lain. Ini lubang L2 di inventaris.
		if ownership.CanAccess(principal, resolvedID) {
			return c.Next()
		}

		ids, all := ownership.OwnedDeviceIDs(principal)
		if all {
			// Admin: biarkan perilaku default lama.
			return c.Next()
		}

		// Catatan keterjangkauan. DefaultDevice() hanya mengembalikan instance
		// kalau registry berisi TEPAT SATU device; dengan lebih dari satu ia
		// nil dan DeviceMiddleware sudah menjawab 400 sebelum guard ini
		// berjalan. Akibatnya, pada perilaku upstream saat ini cabang di bawah
		// hanya pernah tercapai untuk instalasi satu device — dan di situ
		// operator pemiliknya sudah lolos lewat CanAccess di atas.
		//
		// Cabang ini tetap dipertahankan sebagai pertahanan: ia menutup kasus
		// baris kepemilikan yang basi (menunjuk device yang sudah dipurge), dan
		// akan langsung menjadi jalur aktif kalau upstream mengubah
		// DefaultDevice() supaya memilih salah satu dari beberapa device.
		switch len(ids) {
		case 0:
			return deviceNotFound(c, resolvedID)
		case 1:
			// Arahkan ke device milik pemanggil sendiri, bukan device default
			// global yang ternyata milik orang lain.
			return retargetDevice(c, dm, ids[0])
		default:
			// Menebak salah satu akan mengirim pesan dari device yang tidak
			// diminta, jadi minta eksplisit.
			return deviceIDRequired(c)
		}
	}
}

// retargetDevice mengarahkan ulang request ke device milik pemanggil sendiri.
//
// KETIGA nilai yang ditulis DeviceMiddleware harus ditimpa. Kalau SetContext
// terlewat, handler yang membaca Locals akan memakai device yang benar
// sementara usecase yang mengambil device dari context tetap memakai device
// orang lain — kebocoran yang tidak terlihat dari luar sama sekali.
func retargetDevice(c fiber.Ctx, dm *whatsapp.DeviceManager, deviceID string) error {
	if dm == nil {
		return deviceNotFound(c, deviceID)
	}

	instance, resolvedID, err := dm.ResolveDevice(deviceID)
	if err != nil || instance == nil {
		// Baris kepemilikan menunjuk device yang tidak ada di registry —
		// misalnya sisa dari device yang sudah dipurge.
		return deviceNotFound(c, deviceID)
	}

	c.Locals("device_id", resolvedID)
	c.Locals("device", instance)
	c.SetContext(whatsapp.ContextWithDevice(c.Context(), instance))
	return c.Next()
}
