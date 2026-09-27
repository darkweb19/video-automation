package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	app := &dashboardApp{store: store, security: security, logger: logger, callbackBaseURL: configuredCallbackBaseURL()}
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

func (p *Processor) startSceneDownload(ctx context.Context, provider VideoService, scene ProjectScene) {
	key := fmt.Sprintf("%s:download:%d", scene.ProjectID, scene.Number)
	p.startProjectTask(ctx, key, func(taskCtx context.Context) {
		defer p.app.store.PublishProject(scene.ProjectID)
		_ = p.app.store.AppendPipelineEvent(scene.ProjectID, "scene_download", "started", "Downloading completed scene video.", scene.Number, scene.DownloadAttempts)
		downloadCtx, cancel := context.WithTimeout(taskCtx, p.downloadTimeout)
		defer cancel()
		response, err := provider.GetVideoContent(downloadCtx, scene.ProviderGenerationID, "")
		if err != nil {
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		finalPath := p.app.store.SceneVideoPath(scene.ProjectID, scene.Number)
		if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		tempPath := finalPath + ".part"
		file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		size, copyErr := io.Copy(file, response.Body)
		syncErr, closeErr := file.Sync(), file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || size == 0 {
			_ = os.Remove(tempPath)
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		if err := os.Rename(tempPath, finalPath); err != nil {
			_ = os.Remove(tempPath)
			_ = p.app.store.ScheduleSceneDownloadRetry(scene.ProjectID, scene.Number)
			return
		}
		if err := p.app.store.MarkSceneVideoReady(scene.ProjectID, scene.Number, finalPath, size); err != nil {
			_ = os.Remove(finalPath)
		}
	})
}

// startCombineTask keeps the one memory-intensive FFmpeg combine independent
// from projectSem. That leaves the project workers available for scene
// submission, status work, and streaming downloads while a final video is
// being encoded.
func (p *Processor) startCombineTask(ctx context.Context, key string, task func(context.Context)) {
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.combineSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		<-p.combineSem
		return
	}
	p.inFlight[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			<-p.combineSem
			p.mu.Lock()
			delete(p.inFlight, key)
			p.mu.Unlock()
		}()
		task(ctx)
	}()
}

func (p *Processor) startProjectTask(ctx context.Context, key string, task func(context.Context)) {
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.projectSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		<-p.projectSem
		return
	}
	p.inFlight[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			<-p.projectSem
			p.mu.Lock()
			delete(p.inFlight, key)
			p.mu.Unlock()
		}()
		task(ctx)
	}()
}

func (p *Processor) startDownload(ctx context.Context, provider VideoService, record GenerationRecord) {
	p.mu.Lock()
	if _, exists := p.inFlight[record.ID]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.downloadSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[record.ID]; exists {
		p.mu.Unlock()
		<-p.downloadSem
		return
	}
	p.inFlight[record.ID] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() { <-p.downloadSem; p.mu.Lock(); delete(p.inFlight, record.ID); p.mu.Unlock() }()
		downloadCtx, cancel := context.WithTimeout(ctx, p.downloadTimeout)
		defer cancel()
		p.download(downloadCtx, provider, record)
	}()
}

func (p *Processor) download(ctx context.Context, provider VideoService, record GenerationRecord) {
	defer p.app.store.PublishGeneration(record.ID)
	response, err := provider.GetVideoContent(ctx, record.ID, "")
	if err != nil {
		p.downloadFailed(record.ID, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		p.downloadFailed(record.ID, fmt.Errorf("content returned status %d", response.StatusCode))
		return
	}
	finalPath := p.app.store.VideoPath(record.ID)
	tempPath := finalPath + ".part"
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		p.downloadFailed(record.ID, err)
		return
	}
	size, copyErr := io.Copy(file, response.Body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tempPath)
		if copyErr != nil {
			p.downloadFailed(record.ID, copyErr)
		} else if syncErr != nil {
			p.downloadFailed(record.ID, syncErr)
		} else {
			p.downloadFailed(record.ID, closeErr)
		}
		return
	}
	if size == 0 {
		_ = os.Remove(tempPath)
		p.downloadFailed(record.ID, fmt.Errorf("downloaded video was empty"))
		return
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		p.downloadFailed(record.ID, err)
		return
	}
	if err := p.app.store.MarkVideoReady(record.ID, finalPath, size); err != nil {
		// The user may have deleted the generation while a download was in
		// flight. Do not leave a local orphan in the Docker volume.
		_ = os.Remove(finalPath)
		p.logger.Error("save downloaded video failed", "generation_id", record.ID)
		return
	}
	p.logger.Info("video stored", "generation_id", record.ID, "bytes", size)
}

func (p *Processor) downloadFailed(id string, err error) {
	p.logger.Warn("video download failed", "generation_id", id)
	if retryErr := p.app.store.ScheduleDownloadRetry(id); retryErr != nil {
		p.logger.Debug("schedule video retry skipped", "generation_id", id)
	}
}
