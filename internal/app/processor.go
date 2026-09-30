package app

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Processor struct {
	app             *dashboardApp
	interval        time.Duration
	logger          *slog.Logger
	downloadTimeout time.Duration
	downloadSem     chan struct{}
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
	app := &dashboardApp{store: store, security: security, logger: logger, callbackBaseURL: configuredCallbackBaseURL(), videoCallbackBaseURL: configuredVideoCallbackBaseURL()}
	if security != nil {
		if err := store.GarbageCollectProviderConfigs(); err != nil {
			logger.Error("provider configuration garbage collection failed", "error", err)
		}
		if err := app.backfillLegacyProviderSnapshots(); err != nil {
			app.legacySnapshotBackfillErr = err
			logger.Error("legacy provider snapshot backfill failed; legacy work will be skipped", "error", err)
		}
	}
	return &Processor{app: app, interval: 5 * time.Second, logger: logger, downloadTimeout: 5 * time.Minute, downloadSem: make(chan struct{}, 2), projectSem: make(chan struct{}, 3), combineSem: make(chan struct{}, 1), combineRunner: runCommand, inFlight: make(map[string]struct{})}
}

func (p *Processor) Run(ctx context.Context) {
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

func (p *Processor) process(ctx context.Context) {
	p.processCallbackJobs(ctx)
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
