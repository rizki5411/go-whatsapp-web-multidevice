package whatsapp

import (
	"context"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	projectSQLite "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// commandRepoSpy records the repository calls handleCommand makes. Reaching
// GetMessageByIDAndDevice means the whole gate chain (prefix, config,
// authorization, dispatch) let the command through, which is what these tests
// assert on — the send itself needs a connected client and is out of scope.
type commandRepoSpy struct {
	domainChatStorage.IChatStorageRepository
	cfg              *domainChatStorage.DeviceCommandConfig
	configLookups    int
	messageLookups   int
	lastMessageQuery string
}

func (s *commandRepoSpy) GetDeviceCommandConfigByIdentifier(string) (*domainChatStorage.DeviceCommandConfig, error) {
	s.configLookups++
	return s.cfg, nil
}

func (s *commandRepoSpy) GetMessageByIDAndDevice(deviceID, id string) (*domainChatStorage.Message, error) {
	s.messageLookups++
	s.lastMessageQuery = id
	// Returning nil sends the handler down the "not found" reply path, which
	// fails harmlessly on the disconnected test client.
	return nil, nil
}

func (s *commandRepoSpy) StoreSentMessageWithContext(context.Context, string, string, string, string, time.Time, *waE2E.Message) error {
	return nil
}

// newCommandTestClient builds a logged-in-looking client backed by an in-memory
// store. It is never connected, so sends fail — the tests only rely on the
// gating that happens before a send.
func newCommandTestClient(t *testing.T) *whatsmeow.Client {
	t.Helper()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, projectSQLite.DriverName,
		projectSQLite.FormatChatStorageURI("file:command-handler-test?mode=memory&cache=shared", false, true), nil)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })

	device := container.NewDevice()
	owner := types.NewJID("628111111111", types.DefaultUserServer)
	device.ID = &owner
	return whatsmeow.NewClient(device, nil)
}

// commandEvent builds an incoming text message, optionally quoting another
// message id so it looks like a reply.
func commandEvent(text string, fromMe bool, quotedID string) *events.Message {
	sender := types.NewJID("628222222222", types.DefaultUserServer)
	chat := types.NewJID("120363000000", types.GroupServer)

	msg := &waE2E.Message{}
	if quotedID == "" {
		msg.Conversation = proto.String(text)
	} else {
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(quotedID),
				Participant:   proto.String(sender.String()),
				QuotedMessage: &waE2E.Message{Conversation: proto.String("original")},
			},
		}
	}

	return &events.Message{
		Info: types.MessageInfo{
			ID:   "cmd-evt-1",
			Type: "text",
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: fromMe,
				IsGroup:  true,
			},
			Timestamp: time.Now(),
		},
		Message: msg,
	}
}

func enabledCommandConfig() *domainChatStorage.DeviceCommandConfig {
	return &domainChatStorage.DeviceCommandConfig{
		DeviceID:       "dev",
		Enabled:        true,
		CommandTargets: map[string][]string{"forward": {"1203630000000001@g.us"}},
	}
}

func withNoopLog(t *testing.T) {
	t.Helper()
	original := log
	log = waLog.Noop
	t.Cleanup(func() { log = original })
}

func TestHandleCommandIgnoresNilClient(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("!ping", true, ""), repo, nil)

	if repo.configLookups != 0 {
		t.Fatalf("a nil client must short-circuit before any storage access, got %d lookups", repo.configLookups)
	}
}

func TestHandleCommandIgnoresNonCommandText(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("halo apa kabar", true, ""), repo, newCommandTestClient(t))

	if repo.configLookups != 0 {
		t.Fatalf("the \"!\" prefix gate must run before any config read, got %d lookups", repo.configLookups)
	}
}

func TestHandleCommandRequiresEnabledConfig(t *testing.T) {
	withNoopLog(t)
	client := newCommandTestClient(t)

	t.Run("no config row", func(t *testing.T) {
		repo := &commandRepoSpy{cfg: nil}
		handleCommand(context.Background(), commandEvent("!forward", true, "src-1"), repo, client)
		if repo.configLookups == 0 {
			t.Fatal("expected the handler to look the config up")
		}
		if repo.messageLookups != 0 {
			t.Fatalf("a device without a config must not dispatch, got %d message lookups", repo.messageLookups)
		}
	})

	t.Run("disabled config", func(t *testing.T) {
		cfg := enabledCommandConfig()
		cfg.Enabled = false
		repo := &commandRepoSpy{cfg: cfg}
		handleCommand(context.Background(), commandEvent("!forward", true, "src-1"), repo, client)
		if repo.messageLookups != 0 {
			t.Fatalf("a disabled config must not dispatch, got %d message lookups", repo.messageLookups)
		}
	})
}

