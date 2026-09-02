package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
)

// Penyimpanan untuk mode multi-tenant: akun aplikasi, kepemilikan device slot,
// dan session login. Ditaruh di file sendiri supaya sqlite_repository.go tidak
// tumbuh lagi.
//
// *SQLiteRepository memenuhi kontrak tenancy secara implisit; kontrak itu bukan
// bagian dari IChatStorageRepository, jadi tidak ada stub delegasi yang perlu
// ditambahkan ke infrastructure/whatsapp/chatstorage_wrapper.go.
var _ domainTenancy.ITenancyRepository = (*SQLiteRepository)(nil)

// NewTenancyRepository mengekspos kontrak tenancy di atas *sql.DB yang sama
// dengan chat storage, supaya wiring tidak perlu type assertion. Mengikuti
// NewMessageQueueRepository.
func NewTenancyRepository(db *sql.DB) domainTenancy.ITenancyRepository {
	return &SQLiteRepository{db: db}
}

const appUserColumns = `id, username, password_hash, display_name, role, device_limit, active, created_at, updated_at`

// isUniqueViolation melaporkan apakah err berasal dari pelanggaran unique
// index.
//
// Project ini bisa dibangun dengan dua driver SQLite (mattn/go-sqlite3 lewat
// cgo, atau modernc.org/sqlite lewat tag purego), dan pesan errornya berbeda.
// Mencocokkan substring yang dipakai keduanya lebih murah daripada mengimpor
// tipe error driver, yang akan mengikat file ini ke satu build tag.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "constraint failed: unique")
}

