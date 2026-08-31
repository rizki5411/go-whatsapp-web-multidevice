package chatstorage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Per-device inbound command configuration storage. Mirrors the Chatwoot device
// config methods in sqlite_repository.go, kept in its own file so the new
// feature does not grow that already large file.

const deviceCommandConfigColumns = `id, device_id, device_jid, enabled, forward_mode, command_targets, allowed_senders, created_at, updated_at`

// scanDeviceCommandConfig decodes one row, including the two JSON columns, and
// is shared by the QueryRow and rows paths.
func (r *SQLiteRepository) scanDeviceCommandConfig(scanner interface{ Scan(...any) error }) (*domainChatStorage.DeviceCommandConfig, error) {
	cfg := &domainChatStorage.DeviceCommandConfig{}
	var targetsJSON, sendersJSON string
	err := scanner.Scan(
		&cfg.ID, &cfg.DeviceID, &cfg.DeviceJID, &cfg.Enabled, &cfg.ForwardMode,
		&targetsJSON, &sendersJSON, &cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(targetsJSON) != "" {
		if err := json.Unmarshal([]byte(targetsJSON), &cfg.CommandTargets); err != nil {
			return nil, fmt.Errorf("failed to decode command targets for %s: %w", cfg.DeviceID, err)
		}
	}
	if strings.TrimSpace(sendersJSON) != "" {
		if err := json.Unmarshal([]byte(sendersJSON), &cfg.AllowedSenders); err != nil {
			return nil, fmt.Errorf("failed to decode allowed senders for %s: %w", cfg.DeviceID, err)
		}
	}
	if cfg.CommandTargets == nil {
		cfg.CommandTargets = map[string][]string{}
	}
	if cfg.AllowedSenders == nil {
		cfg.AllowedSenders = []string{}
	}
	cfg.ForwardMode = normalizeForwardMode(cfg.ForwardMode)
	return cfg, nil
}

// normalizeForwardMode falls back to the labelled forward for anything it does
// not recognize, including the empty string a pre-Migration-48 row scans as.
// Defaulting the other way would silently strip the Forwarded label.
func normalizeForwardMode(mode string) string {
	if mode == domainChatStorage.ForwardModePlain {
		return domainChatStorage.ForwardModePlain
	}
	return domainChatStorage.ForwardModeForwarded
}

// encodeDeviceCommandConfig marshals the JSON columns. Nil map/slice are
// normalized to empty JSON containers so the stored value always parses back
// into a usable (non-nil) config.
func encodeDeviceCommandConfig(cfg *domainChatStorage.DeviceCommandConfig) (targetsJSON, sendersJSON string, err error) {
	targets := cfg.CommandTargets
	if targets == nil {
		targets = map[string][]string{}
	}
	senders := cfg.AllowedSenders
	if senders == nil {
		senders = []string{}
	}
	encodedTargets, err := json.Marshal(targets)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal command targets: %w", err)
	}
	encodedSenders, err := json.Marshal(senders)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal allowed senders: %w", err)
	}
	return string(encodedTargets), string(encodedSenders), nil
}

