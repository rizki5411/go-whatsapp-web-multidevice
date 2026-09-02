package chatstorage

import (
	"errors"
	"testing"
	"time"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
)

// Test di file ini memakai newTestSQLiteRepository dari
// sqlite_repository_test.go, yang membuka DB lewat sqlite.DriverName — jadi
// ikut menghormati build tag purego. Jalankan dengan `-tags purego` di mesin
// tanpa cgo; lihat docs/multitenant/README.md bagian "Baseline lingkungan".

func newTestUser(t *testing.T, repo *SQLiteRepository, username string, role domainTenancy.Role) *domainTenancy.User {
	t.Helper()

	user := &domainTenancy.User{
		Username:     username,
		PasswordHash: "$2a$10$hashplaceholderhashplaceholderhashplaceholderhashpla",
		DisplayName:  username,
		Role:         role,
		Active:       true,
	}
	if _, err := repo.CreateUser(user); err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return user
}

func TestTenancySchemaCreatesTables(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	for _, table := range []string{"app_user", "device_owner", "user_session"} {
		var name string
		err := repo.db.QueryRow(`
			SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?
		`, table).Scan(&name)
		if err != nil {
			t.Fatalf("expected table %q to exist: %v", table, err)
		}
	}
}

// TestTenancySchemaVersionMatchesMigrationCount menjaga agar migration tenancy
// benar-benar berjalan sampai yang terakhir. Runner memakai indeks array
// sebagai nomor versi, jadi versi yang tertinggal berarti ada migration yang
// tidak pernah dieksekusi.
func TestTenancySchemaVersionMatchesMigrationCount(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	version, err := repo.getSchemaVersion()
	if err != nil {
		t.Fatalf("get schema version: %v", err)
	}
	if want := len(repo.getMigrations()); version != want {
		t.Fatalf("schema version = %d, want %d", version, want)
	}
}

// TestTenancySchemaIsIdempotent meniru restart aplikasi: InitializeSchema
// dipanggil setiap boot dan tidak boleh gagal pada DB yang sudah bermigrasi.
func TestTenancySchemaIsIdempotent(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if err := repo.InitializeSchema(); err != nil {
		t.Fatalf("second InitializeSchema: %v", err)
	}
}

func TestCreateAndGetUserRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	created := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	if created.ID == 0 {
		t.Fatal("expected CreateUser to populate the id")
	}

	byID, err := repo.GetUserByID(created.ID)
	if err != nil {
		t.Fatalf("get user by id: %v", err)
	}
	if byID == nil {
		t.Fatal("expected to find the user by id")
	}
	if byID.Username != "operator1" {
		t.Fatalf("username = %q, want %q", byID.Username, "operator1")
	}
	if byID.Role != domainTenancy.RoleOperator {
		t.Fatalf("role = %q, want %q", byID.Role, domainTenancy.RoleOperator)
	}
	if !byID.Active {
		t.Fatal("expected the user to be active")
	}
	if byID.CreatedAt.IsZero() || byID.UpdatedAt.IsZero() {
		t.Fatal("expected timestamps to be persisted")
	}
}

// TestUsernameLookupIsCaseInsensitive: username disimpan ternormalisasi, jadi
// dua ejaan yang beda kapitalisasi harus menunjuk akun yang sama. Tanpa ini,
// "Admin" dan "admin" bisa jadi dua identitas dan salah satunya lolos dari
// pemeriksaan role.
func TestUsernameLookupIsCaseInsensitive(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	newTestUser(t, repo, "  Rizki  ", domainTenancy.RoleOperator)

	found, err := repo.GetUserByUsername("RIZKI")
	if err != nil {
		t.Fatalf("get user by username: %v", err)
	}
	if found == nil {
		t.Fatal("expected the lookup to be case-insensitive")
	}
	if found.Username != "rizki" {
		t.Fatalf("stored username = %q, want %q", found.Username, "rizki")
	}
}

func TestCreateUserRejectsDuplicateUsernameAcrossCase(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)

	_, err := repo.CreateUser(&domainTenancy.User{
		Username: "OPERATOR1",
		Role:     domainTenancy.RoleOperator,
		Active:   true,
	})
	if !errors.Is(err, domainTenancy.ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
}

func TestGetUserReturnsNilWhenMissing(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	// "Belum ada" adalah keadaan normal dan harus (nil, nil), bukan error —
	// lihat konvensi baca di domains/tenancy/interfaces.go.
	user, err := repo.GetUserByUsername("nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != nil {
		t.Fatalf("expected nil user, got %+v", user)
	}

	byID, err := repo.GetUserByID(4242)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if byID != nil {
		t.Fatalf("expected nil user, got %+v", byID)
	}
}

