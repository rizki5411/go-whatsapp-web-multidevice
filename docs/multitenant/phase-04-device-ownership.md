# Fase 04 — Kepemilikan Device, Guard, Filter Daftar

**Tujuan:** device punya pemilik, dan pemilik itu ditegakkan. Ini fase inti:
setelah fase ini, mayoritas endpoint data (L1, L2, L3, L4 di inventaris README)
sudah terisolasi.

**Tergantung:** Fase 03
**Branch:** `feature/multitenant-phase04`

---

## Prasyarat

Baca dulu, dan pahami perbedaan keduanya:

- `src/ui/rest/middleware/device.go` — `DeviceMiddleware`. Membaca device dari
  header `X-Device-Id` atau query `device_id`, memanggil `dm.ResolveDevice`, lalu
  menaruh hasilnya di `c.Locals("device_id")`, `c.Locals("device")`, dan
  `c.SetContext(whatsapp.ContextWithDevice(...))`. **File ini tidak boleh
  disentuh.**
- `src/infrastructure/whatsapp/device_manager.go` — `ResolveDevice`. Kalau
  `deviceID` kosong, dia jatuh ke `DefaultDevice()`. Inilah L2.
- `src/usecase/device.go` — `ListDevices`, `AddDevice`, `RemoveDevice`.
- `src/usecase/app.go` — `FetchDevices`.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/domains/tenancy/interfaces.go` | modifikasi (fork) | tambah `IDeviceOwnership` |
| `src/usecase/device_ownership.go` | **BARU** | logika kepemilikan + kuota |
| `src/usecase/device_ownership_test.go` | **BARU** | test |
| `src/ui/rest/middleware/device_owner_guard.go` | **BARU** | guard, additive |
| `src/ui/rest/middleware/device_owner_guard_test.go` | **BARU** | test |
| `src/ui/rest/tenantfilter/filter.go` | **BARU** | helper filter daftar device |
| `src/ui/rest/device.go` | modifikasi kecil | filter `ListDevices`, klaim di `AddDevice` |
| `src/ui/rest/app.go` | modifikasi kecil | filter `Devices` (`/app/devices`) |
| `src/ui/rest/admin_device_owner.go` | **BARU** | `/admin/devices/:device_id/owner` |
| `src/cmd/rest.go` | modifikasi kecil | pasang guard di grup, wiring |

---

## Detail implementasi

### 1. `IDeviceOwnership` (di `src/domains/tenancy/interfaces.go`)

```go
// IDeviceOwnership adalah satu-satunya tempat keputusan "boleh atau tidak"
// soal device diambil. Middleware, handler, dan MCP semuanya memanggil ini,
// supaya tidak ada dua definisi kepemilikan yang bisa berbeda.
type IDeviceOwnership interface {
	// CanAccess true kalau principal boleh mengakses device itu. Admin selalu
	// true. Device tanpa owner: lihat aturan device tak-ber-owner di bawah.
	CanAccess(p *Principal, deviceID string) bool
	// OwnedDeviceIDs mengembalikan daftar device milik principal. Untuk admin
	// mengembalikan nil beserta all=true, artinya "tidak perlu difilter".
	OwnedDeviceIDs(p *Principal) (ids []string, all bool)
	// Claim mencatat principal sebagai pemilik device yang baru dibuat.
	// Menolak principal break-glass (UserID == 0).
	Claim(p *Principal, deviceID string) error
	// Release melepas kepemilikan, dipanggil setelah device dihapus.
	Release(deviceID string) error
	// Assign dipakai admin untuk memindahkan device ke user lain.
	Assign(deviceID string, userID int64) error
	// EnsureQuota mengembalikan error kalau principal sudah mencapai
	// device_limit-nya.
	EnsureQuota(p *Principal) error
}
```

### 2. Aturan device tak-ber-owner — keputusan yang dikunci

Device yang sudah ada **sebelum** multi-tenant dinyalakan tidak punya baris di
`device_owner`. Aturannya:

> **Device tanpa owner hanya bisa diakses oleh admin.** Operator tidak bisa
> melihat maupun memakainya sampai admin menetapkan pemiliknya.

Kenapa begitu, bukan "boleh diakses semua" (yang lebih ramah): default yang
permisif berarti setiap device yang gagal ter-klaim — karena bug, karena race,
karena migrasi setengah jalan — otomatis terbuka untuk semua orang. Default yang
ketat gagal ke arah yang aman, dan gejalanya kelihatan langsung ("device saya
hilang") bukan diam-diam ("device saya dilihat orang lain").

Konsekuensi praktis: saat pertama menyalakan flag, admin **harus** menetapkan
owner untuk device yang sudah ada. Sediakan jalannya lewat
`/admin/devices/:device_id/owner` dan dokumentasikan di fase 09 sebagai langkah
rollout wajib.

### 3. `CanAccess` — logika lengkap

```
kalau !config.MultiTenantEnabled              -> true
kalau p == nil                                -> false   (fail closed)
kalau p.IsAdmin()                             -> true
owner := GetDeviceOwner(deviceID)
kalau owner == nil                            -> false   (tak-ber-owner)
-> owner.UserID == p.UserID
```

`p == nil` mengembalikan false itu penting. Kalau nanti guard ini terpasang di
jalur yang tidak punya principal, kita mau request-nya gagal, bukan lolos.

**Cache.** `CanAccess` dipanggil di setiap request device-scoped, jadi tiap
request akan jadi satu query SQLite. Tambahkan cache map `deviceID -> userID`
dengan TTL pendek (30 detik) **atau** invalidasi eksplisit di `Claim`, `Release`,
`Assign`. Pilih invalidasi eksplisit — lebih tepat dan tidak punya jendela stale.
Kalau memilih TTL, jendela stale-nya berarti device yang baru dipindahkan masih
bisa diakses pemilik lama selama 30 detik; itu tidak bisa diterima untuk kontrol
akses. **Pakai invalidasi eksplisit.**

### 4. `src/ui/rest/middleware/device_owner_guard.go`

Ini bagian paling penting di fase ini, dan sengaja dibuat additive supaya
`device.go` upstream tidak tersentuh (K5).

```go
// DeviceOwnerGuard berjalan SETELAH DeviceMiddleware dan menegakkan kepemilikan
// atas device yang sudah diresolve. Dipisah dari DeviceMiddleware supaya file
// upstream itu tidak perlu diubah sama sekali.
func DeviceOwnerGuard(dm *whatsapp.DeviceManager, own tenancy.IDeviceOwnership) fiber.Handler
```

Alur:

```
1. kalau !config.MultiTenantEnabled -> c.Next()
2. lolosi path publik yang sama dengan DeviceMiddleware ("/", base path)
3. resolvedID := c.Locals("device_id") sebagai string
   kalau kosong -> c.Next()   (DeviceMiddleware sudah menolak duluan)
