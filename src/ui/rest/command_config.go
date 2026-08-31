package rest

import (
	"fmt"
	"sort"
	"strings"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Per-device configuration for the inbound "!" chat command system. Mirrors
// chatwoot_config.go: path-param device resolution, tri-state enabled, upsert on
// PUT, and the same response envelope.
//
// Unlike Chatwoot there is no in-memory registry to invalidate — the event side
// reads the row directly, once per "!"-prefixed message, so there is no cache to
// go stale.

// CommandConfigHandler serves the per-device command configuration routes.
type CommandConfigHandler struct {
	DeviceManager   *whatsapp.DeviceManager
	ChatStorageRepo domainChatStorage.IChatStorageRepository
}

// InitRestCommandConfig registers the command config routes. They take the
// device as a path param and resolve it manually, so they must be registered
// outside DeviceMiddleware (which only reads header/query).
func InitRestCommandConfig(app fiber.Router, dm *whatsapp.DeviceManager, chatStorageRepo domainChatStorage.IChatStorageRepository) *CommandConfigHandler {
	h := &CommandConfigHandler{DeviceManager: dm, ChatStorageRepo: chatStorageRepo}

	// Operator console for this feature. The main dashboard (gowa-ui) is a
	// separate project, so its released HTML knows nothing about these routes.
	app.Get("/command/ui", h.CommandUI)

	app.Get("/command/commands", h.ListCommands)
	app.Get("/command/configs", h.ListCommandConfigs)
	app.Get("/devices/:device_id/command/config", h.GetCommandConfig)
	app.Put("/devices/:device_id/command/config", h.UpsertCommandConfig)
	app.Delete("/devices/:device_id/command/config", h.DeleteCommandConfig)

	return h
}

// commandConfigRequest is the PUT body. enabled is a pointer so an omitted field
// keeps the stored value (and defaults to true on create) instead of silently
// disabling the device.
type commandConfigRequest struct {
	Enabled *bool `json:"enabled"`
	// ForwardMode is optional; omitted keeps the stored mode, and a new config
	// defaults to the labelled forward.
	ForwardMode    string              `json:"forward_mode"`
	CommandTargets map[string][]string `json:"command_targets"`
	AllowedSenders []string            `json:"allowed_senders"`
}

func commandConfigView(cfg *domainChatStorage.DeviceCommandConfig) map[string]any {
	targets := cfg.CommandTargets
	if targets == nil {
		targets = map[string][]string{}
	}
	senders := cfg.AllowedSenders
	if senders == nil {
		senders = []string{}
	}
	return map[string]any{
		"device_id":          cfg.DeviceID,
		"device_jid":         cfg.DeviceJID,
		"enabled":            cfg.Enabled,
		"forward_mode":       cfg.ForwardMode,
		"command_targets":    targets,
		"allowed_senders":    senders,
		"available_commands": whatsapp.RegisteredCommands(),
		"forward_modes":      []string{domainChatStorage.ForwardModeForwarded, domainChatStorage.ForwardModePlain},
		"created_at":         cfg.CreatedAt,
		"updated_at":         cfg.UpdatedAt,
	}
}

// ListCommandConfigs returns every device's command config.
// GET /command/configs
func (h *CommandConfigHandler) ListCommandConfigs(c fiber.Ctx) error {
	if h.ChatStorageRepo == nil {
		return utils.ResponseError(c, "storage not available")
	}
	configs, err := h.ChatStorageRepo.ListDeviceCommandConfigs()
	if err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to list configs: %v", err))
	}
	views := make([]map[string]any, 0, len(configs))
	for _, cfg := range configs {
		views = append(views, commandConfigView(cfg))
	}
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Device command configs", Results: views})
}

// ListCommands returns the commands this build understands. It is independent
// of any device config, so the console can show what is available before the
// first config is ever saved.
// GET /command/commands
func (h *CommandConfigHandler) ListCommands(c fiber.Ctx) error {
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Available chat commands", Results: whatsapp.RegisteredCommands()})
}

