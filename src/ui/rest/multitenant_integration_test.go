package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Test integrasi yang mengunci perilaku inti isolasi multi-tenant.
//
// Ini test paling berharga di seluruh fitur: ia menangkap regresi saat sync
// upstream berikutnya menambahkan rute baru, karena rute yang memakai path
// param TIDAK otomatis ter-guard.
//
// Yang dikunci adalah matriks A2 (cross-tenant harus 404) dan A3 (fallback
// tanpa X-Device-Id) dari docs/multitenant/phase-09-verifikasi-rollout.md.

// integrationOwnership adalah IDeviceOwnership dengan data tetap:
// dev-a milik user 7, dev-b milik user 8, dev-orphan tanpa pemilik.
type integrationOwnership struct {
	owners map[string]int64
	order  []string
}

func newIntegrationOwnership() *integrationOwnership {
	return &integrationOwnership{
		owners: map[string]int64{"dev-a": 7, "dev-b": 8},
		order:  []string{"dev-a", "dev-a2", "dev-b", "dev-orphan"},
	}
}

func (o *integrationOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
	if !config.MultiTenantEnabled {
		return true
	}
	if p == nil {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	owner, ok := o.owners[deviceID]
	return ok && p.UserID != 0 && owner == p.UserID
}

func (o *integrationOwnership) OwnedDeviceIDs(p *domainTenancy.Principal) ([]string, bool) {
	if !config.MultiTenantEnabled {
		return nil, true
	}
	if p == nil {
		return nil, false
	}
	if p.IsAdmin() {
		return nil, true
	}
	var ids []string
	for _, candidate := range o.order {
		if owner, ok := o.owners[candidate]; ok && owner == p.UserID {
			ids = append(ids, candidate)
		}
	}
	return ids, false
}

func (o *integrationOwnership) Claim(p *domainTenancy.Principal, deviceID string) error {
	o.owners[deviceID] = p.UserID
	return nil
}
func (o *integrationOwnership) Release(deviceID string) error {
	delete(o.owners, deviceID)
	return nil
}
func (o *integrationOwnership) Assign(deviceID string, userID int64) error {
	o.owners[deviceID] = userID
	return nil
}
func (o *integrationOwnership) Owner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	if owner, ok := o.owners[deviceID]; ok {
		return &domainTenancy.DeviceOwner{DeviceID: deviceID, UserID: owner}, nil
	}
	return nil, nil
}
func (o *integrationOwnership) EnsureQuota(*domainTenancy.Principal) error { return nil }
func (o *integrationOwnership) DeviceCountByUser(userID int64) (int, error) {
	count := 0
	for _, owner := range o.owners {
		if owner == userID {
			count++
		}
	}
	return count, nil
}

var _ domainTenancy.IDeviceOwnership = (*integrationOwnership)(nil)

// newIntegrationApp menyusun middleware dengan urutan yang SAMA seperti
// cmd/rest.go: principal lebih dulu, lalu DeviceMiddleware, lalu
// DeviceOwnerGuard pada grup ber-prefiks kosong.
//
// Meniru susunannya, bukan memanggil restServer, karena restServer membuka DB
// dan menyambung ke WhatsApp. Yang diuji di sini adalah urutan penjagaannya.
func newIntegrationApp(
	t *testing.T,
	principal *domainTenancy.Principal,
	own domainTenancy.IDeviceOwnership,
	devices ...string,
) *fiber.App {
	t.Helper()

	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	for _, id := range devices {
		dm.AddDevice(whatsapp.NewDeviceInstance(id, nil, nil))
	}

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			middleware.StorePrincipal(c, principal)
		}
		return c.Next()
	})

	// Rute device-scoped: header/query, dijaga oleh middleware berpasangan.
	scoped := app.Group("",
		middleware.DeviceMiddleware(dm),
		middleware.DeviceOwnerGuard(dm, own),
	)
	scoped.Get("/probe/scoped", func(c fiber.Ctx) error {
		deviceID, _ := c.Locals("device_id").(string)
		contextID := ""
		if inst, ok := whatsapp.DeviceFromContext(c.Context()); ok && inst != nil {
			contextID = inst.ID()
		}
		return c.JSON(fiber.Map{"device_id": deviceID, "context_device_id": contextID})
	})

	return app
}

