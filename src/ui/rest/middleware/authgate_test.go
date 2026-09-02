package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/gofiber/fiber/v3"
)

// fakeAuthenticator adalah TenancyAuthenticator yang dikendalikan test.
type fakeAuthenticator struct {
	sessions map[string]*domainTenancy.Principal
	basic    map[string]*domainTenancy.Principal

	sessionCalls int
	basicCalls   int
}

func newFakeAuthenticator() *fakeAuthenticator {
	return &fakeAuthenticator{
		sessions: map[string]*domainTenancy.Principal{},
		basic:    map[string]*domainTenancy.Principal{},
	}
}

func (f *fakeAuthenticator) ResolveSession(_ context.Context, token string) (*domainTenancy.Principal, error) {
	f.sessionCalls++
	return f.sessions[token], nil
}

func (f *fakeAuthenticator) ResolveBasic(_ context.Context, username, password string) (*domainTenancy.Principal, error) {
	f.basicCalls++
	return f.basic[username+":"+password], nil
}

var _ TenancyAuthenticator = (*fakeAuthenticator)(nil)

// withMultiTenant menyalakan flag untuk satu kasus uji dan memulihkannya
// sesudahnya. config adalah variabel global paket, jadi tanpa ini kasus uji
// akan saling bocor.
func withMultiTenant(t *testing.T, enabled bool) {
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

// newGateApp membangun app dengan gate dan satu handler yang melaporkan
// principal yang berhasil masuk.
func newGateApp(resolver TenancyAuthenticator, fallback fiber.Handler) *fiber.App {
	app := fiber.New()
	app.Use(AuthGate(resolver, fallback))
	app.Get("/probe", func(c fiber.Ctx) error {
		if principal := PrincipalFrom(c); principal != nil {
			return c.SendString("principal:" + principal.Username)
		}
		return c.SendString("no-principal")
	})
	return app
}

func basicHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestAuthGateAcceptsSessionCookie(t *testing.T) {
	withMultiTenant(t, true)

	auth := newFakeAuthenticator()
	auth.sessions["token-a"] = &domainTenancy.Principal{UserID: 7, Username: "operator1", Role: domainTenancy.RoleOperator}
	app := newGateApp(auth, nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: config.MultiTenantSessionCookie, Value: "token-a"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if auth.basicCalls != 0 {
		t.Fatal("cookie yang valid tidak perlu menyentuh jalur basic")
	}
}

func TestAuthGateAcceptsBasicAuth(t *testing.T) {
	withMultiTenant(t, true)

	auth := newFakeAuthenticator()
	auth.basic["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1", Role: domainTenancy.RoleOperator}
	app := newGateApp(auth, nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, basicHeader("operator1", "rahasia123"))

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// TestAuthGateFallsBackToBasicWhenCookieStale: klien API yang kebetulan membawa
// cookie basi harus tetap bisa masuk lewat header. Kalau cookie tidak valid
// langsung 401, dashboard yang cookie-nya kedaluwarsa akan macet total.
func TestAuthGateFallsBackToBasicWhenCookieStale(t *testing.T) {
	withMultiTenant(t, true)

	auth := newFakeAuthenticator()
	auth.basic["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1", Role: domainTenancy.RoleOperator}
	app := newGateApp(auth, nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: config.MultiTenantSessionCookie, Value: "cookie-basi"})
	req.Header.Set(fiber.HeaderAuthorization, basicHeader("operator1", "rahasia123"))

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if auth.sessionCalls != 1 || auth.basicCalls != 1 {
		t.Fatalf("kedua jalur harus dicoba: session=%d basic=%d", auth.sessionCalls, auth.basicCalls)
	}
}

// TestAuthGateChallengesWithBasic: header WWW-Authenticate wajib ada, karena
// dashboard gowa-ui dan klien API mengandalkan prompt itu.
func TestAuthGateChallengesWithBasic(t *testing.T) {
	withMultiTenant(t, true)

	app := newGateApp(newFakeAuthenticator(), nil)

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	if challenge := res.Header.Get(fiber.HeaderWWWAuthenticate); challenge == "" {
		t.Fatal("response 401 harus membawa tantangan WWW-Authenticate")
	}
}

func TestAuthGateRejectsWrongCredentials(t *testing.T) {
	withMultiTenant(t, true)

	auth := newFakeAuthenticator()
	auth.basic["operator1:rahasia123"] = &domainTenancy.Principal{UserID: 7, Username: "operator1"}
	app := newGateApp(auth, nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, basicHeader("operator1", "salah"))

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

// TestAuthGateUsesFallbackWhenDisabled adalah jaminan zero-regression: dengan
// flag mati, gate tidak boleh melakukan apa pun selain menjalankan basic auth
// lama.
func TestAuthGateUsesFallbackWhenDisabled(t *testing.T) {
	withMultiTenant(t, false)

	auth := newFakeAuthenticator()
	fallbackCalled := 0
	fallback := func(c fiber.Ctx) error {
		fallbackCalled++
		return c.Next()
	}
	app := newGateApp(auth, fallback)

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if fallbackCalled != 1 {
		t.Fatalf("fallback dipanggil %d kali, want 1", fallbackCalled)
	}
	if auth.sessionCalls != 0 || auth.basicCalls != 0 {
		t.Fatal("dengan flag mati, resolver tenancy tidak boleh disentuh")
	}
}

// TestAuthGateRejectsWhenResolverMissing: gagal ke arah aman. Konfigurasi yang
// salah harus menolak request, bukan membiarkannya lewat.
func TestAuthGateRejectsWhenResolverMissing(t *testing.T) {
	withMultiTenant(t, true)

	app := newGateApp(nil, nil)

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

// TestParseBasicAuthHandlesColonInPassword: password boleh memuat ":", jadi
// pemisahannya harus belah pertama (strings.Cut), bukan strings.Split.
func TestParseBasicAuthHandlesColonInPassword(t *testing.T) {
	header := basicHeader("operator1", "pass:with:colons")

	username, password, ok := parseBasicAuth(header)
	if !ok {
		t.Fatal("header valid harus terparse")
	}
	if username != "operator1" {
		t.Fatalf("username = %q", username)
	}
	if password != "pass:with:colons" {
		t.Fatalf("password = %q", password)
	}
}

func TestParseBasicAuthRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"Bearer sometoken",
		"Basic !!!bukan-base64!!!",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("tanpa-titik-dua")),
	}
	for _, header := range cases {
		if _, _, ok := parseBasicAuth(header); ok {
			t.Fatalf("header %q tidak boleh terparse", header)
		}
	}
}

// _____________________________________________________________________________
// RequireAdmin

func newRequireAdminApp(principal *domainTenancy.Principal) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			StorePrincipal(c, principal)
		}
		return c.Next()
	})
	app.Get("/admin/probe", RequireAdmin(), func(c fiber.Ctx) error {
		return c.SendString("ok")
	})
	return app
}

