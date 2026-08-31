package chatstorage

import (
	"reflect"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

func TestSQLiteRepositoryDeviceCommandConfigCRUD(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	cfg := &domainChatStorage.DeviceCommandConfig{
		DeviceID:  "busine",
		DeviceJID: "628111111111@s.whatsapp.net",
		Enabled:   true,
		CommandTargets: map[string][]string{
			"forward": {"1203630000000001@g.us", "1203630000000002@g.us"},
		},
		AllowedSenders: []string{"628222222222@s.whatsapp.net"},
	}
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if cfg.ID == 0 {
		t.Fatal("expected config ID to be populated after insert")
	}

	got, err := repo.GetDeviceCommandConfig("busine")
	if err != nil || got == nil {
		t.Fatalf("get config: %v (got=%v)", err, got)
	}
	if !got.Enabled || got.DeviceJID != "628111111111@s.whatsapp.net" {
		t.Fatalf("unexpected config: %+v", got)
	}
	if !reflect.DeepEqual(got.CommandTargets, cfg.CommandTargets) {
		t.Fatalf("command targets round-trip mismatch: got %v want %v", got.CommandTargets, cfg.CommandTargets)
	}
	if !reflect.DeepEqual(got.AllowedSenders, cfg.AllowedSenders) {
		t.Fatalf("allowed senders round-trip mismatch: got %v want %v", got.AllowedSenders, cfg.AllowedSenders)
	}

	// Upsert on the same device id must update in place, not insert a second row.
	cfg.Enabled = false
	cfg.CommandTargets = map[string][]string{"forward": {"1203630000000003@g.us"}}
	cfg.AllowedSenders = nil
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	got, err = repo.GetDeviceCommandConfig("busine")
	if err != nil || got == nil {
		t.Fatalf("get updated config: %v (got=%v)", err, got)
	}
	if got.Enabled {
		t.Fatal("expected config to be disabled after update")
	}
	if len(got.CommandTargets["forward"]) != 1 || got.CommandTargets["forward"][0] != "1203630000000003@g.us" {
		t.Fatalf("unexpected targets after update: %v", got.CommandTargets)
	}
	// A nil slice must come back as an empty (non-nil) slice so callers can range
	// over it without a nil check.
	if got.AllowedSenders == nil || len(got.AllowedSenders) != 0 {
		t.Fatalf("expected empty allowed senders, got %v", got.AllowedSenders)
	}

	list, err := repo.ListDeviceCommandConfigs()
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 config after upsert, got %d", len(list))
	}

	if err := repo.DeleteDeviceCommandConfig("busine"); err != nil {
		t.Fatalf("delete config: %v", err)
	}
	got, err = repo.GetDeviceCommandConfig("busine")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil config after delete, got %+v", got)
	}
}

func TestSQLiteRepositoryDeviceCommandConfigForwardMode(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	// An unset mode must persist as the labelled forward. Storing "" and reading
	// it back as plain would silently strip the Forwarded label from every send.
	cfg := &domainChatStorage.DeviceCommandConfig{DeviceID: "a", Enabled: true}
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ := repo.GetDeviceCommandConfig("a")
	if got.ForwardMode != domainChatStorage.ForwardModeForwarded {
		t.Fatalf("unset mode should store as forwarded, got %q", got.ForwardMode)
	}

	cfg.ForwardMode = domainChatStorage.ForwardModePlain
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = repo.GetDeviceCommandConfig("a")
	if got.ForwardMode != domainChatStorage.ForwardModePlain {
		t.Fatalf("plain mode should round-trip, got %q", got.ForwardMode)
	}

	// Anything unrecognized falls back rather than reaching the send path.
	cfg.ForwardMode = "nonsense"
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("update with bad mode: %v", err)
	}
	got, _ = repo.GetDeviceCommandConfig("a")
	if got.ForwardMode != domainChatStorage.ForwardModeForwarded {
		t.Fatalf("unrecognized mode should fall back to forwarded, got %q", got.ForwardMode)
	}
}

func TestSQLiteRepositoryDeviceCommandConfigMissingReturnsNil(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	cfg, err := repo.GetDeviceCommandConfig("nope")
	if err != nil {
		t.Fatalf("expected nil error for missing config, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing device, got %+v", cfg)
	}

	cfg, err = repo.GetDeviceCommandConfigByIdentifier("nope")
	if err != nil {
		t.Fatalf("expected nil error for missing identifier, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing identifier, got %+v", cfg)
	}
}

