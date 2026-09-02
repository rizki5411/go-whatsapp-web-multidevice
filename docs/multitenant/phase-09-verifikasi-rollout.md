# Fase 09 — Verifikasi End-to-End, Dokumentasi, Rollout

**Tujuan:** membuktikan isolasinya benar-benar bekerja lewat matriks uji yang
menyeluruh, memperbarui dokumen project, dan menyalakan flag di produksi dengan
langkah yang jelas termasuk cara mundur.

**Tergantung:** semua fase sebelumnya
**Branch:** `feature/multitenant-phase09`

---

## Bagian A — Matriks verifikasi

Siapkan lingkungan uji: flag on, tiga akun (`admin`, `op1`, `op2`), tiga device
(`dev-a` milik op1, `dev-b` milik op2, `dev-orphan` tanpa owner).

Setiap baris harus dijalankan dan hasilnya dicatat. **Baris yang tidak
dijalankan dihitung gagal** — matriks yang setengah diisi lebih buruk daripada
tidak ada matriks, karena memberi rasa aman yang salah.

### A1. Daftar dan penemuan

| # | Aksi | Pelaku | Harapan |
|---|------|--------|---------|
| 1 | `GET /devices` | op1 | hanya `dev-a` |
| 2 | `GET /devices` | op2 | hanya `dev-b` |
| 3 | `GET /devices` | admin | ketiganya |
| 4 | `GET /app/devices` | op1 | hanya `dev-a` |
| 5 | `GET /command/configs` | op1 | hanya config `dev-a` |
| 6 | `GET /chatwoot/configs` | op1 | hanya config `dev-a` |

### A2. Akses lintas tenant (semua harus 404, badan identik)

| # | Aksi | Pelaku |
|---|------|--------|
| 7 | `GET /app/status` dengan `X-Device-Id: dev-b` | op1 |
| 8 | `POST /send/message` dengan `X-Device-Id: dev-b` | op1 |
| 9 | `GET /chats` dengan `X-Device-Id: dev-b` | op1 |
| 10 | `GET /devices/dev-b` | op1 |
| 11 | `GET /devices/dev-b/status` | op1 |
| 12 | `GET /devices/dev-b/login` (QR) | op1 |
| 13 | `POST /devices/dev-b/logout` | op1 |
| 14 | `DELETE /devices/dev-b` | op1 |
| 15 | `GET /devices/dev-b/command/config` | op1 |
| 16 | `PUT /devices/dev-b/command/config` | op1 |
| 17 | `GET /devices/dev-b/queue` | op1 |
| 18 | `DELETE /devices/dev-b/queue/<id milik dev-b>` | op1 |
| 19 | `GET /devices/dev-b/chatwoot/config` | op1 |
| 20 | `PATCH /devices/dev-b/webhook` | op1 |
| 21 | `GET /devices/dev-orphan/status` | op1 |
| 22 | `GET /admin/users` | op1 |
| 23 | `PUT /admin/devices/dev-a/owner` | op1 |
| 24 | `GET /custom/users` | op1 |

Untuk #13 dan #14, **verifikasi juga bahwa `dev-b` masih ada dan masih login**
setelah percobaan itu. Response 404 yang benar tapi efek samping yang tetap
terjadi adalah kegagalan terburuk yang mungkin di fitur ini.

### A3. Fallback tanpa `X-Device-Id`

| # | Kondisi | Pelaku | Harapan |
|---|---------|--------|---------|
| 25 | punya 1 device | op1 | dipakai device sendiri, 200 |
| 26 | punya 0 device | op3 (baru) | 404 |
| 27 | punya 2 device | op1 (+`dev-c`) | 400 `DEVICE_ID_REQUIRED` |
| 28 | admin | admin | perilaku default lama, 200 |

Untuk #25, buktikan device yang **benar-benar** dipakai, bukan cuma status 200:
kirim pesan dan periksa dari device mana pesan itu keluar.

### A4. Kuota dan siklus hidup

