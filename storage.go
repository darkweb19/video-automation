package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	dataDir    string
	videoDir   string
	projectDir string
}

type GenerationRecord struct {
	ID               string            `json:"id"`
	VideoProvider    string            `json:"video_provider"`
	ProviderConfigID string            `json:"-"`
	Prompt           string            `json:"prompt"`
	Model            string            `json:"model"`
	Duration         int               `json:"duration,omitempty"`
	AspectRatio      string            `json:"aspect_ratio,omitempty"`
	Status           string            `json:"status"`
	Progress         int               `json:"progress,omitempty"`
	CostUSD          string            `json:"cost_usd,omitempty"`
	EstimatedCostUSD string            `json:"estimated_cost_usd,omitempty"`
	VideoPath        string            `json:"-"`
	VideoReady       bool              `json:"video_ready"`
	SizeBytes        int64             `json:"size_bytes,omitempty"`
	Error            string            `json:"error,omitempty"`
	DownloadAttempts int               `json:"download_attempts,omitempty"`
	NextDownloadAt   int64             `json:"next_download_at,omitempty"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
	Events           []GenerationEvent `json:"events"`
}

// GenerationEvent is a backend-owned lifecycle entry. It intentionally never
// contains provider headers, credentials, or raw upstream responses.
type GenerationEvent struct {
	ID        int64  `json:"id"`
	Stage     string `json:"stage"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Progress  int    `json:"progress,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ModalVideoAccount is the safe account shape returned to the dashboard. The
// ciphertext is storage-only and deliberately excluded from JSON.
type ModalVideoAccount struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Endpoint         string `json:"endpoint"`
	EncryptedAPIKey  string `json:"-"`
	APIKeyConfigured bool   `json:"configured"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

// ProviderConfig is intentionally storage-only: it contains a ciphertext and
// must never be serialized through dashboard APIs.
type ProviderConfig struct {
	ID              string
	Provider        string
	BaseURL         string
	EncryptedAPIKey string
}

func newProviderConfigID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "provider_config_" + hex.EncodeToString(raw), nil
}

func newModalVideoAccountID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "modal_account_" + hex.EncodeToString(raw), nil
}

func (s *Store) CreateProviderConfig(provider, baseURL, encryptedAPIKey string) (ProviderConfig, error) {
	id, err := newProviderConfigID()
	if err != nil {
		return ProviderConfig{}, err
	}
	return s.InsertProviderConfig(ProviderConfig{ID: id, Provider: provider, BaseURL: baseURL, EncryptedAPIKey: encryptedAPIKey})
}

func (s *Store) InsertProviderConfig(config ProviderConfig) (ProviderConfig, error) {
	if config.ID == "" {
		return ProviderConfig{}, errors.New("provider config id is required")
	}
	_, err := s.db.Exec(`INSERT INTO video_provider_configs(id,provider,base_url,encrypted_api_key,created_at) VALUES(?,?,?,?,?)`, config.ID, config.Provider, config.BaseURL, config.EncryptedAPIKey, time.Now().Unix())
	return config, err
}

func (s *Store) ProviderConfig(id string) (ProviderConfig, error) {
	var config ProviderConfig
	err := s.db.QueryRow(`SELECT id,provider,base_url,encrypted_api_key FROM video_provider_configs WHERE id=?`, id).Scan(&config.ID, &config.Provider, &config.BaseURL, &config.EncryptedAPIKey)
	return config, err
}

