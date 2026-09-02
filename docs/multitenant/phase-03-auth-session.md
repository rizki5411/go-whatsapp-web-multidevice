# Fase 03 — Auth Gate: Session Cookie + Basic Auth dari DB

**Tujuan:** setiap request yang lolos autentikasi membawa `Principal` di
context. Dua jalur masuk: cookie session (untuk halaman `/custom/*`) dan HTTP
Basic (untuk klien API, gowa-ui, MCP) yang kini divalidasi ke `app_user` dengan
break-glass ke `APP_BASIC_AUTH`.

Setelah fase ini identitas sudah ada, tapi **belum dipakai untuk memfilter
apa pun** — itu fase 04. Yang sudah aktif: role guard untuk `/admin/*`.

**Tergantung:** Fase 02
**Branch:** `feature/multitenant-phase03`

---

## Prasyarat

Baca dulu:
- `src/cmd/rest.go` — bagian pendaftaran middleware. Perhatikan urutannya:
  webhook Chatwoot dan rute OAuth MCP **sengaja** didaftarkan **sebelum**
  `newBasicAuthMiddleware` supaya tetap publik. Urutan ini tidak boleh rusak.
- `src/ui/rest/middleware/websocketauth.go` — `WebsocketQueryAuth` memindahkan
  `?authorization=` ke header sebelum basic auth berjalan. Auth gate baru harus
  tetap kompatibel dengan ini.
- `src/ui/mcp/oauth/server.go` — `c.Locals("oauth_subject", principal.Subject)`.
  MCP OAuth **sudah** membawa username; fase 07 memanfaatkannya.

Fakta yang sudah diverifikasi (tidak perlu dicek ulang):
- Fiber v3.4.0 `basicauth` menyimpan username lewat `fiber.StoreInContext`, yang
  selalu menulis ke `c.Locals`. Jadi `basicauth.UsernameFromContext(c)` bisa
  dipakai tanpa `PassLocalsToContext`. Meski begitu, **auth gate kita tidak
  bergantung pada helper itu** — kita menyimpan `Principal` sendiri.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/ui/rest/middleware/principal.go` | **BARU** | simpan/baca principal, `RequireAdmin` |
| `src/ui/rest/middleware/authgate.go` | **BARU** | gate: cookie / basic / 401 |
| `src/ui/rest/middleware/authgate_test.go` | **BARU** | test |
| `src/ui/rest/auth.go` | **BARU** | `/auth/login`, `/auth/logout`, `/auth/me` |
| `src/ui/rest/auth_test.go` | **BARU** | test |
| `src/usecase/tenancy.go` | modifikasi (file fork sendiri) | tambah login/session/verify |
| `src/ui/rest/admin_users.go` | modifikasi (file fork sendiri) | pasang `RequireAdmin`, hapus TODO fase-02 |
| `src/cmd/rest.go` | modifikasi kecil | pilih gate vs basic auth lama |
| `src/cmd/multitenant.go` | modifikasi (file fork sendiri) | penyapu session kedaluwarsa |

---

## Detail implementasi

### 1. Tambahan di `ITenancyUsecase`

```go
	// Login memverifikasi kredensial lalu menerbitkan session. Mengembalikan
	// token mentah (untuk dikirim sebagai cookie) — token ini tidak pernah
	// disimpan, hanya hash-nya.
	Login(ctx context.Context, username, password, userAgent string) (token string, principal *Principal, err error)
	Logout(ctx context.Context, token string) error
	// ResolveSession memvalidasi token cookie: hash, cari, cek kedaluwarsa, cek
	// user masih aktif. Session kedaluwarsa langsung dihapus supaya tabel tidak
	// menumpuk sampah.
	ResolveSession(ctx context.Context, token string) (*Principal, error)
	// ResolveBasic memvalidasi username+password untuk jalur HTTP Basic,
	// termasuk aturan break-glass. Lihat catatan cache di bawah.
	ResolveBasic(ctx context.Context, username, password string) (*Principal, error)
	SweepExpiredSessions(ctx context.Context) (int64, error)