func TestSQLiteRepositoryDeviceCommandConfigByIdentifier(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	cfg := &domainChatStorage.DeviceCommandConfig{
		DeviceID:  "busine",
		DeviceJID: "628111111111@s.whatsapp.net",
		Enabled:   true,
	}
	if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	byID, err := repo.GetDeviceCommandConfigByIdentifier("busine")
	if err != nil || byID == nil {
		t.Fatalf("resolve by device id: %v (got=%v)", err, byID)
	}
	byJID, err := repo.GetDeviceCommandConfigByIdentifier("628111111111@s.whatsapp.net")
	if err != nil || byJID == nil {
		t.Fatalf("resolve by device jid: %v (got=%v)", err, byJID)
	}
	if byID.ID != byJID.ID {
		t.Fatalf("both identities must resolve the same row: %d vs %d", byID.ID, byJID.ID)
	}
}

func TestSQLiteRepositoryDeviceCommandConfigIdentifierCollisionErrors(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	// One device's user-facing id equals another device's WhatsApp JID. Picking a
	// winner would run commands against the wrong device's targets, so the
	// repository must surface the collision instead.
	first := &domainChatStorage.DeviceCommandConfig{
		DeviceID:  "628111111111@s.whatsapp.net",
		DeviceJID: "629000000000@s.whatsapp.net",
		Enabled:   true,
	}
	second := &domainChatStorage.DeviceCommandConfig{
		DeviceID:  "other",
		DeviceJID: "628111111111@s.whatsapp.net",
		Enabled:   true,
	}
	for _, cfg := range []*domainChatStorage.DeviceCommandConfig{first, second} {
		if err := repo.SaveDeviceCommandConfig(cfg); err != nil {
			t.Fatalf("save config %s: %v", cfg.DeviceID, err)
		}
	}

	if _, err := repo.GetDeviceCommandConfigByIdentifier("628111111111@s.whatsapp.net"); err == nil {
		t.Fatal("expected an ambiguity error for a colliding identifier")
	}
}

func TestSQLiteRepositoryDeviceCommandConfigRejectsEmptyDeviceID(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if err := repo.SaveDeviceCommandConfig(&domainChatStorage.DeviceCommandConfig{}); err == nil {
		t.Fatal("expected save to reject an empty device id")
	}
	if err := repo.DeleteDeviceCommandConfig("   "); err == nil {
		t.Fatal("expected delete to reject an empty device id")
	}
}

func TestSQLiteRepositoryMessageForwardSourceRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	src := &domainChatStorage.MessageForwardSource{
		DeviceID:        "dev",
		ChatJID:         "628111111111@s.whatsapp.net",
		MessageID:       "msg-1",
		NewsletterJID:   "120363000@newsletter",
		NewsletterName:  "BBC News",
		ServerMessageID: 4247,
	}
	if err := repo.SaveMessageForwardSource(src); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := repo.GetMessageForwardSource("dev", "628111111111@s.whatsapp.net", "msg-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (got=%v)", err, got)
	}
	if got.NewsletterJID != src.NewsletterJID || got.NewsletterName != "BBC News" || got.ServerMessageID != 4247 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// The same message can be delivered twice (history sync, retries); saving
	// again must update in place rather than fail on the primary key.
	src.NewsletterName = "BBC News Indonesia"
	if err := repo.SaveMessageForwardSource(src); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, _ = repo.GetMessageForwardSource("dev", "628111111111@s.whatsapp.net", "msg-1")
	if got.NewsletterName != "BBC News Indonesia" {
		t.Fatalf("re-save should update in place, got %q", got.NewsletterName)
	}

	// Another device must not see this row.
	other, err := repo.GetMessageForwardSource("other", "628111111111@s.whatsapp.net", "msg-1")
	if err != nil {
		t.Fatalf("cross-device get: %v", err)
	}
	if other != nil {
		t.Fatalf("forward sources must be device-scoped, got %+v", other)
	}
}

func TestSQLiteRepositoryMessageForwardSourceMissingReturnsNil(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	got, err := repo.GetMessageForwardSource("dev", "chat@s.whatsapp.net", "nope")
	if err != nil {
		t.Fatalf("expected nil error for a missing row, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a message with no recorded origin, got %+v", got)
	}
}

func TestSQLiteRepositoryDeleteDeviceDataRemovesForwardSources(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if err := repo.SaveMessageForwardSource(&domainChatStorage.MessageForwardSource{
		DeviceID: "dev", ChatJID: "c@s.whatsapp.net", MessageID: "m", NewsletterJID: "n@newsletter",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := repo.DeleteDeviceData("dev"); err != nil {
		t.Fatalf("delete device data: %v", err)
	}
	got, _ := repo.GetMessageForwardSource("dev", "c@s.whatsapp.net", "m")
	if got != nil {
		t.Fatalf("forward sources must not survive device deletion, got %+v", got)
	}
}