// TestRequireAdminHides404FromNonAdmin: menjawab 404 dan bukan 403, supaya
// operator yang menebak URL tidak mendapat konfirmasi bahwa permukaan admin
// ada di alamat itu.
func TestRequireAdminHides404FromNonAdmin(t *testing.T) {
	withMultiTenant(t, true)

	cases := map[string]*domainTenancy.Principal{
		"operator":        {UserID: 2, Username: "operator1", Role: domainTenancy.RoleOperator},
		"tanpa principal": nil,
		"role kosong":     {UserID: 3, Username: "aneh"},
	}

	for name, principal := range cases {
		t.Run(name, func(t *testing.T) {
			app := newRequireAdminApp(principal)
			res, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/probe", nil))
			if err != nil {
				t.Fatalf("test: %v", err)
			}
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", res.StatusCode)
			}
		})
	}
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	withMultiTenant(t, true)

	app := newRequireAdminApp(&domainTenancy.Principal{UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin})
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/probe", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// TestRequireAdminPassesThroughWhenDisabled: rute admin memang tidak
// didaftarkan di mode single-tenant, tapi middleware-nya sendiri harus tetap
// aman kalau nanti dipakai di tempat lain.
func TestRequireAdminPassesThroughWhenDisabled(t *testing.T) {
	withMultiTenant(t, false)

	app := newRequireAdminApp(nil)
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/admin/probe", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// TestSessionCookiePathHonoursBasePath: path cookie yang salah membuat deploy
// di subpath menerima cookie yang tidak pernah terkirim balik, sehingga login
// tampak berhasil tapi tidak nyangkut.
func TestSessionCookiePathHonoursBasePath(t *testing.T) {
	previous := config.AppBasePath
	t.Cleanup(func() { config.AppBasePath = previous })

	config.AppBasePath = ""
	if path := SessionCookiePath(); path != "/" {
		t.Fatalf("path = %q, want /", path)
	}

	config.AppBasePath = "/gowa"
	if path := SessionCookiePath(); path != "/gowa" {
		t.Fatalf("path = %q, want /gowa", path)
	}
}
