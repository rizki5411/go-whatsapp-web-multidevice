package usecase

import (
	"fmt"
	"strings"
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/sirupsen/logrus"
)

// Kepemilikan device slot untuk mode multi-tenant.
//
// Lihat docs/multitenant/phase-04-device-ownership.md.

type serviceDeviceOwnership struct {
	repo domainTenancy.ITenancyRepository

	// ownerCache memetakan device id ke pemiliknya; nilai 0 berarti device itu
	// belum di-klaim.
	//
	// CanAccess dipanggil di setiap request device-scoped, jadi tanpa cache
	// setiap request menjadi satu query SQLite. Invalidasinya EKSPLISIT, bukan
	// TTL: dengan TTL, device yang baru dipindahkan masih bisa diakses pemilik
	// lama selama jendela itu, dan jendela stale pada kontrol akses tidak bisa
	// diterima.
	mu         sync.RWMutex
	ownerCache map[string]int64
}

func NewDeviceOwnershipService(repo domainTenancy.ITenancyRepository) domainTenancy.IDeviceOwnership {
	return &serviceDeviceOwnership{
		repo:       repo,
		ownerCache: make(map[string]int64),
	}
}

func (s *serviceDeviceOwnership) cachedOwner(deviceID string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	userID, ok := s.ownerCache[deviceID]
	return userID, ok
}

func (s *serviceDeviceOwnership) cacheOwner(deviceID string, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownerCache[deviceID] = userID
}

// invalidate membuang satu device dari cache. Dipanggil setiap kali
// kepemilikannya berubah, sehingga perubahan berlaku pada request berikutnya.
func (s *serviceDeviceOwnership) invalidate(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ownerCache, deviceID)
}

// ownerUserID mengembalikan pemilik device, 0 kalau belum di-klaim.
func (s *serviceDeviceOwnership) ownerUserID(deviceID string) (int64, error) {
	if cached, ok := s.cachedOwner(deviceID); ok {
		return cached, nil
	}

	owner, err := s.repo.GetDeviceOwner(deviceID)
	if err != nil {
		return 0, err
	}

	var userID int64
	if owner != nil {
		userID = owner.UserID
	}
	s.cacheOwner(deviceID, userID)
	return userID, nil
}

// CanAccess melaporkan apakah principal boleh mengakses device.
//
// Sengaja tidak mengembalikan error: pemanggilnya adalah guard yang harus
// memutuskan boleh/tidak, dan kegagalan membaca kepemilikan harus berarti
// "tidak boleh". Error-nya dicatat, bukan diteruskan, supaya tidak ada
// pemanggil yang bisa keliru menangani error sebagai izin.
func (s *serviceDeviceOwnership) CanAccess(p *domainTenancy.Principal, deviceID string) bool {
	if !config.MultiTenantEnabled {
		return true
	}
	// Gagal ke arah aman: tanpa identitas, tidak ada akses.
	if p == nil {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	if p.UserID == 0 {
		// Principal non-admin tanpa baris app_user tidak mungkin memiliki
		// device apa pun.
		return false
	}

	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return false
	}

	ownerID, err := s.ownerUserID(deviceID)
	if err != nil {
		logrus.WithError(err).Warnf("[MULTITENANT] gagal membaca pemilik device %s; akses ditolak", deviceID)
		return false
	}
	// ownerID 0 berarti device tak-ber-owner: hanya admin, dan admin sudah
	// ditangani di atas.
	return ownerID != 0 && ownerID == p.UserID
}

func (s *serviceDeviceOwnership) OwnedDeviceIDs(p *domainTenancy.Principal) ([]string, bool) {
	if !config.MultiTenantEnabled {
		return nil, true
	}
	if p == nil {
		return nil, false
	}
	if p.IsAdmin() {
		return nil, true
	}
	if p.UserID == 0 {
		return nil, false
	}

	ids, err := s.repo.ListDeviceIDsByOwner(p.UserID)
	if err != nil {
		logrus.WithError(err).Warnf("[MULTITENANT] gagal membaca daftar device milik user %d", p.UserID)
		return nil, false
	}
	return ids, false
}

