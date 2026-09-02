package middleware

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Penyimpanan dan pembacaan identitas pemanggil untuk mode multi-tenant.
//
// Lihat docs/multitenant/ untuk rancangan lengkapnya.

// principalKey adalah tipe privat, bukan string.
//
// Dengan begitu tidak ada paket lain yang bisa menimpa principal hanya dengan
// menebak nama key-nya — dan menimpa principal berarti mengambil alih
// identitas.
type principalKey struct{}

// StorePrincipal menaruh identitas pemanggil di context request.
func StorePrincipal(c fiber.Ctx, principal *domainTenancy.Principal) {
	c.Locals(principalKey{}, principal)
}

// PrincipalFrom mengembalikan identitas pemanggil, atau nil kalau tidak ada.
//
// Pemanggil WAJIB menangani nil: rute publik (webhook Chatwoot, halaman login,
// discovery OAuth) memang tidak punya principal, dan begitu juga seluruh
// request saat mode multi-tenant mati.
func PrincipalFrom(c fiber.Ctx) *domainTenancy.Principal {
	if principal, ok := c.Locals(principalKey{}).(*domainTenancy.Principal); ok {
		return principal
	}
	return nil
}

// RequireAdmin menolak pemanggil yang bukan admin.
//
// Menjawab 404, bukan 403: 403 mengonfirmasi bahwa permukaan admin itu ada di
// alamat tersebut. Operator yang menebak URL cukup melihat "tidak ditemukan".
//
// Saat mode multi-tenant mati, middleware ini meneruskan request apa adanya —
// rute admin memang tidak didaftarkan di mode itu, tapi middleware-nya sendiri
// harus tetap aman kalau nanti dipakai di tempat lain.
func RequireAdmin() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !config.MultiTenantEnabled {
			return c.Next()
		}
		if PrincipalFrom(c).IsAdmin() {
			return c.Next()
		}
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
			Status:  fiber.StatusNotFound,
			Code:    "NOT_FOUND",
			Message: "resource tidak ditemukan",
		})
	}
}