| # | Aksi | Harapan |
|---|------|---------|
| 29 | op1 (`device_limit: 1`, sudah punya 1) `POST /devices` | 403 `DEVICE_LIMIT_REACHED`, tidak ada sesi WhatsApp baru |
| 30 | op1 (`device_limit: 0`) `POST /devices` ×3 | ketiganya berhasil dan ter-klaim |
| 31 | admin hapus op1 | device op1 jadi tak-ber-owner, **tidak** terhapus |
| 32 | admin `PUT owner` `dev-orphan` ke op2 | op2 langsung bisa mengaksesnya (cache ter-invalidasi) |
| 33 | admin `DELETE owner` `dev-a` | op1 langsung kehilangan akses |

### A5. Session dan pencabutan

| # | Aksi | Harapan |
|---|------|---------|
| 34 | admin ganti password op1 | cookie op1 mati, Basic lama op1 gagal (dalam <1 detik, bukan 60) |
| 35 | admin nonaktifkan op1 | cookie dan Basic op1 langsung gagal |
| 36 | op1 `POST /auth/logout` | cookie itu mati, cookie perangkat lain op1 masih hidup |
| 37 | session lewat TTL | ditolak dan barisnya tersapu |

#34 dan #35 adalah uji cache invalidation dari fase 03. Kalau butuh sampai 60
detik, cache-nya belum ter-invalidasi dan itu bug keamanan.

### A6. WebSocket

| # | Aksi | Harapan |
|---|------|---------|
| 38 | op1 dan op2 tersambung; login `dev-a` | QR hanya di koneksi op1 |
| 39 | pesan masuk ke `dev-b` | event hanya di koneksi op2 |
| 40 | op1 kirim `FETCH_DEVICES` | hanya op1 yang menerima `LIST_DEVICES`, isinya hanya `dev-a` |
| 41 | admin tersambung | menerima event kedua device |

### A7. MCP

| # | Aksi | Harapan |
|---|------|---------|
| 42 | op1 `tools/list` lalu tool tanpa device id | pakai `dev-a` |
| 43 | op1 tool dengan device id `dev-b` | error identik device-tidak-ada |
| 44 | `/.well-known/oauth-authorization-server` tanpa auth | 200 |

### A8. Regresi mode single-tenant

Matikan flag, jalankan ulang sanity check ini. **Semua harus berperilaku seperti
sebelum proyek ini dimulai.**

| # | Aksi | Harapan |
|---|------|---------|
| 45 | `GET /devices` dengan kredensial env | semua device |
| 46 | request tanpa `X-Device-Id` | jatuh ke default device |
| 47 | tidak ada rute `/auth/*`, `/admin/*`, `/custom/users` | 404 |
| 48 | payload WebSocket | identik dengan build sebelum fase 06 |
| 49 | dashboard gowa-ui | normal dengan Basic Auth |
| 50 | webhook Chatwoot | tetap menerima tanpa kredensial |

---

## Bagian B — Uji otomatis

Selain matriks manual, tambahkan satu test integrasi yang mengunci perilaku inti
supaya tidak bisa mundur tanpa disadari. Taruh di
`src/ui/rest/multitenant_integration_test.go`:

- bangun `fiber.App` dengan susunan middleware yang sama dengan `rest.go`
- repository tenancy asli dengan SQLite sementara
- `DeviceManager` dengan dua device palsu
- assert: matriks A2 (cross-tenant → 404) dan A3 (fallback) sebagai table test

Ini test paling berharga di seluruh proyek: dia yang akan menangkap regresi saat
sync upstream berikutnya menambah rute baru.

Jalankan juga:

```bash
cd src && go test -race ./... 2>&1 | tail -30
```

---

## Bagian C — Dokumentasi

### C1. `CLAUDE.md` (root)

Tambahkan di bagian "Status kustomisasi saat ini":

