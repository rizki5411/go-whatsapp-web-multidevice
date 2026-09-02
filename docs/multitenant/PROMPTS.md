# Prompt Siap Pakai

Kumpulan prompt untuk menyuruh AI mengerjakan rencana di folder ini. Tinggal
copy-paste.

## Aturan pemakaian

1. **Satu fase = satu sesi baru.** Jangan mengerjakan dua fase dalam satu sesi.
   Konteks yang menumpuk dari fase sebelumnya justru menurunkan akurasi, dan
   itu seluruh alasan rencana ini dipecah per file.
2. **Jangan lampirkan seluruh folder `docs/multitenant/`.** Prompt sudah
   menyuruh AI membaca file yang tepat. Melampirkan semuanya membuat AI
   mencampur instruksi antar fase.
3. Setelah tiap fase, jalankan **prompt review** (bagian B) di sesi terpisah
   sebelum merge.

---

## A. Prompt pengerjaan

### A1. Template umum (ganti `NN` dan nama filenya)

```
Kerjakan Fase NN dari rencana multi-tenant di repo ini.

Baca dulu, dalam urutan ini:
1. CLAUDE.md dan AGENTS.md di root — aturan fork dan konvensi project
2. docs/multitenant/README.md — bagian "Keputusan arsitektur" (K1-K9) dan
   "Aturan main"
3. docs/multitenant/phase-NN-<nama>.md — ini spesifikasi yang kamu kerjakan

Aturan:
- Kerjakan HANYA Fase NN. Kalau kamu melihat masalah yang masuk fase lain,
  catat di laporan akhir, jangan dikerjakan.
- Ikuti aturan fork: additive-first, jangan reformat atau reorganisasi file
  upstream, perubahan pada file lama sekecil mungkin.
- Nomor baris di dokumen itu mungkin sudah bergeser. Verifikasi dengan grep,
  jangan percaya nomor baris mentah.
- Kalau spesifikasinya keliru atau tidak cocok dengan kondisi kode sekarang,
  BERHENTI dan bilang ke saya sebelum menulis kode. Jangan mengarang jalan
  keluar sendiri.
- Buat branch feature/multitenant-phaseNN dari develop sebelum mulai.
- Jangan commit sampai saya minta.

Selesai kalau:
- `cd src && go build ./... && go vet ./... && go test ./...` hijau
- Semua item Definition of Done di file fase itu terpenuhi

Laporkan di akhir:
- daftar file yang dibuat dan yang dimodifikasi, plus `git diff --stat`
- status tiap item Definition of Done (terpenuhi / tidak, dan kenapa)
- penyimpangan dari spesifikasi, kalau ada, beserta alasannya
- hal yang kamu temukan tapi di luar scope fase ini
```

### A2. Per fase — tinggal pakai

Fase 00:

```
Kerjakan Fase 00 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (bagian Keputusan arsitektur dan Aturan
main), lalu docs/multitenant/phase-00-fondasi.md sebagai spesifikasi.

Kerjakan HANYA fase itu. Fase ini murni konfigurasi: kalau ada kode yang
membaca config.MultiTenantEnabled, berarti scope sudah melebar — jangan.

Ambil baseline `go test ./...` sebelum mengubah apa pun dan catat hasilnya.
Branch: feature/multitenant-phase00 dari develop. Jangan commit.

Laporkan status tiap item Definition of Done di akhir.
```

Fase 01:

```
Kerjakan Fase 01 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-01-skema-repository.md.

Dua hal yang paling gampang salah di fase ini, tolong ekstra hati-hati:
1. Hitung dulu jumlah migration aktual dengan grep. Migration WAJIB append-only
   di akhir getMigrations(); runner-nya memakai indeks array sebagai nomor versi,
   jadi menyisipkan di tengah akan merusak DB yang sudah jalan.
2. JANGAN menambah method apa pun ke IChatStorageRepository
   (src/domains/chatstorage/interfaces.go). Interface tenancy berdiri sendiri.
   Alasannya ada di K2 di README.

Branch: feature/multitenant-phase01 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 02:

```
Kerjakan Fase 02 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-02-user-management.md.

Perhatian khusus: invariant "admin terakhir tidak boleh hilang" punya tiga
kasus (hapus, turunkan role, nonaktifkan) dan ketiganya wajib punya test
terpisah. Bootstrap admin dari APP_BASIC_AUTH harus idempoten dan TIDAK boleh
menggagalkan startup kalau password env-nya terlalu pendek.

Fase ini belum menyambungkan login user DB ke autentikasi — itu fase 03. Jangan
coba memperbaikinya di sini.

Branch: feature/multitenant-phase02 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 03:

```
Kerjakan Fase 03 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-03-auth-session.md.

Tiga hal yang tidak boleh rusak:
1. Urutan pendaftaran middleware di src/cmd/rest.go. Webhook Chatwoot dan rute
   OAuth MCP HARUS tetap didaftarkan sebelum auth gate supaya tetap publik.
2. Jalur flag-off harus memakai newBasicAuthMiddleware yang ada, apa adanya.
3. Cache verifikasi Basic Auth wajib ter-invalidasi saat password/role/active
   berubah. Kalau tidak, user yang dinonaktifkan masih bisa masuk sampai satu
   menit — dan itu tidak akan ketahuan dari test manual.

Branch: feature/multitenant-phase03 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 04:

```
Kerjakan Fase 04 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-04-device-ownership.md.

