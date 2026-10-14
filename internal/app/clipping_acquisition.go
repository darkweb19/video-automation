package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ClippingUploadChunkBytes           int64 = 16 << 20
	clippingImportReadBytes                  = int(ClippingUploadChunkBytes)
	clippingImportMaxDuration                = 12 * time.Hour
	clippingImportLeaseDuration              = 15 * time.Minute
	clippingImportLeaseRenewInterval         = 5 * time.Minute
	clippingImportMaxRedirects               = 5
	clippingImportResponseTimeout            = 30 * time.Second
	clippingFreeSpaceFloorBytes              = int64(4 << 30)
	clippingYouTubeTrackWorkspaceBytes       = MaxClippingSourceBytes
	clippingYouTubeMuxTimeout                = 3 * time.Hour
	clippingYouTubeDurationToleranceMS int64 = 2_000
	clippingProbeTimeout                     = 15 * time.Second
	clippingProbeOutputLimit                 = 64 << 10
	clippingProbeMaxAllocation               = 64 << 20
	clippingDecoderTimeout                   = 5 * time.Second
	clippingDecoderOutputLimit               = 2 << 20
	clippingMediaMinDimension                = 144
	clippingMediaMaxDimension                = 8192
	clippingMediaMaxPixels                   = 50_000_000
)

var (
	ErrClippingUploadChunkTooLarge     = errors.New("upload chunk exceeds 16 MiB")
	ErrClippingUploadOffset            = errors.New("upload offset does not match the persisted offset")
	ErrClippingUploadIncomplete        = errors.New("upload is incomplete")
	ErrClippingRightsNotAttested       = errors.New("explicit rights and processing attestation is required")
	ErrClippingPlatformUnavailable     = errors.New("native clipping source files require a Linux or Unix host; run the application in Docker/Linux")
	ErrYouTubeImportUnavailable        = errors.New("YouTube import is unavailable; upload an original file you are permitted to reuse")
	errClippingInvalidMedia            = errors.New("source is not a supported video with a valid duration and dimensions")
	errClippingImportRetryable         = errors.New("public media import failed")
	errClippingYouTubeAVStreamMissing  = errors.New("YouTube source is missing required audio/video stream timing")
	errClippingYouTubeAVTimingMismatch = errors.New("YouTube source audio/video timing does not match")
)

var clippingDecoderCache sync.Map // executable identity -> map[codec]struct{}

const clippingSourceURLSetting = "clipping_source_url"

type ClippingAcquisitionError struct {
	Status  string
	Reason  string
	Message string
}

func (failure *ClippingAcquisitionError) Error() string {
	if failure == nil || failure.Message == "" {
		return "source acquisition failed"
	}
	return failure.Message
}

func clippingOutcome(status, reason, message string) error {
	return &ClippingAcquisitionError{Status: status, Reason: reason, Message: message}
}

type clippingIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type clippingTransportFactory func(*url.URL, []net.IP) http.RoundTripper

type clippingMediaProbeResult struct {
	DurationMS       int64
	Width            int
	Height           int
	AudioStreams     int
	VideoCodec       string
	AudioCodec       string
	VideoStartMS     int64
	AudioStartMS     int64
	VideoDurationMS  int64
	AudioDurationMS  int64
	VideoTimingValid bool
	AudioTimingValid bool
	VideoFPS         float64
	VideoFPSValid    bool
}

type clippingMediaProbe func(context.Context, string) (clippingMediaProbeResult, error)

type clippingActiveOperation struct {
	cancel context.CancelFunc
	closer io.Closer
	done   chan struct{}
}

type clippingOperationRegistry struct {
	mu         sync.Mutex
	operations map[string]clippingActiveOperation
}

// ClippingAcquisition owns the bounded local and public-link media adapters.
// Its Store remains the source of truth for source state and upload offsets.
type ClippingAcquisition struct {
	store    *Store
	security *Security

	resolver                clippingIPResolver
	transportFactory        clippingTransportFactory
	probe                   clippingMediaProbe
	now                     func() time.Time
	diskAvailable           func(string) (int64, error)
	leaseRenewEvery         time.Duration
	youtubeExecutable       string
	youtubeRuntime          string
	youtubeMaxBytes         int64
	youtubeFFmpegExecutable string

	processImportMu sync.Mutex
	locksMu         sync.Mutex
	sourceLocks     map[string]*clippingSourceLock
}

type clippingSourceLock struct {
	mu   sync.Mutex
	refs int
}

func NewClippingAcquisition(store *Store, security *Security) *ClippingAcquisition {
	return &ClippingAcquisition{
		store:            store,
		security:         security,
		resolver:         net.DefaultResolver,
		transportFactory: newPinnedClippingTransport,
		probe:            probeClippingMedia,
		now:              time.Now,
		diskAvailable:    clippingAvailableDiskBytes,
		leaseRenewEvery:  clippingImportLeaseRenewInterval,
		sourceLocks:      make(map[string]*clippingSourceLock),
	}
}

// CreateUpload reserves its declared byte size before accepting any body data.
func (a *ClippingAcquisition) CreateUpload(ctx context.Context, rightsAttested bool, originalName, mediaType string, declaredSize, retainUntil int64) (ClippingSource, error) {
	if err := ctx.Err(); err != nil {
		return ClippingSource{}, err
	}
	if a == nil || a.store == nil {
		return ClippingSource{}, errors.New("clipping source storage is unavailable")
	}
	if !rightsAttested {
		return ClippingSource{}, clippingOutcome("authorization_needed", "reuse_rights_not_attested", ErrClippingRightsNotAttested.Error())
	}
	if err := clippingFilesystemSupported(); err != nil {
		return ClippingSource{}, err
	}
	if declaredSize <= 0 || declaredSize > MaxClippingSourceBytes {
		return ClippingSource{}, fmt.Errorf("upload size must be between 1 byte and %d bytes", MaxClippingSourceBytes)
	}
	if retainUntil < 0 {
		return ClippingSource{}, errors.New("retention timestamp cannot be negative")
	}
	if mediaType != "" {
		if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
			mediaType = parsed
		} else {
			mediaType = "application/octet-stream"
		}
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := a.checkClippingStorageCapacity(declaredSize); err != nil {
		return ClippingSource{}, err
	}
	source, err := a.store.CreateClippingSource(ClippingSourceCreate{
		Kind:              ClippingSourceUpload,
		OriginalName:      sanitizeClippingSourceName(originalName),
		MediaType:         mediaType,
		DeclaredSizeBytes: declaredSize,
		RightsAttested:    true,
		RetainUntil:       retainUntil,
	})
	if err != nil {
		return ClippingSource{}, err
	}
	if _, err := a.ensureSourcePaths(source.ID); err != nil {
		_ = a.store.FailClippingSource(source.ID, "storage_path_unavailable")
		a.store.PublishClippingSource(source.ID)
		return ClippingSource{}, err
	}
	a.store.PublishClippingSource(source.ID)
	return source, nil
}

// CreatePublicImport accepts only HTTPS public links and stores their
// normalized reference encrypted until the bounded import worker consumes it.
func (a *ClippingAcquisition) CreatePublicImport(ctx context.Context, rightsAttested bool, rawURL string, retainUntil int64) (ClippingSource, error) {
	if err := ctx.Err(); err != nil {
		return ClippingSource{}, err
	}
	if a == nil || a.store == nil {
		return ClippingSource{}, errors.New("clipping source storage is unavailable")
	}
	if !rightsAttested {
		return ClippingSource{}, clippingOutcome("authorization_needed", "reuse_rights_not_attested", ErrClippingRightsNotAttested.Error())
	}
	if err := clippingFilesystemSupported(); err != nil {
		return ClippingSource{}, err
	}
	if a.security == nil {
		return ClippingSource{}, errors.New("source URL encryption is unavailable")
	}
	if retainUntil < 0 {
		return ClippingSource{}, errors.New("retention timestamp cannot be negative")
	}
	u, kind, err := normalizeClippingImportURL(rawURL)
	if err != nil {
		return ClippingSource{}, err
	}
	// The encrypted value expands by roughly one third. Keep it within the
	// storage layer's ciphertext field limit before reserving aggregate space.
	if len(u.String()) > 3000 {
		return ClippingSource{}, clippingOutcome("unsupported", "invalid_url", "The public share URL is too long")
	}
	encryptedURL, err := a.security.EncryptSetting(clippingSourceURLSetting, u.String())
	if err != nil {
		return ClippingSource{}, errors.New("could not protect public source URL")
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := a.checkClippingStorageCapacity(MaxClippingSourceBytes); err != nil {
		return ClippingSource{}, err
	}
	originalName := clippingURLName(u)
	if kind == ClippingSourceYouTube {
		videoID, ok := youtubeVideoID(u)
		if !ok {
			return ClippingSource{}, clippingOutcome("unsupported", "invalid_url", "Enter a supported YouTube video reference")
		}
		originalName = "youtube-" + videoID + ".media"
	}
	source, err := a.store.CreateClippingSource(ClippingSourceCreate{
		Kind:              kind,
		OriginalName:      originalName,
		MediaType:         "application/octet-stream",
		DeclaredSizeBytes: MaxClippingSourceBytes,
		SourceURL:         encryptedURL,
		RightsAttested:    true,
		RetainUntil:       retainUntil,
	})
	if err != nil {
		return ClippingSource{}, err
	}
	if _, err := a.ensureSourcePaths(source.ID); err != nil {
		trace := newClippingDiagnosticTrace(a, source.ID)
		trace.record(ClippingDiagnosticStageURLValidation, ClippingDiagnosticCompleted, "")
		trace.record(ClippingDiagnosticStageReservation, ClippingDiagnosticFailed, "source_unavailable")
		_ = a.store.FailClippingSource(source.ID, "storage_path_unavailable")
		a.store.PublishClippingSource(source.ID)
		return ClippingSource{}, err
	}
	trace := newClippingDiagnosticTrace(a, source.ID)
	trace.record(ClippingDiagnosticStageURLValidation, ClippingDiagnosticCompleted, "")
	trace.record(ClippingDiagnosticStageReservation, ClippingDiagnosticCompleted, "")
	trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticQueued, "")
	if updated, readErr := a.store.ClippingSource(source.ID); readErr == nil {
		source = updated
	} else {
		a.store.PublishClippingSource(source.ID)
	}
	return source, nil
}

