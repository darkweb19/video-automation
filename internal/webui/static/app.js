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
    connectionState: $("#connection-state"),
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
    projectCostNote: $("#project-cost-note"),
    creatorProviderHelp: $("#creator-provider-help"),
    creatorProviderMessage: $("#creator-provider-message"),
    creatorOpenSettings: $("#creator-open-settings"),
    modeOptions: $$(".mode-option"),
    createModeButtons: $$('[data-create-mode]'),
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
    singleYouTubeUpload: $("#single-youtube-upload"),
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
    projectYouTubeUpload: $("#project-youtube-upload"),
    projectHistory: $("#project-history"),
    historyGrid: $("#history-grid"),
    historySearch: $("#history-search"),
    historyStatus: $("#history-status"),
    historyFormat: $("#history-format"),
    historyResultsSummary: $("#history-results-summary"),
    historyClearFilters: $("#history-clear-filters"),
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
    vaultPlayerYouTubeUpload: $("#vault-player-youtube-upload"),
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
    youtubeChannelState: $("#youtube-channel-state"),
    youtubeChannelSummary: $("#youtube-channel-summary"),
    youtubeChannelName: $("#youtube-channel-name"),
    youtubeConfigForm: $("#youtube-config-form"),
    youtubeClientID: $("#youtube-client-id"),
    youtubeClientSecret: $("#youtube-client-secret"),
    youtubeBaseURL: $("#youtube-base-url"),
    youtubeRedirectURI: $("#youtube-redirect-uri"),
    youtubeSecretHelp: $("#youtube-secret-help"),
    youtubeConfigError: $("#youtube-config-error"),
    youtubeSaveConfig: $("#youtube-save-config"),
    youtubeConnect: $("#youtube-connect"),
    youtubeDisconnect: $("#youtube-disconnect"),
    youtubeDialog: $("#youtube-upload-dialog"),
    youtubeDialogClose: $("#youtube-dialog-close"),
    youtubePreview: $("#youtube-preview"),
    youtubeSourceLabel: $("#youtube-source-label"),
    youtubeConnectRequired: $("#youtube-connect-required"),
    youtubeGoSettings: $("#youtube-go-settings"),
    youtubeUploadForm: $("#youtube-upload-form"),
    youtubeUploadChannel: $("#youtube-upload-channel"),
    youtubeTitle: $("#youtube-title"),
    youtubeDescription: $("#youtube-description"),
    youtubeGenerateMetadata: $("#youtube-generate-metadata"),
    youtubeMetadataError: $("#youtube-metadata-error"),
    youtubeVisibility: $("#youtube-visibility"),
    youtubeVisibilityHelp: $("#youtube-visibility-help"),
    youtubeMadeForKidsYes: $("#youtube-made-for-kids-yes"),
    youtubeMadeForKidsNo: $("#youtube-made-for-kids-no"),
    youtubeSynthetic: $("#youtube-synthetic"),
    youtubeUploadStatus: $("#youtube-upload-status"),
    youtubeUploadStatusLabel: $("#youtube-upload-status-label"),
    youtubeUploadProgressLabel: $("#youtube-upload-progress-label"),
    youtubeUploadProgressWrap: $("#youtube-upload-progress-wrap"),
    youtubeUploadProgressBar: $("#youtube-upload-progress-bar"),
    youtubeUploadError: $("#youtube-upload-error"),
    youtubeUploadNote: $("#youtube-upload-note"),
    youtubeUploadSubmit: $("#youtube-upload-submit"),
    youtubeUploadCancel: $("#youtube-upload-cancel"),
    youtubeUploadAbandon: $("#youtube-upload-abandon"),
    youtubeUploadRetry: $("#youtube-upload-retry"),
    youtubeDuplicateRecovery: $("#youtube-duplicate-recovery"),
    youtubeRecoveryConfirmControls: $("#youtube-recovery-confirm-controls"),
    youtubeDuplicateConfirm: $("#youtube-duplicate-confirm"),
    youtubeUploadRestart: $("#youtube-upload-restart"),
    youtubeOpenVideo: $("#youtube-open-video"),
    youtubeOpenStudio: $("#youtube-open-studio"),
    toast: $("#toast")
  };

  const viewMeta = {
    overview: { title: "Workspace", kicker: "Studio" },
    generate: { title: "Create", kicker: "Production" },
    history: { title: "History", kicker: "Library" },
    vault: { title: "Vault", kicker: "Private library" },
    settings: { title: "Settings", kicker: "Workspace" }
  };

  const promptCategories = ["Kid Animation", "Horror Story", "Nature", "Seduction", "Mature Content", "Soft Corn"];

  const state = {
    models: [],
    modalAccounts: [],
    modalAccountsActiveID: "",
    modalAccountsRequestID: 0,
    modelRequestID: 0,
    modelsLoading: false,
    modelLoadError: "",
    modelCredentialsMissing: false,
    openRouterConfigured: null,
    openRouterConfigurationRevision: 0,
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
    historyCards: new Map(),
    historyChrome: new Map(),
    recentRows: new Map(),
    recentEmpty: null,
    currentGenerationID: "",
    displayedGenerationID: "",
    currentGenerationRecord: null,
    displayedGenerationStatus: "",
    mode: "project",
    currentView: "",
    submissionPending: false,
    projectSubmissionPending: false,
    projectSubmissionFailed: false,
    projectRecoveryAttemptedID: "",
    projectRecoveryPromise: null,
    projectListRequestID: 0,
    pendingProjectSubmission: null,
    pendingProjectFormEdited: false,
    authGeneration: 0,
    randomPromptPending: false,
    promptEditRevisions: { project: 0, single: 0 },
    categoryEditRevision: 0,
    singleSubmissionPending: false,
    singleSubmissionFailed: false,
    projects: [],
    currentProjectID: "",
    displayedProjectID: "",
    displayedProjectStatus: "",
    currentProjectRecord: null,
    projectStoryRevision: null,
    projectTraceFetches: new Map(),
    projectTraceRequestEpoch: 0,
    projectRawTraceOpenKey: "",
    jobEventSource: null,
    jobStreamHasOpened: false,
    jobStreamDisconnected: false,
    jobStreamAuthCheckPending: false,
    jobStreamUnavailable: false,
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
    vaultPlayerItemKey: "",
    youtubeStatus: null,
    youtubeStatusRequestID: 0,
    youtubeConfigDirty: false,
    youtubeSource: null,
    youtubeUpload: null,
    youtubeReviewedChannelID: "",
    youtubeSubmitting: false,
    youtubeAbandoning: false,
    youtubeRestarting: false,
    youtubeDialogReady: false,
    youtubeDialogEpoch: 0,
    youtubeDialogController: null,
    youtubePollTimer: 0,
    youtubeSourcePreviewURL: "",
    youtubeRestoreFocus: null
  };

  const VIDEO_PROVIDER_NAMES = Object.freeze({ modal: "Modal", openrouter: "OpenRouter" });
  const PENDING_PROJECT_SUBMISSION_KEY = "framevault.pendingProjectSubmission";
  state.pendingProjectSubmission = readPendingProjectSubmission();
  restorePendingProjectForm();

  function readPendingProjectSubmission() {
    try {
      const raw = window.sessionStorage?.getItem(PENDING_PROJECT_SUBMISSION_KEY);
      if (!raw) return null;
      const pending = JSON.parse(raw);
      if (!pending || typeof pending.request_id !== "string" || !pending.body
        || typeof pending.body.topic !== "string") return null;
      return pending;
    } catch (_error) {
      return null;
    }
  }

  function writePendingProjectSubmission(pending) {
    state.pendingProjectSubmission = pending;
    state.projectRecoveryAttemptedID = "";
    state.pendingProjectFormEdited = false;
    try {
      window.sessionStorage?.setItem(PENDING_PROJECT_SUBMISSION_KEY, JSON.stringify(pending));
    } catch (_error) {
      // The request still carries its idempotency key when session storage is unavailable.
    }
  }

  function clearPendingProjectSubmission(requestID) {
    if (state.pendingProjectSubmission?.request_id !== requestID) return;
    state.pendingProjectSubmission = null;
    state.projectRecoveryAttemptedID = "";
    state.pendingProjectFormEdited = false;
    try {
      window.sessionStorage?.removeItem(PENDING_PROJECT_SUBMISSION_KEY);
    } catch (_error) {
      // Ignore unavailable session storage; the in-memory request is already settled.
    }
  }

  function newProjectRequestID() {
    try {
      if (typeof window.crypto?.randomUUID === "function") return window.crypto.randomUUID();
    } catch (_error) {
      // Use the local fallback if browser crypto is unavailable.
    }
    return `project-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  }

  function sameProjectRequestBody(left, right) {
    return Boolean(left && right)
      && String(left.topic || "") === String(right.topic || "")
      && String(left.category || "") === String(right.category || "")
      && String(left.model || "") === String(right.model || "")
      && String(left.modal_account_id || "") === String(right.modal_account_id || "");
  }

  function restorePendingProjectForm() {
    const pending = state.pendingProjectSubmission;
    if (!pending || state.pendingProjectFormEdited) return;
    const body = pending.body || {};
    if (!elements.projectTopic.value.trim()) elements.projectTopic.value = String(body.topic || "");
    const categoryIndex = promptCategories.indexOf(String(body.category || ""));
    if (categoryIndex >= 0) {
      elements.promptCategory.value = String(categoryIndex);
      updatePromptCategory();
    }
    if (body.model && state.models.some((model) => model.id === body.model)) {
      elements.model.value = body.model;
      renderModelPicker();
      updateModelOptions();
    }
  }

  function markPendingProjectFormEdited() {
    if (state.pendingProjectSubmission) state.pendingProjectFormEdited = true;
  }

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
    const authGeneration = state.authGeneration;
    const requestID = ++state.modalAccountsRequestID;
    const isCurrentRequest = () => state.authenticated && !state.mustChangePassword && state.authGeneration === authGeneration
      && requestID === state.modalAccountsRequestID;
    try {
      const payload = await request("/api/modal-accounts");
      if (!isCurrentRequest()) return;
      state.modalAccounts = Array.isArray(payload && payload.accounts) ? payload.accounts : [];
      state.modalAccountsActiveID = String(payload && payload.active_id || "");
      renderModalAccountList();
      await refreshModalAccountSelectors();
      if (!isCurrentRequest()) return;
      const configured = state.modalAccounts.filter((account) => account.configured).length;
      elements.modalKeyState.className = `badge ${configured ? "completed" : "neutral"}`;
      elements.modalKeyState.textContent = `${configured} configured`;
    } catch (error) {
      if (!isCurrentRequest()) return;
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
      if (state.pendingProjectSubmission
        && String(projectValue(snapshot, "request_id", "requestID") || "") === state.pendingProjectSubmission.request_id) {
        if (!state.projects.some((item) => projectID(item) === id)) state.projects.unshift(snapshot);
        void recoverPendingProjectSubmission();
      }
    }

    renderRecent();
    renderHistory();
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

  function renderConnectionState() {
    let status = "paused";
    if (!state.authenticated) status = "signed-out";
    else if (state.mustChangePassword) status = "locked";
    else if (window.navigator?.onLine === false) status = "offline";
    else if (typeof window.EventSource !== "function" || state.jobStreamUnavailable) status = "unavailable";
    else if (state.jobEventSource && state.jobEventSource.readyState !== 2) {
      status = state.jobStreamDisconnected ? "reconnecting" : state.jobStreamHasOpened ? "live" : "connecting";
    }
    const messages = {
      "signed-out": ["Sign in to connect", "Live updates start after you sign in."],
      locked: ["Workspace locked", "Change your temporary password to enable live updates."],
      offline: ["Offline · updates paused", "Your jobs may continue running. Updates resume when your connection returns."],
      unavailable: ["Live updates unavailable", "Use Refresh in History to check your jobs."],
      paused: ["Live updates paused", "Use Refresh in History to reconnect and check your jobs."],
      connecting: ["Connecting to workspace", "Waiting for the live update connection."],
      reconnecting: ["Reconnecting to workspace", "Your jobs may continue running. Waiting to reconnect for their latest status."],
      live: ["Live updates connected", "Job updates are connected to this workspace."]
    };
    elements.connectionState.dataset.state = status;
    elements.connectionState.textContent = messages[status][0];
    elements.connectionState.title = messages[status][1];
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
      state.jobStreamUnavailable = false;
      window.clearTimeout(state.terminalRefreshTimer);
      state.terminalRefreshTimer = 0;
      state.terminalRefreshKinds.clear();
    }
    renderConnectionState();
  }

  function startJobEventStream() {
    if (state.jobEventSource?.readyState === 2) stopJobEventStream();
    if (!state.authenticated || state.mustChangePassword || state.jobEventSource || typeof window.EventSource !== "function") {
      renderConnectionState();
      return;
    }
    state.jobStreamUnavailable = false;
    let source;
    try {
      source = new window.EventSource("/api/events", { withCredentials: true });
    } catch (_error) {
      state.jobStreamUnavailable = true;
      renderConnectionState();
      return;
    }
    state.jobEventSource = source;
    state.jobStreamDisconnected = state.jobStreamHasOpened;
    renderConnectionState();
    source.addEventListener("open", () => {
      if (state.jobEventSource !== source) return;
      state.jobStreamHasOpened = true;
      state.jobStreamDisconnected = false;
      state.jobStreamAuthCheckPending = false;
      renderConnectionState();
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
      renderConnectionState();
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
    elements.projectCostNote.hidden = !project;
    elements.singleOptions.hidden = project;
    elements.prompt.hidden = project;
    elements.prompt.closest(".field").hidden = project;
    updateCreatorActionLabels();
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

  function openCreator(mode) {
    if (!state.authenticated || state.mustChangePassword) return;
    setGenerationMode(mode);
    navigate("generate");
    if (state.videoProvider === "modal") void loadModels(false);
  }

  function updateCreatorActionLabels() {
    const project = state.mode === "project";
    if (!state.submissionPending) elements.generate.textContent = project ? "Generate 30-second story" : "Generate clip";
    if (!state.randomPromptPending) elements.randomPrompt.textContent = project ? "Suggest story idea" : "Suggest clip prompt";
  }

  function selectedPromptCategory() {
    return promptCategories[Number(elements.promptCategory.value)] || promptCategories[0];
  }

  function updatePromptCategory() {
    const category = selectedPromptCategory();
    elements.promptCategoryName.textContent = category;
    elements.promptCategory.removeAttribute("aria-valuetext");
  }

  async function generateRandomPrompt() {
    if (state.randomPromptPending || !state.authenticated || state.mustChangePassword) return;
    const mode = state.mode;
    const category = selectedPromptCategory();
    const field = mode === "project" ? elements.projectTopic : elements.prompt;
    const previousValue = field.value;
    const editRevision = state.promptEditRevisions[mode];
    const categoryRevision = state.categoryEditRevision;
    const focusOrigin = document.activeElement;
    const authGeneration = state.authGeneration;
    state.randomPromptPending = true;
    setButtonBusy(elements.randomPrompt, true, "Suggesting…");
    try {
      const payload = await request("/api/prompts/random", {
        method: "POST",
        body: JSON.stringify({ category, mode })
      });
      if (state.authGeneration !== authGeneration || !state.authenticated || state.mustChangePassword) return;
      const prompt = typeof payload?.prompt === "string" ? payload.prompt.trim() : "";
      if (!prompt) throw new APIError("No prompt was returned. Try again.", 502);
      if (field.value !== previousValue || state.promptEditRevisions[mode] !== editRevision
        || selectedPromptCategory() !== category || state.categoryEditRevision !== categoryRevision) {
        toast("Your edits were kept. Request another suggestion when you're ready.");
        return;
      }
      field.value = prompt;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      if (state.mode === mode && state.currentView === "generate" && document.activeElement === focusOrigin
        && (focusOrigin === elements.randomPrompt || focusOrigin === field)) field.focus();
      if (mode === "project") elements.projectTopicError.textContent = "";
      toast(mode === "project" ? "Story idea ready." : "Clip prompt ready.");
    } catch (error) {
      if (state.authGeneration === authGeneration && state.authenticated && error.status !== 401) toast(error.message, true);
    } finally {
      if (state.authGeneration === authGeneration) {
        state.randomPromptPending = false;
        setButtonBusy(elements.randomPrompt, false);
        updateCreatorActionLabels();
      }
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
    elements.createModeButtons.forEach((button) => {
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
    renderConnectionState();
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
    state.authGeneration += 1;
    state.submissionPending = false;
    state.projectSubmissionPending = false;
    state.projectSubmissionFailed = false;
    state.singleSubmissionPending = false;
    state.singleSubmissionFailed = false;
    state.randomPromptPending = false;
    state.projectRecoveryAttemptedID = "";
    state.projectRecoveryPromise = null;
    setButtonBusy(elements.generate, false);
    setButtonBusy(elements.randomPrompt, false);
    setButtonBusy($("button[type='submit']", elements.apiKeyForm), false);
    elements.apiKey.value = "";
    stopJobEventStream(true);
    invalidateProjectRawTraceRequests();
    closeYouTubeComposer(false);
    state.youtubeStatus = null;
    state.youtubeStatusRequestID += 1;
    state.youtubeConfigDirty = false;
    elements.youtubeClientSecret.value = "";
    resetVaultRestoreState();
    clearVaultClientState();
    clearGenerationDisplay();
    clearProjectDisplay();
    state.authenticated = false;
    state.openRouterConfigured = null;
    state.modelsLoading = false;
    state.modelLoadError = "";
    state.modelCredentialsMissing = false;
    state.modelRequestID++;
    elements.creatorProviderHelp.hidden = true;
    renderConnectionState();
    state.vaultConfigured = false;
    state.currentGenerationID = "";
    state.currentProjectID = "";
    state.currentGenerationRecord = null;
    state.currentProjectRecord = null;
    state.generations = [];
    state.projects = [];
    state.stats = null;
    state.historyHasMore = false;
    state.historyLoading = false;
    elements.refreshHistory.disabled = false;
    state.historyCards.clear();
    state.historyChrome.clear();
    state.recentRows.clear();
    state.recentEmpty = null;
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
    state.authGeneration += 1;
    state.randomPromptPending = false;
    setButtonBusy(elements.randomPrompt, false);
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
    if (state.currentView === "settings" && view !== "settings") elements.youtubeClientSecret.value = "";
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
    elements.generate.disabled = state.submissionPending || state.modelsLoading || !model
      || (state.videoProvider === "modal" && !selectedModalAccountID())
      || (state.mode === "project" && (!supportsProject(model) || state.openRouterConfigured === false));
    if (!state.submissionPending) {
      updateCreatorActionLabels();
    }
    renderCreatorProviderHelp();
  }

  function renderCreatorProviderHelp() {
    let message = "";
    let settingsNeeded = false;
    if (state.authenticated && !state.mustChangePassword) {
      if (state.modelsLoading) {
        message = "Loading available video models…";
      } else if (state.videoProvider === "modal" && !selectedModalAccountID()) {
        message = state.modalAccounts.length
          ? "Choose a production account to load its video models. Manage production accounts in Settings."
          : "Add a Modal production account in Settings to load its video models.";
        settingsNeeded = true;
      } else if (!state.models.length) {
        message = state.modelCredentialsMissing
          ? `Configure ${videoProviderName()} credentials in Settings to load video models.`
          : state.modelLoadError
            ? `Video models could not be loaded. ${state.modelLoadError} Review the provider in Settings and try again.`
            : "No video models are available. Review your provider in Settings and try again.";
        settingsNeeded = true;
      } else if (state.mode === "project" && state.openRouterConfigured === false) {
        message = "Add an OpenRouter API key in Settings for story planning. Stories require it with every video provider.";
        settingsNeeded = true;
      }
      if (message && !state.modelsLoading && state.mode === "project" && state.openRouterConfigured === false
        && !message.includes("story planning")) {
        message += " Add an OpenRouter API key there for story planning as well.";
      }
    }
    elements.creatorProviderHelp.hidden = !message;
    elements.creatorProviderMessage.textContent = message;
    elements.creatorOpenSettings.hidden = !settingsNeeded;
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
    const authGeneration = state.authGeneration;
    const requestID = ++state.modelRequestID;
    const requestedProvider = state.videoProvider;
    const requestedMode = state.mode;
    const modalAccountID = requestedProvider === "modal" ? selectedModalAccountID() : "";
    const isCurrentRequest = () => state.authenticated && !state.mustChangePassword && state.authGeneration === authGeneration
      && requestID === state.modelRequestID
      && requestedProvider === state.videoProvider
      && (requestedProvider !== "modal" || requestedMode === state.mode)
      && (requestedProvider !== "modal" || modalAccountID === selectedModalAccountID());
    state.modelsLoading = true;
    state.modelLoadError = "";
    state.modelCredentialsMissing = false;
    renderCreatorProviderHelp();
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
        state.modelsLoading = false;
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
      state.modelsLoading = false;
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
      const selected = (savedModel && (state.mode !== "project" || supportsProject(savedModel)) ? savedModel : null)
        || (state.mode === "project" ? state.models.find(supportsProject) : null)
        || savedModel
        || state.models[0];
      if (selected) elements.model.value = selected.id;
      restorePendingProjectForm();
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
      state.modelsLoading = false;
      state.modelLoadError = error.message;
      state.modelCredentialsMissing = error.status === 422;
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

  function emptyState(message, actionLabel = "", onAction = () => navigate("generate")) {
    const empty = make("div", { className: "empty" });
    empty.append(make("p", { text: message }));
    if (actionLabel) {
      const action = make("button", { className: "button secondary empty-action", type: "button", text: actionLabel });
      action.addEventListener("click", onAction);
      empty.append(action);
    }
    return empty;
  }
  function renderRecent() {
    const recent = [
      ...state.projects.map((record) => ({ kind: "project", record })),
      ...state.generations.map((record) => ({ kind: "single", record }))
    ].sort((left, right) => numericStat(projectValue(right.record, "created_at", "createdAt"))
      - numericStat(projectValue(left.record, "created_at", "createdAt"))).slice(0, 5);
    const keys = new Set(recent.map(({ kind, record }) => `${kind}:${kind === "project" ? projectID(record) : String(record.id || "")}`));
    state.recentRows.forEach((_value, key) => { if (!keys.has(key)) state.recentRows.delete(key); });
    if (!recent.length) {
      if (!state.recentEmpty) state.recentEmpty = emptyState("Your projects and clips will appear here.", "Create a video");
      reconcileChildren(elements.recentList, [state.recentEmpty]);
      return;
    }
    const rows = [];
    recent.forEach(({ kind, record }) => {
      const project = kind === "project";
      const id = project ? projectID(record) : String(record.id || "");
      const key = `${kind}:${id}`;
      let entry = state.recentRows.get(key);
      if (!entry) {
        const row = make("div", { className: "recent-item" });
        row.dataset.kind = kind;
        row.dataset.id = id;
        const main = make("div", { className: "recent-main" });
        const open = make("button", { className: "text-button recent-open", type: "button" });
        const model = make("span");
        const upload = make("button", { className: "button text-button recent-upload", type: "button", text: "Upload to YouTube" });
        const latestRecord = () => project ? state.projects.find((item) => projectID(item) === id)
          : state.generations.find((item) => String(item.id) === id);
        open.addEventListener("click", () => {
          const latest = latestRecord();
          if (!latest || !state.authenticated || state.mustChangePassword) return;
          openCreator(kind);
          if (project) renderProject(latest);
          else setStatusRecord(latest);
        });
        upload.addEventListener("click", () => {
          const latest = latestRecord();
          if (latest) void openYouTubeUpload(project ? projectYouTubeSource(latest) : generationYouTubeSource(latest));
        });
        main.append(open, model, upload);
        const date = make("span", { className: "meta" });
        const badge = statusBadge("queued");
        const cost = make("strong");
        row.append(main, date, badge, cost);
        entry = { row, open, model, upload, date, badge, cost, record: null };
        state.recentRows.set(key, entry);
      }
      if (entry.record !== record) {
        const title = String(projectValue(record, project ? "topic" : "prompt") || (project ? "Untitled project" : "Untitled clip"));
        const status = String(projectValue(record, "status") || "queued").toLowerCase();
        entry.open.textContent = title;
        entry.open.setAttribute("aria-label", `Open ${project ? "30-second project" : "single clip"}: ${title}`);
        entry.model.textContent = `${project ? "30-second project" : "Single clip"} · ${friendlyModel(projectValue(record, "model"))}`;
        entry.upload.hidden = status !== "completed" || !Boolean(projectValue(record, project ? "final_video_ready" : "video_ready"));
        entry.date.textContent = formatDate(projectValue(record, "created_at", "createdAt"));
        entry.badge.className = "badge " + statusClass(status);
        entry.badge.textContent = status.replaceAll("_", " ");
        entry.cost.textContent = project ? projectRecordedCost(record) : recordCost(record).value;
        entry.record = record;
      }
      rows.push(entry.row);
    });
    reconcileChildren(elements.recentList, rows);
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

  function reconcileChildren(parent, children) {
    const wanted = new Set(children);
    Array.from(parent.children).forEach((child) => {
      if (!wanted.has(child)) child.remove();
    });
    children.forEach((child, index) => {
      if (parent.children[index] !== child) parent.insertBefore(child, parent.children[index] || null);
    });
  }

  function retainedNode(cache, key, revision, create) {
    const previous = cache.get(key);
    if (previous && previous.revision === revision) return previous.node;
    const node = create();
    cache.set(key, { revision, node });
    return node;
  }

  function historyHeading(key, title, count, className = "history-section-heading") {
    return retainedNode(state.historyChrome, key, count, () => {
      const heading = make("div", { className });
      heading.append(make("h3", { text: title }), make("span", { className: "history-count", text: String(count) }));
      return heading;
    });
  }

  function historyFilters() {
    return {
      query: elements.historySearch.value.trim().toLowerCase(),
      status: elements.historyStatus.value || "all",
      format: elements.historyFormat.value || "all"
    };
  }

  function hasHistoryFilters(filters) {
    return Boolean(filters.query || filters.status !== "all" || filters.format !== "all");
  }

  function historyMatches(record, kind, filters) {
    if (filters.format !== "all" && filters.format !== kind) return false;
    const status = String(projectValue(record, "status") || "queued").toLowerCase();
    if (filters.status === "active" && isTerminalJobStatus(status)) return false;
    if (filters.status === "completed" && !["completed", "complete"].includes(status)) return false;
    if (filters.status === "failed" && !["failed", "error"].includes(status)) return false;
    if (!filters.query) return true;
    const searchable = ["topic", "prompt", "model", "category", "id"]
      .map((key) => String(projectValue(record, key) || "")).join(" ").toLowerCase();
    return searchable.includes(filters.query);
  }

  function clearHistoryFilters() {
    elements.historySearch.value = "";
    elements.historyStatus.value = "all";
    elements.historyFormat.value = "all";
    renderHistory();
    elements.historySearch.focus({ preventScroll: true });
  }

  function retryFailedHistoryPreviews() {
    state.historyCards.forEach((entry, key) => {
      if (entry.node.dataset.previewFailed === "true") state.historyCards.delete(key);
    });
  }

  function renderProjectHistory(projects) {
    const children = [];
    if (projects.length) {
      children.push(historyHeading("project-heading", "30-second projects", projects.length));
      const list = retainedNode(state.historyChrome, "project-list", true, () => make("div", { className: "project-history-list" }));
      const cards = [];
      projects.forEach((project, index) => {
        const key = `project:${projectID(project) || index}`;
        const item = retainedNode(state.historyCards, key, project, () => {
          const id = projectID(project);
          const status = String(projectValue(project, "status") || "queued");
          const videoReady = String(status).toLowerCase() === "completed" && Boolean(projectValue(project, "final_video_ready"));
          const videoURL = id ? `/api/projects/${encodeURIComponent(id)}/video` : "";
          const item = make("article", { className: "history-card" });
          let download;
          const topic = String(projectValue(project, "topic") || "30-second project").slice(0, 120);
          const preview = historyPreview(videoReady, videoURL, status, () => {
            if (videoReady && videoURL) item.dataset.previewFailed = "true";
            download?.remove();
          }, "Project video: " + topic);
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
          if (videoReady && videoURL) {
            const upload = make("button", { className: "button secondary", type: "button", text: "Upload to YouTube" });
            upload.addEventListener("click", () => openYouTubeUpload(projectYouTubeSource(project)));
            actions.append(upload);
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
          return item;
        });
        cards.push(item);
      });
      reconcileChildren(list, cards);
      children.push(list);
    }
    reconcileChildren(elements.projectHistory, children);
    elements.projectHistory.hidden = !projects.length;
  }

  function renderHistory() {
    const keys = new Set([
      ...state.projects.map((record, index) => `project:${projectID(record) || index}`),
      ...state.generations.map((record, index) => `single:${String(record.id || index)}`)
    ]);
    state.historyCards.forEach((_value, key) => { if (!keys.has(key)) state.historyCards.delete(key); });
    const filters = historyFilters();
    const filtered = hasHistoryFilters(filters);
    const projects = state.projects.filter((record) => historyMatches(record, "project", filters));
    const generations = state.generations.filter((record) => historyMatches(record, "single", filters));
    const loaded = state.projects.length + state.generations.length;
    const matched = projects.length + generations.length;
    elements.historyResultsSummary.textContent = `Showing ${matched} of ${loaded} loaded ${loaded === 1 ? "item" : "items"}: ${projects.length} ${projects.length === 1 ? "project" : "projects"}, ${generations.length} ${generations.length === 1 ? "clip" : "clips"}.`
      + (state.historyHasMore ? " Load more to include older clips." : "");
    elements.historyClearFilters.disabled = !filtered;
    renderProjectHistory(projects);
    const children = [];
    elements.historyGrid.hidden = filters.format === "project" && projects.length > 0;
    if (!matched) {
      elements.historyGrid.hidden = false;
      children.push(retainedNode(state.historyChrome, "history-empty", filtered, () => filtered
        ? emptyState("No loaded items match these filters.", "Clear filters", clearHistoryFilters)
        : emptyState("Your projects and clips will appear here.", "Create a video")));
    }

    if (generations.length) {
      children.push(historyHeading("single-heading", "Single clips", generations.length, "history-section-heading history-generation-heading"));
    }

    if (!generations.length && projects.length && filters.format !== "project" && state.historyHasMore) {
      children.push(retainedNode(state.historyChrome, "history-clips-empty", true, () => make("div", {
        className: "empty history-generation-empty",
        text: "No loaded clips match. Load more to include older clips."
      })));
    }

    generations.forEach((record, index) => {
      const key = `single:${String(record.id || index)}`;
      const card = retainedNode(state.historyCards, key, record, () => {
        const card = make("article", { className: "history-card" });
        const videoURL = `/video?id=${encodeURIComponent(record.id)}`;
        const videoReady = String(record.status || "").toLowerCase() === "completed" && Boolean(record.video_ready);
        let download;
        const previewLabel = record.prompt
          ? "Generated video: " + record.prompt.slice(0, 120)
          : "Generated video";
        const preview = historyPreview(videoReady, videoURL, record.status, () => {
          if (videoReady) card.dataset.previewFailed = "true";
          download?.remove();
        }, previewLabel);

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
          const upload = make("button", { className: "button secondary", text: "Upload to YouTube", type: "button" });
          upload.addEventListener("click", () => openYouTubeUpload(generationYouTubeSource(record)));
          actions.append(upload);
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
        return card;
      });
      children.push(card);
    });

    if (state.historyHasMore && filters.format !== "project") {
      const more = retainedNode(state.historyChrome, "history-more", true, () => {
        const container = make("div", { className: "empty" });
        const button = make("button", { className: "button secondary", text: "Load more", type: "button" });
        button.addEventListener("click", async () => {
          setButtonBusy(button, true, "Loading older clips…");
          await loadHistory(true, true);
          setButtonBusy(button, false);
        });
        container.append(button);
        return container;
      });
      setButtonBusy(more.children[0], state.historyLoading, "Loading older clips…");
      children.push(more);
    }
    reconcileChildren(elements.historyGrid, children);
  }


  async function loadHistory(notify = true, append = false) {
    if (!state.authenticated || state.mustChangePassword || state.historyLoading) return;
    const authGeneration = state.authGeneration;
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
      if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration) return;
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
    } catch (error) {
      if (notify && state.authenticated && state.authGeneration === authGeneration && error.status !== 401) toast(error.message, true);
    } finally {
      if (state.authGeneration === authGeneration) {
        state.historyLoading = false;
        elements.refreshHistory.disabled = false;
        renderHistory();
      }
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
    elements.singleYouTubeUpload.hidden = true;
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
    elements.projectYouTubeUpload.hidden = true;
    elements.projectStory.replaceChildren();
    state.projectStoryRevision = null;
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
      invalidateYouTubeSource(kind, id);
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
    elements.singleYouTubeUpload.hidden = true;

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
      elements.singleYouTubeUpload.hidden = false;
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
    elements.projectPipeline.hidden = !events.length;
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

  function renderProjectStory(project) {
    const value = projectValue(project, "story", "story_text", "storyText");
    const story = typeof value === "string" ? value.trim() : "";
    const id = projectID(project);
    if (state.projectStoryRevision?.id === id && state.projectStoryRevision.story === story) return;
    state.projectStoryRevision = { id, story };
    elements.projectStory.replaceChildren();
    elements.projectStory.hidden = !story;
    if (!story) return;
    elements.projectStory.append(make("strong", { text: "Story" }));
    const characters = Array.from(story);
    if (characters.length <= 600) {
      elements.projectStory.append(make("p", { text: story }));
    } else {
      elements.projectStory.append(make("p", { text: characters.slice(0, 300).join("") + "…" }));
      const details = make("details");
      details.append(make("summary", { text: "Read story" }), make("p", { text: story }));
      elements.projectStory.append(details);
    }
  }

  function renderProject(project, authoritative = false) {
    if (!project) return;
    const incomingID = projectID(project);
    const previousScenePrompts = incomingID === state.displayedProjectID
      ? $$(".scene-prompt-details", elements.projectScenes) : [];
    const openScenePrompts = new Map(previousScenePrompts.filter((details) => details.open).map((details) => [details.dataset.scene, details.children[1].textContent]));
    const focusedScenePrompt = previousScenePrompts.find((details) => details.children[0] === document.activeElement);
    let sceneFocusTarget = null;
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

    renderProjectStory(project);

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
      if (prompt) {
        const details = make("details", { className: "scene-prompt-details" });
        details.dataset.scene = String(number);
        details.open = openScenePrompts.get(String(number)) === String(prompt);
        const summary = make("summary", { text: "Scene prompt" });
        if (focusedScenePrompt?.dataset.scene === String(number) && focusedScenePrompt.children[1].textContent === String(prompt)) {
          sceneFocusTarget = summary;
        }
        details.append(summary, make("p", { className: "scene-prompt", text: prompt }));
        card.append(details);
      }
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
      elements.projectYouTubeUpload.hidden = false;
    } else {
      if (elements.projectVideo.getAttribute("src")) {
        elements.projectVideo.pause();
        elements.projectVideo.removeAttribute("src");
        elements.projectVideo.load();
      }
      elements.projectDownload.removeAttribute("href");
      elements.projectDownload.removeAttribute("download");
      elements.projectYouTubeUpload.hidden = true;
    }
    syncGenerationModeDisplay();
    if (sceneFocusTarget) sceneFocusTarget.focus({ preventScroll: true });
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

  function projectListFromPayload(payload) {
    return Array.isArray(payload) ? payload : (Array.isArray(payload && payload.projects) ? payload.projects : []);
  }

  function applyRecoveredProjectSubmission(id, pending, project, authGeneration) {
    if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration
      || state.pendingProjectSubmission !== pending) return { status: "stale" };
    const projectIDValue = projectID(project);
    if (!projectIDValue) return { status: "missing" };
    const existing = state.projects.find((item) => projectID(item) === projectIDValue);
    project = mergeProjectRawTrace(existing, project);
    if (!existing) state.projects.unshift(project);
    else state.projects = state.projects.map((item) => projectID(item) === projectIDValue ? project : item);
    const formEdited = state.pendingProjectFormEdited;
    clearPendingProjectSubmission(id);
    state.projectSubmissionPending = false;
    state.projectSubmissionFailed = false;
    if (!formEdited && elements.projectTopic.value.trim() === String(pending.body.topic || "").trim()) {
      elements.projectTopic.value = "";
      elements.projectTopicError.textContent = "";
    }
    renderProject(project, true);
    toast("Project submission restored after a connection interruption.");
    return { status: "recovered", project };
  }

  async function recoverPendingProjectSubmission({ requestID = "", force = false, notifyUncertain = false } = {}) {
    const pending = state.pendingProjectSubmission;
    const id = requestID || (pending && pending.request_id) || "";
    if (!pending || !id || pending.request_id !== id) return { status: "none" };
    if (!state.authenticated || state.mustChangePassword) return { status: "stale" };
    const listedProject = state.projects.find((item) => String(projectValue(item, "request_id", "requestID") || "") === id);
    const authGeneration = state.authGeneration;
    if (listedProject) return applyRecoveredProjectSubmission(id, pending, listedProject, authGeneration);
    if (!force && state.projectRecoveryAttemptedID === id) return { status: "checked" };
    if (state.projectRecoveryPromise && state.projectRecoveryPromise.requestID === id) {
      return state.projectRecoveryPromise.promise;
    }

    const promise = (async () => {
      try {
        const payload = await request(`/api/projects?request_id=${encodeURIComponent(id)}`);
        if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration
          || state.pendingProjectSubmission !== pending) return { status: "stale" };
        let project = projectListFromPayload(payload).find((item) => String(projectValue(item, "request_id", "requestID") || "") === id)
          || state.projects.find((item) => String(projectValue(item, "request_id", "requestID") || "") === id);
        if (project) {
          return applyRecoveredProjectSubmission(id, pending, project, authGeneration);
        }

        state.projectRecoveryAttemptedID = id;
        if (!elements.projectTopic.value.trim()) elements.projectTopic.value = String(pending.body.topic || "");
        restorePendingProjectForm();
        if (notifyUncertain) {
          toast("The project is not confirmed yet. Your topic is saved; retrying the same request is safe.", true);
        }
        return { status: "missing" };
      } catch (error) {
        if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration
          || state.pendingProjectSubmission !== pending) return { status: "stale" };
        const listed = state.projects.find((item) => String(projectValue(item, "request_id", "requestID") || "") === id);
        if (listed) return applyRecoveredProjectSubmission(id, pending, listed, authGeneration);
        if (!elements.projectTopic.value.trim()) elements.projectTopic.value = String(pending.body.topic || "");
        restorePendingProjectForm();
        if (notifyUncertain && error.status !== 401) {
          toast("Could not check whether the project was saved. Your topic is saved; retrying the same request is safe.", true);
        }
        return { status: "unavailable", error };
      }
    })();
    const recovery = { requestID: id, promise };
    state.projectRecoveryPromise = recovery;
    try {
      return await promise;
    } finally {
      if (state.projectRecoveryPromise === recovery) state.projectRecoveryPromise = null;
    }
  }

  async function loadProjects(notify = true, restoreSelection = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    const authGeneration = state.authGeneration;
    const listRequestID = ++state.projectListRequestID;
    const isCurrentListRequest = () => state.projectListRequestID === listRequestID;
    try {
      const selectedIDAtRequestStart = state.currentProjectID || state.displayedProjectID;
      const selectedAtRequestStart = state.currentProjectRecord
        || state.projects.find((project) => projectID(project) === selectedIDAtRequestStart);
      const payload = await request("/api/projects");
      if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration || !isCurrentListRequest()) return;
      const projects = projectListFromPayload(payload);
      const selectedIDBeforeRefresh = state.currentProjectID || state.displayedProjectID;
      const selectedBeforeRefresh = state.currentProjectRecord
        || state.projects.find((project) => projectID(project) === selectedIDBeforeRefresh)
        || (selectedIDBeforeRefresh === selectedIDAtRequestStart ? selectedAtRequestStart : null);
      state.projects = preserveNewerSnapshots(
        projects,
        state.projects,
        state.currentProjectRecord,
        "project",
        projectID
      );

      if (selectedIDBeforeRefresh && selectedBeforeRefresh
        && !state.projects.some((project) => projectID(project) === selectedIDBeforeRefresh)) {
        try {
          const detailPayload = await request(`/api/projects/${encodeURIComponent(selectedIDBeforeRefresh)}`);
          if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration || !isCurrentListRequest()) return;
          const detail = detailPayload && detailPayload.project ? detailPayload.project : detailPayload;
          if (projectID(detail) === selectedIDBeforeRefresh
            && (state.currentProjectID === selectedIDBeforeRefresh || state.displayedProjectID === selectedIDBeforeRefresh)
            && !state.projects.some((project) => projectID(project) === selectedIDBeforeRefresh)) {
            state.projects.unshift(mergeProjectRawTrace(selectedBeforeRefresh, detail));
          }
        } catch (error) {
          if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration || !isCurrentListRequest()) return;
          if (error.status === 404) {
            state.projects = state.projects.filter((project) => projectID(project) !== selectedIDBeforeRefresh);
            if (state.currentProjectID === selectedIDBeforeRefresh || state.displayedProjectID === selectedIDBeforeRefresh) {
              clearProjectDisplay(selectedIDBeforeRefresh);
            }
          } else if ((state.currentProjectID === selectedIDBeforeRefresh || state.displayedProjectID === selectedIDBeforeRefresh)
            && !state.projects.some((project) => projectID(project) === selectedIDBeforeRefresh)) {
            state.projects.unshift(selectedBeforeRefresh);
          }
        }
      }

      const hadSelection = Boolean(state.currentProjectID || state.displayedProjectID);
      if (!state.currentProjectID && state.displayedProjectID
        && state.projects.some((project) => projectID(project) === state.displayedProjectID)) {
        state.currentProjectID = state.displayedProjectID;
      }
      const current = state.projects.find((project) => projectID(project) === state.currentProjectID);
      if (!current && state.currentProjectID) clearProjectDisplay(state.currentProjectID);
      if (!state.currentProjectID && restoreSelection && !hadSelection && !state.submissionPending) {
        const active = state.projects.find((project) => !isTerminalJobStatus(projectValue(project, "status")));
        // On a fresh page load, restore an in-flight job first. If all jobs are
        // terminal, show the newest saved result so failures and errors remain
        // visible after refresh without changing an existing selection.
        const selected = active || state.projects[0];
        state.currentProjectID = selected ? projectID(selected) : "";
      }
      const selectedProject = state.projects.find((project) => projectID(project) === state.currentProjectID);
      if (selectedProject) state.currentProjectRecord = selectedProject;
      setGenerationMode(state.mode);
      if (state.currentProjectID && state.mode === "project") {
        const active = state.projects.find((project) => projectID(project) === state.currentProjectID);
        if (active) renderProject(active);
      }
      renderHistory();
      renderRecent();
      if (restoreSelection && !state.submissionPending && state.pendingProjectSubmission) {
        await recoverPendingProjectSubmission({ notifyUncertain: true });
      }
    } catch (error) {
      if (notify && state.authenticated && state.authGeneration === authGeneration && isCurrentListRequest()
        && error.status !== 401 && error.status !== 404) {
        toast(error.message, true);
      }
    }
  }

  async function submitProject() {
    if (state.submissionPending || state.modelsLoading || !state.authenticated) return;
    if (state.mustChangePassword) {
      navigate("settings");
      return;
    }
    if (state.openRouterConfigured === false) {
      renderCreatorProviderHelp();
      elements.creatorOpenSettings.focus({ preventScroll: true });
      return;
    }
    const submittedValue = elements.projectTopic.value;
    const submittedRevision = state.promptEditRevisions.project;
    const topic = submittedValue.trim();
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
    const currentRequestBody = { topic, category: selectedPromptCategory(), model: elements.model.value };
    if (state.videoProvider === "modal") {
      const accountID = selectedModalAccountID();
      if (!accountID) {
        toast("Choose a Modal account for this submission.", true);
        return;
      }
      currentRequestBody.modal_account_id = accountID;
    }
    const previousPending = state.pendingProjectSubmission;
    const samePendingBody = previousPending && sameProjectRequestBody(previousPending.body, currentRequestBody);
    const restorePendingBody = previousPending && !state.pendingProjectFormEdited
      && String(previousPending.body.topic || "").trim() === topic;
    const reusePending = Boolean(samePendingBody || restorePendingBody);
    const requestBody = restorePendingBody && !samePendingBody
      ? { ...previousPending.body }
      : currentRequestBody;
    clearProjectDisplay();
    const authGeneration = state.authGeneration;
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
    const requestID = reusePending && sameProjectRequestBody(previousPending.body, requestBody)
      ? previousPending.request_id
      : newProjectRequestID();
    writePendingProjectSubmission({ request_id: requestID, body: { ...requestBody } });
    requestBody.request_id = requestID;
    try {
      const payload = await request("/api/projects", { method: "POST", body: JSON.stringify(requestBody) });
      if (!state.authenticated || state.authGeneration !== authGeneration
        || state.pendingProjectSubmission?.request_id !== requestID) return;
      const project = payload && payload.project ? payload.project : payload;
      const id = projectID(project);
      if (!id) throw new APIError("Project was created without an id.", 500);
      clearPendingProjectSubmission(requestID);
      state.projectSubmissionPending = false;
      state.projectSubmissionFailed = false;
      renderProject(project, true);
      if (elements.projectTopic.value === submittedValue && state.promptEditRevisions.project === submittedRevision) {
        elements.projectTopic.value = "";
        elements.projectTopicError.textContent = "";
      }
      toast("Project submitted. Script generation has started.");
      loadProjects(false);
    } catch (error) {
      if (!state.authenticated || state.authGeneration !== authGeneration
        || state.pendingProjectSubmission?.request_id !== requestID) return;
      const status = Number(error && error.status);
      const outcomeMayBeUnknown = error.status !== 401
        && (!Number.isFinite(status) || status === 408 || status >= 500);
      if (outcomeMayBeUnknown) {
        const recovered = await recoverPendingProjectSubmission({ requestID, force: true });
        if (recovered.status === "recovered") return;
        if (!state.authenticated || state.authGeneration !== authGeneration
          || state.pendingProjectSubmission?.request_id !== requestID) return;
      } else {
        clearPendingProjectSubmission(requestID);
      }

      state.projectSubmissionPending = false;
      state.projectSubmissionFailed = true;
      elements.projectError.hidden = false;
      elements.projectError.textContent = outcomeMayBeUnknown
        ? `${error.message} The project may still be running. Retrying this same request is safe.`
        : error.message;
      elements.statusBadge.className = "badge failed";
      elements.statusBadge.textContent = "Failed";
      syncGenerationModeDisplay();
      if (error.status !== 401) toast(elements.projectError.textContent, true);
    } finally {
      if (state.authGeneration === authGeneration) {
        state.submissionPending = false;
        setButtonBusy(elements.generate, false);
        updateGenerateAvailability();
        syncGenerationModeDisplay();
      }
    }
  }

  async function submitGeneration(event) {
    event.preventDefault();
    if (state.submissionPending || state.modelsLoading || !state.authenticated) return;
    if (state.mode === "project") {
      await submitProject();
      return;
    }
    if (state.mustChangePassword) {
      navigate("settings");
      return;
    }
    const submittedValue = elements.prompt.value;
    const submittedRevision = state.promptEditRevisions.single;
    const authGeneration = state.authGeneration;
    const prompt = submittedValue.trim();
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
      if (!state.authenticated || state.authGeneration !== authGeneration) return;
      state.singleSubmissionPending = false;
      state.singleSubmissionFailed = false;
      setStatusRecord(record);
      if (elements.prompt.value === submittedValue && state.promptEditRevisions.single === submittedRevision) {
        elements.prompt.value = "";
        elements.promptCount.textContent = "0 / 4000";
        elements.promptError.textContent = "";
      }
      updateModelOptions();
      toast("Generation submitted.");
      loadHistory(false);
    } catch (error) {
      if (!state.authenticated || state.authGeneration !== authGeneration) return;
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
      if (state.authGeneration === authGeneration) {
        state.submissionPending = false;
        setButtonBusy(elements.generate, false);
        updateGenerateAvailability();
        syncGenerationModeDisplay();
      }
    }
  }

  function renderProviderSettings(settings, preserveOpenRouterConfiguration = false) {
    state.videoProvider = settings.video_provider === "modal" ? "modal" : "openrouter";
    elements.videoProvider.value = state.videoProvider;
    elements.modalSettings.hidden = false;
    updateModalAccountSelectors();
    elements.videoProviderState.className = "badge completed";
    elements.videoProviderState.textContent = `Active: ${videoProviderName()}`;
    elements.modalBaseURL.value = settings.modal_video_base_url || "";
    if (!preserveOpenRouterConfiguration) {
      state.openRouterConfigured = typeof settings.openrouter_api_key_configured === "boolean" ? settings.openrouter_api_key_configured : null;
    }
    const openRouterConfigured = state.openRouterConfigured === true;
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
    updateGenerateAvailability();
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
    const authGeneration = state.authGeneration;
    const openRouterConfigurationRevision = state.openRouterConfigurationRevision;
    try {
      const settings = await request("/api/settings");
      if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration) return;
      // Keep a verified key save newer than this read; unrelated settings still apply.
      renderProviderSettings(settings || {}, state.openRouterConfigurationRevision !== openRouterConfigurationRevision);
      await Promise.all([loadModalAccounts(notify), loadYouTubeStatus(notify)]);
    } catch (error) {
      if (!state.authenticated || state.mustChangePassword || state.authGeneration !== authGeneration) return;
      elements.keyState.className = "badge failed";
      elements.keyState.textContent = "Unavailable";
      if (notify && error.status !== 401) toast(error.message, true);
    }
  }

  function generationYouTubeSource(record) {
    const id = String(record && record.id || "");
    const prompt = String(record && record.prompt || "");
    return {
      kind: "generation",
      id,
      label: prompt || "Generated video",
      titleSuggestion: prompt,
      descriptionSuggestion: prompt,
      videoURL: `/video?id=${encodeURIComponent(id)}`
    };
  }

  function projectYouTubeSource(project) {
    const id = projectID(project);
    const topic = String(projectValue(project, "topic") || "");
    const story = projectValue(project, "story", "description", "script", "narrative");
    return {
      kind: "project",
      id,
      label: topic || "30-second project",
      titleSuggestion: topic,
      descriptionSuggestion: typeof story === "string" ? story : "",
      videoURL: `/api/projects/${encodeURIComponent(id)}/video`
    };
  }

  function vaultYouTubeSource(item) {
    return {
      kind: String(item && item.kind || ""),
      id: String(item && item.id || ""),
      label: String(item && item.title || "Vault video"),
      titleSuggestion: String(item && item.title || ""),
      descriptionSuggestion: "",
      isVault: true,
      vaultItem: item
    };
  }

  function youtubeAbortError() {
    const error = new Error("Upload dialog is no longer active");
    error.name = "AbortError";
    return error;
  }

  function isCurrentYouTubeDialog(epoch, source = state.youtubeSource) {
    if (epoch !== state.youtubeDialogEpoch || !source || source !== state.youtubeSource
      || !elements.youtubeDialog.open || !state.authenticated) return false;
    if (source.isVault && (state.vaultLocking || !state.vaultToken
      || source.vaultToken !== state.vaultToken || source.vaultGeneration !== state.vaultGeneration)) return false;
    return true;
  }

  function clearYouTubePreview() {
    elements.youtubePreview.pause();
    elements.youtubePreview.removeAttribute("src");
    elements.youtubePreview.load();
    if (state.youtubeSourcePreviewURL) URL.revokeObjectURL(state.youtubeSourcePreviewURL);
    state.youtubeSourcePreviewURL = "";
  }

  function resetYouTubeComposerFields() {
    [elements.youtubeGenerateMetadata, elements.youtubeUploadSubmit, elements.youtubeUploadCancel,
      elements.youtubeUploadAbandon, elements.youtubeUploadRetry, elements.youtubeUploadRestart]
      .forEach((button) => setButtonBusy(button, false));
    elements.youtubeTitle.value = "";
    elements.youtubeDescription.value = "";
    elements.youtubeVisibility.value = "private";
    elements.youtubeVisibility.disabled = false;
    elements.youtubeVisibilityHelp.textContent = "Private is selected by default. Some Google API projects can upload only privately until audited.";
    elements.youtubeMadeForKidsYes.checked = false;
    elements.youtubeMadeForKidsNo.checked = false;
    elements.youtubeSynthetic.checked = true;
    elements.youtubeUploadStatus.hidden = true;
    elements.youtubeUploadError.hidden = true;
    elements.youtubeUploadNote.hidden = true;
    elements.youtubeMetadataError.hidden = true;
    elements.youtubeUploadSubmit.hidden = false;
    elements.youtubeUploadSubmit.disabled = false;
    elements.youtubeUploadSubmit.textContent = "Upload video";
    elements.youtubeGenerateMetadata.textContent = "Generate title and description";
    elements.youtubeUploadCancel.hidden = true;
    elements.youtubeUploadCancel.disabled = false;
    elements.youtubeUploadCancel.textContent = "Cancel upload";
    elements.youtubeUploadAbandon.hidden = true;
    elements.youtubeUploadAbandon.disabled = false;
    elements.youtubeUploadAbandon.textContent = "Abandon upload";
    elements.youtubeUploadRetry.hidden = true;
    elements.youtubeDuplicateRecovery.hidden = true;
    elements.youtubeRecoveryConfirmControls.hidden = false;
    elements.youtubeDuplicateConfirm.checked = false;
    elements.youtubeDuplicateConfirm.disabled = false;
    elements.youtubeUploadRestart.disabled = true;
    elements.youtubeUploadRestart.textContent = "Start upload again";
    elements.youtubeOpenVideo.hidden = true;
    elements.youtubeOpenVideo.removeAttribute("href");
    elements.youtubeOpenStudio.hidden = true;
    elements.youtubeUploadProgressWrap.hidden = true;
    elements.youtubeUploadProgressBar.style.width = "0%";
    elements.youtubeUploadProgressBar.classList.remove("indeterminate");
    elements.youtubeUploadProgressLabel.textContent = "";
    elements.youtubeUploadForm.querySelectorAll?.("input, textarea, select, button").forEach((control) => {
      control.disabled = false;
    });
  }

  function setYouTubeDialogReady(ready) {
    state.youtubeDialogReady = Boolean(ready);
    const disabled = !state.youtubeDialogReady;
    [elements.youtubeTitle, elements.youtubeDescription, elements.youtubeVisibility,
      elements.youtubeMadeForKidsYes, elements.youtubeMadeForKidsNo, elements.youtubeSynthetic,
      elements.youtubeGenerateMetadata].forEach((field) => { field.disabled = disabled; });
    elements.youtubeUploadSubmit.disabled = disabled;
    elements.youtubeUploadRetry.disabled = disabled;
    elements.youtubeUploadAbandon.disabled = disabled;
    elements.youtubeDuplicateConfirm.disabled = disabled;
    elements.youtubeUploadRestart.disabled = disabled || !elements.youtubeDuplicateConfirm.checked;
  }

  function renderYouTubeSettings(status) {
    const configured = Boolean(status && status.configured);
    const connected = Boolean(status && status.connected);
    const channel = status && status.channel && typeof status.channel === "object" ? status.channel : null;
    const channelName = String(channel && (channel.title || channel.name || channel.id) || "");
    elements.youtubeChannelState.className = `badge ${connected ? "completed" : configured ? "neutral" : "neutral"}`;
    elements.youtubeChannelState.textContent = connected ? "Connected" : configured ? "Ready to connect" : "Not configured";
    elements.youtubeChannelSummary.hidden = !connected;
    elements.youtubeChannelName.textContent = channelName;
    elements.youtubeConnect.hidden = !configured || connected;
    elements.youtubeRedirectURI.value = String(status && status.redirect_uri || "");
    elements.youtubeSecretHelp.textContent = status && status.has_client_secret
      ? "A client secret is saved and never sent back to the browser. Leave this blank to keep it."
      : "The secret is write-only and will never be shown again after saving.";
    if (!state.youtubeConfigDirty) {
      elements.youtubeClientID.value = String(status && status.client_id || "");
      elements.youtubeBaseURL.value = String(status && status.base_url || "");
      elements.youtubeClientSecret.value = "";
    }
    if (state.youtubeSource && elements.youtubeDialog.open) {
      const reviewedChannelID = state.youtubeReviewedChannelID;
      const currentChannelID = String(channel && channel.id || "");
      if (reviewedChannelID && currentChannelID !== reviewedChannelID) {
        closeYouTubeComposer(false);
        toast("The connected YouTube channel changed. Reopen the upload to review it again.", true);
        return;
      }
      renderYouTubeComposerConnection();
    }
  }

  async function loadYouTubeStatus(notify = false, controller = null, dialogEpoch = 0) {
    if (!state.authenticated || state.mustChangePassword) return null;
    const authGeneration = state.authGeneration;
    const requestID = ++state.youtubeStatusRequestID;
    const isCurrentRequest = () => state.authenticated && !state.mustChangePassword && state.authGeneration === authGeneration
      && requestID === state.youtubeStatusRequestID;
    try {
      const options = controller ? { signal: controller.signal } : {};
      const status = await request("/api/youtube/status", options);
      if (!isCurrentRequest()) return null;
      if (dialogEpoch && !isCurrentYouTubeDialog(dialogEpoch)) return null;
      state.youtubeStatus = status || { configured: false, connected: false };
      renderYouTubeSettings(state.youtubeStatus);
      return state.youtubeStatus;
    } catch (error) {
      if (!isCurrentRequest() || error.name === "AbortError" || error.status === 401) return null;
      if (notify) toast(error.message, true);
      if (!dialogEpoch) {
        elements.youtubeChannelState.className = "badge failed";
        elements.youtubeChannelState.textContent = "Unavailable";
      } else if (isCurrentYouTubeDialog(dialogEpoch)) {
        showYouTubeUploadFeedback("YouTube connection status could not be loaded.", error.message);
      }
      return null;
    }
  }

  async function saveYouTubeConfig(event) {
    event.preventDefault();
    if (state.mustChangePassword) return;
    const clientID = elements.youtubeClientID.value.trim();
    const clientSecret = elements.youtubeClientSecret.value;
    const baseURLValue = elements.youtubeBaseURL.value.trim();
    let baseURL;
    try {
      baseURL = new URL(baseURLValue);
    } catch {
      baseURL = null;
    }
    const normalizedInput = baseURLValue.replace(/\/+$/, "");
    const localHost = baseURL && ["localhost", "127.0.0.1", "[::1]", "::1"].includes(baseURL.hostname.toLowerCase());
    const allowedProtocol = baseURL && (baseURL.protocol === "https:" || (baseURL.protocol === "http:" && localHost));
    const normalizedBaseURL = baseURL && baseURL.origin === normalizedInput && allowedProtocol ? baseURL.origin : "";
    if (!clientID) {
      elements.youtubeConfigError.textContent = "Enter a Google OAuth client ID.";
      elements.youtubeConfigError.hidden = false;
      elements.youtubeClientID.focus();
      return;
    }
    if (!normalizedBaseURL) {
      elements.youtubeConfigError.textContent = "Enter the public HTTPS origin. HTTP is allowed for localhost.";
      elements.youtubeConfigError.hidden = false;
      elements.youtubeBaseURL.focus();
      return;
    }
    if (!clientSecret.trim() && !(state.youtubeStatus && state.youtubeStatus.has_client_secret)) {
      elements.youtubeConfigError.textContent = "Enter the Google OAuth client secret.";
      elements.youtubeConfigError.hidden = false;
      elements.youtubeClientSecret.focus();
      return;
    }
    elements.youtubeConfigError.hidden = true;
    setButtonBusy(elements.youtubeSaveConfig, true, "Saving…");
    try {
      await request("/api/youtube/config", {
        method: "PUT",
        body: JSON.stringify({ client_id: clientID, client_secret: clientSecret, base_url: normalizedBaseURL })
      });
      elements.youtubeClientSecret.value = "";
      state.youtubeConfigDirty = false;
      await loadYouTubeStatus(false);
      toast("YouTube settings saved.");
    } catch (error) {
      if (error.status !== 401) {
        elements.youtubeConfigError.textContent = error.message;
        elements.youtubeConfigError.hidden = false;
      }
    } finally {
      setButtonBusy(elements.youtubeSaveConfig, false);
    }
  }

  async function connectYouTube() {
    setButtonBusy(elements.youtubeConnect, true, "Connecting…");
    try {
      const result = await request("/api/youtube/connect", { method: "POST", body: JSON.stringify({}) });
      const authorizationURL = new URL(String(result && result.authorization_url || ""));
      if (authorizationURL.protocol !== "https:" || authorizationURL.hostname !== "accounts.google.com") {
        throw new APIError("Google returned an invalid authorization link.", 502);
      }
      window.location.assign(authorizationURL.toString());
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.youtubeConnect, false);
    }
  }

  async function disconnectYouTube() {
    setButtonBusy(elements.youtubeDisconnect, true, "Disconnecting…");
    try {
      await request("/api/youtube/connection", { method: "DELETE" });
      await loadYouTubeStatus(false);
      toast("YouTube channel disconnected.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.youtubeDisconnect, false);
    }
  }

  function consumeYouTubeReturn() {
    const url = new URL(window.location.href);
    const result = url.searchParams.get("youtube");
    if (!result) return;
    const reason = url.searchParams.get("youtube_reason") || "";
    url.searchParams.delete("youtube");
    url.searchParams.delete("youtube_reason");
    window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
    navigate("settings");
    if (result === "connected") toast("YouTube channel connected.");
    else {
      const messages = {
        state_invalid: "YouTube connection could not be verified. Try connecting again.",
        consent_denied: "YouTube access was not granted.",
        missing_scopes: "Grant YouTube upload access when reconnecting.",
        offline_access_missing: "Google did not grant offline access. Connect again and approve account access.",
        channel_unavailable: "Google could not confirm a YouTube channel for this account.",
        channel_busy: "This source has an active upload on a different YouTube channel. Wait for it to finish or reconnect that channel in Settings.",
        settings_changed: "YouTube settings changed during authorization. Start the connection again.",
        session_expired: "Your FrameVault session expired. Sign in and connect YouTube again.",
        provider_error: "Google could not complete the connection. Try again."
      };
      toast(messages[reason] || "YouTube could not connect. Review the settings and try again.", true);
    }
  }

  function youtubeRequest(path, options = {}, source = state.youtubeSource) {
    if (source && source.isVault) {
      if (!source.vaultToken || source.vaultToken !== state.vaultToken || source.vaultGeneration !== state.vaultGeneration
        || state.vaultLocking) return Promise.reject(new APIError("Vault is locked", 423));
      return vaultRequest(path, options).catch((error) => {
        if (error.status === 423 && state.vaultToken) {
          renderVaultLocked("Your Vault session ended. Enter your code to unlock it again.");
        }
        throw error;
      });
    }
    return request(path, options);
  }

  function renderYouTubeComposerConnection() {
    const connected = Boolean(state.youtubeStatus && state.youtubeStatus.connected && state.youtubeStatus.channel);
    const needsReconnect = Boolean(state.youtubeUpload && state.youtubeUpload.status === "needs_reconnect");
    const needsSettings = !connected || needsReconnect;
    elements.youtubeConnectRequired.hidden = !needsSettings;
    elements.youtubeUploadForm.hidden = !connected;
    elements.youtubeGoSettings.textContent = needsReconnect ? "Reconnect in Settings" : "Open Settings";
    const channel = state.youtubeStatus && state.youtubeStatus.channel;
    elements.youtubeUploadChannel.textContent = String(channel && (channel.title || channel.name || channel.id) || "");
  }

  function showYouTubeUploadFeedback(label, message) {
    elements.youtubeUploadStatus.hidden = false;
    elements.youtubeUploadStatusLabel.textContent = label;
    elements.youtubeUploadError.textContent = message;
    elements.youtubeUploadError.hidden = !message;
    elements.youtubeUploadNote.hidden = true;
    elements.youtubeUploadProgressWrap.hidden = true;
  }

  function clearYouTubeComposer() {
    window.clearTimeout(state.youtubePollTimer);
    state.youtubePollTimer = 0;
    if (state.youtubeDialogController) {
      state.youtubeDialogController.abort();
      state.vaultMediaControllers.delete(state.youtubeDialogController);
    }
    state.youtubeDialogController = null;
    state.youtubeSource = null;
    state.youtubeUpload = null;
    state.youtubeReviewedChannelID = "";
    state.youtubeSubmitting = false;
    state.youtubeAbandoning = false;
    state.youtubeRestarting = false;
    state.youtubeDialogReady = false;
    state.youtubeDialogEpoch += 1;
    clearYouTubePreview();
    resetYouTubeComposerFields();
    elements.youtubeSourceLabel.textContent = "";
    elements.youtubeUploadChannel.textContent = "";
    elements.youtubeConnectRequired.hidden = true;
    elements.youtubeUploadForm.hidden = false;
  }

  function closeYouTubeComposer(restoreFocus = true) {
    const wasOpen = elements.youtubeDialog.open;
    if (!wasOpen && !state.youtubeSource && !state.youtubeDialogController) return;
    const focusTarget = state.youtubeRestoreFocus;
    state.youtubeRestoreFocus = null;
    clearYouTubeComposer();
    if (wasOpen) elements.youtubeDialog.close();
    if (restoreFocus && focusTarget && focusTarget.isConnected !== false && typeof focusTarget.focus === "function") {
      focusTarget.focus({ preventScroll: true });
    } else if (restoreFocus && state.authenticated && !state.mustChangePassword) {
      focusPageTitle();
    }
  }

  function setYouTubeVisibilityRules(source) {
    elements.youtubeVisibility.value = "private";
    elements.youtubeVisibility.disabled = false;
    elements.youtubeVisibilityHelp.textContent = "Private is selected by default. Some Google API projects can upload only privately until audited.";
  }

  function youtubeTitleSuggestion(value) {
    const title = String(value || "").trim();
    if (!title || /[<>]/.test(title)) return "";
    return [...title].slice(0, 100).join("");
  }

  function youtubeDescriptionSuggestion(value) {
    const description = String(value || "");
    if (/[<>]/.test(description)) return "";
    const encoder = new TextEncoder();
    let bytes = 0;
    let result = "";
    for (const character of description) {
      const size = encoder.encode(character).length;
      if (bytes + size > 5000) break;
      result += character;
      bytes += size;
    }
    return result;
  }

  async function openYouTubeUpload(source) {
    if (!source || !["generation", "project"].includes(source.kind) || !source.id) return;
    if (source.isVault && (!state.vaultToken || state.vaultLocking)) {
      toast("Unlock the Vault before preparing this upload.", true);
      return;
    }
    if (elements.youtubeDialog.open || state.youtubeSource) closeYouTubeComposer(false);
    const controller = new AbortController();
    const normalized = {
      ...source,
      id: String(source.id),
      vaultToken: source.isVault ? state.vaultToken : "",
      vaultGeneration: source.isVault ? state.vaultGeneration : 0
    };
    state.youtubeSource = normalized;
    state.youtubeDialogController = controller;
    if (normalized.isVault) state.vaultMediaControllers.add(controller);
    state.youtubeUpload = null;
    state.youtubeReviewedChannelID = "";
    state.youtubeSubmitting = false;
    state.youtubeDialogReady = false;
    state.youtubeRestoreFocus = document.activeElement;
    const epoch = ++state.youtubeDialogEpoch;
    resetYouTubeComposerFields();
    setYouTubeVisibilityRules(normalized);
    elements.youtubeTitle.value = youtubeTitleSuggestion(normalized.titleSuggestion);
    elements.youtubeDescription.value = youtubeDescriptionSuggestion(normalized.descriptionSuggestion);
    elements.youtubeSourceLabel.textContent = String(normalized.label || "").slice(0, 240);
    elements.youtubeMetadataError.hidden = true;
    elements.youtubeDialog.showModal();
    setYouTubeDialogReady(false);
    if (!normalized.isVault) elements.youtubePreview.src = normalized.videoURL;
    renderYouTubeComposerConnection();

    const statusTask = loadYouTubeStatus(false, controller, epoch);
    const tasks = [statusTask];
    if (normalized.isVault) {
      tasks.push((async () => {
        const blob = await fetchVaultVideo(normalized.vaultItem, false, controller);
        if (!isCurrentYouTubeDialog(epoch, normalized)) return;
        state.youtubeSourcePreviewURL = URL.createObjectURL(blob);
        elements.youtubePreview.src = state.youtubeSourcePreviewURL;
      })());
    }
    const [freshStatus] = await Promise.allSettled(tasks);
    if (isCurrentYouTubeDialog(epoch, normalized) && freshStatus.status === "fulfilled"
      && freshStatus.value && freshStatus.value.connected) {
      await loadYouTubeUploads(normalized, epoch, freshStatus.value);
    }
  }

  async function loadYouTubeUploads(source, epoch, reviewedStatus) {
    const channelID = String(reviewedStatus && reviewedStatus.channel && reviewedStatus.channel.id || "");
    if (!channelID) {
      showYouTubeUploadFeedback("YouTube channel could not be confirmed.", "Return to Settings and connect a channel again.");
      return;
    }
    const query = new URLSearchParams({ source_kind: source.kind, source_id: source.id });
    try {
      const result = await youtubeRequest(`/api/youtube/uploads?${query.toString()}`, {
        signal: state.youtubeDialogController && state.youtubeDialogController.signal
      }, source);
      if (!isCurrentYouTubeDialog(epoch, source)) return;
      if (String(state.youtubeStatus && state.youtubeStatus.channel && state.youtubeStatus.channel.id || "") !== channelID) {
        closeYouTubeComposer(false);
        return;
      }
      state.youtubeReviewedChannelID = channelID;
      const uploads = (Array.isArray(result && result.uploads) ? result.uploads : [])
        .filter((upload) => channelID && String(upload && upload.channel_id || "") === channelID);
      const latest = uploads[0];
      setYouTubeDialogReady(true);
      if (!latest) return;
      state.youtubeUpload = latest;
      elements.youtubeTitle.value = String(latest.title || "");
      elements.youtubeDescription.value = String(latest.description || "");
      elements.youtubeVisibility.value = String(latest.privacy_status || "private");
      elements.youtubeMadeForKidsYes.checked = latest.made_for_kids === true;
      elements.youtubeMadeForKidsNo.checked = latest.made_for_kids === false;
      elements.youtubeSynthetic.checked = latest.contains_synthetic_media !== false;
      renderYouTubeUpload(latest);
    } catch (error) {
      if (error.name === "AbortError" || error.status === 401 || error.status === 423 || !isCurrentYouTubeDialog(epoch, source)) return;
      showYouTubeUploadFeedback("Upload status could not be loaded.", error.message);
    }
  }

  function youTubeWatchURL(upload) {
    const value = String(upload && upload.youtube_url || "");
    const videoID = String(upload && upload.youtube_video_id || "");
    const candidate = value || (videoID ? `https://www.youtube.com/watch?v=${encodeURIComponent(videoID)}` : "");
    try {
      const url = new URL(candidate);
      if (url.protocol === "https:" && ["youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be"].includes(url.hostname)) return url.toString();
    } catch {}
    return "";
  }

  function renderYouTubeUpload(upload) {
    state.youtubeUpload = upload || null;
    renderYouTubeComposerConnection();
    if (!upload) {
      elements.youtubeUploadStatus.hidden = true;
      const lockFields = !state.youtubeDialogReady || state.youtubeSubmitting;
      [elements.youtubeTitle, elements.youtubeDescription, elements.youtubeVisibility,
        elements.youtubeMadeForKidsYes, elements.youtubeMadeForKidsNo, elements.youtubeSynthetic,
        elements.youtubeGenerateMetadata].forEach((field) => { field.disabled = lockFields; });
      elements.youtubeUploadSubmit.disabled = lockFields;
      return;
    }
    const status = String(upload.status || "").toLowerCase();
    const labels = {
      queued: "Upload queued",
      initiating: "Starting upload",
      uploading: "Uploading to YouTube",
      processing: "YouTube is processing the video",
      completed: "Ready on YouTube",
      failed: "Upload failed",
      canceled: "Upload canceled",
      needs_reconnect: "Reconnect your YouTube channel",
      attention_required: "YouTube needs attention"
    };
    const active = ["queued", "initiating", "uploading"].includes(status);
    const cancelRequested = Boolean(upload.cancel_requested);
    const hasYouTubeID = Boolean(upload.youtube_video_id);
    const progressValue = Number(upload.progress);
    const hasProgress = upload.progress !== null && upload.progress !== undefined && upload.progress !== "" && Number.isFinite(progressValue);
    const watchURL = youTubeWatchURL(upload);
    elements.youtubeUploadStatus.hidden = false;
    elements.youtubeUploadStatusLabel.textContent = labels[status] || (status ? status.replaceAll("_", " ") : "Upload status");
    elements.youtubeUploadError.textContent = String(upload.error || "");
    elements.youtubeUploadError.hidden = !upload.error;
    let note = "";
    if (status === "processing") {
      note = upload.processing_status
        ? `YouTube processing status: ${String(upload.processing_status).replaceAll("_", " ")}. Playback may not be ready yet.`
        : "Playback may not be ready until YouTube finishes processing the video.";
    } else if (status === "completed") {
      note = "YouTube finished processing this video.";
    } else if (cancelRequested) {
      note = "Cancel requested. Waiting for the upload stream to stop.";
    } else if (status === "needs_reconnect") {
      note = "Reconnect the channel in Settings, then return here to retry.";
    } else if (status === "attention_required") {
      note = "Review the video in YouTube Studio before taking another upload action.";
    } else if (status === "canceled" && (upload.outcome_uncertain || upload.error)) {
      note = "The upload outcome is unresolved. Check YouTube before starting another upload.";
    }
    if (upload.manual_restart_note) {
      note = `${note ? `${note} ` : ""}${String(upload.manual_restart_note)}`;
    }
    if (upload.requested_privacy_status && upload.privacy_status
      && upload.requested_privacy_status !== upload.privacy_status) {
      note = `${note ? `${note} ` : ""}YouTube applied ${upload.privacy_status} visibility.`;
    }
    elements.youtubeUploadNote.textContent = note;
    elements.youtubeUploadNote.hidden = !note;
    elements.youtubeUploadProgressWrap.hidden = !active;
    elements.youtubeUploadProgressBar.classList.toggle("indeterminate", active && !hasProgress);
    if (hasProgress) {
      const progress = Math.max(0, Math.min(100, progressValue));
      elements.youtubeUploadProgressBar.style.width = `${progress}%`;
      elements.youtubeUploadProgressLabel.textContent = `${Math.round(progress)}%`;
      elements.youtubeUploadProgressBar.setAttribute("role", "progressbar");
      elements.youtubeUploadProgressBar.setAttribute("aria-valuemin", "0");
      elements.youtubeUploadProgressBar.setAttribute("aria-valuemax", "100");
      elements.youtubeUploadProgressBar.setAttribute("aria-valuenow", String(progress));
    } else {
      elements.youtubeUploadProgressBar.style.width = active ? "38%" : "0%";
      elements.youtubeUploadProgressLabel.textContent = active ? "In progress" : "";
      elements.youtubeUploadProgressBar.removeAttribute("aria-valuenow");
    }
    const lockFields = !state.youtubeDialogReady || state.youtubeSubmitting || active || ["processing", "completed", "failed", "needs_reconnect", "attention_required"].includes(status);
    [elements.youtubeTitle, elements.youtubeDescription, elements.youtubeVisibility,
      elements.youtubeMadeForKidsYes, elements.youtubeMadeForKidsNo, elements.youtubeSynthetic,
      elements.youtubeGenerateMetadata].forEach((field) => { field.disabled = lockFields; });
    const canSubmit = !upload.id || (status === "canceled" && !upload.error && !upload.outcome_uncertain && !cancelRequested && !hasYouTubeID);
    elements.youtubeUploadSubmit.hidden = !canSubmit;
    elements.youtubeUploadSubmit.disabled = !state.youtubeDialogReady || state.youtubeSubmitting;
    elements.youtubeUploadSubmit.textContent = status === "canceled" ? "Upload again" : "Upload video";
    elements.youtubeUploadCancel.hidden = !active;
    elements.youtubeUploadCancel.disabled = !state.youtubeDialogReady || cancelRequested;
    elements.youtubeUploadCancel.textContent = cancelRequested ? "Canceling…" : "Cancel upload";
    const abandonAllowed = !hasYouTubeID && ["needs_reconnect", "attention_required"].includes(status);
    elements.youtubeUploadAbandon.hidden = !abandonAllowed;
    elements.youtubeUploadAbandon.disabled = !state.youtubeDialogReady || state.youtubeAbandoning;
    elements.youtubeUploadAbandon.textContent = state.youtubeAbandoning ? "Abandoning…" : "Abandon upload";
    elements.youtubeUploadRetry.hidden = status !== "failed" || hasYouTubeID;
    elements.youtubeUploadRetry.disabled = !state.youtubeDialogReady;
    elements.youtubeOpenVideo.hidden = !watchURL;
    if (watchURL) elements.youtubeOpenVideo.href = watchURL;
    const canCheckYouTube = status === "attention_required" || (status === "canceled" && upload.outcome_uncertain === true);
    if (hasYouTubeID) elements.youtubeDuplicateConfirm.checked = false;
    elements.youtubeOpenStudio.hidden = !canCheckYouTube;
    elements.youtubeDuplicateRecovery.hidden = !canCheckYouTube;
    elements.youtubeRecoveryConfirmControls.hidden = hasYouTubeID;
    elements.youtubeDuplicateConfirm.disabled = !state.youtubeDialogReady || state.youtubeRestarting;
    elements.youtubeUploadRestart.disabled = !state.youtubeDialogReady || state.youtubeRestarting || !elements.youtubeDuplicateConfirm.checked;
    elements.youtubeUploadRestart.textContent = state.youtubeRestarting ? "Starting…" : "Start upload again";
    if (status === "processing" || active) scheduleYouTubePoll(state.youtubeDialogEpoch, String(upload.id || ""));
    else {
      window.clearTimeout(state.youtubePollTimer);
      state.youtubePollTimer = 0;
    }
  }

  function canEditYouTubeMetadata(upload) {
    if (!state.youtubeDialogReady || state.youtubeSubmitting) return false;
    if (!upload || !upload.id) return true;
    const status = String(upload.status || "").toLowerCase();
    return status === "canceled" && !upload.error && upload.outcome_uncertain !== true
      && !upload.cancel_requested && !upload.youtube_video_id;
  }

  function validateYouTubeUpload() {
    const title = elements.youtubeTitle.value.trim();
    const description = elements.youtubeDescription.value;
    if (!title) return "Enter a video title.";
    if ([...title].length > 100) return "Video titles can contain up to 100 characters.";
    if (/[<>]/.test(title) || /[<>]/.test(description)) return "Remove angle brackets from the title and description.";
    if (new TextEncoder().encode(description).length > 5000) return "Descriptions can contain up to 5,000 UTF-8 bytes.";
    if (!elements.youtubeMadeForKidsYes.checked && !elements.youtubeMadeForKidsNo.checked) return "Choose whether this video is made for kids.";
    if (!["private", "unlisted", "public"].includes(elements.youtubeVisibility.value)) return "Choose a visibility setting.";
    return "";
  }

  async function generateYouTubeMetadata() {
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    if (!source || !isCurrentYouTubeDialog(epoch, source) || !canEditYouTubeMetadata(state.youtubeUpload)) return;
    const previousTitle = elements.youtubeTitle.value;
    const previousDescription = elements.youtubeDescription.value;
    elements.youtubeMetadataError.hidden = true;
    setButtonBusy(elements.youtubeGenerateMetadata, true, "Generating…");
    try {
      const result = await youtubeRequest("/api/youtube/metadata", {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal,
        body: JSON.stringify({ source_kind: source.kind, source_id: source.id })
      }, source);
      if (!isCurrentYouTubeDialog(epoch, source) || !canEditYouTubeMetadata(state.youtubeUpload)) return;
      if (elements.youtubeTitle.value === previousTitle) elements.youtubeTitle.value = String(result && result.title || "");
      if (elements.youtubeDescription.value === previousDescription) elements.youtubeDescription.value = String(result && result.description || "");
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        elements.youtubeMetadataError.textContent = error.message;
        elements.youtubeMetadataError.hidden = false;
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        setButtonBusy(elements.youtubeGenerateMetadata, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  async function submitYouTubeUpload(event) {
    event.preventDefault();
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    if (!source || !isCurrentYouTubeDialog(epoch, source) || state.youtubeSubmitting) return;
    if (!state.youtubeDialogReady) return;
    if (!state.youtubeStatus || !state.youtubeStatus.connected) {
      renderYouTubeComposerConnection();
      return;
    }
    const existingStatus = String(state.youtubeUpload && state.youtubeUpload.status || "").toLowerCase();
    const safelyCanceled = existingStatus === "canceled" && state.youtubeUpload
      && !state.youtubeUpload.error && state.youtubeUpload.outcome_uncertain !== true
      && !state.youtubeUpload.cancel_requested && !state.youtubeUpload.youtube_video_id;
    if (state.youtubeUpload && state.youtubeUpload.id && !safelyCanceled) return;
    const reviewedChannelID = String(state.youtubeReviewedChannelID || "");
    const currentChannelID = String(state.youtubeStatus.channel && state.youtubeStatus.channel.id || "");
    if (!reviewedChannelID || reviewedChannelID !== currentChannelID) {
      closeYouTubeComposer(false);
      toast("The connected YouTube channel changed. Reopen the upload to review it again.", true);
      return;
    }
    const validationError = validateYouTubeUpload();
    if (validationError) {
      showYouTubeUploadFeedback("Check upload details", validationError);
      return;
    }
    const madeForKids = elements.youtubeMadeForKidsYes.checked;
    const privacyStatus = elements.youtubeVisibility.value;
    const body = {
      source_kind: source.kind,
      source_id: source.id,
      channel_id: reviewedChannelID,
      title: elements.youtubeTitle.value.trim(),
      description: elements.youtubeDescription.value,
      privacy_status: privacyStatus,
      made_for_kids: madeForKids,
      contains_synthetic_media: Boolean(elements.youtubeSynthetic.checked)
    };
    elements.youtubeUploadError.hidden = true;
    elements.youtubeUploadNote.hidden = true;
    state.youtubeSubmitting = true;
    setButtonBusy(elements.youtubeUploadSubmit, true, "Starting upload…");
    try {
      const result = await youtubeRequest("/api/youtube/uploads", {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal,
        body: JSON.stringify(body)
      }, source);
      if (!isCurrentYouTubeDialog(epoch, source)) return;
      const upload = result && result.upload || result;
      renderYouTubeUpload(upload || {});
      scheduleYouTubePoll(epoch, String(upload && upload.id || ""));
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        showYouTubeUploadFeedback("Upload could not be started.", error.message);
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        state.youtubeSubmitting = false;
        setButtonBusy(elements.youtubeUploadSubmit, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  function scheduleYouTubePoll(epoch, uploadID, delay = 3000) {
    window.clearTimeout(state.youtubePollTimer);
    state.youtubePollTimer = 0;
    if (!uploadID || !state.youtubeSource || !isCurrentYouTubeDialog(epoch)) return;
    const status = String(state.youtubeUpload && state.youtubeUpload.status || "").toLowerCase();
    if (!["queued", "initiating", "uploading", "processing"].includes(status)) return;
    state.youtubePollTimer = window.setTimeout(async () => {
      if (!isCurrentYouTubeDialog(epoch)) return;
      try {
        const result = await youtubeRequest(`/api/youtube/uploads/${encodeURIComponent(uploadID)}`, {
          signal: state.youtubeDialogController && state.youtubeDialogController.signal
        });
        if (!isCurrentYouTubeDialog(epoch)) return;
        renderYouTubeUpload(result && result.upload || result);
      } catch (error) {
        if (error.name === "AbortError" || error.status === 401 || error.status === 423 || !isCurrentYouTubeDialog(epoch)) return;
        elements.youtubeUploadError.textContent = `Upload status could not be refreshed. ${error.message}`;
        elements.youtubeUploadError.hidden = false;
      }
      if (isCurrentYouTubeDialog(epoch)) scheduleYouTubePoll(epoch, uploadID, 5000);
    }, delay);
  }

  async function cancelYouTubeUpload() {
    const upload = state.youtubeUpload;
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    if (!upload || !upload.id || !source || !isCurrentYouTubeDialog(epoch, source)) return;
    setButtonBusy(elements.youtubeUploadCancel, true, "Canceling…");
    try {
      const result = await youtubeRequest(`/api/youtube/uploads/${encodeURIComponent(upload.id)}/cancel`, {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal
      }, source);
      if (isCurrentYouTubeDialog(epoch, source)) renderYouTubeUpload(result && result.upload || result);
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        elements.youtubeUploadError.textContent = error.message;
        elements.youtubeUploadError.hidden = false;
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        setButtonBusy(elements.youtubeUploadCancel, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  async function abandonYouTubeUpload() {
    const upload = state.youtubeUpload;
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    const status = String(upload && upload.status || "").toLowerCase();
    if (!upload || !upload.id || upload.youtube_video_id || !source
      || !["needs_reconnect", "attention_required"].includes(status)
      || state.youtubeAbandoning || !isCurrentYouTubeDialog(epoch, source)) return;
    state.youtubeAbandoning = true;
    setButtonBusy(elements.youtubeUploadAbandon, true, "Abandoning…");
    try {
      const result = await youtubeRequest(`/api/youtube/uploads/${encodeURIComponent(upload.id)}/cancel`, {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal
      }, source);
      if (isCurrentYouTubeDialog(epoch, source)) renderYouTubeUpload(result && result.upload || result);
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        elements.youtubeUploadError.textContent = error.message;
        elements.youtubeUploadError.hidden = false;
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        state.youtubeAbandoning = false;
        setButtonBusy(elements.youtubeUploadAbandon, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  async function retryYouTubeUpload() {
    const upload = state.youtubeUpload;
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    if (!upload || !upload.id || upload.youtube_video_id || !source || String(upload.status).toLowerCase() !== "failed"
      || !isCurrentYouTubeDialog(epoch, source)) return;
    setButtonBusy(elements.youtubeUploadRetry, true, "Retrying…");
    try {
      const result = await youtubeRequest(`/api/youtube/uploads/${encodeURIComponent(upload.id)}/retry`, {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal
      }, source);
      if (isCurrentYouTubeDialog(epoch, source)) {
        renderYouTubeUpload(result && result.upload || result);
        scheduleYouTubePoll(epoch, String(upload.id));
      }
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        elements.youtubeUploadError.textContent = error.message;
        elements.youtubeUploadError.hidden = false;
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        setButtonBusy(elements.youtubeUploadRetry, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  async function restartYouTubeUpload() {
    const upload = state.youtubeUpload;
    const source = state.youtubeSource;
    const epoch = state.youtubeDialogEpoch;
    const status = String(upload && upload.status || "").toLowerCase();
    const recoverable = status === "attention_required" || (status === "canceled" && upload && upload.outcome_uncertain === true);
    if (!upload || !upload.id || upload.youtube_video_id || !source || !recoverable
      || !elements.youtubeDuplicateConfirm.checked || state.youtubeRestarting || !isCurrentYouTubeDialog(epoch, source)) return;
    state.youtubeRestarting = true;
    elements.youtubeDuplicateConfirm.checked = false;
    setButtonBusy(elements.youtubeUploadRestart, true, "Starting…");
    try {
      const result = await youtubeRequest(`/api/youtube/uploads/${encodeURIComponent(upload.id)}/restart`, {
        method: "POST",
        signal: state.youtubeDialogController && state.youtubeDialogController.signal,
        body: JSON.stringify({ confirm_not_uploaded: true })
      }, source);
      if (isCurrentYouTubeDialog(epoch, source)) {
        const restarted = result && result.upload || result;
        renderYouTubeUpload(restarted);
        scheduleYouTubePoll(epoch, String(restarted && restarted.id || upload.id));
      }
    } catch (error) {
      if (error.name !== "AbortError" && error.status !== 401 && error.status !== 423 && isCurrentYouTubeDialog(epoch, source)) {
        elements.youtubeUploadError.textContent = error.message;
        elements.youtubeUploadError.hidden = false;
      }
    } finally {
      if (isCurrentYouTubeDialog(epoch, source)) {
        state.youtubeRestarting = false;
        setButtonBusy(elements.youtubeUploadRestart, false);
        renderYouTubeUpload(state.youtubeUpload);
      }
    }
  }

  function invalidateYouTubeSource(kind, id) {
    const source = state.youtubeSource;
    if (!source || source.kind !== kind || String(source.id) !== String(id)) return;
    closeYouTubeComposer(false);
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
    if (state.youtubeSource && state.youtubeSource.isVault) closeYouTubeComposer(false);
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
      const upload = make("button", { className: "button secondary", text: "Upload to YouTube", type: "button" });
      upload.addEventListener("click", () => openYouTubeUpload(vaultYouTubeSource(item)));
      actions.append(view, download, restore, upload);
      body.append(actions);
      card.append(preview, body);
      elements.vaultGrid.append(card);
    });
  }

  async function fetchVaultVideo(item, download = false, suppliedController = null) {
    if (state.vaultLocking || !state.vaultToken) throw new APIError("Vault is locked", 423);
    const path = `/api/vault/items/${encodeURIComponent(item.kind)}/${encodeURIComponent(item.id)}/video${download ? "?download=1" : ""}`;
    const generation = state.vaultGeneration;
    const token = state.vaultToken;
    const controller = suppliedController || new AbortController();
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
      elements.vaultPlayerYouTubeUpload.hidden = false;
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
      invalidateYouTubeSource(item.kind, item.id);
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
    if (elements.vaultPlayerYouTubeUpload) elements.vaultPlayerYouTubeUpload.hidden = true;
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
    if (!state.authenticated) return;
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
    const authGeneration = state.authGeneration;
    setButtonBusy(button, true, "Testing key…");
    try {
      await request("/api/settings/api-key", {
        method: "PUT",
        body: JSON.stringify({ api_key: key })
      });
      if (!state.authenticated || state.authGeneration !== authGeneration) return;
      state.openRouterConfigurationRevision += 1;
      state.openRouterConfigured = true;
      elements.apiKey.value = "";
      elements.keyState.className = "badge completed";
      elements.keyState.textContent = "Configured";
      elements.maskedKey.textContent = "Configured";
      updateGenerateAvailability();
      toast("API key tested and saved.");
      if (state.videoProvider === "openrouter") await loadModels(false);
    } catch (error) {
      if (!state.authenticated || state.authGeneration !== authGeneration) return;
      if (error.status !== 401) toast(error.message, true);
    } finally {
      if (state.authGeneration === authGeneration) setButtonBusy(button, false);
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
        consumeYouTubeReturn();
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
      if (state.pendingProjectSubmission) clearPendingProjectSubmission(state.pendingProjectSubmission.request_id);
      elements.projectTopic.value = "";
      showLoggedOut();
    }
  }

  function bindEvents() {
    syncNavigationAccessibility();
    renderConnectionState();
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
    elements.promptCategory.addEventListener("input", () => {
      markPendingProjectFormEdited();
      state.categoryEditRevision++;
      updatePromptCategory();
    });
    elements.projectTopic.addEventListener("input", () => {
      markPendingProjectFormEdited();
      state.promptEditRevisions.project++;
    });
    elements.randomPrompt.addEventListener("click", generateRandomPrompt);
    elements.retryProject.addEventListener("click", retryCurrentProject);
    elements.modeOptions.forEach((button) => {
      button.addEventListener("click", () => {
        setGenerationMode(button.dataset.mode);
        if (state.videoProvider === "modal") void loadModels(false);
      });
      button.addEventListener("keydown", moveGenerationMode);
    });
    elements.createModeButtons.forEach((button) => {
      button.addEventListener("click", () => openCreator(button.dataset.createMode));
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
    elements.vaultPlayerYouTubeUpload.addEventListener("click", () => {
      const item = state.vaultItems.find((entry) => vaultItemKey(entry) === state.vaultPlayerItemKey);
      if (item) void openYouTubeUpload(vaultYouTubeSource(item));
    });
    elements.vaultPlayerDialog.addEventListener("close", closeVaultPlayer);
    elements.youtubeConfigForm.addEventListener("submit", saveYouTubeConfig);
    elements.youtubeConfigForm.addEventListener("input", () => { state.youtubeConfigDirty = true; });
    elements.youtubeConnect.addEventListener("click", connectYouTube);
    elements.youtubeDisconnect.addEventListener("click", disconnectYouTube);
    elements.singleYouTubeUpload.addEventListener("click", () => {
      if (state.currentGenerationRecord) void openYouTubeUpload(generationYouTubeSource(state.currentGenerationRecord));
    });
    elements.projectYouTubeUpload.addEventListener("click", () => {
      if (state.currentProjectRecord) void openYouTubeUpload(projectYouTubeSource(state.currentProjectRecord));
    });
    elements.youtubeUploadForm.addEventListener("submit", submitYouTubeUpload);
    elements.youtubeGenerateMetadata.addEventListener("click", generateYouTubeMetadata);
    elements.youtubeUploadCancel.addEventListener("click", cancelYouTubeUpload);
    elements.youtubeUploadAbandon.addEventListener("click", abandonYouTubeUpload);
    elements.youtubeUploadRetry.addEventListener("click", retryYouTubeUpload);
    elements.youtubeDuplicateConfirm.addEventListener("change", () => {
      elements.youtubeUploadRestart.disabled = !state.youtubeDialogReady || state.youtubeRestarting || !elements.youtubeDuplicateConfirm.checked;
    });
    elements.youtubeUploadRestart.addEventListener("click", restartYouTubeUpload);
    elements.youtubeDialogClose.addEventListener("click", () => closeYouTubeComposer());
    elements.youtubeDialog.addEventListener("close", () => closeYouTubeComposer());
    elements.youtubeDialog.addEventListener("cancel", () => closeYouTubeComposer(false));
    elements.youtubeGoSettings.addEventListener("click", () => {
      closeYouTubeComposer(false);
      navigate("settings");
    });
    elements.modalConfigForm.addEventListener("submit", saveModalConfig);
    elements.modalAccountCancel.addEventListener("click", resetModalAccountForm);
    elements.modalAccountProject.addEventListener("change", () => {
      markPendingProjectFormEdited();
      if (state.videoProvider === "modal" && state.mode === "project") void loadModels(false);
    });
    elements.modalAccountSingle.addEventListener("change", () => {
      if (state.videoProvider === "modal" && state.mode === "single") void loadModels(false);
    });
    elements.videoProvider.addEventListener("change", changeVideoProvider);
    elements.testVideoProvider.addEventListener("click", testVideoProvider);
    elements.passwordForm.addEventListener("submit", updatePassword);
    elements.refreshHistory.addEventListener("click", () => {
      retryFailedHistoryPreviews();
      startJobEventStream();
      loadHistory(true);
      loadProjects(true);
    });
    elements.historySearch.addEventListener("input", renderHistory);
    [elements.historyStatus, elements.historyFormat].forEach((select) => select.addEventListener("change", renderHistory));
    elements.historyClearFilters.addEventListener("click", clearHistoryFilters);
    elements.model.addEventListener("change", () => {
      markPendingProjectFormEdited();
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
      state.promptEditRevisions.single++;
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
    window.addEventListener("offline", renderConnectionState);
    window.addEventListener("online", startJobEventStream);
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
