package validations

import (
	"context"
	"strings"
	"testing"
)

func TestValidateNewDeviceIDAcceptsEmptyAsAutoGenerate(t *testing.T) {
	// Kosong BUKAN kesalahan: itu cara memintanya dibuatkan otomatis.
	if err := ValidateNewDeviceID(context.Background(), ""); err != nil {
		t.Fatalf("expected empty device id to be accepted, got %v", err)
	}
}

func TestValidateNewDeviceIDAcceptsGeneratedUUID(t *testing.T) {
	// Aturan di sini tidak boleh menolak bentuk id yang backend sendiri buat,
	// atau id lama akan jadi id yang tidak bisa dibuat ulang.
	if err := ValidateNewDeviceID(context.Background(), "82be448d-9e72-4df0-8c6f-e8a687646678"); err != nil {
		t.Fatalf("expected generated UUID to be accepted, got %v", err)
	}
}

func TestValidateNewDeviceIDAcceptsReadableIDs(t *testing.T) {
	for _, id := range []string{"cs-utama", "cs_utama", "cs.utama.01", "CS1", "dev-2026"} {
		if err := ValidateNewDeviceID(context.Background(), id); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", id, err)
		}
	}
}

// Ini penjaga yang sesungguhnya. Id device disisipkan ke path
// (/devices/:device_id/...) dan dikirim sebagai nilai header X-Device-Id, jadi
// karakter di bawah ini bukan sekadar jelek — ia mengubah rute yang dituju atau
// menghasilkan header yang tidak sah.
func TestValidateNewDeviceIDRejectsPathAndHeaderBreakers(t *testing.T) {
	for _, id := range []string{
		"cs/utama",
		"cs?utama",
		"cs#utama",
		"cs utama",
		"cs\nutama",
		"../etc",
		"-cs",
		".cs",
		"cs@utama",
		"cs%2Futama",
		"perangkàt",
	} {
		if err := ValidateNewDeviceID(context.Background(), id); err == nil {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

func TestValidateNewDeviceIDEnforcesLength(t *testing.T) {
	if err := ValidateNewDeviceID(context.Background(), "ab"); err == nil {
		t.Fatal("expected a 2-character device id to be rejected")
	}
	if err := ValidateNewDeviceID(context.Background(), strings.Repeat("a", deviceIDMaxLen)); err != nil {
		t.Fatalf("expected a %d-character device id to be accepted, got %v", deviceIDMaxLen, err)
	}
	if err := ValidateNewDeviceID(context.Background(), strings.Repeat("a", deviceIDMaxLen+1)); err == nil {
		t.Fatal("expected an over-long device id to be rejected")
	}
}
