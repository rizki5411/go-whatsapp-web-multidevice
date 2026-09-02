package usecase

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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
}

func NewTenancyService(repo domainTenancy.ITenancyRepository) domainTenancy.ITenancyUsecase {
	return &serviceTenancy{repo: repo}
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
	return s.repo.DeleteUser(id)
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
func (s *serviceTenancy) BootstrapAdminsFromEnv(ctx context.Context, credentials []string) (int, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("tenancy repository not initialized")
	}

	created := 0
	for _, credential := range credentials {
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
