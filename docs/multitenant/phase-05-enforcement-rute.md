# Fase 05 — Enforcement Rute Manual-Resolve & Endpoint Agregat

**Tujuan:** menutup lubang sisa di jalur HTTP, yaitu semua rute `:device_id` yang
tidak lewat `DeviceMiddleware`, plus endpoint yang mengembalikan data lintas
device. Setelah fase ini, permukaan REST sudah terisolasi penuh.

**Tergantung:** Fase 04
**Branch:** `feature/multitenant-phase05`

---

## Prasyarat

Sebelum menulis kode, **lakukan inventaris ulang**. Daftar di bawah benar saat
rencana disusun, tapi sync upstream bisa menambah rute baru. Jalankan:

```bash
cd src && grep -rn "Params(\"device_id\")" --include=*.go ui/ | grep -v _test
```

```bash
cd src && grep -rn ":device_id" --include=*.go ui/ cmd/ | grep -v _test
```

Setiap hasil harus masuk salah satu dari tiga kategori: **(a) sudah ter-guard
oleh `DeviceOwnerGuard`**, **(b) di-guard di fase ini**, atau **(c) sengaja
publik** (hanya webhook Chatwoot). Tidak ada kategori keempat. Tulis hasil
inventarisnya di deskripsi PR.

---

## Daftar target (hasil audit awal)

| Rute | File | Kategori |
|------|------|----------|
| `GET/POST/DELETE /devices/:device_id...` (get, login, login/code, logout, reconnect, status, webhook) | `src/ui/rest/device.go` | b |
| `GET/PUT/DELETE /devices/:device_id/command/config` | `src/ui/rest/command_config.go` | b |
| `GET /command/configs` (agregat) | `src/ui/rest/command_config.go` | b |
| `GET /devices/:device_id/queue`, `DELETE /devices/:device_id/queue/:queue_id` | `src/ui/rest/message_queue.go` | b |
| `GET/PUT/DELETE /devices/:device_id/chatwoot/config` | `src/ui/rest/chatwoot_config.go` (didaftarkan di `src/cmd/rest.go`) | b |
| `GET /chatwoot/configs` (agregat) | `src/ui/rest/chatwoot.go` | b |
| `POST /chatwoot/sync`, `GET /chatwoot/sync/status` | `src/ui/rest/chatwoot.go` | b — cek apakah device-scoped |
| `POST {webhookPath}/:device_id` | `src/cmd/rest.go` | c (publik, jangan diubah) |

`AddDevice` dan `RemoveDevice` sudah ditangani fase 04.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/ui/rest/tenantfilter/guard.go` | **BARU** | helper `GuardParamDevice`, `FilterByDevice` |
| `src/ui/rest/tenantfilter/guard_test.go` | **BARU** | test |
| `src/ui/rest/device.go` | modifikasi kecil | guard di tiap handler `:device_id` |
| `src/ui/rest/command_config.go` | modifikasi kecil | guard + filter agregat |
| `src/ui/rest/message_queue.go` | modifikasi kecil | guard |
| `src/ui/rest/chatwoot_config.go` | modifikasi kecil | guard |
| `src/ui/rest/chatwoot.go` | modifikasi kecil | filter agregat |
| `src/cmd/rest.go` | modifikasi kecil | teruskan `ownership` ke `InitRest*` |

---

## Detail implementasi

### 1. Satu helper, dipakai semua

Jangan menulis logika guard yang sama enam kali. Buat satu helper dan pakai di
mana-mana — kalau nanti aturannya berubah, cukup satu tempat:

```go
// GuardParamDevice memeriksa kepemilikan atas :device_id di path. Mengembalikan
// device id yang sudah diresolve dan di-clone (siap dipersistensi), atau error
// fiber yang sudah berisi response 404 yang benar.
//
// Dipakai oleh rute yang resolve device secara manual, yaitu yang berada di luar
// DeviceMiddleware/DeviceOwnerGuard karena memakai path param.
func GuardParamDevice(
	c fiber.Ctx,
	dm *whatsapp.DeviceManager,
	own tenancy.IDeviceOwnership,
) (deviceID string, err error)
```

Alur:

```
1. raw := strings.TrimSpace(c.Params("device_id"))
   kosong -> 400 DEVICE_ID_REQUIRED