Ini fase inti keamanan. Yang paling kritis:
1. src/ui/rest/middleware/device.go dan
   src/infrastructure/whatsapp/device_manager.go TIDAK BOLEH berubah satu baris
   pun. Guard dipasang sebagai middleware terpisah (K5). Buktikan dengan git diff.
2. Saat guard me-resolve ulang device (kasus request tanpa X-Device-Id), KETIGA
   nilai yang ditulis DeviceMiddleware harus ditimpa: Locals("device_id"),
   Locals("device"), dan SetContext(ContextWithDevice(...)). Kalau SetContext
   terlewat, usecase tetap memakai device orang lain. Wajib ada test terpisah
   untuk ini.
3. RemoveDevice harus cek CanAccess sebelum purge. Tanpa itu, operator mana pun
   bisa mem-purge device orang lain.
4. Cache kepemilikan pakai invalidasi eksplisit, BUKAN TTL.

Branch: feature/multitenant-phase04 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 05:

```
Kerjakan Fase 05 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-05-enforcement-rute.md.

Mulai dengan menjalankan inventaris grep di bagian Prasyarat file itu. Setiap
hasil harus terklasifikasi (a) sudah ter-guard, (b) di-guard di fase ini, atau
(c) sengaja publik. Lampirkan tabelnya di laporan. Tidak boleh ada hasil yang
tanpa kategori.

Poin desain yang penting: guard dipasang DI DALAM resolver device (mis.
resolveConfigDeviceID), bukan dipanggil terpisah di setiap handler. Dengan
begitu handler baru dari upstream otomatis terlindungi.

Jangan men-guard webhook Chatwoot publik — itu akan mematikan balasan agen.

Branch: feature/multitenant-phase05 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 06:

```
Kerjakan Fase 06 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-06-websocket.md.

Fase ini menyentuh file upstream paling banyak, jadi disiplin: tambah field,
jangan ubah yang ada. BroadcastMessage tanpa DeviceID harus tetap kompilasi.

Yang wajib benar:
1. Semua titik websocket.Broadcast harus mengisi DeviceID. Cari dengan grep
   dulu — saat rencana disusun ada 9, verifikasi jumlah aktualnya.
2. Jangan pernah menulis ke conn dari goroutine pembaca. Pakai channel Direct
   yang dilayani RunHub.
3. `go test -race ./ui/websocket/...` wajib hijau.
4. Flag off: payload WebSocket harus identik dengan sebelum fase ini.

Branch: feature/multitenant-phase06 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 07:

```
Kerjakan Fase 07 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-07-mcp-permukaan-lain.md.

Catatan: MCP OAuth SUDAH membawa identitas user lewat
c.Locals("oauth_subject") — jadi tidak perlu mendesain ulang OAuth-nya, cukup
jembatani ke Principal.

Jangan memindahkan registerMcpOAuth ke bawah auth gate. Rute discovery,
registration, dan token endpoint wajib tetap publik.

Bagian D (audit lubang sisa) bukan opsional. Jalankan setiap perintah grep di
situ dan lampirkan tabel hasilnya. Baris yang berstatus "belum diperiksa"
dihitung gagal.

Branch: feature/multitenant-phase07 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 08:

```
Kerjakan Fase 08 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md (Keputusan arsitektur + Aturan main),
lalu docs/multitenant/phase-08-ui-operator.md.

Kontrak halaman /custom yang tidak boleh dilanggar (ada di komentar
src/ui/rest/custom_ui.go): di-embed ke binary, self-contained tanpa CDN atau
font eksternal, dan API root diturunkan dengan memotong path halaman itu
sendiri supaya APP_BASE_PATH tetap jalan.

JANGAN menyentuh dashboard gowa-ui — file itu di-download runtime dan ditimpa
auto-update.

/custom/login harus bisa dibuka tanpa kredensial, jadi didaftarkan di jalur
publik bersama POST /auth/login, bukan lewat InitRestCustomUI.

