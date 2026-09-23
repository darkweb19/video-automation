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
    prompt: $("#prompt"),
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
    projectStory: $("#project-story"),
    projectScenes: $("#project-scenes"),
    projectTrace: $("#project-trace"),
    projectTraceSections: $("#project-trace-sections"),
    projectFinal: $("#project-final"),
    projectVideo: $("#project-video"),
    projectDownload: $("#project-download"),
    projectHistory: $("#project-history"),
    historyGrid: $("#history-grid"),
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
    modalBaseURL: $("#modal-base-url"),
    modalAPIKey: $("#modal-api-key"),
    modalKeyHelp: $("#modal-key-help"),
    passwordForm: $("#password-form"),
    currentPassword: $("#current-password"),
    newPassword: $("#new-password"),
    toast: $("#toast")
  };

  const viewMeta = {
    overview: { title: "Overview", kicker: "Workspace" },
    generate: { title: "Generate", kicker: "Create" },
    history: { title: "History", kicker: "Library" },
    settings: { title: "Settings", kicker: "AI providers" }
  };

  const state = {
    models: [],
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
    historyRefreshTimer: 0,
    currentGenerationID: "",
    pollTimer: 0,
    mode: "project",
    projects: [],
    currentProjectID: "",
    projectPollTimer: 0,
    toastTimer: 0,
    authenticated: false,
    mustChangePassword: false
  };

  const VIDEO_PROVIDER_NAMES = Object.freeze({ modal: "Modal", openrouter: "OpenRouter" });

  function videoProviderName(provider = state.videoProvider) {
    return VIDEO_PROVIDER_NAMES[provider] || provider;
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
      button.dataset.originalText = button.textContent;
      button.textContent = busyText;
      button.disabled = true;
    } else {
      button.textContent = button.dataset.originalText || button.textContent;
      button.disabled = false;
      delete button.dataset.originalText;
    }
  }

  function toast(message, isError = false) {
    window.clearTimeout(state.toastTimer);
    elements.toast.textContent = message;
    elements.toast.classList.toggle("error", isError);
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
        stopHistoryRefresh();
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

  function stopPolling() {
    window.clearTimeout(state.pollTimer);
    state.pollTimer = 0;
  }

  function stopProjectPolling() {
    window.clearTimeout(state.projectPollTimer);
    state.projectPollTimer = 0;
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

  function setGenerationMode(mode) {
    state.mode = mode === "single" ? "single" : "project";
    const project = state.mode === "project";
    elements.modeOptions.forEach((button) => {
      const active = button.dataset.mode === state.mode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
    });
    elements.projectInputs.hidden = !project;
    elements.projectOptions.hidden = !project;
    elements.projectModelNote.hidden = !project;
    elements.singleOptions.hidden = project;
    elements.prompt.hidden = project;
    elements.prompt.closest(".field").hidden = project;
    elements.generate.textContent = project ? "Generate 30-second project" : "Generate video";
    elements.statusActive.hidden = project;
    elements.projectStatus.hidden = !project || !state.currentProjectID;
    if (project) {
      const selected = state.models.find((model) => model.id === elements.model.value);
      if (state.models.length && !supportsProject(selected)) {
        const fallback = state.models.find(supportsProject);
        if (fallback) selectModel(fallback.id);
      }
      elements.statusEmpty.hidden = Boolean(state.currentProjectID);
    } else {
      elements.statusEmpty.hidden = false;
    }
    updateModelOptions();
  }

  function supportsProject(model) {
    return Array.isArray(model && model.durations) && model.durations.includes(6)
      && Array.isArray(model.resolutions) && model.resolutions.includes("480p")
      && Array.isArray(model.aspect_ratios) && model.aspect_ratios.includes("9:16");
  }

  function stopHistoryRefresh() {
    window.clearTimeout(state.historyRefreshTimer);
    state.historyRefreshTimer = 0;
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

  function showLoggedOut() {
    stopPolling();
    stopProjectPolling();
    stopHistoryRefresh();
    state.authenticated = false;
    state.currentGenerationID = "";
    state.currentProjectID = "";
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
    elements.appShell.hidden = true;
    elements.loginScreen.hidden = false;
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
    state.authenticated = true;
    applyPasswordGate(mustChangePassword);
    elements.accountName.textContent = username || "sujanshrestha";
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
    $$(".view").forEach((node) => node.classList.toggle("active", node.dataset.page === view));
    $$(".nav-item").forEach((node) => node.classList.toggle("active", node.dataset.view === view));
    elements.pageTitle.textContent = viewMeta[view].title;
    elements.pageKicker.textContent = viewMeta[view].kicker;
    elements.sidebar.classList.remove("open");
    elements.menuButton.setAttribute("aria-expanded", "false");
    if (view === "settings" && !state.mustChangePassword) loadSettings(false);
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
      elements.generate.disabled = true;
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
    elements.generate.disabled = state.mode === "project" && !projectCompatible;
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
      const payload = await request(`/models?provider=${encodeURIComponent(state.videoProvider)}`);
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
      elements.activeVideoProvider.textContent = `Video provider: ${videoProviderName(payload && payload.provider || state.videoProvider)}`;
      elements.model.disabled = false;
      elements.modelTrigger.disabled = false;
      renderModelMenu();
      updateModelOptions();
      if (selected && selected.id !== requestedModel) void persistVideoModel(selected.id, false);
      renderHistory();
      renderRecent();
    } catch (error) {
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

  function hasActiveJobs() {
    const activeStatuses = new Set(["queued", "processing", "downloading", "download_failed"]);
    const aggregateActive = state.stats ? state.stats.active : 0;
    return aggregateActive > 0 || state.generations.some((record) => activeStatuses.has(record.status));
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

  function scheduleHistoryRefresh() {
    stopHistoryRefresh();
    if (!state.authenticated || state.mustChangePassword || document.hidden || !hasActiveJobs()) return;
    state.historyRefreshTimer = window.setTimeout(() => {
      state.historyRefreshTimer = 0;
      loadHistory(false, false);
    }, 7000);
  }

  function renderRecent() {
    elements.recentList.replaceChildren();
    const recent = state.generations.slice(0, 5);
    if (!recent.length) {
      elements.recentList.append(make("div", { className: "empty", text: "No generations yet." }));
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

  function renderHistory() {
    elements.historyGrid.replaceChildren();
    if (!state.generations.length) {
      elements.historyGrid.append(make("div", { className: "empty", text: "No generations yet." }));
      return;
    }

    state.generations.forEach((record) => {
      const card = make("article", { className: "history-card" });
      const preview = make("div", { className: "history-preview" });
      if (record.video_ready) {
        const video = make("video");
        video.controls = true;
        video.preload = "metadata";
        video.playsInline = true;
        video.src = `/video?id=${encodeURIComponent(record.id)}`;
        preview.append(video);
      } else {
        preview.append(statusBadge(record.status));
      }

      const body = make("div", { className: "history-body" });
      body.append(make("h3", { className: "history-title", text: record.prompt || "Untitled generation" }));
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
      if (record.error) body.append(make("div", { className: "alert error", text: record.error }));

      const actions = make("div", { className: "history-actions" });
      if (record.video_ready) {
        const download = make("a", { className: "button secondary", text: "Download" });
        download.href = `/video?id=${encodeURIComponent(record.id)}&download=1`;
        actions.append(download);
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
      const records = Array.isArray(payload && payload.generations) ? payload.generations : [];
      if (append) {
        const byID = new Map(state.generations.map((record) => [record.id, record]));
        records.forEach((record) => byID.set(record.id, record));
        state.generations = Array.from(byID.values());
      } else {
        state.generations = records;
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
      scheduleHistoryRefresh();
    }
  }

  async function deleteGeneration(record) {
    const preview = (record.prompt || "this generation").slice(0, 80);
    if (!window.confirm(`Delete “${preview}” and its stored video? This cannot be undone.`)) return;
    try {
      await request(`/api/generations/${encodeURIComponent(record.id)}`, { method: "DELETE" });
      state.generations = state.generations.filter((item) => item.id !== record.id);
      if (state.currentGenerationID === record.id) {
        stopPolling();
        state.currentGenerationID = "";
      }
      await loadHistory(false, false);
      toast("Generation deleted.");
    } catch (error) {
      if (error.status !== 401) toast(error.message, true);
    }
  }

  function setStatusRecord(record) {
    const status = record.status || "processing";
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
      elements.generatedVideo.src = videoURL;
      elements.generatedVideo.hidden = false;
      elements.openVideo.href = `${videoURL}&download=1`;
      elements.openVideo.hidden = false;
    }
  }

  async function pollGeneration(id, delay = 0) {
    stopPolling();
    state.currentGenerationID = id;
    state.pollTimer = window.setTimeout(async () => {
      if (!state.authenticated || state.currentGenerationID !== id) return;
      try {
        const record = await request(`/status?id=${encodeURIComponent(id)}`);
        setStatusRecord(record);
        const terminal = record.status === "completed" || record.status === "failed";
        if (terminal) {
          state.currentGenerationID = "";
          await loadHistory(false);
          toast(record.status === "completed" ? "Video saved successfully." : "Generation failed.", record.status === "failed");
          return;
        }
        pollGeneration(id, 3000);
      } catch (error) {
        if (error.status === 401) return;
        elements.statusError.textContent = error.message;
        elements.statusError.hidden = false;
        elements.retryStatus.hidden = false;
        elements.progressWrap.hidden = true;
        elements.statusDetail.textContent = "Status updates are paused.";
      }
    }, delay);
  }

  function projectScenes(project) {
    const scenes = projectValue(project, "scenes", "scene_list");
    return Array.isArray(scenes) ? scenes.slice().sort((a, b) => Number(projectValue(a, "scene_number", "number", "scene")) - Number(projectValue(b, "scene_number", "number", "scene"))) : [];
  }

  function projectProgress(project, scenes) {
    const supplied = Number(projectValue(project, "progress", "progress_percent", "percent"));
    if (Number.isFinite(supplied)) return Math.max(0, Math.min(100, supplied <= 1 ? supplied * 100 : supplied));
    if (!scenes.length) return ["completed", "complete"].includes(String(project.status).toLowerCase()) ? 100 : 0;
    const done = scenes.filter((scene) => ["completed", "complete", "ready"].includes(String(projectValue(scene, "status")).toLowerCase()) || projectValue(scene, "video_ready")).length;
    return Math.round((done / 5) * 100);
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
    const retryCount = firstTraceValue(project, "retry_count", "retries", "attempts", "attempt");
    const runFields = [
      { label: "Project status", value: String(status || "queued").replaceAll("_", " ") },
      { label: "Created", value: formatTraceDate(projectValue(project, "created_at", "createdAt", "created")) },
      { label: "Updated", value: formatTraceDate(projectValue(project, "updated_at", "updatedAt", "updated")) },
      { label: "Retries", value: retryCount },
      { label: "Error", value: projectValue(project, "error", "message") }
    ].filter((field) => field.value !== "" && field.value !== undefined && field.value !== null);
    if (!runFields.length) runFields.push({ label: "Record", value: "No run metadata was recorded." });

    elements.projectTraceSections.replaceChildren(
      traceSection("Text model", "", { fields: modelFields, meta: traceText(modelFields.find((field) => field.label === "Selected model")?.value) || "Audit metadata" }),
      traceSection("System prompt", systemPrompt, { code: false }),
      traceSection("User prompt", userPrompt, { code: false }),
      traceSection("JSON schema", schema, { json: true }),
      traceSection("Raw model response", rawResponse, { json: true }),
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

  function renderProject(project) {
    if (!project) return;
    const scenes = projectScenes(project);
    const status = String(projectValue(project, "status") || "queued").toLowerCase();
    const id = projectID(project);
    const progress = projectProgress(project, scenes);
    state.currentProjectID = id;
    elements.statusEmpty.hidden = true;
    elements.statusActive.hidden = true;
    elements.projectStatus.hidden = false;
    elements.statusBadge.className = `badge ${statusClass(status)}`;
    elements.statusBadge.textContent = status.replaceAll("_", " ");
    elements.projectProgressWrap.hidden = ["completed", "complete", "failed"].includes(status);
    elements.projectProgressBar.style.width = `${progress}%`;
    elements.projectProgressBar.style.animation = "none";
    elements.projectStatusDetail.textContent = status === "completed" || status === "complete"
      ? "Your 30-second video is ready."
      : status === "failed" ? "The project could not be completed." : `Project progress: ${progress}%`;
    const error = projectValue(project, "error", "message");
    elements.projectError.hidden = !error;
    elements.projectError.textContent = error || "";
    const hasFailedScene = scenes.some((scene) => ["failed", "error"].includes(String(projectValue(scene, "status")).toLowerCase()));
    elements.retryProject.hidden = status !== "failed" || hasFailedScene;

    renderProjectPipeline(project, scenes, status);
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

    const videoReady = Boolean(projectValue(project, "video_ready", "final_video_ready")) || status === "completed" || status === "complete";
    elements.projectFinal.hidden = !videoReady || !id;
    if (videoReady && id) {
      const videoURL = `/api/projects/${encodeURIComponent(id)}/video`;
      elements.projectVideo.src = videoURL;
      elements.projectDownload.href = `${videoURL}?download=1`;
    }
  }

  async function pollProject(id, delay = 0) {
    stopProjectPolling();
    state.currentProjectID = id;
    state.projectPollTimer = window.setTimeout(async () => {
      if (!state.authenticated || state.currentProjectID !== id || state.mode !== "project") return;
      try {
        const payload = await request(`/api/projects/${encodeURIComponent(id)}`);
        const project = payload && payload.project ? payload.project : payload;
        renderProject(project);
        const status = String(projectValue(project, "status")).toLowerCase();
        if (!["completed", "complete", "failed", "error"].includes(status)) pollProject(id, 3000);
        else {
          await loadProjects(false);
          toast(status === "failed" || status === "error" ? "Project failed." : "30-second video saved.", status === "failed" || status === "error");
        }
      } catch (error) {
        if (error.status === 401) return;
        elements.projectError.hidden = false;
        elements.projectError.textContent = error.message;
        pollProject(id, 5000);
      }
    }, delay);
  }

  async function retryProjectScene(id, number, button) {
    setButtonBusy(button, true, "Retryingâ€¦");
    try {
      const payload = await request(`/api/projects/${encodeURIComponent(id)}/scenes/${encodeURIComponent(number)}/retry`, { method: "POST" });
      renderProject(payload && payload.project ? payload.project : payload);
      toast(`Scene ${number} retry submitted.`);
      pollProject(id, 1000);
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
      renderProject(payload && payload.project ? payload.project : payload);
      toast("Project retry started.");
      pollProject(id, 1000);
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
      state.projects = Array.isArray(payload) ? payload : (Array.isArray(payload && payload.projects) ? payload.projects : []);
      const terminal = (project) => ["completed", "complete", "failed", "error"].includes(String(projectValue(project, "status")).toLowerCase());
      const current = state.projects.find((project) => projectID(project) === state.currentProjectID);
      if (!current || terminal(current)) {
        const active = state.projects.find((project) => !terminal(project));
        state.currentProjectID = active ? projectID(active) : "";
      }
      setGenerationMode(state.mode);
      elements.projectHistory.replaceChildren();
      if (state.projects.length) {
        elements.projectHistory.append(make("h3", { className: "project-history-title", text: "30-second projects" }));
        const list = make("div", { className: "project-history-list" });
        state.projects.forEach((project) => {
          const id = projectID(project);
          const item = make("article", { className: "project-history-item" });
          const copy = make("div");
          copy.append(make("strong", { text: projectValue(project, "topic", "title", "story") || "Untitled project" }));
          copy.append(make("p", { text: `${String(projectValue(project, "status") || "queued").replaceAll("_", " ")} · ${formatDate(projectValue(project, "created_at", "createdAt"))}` }));
          item.append(copy);
          const open = make("button", { className: "button secondary", type: "button", text: "Open" });
          open.addEventListener("click", () => { setGenerationMode("project"); navigate("generate"); renderProject(project); pollProject(id, 0); });
          item.append(open);
          list.append(item);
        });
        elements.projectHistory.append(list);
        if (state.currentProjectID && state.mode === "project") {
          const active = state.projects.find((project) => projectID(project) === state.currentProjectID);
          if (active) {
            setGenerationMode("project");
            renderProject(active);
            const activeStatus = String(projectValue(active, "status")).toLowerCase();
            if (!["completed", "complete", "failed", "error"].includes(activeStatus)) pollProject(state.currentProjectID, 0);
          }
        }
      }
    } catch (error) {
      if (notify && error.status !== 401 && error.status !== 404) toast(error.message, true);
    }
  }

  async function submitProject() {
    const topic = elements.projectTopic.value.trim();
    elements.projectTopicError.textContent = "";
    if (!topic) {
      elements.projectTopicError.textContent = "Enter a topic or story idea.";
      elements.projectTopic.focus();
      return;
    }
    if (!elements.model.value) {
      toast("Select an available model first.", true);
      return;
    }
    setButtonBusy(elements.generate, true, "Building projectâ€¦");
    elements.statusEmpty.hidden = true;
    elements.projectStatus.hidden = false;
    elements.statusBadge.className = "badge queued";
    elements.statusBadge.textContent = "Submitting";
    elements.projectStatusDetail.textContent = "Generating story and five-scene scriptâ€¦";
    elements.projectError.hidden = true;
    elements.projectScenes.replaceChildren();
    try {
      const payload = await request("/api/projects", { method: "POST", body: JSON.stringify({ topic, model: elements.model.value }) });
      const project = payload && payload.project ? payload.project : payload;
      const id = projectID(project);
      renderProject(project);
      if (!id) throw new APIError("Project was created without an id.", 500);
      elements.projectTopic.value = "";
      elements.projectTopicError.textContent = "";
      toast("Project submitted. Script generation has started.");
      pollProject(id, 1000);
      loadProjects(false);
    } catch (error) {
      elements.projectError.hidden = false;
      elements.projectError.textContent = error.message;
      elements.statusBadge.className = "badge failed";
      elements.statusBadge.textContent = "Failed";
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.generate, false);
      elements.generate.disabled = !elements.model.value;
    }
  }

  async function submitGeneration(event) {
    event.preventDefault();
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
      return;
    }

    const requestBody = { prompt, model: elements.model.value };
    if (elements.duration.value) requestBody.duration = Number(elements.duration.value);
    if (elements.resolution.value) requestBody.resolution = elements.resolution.value;
    if (elements.aspectRatio.value) requestBody.aspect_ratio = elements.aspectRatio.value;
    if (elements.generateAudio.checked) requestBody.generate_audio = true;

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
      setStatusRecord(record);
      elements.generatorForm.reset();
      elements.promptCount.textContent = "0 / 4000";
      elements.promptError.textContent = "";
      updateModelOptions();
      toast("Generation submitted.");
      pollGeneration(record.id, 1000);
      loadHistory(false);
    } catch (error) {
      elements.statusBadge.className = "badge failed";
      elements.statusBadge.textContent = "Failed";
      elements.progressWrap.hidden = true;
      elements.statusDetail.textContent = "The request was not submitted.";
      elements.statusError.textContent = error.message;
      elements.statusError.hidden = false;
      if (error.status !== 401) toast(error.message, true);
    } finally {
      setButtonBusy(elements.generate, false);
      elements.generate.disabled = !elements.model.value;
    }
  }

  function renderProviderSettings(settings) {
    state.videoProvider = settings.video_provider === "modal" ? "modal" : "openrouter";
    elements.videoProvider.value = state.videoProvider;
    elements.modalSettings.hidden = state.videoProvider !== "modal";
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
  }

  async function loadSettings(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    try {
      const settings = await request("/api/settings");
      renderProviderSettings(settings || {});
    } catch (error) {
      elements.keyState.className = "badge failed";
      elements.keyState.textContent = "Unavailable";
      if (notify && error.status !== 401) toast(error.message, true);
    }
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

  async function persistVideoModel(modelID, notify = false) {
    if (!modelID || !state.videoProvider) return;
    try {
      await request("/api/settings/video-model", {
        method: "PUT",
        body: JSON.stringify({ provider: state.videoProvider, model: modelID })
      });
    } catch (error) {
      if (notify && error.status !== 401) toast(error.message, true);
    }
  }

  async function saveModalConfig(event) {
    event.preventDefault();
    const button = $("button[type='submit']", elements.modalConfigForm);
    setButtonBusy(button, true, "Testing Modal…");
    try {
      await request("/api/settings/modal", {
        method: "PUT",
        body: JSON.stringify({
          base_url: elements.modalBaseURL.value.trim(),
          api_key: elements.modalAPIKey.value
        })
      });
      elements.modalAPIKey.value = "";
      await loadSettings(false);
      toast("Modal settings tested and saved.");
      if (state.videoProvider === "modal") await loadModels(false);
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
        await Promise.allSettled([loadHistory(false, false), loadModels(false), loadSettings(false)]);
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
        await Promise.allSettled([loadHistory(false, false), loadModels(false)]);
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
    elements.retryProject.addEventListener("click", retryCurrentProject);
    elements.modeOptions.forEach((button) => button.addEventListener("click", () => setGenerationMode(button.dataset.mode)));
    elements.apiKeyForm.addEventListener("submit", saveAPIKey);
    elements.modalConfigForm.addEventListener("submit", saveModalConfig);
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
      } else if (event.key === "Escape") {
        closeModelMenu();
        elements.modelTrigger.focus();
      }
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
      if (state.currentGenerationID) pollGeneration(state.currentGenerationID, 0);
    });
    elements.menuButton.addEventListener("click", () => {
      const open = elements.sidebar.classList.toggle("open");
      elements.menuButton.setAttribute("aria-expanded", String(open));
    });
    $$(".nav-item").forEach((button) => button.addEventListener("click", () => navigate(button.dataset.view)));
    $$('[data-go]').forEach((button) => button.addEventListener("click", () => navigate(button.dataset.go)));
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        elements.sidebar.classList.remove("open");
        elements.menuButton.setAttribute("aria-expanded", "false");
      }
    });
    document.addEventListener("visibilitychange", () => {
      if (document.hidden) {
        stopHistoryRefresh();
      } else if (state.authenticated && !state.mustChangePassword) {
        loadHistory(false, false);
        loadProjects(false);
      }
    });
  }

  async function bootstrap() {
    bindEvents();
    setGenerationMode("project");
    elements.menuButton.setAttribute("aria-expanded", "false");
    try {
      const session = await request("/api/session", {}, true);
      showAuthenticated(session.username, session.must_change_password);
      if (!state.mustChangePassword) {
        await loadSettings(false);
        await Promise.allSettled([loadHistory(false, false), loadProjects(false), loadModels(false)]);
      }
    } catch (error) {
      showLoggedOut();
    }
  }

  bootstrap();
})();
