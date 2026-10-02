package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	youtubeUploadQueued            = "queued"
	youtubeUploadInitiating        = "initiating"
	youtubeUploadSending           = "uploading"
	youtubeUploadProcessing        = "processing"
	youtubeUploadCompleted         = "completed"
	youtubeUploadFailed            = "failed"
	youtubeUploadCanceled          = "canceled"
	youtubeUploadNeedsReconnect    = "needs_reconnect"
	youtubeUploadAttentionRequired = "attention_required"
)

var errYouTubeUploadExists = errors.New("a YouTube upload already exists for this video and channel")

type YouTubeConfig struct {
	ClientID              string
	EncryptedClientSecret string
	BaseURL               string
	EncryptedAccessToken  string
	EncryptedRefreshToken string
	AccessTokenExpiresAt  int64
	ChannelID             string
	ChannelTitle          string
	ConnectionVersion     int64
	ConfigVersion         int64
	ReconnectRequired     bool
}

type YouTubeOAuthFlow struct {
	StateHash         string
	SessionHash       string
	BindingHash       string
	RedirectURI       string
	EncryptedVerifier string
	ConfigVersion     int64
	ExpiresAt         int64
}

// YouTubeUpload is both the durable upload record and the public safe view.
// Paths, OAuth data, version pins, and resumable URLs are deliberately omitted
// from JSON so a browser can never obtain a bearer capability.
type YouTubeUpload struct {
	ID                     string `json:"id"`
	SourceKind             string `json:"source_kind"`
	SourceID               string `json:"source_id"`
	Title                  string `json:"title"`
	Description            string `json:"description"`
	RequestedPrivacyStatus string `json:"requested_privacy_status"`
	PrivacyStatus          string `json:"privacy_status"`
	MadeForKids            bool   `json:"made_for_kids"`
	ContainsSyntheticMedia bool   `json:"contains_synthetic_media"`
	Status                 string `json:"status"`
	Progress               int    `json:"progress"`
	CancelRequested        bool   `json:"cancel_requested,omitempty"`
	Error                  string `json:"error,omitempty"`
	YouTubeVideoID         string `json:"youtube_video_id,omitempty"`
	YouTubeURL             string `json:"youtube_url,omitempty"`
	ProcessingStatus       string `json:"processing_status,omitempty"`
	CreatedAt              int64  `json:"created_at"`
	UpdatedAt              int64  `json:"updated_at"`

	ChannelID            string `json:"-"`
	ConnectionVersion    int64  `json:"-"`
	SourcePath           string `json:"-"`
	SourceSize           int64  `json:"-"`
	SourceModTimeNS      int64  `json:"-"`
	EncryptedSessionURL  string `json:"-"`
	NextAttemptAt        int64  `json:"-"`
	AttemptCount         int    `json:"-"`
	ProcessingCheckCount int    `json:"-"`
}

func newYouTubeUploadID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "youtube_" + hex.EncodeToString(raw), nil
}