```markdown
- [x] Multi-tenant: isolasi device per user + user management (role admin/operator).
      Flag `MULTI_TENANT_ENABLED`. Domain `src/domains/tenancy/`, repo
      `sqlite_repository_tenancy.go` (tabel `app_user`, `device_owner`,
      `user_session`; migration 55-61), auth gate
      `src/ui/rest/middleware/authgate.go`, guard
      `src/ui/rest/middleware/device_owner_guard.go`, API `src/ui/rest/auth.go`
      + `admin_users.go`, UI `/custom/login`, `/custom/users`, `/custom/devices`.
      Rencana & rasional per fase: `docs/multitenant/`.
```

Tambahkan juga satu aturan di "Pola yang WAJIB diikuti untuk fitur baru":

```markdown
- Endpoint baru yang device-scoped: kalau memakai header/query `device_id`,
  daftarkan di `headerDeviceGroup` (otomatis ter-guard). Kalau memakai path
  param `:device_id`, WAJIB memanggil `tenantfilter.GuardParamDevice` —
  lihat `docs/multitenant/phase-05-enforcement-rute.md`.
```

Aturan terakhir ini yang mencegah fitur berikutnya membuka lubang baru.

### C2. `AGENTS.md`

Tambahkan baris di tabel "WHERE TO LOOK":

```markdown
| Multi-tenant / kepemilikan device | `src/domains/tenancy/`, `src/ui/rest/middleware/authgate.go`, `device_owner_guard.go`, `src/usecase/tenancy.go` | Di belakang `MULTI_TENANT_ENABLED`. Rute `:device_id` wajib lewat `tenantfilter.GuardParamDevice`. |
```

Perbarui juga jumlah migration di baris "Add DB migration" (di AGENTS.md masih
tertulis 52; setelah fase 01 jadi 61).

### C3. `docs/multitenant/OPERASI.md` (baru)

Panduan operator, bukan pengembang:

- cara menambah user dan menetapkan device
- arti `device_limit`
- apa yang terjadi kalau user dihapus
- cara memulihkan akses kalau admin terkunci (break-glass lewat
  `APP_BASIC_AUTH` dengan username yang belum ada di `app_user`)
- known limitations (lihat Bagian E)

### C4. `src/.env.example`

Pastikan keempat key `MULTI_TENANT_*` terdokumentasi, plus catatan bahwa
`CHATWOOT_WEBHOOK_SECRET` menjadi **wajib** kalau Chatwoot dipakai bersama mode
multi-tenant.

---

## Bagian D — Rollout

Urutan ini penting; jangan diacak.

**D1. Sebelum menyalakan**

- [ ] Backup `storages/chatstorage.db` dan `storages/whatsapp.db`.
- [ ] Catat daftar device yang ada sekarang beserta pemilik yang dimaksudkan
      (di luar sistem — spreadsheet, catatan, apa pun). Ini yang akan
      dimasukkan setelah flag nyala.
- [ ] Pastikan `APP_BASIC_AUTH` berisi minimal satu kredensial yang kamu pegang.
      Ini yang akan jadi admin pertama.
- [ ] Kalau Chatwoot aktif: set `CHATWOOT_WEBHOOK_SECRET`.

**D2. Menyalakan**

- [ ] Set `MULTI_TENANT_ENABLED=true`, restart.
- [ ] Verifikasi log: seeding admin berjalan, migration tanpa error.
- [ ] Login sebagai admin lewat `/custom/login`.
- [ ] Buka `/custom/devices` — **semua device lama akan tampil tak-ber-owner.**
      Ini normal dan disengaja (lihat fase 04).
- [ ] Tetapkan owner untuk setiap device. Sampai langkah ini selesai, operator
      tidak melihat apa pun.
- [ ] Buat akun operator, set `device_limit`.
- [ ] Ganti password admin dari nilai env lewat `/custom/users`.
- [ ] Jalankan sanity check A1, A2 (beberapa baris), A3, A6.

**D3. Kalau harus mundur**

Set `MULTI_TENANT_ENABLED=false` dan restart. Itu saja.