2. _, resolvedID, err := dm.ResolveDevice(raw)
   err -> 404 DEVICE_NOT_FOUND   (pesan sama dengan DeviceMiddleware)
3. kalau config.MultiTenantEnabled:
       own.CanAccess(PrincipalFrom(c), resolvedID) == false -> 404 DEVICE_NOT_FOUND
4. return strings.Clone(resolvedID), nil
```

Langkah 4 (`strings.Clone`) menggantikan sekaligus `resolveConfigDeviceID` di
`command_config.go` dan padanannya di `chatwoot_config.go` — komentar di kedua
fungsi itu sudah menjelaskan kenapa clone-nya wajib (fasthttp mendaur ulang
buffer param, dan id ini dipersistensi). Helper baru **harus** mempertahankan
perilaku itu.

Cara pakai di handler jadi seragam dan pendek:

```go
deviceID, err := tenantfilter.GuardParamDevice(c, h.DeviceManager, h.Ownership)
if err != nil {
    return err
}
```

### 2. Lubang fallback pada handler DELETE (WAJIB, mudah terlewat)

`DeleteCommandConfig` dan `DeleteChatwootConfig` sengaja jatuh ke path param
mentah kalau resolusi device gagal, "so a config orphaned by device removal
stays deletable".

Jalur itu **melewati** pemeriksaan kepemilikan — memasang guard di dalam
resolver saja tidak cukup. Tanpa penanganan khusus, operator bisa menghapus
config device orang lain hanya dengan mengirim id yang tidak resolve.

Aturannya: jalur fallback dibatasi ke admin, konsisten dengan "device tanpa
pemilik hanya untuk admin". Pakai `tenantfilter.CanActOnUnresolvedDevice`.
Keduanya wajib punya test — operator ditolak dan config-nya utuh, admin tetap
bisa membersihkan.

### 3. Ganti helper lama, jangan menumpuk

`command_config.go` punya `resolveConfigDeviceID`, `chatwoot_config.go` punya
yang serupa. **Ubah isi fungsi lama itu** agar mendelegasikan ke
`GuardParamDevice`, jangan menambah pemanggilan guard terpisah di setiap handler.

Alasannya bukan estetika: kalau guard dipanggil terpisah, satu handler baru dari
upstream yang memanggil `resolveConfigDeviceID` tanpa guard akan lolos tanpa
suara. Dengan guard di dalam resolver, handler baru mana pun otomatis ter-guard.

Signature lama mengembalikan `(string, bool)`. Ubah jadi mengembalikan error
fiber, atau pertahankan `(string, bool)` dan tulis response di dalamnya. Pilih
yang perubahannya paling kecil terhadap call site yang ada — **jangan** refactor
seluruh file (aturan fork nomor 2).

### 4. Endpoint agregat

**`GET /command/configs`** (`ListCommandConfigs`) dan **`GET /chatwoot/configs`**
(`ListChatwootConfigs`) mengembalikan config untuk semua device. Filter hasilnya
dengan `OwnedDeviceIDs`:

```go
ids, all := h.Ownership.OwnedDeviceIDs(middleware.PrincipalFrom(c))
if !all {
    configs = filterByDeviceID(configs, ids)
}
```

Kalau `ids` kosong dan `all == false`, hasilnya daftar kosong — **bukan** error.

Perhatikan: config bisa ter-key oleh `device_id` **atau** `device_jid` (lihat
`GetDeviceCommandConfigByIdentifier`). Filter berdasarkan `device_id`; baris yang
`device_id`-nya kosong (kalau ada) tidak boleh lolos ke operator.

**`POST /chatwoot/sync` dan `GET /chatwoot/sync/status`**: baca dulu
implementasinya. Kalau dia mengambil device dari `config.ChatwootDeviceID` atau
menyapu semua device, maka di mode multi-tenant endpoint ini harus **dibatasi ke
admin** (`RequireAdmin`) — sinkronisasi lintas device bukan operasi yang bisa
dipegang operator. Kalau ternyata device-scoped, guard seperti yang lain.
Putuskan berdasarkan kode, dan **tulis alasannya di komentar**.

### 5. `message_queue.go`

`DELETE /devices/:device_id/queue/:queue_id` punya lubang kedua yang mudah
terlewat: setelah device ter-guard, `queue_id` masih bisa milik device lain kalau
repository menghapus hanya berdasarkan id. Periksa
`sqlite_repository_message_queue.go` — kalau pembatalan tidak memfilter
`device_id`, maka handler wajib memverifikasi bahwa baris itu memang milik
device yang sudah di-guard sebelum menghapus.

Cek dengan:

```bash
cd src && grep -n "func (r \*SQLiteRepository).*Queue" infrastructure/chatstorage/sqlite_repository_message_queue.go
```

Kalau ada method yang menerima hanya `queueID`, tambahkan varian device-scoped
di file itu (additive, method baru — jangan ubah yang lama), atau lakukan
pengecekan di handler. Yang mana pun, **harus ada test** untuk kasus "operator A
membatalkan queue id milik device B".

### 6. `device.go` — sudah selesai di Fase 04

`GET /devices/:device_id/login` mengembalikan QR code. Tanpa guard, operator lain
bisa memancing QR untuk device orang lain dan memasangkan dirinya. Ini rute
paling berbahaya di daftar — pastikan ter-guard dan ada test-nya.

`GET /devices/:device_id/status` membocorkan keberadaan dan keadaan device. Guard.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] Inventaris `grep` sudah dijalankan dan **setiap** hasil terklasifikasi
      (a/b/c) di deskripsi PR. Tidak ada yang tanpa kategori.
- [ ] Semua rute kategori (b) menolak cross-tenant dengan `404` berbadan identik.
- [ ] `GET /command/configs` dan `GET /chatwoot/configs` hanya menampilkan device
      milik pemanggil; admin melihat semua.
- [ ] `GET /devices/:device_id/login` (QR) tidak bisa diakses lintas tenant —
      ada test khusus.
- [ ] Pembatalan queue lintas tenant ditolak — ada test khusus.
- [ ] Resolver lama (`resolveConfigDeviceID` dan padanannya) sekarang
      men-guard di dalam dirinya, sehingga handler baru otomatis terlindungi.
- [ ] `strings.Clone` tetap dipertahankan untuk id yang dipersistensi.
- [ ] Flag off: semua rute berperilaku identik dengan sebelumnya.
- [ ] Tidak ada file yang direformat; `git diff --stat` menunjukkan perubahan
      kecil dan tersebar, bukan file yang ditulis ulang.

## Verifikasi

```bash
cd src && go test ./ui/rest/... -v
```

Matriks cross-tenant, dijalankan sebagai operator1 terhadap device milik
operator2 (`dev-b`) — **semua harus 404**:

```bash
for p in "/devices/dev-b" "/devices/dev-b/status" "/devices/dev-b/login" "/devices/dev-b/command/config" "/devices/dev-b/queue" "/devices/dev-b/chatwoot/config" "/devices/dev-b/webhook"; do printf '%s -> ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' -u operator1:rahasia123 "localhost:3000$p"; done
```

Endpoint agregat hanya berisi device sendiri:

```bash
curl -s -u operator1:rahasia123 localhost:3000/command/configs
```

Dan admin tetap melihat semuanya:

```bash
curl -s -u admin:rahasia123 localhost:3000/command/configs
```

---

## Catatan & jebakan

- Rute Chatwoot config didaftarkan di `src/cmd/rest.go`, **bukan** di dalam
  `chatwoot_config.go` — jangan mencari `InitRestChatwootConfig` yang tidak ada.
- Webhook Chatwoot publik (`POST {webhookPath}/:device_id`) **jangan** di-guard.
  Dia dipanggil oleh server Chatwoot, tanpa kredensial, dan pengamanannya adalah
  `CHATWOOT_WEBHOOK_SECRET`. Menambahkan guard di sini akan mematikan balasan
  agen. Fase 09 mendokumentasikan bahwa secret itu wajib di mode multi-tenant.
- Rute OAuth MCP dan `/mcp` tidak disentuh di fase ini — itu fase 07.
- Jangan menambahkan guard ke `InitRestAppInfo` (`/app/info`): isinya versi dan
  limit, tidak ada data tenant.
- Kalau ada rute yang setelah dipikir-pikir sebaiknya admin-only, pakai
  `middleware.RequireAdmin()` dari fase 03 — jangan bikin mekanisme baru.
- Setelah fase ini, WebSocket masih membocorkan event lintas tenant (L7). Itu
  fase 06, dan sifat kebocorannya berbeda: bukan data yang bisa diminta, tapi
  data yang dikirim tanpa diminta.