func (s *Store) migrateYouTube() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS youtube_config (
			id INTEGER PRIMARY KEY CHECK(id=1),
			client_id TEXT NOT NULL DEFAULT '',
			encrypted_client_secret TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL DEFAULT '',
			encrypted_access_token TEXT NOT NULL DEFAULT '',
			encrypted_refresh_token TEXT NOT NULL DEFAULT '',
			access_token_expires_at INTEGER NOT NULL DEFAULT 0,
			channel_id TEXT NOT NULL DEFAULT '',
			channel_title TEXT NOT NULL DEFAULT '',
			connection_version INTEGER NOT NULL DEFAULT 1,
			config_version INTEGER NOT NULL DEFAULT 1,
			reconnect_required INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO youtube_config(id) VALUES(1) ON CONFLICT(id) DO NOTHING;
		CREATE TABLE IF NOT EXISTS youtube_oauth_flows (
			state_hash TEXT PRIMARY KEY,
			session_hash TEXT NOT NULL,
			binding_hash TEXT NOT NULL,
			redirect_uri TEXT NOT NULL,
			encrypted_verifier TEXT NOT NULL,
			config_version INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS youtube_oauth_flows_session ON youtube_oauth_flows(session_hash);
		CREATE TABLE IF NOT EXISTS youtube_uploads (
			id TEXT PRIMARY KEY,
			source_kind TEXT NOT NULL CHECK(source_kind IN ('generation','project')),
			source_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			connection_version INTEGER NOT NULL,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			requested_privacy_status TEXT NOT NULL CHECK(requested_privacy_status IN ('private','unlisted','public')),
			privacy_status TEXT NOT NULL CHECK(privacy_status IN ('private','unlisted','public')),
			made_for_kids INTEGER NOT NULL,
			contains_synthetic_media INTEGER NOT NULL,
			status TEXT NOT NULL,
			progress INTEGER NOT NULL DEFAULT 0,
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			youtube_video_id TEXT NOT NULL DEFAULT '',
			processing_status TEXT NOT NULL DEFAULT '',
			source_path TEXT NOT NULL,
			source_size INTEGER NOT NULL,
			source_mtime_ns INTEGER NOT NULL,
			encrypted_session_url TEXT NOT NULL DEFAULT '',
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			processing_check_count INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(source_kind,source_id,channel_id)
		);
		CREATE INDEX IF NOT EXISTS youtube_uploads_work ON youtube_uploads(status,next_attempt_at,created_at);
		CREATE INDEX IF NOT EXISTS youtube_uploads_source ON youtube_uploads(source_kind,source_id,created_at DESC);
		DELETE FROM youtube_oauth_flows WHERE expires_at <= unixepoch();
		UPDATE youtube_uploads SET status='attention_required',error='The app restarted while starting the upload. Check YouTube Studio before starting another upload.',updated_at=unixepoch() WHERE status='initiating' AND cancel_requested=0;
		UPDATE youtube_uploads SET status='canceled',error='',updated_at=unixepoch() WHERE status='initiating' AND cancel_requested=1;
	`)
	if err != nil {
		return fmt.Errorf("initialize YouTube storage: %w", err)
	}
	return nil
}

func (s *Store) youtubeConfig() (YouTubeConfig, error) {
	var config YouTubeConfig
	err := s.db.QueryRow(`SELECT client_id,encrypted_client_secret,base_url,encrypted_access_token,encrypted_refresh_token,access_token_expires_at,channel_id,channel_title,connection_version,config_version,reconnect_required FROM youtube_config WHERE id=1`).Scan(
		&config.ClientID, &config.EncryptedClientSecret, &config.BaseURL, &config.EncryptedAccessToken, &config.EncryptedRefreshToken, &config.AccessTokenExpiresAt, &config.ChannelID, &config.ChannelTitle, &config.ConnectionVersion, &config.ConfigVersion, &config.ReconnectRequired,
	)
	return config, err
}

func (s *Store) saveYouTubeSettings(clientID, encryptedSecret, baseURL string, configVersion int64) error {
	_, err := s.db.Exec(`UPDATE youtube_config SET client_id=?,encrypted_client_secret=?,base_url=?,config_version=?,updated_at=unixepoch() WHERE id=1`, clientID, encryptedSecret, baseURL, configVersion)
	return err
}

func (s *Store) clearYouTubeOAuthFlows() error {
	_, err := s.db.Exec(`DELETE FROM youtube_oauth_flows`)
	return err
}

func (s *Store) insertYouTubeOAuthFlow(flow YouTubeOAuthFlow, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM youtube_oauth_flows WHERE expires_at<=? OR session_hash=?`, now, flow.SessionHash); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO youtube_oauth_flows(state_hash,session_hash,binding_hash,redirect_uri,encrypted_verifier,config_version,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, flow.StateHash, flow.SessionHash, flow.BindingHash, flow.RedirectURI, flow.EncryptedVerifier, flow.ConfigVersion, now, flow.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

// consumeYouTubeOAuthFlow consumes a valid state exactly once and verifies the
// initiating authenticated session remains live. The callback cookie is Lax
// so it is sent on Google's top-level GET redirect, while the dashboard's
// normal Strict session cookie remains unavailable cross-site.
func (s *Store) consumeYouTubeOAuthFlow(stateHash, bindingHash string, now int64) (YouTubeOAuthFlow, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return YouTubeOAuthFlow{}, err
	}
	defer tx.Rollback()
	var flow YouTubeOAuthFlow
	err = tx.QueryRow(`SELECT state_hash,session_hash,binding_hash,redirect_uri,encrypted_verifier,config_version,expires_at FROM youtube_oauth_flows WHERE state_hash=?`, stateHash).Scan(
		&flow.StateHash, &flow.SessionHash, &flow.BindingHash, &flow.RedirectURI, &flow.EncryptedVerifier, &flow.ConfigVersion, &flow.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return YouTubeOAuthFlow{}, sql.ErrNoRows
	}
	if err != nil {
		return YouTubeOAuthFlow{}, err
	}
	if flow.ExpiresAt <= now || !constantStringEqual(flow.BindingHash, bindingHash) {
		_, _ = tx.Exec(`DELETE FROM youtube_oauth_flows WHERE state_hash=?`, stateHash)
		return YouTubeOAuthFlow{}, sql.ErrNoRows
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions JOIN users ON users.username=sessions.username WHERE sessions.token_hash=? AND sessions.expires_at>? AND users.must_change_password=0`, flow.SessionHash, now).Scan(&active); err != nil {
		return YouTubeOAuthFlow{}, err
	}
	if active != 1 {
		_, _ = tx.Exec(`DELETE FROM youtube_oauth_flows WHERE state_hash=?`, stateHash)
		return YouTubeOAuthFlow{}, sql.ErrNoRows
	}
	result, err := tx.Exec(`DELETE FROM youtube_oauth_flows WHERE state_hash=? AND binding_hash=? AND expires_at>?`, stateHash, flow.BindingHash, now)
	if err != nil {
		return YouTubeOAuthFlow{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return YouTubeOAuthFlow{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return YouTubeOAuthFlow{}, err
	}
	return flow, nil
}

func (s *Store) connectYouTube(config YouTubeConfig, activeChannel, sessionHash string, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentVersion int64
	var currentConfigVersion int64
	if err := tx.QueryRow(`SELECT connection_version,config_version FROM youtube_config WHERE id=1`).Scan(&currentVersion, &currentConfigVersion); err != nil {
		return err
	}
	if currentConfigVersion != config.ConfigVersion {
		return errors.New("YouTube settings changed during authorization; connect again")
	}
	var validSession int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions JOIN users ON users.username=sessions.username WHERE sessions.token_hash=? AND sessions.expires_at>? AND users.must_change_password=0`, sessionHash, now).Scan(&validSession); err != nil {
		return err
	}
	if validSession != 1 {
		return errors.New("the dashboard session expired during YouTube authorization")
	}
	var countOtherChannel int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM youtube_uploads WHERE channel_id<>? AND status IN ('queued','initiating','uploading','processing','needs_reconnect')`, config.ChannelID).Scan(&countOtherChannel); err != nil {
		return err
	}
	if countOtherChannel != 0 {
		return errors.New("finish or cancel uploads for the current YouTube channel before connecting a different channel")
	}
	newVersion := currentVersion + 1
	result, err := tx.Exec(`UPDATE youtube_config SET encrypted_access_token=?,encrypted_refresh_token=?,access_token_expires_at=?,channel_id=?,channel_title=?,connection_version=?,reconnect_required=0 WHERE id=1 AND config_version=?`, config.EncryptedAccessToken, config.EncryptedRefreshToken, config.AccessTokenExpiresAt, config.ChannelID, config.ChannelTitle, newVersion, config.ConfigVersion)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("YouTube settings changed during authorization; connect again")
	}
	if activeChannel != "" && activeChannel == config.ChannelID {
		if _, err := tx.Exec(`UPDATE youtube_uploads SET connection_version=?,status=CASE WHEN status='needs_reconnect' THEN CASE WHEN encrypted_session_url='' THEN 'queued' ELSE 'uploading' END ELSE status END,error=CASE WHEN status='failed' THEN error ELSE '' END,next_attempt_at=0,updated_at=? WHERE channel_id=? AND status IN ('queued','uploading','processing','needs_reconnect','failed')`, newVersion, now, activeChannel); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) disconnectYouTube() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM youtube_uploads WHERE status IN ('queued','initiating','uploading','processing','needs_reconnect')`).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("finish or cancel outstanding uploads before disconnecting YouTube")
	}
	if _, err := tx.Exec(`UPDATE youtube_config SET encrypted_access_token='',encrypted_refresh_token='',access_token_expires_at=0,channel_id='',channel_title='',connection_version=connection_version+1,reconnect_required=0 WHERE id=1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) setYouTubeReconnectRequired(required bool) error {
	value := 0
	if required {
		value = 1
	}
	_, err := s.db.Exec(`UPDATE youtube_config SET reconnect_required=? WHERE id=1`, value)
	return err
}

func (s *Store) updateYouTubeTokens(connectionVersion int64, access, refresh string, expiresAt int64) (bool, error) {
	result, err := s.db.Exec(`UPDATE youtube_config SET encrypted_access_token=?,encrypted_refresh_token=?,access_token_expires_at=? WHERE id=1 AND connection_version=?`, access, refresh, expiresAt, connectionVersion)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) activeYouTubeUploadCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM youtube_uploads WHERE status IN ('queued','initiating','uploading','processing','needs_reconnect')`).Scan(&count)
	return count, err
}

