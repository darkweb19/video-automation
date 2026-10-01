package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	apiKeySetting                = "openrouter_api_key"
	videoProviderSetting         = "video_provider"
	modalVideoBaseURLSetting     = "modal_video_base_url"
	modalVideoAPIKeySetting      = "modal_video_api_key"
	modalAccountsMigratedSetting = "modal_video_accounts_migrated"
)

func (a *dashboardApp) openRouterTextProvider() (*OpenRouterClient, error) {
	encrypted, err := a.store.Setting(apiKeySetting)
	if err != nil {
		return nil, errors.New("OpenRouter API key is not configured")
	}
	key, err := a.security.DecryptSetting(apiKeySetting, encrypted)
	if err != nil {
		return nil, err
	}
	client := NewOpenRouterClient(key)
	// The HTTP dashboard and background processor share one store lifecycle.
	client.promptState = &a.store.promptState
	if a.baseURL != "" {
		client.BaseURL = a.baseURL
	}
	return client, nil
}

func (a *dashboardApp) selectedVideoProvider() (VideoProviderID, error) {
	value, err := a.store.Setting(videoProviderSetting)
	if errors.Is(err, sql.ErrNoRows) {
		return VideoProviderOpenRouter, nil
	}
	if err != nil {
		return "", err
	}
	provider := VideoProviderID(value)
	if !validVideoProvider(provider) {
		return "", errors.New("unsupported video provider")
	}
	return provider, nil
}

func modalAccountAAD(id string) string { return "modal_video_account." + id }

func (a *dashboardApp) defaultModalAccountID() string {
	id, err := a.store.Setting("modal_video_default_account_id")
	if err != nil {
		return ""
	}
	return id
}

func (a *dashboardApp) modalVideoProviderForAccount(id string) (VideoService, error) {
	if !safeID(id) {
		return nil, errors.New("select a valid Modal account")
	}
	account, err := a.store.ModalVideoAccount(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("selected Modal account no longer exists")
	}
	if err != nil {
		return nil, err
	}
	key, err := a.security.DecryptSetting(modalAccountAAD(account.ID), account.EncryptedAPIKey)
	if err != nil {
		return nil, errors.New("unable to load selected Modal account")
	}
	return NewModalVideoClient(account.Endpoint, key), nil
}

// migrateLegacyModalAccount preserves old single-account installations. The
// legacy settings remain in place for pre-upgrade immutable snapshot backfill;
// all new account ciphertext uses a stable account-id AAD.
func (a *dashboardApp) migrateLegacyModalAccount() error {
	if migrated, err := a.store.Setting(modalAccountsMigratedSetting); err == nil && migrated == "1" {
		return nil
	}
	accounts, err := a.store.ModalVideoAccounts()
	if err != nil {
		return err
	}
	if len(accounts) > 0 {
		if a.defaultModalAccountID() == "" {
			if err := a.store.SetSetting("modal_video_default_account_id", accounts[0].ID); err != nil {
				return err
			}
		}
		return a.store.SetSetting(modalAccountsMigratedSetting, "1")
	}
	endpoint, endpointErr := a.store.Setting(modalVideoBaseURLSetting)
	ciphertext, keyErr := a.store.Setting(modalVideoAPIKeySetting)
	if endpointErr != nil || keyErr != nil || strings.TrimSpace(endpoint) == "" {
		return a.store.SetSetting(modalAccountsMigratedSetting, "1")
	}
	key, err := a.security.DecryptSetting(modalVideoAPIKeySetting, ciphertext)
	if err != nil {
		return err
	}
	id, err := newModalVideoAccountID()
	if err != nil {
		return err
	}
	encrypted, err := a.security.EncryptSetting(modalAccountAAD(id), key)
	if err != nil {
		return err
	}
	if _, err := a.store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Legacy Modal account", Endpoint: endpoint, EncryptedAPIKey: encrypted}); err != nil {
		return err
	}
	if err := a.store.SetSetting("modal_video_default_account_id", id); err != nil {
		return err
	}
	return a.store.SetSetting(modalAccountsMigratedSetting, "1")
}

