package rest

import (
	"encoding/json"
	"net/http"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/gofiber/fiber/v3"
)

// fakeCommandConfigStore is an in-memory IChatStorageRepository covering only the
// methods the command config handlers use; the rest is the embedded (nil)
// interface, matching fakeConfigStore.
type fakeCommandConfigStore struct {
	domainChatStorage.IChatStorageRepository
	configs map[string]*domainChatStorage.DeviceCommandConfig
	nextID  int64
}

func newFakeCommandConfigStore() *fakeCommandConfigStore {
	return &fakeCommandConfigStore{configs: map[string]*domainChatStorage.DeviceCommandConfig{}}
}

func (f *fakeCommandConfigStore) SaveDeviceCommandConfig(cfg *domainChatStorage.DeviceCommandConfig) error {
	if cfg.ID == 0 {
		f.nextID++
		cfg.ID = f.nextID
	}
	clone := *cfg
	f.configs[cfg.DeviceID] = &clone
	return nil
}

func (f *fakeCommandConfigStore) GetDeviceCommandConfig(deviceID string) (*domainChatStorage.DeviceCommandConfig, error) {
	if cfg, ok := f.configs[deviceID]; ok {
		clone := *cfg
		return &clone, nil
	}
	return nil, nil
}

func (f *fakeCommandConfigStore) ListDeviceCommandConfigs() ([]*domainChatStorage.DeviceCommandConfig, error) {
	out := make([]*domainChatStorage.DeviceCommandConfig, 0, len(f.configs))
	for _, cfg := range f.configs {
		clone := *cfg
		out = append(out, &clone)
	}
	return out, nil
}

func (f *fakeCommandConfigStore) DeleteDeviceCommandConfig(deviceID string) error {
	delete(f.configs, deviceID)
	return nil
}

func newCommandConfigTestApp(t *testing.T, store *fakeCommandConfigStore) *fiber.App {
	t.Helper()
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance("dev", nil, nil))

	app := fiber.New()
	InitRestCommandConfig(app, dm, store)
	return app
}

// resultsOf decodes the Results object of a success envelope.
func resultsOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var envelope struct {
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
	return envelope.Results
}

func TestCommandConfigCRUDFlow(t *testing.T) {
	store := newFakeCommandConfigStore()
	app := newCommandConfigTestApp(t, store)

	// Missing config is a 404, not an empty 200.
	resp, body := doJSON(t, app, http.MethodGet, "/devices/dev/command/config", "")
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("get before create status = %d body=%s", resp.StatusCode, body)
	}

	// Create. enabled omitted must default to true.
	resp, body = doJSON(t, app, http.MethodPut, "/devices/dev/command/config",
		`{"command_targets":{"forward":["1203630000000001@g.us"]},"allowed_senders":["628222222222@s.whatsapp.net"]}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("create status = %d body=%s", resp.StatusCode, body)
	}
	results := resultsOf(t, body)
	if enabled, _ := results["enabled"].(bool); !enabled {
		t.Fatalf("enabled should default to true on create, got %v", results["enabled"])
	}
	if _, ok := results["available_commands"]; !ok {
		t.Fatalf("response should surface available_commands: %s", body)
	}

	// Update without enabled must keep the stored value rather than resetting it.
	if _, body := doJSON(t, app, http.MethodPut, "/devices/dev/command/config",
		`{"enabled":false,"command_targets":{"forward":["1203630000000001@g.us"]}}`); body == "" {
		t.Fatal("expected a response body")
	}
	resp, body = doJSON(t, app, http.MethodPut, "/devices/dev/command/config",
		`{"command_targets":{"forward":["1203630000000001@g.us"]}}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("update status = %d body=%s", resp.StatusCode, body)
	}
	if enabled, _ := resultsOf(t, body)["enabled"].(bool); enabled {
		t.Fatalf("omitted enabled must preserve the stored false, got %s", body)
	}

	// The upsert must not create a second row.
	if len(store.configs) != 1 {
		t.Fatalf("expected exactly 1 stored config, got %d", len(store.configs))
	}

	resp, body = doJSON(t, app, http.MethodGet, "/command/configs", "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("list status = %d body=%s", resp.StatusCode, body)
	}

	resp, body = doJSON(t, app, http.MethodDelete, "/devices/dev/command/config", "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("delete status = %d body=%s", resp.StatusCode, body)
	}
	if len(store.configs) != 0 {
		t.Fatalf("expected config to be deleted, store still has %d", len(store.configs))
	}
}

