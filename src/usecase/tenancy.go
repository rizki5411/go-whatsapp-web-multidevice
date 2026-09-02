package usecase

import (
	"context"
	"crypto/subtle"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/authhash"
	"github.com/sirupsen/logrus"
)

// Aturan bisnis akun aplikasi untuk mode multi-tenant.
//
// Semua validasi dan invarian tinggal di lapisan ini, bukan di handler REST,
// supaya jalur API dan jalur seeding admin tunduk pada aturan yang sama persis.
// Lihat docs/multitenant/phase-02-user-management.md.

// usernamePattern membatasi username ke huruf kecil, angka, titik, garis bawah,
// dan tanda hubung.
//
// Pembatasan charset ini bukan soal selera: username muncul di log dan di
// halaman operator, dan mengunci bentuknya menghilangkan seluruh kelas masalah
// encoding sekaligus.
var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)

type serviceTenancy struct {
	repo domainTenancy.ITenancyRepository

	// breakGlass adalah kredensial APP_BASIC_AUTH. Disimpan di service, bukan
	// dibaca dari config global di tiap pemanggilan, supaya perilakunya bisa
	// diuji tanpa mengubah state global.
	breakGlass []string

	// basicCache menghindari satu operasi bcrypt per request pada jalur HTTP
	// Basic; lihat tenancy_basiccache.go.
	basicCache *basicAuthCache
}

// NewTenancyService membangun service akun aplikasi.
//
// breakGlassCredentials adalah isi APP_BASIC_AUTH dalam bentuk "user:pass".
// Dipakai untuk dua hal yang harus konsisten: menyemai admin pertama, dan
// jalur break-glass di ResolveBasic. Keduanya membaca daftar yang sama persis.
func NewTenancyService(repo domainTenancy.ITenancyRepository, breakGlassCredentials []string) domainTenancy.ITenancyUsecase {
	return &serviceTenancy{
		repo:       repo,
		breakGlass: breakGlassCredentials,
		basicCache: newBasicAuthCache(),
	}
}

// validateUsername menormalisasi lalu memeriksa bentuk username.
func validateUsername(raw string) (string, error) {
	username := domainTenancy.NormalizeUsername(raw)
	if !usernamePattern.MatchString(username) {
		return "", domainTenancy.ErrUsernameInvalid
	}
	return username, nil
}

// validateRole mengembalikan role yang dipakai. Role kosong menjadi operator:
// user yang dibuat tanpa menyebut role tidak boleh diam-diam jadi admin.
func validateRole(role domainTenancy.Role) (domainTenancy.Role, error) {
	if strings.TrimSpace(string(role)) == "" {
		return domainTenancy.RoleOperator, nil
	}
	if !role.Valid() {
		return "", domainTenancy.ErrRoleInvalid
	}
	return role, nil
}

func (s *serviceTenancy) CreateUser(_ context.Context, in domainTenancy.CreateUserInput) (*domainTenancy.User, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}

	username, err := validateUsername(in.Username)
	if err != nil {
		return nil, err
	}
	role, err := validateRole(in.Role)
	if err != nil {
		return nil, err
	}
	if in.DeviceLimit < 0 {
		return nil, domainTenancy.ErrDeviceLimitInvalid
	}
	// Aturan panjang password milik authhash; jangan diduplikasi di sini,
	// supaya API dan seeding tidak bisa menyimpan password yang lebih lemah
	// dari aturan yang berlaku di tempat lain.
	hash, err := authhash.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	user := &domainTenancy.User{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		Role:         role,
		DeviceLimit:  in.DeviceLimit,
		Active:       true,
	}
	if _, err := s.repo.CreateUser(user); err != nil {
		return nil, err
	}
	return user, nil
}

