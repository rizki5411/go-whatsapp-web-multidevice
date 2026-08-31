package whatsapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Inbound "!" chat command system.
//
// handleCommand is called from handleMessage next to handleAutoReply. Unlike
// auto-reply it is configured per device (device_command_config) rather than by
// a process-wide env var, and it deliberately runs in groups as well as 1:1
// chats: forwarding a message that lives in a group is the main use case.
//
// The feature is inert until an enabled config row exists for the device, so
// existing installations see no behavior change.

// commandPrefix marks a message as a command. It is checked before any database
// read so ordinary messages cost nothing.
const commandPrefix = "!"

// commandTimeout bounds a command's own work once it is detached from the event
// loop, mirroring the webhook forward goroutine's bounded context.
const commandTimeout = 2 * time.Minute

// commandContext carries everything a command handler needs. Commands take this
// single struct rather than loose arguments so that adding a new dependency
// later does not force a change to every registered handler.
type commandContext struct {
	evt    *events.Message
	client *whatsmeow.Client
	repo   domainChatStorage.IChatStorageRepository
	cfg    *domainChatStorage.DeviceCommandConfig
	// deviceID is the storage identity (JID first, device id as fallback), used
	// for device-scoped message lookups.
	deviceID string
	// name is the invoked command, lowercased and without the prefix.
	name string
	// args are the whitespace-separated arguments after the command word.
	args []string
}

type commandHandlerFunc func(ctx context.Context, cmd *commandContext) error

// commandRegistry maps a command name (lowercase, without the "!" prefix) to its
// handler. Register new commands here — do not grow handleCommand into an
// if-else chain.
var commandRegistry = map[string]commandHandlerFunc{
	"ping":    handlePingCommand,
	"forward": handleForwardCommand,
}

// RegisteredCommands returns the known command names in sorted order. The REST
// config handler uses it to reject targets for commands that will never run.
func RegisteredCommands() []string {
	names := make([]string, 0, len(commandRegistry))
	for name := range commandRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// handleCommand dispatches an inbound "!" command for the device that received
// the message. It never returns an error: like the other message-side handlers
// it logs and moves on so one bad command cannot break message processing.
func handleCommand(ctx context.Context, evt *events.Message, chatStorageRepo domainChatStorage.IChatStorageRepository, client *whatsmeow.Client) {
	if client == nil {
		log.Debugf("Command: skipping, client is nil")
		return
	}
	if evt == nil || chatStorageRepo == nil {
		return
	}

	text := commandMessageText(evt)
	if !strings.HasPrefix(text, commandPrefix) {
		return
	}

	// Broadcast and status posts are never commands. Groups are intentionally
	// allowed here, unlike handleAutoReply.
	if evt.Info.IsIncomingBroadcast() ||
		strings.Contains(evt.Info.SourceString(), "broadcast") ||
		strings.HasSuffix(evt.Info.Chat.String(), "@broadcast") ||
		strings.HasPrefix(evt.Info.Chat.String(), "status@") {
		log.Debugf("Command: skipping message %s, broadcast/status context", evt.Info.ID)
		return
	}

	cfg, deviceID := loadCommandConfig(ctx, chatStorageRepo, client, evt)
	if cfg == nil {
		return
	}

	if !commandSenderAllowed(ctx, evt, cfg, client) {
		// Stay silent rather than replying: an unauthorized sender must not be
		// able to confirm that this number runs a bot, or to make it send.
		log.Debugf("Command: message %s from %s is not authorized for device %s", evt.Info.ID, evt.Info.Sender, cfg.DeviceID)
		return
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], commandPrefix))
	handler, ok := commandRegistry[name]
	if !ok {
		log.Debugf("Command: unknown command %q on device %s", name, cfg.DeviceID)
		return
	}

	cmd := &commandContext{
		evt:      evt,
		client:   client,
		repo:     chatStorageRepo,
		cfg:      cfg,
		deviceID: deviceID,
		name:     name,
		args:     fields[1:],
	}

	log.Infof("Command: running !%s for device %s (message %s)", name, cfg.DeviceID, evt.Info.ID)
	if err := handler(ctx, cmd); err != nil {
		log.Errorf("Command: !%s failed for device %s: %v", name, cfg.DeviceID, err)
	}
}