func (s *serviceDeviceOwnership) Claim(p *domainTenancy.Principal, deviceID string) error {
	if !config.MultiTenantEnabled {
		return nil
	}
	if p == nil {
		return domainTenancy.ErrUserRequired
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return domainTenancy.ErrDeviceIDRequired
	}
	if p.UserID == 0 {
		// Termasuk principal break-glass. Device yang dibuatnya akan berstatus
		// tak-ber-owner dan hanya terlihat admin sampai pemiliknya ditetapkan.
		return domainTenancy.ErrBreakGlassCannotOwn
	}

	if err := s.repo.SetDeviceOwner(deviceID, p.UserID); err != nil {
		return err
	}
	s.invalidate(deviceID)
	return nil
}

func (s *serviceDeviceOwnership) Release(deviceID string) error {
	if !config.MultiTenantEnabled {
		return nil
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return domainTenancy.ErrDeviceIDRequired
	}

	if err := s.repo.DeleteDeviceOwner(deviceID); err != nil {
		return err
	}
	s.invalidate(deviceID)
	return nil
}

// Assign memindahkan device ke user lain.
//
// Memvalidasi user tujuannya di sini, bukan di repository: repository sengaja
// tetap dumb, dan aturan "user harus ada, aktif, dan belum penuh kuotanya"
// adalah aturan bisnis.
func (s *serviceDeviceOwnership) Assign(deviceID string, userID int64) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return domainTenancy.ErrDeviceIDRequired
	}
	if userID == 0 {
		return domainTenancy.ErrUserRequired
	}

	user, err := s.repo.GetUserByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return domainTenancy.ErrUserNotFound
	}
	if !user.Active {
		return fmt.Errorf("user %s sedang nonaktif", user.Username)
	}

	// Kuota diperiksa hanya kalau device ini memang berpindah pemilik;
	// menetapkan ulang pemilik yang sama tidak boleh tertolak karena kuota.
	currentOwner, err := s.ownerUserID(deviceID)
	if err != nil {
		return err
	}
	if currentOwner != userID {
		if err := s.ensureQuotaForUser(user); err != nil {
			return err
		}
	}

	if err := s.repo.SetDeviceOwner(deviceID, userID); err != nil {
		return err
	}
	s.invalidate(deviceID)
	return nil
}

func (s *serviceDeviceOwnership) Owner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, domainTenancy.ErrDeviceIDRequired
	}
	return s.repo.GetDeviceOwner(deviceID)
}

func (s *serviceDeviceOwnership) EnsureQuota(p *domainTenancy.Principal) error {
	if !config.MultiTenantEnabled {
		return nil
	}
	if p == nil {
		return domainTenancy.ErrUserRequired
	}
	// Admin tidak dibatasi kuota, dan principal break-glass memang tidak bisa
	// memiliki device sama sekali — Claim yang menolaknya, bukan kuota.
	if p.IsAdmin() || p.UserID == 0 {
		return nil
	}

	user, err := s.repo.GetUserByID(p.UserID)
	if err != nil {
		return err
	}
	if user == nil {
		return domainTenancy.ErrUserNotFound
	}
	return s.ensureQuotaForUser(user)
}

func (s *serviceDeviceOwnership) ensureQuotaForUser(user *domainTenancy.User) error {
	// 0 berarti tanpa batas.
	if user.DeviceLimit <= 0 {
		return nil
	}
	used, err := s.repo.CountDevicesByOwner(user.ID)
	if err != nil {
		return err
	}
	if used >= user.DeviceLimit {
		return fmt.Errorf("%w (%d dari %d terpakai)", domainTenancy.ErrDeviceLimitReached, used, user.DeviceLimit)
	}
	return nil
}
