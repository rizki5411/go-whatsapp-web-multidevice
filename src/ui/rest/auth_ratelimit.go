package rest

import (
	"sync"
	"time"
)

// Pembatas percobaan login.
//
// Tanpa ini, POST /auth/login menjadi oracle brute-force yang justru lebih
// nyaman dipakai daripada HTTP Basic: satu permintaan JSON per percobaan, tanpa
// prompt browser. Dua kunci dipakai bersama karena masing-masing menutup celah
// yang lain: per-username menahan penebakan password satu akun dari banyak IP,
// per-IP menahan penyisiran banyak username dari satu tempat.
//
// Disimpan di memori dan tidak dibagi antar proses. Untuk deployment satu
// instance — yang menjadi asumsi fitur ini — itu memadai; map + mutex jauh
// lebih mudah dibaca dan diaudit daripada menambah dependensi rate limiter.
const (
	loginRateWindow       = 5 * time.Minute
	loginMaxPerUsername   = 10
	loginMaxPerIP         = 20
	loginRateMaxTrackKeys = 4096
)

type loginAttempts struct {
	count       int
	windowStart time.Time
}

type loginRateLimiter struct {
	mu       sync.Mutex
	perKey   map[string]*loginAttempts
	window   time.Duration
	maxKeys  int
	maxUser  int
	maxPerIP int
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		perKey:   make(map[string]*loginAttempts),
		window:   loginRateWindow,
		maxKeys:  loginRateMaxTrackKeys,
		maxUser:  loginMaxPerUsername,
		maxPerIP: loginMaxPerIP,
	}
}

// allow melaporkan apakah percobaan login boleh diproses.
//
// Hanya membaca; penghitungnya baru naik lewat recordFailure. Login yang
// berhasil sengaja tidak dihitung, supaya pemakaian normal — beberapa orang
// login dari satu kantor ber-IP sama — tidak pernah terkena batas.
func (l *loginRateLimiter) allow(username, ip string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if !l.underLimitLocked("u:"+username, l.maxUser, now) {
		return false
	}
	return l.underLimitLocked("ip:"+ip, l.maxPerIP, now)
}

func (l *loginRateLimiter) underLimitLocked(key string, max int, now time.Time) bool {
	entry, ok := l.perKey[key]
	if !ok {
		return true
	}
	if now.Sub(entry.windowStart) >= l.window {
		delete(l.perKey, key)
		return true
	}
	return entry.count < max
}

// recordFailure menaikkan penghitung untuk username dan IP.
func (l *loginRateLimiter) recordFailure(username, ip string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	// Batas jumlah kunci mencegah map tumbuh tanpa batas kalau ada yang
	// menembak username acak dalam jumlah besar. Mengosongkan seluruhnya lebih
	// sederhana daripada mengusir per entri, dan efek sampingnya hanya
	// memberi penyerang satu jendela bersih — bukan akses.
	if len(l.perKey) >= l.maxKeys {
		l.perKey = make(map[string]*loginAttempts)
	}

	for _, key := range []string{"u:" + username, "ip:" + ip} {
		entry, ok := l.perKey[key]
		if !ok || now.Sub(entry.windowStart) >= l.window {
			l.perKey[key] = &loginAttempts{count: 1, windowStart: now}
			continue
		}
		entry.count++
	}
}

// reset dipakai test untuk mengosongkan state antar kasus.
func (l *loginRateLimiter) reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perKey = make(map[string]*loginAttempts)
}
