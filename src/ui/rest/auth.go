package rest

import (
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Login berbasis cookie untuk mode multi-tenant.
//
// Rute dipecah jadi dua kelompok yang PENTING dibedakan tempat pendaftarannya:
//
//   - InitRestAuthPublic: /auth/login dan /auth/logout, harus terjangkau tanpa
//     kredensial, jadi didaftarkan sebelum AuthGate — pola yang sama dipakai
//     webhook Chatwoot dan rute discovery OAuth MCP.
//   - InitRestAuth: /auth/me, butuh principal, jadi didaftarkan di belakang gate.
//
// Lihat docs/multitenant/phase-03-auth-session.md.

// AuthHandler menyajikan rute /auth/*.
type AuthHandler struct {
	Service domainTenancy.ITenancyUsecase

	// limiter menahan brute-force pada /auth/login.
	limiter *loginRateLimiter
}

// NewAuthHandler membangun handler beserta pembatas percobaannya.
func NewAuthHandler(service domainTenancy.ITenancyUsecase) *AuthHandler {
	return &AuthHandler{Service: service, limiter: newLoginRateLimiter()}
}

// InitRestAuthPublic mendaftarkan rute yang harus terjangkau tanpa kredensial.
//
// Dipanggil pada *fiber.App sebelum AuthGate dipasang. Kalau APP_BASE_PATH
// diisi, path-nya ikut diprefiks di sini — sama seperti cara webhookPath
// disusun di cmd/rest.go.
func InitRestAuthPublic(app fiber.Router, handler *AuthHandler) *AuthHandler {
	prefix := config.AppBasePath

	app.Post(prefix+"/auth/login", handler.Login)
	// Logout ikut publik dan idempoten: pengguna yang cookie-nya sudah basi
	// harus tetap bisa membersihkannya tanpa lebih dulu berhasil autentikasi.
	app.Post(prefix+"/auth/logout", handler.Logout)

	return handler
}

// InitRestAuth mendaftarkan rute yang butuh principal.
func InitRestAuth(app fiber.Router, handler *AuthHandler) *AuthHandler {
	app.Get("/auth/me", handler.Me)
	return handler
}

// loginRequest menerima JSON maupun form, supaya halaman login bisa memakai
// keduanya.
type loginRequest struct {
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

// Login memverifikasi kredensial dan menerbitkan cookie session.
// POST /auth/login
func (h *AuthHandler) Login(c fiber.Ctx) error {
	if !config.MultiTenantEnabled || h.Service == nil {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
			Status:  fiber.StatusNotFound,
			Code:    "NOT_FOUND",
			Message: "login tidak tersedia",
		})
	}

	var req loginRequest
	if err := c.Bind().Body(&req); err != nil {
		return utils.ResponseError(c, "Invalid request body")
	}

	username := domainTenancy.NormalizeUsername(req.Username)
	ip := c.IP()

	if !h.limiter.allow(username, ip) {
		return c.Status(fiber.StatusTooManyRequests).JSON(utils.ResponseData{
			Status:  fiber.StatusTooManyRequests,
			Code:    "TOO_MANY_ATTEMPTS",
			Message: "terlalu banyak percobaan login; coba lagi beberapa menit lagi",
		})
	}

	token, principal, err := h.Service.Login(c.Context(), username, req.Password, c.Get(fiber.HeaderUserAgent))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(utils.ResponseData{
			Status:  fiber.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: "gagal memproses login",
		})
	}
	if principal == nil {
		h.limiter.recordFailure(username, ip)
		// Pesan generik yang sama untuk semua kegagalan: user tidak ada,
		// password salah, dan user nonaktif tidak boleh bisa dibedakan.
		return c.Status(fiber.StatusUnauthorized).JSON(utils.ResponseData{
			Status:  fiber.StatusUnauthorized,
			Code:    "INVALID_CREDENTIALS",
			Message: "username atau password salah",
		})
	}

	middleware.SetSessionCookie(c, token, int(config.MultiTenantSessionTTL.Seconds()))

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Login berhasil",
		Results: principalView(principal),
	})
}

// Logout mencabut session dan menghapus cookie-nya.
//
// Selalu menjawab sukses: logout yang gagal tidak memberi pengguna jalan keluar
// apa pun, dan token yang tidak dikenal memang sudah dalam keadaan yang
// diinginkan.
// POST /auth/logout
func (h *AuthHandler) Logout(c fiber.Ctx) error {
	if config.MultiTenantEnabled && h.Service != nil {
		if token := strings.TrimSpace(c.Cookies(config.MultiTenantSessionCookie)); token != "" {
			_ = h.Service.Logout(c.Context(), token)
		}
	}

	middleware.ExpireSessionCookie(c)

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Logout berhasil",
	})
}

// Me mengembalikan identitas pemanggil.
//
// Dipakai halaman /custom/* untuk menampilkan siapa yang login dan memutuskan
// apakah menu admin dirender. Di mode single-tenant rute ini tidak didaftarkan,
// jadi halaman harus menangani 404 dengan diam — lihat fase 08.
// GET /auth/me
func (h *AuthHandler) Me(c fiber.Ctx) error {
	principal := middleware.PrincipalFrom(c)
	if principal == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(utils.ResponseData{
			Status:  fiber.StatusUnauthorized,
			Code:    "UNAUTHORIZED",
			Message: "belum login",
		})
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Identitas pemanggil",
		Results: principalView(principal),
	})
}

// principalView membentuk representasi principal untuk response.
//
// Dibangun eksplisit dan tidak menyerahkan struct-nya langsung ke JSON, dengan
// alasan yang sama seperti userView di admin_users.go.
func principalView(principal *domainTenancy.Principal) map[string]any {
	if principal == nil {
		return nil
	}
	return map[string]any{
		"user_id":  principal.UserID,
		"username": principal.Username,
		"role":     string(principal.Role),
		"is_admin": principal.IsAdmin(),
		// Berguna bagi operator untuk tahu bahwa ia sedang memakai kredensial
		// darurat dari APP_BASIC_AUTH dan belum punya akun di database —
		// principal seperti ini tidak memiliki device apa pun.
		"via_break_glass": principal.ViaBreakGlass,
	}
}
