package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func newProjectID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "project_" + hex.EncodeToString(raw), nil
}

func (s *Store) InsertProject(topic, model string) (VideoProject, error) {
	return s.InsertProjectForProvider(topic, string(VideoProviderOpenRouter), model)
}

func (s *Store) InsertProjectForProvider(topic, provider, model string) (VideoProject, error) {
	id, err := newProjectID()
	if err != nil {
		return VideoProject{}, err
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return VideoProject{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO video_projects(id,topic,model,video_provider,status,created_at,updated_at) VALUES(?,?,?,?,'planning',?,?)`, id, topic, model, provider, now, now)
	if err != nil {
		return VideoProject{}, err
	}
	if err := appendPipelineEvent(tx, id, "project", "created", "Topic received; project created.", 0, 0, now); err != nil {
		return VideoProject{}, err
	}
	if err := tx.Commit(); err != nil {
		return VideoProject{}, err
	}
	return s.Project(id)
}

func validateStoryPlan(plan StoryPlan) error {
	if strings.TrimSpace(plan.Title) == "" || strings.TrimSpace(plan.Story) == "" || strings.TrimSpace(plan.Script) == "" || strings.TrimSpace(plan.Continuity) == "" {
		return errors.New("script plan is missing required content")
	}
	if len(plan.Scenes) != ProjectSceneCount {
		return fmt.Errorf("script plan must contain exactly %d scenes", ProjectSceneCount)
	}
	seen := make(map[int]bool, ProjectSceneCount)
	for _, scene := range plan.Scenes {
		if scene.Number < 1 || scene.Number > ProjectSceneCount || seen[scene.Number] {
			return errors.New("script plan has invalid scene numbering")
		}
		seen[scene.Number] = true
		if strings.TrimSpace(scene.Title) == "" || strings.TrimSpace(scene.Script) == "" || strings.TrimSpace(scene.VideoPrompt) == "" {
			return fmt.Errorf("scene %d is missing required content", scene.Number)
		}
	}
	return nil
}

func (s *Store) SaveStoryPlan(projectID string, plan StoryPlan) error {
	if err := validateStoryPlan(plan); err != nil {
		return err
	}
	plan.Scenes = append([]StoryPlanScene(nil), plan.Scenes...)
	if err := appendContinuityBible(&plan); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE video_projects SET title=?,story=?,script=?,continuity=?,status='generating',error='',updated_at=? WHERE id=? AND status='planning'`, plan.Title, plan.Story, plan.Script, plan.Continuity, now, projectID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	for _, scene := range plan.Scenes {
		_, err = tx.Exec(`INSERT INTO project_scenes(project_id,scene_number,title,scene_script,prompt,status,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?)`, projectID, scene.Number, scene.Title, scene.Script, scene.VideoPrompt, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const projectColumns = `id,topic,title,story,script,continuity,model,video_provider,status,error,final_video_path,final_size_bytes,created_at,updated_at`
const sceneColumns = `project_id,scene_number,title,scene_script,prompt,status,progress,attempts,provider_generation_id,cost_usd,video_path,size_bytes,error,download_attempts,next_attempt_at,created_at,updated_at`

func appendPipelineEvent(execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}, projectID, stage, status, message string, sceneNumber, attempt int, createdAt int64) error {
	_, err := execer.Exec(`INSERT INTO project_pipeline_events(project_id,stage,status,message,scene_number,attempt,created_at) VALUES(?,?,?,?,?,?,?)`, projectID, stage, status, message, sceneNumber, attempt, createdAt)
	return err
}

// AppendPipelineEvent appends a durable project event. Call it for meaningful
// workflow transitions only; polling progress is intentionally not logged.
func (s *Store) AppendPipelineEvent(projectID, stage, status, message string, sceneNumber, attempt int) error {
	return appendPipelineEvent(s.db, projectID, stage, status, message, sceneNumber, attempt, time.Now().Unix())
}

// AppendPipelineEventOnce prevents repeated transient failures from growing
// the audit trail on every processor tick. A later different status remains
// visible as a new transition.
func (s *Store) AppendPipelineEventOnce(projectID, stage, status, message string, sceneNumber, attempt int) error {
	var previousStage, previousStatus string
	var previousScene int
	err := s.db.QueryRow(`SELECT stage,status,scene_number FROM project_pipeline_events WHERE project_id=? AND stage=? AND scene_number=? ORDER BY id DESC LIMIT 1`, projectID, stage, sceneNumber).Scan(&previousStage, &previousStatus, &previousScene)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && previousStage == stage && previousStatus == status && previousScene == sceneNumber {
		return nil
	}
	return s.AppendPipelineEvent(projectID, stage, status, message, sceneNumber, attempt)
}

func (s *Store) SaveTextGenerationTrace(projectID string, trace TextGenerationTrace) error {
	if len(trace.RawResponse) > MaxScriptRawResponseBytes {
		return fmt.Errorf("raw script response exceeds %d bytes", MaxScriptRawResponseBytes)
	}
	now := time.Now().Unix()
	if trace.UpdatedAt == 0 {
		trace.UpdatedAt = now
	}
	_, err := s.db.Exec(`INSERT INTO project_text_generation(project_id,router_model,actual_model,system_prompt,user_prompt,response_schema,raw_response,status,error,started_at,completed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET router_model=excluded.router_model,actual_model=excluded.actual_model,system_prompt=excluded.system_prompt,user_prompt=excluded.user_prompt,response_schema=excluded.response_schema,raw_response=excluded.raw_response,status=excluded.status,error=excluded.error,started_at=excluded.started_at,completed_at=excluded.completed_at,updated_at=excluded.updated_at`, projectID, trace.RouterModel, trace.ActualModel, trace.SystemPrompt, trace.UserPrompt, trace.ResponseSchema, trace.RawResponse, trace.Status, trace.Error, trace.StartedAt, trace.CompletedAt, trace.UpdatedAt)
	return err
}

func scanTextGenerationTrace(scanner interface{ Scan(...any) error }) (TextGenerationTrace, error) {
	var trace TextGenerationTrace
	err := scanner.Scan(&trace.RouterModel, &trace.ActualModel, &trace.SystemPrompt, &trace.UserPrompt, &trace.ResponseSchema, &trace.RawResponse, &trace.Status, &trace.Error, &trace.StartedAt, &trace.CompletedAt, &trace.UpdatedAt)
	return trace, err
}

func scanProject(scanner interface{ Scan(...any) error }) (VideoProject, error) {
	var project VideoProject
	err := scanner.Scan(&project.ID, &project.Topic, &project.Title, &project.Story, &project.Script, &project.Continuity, &project.Model, &project.VideoProvider, &project.Status, &project.Error, &project.FinalVideoPath, &project.FinalSizeBytes, &project.CreatedAt, &project.UpdatedAt)
	project.FinalVideoReady = project.FinalVideoPath != ""
	return project, err
}

func scanScene(scanner interface{ Scan(...any) error }) (ProjectScene, error) {
	var scene ProjectScene
	err := scanner.Scan(&scene.ProjectID, &scene.Number, &scene.Title, &scene.Script, &scene.Prompt, &scene.Status, &scene.Progress, &scene.Attempts, &scene.ProviderGenerationID, &scene.CostUSD, &scene.VideoPath, &scene.SizeBytes, &scene.Error, &scene.DownloadAttempts, &scene.NextAttemptAt, &scene.CreatedAt, &scene.UpdatedAt)
	scene.VideoReady = scene.VideoPath != ""
	return scene, err
}

func (s *Store) Project(id string) (VideoProject, error) {
	project, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM video_projects WHERE id=?`, id))
	if err != nil {
		return VideoProject{}, err
	}
	rows, err := s.db.Query(`SELECT `+sceneColumns+` FROM project_scenes WHERE project_id=? ORDER BY scene_number`, id)
	if err != nil {
		return VideoProject{}, err
	}
	defer rows.Close()
	for rows.Next() {
		scene, err := scanScene(rows)
		if err != nil {
			return VideoProject{}, err
		}
		project.Scenes = append(project.Scenes, scene)
	}
	if err := rows.Err(); err != nil {
		return VideoProject{}, err
	}
	trace, err := scanTextGenerationTrace(s.db.QueryRow(`SELECT router_model,actual_model,system_prompt,user_prompt,response_schema,raw_response,status,error,started_at,completed_at,updated_at FROM project_text_generation WHERE project_id=?`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return VideoProject{}, err
	}
	if err == nil {
		project.TextGeneration = trace
	}
	eventRows, err := s.db.Query(`SELECT id,stage,status,message,scene_number,attempt,created_at FROM project_pipeline_events WHERE project_id=? ORDER BY id`, id)
	if err != nil {
		return VideoProject{}, err
	}
	defer eventRows.Close()
	project.PipelineEvents = make([]PipelineEvent, 0)
	for eventRows.Next() {
		var event PipelineEvent
		if err := eventRows.Scan(&event.ID, &event.Stage, &event.Status, &event.Message, &event.SceneNumber, &event.Attempt, &event.CreatedAt); err != nil {
			return VideoProject{}, err
		}
		project.PipelineEvents = append(project.PipelineEvents, event)
	}
	if err := eventRows.Err(); err != nil {
		return VideoProject{}, err
	}
	s.populateProjectSummary(&project)
	return project, nil
}

func (s *Store) Projects(limit int) ([]VideoProject, error) {
	if limit < 1 || limit > 100 {
		limit = 24
	}
	rows, err := s.db.Query(`SELECT `+projectColumns+` FROM video_projects ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []VideoProject
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range projects {
		full, err := s.Project(projects[index].ID)
		if err != nil {
			return nil, err
		}
		projects[index] = full
	}
	return projects, nil
}

func (s *Store) PendingProjects(ctx context.Context) ([]VideoProject, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM video_projects WHERE status IN ('planning','generating','combining') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	var projects []VideoProject
	for _, id := range ids {
		project, err := s.Project(id)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *Store) populateProjectSummary(project *VideoProject) {
	completed := 0
	totalCost := 0.0
	for _, scene := range project.Scenes {
		if scene.Status == "completed" {
			completed++
		}
		var cost float64
		_, _ = fmt.Sscanf(scene.CostUSD, "%f", &cost)
		totalCost += cost
	}
	project.TotalCostUSD = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", totalCost), "0"), ".")
	switch project.Status {
	case "planning":
		project.Progress = 5
	case "generating":
		project.Progress = 10 + completed*16
	case "combining":
		project.Progress = 95
	case "completed":
		project.Progress = 100
	default:
		project.Progress = completed * 16
	}
}

func (s *Store) UpdateProjectStatus(id, status, message string) error {
	var previous string
	if err := s.db.QueryRow(`SELECT status FROM video_projects WHERE id=?`, id).Scan(&previous); err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE video_projects SET status=?,error=?,updated_at=? WHERE id=?`, status, message, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if previous != status {
		return s.AppendPipelineEvent(id, "project", status, message, 0, 0)
	}
	return nil
}

func (s *Store) MarkSceneSubmitting(projectID string, number int) error {
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM project_scenes WHERE project_id=? AND scene_number=? AND status='pending'`, projectID, number).Scan(&attempts); err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE project_scenes SET status='submitting',attempts=attempts+1,error='',updated_at=? WHERE project_id=? AND scene_number=? AND status='pending'`, time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendPipelineEvent(projectID, "scene_submission", "started", "Scene submitted to video generation.", number, attempts+1)
}

func (s *Store) SetSceneGeneration(projectID string, number int, generation Generation) error {
	status := generation.Status
	if status == "completed" {
		status = "downloading"
	}
	progress := 10
	if generation.Progress != nil {
		progress = min(100, max(0, *generation.Progress))
	}
	result, err := s.db.Exec(`UPDATE project_scenes SET provider_generation_id=?,status=?,progress=?,cost_usd=?,error=?,next_attempt_at=0,updated_at=? WHERE project_id=? AND scene_number=? AND status='submitting'`, generation.ID, status, progress, generation.CostUSD, generation.Error, time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendPipelineEvent(projectID, "scene_submission", "completed", "Video generation request accepted.", number, 0)
}

func (s *Store) UpdateScene(projectID string, number int, status, cost, message string) error {
	var previous string
	if err := s.db.QueryRow(`SELECT status FROM project_scenes WHERE project_id=? AND scene_number=?`, projectID, number).Scan(&previous); err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE project_scenes SET status=?,progress=CASE WHEN ?='completed' THEN 100 WHEN ?='downloading' THEN MAX(progress,90) WHEN ?='processing' THEN MAX(progress,25) WHEN ?='queued' THEN MAX(progress,10) ELSE progress END,cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=? WHERE project_id=? AND scene_number=?`, status, status, status, status, status, cost, cost, message, time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if previous != status {
		return s.AppendPipelineEvent(projectID, "scene_poll", status, "Video generation status changed to "+status+".", number, 0)
	}
	return nil
}

func (s *Store) SetSceneProgress(projectID string, number, progress int) error {
	progress = min(100, max(0, progress))
	_, err := s.db.Exec(`UPDATE project_scenes SET progress=?,updated_at=? WHERE project_id=? AND scene_number=?`, progress, time.Now().Unix(), projectID, number)
	return err
}

func (s *Store) ScheduleSceneDownloadRetry(projectID string, number int) error {
	var attempts int
	if err := s.db.QueryRow(`SELECT download_attempts FROM project_scenes WHERE project_id=? AND scene_number=?`, projectID, number).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	backoff := time.Second * time.Duration(1<<min(attempts-1, 8))
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	_, err := s.db.Exec(`UPDATE project_scenes SET status='download_failed',download_attempts=?,next_attempt_at=?,error='Scene download will retry automatically',updated_at=? WHERE project_id=? AND scene_number=?`, attempts, time.Now().Add(backoff).Unix(), time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	return s.AppendPipelineEvent(projectID, "scene_download", "retry", "Scene download failed; retry scheduled.", number, attempts)
}

func (s *Store) MarkSceneVideoReady(projectID string, number int, path string, size int64) error {
	result, err := s.db.Exec(`UPDATE project_scenes SET status='completed',progress=100,video_path=?,size_bytes=?,error='',next_attempt_at=0,updated_at=? WHERE project_id=? AND scene_number=?`, path, size, time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendPipelineEvent(projectID, "scene_download", "completed", "Scene video downloaded and stored.", number, 0)
}

func (s *Store) RetryScene(projectID string, number int) error {
	result, err := s.db.Exec(`UPDATE project_scenes SET status='pending',progress=0,provider_generation_id='',cost_usd='',video_path='',size_bytes=0,error='',download_attempts=0,next_attempt_at=0,updated_at=? WHERE project_id=? AND scene_number=? AND status='failed'`, time.Now().Unix(), projectID, number)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	_, err = s.db.Exec(`UPDATE video_projects SET status='generating',error='',updated_at=? WHERE id=?`, time.Now().Unix(), projectID)
	if err != nil {
		return err
	}
	return s.AppendPipelineEvent(projectID, "scene_retry", "started", "Failed scene reset for another submission.", number, 0)
}

func (s *Store) RetryProject(projectID string) error {
	project, err := s.Project(projectID)
	if err != nil || project.Status != "failed" {
		return sql.ErrNoRows
	}
	status := "planning"
	if project.Script != "" {
		status = "generating"
		allComplete := len(project.Scenes) == ProjectSceneCount
		for _, scene := range project.Scenes {
			allComplete = allComplete && scene.Status == "completed"
		}
		if allComplete {
			status = "combining"
		}
	}
	result, err := s.db.Exec(`UPDATE video_projects SET status=?,error='',updated_at=? WHERE id=? AND status='failed'`, status, time.Now().Unix(), projectID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendPipelineEvent(projectID, "project", "retry", "Project retry requested.", 0, 0)
}

func (s *Store) SceneVideoPath(projectID string, number int) string {
	return filepath.Join(s.projectDir, projectID, fmt.Sprintf("scene-%d.mp4", number))
}

func (s *Store) ProjectFinalPath(projectID string) string {
	return filepath.Join(s.projectDir, projectID, "final.mp4")
}

func (s *Store) MarkProjectReady(projectID, path string, size int64) error {
	result, err := s.db.Exec(`UPDATE video_projects SET status='completed',final_video_path=?,final_size_bytes=?,error='',updated_at=? WHERE id=?`, path, size, time.Now().Unix(), projectID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return s.AppendPipelineEvent(projectID, "combine", "completed", "Final video combined; project complete.", 0, 0)
}
