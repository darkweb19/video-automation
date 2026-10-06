package app

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type youtubeSource struct {
	Kind      string
	ID        string
	Title     string
	Topic     string
	Story     string
	Prompt    string
	Model     string
	Path      string
	InVault   bool
	Size      int64
	ModTimeNS int64
}

type youtubeUploadRequest struct {
	SourceKind             string `json:"source_kind"`
	SourceID               string `json:"source_id"`
	ChannelID              string `json:"channel_id"`
	Title                  string `json:"title"`
	Description            string `json:"description"`
	PrivacyStatus          string `json:"privacy_status"`
	MadeForKids            *bool  `json:"made_for_kids"`
	ContainsSyntheticMedia *bool  `json:"contains_synthetic_media"`
}

func (a *dashboardApp) youtubeSource(r *http.Request, kind, id string) (youtubeSource, error) {
	if !safeID(id) || (kind != "generation" && kind != "project") {
		return youtubeSource{}, sql.ErrNoRows
	}
	var source youtubeSource
	source.Kind, source.ID = kind, id
	if kind == "generation" {
		record, err := a.store.Generation(id)
		if err != nil {
			return youtubeSource{}, err
		}
		if record.Status != "completed" || !record.VideoReady {
			return youtubeSource{}, ErrYouTubeSourceNotReady
		}
		source.Title, source.Prompt, source.Model = record.Prompt, record.Prompt, record.Model
		source.Path, source.InVault = record.VideoPath, record.InVault
		source.Size = record.SizeBytes
	} else {
		project, err := a.store.projectForWorker(r.Context(), id)
		if err != nil {
			return youtubeSource{}, err
		}
		if project.Status != "completed" || !project.FinalVideoReady {
			return youtubeSource{}, ErrYouTubeSourceNotReady
		}
		source.Title, source.Topic, source.Story = project.Title, project.Topic, project.Story
		if source.Title == "" {
			source.Title = project.Topic
		}
		source.Path, source.InVault, source.Size = project.FinalVideoPath, project.InVault, project.FinalSizeBytes
		source.Model = project.Model
	}
	baseDir := a.store.videoDir
	if kind == "project" {
		baseDir = a.store.projectDir
	}
	clean := filepath.Clean(source.Path)
	rel, err := filepath.Rel(baseDir, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return youtubeSource{}, errors.New("stored YouTube source path is invalid")
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return youtubeSource{}, ErrYouTubeSourceNotReady
	}
	source.Path, source.Size, source.ModTimeNS = clean, info.Size(), info.ModTime().UnixNano()
	return source, nil
}

func (a *dashboardApp) requireYouTubeSource(w http.ResponseWriter, r *http.Request, kind, id string) (youtubeSource, bool) {
	source, err := a.youtubeSource(r, kind, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "video not found")
		return youtubeSource{}, false
	}
	if errors.Is(err, ErrYouTubeSourceNotReady) {
		writeError(w, http.StatusConflict, "Only completed videos can be uploaded to YouTube")
		return youtubeSource{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load video")
		return youtubeSource{}, false
	}
	if source.InVault && !a.vaultGrantValid(r) {
		writeError(w, http.StatusLocked, "Unlock the Vault to continue")
		return youtubeSource{}, false
	}
	return source, true
}

func (a *dashboardApp) youtubeUploads(w http.ResponseWriter, r *http.Request) {
	kind, id := singleQueryValue(r, "source_kind"), singleQueryValue(r, "source_id")
	if kind == "" || id == "" {
		writeError(w, http.StatusBadRequest, "source_kind and source_id are required")
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	source, ok := a.requireYouTubeSource(w, r, kind, id)
	if !ok {
		return
	}
	_ = source
	uploads, err := a.store.YouTubeUploadsForSource(kind, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube uploads")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": uploads})
}

func (a *dashboardApp) createYouTubeUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input youtubeUploadRequest
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.MadeForKids == nil || input.ContainsSyntheticMedia == nil {
		writeError(w, http.StatusBadRequest, "Choose both audience and synthetic-media disclosures")
		return
	}
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	if input.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "channel_id is required; refresh the connected channel and try again")
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if err := validateYouTubeTitle(input.Title); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateYouTubeDescription(input.Description); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.PrivacyStatus != "private" && input.PrivacyStatus != "unlisted" && input.PrivacyStatus != "public" {
		writeError(w, http.StatusBadRequest, "Choose private, unlisted, or public visibility")
		return
	}
	if input.SourceKind == "" || input.SourceID == "" {
		writeError(w, http.StatusBadRequest, "source_kind and source_id are required")
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	source, ok := a.requireYouTubeSource(w, r, input.SourceKind, input.SourceID)
	if !ok {
		return
	}
	config, err := a.store.youtubeConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube settings")
		return
	}
	if config.ChannelID == "" || config.EncryptedRefreshToken == "" || config.ReconnectRequired {
		writeError(w, http.StatusConflict, "Connect a YouTube channel before starting an upload")
		return
	}
	if input.ChannelID != config.ChannelID {
		writeError(w, http.StatusConflict, "The connected YouTube channel changed. Reopen the upload details.")
		return
	}
	id, err := newYouTubeUploadID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to queue YouTube upload")
		return
	}
	upload := YouTubeUpload{
		ID: id, SourceKind: source.Kind, SourceID: source.ID, ChannelID: input.ChannelID,
		ConnectionVersion: config.ConnectionVersion, Title: input.Title, Description: input.Description,
		RequestedPrivacyStatus: input.PrivacyStatus, PrivacyStatus: input.PrivacyStatus,
		MadeForKids: *input.MadeForKids, ContainsSyntheticMedia: *input.ContainsSyntheticMedia,
		Status: youtubeUploadQueued, SourcePath: source.Path, SourceInVault: source.InVault,
		SourceSize: source.Size, SourceModTimeNS: source.ModTimeNS,
	}
	if err := a.store.createYouTubeUploadLocked(upload); err != nil {
		switch {
		case errors.Is(err, errYouTubeUploadExists):
			writeError(w, http.StatusConflict, "A YouTube upload already exists for this video and channel")
		case errors.Is(err, ErrYouTubeSourceNotReady):
			writeError(w, http.StatusConflict, "Only completed videos can be uploaded to YouTube")
		case errors.Is(err, ErrYouTubeSourceChanged):
			writeError(w, http.StatusConflict, "The video moved or changed while the upload was being queued. Refresh and try again.")
		case strings.Contains(err.Error(), "connect the selected YouTube channel"):
			writeError(w, http.StatusConflict, "The connected YouTube channel changed. Refresh and connect the intended channel.")
		default:
			writeError(w, http.StatusInternalServerError, "unable to queue YouTube upload")
		}
		return
	}
	created, err := a.store.YouTubeUpload(upload.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload was queued but could not be loaded")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"upload": created})
}