func (a *dashboardApp) videoProvider(provider VideoProviderID) (VideoService, error) {
	switch provider {
	case VideoProviderOpenRouter:
		return a.openRouterTextProvider()
	case VideoProviderModal:
		if id := a.defaultModalAccountID(); id != "" {
			return a.modalVideoProviderForAccount(id)
		}
		baseURL, err := a.store.Setting(modalVideoBaseURLSetting)
		if err != nil {
			return nil, errors.New("Modal video base URL is not configured")
		}
		key, err := a.store.Setting(modalVideoAPIKeySetting)
		if err != nil {
			return nil, errors.New("Modal video API key is not configured")
		}
		plain, err := a.security.DecryptSetting(modalVideoAPIKeySetting, key)
		if err != nil {
			return nil, err
		}
		return NewModalVideoClient(baseURL, plain), nil
	default:
		return nil, errors.New("unsupported video provider")
	}
}

func (a *dashboardApp) videoProviderFromConfig(config ProviderConfig) (VideoService, error) {
	provider := VideoProviderID(config.Provider)
	if !validVideoProvider(provider) {
		return nil, errors.New("unsupported video provider")
	}
	key, err := a.security.DecryptSetting("video_provider_config."+config.ID, config.EncryptedAPIKey)
	if err != nil {
		return nil, err
	}
	switch provider {
	case VideoProviderOpenRouter:
		client := NewOpenRouterClient(key)
		if config.BaseURL != "" {
			client.BaseURL = config.BaseURL
		}
		return client, nil
	case VideoProviderModal:
		return NewModalVideoClient(config.BaseURL, key), nil
	default:
		return nil, errors.New("unsupported video provider")
	}
}

func (a *dashboardApp) activeVideoProviderSnapshot() (VideoService, VideoProviderID, string, error) {
	provider, err := a.selectedVideoProvider()
	if err != nil {
		return nil, "", "", err
	}
	return a.videoProviderSnapshot(provider)
}

func (a *dashboardApp) videoProviderSnapshot(provider VideoProviderID) (VideoService, VideoProviderID, string, error) {
	return a.videoProviderSnapshotForAccount(provider, "")
}

// videoProviderSnapshotForAccount creates an immutable credential snapshot at
// submission time. Deleting or editing a reusable Modal account can therefore
// never retarget an already accepted job.
func (a *dashboardApp) videoProviderSnapshotForAccount(provider VideoProviderID, modalAccountID string) (VideoService, VideoProviderID, string, error) {
	var baseURL, encryptedSetting string
	var err error
	switch provider {
	case VideoProviderOpenRouter:
		encryptedSetting, err = a.store.Setting(apiKeySetting)
		baseURL = a.baseURL
	case VideoProviderModal:
		if modalAccountID != "" {
			account, accountErr := a.store.ModalVideoAccount(modalAccountID)
			if errors.Is(accountErr, sql.ErrNoRows) {
				return nil, "", "", errors.New("selected Modal account no longer exists")
			}
			if accountErr != nil {
				return nil, "", "", accountErr
			}
			baseURL = account.Endpoint
			encryptedSetting = account.EncryptedAPIKey
			key, decryptErr := a.security.DecryptSetting(modalAccountAAD(account.ID), encryptedSetting)
			if decryptErr != nil {
				return nil, "", "", errors.New("unable to load selected Modal account")
			}
			return a.createProviderSnapshot(provider, baseURL, key)
		}
		baseURL, err = a.store.Setting(modalVideoBaseURLSetting)
		if err == nil {
			encryptedSetting, err = a.store.Setting(modalVideoAPIKeySetting)
		}
	}
	if err != nil {
		return nil, "", "", errors.New("selected video provider is not configured")
	}
	key, err := a.security.DecryptSetting(map[VideoProviderID]string{VideoProviderOpenRouter: apiKeySetting, VideoProviderModal: modalVideoAPIKeySetting}[provider], encryptedSetting)
	if err != nil {
		return nil, "", "", err
	}
	return a.createProviderSnapshot(provider, baseURL, key)
}