const youtubeUploadColumns = `id,source_kind,source_id,channel_id,connection_version,title,description,requested_privacy_status,privacy_status,made_for_kids,contains_synthetic_media,status,progress,cancel_requested,error,youtube_video_id,processing_status,source_path,source_size,source_mtime_ns,encrypted_session_url,next_attempt_at,attempt_count,processing_check_count,created_at,updated_at`

func scanYouTubeUpload(row interface{ Scan(...any) error }) (YouTubeUpload, error) {
	var upload YouTubeUpload
	err := row.Scan(&upload.ID, &upload.SourceKind, &upload.SourceID, &upload.ChannelID, &upload.ConnectionVersion, &upload.Title, &upload.Description, &upload.RequestedPrivacyStatus, &upload.PrivacyStatus, &upload.MadeForKids, &upload.ContainsSyntheticMedia, &upload.Status, &upload.Progress, &upload.CancelRequested, &upload.Error, &upload.YouTubeVideoID, &upload.ProcessingStatus, &upload.SourcePath, &upload.SourceSize, &upload.SourceModTimeNS, &upload.EncryptedSessionURL, &upload.NextAttemptAt, &upload.AttemptCount, &upload.ProcessingCheckCount, &upload.CreatedAt, &upload.UpdatedAt)
	if upload.YouTubeVideoID != "" {
		upload.YouTubeURL = "https://www.youtube.com/watch?v=" + upload.YouTubeVideoID
	}
	return upload, err
}

