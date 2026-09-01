package whatsapp

import (
	"context"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func statusEnabledCommandConfig() *domainChatStorage.DeviceCommandConfig {
	cfg := enabledCommandConfig()
	cfg.StatusEnabled = true
	return cfg
}

func TestHandleCommandStatusRequiresStatusEnabled(t *testing.T) {
	withNoopLog(t)

	// The device config is enabled, but !status has its own switch: a reply that
	// would otherwise be posted must not even be looked up while it is off.
	repo := &commandRepoSpy{cfg: enabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("!status", true, "src-1"), repo, newCommandTestClient(t))

	if repo.messageLookups != 0 {
		t.Fatalf("!status must not query storage while disabled, got %d message lookups", repo.messageLookups)
	}
}

func TestHandleCommandStatusWithoutReplyDoesNotLookUpMessage(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: statusEnabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("!status", true, ""), repo, newCommandTestClient(t))

	if repo.messageLookups != 0 {
		t.Fatalf("!status without a reply must not query storage, got %d message lookups", repo.messageLookups)
	}
}

func TestHandleCommandStatusEnabledLooksUpQuotedMessage(t *testing.T) {
	withNoopLog(t)

	repo := &commandRepoSpy{cfg: statusEnabledCommandConfig()}
	handleCommand(context.Background(), commandEvent("!status", true, "src-9"), repo, newCommandTestClient(t))

	if repo.messageLookups != 1 {
		t.Fatalf("!status must resolve the quoted message, got %d message lookups", repo.messageLookups)
	}
	if repo.lastMessageQuery != "src-9" {
		t.Fatalf("!status looked up %q, want the quoted id src-9", repo.lastMessageQuery)
	}
}

func TestIsStatusPostableMessage(t *testing.T) {
	cases := []struct {
		name    string
		message *domainChatStorage.Message
		want    bool
	}{
		{"text", &domainChatStorage.Message{Content: "halo"}, true},
		{"empty text", &domainChatStorage.Message{}, false},
		{"image", &domainChatStorage.Message{MediaType: "image", URL: "https://x/1"}, true},
		{"video", &domainChatStorage.Message{MediaType: "video", URL: "https://x/1"}, true},
		{"voice note", &domainChatStorage.Message{MediaType: "ptt", URL: "https://x/1"}, true},
		// Rejected up front: WhatsApp has no status representation for these, so
		// sending them would look like it worked and show nothing.
		{"document", &domainChatStorage.Message{MediaType: "document", URL: "https://x/1"}, false},
		{"sticker", &domainChatStorage.Message{MediaType: "sticker", URL: "https://x/1"}, false},
		{"video note", &domainChatStorage.Message{MediaType: "video_note", URL: "https://x/1"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStatusPostableMessage(tc.message); got != tc.want {
				t.Fatalf("isStatusPostableMessage(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestBuildStatusTextMessageCarriesStatusColors(t *testing.T) {
	msg := buildStatusTextMessage("halo")

	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		t.Fatal("text status must be an ExtendedTextMessage")
	}
	if ext.GetText() != "halo" {
		t.Fatalf("text = %q, want halo", ext.GetText())
	}
	if ext.GetBackgroundArgb() != statusTextBackgroundARGB || ext.GetTextArgb() != statusTextForegroundARGB {
		t.Fatalf("colors = %#x/%#x, want %#x/%#x",
			ext.GetBackgroundArgb(), ext.GetTextArgb(), statusTextBackgroundARGB, statusTextForegroundARGB)
	}
	if ext.GetContextInfo().GetIsForwarded() {
		t.Fatal("a status must never be marked as forwarded")
	}
}

func TestBuildStatusPayloadStripsForwardMarkers(t *testing.T) {
	withNoopLog(t)

	message := &domainChatStorage.Message{
		ID:        "src-1",
		MediaType: "image",
		Content:   "caption",
		URL:       "https://mmg.whatsapp.net/x",
		MediaKey:  []byte("key"),
	}
	cmd := &commandContext{cfg: statusEnabledCommandConfig()}

	payload, err := buildStatusPayload(context.Background(), cmd, message, nil)
	if err != nil {
		t.Fatalf("buildStatusPayload: %v", err)
	}

	img := payload.GetImageMessage()
	if img == nil {
		t.Fatal("image status must be an ImageMessage")
	}
	if img.GetCaption() != "caption" {
		t.Fatalf("caption = %q, want caption", img.GetCaption())
	}
	if img.GetContextInfo().GetIsForwarded() || img.GetContextInfo().GetForwardingScore() != 0 {
		t.Fatal("a status must carry no forward markers")
	}
}

func TestBuildStatusPayloadTextIgnoresForwardContext(t *testing.T) {
	withNoopLog(t)

	cmd := &commandContext{cfg: statusEnabledCommandConfig()}
	payload, err := buildStatusPayload(context.Background(), cmd, &domainChatStorage.Message{Content: "halo"}, &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
		NewsletterJID: proto.String("120363000000000000@newsletter"),
	})
	if err != nil {
		t.Fatalf("buildStatusPayload: %v", err)
	}
	if ci := payload.GetExtendedTextMessage().GetContextInfo(); ci.GetForwardedNewsletterMessageInfo() != nil {
		t.Fatal("a text status must not carry a channel card")
	}
}