```

### 2. `ResolveBasic` — urutan yang harus tepat

```
1. normalisasi username
2. user := GetUserByUsername(username)
3. kalau user ada:
       kalau !user.Active                        -> gagal
       kalau !VerifyPassword(user.PasswordHash)  -> gagal
       -> Principal{user.ID, user.Username, user.Role, ViaBreakGlass: false}
4. kalau user TIDAK ada:
       cek terhadap config.AppBasicAuthCredential (constant-time compare)
       kalau cocok -> Principal{UserID: 0, Username: username,
                                Role: RoleAdmin, ViaBreakGlass: true}
       kalau tidak -> gagal
```

Poin kunci di langkah 4: break-glass **hanya** berlaku untuk username yang belum
ada barisnya di `app_user`. Begitu admin di-seed (fase 02), password DB yang
menang dan nilai env tidak bisa lagi dipakai untuk username itu. Ini yang
mencegah env jadi backdoor permanen sekaligus tetap menyediakan jalan darurat.

`UserID: 0` untuk break-glass **disengaja**: principal itu tidak memiliki device
mana pun. Karena rolenya admin, dia bisa melihat semuanya dan menetapkan owner —
cukup untuk memulihkan keadaan. Tapi jangan pernah menulis `device_owner` dengan
`user_id = 0`; fase 04 wajib menolaknya.

**Biaya bcrypt per request.** Basic Auth memverifikasi di setiap request dan
bcrypt cost 10 butuh ~50–100 ms. Untuk klien yang melakukan polling ini tidak
bisa diterima. Tambahkan cache kecil di usecase:

- key: `username + ":" + sha256(password)`
- value: `Principal` + waktu kedaluwarsa
- TTL 60 detik, `sync.Map` atau map + `sync.RWMutex`, batas ~256 entri
- **wajib dikosongkan** saat `UpdateUser` mengubah password/role/active, dan saat
  `DeleteUser`. Kalau tidak, user yang dinonaktifkan masih bisa masuk sampai satu
  menit — dan itu tepat jenis bug yang tidak akan ketahuan saat test manual.

Taruh cache-nya di file terpisah `src/usecase/tenancy_basiccache.go` supaya
mudah dibaca dan dites sendiri.

### 3. `src/ui/rest/middleware/principal.go`

```go
// principalKey adalah tipe privat supaya tidak ada paket lain yang bisa
// menimpa principal dengan menebak nama key-nya.
type principalKey struct{}

func StorePrincipal(c fiber.Ctx, p *tenancy.Principal) { c.Locals(principalKey{}, p) }

// PrincipalFrom mengembalikan nil kalau tidak ada. Pemanggil WAJIB menangani
// nil: rute publik (webhook, login) memang tidak punya principal.
func PrincipalFrom(c fiber.Ctx) *tenancy.Principal { ... }

