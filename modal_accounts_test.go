package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyModalAccountMigrationIsRestartSafeAndUsesStableAAD(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := "legacy-modal-secret"
	ciphertext, err := security.EncryptSetting(modalVideoAPIKeySetting, legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(modalVideoBaseURLSetting, "https://modal.example/v1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(modalVideoAPIKeySetting, ciphertext); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	if err := app.migrateLegacyModalAccount(); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ModalVideoAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%#v err=%v", accounts, err)
	}
	if accounts[0].EncryptedAPIKey == legacyKey || app.defaultModalAccountID() != accounts[0].ID {
		t.Fatalf("unsafe migration account=%#v default=%q", accounts[0], app.defaultModalAccountID())
	}
	plain, err := security.DecryptSetting(modalAccountAAD(accounts[0].ID), accounts[0].EncryptedAPIKey)
	if err != nil || plain != legacyKey {
		t.Fatalf("migrated key = %q, %v", plain, err)
	}
	if err := app.migrateLegacyModalAccount(); err != nil {
		t.Fatal(err)
	}
	accounts, err = store.ModalVideoAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("migration duplicated accounts=%#v err=%v", accounts, err)
	}
	// A migrated legacy account may be deleted after another account becomes
	// active. The migration-complete marker must keep old settings from
	// recreating it on restart.
	otherID := "modal_account_other"
	otherCiphertext, err := security.EncryptSetting(modalAccountAAD(otherID), "another-modal-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: otherID, Name: "Other", Endpoint: "https://modal-other.example/v1", EncryptedAPIKey: otherCiphertext}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting("modal_video_default_account_id", otherID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteModalVideoAccount(accounts[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := app.migrateLegacyModalAccount(); err != nil {
		t.Fatal(err)
	}
	accounts, err = store.ModalVideoAccounts()
	if err != nil || len(accounts) != 1 || accounts[0].ID != otherID {
		t.Fatalf("deleted legacy account reappeared: %#v err=%v", accounts, err)
	}
}

