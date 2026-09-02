# Fase 01 — Skema DB, Domain, Repository Tenancy

**Tujuan:** menyediakan penyimpanan untuk user, kepemilikan device, dan session.
Belum ada yang memakainya — tabel dibuat, kode repository ada dan tertest, tapi
tidak ada satu pun handler yang berubah.

**Tergantung:** Fase 00
**Branch:** `feature/multitenant-phase01`

---

## Prasyarat

Cek jumlah migration **aktual** lebih dulu. Angka di dokumen ini (54) benar saat
rencana disusun, tapi sync upstream bisa menambah:

```bash
cd src && grep -c "// Migration " infrastructure/chatstorage/sqlite_repository.go
```

Kalau hasilnya `N`, migration pertama yang kamu tambahkan bernomor `N+1`.
Runner-nya (`InitializeSchema`) memakai indeks array sebagai versi, jadi
**append-only itu wajib mutlak** — menyisipkan di tengah akan menggeser versi
semua migration setelahnya dan merusak DB yang sudah jalan.

Baca dulu sebagai contoh pola:
- `src/domains/chatstorage/chatstorage.go` — bentuk struct DTO + tag `db`
- `src/infrastructure/chatstorage/sqlite_repository.go` — `SaveChatwootDeviceConfig`,
  `scanChatwootDeviceConfig`, `GetChatwootDeviceConfig` (pola upsert + scanner)
- `src/ui/mcp/oauth/store.go` — `hashSecret` (pola simpan hash token, bukan token)

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/infrastructure/chatstorage/sqlite_repository.go` | modifikasi kecil | append 7 migration di akhir `getMigrations()` |
| `src/domains/tenancy/tenancy.go` | **BARU** | DTO |
| `src/domains/tenancy/interfaces.go` | **BARU** | `ITenancyRepository` |
| `src/infrastructure/chatstorage/sqlite_repository_tenancy.go` | **BARU** | implementasi |
| `src/infrastructure/chatstorage/sqlite_repository_tenancy_test.go` | **BARU** | test |
| `src/pkg/authhash/password.go` | **BARU** | bcrypt + token opaque |
| `src/pkg/authhash/password_test.go` | **BARU** | test |

> **JANGAN** menambahkan method apa pun ke
> `src/domains/chatstorage/interfaces.go` (`IChatStorageRepository`). Lihat K2 di
> README: `chatstorage_wrapper.go` (424 baris) wajib punya stub untuk tiap method
> di interface itu. Interface tenancy berdiri sendiri.

---

## Detail implementasi

### 1. Migration (append di akhir `getMigrations()`)

Satu statement per entry, dengan komentar bernomor mengikuti gaya yang sudah ada.
Ganti `55` dst. dengan hasil hitung aktual:

```go
		// Migration 55: Akun aplikasi untuk mode multi-tenant. Tabel ini kosong
		// selama MULTI_TENANT_ENABLED=false; kredensial APP_BASIC_AUTH tetap
		// jadi satu-satunya jalan masuk sampai admin di-seed (fase 02).
		// device_limit 0 berarti tanpa batas.
		`CREATE TABLE IF NOT EXISTS app_user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username VARCHAR(64) NOT NULL,
			password_hash VARCHAR(255) NOT NULL DEFAULT '',
			display_name VARCHAR(255) NOT NULL DEFAULT '',
			role VARCHAR(16) NOT NULL DEFAULT 'operator',
			device_limit INTEGER NOT NULL DEFAULT 0,
			active BOOLEAN NOT NULL DEFAULT 1,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		// Migration 56: Username adalah identitas login, harus unik.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_app_user_username ON app_user(username)`,
		// Migration 57: Kepemilikan device slot. Tabel terpisah, bukan kolom baru
		// di tabel devices: devices milik upstream dan ALTER di sana menaikkan
		// risiko konflik migration saat sync. Satu device dimiliki satu user;
		// device tanpa baris di sini dianggap belum di-klaim.
		`CREATE TABLE IF NOT EXISTS device_owner (
			device_id VARCHAR(255) PRIMARY KEY,
			user_id INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		// Migration 58: Daftar device milik satu user adalah query terpanas
		// (dipakai tiap request device-scoped lewat cache dan tiap GET /devices).
		`CREATE INDEX IF NOT EXISTS idx_device_owner_user ON device_owner(user_id)`,
		// Migration 59: Session login berbasis cookie. Yang disimpan adalah
		// SHA-256 dari token, bukan tokennya: dump DB tidak boleh cukup untuk
		// membajak session yang masih hidup. Pola yang sama dipakai
		// ui/mcp/oauth/store.go (hashSecret).
		`CREATE TABLE IF NOT EXISTS user_session (
			token_hash VARCHAR(64) PRIMARY KEY,
			user_id INTEGER NOT NULL,
			user_agent VARCHAR(255) NOT NULL DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL
		)`,
		// Migration 60: Cabut semua session satu user sekaligus (nonaktifkan
		// user, ganti password) tanpa full scan.
		`CREATE INDEX IF NOT EXISTS idx_user_session_user ON user_session(user_id)`,
		// Migration 61: Sapu session kedaluwarsa secara berkala.
		`CREATE INDEX IF NOT EXISTS idx_user_session_expires ON user_session(expires_at)`,
```

