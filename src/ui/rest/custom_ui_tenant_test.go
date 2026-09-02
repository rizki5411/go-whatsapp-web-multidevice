package rest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Halaman operator untuk mode multi-tenant.
//
// Lihat docs/multitenant/phase-08-ui-operator.md.

// newTenantUIApp mendaftarkan halaman /custom dengan principal tertentu,
// meniru apa yang dilakukan AuthGate di aplikasi sungguhan.
func newTenantUIApp(t *testing.T, principal *domainTenancy.Principal) *fiber.App {
	t.Helper()

	app := fiber.New()
	// Halaman login didaftarkan di jalur publik, sebelum principal ada.
	InitRestCustomUIPublic(app)
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			middleware.StorePrincipal(c, principal)
		}
		return c.Next()
	})
	InitRestCustomUI(app)
	return app
}

// TestLoginPageIsPublic: halaman login yang meminta login lebih dulu tidak ada
// gunanya, jadi ia harus terjangkau tanpa principal sama sekali.
func TestLoginPageIsPublic(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app := newTenantUIApp(t, nil)
	res, body := getPage(t, app, "/custom/login")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "<title>Masuk</title>") {
		t.Fatalf("halaman login tidak dilayani: %.120s", body)
	}
	if res.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", res.Header.Get(fiber.HeaderCacheControl))
	}
}

// TestLoginPageIsPublicEvenWhenDisabled: halaman itu di-embed tanpa syarat, dan
// dilayani apa adanya; JavaScript-nya yang menangani server tanpa /auth/login.
func TestLoginPageIsPublicEvenWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	res, _ := getPage(t, newTenantUIApp(t, nil), "/custom/login")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// TestAdminPagesRequireAdmin: 404 dan bukan 403, supaya operator yang menebak
// URL tidak mendapat konfirmasi bahwa permukaan admin ada di alamat itu.
func TestAdminPagesRequireAdmin(t *testing.T) {
	enableMultiTenantForTest(t, true)

	paths := []string{"/custom/users", "/custom/devices"}

	operatorApp := newTenantUIApp(t, &domainTenancy.Principal{
		UserID: 2, Username: "operator1", Role: domainTenancy.RoleOperator,
	})
	for _, path := range paths {
		res, _ := getPage(t, operatorApp, path)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("operator ke %s: status = %d, want 404", path, res.StatusCode)
		}
	}

	anonApp := newTenantUIApp(t, nil)
	for _, path := range paths {
		res, _ := getPage(t, anonApp, path)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("tanpa principal ke %s: status = %d, want 404", path, res.StatusCode)
		}
	}

	adminApp := newTenantUIApp(t, &domainTenancy.Principal{
		UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin,
	})
	for _, path := range paths {
		res, body := getPage(t, adminApp, path)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("admin ke %s: status = %d, want 200", path, res.StatusCode)
		}
		if !strings.Contains(body, "<title>") {
			t.Fatalf("halaman %s kosong", path)
		}
	}
}

// TestAdminPagesNotRegisteredWhenDisabled: di mode single-tenant halaman ini
// tidak punya arti dan tidak boleh ada sama sekali.
func TestAdminPagesNotRegisteredWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	app := newTenantUIApp(t, &domainTenancy.Principal{
		UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin,
	})
	for _, path := range []string{"/custom/users", "/custom/devices"} {
		res, _ := getPage(t, app, path)
		if res.StatusCode == http.StatusOK {
			t.Fatalf("%s tidak boleh dilayani di mode single-tenant", path)
		}
	}
}

// TestTenantPagesAreSelfContained menegakkan kontrak /custom: server ini sering
// di-host tanpa akses internet keluar, dan halaman admin yang butuh CDN adalah
// halaman admin yang mati justru saat paling dibutuhkan.
func TestTenantPagesAreSelfContained(t *testing.T) {
	pages := map[string][]byte{
		"login_ui.html":         loginUIPage,
		"users_ui.html":         usersUIPage,
		"devices_owner_ui.html": devicesOwnerUIPage,
	}

	forbidden := []string{"http://", "https://", "//cdn", "fonts.googleapis", "<script src", "<link rel=\"stylesheet\""}

	for name, page := range pages {
		body := string(page)
		if len(body) == 0 {
			t.Fatalf("%s tidak ter-embed", name)
		}
		for _, needle := range forbidden {
			if strings.Contains(body, needle) {
				t.Fatalf("%s memuat referensi eksternal %q", name, needle)
			}
		}
	}
}

