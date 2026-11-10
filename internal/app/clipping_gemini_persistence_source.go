package app

import (
	"database/sql"
	"errors"
	"time"
)

// CreateClippingYouTubeReference stores a rights-attested public video URL
// without acquiring media. Ready means the reference is accepted, not that
// Gemini or YouTube has verified the video or its duration.
func (s *Store) CreateClippingYouTubeReference(security *Security, rawURL string, rightsAttested bool, retainUntil int64) (ClippingSource, error) {
	if s == nil || security == nil || security.store != s {
		return ClippingSource{}, errors.New("source URL encryption is unavailable")
	}
	if !rightsAttested {
		return ClippingSource{}, ErrClippingRightsNotAttested
	}
	now := time.Now().Unix()
	if retainUntil < 0 || retainUntil > 0 && retainUntil <= now {
		return ClippingSource{}, errors.New("source retention must be in the future")
	}
	canonicalURL, err := canonicalClippingGeminiYouTubeURL(rawURL)
	if err != nil {
		return ClippingSource{}, err
	}
	id, err := newClippingStorageID("clipsrc_")
	if err != nil {
		return ClippingSource{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingSource{}, err
	}
	defer tx.Rollback()
	source, err := insertClippingYouTubeReferenceTx(tx, security, id, canonicalURL, retainUntil, now)
	if err != nil {
		return ClippingSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingSource{}, err
	}
	return source, nil
}

// insertClippingYouTubeReferenceTx is also the atomic URL-and-job creation
// seam. The caller validates rights and retention and performs idempotency
// lookup before invoking it. It never stores plaintext URL or media metadata.
func insertClippingYouTubeReferenceTx(tx *sql.Tx, security *Security, id, canonicalURL string, retainUntil, now int64) (ClippingSource, error) {
	validated, err := canonicalClippingGeminiYouTubeURL(canonicalURL)
	if err != nil || validated != canonicalURL || !safeClippingSourceID(id) {
		return ClippingSource{}, ErrClippingGeminiInvalidRequest
	}
	encryptedURL, err := security.EncryptSetting(clippingSourceURLSetting, canonicalURL)
	if err != nil {
		return ClippingSource{}, errors.New("could not protect public source URL")
	}
	name := "youtube-" + canonicalURL[len(canonicalURL)-11:]
	_, err = tx.Exec(`INSERT INTO clipping_sources(id,kind,status,original_name,source_url,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,size_bytes,duration_ms,sha256,retain_until,created_at,updated_at)
		VALUES(?,'youtube_url','ready',?,?,1,NULL,NULL,0,NULL,NULL,NULL,?,?,?)`, id, name, encryptedURL, retainUntil, now, now)
	if err != nil {
		return ClippingSource{}, err
	}
	return ClippingSource{ID: id, Kind: ClippingSourceYouTubeURL, Status: ClippingSourceReady,
		OriginalName: name, SourceURL: encryptedURL, RightsAttested: true, RetainUntil: retainUntil, CreatedAt: now, UpdatedAt: now}, nil
}
