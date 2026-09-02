package websocket

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/gofiber/contrib/v3/websocket"
)

// fakeOwnership adalah IDeviceOwnership yang dikendalikan test.
type fakeOwnership struct {
	owners map[string]int64
}

func newFakeOwnership(owners map[string]int64) *fakeOwnership {
	if owners == nil {
		owners = map[string]int64{}
	}
	return &fakeOwnership{owners: owners}
}

func (f *fakeOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
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
	if p == nil {
		return nil, false
	}
	if p.IsAdmin() {
		return nil, true
	}
	var ids []string
	for _, candidate := range []string{"dev-a", "dev-b", "dev-orphan"} {
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
func (f *fakeOwnership) Release(deviceID string) error { delete(f.owners, deviceID); return nil }
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

// withHubState menyetel flag dan ownership paket untuk satu kasus uji, lalu
// memulihkannya. Keduanya state paket, jadi tanpa ini kasus uji saling bocor.
func withHubState(t *testing.T, enabled bool, own domainTenancy.IDeviceOwnership) {
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

func operatorClient(userID int64) client {
	return client{principal: &domainTenancy.Principal{UserID: userID, Role: domainTenancy.RoleOperator}}
}

func adminClient() client {
	return client{principal: &domainTenancy.Principal{UserID: 1, Role: domainTenancy.RoleAdmin}}
}

// _____________________________________________________________________________
// mayReceive

func TestMayReceiveRoutesEventsByOwnership(t *testing.T) {
	own := newFakeOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	withHubState(t, true, own)

	cases := []struct {
		name   string
		cl     client
		msg    BroadcastMessage
		expect bool
	}{
		{"pemilik menerima event device-nya", operatorClient(7), BroadcastMessage{DeviceID: "dev-a"}, true},
		{"bukan pemilik tidak menerima", operatorClient(7), BroadcastMessage{DeviceID: "dev-b"}, false},
		{"admin menerima semuanya", adminClient(), BroadcastMessage{DeviceID: "dev-b"}, true},
		{"device tak-ber-owner tidak ke operator", operatorClient(7), BroadcastMessage{DeviceID: "dev-orphan"}, false},
		{"device tak-ber-owner ke admin", adminClient(), BroadcastMessage{DeviceID: "dev-orphan"}, true},
		{"koneksi tanpa identitas tidak menerima", client{}, BroadcastMessage{DeviceID: "dev-a"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mayReceive(tc.cl, tc.msg); got != tc.expect {
				t.Fatalf("mayReceive() = %v, want %v", got, tc.expect)
			}
		})
	}
}

// TestMayReceiveSendsGlobalEventsToAdminOnly: broadcast tanpa DeviceID gagal ke
// arah aman. Itu yang membuat event baru dari upstream yang belum ditandai
// tidak bocor ke operator.
func TestMayReceiveSendsGlobalEventsToAdminOnly(t *testing.T) {
	own := newFakeOwnership(map[string]int64{"dev-a": 7})
	withHubState(t, true, own)

	global := BroadcastMessage{Code: "SESUATU_YANG_BARU", Message: "belum ditandai"}

	if mayReceive(operatorClient(7), global) {
		t.Fatal("operator tidak boleh menerima event global")
	}
	if !mayReceive(adminClient(), global) {
		t.Fatal("admin harus menerima event global")
	}
}

// TestMayReceiveIsTransparentWhenDisabled adalah jaminan zero-regression:
// dengan flag mati semua koneksi menerima semuanya, seperti sebelumnya.
func TestMayReceiveIsTransparentWhenDisabled(t *testing.T) {
	withHubState(t, false, newFakeOwnership(map[string]int64{"dev-b": 8}))

	for _, cl := range []client{operatorClient(7), client{}, adminClient()} {
		for _, msg := range []BroadcastMessage{{DeviceID: "dev-b"}, {}} {
			if !mayReceive(cl, msg) {
				t.Fatalf("flag mati: semua harus lolos (client %+v, msg %+v)", cl, msg)
			}
		}
	}
}

// TestMayReceiveFailsClosedWithoutOwnership: flag menyala tapi ownership belum
// terpasang berarti konfigurasi salah, dan itu harus menolak — bukan
// membocorkan.
func TestMayReceiveFailsClosedWithoutOwnership(t *testing.T) {
	withHubState(t, true, nil)

	if mayReceive(operatorClient(7), BroadcastMessage{DeviceID: "dev-a"}) {
		t.Fatal("tanpa ownership, operator tidak boleh menerima apa pun")
	}
	// Admin tetap boleh: keputusannya tidak bergantung pada data kepemilikan.
	if !mayReceive(adminClient(), BroadcastMessage{DeviceID: "dev-a"}) {
		t.Fatal("admin harus tetap menerima")
	}
}

// _____________________________________________________________________________
// forWire

// TestForWireKeepsSingleTenantPayloadIdentical: DeviceID adalah field baru, dan
// di mode single-tenant payload harus persis seperti sebelum fase ini.
func TestForWireKeepsSingleTenantPayloadIdentical(t *testing.T) {
	msg := BroadcastMessage{Code: "LOGIN_SUCCESS", Message: "ok", DeviceID: "dev-a"}

	withHubState(t, false, nil)
	off, err := json.Marshal(msg.forWire())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(off); got != `{"code":"LOGIN_SUCCESS","message":"ok","result":null}` {
		t.Fatalf("payload single-tenant berubah: %s", got)
	}

	withHubState(t, true, nil)
	on, err := json.Marshal(msg.forWire())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(on); got != `{"code":"LOGIN_SUCCESS","message":"ok","result":null,"device_id":"dev-a"}` {
		t.Fatalf("payload multi-tenant salah: %s", got)
	}
}

func TestForWireOmitsEmptyDeviceID(t *testing.T) {
	withHubState(t, true, nil)

	encoded, err := json.Marshal(BroadcastMessage{Code: "X", Message: "y"}.forWire())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); got != `{"code":"X","message":"y","result":null}` {
		t.Fatalf("device_id kosong harus dihilangkan: %s", got)
	}
}

// _____________________________________________________________________________
// FETCH_DEVICES

func TestFilterDevicesForPrincipal(t *testing.T) {
	own := newFakeOwnership(map[string]int64{"dev-a": 7, "dev-b": 8})
	withHubState(t, true, own)

	all := []domainApp.DevicesResponse{
		{Name: "A", Device: "dev-a", JID: "1@s.whatsapp.net"},
		{Name: "B", Device: "dev-b", JID: "2@s.whatsapp.net"},
		{Name: "O", Device: "dev-orphan"},
	}

	operatorView := filterDevicesForPrincipal(&domainTenancy.Principal{UserID: 7, Role: domainTenancy.RoleOperator}, all)
	if len(operatorView) != 1 || operatorView[0].Device != "dev-a" {
		t.Fatalf("operator melihat %+v, want hanya dev-a", operatorView)
	}

	adminView := filterDevicesForPrincipal(&domainTenancy.Principal{UserID: 1, Role: domainTenancy.RoleAdmin}, all)
	if len(adminView) != 3 {
		t.Fatalf("admin melihat %d device, want 3", len(adminView))
	}

	// Tanpa principal tidak ada device yang terlihat.
	if view := filterDevicesForPrincipal(nil, all); len(view) != 0 {
		t.Fatalf("tanpa principal harus kosong, dapat %+v", view)
	}

	// Slice input tidak boleh dimutasi.
	if len(all) != 3 || all[0].Device != "dev-a" || all[1].Device != "dev-b" {
		t.Fatalf("slice input termutasi: %+v", all)
	}
}

func TestFilterDevicesIsTransparentWhenDisabled(t *testing.T) {
	withHubState(t, false, newFakeOwnership(map[string]int64{"dev-b": 8}))

	all := []domainApp.DevicesResponse{{Device: "dev-a"}, {Device: "dev-b"}}
	if view := filterDevicesForPrincipal(nil, all); len(view) != 2 {
		t.Fatalf("flag mati: daftar tidak boleh disaring, dapat %+v", view)
	}
}

// _____________________________________________________________________________
// Registrasi

// TestHandleRegisterKeepsPrincipal memastikan identitas koneksi tersimpan,
// karena itulah satu-satunya dasar keputusan mayReceive.
func TestHandleRegisterKeepsPrincipal(t *testing.T) {
	// Clients adalah state paket; kosongkan dan pulihkan.
	prev := Clients
	Clients = make(map[*websocket.Conn]client)
	t.Cleanup(func() { Clients = prev })

	principal := &domainTenancy.Principal{UserID: 7, Username: "operator1", Role: domainTenancy.RoleOperator}
	handleRegister(registration{conn: nil, principal: principal})

	cl, ok := Clients[nil]
	if !ok {
		t.Fatal("koneksi tidak terdaftar")
	}
	if cl.principal == nil || cl.principal.UserID != 7 {
		t.Fatalf("principal tidak tersimpan: %+v", cl.principal)
	}

	handleUnregister(nil)
	if _, ok := Clients[nil]; ok {
		t.Fatal("koneksi tidak terhapus")
	}
}

// _____________________________________________________________________________
// Pencabutan

// withClients memasang isi Clients untuk satu kasus uji lalu memulihkannya.
func withClients(t *testing.T, entries map[*websocket.Conn]client) {
	t.Helper()
	prev := Clients
	Clients = entries
	t.Cleanup(func() { Clients = prev })
}

// TestConnsOfUserSelectsOnlyThatUser mengunci inti perbaikan: principal koneksi
// dibekukan saat upgrade, jadi pencabutan hak harus memutus koneksinya — dan
// HANYA koneksi milik user yang haknya berubah.
func TestConnsOfUserSelectsOnlyThatUser(t *testing.T) {
	korban1, korban2 := &websocket.Conn{}, &websocket.Conn{}
	lain, tanpaIdentitas := &websocket.Conn{}, &websocket.Conn{}

	withClients(t, map[*websocket.Conn]client{
		korban1:        {principal: &domainTenancy.Principal{UserID: 7, Role: domainTenancy.RoleOperator}},
		korban2:        {principal: &domainTenancy.Principal{UserID: 7, Role: domainTenancy.RoleAdmin}},
		lain:           {principal: &domainTenancy.Principal{UserID: 8, Role: domainTenancy.RoleOperator}},
		tanpaIdentitas: {},
	})

	targets := connsOfUser(7)
	if len(targets) != 2 {
		t.Fatalf("connsOfUser(7) mengembalikan %d koneksi, want 2", len(targets))
	}
	for _, conn := range targets {
		if conn == lain || conn == tanpaIdentitas {
			t.Fatal("koneksi milik user lain ikut terpilih")
		}
	}

	// UserID 0 adalah principal break-glass dan koneksi tanpa identitas; tidak
	// boleh ada yang terpilih atasnya, kalau tidak "hapus user" akan memutus
	// koneksi yang tidak terkait.
	if got := connsOfUser(0); len(got) != 0 {
		t.Fatalf("connsOfUser(0) mengembalikan %d koneksi, want 0", len(got))
	}
}

// TestRevokeUserPublishesToHub: titik panggil di usecase mengandalkan ini, dan
// pencabutan yang tidak pernah sampai ke hub gagal secara senyap.
func TestRevokeUserPublishesToHub(t *testing.T) {
	prev := Revoke
	Revoke = make(chan int64, 4)
	t.Cleanup(func() { Revoke = prev })

	RevokeUser(7)
	select {
	case got := <-Revoke:
		if got != 7 {
			t.Fatalf("menerima user %d, want 7", got)
		}
	default:
		t.Fatal("tidak ada pencabutan yang dikirim ke hub")
	}

	// Break-glass tidak punya baris app_user; mencabut atasnya akan memutus
	// koneksi yang tidak ada hubungannya.
	RevokeUser(0)
	select {
	case got := <-Revoke:
		t.Fatalf("user 0 tidak boleh dikirim, dapat %d", got)
	default:
	}
}

// TestRevokeUserDoesNotBlockWhenHubIsBusy: pemanggilnya adalah handler HTTP
// admin, dan hub bisa tertahan menulis ke koneksi yang lambat.
func TestRevokeUserDoesNotBlockWhenHubIsBusy(t *testing.T) {
	prev := Revoke
	Revoke = make(chan int64) // tanpa buffer, tanpa hub: pengiriman langsung pasti gagal
	t.Cleanup(func() { Revoke = prev })

	selesai := make(chan struct{})
	go func() {
		RevokeUser(7)
		close(selesai)
	}()

	select {
	case <-selesai:
	case <-time.After(2 * time.Second):
		t.Fatal("RevokeUser memblokir saat hub sibuk")
	}

	// Pesannya tidak boleh hilang: ia harus tetap tiba begitu hub siap.
	select {
	case got := <-Revoke:
		if got != 7 {
			t.Fatalf("menerima user %d, want 7", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pencabutan hilang, bukan tertunda")
	}
}

// _____________________________________________________________________________
// Daur ulang pointer koneksi

// TestWriteToRejectsRecycledConnection: contrib mengembalikan *websocket.Conn ke
// sync.Pool, jadi koneksi berikutnya bisa mewarisi pointer yang sama. Balasan
// yang masih menunggu di buffer Direct tidak boleh tertulis ke koneksi itu.
func TestWriteToRejectsRecycledConnection(t *testing.T) {
	conn := &websocket.Conn{}
	withClients(t, map[*websocket.Conn]client{
		// Registrasi BARU (seq 2) di atas pointer yang sama.
		conn: {principal: &domainTenancy.Principal{UserID: 8, Role: domainTenancy.RoleOperator}, seq: 2},
	})

	// Pesan milik registrasi LAMA (seq 1). Tanpa pemeriksaan seq, writeTo akan
	// mencoba menulisnya ke registrasi baru; penulisan itu gagal (Conn ini tidak
	// punya koneksi jaringan di baliknya) sehingga writeTo menutup koneksinya
	// dan membuangnya dari Clients. Entri yang masih utuh setelahnya adalah
	// bukti pesan itu memang tidak pernah dikirim.
	writeTo(conn, 1, BroadcastMessage{Code: "LIST_DEVICES"})

	cl, ok := Clients[conn]
	if !ok {
		t.Fatal("registrasi baru ikut tertutup oleh pesan milik registrasi lama")
	}
	if cl.principal == nil || cl.principal.UserID != 8 {
		t.Fatalf("registrasi baru berubah: %+v", cl.principal)
	}

	// Kontrol positif: dengan seq yang cocok, pesannya memang diproses —
	// penulisannya gagal dan koneksinya dibuang, jadi test di atas tidak lulus
	// hanya karena writeTo tidak pernah menulis apa pun.
	writeTo(conn, 2, BroadcastMessage{Code: "LIST_DEVICES"})
	if _, ok := Clients[conn]; ok {
		t.Fatal("seq yang cocok seharusnya diproses")
	}
}