func (a *dashboardApp) createProviderSnapshot(provider VideoProviderID, baseURL, key string) (VideoService, VideoProviderID, string, error) {
	configID, err := newProviderConfigID()
	if err != nil {
		return nil, "", "", err
	}
	encrypted, err := a.security.EncryptSetting("video_provider_config."+configID, key)
	if err != nil {
		return nil, "", "", err
	}
	config, err := a.store.InsertProviderConfig(ProviderConfig{ID: configID, Provider: string(provider), BaseURL: baseURL, EncryptedAPIKey: encrypted})
	if err != nil {
		return nil, "", "", err
	}
	service, err := a.videoProviderFromConfig(config)
	return service, provider, config.ID, err
}

// BackfillLegacyProviderSnapshots runs once when the worker starts after an
// upgrade. It captures existing mutable settings before a future account
// switch. Unbackfilled legacy rows are deliberately fail-safe skipped.
func (a *dashboardApp) backfillLegacyProviderSnapshots() error {
	providers, err := a.store.LegacyPendingProviderIDs()
	if err != nil {
		return err
	}
	for _, provider := range providers {
		_, _, configID, err := a.videoProviderSnapshot(provider)
		if err != nil {
			return fmt.Errorf("snapshot legacy %s provider work: %w", provider, err)
		}
		if err := a.store.AssignLegacyProviderConfig(provider, configID); err != nil {
			return err
		}
	}
	return nil
}

// videoProviderForSnapshot uses immutable request-time configuration. A
// legacy row without a completed migration is never sent with mutable current
// credentials because that could target a different account after a switch.
func (a *dashboardApp) videoProviderForSnapshot(configID string, legacyProvider VideoProviderID) (VideoService, error) {
	if configID == "" {
		return nil, errors.New("legacy video work is waiting for a provider configuration snapshot; retry after configuring the original provider")
	}
	config, err := a.store.ProviderConfig(configID)
	if err != nil {
		return nil, err
	}
	return a.videoProviderFromConfig(config)
}

func (a *dashboardApp) activeVideoProvider() (VideoService, VideoProviderID, error) {
	provider, err := a.selectedVideoProvider()
	if err != nil {
		return nil, "", err
	}
	service, err := a.videoProvider(provider)
	return service, provider, err
}

func (a *dashboardApp) models(w http.ResponseWriter, r *http.Request) {
	requested := VideoProviderID(r.URL.Query().Get("provider"))
	modalAccountID := strings.TrimSpace(r.URL.Query().Get("modal_account_id"))
	var err error
	if requested == "" {
		requested, err = a.selectedVideoProvider()
	} else if !validVideoProvider(requested) {
		writeError(w, http.StatusBadRequest, "unsupported video provider")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load video provider")
		return
	}
	var provider VideoService
	if requested == VideoProviderModal && modalAccountID != "" {
		provider, err = a.modalVideoProviderForAccount(modalAccountID)
	} else {
		provider, err = a.videoProvider(requested)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := provider.ListVideoModels(r.Context())
	if err != nil {
		a.logger.Error("models request failed")
		writeError(w, http.StatusBadGateway, "unable to load models")
		return
	}
	if models == nil {
		models = []VideoModel{}
	}
	effectiveModalAccountID := modalAccountID
	if requested == VideoProviderModal && effectiveModalAccountID == "" {
		effectiveModalAccountID = a.defaultModalAccountID()
	}
	writeJSON(w, http.StatusOK, struct {
		Models          []VideoModel `json:"models"`
		MaxPromptLength int          `json:"max_prompt_length"`
		Provider        string       `json:"provider"`
		SelectedModel   string       `json:"selected_model,omitempty"`
	}{models, MaxPromptLength, string(requested), a.selectedVideoModelForAccount(requested, effectiveModalAccountID)})
}

func (a *dashboardApp) selectedVideoModel(provider VideoProviderID) string {
	return a.selectedVideoModelForAccount(provider, "")
}

func (a *dashboardApp) selectedVideoModelForAccount(provider VideoProviderID, modalAccountID string) string {
	key := "video_model." + string(provider)
	if provider == VideoProviderModal && modalAccountID != "" {
		key += "." + modalAccountID
	}
	model, err := a.store.Setting(key)
	if err != nil {
		return ""
	}
	return model
}
