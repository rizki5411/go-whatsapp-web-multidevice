package websocket

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/sirupsen/logrus"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

// client menyimpan identitas pemilik satu koneksi.
//
// Sebelumnya struct kosong; sekarang membawa principal supaya hub bisa
// memfilter tanpa query database per pesan.
type client struct {
	principal *domainTenancy.Principal

	// seq adalah nomor urut registrasi koneksi ini; lihat connSeq.
	seq uint64
}

type BroadcastMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Result  any    `json:"result"`
	// DeviceID menandai event ini milik device mana, supaya hub bisa
	// mengirimkannya hanya ke koneksi yang berhak.
	//
	// omitempty membuat payload untuk klien yang sudah ada tidak berubah sama
	// sekali selama nilainya kosong — dashboard gowa-ui tidak perlu tahu
	// apa pun tentang field ini.
	//
	// Kosong berarti event global. Saat mode multi-tenant aktif, event seperti
	// itu HANYA dikirim ke admin: gagal ke arah aman, sehingga broadcast baru
	// dari upstream yang belum ditandai tidak bocor ke operator.
	DeviceID string `json:"device_id,omitempty"`
}

// registration menyertakan identitas saat koneksi mendaftar ke hub.
type registration struct {
	conn      *websocket.Conn
	principal *domainTenancy.Principal
	seq       uint64
}

// directMessage adalah pesan untuk SATU koneksi.
//
// Penulisan tetap lewat hub, bukan langsung dari goroutine pembaca:
// gorilla/websocket tidak mengizinkan penulisan konkuren pada satu koneksi, dan
// hub juga menulis ke koneksi yang sama.
type directMessage struct {
	conn *websocket.Conn
	// seq mengunci pesan ini ke SATU registrasi koneksi. *websocket.Conn
	// berasal dari sync.Pool milik contrib, jadi pointernya dipakai ulang oleh
	// koneksi berikutnya; tanpa seq, pesan yang masih menunggu di buffer bisa
	// tertulis ke koneksi lain yang kebetulan mewarisi pointer yang sama.
	seq uint64
	msg BroadcastMessage
}

var (
	Clients    = make(map[*websocket.Conn]client)
	Register   = make(chan registration)
	Broadcast  = make(chan BroadcastMessage)
	Unregister = make(chan *websocket.Conn)
	// Direct diberi buffer supaya goroutine pembaca tidak memblokir saat hub
	// sibuk. Kalau buffer penuh ia memang memblokir — pesan tidak boleh
	// dibuang, karena klien akan menunggu daftar device yang tak pernah datang.
	Direct = make(chan directMessage, 32)
	// Revoke meminta hub menutup semua koneksi milik satu user.
	//
	// Principal disalin sekali saat koneksi dibuka dan tidak diperiksa ulang:
	// memvalidasinya per pesan berarti satu query per pesan per koneksi, di
	// dalam loop panas hub. Konsekuensinya principal bisa basi, dan koneksi
	// WebSocket berumur sangat panjang — tab dashboard bisa terbuka berhari-hari.
	// Karena itu pencabutan hak dikirim ke sini sebagai peristiwa dan koneksinya
	// DIPUTUS. Klien tinggal menyambung ulang; kalau haknya memang sudah
	// dicabut, AuthGate dan DeviceOwnerGuard yang menolak — penegakannya tetap
	// di satu tempat, bukan disalin ke sini.
	//
	// Kirim lewat RevokeUser, jangan langsung ke channel-nya.
	Revoke = make(chan int64, 64)
)

// connSeq menomori setiap registrasi koneksi.
//
// Dinaikkan dari goroutine pembaca (satu per koneksi), jadi harus atomic —
// ini satu-satunya state paket di file ini yang disentuh di luar RunHub, dan
// ia sengaja dibuat tidak butuh penguncian.
var connSeq uint64

// ownership dipasang oleh RegisterRoutes.
//
// Variabel paket, mengikuti Clients/Register/Broadcast di atas: memperkenalkan
// pola kedua di file yang sudah memakai state paket hanya akan membuatnya lebih
// sulit dibaca. nil berarti fitur tidak terpasang, dan filter jadi transparan.
var ownership domainTenancy.IDeviceOwnership

// PrincipalResolver membaca identitas pemanggil dari request.
//
// Disuntikkan sebagai fungsi, bukan dengan mengimpor ui/rest/middleware:
// middleware itu mengimpor infrastructure/whatsapp, yang mengimpor paket ini,
// sehingga import langsung akan membentuk siklus.
type PrincipalResolver func(c fiber.Ctx) *domainTenancy.Principal

func handleRegister(reg registration) {
	Clients[reg.conn] = client{principal: reg.principal, seq: reg.seq}
	logrus.Println("connection registered")
}

