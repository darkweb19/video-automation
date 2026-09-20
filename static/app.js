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
    appShell: $("#app-shell"),
    accountName: $("#account-name"),
    logout: $("#logout"),
    sidebar: $(".sidebar"),
    menuButton: $("#menu-button"),
    pageKicker: $("#page-kicker"),
    pageTitle: $("#page-title"),
    model: $("#model"),
    duration: $("#duration"),
    aspectRatio: $("#aspect-ratio"),
    prompt: $("#prompt"),
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
    passwordForm: $("#password-form"),
    currentPassword: $("#current-password"),
    newPassword: $("#new-password"),
    toast: $("#toast")
  };

  const viewMeta = {
    overview: { title: "Overview", kicker: "Workspace" },
    generate: { title: "Generate", kicker: "Create" },
    history: { title: "History", kicker: "Library" },
    settings: { title: "Settings", kicker: "Security" }
  };

  const state = {
    models: [],
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
    toastTimer: 0,
    authenticated: false,
    mustChangePassword: false
  };

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
    stopHistoryRefresh();
    state.authenticated = false;
    state.currentGenerationID = "";
    state.generations = [];
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
    window.setTimeout(() => elements.loginUsername.focus(), 0);
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
    if (view === "history" && !state.mustChangePassword) loadHistory(false);
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
    if (!model) {
      fillSelect(elements.duration, [], String, "Provider default");
      fillSelect(elements.aspectRatio, [], String, "Provider default");
      elements.generate.disabled = true;
      updateEstimate();
      return;
    }

    fillSelect(elements.duration, model.durations, (value) => `${value} seconds`, "Provider default");
    fillSelect(elements.aspectRatio, model.aspect_ratios, String, "Provider default");
    elements.generate.disabled = false;
    updateEstimate();
  }

  function updateEstimate() {
    const model = selectedModel();
    const pricing = modelPricing(model);
    if (!model || pricing.price === null) {
      elements.costEstimate.textContent = "Unavailable";
      return;
    }
    if (pricing.unit === "generation") {
      elements.costEstimate.textContent = `${formatUSD(pricing.price)}/generation`;
      return;
    }
    const duration = Number(elements.duration.value);
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
    elements.generate.disabled = true;
    elements.model.replaceChildren();
    const loadingOption = make("option", { text: "Loading models…" });
    loadingOption.value = "";
    elements.model.append(loadingOption);
    try {
      const payload = await request("/models");
      state.models = Array.isArray(payload && payload.models) ? payload.models : [];
      elements.model.replaceChildren();
      if (!state.models.length) {
        const option = make("option", { text: "No video models available" });
        option.value = "";
        elements.model.append(option);
        updateModelOptions();
        return;
      }
      state.models.forEach((model) => {
        const option = make("option", { text: modelLabel(model) });
        option.value = model.id;
        elements.model.append(option);
      });
      elements.model.disabled = false;
      updateModelOptions();
      renderHistory();
      renderRecent();
    } catch (error) {
      state.models = [];
      elements.model.replaceChildren();
      const option = make("option", {
        text: error.status === 422 ? "Configure an API key in Settings" : "Models unavailable"
      });
      option.value = "";
      elements.model.append(option);
      updateModelOptions();
      if (notify && error.status !== 422 && error.status !== 401) toast(error.message, true);
    }
  }

  function statusBadge(status) {
    const normalized = status || "unknown";
    return make("span", { className: `badge ${statusClass(normalized)}`, text: normalized.replaceAll("_", " ") });
  }

  function statusClass(status) {
    const supported = new Set(["completed", "queued", "processing", "downloading", "download_failed", "failed", "neutral"]);
    return supported.has(status) ? status : "neutral";
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
      queued: "Your request is queued with OpenRouter.",
      processing: "OpenRouter is generating your video.",
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

  async function submitGeneration(event) {
    event.preventDefault();
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
    if (elements.aspectRatio.value) requestBody.aspect_ratio = elements.aspectRatio.value;

    setButtonBusy(elements.generate, true, "Submitting…");
    elements.statusEmpty.hidden = true;
    elements.statusActive.hidden = false;
    elements.statusBadge.className = "badge queued";
    elements.statusBadge.textContent = "Submitting";
    elements.progressWrap.hidden = false;
    elements.statusDetail.textContent = "Sending your request to OpenRouter…";
    elements.statusError.hidden = true;
    elements.generatedVideo.hidden = true;
    elements.generatedVideo.removeAttribute("src");
    elements.generatedVideo.load();
    elements.openVideo.hidden = true;

    try {
      const record = await request("/generate", { method: "POST", body: JSON.stringify(requestBody) });
      setStatusRecord(record);
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

  async function loadSettings(notify = true) {
    if (!state.authenticated || state.mustChangePassword) return;
    try {
      const settings = await request("/api/settings");
      const configured = Boolean(settings && settings.api_key_configured);
      elements.keyState.className = `badge ${configured ? "completed" : "neutral"}`;
      elements.keyState.textContent = configured ? "Configured" : "Not configured";
      elements.maskedKey.textContent = configured ? (settings.api_key_masked || "Configured") : "Not configured";
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
      const settings = await request("/api/settings/api-key", {
        method: "PUT",
        body: JSON.stringify({ api_key: key })
      });
      elements.apiKey.value = "";
      elements.keyState.className = "badge completed";
      elements.keyState.textContent = "Configured";
      elements.maskedKey.textContent = settings.api_key_masked || "Configured";
      toast("API key tested and saved.");
      await loadModels(false);
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
        await Promise.allSettled([loadHistory(false, false), loadModels(false), loadSettings(false)]);
      }
    } catch (error) {
      elements.loginError.textContent = error.message;
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
    elements.logout.addEventListener("click", logout);
    elements.generatorForm.addEventListener("submit", submitGeneration);
    elements.apiKeyForm.addEventListener("submit", saveAPIKey);
    elements.passwordForm.addEventListener("submit", updatePassword);
    elements.refreshHistory.addEventListener("click", () => loadHistory(true));
    elements.model.addEventListener("change", updateModelOptions);
    elements.duration.addEventListener("change", updateEstimate);
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
      }
    });
  }

  async function bootstrap() {
    bindEvents();
    elements.menuButton.setAttribute("aria-expanded", "false");
    try {
      const session = await request("/api/session", {}, true);
      showAuthenticated(session.username, session.must_change_password);
      if (!state.mustChangePassword) {
        await Promise.allSettled([loadHistory(false, false), loadModels(false), loadSettings(false)]);
      }
    } catch (error) {
      showLoggedOut();
    }
  }

  bootstrap();
})();