4. explicit := header X-Device-Id atau query device_id tidak kosong
5. kalau explicit:
       CanAccess(principal, resolvedID) ? c.Next() : 404 DEVICE_NOT_FOUND
6. kalau !explicit (DeviceMiddleware jatuh ke DefaultDevice, ini L2):
       CanAccess -> c.Next()
       kalau tidak -> cari device default milik principal sendiri:
           ids, all := OwnedDeviceIDs(principal)
           kalau all (admin)          -> c.Next()
           kalau len(ids) == 1        -> RE-RESOLVE ke ids[0], timpa locals, c.Next()
           kalau len(ids) == 0        -> 404 DEVICE_NOT_FOUND
           kalau len(ids) > 1         -> 400 DEVICE_ID_REQUIRED
```

Langkah 6 itu yang membuat operator single-device tetap nyaman: tanpa itu,
setiap request tanpa `X-Device-Id` akan 404 dan seluruh klien lama pecah.

Saat re-resolve di langkah 6, **ketiga** nilai yang ditulis `DeviceMiddleware`
harus ditimpa, bukan cuma satu:

```go
inst, id, err := dm.ResolveDevice(ids[0])
if err != nil { /* 404 */ }
c.Locals("device_id", id)
c.Locals("device", inst)
c.SetContext(whatsapp.ContextWithDevice(c.Context(), inst))
```

Kalau `SetContext` terlewat, usecase yang mengambil device dari `context` akan
tetap memakai device orang lain sementara handler yang membaca `Locals` memakai
yang benar. Itu bug isolasi yang paling sulit dilihat dari luar — **wajib ada
test khusus untuk kasus ini**.

Pesan 404 harus **identik** dengan yang dipakai `DeviceMiddleware` untuk device
yang benar-benar tidak ada. Copy string-nya apa adanya (K4).

### 5. Pemasangan di `src/cmd/rest.go`

Satu baris berubah:

```go
	// sebelum
	headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))

	// sesudah
	headerDeviceGroup := apiGroup.Group("",
		middleware.DeviceMiddleware(dm),
		middleware.DeviceOwnerGuard(dm, ownership),
	)
