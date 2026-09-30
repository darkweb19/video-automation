(function () {
  "use strict";

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));

  const elements = {
    loginScreen: $("#login-screen"),
    loginForm: $("#login-form"),
    loginUsername: $("#login-username"),
    loginPassword: $("#login-password"),
    loginError: $("#login-error"),
    loginView: $("#login-view"),
    recoveryView: $("#recovery-view"),
    showRecovery: $("#show-recovery"),
    hideRecovery: $("#hide-recovery"),
    recoveryForm: $("#recovery-form"),
    recoveryUsername: $("#recovery-username"),
    recoveryCode: $("#recovery-code"),
    recoveryPassword: $("#recovery-password"),
    recoveryConfirm: $("#recovery-confirm"),
    recoveryError: $("#recovery-error"),
    appShell: $("#app-shell"),
    accountName: $("#account-name"),
    logout: $("#logout"),
    sidebar: $(".sidebar"),
    menuButton: $("#menu-button"),
    pageKicker: $("#page-kicker"),
    pageTitle: $("#page-title"),
    model: $("#model"),
    modelPicker: $("#model-picker"),
    modelTrigger: $("#model-trigger"),
    modelTriggerContent: $("#model-trigger-content"),
    modelMenu: $("#model-menu"),
    duration: $("#duration"),
    resolution: $("#resolution"),
    resolutionField: $("#resolution-field"),
    aspectRatio: $("#aspect-ratio"),
    audioOption: $("#audio-option"),
    generateAudio: $("#generate-audio"),
    activeVideoProvider: $("#active-video-provider"),
    modalAccountProjectField: $("#modal-account-project-field"),
    modalAccountProject: $("#modal-account-project"),
    modalAccountSingleField: $("#modal-account-single-field"),
    modalAccountSingle: $("#modal-account-single"),
    prompt: $("#prompt"),
    promptCategory: $("#prompt-category"),
    promptCategoryName: $("#prompt-category-name"),
    randomPrompt: $("#random-prompt"),
    projectTopic: $("#project-topic"),
    projectTopicError: $("#project-topic-error"),
    projectInputs: $("#project-inputs"),
    singleOptions: $("#single-options"),
    projectOptions: $("#project-options"),
    projectModelNote: $("#project-model-note"),
    modeOptions: $$(".mode-option"),
    promptCount: $("#prompt-count"),
    promptError: $("#prompt-error"),
    costEstimate: $("#cost-estimate"),
    generatorForm: $("#generator-form"),
    generate: $("#generate"),
    statusBadge: $("#status-badge"),
    statusEmpty: $("#status-empty"),
    statusActive: $("#status-active"),
    progressWrap: $("#progress-wrap"),
    statusDetail: $("#status-detail"),
    statusError: $("#error"),
    singleTerminalSection: $("#single-terminal-section"),
    singleTerminal: $("#single-terminal"),
    retryStatus: $("#retry-status"),
    generatedVideo: $("#generated-video"),
    openVideo: $("#open-video"),
    projectStatus: $("#project-status"),
    projectProgressWrap: $("#project-progress-wrap"),
    projectProgressBar: $("#project-progress-bar"),
    projectStatusDetail: $("#project-status-detail"),
    projectError: $("#project-error"),
    retryProject: $("#retry-project"),
    projectPipeline: $("#project-pipeline"),
    projectPipelineList: $("#project-pipeline-list"),
    projectTerminal: $("#project-terminal"),
    projectStory: $("#project-story"),
    projectScenes: $("#project-scenes"),
    projectTrace: $("#project-trace"),
    projectTraceSections: $("#project-trace-sections"),
    projectFinal: $("#project-final"),
    projectVideo: $("#project-video"),
    projectDownload: $("#project-download"),
    projectHistory: $("#project-history"),
    historyGrid: $("#history-grid"),
    vaultLock: $("#vault-lock"),
    vaultLocked: $("#vault-locked"),
    vaultGateTitle: $("#vault-gate-title"),
    vaultGateCopy: $("#vault-gate-copy"),
    vaultUnlockForm: $("#vault-unlock-form"),
    vaultCode: $("#vault-code"),
    vaultCodeError: $("#vault-code-error"),
    vaultUnlockButton: $("#vault-unlock-button"),
    vaultGoSettings: $("#vault-go-settings"),
    vaultContent: $("#vault-content"),
    vaultCount: $("#vault-count"),
    vaultEmpty: $("#vault-empty"),
    vaultGrid: $("#vault-grid"),
    vaultPlayerDialog: $("#vault-player-dialog"),
    vaultPlayerTitle: $("#vault-player-title"),
    vaultPlayer: $("#vault-player"),
    vaultPlayerDownload: $("#vault-player-download"),
    vaultPlayerClose: $("#vault-player-close"),
    recentList: $("#recent-list"),
    refreshHistory: $("#refresh-history"),
    statTotal: $("#stat-total"),
    statCompleted: $("#stat-completed"),
    statActive: $("#stat-active"),
    statCost: $("#stat-cost"),
    keyState: $("#key-state"),
    maskedKey: $("#masked-key"),
    apiKeyForm: $("#api-key-form"),
    apiKey: $("#api-key"),
    videoProvider: $("#video-provider"),
    videoProviderState: $("#video-provider-state"),
    testVideoProvider: $("#test-video-provider"),
    modalSettings: $("#modal-settings"),
    modalKeyState: $("#modal-key-state"),
    modalConfigForm: $("#modal-config-form"),
    modalAccountList: $("#modal-account-list"),
    modalAccountFormTitle: $("#modal-account-form-title"),
    modalAccountID: $("#modal-account-id"),
    modalAccountName: $("#modal-account-name"),
    modalAccountCancel: $("#modal-account-cancel"),
    modalBaseURL: $("#modal-base-url"),
    modalAPIKey: $("#modal-api-key"),
    modalKeyHelp: $("#modal-key-help"),
    passwordForm: $("#password-form"),
    currentPassword: $("#current-password"),
    newPassword: $("#new-password"),
    vaultCodeState: $("#vault-code-state"),
    vaultCodeForm: $("#vault-code-form"),
    vaultCurrentCodeField: $("#vault-current-code-field"),
    vaultCurrentCode: $("#vault-current-code"),
    vaultNewCode: $("#vault-new-code"),
    vaultConfirmCode: $("#vault-confirm-code"),
    vaultCodeSave: $("#vault-code-save"),
    vaultCodeHelp: $("#vault-code-help"),
    toast: $("#toast")
  };

  const viewMeta = {
    overview: { title: "Overview", kicker: "Workspace" },
    generate: { title: "Generate", kicker: "Create" },
    history: { title: "History", kicker: "Library" },
    vault: { title: "Vault", kicker: "Private library" },
    settings: { title: "Settings", kicker: "AI providers" }
  };

  const promptCategories = ["Kid Animation", "Horror Story", "Nature", "Seduction", "Mature Content", "Soft Corn"];

  const state = {
    models: [],
    modalAccounts: [],
    modalAccountsActiveID: "",
    modalAccountsRequestID: 0,
    modelRequestID: 0,
    videoProvider: "openrouter",
    generations: [],
    stats: null,
    historyHasMore: false,
    historyNextOffset: 0,
    historyNextCursor: "",
    historyCursorParam: "before",
    historyNextBeforeCreated: 0,
    historyNextBeforeID: "",
    historyLoading: false,
    historyTerminalOpen: new Map(),
    currentGenerationID: "",
    displayedGenerationID: "",
    currentGenerationRecord: null,
    displayedGenerationStatus: "",
    mode: "project",
    currentView: "",
    submissionPending: false,
    projectSubmissionPending: false,
    projectSubmissionFailed: false,
    singleSubmissionPending: false,
    singleSubmissionFailed: false,
    projects: [],
    currentProjectID: "",
    displayedProjectID: "",
    displayedProjectStatus: "",
    currentProjectRecord: null,
    projectTraceFetches: new Map(),
    projectTraceRequestEpoch: 0,
    projectRawTraceOpenKey: "",
    jobEventSource: null,
    jobStreamHasOpened: false,
    jobStreamDisconnected: false,
    jobStreamAuthCheckPending: false,
    terminalRefreshTimer: 0,
    terminalRefreshKinds: new Set(),
    toastTimer: 0,
    authenticated: false,
    mustChangePassword: false,
    vaultConfigured: false,
    vaultToken: "",
    vaultGeneration: 0,
    vaultLocking: false,
    vaultLockQueue: Promise.resolve(),
    vaultPendingLocks: 0,
    vaultRestoringLock: false,
    vaultRestoreLockReady: false,
    vaultRestoreLockFailed: false,
    vaultRestoreGeneration: 0,
    vaultAuthGeneration: 0,
    vaultUnlockPending: 0,
    vaultMediaControllers: new Set(),
    vaultItems: [],
    vaultObjectURL: "",
    vaultPlayerItemKey: ""
  };

  const VIDEO_PROVIDER_NAMES = Object.freeze({ modal: "Modal", openrouter: "OpenRouter" });

  function videoProviderName(provider = state.videoProvider) {
    return VIDEO_PROVIDER_NAMES[provider] || provider;
  }

  function updateModalAccountSelectors() {
    const project = state.mode === "project";
    const required = state.videoProvider === "modal";
    elements.modalAccountProjectField.hidden = !required || !project;
    elements.modalAccountSingleField.hidden = !required || project;
    elements.modalAccountProject.required = required && project;
    elements.modalAccountSingle.required = required && !project;
    elements.modalAccountProject.disabled = !required || !state.modalAccounts.length;
    elements.modalAccountSingle.disabled = !required || !state.modalAccounts.length;
  }

  function selectedModalAccountID() {
    return state.mode === "project" ? elements.modalAccountProject.value : elements.modalAccountSingle.value;
  }

  function populateModalAccountSelect(select) {
    const previous = select.value;
    select.replaceChildren(make("option", { text: "Choose a Modal account" }));
    select.options[0].value = "";
    state.modalAccounts.forEach((account) => {
      const option = make("option", { text: account.name || "Modal account" });
      option.value = String(account.id || "");
      select.append(option);
    });
    if (state.modalAccounts.some((account) => String(account.id) === previous)) select.value = previous;
    else if (state.modalAccounts.some((account) => String(account.id) === state.modalAccountsActiveID)) select.value = state.modalAccountsActiveID;
    else if (state.modalAccounts.length === 1) select.value = String(state.modalAccounts[0].id);
  }

  function refreshModalAccountSelectors() {
    populateModalAccountSelect(elements.modalAccountProject);
    populateModalAccountSelect(elements.modalAccountSingle);
    updateModalAccountSelectors();
    if (state.videoProvider === "modal") return loadModels(false);
    return Promise.resolve();
  }

  function resetModalAccountForm() {
    elements.modalConfigForm.reset();
    elements.modalAccountID.value = "";
    elements.modalAccountFormTitle.textContent = "Add Modal account";
    elements.modalAccountCancel.hidden = true;
    elements.modalAPIKey.required = true;
    elements.modalKeyHelp.textContent = "Required when adding an account. Leave blank when editing to keep its saved key.";
  }

  function editModalAccount(account) {
    elements.modalAccountID.value = String(account.id || "");
    elements.modalAccountName.value = account.name || "";
    elements.modalBaseURL.value = account.endpoint || "";
    elements.modalAPIKey.value = "";
    elements.modalAPIKey.required = false;
    elements.modalAccountFormTitle.textContent = "Edit Modal account";
    elements.modalAccountCancel.hidden = false;
    elements.modalKeyHelp.textContent = account.configured
      ? "The saved key is never returned. Leave this blank to keep it."
      : "Enter the key for this account.";
    elements.modalAccountName.focus();
  }

  function renderModalAccountList() {
    elements.modalAccountList.replaceChildren();
    if (!state.modalAccounts.length) {
      elements.modalAccountList.append(make("div", { className: "empty modal-account-empty", text: "No Modal accounts yet. Add an endpoint and encrypted API key below." }));
      return;
    }
    state.modalAccounts.forEach((account) => {
      const row = make("article", { className: "modal-account-row" });
      const isDefault = String(account.id) === state.modalAccountsActiveID;
      const details = make("div", { className: "modal-account-copy" });
      details.append(
        make("strong", { text: account.name || "Modal account" }),
        make("span", { text: account.endpoint || "Endpoint unavailable" }),
        make("small", { text: account.configured ? "API key saved securely" : "API key not configured" })
      );
      if (isDefault) details.append(make("span", { className: "badge completed modal-default-badge", text: "Default account" }));
      const actions = make("div", { className: "modal-account-actions" });
      const activate = make("button", {
        className: "button secondary",
        type: "button",
        text: isDefault ? "Default" : "Make default"
      });
      activate.disabled = isDefault;
      activate.setAttribute("aria-label", isDefault ? `${account.name} is the default Modal account` : `Make ${account.name} the default Modal account`);
      activate.addEventListener("click", () => activateModalAccount(account, activate));
      const edit = make("button", { className: "button secondary", type: "button", text: "Edit" });
      edit.addEventListener("click", () => editModalAccount(account));
      const remove = make("button", { className: "button secondary danger-button", type: "button", text: "Delete" });
      remove.disabled = isDefault;
      if (isDefault) remove.title = "Make another account the default before deleting this account.";
      remove.addEventListener("click", () => deleteModalAccount(account));
      actions.append(activate, edit, remove);
      row.append(details, actions);
      elements.modalAccountList.append(row);
    });
  }

  async function loadModalAccounts(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    const requestID = ++state.modalAccountsRequestID;
    try {
      const payload = await request("/api/modal-accounts");
      if (requestID !== state.modalAccountsRequestID || !state.authenticated) return;
      state.modalAccounts = Array.isArray(payload && payload.accounts) ? payload.accounts : [];
      state.modalAccountsActiveID = String(payload && payload.active_id || "");
      renderModalAccountList();
      await refreshModalAccountSelectors();
      const configured = state.modalAccounts.filter((account) => account.configured).length;
      elements.modalKeyState.className = `badge ${configured ? "completed" : "neutral"}`;
      elements.modalKeyState.textContent = `${configured} configured`;
    } catch (error) {
      if (requestID !== state.modalAccountsRequestID) return;
      elements.modalKeyState.className = "badge failed";
      elements.modalKeyState.textContent = "Unavailable";
      if (notify && error.status !== 401) toast(error.message, true);
    }
  }

  async function deleteModalAccount(account) {
    if (!window.confirm(`Delete Modal account “${account.name}”? Existing jobs keep their saved credentials.`)) return;
    try {
      await request(`/api/modal-accounts/${encodeURIComponent(account.id)}`, { method: "DELETE" });
      if (elements.modalAccountID.value === String(account.id)) resetModalAccountForm();
      await loadModalAccounts(false);
      toast("Modal account deleted.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    }
  }

  async function activateModalAccount(account, button) {
    setButtonBusy(button, true, "Switching…");
    try {
      await request("/api/modal-accounts/active", {
        method: "PUT",
        body: JSON.stringify({ account_id: account.id })
      });
      await loadModalAccounts(false);
      toast(`${account.name} is now the default Modal account.`);
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  class APIError extends Error {
    constructor(message, status) {
      super(message);
      this.name = "APIError";
      this.status = status;
    }
  }

  function make(tag, options = {}) {
    const node = document.createElement(tag);
    if (options.className) node.className = options.className;
    if (options.text !== undefined) node.textContent = String(options.text);
    if (options.type) node.type = options.type;
    return node;
  }

  function setButtonBusy(button, busy, busyText) {
    if (!button) return;
    if (busy) {
      if (!button.dataset.originalText) button.dataset.originalText = button.textContent;
      if (busyText) button.textContent = busyText;
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
    } else {
      button.textContent = button.dataset.originalText || button.textContent;
      button.disabled = false;
      button.removeAttribute("aria-busy");
      delete button.dataset.originalText;
    }
  }

  function toast(message, isError = false) {
    window.clearTimeout(state.toastTimer);
    elements.toast.textContent = message;
    elements.toast.classList.toggle("error", isError);
    elements.toast.setAttribute("role", isError ? "alert" : "status");
    elements.toast.setAttribute("aria-live", isError ? "assertive" : "polite");
    elements.toast.hidden = false;
    state.toastTimer = window.setTimeout(() => {
      elements.toast.hidden = true;
    }, 4200);
  }

  async function request(path, options = {}, allowUnauthorized = false) {
    const response = await fetch(path, {
      credentials: "same-origin",
      ...options,
      headers: {
        Accept: "application/json",
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {})
      }
    });

    let payload = null;
    if (response.status !== 204) {
      const contentType = response.headers.get("content-type") || "";
      if (contentType.includes("application/json")) {
        payload = await response.json().catch(() => null);
      }
    }

    if (!response.ok) {
      if (response.status === 401 && !allowUnauthorized) showLoggedOut();
      if (response.status === 428 && payload && payload.must_change_password && state.authenticated) {
        stopJobEventStream();
        applyPasswordGate(true);
        navigate("settings");
      }
      const message = payload && typeof payload.error === "string"
        ? payload.error
        : `Request failed (${response.status})`;
      throw new APIError(message, response.status);
    }
    return payload;
  }

  function projectValue(project, ...keys) {
    for (const key of keys) {
      if (project && project[key] !== undefined && project[key] !== null) return project[key];
    }
    return "";
  }

  function projectID(project) {
    return String(projectValue(project, "id", "project_id", "projectID"));
  }

  function projectTextGeneration(project) {
    const trace = projectValue(project, "text_generation", "textGeneration", "generation_details");
    return trace && typeof trace === "object" ? trace : null;
  }

  function traceMetadataValue(trace, ...keys) {
    if (!trace || typeof trace !== "object") return "";
    for (const key of keys) {
      if (Object.prototype.hasOwnProperty.call(trace, key) && trace[key] !== undefined && trace[key] !== null) return trace[key];
    }
    return "";
  }

  function traceRawResponseOmitted(trace) {
    return Boolean(trace && (trace.raw_response_omitted === true || trace.rawResponseOmitted === true));
  }

  function projectTraceRevision(project) {
    const trace = projectTextGeneration(project);
    if (!trace) return "";
    // The complete trace metadata makes the key safe across multiple saves in
    // one timestamp bucket as well as retries with a different request.
    return JSON.stringify([
      traceMetadataValue(trace, "status", "state"),
      traceMetadataValue(trace, "started_at", "startedAt"),
      traceMetadataValue(trace, "completed_at", "completedAt"),
      traceMetadataValue(trace, "updated_at", "updatedAt"),
      traceMetadataValue(trace, "router_model", "routerModel"),
      traceMetadataValue(trace, "actual_model", "actualModel"),
      traceMetadataValue(trace, "system_prompt", "systemPrompt"),
      traceMetadataValue(trace, "user_prompt", "userPrompt"),
      traceMetadataValue(trace, "response_schema", "responseSchema"),
      traceMetadataValue(trace, "error", "error_message")
    ]);
  }

  function projectTraceDisclosureKey(id, revision) {
    return id && revision ? `${id}\u0000${revision}` : "";
  }

  // Live project snapshots deliberately omit the potentially large raw model
  // response. Keep a previously fetched response only when the trace record
  // itself has not changed.
  function mergeProjectRawTrace(previous, incoming) {
    const incomingTrace = projectTextGeneration(incoming);
    if (!traceRawResponseOmitted(incomingTrace)) return incoming;
    const previousTrace = projectTextGeneration(previous);
    if (!previousTrace || traceRawResponseOmitted(previousTrace)
      || projectTraceRevision(previous) !== projectTraceRevision(incoming)) return incoming;
    return {
      ...incoming,
      text_generation: {
        ...incomingTrace,
        raw_response: traceMetadataValue(previousTrace, "raw_response", "rawResponse", "response_raw", "response", "raw"),
        raw_response_omitted: false
      }
    };
  }

  function mergeFetchedProjectRawTrace(latest, detailed) {
    const latestTrace = projectTextGeneration(latest);
    const detailedTrace = projectTextGeneration(detailed);
    if (!latestTrace || !detailedTrace || traceRawResponseOmitted(detailedTrace)
      || projectTraceRevision(latest) !== projectTraceRevision(detailed)) return latest;
    return {
      ...latest,
      text_generation: {
        ...latestTrace,
        raw_response: traceMetadataValue(detailedTrace, "raw_response", "rawResponse", "response_raw", "response", "raw"),
        raw_response_omitted: false
      }
    };
  }

  function invalidateProjectRawTraceRequests() {
    state.projectTraceRequestEpoch += 1;
    state.projectRawTraceOpenKey = "";
    state.projectTraceFetches.forEach((controller) => controller.abort());
    state.projectTraceFetches.clear();
  }

  function currentProjectMatchesTrace(id, revision, epoch) {
    return state.authenticated
      && !state.mustChangePassword
      && state.currentView === "generate"
      && state.mode === "project"
      && state.projectTraceRequestEpoch === epoch
      && state.currentProjectID === id
      && state.displayedProjectID === id
      && state.currentProjectRecord
      && projectID(state.currentProjectRecord) === id
      && projectTraceRevision(state.currentProjectRecord) === revision;
  }

  function fetchProjectRawTrace(id, revision) {
    if (!id || !revision || !state.authenticated || state.mustChangePassword) return;
    const key = projectTraceDisclosureKey(id, revision);
    if (!key || state.projectTraceFetches.has(key)) return;
    const epoch = state.projectTraceRequestEpoch;
    if (!currentProjectMatchesTrace(id, revision, epoch)) return;
    const controller = new AbortController();
    state.projectTraceFetches.set(key, controller);
    void request(`/api/projects/${encodeURIComponent(id)}`, { signal: controller.signal }).then((payload) => {
      if (!currentProjectMatchesTrace(id, revision, epoch)) return;
      const detailed = payload && payload.project ? payload.project : payload;
      if (!detailed || projectID(detailed) !== id || projectTraceRevision(detailed) !== revision) return;
      const latest = state.currentProjectRecord;
      const merged = mergeFetchedProjectRawTrace(latest, detailed);
      if (merged === latest) return;
      // Apply only the raw trace to the latest snapshot so a delayed detail
      // request cannot overwrite scene state, status, cost, or diagnostics.
      state.currentProjectRecord = merged;
      upsertProjectSnapshot(merged);
      if (state.mode === "project") renderProject(merged, true);
    }).catch((error) => {
      if (!currentProjectMatchesTrace(id, revision, epoch) || controller.signal.aborted) return;
      if (error.status !== 401 && error.status !== 423) toast("Unable to load the raw model response.", true);
    }).finally(() => {
      if (state.projectTraceFetches.get(key) === controller) state.projectTraceFetches.delete(key);
    });
  }

  const terminalJobStatuses = new Set(["completed", "complete", "failed", "error"]);

  function isTerminalJobStatus(status) {
    return terminalJobStatuses.has(String(status || "").toLowerCase());
  }

  function jobStatusRank(status) {
    const normalized = String(status || "").toLowerCase();
    if (["completed", "complete", "failed", "error"].includes(normalized)) return 3;
    if (["downloading", "combining", "assembling", "finalizing"].includes(normalized)) return 2;
    if (["queued", "pending", "created"].includes(normalized)) return 0;
    if (normalized === "download_failed") return 1;
    return 1;
  }

  function snapshotTimestamp(record) {
    const value = projectValue(record, "updated_at", "updatedAt", "last_updated_at", "lastUpdatedAt");
    if (value === "") return null;
    const numeric = Number(value);
    if (Number.isFinite(numeric)) return numeric < 100000000000 ? numeric * 1000 : numeric;
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function snapshotProgress(record) {
    const value = projectValue(record, "progress", "progress_percent", "percent");
    const numeric = Number(value);
    return value !== "" && value !== null && value !== undefined && Number.isFinite(numeric) ? numeric : null;
  }

  function retryCount(record) {
    return Number(projectValue(record, "retry_count", "retries", "attempts", "attempt")) || 0;
  }

  function isStaleJobSnapshot(previous, incoming, kind) {
    if (!previous || !incoming) return false;
    try {
      if (JSON.stringify(previous) === JSON.stringify(incoming)) return true;
    } catch (_error) {
      // Snapshot comparison below still rejects older timestamps and progress.
    }

    const previousTime = snapshotTimestamp(previous);
    const incomingTime = snapshotTimestamp(incoming);
    if (previousTime !== null && incomingTime !== null && incomingTime < previousTime) return true;
    if (previousTime !== null && incomingTime !== null && incomingTime > previousTime) return false;

    if (kind === "project") {
      const oldScenes = projectScenes(previous);
      const newScenes = projectScenes(incoming);
      const oldByNumber = new Map(oldScenes.map((scene, index) => [
        Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1,
        scene
      ]));
      for (const [index, scene] of newScenes.entries()) {
        const number = Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1;
        const oldScene = oldByNumber.get(number);
        if (oldScene && retryCount(scene) > retryCount(oldScene)) return false;
      }
    }
    const retriesAdvanced = retryCount(incoming) > retryCount(previous);
    if (retriesAdvanced) return false;

    const previousStatus = String(projectValue(previous, "status") || "").toLowerCase();
    const incomingStatus = String(projectValue(incoming, "status") || "").toLowerCase();
    if (isTerminalJobStatus(previousStatus) && !isTerminalJobStatus(incomingStatus)) return true;
    const sameOrderBucket = previousTime === null || incomingTime === null || incomingTime === previousTime;
    if (sameOrderBucket) {
      if (isTerminalJobStatus(previousStatus) && isTerminalJobStatus(incomingStatus) && previousStatus !== incomingStatus) return true;
      if (jobStatusRank(incomingStatus) < jobStatusRank(previousStatus)) return true;
    }

    const previousProgress = snapshotProgress(previous);
    const incomingProgress = snapshotProgress(incoming);
    if (sameOrderBucket
      && previousProgress !== null && incomingProgress !== null && incomingProgress < previousProgress) return true;

    if (kind === "project" && sameOrderBucket) {
      const oldScenes = projectScenes(previous);
      const newScenes = projectScenes(incoming);
      const oldByNumber = new Map(oldScenes.map((scene, index) => [
        Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1,
        scene
      ]));
      for (const [index, scene] of newScenes.entries()) {
        const number = Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1;
        const oldScene = oldByNumber.get(number);
        const oldProgress = snapshotProgress(oldScene || {});
        const newProgress = snapshotProgress(scene);
        if (oldProgress !== null && newProgress !== null && newProgress < oldProgress) return true;
        const oldSceneStatus = String(projectValue(oldScene, "status") || "").toLowerCase();
        const newSceneStatus = String(projectValue(scene, "status") || "").toLowerCase();
        if (oldScene && isTerminalJobStatus(oldSceneStatus) && isTerminalJobStatus(newSceneStatus)
          && oldSceneStatus !== newSceneStatus) return true;
        if (oldScene && jobStatusRank(newSceneStatus) < jobStatusRank(oldSceneStatus)) return true;
      }
    }
    return false;
  }

  function upsertGenerationSnapshot(record) {
    const id = String(record && record.id || "");
    if (!id) return;
    const index = state.generations.findIndex((item) => String(item.id) === id);
    if (index >= 0) {
      state.generations[index] = record;
    } else if (id === state.currentGenerationID || id === state.displayedGenerationID) {
      state.generations.unshift(record);
    }
  }

  function upsertProjectSnapshot(project) {
    const id = projectID(project);
    if (!id) return;
    const index = state.projects.findIndex((item) => projectID(item) === id);
    if (index >= 0) state.projects[index] = project;
    else if (id === state.currentProjectID || id === state.displayedProjectID) state.projects.unshift(project);
  }

  function preserveNewerSnapshots(incoming, previousRecords, currentRecord, kind, getID) {
    const known = new Map(previousRecords.map((record) => [getID(record), record]));
    const currentID = currentRecord && getID(currentRecord);
    if (currentID) {
      const knownCurrent = known.get(currentID);
      if (!knownCurrent || !isStaleJobSnapshot(currentRecord, knownCurrent, kind)) known.set(currentID, currentRecord);
    }
    return incoming.map((record) => {
      const previous = known.get(getID(record));
      const candidate = kind === "project" ? mergeProjectRawTrace(previous, record) : record;
      return previous && isStaleJobSnapshot(previous, candidate, kind) ? previous : candidate;
    });
  }

  function refreshJobState() {
    if (!state.authenticated || state.mustChangePassword) return Promise.resolve();
    return Promise.allSettled([loadHistory(false, false), loadProjects(false)]);
  }

  function queueTerminalRefresh(kind) {
    state.terminalRefreshKinds.add(kind);
    if (state.terminalRefreshTimer) return;
    state.terminalRefreshTimer = window.setTimeout(() => {
      state.terminalRefreshTimer = 0;
      const kinds = new Set(state.terminalRefreshKinds);
      state.terminalRefreshKinds.clear();
      const refreshes = [];
      if (kinds.has("generation")) refreshes.push(loadHistory(false, false));
      if (kinds.has("project")) refreshes.push(loadProjects(false));
      void Promise.allSettled(refreshes);
    }, 250);
  }

  function handleJobSnapshot(kind, event) {
    if (!state.authenticated || state.mustChangePassword) return;
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch (_error) {
      return;
    }
    let snapshot = kind === "generation" ? payload && payload.generation : payload && payload.project;
    const id = String(payload && payload.id || (kind === "project" ? projectID(snapshot) : snapshot && snapshot.id) || "");
    if (!snapshot || !id) return;

    const selectedID = kind === "generation"
      ? (state.displayedGenerationID || state.currentGenerationID)
      : (state.displayedProjectID || state.currentProjectID);
    const selected = selectedID === id;
    const previous = kind === "generation"
      ? state.generations.find((item) => String(item.id) === id) || (selected ? state.currentGenerationRecord : null)
      : state.projects.find((item) => projectID(item) === id) || (selected ? state.currentProjectRecord : null);
    if (kind === "project") {
      snapshot = mergeProjectRawTrace(previous, snapshot);
    }
    if (isStaleJobSnapshot(previous, snapshot, kind)) return;
    if (kind === "project" && selected && previous && projectTraceRevision(previous) !== projectTraceRevision(snapshot)) {
      invalidateProjectRawTraceRequests();
    }

    const wasTerminal = previous && isTerminalJobStatus(projectValue(previous, "status"));
    const status = String(projectValue(snapshot, "status") || "").toLowerCase();
    const becameTerminal = isTerminalJobStatus(status) && !wasTerminal;
    if (kind === "generation") {
      upsertGenerationSnapshot(snapshot);
      if (selected) {
        state.currentGenerationRecord = snapshot;
        if (state.mode === "single") setStatusRecord(snapshot);
      }
      if (status === "completed" || status === "failed" || status === "error") {
        if (state.currentGenerationID === id) state.currentGenerationID = "";
      }
    } else {
      upsertProjectSnapshot(snapshot);
      if (selected) {
        state.currentProjectRecord = snapshot;
        if (state.mode === "project") renderProject(snapshot);
      }
    }

    if (becameTerminal) {
      queueTerminalRefresh(kind);
      if (selected) {
        const failed = status === "failed" || status === "error";
        toast(kind === "generation"
          ? (failed ? "Generation failed." : "Video saved successfully.")
          : (failed ? "Project failed." : "30-second video saved."), failed);
      }
    }
  }

  function stopJobEventStream(reset = false) {
    if (state.jobEventSource) {
      state.jobEventSource.close();
      state.jobEventSource = null;
      state.jobStreamDisconnected = true;
    }
    state.jobStreamAuthCheckPending = false;
    if (reset) {
      state.jobStreamHasOpened = false;
      state.jobStreamDisconnected = false;
      window.clearTimeout(state.terminalRefreshTimer);
      state.terminalRefreshTimer = 0;
      state.terminalRefreshKinds.clear();
    }
  }

  function startJobEventStream() {
    if (!state.authenticated || state.mustChangePassword || state.jobEventSource || typeof window.EventSource !== "function") return;
    const source = new window.EventSource("/api/events", { withCredentials: true });
    state.jobEventSource = source;
    source.addEventListener("open", () => {
      if (state.jobEventSource !== source) return;
      state.jobStreamHasOpened = true;
      state.jobStreamDisconnected = false;
      state.jobStreamAuthCheckPending = false;
      // The normal API loads happen before subscription. Refresh after every
      // successful open so a transition in that small gap is authoritative.
      // The ready event stays informational and does not duplicate this read.
      void refreshJobState();
    });
    source.addEventListener("generation", (event) => handleJobSnapshot("generation", event));
    source.addEventListener("project", (event) => handleJobSnapshot("project", event));
    source.addEventListener("ready", () => {});
    source.addEventListener("ping", () => {});
    source.addEventListener("error", () => {
      if (state.jobEventSource !== source) return;
      state.jobStreamDisconnected = true;
      if (state.jobStreamAuthCheckPending) return;
      state.jobStreamAuthCheckPending = true;
      void request("/api/session").then((session) => {
        if (state.jobEventSource !== source) return;
        if (session && session.must_change_password && !state.mustChangePassword) {
          stopJobEventStream();
          applyPasswordGate(true);
          navigate("settings");
        }
      }).catch(() => {
        // Network errors leave EventSource's native reconnect active.
      });
    });
  }

  function displayedGenerationAvailable() {
    return Boolean(state.displayedGenerationID);
  }

  function displayedProjectAvailable() {
    return Boolean(state.displayedProjectID);
  }

  function resetGenerationStatusBadge() {
    elements.statusBadge.className = "badge neutral";
    elements.statusBadge.textContent = "Idle";
  }

  function syncGenerationModeDisplay() {
    const project = state.mode === "project";
    const hasSingleResult = displayedGenerationAvailable() || state.singleSubmissionPending || state.singleSubmissionFailed;
    const hasProjectResult = displayedProjectAvailable() || state.projectSubmissionPending || state.projectSubmissionFailed;
    const hasSelectedResult = project ? hasProjectResult : hasSingleResult;

    elements.statusActive.hidden = project || !hasSingleResult;
    elements.projectStatus.hidden = !project || !hasProjectResult;
    elements.statusEmpty.hidden = hasSelectedResult;

    if (!hasSelectedResult) {
      resetGenerationStatusBadge();
      return;
    }

    const status = project
      ? (state.projectSubmissionPending ? "submitting" : state.projectSubmissionFailed ? "failed" : state.displayedProjectStatus)
      : (state.singleSubmissionPending ? "submitting" : state.singleSubmissionFailed ? "failed" : state.displayedGenerationStatus);
    if (!status) return;
    elements.statusBadge.className = "badge " + statusClass(status);
    elements.statusBadge.textContent = status === "submitting" ? "Submitting" : status.replaceAll("_", " ");
  }

  function setGenerationMode(mode) {
    const previousMode = state.mode;
    state.mode = mode === "single" ? "single" : "project";
    if (previousMode === "project" && state.mode !== "project") invalidateProjectRawTraceRequests();
    const project = state.mode === "project";
    elements.modeOptions.forEach((button) => {
      const active = button.dataset.mode === state.mode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
    });
    elements.projectInputs.hidden = !project;
    elements.projectOptions.hidden = !project;
    elements.projectModelNote.hidden = !project;
    elements.singleOptions.hidden = project;
    elements.prompt.hidden = project;
    elements.prompt.closest(".field").hidden = project;
    if (!state.submissionPending) elements.generate.textContent = project ? "Generate 30-second project" : "Generate video";
    elements.modalAccountProjectField.hidden = state.videoProvider !== "modal" || !project;
    elements.modalAccountSingleField.hidden = state.videoProvider !== "modal" || project;
    if (project) {
      const selected = state.models.find((model) => model.id === elements.model.value);
      if (state.models.length && !supportsProject(selected)) {
        const fallback = state.models.find(supportsProject);
        if (fallback) selectModel(fallback.id);
      }
    }
    updateModelOptions();
    if (previousMode !== state.mode) {
      if (project) {
        if (state.currentProjectRecord && projectID(state.currentProjectRecord) === state.currentProjectID) {
          renderProject(state.currentProjectRecord);
        }
      } else if (state.currentGenerationRecord
        && String(state.currentGenerationRecord.id || "") === state.displayedGenerationID) {
        setStatusRecord(state.currentGenerationRecord);
      }
    }
    syncGenerationModeDisplay();
  }

  function moveGenerationMode(event) {
    const tabs = elements.modeOptions;
    const current = tabs.indexOf(event.currentTarget);
    if (current < 0) return;
    let next = current;
    if (event.key === "ArrowLeft") next = (current - 1 + tabs.length) % tabs.length;
    else if (event.key === "ArrowRight") next = (current + 1) % tabs.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = tabs.length - 1;
    else return;

    event.preventDefault();
    const target = tabs[next];
    if (!target) return;
    target.focus({ preventScroll: true });
    target.click();
  }

  function selectedPromptCategory() {
    return promptCategories[Number(elements.promptCategory.value)] || promptCategories[0];
  }

  function updatePromptCategory() {
    const category = selectedPromptCategory();
    elements.promptCategoryName.textContent = category;
    elements.promptCategory.setAttribute("aria-valuetext", category);
  }

  async function generateRandomPrompt() {
    const mode = state.mode;
    const category = selectedPromptCategory();
    setButtonBusy(elements.randomPrompt, true, "Generating…");
    try {
      const payload = await request("/api/prompts/random", {
        method: "POST",
        body: JSON.stringify({ category, mode })
      });
      const prompt = typeof payload?.prompt === "string" ? payload.prompt.trim() : "";
      if (!prompt) throw new APIError("No prompt was returned. Try again.", 502);
      const field = mode === "project" ? elements.projectTopic : elements.prompt;
      field.value = prompt;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.focus();
      if (mode === "project") elements.projectTopicError.textContent = "";
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.randomPrompt, false);
    }
  }

  function supportsProject(model) {
    return Array.isArray(model && model.durations) && model.durations.includes(6)
      && Array.isArray(model.resolutions) && model.resolutions.includes("480p")
      && Array.isArray(model.aspect_ratios) && model.aspect_ratios.includes("9:16");
  }

  function applyPasswordGate(required) {
    state.mustChangePassword = Boolean(required);
    $$(".nav-item").forEach((button) => {
      button.disabled = state.mustChangePassword && button.dataset.view !== "settings";
    });
    $$('[data-go]').forEach((button) => {
      button.disabled = state.mustChangePassword;
    });

    const apiKeyPanel = elements.apiKeyForm.closest(".panel");
    if (apiKeyPanel) apiKeyPanel.hidden = state.mustChangePassword;
    const vaultCodePanel = elements.vaultCodeForm.closest(".panel");
    if (vaultCodePanel) vaultCodePanel.hidden = state.mustChangePassword;
    let notice = $("#forced-password-notice");
    if (!notice) {
      notice = make("div", {
        className: "alert error",
        text: "Password change required. Update the temporary password before using the dashboard."
      });
      notice.id = "forced-password-notice";
      elements.passwordForm.parentElement.insertBefore(notice, elements.passwordForm);
    }
    notice.hidden = !state.mustChangePassword;
    if (state.mustChangePassword) {
      elements.keyState.className = "badge neutral";
      elements.keyState.textContent = "Locked";
      elements.maskedKey.textContent = "Change password first";
    }
  }

  function resetVaultRestoreState() {
    state.vaultRestoreGeneration += 1;
    state.vaultAuthGeneration += 1;
    state.vaultRestoringLock = false;
    state.vaultRestoreLockReady = false;
    state.vaultRestoreLockFailed = false;
  }

  function isCompactNavigation() {
    return window.matchMedia("(max-width: 760px)").matches;
  }

  function syncNavigationAccessibility() {
    const compact = isCompactNavigation();
    const focusWasInSidebar = elements.sidebar.contains(document.activeElement);
    if (!compact) elements.sidebar.classList.remove("open");
    const open = compact && elements.sidebar.classList.contains("open");
    const hidden = compact && !open;
    elements.sidebar.toggleAttribute("inert", hidden);
    elements.sidebar.setAttribute("aria-hidden", String(hidden));
    elements.menuButton.setAttribute("aria-expanded", String(open));
    elements.menuButton.setAttribute("aria-label", open ? "Close navigation" : "Open navigation");
    if (hidden && focusWasInSidebar) elements.menuButton.focus({ preventScroll: true });
  }

  function setNavigationOpen(open, options = {}) {
    const compact = isCompactNavigation();
    const visible = compact && Boolean(open);
    elements.sidebar.classList.toggle("open", visible);
    syncNavigationAccessibility();
    if (visible) {
      const current = $(".nav-item[aria-current='page']:not(:disabled)", elements.sidebar)
        || $(".nav-item:not(:disabled)", elements.sidebar);
      if (current) current.focus({ preventScroll: true });
    } else if (options.returnFocus && compact) {
      elements.menuButton.focus({ preventScroll: true });
    }
  }

  function focusPageTitle() {
    window.requestAnimationFrame(() => {
      if (!elements.appShell.hidden && !state.mustChangePassword) {
        elements.pageTitle.focus({ preventScroll: true });
      }
    });
  }

  function showLoggedOut() {
    stopJobEventStream(true);
    invalidateProjectRawTraceRequests();
    resetVaultRestoreState();
    clearVaultClientState();
    clearGenerationDisplay();
    clearProjectDisplay();
    state.authenticated = false;
    state.vaultConfigured = false;
    state.currentGenerationID = "";
    state.currentProjectID = "";
    state.currentGenerationRecord = null;
    state.currentProjectRecord = null;
    state.generations = [];
    state.projects = [];
    state.stats = null;
    state.historyHasMore = false;
    state.historyNextOffset = 0;
    state.historyNextCursor = "";
    state.historyCursorParam = "before";
    state.historyNextBeforeCreated = 0;
    state.historyNextBeforeID = "";
    applyPasswordGate(false);
    setNavigationOpen(false);
    elements.appShell.hidden = true;
    elements.loginScreen.hidden = false;
    document.title = "FrameVault | Sign in";
    elements.loginPassword.value = "";
    showLoginView(false);
    window.setTimeout(() => elements.loginUsername.focus(), 0);
  }

  function showLoginView(focus = true) {
    elements.recoveryView.hidden = true;
    elements.loginView.hidden = false;
    elements.recoveryError.textContent = "";
    if (focus) window.setTimeout(() => elements.loginUsername.focus(), 0);
  }

  function showRecoveryView() {
    elements.loginView.hidden = true;
    elements.recoveryView.hidden = false;
    elements.recoveryError.textContent = "";
    elements.recoveryUsername.value = elements.loginUsername.value.trim() || "sujanshrestha";
    window.setTimeout(() => elements.recoveryCode.focus(), 0);
  }

  function showAuthenticated(username, mustChangePassword) {
    resetVaultRestoreState();
    stopJobEventStream(true);
    invalidateProjectRawTraceRequests();
    state.authenticated = true;
    applyPasswordGate(mustChangePassword);
    elements.accountName.textContent = username || "Signed in";
    elements.loginScreen.hidden = true;
    elements.appShell.hidden = false;
    elements.loginError.textContent = "";
    navigate(state.mustChangePassword ? "settings" : "overview");
    if (state.mustChangePassword) {
      toast("Change the temporary password to unlock the dashboard.", true);
      window.setTimeout(() => elements.currentPassword.focus(), 0);
    }
  }

  function navigate(view) {
    if (!viewMeta[view]) return;
    if (state.mustChangePassword && view !== "settings") {
      view = "settings";
      toast("Change your password before using the dashboard.", true);
    }
    if (state.currentView === "generate" && view !== "generate") invalidateProjectRawTraceRequests();
    state.currentView = view;
    const previousView = $(".view.active")?.dataset.page || "";
    const navigationWasOpen = isCompactNavigation() && elements.sidebar.classList.contains("open");
    const metadata = viewMeta[view];
    $$(".view").forEach((node) => node.classList.toggle("active", node.dataset.page === view));
    $$(".nav-item").forEach((node) => {
      const active = node.dataset.view === view;
      node.classList.toggle("active", active);
      if (active) node.setAttribute("aria-current", "page");
      else node.removeAttribute("aria-current");
    });
    elements.pageTitle.textContent = metadata.title;
    elements.pageKicker.textContent = metadata.kicker;
    document.title = metadata.title + " | FrameVault";
    setNavigationOpen(false);
    if (previousView !== view) window.scrollTo({ top: 0, left: 0, behavior: "auto" });
    if (previousView !== view || navigationWasOpen) focusPageTitle();
    if (view === "settings" && !state.mustChangePassword) loadSettings(false);
    if (view === "vault" && !state.mustChangePassword) loadVault();
    if (view === "history" && !state.mustChangePassword) {
      loadHistory(false);
      loadProjects(false);
    }
  }

  function formatPriceNumber(value, minimum = 2) {
    const number = Number(value);
    if (!Number.isFinite(number)) return "";
    return number.toLocaleString("en-US", {
      minimumFractionDigits: minimum,
      maximumFractionDigits: 6
    });
  }

  function formatUSD(value) {
    const formatted = formatPriceNumber(value);
    return formatted ? `$${formatted}` : "—";
  }

  function validPrice(value) {
    const price = Number(value);
    return Number.isFinite(price) && price >= 0 ? price : null;
  }

  function modelPricing(model) {
    const skus = model && model.pricing_skus && typeof model.pricing_skus === "object"
      ? Object.entries(model.pricing_skus)
      : [];
    const perSecond = skus
      .filter(([name, value]) => name.startsWith("per-video-second") && validPrice(value) !== null)
      .map(([, value]) => Number(value));
    const suppliedPerSecond = validPrice(model && model.price_per_second);
    if (suppliedPerSecond !== null || perSecond.length) {
      return {
        price: suppliedPerSecond !== null ? suppliedPerSecond : Math.min(...perSecond),
        unit: "second",
        hasVariants: perSecond.length > 1
      };
    }

    const suppliedPerGeneration = validPrice(model && model.price_per_generation);
    const generationSKU = skus.find(([name, value]) => name === "generate" && validPrice(value) !== null);
    const generationPrice = suppliedPerGeneration !== null
      ? suppliedPerGeneration
      : (generationSKU ? Number(generationSKU[1]) : null);
    if (generationPrice !== null) {
      return { price: generationPrice, unit: "generation", hasVariants: false };
    }

    return { price: null, unit: "", hasVariants: false };
  }

  function modelLabel(model) {
    const name = model.name || model.id || "Unnamed model";
    const pricing = modelPricing(model);
    if (pricing.price === null) return `${name} — Price unavailable`;
    const prefix = pricing.hasVariants ? "From " : "";
    if (pricing.unit === "generation") {
      return `${name} — ${prefix}$${formatPriceNumber(pricing.price)}/generation`;
    }
    return `${name} — ${prefix}$${formatPriceNumber(pricing.price)}/sec`;
  }

  function providerDetails(model) {
    const provider = String(model.provider || model.id.split("/")[0] || "AI");
    const key = provider.toLowerCase().replace(/[^a-z0-9]/g, "");
    const known = {
      google: { name: "Google", initials: "G", className: "google" },
      openai: { name: "OpenAI", initials: "OA", className: "openai" },
      minimax: { name: "MiniMax", initials: "M", className: "minimax" },
      bytedance: { name: "ByteDance", initials: "BD", className: "bytedance" },
      kwaivgi: { name: "Kling AI", initials: "K", className: "kling" },
      klingai: { name: "Kling AI", initials: "K", className: "kling" },
      luma: { name: "Luma", initials: "L", className: "luma" },
      runway: { name: "Runway", initials: "R", className: "runway" }
    };
    if (known[key]) return known[key];
    const name = provider.replace(/[-_]+/g, " ").replace(/\b\w/g, (character) => character.toUpperCase());
    const initials = name.split(/\s+/).map((part) => part[0]).join("").slice(0, 2).toUpperCase() || "AI";
    return { name, initials, className: "generic" };
  }

  function modelPriceLabel(model) {
    const pricing = modelPricing(model);
    if (pricing.price === null) return "Price unavailable";
    const prefix = pricing.hasVariants ? "From " : "";
    const suffix = pricing.unit === "generation" ? "/generation" : "/sec";
    return `${prefix}$${formatPriceNumber(pricing.price)}${suffix}`;
  }

  function compactCapabilityValues(values, detailed = false) {
    const unique = [...new Set((Array.isArray(values) ? values : [])
      .map((value) => String(value).trim())
      .filter(Boolean))];
    if (!unique.length) return "";
    if (detailed) return unique.join(", ");
    return unique.length === 1 ? unique[0] : unique[0] + " +" + String(unique.length - 1);
  }

  function modelCapabilityLabel(model, detailed = false) {
    const labels = [];
    const durations = [...new Set((Array.isArray(model && model.durations) ? model.durations : [])
      .map(Number)
      .filter((value) => Number.isFinite(value) && value > 0))]
      .sort((a, b) => a - b);
    if (durations.length) {
      const first = durations[0];
      const last = durations[durations.length - 1];
      const contiguous = durations.every((value, index) => index === 0 || value === durations[index - 1] + 1);
      if (durations.length === 1) labels.push(String(first) + " sec");
      else if (detailed) labels.push(durations.map((value) => String(value) + " sec").join(", "));
      else if (contiguous) labels.push(String(first) + "-" + String(last) + " sec");
      else labels.push(String(first) + " sec +" + String(durations.length - 1));
    }
    const resolution = compactCapabilityValues(model && model.resolutions, detailed);
    if (resolution) labels.push(resolution);
    const aspectRatio = compactCapabilityValues(model && model.aspect_ratios, detailed);
    if (aspectRatio) labels.push(aspectRatio);
    if (model && model.audio === true) labels.push("Audio");
    if (model && model.audio === false) labels.push("Silent");
    return labels.join(", ");
  }

  function providerMark(model) {
    const provider = providerDetails(model);
    return make("span", { className: `provider-mark ${provider.className}`, text: provider.initials });
  }

  function modelOptionContent(model, compact = false) {
    const provider = providerDetails(model);
    const fragment = document.createDocumentFragment();
    fragment.append(providerMark(model));
    const copy = make("span", { className: "model-option-copy" });
    copy.append(make("strong", { text: model.name || model.id || "Unnamed model" }));
    copy.append(make("span", {
      text: compact ? `${provider.name} · ${modelPriceLabel(model)}` : provider.name
    }));
    const capabilities = modelCapabilityLabel(model);
    if (capabilities) {
      const capability = make("span", { className: "model-capability", text: capabilities });
      capability.title = modelCapabilityLabel(model, true);
      copy.append(capability);
    }
    fragment.append(copy);
    if (!compact) fragment.append(make("span", { className: "model-option-price", text: modelPriceLabel(model) }));
    return fragment;
  }

  function closeModelMenu() {
    elements.modelMenu.hidden = true;
    elements.modelTrigger.setAttribute("aria-expanded", "false");
    elements.modelPicker.classList.remove("open");
  }

  function openModelMenu() {
    if (elements.modelTrigger.disabled) return;
    elements.modelMenu.hidden = false;
    elements.modelTrigger.setAttribute("aria-expanded", "true");
    elements.modelPicker.classList.add("open");
    const selected = $(".model-option[aria-selected='true']", elements.modelMenu)
      || $(".model-option", elements.modelMenu);
    if (selected) selected.focus();
  }

  function renderModelPicker(message = "Select a model") {
    const selected = selectedModel();
    elements.modelTriggerContent.replaceChildren();
    if (selected) {
      elements.modelTriggerContent.append(modelOptionContent(selected, true));
    } else {
      elements.modelTriggerContent.append(make("span", { className: "model-placeholder", text: message }));
    }
    $$(".model-option", elements.modelMenu).forEach((option) => {
      option.setAttribute("aria-selected", String(option.dataset.value === elements.model.value));
    });
  }

  function selectModel(value) {
    elements.model.value = value;
    renderModelPicker();
    closeModelMenu();
    elements.model.dispatchEvent(new Event("change", { bubbles: true }));
    elements.modelTrigger.focus();
  }

  function renderModelMenu() {
    elements.modelMenu.replaceChildren();
    state.models.forEach((model) => {
      const option = make("button", { className: "model-option", type: "button" });
      option.dataset.value = model.id;
      option.tabIndex = -1;
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", String(model.id === elements.model.value));
      option.append(modelOptionContent(model));
      option.addEventListener("click", () => selectModel(model.id));
      elements.modelMenu.append(option);
    });
    renderModelPicker();
  }

  function selectedModel() {
    return state.models.find((model) => model.id === elements.model.value) || null;
  }

  function updateGenerateAvailability() {
    const model = selectedModel();
    elements.generate.disabled = state.submissionPending || !model
      || (state.mode === "project" && !supportsProject(model));
    if (!state.submissionPending) {
      elements.generate.textContent = state.mode === "project" ? "Generate 30-second project" : "Generate video";
    }
  }

  function fillSelect(select, values, formatter, fallbackLabel) {
    const previous = select.value;
    select.replaceChildren();
    if (!Array.isArray(values) || values.length === 0) {
      const option = make("option", { text: fallbackLabel });
      option.value = "";
      select.append(option);
      select.disabled = true;
      return;
    }
    values.forEach((value) => {
      const option = make("option", { text: formatter(value) });
      option.value = String(value);
      select.append(option);
    });
    select.disabled = false;
    if (values.some((value) => String(value) === previous)) select.value = previous;
  }

  function updateModelOptions() {
    const model = selectedModel();
    renderModelPicker();
    if (!model) {
      fillSelect(elements.duration, [], String, "Provider default");
      fillSelect(elements.resolution, [], String, "Provider default");
      fillSelect(elements.aspectRatio, [], String, "Provider default");
      elements.resolutionField.hidden = true;
      elements.audioOption.hidden = true;
      updateGenerateAvailability();
      updateEstimate();
      return;
    }

    fillSelect(elements.duration, model.durations, (value) => `${value} seconds`, "Provider default");
    fillSelect(elements.resolution, model.resolutions, String, "Provider default");
    fillSelect(elements.aspectRatio, model.aspect_ratios, String, "Provider default");
    elements.resolutionField.hidden = !Array.isArray(model.resolutions) || model.resolutions.length === 0;
    elements.audioOption.hidden = model.audio !== true;
    if (model.audio !== true) elements.generateAudio.checked = false;
    const projectCompatible = supportsProject(model);
    updateGenerateAvailability();
    updateModalAccountSelectors();
    elements.projectModelNote.textContent = projectCompatible
      ? "This model supports the five-scene 6-second, 480p, 9:16 project preset."
      : "This model does not support the fixed 6-second, 480p, 9:16 project preset. Choose another model or switch to Single clip.";
    updateEstimate();
  }

  function updateEstimate() {
    const model = selectedModel();
    const pricing = modelPricing(model);
    if (!model || pricing.price === null) {
      elements.costEstimate.textContent = model ? "Not supplied by selected provider" : "Unavailable";
      return;
    }
    if (pricing.unit === "generation") {
      elements.costEstimate.textContent = state.mode === "project"
        ? `${formatUSD(pricing.price * 5)} total for 5 generations`
        : `${formatUSD(pricing.price)}/generation`;
      return;
    }
    const duration = state.mode === "project" ? 30 : Number(elements.duration.value);
    if (!Number.isFinite(duration) || duration <= 0) {
      elements.costEstimate.textContent = "Unavailable";
      return;
    }
    const prefix = pricing.hasVariants ? "From " : "";
    elements.costEstimate.textContent = `${prefix}${formatUSD(pricing.price * duration)}`;
  }

  async function loadModels(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    const requestID = ++state.modelRequestID;
    const requestedProvider = state.videoProvider;
    const requestedMode = state.mode;
    const modalAccountID = requestedProvider === "modal" ? selectedModalAccountID() : "";
    const isCurrentRequest = () => requestID === state.modelRequestID
      && requestedProvider === state.videoProvider
      && requestedMode === state.mode
      && (requestedProvider !== "modal" || modalAccountID === selectedModalAccountID());
    elements.model.disabled = true;
    elements.modelTrigger.disabled = true;
    closeModelMenu();
    elements.generate.disabled = true;
    elements.model.replaceChildren();
    const loadingOption = make("option", { text: "Loading models…" });
    loadingOption.value = "";
    elements.model.append(loadingOption);
    renderModelPicker("Loading models…");
    try {
      if (requestedProvider === "modal" && !modalAccountID) {
        if (!isCurrentRequest()) return;
        state.models = [];
        elements.model.replaceChildren(make("option", { text: "Choose a Modal account first" }));
        elements.model.options[0].value = "";
        elements.model.disabled = true;
        elements.modelTrigger.disabled = true;
        elements.modelMenu.replaceChildren();
        renderModelPicker("Choose a Modal account first");
        updateModelOptions();
        return;
      }
      const modelPath = requestedProvider === "modal"
        ? `/api/video-models?provider=modal&modal_account_id=${encodeURIComponent(modalAccountID)}`
        : `/models?provider=${encodeURIComponent(requestedProvider)}`;
      const payload = await request(modelPath);
      if (!isCurrentRequest()) return;
      state.models = Array.isArray(payload && payload.models) ? payload.models : [];
      elements.model.replaceChildren();
      if (!state.models.length) {
        const option = make("option", { text: "No video models available" });
        option.value = "";
        elements.model.append(option);
        elements.modelMenu.replaceChildren();
        updateModelOptions();
        renderModelPicker("No video models available");
        return;
      }
      const placeholder = make("option", { text: "Select a model" });
      placeholder.value = "";
      elements.model.append(placeholder);
      state.models.forEach((model) => {
        const option = make("option", { text: modelLabel(model) });
        option.value = model.id;
        elements.model.append(option);
      });
      const requestedModel = payload && payload.selected_model;
      const savedModel = state.models.find((model) => model.id === requestedModel);
      const selected = (savedModel && (state.mode !== "project" || supportsProject(savedModel)))
        || (state.mode === "project" ? state.models.find(supportsProject) : null)
        || savedModel
        || state.models[0];
      if (selected) elements.model.value = selected.id;
      elements.activeVideoProvider.textContent = `Video provider: ${videoProviderName(payload && payload.provider || requestedProvider)}`;
      elements.model.disabled = false;
      elements.modelTrigger.disabled = false;
      renderModelMenu();
      updateModelOptions();
      if (selected && selected.id !== requestedModel) void persistVideoModel(selected.id, false, modalAccountID);
      renderHistory();
      renderRecent();
    } catch (error) {
      if (!isCurrentRequest()) return;
      state.models = [];
      elements.model.replaceChildren();
      const option = make("option", { text: error.status === 422 ? "Configure provider credentials in Settings" : "Models unavailable" });
      option.value = "";
      elements.model.append(option);
      elements.modelMenu.replaceChildren();
      updateModelOptions();
      renderModelPicker(error.status === 422 ? "Configure provider credentials in Settings" : "Models unavailable");
      if (notify && error.status !== 422 && error.status !== 401) toast(error.message, true);
    }
  }

  function statusBadge(status) {
    const normalized = status || "unknown";
    return make("span", { className: `badge ${statusClass(normalized)}`, text: normalized.replaceAll("_", " ") });
  }

  function renderJobTerminal(list, events) {
    list.replaceChildren();
    const rows = Array.isArray(events) ? events : [];
    if (!rows.length) {
      list.append(make("li", { className: "terminal-empty", text: "No job events recorded yet." }));
      return;
    }
    rows.forEach((event) => {
      if (!event || typeof event !== "object") return;
      const item = make("li", { className: "terminal-line" });
      const time = event.created_at || event.timestamp || event.time;
      const numericTime = typeof time === "number" || (typeof time === "string" && /^\d+(?:\.\d+)?$/.test(time.trim()))
        ? Number(time)
        : null;
      const dateValue = numericTime === null ? time : (Math.abs(numericTime) < 1e12 ? numericTime * 1000 : numericTime);
      const date = dateValue ? new Date(dateValue) : null;
      const timeText = date && Number.isFinite(date.getTime())
        ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
        : "--:--:--";
      const stage = String(event.stage || "job").replaceAll("_", " ");
      const status = String(event.status || "info").replaceAll("_", " ");
      const message = String(event.message || "").trim();
      item.append(
        make("time", { text: timeText }),
        make("span", { className: "terminal-stage", text: stage }),
        make("span", { className: `terminal-status terminal-${status.replace(/[^a-z0-9_-]/gi, "")}`, text: status }),
        make("span", { className: "terminal-message", text: message })
      );
      list.append(item);
    });
  }

  function historyTerminal(events, key, status) {
    const section = make("details", { className: "job-terminal-section history-terminal-section" });
    const rows = Array.isArray(events) ? events.filter((event) => event && typeof event === "object") : [];
    const normalizedStatus = String(status || "").toLowerCase();
    const openByDefault = [
      "queued", "processing", "downloading", "download_failed", "failed", "error",
      "planning", "generating", "combining", "submitting", "pending", "retry", "retrying", "started", "running"
    ].includes(normalizedStatus);
    section.open = state.historyTerminalOpen.has(key)
      ? state.historyTerminalOpen.get(key)
      : openByDefault;

    const heading = make("summary", { className: "history-terminal-head" });
    heading.append(
      make("h4", { text: "Process log" }),
      make("span", { className: "history-terminal-count", text: `${rows.length} ${rows.length === 1 ? "event" : "events"}` })
    );
    const terminal = make("ol", { className: "job-terminal", "aria-label": "Recorded process events" });
    renderJobTerminal(terminal, rows);
    const toggleLabel = make("span", {
      className: "history-terminal-action",
      text: section.open ? "Hide logs" : "View logs"
    });
    heading.append(toggleLabel);
    section.addEventListener("toggle", () => {
      toggleLabel.textContent = section.open ? "Hide logs" : "View logs";
    });
    heading.addEventListener("click", () => {
      window.setTimeout(() => state.historyTerminalOpen.set(key, section.open), 0);
    });
    section.append(heading);
    section.append(terminal);
    return section;
  }

  function statusClass(status) {
    const normalized = String(status || "").toLowerCase();
    const supported = new Set(["completed", "queued", "processing", "downloading", "download_failed", "failed", "neutral"]);
    if (["planning", "generating", "combining", "submitting", "pending", "retry", "retrying", "started", "running"].includes(normalized)) return "processing";
    return supported.has(normalized) ? normalized : "neutral";
  }

  function formatDate(timestamp) {
    if (!Number.isFinite(Number(timestamp)) || Number(timestamp) <= 0) return "Just now";
    return new Intl.DateTimeFormat("en-CA", {
      month: "short", day: "numeric", year: "numeric", hour: "numeric", minute: "2-digit"
    }).format(new Date(Number(timestamp) * 1000));
  }

  function formatBytes(bytes) {
    const size = Number(bytes);
    if (!Number.isFinite(size) || size <= 0) return "—";
    if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
    return `${(size / (1024 * 1024)).toFixed(1)} MB`;
  }

  function friendlyModel(id) {
    const model = state.models.find((item) => item.id === id);
    return model ? (model.name || model.id) : (id || "—");
  }

  function recordCost(record) {
    if (record.cost_usd !== undefined && record.cost_usd !== "") {
      return { label: "Actual cost", value: formatUSD(record.cost_usd) };
    }
    if (record.estimated_cost_usd !== undefined && record.estimated_cost_usd !== "") {
      return { label: "Estimated cost", value: formatUSD(record.estimated_cost_usd) };
    }
    return { label: "Cost", value: "—" };
  }

  function renderStats() {
    const activeStatuses = new Set(["queued", "processing", "downloading", "download_failed"]);
    const pageCompleted = state.generations.filter((record) => record.status === "completed").length;
    const pageActive = state.generations.filter((record) => activeStatuses.has(record.status)).length;
    const pageCost = state.generations.reduce((sum, record) => {
      const cost = Number(record.cost_usd);
      return sum + (Number.isFinite(cost) ? cost : 0);
    }, 0);
    const stats = state.stats;
    const total = stats ? stats.total : state.generations.length;
    const completed = stats ? stats.completed : pageCompleted;
    const active = stats ? stats.active : pageActive;
    const totalCost = stats ? stats.totalCost : pageCost;
    elements.statTotal.textContent = String(total);
    elements.statCompleted.textContent = String(completed);
    elements.statActive.textContent = String(active);
    elements.statCost.textContent = formatUSD(totalCost);
  }

  function numericStat(...values) {
    for (const value of values) {
      if (value !== null && value !== undefined && value !== "" && Number.isFinite(Number(value))) {
        return Number(value);
      }
    }
    return 0;
  }

  function normalizeStats(stats) {
    if (!stats || typeof stats !== "object") return null;
    const queued = numericStat(stats.queued, stats.Queued);
    const processing = numericStat(stats.processing, stats.Processing);
    const downloading = numericStat(stats.downloading, stats.Downloading);
    const explicitActive = [stats.active_generations, stats.active, stats.in_progress, stats.Active]
      .find((value) => value !== null && value !== undefined && value !== "" && Number.isFinite(Number(value)));
    return {
      total: numericStat(stats.total_generations, stats.total, stats.Total),
      completed: numericStat(stats.completed_generations, stats.completed, stats.Completed),
      active: explicitActive === undefined ? queued + processing + downloading : Number(explicitActive),
      totalCost: numericStat(stats.total_cost_usd, stats.total_cost, stats.cost_usd, stats.TotalCostUSD)
    };
  }

  function emptyState(message, actionLabel = "") {
    const empty = make("div", { className: "empty" });
    empty.append(make("p", { text: message }));
    if (actionLabel) {
      const action = make("button", { className: "button secondary empty-action", type: "button", text: actionLabel });
      action.addEventListener("click", () => navigate("generate"));
      empty.append(action);
    }
    return empty;
  }
  function renderRecent() {
    elements.recentList.replaceChildren();
    const recent = state.generations.slice(0, 5);
    if (!recent.length) {
      elements.recentList.append(emptyState("No generations yet."));
      return;
    }
    recent.forEach((record) => {
      const row = make("div", { className: "recent-item" });
      const main = make("div", { className: "recent-main" });
      main.append(
        make("strong", { text: record.prompt || "Untitled generation" }),
        make("span", { text: friendlyModel(record.model) })
      );
      const date = make("span", { className: "meta", text: formatDate(record.created_at) });
      const badge = statusBadge(record.status);
      const cost = make("strong", { text: recordCost(record).value });
      row.append(main, date, badge, cost);
      elements.recentList.append(row);
    });
  }

  function metadataItem(label, value) {
    const wrapper = make("div");
    wrapper.append(make("span", { text: label }), make("strong", { text: value }));
    return wrapper;
  }

  function projectRecordedCost(project) {
    const scenes = projectScenes(project);
    const costs = scenes.map((scene) => {
      const raw = projectValue(scene, "cost_usd", "costUSD");
      if (raw === "" || raw === null || raw === undefined) return null;
      const value = Number(raw);
      return Number.isFinite(value) && value >= 0 ? value : null;
    });
    const recorded = costs.filter((cost) => cost !== null);
    if (!recorded.length) return "Not recorded";
    const total = recorded.reduce((sum, cost) => sum + cost, 0);
    if (recorded.length !== costs.length) return `${formatUSD(total)} recorded (partial)`;
    return formatUSD(total);
  }

  function historyPreview(videoReady, videoURL, status, onUnavailable = () => {}, label = "Generated video") {
    const preview = make("div", { className: "history-preview" });
    const showUnavailable = () => {
      const normalized = String(status || "").toLowerCase();
      const finished = ["completed", "complete", "failed", "error"].includes(normalized);
      preview.replaceChildren(
        statusBadge(status),
        make("span", { text: finished ? "Video unavailable" : "Video not ready yet" })
      );
      preview.classList.add("history-preview-empty");
      onUnavailable();
    };
    if (!videoReady || !videoURL) {
      showUnavailable();
      return preview;
    }
    const video = make("video");
    video.controls = true;
    video.preload = "none";
    video.playsInline = true;
    video.setAttribute("aria-label", label);
    video.addEventListener("error", showUnavailable, { once: true });
    video.src = videoURL;
    preview.append(video);
    return preview;
  }

  function historyPrompt(body, label, value) {
    body.append(make("h3", { className: "history-title", text: value || "Untitled generation" }));
    if (!value) return;
    const details = make("details", { className: "history-prompt-details" });
    details.append(
      make("summary", { text: `View full ${label}` }),
      make("p", { text: value })
    );
    body.append(details);
  }

  function renderHistory() {
    elements.historyGrid.replaceChildren();
    if (!state.generations.length && !state.projects.length) {
      elements.historyGrid.append(emptyState("No generations yet.", "Create a video"));
      return;
    }

    if (state.generations.length || state.projects.length) {
      const heading = make("div", { className: "history-section-heading history-generation-heading" });
      heading.append(
        make("h3", { text: "Video generations" }),
        make("span", { className: "history-count", text: String(state.generations.length) })
      );
      elements.historyGrid.append(heading);
    }

    if (!state.generations.length && state.projects.length) {
      elements.historyGrid.append(make("div", {
        className: "empty history-generation-empty",
        text: "No individual video generations yet."
      }));
      return;
    }

    state.generations.forEach((record, index) => {
      const card = make("article", { className: "history-card" });
      const videoURL = `/video?id=${encodeURIComponent(record.id)}`;
      const videoReady = String(record.status || "").toLowerCase() === "completed" && Boolean(record.video_ready);
      let download;
      const previewLabel = record.prompt
        ? "Generated video: " + record.prompt.slice(0, 120)
        : "Generated video";
      const preview = historyPreview(videoReady, videoURL, record.status, () => download?.remove(), previewLabel);

      const body = make("div", { className: "history-body" });
      historyPrompt(body, "prompt", record.prompt);
      const metadata = make("div", { className: "history-meta" });
      const cost = recordCost(record);
      metadata.append(
        metadataItem("Model", friendlyModel(record.model)),
        metadataItem("Created", formatDate(record.created_at)),
        metadataItem("Duration", record.duration ? `${record.duration} sec` : "Provider default"),
        metadataItem(cost.label, cost.value),
        metadataItem("Aspect ratio", record.aspect_ratio || "Provider default"),
        metadataItem("File size", formatBytes(record.size_bytes))
      );
      body.append(metadata);
      if (record.error) body.append(make("div", { className: "alert error history-error", text: record.error }));
      body.append(historyTerminal(
        record.events || record.pipeline_events || record.logs,
        `generation:${record.id || record.created_at || index}`,
        record.status
      ));

      const actions = make("div", { className: "history-actions" });
      if (videoReady) {
        download = make("a", { className: "button secondary", text: "Download" });
        download.href = videoURL;
        download.download = `generation-${record.id}.mp4`;
        actions.append(download);
      }
      if (videoReady) {
        const move = make("button", { className: "button secondary vault-move-button", text: "Move to Vault", type: "button" });
        move.addEventListener("click", () => moveHistoryItem("generation", record.id, move));
        actions.append(move);
      }
      const remove = make("button", { className: "button secondary danger-button", text: "Delete", type: "button" });
      remove.addEventListener("click", () => deleteGeneration(record));
      actions.append(remove);
      body.append(actions);
      card.append(preview, body);
      elements.historyGrid.append(card);
    });

    if (state.historyHasMore) {
      const more = make("div", { className: "empty" });
      const button = make("button", { className: "button secondary", text: "Load more", type: "button" });
      button.addEventListener("click", () => loadHistory(true, true));
      more.append(button);
      elements.historyGrid.append(more);
    }
  }

  async function loadHistory(notify = true, append = false) {
    if (!state.authenticated || state.mustChangePassword || state.historyLoading) return;
    state.historyLoading = true;
    elements.refreshHistory.disabled = true;
    try {
      const offset = append ? state.historyNextOffset : 0;
      const query = new URLSearchParams({ limit: "24" });
      if (append && state.historyNextCursor) {
        query.set(state.historyCursorParam, state.historyNextCursor);
      } else if (append && state.historyNextBeforeCreated && state.historyNextBeforeID) {
        query.set("before_created", String(state.historyNextBeforeCreated));
        query.set("before_id", state.historyNextBeforeID);
      } else if (append) {
        query.set("offset", String(offset));
      }
      const payload = await request(`/api/generations?${query.toString()}`);
      const records = preserveNewerSnapshots(
        Array.isArray(payload && payload.generations) ? payload.generations : [],
        state.generations,
        state.currentGenerationRecord,
        "generation",
        (record) => String(record && record.id || "")
      );
      if (append) {
        const byID = new Map(state.generations.map((record) => [record.id, record]));
        records.forEach((record) => byID.set(record.id, record));
        state.generations = Array.from(byID.values());
      } else {
        state.generations = records;
      }
      if (!state.displayedGenerationID) {
        const active = state.generations.find((record) => ["queued", "processing", "downloading", "download_failed"].includes(String(record.status || "").toLowerCase()));
        if (active) {
          state.currentGenerationID = String(active.id || "");
          state.displayedGenerationID = state.currentGenerationID;
          state.currentGenerationRecord = active;
        }
      }
      if (state.mode === "single" && state.displayedGenerationID) {
        const displayed = state.generations.find((record) => String(record.id) === state.displayedGenerationID);
        if (displayed) setStatusRecord(displayed);
      }
      const aggregateStats = normalizeStats(payload && payload.stats);
      if (aggregateStats || !append) state.stats = aggregateStats;
      const nextPage = payload && payload.next_page;
      const nextCursor = payload && payload.next_cursor;
      state.historyHasMore = Boolean(payload && payload.has_more) || Boolean(nextPage) || Boolean(nextCursor);
      const suppliedOffset = Number(payload && payload.next_offset);
      state.historyNextOffset = Number.isInteger(suppliedOffset) && suppliedOffset >= 0
        ? suppliedOffset
        : offset + records.length;
      const cursor = nextPage || nextCursor;
      state.historyNextCursor = typeof cursor === "string" ? cursor : "";
      state.historyCursorParam = nextPage ? "before" : "cursor";
      const cursorObject = cursor && typeof cursor === "object" ? cursor : null;
      const nextObject = payload && payload.next && typeof payload.next === "object" ? payload.next : null;
      state.historyNextBeforeCreated = numericStat(
        payload && payload.next_before_created,
        cursorObject && cursorObject.before_created,
        nextObject && nextObject.before_created
      );
      state.historyNextBeforeID = String(
        (payload && payload.next_before_id) ||
        (cursorObject && cursorObject.before_id) ||
        (nextObject && nextObject.before_id) ||
        ""
      );
      renderStats();
      renderRecent();
      renderHistory();
    } catch (error) {
      if (notify && error.status !== 401) toast(error.message, true);
    } finally {
      state.historyLoading = false;
      elements.refreshHistory.disabled = false;
    }
  }

  async function deleteGeneration(record) {
    const preview = (record.prompt || "this generation").slice(0, 80);
    if (!window.confirm(`Delete “${preview}” and its stored video? This cannot be undone.`)) return;
    try {
      await request(`/api/generations/${encodeURIComponent(record.id)}`, { method: "DELETE" });
      state.generations = state.generations.filter((item) => item.id !== record.id);
      clearGenerationDisplay(record.id);
      await loadHistory(false, false);
      toast("Generation deleted.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    }
  }

  function clearGenerationDisplay(id = "") {
    const normalizedID = String(id || "");
    const matchesCurrent = !normalizedID || state.currentGenerationID === normalizedID;
    const matchesDisplay = !normalizedID || state.displayedGenerationID === normalizedID;
    if (!normalizedID) {
      state.singleSubmissionPending = false;
      state.singleSubmissionFailed = false;
    }
    if (matchesCurrent) {
      state.currentGenerationID = "";
      state.currentGenerationRecord = null;
    }
    if (!matchesDisplay) return;
    state.displayedGenerationID = "";
    state.currentGenerationRecord = null;
    state.displayedGenerationStatus = "";
    elements.generatedVideo.pause();
    elements.generatedVideo.removeAttribute("src");
    elements.generatedVideo.load();
    elements.generatedVideo.hidden = true;
    elements.openVideo.removeAttribute("href");
    elements.openVideo.removeAttribute("download");
    elements.openVideo.hidden = true;
    elements.singleTerminal.replaceChildren();
    elements.singleTerminalSection.hidden = true;
    elements.statusActive.hidden = true;
    elements.statusError.hidden = true;
    elements.statusError.textContent = "";
    elements.retryStatus.hidden = true;
    elements.progressWrap.hidden = true;
    elements.statusDetail.textContent = "";
    syncGenerationModeDisplay();
  }

  function clearProjectDisplay(id = "") {
    const normalizedID = String(id || "");
    const matchesCurrent = !normalizedID || state.currentProjectID === normalizedID;
    const matchesDisplay = !normalizedID || state.displayedProjectID === normalizedID;
    if (matchesCurrent || matchesDisplay) invalidateProjectRawTraceRequests();
    if (!normalizedID) {
      state.projectSubmissionPending = false;
      state.projectSubmissionFailed = false;
    }
    if (matchesCurrent) {
      state.currentProjectID = "";
      state.currentProjectRecord = null;
    }
    if (!matchesDisplay) return;
    state.displayedProjectID = "";
    state.currentProjectRecord = null;
    state.displayedProjectStatus = "";
    elements.projectVideo.pause();
    elements.projectVideo.removeAttribute("src");
    elements.projectVideo.load();
    elements.projectFinal.hidden = true;
    elements.projectDownload.removeAttribute("href");
    elements.projectDownload.removeAttribute("download");
    elements.projectStory.replaceChildren();
    elements.projectStory.hidden = true;
    elements.projectScenes.replaceChildren();
    elements.projectPipelineList.replaceChildren();
    elements.projectTerminal.replaceChildren();
    elements.projectTraceSections.replaceChildren();
    elements.projectPipeline.hidden = true;
    elements.projectTrace.hidden = true;
    elements.projectProgressWrap.hidden = true;
    elements.projectError.hidden = true;
    elements.projectError.textContent = "";
    elements.retryProject.hidden = true;
    elements.projectStatusDetail.textContent = "";
    elements.projectStatus.hidden = true;
    syncGenerationModeDisplay();
  }

  async function moveHistoryItem(kind, id, button) {
    if (!id) return;
    setButtonBusy(button, true, "Moving…");
    try {
      await request(`/api/vault/items/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/move`, { method: "POST" });
      if (kind === "generation") {
        state.generations = state.generations.filter((item) => item.id !== id);
        clearGenerationDisplay(id);
      } else {
        state.projects = state.projects.filter((item) => projectID(item) !== id);
        clearProjectDisplay(id);
      }
      await Promise.allSettled([loadHistory(false), loadProjects(false)]);
      toast("Moved to Vault.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function deleteProject(project) {
    const id = projectID(project);
    if (!id) return;
    const status = String(projectValue(project, "status") || "queued").toLowerCase();
    if (!["completed", "complete", "failed", "error"].includes(status)) return;
    const topic = String(projectValue(project, "topic") || "this project").slice(0, 80);
    if (!window.confirm(`Delete “${topic}” and its stored video? This cannot be undone.`)) return;
    try {
      await request(`/api/projects/${encodeURIComponent(id)}`, { method: "DELETE" });
      clearProjectDisplay(id);
      state.projects = state.projects.filter((item) => projectID(item) !== id);
      await loadProjects(false);
      toast("Project deleted.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    }
  }

  function setStatusRecord(record) {
    const id = String(record.id || "");
    const previous = state.generations.find((item) => String(item.id) === id)
      || (state.currentGenerationRecord && String(state.currentGenerationRecord.id || "") === id ? state.currentGenerationRecord : null);
    if (previous && isStaleJobSnapshot(previous, record, "generation")) record = previous;
    state.displayedGenerationID = id;
    state.currentGenerationRecord = record;
    if (id) upsertGenerationSnapshot(record);
    const status = String(record.status || "processing").toLowerCase();
    if (["queued", "processing", "downloading", "download_failed"].includes(status)) {
      state.currentGenerationID = id;
    } else if (state.currentGenerationID === id) {
      state.currentGenerationID = "";
    }
    state.displayedGenerationStatus = status;
    elements.statusEmpty.hidden = true;
    elements.statusActive.hidden = false;
    elements.statusBadge.className = `badge ${statusClass(status)}`;
    elements.statusBadge.textContent = status.replaceAll("_", " ");
    elements.statusError.hidden = !record.error;
    elements.statusError.textContent = record.error || "";
    elements.retryStatus.hidden = true;
    elements.generatedVideo.hidden = true;
    elements.openVideo.hidden = true;

    const pending = ["queued", "processing", "downloading", "download_failed"].includes(status);
    elements.progressWrap.hidden = !pending;
    const recordEvents = record.events || record.pipeline_events || record.logs;
    const latestEventProgress = Array.isArray(recordEvents)
      ? [...recordEvents].reverse().find((event) => event && event.progress !== undefined)?.progress
      : undefined;
    const suppliedProgress = Number(record.progress ?? record.progress_percent ?? record.percent ?? latestEventProgress);
    const hasProgress = Number.isFinite(suppliedProgress);
    const progress = hasProgress ? Math.max(0, Math.min(100, suppliedProgress)) : 0;
    $("#progress-bar").style.width = `${progress}%`;
    $("#progress-bar").classList.toggle("indeterminate", pending && !hasProgress);
    $("#progress-bar").setAttribute("role", "progressbar");
    $("#progress-bar").setAttribute("aria-valuemin", "0");
    $("#progress-bar").setAttribute("aria-valuemax", "100");
    if (hasProgress) $("#progress-bar").setAttribute("aria-valuenow", String(progress));
    else $("#progress-bar").removeAttribute("aria-valuenow");
    elements.singleTerminalSection.hidden = false;
    renderJobTerminal(elements.singleTerminal, recordEvents);
    const details = {
      queued: "Your request is queued with the selected video provider.",
      processing: "Your video provider is generating the video.",
      downloading: "Generation complete. Saving the video to Docker storage.",
      download_failed: "The video is ready upstream. Storage download will be retried.",
      completed: "Video complete and saved in Docker storage.",
      failed: "The generation could not be completed."
    };
    elements.statusDetail.textContent = details[status] || `Status: ${status}`;

    if (status === "completed" && record.video_ready) {
      const videoURL = `/video?id=${encodeURIComponent(record.id)}`;
      if (elements.generatedVideo.getAttribute("src") !== videoURL) elements.generatedVideo.src = videoURL;
      elements.generatedVideo.hidden = false;
      if (elements.openVideo.getAttribute("href") !== videoURL) elements.openVideo.href = videoURL;
      elements.openVideo.download = `generation-${record.id}.mp4`;
      elements.openVideo.hidden = false;
    }
    syncGenerationModeDisplay();
  }

  function projectScenes(project) {
    const scenes = projectValue(project, "scenes", "scene_list");
    return Array.isArray(scenes) ? scenes.slice().sort((a, b) => Number(projectValue(a, "scene_number", "number", "scene")) - Number(projectValue(b, "scene_number", "number", "scene"))) : [];
  }

  function projectProgress(project, scenes) {
    const raw = projectValue(project, "progress", "progress_percent", "percent");
    const supplied = Number(raw);
    if (raw !== "" && raw !== null && raw !== undefined && Number.isFinite(supplied)) return Math.max(0, Math.min(100, supplied));
    return null;
  }

  function firstTraceValue(source, ...keys) {
    if (!source || typeof source !== "object") return "";
    for (const key of keys) {
      const value = source[key];
      if (value !== undefined && value !== null && value !== "") return value;
    }
    return "";
  }

  function traceText(value) {
    if (value === undefined || value === null || value === "") return "";
    if (typeof value === "string") return value;
    try {
      return JSON.stringify(value, null, 2);
    } catch (error) {
      return String(value);
    }
  }

  function traceJSON(value) {
    if (value === undefined || value === null || value === "") return "";
    if (typeof value !== "string") return traceText(value);
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch (error) {
      return value;
    }
  }

  function formatTraceDate(value) {
    if (value === undefined || value === null || value === "") return "";
    if (typeof value === "string" && Number.isNaN(Number(value))) {
      const parsed = new Date(value);
      if (!Number.isNaN(parsed.getTime())) {
        return new Intl.DateTimeFormat("en-CA", {
          month: "short", day: "numeric", year: "numeric", hour: "numeric", minute: "2-digit"
        }).format(parsed);
      }
    }
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric <= 0) return String(value);
    return formatDate(numeric > 100000000000 ? numeric / 1000 : numeric);
  }

  function traceStatus(value, fallback = "queued") {
    const normalized = String(value || "").toLowerCase().replace(/[\s-]+/g, "_");
    if (["completed", "complete", "done", "success", "succeeded", "ready"].includes(normalized)) return "completed";
    if (["failed", "failure", "error", "errored", "cancelled", "canceled"].includes(normalized)) return "failed";
    if (["processing", "running", "started", "in_progress", "active"].includes(normalized)) return "processing";
    if (["retry", "retrying", "rescheduled"].includes(normalized)) return "processing";
    if (["downloading", "download_failed"].includes(normalized)) return normalized;
    if (["queued", "pending", "waiting", "created", "submitted"].includes(normalized)) return "queued";
    return fallback;
  }

  function traceStageLabel(value) {
    const stage = String(value || "pipeline").trim();
    const normalized = stage.toLowerCase().replace(/[\s-]+/g, "_");
    const known = {
      text_generation: "Story and script",
      story_generation: "Story and script",
      story_script: "Story and script",
      script_generation: "Story and script",
      scene_generation: "Scene generation",
      scenes: "Scene generation",
      video_generation: "Scene generation",
      assembly: "Final video",
      final_video: "Final video",
      final_video_assembly: "Final video",
      project: "Project",
      text_request: "Text request",
      validation: "Validation",
      continuity: "Continuity",
      scene_submission: "Scene submission",
      scene_poll: "Scene polling",
      scene_download: "Scene download",
      scene_retry: "Scene retry",
      combine: "Final video",
      project_complete: "Complete",
      complete: "Complete"
    };
    if (known[normalized]) return known[normalized];
    const sceneMatch = normalized.match(/^(?:scene|clip)[_: ]?(\d+)$/);
    if (sceneMatch) return `Scene ${sceneMatch[1]}`;
    return stage.replace(/[_-]+/g, " ").replace(/\b\w/g, (character) => character.toUpperCase()) || "Pipeline";
  }

  function projectPipelineEvents(project, scenes, status) {
    let supplied = projectValue(project, "pipeline_events", "pipelineEvents", "events", "pipeline");
    if (supplied && !Array.isArray(supplied) && typeof supplied === "object") {
      supplied = firstTraceValue(supplied, "events", "items", "pipeline_events");
    }
    const recordedEvents = Array.isArray(supplied) ? supplied.map((event, index) => {
        const source = event && typeof event === "object" ? event : { message: event };
        const eventStatus = traceStatus(firstTraceValue(source, "status", "state", "result"), "queued");
        const error = firstTraceValue(source, "error", "error_message", "failure");
        return {
          stage: `${traceStageLabel(firstTraceValue(source, "stage", "name", "step", "type") || `Stage ${index + 1}`)}${firstTraceValue(source, "scene_number", "sceneNumber", "scene") ? ` · Scene ${firstTraceValue(source, "scene_number", "sceneNumber", "scene")}` : ""}`,
          status: error ? "failed" : eventStatus,
          message: String(firstTraceValue(source, "message", "description", "detail") || (error ? error : "")),
          details: firstTraceValue(source, "details", "metadata", "data"),
          error: String(error || ""),
          timestamp: firstTraceValue(source, "timestamp", "created_at", "createdAt", "time", "at"),
          retries: firstTraceValue(source, "retries", "retry_count", "attempts"),
          attempt: firstTraceValue(source, "attempt", "attempt_number", "attemptNumber")
        };
      }) : [];

    const textGeneration = projectValue(project, "text_generation", "textGeneration", "generation_details");
    const textRecordStatus = textGeneration && typeof textGeneration === "object"
      ? traceStatus(firstTraceValue(textGeneration, "status", "state"), "queued")
      : "queued";
    const hasText = Boolean(projectValue(project, "story", "story_text", "script", "full_script", "fullScript")
      || textRecordStatus === "completed"
      || (textGeneration && typeof textGeneration === "object" && firstTraceValue(textGeneration, "raw_response", "rawResponse", "response")));
    const textStatus = hasText ? "completed" : (status === "failed" || status === "error" ? "failed" : "processing");
    const sceneStatuses = scenes.map((scene) => traceStatus(projectValue(scene, "status", "state"), "queued"));
    const allScenesDone = scenes.length > 0 && sceneStatuses.every((item) => item === "completed");
    const sceneFailed = sceneStatuses.some((item) => item === "failed" || item === "download_failed");
    const sceneActive = sceneStatuses.some((item) => item === "processing" || item === "downloading");
    const sceneStatus = sceneFailed ? "failed" : allScenesDone ? "completed" : sceneActive ? "processing" : "queued";
    const finalStatus = ["completed", "complete"].includes(status) ? "completed" : status === "failed" || status === "error" ? "failed" : allScenesDone ? "processing" : "queued";
    const derivedEvents = [
      { stage: "Story and script", status: textStatus, message: hasText ? "Story, script, and continuity data recorded." : "Waiting for text generation." },
      { stage: "Scene generation", status: sceneStatus, message: scenes.length ? `${scenes.length} scene${scenes.length === 1 ? "" : "s"} tracked.` : "Waiting for scene records." },
      { stage: "Final video", status: finalStatus, message: finalStatus === "completed" ? "Final video assembled and saved." : "Waiting for all scenes to finish." }
    ].map((event) => ({ ...event, derived: true }));
    const hasTextStage = recordedEvents.some((event) => ["Story and script", "Text request", "Validation", "Continuity"].includes(event.stage));
    const hasSceneStage = recordedEvents.some((event) => ["Scene generation", "Scene submission", "Scene polling", "Scene download", "Scene retry"].includes(event.stage) || event.stage.startsWith("Scene "));
    const hasFinalStage = recordedEvents.some((event) => event.stage === "Final video" || event.stage === "Complete");
    return recordedEvents.concat([
      !hasTextStage ? derivedEvents[0] : null,
      !hasSceneStage ? derivedEvents[1] : null,
      !hasFinalStage ? derivedEvents[2] : null
    ].filter(Boolean));
  }

  function traceField(label, value, options = {}) {
    const field = make("div", { className: "trace-field" });
    field.append(make("span", { className: "trace-label", text: label }));
    const content = make(options.code ? "pre" : "p", { className: options.code ? "trace-code" : "trace-value", text: value || "Not recorded" });
    field.append(content);
    return field;
  }

  function traceSection(title, value, options = {}) {
    const section = document.createElement("details");
    section.className = "trace-section";
    if (options.open) section.open = true;
    const summary = document.createElement("summary");
    summary.append(make("span", { className: "trace-section-title", text: title }));
    if (options.meta) summary.append(make("span", { className: "trace-section-meta", text: options.meta }));
    section.append(summary);
    const body = make("div", { className: "trace-section-body" });
    if (Array.isArray(options.fields)) {
      options.fields.forEach((field) => body.append(traceField(field.label, traceText(field.value), { code: Boolean(field.code) })));
    } else {
      body.append(traceField(options.label || title, options.json ? traceJSON(value) : traceText(value), { code: options.code !== false }));
    }
    section.append(body);
    if (typeof options.onToggle === "function") {
      section.addEventListener("toggle", () => {
        if (section.isConnected && state.currentView === "generate" && state.mode === "project") options.onToggle(section.open);
      });
    }
    return section;
  }

  function renderProjectPipeline(project, scenes, status) {
    const events = projectPipelineEvents(project, scenes, status);
    elements.projectPipelineList.replaceChildren();
    events.forEach((event) => {
      const item = make("li", { className: `pipeline-item pipeline-${event.status}` });
      const marker = make("span", { className: "pipeline-marker", text: event.status === "completed" ? "✓" : event.status === "failed" ? "!" : "•" });
      marker.setAttribute("aria-hidden", "true");
      const body = make("div", { className: "pipeline-copy" });
      const title = make("div", { className: "pipeline-title" });
      title.append(make("strong", { text: event.stage }), statusBadge(event.status));
      body.append(title);
      if (event.message) body.append(make("p", { text: event.message }));
      const meta = [];
      const timestamp = formatTraceDate(event.timestamp);
      if (timestamp) meta.push(timestamp);
      if (event.attempt !== "" && event.attempt !== undefined) meta.push(`Attempt ${event.attempt}`);
      if (event.retries !== "" && event.retries !== undefined) meta.push(`${event.retries} retr${Number(event.retries) === 1 ? "y" : "ies"}`);
      if (event.derived) meta.push("Derived from current state");
      if (meta.length) body.append(make("span", { className: "pipeline-meta", text: meta.join(" · ") }));
      if (event.error && event.error !== event.message) body.append(make("div", { className: "pipeline-error", text: event.error }));
      if (event.details !== undefined && event.details !== null && event.details !== "") {
        const detail = document.createElement("details");
        detail.className = "pipeline-event-details";
        detail.append(make("summary", { text: "Event details" }));
        detail.append(make("pre", { className: "trace-code", text: traceText(event.details) }));
        body.append(detail);
      }
      item.append(marker, body);
      elements.projectPipelineList.append(item);
    });
  }

  function renderProjectTrace(project, scenes, status) {
    const textGeneration = projectValue(project, "text_generation", "textGeneration", "generation_details");
    const generation = textGeneration && typeof textGeneration === "object" ? textGeneration : {};
    const modelMetadata = firstTraceValue(generation, "model_metadata", "modelMetadata", "model_info", "metadata");
    const modelInfo = modelMetadata && typeof modelMetadata === "object" ? modelMetadata : {};
    const modelFields = [
      { label: "Router", value: firstTraceValue(generation, "router_model", "routerModel", "router", "route", "provider_route") || firstTraceValue(modelInfo, "router_model", "router", "route") },
      { label: "Selected model", value: firstTraceValue(generation, "actual_model", "actualModel", "selected_model", "selectedModel", "model", "model_id", "modelID") || firstTraceValue(modelInfo, "actual_model", "selected_model", "model", "model_id") },
      { label: "Model provider", value: firstTraceValue(generation, "provider", "provider_name") || firstTraceValue(modelInfo, "provider", "provider_name") },
      { label: "Request id", value: firstTraceValue(generation, "request_id", "requestID", "response_id", "responseID") || firstTraceValue(modelInfo, "request_id", "response_id") },
      { label: "Text-generation status", value: firstTraceValue(generation, "status", "state") },
      { label: "Text-generation error", value: firstTraceValue(generation, "error", "error_message", "failure") },
      { label: "Started", value: formatTraceDate(firstTraceValue(generation, "started_at", "startedAt", "created_at", "createdAt")) },
      { label: "Completed", value: formatTraceDate(firstTraceValue(generation, "completed_at", "completedAt", "updated_at", "updatedAt")) }
    ].filter((field) => field.value !== "" && field.value !== undefined && field.value !== null);
    if (!modelFields.length) modelFields.push({ label: "Status", value: "Text-generation model metadata was not recorded." });

    const story = projectValue(project, "story", "story_text", "storyText") || firstTraceValue(generation, "story", "story_text", "storyText");
    const script = projectValue(project, "script", "full_script", "fullScript") || firstTraceValue(generation, "script", "full_script", "fullScript");
    const continuity = projectValue(project, "continuity", "continuity_bible", "continuityBible", "bible") || firstTraceValue(generation, "continuity", "continuity_bible", "continuityBible", "bible");
    const systemPrompt = firstTraceValue(generation, "system_prompt", "systemPrompt", "prompt_system");
    const userPrompt = firstTraceValue(generation, "user_prompt", "userPrompt", "prompt_user", "input_prompt");
    const schema = firstTraceValue(generation, "response_schema", "responseSchema", "json_schema", "jsonSchema", "schema", "output_schema", "outputSchema");
    const rawResponse = firstTraceValue(generation, "raw_response", "rawResponse", "response_raw", "response", "raw");
    const rawResponseOmitted = traceRawResponseOmitted(generation);
    const traceRevision = projectTraceRevision(project);
    const rawDisclosureKey = projectTraceDisclosureKey(projectID(project), traceRevision);
    const retryCount = firstTraceValue(project, "retry_count", "retries", "attempts", "attempt");
    const runFields = [
      { label: "Project status", value: String(status || "queued").replaceAll("_", " ") },
      { label: "Created", value: formatTraceDate(projectValue(project, "created_at", "createdAt", "created")) },
      { label: "Updated", value: formatTraceDate(projectValue(project, "updated_at", "updatedAt", "updated")) },
      { label: "Retries", value: retryCount },
      { label: "Error", value: projectValue(project, "error", "message") }
    ].filter((field) => field.value !== "" && field.value !== undefined && field.value !== null);
    if (!runFields.length) runFields.push({ label: "Record", value: "No run metadata was recorded." });

    const rawResponseSection = traceSection(
      "Raw model response",
      rawResponseOmitted ? "Open this section to load the recorded raw response." : rawResponse,
      {
        json: !rawResponseOmitted,
        meta: rawResponseOmitted ? "Load on demand" : "",
        open: rawDisclosureKey !== "" && state.projectRawTraceOpenKey === rawDisclosureKey,
        onToggle: (open) => {
          if (!rawDisclosureKey) return;
          if (!open) {
            if (state.projectRawTraceOpenKey === rawDisclosureKey) state.projectRawTraceOpenKey = "";
            return;
          }
          state.projectRawTraceOpenKey = rawDisclosureKey;
          if (rawResponseOmitted) fetchProjectRawTrace(projectID(project), traceRevision);
        }
      }
    );

    elements.projectTraceSections.replaceChildren(
      traceSection("Text model", "", { fields: modelFields, meta: traceText(modelFields.find((field) => field.label === "Selected model")?.value) || "Audit metadata" }),
      traceSection("System prompt", systemPrompt, { code: false }),
      traceSection("User prompt", userPrompt, { code: false }),
      traceSection("JSON schema", schema, { json: true }),
      rawResponseSection,
      traceSection("Story", story, { code: false }),
      traceSection("Full script", script, { code: false }),
      traceSection("Continuity bible", continuity, { code: false }),
      traceSection("Run record", "", { fields: runFields, meta: status === "failed" ? "Contains failure state" : "Timestamps and retries" })
    );

    scenes.forEach((scene, index) => {
      const number = Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1;
      const finalPrompt = projectValue(scene, "final_video_prompt", "finalVideoPrompt", "final_prompt", "finalPrompt", "video_prompt", "prompt", "generation_prompt");
      elements.projectTraceSections.append(traceSection(`Scene ${number} final video prompt`, finalPrompt, { code: false }));
    });
    elements.projectTrace.hidden = false;
  }

  function renderProject(project, authoritative = false) {
    if (!project) return;
    const incomingID = projectID(project);
    const displayedPrevious = state.currentProjectRecord && projectID(state.currentProjectRecord) === incomingID ? state.currentProjectRecord : null;
    if (state.displayedProjectID && state.displayedProjectID !== incomingID) invalidateProjectRawTraceRequests();
    const previous = state.projects.find((item) => projectID(item) === incomingID) || displayedPrevious;
    const candidate = mergeProjectRawTrace(previous, project);
    if (!authoritative && previous && isStaleJobSnapshot(previous, candidate, "project")) {
      project = previous;
    } else {
      if (displayedPrevious && projectTraceRevision(displayedPrevious) !== projectTraceRevision(candidate)) {
        invalidateProjectRawTraceRequests();
      }
      project = candidate;
    }
    const scenes = projectScenes(project);
    const status = String(projectValue(project, "status") || "queued").toLowerCase();
    const id = projectID(project);
    const progress = projectProgress(project, scenes);
    state.displayedProjectID = id;
    state.displayedProjectStatus = status;
    state.currentProjectID = id;
    state.currentProjectRecord = project;
    upsertProjectSnapshot(project);
    elements.statusEmpty.hidden = true;
    elements.statusActive.hidden = true;
    elements.projectStatus.hidden = false;
    elements.statusBadge.className = `badge ${statusClass(status)}`;
    elements.statusBadge.textContent = status.replaceAll("_", " ");
    const projectPending = !["completed", "complete", "failed", "error"].includes(status);
    elements.projectProgressWrap.hidden = !projectPending;
    elements.projectProgressBar.style.width = `${progress ?? 0}%`;
    elements.projectProgressBar.classList.toggle("indeterminate", projectPending && progress === null);
    elements.projectProgressBar.setAttribute("role", "progressbar");
    elements.projectProgressBar.setAttribute("aria-valuemin", "0");
    elements.projectProgressBar.setAttribute("aria-valuemax", "100");
    if (progress !== null) elements.projectProgressBar.setAttribute("aria-valuenow", String(progress));
    else elements.projectProgressBar.removeAttribute("aria-valuenow");
    elements.projectStatusDetail.textContent = status === "completed" || status === "complete"
      ? "Your 30-second video is ready."
      : status === "failed" ? "The project could not be completed." : progress === null ? "Project is in progress." : `Project progress: ${progress}%`;
    const error = projectValue(project, "error", "message");
    elements.projectError.hidden = !error;
    elements.projectError.textContent = error || "";
    const hasFailedScene = scenes.some((scene) => ["failed", "error"].includes(String(projectValue(scene, "status")).toLowerCase()));
    elements.retryProject.hidden = status !== "failed" || hasFailedScene;

    renderProjectPipeline(project, scenes, status);
    renderJobTerminal(elements.projectTerminal, projectValue(project, "pipeline_events", "pipelineEvents", "events", "logs"));
    renderProjectTrace(project, scenes, status);

    elements.projectStory.hidden = true;
    elements.projectStory.replaceChildren();

    elements.projectScenes.replaceChildren();
    scenes.forEach((scene, index) => {
      const number = Number(projectValue(scene, "scene_number", "number", "scene")) || index + 1;
      const sceneStatus = String(projectValue(scene, "status") || "queued").toLowerCase();
      const card = make("article", { className: `scene-card scene-${statusClass(sceneStatus)}` });
      const head = make("div", { className: "scene-head" });
      const suppliedProgress = Number(projectValue(scene, "progress"));
      const defaults = { pending: 0, submitting: 5, queued: 10, processing: 40, downloading: 90, download_failed: 90, completed: 100, complete: 100, ready: 100 };
      const sceneProgress = Number.isFinite(suppliedProgress) ? Math.max(0, Math.min(100, suppliedProgress)) : (defaults[sceneStatus] || 0);
      head.append(make("strong", { text: `Scene ${number}` }), make("span", { className: "scene-progress-label", text: `${sceneProgress}%` }), statusBadge(sceneStatus));
      card.append(head);
      const sceneProgressTrack = make("div", { className: "scene-progress" });
      const sceneProgressBar = make("div", { className: "scene-progress-bar" });
      sceneProgressBar.style.width = `${sceneProgress}%`;
      sceneProgressTrack.append(sceneProgressBar);
      card.append(sceneProgressTrack);
      const sceneScript = projectValue(scene, "script", "scene_script", "description");
      const prompt = projectValue(scene, "prompt", "video_prompt", "generation_prompt");
      if (sceneScript) card.append(make("p", { className: "scene-copy", text: sceneScript }));
      if (prompt) card.append(make("p", { className: "scene-prompt", text: prompt }));
      const sceneError = projectValue(scene, "error", "message");
      if (sceneError) card.append(make("div", { className: "alert error", text: sceneError }));
      if (["failed", "error"].includes(sceneStatus)) {
        const retry = make("button", { className: "button secondary scene-retry", type: "button", text: "Retry scene" });
        retry.addEventListener("click", () => retryProjectScene(id, number, retry));
        card.append(retry);
      }
      elements.projectScenes.append(card);
    });

    const videoReady = (Boolean(projectValue(project, "video_ready", "final_video_ready")) || status === "completed" || status === "complete")
      && (status === "completed" || status === "complete");
    elements.projectFinal.hidden = !videoReady || !id;
    if (videoReady && id) {
      const videoURL = `/api/projects/${encodeURIComponent(id)}/video`;
      if (elements.projectVideo.getAttribute("src") !== videoURL) elements.projectVideo.src = videoURL;
      if (elements.projectDownload.getAttribute("href") !== videoURL) elements.projectDownload.href = videoURL;
      elements.projectDownload.download = `project-${id}.mp4`;
    } else {
      if (elements.projectVideo.getAttribute("src")) {
        elements.projectVideo.pause();
        elements.projectVideo.removeAttribute("src");
        elements.projectVideo.load();
      }
      elements.projectDownload.removeAttribute("href");
      elements.projectDownload.removeAttribute("download");
    }
    syncGenerationModeDisplay();
  }

  async function retryProjectScene(id, number, button) {
    setButtonBusy(button, true, "Retrying...");
    try {
      const payload = await request(`/api/projects/${encodeURIComponent(id)}/scenes/${encodeURIComponent(number)}/retry`, { method: "POST" });
      renderProject(payload && payload.project ? payload.project : payload, true);
      toast(`Scene ${number} retry submitted.`);
    } catch (error) {
      setButtonBusy(button, false);
      if (error.status !== 401) toast(error.message, true);
    }
  }

  async function retryCurrentProject() {
    const id = state.currentProjectID;
    if (!id) return;
    setButtonBusy(elements.retryProject, true, "Retrying...");
    try {
      const payload = await request(`/api/projects/${encodeURIComponent(id)}/retry`, { method: "POST" });
      renderProject(payload && payload.project ? payload.project : payload, true);
      toast("Project retry started.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.retryProject, false);
    }
  }

  async function loadProjects(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    try {
      const payload = await request("/api/projects");
      const projects = Array.isArray(payload) ? payload : (Array.isArray(payload && payload.projects) ? payload.projects : []);
      state.projects = preserveNewerSnapshots(
        projects,
        state.projects,
        state.currentProjectRecord,
        "project",
        projectID
      );
      const current = state.projects.find((project) => projectID(project) === state.currentProjectID);
      if (!current && state.currentProjectID) clearProjectDisplay(state.currentProjectID);
      if (!state.currentProjectID) {
        const active = state.projects.find((project) => !isTerminalJobStatus(projectValue(project, "status")));
        state.currentProjectID = active ? projectID(active) : "";
      }
      const selectedProject = state.projects.find((project) => projectID(project) === state.currentProjectID);
      if (selectedProject) state.currentProjectRecord = selectedProject;
      setGenerationMode(state.mode);
      elements.projectHistory.replaceChildren();
      if (state.projects.length) {
        const heading = make("div", { className: "history-section-heading" });
        heading.append(
          make("h3", { text: "30-second projects" }),
          make("span", { className: "history-count", text: String(state.projects.length) })
        );
        elements.projectHistory.append(heading);
        const list = make("div", { className: "project-history-list" });
        state.projects.forEach((project, index) => {
          const id = projectID(project);
          const status = String(projectValue(project, "status") || "queued");
          const videoReady = String(status).toLowerCase() === "completed" && Boolean(projectValue(project, "final_video_ready"));
          const videoURL = id ? `/api/projects/${encodeURIComponent(id)}/video` : "";
          const item = make("article", { className: "history-card" });
          let download;
          const topic = String(projectValue(project, "topic") || "30-second project").slice(0, 120);
          const preview = historyPreview(videoReady, videoURL, status, () => download?.remove(), "Project video: " + topic);
          const body = make("div", { className: "history-body" });
          historyPrompt(body, "topic", projectValue(project, "topic"));
          const metadata = make("div", { className: "history-meta" });
          metadata.append(
            metadataItem("Recorded video cost", projectRecordedCost(project)),
            metadataItem("Status", status.replaceAll("_", " ")),
            metadataItem("Created", formatDate(projectValue(project, "created_at", "createdAt"))),
            metadataItem("Model", friendlyModel(projectValue(project, "model"))),
            metadataItem("File size", formatBytes(projectValue(project, "final_size_bytes")))
          );
          body.append(metadata);
          const projectError = projectValue(project, "error", "message");
          if (projectError) body.append(make("div", { className: "alert error history-error", text: projectError }));
          body.append(historyTerminal(
            projectValue(project, "pipeline_events", "pipelineEvents", "events", "logs"),
            `project:${id || projectValue(project, "created_at", "createdAt") || index}`,
            status
          ));
          const actions = make("div", { className: "history-actions" });
          if (videoReady && videoURL) {
            download = make("a", { className: "button secondary", text: "Download" });
            download.href = videoURL;
            download.download = `project-${id}.mp4`;
            actions.append(download);
          }
          if (status === "completed" && videoReady) {
            const move = make("button", { className: "button secondary vault-move-button", type: "button", text: "Move to Vault" });
            move.addEventListener("click", () => moveHistoryItem("project", id, move));
            actions.append(move);
          }
          const open = make("button", { className: "button secondary", type: "button", text: "Open" });
          open.addEventListener("click", () => { setGenerationMode("project"); navigate("generate"); renderProject(project); });
          actions.append(open);
          if (isTerminalJobStatus(status)) {
            const remove = make("button", { className: "button secondary danger-button", type: "button", text: "Delete" });
            remove.addEventListener("click", () => deleteProject(project));
            actions.append(remove);
          }
          body.append(actions);
          item.append(preview, body);
          list.append(item);
        });
        elements.projectHistory.append(list);
        if (state.currentProjectID && state.mode === "project") {
          const active = state.projects.find((project) => projectID(project) === state.currentProjectID);
          if (active) {
            setGenerationMode("project");
            renderProject(active);
          }
        }
      }
      renderHistory();
    } catch (error) {
      if (notify && error.status !== 401 && error.status !== 404) toast(error.message, true);
    }
  }

  async function submitProject() {
    if (state.submissionPending) return;
    const topic = elements.projectTopic.value.trim();
    const topicLength = Array.from(topic).length;
    elements.projectTopicError.textContent = "";
    if (!topic || topicLength > 4000) {
      elements.projectTopicError.textContent = topic ? "Topic must be 4,000 characters or fewer." : "Enter a topic or story idea.";
      elements.projectTopic.focus();
      return;
    }
    const model = selectedModel();
    if (!model) {
      toast("Select an available model first.", true);
      elements.modelTrigger.focus({ preventScroll: true });
      return;
    }
    if (!supportsProject(model)) {
      toast("Choose a model that supports 6-second clips at 480p in 9:16.", true);
      return;
    }
    clearProjectDisplay();
    state.submissionPending = true;
    state.projectSubmissionPending = true;
    state.projectSubmissionFailed = false;
    setButtonBusy(elements.generate, true, "Building project...");
    elements.statusEmpty.hidden = true;
    elements.projectStatus.hidden = false;
    elements.statusBadge.className = "badge queued";
    elements.statusBadge.textContent = "Submitting";
    elements.projectStatusDetail.textContent = "Generating story and five-scene script...";
    elements.projectError.hidden = true;
    elements.projectScenes.replaceChildren();
    try {
      const requestBody = { topic, model: elements.model.value };
      if (state.videoProvider === "modal") {
        const accountID = selectedModalAccountID();
        if (!accountID) throw new APIError("Choose a Modal account for this submission.", 400);
        requestBody.modal_account_id = accountID;
      }
      const payload = await request("/api/projects", { method: "POST", body: JSON.stringify(requestBody) });
      const project = payload && payload.project ? payload.project : payload;
      const id = projectID(project);
      state.projectSubmissionPending = false;
      state.projectSubmissionFailed = false;
      renderProject(project);
      if (!id) throw new APIError("Project was created without an id.", 500);
      elements.projectTopic.value = "";
      elements.projectTopicError.textContent = "";
      toast("Project submitted. Script generation has started.");
      loadProjects(false);
    } catch (error) {
      state.projectSubmissionPending = false;
      state.projectSubmissionFailed = true;
      elements.projectError.hidden = false;
      elements.projectError.textContent = error.message;
      elements.statusBadge.className = "badge failed";
      elements.statusBadge.textContent = "Failed";
      syncGenerationModeDisplay();
      if (error.status !== 401) toast(error.message, true);
    } finally {
      state.submissionPending = false;
      setButtonBusy(elements.generate, false);
      updateGenerateAvailability();
      syncGenerationModeDisplay();
    }
  }

  async function submitGeneration(event) {
    event.preventDefault();
    if (state.submissionPending) return;
    if (state.mode === "project") {
      await submitProject();
      return;
    }
    if (state.mustChangePassword) {
      navigate("settings");
      return;
    }
    const prompt = elements.prompt.value.trim();
    const promptLength = Array.from(prompt).length;
    elements.promptError.textContent = "";
    if (!prompt || promptLength > 4000) {
      elements.promptError.textContent = prompt ? "Prompt must be 4,000 characters or fewer." : "Enter a prompt.";
      elements.prompt.focus();
      return;
    }
    if (!elements.model.value) {
      toast("Select an available model first.", true);
      elements.modelTrigger.focus({ preventScroll: true });
      return;
    }

    const requestBody = { prompt, model: elements.model.value };
    if (state.videoProvider === "modal") {
      const accountID = selectedModalAccountID();
      if (!accountID) {
        toast("Choose a Modal account for this submission.", true);
        return;
      }
      requestBody.modal_account_id = accountID;
    }
    if (elements.duration.value) requestBody.duration = Number(elements.duration.value);
    if (elements.resolution.value) requestBody.resolution = elements.resolution.value;
    if (elements.aspectRatio.value) requestBody.aspect_ratio = elements.aspectRatio.value;
    if (elements.generateAudio.checked) requestBody.generate_audio = true;

    clearGenerationDisplay();
    state.submissionPending = true;
    state.singleSubmissionPending = true;
    state.singleSubmissionFailed = false;
    setButtonBusy(elements.generate, true, "Submitting…");
    elements.statusEmpty.hidden = true;
    elements.statusActive.hidden = false;
    elements.statusBadge.className = "badge queued";
    elements.statusBadge.textContent = "Submitting";
    elements.progressWrap.hidden = false;
    elements.statusDetail.textContent = "Sending your request to the selected video provider…";
    elements.statusError.hidden = true;
    elements.generatedVideo.hidden = true;
    elements.generatedVideo.removeAttribute("src");
    elements.generatedVideo.load();
    elements.openVideo.hidden = true;

    try {
      const record = await request("/generate", { method: "POST", body: JSON.stringify(requestBody) });
      state.singleSubmissionPending = false;
      state.singleSubmissionFailed = false;
      setStatusRecord(record);
      elements.prompt.value = "";
      elements.promptCount.textContent = "0 / 4000";
      elements.promptError.textContent = "";
      updateModelOptions();
      toast("Generation submitted.");
      loadHistory(false);
    } catch (error) {
      state.singleSubmissionPending = false;
      state.singleSubmissionFailed = true;
      elements.statusBadge.className = "badge failed";
      elements.statusBadge.textContent = "Failed";
      elements.progressWrap.hidden = true;
      elements.statusDetail.textContent = "The request was not submitted.";
      elements.statusError.textContent = error.message;
      elements.statusError.hidden = false;
      syncGenerationModeDisplay();
      if (error.status !== 401) toast(error.message, true);
    } finally {
      state.submissionPending = false;
      setButtonBusy(elements.generate, false);
      updateGenerateAvailability();
      syncGenerationModeDisplay();
    }
  }

  function renderProviderSettings(settings) {
    state.videoProvider = settings.video_provider === "modal" ? "modal" : "openrouter";
    elements.videoProvider.value = state.videoProvider;
    elements.modalSettings.hidden = false;
    updateModalAccountSelectors();
    elements.videoProviderState.className = "badge completed";
    elements.videoProviderState.textContent = `Active: ${videoProviderName()}`;
    elements.modalBaseURL.value = settings.modal_video_base_url || "";
    const openRouterConfigured = Boolean(settings.openrouter_api_key_configured);
    elements.keyState.className = `badge ${openRouterConfigured ? "completed" : "neutral"}`;
    elements.keyState.textContent = openRouterConfigured ? "Configured" : "Not configured";
    elements.maskedKey.textContent = openRouterConfigured ? "Configured" : "Not configured";
    const modalConfigured = Boolean(settings.modal_video_api_key_configured);
    elements.modalKeyState.className = `badge ${modalConfigured ? "completed" : "neutral"}`;
    elements.modalKeyState.textContent = modalConfigured ? "Configured" : "Not configured";
    elements.modalKeyHelp.textContent = modalConfigured
      ? "A key is saved securely. Leave this field blank to keep it while changing the URL."
      : "A key is required for the first save. The saved key is never returned to the browser.";
    renderVaultCodeSettings(Boolean(settings.vault_code_configured));
  }

  function renderVaultCodeSettings(configured) {
    state.vaultConfigured = configured;
    elements.vaultCodeState.className = `badge ${state.vaultConfigured ? "completed" : "neutral"}`;
    elements.vaultCodeState.textContent = state.vaultConfigured ? "Code set" : "Not set";
    elements.vaultCurrentCodeField.hidden = !state.vaultConfigured;
    elements.vaultCurrentCode.required = state.vaultConfigured;
    elements.vaultCodeSave.textContent = state.vaultConfigured ? "Change Vault code" : "Set Vault code";
    elements.vaultCodeHelp.textContent = state.vaultConfigured
      ? "Keep this code safe. Changing it locks active Vault sessions."
      : "Keep this code safe. If you lose it, the Vault cannot be opened in this version.";
  }

  async function loadSettings(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    try {
      const settings = await request("/api/settings");
      renderProviderSettings(settings || {});
      await loadModalAccounts(notify);
    } catch (error) {
      elements.keyState.className = "badge failed";
      elements.keyState.textContent = "Unavailable";
      if (notify && error.status !== 401) toast(error.message, true);
    }
  }

  function clearVaultClientState() {
    invalidateVaultOperations();
    closeVaultPlayer();
    state.vaultToken = "";
    state.vaultLocking = false;
    state.vaultItems = [];
    if (elements.vaultGrid) elements.vaultGrid.replaceChildren();
    if (elements.vaultCount) elements.vaultCount.textContent = "0";
    if (elements.vaultEmpty) elements.vaultEmpty.hidden = true;
    if (elements.vaultContent) elements.vaultContent.hidden = true;
    if (elements.vaultLock) elements.vaultLock.hidden = true;
  }

  function invalidateVaultOperations() {
    state.vaultGeneration += 1;
    state.vaultMediaControllers.forEach((controller) => controller.abort());
    state.vaultMediaControllers.clear();
  }

  function renderVaultLocked(message = "") {
    clearVaultClientState();
    elements.vaultLocked.hidden = false;
    const lockMessage = state.vaultRestoringLock
      ? "Securing the Vault session. Wait before entering your code."
      : state.vaultRestoreLockFailed
        ? "Could not confirm the Vault is locked. Refresh the page and try again."
        : message;
    elements.vaultCodeError.textContent = lockMessage;
    elements.vaultCodeError.hidden = !lockMessage;
    elements.vaultCode.value = "";
    elements.vaultUnlockForm.hidden = !state.vaultConfigured;
    elements.vaultGoSettings.hidden = state.vaultConfigured;
    const unlockDisabled = state.vaultRestoringLock || state.vaultRestoreLockFailed;
    elements.vaultCode.disabled = unlockDisabled;
    elements.vaultUnlockButton.disabled = unlockDisabled;
    elements.vaultUnlockButton.textContent = state.vaultRestoringLock ? "Securing…" : "Unlock Vault";
    elements.vaultGateTitle.textContent = state.vaultConfigured ? "Vault is locked" : "Set up your Vault";
    elements.vaultGateCopy.textContent = state.vaultRestoringLock
      ? "Confirming the previous Vault session is locked."
      : state.vaultRestoreLockFailed
        ? "Refresh to retry the lock check before unlocking."
        : state.vaultConfigured
          ? "Enter your four-digit code to view protected videos."
          : "Choose a four-digit code in Settings before moving videos here.";
  }

  function vaultHeaders() {
    return state.vaultToken ? { Authorization: `Vault ${state.vaultToken}` } : {};
  }

  async function vaultRequest(path, options = {}) {
    if (state.vaultLocking) throw new APIError("Vault is locking", 423);
    return request(path, {
      ...options,
      headers: { ...(options.headers || {}), ...vaultHeaders() }
    });
  }

  function requestVaultLock(options = {}, shouldRun = null) {
    state.vaultPendingLocks += 1;
    const operation = state.vaultLockQueue.then(async () => {
      if (shouldRun && !shouldRun()) return false;
      await request("/api/vault/lock", { method: "POST", ...options });
      return true;
    });
    const settled = operation.finally(() => {
      state.vaultPendingLocks = Math.max(0, state.vaultPendingLocks - 1);
      finishVaultRestoreIfReady();
    });
    state.vaultLockQueue = settled.then(() => undefined, () => undefined);
    return settled;
  }

  function finishVaultRestoreIfReady() {
    if (!state.vaultRestoreLockReady || state.vaultRestoreLockFailed
      || state.vaultUnlockPending > 0 || state.vaultPendingLocks > 0) return;
    state.vaultRestoreLockReady = false;
    state.vaultRestoringLock = false;
    renderVaultLocked();
  }

  async function loadVault() {
    if (!state.authenticated || state.mustChangePassword) return;
    try {
      const status = await request("/api/vault/status");
      state.vaultConfigured = Boolean(status && status.configured);
      if (!state.vaultConfigured) {
        renderVaultLocked();
        return;
      }
      if (!state.vaultToken) {
        renderVaultLocked();
        return;
      }
      await loadVaultItems();
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    }
  }

  async function loadVaultItems() {
    const generation = state.vaultGeneration;
    const token = state.vaultToken;
    try {
      const payload = await vaultRequest("/api/vault/items");
      if (generation !== state.vaultGeneration || token !== state.vaultToken || !token) return;
      state.vaultItems = Array.isArray(payload && payload.items) ? payload.items : [];
      renderVaultItems();
      elements.vaultLocked.hidden = true;
      elements.vaultContent.hidden = false;
      elements.vaultLock.hidden = false;
    } catch (error) {
      if (generation !== state.vaultGeneration || token !== state.vaultToken) return;
      if (error.status === 423) {
        renderVaultLocked("Your Vault session ended. Enter your code to unlock it again.");
      } else if (error.status !== 401) {
        toast(error.message, true);
      }
    }
  }

  function renderVaultItems() {
    elements.vaultGrid.replaceChildren();
    elements.vaultCount.textContent = String(state.vaultItems.length);
    elements.vaultEmpty.hidden = state.vaultItems.length > 0;
    state.vaultItems.forEach((item) => {
      const card = make("article", { className: "history-card vault-card" });
      const preview = make("div", { className: "vault-preview" });
      preview.append(
        make("span", { className: "vault-preview-mark", text: "▣", attrs: { "aria-hidden": "true" } }),
        make("span", { className: "vault-preview-kind", text: item.kind === "project" ? "30-second video" : "Video" })
      );
      const body = make("div", { className: "history-body" });
      body.append(make("h3", { className: "history-title", text: item.title || "Untitled video" }));
      const metadata = make("div", { className: "history-meta" });
      metadata.append(
        metadataItem("Created", formatDate(item.created_at)),
        metadataItem("Duration", item.duration ? `${item.duration} sec` : "Provider default"),
        metadataItem("Model", friendlyModel(item.model)),
        metadataItem("File size", formatBytes(item.size_bytes))
      );
      body.append(metadata);
      const actions = make("div", { className: "history-actions vault-actions" });
      const view = make("button", { className: "button primary", text: "View video", type: "button" });
      view.addEventListener("click", () => openVaultVideo(item, view));
      const download = make("button", { className: "button secondary", text: "Download", type: "button" });
      download.addEventListener("click", () => downloadVaultVideo(item, download));
      const restore = make("button", { className: "button secondary", text: "Return to History", type: "button" });
      restore.addEventListener("click", () => restoreVaultItem(item, restore));
      actions.append(view, download, restore);
      body.append(actions);
      card.append(preview, body);
      elements.vaultGrid.append(card);
    });
  }

  async function fetchVaultVideo(item, download = false) {
    if (state.vaultLocking || !state.vaultToken) throw new APIError("Vault is locked", 423);
    const path = `/api/vault/items/${encodeURIComponent(item.kind)}/${encodeURIComponent(item.id)}/video${download ? "?download=1" : ""}`;
    const generation = state.vaultGeneration;
    const token = state.vaultToken;
    const controller = new AbortController();
    state.vaultMediaControllers.add(controller);
    try {
      const response = await fetch(path, {
        credentials: "same-origin",
        cache: "no-store",
        signal: controller.signal,
        headers: { Accept: "video/mp4", ...vaultHeaders() }
      });
      if (generation !== state.vaultGeneration || token !== state.vaultToken || !token) {
        throw new APIError("Vault is locked", 423);
      }
      if (!response.ok) {
        const payload = await response.json().catch(() => null);
        if (response.status === 401) showLoggedOut();
        if (response.status === 423) {
          renderVaultLocked("Your Vault session ended. Enter your code to unlock it again.");
        }
        throw new APIError(payload && payload.error || `Request failed (${response.status})`, response.status);
      }
      const blob = await response.blob();
      if (generation !== state.vaultGeneration || token !== state.vaultToken || !token) {
        throw new APIError("Vault is locked", 423);
      }
      return blob;
    } catch (error) {
      if (controller.signal.aborted) throw new APIError("Vault is locked", 423);
      throw error;
    } finally {
      state.vaultMediaControllers.delete(controller);
    }
  }

  function vaultItemKey(item) {
    return `${item.kind}:${item.id}`;
  }

  async function openVaultVideo(item, button) {
    const generation = state.vaultGeneration;
    setButtonBusy(button, true, "Loading…");
    try {
      const blob = await fetchVaultVideo(item);
      if (generation !== state.vaultGeneration || !state.vaultToken) return;
      closeVaultPlayer();
      state.vaultObjectURL = URL.createObjectURL(blob);
      elements.vaultPlayerTitle.textContent = item.title || "Vault video";
      elements.vaultPlayer.src = state.vaultObjectURL;
      elements.vaultPlayerDialog.showModal();
      state.vaultPlayerItemKey = vaultItemKey(item);
      elements.vaultPlayerDownload.disabled = false;
    } catch (error) {
      if (error.status !== 401 && error.status !== 423) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function downloadVaultVideo(item, button) {
    const generation = state.vaultGeneration;
    const token = state.vaultToken;
    setButtonBusy(button, true, "Preparing…");
    try {
      if (state.vaultLocking || !token) throw new APIError("Vault is locked", 423);
      const reusePlayerVideo = Boolean(
        state.vaultObjectURL
        && state.vaultPlayerItemKey === vaultItemKey(item)
        && elements.vaultPlayerDialog.open
      );
      if (generation !== state.vaultGeneration || token !== state.vaultToken || state.vaultLocking || !token) {
        throw new APIError("Vault is locked", 423);
      }
      let url = reusePlayerVideo ? state.vaultObjectURL : "";
      if (!url) {
        const blob = await fetchVaultVideo(item, true);
        if (generation !== state.vaultGeneration || token !== state.vaultToken || !state.vaultToken) return;
        url = URL.createObjectURL(blob);
      }
      const link = document.createElement("a");
      link.href = url;
      link.download = `${item.kind}-${item.id}.mp4`;
      (reusePlayerVideo ? elements.vaultPlayerDialog : document.body).append(link);
      link.click();
      link.remove();
      if (!reusePlayerVideo) window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      if (error.status !== 401 && error.status !== 423) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function restoreVaultItem(item, button) {
    const generation = state.vaultGeneration;
    const token = state.vaultToken;
    setButtonBusy(button, true, "Returning…");
    try {
      await vaultRequest(`/api/vault/items/${encodeURIComponent(item.kind)}/${encodeURIComponent(item.id)}/restore`, { method: "POST" });
      if (generation !== state.vaultGeneration || token !== state.vaultToken || !token) return;
      state.vaultItems = state.vaultItems.filter((entry) => entry.id !== item.id || entry.kind !== item.kind);
      renderVaultItems();
      await Promise.allSettled([loadHistory(false), loadProjects(false)]);
      toast("Returned to History.");
    } catch (error) {
      if (generation !== state.vaultGeneration || token !== state.vaultToken) return;
      if (error.status === 423) {
        renderVaultLocked("Your Vault session ended. Enter your code to unlock it again.");
      } else if (error.status !== 401) {
        toast(error.message, true);
      }
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function unlockVault(event) {
    event.preventDefault();
    if (state.vaultRestoringLock || state.vaultRestoreLockFailed) return;
    const generation = state.vaultGeneration;
    const authGeneration = state.vaultAuthGeneration;
    const code = elements.vaultCode.value;
    if (!/^[0-9]{4}$/.test(code)) {
      renderVaultLocked("Enter all four digits.");
      elements.vaultCode.focus();
      return;
    }
    elements.vaultCodeError.hidden = true;
    state.vaultUnlockPending += 1;
    setButtonBusy(elements.vaultUnlockButton, true, "Unlocking…");
    try {
      const result = await request("/api/vault/unlock", {
        method: "POST",
        body: JSON.stringify({ code })
      });
      const token = result && typeof result.token === "string" ? result.token : "";
      if (generation !== state.vaultGeneration) {
        if (authGeneration !== state.vaultAuthGeneration || !state.authenticated) return;
        try {
          await requestVaultLock({}, () => authGeneration === state.vaultAuthGeneration && state.authenticated);
        } catch (error) {
          if (error.status !== 401 && authGeneration === state.vaultAuthGeneration
            && state.authenticated && !state.mustChangePassword) {
            state.vaultRestoreLockFailed = true;
            state.vaultRestoreLockReady = false;
            state.vaultRestoringLock = false;
            renderVaultLocked();
          }
        }
        return;
      }
      state.vaultToken = token;
      elements.vaultCode.value = "";
      if (!state.vaultToken) throw new APIError("Unable to unlock Vault", 500);
      await loadVaultItems();
    } catch (error) {
      if (generation !== state.vaultGeneration) return;
      if (error.status !== 401) {
        elements.vaultCode.value = "";
        elements.vaultCodeError.textContent = error.message;
        elements.vaultCodeError.hidden = false;
        elements.vaultCode.focus();
      }
    } finally {
      state.vaultUnlockPending = Math.max(0, state.vaultUnlockPending - 1);
      setButtonBusy(elements.vaultUnlockButton, false);
      finishVaultRestoreIfReady();
      if (state.vaultRestoringLock || state.vaultRestoreLockFailed) {
        elements.vaultUnlockButton.disabled = true;
        elements.vaultUnlockButton.textContent = state.vaultRestoringLock ? "Securing…" : "Unlock Vault";
      }
    }
  }

  async function lockVault() {
    if (state.vaultLocking) return;
    state.vaultLocking = true;
    invalidateVaultOperations();
    elements.vaultLock.disabled = true;
    elements.vaultLock.textContent = "Locking…";
    try {
      // The server clears every grant for this login session, including one
      // whose in-memory browser token was lost during a navigation.
      await requestVaultLock();
    } catch (error) {
      if (error.status === 401) return;
      toast(`Vault could not be locked. ${error.message}`, true);
      return;
    } finally {
      state.vaultLocking = false;
      elements.vaultLock.disabled = false;
      elements.vaultLock.textContent = "Lock Vault";
    }
    clearVaultClientState();
    if (state.authenticated && !state.mustChangePassword) renderVaultLocked();
  }

  async function secureVaultAfterPageRestore() {
    const restoreGeneration = ++state.vaultRestoreGeneration;
    state.vaultRestoringLock = true;
    state.vaultRestoreLockReady = false;
    state.vaultRestoreLockFailed = false;
    renderVaultLocked();
    try {
      await requestVaultLock();
      if (restoreGeneration !== state.vaultRestoreGeneration) return;
      state.vaultRestoreLockReady = true;
    } catch (error) {
      if (restoreGeneration !== state.vaultRestoreGeneration) return;
      if (error.status === 401) return;
      state.vaultRestoreLockFailed = true;
      state.vaultRestoreLockReady = false;
      state.vaultRestoringLock = false;
      renderVaultLocked();
      return;
    }
    if (restoreGeneration !== state.vaultRestoreGeneration) return;
    if (!state.authenticated || state.mustChangePassword) return;
    finishVaultRestoreIfReady();
    renderVaultLocked();
  }

  function closeVaultPlayer() {
    if (elements.vaultPlayer) {
      elements.vaultPlayer.pause();
      elements.vaultPlayer.removeAttribute("src");
      elements.vaultPlayer.load();
    }
    if (state.vaultObjectURL) URL.revokeObjectURL(state.vaultObjectURL);
    state.vaultObjectURL = "";
    state.vaultPlayerItemKey = "";
    if (elements.vaultPlayerDownload) elements.vaultPlayerDownload.disabled = true;
    if (elements.vaultPlayerDialog && elements.vaultPlayerDialog.open) elements.vaultPlayerDialog.close();
  }

  async function saveVaultCode(event) {
    event.preventDefault();
    const currentCode = elements.vaultCurrentCode.value;
    const newCode = elements.vaultNewCode.value;
    const confirmation = elements.vaultConfirmCode.value;
    if (!/^[0-9]{4}$/.test(newCode)) {
      toast("Enter a new four-digit code.", true);
      elements.vaultNewCode.focus();
      return;
    }
    if (newCode !== confirmation) {
      toast("The new codes do not match.", true);
      elements.vaultConfirmCode.focus();
      return;
    }
    const configured = state.vaultConfigured;
    if (configured && !/^[0-9]{4}$/.test(currentCode)) {
      toast("Enter the current four-digit code.", true);
      elements.vaultCurrentCode.focus();
      return;
    }
    setButtonBusy(elements.vaultCodeSave, true, "Saving…");
    try {
      const result = await request("/api/settings/vault-code", {
        method: "PUT",
        body: JSON.stringify({ current_code: currentCode, new_code: newCode })
      });
      state.vaultConfigured = Boolean(result && result.configured);
      clearVaultClientState();
      elements.vaultCodeForm.reset();
      renderVaultCodeSettings(state.vaultConfigured);
      toast(configured ? "Vault code changed." : "Vault code set.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.vaultCodeSave, false);
    }
  }

  function constrainVaultCodeInput(input) {
    input.value = input.value.replace(/[^0-9]/g, "").slice(0, 4);
  }

  async function saveAPIKey(event) {
    event.preventDefault();
    if (state.mustChangePassword) {
      navigate("settings");
      return;
    }
    const key = elements.apiKey.value.trim();
    if (key.length < 10) {
      toast("Enter a valid OpenRouter API key.", true);
      elements.apiKey.focus();
      return;
    }
    const button = $("button[type='submit']", elements.apiKeyForm);
    setButtonBusy(button, true, "Testing key…");
    try {
      await request("/api/settings/api-key", {
        method: "PUT",
        body: JSON.stringify({ api_key: key })
      });
      elements.apiKey.value = "";
      elements.keyState.className = "badge completed";
      elements.keyState.textContent = "Configured";
      elements.maskedKey.textContent = "Configured";
      toast("API key tested and saved.");
      if (state.videoProvider === "openrouter") await loadModels(false);
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function changeVideoProvider() {
    const requestedProvider = elements.videoProvider.value;
    elements.videoProvider.disabled = true;
    try {
      const settings = await request("/api/settings/video-provider", {
        method: "PUT",
        body: JSON.stringify({ video_provider: requestedProvider })
      });
      state.videoProvider = (settings && settings.video_provider) || requestedProvider;
      await loadSettings(false);
      await loadModels(false);
      toast(`Video provider changed to ${videoProviderName()}.`);
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
      await loadSettings(false);
    } finally {
      elements.videoProvider.disabled = false;
    }
  }

  async function persistVideoModel(modelID, notify = false, modalAccountID = selectedModalAccountID()) {
    if (!modelID || !state.videoProvider) return;
    const provider = state.videoProvider;
    if (provider === "modal" && !modalAccountID) return;
    try {
      await request("/api/settings/video-model", {
        method: "PUT",
        body: JSON.stringify({
          provider,
          model: modelID,
          ...(provider === "modal" ? { modal_account_id: modalAccountID } : {})
        })
      });
    } catch (error) {
      if (notify && error.status !== 401) toast(error.message, true);
    }
  }

  async function saveModalConfig(event) {
    event.preventDefault();
    const button = $("button[type='submit']", elements.modalConfigForm);
    setButtonBusy(button, true, "Saving account…");
    try {
      const id = elements.modalAccountID.value.trim();
      const body = JSON.stringify({
        name: elements.modalAccountName.value.trim(),
        endpoint: elements.modalBaseURL.value.trim(),
        api_key: elements.modalAPIKey.value
      });
      await request(id ? `/api/modal-accounts/${encodeURIComponent(id)}` : "/api/modal-accounts", {
        method: id ? "PUT" : "POST",
        body
      });
      elements.modalAPIKey.value = "";
      resetModalAccountForm();
      await loadModalAccounts(false);
      toast(id ? "Modal account updated." : "Modal account added.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function testVideoProvider() {
    const button = elements.testVideoProvider;
    setButtonBusy(button, true, "Testing…");
    try {
      const result = await request("/api/settings/video-provider/test", { method: "POST" });
      const provider = videoProviderName(result && result.provider || state.videoProvider);
      toast(`${provider} connection succeeded (${Number(result && result.model_count) || 0} models).`);
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function updatePassword(event) {
    event.preventDefault();
    const currentPassword = elements.currentPassword.value;
    const newPassword = elements.newPassword.value;
    if (newPassword.length < 10 || newPassword.length > 200) {
      toast("New password must be 10–200 characters.", true);
      elements.newPassword.focus();
      return;
    }
    const button = $("button[type='submit']", elements.passwordForm);
    setButtonBusy(button, true, "Updating…");
    try {
      await request("/api/settings/password", {
        method: "PUT",
        body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
      }, true);
      elements.passwordForm.reset();
      const wasForced = state.mustChangePassword;
      applyPasswordGate(false);
      toast(wasForced ? "Password updated. Dashboard unlocked." : "Password updated. Other sessions were signed out.");
      if (wasForced) {
        navigate("overview");
        await Promise.allSettled([loadHistory(false, false), loadProjects(false), loadModels(false), loadSettings(false)]);
        startJobEventStream();
      }
    } catch (error) {
      toast(error.message, true);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function submitLogin(event) {
    event.preventDefault();
    elements.loginError.textContent = "";
    const button = $("button[type='submit']", elements.loginForm);
    setButtonBusy(button, true, "Signing in…");
    try {
      const session = await request("/api/login", {
        method: "POST",
        body: JSON.stringify({
          username: elements.loginUsername.value.trim(),
          password: elements.loginPassword.value
        })
      }, true);
      elements.loginForm.reset();
      showAuthenticated(session.username, session.must_change_password);
      if (!state.mustChangePassword) {
        await loadSettings(false);
        await Promise.allSettled([loadHistory(false, false), loadProjects(false), loadModels(false)]);
        startJobEventStream();
      }
    } catch (error) {
      elements.loginError.textContent = error.message;
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function submitRecovery(event) {
    event.preventDefault();
    elements.recoveryError.textContent = "";
    const password = elements.recoveryPassword.value;
    if (password.length < 10 || password.length > 200) {
      elements.recoveryError.textContent = "New password must be 10–200 characters.";
      elements.recoveryPassword.focus();
      return;
    }
    if (password !== elements.recoveryConfirm.value) {
      elements.recoveryError.textContent = "Passwords do not match.";
      elements.recoveryConfirm.focus();
      return;
    }
    const button = $("button[type='submit']", elements.recoveryForm);
    setButtonBusy(button, true, "Resetting…");
    try {
      const username = elements.recoveryUsername.value.trim();
      await request("/api/password/recover", {
        method: "POST",
        body: JSON.stringify({
          username,
          code: elements.recoveryCode.value,
          new_password: password
        })
      }, true);
      elements.recoveryForm.reset();
      elements.loginUsername.value = username;
      showLoginView();
      toast("Password reset. Sign in with your new password.");
    } catch (error) {
      elements.recoveryError.textContent = error.message;
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function logout() {
    elements.logout.disabled = true;
    try {
      await request("/api/logout", { method: "POST" });
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      elements.logout.disabled = false;
      showLoggedOut();
    }
  }

  function bindEvents() {
    syncNavigationAccessibility();
    elements.loginForm.addEventListener("submit", submitLogin);
    elements.recoveryForm.addEventListener("submit", submitRecovery);
    elements.showRecovery.addEventListener("click", showRecoveryView);
    elements.hideRecovery.addEventListener("click", () => showLoginView());
    elements.recoveryCode.addEventListener("input", () => {
      const value = elements.recoveryCode.value.toUpperCase().replace(/[^A-Z2-9]/g, "").slice(0, 16);
      elements.recoveryCode.value = value.match(/.{1,4}/g)?.join("-") || "";
    });
    elements.logout.addEventListener("click", logout);
    elements.generatorForm.addEventListener("submit", submitGeneration);
    elements.promptCategory.addEventListener("input", updatePromptCategory);
    elements.randomPrompt.addEventListener("click", generateRandomPrompt);
    elements.retryProject.addEventListener("click", retryCurrentProject);
    elements.modeOptions.forEach((button) => {
      button.addEventListener("click", () => {
        setGenerationMode(button.dataset.mode);
        if (state.videoProvider === "modal") void loadModels(false);
      });
      button.addEventListener("keydown", moveGenerationMode);
    });
    elements.apiKeyForm.addEventListener("submit", saveAPIKey);
    elements.vaultCodeForm.addEventListener("submit", saveVaultCode);
    [elements.vaultCurrentCode, elements.vaultNewCode, elements.vaultConfirmCode, elements.vaultCode].forEach((input) => {
      input.addEventListener("input", () => constrainVaultCodeInput(input));
    });
    elements.vaultUnlockForm.addEventListener("submit", unlockVault);
    elements.vaultLock.addEventListener("click", lockVault);
    elements.vaultGoSettings.addEventListener("click", () => navigate("settings"));
    elements.vaultPlayerClose.addEventListener("click", closeVaultPlayer);
    elements.vaultPlayerDownload.addEventListener("click", () => {
      const item = state.vaultItems.find((entry) => vaultItemKey(entry) === state.vaultPlayerItemKey);
      if (item) void downloadVaultVideo(item, elements.vaultPlayerDownload);
    });
    elements.vaultPlayerDialog.addEventListener("close", closeVaultPlayer);
    elements.modalConfigForm.addEventListener("submit", saveModalConfig);
    elements.modalAccountCancel.addEventListener("click", resetModalAccountForm);
    elements.modalAccountProject.addEventListener("change", () => {
      if (state.videoProvider === "modal" && state.mode === "project") void loadModels(false);
    });
    elements.modalAccountSingle.addEventListener("change", () => {
      if (state.videoProvider === "modal" && state.mode === "single") void loadModels(false);
    });
    elements.videoProvider.addEventListener("change", changeVideoProvider);
    elements.testVideoProvider.addEventListener("click", testVideoProvider);
    elements.passwordForm.addEventListener("submit", updatePassword);
    elements.refreshHistory.addEventListener("click", () => { loadHistory(true); loadProjects(true); });
    elements.model.addEventListener("change", () => {
      updateModelOptions();
      void persistVideoModel(elements.model.value, true);
    });
    elements.modelTrigger.addEventListener("click", () => {
      if (elements.modelMenu.hidden) openModelMenu();
      else closeModelMenu();
    });
    elements.modelMenu.addEventListener("keydown", (event) => {
      const options = $$(".model-option", elements.modelMenu);
      const index = options.indexOf(document.activeElement);
      if ((event.key === "ArrowDown" || event.key === "ArrowUp") && options.length) {
        event.preventDefault();
        const direction = event.key === "ArrowDown" ? 1 : -1;
        options[(index + direction + options.length) % options.length].focus();
      } else if (event.key === "Home" && options.length) {
        event.preventDefault();
        options[0].focus();
      } else if (event.key === "End" && options.length) {
        event.preventDefault();
        options[options.length - 1].focus();
      } else if (event.key === "Escape") {
        closeModelMenu();
        elements.modelTrigger.focus({ preventScroll: true });
      }
    });
    elements.modelPicker.addEventListener("focusout", (event) => {
      if (!elements.modelPicker.contains(event.relatedTarget)) closeModelMenu();
    });
    document.addEventListener("click", (event) => {
      if (!elements.modelPicker.contains(event.target)) closeModelMenu();
    });
    elements.duration.addEventListener("change", updateEstimate);
    elements.resolution.addEventListener("change", updateEstimate);
    elements.prompt.addEventListener("input", () => {
      const count = Array.from(elements.prompt.value).length;
      elements.promptCount.textContent = `${count.toLocaleString()} / 4000`;
      elements.promptError.textContent = count > 4000 ? "Prompt must be 4,000 characters or fewer." : "";
    });
    elements.retryStatus.addEventListener("click", () => {
      startJobEventStream();
      void refreshJobState();
    });
    elements.menuButton.addEventListener("click", () => {
      setNavigationOpen(!elements.sidebar.classList.contains("open"));
    });
    $$(".nav-item").forEach((button) => button.addEventListener("click", () => navigate(button.dataset.view)));
    $$('[data-go]').forEach((button) => button.addEventListener("click", () => navigate(button.dataset.go)));
    document.addEventListener("pointerdown", (event) => {
      if (!isCompactNavigation() || !elements.sidebar.classList.contains("open")) return;
      if (elements.sidebar.contains(event.target) || elements.menuButton.contains(event.target)) return;
      setNavigationOpen(false, { returnFocus: true });
    });
    elements.sidebar.addEventListener("focusout", () => {
      window.requestAnimationFrame(() => {
        if (isCompactNavigation() && elements.sidebar.classList.contains("open")
          && !elements.sidebar.contains(document.activeElement)) {
          setNavigationOpen(false);
        }
      });
    });
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      if (!elements.modelMenu.hidden) {
        closeModelMenu();
        elements.modelTrigger.focus({ preventScroll: true });
        return;
      }
      if (isCompactNavigation() && elements.sidebar.classList.contains("open")) {
        event.preventDefault();
        setNavigationOpen(false, { returnFocus: true });
      }
    });
    window.addEventListener("resize", syncNavigationAccessibility);
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && state.authenticated && !state.mustChangePassword) void refreshJobState();
    });
    window.addEventListener("pagehide", () => {
      if (!state.authenticated) return;
      stopJobEventStream();
      invalidateProjectRawTraceRequests();
      void requestVaultLock({ cache: "no-store", keepalive: true }).catch(() => {});
      renderVaultLocked();
    });
    window.addEventListener("pageshow", (event) => {
      if (!event.persisted || !state.authenticated) return;
      void secureVaultAfterPageRestore();
      startJobEventStream();
    });
  }

  async function bootstrap() {
    bindEvents();
    setGenerationMode("project");
    elements.menuButton.setAttribute("aria-expanded", "false");
    try {
      const session = await request("/api/session", {}, true);
      if (!session.must_change_password) {
        await requestVaultLock();
      }
      showAuthenticated(session.username, session.must_change_password);
      if (!state.mustChangePassword) {
        await loadSettings(false);
        await Promise.allSettled([loadHistory(false, false), loadProjects(false), loadModels(false)]);
        startJobEventStream();
      }
    } catch (error) {
      showLoggedOut();
    }
  }

  bootstrap();
})();