func integrationOperator(userID int64) *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: userID, Username: "operator", Role: domainTenancy.RoleOperator}
}

func integrationAdmin() *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin}
}

func hitScoped(t *testing.T, app *fiber.App, header string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe/scoped", nil)
	if header != "" {
		req.Header.Set(middleware.DeviceIDHeader, header)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// _____________________________________________________________________________
// Matriks A2 — akses lintas tenant

// TestIntegrationCrossTenantIsRejected mengunci baris 7-9 matriks A2: setiap
// endpoint device-scoped yang diminta dengan device milik tenant lain harus
// menjawab 404.
func TestIntegrationCrossTenantIsRejected(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newIntegrationOwnership()
	app := newIntegrationApp(t, integrationOperator(7), own, "dev-a", "dev-b", "dev-orphan")

	cases := []struct {
		name   string
		header string
	}{
		{"device milik tenant lain", "dev-b"},
		{"device tanpa pemilik", "dev-orphan"},
		{"device tidak ada", "dev-hantu"},
	}

	bodies := make([]string, 0, len(cases))
	for _, tc := range cases {
		status, body := hitScoped(t, app, tc.header)
		if status != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404 (body %s)", tc.name, status, body)
		}
		bodies = append(bodies, body)
	}

	// Ketiganya harus tidak bisa dibedakan setelah device_id dinormalkan:
	// perbedaan sekecil apa pun mengonfirmasi keberadaan sebuah device (K4).
	normalise := func(body string) string {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		if results, ok := parsed["results"].(map[string]any); ok {
			results["device_id"] = "<normalised>"
		}
		encoded, _ := json.Marshal(parsed)
		return string(encoded)
	}
	first := normalise(bodies[0])
	for i := 1; i < len(bodies); i++ {
		if got := normalise(bodies[i]); got != first {
			t.Fatalf("response harus tidak bisa dibedakan:\n  %s: %s\n  %s: %s",
				cases[0].name, first, cases[i].name, got)
		}
	}
}

func TestIntegrationOwnDeviceIsAllowed(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newIntegrationOwnership()
	app := newIntegrationApp(t, integrationOperator(7), own, "dev-a", "dev-b")

	status, body := hitScoped(t, app, "dev-a")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, `"device_id":"dev-a"`) {
		t.Fatalf("device yang dipakai salah: %s", body)
	}
}

func TestIntegrationAdminSeesEverything(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newIntegrationOwnership()
	app := newIntegrationApp(t, integrationAdmin(), own, "dev-a", "dev-b", "dev-orphan")

	for _, deviceID := range []string{"dev-a", "dev-b", "dev-orphan"} {
		if status, body := hitScoped(t, app, deviceID); status != http.StatusOK {
			t.Fatalf("admin ke %s: status = %d (body %s)", deviceID, status, body)
		}
	}
}

// TestIntegrationRejectsWithoutPrincipal: gagal ke arah aman. Kalau guard
// terpasang di jalur yang kehilangan principal, request harus gagal — bukan
// lolos.
func TestIntegrationRejectsWithoutPrincipal(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newIntegrationOwnership()
	app := newIntegrationApp(t, nil, own, "dev-a", "dev-b")

	if status, body := hitScoped(t, app, "dev-a"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, body)
	}
}

// _____________________________________________________________________________
// Matriks A3 — fallback tanpa X-Device-Id

