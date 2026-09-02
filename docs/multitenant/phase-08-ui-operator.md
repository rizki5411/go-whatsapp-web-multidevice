# Fase 08 — UI Operator: Login, Manajemen User, Kepemilikan Device

**Tujuan:** operator dan admin bisa mengurus akun tanpa `curl`. Tiga halaman
baru di bawah `/custom`, plus penanda user yang sedang login.

**Tergantung:** Fase 03 (idealnya setelah 05, supaya data yang ditampilkan sudah
terfilter)
**Branch:** `feature/multitenant-phase08`

Bisa dikerjakan paralel dengan fase 06 dan 07.

---

## Prasyarat

Baca `src/ui/rest/custom_ui.go` seluruhnya — komentar di atas file itu sudah
menjelaskan kontraknya, dan kontrak itu wajib dipatuhi:

1. Halaman di-embed ke binary (`//go:embed assets/...`), **bukan** dilayani dari
   disk. Dashboard utama (gowa-ui) di-download runtime dan ditimpa auto-update,
   jadi apa pun yang ditaruh di sana akan hilang.
2. Setiap halaman **self-contained**: tanpa font eksternal, tanpa CDN, tanpa
   script dari luar. Server ini sering di-host tanpa akses internet keluar, dan
   halaman admin yang butuh CDN adalah halaman admin yang mati justru saat paling
   dibutuhkan.
3. API root diturunkan dengan **memotong path halaman itu sendiri**, supaya tetap
   jalan kalau `APP_BASE_PATH` diisi. Lihat pola di `assets/queue_ui.html`
   (komentar "Served at {basePath}/custom/queue, so strip that suffix").
4. `Cache-Control: no-store` — lewat `sendConsolePage`, sudah ditangani.

Lihat juga `assets/custom_index.html` untuk gaya kartu navigasi yang sudah ada,
dan ikuti tampilannya. Halaman baru harus terasa satu keluarga dengan
`/custom/command` dan `/custom/queue`, bukan seperti tempelan.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/ui/rest/assets/login_ui.html` | **BARU** | form login |
| `src/ui/rest/assets/users_ui.html` | **BARU** | manajemen user (admin) |
| `src/ui/rest/assets/devices_owner_ui.html` | **BARU** | penetapan owner device (admin) |
| `src/ui/rest/assets/custom_index.html` | modifikasi (fork) | kartu baru + identitas user |
| `src/ui/rest/custom_ui.go` | modifikasi (fork) | embed + daftarkan rute |
| `src/ui/rest/auth.go` | modifikasi (fork) | endpoint ganti password sendiri |

---

## Detail implementasi

### 1. Rute halaman

| Path | Auth | Isi |
|------|------|-----|
| `/custom/login` | **publik** | form login; wajib di luar auth gate |
| `/custom/users` | admin | daftar/tambah/ubah/hapus user |
| `/custom/devices` | admin | daftar device + owner-nya, penetapan owner |

`/custom/login` harus didaftarkan di jalur publik yang sama dengan
`POST /auth/login` (fase 03, `InitRestAuthPublic`), **bukan** lewat
`InitRestCustomUI` yang berada di belakang gate. Kalau tidak, halaman login
sendiri akan meminta Basic Auth dan gunanya hilang.

Konsekuensi struktural: `custom_ui.go` meng-embed `login_ui.html` tapi rutenya
didaftarkan dari `auth.go`. Ekspor variabelnya (`LoginPage`) atau sediakan
fungsi `ServeLoginPage(c fiber.Ctx) error` di `custom_ui.go` yang dipanggil dari
jalur publik. Yang kedua lebih rapi — `sendConsolePage` tetap jadi satu-satunya
jalan menulis halaman.

`/custom/users` dan `/custom/devices` didaftarkan di `InitRestCustomUI` dengan
`middleware.RequireAdmin()`. Karena `RequireAdmin` menjawab 404 untuk non-admin
(K4), operator yang menebak URL-nya hanya melihat halaman tidak ditemukan.

Semua rute baru dibungkus `if config.MultiTenantEnabled` — di mode single-tenant
halaman ini tidak punya arti dan tidak boleh muncul.

### 2. `login_ui.html`

- Form `POST` ke `{apiRoot}/auth/login` via `fetch`, bukan submit HTML biasa,
  supaya error bisa ditampilkan tanpa reload.
- Sukses → redirect ke `{apiRoot}/` (dashboard gowa-ui) atau ke `?next=` kalau
  ada. **Validasi `next`**: hanya terima path relatif yang dimulai dengan `/`
  dan tidak dimulai dengan `//`. Tanpa validasi ini, `?next=//evil.example`
  menjadikan halaman login kita open redirect.