Validasi parameter ?next= (hanya path relatif, tolak yang dimulai //) supaya
halaman login tidak jadi open redirect.

Branch: feature/multitenant-phase08 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

Fase 09:

```
Kerjakan Fase 09 dari rencana multi-tenant di repo ini. Baca CLAUDE.md,
AGENTS.md, docs/multitenant/README.md, lalu
docs/multitenant/phase-09-verifikasi-rollout.md.

Fase ini didominasi verifikasi, bukan menulis fitur. Yang harus dihasilkan:
1. Test integrasi src/ui/rest/multitenant_integration_test.go yang mengunci
   matriks A2 (cross-tenant -> 404) dan A3 (fallback tanpa header) sebagai
   table test.
2. Update CLAUDE.md, AGENTS.md, src/.env.example sesuai Bagian C.
3. docs/multitenant/OPERASI.md baru, termasuk daftar known limitations.

Untuk matriks manual A1-A8: siapkan skrip yang menjalankannya terhadap server
lokal dan mencetak tabel hasil, jangan disuruh saya jalankan satu-satu. Kalau
ada baris yang tidak bisa diotomatiskan, sebutkan dan jelaskan kenapa.

Jangan menandai baris matriks sebagai lulus kalau kamu tidak benar-benar
menjalankannya.

Branch: feature/multitenant-phase09 dari develop. Jangan commit.
Laporkan status tiap item Definition of Done di akhir.
```

---

## B. Prompt review (jalankan di sesi BARU setelah tiap fase)

Ini yang paling berharga. AI yang baru saja menulis kode adalah penilai yang
buruk atas kodenya sendiri; sesi bersih jauh lebih jujur.

```
Review implementasi Fase NN multi-tenant di branch ini. Kamu BUKAN yang
menulisnya — bersikap skeptis.

Baca docs/multitenant/phase-NN-<nama>.md sebagai spesifikasi, lalu periksa
`git diff develop...HEAD`.

Periksa dan jawab satu per satu:
1. Setiap item Definition of Done: benar-benar terpenuhi, atau cuma diklaim?
   Buktikan dengan menunjuk kode atau test yang relevan.
2. Ada scope creep? Perubahan yang tidak diminta fase ini?
3. Jalur flag-off (MULTI_TENANT_ENABLED=false) masih 100% seperti perilaku lama?
   Ini jaminan rollback kita — periksa serius, jangan diasumsikan.
4. Ada file upstream yang diubah lebih dari yang perlu, direformat, atau
   direorganisasi? Bandingkan dengan aturan di CLAUDE.md.
5. Ada jalur di mana guard bisa dilewati? Pikirkan seperti penyerang yang punya
   kredensial operator yang valid.
6. Test-nya benar-benar menguji perilakunya, atau cuma menguji bahwa fungsinya
   bisa dipanggil?

Jangan memperbaiki apa pun. Laporkan temuan dengan file:line, urut dari yang
paling serius. Kalau tidak ada temuan serius, katakan begitu — jangan mengarang
temuan supaya kelihatan teliti.
```

---

## C. Prompt situasional

### C1. Melanjutkan fase yang terputus di tengah

```
Sesi sebelumnya sedang mengerjakan Fase NN multi-tenant dan terputus.

Baca docs/multitenant/phase-NN-<nama>.md, lalu periksa `git status` dan
`git diff` untuk melihat apa yang sudah ada. Petakan dulu: mana item Definition
of Done yang sudah selesai, mana yang belum.

Laporkan peta itu ke saya SEBELUM melanjutkan menulis kode, supaya saya bisa
konfirmasi. Jangan mengulang pekerjaan yang sudah jadi, dan jangan
mengasumsikan yang setengah jadi itu benar — verifikasi.
```

### C2. Kalau AI bilang spesifikasinya keliru

```
Jelaskan ketidakcocokannya: bagian mana dari dokumen fase yang tidak sesuai
kondisi kode sekarang, dan apa buktinya (file:line).

Lalu ajukan 1-2 pilihan penyelesaian beserta konsekuensinya masing-masing
terhadap keputusan arsitektur K1-K9 di docs/multitenant/README.md.

Jangan menulis kode dulu. Dan kalau perbaikannya menyangkut dokumen fase itu
sendiri, usulkan perubahan dokumennya juga.
```

### C3. Setelah sync upstream (dipakai berkali-kali nanti)

```
Repo ini baru sync dari upstream. Periksa apakah ada rute atau broadcast baru
yang melewati guard multi-tenant.

Jalankan inventaris grep di bagian Prasyarat
docs/multitenant/phase-05-enforcement-rute.md, plus cek titik
websocket.Broadcast baru.

Untuk setiap rute baru yang menerima device_id dari pemanggil: pastikan dia
lewat headerDeviceGroup atau memanggil tenantfilter.GuardParamDevice. Untuk
setiap broadcast baru: pastikan mengisi DeviceID.

Jalankan juga test integrasi multi-tenant.

Laporkan temuan dulu sebelum memperbaiki apa pun.
```

### C4. Menjalankan matriks verifikasi saja

```
Jalankan matriks verifikasi di docs/multitenant/phase-09-verifikasi-rollout.md
bagian A terhadap server lokal (flag MULTI_TENANT_ENABLED=true).

Siapkan lingkungan ujinya lebih dulu sesuai yang disebut di file itu: tiga akun
(admin, op1, op2) dan tiga device (dev-a milik op1, dev-b milik op2, dev-orphan
tanpa owner).

Cetak hasilnya sebagai tabel: nomor baris, aksi, harapan, hasil aktual,
lulus/gagal. Jangan menandai lulus untuk baris yang tidak kamu jalankan —
tandai "tidak dijalankan" dan sebutkan alasannya.
```
