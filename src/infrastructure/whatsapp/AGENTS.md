# WHATSAPP INFRASTRUCTURE

Generated: 2026-06-06

## OVERVIEW

This package owns whatsmeow clients, multi-device lifecycle, JID normalization, events, send retry helpers,
presence pulse scheduling, webhook forwarding, and the event-side chatstorage wrapper.

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Device lifecycle | `device_manager.go`, `device_instance.go`, `client_lifecycle.go` | Create/load/purge/reconnect and persisted registry behavior. |
| Event routing | `event_handler.go`, `event_*.go` | Add new event types to the central switch. |
| Message webhooks | `event_message.go`, `webhook_forward.go` | Payload construction, media fields, signatures, event filters. |
| Inbound chat commands | `event_command_handler.go` | `!`-prefixed commands; dispatch table plus per-device config and authorization. |
| Channel forward provenance | `command_forward_source.go` | Records WhatsApp Channel attribution on arrival and rebuilds it for `!forward`. |
| Status posting | `command_status.go` | `!status` posts the replied-to message as the device's own status. |
| Chatwoot forward retry | `webhook_forward.go` | Queue-backed retries for WhatsApp-to-Chatwoot forward failures. |
| Send retry / 463 | `send_retry.go`, `key_cache.go` | Reachout timelock retry and privacy-token store wiring. |
| Presence behavior | `event_handler.go`, `event_chat_presence.go`, `presence_pulse.go` | Connect-time presence, chat presence webhooks, scheduled daily pulses. |
| History import | `history_sync.go` | Stores chats/messages for history sync batches. |
| JID conversion | `jid_utils.go`, `context_device.go` | LID normalization and request/device context. |
| Storage wrapper | `chatstorage_wrapper.go` | Injects device ID into chat/message repository operations. |

## CONVENTIONS

- `DeviceManager` keys the active registry by requested device ID or alias, but logged-in storage identity may become the WhatsApp JID.
- Chat/message table `device_id` should be the WhatsApp JID without device number: `client.Store.ID.ToNonAD().String()`.
- Event handlers call `ContextWithDevice(ctx, instance)` before downstream logic.
- Presence pulse only targets connected and logged-in devices, then returns them to unavailable after the configured duration.
- Register a new chat command in `commandRegistry` (`event_command_handler.go`); handlers take a single `*commandContext` so new dependencies never change existing signatures.
- `handleCommand` checks the `!` prefix before any storage read, so ordinary messages cost no query.
- A command runs only when the device has an enabled `device_command_config` row AND the sender is authorized: `evt.Info.IsFromMe`, or a JID in that row's `allowed_senders`.
- Command replies go to `evt.Info.Chat`, not `evt.Info.Sender`, so a command issued in a group is answered in that group.
- `!forward` delivery is per device: `ForwardModeForwarded` keeps WhatsApp's label, `ForwardModePlain` strips `ContextInfo.IsForwarded`/`ForwardingScore` (a text forward becomes a plain `Conversation`). Anything unrecognized, including the empty string from a pre-Migration-48 row, resolves to forwarded.
- `!status` has its own per-device switch (`DeviceCommandConfig.StatusEnabled`, default off) on top of the row being enabled: a status reaches every contact the account's privacy settings allow, unlike a forward into named groups.
- A status has no targets — whatsmeow resolves the audience from the account's status privacy when sending to `types.StatusBroadcastJID` — and no forward markers: `!status` reuses `plainDeliveryMessage` so no "Forwarded" label or channel card survives.
- Only text, image, video and voice note post as a status; documents, stickers and round video notes are rejected before the send instead of being posted as nothing.
- Chat storage keeps no `ContextInfo`, so WhatsApp Channel attribution is recorded on arrival by `captureForwardSource` into `command_forward_source`; without it `!forward` cannot rebuild the channel heading and View channel button.
- Channel media must be re-uploaded, not re-sent by reference: it is encrypted for the channel, and recipients elsewhere get a placeholder that never downloads. The send succeeds regardless, so this cannot be a retry-on-error path.
- Commands intentionally run in groups as well as 1:1 chats; only broadcast and `status@` contexts are excluded.
- Normalize `@lid` JIDs with `NormalizeJIDFromLID(ctx, jid, client)` before DB lookup/storage when a phone JID is needed.
- Use `ToNonAD()` when persisting or emitting stable non-device JIDs.
- Webhook forwarding uses goroutines and bounded contexts in selected handlers; keep failures logged without blocking the event loop.
- `StartChatwootForwardRetryWorker` persists retry jobs in chat storage; queued events need stable `device_id`, event name, and WhatsApp message ID.
- `chatstorage_wrapper.go` should provide device defaults for scoped repository methods, including message edit and device-scoped ID lookups.

## ANTI-PATTERNS

- Do not store `instance.ID()` as chat/message `device_id` after a real WhatsApp JID is available.
- Do not bypass `chatstorage_wrapper.go` for event-side chat/message access.
- Do not add an `IChatStorageRepository` method without implementing the wrapper method.
- Do not remove the receipt `Device == 0` filter; linked devices produce duplicate receipts.
- Do not start a second presence pulse loop; `cmd/helpers.go` uses `sync.Once` for process-wide startup.
- Do not move privacy tokens to a volatile keys DB; long-lived sessions need them in durable primary WhatsApp storage.
- Do not assume `client`, `client.Store`, or `client.Store.LIDs` is non-nil during LID resolution.
- Do not import `usecase` from a command handler; it would invert the layering. Reuse `utils.BuildForwardMessageFromStorage` and friends from `pkg/utils` instead.
- Do not reply to an unauthorized sender, not even with an error: silence keeps the bot from being probed or used as a send oracle.
- Do not copy `auto_reply.go`'s 1:1-only filters into command handling; `!forward` is meant to be triggered from inside a group.
- Do not grow `handleCommand` into an if-else chain over command names.
- Do not turn `!status` on from an omitted API field, and do not store the posted status in chat storage: `status@broadcast` is ignored on the way in, so a row would surface as a phantom "Status" chat.
- Do not default an unknown forward mode to plain; silently dropping the Forwarded label changes what recipients are told about a message's origin.
- Do not rely on the live quoted message alone for channel attribution; clients trim the quoted copy, which is why the recorded row exists as the fallback.
- Do not replay Chatwoot forward events without device scoping; the retry queue uniqueness depends on it.

## TESTING

- Tests in this package often exercise unexported helpers directly from package `whatsapp`.
- Webhook tests replace package-level functions/globals and restore them with `defer`; preserve cleanup on new tests.
- Presence pulse tests use fake clients, injected clocks/sleeps, channels, and timeouts; avoid `t.Parallel` around shared scheduler state.