// RequireAdmin menolak non-admin dengan 404, bukan 403, supaya keberadaan
// permukaan admin tidak terkonfirmasi ke operator biasa.
func RequireAdmin() fiber.Handler { ... }
```

Saat `config.MultiTenantEnabled == false`, `RequireAdmin` harus **lolos** —
rute `/admin/*` memang tidak didaftarkan di mode itu, tapi middleware-nya tetap
harus aman kalau nanti dipakai di tempat lain.

### 4. `src/ui/rest/middleware/authgate.go`

```go
func AuthGate(uc tenancy.ITenancyUsecase, fallback fiber.Handler) fiber.Handler
```

Alur:

```
1. kalau !config.MultiTenantEnabled -> jalankan fallback (basic auth lama), selesai
2. cookie config.MultiTenantSessionCookie ada?
       ResolveSession -> sukses: StorePrincipal, c.Next()
                      -> gagal : HAPUS cookie (expired), lanjut ke 3
3. header Authorization Basic ada?
       parse -> ResolveBasic -> sukses: StorePrincipal, c.Next()
                             -> gagal : ke 4
4. 401 + header WWW-Authenticate: Basic realm="gowa"
       supaya klien API dan gowa-ui tetap dapat prompt seperti sekarang
```

Yang tidak boleh terlewat:

- **Langkah 1 itu wajib.** Saat flag off, jalur lama harus dipakai apa adanya —
  itu jaminan zero-regression kita.
- Cookie session tidak valid **tidak boleh** langsung 401. Harus jatuh ke Basic,
  supaya klien API yang kebetulan membawa cookie basi tetap bisa masuk.
- Jangan lupa memasang `middleware.WebsocketQueryAuth()` **sebelum** gate, sama
  seperti sekarang, supaya WebSocket dari browser tetap bisa autentikasi. Gate
  membaca header `Authorization` yang sudah diisi middleware itu.
- Parsing header Basic: pakai `strings.CutPrefix` untuk `"Basic "`, lalu
  `base64.StdEncoding`, lalu `strings.Cut(":", ...)`. Password boleh mengandung
  `:` — jadi pakai `Cut` (belah pertama), **jangan** `strings.Split`.

### 5. `src/ui/rest/auth.go`

| Method | Path | Auth | Catatan |
|--------|------|------|---------|
| POST | `/auth/login` | **publik** | body JSON atau form: `username`, `password` |
| POST | `/auth/logout` | publik | menghapus cookie + barisnya; idempoten |
| GET | `/auth/me` | perlu principal | `{username, role, is_admin, device_limit}` |

Atribut cookie:

```go
c.Cookie(&fiber.Cookie{
    Name:     config.MultiTenantSessionCookie,
    Value:    token,
    Path:     basePathOr("/"),        // hormati config.AppBasePath
    HTTPOnly: true,
    Secure:   config.MultiTenantSecureCookie,
    SameSite: fiber.CookieSameSiteLaxMode,
    Expires:  time.Now().Add(config.MultiTenantSessionTTL),
})
```

`HTTPOnly` wajib. `SameSite=Lax` cukup: tidak ada aksi state-changing yang
dilakukan lewat navigasi lintas situs, dan `Strict` akan merusak alur redirect
dari halaman login.

**Rate limit login.** Tanpa ini `/auth/login` jadi oracle brute-force yang lebih
enak dipakai daripada Basic Auth. Cukup in-memory: maksimal 10 percobaan gagal
per username per 5 menit, plus 20 per IP per 5 menit. Simpan di
`src/ui/rest/auth_ratelimit.go`. Jangan tambah dependensi rate-limiter baru —
Fiber punya `middleware/limiter`, tapi kunci per-username butuh logika sendiri,
jadi map + mutex lebih jelas di sini.

### 6. `src/cmd/rest.go` (modifikasi kecil)

Ini satu-satunya perubahan yang agak sensitif. Bentuk targetnya:

```go
	if len(config.AppBasicAuthCredential) > 0 || config.MultiTenantEnabled {
		app.Use(middleware.WebsocketQueryAuth())

		if config.MultiTenantEnabled {
			// Login dan halaman login harus terjangkau tanpa kredensial,
			// sama seperti webhook Chatwoot dan rute OAuth MCP di atas.
			rest.InitRestAuthPublic(app, tenancyUsecase)
			app.Use(middleware.AuthGate(tenancyUsecase, newBasicAuthMiddleware(account)))
		} else {
			app.Use(newBasicAuthMiddleware(account))
		}
	}
```

Perhatikan:

- Pembangunan map `account` dari `config.AppBasicAuthCredential` yang sudah ada
  **jangan diubah** — masih dipakai sebagai fallback dan oleh break-glass.
- Kondisi `len(...) > 0` yang sekarang harus diperluas dengan
  `|| config.MultiTenantEnabled`, karena mode multi-tenant harus tetap
  terautentikasi meskipun `APP_BASIC_AUTH` kosong (semua user dari DB).
- `InitRestAuthPublic` didaftarkan pada `app`, bukan `apiGroup`, mengikuti pola
  webhook Chatwoot. Kalau `config.AppBasePath` diisi, path-nya harus
  ikut diprefiks — cek bagaimana webhook Chatwoot menyusun `webhookPath` dan
  ikuti cara yang sama.
- `/auth/me` dan `/auth/logout` didaftarkan di `apiGroup` (di belakang gate),
  bukan di jalur publik.

### 7. Penyapu session

Tambahkan di `src/cmd/multitenant.go` sebuah goroutine ticker (1 jam) yang
memanggil `SweepExpiredSessions`. Ikuti pola lifecycle
`StartPresencePulseScheduler` di `src/infrastructure/whatsapp/presence_pulse.go`
seperti diminta `CLAUDE.md`. Hanya jalan kalau flag on.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] **Flag off**: seluruh perilaku auth identik dengan sebelum fase ini.
      Buktikan: `curl -u user:pass` sukses, `curl` tanpa kredensial dapat 401
      dengan `WWW-Authenticate: Basic`, dan tidak ada rute `/auth/*`.
- [ ] **Flag on**: user yang dibuat lewat `/admin/users` (fase 02) bisa login
      lewat Basic Auth. Ini bukti utama fase ini berhasil.
- [ ] **Flag on**: `POST /auth/login` menaruh cookie HttpOnly; request berikutnya
      tanpa header Basic tetap lolos.
- [ ] `POST /auth/logout` membuat cookie itu langsung tidak berlaku.
- [ ] User dengan `active = 0` tidak bisa masuk lewat Basic maupun cookie.
- [ ] Ganti password → session lama mati **dan** cache Basic ter-invalidasi.
      Ada test untuk keduanya.
- [ ] Break-glass: username yang ada di `APP_BASIC_AUTH` tapi **tidak** ada di
      `app_user` bisa masuk sebagai admin. Username yang ada di keduanya **hanya**
      menerima password dari DB (test negatif dengan password env lama).
- [ ] `RequireAdmin` terpasang di semua rute `/admin/*`; operator biasa dapat 404.
      TODO fase-02 di `admin_users.go` sudah dihapus.
- [ ] WebSocket dari browser masih bisa autentikasi lewat `?authorization=`.
- [ ] Rate limit login jalan (test: 11 percobaan gagal, yang ke-11 ditolak).
- [ ] Webhook Chatwoot dan rute discovery OAuth MCP **masih publik** — pastikan
      urutan pendaftaran di `rest.go` tidak berubah.
- [ ] `git diff src/cmd/rest.go` maksimal ~15 baris, tanpa reformat.

## Verifikasi

```bash
cd src && go test ./ui/rest/... ./usecase/... -v
```

Alur manual, flag on:

```bash
curl -u operator1:rahasia123 localhost:3000/auth/me
```

```bash
curl -c /tmp/c.txt -X POST localhost:3000/auth/login -H 'Content-Type: application/json' -d '{"username":"operator1","password":"rahasia123"}' -i
```

```bash
curl -b /tmp/c.txt localhost:3000/auth/me
```

Operator dilarang masuk area admin (harus 404):

```bash
curl -u operator1:rahasia123 localhost:3000/admin/users -i
```

Dashboard gowa-ui masih hidup:

```bash
curl -u operator1:rahasia123 localhost:3000/ -o /dev/null -w '%{http_code}\n'
```

---

## Catatan & jebakan

- **Jangan pindahkan** pendaftaran webhook Chatwoot atau OAuth MCP. Keduanya
  wajib tetap di atas gate; kalau tergeser ke bawah, Chatwoot akan berhenti
  mengirim balasan agen dan klien MCP tidak bisa discovery.
- Jangan memasang gate dengan `app.Use("/", ...)` pada grup ber-prefiks kosong.
  Komentar di `useMcpOAuthMiddleware` (`src/cmd/mcp_oauth.go`) menjelaskan
  kenapa: mounting pada prefiks kosong ikut menangkap rute lain yang didaftarkan
  belakangan. Pakai `app.Use(handler)` seperti kode yang ada.
- Cache verifikasi Basic itu **wajib**, bukan optimasi opsional. Tanpanya, satu
  klien polling `/app/status` tiap detik akan menghabiskan CPU di bcrypt.
- Cookie `Path` harus menghormati `config.AppBasePath`. Kalau salah, deploy di
  subpath akan menerima cookie yang tidak pernah terkirim balik dan login
  kelihatan "berhasil tapi tidak nyangkut".
- `ViaBreakGlass` hanya untuk log. Jangan pernah dipakai sebagai syarat
  otorisasi — role-lah yang menentukan.
- Setelah fase ini identitas sudah ada tapi **data belum terisolasi**. Jangan
  bilang ke siapa pun bahwa multi-tenant sudah jalan sampai fase 05 selesai.
