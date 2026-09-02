# Fase 02 — User Management + Bootstrap Admin

**Tujuan:** CRUD user lewat usecase dan REST, plus seeding admin pertama dari
`APP_BASIC_AUTH` supaya tidak ada skenario terkunci. Endpoint `/admin/users*`
sudah hidup, tapi belum ada isolasi device — itu fase 04.

**Tergantung:** Fase 01
**Branch:** `feature/multitenant-phase02`

---

## Prasyarat

Baca sebagai contoh pola:
- `src/usecase/device.go` — bentuk service struct + constructor `New*Service`
- `src/ui/rest/command_config.go` — handler struct, `InitRest*`, request struct
  dengan pointer field untuk tri-state, response envelope `utils.ResponseData`
- `src/cmd/helpers.go` — pola pekerjaan startup yang dipanggil dari `rest.go`
  (contoh: `CountChatwootDeviceConfigs` dipakai untuk memutuskan mode)

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/domains/tenancy/interfaces.go` | modifikasi (file fork sendiri) | tambah `ITenancyUsecase` |
| `src/usecase/tenancy.go` | **BARU** | service user + validasi |
| `src/usecase/tenancy_test.go` | **BARU** | test |
| `src/ui/rest/admin_users.go` | **BARU** | handler `/admin/users*` |
| `src/ui/rest/admin_users_test.go` | **BARU** | test |
| `src/cmd/multitenant.go` | **BARU** | bootstrap admin + wiring helper |
| `src/cmd/rest.go` | modifikasi kecil | init repo tenancy, seeding, daftarkan rute |

---

## Detail implementasi

### 1. `ITenancyUsecase` (tambahkan di `src/domains/tenancy/interfaces.go`)

```go
// CreateUserInput dipakai supaya penambahan field baru tidak mengubah signature
// method (dan tidak memaksa semua caller ikut berubah).
type CreateUserInput struct {
	Username    string
	Password    string
	DisplayName string
	Role        Role
	DeviceLimit int
}

// UpdateUserInput memakai pointer untuk setiap field: nil berarti "jangan ubah".
// Tanpa ini, PATCH yang hanya mengganti nama akan diam-diam mereset role dan
// mengaktifkan ulang user yang sengaja dinonaktifkan.
type UpdateUserInput struct {
	Password    *string
	DisplayName *string
	Role        *Role
	DeviceLimit *int
	Active      *bool
}

type ITenancyUsecase interface {
	CreateUser(ctx context.Context, in CreateUserInput) (*User, error)
	UpdateUser(ctx context.Context, id int64, in UpdateUserInput) (*User, error)
	DeleteUser(ctx context.Context, id int64) error
	GetUser(ctx context.Context, id int64) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)

	// Authenticate memverifikasi username+password terhadap app_user. Return
	// (nil, nil) kalau kredensial salah atau user nonaktif — bukan error, supaya
	// caller tidak bisa membedakan "user tidak ada" dari "password salah".
	Authenticate(ctx context.Context, username, password string) (*Principal, error)

	// BootstrapAdminsFromEnv menyemai satu admin per kredensial APP_BASIC_AUTH
	// yang belum punya baris di app_user. Idempoten.
	BootstrapAdminsFromEnv(ctx context.Context, credentials []string) (created int, err error)
}
```

`Session` dan cookie ditangani fase 03; jangan tambahkan method session ke
usecase di fase ini.

### 2. `src/usecase/tenancy.go`

Aturan validasi yang harus ditegakkan di usecase (bukan di handler, bukan di
repository — supaya jalur REST dan jalur seeding tunduk pada aturan yang sama):

- **Username**: 3–64 karakter, hanya `a-z`, `0-9`, `.`, `_`, `-`. Dinormalisasi
  lowercase + trim. Tolak yang lain. Alasan pembatasan: username muncul di log
  dan (di fase 08) di UI; membatasi charset menghilangkan seluruh kelas masalah
  encoding sekaligus.
- **Password**: delegasikan ke `authhash.HashPassword`, jangan duplikasi aturan
  panjangnya di sini.
- **Role**: hanya `admin` atau `operator`. String lain → error validasi. Kosong
  pada create → default `operator` (aman secara default).
- **DeviceLimit**: `>= 0`. 0 = tanpa batas.
- **Invariant admin terakhir**: `DeleteUser` dan `UpdateUser` yang akan membuat
  jumlah admin aktif jadi 0 harus **ditolak** (`CountAdmins()` dari fase 01).
  Ini berlaku untuk tiga kasus: hapus admin terakhir, turunkan role admin
  terakhir jadi operator, dan nonaktifkan admin terakhir. Ketiganya harus ada
  test-nya.
- **Ganti password mencabut session**: setiap `UpdateUser` yang mengubah password
  wajib memanggil `DeleteSessionsByUser`. Begitu juga saat `Active` diubah jadi
  false. Tanpa ini, "nonaktifkan user" tidak berefek sampai cookie-nya
  kedaluwarsa sendiri.
- `Authenticate` harus **selalu** menjalankan verifikasi bcrypt, bahkan saat user
  tidak ada, dengan membandingkan terhadap satu hash dummy tetap. Kalau tidak,
  selisih waktu respons membocorkan username mana yang terdaftar.

Bentuk service ikut pola yang ada di project:

```go
type serviceTenancy struct {
	repo tenancy.ITenancyRepository
}

