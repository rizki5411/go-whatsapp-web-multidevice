package whatsapp

import (
	"context"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// forwardSourceSpy captures what the capture hook persists and serves it back.
type forwardSourceSpy struct {
	domainChatStorage.IChatStorageRepository
	saved  []*domainChatStorage.MessageForwardSource
	stored *domainChatStorage.MessageForwardSource
}

func (s *forwardSourceSpy) SaveMessageForwardSource(src *domainChatStorage.MessageForwardSource) error {
	s.saved = append(s.saved, src)
	return nil
}

func (s *forwardSourceSpy) GetMessageForwardSource(string, string, string) (*domainChatStorage.MessageForwardSource, error) {
	return s.stored, nil
}

func newsletterEvent(jid, name string, serverID int32) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			ID: "chan-1",
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("628111111111", types.DefaultUserServer),
				Sender: types.NewJID("628222222222", types.DefaultUserServer),
			},
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("berita"),
				ContextInfo: &waE2E.ContextInfo{
					IsForwarded: proto.Bool(true),
					ForwardedNewsletterMessageInfo: &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
						NewsletterJID:   proto.String(jid),
						NewsletterName:  proto.String(name),
						ServerMessageID: proto.Int32(serverID),
					},
				},
			},
		},
	}
}

func TestCaptureForwardSourceRecordsChannelAttribution(t *testing.T) {
	withNoopLog(t)

	repo := &forwardSourceSpy{}
	evt := newsletterEvent("120363000@newsletter", "BBC News", 4247)
	captureForwardSource(context.Background(), evt, repo, nil)

	if len(repo.saved) != 1 {
		t.Fatalf("expected one recorded source, got %d", len(repo.saved))
	}
	got := repo.saved[0]
	if got.NewsletterJID != "120363000@newsletter" || got.NewsletterName != "BBC News" {
		t.Fatalf("unexpected attribution: %+v", got)
	}
	if got.ServerMessageID != 4247 {
		t.Fatalf("server message id should be kept so View channel opens the post, got %d", got.ServerMessageID)
	}
	if got.MessageID != "chan-1" {
		t.Fatalf("row must be keyed by the message it describes, got %q", got.MessageID)
	}
}

func TestCaptureForwardSourceIgnoresOrdinaryMessages(t *testing.T) {
	withNoopLog(t)

	repo := &forwardSourceSpy{}
	captureForwardSource(context.Background(), commandEvent("halo", false, ""), repo, nil)
	captureForwardSource(context.Background(), commandEvent("balasan", false, "src-1"), repo, nil)

	if len(repo.saved) != 0 {
		t.Fatalf("ordinary messages must not write rows, got %d", len(repo.saved))
	}
}

func TestResolveForwardSourcePrefersLiveQuotedContext(t *testing.T) {
	withNoopLog(t)

	repo := &forwardSourceSpy{stored: &domainChatStorage.MessageForwardSource{
		NewsletterJID: "stored@newsletter", NewsletterName: "Stored",
	}}
	cmd := &commandContext{evt: commandEvent("!forward", true, "src-1"), repo: repo}

	live := &waE2E.ContextInfo{QuotedMessage: newsletterEvent("live@newsletter", "Live", 7).Message}
	got := resolveForwardSource(cmd, live, "src-1")
	if got == nil || got.GetNewsletterJID() != "live@newsletter" {
		t.Fatalf("the live quoted context should win when present, got %+v", got)
	}
	if got.GetServerMessageID() != 7 {
		t.Fatalf("live context carries the exact server id, got %d", got.GetServerMessageID())
	}
}

func TestResolveForwardSourceFallsBackToRecordedRow(t *testing.T) {
	withNoopLog(t)

	repo := &forwardSourceSpy{stored: &domainChatStorage.MessageForwardSource{
		NewsletterJID: "stored@newsletter", NewsletterName: "Stored", ServerMessageID: 12,
	}}
	cmd := &commandContext{evt: commandEvent("!forward", true, "src-1"), repo: repo}

	// A quoted copy with no nested context is the common case: clients trim it.
	trimmed := &waE2E.ContextInfo{QuotedMessage: &waE2E.Message{Conversation: proto.String("berita")}}
	got := resolveForwardSource(cmd, trimmed, "src-1")
	if got == nil || got.GetNewsletterJID() != "stored@newsletter" {
		t.Fatalf("expected the recorded row to be used, got %+v", got)
	}
	if got.GetServerMessageID() != 12 {
		t.Fatalf("recorded server id should be used, got %d", got.GetServerMessageID())
	}
}