```

### 6. Klaim saat device dibuat

`POST /devices` → `AddDevice`. Urutannya harus:

```
1. EnsureQuota(principal)                -> gagal: 403 DEVICE_LIMIT_REACHED
2. service.AddDevice(...)                -> menghasilkan deviceID final
3. ownership.Claim(principal, deviceID)  -> gagal: lihat di bawah
```

Kuota dicek **sebelum** device dibuat, supaya tidak ada sesi WhatsApp yatim.

Kalau `Claim` gagal setelah device terbuat, jangan pura-pura sukses. Kembalikan
`500` dengan pesan yang menyebut device id-nya dan bahwa device itu belum
ber-owner sehingga hanya admin yang bisa melihatnya. Pola pesan error jujur
seperti ini sudah dipakai di `AddDevice` untuk kegagalan simpan webhook — ikuti
gaya itu.

`DELETE /devices/:device_id` → setelah `RemoveDevice` sukses, panggil
`ownership.Release(deviceID)`. Kalau tidak, baris owner yatim akan membuat device
baru yang kebetulan memakai id yang sama langsung dimiliki orang lama.

Catat: `RemoveDevice` ada di jalur `/devices/:device_id`, yang **tidak** lewat
`DeviceOwnerGuard` (rute ini didaftarkan di `apiGroup`, bukan
`headerDeviceGroup`). Guard untuk rute-rute itu adalah pekerjaan fase 05 —
tapi khusus `AddDevice`/`RemoveDevice` sudah ditangani di fase ini karena
menyangkut siklus hidup kepemilikan. Pastikan `RemoveDevice` mengecek
`CanAccess` sebelum menghapus. **Ini yang paling berbahaya kalau terlewat:**
tanpa cek itu, operator mana pun bisa mem-purge device orang lain.

### 7. Filter daftar device

Dua endpoint, dua handler, satu helper bersama:

- `GET /devices` → `src/ui/rest/device.go` `ListDevices`
- `GET /app/devices` → `src/ui/rest/app.go` `Devices`

Filter dilakukan **di layer handler**, bukan usecase. Alasannya: principal ada
di `fiber.Ctx`, sementara usecase hanya menerima `context.Context` dan
`PassLocalsToContext` tidak aktif di app ini. Menyalakan flag Fiber itu demi ini
akan mengubah perilaku seluruh aplikasi — perubahan yang jauh lebih luas daripada
yang dibutuhkan.

`src/ui/rest/tenantfilter/filter.go`:

```go
// Devices menyaring daftar device menurut kepemilikan. Bekerja atas tipe apa
// pun lewat fungsi pengekstrak id, supaya bisa dipakai baik oleh
// []device.Device maupun []app.DevicesResponse tanpa duplikasi.
func Devices[T any](p *tenancy.Principal, own tenancy.IDeviceOwnership, items []T, id func(T) string) []T
```

Saat flag off, kembalikan `items` apa adanya. Saat admin, sama. Jangan
mengembalikan `nil` untuk daftar kosong kalau input-nya non-nil — beberapa klien
membedakan `[]` dari `null`, dan `ListDevices` yang ada sekarang mengembalikan
`nil` untuk registry kosong (perilaku itu jangan diubah).

### 8. `/admin/devices/:device_id/owner`

| Method | Path | Akses | Body |
|--------|------|-------|------|
| GET | `/admin/devices/:device_id/owner` | admin | — |
| PUT | `/admin/devices/:device_id/owner` | admin | `{"user_id": 3}` |
| DELETE | `/admin/devices/:device_id/owner` | admin | melepas jadi tak-ber-owner |

Validasi di `Assign`:

- `user_id` harus ada di `app_user` dan `active = 1`.
- `user_id` tidak boleh 0.
- Kuota user tujuan diperiksa; kalau penuh, tolak `400`.
- `device_id` harus resolve di `DeviceManager` — jangan izinkan menetapkan owner
  untuk device yang tidak ada.
- `strings.Clone` untuk `device_id` sebelum disimpan (buffer fasthttp).

### 9. Test yang wajib ada

Test middleware (paling penting — pakai `fiber.New()` + handler tiruan):

- explicit id milik sendiri → 200
- explicit id milik orang lain → 404, dan body-nya **identik** dengan 404
  device-tidak-ada
- explicit id device tak-ber-owner, sebagai operator → 404
- explicit id device tak-ber-owner, sebagai admin → 200
- tanpa id, operator punya tepat 1 device → 200 **dan** `c.Locals("device_id")`
  sudah tertimpa ke device miliknya
- tanpa id, operator punya 1 device → device yang terlihat lewat
  `whatsapp.DeviceFromContext(c.Context())` juga sudah tertimpa (test terpisah,
  ini yang gampang lolos dari perhatian)
- tanpa id, operator punya 0 device → 404
- tanpa id, operator punya 2 device → 400 `DEVICE_ID_REQUIRED`
- tanpa id, admin → 200 (tetap pakai default lama)
- flag off → semua kombinasi di atas → 200

Test ownership usecase:

- `Claim` menolak principal break-glass (`UserID == 0`)
- `EnsureQuota` menghormati `device_limit`; 0 = tanpa batas
- `Assign` menolak user nonaktif dan user yang tidak ada
- `Release` membuat device jadi tak-ber-owner (bukan menghapus device)
- invalidasi cache: `Assign` lalu `CanAccess` langsung memberi hasil baru

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] `src/ui/rest/middleware/device.go` **tidak berubah satu baris pun**
      (`git diff` kosong untuk file itu).
- [ ] `src/infrastructure/whatsapp/device_manager.go` **tidak berubah**.
- [ ] Flag off: semua endpoint berperilaku sama seperti sebelumnya, termasuk
      request tanpa `X-Device-Id` yang jatuh ke default device.
- [ ] Flag on, dua operator dengan satu device masing-masing:
      - operator A `GET /devices` hanya melihat device A
      - operator A `GET /app/devices` hanya melihat device A
      - operator A memakai `X-Device-Id` device B → 404
      - operator A `POST /send/message` dengan `X-Device-Id` device B → 404
      - operator A `DELETE /devices/<device B>` → 404, device B **masih ada**
      - admin melihat kedua device
- [ ] Device lama (tak-ber-owner) tidak terlihat oleh operator, terlihat oleh admin.
- [ ] `POST /devices` mengklaim otomatis; `device_limit` ditegakkan.
- [ ] Re-resolve tanpa header menimpa `Locals` **dan** context (dua test).
- [ ] Cache kepemilikan ter-invalidasi saat `Assign`/`Release`/`Claim`.
- [ ] `git diff src/cmd/rest.go` maksimal ~8 baris tambahan di fase ini.

## Verifikasi

```bash
cd src && go test ./ui/rest/middleware/... ./usecase/... -v
```

Skenario dua user (server flag on, sudah ada operator1 & operator2 dengan
device masing-masing `dev-a` dan `dev-b`):

```bash
curl -u operator1:rahasia123 localhost:3000/devices
```

```bash
curl -u operator1:rahasia123 -H 'X-Device-Id: dev-b' localhost:3000/app/status -i
```

Harus `404`. Lalu pastikan device B benar-benar tidak bisa dihapus:

```bash
curl -u operator1:rahasia123 -X DELETE localhost:3000/devices/dev-b -i
```

Harus `404`, dan `dev-b` masih muncul di daftar admin:

```bash
curl -u admin:rahasia123 localhost:3000/devices
```

---

## Catatan & jebakan

- **Rute yang tidak lewat `DeviceOwnerGuard` masih bocor setelah fase ini.**
  Yaitu semua `:device_id` di `src/ui/rest/device.go`, `command_config.go`,
  `message_queue.go`, dan `chatwoot_config.go` (kecuali Add/Remove yang sudah
  ditangani di sini). Itu fase 05. Jangan menyatakan isolasi selesai.
- Jangan mencoba memindahkan rute-rute itu ke `headerDeviceGroup` supaya
  otomatis ter-guard. Mereka memakai `:device_id` sebagai path param, sementara
  `DeviceMiddleware` hanya membaca header/query — memindahkannya akan mengubah
  kontrak URL yang sudah dipakai UI dan klien.
- `DefaultDevice()` tidak diubah. Guard-lah yang menangani konsekuensinya. Kalau
  tergoda mengubah `DefaultDevice` supaya sadar tenant, ingat: dia dipanggil juga
  dari jalur non-HTTP (worker) yang tidak punya principal.
- Worker background (`message_queue_worker.go`, `presence_pulse.go`,
  `event_command_handler.go`) beroperasi langsung atas device instance tanpa
  principal. Mereka **tidak** butuh guard — device sudah ditentukan oleh event
  WhatsApp itu sendiri, bukan oleh pemanggil HTTP. Jangan menambahkan guard di
  sana; itu hanya akan mematikan worker.
- Hati-hati saat menyalin `items` di `tenantfilter.Devices`: jangan memutasi slice
  input di tempat, karena caller mungkin masih memakainya.
