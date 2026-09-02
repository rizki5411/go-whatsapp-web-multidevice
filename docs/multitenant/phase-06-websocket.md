# Fase 06 — Isolasi WebSocket

**Tujuan:** event realtime (QR, login, logout, pesan masuk) hanya sampai ke
koneksi milik pemilik device yang bersangkutan. Ini menutup L7.

**Tergantung:** Fase 04 (butuh `IDeviceOwnership`)
**Branch:** `feature/multitenant-phase06`

Fase ini bisa dikerjakan paralel dengan fase 05 dan 08.

---

## Prasyarat

Baca seluruh `src/ui/websocket/websocket.go` — filenya pendek (~140 baris) dan
seluruh perubahan fase ini ada di situ plus pemanggilnya.

Struktur yang ada sekarang:

```go
var (
	Clients    = make(map[*websocket.Conn]client)  // client adalah struct{} kosong
	Register   = make(chan *websocket.Conn)
	Broadcast  = make(chan BroadcastMessage)
	Unregister = make(chan *websocket.Conn)
)
```

`RunHub()` adalah satu goroutine yang melayani keempat channel. `broadcastMessage`
menulis ke **semua** koneksi. `BroadcastMessage` hanya punya `Code`, `Message`,
`Result` — **tidak ada `device_id`**, itu masalah pertama yang harus diselesaikan.

Pemanggil `websocket.Broadcast` (verifikasi ulang dengan grep, jangan percaya
daftar ini mentah-mentah):

```bash
cd src && grep -rn "websocket.Broadcast" --include=*.go . | grep -v _test
```

Saat audit: 5 titik di `infrastructure/whatsapp/event_handler.go`, 1 di
`usecase/app.go`, 3 di `usecase/device.go`.

---

## File yang disentuh

| File | Jenis | Perubahan |
|------|-------|-----------|
| `src/ui/websocket/websocket.go` | modifikasi terukur | field `DeviceID`, principal per koneksi, filter, channel direct |
| `src/ui/websocket/websocket_test.go` | **BARU** (atau tambah) | test filter |
| `src/infrastructure/whatsapp/event_handler.go` | modifikasi kecil | isi `DeviceID` di 5 broadcast |
| `src/usecase/app.go` | modifikasi kecil | isi `DeviceID` di 1 broadcast |
| `src/usecase/device.go` | modifikasi kecil | isi `DeviceID` di 3 broadcast |
| `src/cmd/rest.go` | modifikasi kecil | teruskan `ownership` ke `RegisterRoutes` |

Ini fase dengan sentuhan terbanyak pada file upstream. Karena itu: **tambah
field, jangan ubah yang ada**. `BroadcastMessage` yang tidak mengisi `DeviceID`
harus tetap kompilasi dan tetap berperilaku benar.

---

## Detail implementasi

### 1. Tambah `DeviceID` ke `BroadcastMessage`

```go
type BroadcastMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Result  any    `json:"result"`
	// DeviceID menandai event ini milik device mana, supaya hub bisa
	// mengirimkannya hanya ke koneksi yang berhak. Kosong berarti event global
	// (tidak terikat device); lihat aturan event kosong di bawah.
	DeviceID string `json:"device_id,omitempty"`
}
```

`omitempty` penting: payload untuk klien yang sudah ada tidak berubah sama sekali
selama `DeviceID` kosong. Dengan begitu dashboard gowa-ui tidak perlu tahu apa
pun tentang field baru ini.

### 2. Aturan event tanpa `DeviceID` — keputusan yang dikunci

> Saat `MultiTenantEnabled` aktif, `BroadcastMessage` dengan `DeviceID` kosong
> **hanya** dikirim ke koneksi admin.

Sama seperti aturan device tak-ber-owner di fase 04, ini gagal ke arah yang aman:
broadcast baru dari upstream yang belum kita tandai tidak akan bocor ke operator.
Gejalanya "ada event yang tidak muncul", bukan "event orang lain muncul".

Karena itu, mengisi `DeviceID` di **semua sembilan** titik broadcast adalah
bagian wajib dari fase ini — kalau terlewat, operator tidak akan melihat QR-nya
sendiri dan fiturnya kelihatan rusak.

### 3. Principal per koneksi

```go
// client menyimpan identitas pemilik koneksi. Sebelumnya struct kosong; sekarang
// membawa principal supaya hub bisa memfilter tanpa query DB per pesan.
type client struct {
	principal *tenancy.Principal
}
```

Diisi saat register. Karena `Register` adalah `chan *websocket.Conn`, ada dua
pilihan:

- **(disarankan)** ganti jadi `chan registration` di mana
  `type registration struct { conn *websocket.Conn; principal *tenancy.Principal }`.
  Perubahan kecil, dan hanya `RegisterRoutes` + `handleRegister` yang menyentuh
  channel ini.
- atau simpan principal di map terpisah yang dikunci mutex — lebih banyak state,
  lebih mudah salah. Jangan.