// UpdateUser menerapkan perubahan yang diminta dan menjaga dua invarian:
// selalu ada minimal satu admin aktif, dan session dicabut begitu kredensial
// atau status aktif berubah.
func (s *serviceTenancy) UpdateUser(_ context.Context, id int64, in domainTenancy.UpdateUserInput) (*domainTenancy.User, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}
	if id == 0 {
		return nil, domainTenancy.ErrUserRequired
	}

	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, domainTenancy.ErrUserNotFound
	}

	// Dicatat sebelum perubahan diterapkan: keduanya menentukan apakah session
	// harus dicabut, dan apakah invarian admin terakhir tersentuh.
	passwordChanged := false
	wasActiveAdmin := user.IsAdmin() && user.Active

	if in.Password != nil {
		hash, err := authhash.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		user.PasswordHash = hash
		passwordChanged = true
	}
	if in.DisplayName != nil {
		user.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Role != nil {
		role, err := validateRole(*in.Role)
		if err != nil {
			return nil, err
		}
		user.Role = role
	}
	if in.DeviceLimit != nil {
		if *in.DeviceLimit < 0 {
			return nil, domainTenancy.ErrDeviceLimitInvalid
		}
		user.DeviceLimit = *in.DeviceLimit
	}
	deactivated := false
	if in.Active != nil {
		deactivated = user.Active && !*in.Active
		user.Active = *in.Active
	}

	// Kalau user ini tadinya admin aktif dan setelah perubahan bukan lagi —
	// entah karena role diturunkan atau dinonaktifkan — pastikan masih ada
	// admin aktif lain yang tersisa.
	if wasActiveAdmin && !(user.IsAdmin() && user.Active) {
		admins, err := s.repo.CountAdmins()
		if err != nil {
			return nil, err
		}
		if admins <= 1 {
			return nil, domainTenancy.ErrLastAdminProtected
		}
	}

	if err := s.repo.UpdateUser(user); err != nil {
		return nil, err
	}

	// Ganti password dan penonaktifan harus langsung berlaku. Tanpa pencabutan
	// ini, "nonaktifkan user" tidak berefek apa pun sampai cookie-nya
	// kedaluwarsa sendiri.
	if passwordChanged || deactivated {
		if err := s.repo.DeleteSessionsByUser(user.ID); err != nil {
			return nil, fmt.Errorf("user %d diperbarui tetapi session lamanya gagal dicabut: %w", user.ID, err)
		}
	}

	// Cache verifikasi Basic dikosongkan untuk SETIAP perubahan di atas, bukan
	// hanya password dan status aktif: role juga ikut tersimpan di principal
	// yang di-cache, jadi penurunan role harus langsung berlaku.
	s.basicCache.clear()

	return user, nil
}

func (s *serviceTenancy) DeleteUser(_ context.Context, id int64) error {
	if s.repo == nil {
		return fmt.Errorf("tenancy repository not initialized")
	}
	if id == 0 {
		return domainTenancy.ErrUserRequired
	}

	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return err
	}
	if user == nil {
		return domainTenancy.ErrUserNotFound
	}

	if user.IsAdmin() && user.Active {
		admins, err := s.repo.CountAdmins()
		if err != nil {
			return err
		}
		if admins <= 1 {
			return domainTenancy.ErrLastAdminProtected
		}
	}

	// Repository menghapus user, session, dan baris kepemilikannya dalam satu
	// transaksi. Device-nya sendiri tetap ada dan menjadi tak-ber-owner.
	if err := s.repo.DeleteUser(id); err != nil {
		return err
	}
	// Tanpa ini, user yang baru dihapus masih bisa masuk lewat Basic sampai
	// entri cache-nya kedaluwarsa sendiri.
	s.basicCache.clear()
	return nil
}

func (s *serviceTenancy) GetUser(_ context.Context, id int64) (*domainTenancy.User, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, domainTenancy.ErrUserNotFound
	}
	return user, nil
}

func (s *serviceTenancy) ListUsers(_ context.Context) ([]*domainTenancy.User, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}
	return s.repo.ListUsers()
}