// UploadChunk appends one persisted, resumable chunk to a source upload.
// The request body is streamed directly to disk and never accumulated whole.
func (a *ClippingAcquisition) UploadChunk(ctx context.Context, sourceID string, expectedOffset int64, body io.Reader) (int64, error) {
	if a == nil || a.store == nil {
		return 0, errors.New("clipping source storage is unavailable")
	}
	if err := clippingFilesystemSupported(); err != nil {
		return 0, err
	}
	if body == nil {
		return 0, errors.New("upload body is required")
	}
	if expectedOffset < 0 {
		return 0, ErrClippingUploadOffset
	}
	if expectedOffset > MaxClippingSourceBytes {
		return 0, ErrClippingUploadOffset
	}
	opCtx, finish, err := a.beginOperation(ctx, sourceID, asCloser(body))
	if err != nil {
		return 0, err
	}
	defer finish()
	releaseSource := a.lockSource(sourceID)
	defer releaseSource()
	if err := opCtx.Err(); err != nil {
		return 0, err
	}

	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return 0, err
	}
	if source.Status != ClippingSourceUploading {
		return 0, fmt.Errorf("source is not accepting upload chunks")
	}
	if expectedOffset != source.UploadOffset {
		return source.UploadOffset, ErrClippingUploadOffset
	}
	if expectedOffset > source.DeclaredSizeBytes || source.DeclaredSizeBytes > MaxClippingSourceBytes {
		return source.UploadOffset, errors.New("upload exceeds its reserved size")
	}
	paths, err := a.sourcePaths(sourceID)
	if err != nil {
		return source.UploadOffset, err
	}
	if err := a.checkClippingStorageCapacity(0); err != nil {
		return source.UploadOffset, err
	}
	file, err := openClippingPart(paths.temp)
	if err != nil {
		return source.UploadOffset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return source.UploadOffset, err
	}
	if info.Size() < source.UploadOffset {
		return source.UploadOffset, errors.New("stored upload is shorter than its persisted offset")
	}
	if info.Size() > source.UploadOffset {
		if err := file.Truncate(source.UploadOffset); err != nil {
			return source.UploadOffset, err
		}
		if err := file.Sync(); err != nil {
			return source.UploadOffset, err
		}
	}
	if _, err := file.Seek(source.UploadOffset, io.SeekStart); err != nil {
		return source.UploadOffset, err
	}
	limit := source.DeclaredSizeBytes - source.UploadOffset
	if limit > ClippingUploadChunkBytes {
		limit = ClippingUploadChunkBytes
	}
	written, copyErr := copyContextLimit(opCtx, file, body, limit+1)
	if copyErr != nil || written > ClippingUploadChunkBytes || written > limit {
		_ = file.Truncate(source.UploadOffset)
		_ = file.Sync()
		if copyErr != nil {
			return source.UploadOffset, copyErr
		}
		return source.UploadOffset, ErrClippingUploadChunkTooLarge
	}
	if written == 0 {
		return source.UploadOffset, errors.New("upload chunk is empty")
	}
	if err := opCtx.Err(); err != nil {
		_ = file.Truncate(source.UploadOffset)
		_ = file.Sync()
		return source.UploadOffset, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(source.UploadOffset)
		_ = file.Sync()
		return source.UploadOffset, err
	}
	newOffset := source.UploadOffset + written
	if err := a.store.UpdateClippingSourceProgress(sourceID, ClippingSourceUploading, newOffset, newOffset); err != nil {
		_ = file.Truncate(source.UploadOffset)
		_ = file.Sync()
		return source.UploadOffset, err
	}
	a.store.PublishClippingSource(sourceID)
	return newOffset, nil
}

// FinalizeUpload validates the actual local bytes before atomically publishing
// the source path and changing its durable state to ready.
func (a *ClippingAcquisition) FinalizeUpload(ctx context.Context, sourceID string) (ClippingSource, error) {
	if a == nil || a.store == nil {
		return ClippingSource{}, errors.New("clipping source storage is unavailable")
	}
	if err := clippingFilesystemSupported(); err != nil {
		return ClippingSource{}, err
	}
	opCtx, finish, err := a.beginOperation(ctx, sourceID, nil)
	if err != nil {
		return ClippingSource{}, err
	}
	defer finish()
	releaseSource := a.lockSource(sourceID)
	defer releaseSource()
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return ClippingSource{}, err
	}
	if source.Status == ClippingSourceReady {
		return source, nil
	}
	if source.Status != ClippingSourceUploading {
		return ClippingSource{}, errors.New("source is not awaiting upload finalization")
	}
	if source.UploadOffset != source.DeclaredSizeBytes || source.DeclaredSizeBytes <= 0 {
		return ClippingSource{}, ErrClippingUploadIncomplete
	}
	result, err := a.finalizeSource(opCtx, sourceID, ClippingSourceUploading)
	if err != nil && errors.Is(err, errClippingInvalidMedia) {
		a.failAndRemove(sourceID, "invalid_media")
	}
	return result, err
}

// ProcessNextImport claims and runs one durable public-link import. It is
// synchronous by design; callers must invoke it from a background worker.
func (a *ClippingAcquisition) ProcessNextImport(ctx context.Context) (bool, error) {
	if a == nil || a.store == nil {
		return false, errors.New("clipping source storage is unavailable")
	}
	if err := clippingFilesystemSupported(); err != nil {
		return false, err
	}
	if !a.processImportMu.TryLock() {
		return false, nil
	}
	defer a.processImportMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	sources, err := a.store.PendingClippingImports(a.now().Unix())
	if err != nil {
		return false, err
	}
	if _, err := a.CleanupFailedSources(ctx); err != nil {
		return false, err
	}
	if len(sources) == 0 {
		return false, nil
	}
	source := sources[0]
	opCtx, finish, err := a.beginOperation(ctx, source.ID, nil)
	if err != nil {
		return true, err
	}
	defer finish()
	releaseSource := a.lockSource(source.ID)
	defer releaseSource()

	a.store.clippingMu.Lock()
	claimed, claimErr := a.store.BeginClippingImport(source.ID, source.ImportAttempts, clippingImportLeaseDuration)
	if claimErr != nil {
		a.store.clippingMu.Unlock()
		if errors.Is(claimErr, sql.ErrNoRows) {
			return true, nil
		}
		return true, claimErr
	}
	trace := newClippingDiagnosticTrace(a, claimed.ID)
	trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticStarted, "")
	paths, pathErr := a.ensureSourcePaths(claimed.ID)
	recoverFinalizedFile := false
	if pathErr == nil {
		if claimed.Kind == ClippingSourceYouTube {
			tempInfo, tempErr := os.Lstat(paths.temp)
			finalInfo, finalErr := os.Lstat(paths.final)
			if tempErr != nil && !errors.Is(tempErr, os.ErrNotExist) {
				pathErr = tempErr
			} else if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
				pathErr = finalErr
			} else if errors.Is(tempErr, os.ErrNotExist) && finalErr == nil && youtubeFinalExpectationMatches(claimed, finalInfo) {
				videoBytes, videoErr := safeGeneratedFileSize(paths.videoTrack)
				audioBytes, audioErr := safeGeneratedFileSize(paths.audioTrack)
				if videoErr != nil {
					pathErr = videoErr
				} else if audioErr != nil {
					pathErr = audioErr
				} else if videoBytes != 0 || audioBytes != 0 {
					pathErr = a.clearYouTubeImportArtifactsLocked(claimed.ID, paths, true)
				} else {
					recoverFinalizedFile = true
				}
			} else {
				// A temporary mux or track file has no durable completion marker.
				// Discard it and restart the selected import from clean paths.
				_ = tempInfo
				pathErr = a.clearYouTubeImportArtifactsLocked(claimed.ID, paths, true)
			}
		} else {
			tempInfo, tempErr := os.Lstat(paths.temp)
			finalInfo, finalErr := os.Lstat(paths.final)
			if tempErr != nil && !errors.Is(tempErr, os.ErrNotExist) {
				pathErr = tempErr
			} else if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
				pathErr = finalErr
			} else if tempErr == nil && finalErr == nil {
				pathErr = errors.New("source storage has conflicting temporary and final files")
			} else if errors.Is(tempErr, os.ErrNotExist) && finalErr == nil {
				if finalInfo.Size() != claimed.SizeBytes || finalInfo.Size() <= 0 {
					pathErr = errors.New("finalized import file does not match its persisted size")
				} else {
					recoverFinalizedFile = true
				}
			} else if tempInfo != nil {
				pathErr = os.Remove(paths.temp)
				if errors.Is(pathErr, os.ErrNotExist) {
					pathErr = nil
				}
			}
		}
	}
	if pathErr == nil && !recoverFinalizedFile {
		pathErr = a.store.ResetClippingSourceImportProgress(claimed.ID)
	}
	if pathErr == nil {
		a.store.PublishClippingSource(claimed.ID)
	}
	a.store.clippingMu.Unlock()
	if pathErr != nil {
		return true, a.retryClippingImport(claimed, trace, pathErr)
	}
	if err := opCtx.Err(); err != nil {
		return true, a.retryClippingImport(claimed, trace, err)
	}

	leaseCtx, stopLease, leaseFailure := a.startImportLeaseKeepalive(opCtx, claimed)
	defer stopLease()
	importCtx, cancel := context.WithTimeout(leaseCtx, clippingImportMaxDuration)
	defer cancel()
	if recoverFinalizedFile {
		if _, err := a.finalizeSource(importCtx, claimed.ID, ClippingSourceImporting); err != nil {
			if leaseErr := leaseFailure(); leaseErr != nil {
				return true, a.retryClippingImport(claimed, trace, leaseErr)
			}
			if errors.Is(err, errClippingInvalidMedia) {
				reason := "invalid_media"
				if claimed.Kind == ClippingSourceYouTube {
					reason = youtubeProbeFailureReason(err)
				}
				trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticFailed, clippingDiagnosticCode(ClippingDiagnosticStageProbe, err))
				a.failAndRemove(claimed.ID, reason)
				return true, nil
			}
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				return true, nil
			}
			return true, a.retryClippingImport(claimed, trace, err)
		}
		trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticCompleted, "")
		return true, nil
	}
	if err := a.importToDisk(importCtx, claimed, paths.temp, trace); err != nil {
		if leaseErr := leaseFailure(); leaseErr != nil {
			return true, a.retryClippingImport(claimed, trace, leaseErr)
		}
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			// CancelSource tombstones the row after canceling the operation.
			trace.finish(ClippingDiagnosticFailed, err)
			trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticFailed, "import_canceled")
			return true, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) && importCtx.Err() != nil && ctx.Err() != nil {
			return true, a.retryClippingImport(claimed, trace, err)
		}
		if isPermanentClippingImportError(err) {
			code := clippingDiagnosticCode(trace.activeStage, err)
			trace.finish(ClippingDiagnosticFailed, err)
			trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticFailed, code)
			a.failAndRemove(claimed.ID, safeClippingFailure(err))
			return true, nil
		}
		return true, a.retryClippingImport(claimed, trace, err)
	}
	if leaseErr := leaseFailure(); leaseErr != nil {
		return true, a.retryClippingImport(claimed, trace, leaseErr)
	}
	if err := importCtx.Err(); err != nil {
		return true, a.retryClippingImport(claimed, trace, err)
	}
	_, err = a.finalizeSource(importCtx, claimed.ID, ClippingSourceImporting)
	if err != nil {
		if leaseErr := leaseFailure(); leaseErr != nil {
			return true, a.retryClippingImport(claimed, trace, leaseErr)
		}
		if errors.Is(err, errClippingInvalidMedia) {
			reason := "invalid_media"
			if claimed.Kind == ClippingSourceYouTube {
				reason = youtubeProbeFailureReason(err)
			}
			trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticFailed, clippingDiagnosticCode(ClippingDiagnosticStageProbe, err))
			a.failAndRemove(claimed.ID, reason)
			return true, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return true, a.retryClippingImport(claimed, trace, err)
		}
		return true, a.retryClippingImport(claimed, trace, err)
	}
	trace.record(ClippingDiagnosticStageDispatch, ClippingDiagnosticCompleted, "")
	return true, nil
}