// commandMessageText returns the trimmed typed text of a message, or "" when the
// message carries none. Edited messages (ProtocolMessage) are deliberately
// ignored so editing a message cannot re-fire a command that already ran.
func commandMessageText(evt *events.Message) string {
	inner := utils.UnwrapMessage(evt.Message)
	if inner == nil {
		return ""
	}
	if conv := inner.GetConversation(); conv != "" {
		return strings.TrimSpace(conv)
	}
	if ext := inner.GetExtendedTextMessage(); ext != nil {
		return strings.TrimSpace(ext.GetText())
	}
	return ""
}

// commandDeviceID resolves the storage identity of the device handling this
// event, matching how chat/message rows are keyed (JID first). Same resolution
// order as pollDeviceID.
func commandDeviceID(ctx context.Context, client *whatsmeow.Client) string {
	if instance, ok := DeviceFromContext(ctx); ok && instance != nil {
		if jid := instance.JID(); jid != "" {
			return jid
		}
		if id := instance.ID(); id != "" {
			return id
		}
	}
	if client != nil && client.Store != nil && client.Store.ID != nil {
		return client.Store.ID.ToNonAD().String()
	}
	return ""
}

// loadCommandConfig returns the enabled command config for this device plus the
// storage device id to use for message lookups. It returns a nil config when the
// device has no config row or the row is disabled, which is the normal state for
// devices that do not use the command system.
func loadCommandConfig(ctx context.Context, repo domainChatStorage.IChatStorageRepository, client *whatsmeow.Client, evt *events.Message) (*domainChatStorage.DeviceCommandConfig, string) {
	deviceID := commandDeviceID(ctx, client)

	// The config is written by REST under the user-facing device id, while the
	// event side may only know the JID. Try both identities.
	identifiers := make([]string, 0, 3)
	if instance, ok := DeviceFromContext(ctx); ok && instance != nil {
		identifiers = append(identifiers, instance.ID(), instance.JID())
	}
	identifiers = append(identifiers, deviceID)

	seen := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		identifier = strings.TrimSpace(identifier)
		if identifier == "" {
			continue
		}
		if _, dup := seen[identifier]; dup {
			continue
		}
		seen[identifier] = struct{}{}

		cfg, err := repo.GetDeviceCommandConfigByIdentifier(identifier)
		if err != nil {
			log.Warnf("Command: failed to load config for %s: %v", identifier, err)
			return nil, deviceID
		}
		if cfg == nil {
			continue
		}
		if !cfg.Enabled {
			log.Debugf("Command: config for %s is disabled, skipping message %s", cfg.DeviceID, evt.Info.ID)
			return nil, deviceID
		}
		return cfg, deviceID
	}
	return nil, deviceID
}

// commandSenderAllowed reports whether the sender may run commands: the device
// owner always may, plus any JID in the device's allowed_senders whitelist.
//
// Group senders often arrive as @lid, while the whitelist is stored as phone
// JIDs (that is what an operator can actually type). Both the raw and the
// LID-resolved identity are therefore compared, so a whitelisted contact is
// still recognized when WhatsApp hands us their LID.
func commandSenderAllowed(ctx context.Context, evt *events.Message, cfg *domainChatStorage.DeviceCommandConfig, client *whatsmeow.Client) bool {
	if evt.Info.IsFromMe {
		return true
	}
	if len(cfg.AllowedSenders) == 0 {
		return false
	}

	candidates := make([]string, 0, 2)
	if raw := evt.Info.Sender.ToNonAD().String(); raw != "" {
		candidates = append(candidates, raw)
	}
	if evt.Info.Sender.Server == types.HiddenUserServer {
		if resolved := NormalizeJIDFromLID(ctx, evt.Info.Sender.ToNonAD(), client); resolved.Server != types.HiddenUserServer {
			candidates = append(candidates, resolved.ToNonAD().String())
		}
	}

	for _, allowed := range cfg.AllowedSenders {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(allowed, candidate) {
				return true
			}
		}
	}
	return false
}