func (s *Store) YouTubeUpload(id string) (YouTubeUpload, error) {
	return scanYouTubeUpload(s.db.QueryRow(`SELECT `+youtubeUploadColumns+` FROM youtube_uploads WHERE id=?`, id))
}

func (s *Store) YouTubeUploadsForSource(kind, id string) ([]YouTubeUpload, error) {
	rows, err := s.db.Query(`SELECT `+youtubeUploadColumns+` FROM youtube_uploads WHERE source_kind=? AND source_id=? ORDER BY created_at DESC,id DESC`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	uploads := make([]YouTubeUpload, 0)
	for rows.Next() {
		upload, err := scanYouTubeUpload(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	return uploads, rows.Err()
}

func (s *Store) pendingYouTubeUploads(now int64, limit int) ([]YouTubeUpload, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Query(`SELECT `+youtubeUploadColumns+` FROM youtube_uploads WHERE status IN ('queued','uploading','processing') AND next_attempt_at<=? ORDER BY created_at,id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	uploads := make([]YouTubeUpload, 0)
	for rows.Next() {
		upload, err := scanYouTubeUpload(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	return uploads, rows.Err()
}

func (s *Store) insertYouTubeUpload(upload YouTubeUpload) error {
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingID, existingStatus, existingError string
	err = tx.QueryRow(`SELECT id,status,error FROM youtube_uploads WHERE source_kind=? AND source_id=? AND channel_id=?`, upload.SourceKind, upload.SourceID, upload.ChannelID).Scan(&existingID, &existingStatus, &existingError)
	if err == nil {
		if existingStatus != youtubeUploadCanceled || existingError != "" {
			return errYouTubeUploadExists
		}
		result, updateErr := tx.Exec(`UPDATE youtube_uploads SET connection_version=?,title=?,description=?,requested_privacy_status=?,privacy_status=?,made_for_kids=?,contains_synthetic_media=?,status='queued',progress=0,cancel_requested=0,error='',youtube_video_id='',processing_status='',source_path=?,source_size=?,source_mtime_ns=?,encrypted_session_url='',next_attempt_at=0,attempt_count=0,processing_check_count=0,updated_at=? WHERE id=? AND status='canceled' AND error=''`, upload.ConnectionVersion, upload.Title, upload.Description, upload.RequestedPrivacyStatus, upload.PrivacyStatus, upload.MadeForKids, upload.ContainsSyntheticMedia, upload.SourcePath, upload.SourceSize, upload.SourceModTimeNS, now, existingID)
		err = updateErr
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return errYouTubeUploadExists
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO youtube_uploads(id,source_kind,source_id,channel_id,connection_version,title,description,requested_privacy_status,privacy_status,made_for_kids,contains_synthetic_media,status,source_path,source_size,source_mtime_ns,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?)`, upload.ID, upload.SourceKind, upload.SourceID, upload.ChannelID, upload.ConnectionVersion, upload.Title, upload.Description, upload.RequestedPrivacyStatus, upload.PrivacyStatus, upload.MadeForKids, upload.ContainsSyntheticMedia, upload.SourcePath, upload.SourceSize, upload.SourceModTimeNS, now, now)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
		return errYouTubeUploadExists
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) claimYouTubeUpload(id string, from, to, message string, now int64) (bool, error) {
	result, err := s.db.Exec(`UPDATE youtube_uploads SET status=?,error=?,updated_at=? WHERE id=? AND status=?`, to, message, now, id, from)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) setYouTubeUploadSession(id, encryptedSession string, now int64) error {
	_, err := s.db.Exec(`UPDATE youtube_uploads SET encrypted_session_url=?,status='uploading',progress=0,error='',attempt_count=0,next_attempt_at=0,updated_at=? WHERE id=? AND status='initiating'`, encryptedSession, now, id)
	return err
}

func (s *Store) setYouTubeUploadProgress(id string, progress int, nextAttempt int64, incrementAttempt bool, now int64) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 99 {
		progress = 99
	}
	increment := 0
	if incrementAttempt {
		increment = 1
	}
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status='uploading',progress=?,next_attempt_at=?,attempt_count=attempt_count+?,updated_at=? WHERE id=? AND status='uploading'`, progress, nextAttempt, increment, now, id)
	return err
}

func (s *Store) setYouTubeUploadProcessing(id, videoID, privacy, processingStatus string, nextAttempt int64, now int64) error {
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status='processing',youtube_video_id=?,privacy_status=CASE WHEN ? IN ('private','unlisted','public') THEN ? ELSE privacy_status END,processing_status=?,progress=100,cancel_requested=0,error='',next_attempt_at=?,updated_at=? WHERE id=? AND status IN ('uploading','processing')`, videoID, privacy, privacy, processingStatus, nextAttempt, now, id)
	return err
}

func (s *Store) updateYouTubeProcessing(id, status, rejection, failure string, checks int, nextAttempt int64, now int64) error {
	message := ""
	if rejection != "" {
		message = "YouTube rejected video processing: " + rejection
	} else if failure != "" {
		message = "YouTube video processing failed: " + failure
	}
	if status == youtubeUploadCompleted {
		message = ""
	}
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status=?,processing_status=?,processing_check_count=?,next_attempt_at=?,error=?,updated_at=? WHERE id=? AND status='processing'`, status, strings.TrimSpace(rejection+" "+failure), checks, nextAttempt, message, now, id)
	return err
}

func (s *Store) failYouTubeUpload(id, status, message string, now int64) error {
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status=?,error=?,next_attempt_at=0,updated_at=? WHERE id=? AND status IN ('queued','initiating','uploading','processing','needs_reconnect')`, status, message, now, id)
	return err
}

