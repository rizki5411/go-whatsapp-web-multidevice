package cmd

import (
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
)

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