// RevokeUser memutus semua koneksi WebSocket milik satu user.
//
// Dipanggil setiap kali hak atau keabsahan akun berubah — role diturunkan,
// akun dinonaktifkan, password diganti admin, user dihapus.
//
// Tidak boleh memblokir pemanggilnya: pemanggilnya adalah handler HTTP admin,
// dan hub bisa sedang tertahan menulis ke koneksi yang lambat. Tidak boleh pula
// membuang pesan — pencabutan yang hilang berarti koneksi yang seharusnya putus
// tetap hidup dan tetap menerima event. Karena itu kalau buffer penuh,
// pengirimannya dilanjutkan di goroutine sendiri; pencabutan adalah aksi admin
// yang jarang, jadi goroutine cadangan itu tidak akan menumpuk.
func RevokeUser(userID int64) {
	if userID == 0 {
		// UserID 0 adalah principal break-glass, yang tidak punya baris
		// app_user. Mencabut atas nilai itu akan memutus koneksi yang tidak ada
		// hubungannya dengan perubahan apa pun.
		return
	}
	select {
	case Revoke <- userID:
	default:
		go func() { Revoke <- userID }()
	}
}

// connsOfUser mengumpulkan koneksi milik satu user.
//
// Terpisah dari handleRevoke supaya keputusan "koneksi mana yang diputus" bisa
// diuji tanpa koneksi jaringan sungguhan.
func connsOfUser(userID int64) []*websocket.Conn {
	var targets []*websocket.Conn
	for conn, cl := range Clients {
		if cl.principal != nil && cl.principal.UserID == userID {
			targets = append(targets, conn)
		}
	}
	return targets
}

func handleRevoke(userID int64) {
	if userID == 0 {
		return
	}
	for _, conn := range connsOfUser(userID) {
		logrus.Printf("connection revoked for user %d", userID)
		closeConnection(conn)
	}
}

func handleUnregister(conn *websocket.Conn) {
	delete(Clients, conn)
	logrus.Println("connection unregistered")
}

// mayReceive menentukan apakah satu koneksi berhak menerima satu event.
func mayReceive(cl client, msg BroadcastMessage) bool {
	if !config.MultiTenantEnabled {
		return true
	}
	// Gagal ke arah aman: koneksi tanpa identitas tidak menerima apa pun.
	if cl.principal == nil {
		return false
	}
	if cl.principal.IsAdmin() {
		return true
	}
	// Event global (tanpa device) hanya untuk admin — lihat komentar DeviceID.
	if msg.DeviceID == "" {
		return false
	}
	if ownership == nil {
		return false
	}
	return ownership.CanAccess(cl.principal, msg.DeviceID)
}

// forWire menyiapkan pesan untuk dikirim.
//
// DeviceID dibuang saat mode multi-tenant mati, supaya payload di mode
// single-tenant tetap identik dengan sebelum fitur ini ada. Dengan begitu titik
// broadcast bisa SELALU menandai device-nya tanpa perlu memeriksa flag
// sembilan kali, dan satu-satunya tempat yang peduli mode adalah di sini.
func (m BroadcastMessage) forWire() BroadcastMessage {
	if !config.MultiTenantEnabled {
		m.DeviceID = ""
	}
	return m
}

func broadcastMessage(message BroadcastMessage) {
	marshalMessage, err := json.Marshal(message.forWire())
	if err != nil {
		logrus.Println("marshal error:", err)
		return
	}

	for conn, cl := range Clients {
		if !mayReceive(cl, message) {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, marshalMessage); err != nil {
			logrus.Println("write error:", err)
			closeConnection(conn)
		}
	}
}

// writeTo mengirim satu pesan ke satu registrasi koneksi.
//
// seq dibandingkan, bukan cuma keberadaan conn di Clients: pointer koneksi
// didaur ulang lewat sync.Pool contrib, jadi entri Clients yang ada belum tentu
// koneksi yang meminta pesan ini.
func writeTo(conn *websocket.Conn, seq uint64, message BroadcastMessage) {
	if conn == nil {
		return
	}
	cl, ok := Clients[conn]
	if !ok || cl.seq != seq {
		// Koneksi sudah lepas sebelum pesannya sampai di hub — atau pointernya
		// sudah dipakai ulang koneksi lain.
		return
	}
	marshalMessage, err := json.Marshal(message.forWire())
	if err != nil {
		logrus.Println("marshal error:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, marshalMessage); err != nil {
		logrus.Println("write error:", err)
		closeConnection(conn)
	}
}

func closeConnection(conn *websocket.Conn) {
	if err := conn.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
		logrus.Println("write close message error:", err)
	}
	if err := conn.Close(); err != nil {
		logrus.Println("close connection error:", err)
	}
	delete(Clients, conn)
}

