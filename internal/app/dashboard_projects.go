package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type projectSubmission struct {
	RequestID      string `json:"request_id,omitempty"`
	Topic          string `json:"topic"`
	Category       string `json:"category,omitempty"`
	Model          string `json:"model"`
	ModalAccountID string `json:"modal_account_id,omitempty"`
}

func compatibleProjectModel(model VideoModel) bool {
	return containsInt(model.Durations, ProjectSceneSeconds) && containsString(model.Resolutions, ProjectResolution) && containsString(model.AspectRatios, ProjectAspectRatio)
}

func preferredProjectModel(models []VideoModel) (VideoModel, bool) {
	for _, model := range models {
		if compatibleProjectModel(model) {
			return model, true
		}
	}
	return VideoModel{}, false
}

func (a *dashboardApp) createProject(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input projectSubmission
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.Topic, input.Model = strings.TrimSpace(input.Topic), strings.TrimSpace(input.Model)
	input.Category = strings.TrimSpace(input.Category)
	input.ModalAccountID = strings.TrimSpace(input.ModalAccountID)
	if input.RequestID != "" && !validProjectRequestID(input.RequestID) {
		writeError(w, http.StatusBadRequest, "a valid project request ID is required")
		return
	}
	if input.Category != "" && !validRandomPromptCategory(input.Category) {
		writeError(w, http.StatusBadRequest, "a supported project category is required")
		return
	}
	if input.Topic == "" || len([]rune(input.Topic)) > MaxPromptLength {
		writeError(w, http.StatusBadRequest, "a topic or story idea of at most 4,000 characters is required")
		return
	}
	if input.RequestID != "" {
		project, replayErr := a.store.projectForSubmission(input.RequestID, input.requestHash())
		if replayErr == nil {
			writeJSON(w, http.StatusAccepted, project)
			return
		}
		if !errors.Is(replayErr, sql.ErrNoRows) {
			writeProjectSubmissionError(w, replayErr)
			return
		}
	}
	providerID, err := a.selectedSubmissionProvider(input.ModalAccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, providerID, providerConfigID, err := a.videoProviderSnapshotForAccount(providerID, input.ModalAccountID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	persisted := false
	defer func() {
		if !persisted {
			_ = a.store.DeleteProviderConfigIfUnused(providerConfigID)
		}
	}()
	if providerID == VideoProviderModal {
		var callbackErr error
		if _, ok := provider.(CallbackVideoProvider); ok {
			_, callbackErr = validateVideoCallbackBaseURL(a.videoCallbackBaseURL)
		} else {
			_, callbackErr = a.modalCallbackURL()
		}
		if callbackErr != nil {
			writeError(w, http.StatusUnprocessableEntity, callbackErr.Error())
			return
		}
	}
	models, err := provider.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to validate video models")
		return
	}
	var model VideoModel
	var ok bool
	if input.Model == "" {
		if selected := a.selectedVideoModelForAccount(providerID, input.ModalAccountID); selected != "" {
			model, ok = findModel(models, selected)
			ok = ok && compatibleProjectModel(model)
		} else {
			model, ok = preferredProjectModel(models)
		}
	} else {
		model, ok = findModel(models, input.Model)
		ok = ok && compatibleProjectModel(model)
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "select a video model that supports 6-second clips at 480p in 9:16")
		return
	}
	project, err := a.store.insertProjectSubmission(input.Topic, input.Category, string(providerID), providerConfigID, model.ID, input.RequestID, input.requestHash())
	if err != nil {
		a.logger.Error("create project failed")
		writeProjectSubmissionError(w, err)
		return
	}
	persisted = project.ProviderConfigID == providerConfigID
	writeJSON(w, http.StatusAccepted, project)
}

func writeProjectSubmissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errProjectSubmissionConflict):
		writeError(w, http.StatusConflict, "This project request ID was already used with different options. Start a new project.")
	case errors.Is(err, errProjectSubmissionUnavailable):
		writeError(w, http.StatusConflict, "This project request was already accepted and is no longer available in History. Start a new project.")
	default:
		writeError(w, http.StatusInternalServerError, "unable to create project")
	}
}

func (a *dashboardApp) projects(w http.ResponseWriter, r *http.Request) {
	if requestID := r.URL.Query().Get("request_id"); requestID != "" {
		if !validProjectRequestID(requestID) {
			writeError(w, http.StatusBadRequest, "a valid project request ID is required")
			return
		}
		projects := []VideoProject{}
		project, err := a.store.projectForSubmission(requestID, "")
		if err == nil {
			projects = append(projects, project)
		} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, errProjectSubmissionUnavailable) {
			writeError(w, http.StatusInternalServerError, "unable to load project")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
		return
	}
	projects, err := a.store.Projects(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load projects")
		return
	}
	if projects == nil {
		projects = []VideoProject{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (a *dashboardApp) project(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := a.store.Project(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load project")
		return
	}
	if project.InVault {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (a *dashboardApp) deleteProject(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := a.store.DeleteProject(id); errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrVaultItemInVault) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if errors.Is(err, ErrYouTubeUploadInUse) {
		writeError(w, http.StatusConflict, "Finish or cancel the YouTube upload before deleting this project")
		return
	} else if errors.Is(err, ErrProjectNotTerminal) {
		writeError(w, http.StatusConflict, "active projects cannot be deleted")
		return
	} else if err != nil {
		a.logger.Error("delete project failed", "project_id", id)
		writeError(w, http.StatusInternalServerError, "unable to delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) retryProjectScene(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	number, err := strconv.Atoi(r.PathValue("scene"))
	if !safeID(id) || err != nil || number < 1 || number > ProjectSceneCount {
		writeError(w, http.StatusBadRequest, "invalid project or scene")
		return
	}
	if err := a.store.RetryScene(id, number); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "only a failed scene can be retried")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to retry scene")
		return
	}
	project, err := a.store.Project(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load project")
		return
	}
	writeJSON(w, http.StatusAccepted, project)
}

func (a *dashboardApp) retryProject(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := a.store.RetryProject(id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "only a failed project can be retried")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to retry project")
		return
	}
	project, _ := a.store.Project(id)
	writeJSON(w, http.StatusAccepted, project)
}