func TestModalAccountSnapshotSurvivesAccountDeletion(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer modal-account-secret" {
			t.Fatalf("unexpected authorization")
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	id := "modal_account_snapshot"
	encrypted, err := security.EncryptSetting(modalAccountAAD(id), "modal-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Snapshot account", Endpoint: server.URL, EncryptedAPIKey: encrypted}); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	_, _, snapshotID, err := app.videoProviderSnapshotForAccount(VideoProviderModal, id)
	if err != nil {
		t.Fatal(err)
	}
	activeID := "modal_account_active_after_snapshot"
	activeKey, err := security.EncryptSetting(modalAccountAAD(activeID), "active-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: activeID, Name: "Active account", Endpoint: server.URL, EncryptedAPIKey: activeKey}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting("modal_video_default_account_id", activeID); err != nil {
		t.Fatal(err)
	}
	deleteRecorder := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/modal-accounts/"+id, nil)
	deleteRequest.SetPathValue("id", id)
	app.deleteModalAccount(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete inactive account status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	service, err := app.videoProviderForSnapshot(snapshotID, VideoProviderModal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListVideoModels(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModalAccountsAndGenerationEventsNeverExposeSecrets(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	id := "modal_account_safe_response"
	encrypted, err := security.EncryptSetting(modalAccountAAD(id), "modal-account-visible-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Safe account", Endpoint: "https://modal.example/v1", EncryptedAPIKey: encrypted}); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	recorder := httptest.NewRecorder()
	app.modalAccounts(recorder, httptest.NewRequest(http.MethodGet, "/api/modal-accounts", nil))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "visible-secret") || strings.Contains(recorder.Body.String(), encrypted) {
		t.Fatalf("unsafe account response: %s", recorder.Body.String())
	}
	var accountResponse struct {
		Accounts []ModalVideoAccount `json:"accounts"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&accountResponse); err != nil || len(accountResponse.Accounts) != 1 || !accountResponse.Accounts[0].APIKeyConfigured {
		t.Fatalf("account response=%#v err=%v", accountResponse, err)
	}

	if err := store.InsertGeneration(GenerationRecord{ID: "job_events", Prompt: "test", Model: "model", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGenerationProgress("job_events", "failed", "", "", "upstream error Authorization: Bearer modal-account-visible-secret", 73); err != nil {
		t.Fatal(err)
	}
	record, err := store.Generation("job_events")
	if err != nil {
		t.Fatal(err)
	}
	if record.Progress != 73 || !strings.Contains(record.Error, "Video provider") || strings.Contains(record.Error, "visible-secret") || len(record.Events) < 2 {
		t.Fatalf("unsafe generation record=%#v", record)
	}
	serialized, err := json.Marshal(record)
	if err != nil || strings.Contains(string(serialized), "visible-secret") {
		t.Fatalf("unsafe generation history=%s err=%v", serialized, err)
	}
}

func TestModalSubmissionsUseSelectedAccountSnapshots(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer selected-account-key" {
			t.Fatal("selected account was not used")
		}
		switch r.URL.Path {
		case "/videos/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"modal/model","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]}]}`))
		case "/videos":
			_, _ = w.Write([]byte(`{"id":"selected_job","status":"queued","model":"modal/model"}`))
		default:
			t.Fatalf("unexpected provider path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	id := "modal_account_selected"
	encrypted, err := security.EncryptSetting(modalAccountAAD(id), "selected-account-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Selected", Endpoint: server.URL, EncryptedAPIKey: encrypted}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(videoProviderSetting, string(VideoProviderModal)); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(`{"prompt":"a scene","model":"modal/model"}`))
	missingRequest.Header.Set("Content-Type", "application/json")
	app.generate(missing, missingRequest)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing account status=%d body=%s", missing.Code, missing.Body.String())
	}

	generated := httptest.NewRecorder()
	generatedRequest := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(`{"prompt":"a scene","model":"modal/model","modal_account_id":"modal_account_selected"}`))
	generatedRequest.Header.Set("Content-Type", "application/json")
	app.generate(generated, generatedRequest)
	if generated.Code != http.StatusAccepted {
		t.Fatalf("generation status=%d body=%s", generated.Code, generated.Body.String())
	}
	record, err := store.Generation("selected_job")
	if err != nil || record.ProviderConfigID == "" || record.VideoProvider != string(VideoProviderModal) {
		t.Fatalf("generation record=%#v err=%v", record, err)
	}

	projectRecorder := httptest.NewRecorder()
	if err := store.SetSetting("video_model.modal."+id, "modal/model"); err != nil {
		t.Fatal(err)
	}
	projectRequest := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"topic":"a vertical story","modal_account_id":"modal_account_selected"}`))
	projectRequest.Header.Set("Content-Type", "application/json")
	app.createProject(projectRecorder, projectRequest)
	if projectRecorder.Code != http.StatusAccepted {
		t.Fatalf("project status=%d body=%s", projectRecorder.Code, projectRecorder.Body.String())
	}
	var project VideoProject
	if err := json.NewDecoder(projectRecorder.Body).Decode(&project); err != nil || project.Model != "modal/model" {
		t.Fatalf("project did not use the account model preference: %#v err=%v", project, err)
	}
}

func TestGenerationStatePersistsWhenTerminalEventWriteFails(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_generation_event BEFORE INSERT ON generation_events BEGIN SELECT RAISE(FAIL, 'forced event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "atomic_insert", Prompt: "test", Model: "model", Status: "queued"}); err != nil {
		t.Fatalf("event failure must not discard a submitted job: %v", err)
	}
	inserted, err := store.Generation("atomic_insert")
	if err != nil || inserted.Status != "queued" {
		t.Fatalf("submitted job was not saved: %#v err=%v", inserted, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_generation_event`); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "atomic_update", Prompt: "test", Model: "model", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_generation_event BEFORE INSERT ON generation_events BEGIN SELECT RAISE(FAIL, 'forced event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGenerationProgress("atomic_update", "failed", "", "", "provider error", 50); err != nil {
		t.Fatalf("event failure must not discard a status transition: %v", err)
	}
	updated, err := store.Generation("atomic_update")
	if err != nil || updated.Status != "failed" || updated.Progress != 50 {
		t.Fatalf("generation state was not committed: %#v err=%v", updated, err)
	}
	if err := store.MarkVideoReady("atomic_update", store.VideoPath("atomic_update"), 42); err != nil {
		t.Fatalf("event failure must not discard a completed video: %v", err)
	}
	updated, err = store.Generation("atomic_update")
	if err != nil || updated.Status != "completed" || !updated.VideoReady {
		t.Fatalf("completion state was not committed: %#v err=%v", updated, err)
	}
}