func TestUpdateUserPersistsChangesAndKeepsCreatedAt(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	user := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	originalCreatedAt := user.CreatedAt

	user.Role = domainTenancy.RoleAdmin
	user.DisplayName = "Operator Satu"
	user.DeviceLimit = 3
	user.Active = false
	if err := repo.UpdateUser(user); err != nil {
		t.Fatalf("update user: %v", err)
	}

	reloaded, err := repo.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if reloaded.Role != domainTenancy.RoleAdmin {
		t.Fatalf("role = %q, want admin", reloaded.Role)
	}
	if reloaded.DisplayName != "Operator Satu" {
		t.Fatalf("display name = %q", reloaded.DisplayName)
	}
	if reloaded.DeviceLimit != 3 {
		t.Fatalf("device limit = %d, want 3", reloaded.DeviceLimit)
	}
	if reloaded.Active {
		t.Fatal("expected the user to be inactive")
	}
	// created_at adalah fakta historis; UpdateUser tidak boleh menulisnya ulang.
	if !reloaded.CreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("created_at berubah: %v -> %v", originalCreatedAt, reloaded.CreatedAt)
	}
}

func TestUpdateUserRejectsUsernameCollision(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	second := newTestUser(t, repo, "operator2", domainTenancy.RoleOperator)

	second.Username = "Operator1"
	if err := repo.UpdateUser(second); !errors.Is(err, domainTenancy.ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
}

func TestListUsersIsSortedByUsername(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	newTestUser(t, repo, "zulu", domainTenancy.RoleOperator)
	newTestUser(t, repo, "alpha", domainTenancy.RoleOperator)

	users, err := repo.ListUsers()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(users))
	}
	if users[0].Username != "alpha" || users[1].Username != "zulu" {
		t.Fatalf("unexpected order: %q, %q", users[0].Username, users[1].Username)
	}
}

// TestCountAdminsIgnoresInactive: angka ini yang menjaga invarian "selalu ada
// minimal satu admin aktif" di fase 02. Admin yang dinonaktifkan tidak bisa
// masuk, jadi menghitungnya akan membuat invarian itu bohong.
func TestCountAdminsIgnoresInactive(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	newTestUser(t, repo, "admin1", domainTenancy.RoleAdmin)
	inactive := newTestUser(t, repo, "admin2", domainTenancy.RoleAdmin)
	newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)

	inactive.Active = false
	if err := repo.UpdateUser(inactive); err != nil {
		t.Fatalf("deactivate admin2: %v", err)
	}

	admins, err := repo.CountAdmins()
	if err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if admins != 1 {
		t.Fatalf("CountAdmins() = %d, want 1", admins)
	}

	total, err := repo.CountUsers()
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if total != 3 {
		t.Fatalf("CountUsers() = %d, want 3", total)
	}
}

// _____________________________________________________________________________
// Kepemilikan device

