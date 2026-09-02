package tenancy

import "errors"

// ErrUsernameTaken dikembalikan saat menyimpan user dengan username yang sudah
// dipakai. Sentinel-nya ada supaya lapisan REST bisa membalas 400 dengan kode
// yang jelas, bukan menerjemahkan pesan error driver (yang berbeda antar
// driver SQLite di project ini).
var ErrUsernameTaken = errors.New("username sudah dipakai")

// ErrUserRequired dikembalikan saat operasi butuh user yang valid tapi
// menerima nil, id 0, atau username kosong.
var ErrUserRequired = errors.New("user tidak valid")

// ErrDeviceIDRequired dikembalikan saat operasi kepemilikan device menerima
// device id kosong.
var ErrDeviceIDRequired = errors.New("device id wajib diisi")