func TestDeleteActiveModalAccountIsRejected(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	id := "modal_account_active"
	ciphertext, err := security.EncryptSetting(modalAccountAAD(id), "active-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Active", Endpoint: "https://modal.example/v1", EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting("modal_video_default_account_id", id); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/modal-accounts/"+id, nil)
	request.SetPathValue("id", id)
	app.deleteModalAccount(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("delete active account status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := store.ModalVideoAccount(id); err != nil {
		t.Fatalf("active account was deleted: %v", err)
	}
}

func TestSwitchingActiveModalAccountAllowsPriorAccountDeletion(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	firstID, secondID := "modal_account_first", "modal_account_second"
	for _, account := range []struct {
		id, name, key string
	}{{firstID, "First", "first-account-secret"}, {secondID, "Second", "second-account-secret"}} {
		ciphertext, err := security.EncryptSetting(modalAccountAAD(account.id), account.key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: account.id, Name: account.name, Endpoint: "https://modal.example/v1", EncryptedAPIKey: ciphertext}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSetting("modal_video_default_account_id", firstID); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	switchRecorder := httptest.NewRecorder()
	switchRequest := httptest.NewRequest(http.MethodPut, "/api/modal-accounts/active", strings.NewReader(`{"account_id":"modal_account_second"}`))
	switchRequest.Header.Set("Content-Type", "application/json")
	app.setActiveModalAccount(switchRecorder, switchRequest)
	if switchRecorder.Code != http.StatusOK || strings.Contains(switchRecorder.Body.String(), "account-secret") || !strings.Contains(switchRecorder.Body.String(), `"active_id":"modal_account_second"`) {
		t.Fatalf("unsafe/invalid switch response status=%d body=%s", switchRecorder.Code, switchRecorder.Body.String())
	}
	deletePrior := httptest.NewRecorder()
	deletePriorRequest := httptest.NewRequest(http.MethodDelete, "/api/modal-accounts/"+firstID, nil)
	deletePriorRequest.SetPathValue("id", firstID)
	app.deleteModalAccount(deletePrior, deletePriorRequest)
	if deletePrior.Code != http.StatusNoContent {
		t.Fatalf("delete prior active status=%d body=%s", deletePrior.Code, deletePrior.Body.String())
	}
	deleteCurrent := httptest.NewRecorder()
	deleteCurrentRequest := httptest.NewRequest(http.MethodDelete, "/api/modal-accounts/"+secondID, nil)
	deleteCurrentRequest.SetPathValue("id", secondID)
	app.deleteModalAccount(deleteCurrent, deleteCurrentRequest)
	if deleteCurrent.Code != http.StatusConflict {
		t.Fatalf("delete current active status=%d body=%s", deleteCurrent.Code, deleteCurrent.Body.String())
	}
}

func TestImmediateFailedSubmissionReturnsPersistedSafeEvent(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"modal/failed","supported_durations":[6]}]}`))
		case "/videos":
			_, _ = w.Write([]byte(`{"id":"failed_immediately","status":"failed","progress":42,"error":"Authorization: Bearer exposed-provider-secret"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	id := "modal_account_failure"
	ciphertext, err := security.EncryptSetting(modalAccountAAD(id), "failure-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Failure", Endpoint: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(videoProviderSetting, string(VideoProviderModal)); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(`{"prompt":"fail safely","model":"modal/failed","duration":6,"modal_account_id":"modal_account_failure"}`))
	request.Header.Set("Content-Type", "application/json")
	app.generate(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response GenerationRecord
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "failed" || response.Progress != 42 || len(response.Events) != 1 || strings.Contains(response.Error, "exposed-provider-secret") || !strings.Contains(response.Error, "Video provider") {
		t.Fatalf("unsafe/incomplete failed response: %#v", response)
	}
	persisted, err := store.Generation(response.ID)
	if err != nil || persisted.Error != response.Error || len(persisted.Events) != 1 {
		t.Fatalf("failed submission was not durably persisted: %#v err=%v", persisted, err)
	}
}

func TestModalModelPreferenceIsAccountAware(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"modal/account-model"}]}`))
	}))
	defer server.Close()
	id := "modal_account_model"
	ciphertext, err := security.EncryptSetting(modalAccountAAD(id), "model-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: id, Name: "Model", Endpoint: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPut, "/api/settings/video-model", strings.NewReader(`{"provider":"modal","model":"modal/account-model"}`))
	missingRequest.Header.Set("Content-Type", "application/json")
	app.updateVideoModel(missing, missingRequest)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing account status=%d body=%s", missing.Code, missing.Body.String())
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/settings/video-model", strings.NewReader(`{"provider":"modal","model":"modal/account-model","modal_account_id":"modal_account_model"}`))
	request.Header.Set("Content-Type", "application/json")
	app.updateVideoModel(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if saved, err := store.Setting("video_model.modal." + id); err != nil || saved != "modal/account-model" {
		t.Fatalf("account-specific model setting=%q err=%v", saved, err)
	}
}