func (s *Store) InsertModalVideoAccount(account ModalVideoAccount) (ModalVideoAccount, error) {
	if account.ID == "" || account.Name == "" || account.Endpoint == "" || account.EncryptedAPIKey == "" {
		return ModalVideoAccount{}, errors.New("modal account is incomplete")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO modal_video_accounts(id,name,endpoint,encrypted_api_key,created_at,updated_at) VALUES(?,?,?,?,?,?)`, account.ID, account.Name, account.Endpoint, account.EncryptedAPIKey, now, now)
	if err != nil {
		return ModalVideoAccount{}, err
	}
	account.APIKeyConfigured = true
	account.CreatedAt, account.UpdatedAt = now, now
	return account, nil
}

func (s *Store) ModalVideoAccount(id string) (ModalVideoAccount, error) {
	var account ModalVideoAccount
	err := s.db.QueryRow(`SELECT id,name,endpoint,encrypted_api_key,created_at,updated_at FROM modal_video_accounts WHERE id=?`, id).Scan(&account.ID, &account.Name, &account.Endpoint, &account.EncryptedAPIKey, &account.CreatedAt, &account.UpdatedAt)
	account.APIKeyConfigured = account.EncryptedAPIKey != ""
	return account, err
}

func (s *Store) ModalVideoAccounts() ([]ModalVideoAccount, error) {
	rows, err := s.db.Query(`SELECT id,name,endpoint,encrypted_api_key,created_at,updated_at FROM modal_video_accounts ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]ModalVideoAccount, 0)
	for rows.Next() {
		var account ModalVideoAccount
		if err := rows.Scan(&account.ID, &account.Name, &account.Endpoint, &account.EncryptedAPIKey, &account.CreatedAt, &account.UpdatedAt); err != nil {
			return nil, err
		}
		account.APIKeyConfigured = account.EncryptedAPIKey != ""
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *Store) UpdateModalVideoAccount(account ModalVideoAccount) (ModalVideoAccount, error) {
	if account.ID == "" || account.Name == "" || account.Endpoint == "" || account.EncryptedAPIKey == "" {
		return ModalVideoAccount{}, errors.New("modal account is incomplete")
	}
	now := time.Now().Unix()
	result, err := s.db.Exec(`UPDATE modal_video_accounts SET name=?,endpoint=?,encrypted_api_key=?,updated_at=? WHERE id=?`, account.Name, account.Endpoint, account.EncryptedAPIKey, now, account.ID)
	if err != nil {
		return ModalVideoAccount{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ModalVideoAccount{}, err
	}
	if count != 1 {
		return ModalVideoAccount{}, sql.ErrNoRows
	}
	account.APIKeyConfigured = true
	account.UpdatedAt = now
	return account, nil
}

func (s *Store) DeleteModalVideoAccount(id string) error {
	result, err := s.db.Exec(`DELETE FROM modal_video_accounts WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteProviderConfigIfUnused removes a snapshot only when no durable job or
// project still references it. Completed records intentionally retain their
// snapshot so content/download recovery remains possible.
func (s *Store) DeleteProviderConfigIfUnused(id string) error {
	if id == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM video_provider_configs WHERE id=? AND NOT EXISTS (SELECT 1 FROM generations WHERE provider_config_id=?) AND NOT EXISTS (SELECT 1 FROM video_projects WHERE provider_config_id=?)`, id, id, id)
	return err
}

func (s *Store) GarbageCollectProviderConfigs() error {
	_, err := s.db.Exec(`DELETE FROM video_provider_configs WHERE NOT EXISTS (SELECT 1 FROM generations WHERE generations.provider_config_id=video_provider_configs.id) AND NOT EXISTS (SELECT 1 FROM video_projects WHERE video_projects.provider_config_id=video_provider_configs.id)`)
	return err
}

func (s *Store) LegacyPendingProviderIDs() ([]VideoProviderID, error) {
	rows, err := s.db.Query(`SELECT DISTINCT video_provider FROM (
		SELECT video_provider FROM generations WHERE provider_config_id='' AND status IN ('queued','processing','downloading','download_failed')
		UNION
		SELECT video_provider FROM video_projects WHERE provider_config_id='' AND status IN ('planning','generating')
	)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var providers []VideoProviderID
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		if validVideoProvider(VideoProviderID(provider)) {
			providers = append(providers, VideoProviderID(provider))
		}
	}
	return providers, rows.Err()
}

func (s *Store) AssignLegacyProviderConfig(provider VideoProviderID, configID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE generations SET provider_config_id=? WHERE provider_config_id='' AND video_provider=? AND status IN ('queued','processing','downloading','download_failed')`, configID, provider); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE video_projects SET provider_config_id=? WHERE provider_config_id='' AND video_provider=? AND status IN ('planning','generating')`, configID, provider); err != nil {
		return err
	}
	return tx.Commit()
}

func OpenStore(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	videoDir := filepath.Join(dataDir, "videos")
	if err := os.MkdirAll(videoDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	projectDir := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return nil, fmt.Errorf("create project directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "app.db"))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, dataDir: dataDir, videoDir: videoDir, projectDir: projectDir}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			must_change_password INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
			expires_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS password_recovery_codes (
			username TEXT PRIMARY KEY REFERENCES users(username) ON DELETE CASCADE,
			token_hash TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS video_provider_configs (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			base_url TEXT NOT NULL DEFAULT '',
			encrypted_api_key TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS modal_video_accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			encrypted_api_key TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS modal_video_accounts_name ON modal_video_accounts(name);
		CREATE TABLE IF NOT EXISTS generations (
			id TEXT PRIMARY KEY,
			prompt TEXT NOT NULL,
			model TEXT NOT NULL,
			duration INTEGER NOT NULL DEFAULT 0,
			aspect_ratio TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			cost_usd TEXT NOT NULL DEFAULT '',
			estimated_cost_usd TEXT NOT NULL DEFAULT '',
			video_path TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			download_attempts INTEGER NOT NULL DEFAULT 0,
			next_download_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS generations_created_at ON generations(created_at DESC);
		CREATE INDEX IF NOT EXISTS generations_status ON generations(status);
		CREATE TABLE IF NOT EXISTS generation_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			generation_id TEXT NOT NULL REFERENCES generations(id) ON DELETE CASCADE,
			stage TEXT NOT NULL,
			status TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			progress INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS generation_events_generation_id ON generation_events(generation_id,id);
		CREATE TABLE IF NOT EXISTS video_projects (
			id TEXT PRIMARY KEY,
			topic TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			story TEXT NOT NULL DEFAULT '',
			script TEXT NOT NULL DEFAULT '',
			continuity TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL,
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			final_video_path TEXT NOT NULL DEFAULT '',
			final_size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS project_scenes (
			project_id TEXT NOT NULL REFERENCES video_projects(id) ON DELETE CASCADE,
			scene_number INTEGER NOT NULL CHECK(scene_number BETWEEN 1 AND 5),
			title TEXT NOT NULL,
			scene_script TEXT NOT NULL,
			prompt TEXT NOT NULL,
			status TEXT NOT NULL,
			progress INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			provider_generation_id TEXT NOT NULL DEFAULT '',
			cost_usd TEXT NOT NULL DEFAULT '',
			video_path TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			download_attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(project_id, scene_number)
		);
		CREATE INDEX IF NOT EXISTS video_projects_status ON video_projects(status);
		CREATE INDEX IF NOT EXISTS project_scenes_status ON project_scenes(status,next_attempt_at);
		CREATE TABLE IF NOT EXISTS project_text_generation (
			project_id TEXT PRIMARY KEY REFERENCES video_projects(id) ON DELETE CASCADE,
			router_model TEXT NOT NULL DEFAULT '',
			actual_model TEXT NOT NULL DEFAULT '',
			system_prompt TEXT NOT NULL DEFAULT '',
			user_prompt TEXT NOT NULL DEFAULT '',
			response_schema TEXT NOT NULL DEFAULT '',
			raw_response TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS project_pipeline_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id TEXT NOT NULL REFERENCES video_projects(id) ON DELETE CASCADE,
			stage TEXT NOT NULL,
			status TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			scene_number INTEGER NOT NULL DEFAULT 0,
			attempt INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS project_pipeline_events_project_id ON project_pipeline_events(project_id,id);
		DELETE FROM sessions WHERE expires_at <= unixepoch();
		DELETE FROM password_recovery_codes WHERE expires_at <= unixepoch();
		UPDATE project_scenes SET status='failed',error='Scene submission was interrupted; retry this scene',updated_at=unixepoch() WHERE status='submitting';
	`)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	// Existing installations predate these columns. A default of one for the
	// user migration forces the known bootstrap account to choose a real secret.
	if err := s.addColumnIfMissing("users", "must_change_password", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("generations", "download_attempts", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("generations", "next_download_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("project_scenes", "progress", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("generations", "video_provider", "TEXT NOT NULL DEFAULT 'openrouter'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("video_projects", "video_provider", "TEXT NOT NULL DEFAULT 'openrouter'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("generations", "provider_config_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("generations", "progress", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("video_projects", "provider_config_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key,value,updated_at) VALUES('video_provider','openrouter',unixepoch()) ON CONFLICT(key) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("initialize video provider setting: %w", err)
	}
	return nil
}

func (s *Store) addColumnIfMissing(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) EnsureUser(username, passwordHash string) error {
	_, err := s.db.Exec(`INSERT INTO users(username,password_hash,must_change_password,updated_at) VALUES(?,?,1,?) ON CONFLICT(username) DO NOTHING`, username, passwordHash, time.Now().Unix())
	return err
}

func (s *Store) PasswordHash(username string) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE username=?`, username).Scan(&hash)
	return hash, err
}

func (s *Store) UserAuthentication(username string) (string, bool, error) {
	var hash string
	var mustChange bool
	err := s.db.QueryRow(`SELECT password_hash,must_change_password FROM users WHERE username=?`, username).Scan(&hash, &mustChange)
	return hash, mustChange, err
}

func (s *Store) ChangePassword(username, passwordHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE users SET password_hash=?,must_change_password=0,updated_at=? WHERE username=?`, passwordHash, time.Now().Unix(), username)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.Exec(`DELETE FROM sessions WHERE username=?`, username); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateRecoveryCode(username, codeHash string, expiresAt int64) error {
	result, err := s.db.Exec(`INSERT INTO password_recovery_codes(username,token_hash,expires_at,created_at) VALUES(?,?,?,?) ON CONFLICT(username) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,created_at=excluded.created_at`, username, codeHash, expiresAt, time.Now().Unix())
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ConsumeRecoveryCode(username, codeHash, passwordHash string, now int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var storedHash string
	var expiresAt int64
	if err := tx.QueryRow(`SELECT token_hash,expires_at FROM password_recovery_codes WHERE username=?`, username).Scan(&storedHash, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if expiresAt <= now || subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
		return false, nil
	}
	result, err := tx.Exec(`UPDATE users SET password_hash=?,must_change_password=0,updated_at=? WHERE username=?`, passwordHash, now, username)
	if err != nil {
		return false, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return false, sql.ErrNoRows
	}
	if _, err = tx.Exec(`DELETE FROM sessions WHERE username=?`, username); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM password_recovery_codes WHERE username=?`, username); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CreateSession(tokenHash, username string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,username,expires_at) VALUES(?,?,?)`, tokenHash, username, expiresAt)
	return err
}

func (s *Store) SessionUser(tokenHash string, now int64) (string, bool, error) {
	var username string
	var mustChange bool
	err := s.db.QueryRow(`SELECT users.username,users.must_change_password FROM sessions JOIN users ON users.username=sessions.username WHERE sessions.token_hash=? AND sessions.expires_at>?`, tokenHash, now).Scan(&username, &mustChange)
	return username, mustChange, err
}

func (s *Store) DeleteSession(tokenHash string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
}

func (s *Store) Setting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	return value, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, time.Now().Unix())
	return err
}

func (s *Store) InsertGeneration(record GenerationRecord) error {
	now := time.Now().Unix()
	if record.VideoProvider == "" {
		record.VideoProvider = string(VideoProviderOpenRouter)
	}
	if record.Status == "failed" {
		record.Error = sanitizeProviderFailure(record.Error)
		if record.Error == "" {
			record.Error = "Video generation failed"
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO generations(id,video_provider,provider_config_id,prompt,model,duration,aspect_ratio,status,progress,cost_usd,estimated_cost_usd,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.VideoProvider, record.ProviderConfigID, record.Prompt, record.Model, record.Duration, record.AspectRatio, record.Status, record.Progress, record.CostUSD, record.EstimatedCostUSD, record.Error, now, now)
	if err != nil {
		return err
	}
	message := "Generation request accepted"
	if record.Status == "failed" {
		message = record.Error
	}
	// A submitted provider job may already be billable. Keep its durable state
	// even if the optional terminal-event write is rejected (for example by a
	// transient SQLite constraint); otherwise the caller would lose the job ID.
	_ = appendGenerationEvent(tx, record.ID, "submission", record.Status, message, record.Progress)
	return tx.Commit()
}

const generationColumns = `id,video_provider,provider_config_id,prompt,model,duration,aspect_ratio,status,progress,cost_usd,estimated_cost_usd,video_path,size_bytes,error,download_attempts,next_download_at,created_at,updated_at`

func scanGeneration(scanner interface{ Scan(...any) error }) (GenerationRecord, error) {
	var record GenerationRecord
	err := scanner.Scan(&record.ID, &record.VideoProvider, &record.ProviderConfigID, &record.Prompt, &record.Model, &record.Duration, &record.AspectRatio, &record.Status, &record.Progress, &record.CostUSD, &record.EstimatedCostUSD, &record.VideoPath, &record.SizeBytes, &record.Error, &record.DownloadAttempts, &record.NextDownloadAt, &record.CreatedAt, &record.UpdatedAt)
	record.VideoReady = record.VideoPath != ""
	return record, err
}

func (s *Store) Generation(id string) (GenerationRecord, error) {
	record, err := scanGeneration(s.db.QueryRow(`SELECT `+generationColumns+` FROM generations WHERE id=?`, id))
	if err != nil {
		return record, err
	}
	record.Events, err = s.GenerationEvents(id)
	return record, err
}

func (s *Store) Generations(limit int) ([]GenerationRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+generationColumns+` FROM generations ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	records := make([]GenerationRecord, 0)
	for rows.Next() {
		record, err := scanGeneration(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range records {
		events, err := s.GenerationEvents(records[index].ID)
		if err != nil {
			return nil, err
		}
		records[index].Events = events
	}
	return records, nil
}

type GenerationStats struct {
	Total        int    `json:"total"`
	Queued       int    `json:"queued"`
	Processing   int    `json:"processing"`
	Downloading  int    `json:"downloading"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	TotalCostUSD string `json:"total_cost_usd"`
}

func (s *Store) GenerationStats() (GenerationStats, error) {
	var stats GenerationStats
	var cost float64
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status='queued'),0),COALESCE(SUM(status='processing'),0),COALESCE(SUM(status IN ('downloading','download_failed')),0),COALESCE(SUM(status='completed'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(CAST(NULLIF(cost_usd,'') AS REAL)),0) FROM generations`).Scan(&stats.Total, &stats.Queued, &stats.Processing, &stats.Downloading, &stats.Completed, &stats.Failed, &cost)
	stats.TotalCostUSD = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", cost), "0"), ".")
	return stats, err
}

func (s *Store) GenerationsPage(limit int, beforeCreated int64, beforeID string) ([]GenerationRecord, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	query := `SELECT ` + generationColumns + ` FROM generations`
	args := []any{}
	if beforeCreated > 0 && beforeID != "" {
		query += ` WHERE created_at < ? OR (created_at = ? AND id < ?)`
		args = append(args, beforeCreated, beforeCreated, beforeID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	records := make([]GenerationRecord, 0, limit)
	for rows.Next() {
		record, err := scanGeneration(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range records {
		events, err := s.GenerationEvents(records[index].ID)
		if err != nil {
			return nil, err
		}
		records[index].Events = events
	}
	return records, nil
}

func (s *Store) PendingGenerations(ctx context.Context) ([]GenerationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationColumns+` FROM generations WHERE status IN ('queued','processing','downloading','download_failed') AND next_download_at<=unixepoch() ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []GenerationRecord
	for rows.Next() {
		record, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

var (
	credentialValuePattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token|secret)\s*(?:=|:)?\s*(?:bearer\s+)?[^\s,;]+`)
	urlQueryPattern        = regexp.MustCompile(`https?://[^\s?]+\?[^\s]+`)
)

// sanitizeProviderFailure converts provider responses into an operator-safe
// card message. Raw upstream bodies must never be persisted or displayed.
func sanitizeProviderFailure(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "401"), strings.Contains(lower, "403"):
		return "Video provider authentication failed"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "429"):
		return "Video provider rate limit reached"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return "Video provider request timed out"
	case strings.Contains(lower, "not found"), strings.Contains(lower, "404"):
		return "Video provider job was not found"
	}
	return "Video provider reported a generation failure"
}

type generationEventExecutor interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

func (s *Store) AppendGenerationEvent(id, stage, status, message string, progress int) error {
	return appendGenerationEvent(s.db, id, stage, status, message, progress)
}

func appendGenerationEvent(executor generationEventExecutor, id, stage, status, message string, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	// Callers own these normalized lifecycle messages. Defensive redaction keeps
	// accidental secret-like values out without converting useful app messages.
	message = credentialValuePattern.ReplaceAllString(message, "[redacted]")
	message = urlQueryPattern.ReplaceAllString(message, "[provider URL redacted]")
	message = strings.Join(strings.Fields(message), " ")
	// Provider polling can repeat the same state. Keep a durable terminal that
	// records transitions/progress changes rather than every polling loop.
	var previous GenerationEvent
	err := executor.QueryRow(`SELECT id,stage,status,message,progress,created_at FROM generation_events WHERE generation_id=? ORDER BY id DESC LIMIT 1`, id).Scan(&previous.ID, &previous.Stage, &previous.Status, &previous.Message, &previous.Progress, &previous.CreatedAt)
	if err == nil && previous.Stage == stage && previous.Status == status && previous.Message == message && previous.Progress == progress {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = executor.Exec(`INSERT INTO generation_events(generation_id,stage,status,message,progress,created_at) VALUES(?,?,?,?,?,?)`, id, stage, status, message, progress, time.Now().Unix())
	return err
}

func (s *Store) GenerationEvents(id string) ([]GenerationEvent, error) {
	rows, err := s.db.Query(`SELECT id,stage,status,message,progress,created_at FROM generation_events WHERE generation_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]GenerationEvent, 0)
	for rows.Next() {
		var event GenerationEvent
		if err := rows.Scan(&event.ID, &event.Stage, &event.Status, &event.Message, &event.Progress, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) UpdateGeneration(id, status, model, cost, message string) error {
	return s.UpdateGenerationProgress(id, status, model, cost, message, -1)
}

// UpdateGenerationProgress is used by the durable worker after polling a
// provider. Progress is optional: pass -1 when an upstream provider has no
// numeric progress signal and the previous value will be retained.
func (s *Store) UpdateGenerationProgress(id, status, model, cost, message string, progress int) error {
	if status == "failed" {
		if message == "" {
			message = "Video generation failed"
		} else {
			message = sanitizeProviderFailure(message)
		}
	} else if message != "" {
		message = sanitizeProviderFailure(message)
	}
	if progress > 100 {
		progress = 100
	}
	if progress < -1 {
		progress = -1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE generations SET status=?,progress=CASE WHEN ?<0 THEN progress ELSE ? END,model=CASE WHEN ?='' THEN model ELSE ? END,cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=? WHERE id=? AND status!='deleting'`, status, progress, progress, model, model, cost, cost, message, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	eventProgress := progress
	if eventProgress < 0 {
		var stored int
		if err := tx.QueryRow(`SELECT progress FROM generations WHERE id=?`, id).Scan(&stored); err == nil {
			eventProgress = stored
		} else {
			return err
		}
	}
	stage := "provider"
	if status == "downloading" || status == "download_failed" {
		stage = "download"
	}
	// Status/progress are the recovery source of truth. Never fail a successful
	// provider transition solely because an auxiliary terminal entry failed.
	_ = appendGenerationEvent(tx, id, stage, status, message, eventProgress)
	return tx.Commit()
}

func (s *Store) MarkVideoReady(id, videoPath string, size int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE generations SET status='completed',progress=100,video_path=?,size_bytes=?,error='',next_download_at=0,updated_at=? WHERE id=? AND status!='deleting'`, videoPath, size, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	// The local file is valid at this point. Do not make the downloader delete it
	// merely because recording an auxiliary terminal line failed.
	_ = appendGenerationEvent(tx, id, "download", "completed", "Video downloaded and ready", 100)
	return tx.Commit()
}

// VideoPath returns the only permissible local filename for a generation.
// Callers must validate the provider id before using it as a filename.
func (s *Store) VideoPath(id string) string {
	return filepath.Join(s.videoDir, id+".mp4")
}

func (s *Store) ScheduleDownloadRetry(id string) error {
	var attempts int
	if err := s.db.QueryRow(`SELECT download_attempts FROM generations WHERE id=? AND status!='deleting'`, id).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	backoff := time.Second * time.Duration(1<<min(attempts-1, 8))
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	result, err := s.db.Exec(`UPDATE generations SET status='download_failed',download_attempts=?,next_download_at=?,error='Video download will retry automatically',updated_at=? WHERE id=? AND status!='deleting'`, attempts, time.Now().Add(backoff).Unix(), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendGenerationEvent(id, "download", "download_failed", "Video download will retry automatically", 0)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Store) BeginDelete(id string) (GenerationRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return GenerationRecord{}, err
	}
	defer tx.Rollback()
	record, err := scanGeneration(tx.QueryRow(`SELECT `+generationColumns+` FROM generations WHERE id=? AND status!='deleting'`, id))
	if err != nil {
		return record, err
	}
	result, err := tx.Exec(`UPDATE generations SET status='deleting',updated_at=? WHERE id=? AND status!='deleting'`, time.Now().Unix(), id)
	if err != nil {
		return record, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return record, sql.ErrNoRows
	}
	return record, tx.Commit()
}

func (s *Store) CancelDelete(id, status string) {
	_, _ = s.db.Exec(`UPDATE generations SET status=?,updated_at=? WHERE id=? AND status='deleting'`, status, time.Now().Unix(), id)
}

func (s *Store) FinishDelete(id string) error {
	record, err := s.Generation(id)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM generations WHERE id=? AND status='deleting'`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return s.DeleteProviderConfigIfUnused(record.ProviderConfigID)
}

func (s *Store) DeleteGeneration(id string) (GenerationRecord, error) {
	record, err := s.Generation(id)
	if err != nil {
		return record, err
	}
	_, err = s.db.Exec(`DELETE FROM generations WHERE id=?`, id)
	if err == nil {
		err = s.DeleteProviderConfigIfUnused(record.ProviderConfigID)
	}
	return record, err
}
