package middleware

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
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// fakeOwnership adalah IDeviceOwnership yang dikendalikan test.
type fakeOwnership struct {
	// owners memetakan device id ke pemiliknya; device yang tidak tercantum
	// dianggap tak-ber-owner.
	owners map[string]int64
	// order menentukan urutan OwnedDeviceIDs, supaya pemilihan device default
	// deterministik seperti pada repository sungguhan.
	order []string
}

func newFakeOwnership(order ...string) *fakeOwnership {
	return &fakeOwnership{owners: map[string]int64{}, order: order}
}

func (f *fakeOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
	if !config.MultiTenantEnabled {
		return true
	}
	if p == nil {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	owner, ok := f.owners[deviceID]
	return ok && p.UserID != 0 && owner == p.UserID
}

func (f *fakeOwnership) OwnedDeviceIDs(p *domainTenancy.Principal) ([]string, bool) {
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
	for _, candidate := range f.order {
		if owner, ok := f.owners[candidate]; ok && owner == p.UserID {
			ids = append(ids, candidate)
		}
	}
	return ids, false
}

func (f *fakeOwnership) Claim(p *domainTenancy.Principal, deviceID string) error {
	f.owners[deviceID] = p.UserID
	return nil
}

func (f *fakeOwnership) Release(deviceID string) error {
	delete(f.owners, deviceID)
	return nil
}

func (f *fakeOwnership) Assign(deviceID string, userID int64) error {
	f.owners[deviceID] = userID
	return nil
}

func (f *fakeOwnership) Owner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	if owner, ok := f.owners[deviceID]; ok {
		return &domainTenancy.DeviceOwner{DeviceID: deviceID, UserID: owner}, nil
	}
	return nil, nil
}

func (f *fakeOwnership) EnsureQuota(*domainTenancy.Principal) error { return nil }

func (f *fakeOwnership) DeviceCountByUser(userID int64) (int, error) {
	count := 0
	for _, owner := range f.owners {
		if owner == userID {
			count++
		}
	}
	return count, nil
}

var _ domainTenancy.IDeviceOwnership = (*fakeOwnership)(nil)

// newTestDeviceManager membangun registry device nyata.
//
// Dipakai daripada stub supaya jalur retarget benar-benar melewati
// ResolveDevice, yang itulah yang menulis ulang Locals dan context.
func newTestDeviceManager(t *testing.T, deviceIDs ...string) *whatsapp.DeviceManager {
	t.Helper()
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	for _, id := range deviceIDs {
		dm.AddDevice(whatsapp.NewDeviceInstance(id, nil, nil))
	}
	return dm
}

// guardProbeResult adalah apa yang dilihat handler di ujung rantai.
type guardProbeResult struct {
	// LocalsDeviceID dibaca dari c.Locals("device_id").
	LocalsDeviceID string `json:"locals_device_id"`
	// ContextDeviceID dibaca dari whatsapp.DeviceFromContext(c.Context()).
	//
	// Dipisah dari Locals dengan sengaja: kalau retarget lupa memanggil
	// SetContext, handler yang membaca Locals akan melihat device yang benar
	// sementara usecase yang mengambil device dari context tetap memakai device
	// orang lain — kebocoran yang tidak terlihat dari luar sama sekali.
	ContextDeviceID string `json:"context_device_id"`
	// LocalsInstanceID dibaca dari c.Locals("device").
	LocalsInstanceID string `json:"locals_instance_id"`
}

// newGuardApp meniru susunan grup di rest.go: sebuah lapisan yang meniru
// DeviceMiddleware (resolusi header/query dengan fallback ke device default),
// lalu DeviceOwnerGuard.
func newGuardApp(
	t *testing.T,
	dm *whatsapp.DeviceManager,
	own domainTenancy.IDeviceOwnership,
	defaultDevice string,
	principal *domainTenancy.Principal,
) *fiber.App {
	t.Helper()

	app := fiber.New()

	// Tiruan DeviceMiddleware: menulis ketiga nilai yang sama.
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			StorePrincipal(c, principal)
		}
		requested := requestedDeviceID(c)
		if requested == "" {
			requested = defaultDevice
		}
		if requested == "" {
			return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
				Status: fiber.StatusBadRequest, Code: "DEVICE_ID_REQUIRED",
			})
		}
		instance, resolvedID, err := dm.ResolveDevice(requested)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
				Status:  fiber.StatusNotFound,
				Code:    "DEVICE_NOT_FOUND",
				Message: "device not found; create a device first from /api/devices or provide a valid X-Device-Id",
				Results: map[string]string{"device_id": resolvedID},
			})
		}
		c.Locals("device_id", resolvedID)
		c.Locals("device", instance)
		c.SetContext(whatsapp.ContextWithDevice(c.Context(), instance))
		return c.Next()
	})

	app.Use(DeviceOwnerGuard(dm, own))

	app.Get("/probe", func(c fiber.Ctx) error {
		result := guardProbeResult{}
		result.LocalsDeviceID, _ = c.Locals("device_id").(string)
		if instance, ok := whatsapp.DeviceFromContext(c.Context()); ok && instance != nil {
			result.ContextDeviceID = instance.ID()
		}
		if instance, ok := c.Locals("device").(*whatsapp.DeviceInstance); ok && instance != nil {
			result.LocalsInstanceID = instance.ID()
		}
		return c.JSON(result)
	})
	return app
}

