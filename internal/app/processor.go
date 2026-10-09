package app

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"
)

type Processor struct {
	app             *dashboardApp
	interval        time.Duration
	logger          *slog.Logger
	downloadTimeout time.Duration
	downloadSem     chan struct{}
	youtubeSem      chan struct{}
	projectSem      chan struct{}
	combineSem      chan struct{}
	combineRunner   commandRunner
	mu              sync.Mutex
	inFlight        map[string]struct{}
}

func NewProcessor(store *Store, security *Security, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	app := &dashboardApp{store: store, security: security, logger: logger, clippingAcquisition: NewClippingAcquisition(store, security), callbackBaseURL: configuredCallbackBaseURL(), videoCallbackBaseURL: configuredVideoCallbackBaseURL()}
	if security != nil {
		if err := store.GarbageCollectProviderConfigs(); err != nil {
			logger.Error("provider configuration garbage collection failed", "error", err)
		}
		if err := app.backfillLegacyProviderSnapshots(); err != nil {
			app.legacySnapshotBackfillErr = err
			logger.Error("legacy provider snapshot backfill failed; legacy work will be skipped", "error", err)
		}
	}
	return &Processor{app: app, interval: 5 * time.Second, logger: logger, downloadTimeout: 5 * time.Minute, downloadSem: make(chan struct{}, 2), youtubeSem: make(chan struct{}, 1), projectSem: make(chan struct{}, 3), combineSem: make(chan struct{}, 1), combineRunner: runCommand, inFlight: make(map[string]struct{})}
}

func (p *Processor) Run(ctx context.Context) {
	go p.runClippingImportWorker(ctx)
	go p.runClippingRetentionCleanup(ctx)
	go p.runClippingStageRecovery(ctx)
	go p.runClippingAnalysisWorker(ctx)
	p.process(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.process(ctx)
		}
	}
}

// runClippingAnalysisWorker dispatches one configured full-source analysis at
// a time. Uncertain network outcomes retain both their budget hold and the
// per-source single-flight claim; this loop never retries them automatically.
func (p *Processor) runClippingAnalysisWorker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := p.processNextClippingAnalysis(ctx)
		if err != nil && ctx.Err() == nil {
			p.logger.Warn("clipping analysis queue could not be processed")
		}
		if err != nil || !worked {
			if !waitProcessor(ctx, 5*time.Second) {
				return
			}
		}
	}
}

