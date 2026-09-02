package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/authhash"
)

// fakeTenancyRepo adalah repository in-memory.
//
// Dipakai daripada SQLite sungguhan supaya test ini menguji aturan bisnis
// (validasi, invarian admin terakhir, pencabutan session) tanpa ikut menguji
// SQL — yang sudah punya test sendiri di infrastructure/chatstorage. Ia juga
// mencatat pemanggilan DeleteSessionsByUser, yang tidak bisa diamati dari luar
// kalau memakai DB nyata.
type fakeTenancyRepo struct {
	users  map[int64]*domainTenancy.User
	nextID int64

	// revokedSessions mencatat setiap DeleteSessionsByUser, berikut
	// urutannya, supaya test bisa memastikan pencabutan benar-benar terjadi.
	revokedSessions []int64

	failCountAdmins bool
	failRevoke      bool
}

func newFakeTenancyRepo() *fakeTenancyRepo {
	return &fakeTenancyRepo{users: map[int64]*domainTenancy.User{}, nextID: 1}
}

func (f *fakeTenancyRepo) CreateUser(user *domainTenancy.User) (int64, error) {
	username := domainTenancy.NormalizeUsername(user.Username)
	for _, existing := range f.users {
		if existing.Username == username {
			return 0, domainTenancy.ErrUsernameTaken
		}
	}
	user.Username = username
	user.ID = f.nextID
	f.nextID++
	user.CreatedAt = time.Now()
	user.UpdatedAt = user.CreatedAt

	stored := *user
	f.users[user.ID] = &stored
	return user.ID, nil
}

func (f *fakeTenancyRepo) UpdateUser(user *domainTenancy.User) error {
	if _, ok := f.users[user.ID]; !ok {
		return domainTenancy.ErrUserNotFound
	}
	username := domainTenancy.NormalizeUsername(user.Username)
	for id, existing := range f.users {
		if id != user.ID && existing.Username == username {
			return domainTenancy.ErrUsernameTaken
		}
	}
	user.Username = username
	user.UpdatedAt = time.Now()

	stored := *user
	f.users[user.ID] = &stored
	return nil
}

func (f *fakeTenancyRepo) GetUserByID(id int64) (*domainTenancy.User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, nil
	}
	clone := *user
	return &clone, nil
}

