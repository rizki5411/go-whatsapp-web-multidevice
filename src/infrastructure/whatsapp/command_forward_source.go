package whatsapp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// WhatsApp Channel (newsletter) provenance for !forward.
//
// Two things are lost when a channel post is saved to chat storage and later
// rebuilt for a forward:
//
//  1. Attribution. The channel name heading and the "View channel" button come
//     from ContextInfo.ForwardedNewsletterMessageInfo, and chat storage keeps no
//     ContextInfo at all — so it has to be recorded separately, on arrival.
//  2. Media. A channel's media is encrypted for that channel. Re-sending the
//     stored reference to a group produces a message the group cannot decrypt:
//     it arrives as a tap-to-download placeholder that never downloads. The send
//     itself succeeds, so this cannot be handled as a retry-after-failure — the
//     media has to be re-uploaded up front.

// captureForwardSource records the channel a message came from, before chat
// storage drops the context that says so. It is a no-op for ordinary messages,
// which is nearly all of them.
func captureForwardSource(ctx context.Context, evt *events.Message, chatStorageRepo domainChatStorage.IChatStorageRepository, client *whatsmeow.Client) {
	if evt == nil || chatStorageRepo == nil {
		return
	}
	info := newsletterInfoFromMessage(evt.Message)
	if info == nil {
		return
	}
	newsletterJID := strings.TrimSpace(info.GetNewsletterJID())
	if newsletterJID == "" {
		return
	}

	src := &domainChatStorage.MessageForwardSource{
		DeviceID:        commandDeviceID(ctx, client),
		ChatJID:         evt.Info.Chat.String(),
		MessageID:       evt.Info.ID,
		NewsletterJID:   newsletterJID,
		NewsletterName:  info.GetNewsletterName(),
		ServerMessageID: int(info.GetServerMessageID()),
	}
	if err := chatStorageRepo.SaveMessageForwardSource(src); err != nil {
		// Losing this only costs the channel card on a later forward, so it must
		// never interrupt message handling.
		log.Warnf("Command: failed to record channel attribution for %s: %v", evt.Info.ID, err)
	}
}

// newsletterInfoFromMessage returns the channel attribution a message carries,
// or nil when it carries none.
func newsletterInfoFromMessage(msg *waE2E.Message) *waE2E.ContextInfo_ForwardedNewsletterMessageInfo {
	ci := utils.ExtractContextInfo(utils.UnwrapMessage(msg))
	if ci == nil {
		return nil
	}
	return ci.GetForwardedNewsletterMessageInfo()
}

// resolveForwardSource finds the channel the message being forwarded came from.
//
// The live reply is tried first: when the replying client included the quoted
// message's own context, that copy is exact and includes the server message id.
// Clients routinely trim the quoted copy though, so the recorded row captured on
// arrival is the dependable fallback.
func resolveForwardSource(cmd *commandContext, contextInfo *waE2E.ContextInfo, sourceID string) *waE2E.ContextInfo_ForwardedNewsletterMessageInfo {
	if contextInfo != nil {
		if info := newsletterInfoFromMessage(contextInfo.GetQuotedMessage()); info != nil && info.GetNewsletterJID() != "" {
			return info
		}
	}

	stored, err := cmd.repo.GetMessageForwardSource(cmd.deviceID, cmd.evt.Info.Chat.String(), sourceID)
	if err != nil {
		log.Warnf("Command: failed to read channel attribution for %s: %v", sourceID, err)
		return nil
	}
	if stored == nil || stored.NewsletterJID == "" {
		return nil
	}

	info := &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
		NewsletterJID: proto.String(stored.NewsletterJID),
	}
	if stored.NewsletterName != "" {
		info.NewsletterName = proto.String(stored.NewsletterName)
	}
	// Omit rather than send zero: a zero id addresses no post, and the button is
	// better off opening the channel than a message that does not exist.
	if stored.ServerMessageID != 0 {
		info.ServerMessageID = proto.Int32(int32(stored.ServerMessageID))
	}
	return info
}

// attachNewsletterInfo puts the channel card back on a rebuilt forward. The
// builder already created the ContextInfo this hangs off.
func attachNewsletterInfo(msg *waE2E.Message, info *waE2E.ContextInfo_ForwardedNewsletterMessageInfo) {
	if msg == nil || info == nil {
		return
	}
	if ci := utils.ExtractContextInfo(msg); ci != nil {
		ci.ForwardedNewsletterMessageInfo = info
	}
}

// reuploadForwardMedia downloads the source media and uploads it again under
// this device's own keys, so recipients outside the channel can decrypt it.
func reuploadForwardMedia(ctx context.Context, client *whatsmeow.Client, message *domainChatStorage.Message, opts utils.ForwardBuildOptions) (*waE2E.Message, error) {
	downloadable, err := utils.BuildDownloadableMessage(
		message.MediaType,
		message.URL,
		utils.ResolveMediaDirectPath(message.DirectPath, message.URL),
		message.Filename,
		message.MediaKey,
		message.FileSHA256,
		message.FileEncSHA256,
		message.FileLength,
	)
	if err != nil {
		return nil, err
	}

	extracted, err := utils.ExtractMedia(ctx, client, config.PathSendItems, downloadable)
	if err != nil {
		return nil, fmt.Errorf("failed to download media for re-upload: %w", err)
	}
	defer os.Remove(extracted.MediaPath)

	data, err := os.ReadFile(extracted.MediaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read downloaded media: %w", err)
	}

	mediaType, err := forwardMediaType(message.MediaType)
	if err != nil {
		return nil, err
	}

	uploaded, err := client.Upload(ctx, data, mediaType)
	if err != nil {
		return nil, fmt.Errorf("failed to re-upload media: %w", err)
	}

	opts.Upload = &uploaded
	opts.MimeType = extracted.MimeType
	return utils.BuildForwardMessageFromStorage(message, opts)
}

// forwardMediaType maps a stored media type onto the whatsmeow upload type.
func forwardMediaType(mediaType string) (whatsmeow.MediaType, error) {
	switch mediaType {
	case "image", "sticker":
		return whatsmeow.MediaImage, nil
	case "video", "video_note":
		return whatsmeow.MediaVideo, nil
	case "audio", "ptt":
		return whatsmeow.MediaAudio, nil
	case "document":
		return whatsmeow.MediaDocument, nil
	default:
		return "", fmt.Errorf("unsupported media type: %s", mediaType)
	}
}
