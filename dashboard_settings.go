package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (a *dashboardApp) settings(w http.ResponseWriter, _ *http.Request) {
	provider, err := a.selectedVideoProvider()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load settings")
		return
	}
	_, openRouterErr := a.store.Setting(apiKeySetting)
	modalBaseURL, _ := a.store.Setting(modalVideoBaseURLSetting)
	_, modalKeyErr := a.store.Setting(modalVideoAPIKeySetting)
	defaultModalAccountID := a.defaultModalAccountID()
	writeJSON(w, http.StatusOK, map[string]any{
		"video_provider":                 string(provider),
		"openrouter_api_key_configured":  openRouterErr == nil,
		"modal_video_base_url":           modalBaseURL,
		"modal_video_api_key_configured": modalKeyErr == nil,
		"video_models":                   map[string]string{"openrouter": a.selectedVideoModel(VideoProviderOpenRouter), "modal": a.selectedVideoModelForAccount(VideoProviderModal, defaultModalAccountID)},
		"vault_code_configured":          a.vaultCodeConfigured(),
	})
}

type modalAccountInput struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// BaseURL keeps the former settings client compatible while the dashboard
	// moves to named accounts.
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func (input modalAccountInput) normalizedEndpoint() string {
	if strings.TrimSpace(input.Endpoint) != "" {
		return strings.TrimSpace(input.Endpoint)
	}
	return strings.TrimSpace(input.BaseURL)
}

func validModalAccountName(name string) bool {
	return name != "" && len([]rune(name)) <= 100
}

func (a *dashboardApp) modalAccounts(w http.ResponseWriter, _ *http.Request) {
	a.writeModalAccounts(w)
}

func (a *dashboardApp) writeModalAccounts(w http.ResponseWriter) {
	accounts, err := a.store.ModalVideoAccounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Modal accounts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts, "active_id": a.defaultModalAccountID()})
}