// GetCommandConfig returns one device's command config.
// GET /devices/:device_id/command/config
func (h *CommandConfigHandler) GetCommandConfig(c fiber.Ctx) error {
	if h.ChatStorageRepo == nil {
		return utils.ResponseError(c, "storage not available")
	}
	deviceID, ok := h.resolveConfigDeviceID(c)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "DEVICE_NOT_FOUND", Message: "device not found"})
	}
	cfg, err := h.ChatStorageRepo.GetDeviceCommandConfig(deviceID)
	if err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to load config: %v", err))
	}
	if cfg == nil {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "CONFIG_NOT_FOUND", Message: "no command config for this device"})
	}
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Device command config", Results: commandConfigView(cfg)})
}

// UpsertCommandConfig creates or updates a device's command config.
// PUT /devices/:device_id/command/config
func (h *CommandConfigHandler) UpsertCommandConfig(c fiber.Ctx) error {
	if h.ChatStorageRepo == nil {
		return utils.ResponseError(c, "storage not available")
	}
	deviceID, ok := h.resolveConfigDeviceID(c)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{Status: fiber.StatusNotFound, Code: "DEVICE_NOT_FOUND", Message: "device not found"})
	}

	var req commandConfigRequest
	if err := c.Bind().Body(&req); err != nil {
		return utils.ResponseError(c, "Invalid request body")
	}

	targets, errCode, errMsg := normalizeCommandTargets(req.CommandTargets)
	if errCode != "" {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{Status: fiber.StatusBadRequest, Code: errCode, Message: errMsg})
	}

	senders, errCode, errMsg := normalizeAllowedSenders(req.AllowedSenders)
	if errCode != "" {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{Status: fiber.StatusBadRequest, Code: errCode, Message: errMsg})
	}

	existing, err := h.ChatStorageRepo.GetDeviceCommandConfig(deviceID)
	if err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to load existing config: %v", err))
	}

	// Reject an unrecognized mode instead of silently falling back: an operator
	// who meant "plain" and mistyped it would otherwise keep sending labelled
	// forwards and have no way to tell from the response.
	forwardMode := strings.TrimSpace(req.ForwardMode)
	switch forwardMode {
	case "":
		forwardMode = domainChatStorage.ForwardModeForwarded
		if existing != nil && existing.ForwardMode != "" {
			forwardMode = existing.ForwardMode
		}
	case domainChatStorage.ForwardModeForwarded, domainChatStorage.ForwardModePlain:
	default:
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  fiber.StatusBadRequest,
			Code:    "INVALID_FORWARD_MODE",
			Message: fmt.Sprintf("forward_mode must be %q or %q", domainChatStorage.ForwardModeForwarded, domainChatStorage.ForwardModePlain),
		})
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}

	cfg := &domainChatStorage.DeviceCommandConfig{
		DeviceID:       deviceID,
		DeviceJID:      h.deviceJID(deviceID),
		Enabled:        enabled,
		ForwardMode:    forwardMode,
		CommandTargets: targets,
		AllowedSenders: senders,
	}
	if existing != nil {
		cfg.ID = existing.ID
		cfg.CreatedAt = existing.CreatedAt
	}

	if err := h.ChatStorageRepo.SaveDeviceCommandConfig(cfg); err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to save config: %v", err))
	}

	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Device command config saved", Results: commandConfigView(cfg)})
}

// DeleteCommandConfig removes a device's command config, disabling the command
// system for that device.
// DELETE /devices/:device_id/command/config
func (h *CommandConfigHandler) DeleteCommandConfig(c fiber.Ctx) error {
	if h.ChatStorageRepo == nil {
		return utils.ResponseError(c, "storage not available")
	}
	// Resolve the same way GET and PUT do, but fall back to the raw param so a
	// config orphaned by device removal stays deletable.
	deviceID, ok := h.resolveConfigDeviceID(c)
	if !ok {
		deviceID = strings.Clone(strings.TrimSpace(c.Params("device_id")))
	}
	if deviceID == "" {
		return utils.ResponseError(c, "device_id is required")
	}
	if err := h.ChatStorageRepo.DeleteDeviceCommandConfig(deviceID); err != nil {
		return utils.ResponseError(c, fmt.Sprintf("failed to delete config: %v", err))
	}
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Device command config deleted", Results: map[string]any{"device_id": deviceID}})
}