// TestIntegrationFallbackMatrix mengunci baris 25-28. DefaultDevice() hanya
// mengembalikan instance kalau registry berisi TEPAT SATU device, jadi tiap
// kasus memakai jumlah device yang berbeda.
func TestIntegrationFallbackMatrix(t *testing.T) {
	enableMultiTenantForTest(t, true)

	t.Run("pemilik satu device, registry satu device", func(t *testing.T) {
		own := newIntegrationOwnership()
		app := newIntegrationApp(t, integrationOperator(7), own, "dev-a")

		status, body := hitScoped(t, app, "")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", status, body)
		}
		// Locals DAN context harus menunjuk device yang sama.
		if !strings.Contains(body, `"device_id":"dev-a"`) ||
			!strings.Contains(body, `"context_device_id":"dev-a"`) {
			t.Fatalf("device tidak konsisten antara Locals dan context: %s", body)
		}
	})

	t.Run("tanpa device sendiri, default milik orang lain", func(t *testing.T) {
		own := newIntegrationOwnership()
		// Registry satu device, milik user 8; pemanggil user 9 tidak punya apa pun.
		app := newIntegrationApp(t, integrationOperator(9), own, "dev-b")

		if status, body := hitScoped(t, app, ""); status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", status, body)
		}
	})

	t.Run("registry banyak device, DeviceMiddleware menolak lebih dulu", func(t *testing.T) {
		own := newIntegrationOwnership()
		app := newIntegrationApp(t, integrationOperator(7), own, "dev-a", "dev-b")

		// DefaultDevice() nil untuk >1 device, jadi DeviceMiddleware menjawab
		// 400 sebelum guard berjalan. Yang penting: TIDAK 200 dengan device
		// milik orang lain.
		status, body := hitScoped(t, app, "")
		if status == http.StatusOK {
			t.Fatalf("request tanpa device_id tidak boleh lolos: %s", body)
		}
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %s)", status, body)
		}
	})

	t.Run("admin memakai default lama", func(t *testing.T) {
		own := newIntegrationOwnership()
		app := newIntegrationApp(t, integrationAdmin(), own, "dev-b")

		if status, body := hitScoped(t, app, ""); status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", status, body)
		}
	})
}

// _____________________________________________________________________________
// Matriks A8 — regresi mode single-tenant

// TestIntegrationSingleTenantIsUnchanged mengunci jaminan rollback: dengan flag
// mati, seluruh kombinasi di atas harus berperilaku seperti sebelum fitur ini
// ada. Kalau test ini pernah gagal, ada fase yang melanggar K1.
func TestIntegrationSingleTenantIsUnchanged(t *testing.T) {
	enableMultiTenantForTest(t, false)

	own := newIntegrationOwnership()

	cases := []struct {
		name      string
		principal *domainTenancy.Principal
		header    string
		devices   []string
	}{
		{"device orang lain", integrationOperator(7), "dev-b", []string{"dev-a", "dev-b"}},
		{"device tanpa pemilik", integrationOperator(7), "dev-orphan", []string{"dev-orphan"}},
		{"tanpa principal", nil, "dev-b", []string{"dev-b"}},
		{"tanpa header, satu device", nil, "", []string{"dev-b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newIntegrationApp(t, tc.principal, own, tc.devices...)
			if status, body := hitScoped(t, app, tc.header); status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", status, body)
			}
		})
	}
}

// TestIntegrationRollbackKeepsOwnership: mematikan flag lalu menyalakannya lagi
// harus memulihkan penjagaan dengan data kepemilikan yang sama. Inilah yang
// membuat rollback aman — tidak ada migration yang perlu dibalik.
func TestIntegrationRollbackKeepsOwnership(t *testing.T) {
	own := newIntegrationOwnership()

	// Menyala: device orang lain ditolak.
	enableMultiTenantForTest(t, true)
	app := newIntegrationApp(t, integrationOperator(7), own, "dev-a", "dev-b")
	if status, _ := hitScoped(t, app, "dev-b"); status != http.StatusNotFound {
		t.Fatalf("mode aktif: status = %d, want 404", status)
	}

	// Mati: permisif kembali.
	config.MultiTenantEnabled = false
	if status, _ := hitScoped(t, app, "dev-b"); status != http.StatusOK {
		t.Fatalf("mode mati: status = %d, want 200", status)
	}

	// Menyala lagi: penjagaan kembali, tanpa perlu menyusun ulang apa pun.
	config.MultiTenantEnabled = true
	if status, _ := hitScoped(t, app, "dev-b"); status != http.StatusNotFound {
		t.Fatalf("dinyalakan lagi: status = %d, want 404", status)
	}
	if status, _ := hitScoped(t, app, "dev-a"); status != http.StatusOK {
		t.Fatalf("device sendiri setelah rollback: status = %d, want 200", status)
	}
}