func TestResolveForwardSourceOmitsZeroServerID(t *testing.T) {
	withNoopLog(t)

	repo := &forwardSourceSpy{stored: &domainChatStorage.MessageForwardSource{
		NewsletterJID: "stored@newsletter",
	}}
	cmd := &commandContext{evt: commandEvent("!forward", true, "src-1"), repo: repo}

	got := resolveForwardSource(cmd, nil, "src-1")
	if got == nil {
		t.Fatal("expected a source")
	}
	// A zero id addresses no post; leaving it unset lets the button open the channel.
	if got.ServerMessageID != nil {
		t.Fatalf("zero server id should be omitted, got %d", got.GetServerMessageID())
	}
}

func TestResolveForwardSourceNilForOrdinaryMessages(t *testing.T) {
	withNoopLog(t)

	cmd := &commandContext{evt: commandEvent("!forward", true, "src-1"), repo: &forwardSourceSpy{}}
	if got := resolveForwardSource(cmd, nil, "src-1"); got != nil {
		t.Fatalf("a message with no channel origin must resolve to nil, got %+v", got)
	}
}

func TestAttachNewsletterInfoAddsCardToRebuiltForward(t *testing.T) {
	stored := &domainChatStorage.Message{ID: "src-1", Content: "berita"}
	built, err := utils.BuildForwardMessageFromStorage(stored, utils.ForwardBuildOptions{})
	if err != nil {
		t.Fatalf("build forward: %v", err)
	}

	info := &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
		NewsletterJID:  proto.String("120363000@newsletter"),
		NewsletterName: proto.String("BBC News"),
	}
	attachNewsletterInfo(built, info)

	ci := utils.ExtractContextInfo(built)
	if ci == nil || ci.GetForwardedNewsletterMessageInfo() == nil {
		t.Fatal("the channel card should be attached to the rebuilt forward")
	}
	if ci.GetForwardedNewsletterMessageInfo().GetNewsletterName() != "BBC News" {
		t.Fatalf("unexpected channel name: %+v", ci.GetForwardedNewsletterMessageInfo())
	}
	if !ci.GetIsForwarded() {
		t.Fatal("attaching the card must not clear the Forwarded marker")
	}
}

func TestPlainModeDropsChannelCard(t *testing.T) {
	withNoopLog(t)

	cfg := &domainChatStorage.DeviceCommandConfig{ForwardMode: domainChatStorage.ForwardModePlain}
	cmd := &commandContext{cfg: cfg, evt: commandEvent("!forward", true, "src-1")}
	stored := &domainChatStorage.Message{ID: "src-1", Content: "berita"}
	info := &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
		NewsletterJID: proto.String("120363000@newsletter"),
	}

	built, err := buildForwardPayload(context.Background(), cmd, stored, info)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	// Plain mode means the group cannot tell the message was relayed, so the
	// channel card has to go along with the Forwarded label.
	if built.GetConversation() != "berita" {
		t.Fatalf("expected a plain Conversation, got %+v", built)
	}
	if ci := utils.ExtractContextInfo(built); ci != nil && ci.GetForwardedNewsletterMessageInfo() != nil {
		t.Fatal("plain mode must not keep the channel card")
	}
}

func TestForwardedModeKeepsChannelCard(t *testing.T) {
	withNoopLog(t)

	cfg := &domainChatStorage.DeviceCommandConfig{ForwardMode: domainChatStorage.ForwardModeForwarded}
	cmd := &commandContext{cfg: cfg, evt: commandEvent("!forward", true, "src-1")}
	stored := &domainChatStorage.Message{ID: "src-1", Content: "berita"}
	info := &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
		NewsletterJID:  proto.String("120363000@newsletter"),
		NewsletterName: proto.String("BBC News"),
	}

	built, err := buildForwardPayload(context.Background(), cmd, stored, info)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	ci := utils.ExtractContextInfo(built)
	if ci == nil || ci.GetForwardedNewsletterMessageInfo().GetNewsletterName() != "BBC News" {
		t.Fatalf("forwarded mode should carry the channel card, got %+v", ci)
	}
}
