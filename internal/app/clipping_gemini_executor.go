package app

import (
	"context"
	"errors"
	"time"
)

type clippingGeminiAnalyzer interface {
	Analyze(context.Context, ClippingGeminiRequest) (clippingGeminiResponse, error)
}

type clippingGeminiClientFactory func(context.Context, ClippingGeminiConfig) (clippingGeminiAnalyzer, error)

func newClippingGeminiExecutorClient(ctx context.Context, config ClippingGeminiConfig) (clippingGeminiAnalyzer, error) {
	return newClippingGeminiClient(ctx, config)
}

// runClippingGeminiWorkers completes startup recovery before starting any
// paid work. A failed recovery is retried without opening a provider client.
func (p *Processor) runClippingGeminiWorkers(ctx context.Context) {
	for ctx.Err() == nil {
		ids, err := p.app.store.RecoverClippingGeminiAttempts(time.Now().Unix(), true)
		if err == nil {
			for _, id := range ids {
				p.app.store.PublishClippingJob(id)
			}
			break
		}
		p.logger.Warn("Gemini clipping startup recovery failed")
		if !waitProcessor(ctx, clippingGeminiSubmissionPollInterval) {
			return
		}
	}
	runClippingGeminiWorkerPool(ctx, func(workerCtx context.Context) {
		for workerCtx.Err() == nil {
			config, err := currentClippingGeminiRuntimeConfig()
			worked := false
			if err == nil {
				worked, err = p.processNextClippingGemini(workerCtx, config, newClippingGeminiExecutorClient)
			}
			if err != nil && workerCtx.Err() == nil {
				p.logger.Warn("Gemini clipping queue could not be processed")
			}
			if err != nil || !worked {
				if !waitProcessor(workerCtx, clippingGeminiSubmissionPollInterval) {
					return
				}
			}
		}
	})
}

func (p *Processor) processNextClippingGemini(ctx context.Context, config clippingGeminiRuntimeConfig, factory clippingGeminiClientFactory) (bool, error) {
	if ctx == nil || factory == nil {
		return false, ErrClippingInvalidState
	}
	jobs, err := p.app.store.PendingClippingGeminiJobs(time.Now().Unix(), 20)
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		cached, err := p.app.store.CompleteClippingGeminiFromCache(job.ID, time.Now())
		if err != nil {
			return false, err
		}
		if cached {
			p.app.store.PublishClippingJob(job.ID)
			return true, nil
		}
		if !config.Enabled {
			continue
		}
		lease, claimed, err := p.app.store.ClaimClippingGeminiAttempt(job.ID, time.Now())
		if err != nil {
			return false, err
		}
		if !claimed {
			continue
		}
		p.app.store.PublishClippingJob(job.ID)
		p.executeClippingGemini(ctx, job, lease, config, factory)
		return true, nil
	}
	return false, nil
}

func (p *Processor) executeClippingGemini(parent context.Context, job ClippingJob, lease clippingGeminiAttemptLease, config clippingGeminiRuntimeConfig, factory clippingGeminiClientFactory) {
	started := time.Now()
	callCtx, cancel := context.WithTimeout(parent, clippingGeminiRequestTimeout)
	defer cancel()
	release, registered := p.app.store.clippingGeminiCancellationRegistry().register(job.ID, lease.Attempt.AttemptID, cancel)
	if !registered {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(context.Canceled))
		return
	}
	defer release()
	defer func() {
		p.app.store.PublishClippingJob(job.ID)
		attempt, err := p.app.store.ClippingGeminiLatestAttempt(job.ID)
		if err == nil {
			p.logger.Info("Gemini clipping attempt finished", "job_id", job.ID, "attempt_id", lease.Attempt.AttemptID, "attempt", lease.Attempt.Attempt, "outcome", string(attempt.Outcome), "failure_code", safeClippingGeminiFailureCode(attempt.FailureCode), "http_status", attempt.HTTPStatus, "elapsed_ms", time.Since(started).Milliseconds(), "usage_available", attempt.Usage.Available, "total_tokens", attempt.Usage.TotalTokens)
		}
	}()
	snapshot, err := p.app.store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil || snapshot.Config != config.Routing {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(errClippingGeminiConfigDrift))
		return
	}
	currentJob, err := p.app.store.ClippingJob(job.ID)
	if err != nil {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(errClippingGeminiSourceInaccessible))
		return
	}
	source, err := p.app.store.ClippingSource(job.SourceID)
	if err != nil {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(errClippingGeminiSourceInaccessible))
		return
	}
	prepared, err := prepareClippingGeminiSubmissionForJob(currentJob, snapshot, source, p.app.security, time.Now())
	if err != nil {
		reason := errClippingGeminiSourceInaccessible
		if errors.Is(err, errClippingGeminiConfigDrift) {
			reason = errClippingGeminiConfigDrift
		}
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(reason))
		return
	}
	client, err := factory(callCtx, snapshot.Config)
	if err != nil || client == nil {
		var classified *clippingGeminiAttemptError
		if !errors.As(err, &classified) || classified == nil {
			classified = clippingGeminiNotSubmittedError(errClippingGeminiClientUnavailable)
		}
		p.finishClippingGeminiLocalFailure(job.ID, lease, classified)
		return
	}
	if err := callCtx.Err(); err != nil {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(err))
		return
	}
	if err := p.app.store.AuthorizeClippingGeminiSubmission(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, time.Now()); err != nil {
		p.finishClippingGeminiLocalFailure(job.ID, lease, clippingGeminiNotSubmittedError(errClippingGeminiSourceInaccessible))
		return
	}
	response, callErr := client.Analyze(callCtx, ClippingGeminiRequest{YouTubeURL: prepared.CanonicalYouTubeURL, Options: snapshot.Options})
	if err := p.app.store.settleClippingGeminiResult(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, response, callErr, time.Now()); err != nil && !errors.Is(err, ErrClippingStaleAttempt) {
		p.logger.Warn("Gemini clipping attempt settlement failed", "job_id", job.ID, "attempt_id", lease.Attempt.AttemptID)
	}
}

func (p *Processor) finishClippingGeminiLocalFailure(jobID string, lease clippingGeminiAttemptLease, err error) {
	if _, settleErr := p.app.store.settleClippingGeminiKnownRejection(jobID, lease.Attempt.AttemptID, lease.LeaseToken, err, time.Now()); settleErr != nil && !errors.Is(settleErr, ErrClippingStaleAttempt) {
		p.logger.Warn("Gemini clipping preflight settlement failed", "job_id", jobID, "attempt_id", lease.Attempt.AttemptID)
	}
}
