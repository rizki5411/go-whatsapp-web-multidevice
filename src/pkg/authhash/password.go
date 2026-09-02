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
