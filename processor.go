package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Processor struct {
	app             *dashboardApp
	interval        time.Duration
	logger          *slog.Logger
	downloadTimeout time.Duration
	downloadSem     chan struct{}
	mu              sync.Mutex
	inFlight        map[string]struct{}
}

func NewProcessor(store *Store, security *Security, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Processor{app: &dashboardApp{store: store, security: security, logger: logger}, interval: 5 * time.Second, logger: logger, downloadTimeout: 5 * time.Minute, downloadSem: make(chan struct{}, 2), inFlight: make(map[string]struct{})}
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
	if len(records) == 0 {
		return
	}
	provider, err := p.app.provider()
	if err != nil {
		p.logger.Warn("generation processor waiting for API key")
		return
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		if record.Status == "downloading" || record.Status == "download_failed" {
			p.startDownload(ctx, provider, record)
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
		if err := p.app.store.UpdateGeneration(record.ID, generation.Status, generation.Model, generation.CostUSD, generation.Error); err != nil {
			p.logger.Error("generation update failed", "generation_id", record.ID)
			continue
		}
		if generation.Status == "completed" {
			_ = p.app.store.UpdateGeneration(record.ID, "downloading", generation.Model, generation.CostUSD, "")
			record.CostUSD = generation.CostUSD
			p.startDownload(ctx, provider, record)
		}
	}
}

func (p *Processor) startDownload(ctx context.Context, provider *OpenRouterClient, record GenerationRecord) {
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

func (p *Processor) download(ctx context.Context, provider *OpenRouterClient, record GenerationRecord) {
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