func (s *Store) retryYouTubeUpload(id string, now int64) (YouTubeUpload, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return YouTubeUpload{}, err
	}
	defer tx.Rollback()
	upload, err := scanYouTubeUpload(tx.QueryRow(`SELECT `+youtubeUploadColumns+` FROM youtube_uploads WHERE id=?`, id))
	if err != nil {
		return YouTubeUpload{}, err
	}
	if upload.Status != youtubeUploadFailed || upload.YouTubeVideoID != "" {
		return YouTubeUpload{}, errors.New("this upload cannot be retried safely; check YouTube Studio before creating another upload")
	}
	status := youtubeUploadQueued
	if upload.EncryptedSessionURL != "" {
		status = youtubeUploadSending
	}
	_, err = tx.Exec(`UPDATE youtube_uploads SET status=?,progress=0,error='',attempt_count=0,next_attempt_at=0,cancel_requested=0,updated_at=? WHERE id=? AND status='failed' AND youtube_video_id=''`, status, now, id)
	if err != nil {
		return YouTubeUpload{}, err
	}
	if err := tx.Commit(); err != nil {
		return YouTubeUpload{}, err
	}
	upload.Status = status
	upload.Progress = 0
	upload.Error = ""
	upload.AttemptCount = 0
	upload.NextAttemptAt = 0
	upload.CancelRequested = false
	return upload, nil
}

