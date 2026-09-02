package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTenantOwnership adalah IDeviceOwnership yang dikendalikan test.
type fakeTenantOwnership struct {
	owners map[string]int64
}

func newFakeTenantOwnership(owners map[string]int64) *fakeTenantOwnership {
	if owners == nil {
		owners = map[string]int64{}
	}
	return &fakeTenantOwnership{owners: owners}
}

func (f *fakeTenantOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
	if p == nil {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	owner, ok := f.owners[deviceID]
	return ok && p.UserID != 0 && owner == p.UserID
}

func (f *fakeTenantOwnership) OwnedDeviceIDs(p *domainTenancy.Principal) ([]string, bool) {
	if p == nil {
		return nil, false
	}
	if p.IsAdmin() {
		return nil, true
	}
	var ids []string
	for _, candidate := range []string{"dev-a", "dev-a2", "dev-b"} {
		if owner, ok := f.owners[candidate]; ok && owner == p.UserID {
			ids = append(ids, candidate)
		}
	}
	return ids, false
}

func (f *fakeTenantOwnership) Claim(p *domainTenancy.Principal, deviceID string) error {
	f.owners[deviceID] = p.UserID
	return nil
}
func (f *fakeTenantOwnership) Release(deviceID string) error {
	delete(f.owners, deviceID)
	return nil
}
func (f *fakeTenantOwnership) Assign(deviceID string, userID int64) error {
	f.owners[deviceID] = userID
	return nil
}
func (f *fakeTenantOwnership) Owner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	if owner, ok := f.owners[deviceID]; ok {
		return &domainTenancy.DeviceOwner{DeviceID: deviceID, UserID: owner}, nil
	}
	return nil, nil
}
func (f *fakeTenantOwnership) EnsureQuota(*domainTenancy.Principal) error { return nil }

func (f *fakeTenantOwnership) DeviceCountByUser(userID int64) (int, error) {
	count := 0
	for _, owner := range f.owners {
		if owner == userID {
			count++
		}
	}
	return count, nil
}

var _ domainTenancy.IDeviceOwnership = (*fakeTenantOwnership)(nil)

// fakeResolver adalah PrincipalResolver yang dikendalikan test.
type fakeResolver struct {
	byBasic    map[string]*domainTenancy.Principal
	byUsername map[string]*domainTenancy.Principal
	err        error
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{
		byBasic:    map[string]*domainTenancy.Principal{},
		byUsername: map[string]*domainTenancy.Principal{},
	}
}

func (f *fakeResolver) ResolveBasic(_ context.Context, username, password string) (*domainTenancy.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byBasic[username+":"+password], nil
}

func (f *fakeResolver) ResolvePrincipalByUsername(_ context.Context, username string) (*domainTenancy.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byUsername[username], nil
}

var _ PrincipalResolver = (*fakeResolver)(nil)

// withTenantState menyetel flag dan ownership paket untuk satu kasus uji.
func withTenantState(t *testing.T, enabled bool, own domainTenancy.IDeviceOwnership) {
	t.Helper()
	prevFlag := config.MultiTenantEnabled
	prevOwn := ownership
	config.MultiTenantEnabled = enabled
	ownership = own
	t.Cleanup(func() {
		config.MultiTenantEnabled = prevFlag
		ownership = prevOwn
	})
}

func operatorPrincipal(userID int64) *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: userID, Username: "operator", Role: domainTenancy.RoleOperator}
}

func adminPrincipal() *domainTenancy.Principal {
	return &domainTenancy.Principal{UserID: 1, Username: "admin", Role: domainTenancy.RoleAdmin}
}

// _____________________________________________________________________________
// PrincipalBridge

// newBridgeApp memasang bridge lalu sebuah handler yang melaporkan principal
// yang berhasil sampai ke context — itulah yang dibaca handler tool nanti.
func newBridgeApp(resolver PrincipalResolver, before fiber.Handler) *fiber.App {
	app := fiber.New()
	if before != nil {
		app.Use(before)
	}
	app.Use("/mcp", PrincipalBridge(resolver))
	app.Post("/mcp", func(c fiber.Ctx) error {
		principal := domainTenancy.PrincipalFromContext(c.Context())
		if principal == nil {
			return c.SendString("no-principal")
		}
		return c.SendString("principal:" + principal.Username)
	})
	return app
}