// Authenticate memverifikasi kredensial terhadap app_user.
//
// Selalu mengembalikan (nil, nil) untuk kegagalan kredensial apa pun, dan
// selalu menjalankan tepat satu verifikasi bcrypt — termasuk saat username
// tidak ditemukan. Keduanya disengaja: pesan yang berbeda atau waktu respons
// yang berbeda sama-sama membocorkan username mana yang terdaftar.
func (s *serviceTenancy) Authenticate(_ context.Context, username, password string) (*domainTenancy.Principal, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}

	user, err := s.repo.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if user == nil {
		// Tetap bayar biaya bcrypt supaya username yang tidak ada tidak
		// menjawab lebih cepat daripada yang ada.
		authhash.VerifyPassword(authhash.DummyHash(), password)
		return nil, nil
	}
	if !authhash.VerifyPassword(user.PasswordHash, password) {
		return nil, nil
	}
	// Pemeriksaan aktif dilakukan SETELAH verifikasi password, supaya user yang
	// dinonaktifkan tidak menjawab lebih cepat daripada yang aktif.
	if !user.Active {
		return nil, nil
	}

	return &domainTenancy.Principal{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
	}, nil
}

// BootstrapAdminsFromEnv menyemai satu admin per kredensial APP_BASIC_AUTH yang
// username-nya belum ada di app_user.
//
// Idempoten dan tidak pernah menimpa: begitu sebuah username punya baris di
// app_user, password di database yang menang dan nilai env diabaikan untuk
// username itu. Kredensial env yang username-nya belum tercatat tetap berlaku
// sebagai break-glass di fase 03.
func (s *serviceTenancy) BootstrapAdminsFromEnv(ctx context.Context) (int, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("tenancy repository not initialized")
	}

	created := 0
	for _, credential := range s.breakGlass {
		username, password, ok := strings.Cut(credential, ":")
		if !ok {
			continue
		}

		normalized := domainTenancy.NormalizeUsername(username)
		if normalized == "" {
			continue
		}

		existing, err := s.repo.GetUserByUsername(normalized)
		if err != nil {
			return created, err
		}
		if existing != nil {
			continue
		}

		if _, err := s.CreateUser(ctx, domainTenancy.CreateUserInput{
			Username: normalized,
			Password: password,
			Role:     domainTenancy.RoleAdmin,
		}); err != nil {
			// Kegagalan seeding TIDAK boleh menggagalkan startup. Penyebab
			// paling mungkin adalah password env yang lebih pendek dari aturan
			// authhash, dan mematikan aplikasi karena itu akan mengubah upgrade
			// jadi outage. Username itu tetap bisa masuk lewat break-glass.
			logrus.WithError(err).Warnf(
				"[MULTITENANT] gagal menyemai admin %q dari APP_BASIC_AUTH; username ini tetap bisa masuk lewat break-glass",
				normalized,
			)
			continue
		}
		created++
	}

	return created, nil
}

// _____________________________________________________________________________
// Session login dan resolusi principal (fase 03)

func (s *serviceTenancy) Login(ctx context.Context, username, password, userAgent string) (string, *domainTenancy.Principal, error) {
	if s.repo == nil {
		return "", nil, fmt.Errorf("tenancy repository not initialized")
	}

	principal, err := s.Authenticate(ctx, username, password)
	if err != nil {
		return "", nil, err
	}
	if principal == nil {
		return "", nil, nil
	}

	token, tokenHash, err := authhash.NewSessionToken()
	if err != nil {
		return "", nil, err
	}

	// user_agent dipotong supaya header yang sengaja dibuat panjang tidak
	// membengkakkan baris session; kolomnya sendiri hanya VARCHAR(255).
	if len(userAgent) > 255 {
		userAgent = userAgent[:255]
	}

	if err := s.repo.CreateSession(&domainTenancy.Session{
		TokenHash: tokenHash,
		UserID:    principal.UserID,
		UserAgent: userAgent,
		ExpiresAt: time.Now().Add(config.MultiTenantSessionTTL),
	}); err != nil {
		return "", nil, err
	}

	return token, principal, nil
}