func (a *ClippingAcquisition) retryClippingImport(source ClippingSource, trace *clippingDiagnosticTrace, cause error) error {
	status := ClippingDiagnosticRetrying
	code := "retry_scheduled"
	if source.ImportAttempts >= MaxClippingImportAttempts {
		status = ClippingDiagnosticFailed
		code = clippingDiagnosticCode(trace.activeStage, cause)
	}
	trace.finish(status, cause)
	trace.record(ClippingDiagnosticStageDispatch, status, code)
	return a.retryImport(source, cause)
}

func (a *ClippingAcquisition) startImportLeaseKeepalive(parent context.Context, source ClippingSource) (context.Context, func(), func() error) {
	ctx, cancel := context.WithCancel(parent)
	interval := a.leaseRenewEvery
	if interval <= 0 || interval >= clippingImportLeaseDuration {
		interval = clippingImportLeaseDuration / 3
	}
	done := make(chan struct{})
	leaseErrors := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.store.RenewClippingImportLease(source.ID, source.ImportAttempts, clippingImportLeaseDuration); err != nil {
					leaseErrors <- err
					cancel()
					return
				}
			}
		}
	}()
	stop := func() {
		cancel()
		<-done
	}
	failure := func() error {
		select {
		case err := <-leaseErrors:
			return err
		default:
			return nil
		}
	}
	return ctx, stop, failure
}

// CancelSource stops any active stream before installing a durable tombstone.
func (a *ClippingAcquisition) CancelSource(ctx context.Context, sourceID string) error {
	if a == nil || a.store == nil {
		return errors.New("clipping source storage is unavailable")
	}
	done := a.cancelOperation(sourceID)
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.store.TombstoneClippingSource(sourceID); err != nil {
		return err
	}
	a.store.PublishClippingSource(sourceID)
	if err := a.removeSourceFiles(sourceID); err != nil {
		return err
	}
	if err := a.store.MarkClippingSourceCleaned(sourceID); err != nil {
		return err
	}
	a.store.PublishClippingSource(sourceID)
	return nil
}

// CleanupExpiredSources tombstones eligible rows before removing files, so a
// restart resumes cleanup from durable source state.
func (a *ClippingAcquisition) CleanupExpiredSources(ctx context.Context, now time.Time) (int, error) {
	if a == nil || a.store == nil {
		return 0, errors.New("clipping source storage is unavailable")
	}
	if now.IsZero() {
		now = a.now()
	}
	sources, err := a.store.ExpiredClippingSources(now.Unix(), 100)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		if a.hasActiveOperation(source.ID) {
			continue
		}
		a.store.clippingMu.Lock()
		if source.Status != ClippingSourceDeleted {
			if err := a.store.ExpireClippingSource(source.ID, now.Unix()); err != nil {
				a.store.clippingMu.Unlock()
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return cleaned, err
			}
			a.store.PublishClippingSource(source.ID)
		}
		if err := a.removeSourceFiles(source.ID); err != nil {
			a.store.clippingMu.Unlock()
			return cleaned, err
		}
		if err := a.store.MarkClippingSourceCleaned(source.ID); err != nil {
			a.store.clippingMu.Unlock()
			return cleaned, err
		}
		a.store.PublishClippingSource(source.ID)
		a.store.clippingMu.Unlock()
		cleaned++
	}
	return cleaned, nil
}

// CleanupFailedSources reconciles permanent failures left by a crash after the
// retry limit was reached. It releases aggregate capacity only after the
// generated directory has been removed and its parent synced.
func (a *ClippingAcquisition) CleanupFailedSources(ctx context.Context) (int, error) {
	if a == nil || a.store == nil {
		return 0, errors.New("clipping source storage is unavailable")
	}
	sources, err := a.store.FailedClippingSources(100)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		if a.hasActiveOperation(source.ID) {
			continue
		}
		a.store.clippingMu.Lock()
		current, err := a.store.ClippingSource(source.ID)
		if err != nil {
			a.store.clippingMu.Unlock()
			if errors.Is(err, ErrClippingSourceNotFound) {
				continue
			}
			return cleaned, err
		}
		if current.Status != ClippingSourceFailed || current.ReservedSizeBytes == 0 {
			a.store.clippingMu.Unlock()
			continue
		}
		// PendingClippingImports can durably mark an expired final attempt
		// failed; publish that transition even if physical cleanup later fails.
		a.store.PublishClippingSource(source.ID)
		paths, err := a.sourcePaths(source.ID)
		if err == nil {
			err = os.RemoveAll(filepath.Dir(paths.final))
		}
		if err == nil {
			err = syncClippingDirectory(filepath.Dir(filepath.Dir(paths.final)))
		}
		if err == nil {
			err = a.store.ReleaseFailedClippingSourceReservation(source.ID)
		}
		if err == nil {
			delete(a.store.clippingImportWorkspaces, source.ID)
			a.store.PublishClippingSource(source.ID)
			cleaned++
		}
		a.store.clippingMu.Unlock()
		if err != nil {
			return cleaned, err
		}
	}
	return cleaned, nil
}