func TestSetDeviceOwnerUpsertsAndLastWriteWins(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	first := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	second := newTestUser(t, repo, "operator2", domainTenancy.RoleOperator)

	if err := repo.SetDeviceOwner("dev-a", first.ID); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if err := repo.SetDeviceOwner("dev-a", second.ID); err != nil {
		t.Fatalf("reassign owner: %v", err)
	}

	owner, err := repo.GetDeviceOwner("dev-a")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	if owner == nil {
		t.Fatal("expected an owner row")
	}
	if owner.UserID != second.ID {
		t.Fatalf("owner = %d, want %d (pemilik terakhir yang menang)", owner.UserID, second.ID)
	}

	// Upsert harus memperbarui baris yang ada, bukan menambah baris kedua.
	var rows int
	if err := repo.db.QueryRow("SELECT COUNT(*) FROM device_owner WHERE device_id = ?", "dev-a").Scan(&rows); err != nil {
		t.Fatalf("count owner rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("device_owner rows = %d, want 1", rows)
	}
}

// TestGetDeviceOwnerReturnsNilForUnclaimedDevice: device tanpa baris owner
// adalah kondisi semua device lama saat flag pertama dinyalakan. Guard di fase
// 04 mengandalkan (nil, nil) di sini untuk memutuskan "hanya admin".
func TestGetDeviceOwnerReturnsNilForUnclaimedDevice(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	owner, err := repo.GetDeviceOwner("dev-orphan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != nil {
		t.Fatalf("expected nil owner, got %+v", owner)
	}
}

func TestSetDeviceOwnerRejectsEmptyDeviceAndZeroUser(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if err := repo.SetDeviceOwner("", 1); !errors.Is(err, domainTenancy.ErrDeviceIDRequired) {
		t.Fatalf("err = %v, want ErrDeviceIDRequired", err)
	}
	// user_id 0 adalah principal break-glass, yang tidak punya baris app_user.
	// Menyimpannya akan membuat device dimiliki identitas yang tidak ada.
	if err := repo.SetDeviceOwner("dev-a", 0); !errors.Is(err, domainTenancy.ErrUserRequired) {
		t.Fatalf("err = %v, want ErrUserRequired", err)
	}
}

func TestListDeviceIDsByOwnerIsScopedToTheUser(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	first := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	second := newTestUser(t, repo, "operator2", domainTenancy.RoleOperator)

	for _, id := range []string{"dev-a1", "dev-a2"} {
		if err := repo.SetDeviceOwner(id, first.ID); err != nil {
			t.Fatalf("set owner %s: %v", id, err)
		}
	}
	if err := repo.SetDeviceOwner("dev-b1", second.ID); err != nil {
		t.Fatalf("set owner dev-b1: %v", err)
	}

	ids, err := repo.ListDeviceIDsByOwner(first.ID)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("len(ids) = %d, want 2 (%v)", len(ids), ids)
	}
	for _, id := range ids {
		if id == "dev-b1" {
			t.Fatal("device milik user lain bocor ke daftar")
		}
	}

	count, err := repo.CountDevicesByOwner(first.ID)
	if err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountDevicesByOwner() = %d, want 2", count)
	}
}

func TestDeleteDeviceOwnerLeavesDeviceUnclaimed(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	user := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	if err := repo.SetDeviceOwner("dev-a", user.ID); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if err := repo.DeleteDeviceOwner("dev-a"); err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	owner, err := repo.GetDeviceOwner("dev-a")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	if owner != nil {
		t.Fatal("expected the device to become unclaimed")
	}
}

// _____________________________________________________________________________
// Session