Tabel `app_user`, `device_owner`, dan `user_session` tetap ada tapi tidak dibaca;
Basic Auth kembali memvalidasi ke `APP_BASIC_AUTH`; tidak ada guard yang aktif.
**Tidak ada migration yang perlu dibalik**, dan data device tidak tersentuh.
Menyalakannya lagi nanti akan menemukan kepemilikan yang sudah ditetapkan masih
utuh.

Inilah alasan flag itu ada, dan kenapa setiap fase wajib mempertahankan jalur
flag-off. Kalau di titik mana pun rollback tidak lagi sesederhana ini, ada fase
yang melanggar K1 dan itu harus diperbaiki sebelum rilis.

---

## Bagian E — Known limitations (tulis di `OPERASI.md`)

Batasan yang **disengaja**, bukan bug. Sebutkan terus terang; batasan yang tidak
didokumentasikan akan ditemukan pada saat yang paling tidak menyenangkan.

1. **Satu database, isolasi di lapisan aplikasi.** Semua tenant berbagi
   `chatstorage.db`. Satu bug filter berarti kebocoran. Kalau yang dibutuhkan
   isolasi keras antar organisasi yang berbeda, jalannya adalah satu proses per
   tenant (opsi B), bukan ini.
2. **Token MCP OAuth tidak tercabut saat password diganti.** Token yang sudah
   terbit tetap berlaku sampai kedaluwarsa. Jalur perbaikan ada di catatan
   fase 07.
3. **Webhook Chatwoot tidak terautentikasi kecuali `CHATWOOT_WEBHOOK_SECRET`
   diisi.** Ini perilaku upstream. Di mode multi-tenant, secret itu wajib.
4. **Break-glass lewat `APP_BASIC_AUTH` selalu admin.** Untuk mematikannya,
   kosongkan `APP_BASIC_AUTH` setelah admin DB dibuat — bukan menghapus barisnya
   dari `app_user`.
5. **Media di `statics/` tidak dipisah per tenant.** Path-nya mengandung nama
   file yang sulit ditebak, tapi tidak ada kontrol akses per file. Kalau ini
   penting, itu pekerjaan terpisah.
6. **Statistik penyimpanan global** (kalau ada endpoint yang mengembalikannya)
   dibatasi ke admin, bukan dipecah per tenant. Lihat audit fase 07 Bagian D.
7. **Dashboard gowa-ui tidak tahu soal role.** Dia hanya menampilkan apa yang
   API kembalikan. Operator tidak akan melihat elemen admin karena API-nya
   menjawab 404, bukan karena UI-nya menyembunyikan.

---

## Definition of Done

- [ ] Matriks A1–A8 (50 baris) dijalankan seluruhnya dan hasilnya dilampirkan.
      Tidak ada baris kosong.
- [ ] Test integrasi `multitenant_integration_test.go` ada dan hijau.
- [ ] `cd src && go test -race ./...` hijau.
- [ ] `CLAUDE.md`, `AGENTS.md`, `src/.env.example` diperbarui.
- [ ] `docs/multitenant/OPERASI.md` ada, termasuk Bagian E.
- [ ] Semua kolom Status di `docs/multitenant/README.md` sudah ✅.
- [ ] Rollback diuji sungguhan: flag dimatikan, aplikasi jalan normal, lalu
      dinyalakan lagi dan kepemilikan device masih utuh.
- [ ] Backup DB sudah diambil sebelum menyalakan di produksi.

---

## Catatan penutup

Setelah fase ini, aturan untuk pekerjaan selanjutnya:

- Setiap endpoint baru yang menerima `device_id` dari pemanggil **wajib**
  melewati salah satu dari dua guard (`headerDeviceGroup` atau
  `GuardParamDevice`). Ini sudah masuk `CLAUDE.md` di Bagian C1.
- Setiap sync upstream: jalankan ulang inventaris `grep` dari fase 05 Bagian
  Prasyarat, plus test integrasi. Rute baru dari upstream **tidak** otomatis
  ter-guard kalau memakai path param.
- Setiap broadcast WebSocket baru harus mengisi `DeviceID`, atau hanya akan
  sampai ke admin (fase 06 keputusan yang dikunci).
