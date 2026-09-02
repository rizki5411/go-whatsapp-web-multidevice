package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/usecase"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// tenancyUsecase diisi initMultiTenant dan tetap nil saat fitur mati, sehingga
// pemanggil bisa memakainya sebagai penanda "mode multi-tenant aktif dan siap".
var tenancyUsecase domainTenancy.ITenancyUsecase

// authHandler dibangun di restServer sebelum gate dipasang, karena rute publik
// /auth/login harus didaftarkan di atas gate sementara /auth/me di bawahnya.
// Keduanya harus memakai handler yang sama supaya pembatas percobaan login
// tidak terpecah menjadi dua penghitung.
var authHandler *rest.AuthHandler

// deviceOwnership nil saat fitur mati. Handler dan middleware yang
// menerimanya menjaga nil itu, sehingga perilaku single-tenant tidak berubah.
var deviceOwnership domainTenancy.IDeviceOwnership

// Wiring konfigurasi untuk mode multi-tenant (isolasi device per user).
// Ditaruh di file sendiri, bukan di root.go, supaya sync upstream tetap bebas
// konflik — lihat aturan fork di CLAUDE.md. Rencana lengkap per fase ada di
// docs/multitenant/.
//
// Pola pemuatan mengikuti mcp_oauth.go dan bukan initEnvConfig: initEnvConfig
// berjalan lewat cobra.OnInitialize, yaitu SETELAH Cobra menulis nilai flag ke
// config, sehingga env di sana akan menimpa flag. Dengan flag.Changed di bawah,
// prioritas flag > env > .env tetap terjaga.

func init() {
	rootCmd.PersistentFlags().BoolVar(
		&config.MultiTenantEnabled,
		"multi-tenant-enabled",
		config.MultiTenantEnabled,
		"enable per-user data isolation (device ownership + user management)",
	)
	rootCmd.PersistentFlags().DurationVar(
		&config.MultiTenantSessionTTL,
		"multi-tenant-session-ttl",
		config.MultiTenantSessionTTL,
		`login session lifetime --multi-tenant-session-ttl <duration> | example: --multi-tenant-session-ttl=12h`,
	)
	rootCmd.PersistentFlags().StringVar(
		&config.MultiTenantSessionCookie,
		"multi-tenant-session-cookie",
		config.MultiTenantSessionCookie,
		`session cookie name --multi-tenant-session-cookie <string> | example: --multi-tenant-session-cookie="gowa_session"`,
	)
	rootCmd.PersistentFlags().BoolVar(
		&config.MultiTenantSecureCookie,
		"multi-tenant-secure-cookie",
		config.MultiTenantSecureCookie,
		"set the Secure attribute on the session cookie; required behind HTTPS",
	)
}

// loadMultiTenantEnvConfig dipanggil dari restServer setelah Cobra selesai
// mem-parse flag. Setiap key hanya dibaca dari env bila flag-nya tidak di-set,
// supaya flag tetap menang.
func loadMultiTenantEnvConfig() {
	flags := rootCmd.PersistentFlags()

	if flag := flags.Lookup("multi-tenant-enabled"); flag == nil || !flag.Changed {
		if viper.IsSet("multi_tenant_enabled") {
			config.MultiTenantEnabled = viper.GetBool("multi_tenant_enabled")
		}
	}
	if flag := flags.Lookup("multi-tenant-session-ttl"); flag == nil || !flag.Changed {
		// Guard > 0: GetDuration mengembalikan 0 untuk key yang tidak ada, dan
		// 0 akan menghapus default 12 jam.
		if value := viper.GetDuration("multi_tenant_session_ttl"); value > 0 {
			config.MultiTenantSessionTTL = value
		}
	}
	if flag := flags.Lookup("multi-tenant-session-cookie"); flag == nil || !flag.Changed {
		if value := strings.TrimSpace(viper.GetString("multi_tenant_session_cookie")); value != "" {
			config.MultiTenantSessionCookie = value
		}
	}
	if flag := flags.Lookup("multi-tenant-secure-cookie"); flag == nil || !flag.Changed {
		if viper.IsSet("multi_tenant_secure_cookie") {
			config.MultiTenantSecureCookie = viper.GetBool("multi_tenant_secure_cookie")
		}
	}
}

// initMultiTenant menyiapkan usecase tenancy dan menyemai admin pertama.
//
// Dipanggil dari restServer, bukan dari initApp, karena urutannya penting:
// initApp berjalan lewat cobra.OnInitialize, yaitu SEBELUM
// loadMultiTenantEnvConfig, sehingga config.MultiTenantEnabled di sana belum
// mencerminkan nilai env. chatStorageDB sendiri sudah siap saat restServer
// mulai, jadi wiring di sini aman dan root.go tidak perlu disentuh.
//
// No-op saat fitur mati.
func initMultiTenant() {
	if !config.MultiTenantEnabled {
		return
	}
	if chatStorageDB == nil {
		logrus.Error("[MULTITENANT] chat storage belum siap; mode multi-tenant tidak diaktifkan")
		return
	}

	tenancyRepo := chatstorage.NewTenancyRepository(chatStorageDB)
	tenancyUsecase = usecase.NewTenancyService(tenancyRepo, config.AppBasicAuthCredential)
	deviceOwnership = usecase.NewDeviceOwnershipService(tenancyRepo)

	created, err := tenancyUsecase.BootstrapAdminsFromEnv(context.Background())
	if err != nil {
		// Seeding yang gagal tidak boleh menggagalkan startup: kredensial
		// APP_BASIC_AUTH tetap berlaku sebagai break-glass selama username-nya
		// belum tercatat di app_user, jadi operator tidak pernah terkunci.
		logrus.WithError(err).Warn("[MULTITENANT] penyemaian admin dari APP_BASIC_AUTH tidak selesai")
	}
	if created > 0 {
		logrus.Infof("[MULTITENANT] %d admin disemai dari APP_BASIC_AUTH", created)
	}

	logrus.Info("[MULTITENANT] mode multi-tenant aktif")
}

// startSessionSweeper membuang session kedaluwarsa secara berkala.
//
// Mengikuti pola lifecycle StartPresencePulseScheduler: goroutine dengan
// ticker yang berhenti saat context dibatalkan. ResolveSession sudah menghapus
// session mati yang kebetulan disentuh, jadi penyapu ini hanya mengurus baris
// yang tidak pernah dipakai lagi.
func startSessionSweeper(ctx context.Context) {
	if tenancyUsecase == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(sessionSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				deleted, err := tenancyUsecase.SweepExpiredSessions(ctx)
				if err != nil {
					logrus.WithError(err).Warn("[MULTITENANT] penyapuan session gagal")
					continue
				}
				if deleted > 0 {
					logrus.Debugf("[MULTITENANT] %d session kedaluwarsa dibersihkan", deleted)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

const sessionSweepInterval = time.Hour
