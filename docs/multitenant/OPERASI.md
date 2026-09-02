# Panduan Operasi — Mode Multi-Tenant

Untuk operator dan admin. Rancangan teknis dan alasan di balik setiap keputusan
ada di [README.md](README.md); halaman ini soal memakainya.

---

## Menyalakan pertama kali

Urutannya penting. Jangan diacak.

**1. Backup dulu.**

```bash
cp storages/chatstorage.db storages/chatstorage.db.bak
cp storages/whatsapp.db storages/whatsapp.db.bak
```

**2. Catat rencana kepemilikan device di luar sistem.** Daftar device yang ada
sekarang, dan siapa yang seharusnya memilikinya. Ini yang akan dimasukkan
setelah flag menyala.

```bash
curl -u admin:PASSWORD http://localhost:3000/devices
```

**3. Pastikan `APP_BASIC_AUTH` berisi kredensial yang Anda pegang.** Kredensial
itu akan menjadi admin pertama.

**4. Kalau Chatwoot dipakai, set `CHATWOOT_WEBHOOK_SECRET`.** Tanpa itu webhook
Chatwoot tidak terautentikasi, dan siapa pun yang bisa menjangkau port ini bisa
menyuntikkan pesan ke device tenant mana pun. Aplikasi memperingatkan di log
saat startup, tapi tidak menolak jalan.

**5. Nyalakan.**

```dotenv
MULTI_TENANT_ENABLED=true
```

Restart, lalu periksa log. Yang harus terlihat:

```
[MULTITENANT] 1 admin disemai dari APP_BASIC_AUTH
[MULTITENANT] mode multi-tenant aktif
```

**6. Login sebagai admin** di `/custom/login`.

**7. Buka `/custom/devices`.** Semua device lama akan muncul **tanpa pemilik**.
Itu disengaja, bukan bug — lihat "Kenapa device lama tidak terlihat" di bawah.
Tetapkan pemiliknya satu per satu sampai penghitung di atas mencapai nol.

**8. Buat akun operator** di `/custom/users`. Set `device_limit` kalau perlu.

**9. Ganti password admin** dari nilai env, lewat tombol "Ganti password" di
`/custom`.

**10. Verifikasi.** Ada skrip yang menjalankan seluruh matriks pemeriksaan
terhadap instalasi uji terpisah (tidak menyentuh `storages/` Anda):

```bash
cd src && go build -tags purego -o /tmp/gowa.exe . && cd .. && python docs/multitenant/verify_matrix.py --binary /tmp/gowa.exe
```

---

## Kalau harus mundur

```dotenv
MULTI_TENANT_ENABLED=false
```

Restart. **Itu saja.**

Tabel `app_user`, `device_owner`, dan `user_session` tetap ada tapi tidak
dibaca. Basic Auth kembali memvalidasi ke `APP_BASIC_AUTH`, dan tidak ada guard
yang aktif. **Tidak ada migration yang perlu dibalik**, dan data device tidak
tersentuh. Menyalakannya lagi nanti akan menemukan kepemilikan yang sudah
ditetapkan masih utuh.

Kalau di titik mana pun rollback tidak lagi sesederhana ini, ada yang salah —
laporkan, jangan diakali.

---

## Peran

|                                   | Operator | Admin |
|-----------------------------------|:--------:|:-----:|
| Melihat device miliknya           | ya       | ya    |
| Melihat device orang lain         | tidak    | ya    |
| Melihat device tanpa pemilik      | tidak    | ya    |
| Membuat device baru               | ya (sesuai batas) | ya |
| Menghapus device miliknya         | ya       | ya    |
| Mengelola akun user               | tidak    | ya    |
| Menetapkan pemilik device         | tidak    | ya    |
| Ganti password sendiri            | ya       | ya    |

Operator yang meminta device milik orang lain mendapat **404**, sama persis
dengan device yang benar-benar tidak ada. Itu disengaja: 403 akan
mengonfirmasi bahwa device tersebut eksis.

---

## Hal yang sering ditanyakan

### Kenapa device lama tidak terlihat setelah flag dinyalakan?

Karena belum ada pemiliknya, dan **device tanpa pemilik hanya terlihat admin**.