func (p *Processor) processNextClippingAnalysis(ctx context.Context) (bool, error) {
	config, configured, err := loadClippingWorkerConfig(p.app.store, p.app.security)
	if err != nil || !configured || !config.Enabled {
		return false, err
	}
	jobs, err := p.app.store.PendingClippingJobs(20)
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		cached, err := p.app.store.TryCompleteClippingJobFromAnalysisCache(job.ID)
		if err != nil {
			p.logger.Warn("clipping source cache could not be applied", "job_id", job.ID)
			continue
		}
		if cached {
			p.app.store.PublishClippingJob(job.ID)
			p.app.publishClippingBatch(job.BatchID)
			return true, nil
		}
		claimedSource, err := p.app.store.ClaimClippingAnalysisSource(job.ID)
		if err != nil {
			p.logger.Warn("clipping source analysis could not be claimed", "job_id", job.ID)
			continue
		}
		if !claimedSource {
			continue
		}
		budget, err := p.app.store.ClippingDispatchBudget(job.ID)
		if err != nil {
			_ = p.app.store.ReleaseClippingAnalysisSourceClaim(job.ID)
			continue
		}
		maxComputeSeconds, err := clippingWorkerComputeSecondsForReservation(budget, config.RateMicroUSDPerSecond)
		if err != nil || config.RateMicroUSDPerSecond > math.MaxInt64/maxComputeSeconds {
			_ = p.app.store.ReleaseClippingAnalysisSourceClaim(job.ID)
			continue
		}
		reserveMicroUSD := config.RateMicroUSDPerSecond * maxComputeSeconds
		prepared, claimed, err := p.app.prepareClippingStageDispatch(job.ID, reserveMicroUSD, p.app.callbackBaseURL)
		if err != nil || !claimed {
			_ = p.app.store.ReleaseClippingAnalysisSourceClaim(job.ID)
			if err != nil {
				p.logger.Warn("clipping analysis dispatch could not be prepared", "job_id", job.ID)
			}
			continue
		}
		if err := p.app.store.SetClippingAnalysisClaimAttempt(job.ID, prepared.Payload.AttemptID); err != nil {
			leaseToken, leaseErr := p.app.clippingCapability(job.ID, prepared.Payload.AttemptID, clippingCapabilityLease)
			if leaseErr == nil {
				_, _ = p.app.store.FailClippingStage(job.ID, ClippingStageAnalysis, prepared.Payload.AttemptID, leaseToken, "Analysis source claim could not be saved", false, 0)
				_ = p.app.store.ReleaseClippingAnalysisSourceClaim(job.ID)
			}
			p.app.store.PublishClippingJob(job.ID)
			p.app.publishClippingBatch(job.BatchID)
			continue
		}
		if ctx.Err() != nil {
			// No network call has started, so this attempt is known not to have
			// incurred a provider charge.
			leaseToken, leaseErr := p.app.clippingCapability(job.ID, prepared.Payload.AttemptID, clippingCapabilityLease)
			if leaseErr == nil {
				_, failErr := p.app.store.FailClippingStageBeforeSubmit(job.ID, ClippingStageAnalysis, prepared.Payload.AttemptID, leaseToken, "Analysis dispatch canceled before submission")
				if failErr == nil || errors.Is(failErr, ErrClippingJobTerminal) {
					_ = p.app.store.ReleaseClippingAnalysisSourceClaimBeforeSubmit(job.ID, prepared.Payload.AttemptID)
				}
			}
			return false, ctx.Err()
		}
		if err := p.app.dispatchPreparedClippingStage(ctx, prepared, maxComputeSeconds); err != nil {
			if clippingWorkerDispatchDefinitelyNotSubmitted(err) {
				leaseToken, leaseErr := p.app.clippingCapability(job.ID, prepared.Payload.AttemptID, clippingCapabilityLease)
				if leaseErr == nil {
					_, failErr := p.app.store.FailClippingStageBeforeSubmit(
						job.ID, ClippingStageAnalysis, prepared.Payload.AttemptID, leaseToken,
						"Analysis dispatch ended before worker submission",
					)
					if failErr == nil || errors.Is(failErr, ErrClippingJobTerminal) {
						if releaseErr := p.app.store.ReleaseClippingAnalysisSourceClaimBeforeSubmit(job.ID, prepared.Payload.AttemptID); releaseErr != nil {
							p.logger.Warn("known-zero analysis source claim could not be released", "job_id", job.ID)
						}
						p.app.store.PublishClippingJob(job.ID)
						p.app.publishClippingBatch(job.BatchID)
					} else {
						p.logger.Warn("known-zero analysis dispatch could not be recorded", "job_id", job.ID)
					}
				} else {
					p.logger.Warn("known-zero analysis lease could not be loaded", "job_id", job.ID)
				}
			} else {
				// Once http.Client.Do starts, any failure can hide a worker
				// acceptance. Keep its hold and source claim until a callback or
				// explicit invoice reconciliation establishes the outcome.
				p.logger.Warn("clipping analysis dispatch outcome is pending", "job_id", job.ID)
			}
		} else {
			p.app.store.PublishClippingJob(job.ID)
			p.app.publishClippingBatch(job.BatchID)
		}
		return true, nil
	}
	return false, nil
}