// TestTenantPagesDeriveAPIRootFromOwnPath: halaman harus tetap jalan saat
// APP_BASE_PATH diisi, dan caranya adalah memotong path-nya sendiri — bukan
// menuliskan "/" secara harfiah.
func TestTenantPagesDeriveAPIRootFromOwnPath(t *testing.T) {
	cases := map[string]string{
		"login_ui.html":         `\/custom\/login\/?$`,
		"users_ui.html":         `\/custom\/users\/?$`,
		"devices_owner_ui.html": `\/custom\/devices\/?$`,
	}
	pages := map[string][]byte{
		"login_ui.html":         loginUIPage,
		"users_ui.html":         usersUIPage,
		"devices_owner_ui.html": devicesOwnerUIPage,
	}

	for name, pattern := range cases {
		if !strings.Contains(string(pages[name]), pattern) {
			t.Fatalf("%s tidak menurunkan API root dari path-nya sendiri (cari %q)", name, pattern)
		}
	}
}

// TestLoginPageValidatesNextParam: tanpa penyaringan, ?next=//situs-lain
// menjadikan halaman login sebagai open redirect yang tampak berasal dari
// domain kita sendiri.
func TestLoginPageValidatesNextParam(t *testing.T) {
	body := string(loginUIPage)

	for _, guard := range []string{
		`raw.charAt(0) !== "/"`,
		`raw.charAt(1) === "/"`,
	} {
		if !strings.Contains(body, guard) {
			t.Fatalf("halaman login tidak memvalidasi ?next= (cari %q)", guard)
		}
	}
}

// TestIndexHandlesMissingAuthEndpoint: custom_index dilayani di KEDUA mode,
// jadi seluruh JavaScript identitasnya harus menangani "endpoint tidak ada"
// dengan diam — bukan dengan error di konsol browser.
func TestIndexHandlesMissingAuthEndpoint(t *testing.T) {
	body := string(customIndexPage)

	if !strings.Contains(body, "/auth/me") {
		t.Fatal("custom_index tidak memeriksa identitas")
	}
	// Bar identitas dan kartu admin harus tersembunyi secara default, sehingga
	// mode single-tenant tidak menampilkan apa pun soal user.
	for _, needle := range []string{`id="who" hidden`, `id="card-users" hidden`, `id="card-devices" hidden`} {
		if !strings.Contains(body, needle) {
			t.Fatalf("custom_index harus menyembunyikan %q secara default", needle)
		}
	}
	if !strings.Contains(body, "catch(function () {") {
		t.Fatal("custom_index harus menelan kegagalan /auth/me tanpa error")
	}
}

// TestCustomIndexStaysSelfContained: halaman lama ikut diperiksa karena fase
// ini menambahkan skrip ke dalamnya.
func TestCustomIndexStaysSelfContained(t *testing.T) {
	body := string(customIndexPage)
	for _, needle := range []string{"http://", "https://", "<script src"} {
		if strings.Contains(body, needle) {
			t.Fatalf("custom_index memuat referensi eksternal %q", needle)
		}
	}
	_ = config.AppBasePath
}

// TestCustomUIDoesNotGuardLaterRoutes menjaga bug yang sempat terjadi di fase
// ini: RequireAdmin dipasang lewat app.Group("", ...), dan grup ber-prefiks
// kosong ikut membungkus SETIAP rute yang didaftarkan setelahnya pada router
// yang sama. Akibatnya /auth/me dan /auth/password ikut menuntut admin, dan
// operator tidak bisa melihat identitasnya sendiri.
//
// Ditemukan hanya lewat verifikasi runtime, jadi test ini yang menjaganya.
func TestCustomUIDoesNotGuardLaterRoutes(t *testing.T) {
	enableMultiTenantForTest(t, true)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		middleware.StorePrincipal(c, &domainTenancy.Principal{
			UserID: 2, Username: "operator1", Role: domainTenancy.RoleOperator,
		})
		return c.Next()
	})
	InitRestCustomUI(app)

	// Didaftarkan SETELAH InitRestCustomUI, seperti /auth/me di cmd/rest.go.
	app.Get("/rute-sesudahnya", func(c fiber.Ctx) error {
		return c.SendString("terjangkau")
	})

	res, body := getPage(t, app, "/rute-sesudahnya")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rute setelah InitRestCustomUI ikut ter-guard: status = %d (body %s)", res.StatusCode, body)
	}

	// Halaman admin-nya sendiri tetap harus tertutup untuk operator.
	if res, _ := getPage(t, app, "/custom/users"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("/custom/users untuk operator: status = %d, want 404", res.StatusCode)
	}
}

// TestIndexHiddenAttributeIsEnforced menjaga bug yang hanya terlihat di browser
// sungguhan: atribut `hidden` milik UA punya spesifisitas rendah, sehingga
// selector seperti `a.card { display: block }` mengalahkannya dan kartu admin
// tetap terlihat oleh operator meski atributnya terpasang.
func TestIndexHiddenAttributeIsEnforced(t *testing.T) {
	body := string(customIndexPage)
	if !strings.Contains(body, "[hidden] { display: none !important; }") {
		t.Fatal("custom_index harus memaksa [hidden], kalau tidak kartu admin tetap terlihat operator")
	}
}
