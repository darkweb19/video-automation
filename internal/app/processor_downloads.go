package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

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
