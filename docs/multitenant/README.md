# Multi-Tenant (Opsi C) — Rencana Pengerjaan

Isolasi data per user untuk fork ini: setiap user punya device sendiri, hanya
melihat dan mengendalikan device miliknya, dengan user management di database
(bukan lagi hanya env `APP_BASIC_AUTH`) plus role `admin` / `operator`.

Dokumen ini adalah **index**. Setiap fase punya file sendiri supaya AI yang
mengerjakan cukup membaca satu file dan tidak kebanjiran konteks.

---

## Cara pakai dokumen ini

Prompt siap pakai untuk menyuruh AI mengerjakan tiap fase ada di
[PROMPTS.md](PROMPTS.md) — termasuk prompt review yang dijalankan di sesi
terpisah setelah tiap fase.

Untuk AI/dev yang mengerjakan satu fase:

1. Baca `CLAUDE.md` dan `AGENTS.md` di root repo lebih dulu — aturan fork
   (additive-first, minim konflik upstream) berlaku penuh di sini.
2. Baca **hanya** file fase yang dikerjakan, plus bagian "Keputusan arsitektur"
   dan "Aturan main" di bawah.
3. Jangan lanjut ke fase berikutnya sebelum *Definition of Done* fase sekarang
   hijau semua.
4. Update kolom Status di tabel fase bawah setelah selesai.

> **Nomor baris di dokumen ini bisa bergeser** setelah sync upstream atau setelah
> fase sebelumnya jalan. Selalu `grep` ulang nama simbolnya, jangan percaya nomor
> baris mentah.

---

## Kondisi awal (hasil audit)

Basic auth di `src/cmd/rest.go` (`newBasicAuthMiddleware`) hanya gerbang biner:
username divalidasi lalu dibuang. Tidak ada konsep pemilik device sama sekali.

Inventaris titik kebocoran yang sudah dipetakan:

| # | Titik | Lokasi | Dampak |
|---|-------|--------|--------|
| L1 | `DeviceMiddleware` menerima device id apa pun dari registry global | `src/ui/rest/middleware/device.go` → `ResolveDevice` | semua endpoint device-scoped: chat, message, send, group, user, newsletter, app |
| L2 | `DefaultDevice()` fallback saat `X-Device-Id` kosong | `src/infrastructure/whatsapp/device_manager.go` → `ResolveDevice`/`DefaultDevice` | request tanpa header nyasar ke device orang lain |
| L3 | `ListDevices()` mengembalikan seluruh registry | `src/usecase/device.go` → `GET /devices` | daftar device bocor |
| L4 | `FetchDevices()` idem | `src/usecase/app.go` → `GET /app/devices` + websocket `FETCH_DEVICES` | daftar device bocor |
| L5 | Rute `:device_id` yang resolve manual (di luar `DeviceMiddleware`) | `src/ui/rest/device.go`, `command_config.go`, `message_queue.go`, `chatwoot_config.go` | tidak tersentuh guard middleware |
| L6 | Endpoint agregat lintas device | `GET /command/configs`, `GET /chatwoot/configs` | config device orang lain bocor |
| L7 | WebSocket hub tunggal, broadcast ke semua koneksi | `src/ui/websocket/websocket.go` → `Clients`, `broadcastMessage` | event QR/login/pesan device lain muncul di layar user lain |
| L8 | MCP tool memakai device manager global | `src/ui/mcp/` → `resolveDeviceContext` | identitas sudah tersedia via `oauth_subject`, tapi belum dipakai |
| L9 | Webhook Chatwoot publik (didaftarkan sebelum auth) | `src/cmd/rest.go` → `POST {webhookPath}/:device_id` | pre-existing; wajib `CHATWOOT_WEBHOOK_SECRET` di mode multi-tenant |

Kabar baiknya: L1 adalah *choke point* tunggal untuk mayoritas endpoint data,
jadi satu guard di sana menutup banyak lubang sekaligus.

---

## Keputusan arsitektur (sudah dikunci — jangan diubah tanpa alasan kuat)

**K1. Semua perilaku baru di belakang feature flag `config.MultiTenantEnabled`
(default `false`).** Flag mati = perilaku sama persis seperti sekarang. Ini yang
membuat rollout aman dan sync upstream tetap rendah risiko.

