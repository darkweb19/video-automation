package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	ID               string `json:"id"`
	VideoProvider    string `json:"video_provider"`
	Prompt           string `json:"prompt"`
	Model            string `json:"model"`
	Duration         int    `json:"duration,omitempty"`
	AspectRatio      string `json:"aspect_ratio,omitempty"`
	Status           string `json:"status"`
	CostUSD          string `json:"cost_usd,omitempty"`
	EstimatedCostUSD string `json:"estimated_cost_usd,omitempty"`
	VideoPath        string `json:"-"`
	VideoReady       bool   `json:"video_ready"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	Error            string `json:"error,omitempty"`
	DownloadAttempts int    `json:"download_attempts,omitempty"`
	NextDownloadAt   int64  `json:"next_download_at,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
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
	_, err := s.db.Exec(`INSERT INTO generations(id,video_provider,prompt,model,duration,aspect_ratio,status,cost_usd,estimated_cost_usd,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.VideoProvider, record.Prompt, record.Model, record.Duration, record.AspectRatio, record.Status, record.CostUSD, record.EstimatedCostUSD, now, now)
	return err
}

const generationColumns = `id,video_provider,prompt,model,duration,aspect_ratio,status,cost_usd,estimated_cost_usd,video_path,size_bytes,error,download_attempts,next_download_at,created_at,updated_at`

func scanGeneration(scanner interface{ Scan(...any) error }) (GenerationRecord, error) {
	var record GenerationRecord
	err := scanner.Scan(&record.ID, &record.VideoProvider, &record.Prompt, &record.Model, &record.Duration, &record.AspectRatio, &record.Status, &record.CostUSD, &record.EstimatedCostUSD, &record.VideoPath, &record.SizeBytes, &record.Error, &record.DownloadAttempts, &record.NextDownloadAt, &record.CreatedAt, &record.UpdatedAt)
	record.VideoReady = record.VideoPath != ""
	return record, err
}

func (s *Store) Generation(id string) (GenerationRecord, error) {
	return scanGeneration(s.db.QueryRow(`SELECT `+generationColumns+` FROM generations WHERE id=?`, id))
}

func (s *Store) Generations(limit int) ([]GenerationRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+generationColumns+` FROM generations ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]GenerationRecord, 0)
	for rows.Next() {
		record, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
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
	defer rows.Close()
	records := make([]GenerationRecord, 0, limit)
	for rows.Next() {
		record, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
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

func (s *Store) UpdateGeneration(id, status, model, cost, message string) error {
	result, err := s.db.Exec(`UPDATE generations SET status=?,model=CASE WHEN ?='' THEN model ELSE ? END,cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=? WHERE id=? AND status!='deleting'`, status, model, model, cost, cost, message, time.Now().Unix(), id)
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

func (s *Store) MarkVideoReady(id, videoPath string, size int64) error {
	result, err := s.db.Exec(`UPDATE generations SET status='completed',video_path=?,size_bytes=?,error='',next_download_at=0,updated_at=? WHERE id=? AND status!='deleting'`, videoPath, size, time.Now().Unix(), id)
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
	return nil
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
	result, err := s.db.Exec(`DELETE FROM generations WHERE id=? AND status='deleting'`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteGeneration(id string) (GenerationRecord, error) {
	record, err := s.Generation(id)
	if err != nil {
		return record, err
	}
	_, err = s.db.Exec(`DELETE FROM generations WHERE id=?`, id)
	return record, err
}