// RunHub adalah satu-satunya goroutine yang menyentuh Clients.
//
// Itulah sebabnya tidak ada mutex di file ini. Kalau ada kode baru yang
// mengiterasi Clients dari luar sini, itu race — perbaiki dengan channel,
// bukan dengan menambah mutex.
func RunHub() {
	for {
		select {
		case reg := <-Register:
			handleRegister(reg)

		case conn := <-Unregister:
			handleUnregister(conn)

		case message := <-Broadcast:
			logrus.Println("message received:", message)
			broadcastMessage(message)

		case direct := <-Direct:
			writeTo(direct.conn, direct.seq, direct.msg)

		case userID := <-Revoke:
			handleRevoke(userID)
		}
	}
}

// filterDevicesForPrincipal menyaring daftar device menurut kepemilikan.
//
// Diimplementasikan di sini alih-alih memakai ui/rest/tenantfilter, dengan
// alasan yang sama seperti PrincipalResolver: import ke arah itu akan
// membentuk siklus lewat infrastructure/whatsapp.
func filterDevicesForPrincipal(principal *domainTenancy.Principal, devices []domainApp.DevicesResponse) []domainApp.DevicesResponse {
	if !config.MultiTenantEnabled || ownership == nil {
		return devices
	}

	allowed, all := ownership.OwnedDeviceIDs(principal)
	if all {
		return devices
	}

	permitted := make(map[string]struct{}, len(allowed))
	for _, deviceID := range allowed {
		permitted[deviceID] = struct{}{}
	}

	filtered := make([]domainApp.DevicesResponse, 0, len(devices))
	for _, device := range devices {
		// DevicesResponse.Device berisi id slot device (inst.ID()), bukan JID —
		// itulah kunci yang dipakai tabel device_owner.
		if _, ok := permitted[device.Device]; ok {
			filtered = append(filtered, device)
		}
	}
	return filtered
}

// wsPrincipalLocalsKey membawa principal melewati upgrade WebSocket.
//
// Harus berupa string: contrib/websocket menyalin locals lewat
// RequestCtx.VisitUserValues, yang HANYA mengunjungi key bertipe string
// (fasthttp memeriksa `kv.key.(string)`). Key privat bertipe struct yang
// dipakai middleware.StorePrincipal karena itu tidak akan sampai ke
// conn.Locals.
const wsPrincipalLocalsKey = "gowa_ws_principal"

func RegisterRoutes(
	app fiber.Router,
	service domainApp.IAppUsecase,
	own domainTenancy.IDeviceOwnership,
	principalOf PrincipalResolver,
) {
	ownership = own

	app.Use("/ws", func(c fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			// Principal hanya tersedia selagi fiber.Ctx masih hidup, jadi ia
			// dititipkan di locals di sini untuk dibaca setelah upgrade.
			if principalOf != nil {
				if principal := principalOf(c); principal != nil {
					c.Locals(wsPrincipalLocalsKey, principal)
				}
			}
			return c.Next()
		}
		return c.SendStatus(fiber.StatusUpgradeRequired)
	})

	app.Get("/ws", websocket.New(func(conn *websocket.Conn) {
		defer func() {
			Unregister <- conn
			_ = conn.Close()
		}()

		principal, _ := conn.Locals(wsPrincipalLocalsKey).(*domainTenancy.Principal)
		seq := atomic.AddUint64(&connSeq, 1)
		Register <- registration{conn: conn, principal: principal, seq: seq}

		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logrus.Println("read error:", err)
				}
				return
			}

			if messageType == websocket.TextMessage {
				var messageData BroadcastMessage
				if err := json.Unmarshal(message, &messageData); err != nil {
					logrus.Println("unmarshal error:", err)
					return
				}

				if messageData.Code == "FETCH_DEVICES" {
					devices, _ := service.FetchDevices(context.Background())
					reply := BroadcastMessage{
						Code:    "LIST_DEVICES",
						Message: "Device found",
						Result:  filterDevicesForPrincipal(principal, devices),
					}

					if config.MultiTenantEnabled {
						// Balasan dikirim HANYA ke koneksi yang meminta, dan
						// isinya disaring. Menyiarkannya adalah kebocoran
						// langsung yang tidak bisa ditutup oleh filter
						// DeviceID, karena satu payload memuat banyak device
						// sekaligus.
						Direct <- directMessage{conn: conn, seq: seq, msg: reply}
					} else {
						// Mode single-tenant mempertahankan penyiaran ke semua
						// koneksi. Perilaku itu memang aneh — satu koneksi
						// meminta, semua menerima — tapi mengubahnya di sini
						// akan membuat rollback dengan mematikan flag tidak
						// lagi memulihkan perilaku lama secara utuh.
						Broadcast <- reply
					}
				}
			} else {
				logrus.Println("unsupported message type:", messageType)
			}
		}
	}))
}