// scanAppUser mendekode satu baris app_user dan dipakai bersama oleh jalur
// QueryRow dan rows.
func (r *SQLiteRepository) scanAppUser(scanner interface{ Scan(...any) error }) (*domainTenancy.User, error) {
	user := &domainTenancy.User{}
	var role string
	err := scanner.Scan(
		&user.ID, &user.Username, &user.PasswordHash, &user.DisplayName,
		&role, &user.DeviceLimit, &user.Active, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	user.Role = domainTenancy.Role(role)
	return user, nil
}

// _____________________________________________________________________________
// Akun aplikasi

func (r *SQLiteRepository) CreateUser(user *domainTenancy.User) (int64, error) {
	if user == nil {
		return 0, domainTenancy.ErrUserRequired
	}
	username := domainTenancy.NormalizeUsername(user.Username)
	if username == "" {
		return 0, domainTenancy.ErrUserRequired
	}

	now := time.Now()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	user.UpdatedAt = now
	user.Username = username

	res, err := r.db.Exec(`
		INSERT INTO app_user (
			username, password_hash, display_name, role, device_limit, active,
			created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, user.Username, user.PasswordHash, user.DisplayName, string(user.Role),
		user.DeviceLimit, user.Active, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, domainTenancy.ErrUsernameTaken
		}
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to load new app user id: %w", err)
	}
	user.ID = id
	return id, nil
}

func (r *SQLiteRepository) UpdateUser(user *domainTenancy.User) error {
	if user == nil || user.ID == 0 {
		return domainTenancy.ErrUserRequired
	}
	username := domainTenancy.NormalizeUsername(user.Username)
	if username == "" {
		return domainTenancy.ErrUserRequired
	}

	user.Username = username
	user.UpdatedAt = time.Now()

	// created_at sengaja tidak ikut di-set: itu fakta historis, dan menulisnya
	// ulang dari struct yang mungkin zero akan menghapusnya.
	_, err := r.db.Exec(`
		UPDATE app_user
		SET username = ?, password_hash = ?, display_name = ?, role = ?,
		    device_limit = ?, active = ?, updated_at = ?
		WHERE id = ?
	`, user.Username, user.PasswordHash, user.DisplayName, string(user.Role),
		user.DeviceLimit, user.Active, user.UpdatedAt, user.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return domainTenancy.ErrUsernameTaken
		}
		return err
	}
	return nil
}

func (r *SQLiteRepository) GetUserByID(id int64) (*domainTenancy.User, error) {
	if id == 0 {
		return nil, nil
	}
	user, err := r.scanAppUser(r.db.QueryRow(
		"SELECT "+appUserColumns+" FROM app_user WHERE id = ?", id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (r *SQLiteRepository) GetUserByUsername(username string) (*domainTenancy.User, error) {
	normalized := domainTenancy.NormalizeUsername(username)
	if normalized == "" {
		return nil, nil
	}
	user, err := r.scanAppUser(r.db.QueryRow(
		"SELECT "+appUserColumns+" FROM app_user WHERE username = ?", normalized,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (r *SQLiteRepository) ListUsers() ([]*domainTenancy.User, error) {
	rows, err := r.db.Query("SELECT " + appUserColumns + " FROM app_user ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]*domainTenancy.User, 0)
	for rows.Next() {
		user, err := r.scanAppUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// DeleteUser menghapus user beserta session dan baris kepemilikan device-nya
// dalam satu transaksi.
//
// Transaksinya penting: kalau delete user berhasil tapi delete owner gagal,
// baris device_owner akan menunjuk user_id yang sudah tidak ada, dan device
// baru yang kebetulan memakai id sama akan langsung "dimiliki" hantu itu.
func (r *SQLiteRepository) DeleteUser(id int64) error {
	if id == 0 {
		return domainTenancy.ErrUserRequired
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM user_session WHERE user_id = ?", id); err != nil {
		return err
	}
	// Device-nya sendiri tidak disentuh: ia jadi tak-ber-owner dan hanya
	// terlihat admin. Mem-purge device berarti memutus sesi WhatsApp, dan itu
	// harus tetap keputusan eksplisit.
	if _, err := tx.Exec("DELETE FROM device_owner WHERE user_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM app_user WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLiteRepository) CountUsers() (int, error) {
	var count int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM app_user").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// CountAdmins hanya menghitung admin yang aktif: admin yang dinonaktifkan tidak
// bisa masuk, jadi ia tidak menolong invarian "selalu ada satu admin aktif".
func (r *SQLiteRepository) CountAdmins() (int, error) {
	var count int
	err := r.db.QueryRow(
		"SELECT COUNT(*) FROM app_user WHERE role = ? AND active = ?",
		string(domainTenancy.RoleAdmin), true,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// _____________________________________________________________________________
// Kepemilikan device

// SetDeviceOwner melakukan upsert dengan pola UPDATE-lalu-INSERT, sama seperti
// SaveChatwootDeviceConfig. Dipilih di atas INSERT ... ON CONFLICT karena
// getMigrations() menyatakan skema ini kompatibel SQLite/MySQL/PostgreSQL, dan
// ON CONFLICT tidak didukung MySQL.
func (r *SQLiteRepository) SetDeviceOwner(deviceID string, userID int64) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return domainTenancy.ErrDeviceIDRequired
	}
	if userID == 0 {
		return domainTenancy.ErrUserRequired
	}

	now := time.Now()
	result, err := r.db.Exec(
		"UPDATE device_owner SET user_id = ?, updated_at = ? WHERE device_id = ?",
		userID, now, deviceID,
	)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		return nil
	}

	_, err = r.db.Exec(`
		INSERT INTO device_owner (device_id, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
	`, deviceID, userID, now, now)
	return err
}

func (r *SQLiteRepository) GetDeviceOwner(deviceID string) (*domainTenancy.DeviceOwner, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, nil
	}

	owner := &domainTenancy.DeviceOwner{}
	err := r.db.QueryRow(`
		SELECT device_id, user_id, created_at, updated_at
		FROM device_owner
		WHERE device_id = ?
	`, deviceID).Scan(&owner.DeviceID, &owner.UserID, &owner.CreatedAt, &owner.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return owner, nil
}

// ListDeviceIDsByOwner mengembalikan device milik satu user dengan urutan
// stabil (created_at lalu device_id). Kestabilannya dipakai untuk memilih
// device default seorang operator, jadi jangan diganti ke urutan sembarang.
func (r *SQLiteRepository) ListDeviceIDsByOwner(userID int64) ([]string, error) {
	if userID == 0 {
		return nil, nil
	}

	rows, err := r.db.Query(`
		SELECT device_id
		FROM device_owner
		WHERE user_id = ?
		ORDER BY created_at, device_id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *SQLiteRepository) CountDevicesByOwner(userID int64) (int, error) {
	if userID == 0 {
		return 0, nil
	}
	var count int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM device_owner WHERE user_id = ?", userID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *SQLiteRepository) DeleteDeviceOwner(deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return domainTenancy.ErrDeviceIDRequired
	}
	_, err := r.db.Exec("DELETE FROM device_owner WHERE device_id = ?", deviceID)
	return err
}

func (r *SQLiteRepository) DeleteDeviceOwnersByUser(userID int64) error {
	if userID == 0 {
		return domainTenancy.ErrUserRequired
	}
	_, err := r.db.Exec("DELETE FROM device_owner WHERE user_id = ?", userID)
	return err
}

// _____________________________________________________________________________
// Session login

func (r *SQLiteRepository) CreateSession(session *domainTenancy.Session) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	tokenHash := strings.TrimSpace(session.TokenHash)
	if tokenHash == "" {
		return fmt.Errorf("session requires a token hash")
	}
	if session.UserID == 0 {
		return domainTenancy.ErrUserRequired
	}
	if session.ExpiresAt.IsZero() {
		return fmt.Errorf("session requires an expiry")
	}

	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	session.TokenHash = tokenHash

	_, err := r.db.Exec(`
		INSERT INTO user_session (token_hash, user_id, user_agent, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, session.TokenHash, session.UserID, session.UserAgent, session.CreatedAt, session.ExpiresAt)
	return err
}

// GetSession sengaja tidak memfilter kedaluwarsa — pemanggilnya yang
// memutuskan lewat Session.Expired. Repository yang menyaring akan membuat
// penyapu session tidak bisa melihat baris yang sudah mati.
func (r *SQLiteRepository) GetSession(tokenHash string) (*domainTenancy.Session, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, nil
	}

	session := &domainTenancy.Session{}
	err := r.db.QueryRow(`
		SELECT token_hash, user_id, user_agent, created_at, expires_at
		FROM user_session
		WHERE token_hash = ?
	`, tokenHash).Scan(
		&session.TokenHash, &session.UserID, &session.UserAgent,
		&session.CreatedAt, &session.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (r *SQLiteRepository) DeleteSession(tokenHash string) error {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil
	}
	_, err := r.db.Exec("DELETE FROM user_session WHERE token_hash = ?", tokenHash)
	return err
}

func (r *SQLiteRepository) DeleteSessionsByUser(userID int64) error {
	if userID == 0 {
		return domainTenancy.ErrUserRequired
	}
	_, err := r.db.Exec("DELETE FROM user_session WHERE user_id = ?", userID)
	return err
}

func (r *SQLiteRepository) DeleteExpiredSessions(now time.Time) (int64, error) {
	result, err := r.db.Exec("DELETE FROM user_session WHERE expires_at <= ?", now)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return deleted, nil
}