func NewTenancyService(repo tenancy.ITenancyRepository) tenancy.ITenancyUsecase {
	return &serviceTenancy{repo: repo}
}
```

### 3. `BootstrapAdminsFromEnv`

Semantik yang harus tepat, karena inilah yang mencegah lockout:

```
untuk setiap kredensial "user:pass" di config.AppBasicAuthCredential:
    normalisasi username
    kalau GetUserByUsername != nil  -> SKIP (jangan timpa; password DB yang menang)
    kalau tidak ada                 -> CreateUser{role: admin, active: true, device_limit: 0}
                                       dengan hash dari password env
```

Konsekuensi yang **disengaja** dan harus ditulis di komentar kode:

- Admin pertama kali punya password yang sama dengan env. Setelah dia
  menggantinya lewat UI, DB yang menang dan nilai env diabaikan untuk username
  itu.
- Kredensial env yang username-nya **belum** ada di `app_user` tetap bisa login
  (jalur break-glass di fase 03). Untuk mematikan break-glass, hapus kredensial
  itu dari `APP_BASIC_AUTH` — bukan dari `app_user`.
- Kalau password env terlalu pendek untuk `authhash.HashPassword`, **jangan
  gagalkan startup**. Catat warning dan lewati; username itu tetap bisa masuk
  lewat break-glass. Startup yang mati gara-gara password env pendek adalah cara
  paling cepat mengubah upgrade jadi outage.

Panggil hanya kalau `config.MultiTenantEnabled` true.

### 4. `src/ui/rest/admin_users.go`

```go
func InitRestAdminUsers(app fiber.Router, service tenancy.ITenancyUsecase) *AdminUsersHandler
```

Rute:

| Method | Path | Akses | Catatan |
|--------|------|-------|---------|
| GET | `/admin/users` | admin | tanpa `password_hash` di response |
| POST | `/admin/users` | admin | body: username, password, display_name, role, device_limit |
| GET | `/admin/users/:id` | admin | |
| PATCH | `/admin/users/:id` | admin | semua field opsional (pointer) |
| DELETE | `/admin/users/:id` | admin | device-nya jadi tak-ber-owner, bukan terhapus |

Catatan implementasi:

- Response pakai `utils.ResponseData` seperti handler lain. Jangan bikin envelope
  baru.
- `PasswordHash` sudah bertag `json:"-"` di DTO, tapi **tetap tulis test** yang
  memastikan body response tidak pernah memuat `$2a$` — tag JSON gampang hilang
  saat refactor, dan yang bocor di sini adalah hash password.
- Guard role: di fase ini `middleware.RequireAdmin` belum ada (dibuat fase 03).
  Untuk sementara **daftarkan rutenya di belakang `config.MultiTenantEnabled`
  saja**, dan tambahkan `TODO(fase-03): pasang RequireAdmin`. Fase 03 wajib
  menghapus TODO itu — masuk ke DoD fase 03.
- Error validasi → 400 dengan `Code` yang jelas (`VALIDATION_ERROR`,
  `USERNAME_TAKEN`, `LAST_ADMIN_PROTECTED`). Jangan pakai `utils.PanicIfNeeded`
  untuk error validasi; itu untuk error tak terduga.

### 5. `src/cmd/rest.go` (modifikasi kecil)

Tiga sisipan saja, sekecil mungkin:

```go
	// (1) setelah chatStorageRepo siap
	tenancyRepo := chatStorageRepo.(tenancy.ITenancyRepository) // atau ambil dari konstruktor konkret
	tenancyUsecase := usecase.NewTenancyService(tenancyRepo)

	// (2) seeding, sebelum listen
	seedMultiTenantAdmins(tenancyUsecase)   // helper di cmd/multitenant.go, no-op saat flag off

	// (3) daftarkan rute, sejajar dengan InitRestCustomUI
	if config.MultiTenantEnabled {
		rest.InitRestAdminUsers(apiGroup, tenancyUsecase)
	}
