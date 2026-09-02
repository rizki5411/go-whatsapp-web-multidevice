package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
)

// ownershipStore melengkapi fakeTenancyRepo dengan penyimpanan kepemilikan
// device, dan mencatat jumlah pembacaannya supaya perilaku cache bisa diamati.
type ownershipStore struct {
	*fakeTenancyRepo
	owners map[string]int64

	getOwnerCalls int
}

func newOwnershipStore() *ownershipStore {
	return &ownershipStore{fakeTenancyRepo: newFakeTenancyRepo(), owners: map[string]int64{}}
}

func (o *ownershipStore) SetDeviceOwner(deviceID string, userID int64) error {
	o.owners[deviceID] = userID
	return nil
}

func (o *ownershipStore) GetDeviceOwner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	o.getOwnerCalls++
	userID, ok := o.owners[deviceID]
	if !ok {
		return nil, nil
	}
	return &domainTenancy.DeviceOwner{DeviceID: deviceID, UserID: userID}, nil
}

func (o *ownershipStore) ListDeviceIDsByOwner(userID int64) ([]string, error) {
	// Urutan stabil supaya pemilihan device default deterministik, seperti
	// repository sungguhan yang mengurutkan created_at lalu device_id.
	var ids []string
	for _, candidate := range []string{"dev-a", "dev-a2", "dev-a3", "dev-b"} {
		if o.owners[candidate] == userID && userID != 0 {
			ids = append(ids, candidate)
		}
	}
	return ids, nil
}

func (o *ownershipStore) CountDevicesByOwner(userID int64) (int, error) {
	count := 0
	for _, owner := range o.owners {
		if owner == userID {
			count++
		}
	}
	return count, nil
}

func (o *ownershipStore) DeleteDeviceOwner(deviceID string) error {
	delete(o.owners, deviceID)
	return nil
}

func (o *ownershipStore) DeleteDeviceOwnersByUser(userID int64) error {
	for deviceID, owner := range o.owners {
		if owner == userID {
			delete(o.owners, deviceID)
		}
	}
	return nil
}

var _ domainTenancy.ITenancyRepository = (*ownershipStore)(nil)

// withMultiTenantUsecase menyalakan flag untuk satu kasus uji. config adalah
// variabel global paket, jadi tanpa pemulihan ini kasus uji akan saling bocor.
func withMultiTenantUsecase(t *testing.T, enabled bool) {
	t.Helper()
	previous := config.MultiTenantEnabled
	config.MultiTenantEnabled = enabled
	t.Cleanup(func() { config.MultiTenantEnabled = previous })
}

func newOwnershipServiceForTest(t *testing.T) (domainTenancy.IDeviceOwnership, *ownershipStore) {
	t.Helper()
	store := newOwnershipStore()
	return NewDeviceOwnershipService(store), store
}

func seedUser(t *testing.T, store *ownershipStore, username string, role domainTenancy.Role, limit int) *domainTenancy.User {
	t.Helper()
	user := &domainTenancy.User{
		Username:    username,
		Role:        role,
		DeviceLimit: limit,
		Active:      true,
	}
	if _, err := store.CreateUser(user); err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	return user
}

// _____________________________________________________________________________
// CanAccess

