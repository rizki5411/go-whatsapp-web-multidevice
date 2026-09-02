// Package authhash menyediakan hashing password dan pembuatan token session
// untuk mode multi-tenant.
//
// Dipisah dari usecase supaya aturan kekuatan password hidup di satu tempat
// dan bisa diuji tanpa database.
package authhash

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength ditegakkan di satu tempat supaya jalur REST dan jalur
// seeding admin tunduk pada aturan yang sama.
const MinPasswordLength = 8

// MaxPasswordBytes: bcrypt memotong input di 72 byte. Menolak yang lebih
// panjang lebih baik daripada diam-diam mengabaikan sisanya, yang akan membuat
// dua password berbeda jadi setara.
const MaxPasswordBytes = 72

var (
	ErrPasswordTooShort = errors.New("password minimal 8 karakter")
	ErrPasswordTooLong  = errors.New("password maksimal 72 byte")
)

// HashPassword mengembalikan hash bcrypt dari plain, atau error validasi kalau
// panjangnya di luar batas.
func HashPassword(plain string) (string, error) {
	if utf8.RuneCountInString(plain) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// VerifyPassword mengembalikan true hanya kalau hash cocok dengan plain.
//
// Hash kosong selalu gagal: baris user yang belum punya password tidak boleh
// bisa login, dan bcrypt sendiri akan menolak hash kosong dengan error yang
// mudah tertelan kalau tidak diperiksa di sini.
func VerifyPassword(hash, plain string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// DummyHash mengembalikan hash bcrypt yang valid dan tidak cocok dengan
// password apa pun yang bisa diketahui seseorang.
//
// Jalur login memakainya untuk tetap menjalankan satu verifikasi bcrypt saat
// username tidak ditemukan. Tanpa itu, permintaan untuk username yang tidak
// terdaftar akan kembali jauh lebih cepat daripada yang terdaftar, dan selisih
// waktu itu membocorkan username mana yang ada.
//
// Nilainya di-generate dari 32 byte acak saat pertama dipakai, BUKAN
// dituliskan sebagai konstanta: hash bcrypt yang beredar di internet umumnya
// adalah test vector dari password umum seperti "password", yang justru akan
// membuat fungsi ini memverifikasi sesuatu yang berguna.
//
// Biayanya satu operasi bcrypt sekali per proses, dan hasilnya di-cache supaya
// waktu verifikasi tetap konstan setelahnya.
func DummyHash() string { return dummyHash() }

var dummyHash = sync.OnceValue(func() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand praktis tidak pernah gagal. Kalaupun gagal, yang
		// dibutuhkan di sini cuma hash valid yang menghabiskan waktu bcrypt,
		// jadi nilai tetap pun sudah memenuhi tujuannya.
		buf = []byte("gowa-login-timing-equalizer")
	}
	hashed, err := bcrypt.GenerateFromPassword(buf, bcrypt.DefaultCost)
	if err != nil {
		return ""
	}
	return string(hashed)
})

// NewSessionToken mengembalikan token acak 256-bit beserta hash SHA-256-nya.
// Yang pertama dikirim ke browser sebagai cookie, yang kedua yang disimpan.
func NewSessionToken() (token, tokenHash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken dipakai baik saat menerbitkan maupun saat memverifikasi session,
// supaya kedua sisi tidak pernah memakai algoritma yang berbeda.
//
// SHA-256 tanpa salt sudah cukup di sini, berbeda dari password: token adalah
// nilai acak 256-bit, jadi tidak ada ruang tebakan yang bisa dipercepat dengan
// rainbow table. Pola yang sama dipakai ui/mcp/oauth/store.go.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
