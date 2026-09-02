package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
)

// Isolasi tenant untuk permukaan MCP.
//
// MCP punya dua mode auth, dan identitasnya datang dari tempat yang berbeda:
//
//   - Basic: rute /mcp dipasang di belakang AuthGate, jadi principal sudah ada
//     di fiber Locals.
//   - OAuth: rute /mcp sengaja didaftarkan SEBELUM gate global supaya
//     discovery tetap publik, jadi AuthGate tidak berjalan. Identitasnya ada
//     di Locals "oauth_subject", yang sudah diverifikasi MCPAuthMiddleware.
//     Untuk kredensial Basic di mode OAuth, middleware itu memvalidasi tapi
//     tidak menyetel oauth_subject, jadi header-nya dibaca ulang di sini.
//
// Lihat docs/multitenant/phase-07-mcp-permukaan-lain.md.

// ownership dipasang oleh Register. nil berarti fitur tidak terpasang, dan
// seluruh penjagaan di file ini jadi transparan.
var ownership domainTenancy.IDeviceOwnership

// principalResolver adalah bagian ITenancyUsecase yang dibutuhkan MCP.
//
// Interface sempit di sisi konsumen supaya test tidak perlu
// mengimplementasikan CRUD user yang tidak dipakainya.
type PrincipalResolver interface {
	ResolveBasic(ctx context.Context, username, password string) (*domainTenancy.Principal, error)
	ResolvePrincipalByUsername(ctx context.Context, username string) (*domainTenancy.Principal, error)
}

// PrincipalBridge mengisi identitas pemanggil untuk permintaan MCP dan
// menyimpannya di context supaya lolos melewati adaptor net/http.
//
// Fiber Locals tidak menyeberangi adaptor, jadi principal-nya dipindah ke
// c.Context(); route.go memakai adaptor.HTTPHandlerWithContext supaya context
// itu terbawa sampai ke handler tool.
func PrincipalBridge(resolver PrincipalResolver) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !config.MultiTenantEnabled {
			return c.Next()
		}

		principal := middleware.PrincipalFrom(c)

		if principal == nil && resolver != nil {
			principal = resolvePrincipalForMCP(c, resolver)
		}

		if principal == nil {
			// Gagal ke arah aman: tanpa identitas, tool MCP tidak boleh jalan.
			return c.Status(fiber.StatusUnauthorized).JSON(utils.ResponseData{
				Status:  fiber.StatusUnauthorized,
				Code:    "UNAUTHORIZED",
				Message: "kredensial tidak valid",
			})
		}

		middleware.StorePrincipal(c, principal)
		c.SetContext(domainTenancy.ContextWithPrincipal(c.Context(), principal))
		return c.Next()
	}
}

// resolvePrincipalForMCP menyusun principal untuk jalur OAuth.
func resolvePrincipalForMCP(c fiber.Ctx, resolver PrincipalResolver) *domainTenancy.Principal {
	// Subject token OAuth: sudah diverifikasi MCPAuthMiddleware, jadi tinggal
	// diterjemahkan ke principal. Status akun TETAP diperiksa di
	// ResolvePrincipalByUsername — token bisa terbit sebelum user
	// dinonaktifkan, dan token yang masih valid secara kriptografis tidak boleh
	// mengalahkan status akun.
	if subject, ok := c.Locals("oauth_subject").(string); ok && strings.TrimSpace(subject) != "" {
		principal, err := resolver.ResolvePrincipalByUsername(c.Context(), subject)
		if err != nil {
			logrus.WithError(err).Warn("[MULTITENANT][MCP] gagal menerjemahkan oauth_subject")
			return nil
		}
		return principal
	}

	// Kredensial Basic di mode OAuth: middleware OAuth memvalidasinya tapi
	// tidak menyetel oauth_subject, jadi dibaca ulang di sini.
	if username, password, ok := middleware.BasicCredentials(c); ok {
		principal, err := resolver.ResolveBasic(c.Context(), username, password)
		if err != nil {
			logrus.WithError(err).Warn("[MULTITENANT][MCP] gagal memeriksa basic auth")
			return nil
		}
		return principal
	}

	return nil
}

// errDeviceNotFoundMCP adalah sentinel untuk device yang tidak boleh diakses.
var errDeviceNotFoundMCP = errors.New("device not found")

// deviceNotFoundErr meniru PERSIS pesan yang dihasilkan
// DeviceManager.ResolveDevice untuk device yang benar-benar tidak ada
// ("device %s not found").
//
// Kesamaan itu wajib: kalau device milik tenant lain menjawab dengan pesan yang
// berbeda — misalnya tanpa menyebut id-nya — maka bentuk pesan itu sendiri
// memberi tahu penyerang bahwa device tersebut EKSIS tapi bukan miliknya (K4).
type deviceNotFoundErr struct{ deviceID string }

func (e deviceNotFoundErr) Error() string {
	return fmt.Sprintf("device %s not found", e.deviceID)
}

// Is membuat errors.Is(err, errDeviceNotFoundMCP) tetap bekerja meski pesannya
// membawa device id.
func (e deviceNotFoundErr) Is(target error) bool { return target == errDeviceNotFoundMCP }

func deviceNotFound(deviceID string) error { return deviceNotFoundErr{deviceID: deviceID} }

// enforceDeviceOwnership menolak device yang bukan milik pemanggil.
//
// Selalu mengizinkan di mode single-tenant dan saat ownership belum terpasang.
func enforceDeviceOwnership(ctx context.Context, deviceID string) error {
	if !config.MultiTenantEnabled || ownership == nil {
		return nil
	}
	if ownership.CanAccess(domainTenancy.PrincipalFromContext(ctx), deviceID) {
		return nil
	}
	return deviceNotFound(deviceID)
}

// ownDefaultDeviceID memilih device default pemanggil saat tool tidak menyebut
// device apa pun.
//
// Mengembalikan string kosong beserta error kalau pilihannya tidak tunggal:
// menebak salah satu berarti mengirim pesan dari device yang tidak diminta.
func ownDefaultDeviceID(ctx context.Context) (string, error) {
	if !config.MultiTenantEnabled || ownership == nil {
		return "", nil
	}

	principal := domainTenancy.PrincipalFromContext(ctx)
	ids, all := ownership.OwnedDeviceIDs(principal)
	if all {
		// Admin: biarkan perilaku default lama.
		return "", nil
	}

	switch len(ids) {
	case 0:
		// Tidak ada device id untuk disebut, dan situasinya memang "tidak
		// menyebut device": pakai pesan yang sama dengan jalur tanpa device.
		return "", errors.New("device identification required: set the X-Device-Id header or pass device_id")
	case 1:
		return ids[0], nil
	default:
		return "", errors.New("device identification required: pass device_id (akun ini memiliki lebih dari satu device)")
	}
}
