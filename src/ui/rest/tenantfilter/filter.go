// Package tenantfilter menyaring hasil endpoint agar hanya memuat device milik
// pemanggil.
//
// Penyaringan dilakukan di lapisan handler, bukan usecase, karena principal
// tinggal di fiber.Ctx sementara usecase hanya menerima context.Context dan
// PassLocalsToContext tidak aktif di aplikasi ini. Menyalakan flag Fiber itu
// hanya demi ini akan mengubah perilaku seluruh aplikasi — perubahan yang jauh
// lebih luas daripada yang dibutuhkan.
//
// Lihat docs/multitenant/phase-04-device-ownership.md.
package tenantfilter

import (
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
)

// Devices menyaring daftar apa pun berdasarkan kepemilikan device.
//
// Generik dengan fungsi pengekstrak id supaya satu implementasi melayani
// []device.Device maupun []app.DevicesResponse — dua bentuk berbeda yang
// dikembalikan GET /devices dan GET /app/devices.
//
// Slice input tidak pernah dimutasi: pemanggil mungkin masih memakainya.
// Daftar kosong dikembalikan sebagai nil kalau inputnya nil, supaya bentuk
// response tidak berubah untuk registry yang memang kosong.
func Devices[T any](
	principal *domainTenancy.Principal,
	ownership domainTenancy.IDeviceOwnership,
	items []T,
	id func(T) string,
) []T {
	if ownership == nil || items == nil {
		return items
	}

	allowed, all := ownership.OwnedDeviceIDs(principal)
	if all {
		return items
	}

	permitted := make(map[string]struct{}, len(allowed))
	for _, deviceID := range allowed {
		permitted[deviceID] = struct{}{}
	}

	filtered := make([]T, 0, len(items))
	for _, item := range items {
		if _, ok := permitted[id(item)]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// ByDeviceID menyaring daftar config lintas device menjadi milik pemanggil.
//
// Dipakai endpoint agregat seperti GET /command/configs dan
// GET /chatwoot/configs. Sama seperti Devices, admin melewati filter.
func ByDeviceID[T any](
	principal *domainTenancy.Principal,
	ownership domainTenancy.IDeviceOwnership,
	items []T,
	deviceID func(T) string,
) []T {
	return Devices(principal, ownership, items, deviceID)
}