### 2. `src/domains/tenancy/tenancy.go`

```go
package tenancy

import "time"

// Role membatasi apa yang boleh dilihat dan diubah seorang user. Hanya dua
// nilai; jangan tambah tanpa permintaan eksplisit (K7).
type Role string

const (
	RoleAdmin    Role = "admin"    // melihat semua device, mengelola user
	RoleOperator Role = "operator" // hanya device miliknya sendiri
)

// User adalah akun aplikasi. PasswordHash tidak pernah ikut ke response API.
type User struct {
	ID           int64     `db:"id"           json:"id"`
	Username     string    `db:"username"     json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	DisplayName  string    `db:"display_name" json:"display_name"`
	Role         Role      `db:"role"         json:"role"`
	DeviceLimit  int       `db:"device_limit" json:"device_limit"` // 0 = tanpa batas
	Active       bool      `db:"active"       json:"active"`
	CreatedAt    time.Time `db:"created_at"   json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"   json:"updated_at"`
}

// Principal adalah identitas pemanggil yang sudah terautentikasi, hasil dari
// cookie session atau Basic Auth. Disimpan di c.Locals("principal").
type Principal struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	// ViaBreakGlass true jika identitas berasal dari APP_BASIC_AUTH dan bukan
	// dari baris app_user. Dipakai untuk audit log, bukan untuk otorisasi.
	ViaBreakGlass bool `json:"-"`
}

// IsAdmin dipakai di seluruh guard sebagai satu-satunya jalan cek privilege,
// supaya perbandingan string role tidak tersebar ke mana-mana.
func (p *Principal) IsAdmin() bool { return p != nil && p.Role == RoleAdmin }

// DeviceOwner memetakan satu device slot ke pemiliknya.
type DeviceOwner struct {
	DeviceID  string    `db:"device_id" json:"device_id"`
	UserID    int64     `db:"user_id"   json:"user_id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// Session adalah satu login berbasis cookie. TokenHash adalah SHA-256 hex dari
// token yang dipegang browser; tokennya sendiri tidak pernah disimpan.
type Session struct {
	TokenHash string    `db:"token_hash" json:"-"`
	UserID    int64     `db:"user_id"    json:"user_id"`
	UserAgent string    `db:"user_agent" json:"user_agent"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
}
```

### 3. `src/domains/tenancy/interfaces.go`

```go
package tenancy

import "time"

// ITenancyRepository berdiri sendiri, terpisah dari IChatStorageRepository,
// supaya deviceChatStorage (infrastructure/whatsapp/chatstorage_wrapper.go)
// tidak perlu stub untuk method-method di sini. Pola yang sama dipakai
// kontrak message queue.
type ITenancyRepository interface {
	// User
	CreateUser(user *User) (int64, error)
	UpdateUser(user *User) error
	GetUserByID(id int64) (*User, error)
	GetUserByUsername(username string) (*User, error)
	ListUsers() ([]*User, error)
	DeleteUser(id int64) error
	CountUsers() (int, error)
	CountAdmins() (int, error)

	// Kepemilikan device
	SetDeviceOwner(deviceID string, userID int64) error
	GetDeviceOwner(deviceID string) (*DeviceOwner, error)
	ListDeviceIDsByOwner(userID int64) ([]string, error)
	CountDevicesByOwner(userID int64) (int, error)
	DeleteDeviceOwner(deviceID string) error
	// DeleteDeviceOwnersByUser melepas semua device milik satu user. Dipakai
	// saat user dihapus: device-nya jadi tak-ber-owner, bukan ikut terhapus —
	// menghapus device adalah operasi destruktif (purge sesi WhatsApp) dan
	// harus tetap keputusan eksplisit admin.
	DeleteDeviceOwnersByUser(userID int64) error

	// Session
	CreateSession(session *Session) error
	GetSession(tokenHash string) (*Session, error)
	DeleteSession(tokenHash string) error
	DeleteSessionsByUser(userID int64) error
	DeleteExpiredSessions(now time.Time) (int64, error)
}
```

### 4. `src/pkg/authhash/password.go`

```go
// Package authhash menyediakan hashing password dan pembuatan token session
// untuk mode multi-tenant. Dipisah dari usecase supaya bisa dites tanpa DB.
package authhash

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength ditegakkan di satu tempat supaya API dan seeding admin
// tidak bisa menyimpan password yang lebih lemah dari aturan UI.
const MinPasswordLength = 8

