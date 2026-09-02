# Fase 07 — MCP, Worker, Webhook, dan Audit Lubang Sisa

**Tujuan:** menutup L8 (MCP), memastikan permukaan non-HTTP tidak perlu diubah,
dan melakukan audit menyeluruh untuk menemukan lubang yang belum terdaftar.

**Tergantung:** Fase 05 dan 06
**Branch:** `feature/multitenant-phase07`

---

## Prasyarat

Baca:
- `src/ui/mcp/route.go`, `server.go`, `device.go` (`resolveDeviceContext`)
- `src/cmd/mcp_oauth.go` — `registerMcpOAuth`, `mcpOAuthCredentialValidator`
- `src/ui/mcp/oauth/server.go` — perhatikan `c.Locals("oauth_subject", principal.Subject)`

Fakta penting yang sudah diverifikasi: **MCP OAuth sudah membawa identitas
user.** `Subject` diisi dari `req.Username` saat login dan disimpan bersama
token, lalu ditaruh di `c.Locals("oauth_subject")` oleh middleware. Jadi MCP bisa
dibuat tenant-aware tanpa mendesain ulang OAuth-nya.

---

## Bagian A — MCP

### A1. Dua mode auth MCP, dua sumber identitas

| Mode | Kondisi | Sumber identitas |
|------|---------|------------------|
| OAuth | `McpEnabled && McpOAuthEnabled` | `c.Locals("oauth_subject")` → username |
| Basic | `McpEnabled && !McpOAuthEnabled` | `AuthGate` fase 03 → `PrincipalFrom(c)` |

Di mode Basic, MCP dipasang di `apiGroup` **setelah** gate, jadi principal-nya
sudah ada dan tidak perlu apa-apa lagi selain guard device.

Di mode OAuth, MCP dipasang **sebelum** gate (lihat komentar
`registerMcpOAuth`: harus di atas Basic Auth global supaya discovery tetap
publik). Jadi principal harus dibangun dari `oauth_subject`.

### A2. Middleware penjembatan

Buat `src/ui/mcp/tenant.go` (file baru):

```go
// PrincipalBridge mengisi principal untuk permintaan MCP yang datang lewat
// OAuth. AuthGate tidak berjalan di jalur ini — rute MCP OAuth sengaja
// didaftarkan sebelum gate global supaya discovery tetap publik — jadi
// identitas diambil dari oauth_subject yang sudah diverifikasi oleh
// MCPAuthMiddleware.
func PrincipalBridge(uc tenancy.ITenancyUsecase) fiber.Handler
```

Alur:

```
1. kalau !config.MultiTenantEnabled -> c.Next()
2. kalau PrincipalFrom(c) != nil    -> c.Next()   (mode Basic, sudah diisi gate)
3. subject := c.Locals("oauth_subject") sebagai string
   kosong -> 401
4. resolve subject jadi Principal:
       user := GetUserByUsername(subject) -> Principal dari user
       kalau tidak ada, tapi subject cocok dengan username di APP_BASIC_AUTH
           -> Principal break-glass (admin, UserID 0)   [sama seperti ResolveBasic]
       kalau tidak ada juga -> 401
   kalau user ada tapi !Active -> 401
5. StorePrincipal(c, p); c.Next()
```

Langkah 4 harus memakai **fungsi yang sama** dengan `ResolveBasic` untuk bagian
resolusi-tanpa-password. Ekstrak jadi
`ResolvePrincipalByUsername(ctx, username) (*Principal, error)` di usecase, lalu
`ResolveBasic` dan `PrincipalBridge` sama-sama memakainya. Dua implementasi
aturan break-glass akan berbeda cepat atau lambat.

Pasang di `registerMcpOAuth`, tepat setelah middleware auth MCP:

```go
	useMcpOAuthMiddleware(mcpRouter, oauthServer.MCPAuthMiddleware(validateCredential))
	if config.MultiTenantEnabled {
		mcpRouter.Use("/mcp", uimcp.PrincipalBridge(tenancyUsecase))
	}
```

Perhatikan: pakai `Use("/mcp", ...)` dan **bukan** prefiks kosong — alasannya
tertulis di komentar `useMcpOAuthMiddleware`.

### A3. Validator kredensial OAuth harus ikut DB

`mcpOAuthCredentialValidator` sekarang hanya memvalidasi ke
`config.AppBasicAuthCredential`. Akibatnya user dari `app_user` tidak bisa login
ke halaman authorize OAuth. Perbaiki dengan membungkusnya:

```go
func mcpOAuthCredentialValidator(credentials []string) (mcpoauth.CredentialValidator, error) {
	envValidator, err := ...   // logika yang ada sekarang, jangan diubah
	if !config.MultiTenantEnabled {
		return envValidator, nil
	}
	return func(username, password string) bool {
		p, err := tenancyUsecase.ResolveBasic(context.Background(), username, password)
		return err == nil && p != nil
	}, nil
}
```

`ResolveBasic` sudah mencakup fallback env, jadi jangan menumpuk kedua validator —
itu akan membuat password env tetap berlaku untuk user yang sudah ada di DB, tepat
kebalikan dari aturan K6.

Catat juga: `mcpOAuthCredentialValidator` sekarang mengembalikan error kalau
`APP_BASIC_AUTH` kosong. Di mode multi-tenant itu tidak lagi benar — user bisa
seluruhnya dari DB. Lunakkan syaratnya jadi
`len(credentials) == 0 && !config.MultiTenantEnabled`.

### A4. Guard device di tool MCP

`resolveDeviceContext` (`src/ui/mcp/device.go`) adalah padanan `DeviceMiddleware`
untuk MCP. Tambahkan pengecekan kepemilikan di dalamnya — di dalam, bukan di
setiap tool, dengan alasan yang sama seperti fase 05: tool baru dari upstream
harus otomatis terlindungi.

Kalau device tidak dimiliki, kembalikan error dengan pesan yang **sama** dengan
device-tidak-ada. Jangan bilang "bukan milik Anda".

Kalau tidak ada device id yang diberikan, terapkan aturan yang sama dengan
`DeviceOwnerGuard` langkah 6: satu device milik sendiri → pakai itu; nol → error;
lebih dari satu → error yang meminta device id eksplisit.

---

## Bagian B — Permukaan non-HTTP: konfirmasi tidak perlu diubah

Tulis hasil pemeriksaan ini di deskripsi PR sebagai konfirmasi eksplisit, bukan
sebagai asumsi.

| Permukaan | File | Kesimpulan |
|-----------|------|-----------|
| Worker antrian pesan | `src/infrastructure/whatsapp/message_queue_worker.go` | **tidak perlu guard.** Device sudah ditentukan oleh baris `message_queue`, yang hanya bisa dibuat lewat endpoint ber-guard. |
| Presence pulse | `src/infrastructure/whatsapp/presence_pulse.go` | tidak perlu guard; beroperasi per device instance. |
| Command handler `!` | `src/infrastructure/whatsapp/event_command_handler.go` | tidak perlu guard. Device ditentukan oleh event WhatsApp yang masuk, bukan oleh pemanggil HTTP. `allowed_senders` sudah membatasi siapa yang boleh memerintah. |
| Forward webhook | `src/infrastructure/whatsapp/webhook_forward.go` | tidak perlu guard; tujuan webhook adalah config per device. |
| Chatwoot sync service | `src/infrastructure/chatwoot/` | dipicu dari endpoint yang sudah di-guard di fase 05. |

Menambahkan guard di lapisan ini akan **mematikan worker**, karena mereka tidak
punya principal. Kalau ada dorongan untuk menambahkannya, itu tanda salah paham
soal di mana batas tenant berada: batasnya di pintu masuk (HTTP/MCP/WebSocket),
bukan di eksekusi.

---

## Bagian C — Webhook Chatwoot publik (L9)

`POST {webhookPath}/:device_id` didaftarkan **sebelum** auth, dengan sengaja.
Konsekuensinya (ini sudah begitu di upstream, bukan regresi dari kita): siapa pun
yang bisa menjangkau port ini dapat menyuntikkan balasan agen ke device mana pun,
kecuali `ChatwootWebhookSecret` diisi.

Yang harus dilakukan di fase ini:

1. **Jangan** menambahkan guard di rute itu.
2. Tambahkan pemeriksaan startup di `src/cmd/multitenant.go`: kalau
   `MultiTenantEnabled && ChatwootEnabled && ChatwootWebhookSecret == ""`, tulis
   `logrus.Warn` yang eksplisit menyebut bahwa webhook Chatwoot tidak
   terautentikasi dan device milik tenant lain bisa disuntik.
3. Pertimbangkan menaikkannya jadi `logrus.Fatalln`. **Keputusan: warn, bukan
   fatal** — mematikan startup karena config yang di upstream memang opsional
   akan mengubah upgrade jadi outage. Dokumentasikan sebagai syarat wajib di
   fase 09.

---

## Bagian D — Audit lubang sisa

Ini bagian terpenting fase 07. Jalankan setiap perintah, dan untuk setiap hasil
tentukan apakah sudah ter-guard atau belum. Lampirkan tabelnya di PR.

Semua tempat yang mengiterasi seluruh registry device:

```bash
cd src && grep -rn "ListDevices()\|ListDeviceRecords()\|FetchDevices(" --include=*.go . | grep -v _test
```

Semua tempat yang mengiterasi config lintas device:

```bash
cd src && grep -rn "ListChatwootDeviceConfigs()\|ListDeviceCommandConfigs()" --include=*.go . | grep -v _test
```

Semua handler yang membaca device id dari input pemanggil:

```bash
cd src && grep -rn "Params(\"device_id\")\|Query(\"device_id\")\|DeviceIDHeader" --include=*.go ui/ | grep -v _test
```

Semua rute yang didaftarkan di `app` (bukan `apiGroup`) — yaitu yang di luar gate:

```bash
cd src && grep -n "app\.\(Get\|Post\|Put\|Patch\|Delete\|Use\)(" cmd/rest.go
```

Statistik global yang bisa membocorkan volume tenant lain:

```bash
cd src && grep -rn "GetTotalMessageCount\|GetTotalChatCount\|GetStorageStatistics" --include=*.go . | grep -v _test
```

Yang terakhir itu perlu perhatian khusus: kalau ada endpoint yang mengembalikan
statistik agregat seluruh DB (jumlah chat, jumlah pesan), operator akan melihat
angka yang mencakup tenant lain. Itu kebocoran kecil tapi nyata. Kalau ada,
pilih salah satu: batasi ke admin, atau ganti dengan varian per-device. Putuskan
dan catat alasannya.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] Mode MCP + OAuth, flag on: user dari `app_user` bisa menyelesaikan alur
      authorize, dan tool-nya hanya melihat device miliknya.
- [ ] Mode MCP + Basic, flag on: idem lewat principal dari gate.
- [ ] Tool MCP dengan device id milik tenant lain → error yang identik dengan
      device-tidak-ada.
- [ ] Tool MCP tanpa device id, user punya satu device → memakai device itu.
- [ ] `resolveDeviceContext` yang men-guard di dalam dirinya, sehingga tool baru
      otomatis terlindungi (ada test).
- [ ] `mcpOAuthCredentialValidator` tidak lagi gagal saat `APP_BASIC_AUTH` kosong
      di mode multi-tenant.
- [ ] Break-glass di MCP memakai fungsi yang sama dengan `ResolveBasic` (satu
      implementasi, dipakai dua tempat).
- [ ] Rute discovery OAuth **masih publik** — `curl` tanpa kredensial ke
      `/.well-known/oauth-authorization-server` tetap 200.
- [ ] Warning startup untuk `CHATWOOT_WEBHOOK_SECRET` kosong muncul di mode
      multi-tenant.
- [ ] Tabel audit Bagian B dan D lengkap di deskripsi PR, tanpa baris yang
      berstatus "belum diperiksa".
- [ ] Flag off: MCP berperilaku identik dengan sebelumnya.

## Verifikasi

```bash
cd src && go test ./ui/mcp/... ./cmd/... -v
```

Discovery harus tetap publik:

```bash
curl -s -o /dev/null -w '%{http_code}\n' localhost:3000/.well-known/oauth-authorization-server
```

MCP mode Basic, flag on — daftar tool dan device yang terlihat:

```bash
curl -s -u operator1:rahasia123 -X POST localhost:3000/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

---

## Catatan & jebakan

- Jangan memindahkan `registerMcpOAuth` ke bawah gate "supaya principal-nya
  otomatis ada". Komentar di fungsi itu menjelaskan kenapa ia harus di atas:
  discovery, registration, dan token endpoint wajib publik. Memindahkannya akan
  mematikan seluruh klien MCP.
- Jangan memasang `PrincipalBridge` pada grup ber-prefiks kosong — itu akan ikut
  menangkap rute REST dan UI yang didaftarkan belakangan, persis masalah yang
  dihindari `useMcpOAuthMiddleware`.
- `oauth_subject` sudah diverifikasi oleh `MCPAuthMiddleware` sebelum sampai ke
  bridge. Jangan memverifikasinya lagi terhadap token; cukup resolve ke user.
  Tapi **tetap** cek `Active` — token yang diterbitkan sebelum user dinonaktifkan
  masih valid secara kriptografis.
- Token OAuth yang sudah terbit tidak ikut tercabut saat password diganti. Itu
  keterbatasan yang diketahui; catat di fase 09 sebagai known limitation, dengan
  jalur perbaikan: hapus baris token di `mcp_oauth_db_uri` berdasarkan subject
  saat `UpdateUser` mengubah password. Jangan kerjakan di fase ini kecuali
  diminta — menyentuh store OAuth memperluas scope secara signifikan.