func (a *ClippingAcquisition) importToDisk(ctx context.Context, source ClippingSource, tempPath string, trace *clippingDiagnosticTrace) error {
	trace.start(ClippingDiagnosticStageURLValidation)
	if a.security == nil {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("source URL encryption is unavailable")}, "source_unavailable")
	}
	plainURL, err := a.security.DecryptSetting(clippingSourceURLSetting, source.SourceURL)
	if err != nil {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("stored source URL could not be decrypted")}, "source_unavailable")
	}
	u, kind, err := normalizeClippingImportURL(plainURL)
	if err != nil {
		return permanentClippingImportError{err}
	}
	if kind != source.Kind {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("stored source type does not match its URL")}, "source_unavailable")
	}
	trace.complete()
	var response *http.Response
	if source.Kind == ClippingSourceYouTube {
		trace.start(ClippingDiagnosticStageMetadata)
		selection, err := a.extractYouTubeMediaWithTrace(ctx, u,
			func() { trace.complete() },
			func() { trace.start(ClippingDiagnosticStageFormat) },
			func(selection youtubeSelection) {
				trace.activeStage = ""
				code := "combined_tracks_selected"
				if selection.Separate {
					code = "separate_tracks_selected"
				}
				trace.record(ClippingDiagnosticStageFormat, ClippingDiagnosticCompleted, code)
				if selection.Separate && selection.Tracks.Audio != nil && youtubeFormatLanguagePreference(selection.Tracks.Audio.LanguagePreference) == youtubeOriginalLanguagePreference {
					trace.record(ClippingDiagnosticStageFormat, ClippingDiagnosticCompleted, "original_audio_selected")
				}
			},
		)
		if err != nil {
			return err
		}
		expectation, expectationErr := expectationForYouTubeSelection(selection)
		if expectationErr != nil {
			return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The selected audio/video tracks cannot be validated safely; upload an original file you are permitted to reuse")}, "format_unavailable")
		}
		a.store.clippingMu.Lock()
		persistErr := a.store.SetClippingSourceImportExpectation(source.ID, expectation)
		if persistErr == nil && selection.Separate {
			persistErr = a.reserveYouTubeWorkspaceLocked(source.ID)
		}
		a.store.clippingMu.Unlock()
		if persistErr != nil {
			return persistErr
		}
		if selection.Separate {
			videoID, validID := youtubeVideoID(u)
			if !validID {
				return withClippingDiagnosticCode(permanentClippingImportError{errors.New("stored source URL could not be validated")}, "source_unavailable")
			}
			return a.importYouTubeSeparateTracks(ctx, source, videoID, tempPath, selection, expectation, trace)
		}
		trace.start(ClippingDiagnosticStageDownload)
		response, err = a.fetchYouTubeMediaURL(ctx, selection)
	} else {
		trace.start(ClippingDiagnosticStageDownload)
		response, err = a.fetchClippingURL(ctx, u, source.Kind)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) && source.Kind != ClippingSourceYouTube {
			return permanentClippingImportError{clippingOutcome("authorization_needed", "provider_denied_download", "The public link does not allow an unauthenticated media download")}
		}
		if response.StatusCode == http.StatusPartialContent || response.Header.Get("Content-Range") != "" {
			if source.Kind == ClippingSourceYouTube {
				return permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
			}
			return permanentClippingImportError{clippingOutcome("unsupported", "partial_response_not_accepted", "The public link returned a partial response")}
		}
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests && source.Kind != ClippingSourceYouTube {
			return permanentClippingImportError{clippingOutcome("unsupported", "source_http_error", "The public link did not return media")}
		}
		if source.Kind == ClippingSourceYouTube {
			return fmt.Errorf("%w: YouTube media endpoint rejected the selected stream", errClippingImportRetryable)
		}
		return fmt.Errorf("%w: source server did not return media", errClippingImportRetryable)
	}
	if response.Header.Get("Content-Range") != "" {
		if source.Kind == ClippingSourceYouTube {
			return permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		return permanentClippingImportError{clippingOutcome("unsupported", "partial_response_not_accepted", "The public link returned a partial response")}
	}
	maxImportBytes := clippingImportSizeLimit(source.Kind, a.youtubeMaxBytes)
	if response.ContentLength > maxImportBytes {
		if source.Kind == ClippingSourceYouTube {
			return permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The video exceeds the 20 GiB limit; upload a smaller original file you are permitted to reuse")}
		}
		return permanentClippingImportError{clippingOutcome("unsupported", "source_size_limit_exceeded", "The public media file exceeds the 20 GiB size limit")}
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if strings.EqualFold(contentType, "text/html") || strings.EqualFold(contentType, "application/xhtml+xml") {
		if source.Kind == ClippingSourceYouTube {
			return permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		if source.Kind == ClippingSourceGoogleDrive {
			return permanentClippingImportError{clippingOutcome("original_file_required", "drive_viewer_page_requires_original_file", "The Drive link opened a viewer or confirmation page; upload the original media file")}
		}
		return permanentClippingImportError{clippingOutcome("unsupported", "non_media_response", "The public link returned a web page instead of media")}
	}
	if !supportedClippingContentType(contentType) {
		if source.Kind == ClippingSourceYouTube {
			return permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		return permanentClippingImportError{clippingOutcome("unsupported", "non_media_response", "The public link did not return a supported media type")}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ensureImporting(source.ID); err != nil {
		return err
	}
	a.store.clippingMu.Lock()
	capacityErr := a.checkClippingStorageCapacity(0)
	a.store.clippingMu.Unlock()
	if capacityErr != nil {
		return capacityErr
	}
	file, err := openClippingPartTruncate(tempPath)
	if err != nil {
		return fmt.Errorf("%w: prepare import file: %v", errClippingImportRetryable, err)
	}
	defer file.Close()
	buffer := make([]byte, clippingImportReadBytes)
	var committed int64
	for {
		if err := ctx.Err(); err != nil {
			_ = truncateAndSync(file, committed)
			return err
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if importStreamExceedsLimit(committed, int64(n), maxImportBytes) {
				_ = truncateAndSync(file, committed)
				if source.Kind == ClippingSourceYouTube {
					return permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The video exceeds the 20 GiB limit; upload a smaller original file you are permitted to reuse")}
				}
				return permanentClippingImportError{clippingOutcome("unsupported", "source_size_limit_exceeded", "The public media file exceeds the 20 GiB size limit")}
			}
			if err := a.commitImportChunk(ctx, source.ID, file, buffer[:n], committed); err != nil {
				_ = truncateAndSync(file, committed)
				return err
			}
			committed += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = truncateAndSync(file, committed)
			return fmt.Errorf("%w: read media response: %v", errClippingImportRetryable, readErr)
		}
		if n == 0 {
			continue
		}
	}
	if committed == 0 {
		if source.Kind == ClippingSourceYouTube {
			return permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		return permanentClippingImportError{clippingOutcome("unsupported", "non_media_response", "The public link returned an empty file")}
	}
	if response.ContentLength >= 0 && committed != response.ContentLength {
		return fmt.Errorf("%w: media length did not match response headers", errClippingImportRetryable)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync media response: %v", errClippingImportRetryable, err)
	}
	trace.complete()
	return nil
}

func (a *ClippingAcquisition) commitImportChunk(ctx context.Context, sourceID string, file *os.File, chunk []byte, previous int64) error {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return err
	}
	if source.Status != ClippingSourceImporting {
		return errors.New("source import was canceled")
	}
	if source.UploadOffset != previous {
		return errors.New("source import progress changed unexpectedly")
	}
	if _, err := file.Seek(previous, io.SeekStart); err != nil {
		return fmt.Errorf("%w: seek import file: %v", errClippingImportRetryable, err)
	}
	if err := writeFull(file, chunk); err != nil {
		return fmt.Errorf("%w: write import file: %v", errClippingImportRetryable, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync import chunk: %v", errClippingImportRetryable, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	newOffset := previous + int64(len(chunk))
	if err := a.store.UpdateClippingSourceProgress(sourceID, ClippingSourceImporting, 0, newOffset); err != nil {
		return fmt.Errorf("%w: persist import offset: %v", errClippingImportRetryable, err)
	}
	a.store.PublishClippingSource(sourceID)
	return nil
}

func (a *ClippingAcquisition) ensureImporting(sourceID string) error {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return err
	}
	if source.Status != ClippingSourceImporting {
		return errors.New("source import was canceled")
	}
	return nil
}

func (a *ClippingAcquisition) fetchClippingURL(ctx context.Context, initial *url.URL, provider ClippingSourceKind) (*http.Response, error) {
	current := cloneURL(initial)
	seen := make(map[string]struct{}, clippingImportMaxRedirects+1)
	for redirects := 0; ; redirects++ {
		if current == nil {
			return nil, permanentClippingImportError{errors.New("source URL is empty")}
		}
		currentKey := current.String()
		if _, exists := seen[currentKey]; exists {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "redirect_loop", "The public link returned a redirect loop")}
		}
		seen[currentKey] = struct{}{}
		if err := validateClippingProviderURL(current, provider); err != nil {
			return nil, permanentClippingImportError{err}
		}
		addresses, err := a.resolvePublicDestination(ctx, current, provider)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "invalid_url", "The source URL could not be requested")}
		}
		request.Header.Set("Accept", "video/*,audio/*,application/octet-stream,*/*;q=0.5")
		request.Header.Set("Accept-Encoding", "identity")
		transport := a.transportFactory(current, addresses)
		client := &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: provider request failed", errClippingImportRetryable)
		}
		if !isRedirectStatus(response.StatusCode) {
			return response, nil
		}
		location := response.Header.Get("Location")
		response.Body.Close()
		if redirects >= clippingImportMaxRedirects {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "redirect_limit_exceeded", "The public link exceeded the redirect limit")}
		}
		if location == "" {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "redirect_location_missing", "The public link returned an invalid redirect")}
		}
		if strings.Contains(location, "#") {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "invalid_redirect", "The public link returned an invalid redirect")}
		}
		reference, err := url.Parse(location)
		if err != nil {
			return nil, permanentClippingImportError{clippingOutcome("unsupported", "invalid_redirect", "The public link returned an invalid redirect")}
		}
		current = current.ResolveReference(reference)
		if err := validateClippingProviderURL(current, provider); err != nil {
			return nil, permanentClippingImportError{err}
		}
	}
}