func TestCanAccessRules(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	operator := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	other := seedUser(t, store, "operator2", domainTenancy.RoleOperator, 0)
	adminUser := seedUser(t, store, "admin1", domainTenancy.RoleAdmin, 0)

	store.owners["dev-a"] = operator.ID
	store.owners["dev-b"] = other.ID

	principalFor := func(u *domainTenancy.User) *domainTenancy.Principal {
		return &domainTenancy.Principal{UserID: u.ID, Username: u.Username, Role: u.Role}
	}

	cases := []struct {
		name      string
		principal *domainTenancy.Principal
		deviceID  string
		want      bool
	}{
		{"pemilik", principalFor(operator), "dev-a", true},
		{"bukan pemilik", principalFor(operator), "dev-b", false},
		{"admin ke device orang lain", principalFor(adminUser), "dev-b", true},
		{"admin ke device tak-ber-owner", principalFor(adminUser), "dev-orphan", true},
		{"operator ke device tak-ber-owner", principalFor(operator), "dev-orphan", false},
		{"tanpa principal", nil, "dev-a", false},
		{"device id kosong", principalFor(operator), "", false},
		{
			"break-glass non-admin",
			&domainTenancy.Principal{UserID: 0, Username: "darurat", Role: domainTenancy.RoleOperator},
			"dev-a", false,
		},
		{
			"break-glass admin",
			&domainTenancy.Principal{UserID: 0, Username: "darurat", Role: domainTenancy.RoleAdmin, ViaBreakGlass: true},
			"dev-a", true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := own.CanAccess(tc.principal, tc.deviceID); got != tc.want {
				t.Fatalf("CanAccess() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCanAccessIsTransparentWhenDisabled: jaminan zero-regression.
func TestCanAccessIsTransparentWhenDisabled(t *testing.T) {
	withMultiTenantUsecase(t, false)

	own, _ := newOwnershipServiceForTest(t)
	if !own.CanAccess(nil, "dev-apa-saja") {
		t.Fatal("dengan flag mati semua akses harus diizinkan")
	}
}

// TestCanAccessUsesCacheButHonoursInvalidation: cache-nya wajib pakai
// invalidasi eksplisit, bukan TTL — jendela stale pada kontrol akses berarti
// device yang baru dipindahkan masih bisa diakses pemilik lama.
func TestCanAccessUsesCacheButHonoursInvalidation(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	first := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	second := seedUser(t, store, "operator2", domainTenancy.RoleOperator, 0)
	store.owners["dev-a"] = first.ID

	p1 := &domainTenancy.Principal{UserID: first.ID, Role: domainTenancy.RoleOperator}
	p2 := &domainTenancy.Principal{UserID: second.ID, Role: domainTenancy.RoleOperator}

	if !own.CanAccess(p1, "dev-a") {
		t.Fatal("pemilik harus boleh")
	}
	callsAfterFirst := store.getOwnerCalls

	// Pemanggilan kedua harus dilayani cache.
	if !own.CanAccess(p1, "dev-a") {
		t.Fatal("pemilik harus boleh")
	}
	if store.getOwnerCalls != callsAfterFirst {
		t.Fatalf("pembacaan kedua tidak memakai cache: %d -> %d", callsAfterFirst, store.getOwnerCalls)
	}

	// Pindahkan pemilik: efeknya harus SEKETIKA, bukan setelah TTL.
	if err := own.Assign("dev-a", second.ID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if own.CanAccess(p1, "dev-a") {
		t.Fatal("pemilik lama masih punya akses setelah device dipindahkan")
	}
	if !own.CanAccess(p2, "dev-a") {
		t.Fatal("pemilik baru harus langsung punya akses")
	}
}

func TestCanAccessInvalidatesOnClaimAndRelease(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	p := &domainTenancy.Principal{UserID: user.ID, Role: domainTenancy.RoleOperator}

	// Belum di-klaim: ditolak, dan hasil itu ikut ter-cache.
	if own.CanAccess(p, "dev-a") {
		t.Fatal("device belum di-klaim tidak boleh bisa diakses operator")
	}

	if err := own.Claim(p, "dev-a"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !own.CanAccess(p, "dev-a") {
		t.Fatal("setelah klaim harus langsung bisa diakses — cache tidak ter-invalidasi")
	}

	if err := own.Release("dev-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if own.CanAccess(p, "dev-a") {
		t.Fatal("setelah dilepas tidak boleh bisa diakses lagi")
	}
}

// _____________________________________________________________________________
// Claim

// TestClaimRejectsBreakGlassPrincipal: principal break-glass tidak punya baris
// app_user, jadi tidak ada user_id sah untuk dicatat. Menyimpannya dengan 0
// akan membuat device dimiliki identitas yang tidak ada.
func TestClaimRejectsBreakGlassPrincipal(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	p := &domainTenancy.Principal{UserID: 0, Username: "darurat", Role: domainTenancy.RoleAdmin, ViaBreakGlass: true}

	err := own.Claim(p, "dev-a")
	if !errors.Is(err, domainTenancy.ErrBreakGlassCannotOwn) {
		t.Fatalf("err = %v, want ErrBreakGlassCannotOwn", err)
	}
	if len(store.owners) != 0 {
		t.Fatalf("tidak boleh ada baris kepemilikan yang tersimpan: %v", store.owners)
	}
}

func TestClaimRejectsEmptyInput(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	p := &domainTenancy.Principal{UserID: user.ID, Role: domainTenancy.RoleOperator}

	if err := own.Claim(p, "  "); !errors.Is(err, domainTenancy.ErrDeviceIDRequired) {
		t.Fatalf("err = %v, want ErrDeviceIDRequired", err)
	}
	if err := own.Claim(nil, "dev-a"); !errors.Is(err, domainTenancy.ErrUserRequired) {
		t.Fatalf("err = %v, want ErrUserRequired", err)
	}
}

// _____________________________________________________________________________
// Kuota

func TestEnsureQuotaRespectsDeviceLimit(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 2)
	p := &domainTenancy.Principal{UserID: user.ID, Role: domainTenancy.RoleOperator}

	if err := own.EnsureQuota(p); err != nil {
		t.Fatalf("kuota kosong harus lolos: %v", err)
	}

	store.owners["dev-a"] = user.ID
	if err := own.EnsureQuota(p); err != nil {
		t.Fatalf("1 dari 2 harus lolos: %v", err)
	}

	store.owners["dev-a2"] = user.ID
	err := own.EnsureQuota(p)
	if !errors.Is(err, domainTenancy.ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached", err)
	}
	// Pesannya harus menyebut angka, supaya operator tahu batasnya berapa.
	if got := err.Error(); !strings.Contains(got, "2 dari 2") {
		t.Fatalf("pesan error harus menyebut pemakaian: %q", got)
	}
}

func TestEnsureQuotaUnlimitedWhenZero(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	p := &domainTenancy.Principal{UserID: user.ID, Role: domainTenancy.RoleOperator}

	for _, deviceID := range []string{"dev-a", "dev-a2", "dev-a3"} {
		store.owners[deviceID] = user.ID
	}
	if err := own.EnsureQuota(p); err != nil {
		t.Fatalf("limit 0 berarti tanpa batas: %v", err)
	}
}

func TestEnsureQuotaSkipsAdminAndBreakGlass(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	adminUser := seedUser(t, store, "admin1", domainTenancy.RoleAdmin, 1)
	store.owners["dev-a"] = adminUser.ID

	if err := own.EnsureQuota(&domainTenancy.Principal{UserID: adminUser.ID, Role: domainTenancy.RoleAdmin}); err != nil {
		t.Fatalf("admin tidak dibatasi kuota: %v", err)
	}
	// Break-glass memang tidak bisa memiliki device sama sekali; Claim yang
	// menolaknya, bukan kuota.
	if err := own.EnsureQuota(&domainTenancy.Principal{UserID: 0, Role: domainTenancy.RoleAdmin, ViaBreakGlass: true}); err != nil {
		t.Fatalf("break-glass tidak boleh tertolak oleh kuota: %v", err)
	}
}

// _____________________________________________________________________________
// Assign

func TestAssignValidatesTargetUser(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	active := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	inactive := seedUser(t, store, "operator2", domainTenancy.RoleOperator, 0)
	stored, _ := store.GetUserByID(inactive.ID)
	stored.Active = false
	if err := store.UpdateUser(stored); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	if err := own.Assign("dev-a", active.ID); err != nil {
		t.Fatalf("assign ke user aktif: %v", err)
	}
	if store.owners["dev-a"] != active.ID {
		t.Fatalf("owners = %v", store.owners)
	}

	if err := own.Assign("dev-b", 9999); !errors.Is(err, domainTenancy.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	if err := own.Assign("dev-b", inactive.ID); err == nil {
		t.Fatal("user nonaktif tidak boleh menerima device")
	}
	if err := own.Assign("dev-b", 0); !errors.Is(err, domainTenancy.ErrUserRequired) {
		t.Fatalf("err = %v, want ErrUserRequired", err)
	}
	if err := own.Assign("", active.ID); !errors.Is(err, domainTenancy.ErrDeviceIDRequired) {
		t.Fatalf("err = %v, want ErrDeviceIDRequired", err)
	}
}

func TestAssignEnforcesQuotaOnTarget(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 1)
	store.owners["dev-a"] = user.ID

	if err := own.Assign("dev-b", user.ID); !errors.Is(err, domainTenancy.ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached", err)
	}

	// Menetapkan ulang pemilik yang SAMA tidak boleh tertolak kuota: tidak ada
	// device baru yang ditambahkan.
	if err := own.Assign("dev-a", user.ID); err != nil {
		t.Fatalf("menetapkan ulang pemilik yang sama harus lolos: %v", err)
	}
}

// TestOwnedDeviceIDsIsScopedAndFlagsAdmin: nilai all=true berarti "tidak perlu
// difilter", dan itu yang dipakai penyaring daftar device.
func TestOwnedDeviceIDsIsScopedAndFlagsAdmin(t *testing.T) {
	withMultiTenantUsecase(t, true)

	own, store := newOwnershipServiceForTest(t)
	first := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	second := seedUser(t, store, "operator2", domainTenancy.RoleOperator, 0)
	adminUser := seedUser(t, store, "admin1", domainTenancy.RoleAdmin, 0)

	store.owners["dev-a"] = first.ID
	store.owners["dev-a2"] = first.ID
	store.owners["dev-b"] = second.ID

	ids, all := own.OwnedDeviceIDs(&domainTenancy.Principal{UserID: first.ID, Role: domainTenancy.RoleOperator})
	if all {
		t.Fatal("operator harus difilter")
	}
	if len(ids) != 2 || ids[0] != "dev-a" || ids[1] != "dev-a2" {
		t.Fatalf("ids = %v, want [dev-a dev-a2] dengan urutan stabil", ids)
	}

	if _, all := own.OwnedDeviceIDs(&domainTenancy.Principal{UserID: adminUser.ID, Role: domainTenancy.RoleAdmin}); !all {
		t.Fatal("admin harus melewati filter")
	}
	if _, all := own.OwnedDeviceIDs(nil); all {
		t.Fatal("tanpa principal tidak boleh melewati filter")
	}
}

func TestOwnerReturnsNilForUnclaimed(t *testing.T) {
	own, store := newOwnershipServiceForTest(t)
	user := seedUser(t, store, "operator1", domainTenancy.RoleOperator, 0)
	store.owners["dev-a"] = user.ID

	owner, err := own.Owner("dev-a")
	if err != nil || owner == nil || owner.UserID != user.ID {
		t.Fatalf("owner = %+v (err %v)", owner, err)
	}

	owner, err = own.Owner("dev-orphan")
	if err != nil || owner != nil {
		t.Fatalf("device tak-ber-owner harus (nil, nil): %+v (err %v)", owner, err)
	}
}
