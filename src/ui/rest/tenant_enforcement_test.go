package rest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// Enforcement kepemilikan untuk rute yang me-resolve device dari path param,
// yaitu yang berada di luar DeviceMiddleware/DeviceOwnerGuard.
//
// Lihat docs/multitenant/phase-05-enforcement-rute.md.

// tenantOwnership adalah IDeviceOwnership sederhana untuk test enforcement.
type tenantOwnership struct {
	owners map[string]int64
}

func newTenantOwnership(owners map[string]int64) *tenantOwnership {
	if owners == nil {
		owners = map[string]int64{}
	}
	return &tenantOwnership{owners: owners}
}

func (o *tenantOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
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

func (o *tenantOwnership) OwnedDeviceIDs(p *domainTenancy.Principal) ([]string, bool) {
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
	for _, candidate := range []string{"dev-a", "dev-b", "dev-orphan"} {
		if owner, ok := o.owners[candidate]; ok && owner == p.UserID {
			ids = append(ids, candidate)
		}
	}
	return ids, false
}

func (o *tenantOwnership) Claim(p *domainTenancy.Principal, deviceID string) error {
	o.owners[deviceID] = p.UserID
	return nil
}
func (o *tenantOwnership) Release(deviceID string) error { delete(o.owners, deviceID); return nil }
func (o *tenantOwnership) Assign(deviceID string, userID int64) error {
	o.owners[deviceID] = userID
	return nil
}
func (o *tenantOwnership) Owner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	if owner, ok := o.owners[deviceID]; ok {
		return &domainTenancy.DeviceOwner{DeviceID: deviceID, UserID: owner}, nil
	}
	return nil, nil
}
func (o *tenantOwnership) EnsureQuota(*domainTenancy.Principal) error { return nil }

func (o *tenantOwnership) DeviceCountByUser(userID int64) (int, error) {
	count := 0
	for _, owner := range o.owners {
		if owner == userID {
			count++
		}
	}
	return count, nil
}

var _ domainTenancy.IDeviceOwnership = (*tenantOwnership)(nil)

func tenantOperator(userID int64) *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: userID, Username: "operator", Role: domainTenancy.RoleOperator}
}

func tenantAdmin() *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin}
}

// withPrincipal menyuntikkan principal sebelum rute dijalankan, meniru apa yang
// dilakukan AuthGate di aplikasi sungguhan.
func withPrincipal(app *fiber.App, principal *domainTenancy.Principal) {
	app.Use(func(c fiber.Ctx) error {
		if principal != nil {
			middleware.StorePrincipal(c, principal)
		}
		return c.Next()
	})
}

func twoDeviceManager() *whatsapp.DeviceManager {
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance("dev-a", nil, nil))
	dm.AddDevice(whatsapp.NewDeviceInstance("dev-b", nil, nil))
	return dm
}

// _____________________________________________________________________________
// Config command

func newCommandConfigTenantApp(t *testing.T, principal *domainTenancy.Principal, own domainTenancy.IDeviceOwnership) (*fiber.App, *fakeCommandConfigStore) {
	t.Helper()

	store := newFakeCommandConfigStore()
	app := fiber.New()
	withPrincipal(app, principal)
	InitRestCommandConfigWithOwnership(app, twoDeviceManager(), store, own)
	return app, store
}

func TestCommandConfigCrossTenantIsRejected(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	app, store := newCommandConfigTenantApp(t, tenantOperator(7), own)

	// Seed config milik dev-b supaya kegagalan bukan sekadar "config tidak ada".
	if err := store.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{DeviceID: "dev-b", Enabled: true}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/devices/dev-b/command/config", ""},
		{http.MethodPut, "/devices/dev-b/command/config", `{"enabled":false}`},
		{http.MethodDelete, "/devices/dev-b/command/config", ""},
	}

	for _, tc := range cases {
		res, body := doJSON(t, app, tc.method, tc.path, tc.body)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404 (body %s)", tc.method, tc.path, res.StatusCode, body)
		}
	}

	// Yang penting: config dev-b harus masih utuh setelah percobaan di atas.
	if cfg, _ := store.GetDeviceCommandConfig("dev-b"); cfg == nil {
		t.Fatal("config device orang lain terhapus / termodifikasi")
	}
}

