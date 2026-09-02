package authhash

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "rahasia123" {
		t.Fatal("hash tidak boleh sama dengan plaintext")
	}
	if !VerifyPassword(hash, "rahasia123") {
		t.Fatal("expected the correct password to verify")
	}
	if VerifyPassword(hash, "rahasia124") {
		t.Fatal("expected a wrong password to fail")
	}
}

// TestHashPasswordIsSalted: dua hash dari password yang sama harus berbeda.
// Kalau sama, berarti hashing tidak ber-salt dan satu tabel rainbow bisa
// membuka semua akun sekaligus.
func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	second, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if first == second {
		t.Fatal("expected bcrypt to salt each hash")
	}
	if !VerifyPassword(second, "rahasia123") {
		t.Fatal("expected the second hash to verify too")
	}
}

func TestHashPasswordRejectsTooShort(t *testing.T) {
	if _, err := HashPassword("short7c"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("err = %v, want ErrPasswordTooShort", err)
	}
	// Tepat di batas harus diterima.
	if _, err := HashPassword("12345678"); err != nil {
		t.Fatalf("password 8 karakter harus diterima: %v", err)
	}
}

// TestHashPasswordRejectsTooLong: bcrypt memotong di 72 byte. Menolak lebih
// baik daripada diam-diam mengabaikan sisanya, yang akan membuat dua password
// berbeda jadi setara.
func TestHashPasswordRejectsTooLong(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("err = %v, want ErrPasswordTooLong", err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Fatalf("password 72 byte harus diterima: %v", err)
	}
}

// TestHashPasswordCountsRunesNotBytes: batas minimum diukur dalam karakter,
// jadi password non-ASCII yang cukup panjang secara visual tidak boleh ditolak
// hanya karena byte-nya lebih banyak.
func TestHashPasswordCountsRunesNotBytes(t *testing.T) {
	if _, err := HashPassword("パスワード12345"); err != nil {
		t.Fatalf("password 10 karakter non-ASCII harus diterima: %v", err)
	}
}

// TestVerifyPasswordRejectsEmptyHash: baris user tanpa password tidak boleh
// bisa login, termasuk dengan string kosong sebagai password.
func TestVerifyPasswordRejectsEmptyHash(t *testing.T) {
	for _, hash := range []string{"", "   "} {
		if VerifyPassword(hash, "") {
			t.Fatalf("hash %q tidak boleh memverifikasi apa pun", hash)
		}
		if VerifyPassword(hash, "rahasia123") {
			t.Fatalf("hash %q tidak boleh memverifikasi apa pun", hash)
		}
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if VerifyPassword("bukan-hash-bcrypt", "rahasia123") {
		t.Fatal("hash yang tidak valid tidak boleh memverifikasi")
	}
}

func TestNewSessionTokenIsRandomAndHashMatches(t *testing.T) {
	token, hash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("new session token: %v", err)
	}
	// 32 byte acak dalam hex.
	if len(token) != 64 {
		t.Fatalf("len(token) = %d, want 64", len(token))
	}
	if len(hash) != 64 {
		t.Fatalf("len(hash) = %d, want 64", len(hash))
	}
	if token == hash {
		t.Fatal("hash tidak boleh sama dengan token")
	}
	if HashToken(token) != hash {
		t.Fatal("HashToken harus menghasilkan hash yang sama dengan NewSessionToken")
	}

	other, _, err := NewSessionToken()
	if err != nil {
		t.Fatalf("new session token: %v", err)
	}
	if other == token {
		t.Fatal("expected each token to be unique")
	}
}

func TestHashTokenIsStable(t *testing.T) {
	if HashToken("abc") != HashToken("abc") {
		t.Fatal("HashToken harus deterministik")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Fatal("token berbeda harus menghasilkan hash berbeda")
	}
}
