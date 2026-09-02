// Package tenancy berisi DTO dan kontrak untuk mode multi-tenant: akun
// aplikasi, kepemilikan device slot, dan session login.
//
// Fitur ini milik fork, bukan upstream, dan seluruhnya berada di belakang
// config.MultiTenantEnabled. Rencana per fase beserta rasionalnya ada di
// docs/multitenant/.
package tenancy

import (
	"strings"
	"time"
)

// Role membatasi apa yang boleh dilihat dan diubah seorang user. Hanya dua
// nilai, dan itu disengaja: setiap role tambahan memperbanyak kombinasi yang
// harus diuji di setiap guard.
type Role string

const (
	// RoleAdmin melihat semua device dan mengelola user.
	RoleAdmin Role = "admin"
	// RoleOperator hanya melihat device miliknya sendiri.
	RoleOperator Role = "operator"
)

// Valid melaporkan apakah r adalah salah satu role yang dikenal. Dipakai
// validasi sebelum menyimpan, supaya string sembarang dari API tidak pernah
// masuk ke kolom role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleOperator
}

// NormalizeUsername mengembalikan bentuk kanonik sebuah username: dipangkas
// dan lowercase.
//
// Satu fungsi dipakai oleh validasi, penyimpanan, dan pencarian. Kalau
// normalisasi berbeda antar lapisan, "Rizki" dan "rizki" bisa lolos jadi dua
// akun terpisah meski ada unique index.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// User adalah akun aplikasi. PasswordHash bertag `json:"-"` supaya tidak
// pernah ikut ke response API; handler tetap punya test yang memastikan itu,
// karena tag JSON mudah hilang saat refactor.
type User struct {
	ID           int64     `db:"id"            json:"id"`
	Username     string    `db:"username"      json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	DisplayName  string    `db:"display_name"  json:"display_name"`
	Role         Role      `db:"role"          json:"role"`
	// DeviceLimit 0 berarti tanpa batas.
	DeviceLimit int       `db:"device_limit" json:"device_limit"`
	Active      bool      `db:"active"       json:"active"`
	CreatedAt   time.Time `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"   json:"updated_at"`
}

// IsAdmin dipakai supaya perbandingan role tidak tersebar sebagai literal
// string ke seluruh guard.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Principal adalah identitas pemanggil yang sudah terautentikasi, hasil dari
// cookie session atau HTTP Basic. Disimpan di fiber Locals per request.
type Principal struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	// ViaBreakGlass menandai identitas yang berasal dari APP_BASIC_AUTH dan
	// bukan dari baris app_user. Hanya untuk audit log — jangan pernah dipakai
	// sebagai syarat otorisasi; role-lah yang menentukan.
	//
	// Principal seperti ini punya UserID 0 dan karenanya tidak memiliki device
	// apa pun. Karena rolenya admin ia tetap bisa melihat semua device dan
	// menetapkan owner, yang cukup untuk memulihkan keadaan.
	ViaBreakGlass bool `json:"-"`
}

// IsAdmin adalah satu-satunya jalan cek privilege di seluruh guard.
func (p *Principal) IsAdmin() bool { return p != nil && p.Role == RoleAdmin }

// DeviceOwner memetakan satu device slot ke pemiliknya.
type DeviceOwner struct {
	DeviceID  string    `db:"device_id"  json:"device_id"`
	UserID    int64     `db:"user_id"    json:"user_id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// Session adalah satu login berbasis cookie.
//
// TokenHash adalah SHA-256 hex dari token yang dipegang browser; tokennya
// sendiri tidak pernah disimpan, jadi salinan DB tidak bisa dipakai untuk
// membajak session yang masih hidup.
type Session struct {
	TokenHash string    `db:"token_hash" json:"-"`
	UserID    int64     `db:"user_id"    json:"user_id"`
	UserAgent string    `db:"user_agent" json:"user_agent"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
}

// Expired melaporkan apakah session sudah lewat masa berlakunya.
//
// Repository sengaja tidak memfilter kedaluwarsa saat membaca — pemanggilnya
// yang memutuskan, supaya test repository tetap deterministik dan penyapu
// session bisa melihat baris yang sudah mati.
func (s *Session) Expired(now time.Time) bool {
	return s == nil || !now.Before(s.ExpiresAt)
}