func TestCommandConfigOwnDeviceStillWorks(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7})
	app, _ := newCommandConfigTenantApp(t, tenantOperator(7), own)

	res, body := doJSON(t, app, http.MethodPut, "/devices/dev-a/command/config", `{"enabled":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
}

// TestCommandConfigListIsFiltered: endpoint agregat mengembalikan config lintas
// device, jadi harus disaring (L6).
func TestCommandConfigListIsFiltered(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})

	seed := func(store *fakeCommandConfigStore) {
		for _, deviceID := range []string{"dev-a", "dev-b"} {
			if err := store.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{DeviceID: deviceID, Enabled: true}); err != nil {
				t.Fatalf("seed %s: %v", deviceID, err)
			}
		}
	}

	operatorApp, operatorStore := newCommandConfigTenantApp(t, tenantOperator(7), own)
	seed(operatorStore)
	_, operatorBody := doJSON(t, operatorApp, http.MethodGet, "/command/configs", "")
	if !strings.Contains(operatorBody, "dev-a") {
		t.Fatalf("operator harus melihat device sendiri: %s", operatorBody)
	}
	if strings.Contains(operatorBody, "dev-b") {
		t.Fatalf("config device orang lain bocor: %s", operatorBody)
	}

	adminApp, adminStore := newCommandConfigTenantApp(t, tenantAdmin(), own)
	seed(adminStore)
	_, adminBody := doJSON(t, adminApp, http.MethodGet, "/command/configs", "")
	if !strings.Contains(adminBody, "dev-a") || !strings.Contains(adminBody, "dev-b") {
		t.Fatalf("admin harus melihat semuanya: %s", adminBody)
	}
}

// TestCommandConfigDeleteOrphanRequiresAdmin menutup lubang yang mudah
// terlewat: handler DELETE sengaja jatuh ke path param mentah supaya config
// yatim tetap bisa dibersihkan, dan jalur itu melewatkan pemeriksaan
// kepemilikan karena device-nya tidak bisa diresolve.
func TestCommandConfigDeleteOrphanRequiresAdmin(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7})

	// "dev-hilang" tidak ada di registry, tapi punya baris config.
	operatorApp, operatorStore := newCommandConfigTenantApp(t, tenantOperator(7), own)
	if err := operatorStore.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{DeviceID: "dev-hilang", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, body := doJSON(t, operatorApp, http.MethodDelete, "/devices/dev-hilang/command/config", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("operator: status = %d, want 404 (body %s)", res.StatusCode, body)
	}
	if cfg, _ := operatorStore.GetDeviceCommandConfig("dev-hilang"); cfg == nil {
		t.Fatal("operator berhasil menghapus config yatim lewat jalur fallback")
	}

	// Admin tetap bisa membersihkannya — itulah gunanya fallback tersebut.
	adminApp, adminStore := newCommandConfigTenantApp(t, tenantAdmin(), own)
	if err := adminStore.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{DeviceID: "dev-hilang", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, body = doJSON(t, adminApp, http.MethodDelete, "/devices/dev-hilang/command/config", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if cfg, _ := adminStore.GetDeviceCommandConfig("dev-hilang"); cfg != nil {
		t.Fatal("admin seharusnya bisa menghapus config yatim")
	}
}

func TestCommandConfigIsTransparentWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	own := newTenantOwnership(map[string]int64{"dev-b": 8})
	app, store := newCommandConfigTenantApp(t, tenantOperator(7), own)
	if err := store.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{DeviceID: "dev-b", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, body := doJSON(t, app, http.MethodGet, "/devices/dev-b/command/config", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if _, listBody := doJSON(t, app, http.MethodGet, "/command/configs", ""); !strings.Contains(listBody, "dev-b") {
		t.Fatalf("daftar tidak boleh disaring di mode single-tenant: %s", listBody)
	}
}

// _____________________________________________________________________________
// Antrian pesan

func newQueueTenantApp(t *testing.T, principal *domainTenancy.Principal, own domainTenancy.IDeviceOwnership, repo *queueRepoSpy) *fiber.App {
	t.Helper()

	app := fiber.New()
	app.Use(middleware.Recovery())
	withPrincipal(app, principal)
	InitRestMessageQueueWithOwnership(app, twoDeviceManager(), repo, own)
	return app
}

func TestQueueCrossTenantIsRejected(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	repo := &queueRepoSpy{}
	app := newQueueTenantApp(t, tenantOperator(7), own, repo)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/devices/dev-b/queue"},
		{http.MethodDelete, "/devices/dev-b/queue/1"},
	} {
		res, body := doJSON(t, app, tc.method, tc.path, "")
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404 (body %s)", tc.method, tc.path, res.StatusCode, body)
		}
	}

	// Repository tidak boleh tersentuh sama sekali: penolakan harus terjadi
	// sebelum ada query, bukan bergantung pada scoping di lapisan bawah.
	if repo.cancelDeviceID != "" || repo.cancelID != 0 {
		t.Fatalf("repository tersentuh untuk device orang lain: %q id=%d", repo.cancelDeviceID, repo.cancelID)
	}
	if repo.listFilter.DeviceID != "" {
		t.Fatalf("list repository tersentuh: %+v", repo.listFilter)
	}
}

// TestQueueCancelForwardsOwnDeviceID: setelah device ter-guard, pembatalan
// masih harus meneruskan device id ke repository, yang memfilter
// "WHERE id = ? AND device_id = ?". Itu lapis kedua terhadap queue_id yang
// ditebak.
func TestQueueCancelForwardsOwnDeviceID(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7})
	repo := &queueRepoSpy{}
	app := newQueueTenantApp(t, tenantOperator(7), own, repo)

	if _, body := doJSON(t, app, http.MethodDelete, "/devices/dev-a/queue/42", ""); body == "" {
		t.Fatal("response kosong")
	}
	if repo.cancelDeviceID != "dev-a" {
		t.Fatalf("cancelDeviceID = %q, want dev-a", repo.cancelDeviceID)
	}
	if repo.cancelID != 42 {
		t.Fatalf("cancelID = %d, want 42", repo.cancelID)
	}
}

func TestQueueIsTransparentWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	own := newTenantOwnership(map[string]int64{"dev-b": 8})
	repo := &queueRepoSpy{}
	app := newQueueTenantApp(t, tenantOperator(7), own, repo)

	res, body := doJSON(t, app, http.MethodGet, "/devices/dev-b/queue", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.StatusCode, body)
	}
}

// _____________________________________________________________________________
// Config Chatwoot

func newChatwootConfigTenantApp(t *testing.T, principal *domainTenancy.Principal, own domainTenancy.IDeviceOwnership, store *fakeConfigStore) *fiber.App {
	t.Helper()

	handler := NewChatwootHandler(nil, nil, nil, twoDeviceManager(), store)
	handler.SetOwnership(own)

	app := fiber.New()
	withPrincipal(app, principal)
	app.Get("/chatwoot/configs", handler.ListChatwootConfigs)
	app.Get("/devices/:device_id/chatwoot/config", handler.GetChatwootConfig)
	app.Put("/devices/:device_id/chatwoot/config", handler.UpsertChatwootConfig)
	app.Delete("/devices/:device_id/chatwoot/config", handler.DeleteChatwootConfig)
	return app
}

func TestChatwootConfigCrossTenantIsRejected(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	store := newFakeConfigStore()
	app := newChatwootConfigTenantApp(t, tenantOperator(7), own, store)

	if err := store.SaveChatwootDeviceConfig(&domainChatStorage.ChatwootDeviceConfig{
		DeviceID: "dev-b", ChatwootURL: "https://203.0.113.10", AccountID: 1, InboxID: 5, APIToken: "rahasia", Enabled: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/devices/dev-b/chatwoot/config", ""},
		{http.MethodPut, "/devices/dev-b/chatwoot/config", `{"chatwoot_url":"https://203.0.113.99","account_id":2,"inbox_id":6,"api_token":"x"}`},
		{http.MethodDelete, "/devices/dev-b/chatwoot/config", ""},
	} {
		res, body := doJSON(t, app, tc.method, tc.path, tc.body)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404 (body %s)", tc.method, tc.path, res.StatusCode, body)
		}
	}

	cfg := store.configs["dev-b"]
	if cfg == nil {
		t.Fatal("config device orang lain terhapus")
	}
	// Token rahasia device lain tidak boleh tertimpa maupun terbaca.
	if cfg.APIToken != "rahasia" {
		t.Fatalf("config device orang lain termodifikasi: %+v", cfg)
	}
}

func TestChatwootConfigListIsFiltered(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	store := newFakeConfigStore()
	for _, deviceID := range []string{"dev-a", "dev-b"} {
		if err := store.SaveChatwootDeviceConfig(&domainChatStorage.ChatwootDeviceConfig{
			DeviceID: deviceID, ChatwootURL: "https://203.0.113.10", AccountID: 1, InboxID: 5, Enabled: true,
		}); err != nil {
			t.Fatalf("seed %s: %v", deviceID, err)
		}
	}

	operatorApp := newChatwootConfigTenantApp(t, tenantOperator(7), own, store)
	_, operatorBody := doJSON(t, operatorApp, http.MethodGet, "/chatwoot/configs", "")
	if !strings.Contains(operatorBody, "dev-a") {
		t.Fatalf("operator harus melihat device sendiri: %s", operatorBody)
	}
	if strings.Contains(operatorBody, "dev-b") {
		t.Fatalf("config device orang lain bocor: %s", operatorBody)
	}

	adminApp := newChatwootConfigTenantApp(t, tenantAdmin(), own, store)
	_, adminBody := doJSON(t, adminApp, http.MethodGet, "/chatwoot/configs", "")
	if !strings.Contains(adminBody, "dev-a") || !strings.Contains(adminBody, "dev-b") {
		t.Fatalf("admin harus melihat semuanya: %s", adminBody)
	}
}

func TestChatwootConfigDeleteOrphanRequiresAdmin(t *testing.T) {
	enableMultiTenantForTest(t, true)

	own := newTenantOwnership(map[string]int64{"dev-a": 7})

	operatorStore := newFakeConfigStore()
	if err := operatorStore.SaveChatwootDeviceConfig(&domainChatStorage.ChatwootDeviceConfig{
		DeviceID: "dev-hilang", ChatwootURL: "https://203.0.113.10", AccountID: 1, InboxID: 5,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	operatorApp := newChatwootConfigTenantApp(t, tenantOperator(7), own, operatorStore)

	res, body := doJSON(t, operatorApp, http.MethodDelete, "/devices/dev-hilang/chatwoot/config", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("operator: status = %d, want 404 (body %s)", res.StatusCode, body)
	}
	if operatorStore.configs["dev-hilang"] == nil {
		t.Fatal("operator berhasil menghapus config yatim lewat jalur fallback")
	}

	adminStore := newFakeConfigStore()
	if err := adminStore.SaveChatwootDeviceConfig(&domainChatStorage.ChatwootDeviceConfig{
		DeviceID: "dev-hilang", ChatwootURL: "https://203.0.113.10", AccountID: 1, InboxID: 5,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	adminApp := newChatwootConfigTenantApp(t, tenantAdmin(), own, adminStore)
	if res, body := doJSON(t, adminApp, http.MethodDelete, "/devices/dev-hilang/chatwoot/config", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200 (body %s)", res.StatusCode, body)
	}
}

func TestChatwootConfigIsTransparentWhenDisabled(t *testing.T) {
	enableMultiTenantForTest(t, false)

	own := newTenantOwnership(map[string]int64{"dev-b": 8})
	store := newFakeConfigStore()
	if err := store.SaveChatwootDeviceConfig(&domainChatStorage.ChatwootDeviceConfig{
		DeviceID: "dev-b", ChatwootURL: "https://203.0.113.10", AccountID: 1, InboxID: 5,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newChatwootConfigTenantApp(t, tenantOperator(7), own, store)

	if res, body := doJSON(t, app, http.MethodGet, "/devices/dev-b/chatwoot/config", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.StatusCode, body)
	}
}