func (s *Store) cancelYouTubeUpload(id string, now int64) (YouTubeUpload, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return YouTubeUpload{}, err
	}
	defer tx.Rollback()
	upload, err := scanYouTubeUpload(tx.QueryRow(`SELECT `+youtubeUploadColumns+` FROM youtube_uploads WHERE id=?`, id))
	if err != nil {
		return YouTubeUpload{}, err
	}
	switch upload.Status {
	case youtubeUploadQueued:
		_, err = tx.Exec(`UPDATE youtube_uploads SET status='canceled',error='',next_attempt_at=0,cancel_requested=0,updated_at=? WHERE id=? AND status='queued'`, now, id)
		upload.Status = youtubeUploadCanceled
	case youtubeUploadInitiating, youtubeUploadSending:
		_, err = tx.Exec(`UPDATE youtube_uploads SET cancel_requested=1,updated_at=? WHERE id=? AND status=?`, now, id, upload.Status)
		upload.CancelRequested = true
	case youtubeUploadCanceled:
		return upload, tx.Commit()
	case youtubeUploadNeedsReconnect, youtubeUploadAttentionRequired:
		_, err = tx.Exec(`UPDATE youtube_uploads SET status='canceled',error='Upload was abandoned locally. Check YouTube Studio before creating another upload.',next_attempt_at=0,cancel_requested=0,updated_at=? WHERE id=? AND status=?`, now, id, upload.Status)
		upload.Status = youtubeUploadCanceled
		upload.Error = "Upload was abandoned locally. Check YouTube Studio before creating another upload."
	default:
		return YouTubeUpload{}, errors.New("this upload can no longer be canceled")
	}
	if err != nil {
		return YouTubeUpload{}, err
	}
	if err := tx.Commit(); err != nil {
		return YouTubeUpload{}, err
	}
	return upload, nil
}

func (s *Store) completeYouTubeUpload(id string, videoID, privacy string, now int64) error {
	if videoID == "" {
		return errors.New("YouTube response did not include a video id")
	}
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status='processing',youtube_video_id=?,privacy_status=CASE WHEN ? IN ('private','unlisted','public') THEN ? ELSE privacy_status END,processing_status='uploaded',progress=100,cancel_requested=0,error='',next_attempt_at=?,updated_at=? WHERE id=? AND status IN ('uploading','processing')`, videoID, privacy, privacy, now+10, now, id)
	return err
}

func (s *Store) unknownYouTubeUploadOutcome(id, message string, now int64) error {
	_, err := s.db.Exec(`UPDATE youtube_uploads SET status='attention_required',error=?,next_attempt_at=0,updated_at=? WHERE id=? AND youtube_video_id='' AND status IN ('initiating','uploading')`, message, now, id)
	return err
}

func (s *Store) activeYouTubeUploadForSource(kind, id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM youtube_uploads WHERE source_kind=? AND source_id=? AND status IN ('queued','initiating','uploading','needs_reconnect','attention_required')`, kind, id).Scan(&count)
	return count != 0, err
}

func constantStringEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
