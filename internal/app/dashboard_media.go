package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *dashboardApp) projectVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := a.store.projectForWorker(r.Context(), id)
	if err != nil || project.InVault || project.Status != "completed" || !project.FinalVideoReady {
		writeError(w, http.StatusNotFound, "final video not found")
		return
	}
	clean := filepath.Clean(project.FinalVideoPath)
	rel, err := filepath.Rel(a.store.projectDir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, "invalid final video path")
		return
	}
	file, err := os.Open(clean)
	if err != nil {
		writeError(w, http.StatusNotFound, "final video not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read final video")
		return
	}
	serveVersionedVideo(w, r, file, info, "project", id, clean, "video/mp4", fmt.Sprintf("project-%s.mp4", id))
}

func (a *dashboardApp) deleteGeneration(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid generation id")
		return
	}
	record, err := a.store.BeginDelete(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to delete generation")
		return
	}
	if record.VideoPath != "" {
		if err := a.removeVideo(record.VideoPath); err != nil {
			a.store.CancelDelete(id, record.Status)
			writeError(w, http.StatusInternalServerError, "unable to remove video file")
			return
		}
	}
	if err := a.store.FinishDelete(id); err != nil {
		a.store.CancelDelete(id, record.Status)
		writeError(w, http.StatusInternalServerError, "unable to delete generation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) removeVideo(path string) error {
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(a.store.videoDir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("invalid video path")
	}
	err = os.Remove(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (a *dashboardApp) video(w http.ResponseWriter, r *http.Request) {
	id, ok := oneSafeID(w, r)
	if !ok {
		return
	}
	record, err := a.store.Generation(id)
	if err != nil || record.InVault || record.Status != "completed" || record.VideoPath == "" {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	cleanPath := filepath.Clean(record.VideoPath)
	rel, err := filepath.Rel(a.store.videoDir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, "invalid video path")
		return
	}
	file, err := os.Open(cleanPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read video")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = "video/mp4"
	}
	serveVersionedVideo(w, r, file, info, "generation", id, cleanPath, contentType, fmt.Sprintf("generation-%s.mp4", id))
}

// serveVersionedVideo permits the browser to keep a private copy and revalidate
// it on reuse. The callers authenticate first, load the current record, and
// check Vault membership before reaching ServeContent, so its conditional 304
// and range responses cannot bypass authorization or the current file state.
func serveVersionedVideo(w http.ResponseWriter, r *http.Request, file *os.File, info os.FileInfo, kind, id, cleanPath, contentType, downloadName string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", videoFileETag(kind, id, cleanPath, info))
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, filepath.Base(cleanPath), info.ModTime(), file)
}

// videoFileETag binds the validator to a single stored media item and its
// current on-disk version. Nanosecond modification time distinguishes retries
// that replace final.mp4 quickly, even when the replacement has the same size.
func videoFileETag(kind, id, cleanPath string, info os.FileInfo) string {
	identity := strings.Join([]string{
		kind,
		id,
		filepath.Clean(cleanPath),
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return `"` + hex.EncodeToString(digest[:]) + `"`
}