// normalizeCommandTargets validates and canonicalizes the command -> target JID
// map. Unknown command names are rejected rather than stored: a typo would
// otherwise be accepted and silently never fire. Targets must be group JIDs,
// are reduced to their non-AD form, and are deduplicated; a command left with no
// targets is dropped from the map entirely.
func normalizeCommandTargets(raw map[string][]string) (map[string][]string, string, string) {
	normalized := map[string][]string{}
	if len(raw) == 0 {
		return normalized, "", ""
	}

	known := make(map[string]struct{}, len(whatsapp.RegisteredCommands()))
	for _, name := range whatsapp.RegisteredCommands() {
		known[name] = struct{}{}
	}

	// Iterate deterministically so the rejected command is stable across calls.
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, rawName := range names {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			continue
		}
		if _, ok := known[name]; !ok {
			return nil, "UNKNOWN_COMMAND", fmt.Sprintf("unknown command %q; known commands: %s", rawName, strings.Join(whatsapp.RegisteredCommands(), ", "))
		}

		seen := map[string]struct{}{}
		targets := make([]string, 0, len(raw[rawName]))
		for _, rawJID := range raw[rawName] {
			candidate := strings.TrimSpace(rawJID)
			if candidate == "" {
				continue
			}
			parsed, err := utils.ParseJID(candidate)
			if err != nil {
				return nil, "INVALID_TARGET_JID", fmt.Sprintf("target %q for command %q is not a valid JID: %v", rawJID, name, err)
			}
			jid := parsed.ToNonAD().String()
			if !utils.IsGroupJID(jid) {
				return nil, "INVALID_TARGET_JID", fmt.Sprintf("target %q for command %q must be a group JID (@g.us)", rawJID, name)
			}
			if _, dup := seen[jid]; dup {
				continue
			}
			seen[jid] = struct{}{}
			targets = append(targets, jid)
		}
		if len(targets) == 0 {
			continue
		}
		normalized[name] = targets
	}
	return normalized, "", ""
}

// normalizeAllowedSenders validates the extra-sender whitelist. Entries are
// reduced to their non-AD form so they match the sender JID the event handler
// compares against, and group JIDs are rejected: a group is never a sender.
func normalizeAllowedSenders(raw []string) ([]string, string, string) {
	senders := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, rawJID := range raw {
		candidate := strings.TrimSpace(rawJID)
		if candidate == "" {
			continue
		}
		parsed, err := utils.ParseJID(candidate)
		if err != nil {
			return nil, "INVALID_SENDER_JID", fmt.Sprintf("allowed sender %q is not a valid JID: %v", rawJID, err)
		}
		jid := parsed.ToNonAD().String()
		if utils.IsGroupJID(jid) {
			return nil, "INVALID_SENDER_JID", fmt.Sprintf("allowed sender %q must not be a group JID", rawJID)
		}
		if _, dup := seen[jid]; dup {
			continue
		}
		seen[jid] = struct{}{}
		senders = append(senders, jid)
	}
	return senders, "", ""
}

// resolveConfigDeviceID resolves the :device_id path param to a known device id
// (DeviceMiddleware reads only header/query, so config routes resolve manually).
//
// The result is cloned: when ResolveDevice matches by exact id it returns the
// param-derived string itself, whose backing buffer fasthttp recycles after the
// request. This id is persisted, so an uncopied value would mutate under the
// next request.
func (h *CommandConfigHandler) resolveConfigDeviceID(c fiber.Ctx) (string, bool) {
	deviceID := strings.TrimSpace(c.Params("device_id"))
	if deviceID == "" || h.DeviceManager == nil {
		return "", false
	}
	_, resolvedID, err := h.DeviceManager.ResolveDevice(deviceID)
	if err != nil {
		return "", false
	}
	return strings.Clone(resolvedID), true
}

// deviceJID returns the WhatsApp storage JID for a device so the event side can
// resolve the config by either identity. Empty before login.
func (h *CommandConfigHandler) deviceJID(deviceID string) string {
	if h.DeviceManager == nil {
		return ""
	}
	instance, _, err := h.DeviceManager.ResolveDevice(deviceID)
	if err != nil || instance == nil {
		return ""
	}
	return instance.JID()
}