func TestHandleCommandAuthorization(t *testing.T) {
	withNoopLog(t)
	client := newCommandTestClient(t)

	t.Run("stranger is ignored", func(t *testing.T) {
		repo := &commandRepoSpy{cfg: enabledCommandConfig()}
		handleCommand(context.Background(), commandEvent("!forward", false, "src-1"), repo, client)
		if repo.messageLookups != 0 {
			t.Fatalf("an unauthorized sender must not reach a command, got %d message lookups", repo.messageLookups)
		}
	})

	t.Run("owner is allowed", func(t *testing.T) {
		repo := &commandRepoSpy{cfg: enabledCommandConfig()}
		handleCommand(context.Background(), commandEvent("!forward", true, "src-1"), repo, client)
		if repo.messageLookups != 1 {
			t.Fatalf("the device owner must be allowed, got %d message lookups", repo.messageLookups)
		}
		if repo.lastMessageQuery != "src-1" {
			t.Fatalf("expected the quoted stanza id to be looked up, got %q", repo.lastMessageQuery)
		}
	})

	t.Run("whitelisted sender is allowed", func(t *testing.T) {
		cfg := enabledCommandConfig()
		cfg.AllowedSenders = []string{"628222222222@s.whatsapp.net"}
		repo := &commandRepoSpy{cfg: cfg}
		handleCommand(context.Background(), commandEvent("!forward", false, "src-1"), repo, client)
		if repo.messageLookups != 1 {
			t.Fatalf("a whitelisted sender must be allowed, got %d message lookups", repo.messageLookups)
		}
	})
}

func TestHandleCommandUnknownCommandDoesNotDispatch(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("!nope", true, "src-1"), repo, newCommandTestClient(t))

	if repo.messageLookups != 0 {
		t.Fatalf("an unknown command must not dispatch, got %d message lookups", repo.messageLookups)
	}
}

func TestHandleCommandForwardWithoutReplyDoesNotLookUpMessage(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	// No quoted message: !forward must answer with guidance instead of guessing
	// which message to forward.
	handleCommand(context.Background(), commandEvent("!forward", true, ""), repo, newCommandTestClient(t))

	if repo.messageLookups != 0 {
		t.Fatalf("!forward without a reply must not query storage, got %d message lookups", repo.messageLookups)
	}
}

func TestHandleCommandSkipsStatusBroadcast(t *testing.T) {
	withNoopLog(t)

	evt := commandEvent("!ping", true, "")
	evt.Info.Chat = types.NewJID("status", types.BroadcastServer)
	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	handleCommand(context.Background(), evt, repo, newCommandTestClient(t))

	if repo.configLookups != 0 {
		t.Fatalf("status broadcasts must never be treated as commands, got %d lookups", repo.configLookups)
	}
}

func TestCommandMessageText(t *testing.T) {
	if got := commandMessageText(commandEvent("  !ping  ", true, "")); got != "!ping" {
		t.Fatalf("plain conversation text should be trimmed, got %q", got)
	}
	if got := commandMessageText(commandEvent("!forward", true, "src-1")); got != "!forward" {
		t.Fatalf("extended text should be read, got %q", got)
	}

	// An edited message must not re-fire a command that already ran.
	edited := commandEvent("!ping", true, "")
	edited.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("cmd-evt-1")},
		EditedMessage: &waE2E.Message{
			Conversation: proto.String("!ping"),
		},
	}}
	if got := commandMessageText(edited); got != "" {
		t.Fatalf("edited messages must not produce command text, got %q", got)
	}
}

