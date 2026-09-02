package middleware

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
)

// AuthGate adalah pintu autentikasi untuk mode multi-tenant. Ia menggantikan
// basic auth global saat fitur aktif, dan meneruskan ke basic auth lama saat
// fitur mati.
//
// Dua jalur masuk, keduanya wajib jalan:
//
//   - Cookie session, untuk halaman /custom/* dan form login.
//   - HTTP Basic, untuk klien API, dashboard gowa-ui, dan MCP. Dashboard itu
//     HTML eksternal yang di-download runtime dan ditimpa auto-update, jadi
//     tidak bisa kita ubah — mematikan Basic berarti mematikan dashboard.
//
// Lihat docs/multitenant/phase-03-auth-session.md.

// TenancyAuthenticator adalah bagian dari tenancy.ITenancyUsecase yang
// dibutuhkan gate ini.
//
// Interface sempit didefinisikan di sisi konsumen supaya test middleware tidak
// perlu mengimplementasikan CRUD user yang tidak dipakainya.
type TenancyAuthenticator interface {
	ResolveSession(ctx context.Context, token string) (*domainTenancy.Principal, error)
	ResolveBasic(ctx context.Context, username, password string) (*domainTenancy.Principal, error)
}

// AuthGate membangun middleware autentikasi.
//
// fallback adalah handler basic auth yang sudah dipakai project ini; ia yang
// dijalankan saat mode multi-tenant mati, sehingga perilaku single-tenant tidak
// berubah sama sekali.
func AuthGate(resolver TenancyAuthenticator, fallback fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		// Jalur flag-off: pakai basic auth lama apa adanya. Ini jaminan
		// zero-regression sekaligus jalur rollback fitur ini.
		if !config.MultiTenantEnabled {
			if fallback == nil {
				return c.Next()
			}
			return fallback(c)
		}

		if resolver == nil {
			logrus.Error("[MULTITENANT] auth gate tanpa resolver; menolak request")
			return unauthorized(c)
		}

		// 1. Cookie session.
		if token := strings.TrimSpace(c.Cookies(config.MultiTenantSessionCookie)); token != "" {
			principal, err := resolver.ResolveSession(c.Context(), token)
			if err != nil {
				logrus.WithError(err).Warn("[MULTITENANT] gagal memeriksa session")
			}
			if principal != nil {
				StorePrincipal(c, principal)
				return c.Next()
			}
			// Cookie tidak berlaku: hapus, lalu JATUH ke Basic — jangan
			// langsung 401. Klien API yang kebetulan membawa cookie basi harus
			// tetap bisa masuk lewat header-nya.
			ExpireSessionCookie(c)
		}

		// 2. HTTP Basic. Header-nya mungkin baru diisi WebsocketQueryAuth dari
		// query ?authorization=, karena WebSocket di browser tidak bisa
		// mengirim header sendiri.
		if username, password, ok := parseBasicAuth(c.Get(fiber.HeaderAuthorization)); ok {
			principal, err := resolver.ResolveBasic(c.Context(), username, password)
			if err != nil {
				logrus.WithError(err).Warn("[MULTITENANT] gagal memeriksa basic auth")
			}
			if principal != nil {
				StorePrincipal(c, principal)
				return c.Next()
			}
		}

		return unauthorized(c)
	}
}

// unauthorized menjawab 401 dengan tantangan Basic.
//
// Header WWW-Authenticate wajib ada: dashboard gowa-ui dan klien API
// mengandalkan prompt itu, dan menghilangkannya akan membuat keduanya gagal
// tanpa penjelasan.
func unauthorized(c fiber.Ctx) error {
	c.Set(fiber.HeaderWWWAuthenticate, `Basic realm="gowa", charset="UTF-8"`)
	return c.Status(fiber.StatusUnauthorized).JSON(utils.ResponseData{
		Status:  fiber.StatusUnauthorized,
		Code:    "UNAUTHORIZED",
		Message: "kredensial tidak valid",
	})
}

// SetSessionCookie menerbitkan cookie session.
//
// HTTPOnly wajib. SameSite=Lax cukup: tidak ada aksi yang mengubah state
// dilakukan lewat navigasi lintas situs, dan Strict akan merusak alur redirect
// dari halaman login.
func SetSessionCookie(c fiber.Ctx, token string, maxAgeSeconds int) {
	c.Cookie(&fiber.Cookie{
		Name:     config.MultiTenantSessionCookie,
		Value:    token,
		Path:     SessionCookiePath(),
		HTTPOnly: true,
		Secure:   config.MultiTenantSecureCookie,
		SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge:   maxAgeSeconds,
	})
}

// ExpireSessionCookie menghapus cookie session di sisi klien.
func ExpireSessionCookie(c fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     config.MultiTenantSessionCookie,
		Value:    "",
		Path:     SessionCookiePath(),
		HTTPOnly: true,
		Secure:   config.MultiTenantSecureCookie,
		SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge:   -1,
	})
}

// SessionCookiePath menghormati APP_BASE_PATH.
//
// Kalau path-nya salah, deploy di subpath akan menerima cookie yang tidak
// pernah terkirim balik, dan login tampak "berhasil tapi tidak nyangkut".
func SessionCookiePath() string {
	if config.AppBasePath != "" {
		return config.AppBasePath
	}
	return "/"
}

// parseBasicAuth membaca header Authorization: Basic.
//
// Password boleh memuat ":" — karena itu pemisahannya memakai strings.Cut
// (belah pertama), bukan strings.Split.
func parseBasicAuth(header string) (username, password string, ok bool) {
	encoded, found := strings.CutPrefix(header, "Basic ")
	if !found {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", false
	}
	username, password, found = strings.Cut(string(decoded), ":")
	if !found {
		return "", "", false
	}
	return username, password, true
}

// BasicCredentials membaca kredensial HTTP Basic dari request.
//
// Diekspor untuk jalur yang tidak melewati AuthGate dan karenanya harus
// memeriksa header itu sendiri — MCP dalam mode OAuth, yang rutenya sengaja
// didaftarkan sebelum gate global supaya discovery tetap publik.
func BasicCredentials(c fiber.Ctx) (username, password string, ok bool) {
	return parseBasicAuth(c.Get(fiber.HeaderAuthorization))
}