func probe(t *testing.T, app *fiber.App, header string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(DeviceIDHeader, header)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func probeResult(t *testing.T, body string) guardProbeResult {
	t.Helper()
	var result guardProbeResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return result
}

func operator(userID int64) *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: userID, Username: "operator", Role: domainTenancy.RoleOperator}
}

func admin() *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin}
}

// _____________________________________________________________________________
// Device disebut eksplisit

func TestGuardAllowsOwnDeviceWhenExplicit(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-a", "dev-b")
	own := newFakeOwnership("dev-a", "dev-b")
	own.owners["dev-a"] = 7
	own.owners["dev-b"] = 8

	status, body := probe(t, newGuardApp(t, dm, own, "", operator(7)), "dev-a")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if got := probeResult(t, body); got.LocalsDeviceID != "dev-a" || got.ContextDeviceID != "dev-a" {
		t.Fatalf("device yang dipakai salah: %+v", got)
	}
}

// TestGuardRejectsOtherTenantIndistinguishably: response untuk device milik
// orang lain harus tidak bisa dibedakan dari device yang benar-benar tidak ada.
// Perbedaan sekecil apa pun mengonfirmasi bahwa device itu eksis (K4).
func TestGuardRejectsOtherTenantIndistinguishably(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-a", "dev-b")
	own := newFakeOwnership("dev-a", "dev-b")
	own.owners["dev-a"] = 7
	own.owners["dev-b"] = 8
	app := newGuardApp(t, dm, own, "", operator(7))

	crossStatus, crossBody := probe(t, app, "dev-b")
	if crossStatus != http.StatusNotFound {
		t.Fatalf("device orang lain: status = %d, want 404 (body %s)", crossStatus, crossBody)
	}

	missingStatus, missingBody := probe(t, app, "dev-tidak-ada")
	if missingStatus != http.StatusNotFound {
		t.Fatalf("device tidak ada: status = %d, want 404 (body %s)", missingStatus, missingBody)
	}

	// Samakan device_id yang memang berbeda, lalu bandingkan sisanya.
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
	if cross, missing := normalise(crossBody), normalise(missingBody); cross != missing {
		t.Fatalf("response harus tidak bisa dibedakan:\n  cross-tenant: %s\n  tidak ada   : %s", cross, missing)
	}
}

// TestGuardRejectsUnclaimedDeviceForOperator: device tanpa pemilik hanya untuk
// admin. Default ketat, supaya device yang gagal ter-klaim tidak otomatis
// terbuka untuk semua orang.
func TestGuardRejectsUnclaimedDeviceForOperator(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-orphan")
	own := newFakeOwnership("dev-orphan")

	status, body := probe(t, newGuardApp(t, dm, own, "", operator(7)), "dev-orphan")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, body)
	}
}

func TestGuardAllowsAdminEverywhere(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b", "dev-orphan")
	own := newFakeOwnership("dev-b", "dev-orphan")
	own.owners["dev-b"] = 8
	app := newGuardApp(t, dm, own, "", admin())

	for _, deviceID := range []string{"dev-b", "dev-orphan"} {
		status, body := probe(t, app, deviceID)
		if status != http.StatusOK {
			t.Fatalf("admin ke %s: status = %d (body %s)", deviceID, status, body)
		}
	}
}

func TestGuardRejectsWithoutPrincipal(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-a")
	own := newFakeOwnership("dev-a")
	own.owners["dev-a"] = 7

	status, _ := probe(t, newGuardApp(t, dm, own, "", nil), "dev-a")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (tanpa principal harus gagal tertutup)", status)
	}
}

// _____________________________________________________________________________
// Fallback tanpa X-Device-Id (lubang L2)