```

**Soal type assertion di (1):** lihat dulu bagaimana `chatStorageRepo` dibuat di
`rest.go`. Kalau tipe konkretnya `*chatstorage.SQLiteRepository` tersedia di situ,
**pakai variabel konkretnya langsung** dan hindari type assertion — assertion yang
gagal akan panic saat startup dan susah dibaca. Kalau yang tersedia hanya
interface, lakukan assertion dengan bentuk dua-nilai dan `logrus.Fatalln` yang
pesannya jelas.

Semua logika seeding taruh di `src/cmd/multitenant.go` (file baru), bukan di
`rest.go`. `rest.go` cuma memanggil.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] Dengan `MULTI_TENANT_ENABLED=false`: tidak ada rute `/admin/*` yang
      terdaftar, tidak ada seeding, `app_user` tetap kosong. Buktikan dengan
      `curl` yang responsnya **bukan** dari handler admin — di app ini path tak
      terdaftar tertangkap `DeviceMiddleware` dan menjawab
      `400 DEVICE_ID_REQUIRED`, bukan 404. Lihat "Rute yang tidak terdaftar
      TIDAK menjawab 404" di `README.md`.
- [ ] Dengan `MULTI_TENANT_ENABLED=true` dan `APP_BASIC_AUTH=admin:rahasia123`:
      startup pertama membuat satu baris admin; startup kedua **tidak** membuat
      duplikat (idempoten).
- [ ] CRUD user jalan lewat `curl` (lihat Verifikasi).
- [ ] Response `/admin/users` tidak pernah memuat hash password (ada test-nya).
- [ ] Admin terakhir tidak bisa dihapus, di-nonaktifkan, atau diturunkan
      role-nya (tiga test terpisah).
- [ ] Ganti password mencabut semua session user itu (test di level usecase
      dengan repo palsu/asli).
- [ ] `Authenticate` menolak user `active = 0`.
- [ ] `git diff src/cmd/rest.go` maksimal ~10 baris penambahan.

## Verifikasi

```bash
cd src && go test ./usecase/... ./ui/rest/... -run -i tenancy -v
```

Manual (asumsi server jalan di port 3000 dengan `MULTI_TENANT_ENABLED=true`):

```bash
curl -u admin:rahasia123 -X POST localhost:3000/admin/users -H 'Content-Type: application/json' -d '{"username":"operator1","password":"rahasia123","display_name":"Operator Satu","role":"operator","device_limit":2}'
```

```bash
curl -u admin:rahasia123 localhost:3000/admin/users
```

Pastikan admin terakhir terlindungi:

```bash
curl -u admin:rahasia123 -X DELETE localhost:3000/admin/users/1 -i
```

Harus `400` dengan code `LAST_ADMIN_PROTECTED`.

---

## Catatan & jebakan

- Fase ini **belum** mengubah cara autentikasi. Basic Auth masih memvalidasi ke
  env, jadi user yang dibuat lewat `/admin/users` belum bisa login. Itu normal —
  fase 03 yang menyambungkannya. Jangan mencoba memperbaikinya di sini.
- Endpoint `/admin/*` di fase ini masih bisa diakses oleh siapa pun yang punya
  kredensial `APP_BASIC_AUTH` (karena `RequireAdmin` belum ada). **Jangan
  aktifkan flag di produksi setelah fase 02.** Lihat tabel "Aman deploy?" di
  README — aman deploy berarti tidak merusak, bukan berarti sudah aman untuk
  dinyalakan.
- Jangan tambahkan endpoint "ganti password sendiri" di fase ini; itu perlu
  principal (fase 03). Catat sebagai TODO fase 08.
- Hati-hati membuat pesan error yang membocorkan keberadaan username saat login.
  `Authenticate` mengembalikan `(nil, nil)` untuk semua kegagalan; jangan
  membedakan pesannya di layer mana pun.
