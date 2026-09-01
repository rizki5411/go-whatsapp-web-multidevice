package whatsapp

import (
	"context"
	"fmt"
	"strings"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// !status posts the message a command replies to as this device's own WhatsApp
// status (story).
//
// It reuses the !forward machinery to rebuild the quoted message from chat
// storage, but differs from it in three ways:
//
//  1. It has its own on/off switch (DeviceCommandConfig.StatusEnabled) and is
//     off until an operator turns it on at /custom/command. A status goes to
//     every contact the device's privacy settings allow, which is a much wider
//     blast radius than a forward into named groups.
//  2. It has no targets: WhatsApp resolves the audience itself from the account's
//     status privacy settings (whatsmeow reads them when sending to
//     status@broadcast), so there is nothing per-device to configure.
//  3. Fewer message types work as a status: documents and stickers have no
//     status representation, so they are rejected up front instead of being sent
//     and silently dropped.

// statusPostableMediaTypes are the stored media types WhatsApp shows as a
// status. Documents, stickers and round video notes are deliberately absent.
var statusPostableMediaTypes = map[string]struct{}{
	"image": {},
	"video": {},
	"audio": {},
	"ptt":   {},
}

// Colors for a text status. Official clients always send a background with a
// text status, so one is chosen here rather than leaving the fields unset and
// letting each receiving client decide how to render it.
const (
	statusTextBackgroundARGB uint32 = 0xFF1F7A6D
	statusTextForegroundARGB uint32 = 0xFFFFFFFF
)

// handleStatusCommand posts the replied-to message as the device's status.
func handleStatusCommand(ctx context.Context, cmd *commandContext) error {
	if !cmd.cfg.StatusEnabled {
		// The sender is already authorized, so saying why nothing happened costs
		// no secrecy and saves an operator from debugging a silent command.
		return commandReplyText(ctx, cmd, "!status belum diaktifkan untuk device ini — nyalakan dulu di halaman /custom/command")
	}

	contextInfo := utils.ExtractContextInfo(utils.UnwrapMessage(cmd.evt.Message))
	sourceID := ""
	if contextInfo != nil {
		sourceID = strings.TrimSpace(contextInfo.GetStanzaID())
	}
	if sourceID == "" {
		return commandReplyText(ctx, cmd, "!status harus dikirim sebagai reply ke pesan yang mau dijadikan status")
	}

	// Device-scoped, same as !forward: a message id from another device must
	// never be posted through this one.
	message, err := cmd.repo.GetMessageByIDAndDevice(cmd.deviceID, sourceID)
	if err != nil {
		return fmt.Errorf("failed to load message %s: %w", sourceID, err)
	}
	if message == nil {
		return commandReplyText(ctx, cmd, fmt.Sprintf("pesan %s tidak ditemukan di penyimpanan device ini", sourceID))
	}
	if !isStatusPostableMessage(message) {
		return commandReplyText(ctx, cmd, "tipe pesan ini tidak bisa dijadikan status — hanya teks, foto, video, dan voice note")
	}

	// A channel post's media is encrypted for that channel, so it has to be
	// re-uploaded before anyone else can open it — see command_forward_source.go.
	source := resolveForwardSource(cmd, contextInfo, sourceID)

	payload, err := buildStatusPayload(ctx, cmd, message, source)
	if err != nil {
		return fmt.Errorf("failed to build status message from %s: %w", sourceID, err)
	}

	// Detach the send: posting a status encrypts to every recipient WhatsApp
	// resolves from the account's privacy settings, which on a large contact list
	// takes long enough to stall the event loop. WithoutCancel keeps the device
	// instance in context.
	go func() {
		postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
		defer cancel()
		runStatusPost(postCtx, cmd, payload)
	}()
	return nil
}

// isStatusPostableMessage reports whether a stored row can be posted as a status.
func isStatusPostableMessage(message *domainChatStorage.Message) bool {
	if message == nil || !utils.IsForwardableStorageMessage(message) {
		return false
	}
	if message.MediaType == "" {
		return true
	}
	_, ok := statusPostableMediaTypes[message.MediaType]
	return ok
}

// buildStatusPayload turns the stored source message into the proto posted to
// status@broadcast.
func buildStatusPayload(ctx context.Context, cmd *commandContext, message *domainChatStorage.Message, source *waE2E.ContextInfo_ForwardedNewsletterMessageInfo) (*waE2E.Message, error) {
	if message.MediaType == "" {
		return buildStatusTextMessage(message.Content), nil
	}

	opts := utils.ForwardBuildOptions{}
	built, err := utils.BuildForwardMessageFromStorage(message, opts)
	if err != nil {
		return nil, err
	}

	if source != nil {
		reuploaded, rerr := reuploadForwardMedia(ctx, cmd.client, message, opts)
		if rerr != nil {
			// Posting by reference still delivers the caption, so post it rather
			// than nothing — but say why the media may not open.
			log.Warnf("Command: !status could not re-upload channel media for %s, posting by reference: %v", message.ID, rerr)
		} else {
			built = reuploaded
		}
	}

	// A status is never "forwarded" and carries no channel card: it is shown as
	// this account's own post, so every trace of the source chat is stripped.
	return plainDeliveryMessage(built), nil
}

// buildStatusTextMessage builds a text status. Unlike the text branch of the
// forward builder it carries status colors instead of forward context.
func buildStatusTextMessage(text string) *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:           proto.String(text),
			BackgroundArgb: proto.Uint32(statusTextBackgroundARGB),
			TextArgb:       proto.Uint32(statusTextForegroundARGB),
			Font:           waE2E.ExtendedTextMessage_SYSTEM.Enum(),
		},
	}
}

// runStatusPost sends the prepared message to status@broadcast and reports the
// result back into the chat the command came from.
//
// The posted status is deliberately not written to chat storage: status@broadcast
// is a system JID the storage and Chatwoot paths ignore on the way in, and a row
// for it would surface as a phantom "Status" chat in the dashboard.
func runStatusPost(ctx context.Context, cmd *commandContext, payload *waE2E.Message) {
	response, err := cmd.client.SendMessage(ctx, types.StatusBroadcastJID, payload)
	if err != nil {
		log.Errorf("Command: !status failed for device %s: %v", cmd.cfg.DeviceID, err)
		if replyErr := commandReplyText(ctx, cmd, "status gagal diposting: "+err.Error()); replyErr != nil {
			log.Errorf("Command: failed to report !status failure: %v", replyErr)
		}
		return
	}

	log.Infof("Command: !status posted %s for device %s", response.ID, cmd.cfg.DeviceID)
	if err := commandReplyText(ctx, cmd, "status berhasil diposting"); err != nil {
		log.Errorf("Command: failed to report !status result: %v", err)
	}
}
