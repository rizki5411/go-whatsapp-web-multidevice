package rest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// fakeAuthUsecase melengkapi fakeTenancyUsecase dengan jalur login/session.
type fakeAuthUsecase struct {
	*fakeTenancyUsecase

	// validLogin memetakan "username:password" ke principal yang dihasilkan.
	validLogin map[string]*domainTenancy.Principal
	// issuedTokens mencatat token yang sudah diterbitkan.
	issuedTokens []string
	loggedOut    []string
	loginCalls   int
}

func newFakeAuthUsecase() *fakeAuthUsecase {
	return &fakeAuthUsecase{
		fakeTenancyUsecase: newFakeTenancyUsecase(),
		validLogin:         map[string]*domainTenancy.Principal{},
	}
}

func (f *fakeAuthUsecase) Login(_ context.Context, username, password, _ string) (string, *domainTenancy.Principal, error) {
	f.loginCalls++
	principal, ok := f.validLogin[username+":"+password]
	if !ok {
		return "", nil, nil
	}
	token := fmt.Sprintf("token-%d", len(f.issuedTokens)+1)
	f.issuedTokens = append(f.issuedTokens, token)
	return token, principal, nil
}

func (f *fakeAuthUsecase) Logout(_ context.Context, token string) error {
	f.loggedOut = append(f.loggedOut, token)
	return nil
}

var _ domainTenancy.ITenancyUsecase = (*fakeAuthUsecase)(nil)

// enableMultiTenantForTest menyalakan flag dan memulihkannya setelah kasus uji.
func enableMultiTenantForTest(t *testing.T, enabled bool) {
	t.Helper()
	previous := config.MultiTenantEnabled
	previousCookie := config.MultiTenantSessionCookie
	config.MultiTenantEnabled = enabled
	if config.MultiTenantSessionCookie == "" {
		config.MultiTenantSessionCookie = "gowa_session"
	}
	t.Cleanup(func() {
		config.MultiTenantEnabled = previous
		config.MultiTenantSessionCookie = previousCookie
	})
}

// newAuthTestApp mendaftarkan rute publik dan rute yang butuh principal, dengan
// principal disuntik langsung supaya handler bisa diuji tanpa gate.
func newAuthTestApp(t *testing.T, principal *domainTenancy.Principal) (*fiber.App, *fakeAuthUsecase) {
	t.Helper()

	svc := newFakeAuthUsecase()
	handler := NewAuthHandler(svc)

	app := fiber.New()
	InitRestAuthPublic(app, handler)
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			middleware.StorePrincipal(c, principal)
		}
		return c.Next()
	})
	InitRestAuth(app, handler)
	return app, svc
}

func TestAuthLoginSetsSessionCookie(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{
		UserID: 7, Username: "operator1", Role: domainTenancy.RoleOperator,
	}

	res, body := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"operator1","password":"rahasia123"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}

	var found *http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == config.MultiTenantSessionCookie {
			found = cookie
		}
	}
	if found == nil {
		t.Fatalf("cookie session tidak diterbitkan; cookies = %v", res.Cookies())
	}
	// HttpOnly wajib: token yang bisa dibaca JavaScript memperluas dampak XSS
	// dari "mencuri tampilan" menjadi "mencuri session".
	if !found.HttpOnly {
		t.Fatal("cookie session harus HttpOnly")
	}
	if found.Value == "" {
		t.Fatal("cookie session tidak boleh kosong")
	}
	if !strings.Contains(body, "operator1") || !strings.Contains(body, "is_admin") {
		t.Fatalf("body tidak memuat identitas: %s", body)
	}
}

