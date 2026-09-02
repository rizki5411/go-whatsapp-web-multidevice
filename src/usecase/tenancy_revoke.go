package usecase

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
)

// Pencabutan koneksi WebSocket saat hak akun berubah.
//
// Principal koneksi WebSocket disalin sekali saat upgrade dan tidak pernah
// divalidasi ulang — lihat komentar channel Revoke di ui/websocket/websocket.go.
// Tanpa berkas ini, "turunkan role" dan "nonaktifkan akun" berlaku seketika di
// HTTP tetapi TIDAK sama sekali pada koneksi WebSocket yang sudah terbuka, dan
// koneksi itu berumur sangat panjang. Admin yang diturunkan jadi operator akan
// terus menerima event seluruh tenant selama tab-nya terbuka.
//
// Dipisah ke berkas sendiri supaya alasannya punya tempat, dan supaya diff di
// tenancy.go tetap sebatas titik panggilnya.

// revokeWebsocketSessions memutus koneksi WebSocket milik satu user.
//
// Perubahan yang TIDAK memanggil ini: display_name, device_limit, dan
// ChangeOwnPassword. Dua yang pertama tidak mengubah hak apa pun; yang ketiga
// sengaja tidak menendang pengguna dari perangkat yang sedang dipakainya
// (lihat komentar di ChangeOwnPassword), dan mengganti password sendiri juga
// tidak mengubah haknya.
//
// Kepemilikan device tidak perlu ada di sini: mayReceive membaca kepemilikan
// lewat ownership.CanAccess setiap kali, jadi pemindahan device sudah berlaku
// seketika pada koneksi yang sedang terbuka.
func revokeWebsocketSessions(userID int64) {
	websocket.RevokeUser(userID)
}
