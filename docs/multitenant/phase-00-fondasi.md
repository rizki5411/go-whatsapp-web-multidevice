# Fase 00 — Fondasi, Feature Flag, Baseline

**Tujuan:** menyiapkan flag konfigurasi dan baseline test, tanpa mengubah
perilaku apa pun. Setelah fase ini di-merge, aplikasi harus berjalan identik
dengan sebelumnya.

**Tergantung:** —
**Branch:** `feature/multitenant-phase00`

---

## Prasyarat

Baca `README.md` di folder ini, bagian **Keputusan arsitektur** dan **Aturan main**.

Ambil baseline dulu, supaya kalau nanti ada test yang merah kita tahu itu bukan
gara-gara kita:

```bash
cd src && go build ./... && go vet ./... && go test ./... 2>&1 | tail -40
```

Catat hasilnya di deskripsi PR. Kalau sudah ada test yang merah sejak awal,
**jangan diperbaiki di fase ini** — cukup dicatat.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/config/settings.go` | modifikasi kecil | tambah blok var baru di akhir `var (...)` |
| `src/cmd/root.go` | modifikasi kecil | tambah binding env di `initEnvConfig()` + flag |
| `src/.env.example` | modifikasi kecil | dokumentasikan key baru |

---

## Detail implementasi

### 1. `src/config/settings.go`

Tambahkan blok baru di **akhir** `var (...)`, jangan menyisipkan di tengah blok
yang sudah ada (mengurangi konflik saat sync upstream):

```go
	// Multi-tenant: isolasi data per user (fitur fork, bukan upstream).
	// Saat false, seluruh guard kepemilikan device dilewati dan perilaku
	// aplikasi identik dengan mode single-tenant. Nyalakan hanya setelah
	// fase enforcement (04-05) selesai; lihat docs/multitenant/README.md.
	MultiTenantEnabled = false
	// Umur cookie session login. Session disimpan di tabel user_session dan
	// bisa dicabut, jadi umur panjang tidak berarti tak bisa di-logout.
	MultiTenantSessionTTL = 12 * time.Hour
	// Nama cookie session. Dibuat konfigurabel supaya dua instance gowa di
	// host yang sama (beda base path) tidak saling menimpa cookie.
	MultiTenantSessionCookie = "gowa_session"
	// MultiTenantSecureCookie memaksa atribut Secure pada cookie session.
	// Biarkan false untuk akses HTTP lokal; wajib true di belakang HTTPS.
	MultiTenantSecureCookie = false
```

`time` sudah diimpor di file ini — verifikasi, jangan tambah import ganda.

### 2. `src/cmd/root.go`

Di `initEnvConfig()`, ikuti pola guard `viper.GetString(...) != ""` yang sudah
dipakai untuk `app_ui_enabled` (lihat komentar di file itu: guard ini mencegah
key yang tidak di-set menimpa default dengan zero value):

```go
	if viper.GetString("multi_tenant_enabled") != "" {
		config.MultiTenantEnabled = viper.GetBool("multi_tenant_enabled")
	}
	if v := viper.GetDuration("multi_tenant_session_ttl"); v > 0 {
		config.MultiTenantSessionTTL = v
	}
	if v := strings.TrimSpace(viper.GetString("multi_tenant_session_cookie")); v != "" {
		config.MultiTenantSessionCookie = v
	}
	if viper.GetString("multi_tenant_secure_cookie") != "" {
		config.MultiTenantSecureCookie = viper.GetBool("multi_tenant_secure_cookie")
	}
```

Tambahkan juga flag Cobra di tempat flag lain didaftarkan, ikuti gaya
`rootCmd.PersistentFlags().BoolVar` yang sudah ada:

```go
	rootCmd.PersistentFlags().BoolVar(
		&config.MultiTenantEnabled,
		"multi-tenant-enabled",
		config.MultiTenantEnabled,
		"enable per-user data isolation (device ownership + user management)",
	)
```

Prioritas yang harus dipertahankan: **flag > env > .env** (sama seperti setting
lain di project ini).

### 3. `src/.env.example`

```dotenv
# Multi-tenant (fork): isolasi device per user.
# Jangan nyalakan sebelum fase enforcement selesai — lihat docs/multitenant/.
MULTI_TENANT_ENABLED=false
MULTI_TENANT_SESSION_TTL=12h
MULTI_TENANT_SESSION_COOKIE=gowa_session
MULTI_TENANT_SECURE_COOKIE=false
```

---

## Definition of Done

- [ ] `cd src && go build ./...` sukses.
- [ ] `cd src && go vet ./...` bersih.
- [ ] `cd src && go test ./...` hasilnya **sama** dengan baseline (tidak ada
      regresi baru).
- [ ] `config.MultiTenantEnabled` default `false`.
- [ ] `MULTI_TENANT_ENABLED=true` di env terbaca (buktikan lewat test unit atau
      log startup), tapi belum mengubah perilaku apa pun.
- [ ] `--multi-tenant-enabled=true` sebagai flag menang atas env.
- [ ] Baseline test dicatat di deskripsi PR.

## Verifikasi

```bash
cd src && go build ./... && go vet ./... && go test ./...
```

Cek prioritas flag/env secara manual:

```bash
cd src && MULTI_TENANT_ENABLED=true go run . rest --help | grep -i multi-tenant
```

---

## Catatan & jebakan

- **Jangan** menambah logika apa pun yang membaca flag ini di fase 00. Fase ini
  murni infrastruktur konfigurasi. Kalau ada yang membaca flag, artinya scope
  fase sudah melebar.
- Jangan mengubah default `AppBasicAuthCredential` atau menyentuh
  `newBasicAuthMiddleware` di fase ini — itu pekerjaan fase 03.
- `viper.GetDuration` mengembalikan 0 kalau key tidak ada; guard `> 0` di atas
  penting supaya default 12 jam tidak tertimpa 0.
