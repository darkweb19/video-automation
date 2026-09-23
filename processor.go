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
	combineRunner   commandRunner
	mu              sync.Mutex
	inFlight        map[string]struct{}
}

func NewProcessor(store *Store, security *Security, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Processor{app: &dashboardApp{store: store, security: security, logger: logger}, interval: 5 * time.Second, logger: logger, downloadTimeout: 5 * time.Minute, downloadSem: make(chan struct{}, 2), projectSem: make(chan struct{}, 3), combineRunner: runCommand, inFlight: make(map[string]struct{})}
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
		provider, err := p.app.videoProvider(VideoProviderID(record.VideoProvider))
		if err != nil {
			p.logger.Warn("generation processor waiting for video provider", "provider", record.VideoProvider)
			continue
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
	p.processProjects(ctx, remoteProjects)
}

func (p *Processor) processCombiningProjects(ctx context.Context, projects []VideoProject) {
	for _, project := range projects {
		if project.Status != "combining" {
			continue
		}
		projectCopy := project
		p.startProjectTask(ctx, project.ID+":combine", func(taskCtx context.Context) {
			_ = p.app.store.AppendPipelineEvent(projectCopy.ID, "combine", "started", "Final video assembly started.", 0, 0)
			inputs := make([]string, 0, ProjectSceneCount)
			for _, scene := range projectCopy.Scenes {
				if !scene.VideoReady {
					_ = p.app.store.AppendPipelineEvent(projectCopy.ID, "combine", "failed", "A scene file is missing.", 0, 0)
					_ = p.app.store.UpdateProjectStatus(projectCopy.ID, "failed", "A scene file is missing; retry that scene.")
					return
				}
				inputs = append(inputs, scene.VideoPath)
			}
			combineCtx, cancel := context.WithTimeout(taskCtx, 15*time.Minute)
			defer cancel()
			finalPath := p.app.store.ProjectFinalPath(projectCopy.ID)
			size, err := combineProjectVideo(combineCtx, p.combineRunner, inputs, finalPath)
			if err != nil {
				_ = p.app.store.AppendPipelineEvent(projectCopy.ID, "combine", "failed", "Final video assembly failed.", 0, 0)
				_ = p.app.store.UpdateProjectStatus(projectCopy.ID, "failed", "Unable to combine the scene clips.")
				p.logger.Error("combine project failed", "project_id", projectCopy.ID)
				return
			}
			if err := p.app.store.MarkProjectReady(projectCopy.ID, finalPath, size); err != nil {
				_ = os.Remove(finalPath)
				p.logger.Error("save final project failed", "project_id", projectCopy.ID)
			}
		})
	}
}

func (p *Processor) processProjects(ctx context.Context, projects []VideoProject) {
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		switch project.Status {
		case "planning":
			p.startProjectTask(ctx, project.ID+":script", func(taskCtx context.Context) {
				trace, traceErr := newTextGenerationTrace(project.Topic)
				if traceErr != nil || p.app.store.SaveTextGenerationTrace(project.ID, trace) != nil {
					_ = p.app.store.AppendPipelineEvent(project.ID, "text_request", "failed", "Unable to prepare script-generation request metadata.", 0, 0)
					_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "Unable to prepare the story and script request. Retry the project.")
					return
				}
				_ = p.app.store.AppendPipelineEvent(project.ID, "text_request", "started", "Structured script-generation request built.", 0, 0)
				_ = p.app.store.AppendPipelineEvent(project.ID, "text_generation", "started", "OpenRouter text generation started.", 0, 0)
				textProvider, providerErr := p.app.openRouterTextProvider()
				if providerErr != nil {
					_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "OpenRouter text generation is not configured. Retry the project.")
					return
				}
				plan, trace, err := textProvider.GenerateStoryPlan(taskCtx, project.Topic)
				if persistErr := p.app.store.SaveTextGenerationTrace(project.ID, trace); persistErr != nil {
					p.logger.Error("save project text trace failed", "project_id", project.ID)
				}
				if err != nil {
					_ = p.app.store.AppendPipelineEvent(project.ID, "text_generation", "failed", "OpenRouter text generation failed.", 0, 0)
					_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "Unable to generate the story and script. Retry the project.")
					p.logger.Warn("project script generation failed", "project_id", project.ID)
					return
				}
				_ = p.app.store.AppendPipelineEvent(project.ID, "text_generation", "completed", "OpenRouter returned a structured script response.", 0, 0)
				_ = p.app.store.AppendPipelineEvent(project.ID, "validation", "started", "Validating the generated story plan.", 0, 0)
				if err := validateStoryPlan(plan); err != nil || len([]rune(plan.Script)) > 12000 || len([]rune(plan.Continuity)) > 8000 {
					_ = p.app.store.AppendPipelineEvent(project.ID, "validation", "failed", "Generated story plan failed validation.", 0, 0)
					_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "Generated story plan was invalid. Retry the project.")
					return
				}
				_ = p.app.store.AppendPipelineEvent(project.ID, "validation", "completed", "Generated story plan validated.", 0, 0)
				_ = p.app.store.AppendPipelineEvent(project.ID, "continuity", "started", "Applying continuity bible to final scene prompts.", 0, 0)
				if err := p.app.store.SaveStoryPlan(project.ID, plan); err != nil {
					_ = p.app.store.AppendPipelineEvent(project.ID, "continuity", "failed", "Unable to prepare final scene prompts.", 0, 0)
					_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "Unable to prepare final scene prompts. Retry the project.")
					p.logger.Error("save project script failed", "project_id", project.ID)
					return
				}
				_ = p.app.store.AppendPipelineEvent(project.ID, "continuity", "completed", "Continuity applied; final scene prompts are ready.", 0, 0)
			})
		case "generating":
			provider, err := p.app.videoProvider(VideoProviderID(project.VideoProvider))
			if err != nil {
				p.logger.Warn("project processor waiting for video provider", "provider", project.VideoProvider)
				continue
			}
			p.processProjectScenes(ctx, provider, project)
		}
	}
}

