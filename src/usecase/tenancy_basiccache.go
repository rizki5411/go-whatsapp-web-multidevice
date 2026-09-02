package usecase

import (
	"sync"
	"time"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/authhash"
)

// Cache hasil verifikasi HTTP Basic.
//
// Ini BUKAN optimasi opsional. Basic Auth memverifikasi ulang di setiap
// request, dan bcrypt dengan cost 10 butuh puluhan milidetik. Tanpa cache, satu
// klien yang mem-polling /app/status tiap detik akan menghabiskan CPU di bcrypt,
// dan dashboard yang menembak beberapa endpoint sekaligus jadi terasa berat.
//
// Kunci cache memuat hash password, jadi password yang salah tidak pernah bisa
// menabrak entri milik password yang benar.
const (
	basicCacheTTL     = 60 * time.Second
	basicCacheMaxSize = 256
)

type basicCacheEntry struct {
	principal domainTenancy.Principal
	expiresAt time.Time
}

type basicAuthCache struct {
	mu      sync.Mutex
	entries map[string]basicCacheEntry
	ttl     time.Duration
	max     int
}

func newBasicAuthCache() *basicAuthCache {
	return &basicAuthCache{
		entries: make(map[string]basicCacheEntry),
		ttl:     basicCacheTTL,
		max:     basicCacheMaxSize,
	}
}

// cacheKey mengikat username DAN password ke satu kunci. Password ikut sebagai
// hash, bukan teks, supaya isi map tidak menjadi salinan kredensial.
func basicCacheKey(username, password string) string {
	return username + ":" + authhash.HashToken(password)
}

func (c *basicAuthCache) get(username, password string) (*domainTenancy.Principal, bool) {
	if c == nil {
		return nil, false
	}
	key := basicCacheKey(username, password)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}

	principal := entry.principal
	return &principal, true
}

func (c *basicAuthCache) put(username, password string, principal *domainTenancy.Principal) {
	if c == nil || principal == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Batas ukuran mencegah cache tumbuh tanpa batas kalau ada yang menembak
	// banyak username berbeda. Pengosongan total lebih sederhana daripada LRU,
	// dan biayanya hanya beberapa verifikasi bcrypt tambahan.
	if len(c.entries) >= c.max {
		c.entries = make(map[string]basicCacheEntry)
	}

	c.entries[basicCacheKey(username, password)] = basicCacheEntry{
		principal: *principal,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// clear mengosongkan seluruh cache.
//
// Dipanggil setiap kali password, role, atau status aktif seorang user berubah,
// dan saat user dihapus. Sengaja mengosongkan SEMUANYA, bukan mencari entri
// milik user itu: kuncinya memuat hash password sehingga entri milik satu user
// tidak bisa ditemukan dari username saja, dan entri yang terlewat berarti user
// yang dinonaktifkan masih bisa masuk sampai satu menit — tepat jenis bug yang
// tidak akan terlihat saat pengujian manual.
func (c *basicAuthCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]basicCacheEntry)
}
