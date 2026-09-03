package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
)

// Test wiring untuk Register.
//
// Dua keputusan di route.go tidak kelihatan dari test yang memanggil
// PrincipalBridge atau resolveDeviceContext secara langsung, padahal keduanya
// menentukan apakah isolasi tenant jalan sama sekali:
//
//  1. PrincipalBridge dipasang DI DALAM Register, supaya kedua jalur mounting
//     — OAuth (sebelum gate global) dan Basic (di belakangnya) — otomatis
//     terlindungi tanpa pemanggil harus mengingatnya.
//  2. adaptor.HTTPHandlerWithContext, bukan adaptor.HTTPHandler. Hanya varian
//     itu yang menitipkan fiber user context ke request, dan itulah
//     satu-satunya jalan principal menyeberangi adaptor net/http menuju
//     handler tool.
//
// Keduanya "fail closed" kalau rusak: setiap panggilan tool ditolak, termasuk
// ke device milik pemanggil sendiri. Test di sini karena itu memeriksa jalur
// SUKSES lewat Register yang sesungguhnya — penolakan saja tidak membedakan
// "identitas hilang" dari "identitas ada tapi tidak berhak", karena keduanya
// menjawab dengan pesan yang memang sengaja dibuat identik.

// fakeAppUsecase mencatat device yang benar-benar dipakai tool.
type fakeAppUsecase struct{ statusDeviceID string }

func (f *fakeAppUsecase) Status(_ context.Context, deviceID string) (bool, bool, error) {
	f.statusDeviceID = deviceID
	return true, true, nil
}

func (f *fakeAppUsecase) Login(context.Context, string) (domainApp.LoginResponse, error) {
	return domainApp.LoginResponse{}, nil
}
func (f *fakeAppUsecase) LoginWithCode(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeAppUsecase) PasskeyChallenge(context.Context, string) (domainApp.PasskeyChallengeResponse, error) {
	return domainApp.PasskeyChallengeResponse{}, nil
}
func (f *fakeAppUsecase) PasskeyResponse(context.Context, string, *types.WebAuthnResponse) error {
	return nil
}
func (f *fakeAppUsecase) PasskeyConfirm(context.Context, string) error { return nil }
func (f *fakeAppUsecase) Logout(context.Context, string) error         { return nil }
func (f *fakeAppUsecase) Reconnect(context.Context, string) error      { return nil }
func (f *fakeAppUsecase) FirstDevice(context.Context) (domainApp.DevicesResponse, error) {
	return domainApp.DevicesResponse{}, nil
}
func (f *fakeAppUsecase) FetchDevices(context.Context) ([]domainApp.DevicesResponse, error) {
	return nil, nil
}

var _ domainApp.IAppUsecase = (*fakeAppUsecase)(nil)

// twoSlotManager membangun DeviceManager sungguhan berisi dua slot yang belum
// dipasangkan. Tidak butuh whatsmeow: slot tanpa client sudah cukup untuk
// ResolveDevice, dan itulah yang dipakai jalur guard.
func twoSlotManager() *whatsapp.DeviceManager {
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance("dev-a", nil, nil))
	dm.AddDevice(whatsapp.NewDeviceInstance("dev-b", nil, nil))
	return dm
}

// callToolViaRegister menjalankan satu tools/call melewati rute yang
// didaftarkan Register, lalu mengembalikan teks hasil atau teks error tool-nya.
func callToolViaRegister(t *testing.T, app *fiber.App, deviceID string) (int, string) {
	t.Helper()
	return callToolViaRegisterWithHeader(t, app, deviceID, "")
}