func (a *ClippingAcquisition) resolvePublicDestination(ctx context.Context, u *url.URL, provider ClippingSourceKind) ([]net.IP, error) {
	if err := validateClippingProviderURL(u, provider); err != nil {
		return nil, err
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	resolved, err := a.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: provider DNS lookup failed", errClippingImportRetryable)
	}
	if len(resolved) == 0 {
		return nil, clippingOutcome("unsupported", "unsafe_dns_answer", "The provider hostname did not resolve to a public address")
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, resolvedAddress := range resolved {
		if resolvedAddress.Zone != "" || !isPublicUnicastIP(resolvedAddress.IP) {
			return nil, clippingOutcome("unsupported", "unsafe_dns_answer", "The provider hostname resolved to an unsafe address")
		}
		addresses = append(addresses, append(net.IP(nil), resolvedAddress.IP...))
	}
	return addresses, nil
}

func newPinnedClippingTransport(u *url.URL, addresses []net.IP) http.RoundTripper {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	allowed := make([]net.IP, 0, len(addresses))
	for _, ip := range addresses {
		if ip != nil && isPublicUnicastIP(ip) {
			allowed = append(allowed, append(net.IP(nil), ip...))
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: clippingImportResponseTimeout,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, destinationPort, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if strings.TrimSuffix(strings.ToLower(host), ".") != strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") || destinationPort != port {
				return nil, errors.New("HTTP transport attempted to dial an unvalidated destination")
			}
			var dialErrors []error
			for _, ip := range allowed {
				if !isPublicUnicastIP(ip) {
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				dialErrors = append(dialErrors, dialErr)
			}
			if len(dialErrors) == 0 {
				return nil, errors.New("no validated public destination address")
			}
			return nil, errors.Join(dialErrors...)
		},
	}
	return transport
}

func (a *ClippingAcquisition) finalizeSource(ctx context.Context, sourceID string, expectedStatus ClippingSourceStatus) (ClippingSource, error) {
	if err := ctx.Err(); err != nil {
		return ClippingSource{}, err
	}
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return ClippingSource{}, err
	}
	if source.Status == ClippingSourceReady {
		return source, nil
	}
	if source.Status != expectedStatus {
		return ClippingSource{}, errors.New("source is no longer available for finalization")
	}
	var youtubeExpectation youtubeImportExpectation
	if source.Kind == ClippingSourceYouTube {
		if json.Unmarshal([]byte(source.ImportExpectation), &youtubeExpectation) != nil || !validYouTubeImportExpectation(youtubeExpectation) {
			return ClippingSource{}, errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
		}
	}
	paths, err := a.sourcePaths(sourceID)
	if err != nil {
		return ClippingSource{}, err
	}
	tempPath, finalPath := paths.temp, paths.final
	localPath := tempPath
	if _, err := os.Lstat(tempPath); errors.Is(err, os.ErrNotExist) {
		if _, finalErr := os.Lstat(finalPath); finalErr == nil {
			localPath = finalPath
		} else {
			return ClippingSource{}, errors.New("source media file is missing")
		}
	} else if err != nil {
		return ClippingSource{}, err
	} else if _, finalErr := os.Lstat(finalPath); finalErr == nil {
		return ClippingSource{}, errors.New("source storage has conflicting temporary and final files")
	} else if !errors.Is(finalErr, os.ErrNotExist) {
		return ClippingSource{}, finalErr
	}
	file, err := openClippingRegularFile(localPath, os.O_RDONLY)
	if err != nil {
		return ClippingSource{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return ClippingSource{}, err
	}
	if info.Size() <= 0 || info.Size() > MaxClippingSourceBytes {
		file.Close()
		return ClippingSource{}, errClippingInvalidMedia
	}
	if expectedStatus == ClippingSourceUploading && info.Size() != source.DeclaredSizeBytes {
		file.Close()
		return ClippingSource{}, ErrClippingUploadIncomplete
	}
	if expectedStatus == ClippingSourceUploading && source.UploadOffset != info.Size() {
		file.Close()
		return ClippingSource{}, errors.New("persisted upload offset does not match the media file")
	}
	if expectedStatus == ClippingSourceImporting && source.SizeBytes != info.Size() {
		file.Close()
		return ClippingSource{}, errors.New("persisted import size does not match the media file")
	}
	if source.Kind == ClippingSourceYouTube && source.ImportExpectedSize > 0 &&
		(source.ImportExpectedSize != info.Size() || !validSHA256Hex(source.ImportExpectedSHA)) {
		file.Close()
		return ClippingSource{}, errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
	}
	if source.Kind == ClippingSourceYouTube && localPath == finalPath &&
		(source.ImportExpectedSize != info.Size() || !validSHA256Hex(source.ImportExpectedSHA)) {
		file.Close()
		return ClippingSource{}, errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return ClippingSource{}, err
	}
	if err := file.Close(); err != nil {
		return ClippingSource{}, err
	}
	trace := newClippingDiagnosticTrace(a, sourceID)
	trace.start(ClippingDiagnosticStageProbe)
	media, err := a.probe(ctx, localPath)
	if err != nil {
		if errors.Is(err, errClippingInvalidMedia) {
			if errors.Is(err, errClippingDurationLimitExceeded) {
				probeErr := errors.Join(errClippingInvalidMedia, errClippingDurationLimitExceeded)
				trace.finish(ClippingDiagnosticFailed, probeErr)
				return ClippingSource{}, probeErr
			}
			trace.finish(ClippingDiagnosticFailed, errClippingInvalidMedia)
			return ClippingSource{}, errClippingInvalidMedia
		}
		trace.finish(ClippingDiagnosticFailed, err)
		return ClippingSource{}, fmt.Errorf("validate local media: %w", err)
	}
	if media.DurationMS > MaxClippingSourceDurationMS {
		probeErr := errors.Join(errClippingInvalidMedia, errClippingDurationLimitExceeded)
		trace.finish(ClippingDiagnosticFailed, probeErr)
		return ClippingSource{}, probeErr
	}
	if media.DurationMS < 1 || !reasonableClippingDimensions(media.Width, media.Height) || source.Kind == ClippingSourceYouTube && media.AudioStreams < 1 {
		trace.finish(ClippingDiagnosticFailed, errClippingInvalidMedia)
		return ClippingSource{}, errClippingInvalidMedia
	}
	if source.Kind == ClippingSourceYouTube {
		trace.complete()
		trace.start(ClippingDiagnosticStageMergedValidate)
		if err := validateYouTubeProbeExpectation(youtubeExpectation, media); err != nil {
			trace.finish(ClippingDiagnosticFailed, err)
			return ClippingSource{}, err
		}
		trace.complete()
	}
	trace.complete()
	trace.start(ClippingDiagnosticStageHash)
	digest, err := hashClippingFile(ctx, localPath)
	if err != nil {
		trace.finish(ClippingDiagnosticFailed, err)
		return ClippingSource{}, err
	}
	trace.complete()
	if err := ctx.Err(); err != nil {
		return ClippingSource{}, err
	}
	if source.Kind == ClippingSourceYouTube && source.ImportExpectedSize > 0 &&
		(source.ImportExpectedSize != info.Size() || !strings.EqualFold(source.ImportExpectedSHA, digest)) {
		return ClippingSource{}, errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	current, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return ClippingSource{}, err
	}
	if current.Status == ClippingSourceReady {
		return current, nil
	}
	if current.Status != expectedStatus {
		return ClippingSource{}, errors.New("source was canceled during finalization")
	}
	if err := ctx.Err(); err != nil {
		return ClippingSource{}, err
	}
	if current.Kind == ClippingSourceYouTube {
		if err := a.store.SetClippingSourceImportFinalExpectation(sourceID, info.Size(), digest); err != nil {
			return ClippingSource{}, err
		}
	}
	if localPath == tempPath {
		if err := os.Rename(tempPath, finalPath); err != nil {
			return ClippingSource{}, err
		}
		if err := syncClippingDirectory(filepath.Dir(finalPath)); err != nil {
			_ = os.Rename(finalPath, tempPath)
			return ClippingSource{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		if localPath == tempPath {
			_ = os.Rename(finalPath, tempPath)
			_ = syncClippingDirectory(filepath.Dir(finalPath))
		}
		return ClippingSource{}, err
	}
	trace.start(ClippingDiagnosticStageReady)
	if err := a.store.MarkClippingSourceReady(sourceID, info.Size(), media.DurationMS, digest); err != nil {
		trace.finish(ClippingDiagnosticFailed, err)
		if localPath == tempPath {
			_ = os.Rename(finalPath, tempPath)
			_ = syncClippingDirectory(filepath.Dir(finalPath))
		}
		return ClippingSource{}, err
	}
	delete(a.store.clippingImportWorkspaces, sourceID)
	trace.complete()
	a.store.PublishClippingSource(sourceID)
	return a.store.ClippingSource(sourceID)
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (a *ClippingAcquisition) retryImport(source ClippingSource, cause error) error {
	paths, pathErr := a.sourcePaths(source.ID)
	a.store.clippingMu.Lock()
	current, currentErr := a.store.ClippingSource(source.ID)
	if errors.Is(currentErr, sql.ErrNoRows) || currentErr == nil && current.Status == ClippingSourceDeleted {
		a.store.clippingMu.Unlock()
		return nil
	}
	if currentErr != nil {
		a.store.clippingMu.Unlock()
		return currentErr
	}
	if pathErr == nil {
		if source.Kind == ClippingSourceYouTube {
			pathErr = a.clearYouTubeImportArtifactsLocked(source.ID, paths, true)
		} else {
			removeErr := removeGeneratedRegularFile(paths.temp)
			if removeErr != nil {
				pathErr = removeErr
			}
		}
	}
	if pathErr == nil {
		pathErr = a.store.ResetClippingSourceImportProgress(source.ID)
	}
	reason := safeClippingFailure(cause)
	if source.Kind == ClippingSourceYouTube && source.ImportAttempts >= MaxClippingImportAttempts {
		reason = "youtube_import_unavailable"
	}
	updateErr := a.store.RetryClippingImport(source.ID, source.ImportAttempts, a.now().Add(importRetryDelay(source.ImportAttempts)).Unix(), reason)
	if updateErr == nil {
		updated, readErr := a.store.ClippingSource(source.ID)
		if readErr == nil && updated.Status == ClippingSourceFailed && pathErr == nil {
			if removeErr := os.RemoveAll(filepath.Dir(paths.final)); removeErr == nil {
				if syncErr := syncClippingDirectory(filepath.Dir(filepath.Dir(paths.final))); syncErr == nil {
					if a.store.ReleaseFailedClippingSourceReservation(source.ID) == nil {
						delete(a.store.clippingImportWorkspaces, source.ID)
					}
				}
			}
		}
		a.store.PublishClippingSource(source.ID)
	}
	a.store.clippingMu.Unlock()
	return errors.Join(pathErr, updateErr)
}

func (a *ClippingAcquisition) failAndRemove(sourceID, reason string) {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := a.store.FailClippingSource(sourceID, reason); err != nil {
		current, readErr := a.store.ClippingSource(sourceID)
		if readErr != nil || current.Status != ClippingSourceFailed {
			return
		}
	} else {
		a.store.PublishClippingSource(sourceID)
	}
	paths, err := a.sourcePaths(sourceID)
	if err != nil {
		return
	}
	if err := os.RemoveAll(filepath.Dir(paths.final)); err != nil {
		return
	}
	if err := syncClippingDirectory(filepath.Dir(filepath.Dir(paths.final))); err != nil {
		return
	}
	if err := a.store.ReleaseFailedClippingSourceReservation(sourceID); err != nil {
		return
	}
	delete(a.store.clippingImportWorkspaces, sourceID)
	a.store.PublishClippingSource(sourceID)
}

func (a *ClippingAcquisition) removeSourceFiles(sourceID string) error {
	paths, err := a.sourcePaths(sourceID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(paths.final)); err != nil {
		return err
	}
	if err := syncClippingDirectory(filepath.Dir(filepath.Dir(paths.final))); err != nil {
		return err
	}
	delete(a.store.clippingImportWorkspaces, sourceID)
	return nil
}

type clippingSourcePaths struct {
	temp       string
	final      string
	videoTrack string
	audioTrack string
}

func (a *ClippingAcquisition) sourcePaths(sourceID string) (clippingSourcePaths, error) {
	finalPath, err := a.store.ClippingSourcePath(sourceID)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	tempPath, err := a.store.ClippingSourceTempPath(sourceID)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	directory := filepath.Dir(finalPath)
	return clippingSourcePaths{
		temp: tempPath, final: finalPath,
		videoTrack: filepath.Join(directory, "source.video.part"),
		audioTrack: filepath.Join(directory, "source.audio.part"),
	}, nil
}

func (a *ClippingAcquisition) ensureSourcePaths(sourceID string) (clippingSourcePaths, error) {
	paths, err := a.sourcePaths(sourceID)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	dataRoot, err := filepath.Abs(filepath.Join(a.store.dataDir, "clipping"))
	if err != nil {
		return clippingSourcePaths{}, err
	}
	final, err := filepath.Abs(paths.final)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	temp, err := filepath.Abs(paths.temp)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	video, err := filepath.Abs(paths.videoTrack)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	audio, err := filepath.Abs(paths.audioTrack)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	if !pathWithin(dataRoot, filepath.Dir(final)) || filepath.Dir(temp) != filepath.Dir(final) || filepath.Dir(video) != filepath.Dir(final) || filepath.Dir(audio) != filepath.Dir(final) ||
		filepath.Base(final) != "source.media" || filepath.Base(temp) != "source.media.part" || filepath.Base(video) != "source.video.part" || filepath.Base(audio) != "source.audio.part" {
		return clippingSourcePaths{}, errors.New("source storage path is outside its generated directory")
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return clippingSourcePaths{}, err
	}
	physicalRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return clippingSourcePaths{}, err
	}
	physicalDir, err := filepath.EvalSymlinks(filepath.Dir(final))
	if err != nil {
		return clippingSourcePaths{}, err
	}
	expectedDir := filepath.Join(physicalRoot, filepath.Base(filepath.Dir(final)))
	if physicalDir != expectedDir || !pathWithin(physicalRoot, physicalDir) {
		return clippingSourcePaths{}, errors.New("source directory resolves through a symlink")
	}
	for _, path := range []string{final, temp, paths.videoTrack, paths.audioTrack} {
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return clippingSourcePaths{}, errors.New("source path is not a regular file")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return clippingSourcePaths{}, err
		}
	}
	return paths, nil
}

func (a *ClippingAcquisition) beginOperation(parent context.Context, sourceID string, closer io.Closer) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	registry := a.operationRegistry()
	registry.mu.Lock()
	if _, exists := registry.operations[sourceID]; exists {
		registry.mu.Unlock()
		cancel()
		return nil, nil, errors.New("source already has an active acquisition operation")
	}
	registry.operations[sourceID] = clippingActiveOperation{cancel: cancel, closer: closer, done: done}
	registry.mu.Unlock()
	finish := func() {
		registry.mu.Lock()
		if active, ok := registry.operations[sourceID]; ok && active.done == done {
			delete(registry.operations, sourceID)
		}
		close(done)
		registry.mu.Unlock()
		cancel()
	}
	return ctx, finish, nil
}

func (a *ClippingAcquisition) cancelOperation(sourceID string) <-chan struct{} {
	registry := a.operationRegistry()
	registry.mu.Lock()
	active, ok := registry.operations[sourceID]
	if ok {
		active.cancel()
	}
	registry.mu.Unlock()
	if ok && active.closer != nil {
		_ = active.closer.Close()
	}
	if !ok {
		return nil
	}
	return active.done
}

func (a *ClippingAcquisition) hasActiveOperation(sourceID string) bool {
	registry := a.operationRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, ok := registry.operations[sourceID]
	return ok
}

func (a *ClippingAcquisition) operationRegistry() *clippingOperationRegistry {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if a.store.clippingOperations == nil {
		a.store.clippingOperations = &clippingOperationRegistry{operations: make(map[string]clippingActiveOperation)}
	}
	if a.store.clippingOperations.operations == nil {
		a.store.clippingOperations.operations = make(map[string]clippingActiveOperation)
	}
	return a.store.clippingOperations
}

func (a *ClippingAcquisition) lockSource(sourceID string) func() {
	a.locksMu.Lock()
	lock := a.sourceLocks[sourceID]
	if lock == nil {
		lock = &clippingSourceLock{}
		a.sourceLocks[sourceID] = lock
	}
	lock.refs++
	a.locksMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		a.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(a.sourceLocks, sourceID)
		}
		a.locksMu.Unlock()
	}
}

