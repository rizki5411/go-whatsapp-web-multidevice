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
	//
	// Daftar kredensialnya diambil dari yang diberikan ke konstruktor, bukan
	// dari parameter, supaya jalur seeding dan jalur break-glass di ResolveBasic
	// tidak pernah bekerja atas dua daftar yang berbeda.
	BootstrapAdminsFromEnv(ctx context.Context) (created int, err error)

	// Login memverifikasi kredensial lalu menerbitkan session.
	//
	// Mengembalikan token mentah untuk dikirim sebagai cookie; yang disimpan
	// hanya hash-nya. Kredensial yang salah menghasilkan ("", nil, nil), sama
	// seperti Authenticate.
	Login(ctx context.Context, username, password, userAgent string) (token string, principal *Principal, err error)

	// Logout mencabut satu session berdasarkan token mentahnya. Idempoten:
	// token yang tidak dikenal bukan error.
	Logout(ctx context.Context, token string) error

	// ResolveSession memvalidasi token cookie: hash, cari, cek kedaluwarsa, dan
	// pastikan user-nya masih ada dan aktif. Session yang kedaluwarsa langsung
	// dihapus. Mengembalikan (nil, nil) untuk token yang tidak berlaku.
	ResolveSession(ctx context.Context, token string) (*Principal, error)

	// ResolveBasic memvalidasi kredensial HTTP Basic.
	//
	// Memeriksa app_user lebih dulu; hanya kalau username itu BELUM ada di
	// app_user, kredensial APP_BASIC_AUTH dipakai sebagai break-glass dan
	// menghasilkan principal admin bertanda ViaBreakGlass. Begitu username
	// tercatat di app_user, password database yang menang — itu yang mencegah
	// env menjadi backdoor permanen.
	ResolveBasic(ctx context.Context, username, password string) (*Principal, error)

	// ResolvePrincipalByUsername menyusun principal tanpa memverifikasi
	// password, untuk pemanggil yang identitasnya sudah diverifikasi jalur lain
	// (fase 07: subject token OAuth MCP). Aturan break-glass-nya sama dengan
	// ResolveBasic, dan keduanya memakai implementasi yang sama.
	ResolvePrincipalByUsername(ctx context.Context, username string) (*Principal, error)

	// SweepExpiredSessions menghapus session yang sudah kedaluwarsa.
	SweepExpiredSessions(ctx context.Context) (int64, error)
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

// IDeviceOwnership adalah satu-satunya tempat keputusan "boleh atau tidak"
// soal device diambil.
//
// Middleware, handler REST, dan (nanti) MCP semuanya memanggil interface ini,
// supaya tidak pernah ada dua definisi kepemilikan yang bisa berbeda.
type IDeviceOwnership interface {
	// CanAccess melaporkan apakah principal boleh mengakses device itu.
	//
	// Admin selalu boleh. Device tanpa baris pemilik hanya bisa diakses admin:
	// default yang ketat, supaya device yang gagal ter-klaim — karena bug, race,
	// atau migrasi setengah jalan — tidak otomatis terbuka untuk semua orang.
	// Principal nil selalu ditolak.
	CanAccess(p *Principal, deviceID string) bool

	// OwnedDeviceIDs mengembalikan device milik principal.
	//
	// all bernilai true untuk admin dan saat mode multi-tenant mati, artinya
	// "tidak perlu difilter"; ids-nya diabaikan dalam kasus itu.
	OwnedDeviceIDs(p *Principal) (ids []string, all bool)

	// Claim mencatat principal sebagai pemilik device yang baru dibuat.
	// Menolak principal break-glass, yang ber-UserID 0 dan tidak memiliki
	// device apa pun.
	Claim(p *Principal, deviceID string) error

	// Release melepas kepemilikan; device menjadi tak-ber-owner.
	Release(deviceID string) error

	// Assign memindahkan device ke user lain. Hanya dipakai admin.
	Assign(deviceID string, userID int64) error

	// Owner mengembalikan pemilik device, atau nil kalau belum di-klaim.
	Owner(deviceID string) (*DeviceOwner, error)

	// EnsureQuota mengembalikan error kalau principal sudah mencapai
	// device_limit-nya. Limit 0 berarti tanpa batas.
	EnsureQuota(p *Principal) error
}