var ErrPasswordTooShort = errors.New("password minimal 8 karakter")

// bcrypt memotong input di 72 byte. Menolak yang lebih panjang lebih baik
// daripada diam-diam mengabaikan sisanya, yang membuat dua password berbeda
// jadi setara.
const maxPasswordBytes = 72

var ErrPasswordTooLong = errors.New("password maksimal 72 byte")

func HashPassword(plain string) (string, error) {
	if utf8.RuneCountInString(plain) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	if len(plain) > maxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// VerifyPassword mengembalikan true hanya kalau hash cocok. Hash kosong selalu
// gagal: baris user tanpa password tidak boleh bisa login.
func VerifyPassword(hash, plain string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// NewSessionToken mengembalikan token acak 256-bit dan hash SHA-256-nya.
// Yang pertama dikirim ke browser, yang kedua disimpan di DB.
func NewSessionToken() (token, tokenHash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken dipakai baik saat membuat maupun saat memverifikasi session.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

Jalankan `cd src && go mod tidy` supaya `golang.org/x/crypto` naik dari indirect
ke direct. Tidak ada modul baru yang perlu diunduh — sudah ada di `go.sum`.

### 5. `src/infrastructure/chatstorage/sqlite_repository_tenancy.go`

Method dipasang pada `*SQLiteRepository` yang sudah ada (receiver sama, file
berbeda), persis seperti `sqlite_repository_message_queue.go`.

Aturan yang harus dipatuhi:

- Semua query pakai placeholder `?`, jangan pernah menyusun SQL dengan
  konkatenasi string.
- Username disimpan **lowercase trimmed**. Normalisasi di satu fungsi
  (`normalizeUsername`) dan pakai di `CreateUser`, `UpdateUser`, dan
  `GetUserByUsername`, supaya `Rizki` dan `rizki` tidak bisa jadi dua akun.
- `SetDeviceOwner` adalah **upsert**:
  `INSERT ... ON CONFLICT(device_id) DO UPDATE SET user_id = excluded.user_id, updated_at = CURRENT_TIMESTAMP`.
  Ikuti gaya upsert `SaveChatwootDeviceConfig`.
- `GetUserByID` / `GetUserByUsername` / `GetDeviceOwner` / `GetSession`
  mengembalikan `(nil, nil)` kalau baris tidak ada — **bukan** error. Cek
  `errors.Is(err, sql.ErrNoRows)`. Pola ini dipakai konsisten di repository ini;
  memutuskannya akan bikin caller salah menangani "belum ada" sebagai "gagal".
- `GetSession` **tidak** memfilter kedaluwarsa; itu tugas usecase, supaya
  repository tetap dumb dan test-nya deterministik.
- Bikin satu `scanUser(scanner interface{ Scan(...any) error }) (*User, error)`
  seperti `scanChatwootDeviceConfig`, dipakai bareng oleh Get dan List.
- `DeleteUser` **tidak** menghapus device. Panggil `DeleteSessionsByUser` dan
  `DeleteDeviceOwnersByUser` di dalam satu transaksi bersama delete user, supaya
  tidak ada owner yatim yang menunjuk user_id yang sudah hilang.
- `CountAdmins()` hanya menghitung admin yang `active = 1`. Ini yang dipakai
  fase 02 untuk mencegah admin terakhir menghapus/menonaktifkan dirinya sendiri.

### 6. Test — `sqlite_repository_tenancy_test.go`

Ikuti pola setup DB di `src/infrastructure/chatstorage/sqlite_repository_test.go`
(cek apakah dia pakai file temp atau in-memory, lalu pakai yang sama).

> **Penting untuk lingkungan dev ini.** `CGO_ENABLED=0` dan `gcc` tidak
> terpasang, jadi jalankan test dengan `-tags purego`. Untuk membuka koneksi DB
> di test baru, pakai `sqlite.DriverName` dari `src/pkg/sqlite` — **jangan**
> menulis literal `"sqlite3"`. Beberapa test lama melakukan itu dan karenanya
> gagal di lingkungan ini meski tag `purego` dipakai. Detailnya di bagian
> "Baseline lingkungan" di `README.md`.

Test minimum:

- `InitializeSchema()` pada DB kosong membuat keempat tabel baru dan
  `getSchemaVersion()` sama dengan `len(getMigrations())`.
- `InitializeSchema()` idempoten: dipanggil dua kali tidak error.
- `CreateUser` + `GetUserByUsername` round-trip, termasuk case-insensitive
  (`simpan "Rizki"` → `GetUserByUsername("rizki")` ketemu).
- `CreateUser` dengan username duplikat (beda case) → error.
- `SetDeviceOwner` dua kali untuk device yang sama → satu baris, owner terakhir
  yang menang.
- `ListDeviceIDsByOwner` hanya mengembalikan device user itu.
- `GetDeviceOwner` untuk device yang belum di-klaim → `(nil, nil)`.
- `DeleteUser` menghapus session dan baris owner-nya, dan **tidak** menyentuh
  device milik user lain.
- `DeleteExpiredSessions` hanya menghapus yang `expires_at` sudah lewat.
- `CountAdmins` mengabaikan admin yang `active = 0`.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] `go mod tidy` sudah jalan; `golang.org/x/crypto` jadi direct dependency dan
      `go.mod`/`go.sum` ikut di-commit.
- [ ] `IChatStorageRepository` (`src/domains/chatstorage/interfaces.go`) **tidak
      berubah satu baris pun** — buktikan dengan `git diff`.
- [ ] `chatstorage_wrapper.go` **tidak berubah**.
- [ ] Migration hanya ditambahkan di akhir array; `git diff` pada
      `getMigrations()` hanya menampilkan penambahan.
- [ ] Aplikasi yang dijalankan dengan DB lama berhasil migrasi tanpa error, dan
      DB lama tetap terbaca (test dengan copy `storages/chatstorage.db` asli).
- [ ] Tidak ada handler, usecase, atau middleware yang berubah di fase ini.

## Verifikasi

```bash
cd src && go build ./... && go vet ./... && go test ./infrastructure/chatstorage/... ./pkg/authhash/... -v
```

Migrasi terhadap DB nyata (pakai salinan, jangan yang asli):

```bash
cp storages/chatstorage.db /tmp/mt-test.db
cd src && CHAT_STORAGE_URI="file:/tmp/mt-test.db" go run . rest --port 3099
# lihat log migration, lalu Ctrl+C
```

Konfirmasi tabelnya jadi:

```bash
sqlite3 /tmp/mt-test.db ".tables" | tr ' ' '\n' | grep -E "app_user|device_owner|user_session"
```

---

## Catatan & jebakan

- **Jangan sekali-kali menyisipkan migration di tengah array.** Runner memakai
  indeks sebagai nomor versi, jadi penyisipan akan membuat DB yang sudah jalan
  melewatkan migration atau menjalankan yang salah.
- `runMigration` mengeksekusi **satu statement** per entry. Jangan gabungkan
  `CREATE TABLE` dan `CREATE INDEX` dalam satu string.
- Komentar di `getMigrations()` menyebut kompatibilitas SQLite/MySQL/PostgreSQL.
  `INSERT ... ON CONFLICT` didukung SQLite dan Postgres tapi tidak MySQL. Karena
  fitur ini fork-only dan default-nya SQLite, ini diterima — tapi **tulis
  catatannya di komentar** supaya orang berikutnya tidak bingung.
- `bcrypt.DefaultCost` (10) itu sengaja: cukup kuat, dan cukup cepat supaya
  login lewat Basic Auth per-request tidak jadi beban. Fase 03 menambahkan cache
  verifikasi untuk mengatasi biaya per-request-nya; jangan naikkan cost di sini.
- `SetDeviceOwner` tidak memvalidasi bahwa `user_id`-nya ada. Validasi itu tugas
  usecase (fase 04), bukan repository.