type clippingProbeOutput struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []clippingProbeStream `json:"streams"`
}

type clippingProbeStream struct {
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	Duration         string            `json:"duration"`
	StartTime        string            `json:"start_time"`
	AverageFrameRate string            `json:"avg_frame_rate"`
	RealFrameRate    string            `json:"r_frame_rate"`
	Tags             map[string]string `json:"tags"`
}

var clippingSupportedVideoCodecs = map[string]struct{}{
	"av1": {}, "h263": {}, "h263i": {}, "h263p": {}, "h264": {}, "hevc": {}, "mjpeg": {},
	"mpeg1video": {}, "mpeg2video": {}, "mpeg4": {}, "prores": {}, "theora": {}, "vc1": {},
	"vp8": {}, "vp9": {}, "wmv1": {}, "wmv2": {},
}

var clippingSupportedAudioCodecs = map[string]struct{}{
	"aac": {}, "ac3": {}, "alac": {}, "amr_nb": {}, "amr_wb": {}, "dts": {}, "eac3": {},
	"flac": {}, "mp2": {}, "mp3": {}, "opus": {}, "pcm_alaw": {}, "pcm_f32le": {},
	"pcm_mulaw": {}, "pcm_s16le": {}, "pcm_s24le": {}, "pcm_s32le": {}, "truehd": {},
	"vorbis": {}, "wavpack": {}, "wma1": {}, "wma2": {},
}

func probeClippingMedia(ctx context.Context, localPath string) (clippingMediaProbeResult, error) {
	if !filepath.IsAbs(localPath) {
		return clippingMediaProbeResult{}, errClippingInvalidMedia
	}
	probePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return clippingMediaProbeResult{}, errClippingProbeUnavailable
	}
	probeCtx, cancel := context.WithTimeout(ctx, clippingProbeTimeout)
	defer cancel()
	args := []string{
		"-v", "error",
		"-probesize", "5000000",
		"-analyzeduration", "15000000",
		"-max_alloc", strconv.Itoa(clippingProbeMaxAllocation),
		"-protocol_whitelist", "file,crypto,data",
		"-format_whitelist", "mov,matroska,webm",
		"-show_entries", "format=duration,format_name:stream=codec_type,codec_name,width,height,duration,start_time,avg_frame_rate,r_frame_rate:stream_tags=DURATION",
		"-of", "json",
		"-i", localPath,
	}
	command := exec.CommandContext(probeCtx, probePath, args...)
	limited := &clippingLimitedWriter{limit: clippingProbeOutputLimit}
	command.Stdout = limited
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if limited.exceeded {
			return clippingMediaProbeResult{}, errors.New("ffprobe output exceeded its limit")
		}
		if probeCtx.Err() != nil {
			return clippingMediaProbeResult{}, probeCtx.Err()
		}
		return clippingMediaProbeResult{}, errClippingInvalidMedia
	}
	if limited.exceeded {
		return clippingMediaProbeResult{}, errors.New("ffprobe output exceeded its limit")
	}
	var output clippingProbeOutput
	if err := json.Unmarshal(limited.data, &output); err != nil {
		return clippingMediaProbeResult{}, errClippingInvalidMedia
	}
	if !supportedClippingContainer(output.Format.FormatName) {
		return clippingMediaProbeResult{}, errClippingInvalidMedia
	}
	decoders, err := installedClippingDecoders(probeCtx)
	if err != nil {
		return clippingMediaProbeResult{}, err
	}
	return summarizeClippingProbeStreams(output, decoders)
}

func summarizeClippingProbeStreams(output clippingProbeOutput, decoders map[string]struct{}) (clippingMediaProbeResult, error) {
	hasVideo := false
	audioStreams := 0
	width, height := 0, 0
	durationMS, durationSet := parseFFprobeDurationMilliseconds(output.Format.Duration)
	result := clippingMediaProbeResult{}
	for _, stream := range output.Streams {
		switch stream.CodecType {
		case "audio":
			if !supportedClippingCodec("audio", stream.CodecName, decoders) {
				return clippingMediaProbeResult{}, errClippingInvalidMedia
			}
			audioStreams++
			streamDuration, hasDuration := ffprobeStreamDuration(stream)
			if hasDuration && (!durationSet || streamDuration > durationMS) {
				durationMS = streamDuration
				durationSet = true
			}
			if result.AudioCodec == "" {
				result.AudioCodec = stream.CodecName
				result.AudioStartMS, result.AudioTimingValid = parseSignedMilliseconds(stream.StartTime)
				result.AudioDurationMS = streamDuration
				result.AudioTimingValid = result.AudioTimingValid && hasDuration
			}
		case "video":
			if !supportedClippingCodec("video", stream.CodecName, decoders) {
				return clippingMediaProbeResult{}, errClippingInvalidMedia
			}
			hasVideo = true
			if result.VideoCodec == "" {
				result.VideoCodec = stream.CodecName
				result.VideoStartMS, result.VideoTimingValid = parseSignedMilliseconds(stream.StartTime)
				result.VideoDurationMS, _ = ffprobeStreamDuration(stream)
				_, hasDuration := ffprobeStreamDuration(stream)
				result.VideoTimingValid = result.VideoTimingValid && hasDuration
				result.VideoFPS, result.VideoFPSValid = parseProbeFrameRate(stream.RealFrameRate)
				if !result.VideoFPSValid {
					result.VideoFPS, result.VideoFPSValid = parseProbeFrameRate(stream.AverageFrameRate)
				}
			}
			if reasonableClippingDimensions(stream.Width, stream.Height) && width == 0 {
				width, height = stream.Width, stream.Height
			}
			if streamDuration, ok := ffprobeStreamDuration(stream); ok && (!durationSet || streamDuration > durationMS) {
				durationMS = streamDuration
				durationSet = true
			}
		default:
			continue
		}
	}
	if durationMS > MaxClippingSourceDurationMS {
		return clippingMediaProbeResult{}, errors.Join(errClippingInvalidMedia, errClippingDurationLimitExceeded)
	}
	if !hasVideo || width == 0 || !durationSet || durationMS <= 0 {
		return clippingMediaProbeResult{}, errClippingInvalidMedia
	}
	result.DurationMS = durationMS
	result.Width = width
	result.Height = height
	result.AudioStreams = audioStreams
	return result, nil
}

func parseProbeFrameRate(raw string) (float64, bool) {
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok || rate.Sign() <= 0 {
		return 0, false
	}
	value, _ := rate.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) || value > 1000 {
		return 0, false
	}
	return value, true
}

func ffprobeStreamDuration(stream clippingProbeStream) (int64, bool) {
	if duration, ok := parseFFprobeDurationMilliseconds(stream.Duration); ok {
		return duration, true
	}
	return parseFFprobeDurationMilliseconds(stream.Tags["DURATION"])
}