// runClippingStageRecovery is a sparse durable timeout recovery. It marks
// expired worker leases uncertain and keeps their budget reserved; it never
// redispatches paid work or polls a remote worker.
func (p *Processor) runClippingStageRecovery(ctx context.Context) {
	if _, err := p.app.cleanupExpiredClippingCapabilities(time.Now().Unix(), maxClippingCapabilityCleanupBatch); err != nil && ctx.Err() == nil {
		p.logger.Warn("clipping worker capability cleanup failed")
	}
	if err := p.recoverClippingStages(); err != nil && ctx.Err() == nil {
		p.logger.Warn("clipping stage recovery failed")
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.app.cleanupExpiredClippingCapabilities(time.Now().Unix(), maxClippingCapabilityCleanupBatch); err != nil && ctx.Err() == nil {
				p.logger.Warn("clipping worker capability cleanup failed")
			}
			if err := p.recoverClippingStages(); err != nil && ctx.Err() == nil {
				p.logger.Warn("clipping stage recovery failed")
			}
		}
	}
}

func (p *Processor) recoverClippingStages() error {
	now := time.Now().Unix()
	expired, err := p.app.store.ExpiredClippingStages(now, 100)
	if err != nil || len(expired) == 0 {
		return err
	}
	if err := p.app.store.RecoverExpiredClippingStages(now); err != nil {
		return err
	}
	seenJobs := make(map[string]struct{}, len(expired))
	seenBatches := make(map[string]struct{}, len(expired))
	for _, stage := range expired {
		if _, seen := seenJobs[stage.JobID]; seen {
			continue
		}
		seenJobs[stage.JobID] = struct{}{}
		job, err := p.app.store.ClippingJob(stage.JobID)
		if err != nil {
			continue
		}
		p.app.store.PublishClippingJob(job.ID)
		if _, seen := seenBatches[job.BatchID]; !seen {
			seenBatches[job.BatchID] = struct{}{}
			p.app.publishClippingBatch(job.BatchID)
		}
	}
	return nil
}

// runClippingImportWorker drains the durable import queue outside the cheap
// five-second project/generation scan. A single bounded worker is intentional:
// imports can run for hours and contend for persistent disk/network capacity.
func (p *Processor) runClippingImportWorker(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		worked, err := p.app.clippingAcquisition.ProcessNextImport(ctx)
		if err != nil && ctx.Err() == nil {
			// Import errors may include remote URLs or filesystem details. The
			// source row contains a sanitized user-facing failure message.
			p.logger.Warn("clipping source import failed")
		}
		if err != nil && ctx.Err() == nil {
			if !waitProcessor(ctx, 5*time.Second) {
				return
			}
		} else if !worked {
			// ProcessNextImport also reconciles exhausted crash-recovery rows;
			// call it before sleeping even when no ordinary import is queued.
			if !waitProcessor(ctx, 30*time.Second) {
				return
			}
		}
	}
}

func (p *Processor) runClippingRetentionCleanup(ctx context.Context) {
	if err := p.cleanupClippingSources(ctx); err != nil && ctx.Err() == nil {
		p.logger.Warn("clipping source cleanup failed")
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.cleanupClippingSources(ctx); err != nil && ctx.Err() == nil {
				p.logger.Warn("clipping source cleanup failed")
			}
		}
	}
}

func (p *Processor) cleanupClippingSources(ctx context.Context) error {
	if _, err := p.app.cleanupExpiredClippingCapabilities(time.Now().Unix(), maxClippingCapabilityCleanupBatch); err != nil {
		return err
	}
	before, err := p.app.store.ExpiredClippingSources(time.Now().Unix(), 100)
	if err != nil {
		return err
	}
	if _, err := p.app.clippingAcquisition.CleanupExpiredSources(ctx, time.Now()); err != nil {
		return err
	}
	for _, source := range before {
		p.app.store.PublishClippingSource(source.ID)
	}
	return nil
}