// callToolViaRegisterWithHeader sama, tapi bisa mengirim X-Device-Id. deviceID
// kosong berarti argumen device_id tidak disertakan sama sekali.
func callToolViaRegisterWithHeader(t *testing.T, app *fiber.App, deviceID, headerDeviceID string) (int, string) {
	t.Helper()

	args := `{"action":"status"}`
	if deviceID != "" {
		args = `{"action":"status","device_id":"` + deviceID + `"}`
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatsapp_app","arguments":` + args + `}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if headerDeviceID != "" {
		req.Header.Set(middleware.DeviceIDHeader, headerDeviceID)
	}

	res, err := app.Test(req)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	if res.StatusCode != 200 {
		return res.StatusCode, string(raw)
	}

	// streamable HTTP boleh menjawab sebagai JSON polos atau satu event SSE.
	payload := string(raw)
	if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
		for _, line := range strings.Split(payload, "\n") {
			if strings.HasPrefix(line, "data:") {
				payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				break
			}
		}
	}

	var decoded struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded), "body: %s", string(raw))
	require.NotEmpty(t, decoded.Result.Content, "body: %s", string(raw))
	return res.StatusCode, decoded.Result.Content[0].Text
}

// registeredApp memasang before (peniru jalur auth) lalu Register yang asli.
func registeredApp(before fiber.Handler, deps Deps) *fiber.App {
	app := fiber.New()
	if before != nil {
		app.Use(before)
	}
	Register(app, twoSlotManager(), deps)
	return app
}

// TestRegisterWiresTenantGuardOnBothMountPaths mengunci kedua keputusan wiring
// sekaligus. Mengganti HTTPHandlerWithContext dengan HTTPHandler, atau
// menghapus router.Use("/mcp", PrincipalBridge(...)) dari Register, membuat
// sub-test "device sendiri" gagal — bukan cuma kehilangan cakupan.
func TestRegisterWiresTenantGuardOnBothMountPaths(t *testing.T) {
	resolver := newFakeResolver()
	resolver.byUsername["operator1"] = operatorPrincipal(7)

	cases := []struct {
		name   string
		before fiber.Handler
	}{
		{
			// Mode Basic: MCP dipasang di apiGroup, di belakang AuthGate,
			// jadi principal sudah ada di fiber Locals.
			name: "jalur Basic lewat AuthGate",
			before: func(c fiber.Ctx) error {
				middleware.StorePrincipal(c, operatorPrincipal(7))
				return c.Next()
			},
		},
		{
			// Mode OAuth: rute /mcp sengaja didaftarkan sebelum gate global
			// supaya discovery tetap publik, jadi AuthGate tidak berjalan dan
			// identitas hanya ada di oauth_subject.
			name: "jalur OAuth lewat oauth_subject",
			before: func(c fiber.Ctx) error {
				c.Locals("oauth_subject", "operator1")
				return c.Next()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			own := newFakeTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
			// Global dibiarkan nil: ownership HARUS datang dari Deps lewat
			// Register. Kalau di-set di sini, test ikut lolos meski Register
			// berhenti menyalinnya — dan guard-nya diam-diam mati.
			withTenantState(t, true, nil)

			appUC := &fakeAppUsecase{}
			app := registeredApp(tc.before, Deps{App: appUC, Tenancy: resolver, Ownership: own})

			// Device sendiri HARUS jalan. Inilah yang membuktikan principal
			// benar-benar sampai ke handler tool: kalau bridge tidak terpasang
			// atau varian adaptornya salah, panggilan ini ikut ditolak.
			status, text := callToolViaRegister(t, app, "dev-a")
			assert.Equal(t, 200, status)
			assert.Equal(t, "connected=true logged_in=true", text)
			assert.Equal(t, "dev-a", appUC.statusDeviceID, "tool harus dijalankan pada device pemanggil")

			// Device tenant lain ditolak, dengan pesan yang sama persis
			// dengan device yang tidak ada.
			appUC.statusDeviceID = ""
			_, text = callToolViaRegister(t, app, "dev-b")
			assert.Equal(t, "device dev-b not found", text)
			_, ghost := callToolViaRegister(t, app, "dev-hantu")
			assert.Equal(t, "device dev-hantu not found", ghost)
			assert.Empty(t, appUC.statusDeviceID, "usecase tidak boleh tersentuh untuk device orang lain")
		})
	}
}

