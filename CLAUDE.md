# Fork & Workflow Rules — WAJIB DIBACA SEBELUM CODING

## Konteks
Repo ini adalah FORK dari https://github.com/aldinokemal/go-whatsapp-web-multidevice
(remote `upstream`). Tujuan fork: kustomisasi internal yang TIDAK akan di-kontribusikan 
balik ke upstream. Fork ini akan terus sync perubahan dari upstream secara berkala.

## Branch strategy
- `main`      → mirror upstream + minimal cross-platform fix (jangan taruh fitur custom di sini)
- `develop`   → integrasi semua kerjaan custom, basis deploy
- `feature/*` → 1 branch per perubahan, dibuat dari `develop`

## Aturan coding — WAJIB DIIKUTI
1. **Jangan refactor/rewrite file upstream yang tidak relevan** dengan task yang diminta.
   Prioritaskan menambah file/fungsi baru daripada mengubah struktur file yang sudah ada,
   supaya sync upstream di masa depan minim conflict.
2. Kalau terpaksa mengubah file upstream yang sudah ada (mis. `src/cmd/rest.go`), buat
   perubahan sekecil dan se-lokal mungkin — jangan reformat/reorganisasi seluruh file.
3. Kode custom baru taruh di folder/file baru yang jelas terpisah, misalnya:
   `src/domains/auth/`, `src/ui/rest/auth.go`, `src/ui/rest/middleware/jwtauth.go`.
4. Sebelum mengubah file lama, cek dulu apakah bisa dicapai lewat penambahan (additive),
   bukan modifikasi struktur yang sudah ada.
5. Selalu baca `AGENTS.md` juga untuk arsitektur, code map, dan convention project ini —
   file ini isinya panduan dari upstream, tetap jadi acuan utama soal struktur kode.
6. Jangan hapus atau ubah perilaku existing endpoint/response yang tidak diminta eksplisit.

## Status kustomisasi saat ini
- [x] Command system per device (!ping, !forward, dst) — `src/infrastructure/whatsapp/event_command_handler.go`,
      config per device di tabel `device_command_config` + `src/ui/rest/command_config.go`
- [ ] Antrian pesan personal per device (persisten, delay acak 2-5mnt)

## Pola yang WAJIB diikuti untuk fitur baru
- Config per-device yang perlu disimpan & diatur lewat API: ikuti pola
  `ChatwootDeviceConfig` + `chatwoot_config.go` (sudah ada di project ini) —
  jangan bikin pola baru.
- Worker/proses background yang nyala-mati ngikutin lifecycle device: ikuti
  pola `StartPresencePulseScheduler` (`presence_pulse.go`).
- Migration tabel baru: HARUS ditambahkan di akhir `getMigrations()`
  (`sqlite_repository.go`), append-only, jangan disisipkan di tengah.