func (s *serviceTenancy) Logout(_ context.Context, token string) error {
	if s.repo == nil {
		return fmt.Errorf("tenancy repository not initialized")
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	// Idempoten: menghapus token yang tidak dikenal bukan kegagalan, dan logout
	// tidak boleh pernah menjawab error ke pengguna yang cookie-nya sudah basi.
	return s.repo.DeleteSession(authhash.HashToken(token))
}

// ResolveSession memvalidasi token cookie.
//
// Session kedaluwarsa langsung dihapus di sini, bukan hanya diabaikan, supaya
// tabelnya tidak menumpuk baris mati di antara dua siklus penyapu.
func (s *serviceTenancy) ResolveSession(_ context.Context, token string) (*domainTenancy.Principal, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}

	tokenHash := authhash.HashToken(token)
	session, err := s.repo.GetSession(tokenHash)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	if session.Expired(time.Now()) {
		if err := s.repo.DeleteSession(tokenHash); err != nil {
			logrus.WithError(err).Warn("[MULTITENANT] gagal menghapus session kedaluwarsa")
		}
		return nil, nil
	}

	user, err := s.repo.GetUserByID(session.UserID)
	if err != nil {
		return nil, err
	}
	// User yang sudah dihapus atau dinonaktifkan tidak boleh lolos meski
	// cookie-nya masih dalam masa berlaku.
	if user == nil || !user.Active {
		if err := s.repo.DeleteSession(tokenHash); err != nil {
			logrus.WithError(err).Warn("[MULTITENANT] gagal mencabut session milik user nonaktif")
		}
		return nil, nil
	}

	return &domainTenancy.Principal{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
	}, nil
}

// ResolveBasic memvalidasi kredensial HTTP Basic.
//
// Urutannya penting: app_user diperiksa lebih dulu, dan break-glass
// APP_BASIC_AUTH hanya berlaku untuk username yang BELUM punya baris di
// app_user. Begitu sebuah username tercatat, password database yang menang —
// itu yang mencegah nilai env menjadi backdoor permanen setelah admin
// mengganti passwordnya.
func (s *serviceTenancy) ResolveBasic(_ context.Context, username, password string) (*domainTenancy.Principal, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}

	normalized := domainTenancy.NormalizeUsername(username)
	if normalized == "" {
		return nil, nil
	}

	if cached, ok := s.basicCache.get(normalized, password); ok {
		return cached, nil
	}

	user, err := s.repo.GetUserByUsername(normalized)
	if err != nil {
		return nil, err
	}

	if user != nil {
		if !authhash.VerifyPassword(user.PasswordHash, password) {
			return nil, nil
		}
		if !user.Active {
			return nil, nil
		}
		principal := &domainTenancy.Principal{
			UserID:   user.ID,
			Username: user.Username,
			Role:     user.Role,
		}
		s.basicCache.put(normalized, password, principal)
		return principal, nil
	}

	// Username belum ada di app_user: jalur break-glass.
	principal := s.matchBreakGlass(normalized, password)
	if principal == nil {
		return nil, nil
	}
	s.basicCache.put(normalized, password, principal)
	return principal, nil
}

// ResolvePrincipalByUsername menyusun principal tanpa memverifikasi password.
//
// Untuk pemanggil yang identitasnya sudah diverifikasi jalur lain — fase 07
// memakainya untuk subject token OAuth MCP. Aturan break-glass-nya sama dengan
// ResolveBasic dan sengaja memakai daftar yang sama, supaya tidak ada dua
// definisi break-glass yang bisa menyimpang.
func (s *serviceTenancy) ResolvePrincipalByUsername(_ context.Context, username string) (*domainTenancy.Principal, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("tenancy repository not initialized")
	}

	normalized := domainTenancy.NormalizeUsername(username)
	if normalized == "" {
		return nil, nil
	}

	user, err := s.repo.GetUserByUsername(normalized)
	if err != nil {
		return nil, err
	}
	if user != nil {
		// Token bisa terbit sebelum user dinonaktifkan; token yang masih valid
		// secara kriptografis tidak boleh mengalahkan status akun.
		if !user.Active {
			return nil, nil
		}
		return &domainTenancy.Principal{
			UserID:   user.ID,
			Username: user.Username,
			Role:     user.Role,
		}, nil
	}

	// Tanpa baris app_user, satu-satunya identitas yang sah adalah username
	// yang memang tercantum di APP_BASIC_AUTH.
	for _, credential := range s.breakGlass {
		credUser, _, ok := strings.Cut(credential, ":")
		if !ok {
			continue
		}
		if domainTenancy.NormalizeUsername(credUser) == normalized {
			return &domainTenancy.Principal{
				Username:      normalized,
				Role:          domainTenancy.RoleAdmin,
				ViaBreakGlass: true,
			}, nil
		}
	}
	return nil, nil
}