// TestGuardFallbackRetargetsToOwnDevice adalah test terpenting di fase ini.
//
// Tanpa header, DeviceMiddleware jatuh ke device default global yang bisa milik
// orang lain. Guard harus mengarahkan ulang ke device milik pemanggil, dan
// pengarahan itu harus terlihat DI KEDUA tempat: Locals dan context.
func TestGuardFallbackRetargetsToOwnDevice(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b", "dev-a")
	own := newFakeOwnership("dev-a", "dev-b")
	own.owners["dev-a"] = 7 // milik pemanggil
	own.owners["dev-b"] = 8 // device default global, milik orang lain

	status, body := probe(t, newGuardApp(t, dm, own, "dev-b", operator(7)), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	got := probeResult(t, body)
	if got.LocalsDeviceID != "dev-a" {
		t.Fatalf("Locals(\"device_id\") = %q, want dev-a", got.LocalsDeviceID)
	}
	if got.LocalsInstanceID != "dev-a" {
		t.Fatalf("Locals(\"device\") = %q, want dev-a", got.LocalsInstanceID)
	}
	// Inilah yang paling mudah terlewat: kalau SetContext tidak ikut ditimpa,
	// nilai ini masih dev-b dan usecase akan mengirim dari device orang lain.
	if got.ContextDeviceID != "dev-a" {
		t.Fatalf("device di context = %q, want dev-a — SetContext tidak ditimpa", got.ContextDeviceID)
	}
}

// TestGuardFallbackContextIsOverriddenSeparately menguji khusus jalur context,
// terpisah dari Locals, supaya kegagalannya tidak bisa tersamarkan oleh
// assertion lain yang lulus.
func TestGuardFallbackContextIsOverriddenSeparately(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b", "dev-a")
	own := newFakeOwnership("dev-a", "dev-b")
	own.owners["dev-a"] = 7
	own.owners["dev-b"] = 8

	_, body := probe(t, newGuardApp(t, dm, own, "dev-b", operator(7)), "")
	if got := probeResult(t, body); got.ContextDeviceID == "dev-b" {
		t.Fatal("context masih menunjuk device orang lain")
	}
}

func TestGuardFallbackRejectsWhenNoOwnDevice(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b")
	own := newFakeOwnership("dev-b")
	own.owners["dev-b"] = 8

	status, body := probe(t, newGuardApp(t, dm, own, "dev-b", operator(7)), "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, body)
	}
}

// TestGuardFallbackRequiresExplicitIDWhenSeveralOwned: menebak salah satu akan
// mengirim pesan dari device yang tidak diminta.
func TestGuardFallbackRequiresExplicitIDWhenSeveralOwned(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b", "dev-a", "dev-a2")
	own := newFakeOwnership("dev-a", "dev-a2", "dev-b")
	own.owners["dev-a"] = 7
	own.owners["dev-a2"] = 7
	own.owners["dev-b"] = 8

	status, body := probe(t, newGuardApp(t, dm, own, "dev-b", operator(7)), "")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(body, "DEVICE_ID_REQUIRED") {
		t.Fatalf("body tidak memuat DEVICE_ID_REQUIRED: %s", body)
	}
}

func TestGuardFallbackAllowsOwnDefault(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-a")
	own := newFakeOwnership("dev-a")
	own.owners["dev-a"] = 7

	status, body := probe(t, newGuardApp(t, dm, own, "dev-a", operator(7)), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

func TestGuardFallbackKeepsDefaultForAdmin(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b")
	own := newFakeOwnership("dev-b")
	own.owners["dev-b"] = 8

	status, body := probe(t, newGuardApp(t, dm, own, "dev-b", admin()), "")
	if status != http.StatusOK {
		t.Fatalf("admin harus tetap memakai default lama: status = %d (body %s)", status, body)
	}
	if got := probeResult(t, body); got.LocalsDeviceID != "dev-b" {
		t.Fatalf("admin tidak boleh diarahkan ulang: %+v", got)
	}
}

// TestGuardFallbackRejectsStaleOwnership: baris kepemilikan bisa menunjuk
// device yang sudah dipurge dari registry. Guard harus menolak, bukan
// meneruskan request dengan device orang lain yang masih ada di Locals.
func TestGuardFallbackRejectsStaleOwnership(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b") // dev-a sudah tidak ada di registry
	own := newFakeOwnership("dev-a", "dev-b")
	own.owners["dev-a"] = 7
	own.owners["dev-b"] = 8

	status, body := probe(t, newGuardApp(t, dm, own, "dev-b", operator(7)), "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, body)
	}
	if strings.Contains(body, `"locals_device_id":"dev-b"`) {
		t.Fatal("request diteruskan ke handler dengan device orang lain")
	}
}

// _____________________________________________________________________________
// Mode single-tenant

// TestGuardIsTransparentWhenDisabled adalah jaminan zero-regression.
func TestGuardIsTransparentWhenDisabled(t *testing.T) {
	withMultiTenant(t, false)

	dm := newTestDeviceManager(t, "dev-b", "dev-orphan")
	own := newFakeOwnership("dev-b", "dev-orphan")
	own.owners["dev-b"] = 8

	cases := []struct {
		name      string
		header    string
		principal *domainTenancy.Principal
	}{
		{"device orang lain", "dev-b", operator(7)},
		{"device tak-ber-owner", "dev-orphan", operator(7)},
		{"tanpa principal", "dev-b", nil},
		{"tanpa header", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := probe(t, newGuardApp(t, dm, own, "dev-b", tc.principal), tc.header)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", status, body)
			}
		})
	}
}

// TestGuardIsTransparentWithoutOwnership: ownership nil berarti fitur tidak
// terpasang, dan guard tidak boleh menghalangi apa pun.
func TestGuardIsTransparentWithoutOwnership(t *testing.T) {
	withMultiTenant(t, true)

	dm := newTestDeviceManager(t, "dev-b")
	status, body := probe(t, newGuardApp(t, dm, nil, "dev-b", nil), "dev-b")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
}
