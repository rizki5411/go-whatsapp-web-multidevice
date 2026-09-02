package tenantfilter

import (
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Penjaga kepemilikan untuk rute yang me-resolve device dari path param.
//
// Rute-rute itu berada DI LUAR DeviceMiddleware/DeviceOwnerGuard — middleware
// itu hanya membaca header dan query — sehingga harus menjaga dirinya sendiri.
//
// Satu helper dipakai semua, bukan logika yang sama diulang enam kali: kalau
// aturannya berubah, hanya ada satu tempat yang perlu diubah.
//
// Lihat docs/multitenant/phase-05-enforcement-rute.md.

// DeviceNotFound menjawab persis seperti device yang benar-benar tidak ada.
//
// Cross-tenant WAJIB tidak bisa dibedakan darinya: pesan atau kode yang
// berbeda akan mengonfirmasi bahwa device itu eksis (K4).
func DeviceNotFound(c fiber.Ctx) error {
	return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
		Status:  fiber.StatusNotFound,
		Code:    "DEVICE_NOT_FOUND",
		Message: "device not found",
	})
}

// GuardParamDevice me-resolve :device_id lalu menegakkan kepemilikannya.
//
// Mengembalikan device id yang sudah diresolve dan di-clone, siap dipersistensi.
// Clone-nya wajib: saat ResolveDevice mencocokkan dengan id yang persis, ia
// mengembalikan string turunan path param, dan buffer fasthttp di belakangnya
// didaur ulang setelah request selesai.
//
// ok bernilai false berarti pemanggil harus berhenti; response-nya BELUM
// ditulis, supaya setiap pemanggil bisa memakai bentuk error yang sudah
// dipakainya (beberapa handler punya pesan sendiri).
func GuardParamDevice(
	c fiber.Ctx,
	dm *whatsapp.DeviceManager,
	ownership domainTenancy.IDeviceOwnership,
) (deviceID string, ok bool) {
	raw := strings.TrimSpace(c.Params("device_id"))
	if raw == "" || dm == nil {
		return "", false
	}

	_, resolvedID, err := dm.ResolveDevice(raw)
	if err != nil {
		return "", false
	}

	if !CanAccess(c, ownership, resolvedID) {
		return "", false
	}

	return strings.Clone(resolvedID), true
}

// CanAccess menegakkan kepemilikan atas satu device id yang sudah diresolve.
//
// Selalu true di mode single-tenant dan saat ownership belum terpasang.
func CanAccess(c fiber.Ctx, ownership domainTenancy.IDeviceOwnership, deviceID string) bool {
	if ownership == nil {
		return true
	}
	return ownership.CanAccess(middleware.PrincipalFrom(c), deviceID)
}

// CanActOnUnresolvedDevice melaporkan apakah pemanggil boleh bertindak atas
// device id yang TIDAK bisa diresolve.
//
// Beberapa handler DELETE sengaja jatuh ke path param mentah supaya config yang
// yatim — device-nya sudah dihapus — tetap bisa dibersihkan. Jalur itu
// melewatkan pemeriksaan kepemilikan, karena tanpa device yang bisa diresolve
// tidak ada kepemilikan yang bisa dibaca.
//
// Karena itu jalur tersebut dibatasi ke admin, konsisten dengan aturan "device
// tanpa pemilik hanya untuk admin". Tanpa pembatasan ini, operator bisa
// menghapus config device orang lain hanya dengan mengirim id yang tidak
// resolve.
func CanActOnUnresolvedDevice(c fiber.Ctx, ownership domainTenancy.IDeviceOwnership) bool {
	if ownership == nil || !config.MultiTenantEnabled {
		return true
	}
	return middleware.PrincipalFrom(c).IsAdmin()
}
