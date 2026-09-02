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

// ErrUsernameInvalid dikembalikan saat username tidak memenuhi aturan bentuk
// (panjang 3-64, hanya huruf kecil, angka, titik, garis bawah, dan tanda
// hubung).
var ErrUsernameInvalid = errors.New("username harus 3-64 karakter dan hanya boleh memuat a-z, 0-9, titik, garis bawah, atau tanda hubung")

// ErrRoleInvalid dikembalikan saat role di luar admin/operator.
var ErrRoleInvalid = errors.New("role harus \"admin\" atau \"operator\"")

// ErrDeviceLimitInvalid dikembalikan saat device_limit negatif. 0 berarti tanpa
// batas, jadi tidak ada arti untuk nilai di bawahnya.
var ErrDeviceLimitInvalid = errors.New("device_limit tidak boleh negatif")

// ErrUserNotFound dikembalikan operasi usecase yang menargetkan user tertentu
// tapi tidak menemukannya.
//
// Berbeda dari repository, yang mengembalikan (nil, nil) untuk baris yang tidak
// ada: di lapisan usecase, "user yang kamu minta untuk diubah tidak ada" adalah
// kegagalan operasi, bukan keadaan normal.
var ErrUserNotFound = errors.New("user tidak ditemukan")

// ErrLastAdminProtected dikembalikan saat sebuah operasi akan membuat jumlah
// admin aktif menjadi nol — lewat penghapusan, penurunan role, atau
// penonaktifan.
//
// Tanpa penjaga ini, satu klik bisa membuat instance tidak punya siapa pun yang
// bisa mengelola user lagi, dan pemulihannya hanya lewat break-glass
// APP_BASIC_AUTH atau edit database manual.
var ErrLastAdminProtected = errors.New("tidak bisa dilakukan: instance harus punya minimal satu admin aktif")

// ErrDeviceLimitReached dikembalikan saat user sudah mencapai device_limit-nya.
var ErrDeviceLimitReached = errors.New("jumlah device sudah mencapai batas untuk user ini")

// ErrBreakGlassCannotOwn dikembalikan saat principal break-glass mencoba
// memiliki device.
//
// Principal itu berasal dari APP_BASIC_AUTH dan tidak punya baris app_user,
// jadi tidak ada user_id yang sah untuk dicatat sebagai pemilik. Menyimpannya
// dengan user_id 0 akan membuat device dimiliki identitas yang tidak ada.
var ErrBreakGlassCannotOwn = errors.New("kredensial APP_BASIC_AUTH tidak bisa memiliki device; buat akun di /admin/users lalu tetapkan pemiliknya")