func (p *Processor) processProjectScenes(ctx context.Context, provider VideoService, project VideoProject) {
	complete, failed, active := 0, 0, 0
	for _, scene := range project.Scenes {
		switch scene.Status {
		case "completed":
			complete++
		case "failed":
			failed++
		case "pending":
			active++
			sceneCopy := scene
			p.startProjectTask(ctx, fmt.Sprintf("%s:submit:%d", project.ID, scene.Number), func(taskCtx context.Context) {
				p.submitScene(taskCtx, provider, project, sceneCopy)
			})
		case "queued", "processing":
			active++
			sceneCopy := scene
			p.startProjectTask(ctx, fmt.Sprintf("%s:poll:%d", project.ID, scene.Number), func(taskCtx context.Context) {
				p.pollScene(taskCtx, provider, sceneCopy)
			})
		case "downloading", "download_failed":
			active++
			if scene.NextAttemptAt <= time.Now().Unix() {
				p.startSceneDownload(ctx, provider, scene)
			}
		default:
			active++
		}
	}
	if len(project.Scenes) == ProjectSceneCount && complete == ProjectSceneCount {
		_ = p.app.store.UpdateProjectStatus(project.ID, "combining", "")
	} else if failed > 0 && active == 0 {
		_ = p.app.store.UpdateProjectStatus(project.ID, "failed", "One or more scenes failed. Retry only the failed scene.")
	}
}

func (p *Processor) submitScene(ctx context.Context, provider VideoService, project VideoProject, scene ProjectScene) {
	if err := p.app.store.MarkSceneSubmitting(project.ID, scene.Number); err != nil {
		return
	}
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	generation, err := provider.GenerateVideo(requestContext, GenerateRequest{Prompt: scene.Prompt, Model: project.Model, Duration: ProjectSceneSeconds, AspectRatio: ProjectAspectRatio})
	if err != nil || generation == nil || !safeID(generation.ID) {
		_ = p.app.store.AppendPipelineEvent(project.ID, "scene_submission", "failed", "Video generation submission failed.", scene.Number, 0)
		_ = p.app.store.UpdateScene(project.ID, scene.Number, "failed", "", "Scene submission failed. Retrying this scene may create a duplicate if OpenRouter accepted the interrupted request.")
		p.logger.Warn("scene submission failed", "project_id", project.ID, "scene", scene.Number)
		return
	}
	if generation.Status == "" {
		generation.Status = "queued"
	}
	if err := p.app.store.SetSceneGeneration(project.ID, scene.Number, *generation); err != nil {
		p.logger.Error("save scene generation failed", "project_id", project.ID, "scene", scene.Number)
	}
}

func (p *Processor) pollScene(ctx context.Context, provider VideoService, scene ProjectScene) {
	requestContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	generation, err := provider.GetGeneration(requestContext, scene.ProviderGenerationID)
	if err != nil || generation == nil {
		_ = p.app.store.AppendPipelineEventOnce(scene.ProjectID, "scene_poll", "failed", "Unable to poll video generation status; will try again.", scene.Number, 0)
		return
	}
	status := generation.Status
	if status == "completed" {
		status = "downloading"
	}
	_ = p.app.store.UpdateScene(scene.ProjectID, scene.Number, status, generation.CostUSD, generation.Error)
	if generation.Progress != nil {
		_ = p.app.store.SetSceneProgress(scene.ProjectID, scene.Number, *generation.Progress)
	}
	if status == "downloading" {
		scene.Status = status
		scene.CostUSD = generation.CostUSD
		p.startSceneDownload(ctx, provider, scene)
	}
}

func (p *Processor) startSceneDownload(ctx context.Context, provider VideoService, scene ProjectScene) {
	key := fmt.Sprintf("%s:download:%d", scene.ProjectID, scene.Number)
	p.startProjectTask(ctx, key, func(taskCtx context.Context) {
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