**K2. Kontrak repository terpisah — JANGAN sentuh `IChatStorageRepository`.**
Tambah interface baru `ITenancyRepository` di `src/domains/tenancy/`.
Alasan: `src/infrastructure/whatsapp/chatstorage_wrapper.go` (424 baris) harus
di-stub untuk setiap method yang masuk `IChatStorageRepository`. Preseden yang
diikuti: `messagequeue` (lihat `AGENTS.md`: "Queue contract is separate from
IChatStorageRepository, so the wrapper needs no stubs").

**K3. Kepemilikan device disimpan di tabel baru `device_owner`, BUKAN kolom baru
di tabel `devices`.** Tabel `devices` milik upstream; `ALTER TABLE` di situ
menaikkan risiko konflik migration saat sync.

**K4. Cross-tenant dijawab `404 DEVICE_NOT_FOUND`, bukan `403`.** 403
mengonfirmasi bahwa device itu ada — itu sendiri sebuah kebocoran. Pakai pesan
yang identik dengan device yang benar-benar tidak ada.

**K5. Guard device dipasang sebagai middleware terpisah SETELAH `DeviceMiddleware`,
bukan dengan mengubah `DeviceMiddleware`.** Jadi `device.go` upstream tidak
disentuh sama sekali:

```go
headerDeviceGroup := apiGroup.Group("",
    middleware.DeviceMiddleware(dm),
    middleware.DeviceOwnerGuard(dm, ownership), // baru, additive
)
```

**K6. Auth dua jalur, keduanya wajib jalan:**

- **Cookie session** (`gowa_session`, HttpOnly, SameSite=Lax) — untuk halaman
  `/custom/*` dan login form. Token opaque; di DB hanya SHA-256-nya yang disimpan.
- **HTTP Basic** — untuk klien API, dashboard gowa-ui, dan MCP. Divalidasi ke
  tabel `app_user` (bcrypt), dengan **fallback ke `APP_BASIC_AUTH` hanya jika
  username itu belum ada di `app_user`**.

Kenapa Basic wajib tetap hidup: dashboard gowa-ui adalah HTML eksternal yang
di-download runtime dan ditimpa auto-update — tidak bisa kita ubah, dan dia
mengandalkan Basic Auth. Mematikan Basic = mematikan dashboard.

**K7. Role hanya dua: `admin` dan `operator`.** `admin` melihat semua device dan
mengelola user. `operator` hanya device miliknya. Jangan tambah role ketiga tanpa
permintaan eksplisit.

**K8. Password: bcrypt** dari `golang.org/x/crypto/bcrypt` (sudah ada di
`src/go.sum` sebagai indirect; `go mod tidy` mempromosikannya ke direct). Jangan
tambah dependensi crypto baru.

**K9. Prefix endpoint baru: `/auth/*` dan `/admin/*`.** Jangan pakai `/users`
karena `/user/*` sudah dipakai untuk info user WhatsApp (`src/ui/rest/user.go`).

---

## Aturan main untuk setiap fase

- Kerja di branch `feature/multitenant-phaseNN` dari `develop`. Merge ke `develop`
  setelah DoD hijau.
- Wajib lulus sebelum merge:
  ```bash
  cd src && go build ./... && go vet ./... && go test -tags purego ./...
  ```
  Kriterianya **sama dengan baseline**, bukan "semua hijau" — lihat bagian
  [Baseline lingkungan](#baseline-lingkungan-wajib-dibaca) di bawah.
- **Kode baru taruh di file baru.** Modifikasi file upstream hanya kalau tidak ada
  jalan additive, dan sekecil mungkin — jangan reformat, jangan reorganisasi.
- **Migration: append-only di akhir `getMigrations()`**
  (`src/infrastructure/chatstorage/sqlite_repository.go`). Satu statement per
  entry. Cek dulu jumlah aktualnya, jangan percaya angka di dokumen ini:
  ```bash
  grep -c "// Migration " src/infrastructure/chatstorage/sqlite_repository.go
  ```
  Saat rencana ini disusun jumlahnya **54**, jadi migration baru mulai dari 55.
- Nilai dari `c.Params(...)` yang akan **dipersistensi atau disimpan** wajib
  di-`strings.Clone` — fasthttp mendaur ulang buffer-nya. Preseden:
  `resolveConfigDeviceID` di `src/ui/rest/command_config.go`.
- Setiap fase menambah test di paket yang sama, ikut pola `*_test.go` yang sudah
  ada. Jangan menyentuh test upstream yang tidak relevan.
- Jangan ubah bentuk response endpoint yang sudah ada. Menambah field opsional
  boleh; mengubah atau menghapus field tidak.

---

## Baseline lingkungan (WAJIB dibaca)

Diukur di mesin dev Windows saat Fase 00, sebelum ada perubahan kode apa pun.
**Suite test project ini tidak hijau seluruhnya di lingkungan ini**, dan itu
bukan akibat pekerjaan multi-tenant.

`CGO_ENABLED=0` dan `gcc` tidak terpasang, sementara driver default
(`github.com/mattn/go-sqlite3`) butuh cgo. Project menyediakan driver alternatif
tanpa cgo lewat build tag `purego` (`src/pkg/sqlite/sqlite_purego.go`), jadi
**pakai `-tags purego` untuk semua perintah test.**

| Mode | Paket yang FAIL | Penyebab |
|------|-----------------|----------|
| `go test ./...` | `infrastructure/chatstorage`, `infrastructure/whatsapp`, `ui/mcp/oauth`, `usecase` | go-sqlite3 butuh cgo (stub) |
| `go test -tags purego ./...` | `infrastructure/chatstorage`, `usecase` | sebagian test hardcode driver `sqlite3` alih-alih memakai `sqlite.DriverName`, jadi tidak menghormati tag `purego`. Plus `TestResolveDocumentMIME/Zip` yang gagal karena registry Windows mengembalikan `application/x-zip-compressed`. |

Aturannya: **bandingkan dengan baseline di atas, bukan dengan "semua hijau".**
Paket yang gagal harus tetap paket yang sama; kalau ada paket baru yang gagal,
itu regresi dari pekerjaan kita.

### Grup ber-prefiks kosong ikut menangkap rute yang didaftarkan setelahnya

Ditemukan saat verifikasi Fase 05.

`headerDeviceGroup := apiGroup.Group("", DeviceMiddleware, DeviceOwnerGuard)`
dibuat di `cmd/rest.go` SEBELUM rute config Chatwoot didaftarkan. Karena
prefiksnya kosong, middleware-nya ikut berjalan untuk rute yang didaftarkan
belakangan pada `apiGroup` — termasuk `/devices/:device_id/chatwoot/config`.

Akibatnya rute itu menuntut `X-Device-Id` meski device-nya sudah ada di path:

```
GET /devices/dev-b/chatwoot/config                  -> 400 DEVICE_ID_REQUIRED
GET /devices/dev-b/chatwoot/config  (X-Device-Id: ) -> 200
```

**Ini perilaku upstream, bukan akibat pekerjaan multi-tenant.** Diverifikasi
dengan `MULTI_TENANT_ENABLED=false`: hasilnya sama persis.

Dua konsekuensi:

1. Rute config Chatwoot ter-guard ganda — oleh `DeviceOwnerGuard` (karena
   tertangkap grup) dan oleh `GuardParamDevice` di dalam resolvernya. Tidak
   berbahaya, tapi jangan bingung saat membaca kodenya.
2. Saat memverifikasi rute yang didaftarkan setelah `headerDeviceGroup`,
   sertakan `X-Device-Id` — kalau tidak, yang terlihat adalah 400 dari
   `DeviceMiddleware`, bukan perilaku handler yang sedang diuji.

### `/ws` device-scoped: koneksi WebSocket butuh device yang resolve

Ditemukan saat verifikasi Fase 06.

`websocket.RegisterRoutes` dipanggil dari `registerDeviceScopedRoutes`, jadi
`/ws` berada di `headerDeviceGroup` dan melewati `DeviceMiddleware`. Koneksi
karena itu harus menyertakan device yang bisa diresolve — lewat query
`?device_id=` (WebSocket API browser tidak bisa mengirim header):

```
ws://host/ws?authorization=<base64>                    -> gagal (non-101) bila device tak resolve
ws://host/ws?authorization=<base64>&device_id=dev-a    -> 101
```

Perilaku upstream. Konsekuensi setelah Fase 04: koneksi juga melewati
`DeviceOwnerGuard`, sehingga operator tidak bisa membuka WebSocket dengan
`device_id` milik orang lain — diverifikasi, koneksinya ditolak sebelum upgrade.

Untuk menguji WebSocket, sertakan `device_id`; tanpa itu yang terlihat hanya
kegagalan upgrade dan mudah disalahartikan sebagai kegagalan autentikasi.

### `-race` tidak tersedia di lingkungan ini

`go test -race` menuntut cgo, dan cgo tidak tersedia (tidak ada gcc). Perintahnya
gagal dengan `-race requires cgo`.

Konsekuensinya, DoD Fase 06 yang mensyaratkan `go test -race ./ui/websocket/...`
**tidak bisa dipenuhi di mesin ini**. Yang harus dilakukan sebagai gantinya:

- pastikan setiap state bersama hanya disentuh dari satu goroutine (untuk hub
  WebSocket: hanya dari dalam `RunHub()`), dan buktikan lewat pembacaan kode,
- jalankan `-race` di lingkungan yang punya cgo kalau ada (CI Linux, misalnya),
- catat di PR bahwa race detector tidak dijalankan beserta alasannya.

Jangan menandai DoD `-race` sebagai lulus tanpa benar-benar menjalankannya.

### Rute yang tidak terdaftar TIDAK menjawab 404

Ditemukan saat verifikasi Fase 02, dan berlaku untuk seluruh fase.

`DeviceMiddleware` dipasang pada grup ber-prefiks kosong
(`apiGroup.Group("", middleware.DeviceMiddleware(dm))`), sehingga ia menangkap
**setiap** path yang tidak punya handler. Jadi path asing tidak menjawab 404
melainkan apa pun yang diputuskan middleware itu:

```
/admin/users          -> 400 DEVICE_ID_REQUIRED   (saat belum ada device)
/jalan-yang-tidak-ada -> 400 DEVICE_ID_REQUIRED
```

Ini perilaku upstream, bukan bug fork. Konsekuensinya untuk verifikasi:

- Untuk membuktikan sebuah rute **tidak terdaftar**, jangan cek status 404.
  Cek bahwa responsnya **bukan** dari handler yang bersangkutan — misalnya
  `code` bernilai `DEVICE_ID_REQUIRED`/`DEVICE_NOT_FOUND`, bukan `SUCCESS`.
- Keputusan K4 (cross-tenant dijawab 404) tetap berlaku dan tidak terpengaruh:
  angka 404 di situ ditulis eksplisit oleh guard kita, bukan diserahkan ke
  router.

Implikasi untuk **Fase 01**, yang butuh test repository terhadap SQLite
sungguhan: pakai `-tags purego`, dan di test baru gunakan
`sqlite.DriverName` — **jangan** menulis `"sqlite3"` sebagai literal, karena itu
persis yang membuat test lama gagal di lingkungan ini.

---

## Peta fase

| Fase | Judul | Tergantung | Aman deploy? | Status |
|------|-------|-----------|--------------|--------|
| [00](phase-00-fondasi.md) | Fondasi, feature flag, baseline | — | ya (no-op) | ✅ |
| [01](phase-01-skema-repository.md) | Skema DB, domain, repository tenancy | 00 | ya (tabel kosong) | ✅ |
| [02](phase-02-user-management.md) | User management + bootstrap admin | 01 | ya | ✅ |
| [03](phase-03-auth-session.md) | Auth gate: session cookie + Basic dari DB | 02 | ya | ✅ |
| [04](phase-04-device-ownership.md) | Kepemilikan device + guard + filter daftar | 03 | ya | ✅ |
| [05](phase-05-enforcement-rute.md) | Enforcement rute manual-resolve & agregat | 04 | ya | ✅ |
| [06](phase-06-websocket.md) | Isolasi WebSocket | 04 | ya | ✅ |
| [07](phase-07-mcp-permukaan-lain.md) | MCP, worker, webhook, audit lubang sisa | 05, 06 | ya | ✅ |
| [08](phase-08-ui-operator.md) | UI operator: login, `/custom/users` | 03 (idealnya 05) | ya | ⬜ |
| [09](phase-09-verifikasi-rollout.md) | Verifikasi end-to-end, dokumentasi, rollout | semua | ya | ⬜ |

Fase 06 dan 08 tidak saling bergantung; boleh diparalelkan setelah 04/05 selesai.

**Fase 04 dan 05 adalah inti keamanannya.** Fase 00–03 belum mengisolasi apa pun.
Jangan aktifkan `MULTI_TENANT_ENABLED=true` di produksi sebelum fase 05 selesai.

---

## Struktur file yang akan lahir

```text
src/
|-- config/settings.go                         # (mod kecil) flag MultiTenant*
|-- cmd/
|   |-- root.go                                # (mod kecil) binding flag/env
|   |-- rest.go                                # (mod kecil) wiring auth gate + guard
|   `-- multitenant.go                         # BARU: bootstrap admin + wiring helper
|-- domains/tenancy/
|   |-- tenancy.go                             # BARU: User, Role, Principal, Session, DeviceOwner
|   `-- interfaces.go                          # BARU: ITenancyRepository, ITenancyUsecase
|-- infrastructure/chatstorage/
|   |-- sqlite_repository.go                   # (mod kecil) append migration 55+
|   |-- sqlite_repository_tenancy.go           # BARU: implementasi ITenancyRepository
|   `-- sqlite_repository_tenancy_test.go      # BARU
|-- pkg/authhash/password.go                   # BARU: bcrypt hash/verify + token opaque
|-- usecase/tenancy.go                         # BARU: user CRUD, login, session
|-- ui/rest/
|   |-- auth.go                                # BARU: /auth/login, /auth/logout, /auth/me
|   |-- admin_users.go                         # BARU: /admin/users*
|   |-- admin_device_owner.go                  # BARU: /admin/devices/:device_id/owner
|   |-- custom_ui.go                           # (mod kecil) daftarkan halaman baru
|   `-- assets/
|       |-- login_ui.html                      # BARU
|       `-- users_ui.html                      # BARU
|-- ui/rest/middleware/
|   |-- authgate.go                            # BARU: cookie|basic -> principal
|   |-- principal.go                           # BARU: helper baca principal dari ctx
|   `-- device_owner_guard.go                  # BARU: guard kepemilikan device
`-- ui/websocket/websocket.go                  # (mod terukur) principal + filter broadcast
```

---

---

## Hasil audit Fase 07

Inventaris lengkap seluruh permukaan, dijalankan di Fase 07. Setiap baris
terklasifikasi; tidak ada yang berstatus "belum diperiksa".

### Iterasi seluruh registry device

| Lokasi | Status |
|--------|--------|
| `ui/rest/device.go` → `ListDevices` | difilter (Fase 04) |
| `ui/rest/app.go` → `FetchDevices` | difilter (Fase 04) |
| `ui/websocket/websocket.go` → `FETCH_DEVICES` | difilter + hanya ke peminta (Fase 06) |
| `usecase/app.go` → `Logout` | daftar device hanya di mode single-tenant (Fase 06) |
| `usecase/device.go` → `LogoutDevice` | idem |
| `usecase/device.go` → `ListDevices` | difilter di handler (Fase 04) |
| `infrastructure/whatsapp/message_queue_worker.go` | **tidak perlu guard** — worker, device ditentukan baris `message_queue` yang hanya bisa dibuat lewat endpoint ber-guard |
| `infrastructure/whatsapp/presence_pulse.go` | **tidak perlu guard** — worker per device instance |
| `ui/rest/helpers/common.go` → `SetAutoConnectAfterBooting` | **tidak perlu guard** — tugas startup, menyambungkan ulang semua device tanpa pemanggil HTTP |
| `infrastructure/whatsapp/device_manager.go` → `LoadExistingDevices` | pemuatan startup |

### Config lintas device

`GET /command/configs` dan `GET /chatwoot/configs` difilter (Fase 05).

### Permukaan non-HTTP

Dikonfirmasi **tidak** butuh guard: worker antrian, presence pulse, command
handler `!`, forward webhook, dan Chatwoot sync service. Batas tenant ada di
pintu masuk (HTTP/MCP/WebSocket), bukan di eksekusi. Menambahkan guard di sana
justru akan mematikan worker, karena mereka tidak punya principal.

### Statistik global

`GetTotalMessageCount`, `GetTotalChatCount`, dan `GetStorageStatistics` hanya
dipakai internal untuk logging saat truncate. **Tidak ada endpoint REST yang
mengeksposnya**, jadi tidak ada kebocoran volume antar tenant.

### Rute publik (di luar auth gate)

| Rute | Status |
|------|--------|
| `GET /health` | aman — hanya mengembalikan `OK` / `Service Unavailable`, tanpa data tenant |
| `POST {webhookPath}` dan `POST {webhookPath}/:device_id` | sengaja publik; wajib `CHATWOOT_WEBHOOK_SECRET` (ada peringatan startup) |
| Rute discovery/token OAuth MCP | sengaja publik |
| `POST /auth/login`, `POST /auth/logout` | sengaja publik |

### Dead code yang perlu diawasi

`serviceApp.FirstDevice` mengembalikan device PERTAMA di registry global. Saat
audit ini **tidak ada pemanggilnya** — hanya deklarasi interface dan
implementasinya — jadi bukan kebocoran aktif. Tapi kalau nanti ada handler yang
memakainya, ia akan mengembalikan device milik siapa pun. Periksa ulang setiap
sync upstream.

## Glosarium

- **Principal** — identitas pemanggil yang sudah terautentikasi:
  `{UserID, Username, Role}`. Disimpan di `c.Locals("principal")`.
- **Owner** — user yang memiliki sebuah device slot (satu baris di `device_owner`).
- **Device slot / device id** — id device yang dipakai klien lewat `X-Device-Id`.
  Bukan JID; bertahan melewati logout/re-login.
- **Break-glass** — kredensial `APP_BASIC_AUTH` yang tetap berlaku selama
  username-nya belum ada di `app_user`, supaya operator tidak pernah terkunci.