func (a *dashboardApp) setActiveModalAccount(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		AccountID string `json:"account_id"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	if !safeID(input.AccountID) {
		writeError(w, http.StatusBadRequest, "select a valid Modal account")
		return
	}
	if _, err := a.store.ModalVideoAccount(input.AccountID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Modal account not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Modal account")
		return
	}
	if err := a.store.SetSetting("modal_video_default_account_id", input.AccountID); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to select Modal account")
		return
	}
	a.writeModalAccounts(w)
}

func (a *dashboardApp) createModalAccount(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input modalAccountInput
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validModalAccountName(input.Name) {
		writeError(w, http.StatusBadRequest, "enter an account name of at most 100 characters")
		return
	}
	endpoint, err := validateModalBaseURL(input.normalizedEndpoint())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(input.APIKey)
	if len(key) < 10 || len(key) > 500 {
		writeError(w, http.StatusBadRequest, "enter a valid Modal video API key")
		return
	}
	if _, err := NewModalVideoClient(endpoint, key).ListVideoModels(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "unable to test Modal video connection")
		return
	}
	id, err := newModalVideoAccountID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to create Modal account")
		return
	}
	encrypted, err := a.security.EncryptSetting(modalAccountAAD(id), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Modal account")
		return
	}
	account, err := a.store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: input.Name, Endpoint: endpoint, EncryptedAPIKey: encrypted})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Modal account")
		return
	}
	if a.defaultModalAccountID() == "" {
		if err := a.store.SetSetting("modal_video_default_account_id", account.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "unable to select Modal account")
			return
		}
	}
	writeJSON(w, http.StatusCreated, account)
}

func (a *dashboardApp) updateModalAccount(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid Modal account")
		return
	}
	var input modalAccountInput
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	account, err := a.store.ModalVideoAccount(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Modal account not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Modal account")
		return
	}
	account.Name = strings.TrimSpace(input.Name)
	if !validModalAccountName(account.Name) {
		writeError(w, http.StatusBadRequest, "enter an account name of at most 100 characters")
		return
	}
	account.Endpoint, err = validateModalBaseURL(input.normalizedEndpoint())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if key := strings.TrimSpace(input.APIKey); key != "" {
		if len(key) < 10 || len(key) > 500 {
			writeError(w, http.StatusBadRequest, "enter a valid Modal video API key")
			return
		}
		account.EncryptedAPIKey, err = a.security.EncryptSetting(modalAccountAAD(account.ID), key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to save Modal account")
			return
		}
	}
	key, err := a.security.DecryptSetting(modalAccountAAD(account.ID), account.EncryptedAPIKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Modal account")
		return
	}
	if _, err := NewModalVideoClient(account.Endpoint, key).ListVideoModels(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "unable to test Modal video connection")
		return
	}
	account, err = a.store.UpdateModalVideoAccount(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Modal account")
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (a *dashboardApp) deleteModalAccount(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid Modal account")
		return
	}
	if a.defaultModalAccountID() == id {
		writeError(w, http.StatusConflict, "select a different Modal account before deleting this account")
		return
	}
	if err := a.store.DeleteModalVideoAccount(id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Modal account not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to delete Modal account")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) updateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		APIKey string `json:"api_key"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if len(input.APIKey) < 10 || len(input.APIKey) > 500 {
		writeError(w, http.StatusBadRequest, "enter a valid API key")
		return
	}
	client := NewOpenRouterClient(input.APIKey)
	if a.baseURL != "" {
		client.BaseURL = a.baseURL
	}
	if _, err := client.ListVideoModels(r.Context()); err != nil {
		var upstream *upstreamError
		if errors.As(err, &upstream) && (upstream.StatusCode == http.StatusUnauthorized || upstream.StatusCode == http.StatusForbidden) {
			writeError(w, http.StatusBadRequest, "API key is invalid or not authorized")
		} else {
			writeError(w, http.StatusBadGateway, "unable to test API key")
		}
		return
	}
	encrypted, err := a.security.EncryptSetting(apiKeySetting, input.APIKey)
	if err != nil || a.store.SetSetting(apiKeySetting, encrypted) != nil {
		writeError(w, http.StatusInternalServerError, "unable to save API key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"openrouter_api_key_configured": true})
}

func (a *dashboardApp) updateVideoProvider(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		Provider VideoProviderID `json:"video_provider"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !validVideoProvider(input.Provider) {
		writeError(w, http.StatusBadRequest, "unsupported video provider")
		return
	}
	if err := a.store.SetSetting(videoProviderSetting, string(input.Provider)); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save video provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"video_provider": string(input.Provider)})
}

func (a *dashboardApp) updateModalSettings(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	baseURL, err := validateModalBaseURL(input.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" {
		encrypted, settingErr := a.store.Setting(modalVideoAPIKeySetting)
		if settingErr != nil {
			writeError(w, http.StatusBadRequest, "enter a Modal video API key")
			return
		}
		key, err = a.security.DecryptSetting(modalVideoAPIKeySetting, encrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to load Modal API key")
			return
		}
	}
	if len(key) < 10 || len(key) > 500 {
		writeError(w, http.StatusBadRequest, "enter a valid Modal video API key")
		return
	}
	if _, err := NewModalVideoClient(baseURL, key).ListVideoModels(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "unable to test Modal video connection")
		return
	}
	if err := a.store.SetSetting(modalVideoBaseURLSetting, baseURL); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Modal settings")
		return
	}
	if strings.TrimSpace(input.APIKey) != "" {
		encrypted, err := a.security.EncryptSetting(modalVideoAPIKeySetting, key)
		if err != nil || a.store.SetSetting(modalVideoAPIKeySetting, encrypted) != nil {
			writeError(w, http.StatusInternalServerError, "unable to save Modal API key")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"modal_video_base_url": baseURL, "modal_video_api_key_configured": true})
}

func (a *dashboardApp) updateVideoModel(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		Provider       VideoProviderID `json:"provider"`
		Model          string          `json:"model"`
		ModalAccountID string          `json:"modal_account_id,omitempty"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.Provider == "" {
		input.Provider, _ = a.selectedVideoProvider()
	}
	input.ModalAccountID = strings.TrimSpace(input.ModalAccountID)
	if !validVideoProvider(input.Provider) || strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "a supported provider and model are required")
		return
	}
	if input.Provider == VideoProviderModal && !safeID(input.ModalAccountID) {
		writeError(w, http.StatusBadRequest, "select a Modal account before saving a Modal model")
		return
	}
	if input.Provider != VideoProviderModal && input.ModalAccountID != "" {
		writeError(w, http.StatusBadRequest, "a Modal account can only be used with the Modal provider")
		return
	}
	var (
		service VideoService
		err     error
	)
	if input.Provider == VideoProviderModal {
		service, err = a.modalVideoProviderForAccount(input.ModalAccountID)
	} else {
		service, err = a.videoProvider(input.Provider)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := service.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to validate video model")
		return
	}
	if _, ok := findModel(models, strings.TrimSpace(input.Model)); !ok {
		writeError(w, http.StatusBadRequest, "selected model is unavailable")
		return
	}
	modelSetting := "video_model." + string(input.Provider)
	if input.Provider == VideoProviderModal {
		modelSetting += "." + input.ModalAccountID
	}
	if err := a.store.SetSetting(modelSetting, strings.TrimSpace(input.Model)); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save video model")
		return
	}
	response := map[string]string{"provider": string(input.Provider), "model": strings.TrimSpace(input.Model)}
	if input.Provider == VideoProviderModal {
		response["modal_account_id"] = input.ModalAccountID
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *dashboardApp) testVideoProvider(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	service, provider, err := a.activeVideoProvider()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := service.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to test video provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": string(provider), "model_count": len(models)})
}