// commandReplyText sends a plain text reply into the chat the command came from
// (the chat, not the sender, so group commands are answered in the group) and
// persists it, mirroring handleAutoReply.
func commandReplyText(ctx context.Context, cmd *commandContext, text string) error {
	chatJID := utils.FormatJID(cmd.evt.Info.Chat.String())
	if chatJID.IsEmpty() {
		return fmt.Errorf("cannot resolve reply chat JID %q", cmd.evt.Info.Chat.String())
	}

	response, err := cmd.client.SendMessage(ctx, chatJID, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return fmt.Errorf("failed to send command reply: %w", err)
	}
	storeCommandSentMessage(ctx, cmd, response, chatJID, text)
	return nil
}

// storeCommandSentMessage records a message the command sent. Storage failures
// are logged but never fail the command — the message already went out.
func storeCommandSentMessage(ctx context.Context, cmd *commandContext, response whatsmeow.SendResponse, recipient types.JID, content string) {
	if cmd.repo == nil {
		return
	}
	senderJID := ""
	if cmd.client.Store != nil && cmd.client.Store.ID != nil {
		senderJID = cmd.client.Store.ID.String()
	}
	if err := cmd.repo.StoreSentMessageWithContext(
		ctx,
		response.ID,
		senderJID,
		recipient.String(),
		content,
		response.Timestamp,
		nil,
	); err != nil {
		log.Errorf("Command: failed to store !%s message %s in chat storage: %v", cmd.name, response.ID, err)
	}
}

// handlePingCommand answers "!ping" so an operator can confirm the device is
// connected and the command system is wired up.
func handlePingCommand(ctx context.Context, cmd *commandContext) error {
	return commandReplyText(ctx, cmd, "bot aktif")
}

// handleForwardCommand forwards the message the command replied to into every
// group registered under the "forward" key of the device's command_targets.
//
// Media is re-uploaded only for channel posts, whose references genuinely cannot
// be re-sent. An ordinary forward still goes by reference, which is what makes
// it cheap; if those references have since expired the send fails and is
// reported in the summary reply. Use POST /message/:message_id/forward with
// force_reupload to recover that case.
func handleForwardCommand(ctx context.Context, cmd *commandContext) error {
	contextInfo := utils.ExtractContextInfo(utils.UnwrapMessage(cmd.evt.Message))
	sourceID := ""
	if contextInfo != nil {
		sourceID = strings.TrimSpace(contextInfo.GetStanzaID())
	}
	if sourceID == "" {
		return commandReplyText(ctx, cmd, "!forward harus dikirim sebagai reply ke pesan yang mau diteruskan")
	}

	// Device-scoped lookup: a message id from another device must never be
	// forwarded through this one.
	message, err := cmd.repo.GetMessageByIDAndDevice(cmd.deviceID, sourceID)
	if err != nil {
		return fmt.Errorf("failed to load message %s: %w", sourceID, err)
	}
	if message == nil {
		return commandReplyText(ctx, cmd, fmt.Sprintf("pesan %s tidak ditemukan di penyimpanan device ini", sourceID))
	}
	if !utils.IsForwardableStorageMessage(message) {
		return commandReplyText(ctx, cmd, utils.ErrUnsupportedForwardType)
	}

	targets := commandForwardTargets(cmd.cfg)
	if len(targets) == 0 {
		return commandReplyText(ctx, cmd, "belum ada grup target terdaftar untuk device ini")
	}

	// A WhatsApp Channel post needs more than the stored columns: its attribution
	// lives in context chat storage does not keep, and its media is encrypted for
	// the channel. Both are recovered here — see command_forward_source.go.
	source := resolveForwardSource(cmd, contextInfo, sourceID)

	// Build the proto once: it is identical for every target.
	forwarded, err := buildForwardPayload(ctx, cmd, message, source)
	if err != nil {
		return fmt.Errorf("failed to build forward message from %s: %w", sourceID, err)
	}
	content := forwardCommandContent(message)

	// Detach the fan-out so a device with many targets cannot stall the event
	// loop. WithoutCancel keeps the device instance in context.
	go func() {
		fanoutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
		defer cancel()
		runForwardFanout(fanoutCtx, cmd, targets, forwarded, content)
	}()
	return nil
}