func waitProcessor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *Processor) process(ctx context.Context) {
	p.processCallbackJobs(ctx)
	p.processYouTubeUploads(ctx)
	records, err := p.app.store.PendingGenerations(ctx)
	if err != nil {
		p.logger.Error("load pending generations failed")
		return
	}
	projects, projectErr := p.app.store.PendingProjects(ctx)
	if projectErr != nil {
		p.logger.Error("load pending projects failed")
		return
	}
	p.processCombiningProjects(ctx, projects)
	remoteProjects := make([]VideoProject, 0, len(projects))
	for _, project := range projects {
		if project.Status != "combining" {
			remoteProjects = append(remoteProjects, project)
		}
	}
	if len(records) == 0 && len(remoteProjects) == 0 {
		return
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		if record.Status == "downloading" || record.Status == "download_failed" {
			provider, err := p.app.videoProviderForSnapshot(record.ProviderConfigID, VideoProviderID(record.VideoProvider))
			if err != nil {
				p.logger.Warn("generation processor waiting for immutable video provider configuration", "provider", record.VideoProvider, "error", err)
				continue
			}
			p.startDownload(ctx, provider, record)
			continue
		}
		callbackJob, callbackErr := p.app.store.hasCallbackJob(record.ID)
		if callbackJob || callbackErr != nil {
			// Callback jobs wait for their authenticated terminal callback or the
			// durable callback deadline. Legacy generations keep their existing
			// provider-status recovery path below.
			continue
		}
		if record.VideoProvider == string(VideoProviderModal) {
			// Modal reports terminal state through the callback. The only
			// fallback is this persisted, delayed watchdog for delivery loss or
			// an app restart; it never performs five-second status polling.
			if record.ModalCallbackRecoveryAt > time.Now().Unix() {
				continue
			}
			provider, err := p.app.videoProviderForSnapshot(record.ProviderConfigID, VideoProviderID(record.VideoProvider))
			if err != nil {
				p.logger.Warn("Modal callback recovery waiting for immutable provider configuration", "generation_id", record.ID)
				_ = p.app.store.ScheduleModalGenerationRecovery(record.ID)
				continue
			}
			p.recoverModalGeneration(ctx, provider, record)
			continue
		}
		provider, err := p.app.videoProviderForSnapshot(record.ProviderConfigID, VideoProviderID(record.VideoProvider))
		if err != nil {
			p.logger.Warn("generation processor waiting for immutable video provider configuration", "provider", record.VideoProvider, "error", err)
			continue
		}
		requestContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		generation, err := provider.GetGeneration(requestContext, record.ID)
		cancel()
		if err != nil {
			p.logger.Warn("generation poll failed", "generation_id", record.ID)
			continue
		}
		if generation == nil {
			continue
		}
		progress := -1
		if generation.Progress != nil {
			progress = *generation.Progress
		}
		if err := p.app.store.UpdateGenerationProgress(record.ID, generation.Status, generation.Model, generation.CostUSD, generation.Error, progress); err != nil {
			p.logger.Error("generation update failed", "generation_id", record.ID)
			continue
		}
		if generation.Status == "completed" {
			_ = p.app.store.UpdateGenerationProgress(record.ID, "downloading", generation.Model, generation.CostUSD, "", 100)
			record.CostUSD = generation.CostUSD
			p.startDownload(ctx, provider, record)
		}
		p.app.store.PublishGeneration(record.ID)
	}
	p.processProjects(ctx, remoteProjects)
}

func (p *Processor) recoverModalGeneration(ctx context.Context, provider VideoService, record GenerationRecord) {
	requestContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	generation, err := provider.GetGeneration(requestContext, record.ID)
	if err != nil || generation == nil {
		_ = p.app.store.ScheduleModalGenerationRecovery(record.ID)
		return
	}
	if generation.ID == "" {
		generation.ID = record.ID
	}
	updated, err := p.app.store.ApplyModalGenerationRecovery(record.ID, *generation)
	if err != nil {
		p.logger.Warn("Modal callback recovery update failed", "generation_id", record.ID)
		return
	}
	if updated {
		p.app.store.PublishGeneration(record.ID)
		if generation.Status == "completed" {
			refreshed, loadErr := p.app.store.Generation(record.ID)
			if loadErr == nil && refreshed.Status == "downloading" {
				p.startDownload(ctx, provider, refreshed)
			}
		}
	}
}
