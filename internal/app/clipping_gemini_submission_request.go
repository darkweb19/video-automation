package app

import (
	"errors"
	"time"
)

var (
	errClippingGeminiSourceInaccessible = errors.New("Gemini clipping source is inaccessible")
	errClippingGeminiConfigDrift        = errors.New("Gemini clipping request configuration changed")
)

// prepareClippingGeminiSubmissionForJob decrypts only the protected canonical
// YouTube URL and verifies it against the durable snapshot. It deliberately
// never reads local media fields, paths, SHA values, or measured duration.
func prepareClippingGeminiSubmissionForJob(job ClippingJob, snapshot ClippingGeminiSnapshot, source ClippingSource, security *Security, now time.Time) (*clippingGeminiPreparedRequest, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if security == nil || security.store == nil || job.ID == "" || job.ID != snapshot.JobID ||
		job.Provider != ClippingProviderGemini || job.SourceID == "" || job.SourceID != source.ID ||
		job.AnalysisDeleted || job.Status == ClippingJobCanceled || job.Status == ClippingJobCompleted || job.Status == ClippingJobFailed {
		return nil, ErrClippingInvalidState
	}
	if (source.Kind != ClippingSourceYouTubeURL && source.Kind != ClippingSourceYouTube) ||
		source.Status != ClippingSourceReady || !source.RightsAttested || source.DeletedAt != 0 ||
		source.RetainUntil > 0 && source.RetainUntil <= now.Unix() || source.SourceURL == "" {
		return nil, errClippingGeminiSourceInaccessible
	}
	canonicalURL, err := security.DecryptSetting(clippingSourceURLSetting, source.SourceURL)
	if err != nil {
		return nil, errClippingGeminiSourceInaccessible
	}
	canonicalURL, err = canonicalClippingGeminiYouTubeURL(canonicalURL)
	if err != nil {
		return nil, errClippingGeminiSourceInaccessible
	}
	prepared, err := prepareClippingGeminiSnapshotRequest(snapshot, canonicalURL)
	if err != nil {
		return nil, errClippingGeminiConfigDrift
	}
	return prepared, nil
}