func TestSessionRoundTripAndDelete(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	user := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	expires := time.Now().Add(time.Hour)

	if err := repo.CreateSession(&domainTenancy.Session{
		TokenHash: "hash-a",
		UserID:    user.ID,
		UserAgent: "curl/8",
		ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	session, err := repo.GetSession("hash-a")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session == nil {
		t.Fatal("expected to find the session")
	}
	if session.UserID != user.ID {
		t.Fatalf("user id = %d, want %d", session.UserID, user.ID)
	}
	if session.Expired(time.Now()) {
		t.Fatal("session should still be valid")
	}

	if err := repo.DeleteSession("hash-a"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if session, err = repo.GetSession("hash-a"); err != nil || session != nil {
		t.Fatalf("expected the session to be gone, got %+v (err %v)", session, err)
	}
}

func TestCreateSessionRejectsIncompleteInput(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	user := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)

	if err := repo.CreateSession(&domainTenancy.Session{UserID: user.ID, ExpiresAt: time.Now()}); err == nil {
		t.Fatal("expected an error for a missing token hash")
	}
	if err := repo.CreateSession(&domainTenancy.Session{TokenHash: "h", ExpiresAt: time.Now()}); !errors.Is(err, domainTenancy.ErrUserRequired) {
		t.Fatal("expected ErrUserRequired for a missing user id")
	}
	if err := repo.CreateSession(&domainTenancy.Session{TokenHash: "h", UserID: user.ID}); err == nil {
		t.Fatal("expected an error for a missing expiry")
	}
}

// TestDeleteSessionsByUserRevokesOnlyThatUser: ini jalur "nonaktifkan user" dan
// "ganti password" di fase 03. Kalau ia ikut mencabut session user lain,
// mengganti password satu operator akan menendang semua orang keluar.
func TestDeleteSessionsByUserRevokesOnlyThatUser(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	first := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	second := newTestUser(t, repo, "operator2", domainTenancy.RoleOperator)
	expires := time.Now().Add(time.Hour)

	for _, s := range []*domainTenancy.Session{
		{TokenHash: "a1", UserID: first.ID, ExpiresAt: expires},
		{TokenHash: "a2", UserID: first.ID, ExpiresAt: expires},
		{TokenHash: "b1", UserID: second.ID, ExpiresAt: expires},
	} {
		if err := repo.CreateSession(s); err != nil {
			t.Fatalf("create session %s: %v", s.TokenHash, err)
		}
	}

	if err := repo.DeleteSessionsByUser(first.ID); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	for _, hash := range []string{"a1", "a2"} {
		session, err := repo.GetSession(hash)
		if err != nil {
			t.Fatalf("get session %s: %v", hash, err)
		}
		if session != nil {
			t.Fatalf("session %s should have been revoked", hash)
		}
	}

	survivor, err := repo.GetSession("b1")
	if err != nil {
		t.Fatalf("get session b1: %v", err)
	}
	if survivor == nil {
		t.Fatal("session milik user lain tidak boleh ikut tercabut")
	}
}

func TestDeleteExpiredSessionsOnlyRemovesExpired(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	user := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	now := time.Now()

	for _, s := range []*domainTenancy.Session{
		{TokenHash: "dead", UserID: user.ID, ExpiresAt: now.Add(-time.Minute)},
		{TokenHash: "alive", UserID: user.ID, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := repo.CreateSession(s); err != nil {
			t.Fatalf("create session %s: %v", s.TokenHash, err)
		}
	}

	deleted, err := repo.DeleteExpiredSessions(now)
	if err != nil {
		t.Fatalf("sweep sessions: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if session, err := repo.GetSession("dead"); err != nil || session != nil {
		t.Fatalf("expected the expired session to be gone (err %v)", err)
	}
	if session, err := repo.GetSession("alive"); err != nil || session == nil {
		t.Fatalf("expected the live session to survive (err %v)", err)
	}
}

// TestDeleteUserCascadesSessionsAndOwnershipOnly memeriksa tiga hal sekaligus,
// karena ketiganya adalah satu transaksi: session dan kepemilikan user itu
// hilang, device-nya jadi tak-ber-owner (bukan terhapus), dan data user lain
// tidak tersentuh.
func TestDeleteUserCascadesSessionsAndOwnershipOnly(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	victim := newTestUser(t, repo, "operator1", domainTenancy.RoleOperator)
	bystander := newTestUser(t, repo, "operator2", domainTenancy.RoleOperator)
	expires := time.Now().Add(time.Hour)

	if err := repo.SetDeviceOwner("dev-a", victim.ID); err != nil {
		t.Fatalf("set owner dev-a: %v", err)
	}
	if err := repo.SetDeviceOwner("dev-b", bystander.ID); err != nil {
		t.Fatalf("set owner dev-b: %v", err)
	}
	if err := repo.CreateSession(&domainTenancy.Session{TokenHash: "a1", UserID: victim.ID, ExpiresAt: expires}); err != nil {
		t.Fatalf("create victim session: %v", err)
	}
	if err := repo.CreateSession(&domainTenancy.Session{TokenHash: "b1", UserID: bystander.ID, ExpiresAt: expires}); err != nil {
		t.Fatalf("create bystander session: %v", err)
	}

	if err := repo.DeleteUser(victim.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if user, err := repo.GetUserByID(victim.ID); err != nil || user != nil {
		t.Fatalf("expected the user to be gone (err %v)", err)
	}
	if session, err := repo.GetSession("a1"); err != nil || session != nil {
		t.Fatalf("expected the user's session to be revoked (err %v)", err)
	}
	// Device-nya harus jadi tak-ber-owner, bukan terhapus: mem-purge device
	// berarti memutus sesi WhatsApp dan itu keputusan terpisah.
	owner, err := repo.GetDeviceOwner("dev-a")
	if err != nil {
		t.Fatalf("get owner dev-a: %v", err)
	}
	if owner != nil {
		t.Fatalf("expected dev-a to become unclaimed, got owner %d", owner.UserID)
	}

	// User lain sama sekali tidak boleh tersentuh.
	if user, err := repo.GetUserByID(bystander.ID); err != nil || user == nil {
		t.Fatalf("bystander user should survive (err %v)", err)
	}
	if session, err := repo.GetSession("b1"); err != nil || session == nil {
		t.Fatalf("bystander session should survive (err %v)", err)
	}
	otherOwner, err := repo.GetDeviceOwner("dev-b")
	if err != nil {
		t.Fatalf("get owner dev-b: %v", err)
	}
	if otherOwner == nil || otherOwner.UserID != bystander.ID {
		t.Fatal("kepemilikan device user lain tidak boleh berubah")
	}
}