func parseFFprobeDurationMilliseconds(raw string) (int64, bool) {
	if milliseconds, ok := parseDurationMilliseconds(raw); ok {
		return milliseconds, true
	}
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, hourErr := strconv.ParseInt(parts[0], 10, 32)
	minutes, minuteErr := strconv.ParseInt(parts[1], 10, 32)
	seconds, secondOK := new(big.Rat).SetString(parts[2])
	if hourErr != nil || minuteErr != nil || hours < 0 || minutes < 0 || minutes >= 60 || !secondOK || seconds.Sign() < 0 || seconds.Cmp(big.NewRat(60, 1)) >= 0 {
		return 0, false
	}
	total := new(big.Rat).Mul(big.NewRat(hours*3600+minutes*60, 1), big.NewRat(1, 1))
	total.Add(total, seconds)
	milliseconds := new(big.Rat).Mul(total, big.NewRat(1000, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(milliseconds.Num(), milliseconds.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(milliseconds.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 {
		return 0, false
	}
	return quotient.Int64(), true
}

func parseSignedMilliseconds(raw string) (int64, bool) {
	seconds, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok {
		return 0, false
	}
	negative := seconds.Sign() < 0
	seconds.Abs(seconds)
	milliseconds := new(big.Rat).Mul(seconds, big.NewRat(1000, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(milliseconds.Num(), milliseconds.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(milliseconds.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}

func youtubeTimesWithin(left, right, tolerance int64) bool {
	difference := new(big.Int).Sub(big.NewInt(left), big.NewInt(right))
	difference.Abs(difference)
	return difference.Cmp(big.NewInt(tolerance)) <= 0
}

func supportedClippingCodec(codecType, codecName string, decoders map[string]struct{}) bool {
	if codecName == "" {
		return false
	}
	var allowed map[string]struct{}
	switch codecType {
	case "video":
		allowed = clippingSupportedVideoCodecs
	case "audio":
		allowed = clippingSupportedAudioCodecs
	default:
		return false
	}
	if _, ok := allowed[codecName]; !ok {
		return false
	}
	_, ok := decoders[codecName]
	return ok
}

func installedClippingDecoders(ctx context.Context) (map[string]struct{}, error) {
	executable, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg decoder support is unavailable")
	}
	identity := executable
	if info, statErr := os.Stat(executable); statErr == nil {
		identity = fmt.Sprintf("%s:%d:%d", executable, info.Size(), info.ModTime().UnixNano())
	}
	if cached, ok := clippingDecoderCache.Load(identity); ok {
		return cached.(map[string]struct{}), nil
	}
	decoderCtx, cancel := context.WithTimeout(ctx, clippingDecoderTimeout)
	defer cancel()
	command := exec.CommandContext(decoderCtx, executable, "-hide_banner", "-decoders")
	limited := &clippingLimitedWriter{limit: clippingDecoderOutputLimit}
	command.Stdout = limited
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if decoderCtx.Err() != nil {
			return nil, decoderCtx.Err()
		}
		return nil, errors.New("ffmpeg decoder support could not be read")
	}
	if limited.exceeded {
		return nil, errors.New("ffmpeg decoder list exceeded its output limit")
	}
	decoders := make(map[string]struct{})
	for _, line := range strings.Split(string(limited.data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 6 || fields[0][0] != 'V' && fields[0][0] != 'A' || fields[0][5] != 'D' {
			continue
		}
		decoders[fields[1]] = struct{}{}
	}
	immutable := make(map[string]struct{}, len(decoders))
	for name := range decoders {
		immutable[name] = struct{}{}
	}
	clippingDecoderCache.Store(identity, immutable)
	return immutable, nil
}

func supportedClippingContainer(formatNames string) bool {
	for _, name := range strings.Split(formatNames, ",") {
		switch strings.TrimSpace(name) {
		case "mov", "mp4", "matroska", "webm":
			return true
		}
	}
	return false
}

func reasonableClippingDimensions(width, height int) bool {
	if width < clippingMediaMinDimension || height < clippingMediaMinDimension || width > clippingMediaMaxDimension || height > clippingMediaMaxDimension {
		return false
	}
	return int64(width)*int64(height) <= clippingMediaMaxPixels
}

func parseDurationMilliseconds(raw string) (int64, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	seconds, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok || seconds.Sign() <= 0 {
		return 0, false
	}
	milliseconds := new(big.Rat).Mul(seconds, big.NewRat(1000, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(milliseconds.Num(), milliseconds.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(milliseconds.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}

type clippingLimitedWriter struct {
	data     []byte
	limit    int
	exceeded bool
}

func (writer *clippingLimitedWriter) Write(data []byte) (int, error) {
	if len(writer.data)+len(data) > writer.limit {
		writer.exceeded = true
		allowed := max(0, writer.limit-len(writer.data))
		writer.data = append(writer.data, data[:allowed]...)
		return allowed, errors.New("output limit exceeded")
	}
	writer.data = append(writer.data, data...)
	return len(data), nil
}

func hashClippingFile(ctx context.Context, path string) (string, error) {
	file, err := openClippingRegularFile(path, os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := copyHashContext(ctx, digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func copyHashContext(ctx context.Context, dst hash.Hash, src io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := src.Read(buffer)
		if count > 0 {
			written, writeErr := dst.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func copyContextLimit(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return 0, errors.New("invalid upload chunk limit")
	}
	buffer := make([]byte, 64<<10)
	var total int64
	for total < limit {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		want := int64(len(buffer))
		if remaining := limit - total; remaining < want {
			want = remaining
		}
		count, readErr := src.Read(buffer[:int(want)])
		if count > 0 {
			written, writeErr := dst.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if count == 0 {
			continue
		}
	}
	return total, nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func truncateAndSync(file *os.File, size int64) error {
	if err := file.Truncate(size); err != nil {
		return err
	}
	return file.Sync()
}

func openClippingPart(path string) (*os.File, error) {
	return openClippingRegularFile(path, os.O_CREATE|os.O_RDWR)
}

func openClippingPartTruncate(path string) (*os.File, error) {
	file, err := openClippingRegularFile(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func asCloser(reader io.Reader) io.Closer {
	closer, _ := reader.(io.Closer)
	return closer
}

func sanitizeClippingSourceName(value string) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.ReplaceAll(value, `\`, "/")
	value = filepath.Base(value)
	value = strings.TrimSpace(value)
	if value == "." || value == string(filepath.Separator) {
		value = ""
	}
	runes := []rune(value)
	if len(runes) > 200 {
		value = string(runes[:200])
	}
	clean := strings.Builder{}
	for _, character := range value {
		if character >= 0x20 && character != 0x7f {
			clean.WriteRune(character)
		}
	}
	return strings.TrimSpace(clean.String())
}

func clippingURLName(u *url.URL) string {
	name := "public-media"
	if u != nil {
		if decoded, err := url.PathUnescape(u.Path); err == nil {
			candidate := sanitizeClippingSourceName(decoded)
			if candidate != "" && candidate != "/" {
				name = candidate
			}
		}
	}
	if name == "" || name == "." {
		return "public-media"
	}
	return name
}

func normalizeClippingImportURL(raw string) (*url.URL, ClippingSourceKind, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 8192 || hasURLControl(raw) {
		return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid HTTPS share URL without credentials or fragments")
	}
	// Diagnostic branch: accept common YouTube links or bare 11-character IDs,
	// but pass only a validated canonical YouTube watch URL to yt-dlp.
	if videoID, ok := flexibleYouTubeVideoID(raw); ok {
		return &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: "v=" + videoID}, ClippingSourceYouTube, nil
	}
	if strings.Contains(raw, "#") {
		return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid HTTPS share URL without fragments")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid HTTPS share URL")
	}
	if err := validatePublicHTTPSURL(u); err != nil {
		return nil, "", err
	}
	host := strings.ToLower(u.Hostname())
	kind := clippingProviderForHost(host)
	if kind == ClippingSourceYouTube {
		videoID, ok := parseYouTubeReference(u, host)
		if !ok {
			return nil, "", clippingOutcome("unsupported", "unsupported_youtube_url_shape", "Enter a supported YouTube video reference")
		}
		return &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: "v=" + videoID}, kind, nil
	}
	if kind != ClippingSourceGoogleDrive && kind != ClippingSourceDropbox {
		return nil, "", clippingOutcome("unsupported", "unsupported_source_host", "Use a public Google Drive or Dropbox share URL")
	}
	if kind == ClippingSourceDropbox {
		if !strings.HasPrefix(u.Path, "/s/") && !strings.HasPrefix(u.Path, "/scl/fi/") {
			return nil, "", clippingOutcome("unsupported", "unsupported_dropbox_link_shape", "Use a public Dropbox shared file link")
		}
		query := u.Query()
		query.Set("dl", "1")
		u.RawQuery = query.Encode()
	}
	return canonicalClippingURL(u), kind, nil
}

func canonicalClippingURL(u *url.URL) *url.URL {
	copy := cloneURL(u)
	copy.Scheme = "https"
	copy.Host = strings.ToLower(copy.Hostname())
	if copy.Path == "" {
		copy.Path = "/"
	}
	copy.Fragment = ""
	copy.RawFragment = ""
	return copy
}

func validatePublicHTTPSURL(u *url.URL) error {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Host == "" || u.Fragment != "" || u.RawFragment != "" {
		return clippingOutcome("unsupported", "https_required", "Use an HTTPS public share URL without credentials or fragments")
	}
	if strings.HasSuffix(u.Host, ".") || strings.Contains(u.Host, "%") {
		return clippingOutcome("unsupported", "invalid_host", "The share URL has an invalid host")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !asciiDNSName(host) {
		return clippingOutcome("unsupported", "invalid_host", "The share URL has an invalid host")
	}
	if net.ParseIP(host) != nil {
		return clippingOutcome("unsupported", "ip_literal_forbidden", "The share URL must use an approved provider hostname")
	}
	if port := u.Port(); port != "" && port != "443" {
		return clippingOutcome("unsupported", "port_forbidden", "The share URL must use HTTPS on port 443")
	}
	return nil
}

func asciiDNSName(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func hasURLControl(raw string) bool {
	for _, character := range raw {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func clippingProviderForHost(host string) ClippingSourceKind {
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be", "youtube-nocookie.com", "www.youtube-nocookie.com":
		return ClippingSourceYouTube
	case "drive.google.com", "docs.google.com", "drive.usercontent.google.com", "googleusercontent.com":
		return ClippingSourceGoogleDrive
	case "dropbox.com", "dropboxusercontent.com":
		return ClippingSourceDropbox
	}
	if strings.HasSuffix(host, ".googleusercontent.com") {
		return ClippingSourceGoogleDrive
	}
	if strings.HasSuffix(host, ".dropbox.com") || strings.HasSuffix(host, ".dropboxusercontent.com") {
		return ClippingSourceDropbox
	}
	return ""
}

func validateClippingProviderURL(u *url.URL, provider ClippingSourceKind) error {
	if err := validatePublicHTTPSURL(u); err != nil {
		return err
	}
	if clippingProviderForHost(strings.ToLower(u.Hostname())) != provider {
		return clippingOutcome("unsupported", "redirect_provider_mismatch", "The public link redirected outside its approved provider")
	}
	return nil
}

func parseYouTubeReference(u *url.URL, host string) (string, bool) {
	if u == nil || u.RawPath != "" && u.RawPath != u.Path {
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", false
	}
	for key := range query {
		switch strings.ToLower(key) {
		case "list", "playlist", "playlist_id", "video_ids", "start_radio", "index":
			return "", false
		}
	}
	var id string
	switch {
	case host == "youtu.be":
		if !strings.HasPrefix(u.Path, "/") || strings.Count(u.Path, "/") != 1 {
			return "", false
		}
		id = strings.TrimPrefix(u.Path, "/")
		if _, exists := query["v"]; exists {
			return "", false
		}
	case u.Path == "/watch":
		values, exists := query["v"]
		if !exists || len(values) != 1 {
			return "", false
		}
		id = values[0]
	case strings.HasPrefix(u.Path, "/shorts/"), strings.HasPrefix(u.Path, "/embed/"):
		prefix := "/shorts/"
		if strings.HasPrefix(u.Path, "/embed/") {
			prefix = "/embed/"
		}
		id = strings.TrimPrefix(u.Path, prefix)
		if strings.Contains(id, "/") || id == "" {
			return "", false
		}
		if _, exists := query["v"]; exists {
			return "", false
		}
	default:
		return "", false
	}
	return id, validYouTubeVideoID(id)
}

// flexibleYouTubeVideoID accepts IDs and common YouTube link shapes without
// ever permitting a caller-supplied arbitrary URL to reach the extractor.
func flexibleYouTubeVideoID(raw string) (string, bool) {
	if validYouTubeVideoID(raw) {
		return raw, true
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawFragment != "" {
		return "", false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", false
	}
	if port := u.Port(); port != "" && port != "443" && !(u.Scheme == "http" && port == "80") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(u.Host, ".") || strings.Contains(u.Host, "%") {
		return "", false
	}
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com",
		"youtube-nocookie.com", "www.youtube-nocookie.com", "youtu.be", "www.youtu.be":
	default:
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", false
	}
	var id string
	if host == "youtu.be" || host == "www.youtu.be" {
		id = strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
		if strings.Contains(id, "/") {
			return "", false
		}
	} else if u.Path == "/watch" || u.Path == "/watch/" {
		values := query["v"]
		if len(values) != 1 {
			return "", false
		}
		id = values[0]
	} else {
		for _, prefix := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
			if strings.HasPrefix(u.Path, prefix) {
				id = strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), "/")
				if strings.Contains(id, "/") {
					return "", false
				}
				break
			}
		}
	}
	return id, validYouTubeVideoID(id)
}

func validYouTubeVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, character := range id {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func supportedClippingContentType(contentType string) bool {
	if contentType == "application/octet-stream" || contentType == "application/x-matroska" {
		return true
	}
	return strings.HasPrefix(contentType, "video/")
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

var errInvalidClippingImportURL = errors.New("invalid public media URL")
var errUnsafeClippingDestination = errors.New("unsafe public media destination")
var errClippingProbeUnavailable = errors.New("ffprobe is unavailable")

type permanentClippingImportError struct {
	err error
}

func (failure permanentClippingImportError) Error() string { return failure.err.Error() }
func (failure permanentClippingImportError) Unwrap() error { return failure.err }

func isPermanentClippingImportError(err error) bool {
	var permanent permanentClippingImportError
	var outcome *ClippingAcquisitionError
	return errors.As(err, &permanent) || errors.As(err, &outcome) || errors.Is(err, errUnsafeClippingDestination) || errors.Is(err, errInvalidClippingImportURL)
}

func safeClippingFailure(err error) string {
	if err == nil {
		return "source_acquisition_failed"
	}
	var outcome *ClippingAcquisitionError
	if errors.As(err, &outcome) && outcome.Reason != "" {
		return outcome.Reason
	}
	if errors.Is(err, context.Canceled) {
		return "source_import_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "source_import_timed_out"
	}
	if errors.Is(err, errClippingInvalidMedia) {
		return "invalid_media"
	}
	if errors.Is(err, errUnsafeClippingDestination) {
		return "unsafe_dns_answer"
	}
	return "source_acquisition_failed"
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	copy := *u
	return &copy
}

func isPublicUnicastIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	address, ok := netipFromIP(ip)
	if !ok || !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, blocked := range clippingReservedIPPrefixes {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}

// netipFromIP is isolated to make all address forms, including IPv4-mapped
// IPv6, pass through the same special-purpose range checks.
func netipFromIP(ip net.IP) (netip.Addr, bool) {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

var clippingReservedIPPrefixes = mustClippingPrefixes([]string{
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
})

func (a *ClippingAcquisition) checkClippingStorageCapacity(additionalSourceBytes int64) error {
	if additionalSourceBytes < 0 || additionalSourceBytes > MaxClippingSourceBytes {
		return clippingOutcome("unsupported", "storage_reservation_unavailable", "The source cannot be reserved within the storage limit")
	}
	available, err := a.diskAvailable(a.store.dataDir)
	if err != nil || available < 0 {
		return clippingOutcome("unsupported", "storage_reservation_unavailable", "Available source storage could not be measured")
	}
	sources, err := a.store.ClippingSources()
	if err != nil {
		return clippingOutcome("unsupported", "storage_reservation_unavailable", "Existing source storage reservations could not be read")
	}
	var outstanding int64
	for _, source := range sources {
		if source.Status == ClippingSourceDeleted || source.CleanupComplete {
			continue
		}
		if source.Status == ClippingSourceUploading || source.Status == ClippingSourceImporting {
			reservation := source.ReservedSizeBytes
			if reservation < source.DeclaredSizeBytes {
				reservation = source.DeclaredSizeBytes
			}
			if remaining := reservation - source.SizeBytes; remaining > 0 {
				outstanding += remaining
			}
		}
	}
	if err := a.reconcileYouTubeWorkspaceReservationsLocked(sources); err != nil {
		return clippingOutcome("unsupported", "storage_reservation_unavailable", "Temporary source workspace could not be reconciled safely")
	}
	var workspace int64
	for _, remaining := range a.store.clippingImportWorkspaces {
		if remaining < 0 || remaining > clippingYouTubeTrackWorkspaceBytes || workspace > int64(^uint64(0)>>1)-remaining {
			return clippingOutcome("unsupported", "storage_reservation_unavailable", "Temporary source workspace reservation is invalid")
		}
		workspace += remaining
	}
	needed := clippingFreeSpaceFloorBytes + additionalSourceBytes + outstanding + workspace
	if available < needed {
		return clippingOutcome("unsupported", "storage_reservation_unavailable", "Not enough free storage is available for the source, temporary track workspace, and safety margin")
	}
	return nil
}

// reconcileYouTubeWorkspaceReservationsLocked reconstructs scratch accounting
// for stale split imports after a process restart. Existing bytes are already
// reflected in Statfs; the map reserves only the remaining 20 GiB track cap.
// The caller holds Store.clippingMu so admission and import writes see one
// serialized view.
func (a *ClippingAcquisition) reconcileYouTubeWorkspaceReservationsLocked(sources []ClippingSource) error {
	if a.store.clippingImportWorkspaces == nil {
		a.store.clippingImportWorkspaces = make(map[string]int64)
	}
	for _, source := range sources {
		if source.Kind != ClippingSourceYouTube {
			continue
		}
		if source.Status == ClippingSourceReady {
			delete(a.store.clippingImportWorkspaces, source.ID)
			continue
		}
		if source.Status != ClippingSourceImporting {
			continue
		}
		if _, active := a.store.clippingImportWorkspaces[source.ID]; active {
			continue
		}
		separate := false
		workspaceComplete := false
		if source.ImportExpectation != "" {
			var expectation youtubeImportExpectation
			if err := json.Unmarshal([]byte(source.ImportExpectation), &expectation); err != nil || !validYouTubeImportExpectation(expectation) {
				return errors.New("stored YouTube import expectation is invalid")
			}
			separate = expectation.SeparateTracks
			workspaceComplete = expectation.WorkspaceComplete
		}
		paths, err := a.sourcePaths(source.ID)
		if err != nil {
			return err
		}
		videoBytes, err := safeGeneratedFileSize(paths.videoTrack)
		if err != nil {
			return err
		}
		audioBytes, err := safeGeneratedFileSize(paths.audioTrack)
		if err != nil {
			return err
		}
		if importStreamExceedsLimit(videoBytes, audioBytes, MaxClippingSourceBytes) {
			return errors.New("stored YouTube track workspace exceeds its aggregate limit")
		}
		used := videoBytes + audioBytes
		if workspaceComplete {
			if used != 0 {
				return errors.New("completed YouTube track workspace still contains staged bytes")
			}
			continue
		}
		if !separate && used == 0 {
			continue
		}
		a.store.clippingImportWorkspaces[source.ID] = clippingYouTubeTrackWorkspaceBytes - used
	}
	return nil
}

func safeGeneratedFileSize(path string) (int64, error) {
	file, err := openClippingRegularFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 0 {
		return 0, errors.New("staged source file size could not be validated")
	}
	return info.Size(), nil
}

func mustClippingPrefixes(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}

func importRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 8))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