`RegisterRoutes` mendapat principal dari `fiber.Ctx` **sebelum** upgrade, di
middleware `app.Use("/ws", ...)` yang sudah ada. Setelah upgrade, `fiber.Ctx`
tidak lagi tersedia — jadi principal **harus** diambil di handler
pre-upgrade dan dititipkan lewat `websocket.Locals`:

```go
app.Use("/ws", func(c fiber.Ctx) error {
    if websocket.IsWebSocketUpgrade(c) {
        if p := middleware.PrincipalFrom(c); p != nil {
            c.Locals("ws_principal", p)   // dibaca lewat conn.Locals setelah upgrade
        }
        return c.Next()
    }
    return c.SendStatus(fiber.StatusUpgradeRequired)
})
```

Verifikasi bahwa `github.com/gofiber/contrib/v3/websocket` memang menyalin
`c.Locals` ke `conn.Locals` (paket contrib biasanya begitu). Kalau tidak, ambil
principal di dalam handler `websocket.New` lewat cara lain — **jangan** mengubah
`AuthGate`. Cek cepat:

```bash
grep -rn "Locals" /c/Users/User/go/pkg/mod/github.com/gofiber/contrib/v3/websocket*/[a-z]*.go | head
```

Signature `RegisterRoutes` berubah:

```go
func RegisterRoutes(app fiber.Router, service domainApp.IAppUsecase, own tenancy.IDeviceOwnership)
```

Update pemanggilnya di `src/cmd/rest.go` (`registerDeviceScopedRoutes`).

### 4. Filter di `broadcastMessage`

```go
func broadcastMessage(message BroadcastMessage) {
	marshalled, err := json.Marshal(message)
	if err != nil { ...; return }

	for conn, cl := range Clients {
		if !mayReceive(cl, message) {
			continue
		}
		// ... tulis seperti sebelumnya
	}
}

// mayReceive menentukan apakah satu koneksi berhak menerima satu event.
func mayReceive(cl client, msg BroadcastMessage) bool {
	if !config.MultiTenantEnabled {
		return true
	}
	if cl.principal == nil {
		return false            // fail closed
	}
	if cl.principal.IsAdmin() {
		return true
	}
	if msg.DeviceID == "" {
		return false            // event global -> admin saja
	}
	return ownership.CanAccess(cl.principal, msg.DeviceID)
}
```

`ownership` di sini perlu jadi variabel paket, diisi dari `RegisterRoutes`.
Variabel paket memang bukan yang paling rapi, tapi `Clients`, `Register`, dan
`Broadcast` sudah variabel paket di file ini — mengikuti pola yang ada lebih baik
daripada memperkenalkan pola kedua. Beri komentar yang menjelaskan itu.

`RunHub()` dijalankan sebagai satu goroutine, jadi `mayReceive` selalu dipanggil
dari goroutine itu — tidak ada race pada `Clients`. Jangan menambah mutex; kalau
tergoda, artinya ada akses `Clients` dari luar hub, dan itu yang harus
diperbaiki.

### 5. `FETCH_DEVICES` — jangan lewat `Broadcast`

Kode sekarang: klien mengirim `{"code":"FETCH_DEVICES"}`, handler memanggil
`service.FetchDevices()` lalu mengirim hasilnya lewat `Broadcast` — yaitu **ke
semua koneksi**. Bahkan tanpa multi-tenant ini sudah aneh; dengan multi-tenant
ini kebocoran langsung, dan tidak bisa diperbaiki dengan filter `DeviceID`
karena payload-nya berisi daftar banyak device.

Perbaikannya: kirim balasan hanya ke koneksi yang meminta, dan filter isinya.

Tambahkan channel baru supaya penulisan tetap terserialisasi di hub (menulis
langsung dari goroutine pembaca akan race dengan penulisan hub ke conn yang sama):

```go
type directMessage struct {
	conn *websocket.Conn
	msg  BroadcastMessage
}

var Direct = make(chan directMessage, 32)
```

Tangani di `RunHub()`:

```go
		case dm := <-Direct:
			writeTo(dm.conn, dm.msg)
```

Lalu di handler `FETCH_DEVICES`:

```go
			devices, _ := service.FetchDevices(context.Background())
			devices = filterDevicesForPrincipal(principal, devices)
			Direct <- directMessage{conn: conn, msg: BroadcastMessage{
				Code:    "LIST_DEVICES",
				Message: "Device found",
				Result:  devices,
			}}
```

`filterDevicesForPrincipal` memakai `OwnedDeviceIDs` dari fase 04. Perhatikan:
`domainApp.DevicesResponse` — cek nama field id-nya, jangan asumsi
(`grep -n "type DevicesResponse" -A 10 src/domains/app/app.go`).

Channel `Direct` diberi buffer supaya goroutine pembaca tidak memblokir kalau hub
sedang sibuk; kalau buffer penuh, biarkan memblokir (jangan `select default` dan
membuang pesan — klien akan menunggu daftar device yang tidak pernah datang).

### 6. Mengisi `DeviceID` di sembilan titik broadcast

Untuk masing-masing, cari device id yang sudah ada di scope-nya:

- `event_handler.go` — handler event punya akses ke `DeviceInstance`; pakai
  `instance.ID()`. Kalau tidak, event handler dipanggil per-device dan device
  id-nya tersedia dari closure atau field struct. **Jangan** memakai JID —
  `device_owner` ter-key oleh device slot id.
- `usecase/app.go` (`Logout`) dan `usecase/device.go` — ketiganya sudah menerima
  `deviceID` sebagai parameter. Langsung pakai.

Beberapa broadcast di `usecase/app.go` dan `device.go` menyertakan daftar
`devices` di `Result` (contoh: `DEVICE_LOGGED_OUT` mengirim `devices` hasil
`FetchDevices`). **Itu kebocoran kedua**: payload-nya sendiri berisi daftar
device orang lain, dan filter berbasis `DeviceID` tidak menolongnya.

Dua pilihan, pilih yang pertama:

1. **Buang daftar `devices` dari payload saat flag on**, sisakan `device_id` saja.
   Klien yang butuh daftar terbaru bisa mengirim `FETCH_DEVICES` (yang sekarang
   sudah terfilter). Ini menghilangkan seluruh kelas masalah.
2. Filter daftar itu per koneksi — berarti payload harus di-marshal ulang per
   koneksi, dan `broadcastMessage` yang sekarang me-marshal sekali harus dirombak.
   Lebih mahal dan lebih mudah salah.

Kalau memilih (1), pastikan flag off tetap mengirim payload lengkap seperti
sekarang, supaya gowa-ui di mode single-tenant tidak berubah perilakunya.

---

## Definition of Done

- [ ] `cd src && go build ./... && go vet ./... && go test ./...` hijau.
- [ ] `go test -race ./ui/websocket/...` hijau (fase ini menambah channel dan
      state per koneksi — race detector wajib).
- [ ] Flag off: payload WebSocket **identik byte-per-byte** dengan sebelum fase
      ini untuk semua event. Bandingkan langsung dengan menyalakan dua build.
- [ ] Semua sembilan titik broadcast mengisi `DeviceID`. Buktikan dengan grep
      bahwa tidak ada `websocket.BroadcastMessage{` tanpa `DeviceID` di luar
      test.
- [ ] Flag on, dua operator terhubung bersamaan:
      - QR device A hanya muncul di koneksi operator A
      - event pesan masuk device B hanya muncul di koneksi operator B
      - admin melihat keduanya
- [ ] `FETCH_DEVICES` dari operator A hanya mengembalikan device A, dan **hanya
      terkirim ke koneksi A** (koneksi B tidak menerima apa pun).
- [ ] Payload `DEVICE_LOGGED_OUT` tidak lagi memuat daftar device orang lain.
- [ ] Koneksi tanpa principal (kalau bisa terjadi) tidak menerima apa pun.
- [ ] `git diff --stat src/ui/websocket/websocket.go` — perubahan terukur, bukan
      penulisan ulang file.

## Verifikasi

```bash
cd src && go test -race ./ui/websocket/... -v
```

Manual, dua terminal `websocat` (atau `wscat`) sekaligus:

```bash
websocat "ws://localhost:3000/ws?authorization=$(printf 'operator1:rahasia123' | base64)"
```

```bash
websocat "ws://localhost:3000/ws?authorization=$(printf 'operator2:rahasia123' | base64)"
```

Lalu di terminal ketiga, picu login device A dan amati terminal mana yang
menerima QR:

```bash
curl -u operator1:rahasia123 localhost:3000/devices/dev-a/login
```

Hanya terminal operator1 yang boleh menampilkan event QR.

---

## Catatan & jebakan

- **Jangan** menulis ke `conn` dari goroutine pembaca. `gorilla/websocket` (yang
  dipakai contrib) tidak mengizinkan penulisan konkuren pada satu koneksi;
  channel `Direct` ada khusus untuk itu.
- `Clients` hanya boleh diakses dari dalam `RunHub()`. Kalau ada kode baru yang
  mengiterasinya dari luar, itu race — perbaiki dengan channel, bukan mutex.
- `WebsocketQueryAuth` (`src/ui/rest/middleware/websocketauth.go`) memindahkan
  `?authorization=` ke header **sebelum** auth gate. Jangan mengubah middleware
  itu; `AuthGate` dari fase 03 sudah membaca header hasilnya.
- Jangan menambahkan query `device_id` sebagai penentu langganan WebSocket. Klien
  bisa mengirim apa pun di situ; yang menentukan tetap kepemilikan.
- Kalau `conn.Locals` ternyata tidak membawa nilai dari `c.Locals`, jangan
  mengakali dengan menaruh principal di query string atau di map global ber-key
  remote address. Ambil ulang principal dari header `Authorization` di dalam
  handler pre-upgrade dan simpan ke `registration` — header masih tersedia di
  situ.
- Setelah fase ini permukaan HTTP dan WebSocket sudah terisolasi. Yang tersisa:
  MCP (L8) dan audit menyeluruh — fase 07.
