package tenancy

import (
	"context"
	"time"
)

// CreateUserInput adalah payload pembuatan user.
//
// Dibungkus struct, bukan daftar parameter, supaya penambahan field nanti tidak
// mengubah signature method dan tidak memaksa semua pemanggil ikut berubah.
type CreateUserInput struct {
	Username    string
	Password    string
	DisplayName string
	// Role kosong berarti RoleOperator: default yang aman, supaya user yang
	// dibuat tanpa menyebut role tidak diam-diam jadi admin.
	Role Role
	// DeviceLimit 0 berarti tanpa batas.
	DeviceLimit int
}

// UpdateUserInput memakai pointer untuk setiap field: nil berarti "jangan
// ubah".
//
// Tanpa pointer, PATCH yang hanya mengganti nama tampilan akan mengirim zero
// value untuk sisanya dan diam-diam mereset role ke "" serta mengaktifkan
// ulang user yang sengaja dinonaktifkan.
type UpdateUserInput struct {
	Password    *string
	DisplayName *string
	Role        *Role
	DeviceLimit *int
	Active      *bool
}

// ITenancyUsecase memegang aturan bisnis akun aplikasi: validasi bentuk,
// invarian admin terakhir, dan pencabutan session saat kredensial berubah.
//
// Semua aturan itu tinggal di sini, bukan di handler REST, supaya jalur API dan
// jalur seeding admin tunduk pada aturan yang sama persis.
type ITenancyUsecase interface {
	CreateUser(ctx context.Context, in CreateUserInput) (*User, error)
	UpdateUser(ctx context.Context, id int64, in UpdateUserInput) (*User, error)
	DeleteUser(ctx context.Context, id int64) error
	GetUser(ctx context.Context, id int64) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)

	// Authenticate memverifikasi username dan password terhadap app_user.
	//
	// Mengembalikan (nil, nil) untuk SEMUA kegagalan kredensial — user tidak
	// ada, password salah, atau user nonaktif. Pemanggil tidak boleh bisa
	// membedakan ketiganya, karena perbedaan itu sendiri membocorkan username
	// mana yang terdaftar.
	Authenticate(ctx context.Context, username, password string) (*Principal, error)

	// BootstrapAdminsFromEnv menyemai satu admin per kredensial APP_BASIC_AUTH
	// yang username-nya belum ada di app_user, dan mengembalikan jumlah yang
	// dibuat. Idempoten: username yang sudah ada dilewati, sehingga password di
	// database selalu menang atas nilai env.
	BootstrapAdminsFromEnv(ctx context.Context, credentials []string) (created int, err error)
}

// ITenancyRepository adalah kontrak persistensi untuk akun aplikasi,
// kepemilikan device, dan session login.
//
// Sengaja dipisah dari chatstorage.IChatStorageRepository: menambahkan
// method-method ini ke sana akan memaksa satu set stub delegasi masuk ke
// infrastructure/whatsapp/chatstorage_wrapper.go tanpa manfaat apa pun, dan
// memperbesar konflik saat sync upstream. Preseden yang sama dipakai
// messagequeue.IMessageQueueRepository. *chatstorage.SQLiteRepository
// memenuhi interface ini secara implisit.
//
// Konvensi baca: method Get* mengembalikan (nil, nil) kalau baris tidak ada —
// bukan error. "Belum ada" adalah keadaan normal di sini (device belum
// di-klaim, session sudah dihapus), dan menjadikannya error memaksa setiap
// pemanggil membedakan sql.ErrNoRows dari kegagalan sungguhan.
type ITenancyRepository interface {
	// --- Akun aplikasi ---

	// CreateUser menyisipkan user baru dan mengembalikan id-nya. Username
	// dinormalisasi lewat NormalizeUsername sebelum disimpan. Mengembalikan
	// ErrUsernameTaken kalau username sudah dipakai.
	CreateUser(user *User) (int64, error)
	// UpdateUser menimpa field yang bisa diubah pada satu baris user.
	// Username ikut dinormalisasi; created_at tidak pernah diubah.
	UpdateUser(user *User) error
	GetUserByID(id int64) (*User, error)
	// GetUserByUsername menormalisasi argumennya, jadi pencarian tidak
	// sensitif kapitalisasi.
	GetUserByUsername(username string) (*User, error)
	// ListUsers mengembalikan semua user, terurut username.
	ListUsers() ([]*User, error)
	// DeleteUser menghapus user beserta session dan baris kepemilikan
	// device-nya dalam satu transaksi.
	//
	// Device itu sendiri TIDAK dihapus: menghapus device berarti mem-purge
	// sesi WhatsApp-nya, dan itu harus tetap keputusan eksplisit admin.
	// Device yang pemiliknya hilang menjadi tak-ber-owner dan hanya terlihat
	// oleh admin.
	DeleteUser(id int64) error
	CountUsers() (int, error)
	// CountAdmins hanya menghitung admin yang aktif. Ini angka yang dipakai
	// untuk menjaga invarian "selalu ada minimal satu admin aktif".
	CountAdmins() (int, error)

	// --- Kepemilikan device ---

	// SetDeviceOwner melakukan upsert: device yang sudah punya owner akan
	// berpindah ke userID.
	SetDeviceOwner(deviceID string, userID int64) error
	GetDeviceOwner(deviceID string) (*DeviceOwner, error)
	// ListDeviceIDsByOwner mengembalikan device milik satu user, terurut
	// created_at lalu device_id. Urutan stabil ini yang dipakai untuk memilih
	// device default seorang operator.
	ListDeviceIDsByOwner(userID int64) ([]string, error)
	CountDevicesByOwner(userID int64) (int, error)
	DeleteDeviceOwner(deviceID string) error
	DeleteDeviceOwnersByUser(userID int64) error

	// --- Session login ---

	// CreateSession menyimpan satu session. session.TokenHash harus sudah
	// berisi hash, bukan token mentah.
	CreateSession(session *Session) error
	// GetSession tidak memfilter kedaluwarsa; pakai Session.Expired.
	GetSession(tokenHash string) (*Session, error)
	DeleteSession(tokenHash string) error
	DeleteSessionsByUser(userID int64) error
	// DeleteExpiredSessions menghapus session yang expires_at-nya sudah lewat
	// dan mengembalikan jumlah baris yang terhapus.
	DeleteExpiredSessions(now time.Time) (int64, error)
}