func postMCP(t *testing.T, app *fiber.App, header map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := app.Test(req)
	require.NoError(t, err)
	defer res.Body.Close()
	buf := make([]byte, 256)
	n, _ := res.Body.Read(buf)
	return res.StatusCode, string(buf[:n])
}

func TestPrincipalBridgeUsesGatePrincipal(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	// Mode Basic: AuthGate sudah menaruh principal di Locals.
	app := newBridgeApp(newFakeResolver(), func(c fiber.Ctx) error {
		middleware.StorePrincipal(c, operatorPrincipal(7))
		return c.Next()
	})

	status, body := postMCP(t, app, nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "principal:operator", body)
}

// TestPrincipalBridgeResolvesOAuthSubject: di mode OAuth, AuthGate tidak
// berjalan karena rute /mcp sengaja didaftarkan sebelum gate global.
func TestPrincipalBridgeResolvesOAuthSubject(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	resolver := newFakeResolver()
	resolver.byUsername["operator1"] = operatorPrincipal(7)

	app := newBridgeApp(resolver, func(c fiber.Ctx) error {
		c.Locals("oauth_subject", "operator1")
		return c.Next()
	})

	status, body := postMCP(t, app, nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "principal:operator", body)
}

// TestPrincipalBridgeRejectsDeactivatedOAuthSubject: token bisa terbit sebelum
// user dinonaktifkan, dan token yang masih valid secara kriptografis tidak
// boleh mengalahkan status akun.
func TestPrincipalBridgeRejectsDeactivatedOAuthSubject(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	// Resolver mengembalikan nil untuk user nonaktif.
	app := newBridgeApp(newFakeResolver(), func(c fiber.Ctx) error {
		c.Locals("oauth_subject", "operator1")
		return c.Next()
	})

	status, _ := postMCP(t, app, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// TestPrincipalBridgeResolvesBasicInOAuthMode: middleware OAuth memvalidasi
// kredensial Basic tapi tidak menyetel oauth_subject, jadi header-nya harus
// dibaca ulang.
func TestPrincipalBridgeResolvesBasicInOAuthMode(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	resolver := newFakeResolver()
	resolver.byBasic["operator1:rahasia123"] = operatorPrincipal(7)

	app := newBridgeApp(resolver, nil)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("operator1:rahasia123"))

	status, body := postMCP(t, app, map[string]string{fiber.HeaderAuthorization: auth})
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "principal:operator", body)
}

func TestPrincipalBridgeRejectsWithoutIdentity(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	status, _ := postMCP(t, newBridgeApp(newFakeResolver(), nil), nil)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// TestPrincipalBridgeIsTransparentWhenDisabled adalah jaminan zero-regression.
func TestPrincipalBridgeIsTransparentWhenDisabled(t *testing.T) {
	withTenantState(t, false, nil)

	status, body := postMCP(t, newBridgeApp(newFakeResolver(), nil), nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "no-principal", body)
}

// _____________________________________________________________________________
// Guard device pada resolveDeviceContext

func TestResolveDeviceContextEnforcesOwnership(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	withTenantState(t, true, own)

	inst := &whatsapp.DeviceInstance{}
	resolver := &stubResolver{inst: inst}

	// Device sendiri: lolos.
	ctx := domainTenancy.ContextWithPrincipal(context.Background(), operatorPrincipal(7))
	_, got, err := resolveDeviceContext(ctx, callReq(map[string]any{"device_id": "dev-a"}), resolver)
	require.NoError(t, err)
	assert.Same(t, inst, got)

	// Device orang lain: ditolak, dan pesannya harus identik dengan device yang
	// benar-benar tidak ada.
	_, _, err = resolveDeviceContext(ctx, callReq(map[string]any{"device_id": "dev-b"}), resolver)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errDeviceNotFoundMCP), "err = %v", err)

	// Pesannya harus IDENTIK dengan device yang benar-benar tidak ada. Kalau
	// berbeda, bentuk pesan itu sendiri memberi tahu bahwa device tersebut
	// eksis tapi bukan milik pemanggil.
	_, _, missingErr := resolveDeviceContext(ctx, callReq(map[string]any{"device_id": "dev-hantu"}), &stubResolver{err: errors.New("device dev-hantu not found")})
	require.Error(t, missingErr)
	assert.Equal(t, "device dev-b not found", err.Error())
	assert.Equal(t, "device dev-hantu not found", missingErr.Error())
}

