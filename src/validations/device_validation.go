package validations

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
)

// ValidateDeviceID ensures a device id was provided before any logout/remove work.
// Device lifecycle routes (POST /devices/{device_id}/logout, DELETE /devices/{device_id})
// are addressed by path param and do not pass through the X-Device-Id header middleware,
// so the usecase validates the id here and operates on it via the device manager rather
// than resolving a WhatsApp client from context (which would fall back to the global
// client and break multi-device, and must work even when no live client is attached).
func ValidateDeviceID(_ context.Context, deviceID string) error {
	if strings.TrimSpace(deviceID) == "" {
		return pkgError.ValidationError("device_id is required")
	}
	return nil
}

// polaDeviceID membatasi id device yang BOLEH DIPILIH PEMANGGIL.
//
// Id sebuah device bukan sekadar kunci baris. Ia disisipkan ke path
// (`/devices/:device_id/...`), dikirim sebagai nilai header `X-Device-Id`, dan
// jadi primary key di belasan tabel. Karena itu batasannya bukan selera:
//
//   - `/`, `?`, dan `#` mengubah path menjadi rute yang sama sekali lain, dan
//     pada `DELETE` itu berarti menghapus benda yang tidak diminta siapa pun.
//   - Spasi dan karakter non-ASCII tidak sah sebagai nilai header HTTP.
//   - Diawali huruf/angka supaya id tidak bisa menyerupai flag atau path relatif.
//
// UUID yang dibuat backend sendiri (`fiberUtils.UUID()`) lolos pola ini, jadi
// aturannya tidak pernah bertabrakan dengan id yang sudah ada.
var polaDeviceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

const (
	deviceIDMinLen = 3
	deviceIDMaxLen = 64
)

// ValidateNewDeviceID memeriksa id pilihan sendiri pada pembuatan device.
//
// Id KOSONG itu sah dan berarti "buatkan otomatis" — backend membuat UUID-nya
// (`DeviceManager.CreateDevice`). Jadi ini bukan "id wajib diisi"; kebalikan
// dari `ValidateDeviceID`, yang dipakai rute yang device-nya sudah pasti ada.
func ValidateNewDeviceID(_ context.Context, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	if len(deviceID) < deviceIDMinLen || len(deviceID) > deviceIDMaxLen {
		return pkgError.ValidationError(fmt.Sprintf(
			"device_id harus %d-%d karakter", deviceIDMinLen, deviceIDMaxLen))
	}
	if !polaDeviceID.MatchString(deviceID) {
		return pkgError.ValidationError(
			"device_id hanya boleh memuat huruf, angka, titik, garis bawah, atau tanda hubung, dan harus diawali huruf atau angka")
	}
	return nil
}
