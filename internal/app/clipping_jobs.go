package app

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

type clippingPreparedDispatch struct {
	Payload        clippingStageDispatch `json:"payload"`
	MediaBearer    string                `json:"-"`
	CallbackBearer string                `json:"-"`
}

// clippingStageDispatch is the credential-free, versioned payload passed to a
// configured worker. The media and callback capabilities are persisted
// encrypted first and delivered separately as bearer headers.
type clippingStageDispatch struct {
	ProtocolVersion   string `json:"protocol_version"`
	JobID             string `json:"job_id"`
	BatchID           string `json:"batch_id"`
	SourceID          string `json:"source_id"`
	Stage             string `json:"stage"`
	DispatchID        string `json:"dispatch_id"`
	AttemptID         string `json:"attempt_id"`
	MediaURL          string `json:"media_url"`
	CallbackURL       string `json:"callback_url"`
	SourceDurationMS  int64  `json:"source_duration_ms"`
	ExpiresAt         int64  `json:"expires_at"`
	CallbackExpiresAt int64  `json:"callback_expires_at"`
	SourceSHA256      string `json:"source_sha256"`
	ContentType       string `json:"content_type"`
	Model             string `json:"model"`
	PipelineRevision  string `json:"pipeline_revision"`
	MinClipSeconds    int    `json:"min_clip_seconds"`
	MaxClipSeconds    int    `json:"max_clip_seconds"`
	CandidateLimit    int    `json:"candidate_limit"`
}

// buildClippingStageDispatch binds the immutable job/source/attempt snapshot
// into the M2 wire contract without selecting a paid provider or dispatching
// work. A future dispatcher can send this only after the attempt capabilities
// have been encrypted by persistClippingDispatchCapabilities.
func buildClippingStageDispatch(job ClippingJob, stage ClippingStage, source ClippingSource, origin string, now time.Time) (clippingStageDispatch, error) {
	if job.Provider != ClippingProviderModalLegacy || !safeID(job.ID) || !safeID(job.BatchID) || !safeID(source.ID) || !safeID(stage.AttemptID) || job.SourceID != source.ID || job.Status != ClippingJobRunning ||
		stage.JobID != job.ID || stage.Name != ClippingStageAnalysis || stage.Status != ClippingStageRunning ||
		stage.IdempotencyKey != job.ID+":"+stage.Name || stage.LeaseExpiresAt <= now.Unix() ||
		source.Status != ClippingSourceReady || !source.RightsAttested || source.DurationMS < 1 || source.DurationMS > MaxClippingSourceDurationMS {
		return clippingStageDispatch{}, errors.New("clipping stage is not eligible for dispatch")
	}
	mediaURL, err := clippingWorkerEndpoint(origin, "/api/clipping/worker-media/"+job.ID+"/"+stage.AttemptID)
	if err != nil {
		return clippingStageDispatch{}, err
	}
	callbackURL, err := clippingWorkerEndpoint(origin, "/api/clipping/worker-callbacks/"+job.ID+"/"+stage.AttemptID)
	if err != nil {
		return clippingStageDispatch{}, err
	}
	callbackExpiresAt := stage.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
	if source.RetainUntil > 0 && source.RetainUntil < callbackExpiresAt {
		callbackExpiresAt = source.RetainUntil
	}
	if callbackExpiresAt <= now.Unix() {
		return clippingStageDispatch{}, errors.New("clipping source retention leaves no worker callback window")
	}
	return clippingStageDispatch{
		ProtocolVersion: clippingProtocolVersion,
		JobID:           job.ID, BatchID: job.BatchID, SourceID: source.ID, Stage: stage.Name,
		DispatchID: stage.IdempotencyKey, AttemptID: stage.AttemptID,
		MediaURL: mediaURL, CallbackURL: callbackURL,
		SourceDurationMS: source.DurationMS, ExpiresAt: stage.LeaseExpiresAt, CallbackExpiresAt: callbackExpiresAt,
		SourceSHA256: source.SHA256, ContentType: job.ContentType,
		Model:          clippingWorkerModel,
		MinClipSeconds: job.MinClipSeconds, MaxClipSeconds: job.MaxClipSeconds,
		CandidateLimit: job.CandidateLimit,
	}, nil
}

// prepareClippingStageDispatch persists the attempt and encrypted capabilities
// before returning a worker envelope. It does not send the
// dispatch or select a provider; M2 leaves this queue waiting until a worker
// configuration exists.
func (a *dashboardApp) prepareClippingStageDispatch(_ string, _ int64, _ string) (clippingPreparedDispatch, bool, error) {
	return clippingPreparedDispatch{}, false, ErrClippingLegacyRetired
}

func clippingWorkerEndpoint(origin, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("clipping worker origin must be a credential-free HTTPS origin")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("clipping worker origin cannot include a path")
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}