// matchBreakGlass mencocokkan kredensial ke APP_BASIC_AUTH.
//
// Perbandingan password memakai subtle.ConstantTimeCompare, sama seperti
// newBasicAuthMiddleware di cmd/rest.go, supaya jalur ini tidak lebih lemah
// daripada jalur yang digantikannya.
//
// Principal hasilnya ber-UserID 0 dan karenanya tidak memiliki device apa pun.
// Karena rolenya admin ia tetap bisa melihat semua device dan menetapkan
// pemilik, yang cukup untuk memulihkan keadaan.
func (s *serviceTenancy) matchBreakGlass(username, password string) *domainTenancy.Principal {
	matched := false
	for _, credential := range s.breakGlass {
		credUser, credPass, ok := strings.Cut(credential, ":")
		if !ok {
			continue
		}
		if domainTenancy.NormalizeUsername(credUser) != username {
			continue
		}
		// Tidak break di sini: perbandingan tetap dijalankan untuk setiap
		// kredensial yang cocok username-nya, supaya waktu eksekusinya tidak
		// bergantung pada posisi entri di daftar.
		if subtle.ConstantTimeCompare([]byte(credPass), []byte(password)) == 1 {
			matched = true
		}
	}
	if !matched {
		return nil
	}
	return &domainTenancy.Principal{
		Username:      username,
		Role:          domainTenancy.RoleAdmin,
		ViaBreakGlass: true,
	}
}

func (s *serviceTenancy) SweepExpiredSessions(_ context.Context) (int64, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("tenancy repository not initialized")
	}
	return s.repo.DeleteExpiredSessions(time.Now())
}

// ChangeOwnPassword mengganti password user sendiri.
//
// Memverifikasi password lama meski pemanggil sudah terautentikasi: tanpa itu,
// cookie yang dicuri cukup untuk mengunci pemilik akun keluar dari akunnya
// sendiri.
func (s *serviceTenancy) ChangeOwnPassword(_ context.Context, userID int64, currentPassword, newPassword string) error {
	if s.repo == nil {
		return fmt.Errorf("tenancy repository not initialized")
	}
	if userID == 0 {
		// Principal break-glass tidak punya baris app_user, jadi tidak ada
		// password yang bisa diganti.
		return domainTenancy.ErrBreakGlassCannotChangePassword
	}

	user, err := s.repo.GetUserByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return domainTenancy.ErrUserNotFound
	}
	if !authhash.VerifyPassword(user.PasswordHash, currentPassword) {
		return domainTenancy.ErrCurrentPasswordWrong
	}

	hash, err := authhash.HashPassword(newPassword)
	if err != nil {
		return err
	}
	user.PasswordHash = hash

	if err := s.repo.UpdateUser(user); err != nil {
		return err
	}

	// Cabut semua session lalu kosongkan cache Basic. Pemanggil (handler REST)
	// menerbitkan session baru untuk request yang sedang berjalan, sehingga
	// pengguna tidak tertendang dari perangkat yang sedang dipakainya.
	if err := s.repo.DeleteSessionsByUser(user.ID); err != nil {
		return fmt.Errorf("password diganti tetapi session lama gagal dicabut: %w", err)
	}
	s.basicCache.clear()
	return nil
}