// TestAuthLoginResponseNeverLeaksToken: token hanya boleh dikirim lewat cookie
// HttpOnly, tidak lewat body — body bisa dibaca JavaScript dan tercatat di log.
func TestAuthLoginResponseNeverLeaksToken(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}

	_, body := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"operator1","password":"rahasia123"}`)
	for _, token := range svc.issuedTokens {
		if strings.Contains(body, token) {
			t.Fatalf("token bocor di body response: %s", body)
		}
	}
	if strings.Contains(body, "token") {
		t.Fatalf("body tidak boleh menyebut token: %s", body)
	}
}

// TestAuthLoginFailureIsGeneric: user tidak ada, password salah, dan user
// nonaktif harus menghasilkan jawaban yang sama persis.
func TestAuthLoginFailureIsGeneric(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}

	_, wrongPassword := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"operator1","password":"salah"}`)
	_, unknownUser := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"hantu","password":"rahasia123"}`)

	if wrongPassword != unknownUser {
		t.Fatalf("jawaban harus identik:\n  password salah: %s\n  user tidak ada: %s", wrongPassword, unknownUser)
	}
	if !strings.Contains(wrongPassword, "INVALID_CREDENTIALS") {
		t.Fatalf("body = %s", wrongPassword)
	}
}

// TestAuthLoginRateLimited: tanpa pembatas ini, /auth/login menjadi oracle
// brute-force yang lebih nyaman daripada HTTP Basic.
func TestAuthLoginRateLimited(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}

	body := `{"username":"operator1","password":"salah"}`
	for i := 1; i <= loginMaxPerUsername; i++ {
		res, respBody := doJSON(t, app, http.MethodPost, "/auth/login", body)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("percobaan %d: status = %d, want 401 (body %s)", i, res.StatusCode, respBody)
		}
	}

	res, respBody := doJSON(t, app, http.MethodPost, "/auth/login", body)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("percobaan ke-%d: status = %d, want 429 (body %s)", loginMaxPerUsername+1, res.StatusCode, respBody)
	}
	if !strings.Contains(respBody, "TOO_MANY_ATTEMPTS") {
		t.Fatalf("body = %s", respBody)
	}

	// Setelah terkena batas, permintaan tidak boleh diteruskan ke usecase —
	// itulah gunanya membatasi.
	callsBefore := svc.loginCalls
	if _, _ = doJSON(t, app, http.MethodPost, "/auth/login", body); svc.loginCalls != callsBefore {
		t.Fatal("permintaan yang dibatasi tidak boleh memanggil usecase")
	}
}

// TestAuthLoginRateLimitDoesNotBlockSuccess: pemakaian normal — beberapa orang
// login dari satu kantor ber-IP sama — tidak boleh pernah terkena batas.
func TestAuthLoginRateLimitDoesNotBlockSuccess(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}

	for i := 0; i < loginMaxPerUsername+5; i++ {
		res, body := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"operator1","password":"rahasia123"}`)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("login sukses ke-%d ditolak: status = %d body = %s", i+1, res.StatusCode, body)
		}
	}
}

func TestAuthLogoutExpiresCookieAndRevokes(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: config.MultiTenantSessionCookie, Value: "token-a"})
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(svc.loggedOut) != 1 || svc.loggedOut[0] != "token-a" {
		t.Fatalf("session tidak dicabut: %v", svc.loggedOut)
	}

	var cleared bool
	for _, cookie := range res.Cookies() {
		if cookie.Name == config.MultiTenantSessionCookie && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("cookie session harus dihapus; cookies = %v", res.Cookies())
	}
}

// TestAuthLogoutWithoutCookieSucceeds: pengguna yang cookie-nya sudah basi
// harus tetap bisa membersihkannya.
func TestAuthLogoutWithoutCookieSucceeds(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, svc := newAuthTestApp(t, nil)
	res, body := doJSON(t, app, http.MethodPost, "/auth/logout", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if len(svc.loggedOut) != 0 {
		t.Fatal("tanpa cookie tidak ada yang perlu dicabut")
	}
}

func TestAuthMeReturnsPrincipal(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, _ := newAuthTestApp(t, &domainTenancy.Principal{
		UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin,
	})

	res, body := doJSON(t, app, http.MethodGet, "/auth/me", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `"is_admin":true`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(body, "admin") {
		t.Fatalf("body = %s", body)
	}
}

func TestAuthMeRequiresPrincipal(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app, _ := newAuthTestApp(t, nil)
	res, body := doJSON(t, app, http.MethodGet, "/auth/me", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", res.StatusCode, body)
	}
}

// TestAuthLoginUnavailableWhenDisabled: di mode single-tenant login berbasis
// cookie tidak punya arti dan tidak boleh menerima kredensial.
func TestAuthLoginUnavailableWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	app, svc := newAuthTestApp(t, nil)
	svc.validLogin["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}

	res, body := doJSON(t, app, http.MethodPost, "/auth/login", `{"username":"operator1","password":"rahasia123"}`)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", res.StatusCode, body)
	}
	if svc.loginCalls != 0 {
		t.Fatal("usecase tidak boleh dipanggil saat fitur mati")
	}
}