func TestResolveDeviceContextAllowsAdmin(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-b": 8})
	withTenantState(t, true, own)

	inst := &whatsapp.DeviceInstance{}
	ctx := domainTenancy.ContextWithPrincipal(context.Background(), adminPrincipal())

	_, got, err := resolveDeviceContext(ctx, callReq(map[string]any{"device_id": "dev-b"}), &stubResolver{inst: inst})
	require.NoError(t, err)
	assert.Same(t, inst, got)
}

func TestResolveDeviceContextRejectsWithoutPrincipal(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7})
	withTenantState(t, true, own)

	_, _, err := resolveDeviceContext(context.Background(), callReq(map[string]any{"device_id": "dev-a"}), &stubResolver{inst: &whatsapp.DeviceInstance{}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errDeviceNotFoundMCP))
}

// TestResolveDeviceContextPicksOwnSingleDevice: tool tanpa device_id memakai
// device milik pemanggil kalau pilihannya tunggal.
func TestResolveDeviceContextPicksOwnSingleDevice(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	withTenantState(t, true, own)

	inst := &whatsapp.DeviceInstance{}
	resolver := &stubResolver{inst: inst}
	ctx := domainTenancy.ContextWithPrincipal(context.Background(), operatorPrincipal(7))

	_, got, err := resolveDeviceContext(ctx, callReq(nil), resolver)
	require.NoError(t, err)
	assert.Same(t, inst, got)
	assert.Equal(t, "dev-a", resolver.gotID, "harus memakai device milik pemanggil")
}

func TestResolveDeviceContextRequiresExplicitWhenSeveralOwned(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7, "dev-a2": 7})
	withTenantState(t, true, own)

	ctx := domainTenancy.ContextWithPrincipal(context.Background(), operatorPrincipal(7))
	_, _, err := resolveDeviceContext(ctx, callReq(nil), &stubResolver{inst: &whatsapp.DeviceInstance{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device_id")
}

// TestResolveDeviceContextRejectsWhenNoOwnDevice: pemanggil tanpa device dan
// tanpa device_id mendapat pesan "identification required" yang sama dengan
// instalasi yang memang belum punya device — tidak ada device id untuk disebut,
// dan situasinya memang itu.
func TestResolveDeviceContextRejectsWhenNoOwnDevice(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-b": 8})
	withTenantState(t, true, own)

	ctx := domainTenancy.ContextWithPrincipal(context.Background(), operatorPrincipal(7))
	_, _, err := resolveDeviceContext(ctx, callReq(nil), &stubResolver{inst: &whatsapp.DeviceInstance{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device identification required")
}

// TestResolveDeviceContextIsTransparentWhenDisabled: dengan flag mati perilaku
// harus sama seperti sebelum fase ini — termasuk pesan errornya.
func TestResolveDeviceContextIsTransparentWhenDisabled(t *testing.T) {
	withTenantState(t, false, newFakeTenantOwnership(map[string]int64{"dev-b": 8}))

	inst := &whatsapp.DeviceInstance{}

	// Device orang lain tetap boleh.
	_, got, err := resolveDeviceContext(context.Background(), callReq(map[string]any{"device_id": "dev-b"}), &stubResolver{inst: inst})
	require.NoError(t, err)
	assert.Same(t, inst, got)

	// Tanpa device apa pun: pesan error lama dipertahankan.
	_, _, err = resolveDeviceContext(context.Background(), callReq(nil), &stubResolver{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device identification required")
}

// _____________________________________________________________________________
// Helper kepemilikan

func TestEnforceDeviceOwnershipIsTransparentWithoutOwnership(t *testing.T) {
	withTenantState(t, true, nil)

	// ownership nil berarti fitur belum terpasang; jangan menghalangi.
	require.NoError(t, enforceDeviceOwnership(context.Background(), "dev-apa-saja"))
}

func TestOwnDefaultDeviceIDSkipsAdmin(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(map[string]int64{"dev-b": 8}))

	ctx := domainTenancy.ContextWithPrincipal(context.Background(), adminPrincipal())
	id, err := ownDefaultDeviceID(ctx)
	require.NoError(t, err)
	assert.Empty(t, id, "admin memakai perilaku default lama")
}