// buildForwardPayload turns the stored source message into the proto that is
// sent to every target, applying the device's delivery mode.
func buildForwardPayload(ctx context.Context, cmd *commandContext, message *domainChatStorage.Message, source *waE2E.ContextInfo_ForwardedNewsletterMessageInfo) (*waE2E.Message, error) {
	opts := utils.ForwardBuildOptions{}
	built, err := utils.BuildForwardMessageFromStorage(message, opts)
	if err != nil {
		return nil, err
	}

	// Channel media must be re-uploaded rather than re-referenced. Note this is
	// done up front, not as a retry: the send succeeds either way, and only the
	// recipient discovers the media will not download.
	if source != nil && utils.IsForwardMediaMessage(message) {
		reuploaded, rerr := reuploadForwardMedia(ctx, cmd.client, message, opts)
		if rerr != nil {
			// By-reference still delivers the text and caption, so send it rather
			// than nothing — but say why the media may not open.
			log.Warnf("Command: !forward could not re-upload channel media for %s, sending by reference: %v", message.ID, rerr)
		} else {
			built = reuploaded
		}
	}

	if cmd.cfg.ForwardMode == domainChatStorage.ForwardModePlain {
		// Plain mode drops every trace of forwarding, the channel card included:
		// the point of the mode is that the group cannot tell it was relayed.
		return plainDeliveryMessage(built), nil
	}

	attachNewsletterInfo(built, source)
	return built, nil
}

// plainDeliveryMessage rewrites a built forward so the recipient sees an
// ordinary message. WhatsApp draws the "Forwarded" label purely from
// ContextInfo.IsForwarded, so clearing that pair is what removes it.
//
// A text-only forward becomes a plain Conversation: its ExtendedTextMessage
// existed only to carry the forward context, and once that is gone there is
// nothing left for the richer type to hold.
func plainDeliveryMessage(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetText() != "" {
		return &waE2E.Message{Conversation: proto.String(ext.GetText())}
	}
	if ci := utils.ExtractContextInfo(msg); ci != nil {
		ci.IsForwarded = nil
		ci.ForwardingScore = nil
	}
	return msg
}

// commandForwardTargets returns the group JIDs registered for "!forward".
func commandForwardTargets(cfg *domainChatStorage.DeviceCommandConfig) []string {
	if cfg == nil || cfg.CommandTargets == nil {
		return nil
	}
	return cfg.CommandTargets["forward"]
}

// runForwardFanout sends the prepared message to each target in turn and then
// reports how many succeeded back into the originating chat.
func runForwardFanout(ctx context.Context, cmd *commandContext, targets []string, forwarded *waE2E.Message, content string) {
	succeeded := 0
	var failed []string

	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		targetJID, err := utils.ParseJID(target)
		if err != nil {
			log.Errorf("Command: !forward target %q is not a valid JID: %v", target, err)
			failed = append(failed, target)
			continue
		}
		response, err := cmd.client.SendMessage(ctx, targetJID.ToNonAD(), forwarded)
		if err != nil {
			log.Errorf("Command: !forward to %s failed: %v", target, err)
			failed = append(failed, target)
			continue
		}
		succeeded++
		storeCommandSentMessage(ctx, cmd, response, targetJID.ToNonAD(), content)
	}

	summary := fmt.Sprintf("forward: %d/%d grup berhasil", succeeded, len(targets))
	if len(failed) > 0 {
		summary += "\ngagal: " + strings.Join(failed, ", ")
	}
	if err := commandReplyText(ctx, cmd, summary); err != nil {
		log.Errorf("Command: failed to report !forward summary: %v", err)
	}
}

// forwardCommandContent is the text stored for the forwarded copy, matching the
// labels the REST forward usecase writes for media messages.
func forwardCommandContent(message *domainChatStorage.Message) string {
	if message.Content != "" {
		return message.Content
	}
	switch message.MediaType {
	case "image":
		return "🖼️ Image"
	case "video", "video_note":
		return "🎬 Video"
	case "audio", "ptt":
		return "🎵 Audio"
	case "document":
		return "📄 Document"
	case "sticker":
		return "🎨 Sticker"
	default:
		return message.Content
	}
}