// SaveDeviceCommandConfig upserts a per-device command configuration keyed by
// device_id and populates cfg.ID. UPDATE-then-INSERT rather than ON CONFLICT so
// the statement stays portable across SQLite, MySQL, and PostgreSQL.
func (r *SQLiteRepository) SaveDeviceCommandConfig(cfg *domainChatStorage.DeviceCommandConfig) error {
	if cfg == nil || strings.TrimSpace(cfg.DeviceID) == "" {
		return fmt.Errorf("device command config requires a device id")
	}

	targetsJSON, sendersJSON, err := encodeDeviceCommandConfig(cfg)
	if err != nil {
		return err
	}

	cfg.ForwardMode = normalizeForwardMode(cfg.ForwardMode)

	now := time.Now()
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = now
	}
	cfg.UpdatedAt = now

	result, err := r.db.Exec(`
		UPDATE device_command_config
		SET device_jid = ?, enabled = ?, forward_mode = ?, command_targets = ?, allowed_senders = ?, updated_at = ?
		WHERE device_id = ?
	`, cfg.DeviceJID, cfg.Enabled, cfg.ForwardMode, targetsJSON, sendersJSON, cfg.UpdatedAt, cfg.DeviceID)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		res, err := r.db.Exec(`
			INSERT INTO device_command_config (
				device_id, device_jid, enabled, forward_mode, command_targets, allowed_senders, created_at, updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, cfg.DeviceID, cfg.DeviceJID, cfg.Enabled, cfg.ForwardMode, targetsJSON, sendersJSON, cfg.CreatedAt, cfg.UpdatedAt)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to load new device command config id: %w", err)
		}
		cfg.ID = id
		return nil
	}

	// Updated an existing row — load its id so callers get a complete config back.
	if cfg.ID == 0 {
		if err := r.db.QueryRow("SELECT id FROM device_command_config WHERE device_id = ?", cfg.DeviceID).Scan(&cfg.ID); err != nil {
			return fmt.Errorf("failed to reload device command config id: %w", err)
		}
	}
	return nil
}

// GetDeviceCommandConfig returns the config for a user-facing device id, or
// (nil, nil) when the device has none — the "nil means absent" contract the
// event and REST handlers rely on.
func (r *SQLiteRepository) GetDeviceCommandConfig(deviceID string) (*domainChatStorage.DeviceCommandConfig, error) {
	cfg, err := r.scanDeviceCommandConfig(r.db.QueryRow(
		"SELECT "+deviceCommandConfigColumns+" FROM device_command_config WHERE device_id = ? LIMIT 1", deviceID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return cfg, err
}

// GetDeviceCommandConfigByIdentifier resolves a config from either the
// user-facing device id or the WhatsApp storage JID, because the event side may
// hold either identity depending on how the device instance was created.
//
// The two keys are resolved separately: device ids are arbitrary user-supplied
// strings, so one row's device_id can collide with another row's device_jid
// (each column is only unique on its own). A single OR query with LIMIT 1 would
// pick a query-plan-dependent winner and run commands against the wrong
// device's targets — the collision is surfaced as an error instead.
func (r *SQLiteRepository) GetDeviceCommandConfigByIdentifier(identifier string) (*domainChatStorage.DeviceCommandConfig, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, nil
	}

	byID, err := r.scanDeviceCommandConfig(r.db.QueryRow(
		"SELECT "+deviceCommandConfigColumns+" FROM device_command_config WHERE device_id = ? LIMIT 1", identifier))
	if err == sql.ErrNoRows {
		byID = nil
	} else if err != nil {
		return nil, err
	}

	byJID, err := r.scanDeviceCommandConfig(r.db.QueryRow(
		"SELECT "+deviceCommandConfigColumns+" FROM device_command_config WHERE device_jid <> '' AND device_jid = ? LIMIT 1", identifier))
	if err == sql.ErrNoRows {
		byJID = nil
	} else if err != nil {
		return nil, err
	}

	if byID != nil && byJID != nil && byID.ID != byJID.ID {
		return nil, fmt.Errorf("device command config identifier %q is ambiguous: matches device_id of %q and device_jid of %q; rename one device", identifier, byID.DeviceID, byJID.DeviceID)
	}
	if byID != nil {
		return byID, nil
	}
	return byJID, nil
}

func (r *SQLiteRepository) ListDeviceCommandConfigs() ([]*domainChatStorage.DeviceCommandConfig, error) {
	rows, err := r.db.Query("SELECT " + deviceCommandConfigColumns + " FROM device_command_config ORDER BY device_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []*domainChatStorage.DeviceCommandConfig
	for rows.Next() {
		cfg, err := r.scanDeviceCommandConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

func (r *SQLiteRepository) DeleteDeviceCommandConfig(deviceID string) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	_, err := r.db.Exec("DELETE FROM device_command_config WHERE device_id = ?", deviceID)
	return err
}

// SaveMessageForwardSource records where a received message originally came
// from. Writes are idempotent: the same message can be re-delivered (history
// sync, retries) and must not fail or duplicate.
func (r *SQLiteRepository) SaveMessageForwardSource(src *domainChatStorage.MessageForwardSource) error {
	if src == nil {
		return fmt.Errorf("forward source is required")
	}
	if strings.TrimSpace(src.ChatJID) == "" || strings.TrimSpace(src.MessageID) == "" {
		return fmt.Errorf("forward source requires a chat jid and message id")
	}

	if src.CreatedAt.IsZero() {
		src.CreatedAt = time.Now()
	}

	result, err := r.db.Exec(`
		UPDATE command_forward_source
		SET newsletter_jid = ?, newsletter_name = ?, server_message_id = ?
		WHERE device_id = ? AND chat_jid = ? AND message_id = ?
	`, src.NewsletterJID, src.NewsletterName, src.ServerMessageID,
		src.DeviceID, src.ChatJID, src.MessageID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		return nil
	}

	_, err = r.db.Exec(`
		INSERT INTO command_forward_source (
			device_id, chat_jid, message_id, newsletter_jid, newsletter_name, server_message_id, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, src.DeviceID, src.ChatJID, src.MessageID, src.NewsletterJID,
		src.NewsletterName, src.ServerMessageID, src.CreatedAt)
	return err
}

// GetMessageForwardSource returns the recorded origin of a message, or
// (nil, nil) when none was recorded — the normal case for ordinary messages.
func (r *SQLiteRepository) GetMessageForwardSource(deviceID, chatJID, messageID string) (*domainChatStorage.MessageForwardSource, error) {
	if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(messageID) == "" {
		return nil, nil
	}
	src := &domainChatStorage.MessageForwardSource{}
	err := r.db.QueryRow(`
		SELECT device_id, chat_jid, message_id, newsletter_jid, newsletter_name, server_message_id, created_at
		FROM command_forward_source
		WHERE device_id = ? AND chat_jid = ? AND message_id = ?
		LIMIT 1
	`, deviceID, chatJID, messageID).Scan(
		&src.DeviceID, &src.ChatJID, &src.MessageID, &src.NewsletterJID,
		&src.NewsletterName, &src.ServerMessageID, &src.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return src, nil
}