func (f *fakeTenancyRepo) GetUserByUsername(username string) (*domainTenancy.User, error) {
	normalized := domainTenancy.NormalizeUsername(username)
	for _, user := range f.users {
		if user.Username == normalized {
			clone := *user
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeTenancyRepo) ListUsers() ([]*domainTenancy.User, error) {
	users := make([]*domainTenancy.User, 0, len(f.users))
	for _, user := range f.users {
		clone := *user
		users = append(users, &clone)
	}
	return users, nil
}

func (f *fakeTenancyRepo) DeleteUser(id int64) error {
	if _, ok := f.users[id]; !ok {
		return domainTenancy.ErrUserNotFound
	}
	delete(f.users, id)
	return nil
}

func (f *fakeTenancyRepo) CountUsers() (int, error) { return len(f.users), nil }

func (f *fakeTenancyRepo) CountAdmins() (int, error) {
	if f.failCountAdmins {
		return 0, errors.New("boom")
	}
	count := 0
	for _, user := range f.users {
		if user.Role == domainTenancy.RoleAdmin && user.Active {
			count++
		}
	}
	return count, nil
}

func (f *fakeTenancyRepo) SetDeviceOwner(string, int64) error { return nil }
func (f *fakeTenancyRepo) GetDeviceOwner(string) (*domainTenancy.DeviceOwner, error) {
	return nil, nil
}
func (f *fakeTenancyRepo) ListDeviceIDsByOwner(int64) ([]string, error) { return nil, nil }
func (f *fakeTenancyRepo) CountDevicesByOwner(int64) (int, error)       { return 0, nil }
func (f *fakeTenancyRepo) DeleteDeviceOwner(string) error               { return nil }
func (f *fakeTenancyRepo) DeleteDeviceOwnersByUser(int64) error         { return nil }

func (f *fakeTenancyRepo) CreateSession(*domainTenancy.Session) error { return nil }
func (f *fakeTenancyRepo) GetSession(string) (*domainTenancy.Session, error) {
	return nil, nil
}
func (f *fakeTenancyRepo) DeleteSession(string) error { return nil }

func (f *fakeTenancyRepo) DeleteSessionsByUser(userID int64) error {
	if f.failRevoke {
		return errors.New("revoke failed")
	}
	f.revokedSessions = append(f.revokedSessions, userID)
	return nil
}

func (f *fakeTenancyRepo) DeleteExpiredSessions(time.Time) (int64, error) { return 0, nil }

// Memastikan fake benar-benar memenuhi kontrak; kalau interface berubah,
// kegagalannya muncul di sini, bukan sebagai error yang membingungkan.
var _ domainTenancy.ITenancyRepository = (*fakeTenancyRepo)(nil)

func newTenancyServiceForTest(t *testing.T) (domainTenancy.ITenancyUsecase, *fakeTenancyRepo) {
	t.Helper()
	repo := newFakeTenancyRepo()
	return NewTenancyService(repo), repo
}

func mustCreateUser(t *testing.T, svc domainTenancy.ITenancyUsecase, username string, role domainTenancy.Role) *domainTenancy.User {
	t.Helper()
	user, err := svc.CreateUser(context.Background(), domainTenancy.CreateUserInput{
		Username: username,
		Password: "rahasia123",
		Role:     role,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return user
}

// _____________________________________________________________________________
// Pembuatan dan validasi

func TestCreateUserHashesPasswordAndDefaultsToOperator(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	user, err := svc.CreateUser(context.Background(), domainTenancy.CreateUserInput{
		Username:    "  Operator1  ",
		Password:    "rahasia123",
		DisplayName: "  Operator Satu  ",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if user.Username != "operator1" {
		t.Fatalf("username = %q, want %q", user.Username, "operator1")
	}
	if user.DisplayName != "Operator Satu" {
		t.Fatalf("display name = %q", user.DisplayName)
	}
	// Role kosong harus jadi operator, bukan admin: default yang aman.
	if user.Role != domainTenancy.RoleOperator {
		t.Fatalf("role = %q, want operator", user.Role)
	}
	if !user.Active {
		t.Fatal("user baru harus aktif")
	}
	if user.PasswordHash == "rahasia123" || user.PasswordHash == "" {
		t.Fatal("password harus disimpan sebagai hash")
	}
	if !authhash.VerifyPassword(user.PasswordHash, "rahasia123") {
		t.Fatal("hash yang disimpan harus memverifikasi password aslinya")
	}
}

func TestCreateUserRejectsInvalidInput(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	cases := []struct {
		name string
		in   domainTenancy.CreateUserInput
		want error
	}{
		{"username terlalu pendek", domainTenancy.CreateUserInput{Username: "ab", Password: "rahasia123"}, domainTenancy.ErrUsernameInvalid},
		{"username dengan spasi", domainTenancy.CreateUserInput{Username: "oper ator", Password: "rahasia123"}, domainTenancy.ErrUsernameInvalid},
		{"username dengan karakter aneh", domainTenancy.CreateUserInput{Username: "oper@tor", Password: "rahasia123"}, domainTenancy.ErrUsernameInvalid},
		{"username kosong", domainTenancy.CreateUserInput{Username: "", Password: "rahasia123"}, domainTenancy.ErrUsernameInvalid},
		{"role tidak dikenal", domainTenancy.CreateUserInput{Username: "operator1", Password: "rahasia123", Role: "superadmin"}, domainTenancy.ErrRoleInvalid},
		{"device limit negatif", domainTenancy.CreateUserInput{Username: "operator1", Password: "rahasia123", DeviceLimit: -1}, domainTenancy.ErrDeviceLimitInvalid},
		{"password terlalu pendek", domainTenancy.CreateUserInput{Username: "operator1", Password: "short7c"}, authhash.ErrPasswordTooShort},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.CreateUser(context.Background(), tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// _____________________________________________________________________________
// Invarian admin terakhir — tiga jalur yang bisa menghabiskan admin

func TestDeleteLastActiveAdminIsRejected(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	admin := mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	if err := svc.DeleteUser(context.Background(), admin.ID); !errors.Is(err, domainTenancy.ErrLastAdminProtected) {
		t.Fatalf("err = %v, want ErrLastAdminProtected", err)
	}

	// Dengan admin kedua, penghapusan harus diizinkan.
	mustCreateUser(t, svc, "admin2", domainTenancy.RoleAdmin)
	if err := svc.DeleteUser(context.Background(), admin.ID); err != nil {
		t.Fatalf("hapus admin saat masih ada admin lain: %v", err)
	}
}

func TestDemotingLastActiveAdminIsRejected(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	admin := mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)

	operator := domainTenancy.RoleOperator
	_, err := svc.UpdateUser(context.Background(), admin.ID, domainTenancy.UpdateUserInput{Role: &operator})
	if !errors.Is(err, domainTenancy.ErrLastAdminProtected) {
		t.Fatalf("err = %v, want ErrLastAdminProtected", err)
	}

	// Role-nya tidak boleh ikut berubah meski permintaannya ditolak.
	reloaded, err := svc.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if reloaded.Role != domainTenancy.RoleAdmin {
		t.Fatalf("role = %q, want admin (perubahan yang ditolak tidak boleh tersimpan)", reloaded.Role)
	}
}

func TestDeactivatingLastActiveAdminIsRejected(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	admin := mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)

	inactive := false
	_, err := svc.UpdateUser(context.Background(), admin.ID, domainTenancy.UpdateUserInput{Active: &inactive})
	if !errors.Is(err, domainTenancy.ErrLastAdminProtected) {
		t.Fatalf("err = %v, want ErrLastAdminProtected", err)
	}

	reloaded, err := svc.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !reloaded.Active {
		t.Fatal("user harus tetap aktif setelah permintaan ditolak")
	}
}

// TestLastAdminGuardIgnoresOperators: operator sebanyak apa pun tidak menolong
// invarian, karena mereka tidak bisa mengelola user.
func TestLastAdminGuardIgnoresOperators(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	admin := mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	for _, name := range []string{"operator1", "operator2", "operator3"} {
		mustCreateUser(t, svc, name, domainTenancy.RoleOperator)
	}

	if err := svc.DeleteUser(context.Background(), admin.ID); !errors.Is(err, domainTenancy.ErrLastAdminProtected) {
		t.Fatalf("err = %v, want ErrLastAdminProtected", err)
	}
}

// TestLastAdminGuardIgnoresInactiveAdmins: admin yang dinonaktifkan tidak bisa
// masuk, jadi menghitungnya akan membuat invarian ini bohong.
func TestLastAdminGuardIgnoresInactiveAdmins(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	admin := mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	sleeping := mustCreateUser(t, svc, "admin2", domainTenancy.RoleAdmin)

	// Nonaktifkan langsung di repo supaya tidak tersangkut guard-nya sendiri.
	stored, _ := repo.GetUserByID(sleeping.ID)
	stored.Active = false
	if err := repo.UpdateUser(stored); err != nil {
		t.Fatalf("deactivate admin2: %v", err)
	}

	if err := svc.DeleteUser(context.Background(), admin.ID); !errors.Is(err, domainTenancy.ErrLastAdminProtected) {
		t.Fatalf("err = %v, want ErrLastAdminProtected", err)
	}
}

func TestDeleteUserRejectsMissingUser(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	if err := svc.DeleteUser(context.Background(), 999); !errors.Is(err, domainTenancy.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

// _____________________________________________________________________________
// Pencabutan session

func TestUpdateUserRevokesSessionsOnPasswordChange(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	password := "rahasiabaru1"
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{Password: &password}); err != nil {
		t.Fatalf("update password: %v", err)
	}

	if len(repo.revokedSessions) != 1 || repo.revokedSessions[0] != user.ID {
		t.Fatalf("revokedSessions = %v, want [%d]", repo.revokedSessions, user.ID)
	}

	// Password lama tidak boleh lolos lagi.
	reloaded, _ := repo.GetUserByID(user.ID)
	if authhash.VerifyPassword(reloaded.PasswordHash, "rahasia123") {
		t.Fatal("password lama masih berlaku setelah diganti")
	}
	if !authhash.VerifyPassword(reloaded.PasswordHash, password) {
		t.Fatal("password baru tidak berlaku")
	}
}

func TestUpdateUserRevokesSessionsOnDeactivation(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	inactive := false
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{Active: &inactive}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	if len(repo.revokedSessions) != 1 || repo.revokedSessions[0] != user.ID {
		t.Fatalf("revokedSessions = %v, want [%d]", repo.revokedSessions, user.ID)
	}
}

// TestUpdateUserDoesNotRevokeOnHarmlessChange: mengganti nama tampilan tidak
// boleh menendang user keluar dari semua perangkatnya.
func TestUpdateUserDoesNotRevokeOnHarmlessChange(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	name := "Nama Baru"
	limit := 5
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{
		DisplayName: &name,
		DeviceLimit: &limit,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if len(repo.revokedSessions) != 0 {
		t.Fatalf("revokedSessions = %v, want kosong", repo.revokedSessions)
	}
}

// TestUpdateUserSurfacesRevokeFailure: kalau pencabutan gagal, permintaan tidak
// boleh dilaporkan sukses. Password sudah berganti tapi session lama masih
// hidup adalah keadaan yang harus diketahui pemanggil, bukan disembunyikan.
func TestUpdateUserSurfacesRevokeFailure(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)
	repo.failRevoke = true

	password := "rahasiabaru1"
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{Password: &password}); err == nil {
		t.Fatal("expected the revoke failure to be surfaced")
	}
}

// TestUpdateUserOnlyChangesGivenFields: field nil berarti "jangan ubah". Tanpa
// ini, PATCH yang cuma mengganti nama akan mereset role dan mengaktifkan ulang
// user yang sengaja dinonaktifkan.
func TestUpdateUserOnlyChangesGivenFields(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	// Nonaktifkan dan beri limit dulu.
	inactive := false
	limit := 4
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{
		Active:      &inactive,
		DeviceLimit: &limit,
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// Sekarang ubah HANYA nama tampilan.
	name := "Operator Satu"
	updated, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{DisplayName: &name})
	if err != nil {
		t.Fatalf("update display name: %v", err)
	}

	if updated.Active {
		t.Fatal("user yang dinonaktifkan tidak boleh ikut aktif kembali")
	}
	if updated.Role != domainTenancy.RoleOperator {
		t.Fatalf("role = %q, want operator", updated.Role)
	}
	if updated.DeviceLimit != 4 {
		t.Fatalf("device limit = %d, want 4", updated.DeviceLimit)
	}

	stored, _ := repo.GetUserByID(user.ID)
	if !authhash.VerifyPassword(stored.PasswordHash, "rahasia123") {
		t.Fatal("password tidak boleh berubah saat tidak diminta")
	}
}

// _____________________________________________________________________________
// Authenticate

func TestAuthenticateAcceptsValidCredentials(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	principal, err := svc.Authenticate(context.Background(), "OPERATOR1", "rahasia123")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if principal == nil {
		t.Fatal("expected a principal")
	}
	if principal.UserID != user.ID || principal.Username != "operator1" {
		t.Fatalf("principal = %+v", principal)
	}
	if principal.Role != domainTenancy.RoleOperator || principal.IsAdmin() {
		t.Fatal("principal role salah")
	}
	if principal.ViaBreakGlass {
		t.Fatal("principal dari app_user tidak boleh ditandai break-glass")
	}
}

// TestAuthenticateFailuresAreIndistinguishable: ketiga kegagalan harus
// menghasilkan (nil, nil) yang sama. Perbedaan apa pun di sini membocorkan
// username mana yang terdaftar.
func TestAuthenticateFailuresAreIndistinguishable(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	stored, _ := repo.GetUserByID(user.ID)
	stored.Active = false
	if err := repo.UpdateUser(stored); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	cases := []struct {
		name     string
		username string
		password string
	}{
		{"user tidak ada", "hantu", "rahasia123"},
		{"password salah", "admin1", "salah-sekali"},
		{"user nonaktif", "operator1", "rahasia123"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			principal, err := svc.Authenticate(context.Background(), tc.username, tc.password)
			if err != nil {
				t.Fatalf("err = %v, want nil (kegagalan kredensial bukan error)", err)
			}
			if principal != nil {
				t.Fatalf("principal = %+v, want nil", principal)
			}
		})
	}
}

// _____________________________________________________________________________
// Bootstrap admin dari env

func TestBootstrapAdminsFromEnvSeedsAdmins(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	created, err := svc.BootstrapAdminsFromEnv(context.Background(), []string{"admin:rahasia123", "ops:rahasia456"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2", created)
	}

	principal, err := svc.Authenticate(context.Background(), "admin", "rahasia123")
	if err != nil || principal == nil {
		t.Fatalf("admin hasil seeding harus bisa login (err %v)", err)
	}
	if !principal.IsAdmin() {
		t.Fatal("admin hasil seeding harus punya role admin")
	}
}

// TestBootstrapAdminsFromEnvIsIdempotent: dijalankan setiap startup, jadi
// pemanggilan kedua tidak boleh membuat duplikat.
func TestBootstrapAdminsFromEnvIsIdempotent(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	credentials := []string{"admin:rahasia123"}
	if _, err := svc.BootstrapAdminsFromEnv(context.Background(), credentials); err != nil {
		t.Fatalf("bootstrap pertama: %v", err)
	}
	created, err := svc.BootstrapAdminsFromEnv(context.Background(), credentials)
	if err != nil {
		t.Fatalf("bootstrap kedua: %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0 pada pemanggilan kedua", created)
	}
	if count, _ := repo.CountUsers(); count != 1 {
		t.Fatalf("jumlah user = %d, want 1", count)
	}
}

// TestBootstrapAdminsFromEnvNeverOverwritesStoredPassword: begitu username ada
// di app_user, password database yang menang. Ini yang mencegah APP_BASIC_AUTH
// jadi backdoor permanen setelah admin mengganti passwordnya.
func TestBootstrapAdminsFromEnvNeverOverwritesStoredPassword(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)

	if _, err := svc.BootstrapAdminsFromEnv(context.Background(), []string{"admin:passwordenv1"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	user, err := svc.Authenticate(context.Background(), "admin", "passwordenv1")
	if err != nil || user == nil {
		t.Fatalf("login awal harus berhasil (err %v)", err)
	}

	// Admin mengganti passwordnya lewat UI.
	newPassword := "passworddb01"
	if _, err := svc.UpdateUser(context.Background(), user.UserID, domainTenancy.UpdateUserInput{Password: &newPassword}); err != nil {
		t.Fatalf("ganti password: %v", err)
	}

	// Startup berikutnya menjalankan seeding lagi dengan nilai env yang lama.
	if _, err := svc.BootstrapAdminsFromEnv(context.Background(), []string{"admin:passwordenv1"}); err != nil {
		t.Fatalf("bootstrap ulang: %v", err)
	}

	if p, _ := svc.Authenticate(context.Background(), "admin", "passwordenv1"); p != nil {
		t.Fatal("password env lama tidak boleh berlaku lagi setelah diganti di database")
	}
	if p, _ := svc.Authenticate(context.Background(), "admin", newPassword); p == nil {
		t.Fatal("password database harus tetap berlaku")
	}
}

// TestBootstrapAdminsFromEnvSkipsUnusableCredentials: password env yang tidak
// memenuhi aturan tidak boleh menggagalkan startup. Username itu tetap bisa
// masuk lewat break-glass di fase 03.
func TestBootstrapAdminsFromEnvSkipsUnusableCredentials(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	created, err := svc.BootstrapAdminsFromEnv(context.Background(), []string{
		"admin:pendek",        // password di bawah batas
		"tanpa-titik-dua",     // bukan format user:pass
		":rahasia123",         // username kosong
		"oper@tor:rahasia123", // username tidak valid
		"valid:rahasia123",    // satu-satunya yang benar
	})
	if err != nil {
		t.Fatalf("bootstrap tidak boleh mengembalikan error: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if count, _ := repo.CountUsers(); count != 1 {
		t.Fatalf("jumlah user = %d, want 1", count)
	}
}

func TestBootstrapAdminsFromEnvWithNoCredentials(t *testing.T) {
	svc, repo := newTenancyServiceForTest(t)

	created, err := svc.BootstrapAdminsFromEnv(context.Background(), nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	if count, _ := repo.CountUsers(); count != 0 {
		t.Fatalf("jumlah user = %d, want 0", count)
	}
}