func TestCommandSenderAllowed(t *testing.T) {
	withNoopLog(t)
	ctx := context.Background()
	client := newCommandTestClient(t)
	cfg := &domainChatStorage.DeviceCommandConfig{AllowedSenders: []string{" 628222222222@s.whatsapp.net "}}

	if !commandSenderAllowed(ctx, commandEvent("!ping", true, ""), &domainChatStorage.DeviceCommandConfig{}, client) {
		t.Fatal("the device owner must always be allowed, even with an empty whitelist")
	}
	if commandSenderAllowed(ctx, commandEvent("!ping", false, ""), &domainChatStorage.DeviceCommandConfig{}, client) {
		t.Fatal("a stranger must not be allowed without a whitelist entry")
	}
	if !commandSenderAllowed(ctx, commandEvent("!ping", false, ""), cfg, client) {
		t.Fatal("a whitelisted sender must be allowed despite surrounding whitespace")
	}

	// An unresolvable @lid sender must not be let through just because the
	// whitelist is non-empty.
	lidEvt := commandEvent("!ping", false, "")
	lidEvt.Info.Sender = types.NewJID("111222333", types.HiddenUserServer)
	if commandSenderAllowed(ctx, lidEvt, cfg, client) {
		t.Fatal("an unresolved LID sender must not match a phone-JID whitelist")
	}
}

func TestRegisteredCommandsIsSorted(t *testing.T) {
	got := RegisteredCommands()
	want := []string{"forward", "ping"}
	if len(got) != len(want) {
		t.Fatalf("RegisteredCommands() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RegisteredCommands() = %v, want %v", got, want)
		}
	}
}

func TestForwardCommandContentLabelsMedia(t *testing.T) {
	if got := forwardCommandContent(&domainChatStorage.Message{Content: "halo"}); got != "halo" {
		t.Fatalf("text content should pass through, got %q", got)
	}
	if got := forwardCommandContent(&domainChatStorage.Message{MediaType: "image"}); got != "🖼️ Image" {
		t.Fatalf("image should get a label, got %q", got)
	}
}

func TestPlainDeliveryMessageRemovesForwardLabel(t *testing.T) {
	stored := &domainChatStorage.Message{ID: "src-1", Content: "halo grup"}

	labelled, err := utils.BuildForwardMessageFromStorage(stored, utils.ForwardBuildOptions{})
	if err != nil {
		t.Fatalf("build forward: %v", err)
	}
	if ci := utils.ExtractContextInfo(labelled); ci == nil || !ci.GetIsForwarded() {
		t.Fatal("the builder is expected to stamp IsForwarded; plain mode depends on that being what it strips")
	}

	plain := plainDeliveryMessage(labelled)
	if plain.GetConversation() != "halo grup" {
		t.Fatalf("a text forward should become a plain Conversation, got %+v", plain)
	}
	if ci := utils.ExtractContextInfo(plain); ci != nil && ci.GetIsForwarded() {
		t.Fatal("plain mode must not leave the forward marker behind")
	}
}

func TestPlainDeliveryMessageKeepsMediaButDropsMarkers(t *testing.T) {
	stored := &domainChatStorage.Message{
		ID:        "src-2",
		MediaType: "image",
		Content:   "caption",
		URL:       "https://mmg.whatsapp.net/x",
		MediaKey:  []byte("k"),
	}

	msg, err := utils.BuildForwardMessageFromStorage(stored, utils.ForwardBuildOptions{})
	if err != nil {
		t.Fatalf("build forward: %v", err)
	}

	plain := plainDeliveryMessage(msg)
	if plain.GetImageMessage() == nil {
		t.Fatal("media must survive plain mode; only the forward markers are dropped")
	}
	if plain.GetImageMessage().GetCaption() != "caption" {
		t.Fatalf("caption should survive, got %q", plain.GetImageMessage().GetCaption())
	}
	ci := utils.ExtractContextInfo(plain)
	if ci != nil && (ci.GetIsForwarded() || ci.GetForwardingScore() != 0) {
		t.Fatalf("forward markers should be cleared, got %+v", ci)
	}
}

func TestForwardModeDefaultsToLabelled(t *testing.T) {
	// A config written before the mode existed scans forward_mode as "", and it
	// must keep behaving exactly as it did rather than silently going plain.
	cfg := &domainChatStorage.DeviceCommandConfig{}
	if cfg.ForwardMode == domainChatStorage.ForwardModePlain {
		t.Fatal("the zero value must not mean plain")
	}
}