func TestCommandConfigUnknownDeviceIs404(t *testing.T) {
	app := newCommandConfigTestApp(t, newFakeCommandConfigStore())

	resp, body := doJSON(t, app, http.MethodPut, "/devices/ghost/command/config", `{}`)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 for unknown device, got %d body=%s", resp.StatusCode, body)
	}
}

func TestCommandConfigValidationRejections(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			// A command that is not registered would never fire; accepting it
			// would silently swallow a typo.
			name: "unknown command",
			body: `{"command_targets":{"nope":["1203630000000001@g.us"]}}`,
		},
		{
			name: "target is not a group",
			body: `{"command_targets":{"forward":["628111111111@s.whatsapp.net"]}}`,
		},
		{
			name: "allowed sender is a group",
			body: `{"allowed_senders":["1203630000000001@g.us"]}`,
		},
		{
			// A mistyped mode must not fall back silently: the operator would keep
			// sending labelled forwards with no way to notice.
			name: "unknown forward mode",
			body: `{"forward_mode":"plainn"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeCommandConfigStore()
			app := newCommandConfigTestApp(t, store)

			resp, body := doJSON(t, app, http.MethodPut, "/devices/dev/command/config", tc.body)
			if resp.StatusCode != fiber.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", resp.StatusCode, body)
			}
			if len(store.configs) != 0 {
				t.Fatal("a rejected request must not persist anything")
			}
		})
	}
}

func TestCommandConfigForwardMode(t *testing.T) {
	store := newFakeCommandConfigStore()
	app := newCommandConfigTestApp(t, store)

	// Omitted on create means the labelled forward, matching pre-existing behavior.
	resp, body := doJSON(t, app, http.MethodPut, "/devices/dev/command/config", `{}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("create status = %d body=%s", resp.StatusCode, body)
	}
	if got := resultsOf(t, body)["forward_mode"]; got != "forwarded" {
		t.Fatalf("forward_mode should default to forwarded, got %v", got)
	}

	// Switch to plain.
	resp, body = doJSON(t, app, http.MethodPut, "/devices/dev/command/config", `{"forward_mode":"plain"}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("switch status = %d body=%s", resp.StatusCode, body)
	}
	if got := resultsOf(t, body)["forward_mode"]; got != "plain" {
		t.Fatalf("forward_mode should be plain, got %v", got)
	}

	// Omitted on update keeps the stored mode rather than resetting it.
	resp, body = doJSON(t, app, http.MethodPut, "/devices/dev/command/config", `{"enabled":true}`)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("update status = %d body=%s", resp.StatusCode, body)
	}
	if got := resultsOf(t, body)["forward_mode"]; got != "plain" {
		t.Fatalf("omitted forward_mode must preserve plain, got %v", got)
	}
}

func TestNormalizeCommandTargetsDedupesAndDropsEmpty(t *testing.T) {
	targets, code, msg := normalizeCommandTargets(map[string][]string{
		"forward": {"1203630000000001@g.us", " 1203630000000001@g.us ", ""},
		"ping":    {},
	})
	if code != "" {
		t.Fatalf("unexpected rejection %s: %s", code, msg)
	}
	if len(targets["forward"]) != 1 {
		t.Fatalf("duplicate targets should collapse to one, got %v", targets["forward"])
	}
	if _, ok := targets["ping"]; ok {
		t.Fatalf("a command with no targets should be dropped, got %v", targets)
	}
}

func TestNormalizeAllowedSendersDedupes(t *testing.T) {
	senders, code, msg := normalizeAllowedSenders([]string{
		"628222222222@s.whatsapp.net",
		" 628222222222@s.whatsapp.net ",
		"",
	})
	if code != "" {
		t.Fatalf("unexpected rejection %s: %s", code, msg)
	}
	if len(senders) != 1 {
		t.Fatalf("duplicate senders should collapse to one, got %v", senders)
	}
}