Default yang ketat ini dipilih dengan sengaja. Kalau defaultnya permisif, setiap
device yang gagal ter-klaim — karena bug, karena migrasi setengah jalan — akan
otomatis terbuka untuk semua orang. Dengan default ketat, gejalanya "device saya
hilang" (kelihatan langsung) bukan "device saya dilihat orang lain" (tidak
kelihatan sampai terlambat).

Selesaikan di `/custom/devices`.

### `device_limit` itu apa?

Batas jumlah device yang boleh dimiliki satu user. **0 berarti tanpa batas.**
Diperiksa sebelum device dibuat, jadi penolakan tidak meninggalkan device yatim.

Di `/custom/users` kolom Device menampilkan `terpakai / batas`, misalnya `1 / 2`
atau `3 / ∞`.

### Apa yang terjadi kalau user dihapus?

Akun, session, dan baris kepemilikannya hilang. **Device-nya TIDAK dihapus** —
device itu menjadi tanpa pemilik dan hanya terlihat admin sampai ditetapkan
lagi.

Menghapus device berarti memutus sesi WhatsApp dan membuang riwayat chat-nya.
Itu harus tetap keputusan eksplisit, bukan efek samping menghapus akun.

### Ganti password langsung berlaku?

Ya, seketika. Mengganti password atau menonaktifkan user langsung mencabut
seluruh session-nya **dan** membatalkan cache verifikasi Basic Auth. Tidak ada
jeda.

Koneksi WebSocket yang sedang terbuka ikut **diputus** — begitu juga saat role
diturunkan atau user dihapus. Itu memang terlihat: dashboard yang sedang
terbuka kehilangan koneksi realtime-nya dan menyambung ulang (kalau haknya
memang sudah dicabut, penyambungan ulangnya ditolak). Tanpa pemutusan itu,
koneksi lama akan terus menerima event dengan hak lamanya selama tab-nya
terbuka.

Yang **tidak** memutus koneksi: mengganti nama tampilan, mengubah
`device_limit`, dan mengganti password sendiri lewat tombol "Ganti password"
(yang terakhir sengaja — supaya Anda tidak menendang diri sendiri dari
perangkat yang sedang dipakai).

### Operator baru tidak menerima event realtime?

Selama akun itu belum punya device sama sekali, koneksi WebSocket-nya memang
ditolak: endpoint `/ws` menuntut device yang bisa diresolve dan dimiliki
pemanggilnya. Begitu device pertamanya dibuat, koneksinya normal. Bukan masalah
kredensial.

### Saya terkunci. Bagaimana masuk lagi?

Jalur darurat (*break-glass*): kredensial di `APP_BASIC_AUTH` yang
**username-nya belum ada di `app_user`** tetap bisa masuk sebagai admin.

```dotenv
APP_BASIC_AUTH=admin:passwordlama,darurat:passworddarurat123
```

Tambahkan entri baru dengan username yang belum terpakai, restart, lalu masuk
dengan kredensial itu. Dari situ Anda bisa mengelola user lagi.

Catatan penting:

- Kredensial darurat **tidak bisa memakai form login** di `/custom/login` — ia
  masuk lewat prompt HTTP Basic browser. Sesi cookie butuh baris `app_user`,
  dan kredensial darurat tidak punya.
- Kredensial darurat **tidak bisa memiliki device**. Ia admin, jadi bisa melihat
  semua dan menetapkan pemilik — cukup untuk memulihkan keadaan.
- Untuk **mematikan** jalur darurat, hapus entrinya dari `APP_BASIC_AUTH` —
  bukan dari `app_user`.
- Begitu sebuah username punya baris di `app_user`, password di database yang
  menang dan nilai env diabaikan untuk username itu.

### Admin terakhir tidak bisa dihapus?

Benar. Menghapus, menurunkan role, atau menonaktifkan admin aktif terakhir akan
ditolak dengan `LAST_ADMIN_PROTECTED`. Buat admin kedua lebih dulu.

### Dashboard utama (gowa-ui) perlu diubah?

Tidak. Dashboard mengambil daftar device dari API, dan API-nya sudah menyaring
per pemilik. Ia juga bekerja dengan sesi cookie tanpa perubahan apa pun, karena
`fetch`-nya same-origin.

