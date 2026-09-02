package cmd

import (
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// resetMultiTenantConfig memulihkan default paket config dan state flag/viper
// setelah tiap kasus uji. Config di project ini adalah variabel global paket,
// jadi tanpa ini kasus uji akan saling bocor.
func resetMultiTenantConfig(t *testing.T) {
	t.Helper()

	prevEnabled := config.MultiTenantEnabled
	prevTTL := config.MultiTenantSessionTTL
	prevCookie := config.MultiTenantSessionCookie
	prevSecure := config.MultiTenantSecureCookie

	t.Cleanup(func() {
		config.MultiTenantEnabled = prevEnabled
		config.MultiTenantSessionTTL = prevTTL
		config.MultiTenantSessionCookie = prevCookie
		config.MultiTenantSecureCookie = prevSecure

		for _, name := range []string{
			"multi-tenant-enabled",
			"multi-tenant-session-ttl",
			"multi-tenant-session-cookie",
			"multi-tenant-secure-cookie",
		} {
			if flag := rootCmd.PersistentFlags().Lookup(name); flag != nil {
				flag.Changed = false
			}
		}

		for _, key := range []string{
			"multi_tenant_enabled",
			"multi_tenant_session_ttl",
			"multi_tenant_session_cookie",
			"multi_tenant_secure_cookie",
		} {
			viper.Set(key, nil)
		}
	})
}

func TestMultiTenantDefaultsAreOff(t *testing.T) {
	resetMultiTenantConfig(t)

	// Default harus mati: fitur ini opt-in, dan default yang menyala akan
	// mengubah perilaku deployment yang sudah jalan begitu binary di-upgrade.
	require.False(t, config.MultiTenantEnabled)
	require.Equal(t, 12*time.Hour, config.MultiTenantSessionTTL)
	require.Equal(t, "gowa_session", config.MultiTenantSessionCookie)
	require.False(t, config.MultiTenantSecureCookie)
}

func TestLoadMultiTenantEnvConfigReadsEnv(t *testing.T) {
	resetMultiTenantConfig(t)

	viper.Set("multi_tenant_enabled", true)
	viper.Set("multi_tenant_session_ttl", "30m")
	viper.Set("multi_tenant_session_cookie", "custom_session")
	viper.Set("multi_tenant_secure_cookie", true)

	loadMultiTenantEnvConfig()

	require.True(t, config.MultiTenantEnabled)
	require.Equal(t, 30*time.Minute, config.MultiTenantSessionTTL)
	require.Equal(t, "custom_session", config.MultiTenantSessionCookie)
	require.True(t, config.MultiTenantSecureCookie)
}

// TestLoadMultiTenantEnvConfigFlagBeatsEnv menjaga prioritas flag > env > .env.
// Ini alasan loadMultiTenantEnvConfig memakai flag.Changed dan bukan menaruh
// binding di initEnvConfig: initEnvConfig berjalan setelah Cobra menulis nilai
// flag, jadi env di sana akan menimpa flag.
func TestLoadMultiTenantEnvConfigFlagBeatsEnv(t *testing.T) {
	resetMultiTenantConfig(t)

	flag := rootCmd.PersistentFlags().Lookup("multi-tenant-enabled")
	require.NotNil(t, flag, "flag multi-tenant-enabled harus terdaftar")

	// Simulasikan --multi-tenant-enabled=false secara eksplisit sementara env
	// meminta true. Flag harus menang.
	require.NoError(t, flag.Value.Set("false"))
	flag.Changed = true
	config.MultiTenantEnabled = false

	viper.Set("multi_tenant_enabled", true)

	loadMultiTenantEnvConfig()

	require.False(t, config.MultiTenantEnabled, "flag eksplisit harus menang atas env")
}

// TestLoadMultiTenantEnvConfigKeepsDefaultsWhenUnset memastikan key yang tidak
// di-set tidak menimpa default dengan zero value — jebakan yang sudah
// didokumentasikan di config/settings.go untuk setting Web UI.
func TestLoadMultiTenantEnvConfigKeepsDefaultsWhenUnset(t *testing.T) {
	resetMultiTenantConfig(t)

	config.MultiTenantSessionTTL = 12 * time.Hour
	config.MultiTenantSessionCookie = "gowa_session"

	loadMultiTenantEnvConfig()

	require.Equal(t, 12*time.Hour, config.MultiTenantSessionTTL)
	require.Equal(t, "gowa_session", config.MultiTenantSessionCookie)
}