// TestRegisterIgnoresForeignDeviceHeader mengunci guard header di route.go.
//
// Ini satu-satunya penjaga untuk X-Device-Id: begitu sebuah device masuk ke
// context lewat WithHTTPContextFunc, cabang kedua resolveDeviceContext
// memakainya apa adanya dan sengaja TIDAK memeriksa ulang kepemilikan. Kalau
// guard di route.go hilang, header device orang lain langsung terpakai dan
// tidak ada test lain yang menyadarinya.
func TestRegisterIgnoresForeignDeviceHeader(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	withTenantState(t, true, nil)

	appUC := &fakeAppUsecase{}
	app := registeredApp(func(c fiber.Ctx) error {
		middleware.StorePrincipal(c, operatorPrincipal(7))
		return c.Next()
	}, Deps{App: appUC, Tenancy: newFakeResolver(), Ownership: own})

	// Header menunjuk device tenant lain, tanpa argumen device_id. Pemanggil
	// punya tepat satu device, jadi yang dipakai harus device itu — bukan yang
	// disebut header.
	status, text := callToolViaRegisterWithHeader(t, app, "", "dev-b")
	assert.Equal(t, 200, status)
	assert.Equal(t, "connected=true logged_in=true", text)
	assert.Equal(t, "dev-a", appUC.statusDeviceID, "header device orang lain tidak boleh terpakai")

	// Header ke device sendiri tetap berfungsi seperti biasa.
	appUC.statusDeviceID = ""
	_, text = callToolViaRegisterWithHeader(t, app, "", "dev-a")
	assert.Equal(t, "connected=true logged_in=true", text)
	assert.Equal(t, "dev-a", appUC.statusDeviceID)
}

// TestRegisterRejectsMCPWithoutIdentity: di mode OAuth, permintaan yang lolos
// sampai Register tanpa identitas sama sekali harus berhenti di bridge, bukan
// diteruskan ke tool.
func TestRegisterRejectsMCPWithoutIdentity(t *testing.T) {
	own := newFakeTenantOwnership(map[string]int64{"dev-a": 7})
	withTenantState(t, true, nil)

	appUC := &fakeAppUsecase{}
	app := registeredApp(nil, Deps{App: appUC, Tenancy: newFakeResolver(), Ownership: own})

	status, _ := callToolViaRegister(t, app, "dev-a")
	assert.Equal(t, fiber.StatusUnauthorized, status)
	assert.Empty(t, appUC.statusDeviceID)
}

// TestRegisterIsTransparentWhenDisabled adalah jaminan zero-regression: dengan
// flag mati, Register tidak boleh memasang apa pun — termasuk bridge yang akan
// menolak permintaan tanpa principal.
func TestRegisterIsTransparentWhenDisabled(t *testing.T) {
	withTenantState(t, false, nil)

	appUC := &fakeAppUsecase{}
	app := registeredApp(nil, Deps{App: appUC})

	// Tanpa identitas apa pun, dan device mana pun boleh — persis perilaku
	// single-tenant sebelum fase 07.
	status, text := callToolViaRegister(t, app, "dev-b")
	assert.Equal(t, 200, status)
	assert.Equal(t, "connected=true logged_in=true", text)
	assert.Equal(t, "dev-b", appUC.statusDeviceID)
}

// TestRegisterKeepsOwnershipNilWhenFeatureOff menjaga agar guard tetap
// transparan saat Deps tidak membawa ownership.
func TestRegisterKeepsOwnershipNilWhenFeatureOff(t *testing.T) {
	withTenantState(t, true, newFakeTenantOwnership(nil))

	registeredApp(nil, Deps{App: &fakeAppUsecase{}})

	assert.Nil(t, ownership, "Register harus menyalin Deps.Ownership apa adanya")
	assert.Nil(t, domainTenancy.PrincipalFromContext(context.Background()))
}