Dashboard tidak tahu soal peran: operator tidak melihat elemen admin karena
API-nya menjawab 404, bukan karena UI-nya menyembunyikan.

### Kenapa `/app/devices` minta `X-Device-Id`?

Itu perilaku upstream, bukan dari fitur ini. `DeviceMiddleware` dipasang pada
grup ber-prefiks kosong sehingga ikut menangkap rute yang didaftarkan
setelahnya, dan `DefaultDevice()` hanya aktif kalau instalasi punya tepat satu
device. Sudah begitu sejak sebelum mode multi-tenant ada — diverifikasi dengan
`MULTI_TENANT_ENABLED=false`.

---

## Batasan yang diketahui

Semuanya **disengaja**, bukan bug. Disebut terus terang di sini supaya tidak
ditemukan pada saat yang paling tidak menyenangkan.

**1. Satu database, isolasi di lapisan aplikasi.** Semua tenant berbagi
`chatstorage.db`. Satu bug filter berarti kebocoran. Kalau yang dibutuhkan
isolasi keras antar organisasi yang berbeda — misalnya beda perusahaan dengan
kewajiban kepatuhan sendiri — jalannya adalah **satu proses per tenant** (DB dan
`storages/` terpisah, dibedakan reverse proxy), bukan fitur ini.

**2. Token MCP OAuth tidak tercabut saat password diganti.** Token yang sudah
terbit tetap berlaku sampai kedaluwarsa sendiri. Session cookie dan Basic Auth
dicabut seketika; token OAuth tidak. Jalur perbaikannya ada (hapus baris token
di `MCP_OAUTH_DB_URI` berdasarkan subject), tapi menyentuh store OAuth
memperluas cakupan secara signifikan dan belum dikerjakan.

**3. Webhook Chatwoot tidak terautentikasi tanpa `CHATWOOT_WEBHOOK_SECRET`.**
Ini perilaku upstream: server Chatwoot memanggilnya tanpa kredensial HTTP. Di
mode multi-tenant artinya lintas tenant, jadi secret itu **wajib**.

**4. Break-glass `APP_BASIC_AUTH` selalu admin.** Tidak ada level di bawahnya.

**5. Media di `statics/` tidak dipisah per tenant.** Nama filenya sulit ditebak,
tapi tidak ada kontrol akses per file. Kalau ini penting, itu pekerjaan
terpisah.

**6. Statistik penyimpanan global tidak dipecah per tenant.** Saat ini tidak ada
endpoint REST yang mengeksposnya (diverifikasi di audit fase 07), jadi belum
menjadi kebocoran. Kalau nanti ditambahkan, batasi ke admin.

**7. Perubahan kepemilikan device tidak memutus koneksi WebSocket** — dan memang
tidak perlu: hub memeriksa kepemilikan pada setiap event, jadi device yang
dipindahkan langsung berhenti terkirim ke pemilik lama. Yang diperiksa sekali
saja adalah identitas pemiliknya (role dan status aktif), dan itu ditangani
lewat pemutusan koneksi di atas.

**8. `serviceApp.FirstDevice` mengembalikan device pertama registry global.**
Saat ini **tidak ada pemanggilnya**, jadi bukan kebocoran aktif. Tapi kalau
nanti ada handler yang memakainya, ia akan mengembalikan device milik siapa pun.
Periksa setiap sync upstream.

---

## Untuk pengembang: setelah sync upstream

Rute baru dari upstream **tidak** otomatis ter-guard kalau memakai path param.
Setelah setiap sync:

1. Jalankan inventaris di
   [phase-05](phase-05-enforcement-rute.md#prasyarat) — setiap hasil harus
   terklasifikasi.
2. Periksa titik `websocket.Broadcast` baru: masing-masing wajib mengisi
   `DeviceID`, atau eventnya hanya akan sampai ke admin.
3. Jalankan test integrasi:
   ```bash
   cd src && go test -tags purego -run TestIntegration ./ui/rest/
   ```
4. Jalankan matriks verifikasi (perintahnya di langkah 10 di atas).
5. Cek `FirstDevice` masih tanpa pemanggil.

Aturan untuk endpoint baru ada di `CLAUDE.md`.