- Gagal → tampilkan pesan generik ("username atau password salah"). Jangan
  membedakan antara user tidak ada dan password salah, konsisten dengan keputusan
  di fase 02/03.
- Kalau kena rate limit (429), tampilkan pesan bahwa percobaan terlalu sering dan
  minta tunggu.
- `autocomplete="username"` dan `autocomplete="current-password"` supaya password
  manager bekerja normal.
- Jangan menyimpan apa pun di `localStorage`. Session ada di cookie HttpOnly;
  menyalin identitas ke `localStorage` hanya menambah permukaan XSS tanpa manfaat.

### 3. `users_ui.html`

Tabel: username, nama tampilan, role, limit device, jumlah device terpakai,
status aktif, dibuat. Aksi: tambah, ubah, reset password, aktif/nonaktif, hapus.

Yang harus benar:

- Kolom "device terpakai / limit" (`3 / 5`, atau `3 / ∞` untuk limit 0). Ini
  informasi yang paling sering dibutuhkan admin, dan tanpa itu `device_limit`
  jadi angka buta.
- Hapus user: konfirmasi eksplisit, dan **jelaskan di dialognya** bahwa device
  miliknya tidak terhapus melainkan menjadi tak-ber-owner dan hanya akan terlihat
  oleh admin. Perilaku itu benar (fase 04) tapi mengejutkan kalau tidak
  diberitahu.
- Tombol yang menyentuh admin terakhir: biarkan API yang menolak
  (`LAST_ADMIN_PROTECTED`), lalu tampilkan pesannya. Jangan menduplikasi aturan
  invariant itu di JavaScript — dua sumber kebenaran akan menyimpang.
- Reset password: minta minimal 8 karakter di sisi klien sebagai kenyamanan, tapi
  aturan sebenarnya tetap milik `authhash.HashPassword`.
- Setelah reset password atau nonaktifkan, tampilkan catatan bahwa semua session
  user itu sudah dicabut.

### 4. `devices_owner_ui.html`

Halaman ini yang membuat rollout mungkin dilakukan tanpa `curl`. Isinya:

- Daftar semua device (`GET /devices` sebagai admin) dengan kolom owner.
- Device tanpa owner **ditandai jelas** — beri label dan pisahkan ke bagian atas.
  Saat pertama menyalakan flag, semua device lama ada di kondisi ini, dan halaman
  ini adalah tempat admin menyelesaikannya.
- Dropdown user untuk menetapkan owner → `PUT /admin/devices/:device_id/owner`.
- Tombol lepas owner → `DELETE`.
- Tampilkan jumlah device tak-ber-owner sebagai angka menonjol, supaya admin tahu
  pekerjaannya belum selesai.

### 5. `custom_index.html` (modifikasi)

- Tambah dua kartu (`custom/users`, `custom/devices`), hanya dirender kalau
  `GET /auth/me` mengembalikan `is_admin: true`.
- Tampilkan `username` dan `role` yang sedang login di sudut, dengan tombol
  keluar yang memanggil `POST /auth/logout` lalu redirect ke `/custom/login`.
- Kalau `/auth/me` menjawab 401 → redirect ke `/custom/login?next=` path
  sekarang.
- Kalau `/auth/me` menjawab 404 (mode single-tenant, rute tidak terdaftar) →
  jangan tampilkan apa pun yang berkaitan dengan user. Halaman harus tetap utuh
  di mode single-tenant.

Poin terakhir itu mudah terlewat: `custom_index.html` dilayani di kedua mode,
jadi seluruh JavaScript yang berhubungan dengan identitas harus menangani
"endpoint tidak ada" dengan diam, bukan dengan error di konsol.

### 6. Endpoint ganti password sendiri

Ditunda dari fase 02, kerjakan di sini:

| Method | Path | Auth | Body |
|--------|------|------|------|
| POST | `/auth/password` | perlu principal | `{"current_password": "...", "new_password": "..."}` |

Aturan:

- Wajib memverifikasi `current_password`, meskipun principal-nya sudah
  terautentikasi. Cookie yang dicuri tidak boleh cukup untuk mengambil alih akun
  secara permanen.
- Setelah sukses, cabut semua session user itu **kecuali** yang sedang dipakai,
  lalu terbitkan cookie baru. Kalau menyertakan pengecualian itu terlalu rumit,
  cabut semuanya dan langsung terbitkan session baru untuk request ini — hasil
  akhirnya sama dan kodenya lebih sederhana.
- Principal break-glass (`UserID == 0`) tidak punya baris `app_user`, jadi
  tidak bisa ganti password. Tolak dengan pesan yang menjelaskan bahwa kredensial
  itu berasal dari `APP_BASIC_AUTH` dan harus diubah di konfigurasi server.
- Invalidasi cache verifikasi Basic (fase 03).

Tambahkan tombol ganti password untuk semua role di `custom_index.html`.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] Ketiga halaman baru **tidak memuat satu pun** referensi eksternal.
      Buktikan:
      ```bash
      cd src && grep -nE "https?://|//cdn|fonts\.googleapis" ui/rest/assets/login_ui.html ui/rest/assets/users_ui.html ui/rest/assets/devices_owner_ui.html
      ```
      harus tidak ada hasil.
- [ ] `/custom/login` bisa dibuka **tanpa** kredensial (200, bukan 401).
- [ ] `/custom/users` dan `/custom/devices` → 404 untuk operator, 200 untuk admin.
- [ ] Semua halaman baru jalan dengan `APP_BASE_PATH=/gowa` — test manual, karena
      inilah yang paling sering rusak pada halaman self-contained.
- [ ] `?next=//evil.example` **tidak** melakukan redirect keluar (ada test, atau
      minimal verifikasi manual yang dicatat).
- [ ] Alur penuh berhasil di browser tanpa satu pun `curl`: login sebagai admin →
      buat operator → tetapkan owner device → logout → login sebagai operator →
      hanya melihat device sendiri di dashboard gowa-ui.
- [ ] Mode single-tenant: `/custom` dan `/custom/command` dan `/custom/queue`
      tetap normal, tanpa error di konsol browser, tanpa elemen identitas user.
- [ ] `POST /auth/password` jalan, menolak `current_password` yang salah, dan
      menolak principal break-glass.

## Verifikasi

```bash
cd src && go build ./... && go test ./ui/rest/... -v
```

Halaman login publik:

```bash
curl -s -o /dev/null -w '%{http_code}\n' localhost:3000/custom/login
```

Operator tidak melihat area admin:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -u operator1:rahasia123 localhost:3000/custom/users
```

Tanpa referensi eksternal:

```bash
cd src && grep -cE "https?://" ui/rest/assets/login_ui.html ui/rest/assets/users_ui.html ui/rest/assets/devices_owner_ui.html
```

---

## Catatan & jebakan

- **Dashboard utama (gowa-ui) tidak boleh disentuh.** File itu di-download saat
  runtime ke `storages/ui` dan ditimpa auto-update. Semua kebutuhan UI fork ada
  di `/custom`. Kalau ada yang harus terlihat di dashboard, jalannya adalah
  memfilter API-nya (sudah dilakukan di fase 04–05), bukan mengedit HTML-nya.
- gowa-ui bekerja dengan cookie session tanpa perubahan apa pun, karena
  `fetch`-nya same-origin dan cookie terkirim otomatis. Jadi setelah login lewat
  `/custom/login`, dashboard langsung bisa dipakai tanpa prompt Basic Auth.
  Verifikasi ini dan catat hasilnya — inilah yang membuat pengalaman multi-user
  terasa wajar.
- Jangan menambahkan library JS. Halaman `/custom` yang ada ditulis dengan
  JavaScript biasa; ikuti itu.
- Jangan menampilkan hash password di mana pun, termasuk di response debug atau
  di atribut `data-*`.
- Kalau butuh referensi visual, `assets/queue_ui.html` adalah yang paling
  kompleks di antara halaman yang ada (tabel + aksi + polling) dan paling dekat
  dengan kebutuhan `users_ui.html`.