func (a *dashboardApp) youtubeUploadByID(w http.ResponseWriter, r *http.Request) (YouTubeUpload, bool) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid YouTube upload ID")
		return YouTubeUpload{}, false
	}
	upload, err := a.store.YouTubeUpload(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "YouTube upload not found")
		return YouTubeUpload{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube upload")
		return YouTubeUpload{}, false
	}
	if _, ok := a.requireYouTubeSource(w, r, upload.SourceKind, upload.SourceID); !ok {
		return YouTubeUpload{}, false
	}
	return upload, true
}

func (a *dashboardApp) youtubeUpload(w http.ResponseWriter, r *http.Request) {
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	upload, ok := a.youtubeUploadByID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upload": upload})
}

func (a *dashboardApp) retryYouTubeUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	upload, ok := a.youtubeUploadByID(w, r)
	if !ok {
		return
	}
	updated, err := a.store.retryYouTubeUploadLocked(upload.ID, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "cannot be retried safely") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "unable to retry YouTube upload")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"upload": updated})
}

func (a *dashboardApp) cancelYouTubeUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	upload, ok := a.youtubeUploadByID(w, r)
	if !ok {
		return
	}
	updated, err := a.store.cancelYouTubeUploadLocked(upload.ID, time.Now().Unix())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	status := http.StatusOK
	if updated.CancelRequested {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"upload": updated})
}

func (a *dashboardApp) restartYouTubeUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		ConfirmNotUploaded bool `json:"confirm_not_uploaded"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !input.ConfirmNotUploaded {
		writeError(w, http.StatusBadRequest, "Confirm that you checked YouTube Studio and understand the duplicate-upload risk")
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	defer unlock()
	upload, ok := a.youtubeUploadByID(w, r)
	if !ok {
		return
	}
	source, ok := a.requireYouTubeSource(w, r, upload.SourceKind, upload.SourceID)
	if !ok {
		return
	}
	updated, err := a.store.restartYouTubeUploadLocked(upload.ID, source, time.Now().Unix())
	if err != nil {
		message := err.Error()
		if errors.Is(err, ErrYouTubeSourceNotReady) {
			message = "Only completed videos can be uploaded to YouTube"
		} else if errors.Is(err, ErrYouTubeSourceChanged) {
			message = "The source video changed. Refresh the upload details before restarting."
		} else if strings.Contains(message, "YouTube channel") {
			message = "Reconnect the same YouTube channel before restarting this upload"
		} else if strings.Contains(message, "cannot be restarted") {
			message = "This upload cannot be restarted safely. Check YouTube Studio before creating another upload."
		} else if !strings.Contains(message, "changed before") {
			message = "Unable to restart this YouTube upload safely"
		}
		writeError(w, http.StatusConflict, message)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"upload": updated})
}

func (a *dashboardApp) lockYouTubeSourceAccess() func() {
	a.store.youtubeMu.Lock()
	if a.vault != nil {
		a.vault.verifyMu.Lock()
	}
	return func() {
		if a.vault != nil {
			a.vault.verifyMu.Unlock()
		}
		a.store.youtubeMu.Unlock()
	}
}

func validateYouTubeTitle(title string) error {
	if !utf8.ValidString(title) || title == "" {
		return errors.New("Enter a valid video title")
	}
	if utf8.RuneCountInString(title) > 100 {
		return errors.New("Title must be 100 characters or fewer")
	}
	if strings.ContainsAny(title, "<>") || containsControlRune(title) {
		return errors.New("Title cannot contain angle brackets or control characters")
	}
	return nil
}

func validateYouTubeDescription(description string) error {
	if !utf8.ValidString(description) {
		return errors.New("Description must be valid UTF-8")
	}
	if len([]byte(description)) > 5000 {
		return errors.New("Description must be 5,000 UTF-8 bytes or fewer")
	}
	if strings.ContainsAny(description, "<>") {
		return errors.New("Description cannot contain angle brackets")
	}
	return nil
}

func containsControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
