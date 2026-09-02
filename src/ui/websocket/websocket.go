package websocket

import (
	"context"
	"encoding/json"

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
}

// directMessage adalah pesan untuk SATU koneksi.
//
// Penulisan tetap lewat hub, bukan langsung dari goroutine pembaca:
// gorilla/websocket tidak mengizinkan penulisan konkuren pada satu koneksi, dan
// hub juga menulis ke koneksi yang sama.
type directMessage struct {
	conn *websocket.Conn
	msg  BroadcastMessage
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
)

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
	Clients[reg.conn] = client{principal: reg.principal}
	logrus.Println("connection registered")
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

// writeTo mengirim satu pesan ke satu koneksi.
func writeTo(conn *websocket.Conn, message BroadcastMessage) {
	if conn == nil {
		return
	}
	if _, ok := Clients[conn]; !ok {
		// Koneksi sudah lepas sebelum pesannya sampai di hub.
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
			writeTo(direct.conn, direct.msg)
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
		Register <- registration{conn: conn, principal: principal}

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
						Direct <- directMessage{conn: conn, msg: reply}
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
