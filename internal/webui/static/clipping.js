(function (root) {
  "use strict";

  const GEMINI_SCHEMA_VERSION = "framevault.clipping.analysis.v3";
  const CONTENT_TYPES = ["general", "podcast", "comedy", "gaming", "movie"];
  const IMPORT_ERROR_COPY = {
    youtube_import_unavailable: "The YouTube importer is unavailable right now. Try again later or ask the operator to check the importer setup.",
    youtube_bot_challenge: "YouTube requested verification of automated access from this server. This import cannot continue; try again later.",
    youtube_video_unavailable: "This video could not be opened from its public link. Check that it plays without signing in or age verification.",
    youtube_format_unavailable: "No supported original video and audio format was available for this video.",
    youtube_original_audio_unavailable: "The original audio track was not available for this video.",
    youtube_duration_limit_exceeded: "This video is longer than the four-hour import limit.",
    youtube_size_limit_exceeded: "This video is larger than the 20 GiB import limit.",
    youtube_media_invalid: "The imported file did not pass video validation.",
    source_duration_limit_exceeded: "This video is longer than the four-hour import limit.",
    source_size_limit_exceeded: "This video is larger than the 20 GiB import limit."
  };
  const JOB_STATUS_COPY = {
    queued: "Queued",
    running: "Analysis in progress",
    completed: "Analysis complete",
    failed: "Analysis failed",
    canceled: "Canceled",
    cancelled: "Canceled",
    uncertain: "Outcome unknown",
    outcome_unknown: "Outcome unknown",
    rate_limited_wait: "Waiting after provider rate limit",
    submitting: "Submitting analysis request"
  };
  const CONTENT_TYPE_LABELS = {
    general: "General",
    podcast: "Podcast",
    comedy: "Comedy",
    gaming: "Gaming",
    movie: "Movie"
  };

  function youtubeVideoID(value) {
    const raw = String(value || "").trim();
    if (!raw || raw.length > 8192 || /[\x00-\x1f\x7f#\\]/.test(raw) || /%(?![0-9a-f]{2})/i.test(raw)) return "";
    if (/^[A-Za-z0-9_-]{11}$/.test(raw)) return raw;
    const withoutScheme = raw.replace(/^https?:\/\//i, "");
    const authority = withoutScheme.split(/[/?]/, 1)[0];
    const rawPath = withoutScheme.slice(authority.length).split("?", 1)[0];
    if (authority.includes("%") || authority.includes("@") || /(?:^|\/)(?:\.|%2e){1,2}(?:\/|$)/i.test(rawPath)) return "";
    const port = authority.match(/:([^:]*)$/);
    if (port && port[1] && port[1] !== "443") return "";
    let url;
    try {
      url = new URL(raw.includes("://") ? raw : "https://" + raw);
    } catch (_error) {
      return "";
    }
    const host = url.hostname.toLowerCase();
    const hosts = new Set([
      "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com",
      "youtube-nocookie.com", "www.youtube-nocookie.com", "youtu.be", "www.youtu.be"
    ]);
    if (!hosts.has(host) || url.protocol !== "https:"
      || url.username || url.password
      || (url.port && url.port !== "443") || url.search.includes(";")) return "";
    for (const key of url.searchParams.keys()) {
      if (["list", "playlist", "playlist_id", "video_ids", "start_radio", "index"].includes(key.toLowerCase())) return "";
    }
    let path;
    try { path = decodeURIComponent(url.pathname); } catch (_error) { return ""; }
    let id = "";
    if (host === "youtu.be" || host === "www.youtu.be") {
      id = path.replace(/^\/+|\/+$/g, "");
      if (id.includes("/")) return "";
    } else if (path === "/watch" || path === "/watch/") {
      const values = url.searchParams.getAll("v");
      if (values.length === 1) id = values[0];
    } else {
      const match = path.match(/^\/(?:shorts|embed|live|v)\/([^/]+)\/?$/);
      if (match) id = match[1];
    }
    return /^[A-Za-z0-9_-]{11}$/.test(id) ? id : "";
  }

  function safeID(value) {
    const id = typeof value === "string" || typeof value === "number" ? String(value) : "";
    return /^[A-Za-z0-9_-]{1,128}$/.test(id) ? id : "";
  }

  function formatDuration(milliseconds) {
    const value = Number(milliseconds);
    if (!Number.isFinite(value) || value < 0) return "";
    const total = Math.floor(value / 1000);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const seconds = total % 60;
    return hours
      ? hours + ":" + String(minutes).padStart(2, "0") + ":" + String(seconds).padStart(2, "0")
      : minutes + ":" + String(seconds).padStart(2, "0");
  }

  function formatBytes(bytes) {
    const value = Number(bytes);
    if (!Number.isFinite(value) || value <= 0) return "size limit unavailable";
    const gib = value / (1024 * 1024 * 1024);
    return (gib >= 10 ? Math.round(gib) : Math.round(gib * 10) / 10) + " GiB";
  }

  const pure = { youtubeVideoID, safeID, formatDuration, formatBytes };
  if (typeof module !== "undefined" && module.exports) module.exports = pure;
  if (!root || !root.document) return;

  const document = root.document;
  const $ = (id) => document.getElementById(id);
  const ui = {
    view: $("clipping-view"),
    refresh: $("clipping-refresh"),
    importForm: $("clipping-link-form"),
    link: $("clipping-link"),
    permission: $("clipping-link-permission-check"),
    contentType: $("clipping-content-type"),
    minClipSeconds: $("clipping-min-clip-seconds"),
    maxClipSeconds: $("clipping-max-clip-seconds"),
    candidateLimit: $("clipping-candidate-limit"),
    importButton: $("clipping-link-submit"),
    importError: $("clipping-link-error"),
    optionsError: $("clipping-options-error"),
    importStatus: $("clipping-workflow-status"),
    importLimits: $("clipping-import-limits"),
    geminiReadiness: $("clipping-gemini-readiness"),
    sourceError: $("clipping-sources-error"),
    sourceCount: $("clipping-source-count"),
    sources: $("clipping-sources"),
    jobsError: $("clipping-jobs-error"),
    jobCount: $("clipping-job-count"),
    jobs: $("clipping-jobs"),
    analysisHeading: $("clipping-analysis-heading"),
    confirmDialog: $("clipping-confirm-dialog"),
    confirmTitle: $("clipping-confirm-title"),
    confirmDescription: $("clipping-confirm-description"),
    confirmError: $("clipping-confirm-error"),
    confirmCancel: $("clipping-confirm-cancel"),
    confirmDelete: $("clipping-confirm-delete")
  };

  const state = {
    active: false,
    lifecycle: 0,
    requestImpl: null,
    toastImpl: null,
    requestSequence: { config: 0, sources: 0, jobs: 0 },
    config: null,
    sources: [],
    sourceConsoleStatuses: new Map(),
    jobs: [],
    pendingSourceIDs: new Set(),
    pendingDeleteSourceIDs: new Set(),
    pendingDeleteJobIDs: new Set(),
    pendingCancelJobIDs: new Set(),
    pendingJobBaselines: new Map(),
    deletedSourceIDs: new Set(),
    deletedJobIDs: new Set(),
    jobTracking: new Map(),
    notifiedFailures: new Set(),
    sourceActionErrors: new Map(),
    jobActionErrors: new Map(),
    expandedResults: new Set(),
    resultDetails: new Map(),
    resultErrors: new Map(),
    loadingResults: new Set(),
    resultSequences: new Map(),
    unknownRequests: new Map(),
    urlSourceIDs: new Map(),
    uncertainSources: new Set(),
    sourcesLoaded: false,
    jobsLoaded: false,
    pendingConfirmation: null,
    geminiRequestPending: false
  };

  function current(lifecycle) {
    return state.active && state.lifecycle === lifecycle;
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function button(label, className, action) {
    const node = element("button", className, label);
    node.type = "button";
    node.addEventListener("click", action);
    return node;
  }

  function contentType(value) {
    const type = String(value || "").toLowerCase();
    return CONTENT_TYPES.includes(type) ? type : "general";
  }

  function geminiConfig() {
    return state.config && state.config.gemini && typeof state.config.gemini === "object"
      ? state.config.gemini : null;
  }

  function geminiLimits() {
    const config = geminiConfig();
    const limits = config && config.limits && typeof config.limits === "object" ? config.limits : null;
    if (!limits) return null;
    const minClipMS = Number(limits.min_clip_ms);
    const maxClipMS = Number(limits.max_clip_ms);
    const maxCandidates = Number(limits.max_candidates);
    if (!Number.isSafeInteger(minClipMS) || !Number.isSafeInteger(maxClipMS)
      || minClipMS < 0 || maxClipMS < minClipMS
      || !Number.isInteger(maxCandidates) || maxCandidates < 1 || maxCandidates > 10) return null;
    return { minClipMS, maxClipMS, maxCandidates, maxSourceDurationMS: Number(limits.max_source_duration_ms) };
  }

  function geminiReady() {
    const config = geminiConfig();
    return Boolean(config && config.configured === true && config.enabled === true && geminiLimits());
  }

  function isGeminiJob(job) {
    return String(job && job.provider || "").toLowerCase() === "gemini";
  }

  function formatRetryAt(value) {
    let milliseconds = NaN;
    if (typeof value === "number" && Number.isFinite(value)) milliseconds = value < 1e12 ? value * 1000 : value;
    else if (typeof value === "string" && value.trim()) milliseconds = Date.parse(value);
    if (!Number.isFinite(milliseconds)) return "";
    try { return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(milliseconds)); }
    catch (_error) { return ""; }
  }

  function retrySafetyCopy(job) {
    if (!isGeminiJob(job)) return "";
    const safety = String(job.retry_safety || "none");
    if (safety === "not_submitted_retryable") {
      if (terminalJobStatus(job.status)) return "This attempt was not submitted and has ended.";
      return "This attempt was not submitted. The service may retry it automatically when safe.";
    }
    if (safety === "rate_limited_wait") {
      const retryAt = formatRetryAt(job.retry_at);
      return "Gemini rate-limited this attempt. The service will wait" + (retryAt ? " until " + retryAt : "") + " before its bounded retry.";
    }
    if (safety === "response_received_terminal") {
      return "Gemini responded. This attempt will not be submitted again.";
    }
    if (safety === "outcome_unknown_no_replay") {
      return "The provider outcome is unknown. Do not resubmit this analysis. Refresh to check its status.";
    }
    if (safety === "terminal") return "This analysis is terminal. No provider retry is available.";
    return "";
  }

  function geminiErrorCopy(job) {
    const messages = {
      config_drift: "Analysis configuration changed before this request could be sent. Refresh and check provider readiness.",
      credentials_unavailable: "The Google provider identity is unavailable. Ask the operator to check provider setup.",
      source_inaccessible: "The provider could not access this public YouTube video. It may be unavailable or unsupported.",
      invalid_response: "The provider response did not match the saved analysis contract.",
      outcome_unknown: "The provider outcome is unknown. Do not resubmit this analysis; refresh to check its status.",
      request_not_submitted: terminalJobStatus(job && job.status)
        ? "This attempt was not submitted and has ended."
        : "This attempt was not submitted. The service may retry it automatically when safe.",
      rate_limited: "Gemini rate-limited this attempt. Review the retry status below.",
      provider_rejected_request: "Gemini rejected this request. Review the public link and provider status before starting another analysis.",
      response_blocked: "Gemini did not return an analysis for this video.",
      response_incomplete: "Gemini returned an incomplete analysis that could not be saved.",
      reported_source_too_long: "The video duration reported by Gemini is above the configured limit.",
      response_unusable: "Gemini returned an analysis that could not be used. Review its saved status before starting another request."
    };
    const code = String(job && (job.error_code || job.error) || "");
    return messages[code] || "Analysis could not be completed. Refresh to check the saved status.";
  }

  function sourceName(source) {
    if (source && source.kind === "youtube_url") return "YouTube video";
    const name = source && (source.title || source.original_name || source.name);
    return typeof name === "string" && name.trim() ? name.trim() : "YouTube video";
  }

  function importErrorCopy(source) {
    const code = String(source && source.error || "");
    return IMPORT_ERROR_COPY[code] || (code ? "This YouTube video could not be imported. Check that the public video is available and try again." : "");
  }

  function sourceStatus(source) {
    const status = String(source && source.status || "").toLowerCase();
    if (status === "importing") return "Preparing video";
    if (status === "ready" && source && source.kind === "youtube_url") return "Reference saved · unverified";
    if (status === "ready") return "Ready to analyze";
    if (status === "failed") return "Import failed";
    return status ? status.charAt(0).toUpperCase() + status.slice(1) : "Status unavailable";
  }

  function jobStatus(job) {
    const status = String(job && job.status || "").toLowerCase();
    if (isGeminiJob(job)) {
      const safety = String(job.retry_safety || "none");
      if (safety === "outcome_unknown_no_replay") return "Outcome unknown";
      if (safety === "rate_limited_wait") return "Waiting after provider rate limit";
      if (safety === "not_submitted_retryable") return "Automatic retry pending";
    }
    return JOB_STATUS_COPY[status] || "Status unavailable";
  }

  function geminiOptionsCopy(options) {
    if (!options || typeof options !== "object") return "Gemini analysis";
    const type = CONTENT_TYPE_LABELS[contentType(options.content_type)] || "General";
    const min = Number(options.min_clip_ms);
    const max = Number(options.max_clip_ms);
    const limit = Number(options.candidate_limit);
    const bounds = Number.isSafeInteger(min) && Number.isSafeInteger(max)
      ? " · " + min.toLocaleString() + "–" + max.toLocaleString() + " ms" : "";
    const candidates = Number.isSafeInteger(limit) ? " · up to " + limit + " suggestions" : "";
    return "Gemini · " + type + bounds + candidates;
  }

  function sourceProgress(source) {
    const progress = Number(source && source.progress);
    if (!Number.isFinite(progress)) return null;
    return Math.max(0, Math.min(100, Math.round(progress <= 1 ? progress * 100 : progress)));
  }

  function api(path, options) {
    if (!state.active || typeof state.requestImpl !== "function") {
      return Promise.reject(new Error("Sign in to use clipping."));
    }
    return state.requestImpl(path, options || {});
  }

  function showToast(message, isError) {
    if (typeof state.toastImpl === "function") state.toastImpl(message, Boolean(isError));
  }

  function toastError(error, fallback) {
    const message = error && typeof error.message === "string" && error.message
      ? error.message
      : fallback;
    if (!error || error.status !== 401) showToast(message, true);
    return message;
  }

  function closeConfirmation(confirmed) {
    const pending = state.pendingConfirmation;
    if (!pending) return;
    state.pendingConfirmation = null;
    ui.confirmCancel.disabled = false;
    ui.confirmDelete.disabled = false;
    ui.confirmDialog.removeAttribute("aria-busy");
    if (ui.confirmDialog.open) ui.confirmDialog.close(confirmed ? "confirm" : "cancel");
    pending.resolve(Boolean(confirmed));
  }

  function confirmAction(title, description, action, confirmLabel) {
    if (!ui.confirmDialog || typeof ui.confirmDialog.showModal !== "function" || state.pendingConfirmation) {
      if (ui.confirmError) {
        ui.confirmError.hidden = false;
        ui.confirmError.textContent = "Confirmation is unavailable. No item was deleted.";
      }
      return Promise.resolve(false);
    }
    ui.confirmTitle.textContent = title;
    ui.confirmDescription.textContent = description;
    ui.confirmError.textContent = "";
    ui.confirmError.hidden = true;
    ui.confirmCancel.disabled = false;
    ui.confirmDelete.disabled = false;
    ui.confirmDialog.removeAttribute("aria-busy");
    ui.confirmDelete.textContent = confirmLabel || "Delete";
    ui.confirmCancel.textContent = confirmLabel === "Cancel analysis" ? "Keep analysis" : "Cancel";
    return new Promise((resolve) => {
      state.pendingConfirmation = { resolve, action, running: false, confirmLabel: confirmLabel || "Delete" };
      try {
        ui.confirmDialog.showModal();
        ui.confirmCancel.focus?.({ preventScroll: true });
      } catch (_error) {
        state.pendingConfirmation = null;
        ui.confirmError.hidden = false;
        ui.confirmError.textContent = "Confirmation could not be opened. No item was deleted.";
        resolve(false);
      }
    });
  }

  async function runConfirmation() {
    const pending = state.pendingConfirmation;
    if (!pending || pending.running || typeof pending.action !== "function") return;
    pending.running = true;
    ui.confirmError.textContent = "";
    ui.confirmError.hidden = true;
    ui.confirmCancel.disabled = true;
    ui.confirmDelete.disabled = true;
    ui.confirmDelete.textContent = pending.confirmLabel === "Delete" ? "Deleting…" : "Canceling…";
    ui.confirmDialog.setAttribute("aria-busy", "true");
    try {
      const result = await pending.action();
      if (state.pendingConfirmation !== pending) return;
      if (!result || result.ok !== true) {
        ui.confirmError.textContent = result && result.message || "The action could not be completed. Close this dialog and check the saved item before trying again.";
        ui.confirmError.hidden = false;
        ui.confirmDelete.disabled = true;
        ui.confirmDelete.textContent = pending.confirmLabel;
        ui.confirmCancel.disabled = false;
        ui.confirmDialog.removeAttribute("aria-busy");
        pending.running = false;
        ui.confirmCancel.focus?.({ preventScroll: true });
        return;
      }
      closeConfirmation(true);
    } catch (_error) {
      if (state.pendingConfirmation !== pending) return;
      ui.confirmError.textContent = "The outcome could not be confirmed. Close this dialog and refresh saved items before trying again.";
      ui.confirmError.hidden = false;
      ui.confirmDelete.disabled = true;
      ui.confirmDelete.textContent = pending.confirmLabel;
      ui.confirmCancel.disabled = false;
      ui.confirmDialog.removeAttribute("aria-busy");
      pending.running = false;
      ui.confirmCancel.focus?.({ preventScroll: true });
    }
  }

  function cancelConfirmation(event) {
    const pending = state.pendingConfirmation;
    if (!pending) return;
    if (pending.running) {
      event?.preventDefault?.();
      return;
    }
    state.pendingConfirmation = null;
    pending.resolve(false);
  }

  function invalidateListLoads() {
    state.requestSequence.sources++;
    state.requestSequence.jobs++;
  }

  function clearPendingKeysForSource(sourceID) {
    const id = safeID(sourceID);
    if (!id) return;
    state.unknownRequests.delete("source:" + id);
    for (const [videoID, mappedID] of state.urlSourceIDs.entries()) {
      if (mappedID === id) state.urlSourceIDs.delete(videoID);
    }
  }

  function activeJobStatus(status) {
    return ["queued", "running", "uncertain", "outcome_unknown", "submitting", "rate_limited_wait"].includes(String(status || "").toLowerCase());
  }

  function terminalJobStatus(status) {
    return ["completed", "failed", "canceled", "cancelled"].includes(String(status || "").toLowerCase());
  }

  function analysisAttemptID(job) {
    const stages = Array.isArray(job && job.stages) ? job.stages : [];
    const stage = stages.find((item) => item && item.name === "analysis");
    const attemptID = stage && stage.attempt_id;
    return typeof attemptID === "string" && attemptID.trim() ? attemptID.trim() : "";
  }

  function observeJobStatus(job) {
    const id = safeID(job && job.id);
    if (!id || state.deletedJobIDs.has(id) || state.deletedSourceIDs.has(safeID(job && job.source_id))) return;
    const status = String(job && job.status || "").toLowerCase();
    const tracking = state.jobTracking.get(id) || { active: false, lastStatus: "", failureGeneration: 0 };
    if (activeJobStatus(status)) {
      if (!tracking.active && tracking.lastStatus === "failed") tracking.failureGeneration++;
      tracking.active = true;
      tracking.lastStatus = status;
    } else if (status === "failed") {
      const sourceID = safeID(job && job.source_id);
      const pendingBaseline = state.pendingJobBaselines.get(sourceID);
      if (!tracking.active && state.pendingSourceIDs.has(sourceID) && pendingBaseline && !pendingBaseline.has(id)) {
        tracking.active = true;
      }
      if (tracking.active) {
        const attemptID = analysisAttemptID(job);
        const failureID = attemptID || "transition-" + tracking.failureGeneration;
        const key = id + ":" + failureID;
        if (!state.notifiedFailures.has(key)) {
          state.notifiedFailures.add(key);
          showToast("Analysis failed. See the job for details.", true);
        }
      }
      tracking.active = false;
      tracking.lastStatus = "failed";
    } else {
      tracking.active = false;
      tracking.lastStatus = status;
    }
    state.jobTracking.set(id, tracking);
  }

  function markJobDeleted(jobID, sourceID) {
    const id = safeID(jobID);
    if (!id) return;
    const existing = state.jobs.find((job) => safeID(job && job.id) === id);
    if (existing && existing.retry_safety === "outcome_unknown_no_replay") {
      const source = safeID(sourceID || existing.source_id);
      if (source) state.uncertainSources.add(source);
    }
    state.deletedJobIDs.add(id);
    state.pendingDeleteJobIDs.delete(id);
    state.pendingCancelJobIDs.delete(id);
    state.jobActionErrors.delete(id);
    state.jobTracking.delete(id);
    state.expandedResults.delete(id);
    state.resultDetails.delete(id);
    state.resultErrors.delete(id);
    state.loadingResults.delete(id);
    state.resultSequences.set(id, (state.resultSequences.get(id) || 0) + 1);
    state.jobs = state.jobs.filter((job) => safeID(job && job.id) !== id);
  }

  function markSourceDeleted(sourceID) {
    const id = safeID(sourceID);
    if (!id) return;
    state.deletedSourceIDs.add(id);
    state.pendingDeleteSourceIDs.delete(id);
    state.pendingSourceIDs.delete(id);
    state.pendingJobBaselines.delete(id);
    clearPendingKeysForSource(id);
    state.sourceActionErrors.delete(id);
    for (const job of state.jobs.filter((item) => safeID(item && item.source_id) === id)) {
      markJobDeleted(job.id, id);
    }
    state.uncertainSources.delete(id);
    state.sources = state.sources.filter((source) => safeID(source && source.id) !== id);
  }

  function focusFirstAction(container, focusKey) {
    const visit = (node) => {
      if (node && node.dataset && node.dataset.clippingFocusKey === focusKey) return node;
      for (const child of node && node.children || []) {
        const found = visit(child);
        if (found) return found;
      }
      return null;
    };
    visit(container)?.focus?.({ preventScroll: true });
  }

  function focusTarget(container, focusKey) {
    if (!container || !focusKey) return null;
    const visit = (node) => {
      if (node.dataset && node.dataset.clippingFocusKey === focusKey) return node;
      for (const child of node.children || []) {
        const found = visit(child);
        if (found) return found;
      }
      return null;
    };
    return visit(container);
  }

  function captureFocus(container, idKey) {
    const active = document.activeElement;
    let row = active;
    while (row && row !== container && !row.dataset?.[idKey]) row = row.parentElement || row.parentNode || row.parent;
    if (!row || row === container || !row.dataset?.[idKey] || !active.dataset?.clippingFocusKey) return null;
    return { id: row.dataset[idKey], focusKey: active.dataset.clippingFocusKey };
  }

  function restoreFocus(container, idKey, focus) {
    if (!focus) return;
    const row = Array.from(container.children || []).find((node) => node.dataset && node.dataset[idKey] === focus.id);
    focusTarget(row, focus.focusKey)?.focus?.({ preventScroll: true });
  }

  function renderProgress(parent, label, value) {
    const percent = value === null ? null : value;
    const wrapper = element("div", "clipping-progress");
    const track = element("div", "clipping-progress-track");
    const bar = element("div", "clipping-progress-bar");
    bar.style.width = (percent === null ? 8 : percent) + "%";
    bar.setAttribute("role", "progressbar");
    bar.setAttribute("aria-label", label);
    bar.setAttribute("aria-valuemin", "0");
    bar.setAttribute("aria-valuemax", "100");
    if (percent !== null) bar.setAttribute("aria-valuenow", String(percent));
    track.append(bar);
    wrapper.append(track, element("span", "clipping-progress-copy", percent === null ? label : label + " · " + percent + "%"));
    parent.append(wrapper);
  }

  function latestJobForSource(sourceID, provider) {
    const matching = state.jobs.filter((job) => String(job && job.source_id) === sourceID
      && (!provider || String(job && job.provider || "").toLowerCase() === provider));
    return matching.reduce((latest, job) => {
      if (!latest) return job;
      const latestCreated = Number(latest.created_at);
      const candidateCreated = Number(job.created_at);
      return Number.isFinite(candidateCreated) && (!Number.isFinite(latestCreated) || candidateCreated > latestCreated)
        ? job : latest;
    }, null);
  }

  function sourceAnalysisState(sourceID) {
    const unresolved = state.unknownRequests.get("source:" + sourceID);
    if (unresolved) return { job: null, canStart: true, resend: true };
    if (state.unknownRequests.size) return { job: null, canStart: false, otherUnknown: true };
    if (state.uncertainSources.has(sourceID)) return { job: null, canStart: false, unknown: true };
    if (!state.jobsLoaded) return { job: null, canStart: false, loading: true };
    const active = state.jobs.find((job) => {
      if (String(job && job.source_id) !== sourceID) return false;
      const status = String(job && job.status || "").toLowerCase();
      const safety = String(job && job.retry_safety || "none");
      return activeJobStatus(status) || safety === "rate_limited_wait" || safety === "outcome_unknown_no_replay"
        || (safety === "not_submitted_retryable" && !terminalJobStatus(status));
    });
    if (active) return { job: active, canStart: false };
    const latest = latestJobForSource(sourceID, "gemini");
    if (!latest) return { job: null, canStart: true };
    const status = String(latest.status || "").toLowerCase();
    const safety = String(latest.retry_safety || "none");
    if (activeJobStatus(status) || safety === "rate_limited_wait" || safety === "outcome_unknown_no_replay") {
      return { job: latest, canStart: false };
    }
    const knownOutcome = ["none", "terminal", "response_received_terminal", "not_submitted_retryable"].includes(safety);
    return { job: latest, canStart: terminalJobStatus(status) && knownOutcome };
  }

  function analysisLockedForSource(sourceID) {
    return state.unknownRequests.has("source:" + sourceID);
  }

  function sourceJobActionLabel(job, canStart) {
    if (canStart) return job ? "Analyze again" : "Analyze with Gemini";
    if (!job) return "Analyze with Gemini";
    const safety = String(job.retry_safety || "none");
    if (safety === "outcome_unknown_no_replay") return "Outcome unknown";
    if (safety === "rate_limited_wait") return "Retry scheduled automatically";
    if (safety === "not_submitted_retryable") return "Analysis retrying automatically";
    return jobStatus(job);
  }

  function renderSources() {
    const focus = captureFocus(ui.sources, "sourceId");
    const fragment = document.createDocumentFragment();
    ui.sourceCount.textContent = String(state.sources.length);
    if (!state.sources.length) {
      fragment.append(element("p", "empty", "Saved references and legacy sources will appear here."));
    }
    for (const source of state.sources) {
      const id = safeID(source && source.id);
      if (!id) continue;
      const status = String(source.status || "").toLowerCase();
      const name = sourceName(source);
      const isReference = source.kind === "youtube_url";
      const row = element("article", "clipping-source-row");
      row.dataset.sourceId = id;
      const main = element("div", "clipping-source-main");
      const heading = element("div", "clipping-source-heading");
      heading.append(element("strong", "clipping-source-title", name));
      const badge = element("span", "badge clipping-source-status", sourceStatus(source));
      badge.dataset.state = status;
      heading.append(badge);
      main.append(heading);
      const duration = !isReference && Number(source.duration_ms) > 0 ? formatDuration(source.duration_ms) : "";
      if (duration) main.append(element("p", "clipping-source-meta", "Imported duration " + duration));
      if (!isReference && status === "importing") renderProgress(main, "Preparing legacy source", sourceProgress(source));
      const error = state.sourceActionErrors.get(id) || String(source.error_message || "").trim() || importErrorCopy(source);
      if (error) main.append(element("p", "clipping-row-error", error));
      const actions = element("div", "clipping-source-actions");
      const deleting = state.pendingDeleteSourceIDs.has(id);
      const supportedYouTubeSource = source.rights_attested === true && ["youtube_url", "youtube_original_file"].includes(String(source.kind || ""));
      if (status === "ready" && supportedYouTubeSource) {
        const analysisState = sourceAnalysisState(id);
        const job = analysisState.job;
      const actionLabel = analysisState.resend ? "Resend same request" : analysisState.otherUnknown ? "Resolve previous request"
          : analysisState.unknown ? "Outcome unknown"
          : analysisState.loading ? "Loading analysis status…"
            : sourceJobActionLabel(job, analysisState.canStart);
        const analyze = button(state.pendingSourceIDs.has(id) ? "Saving request…" : actionLabel, "button primary clipping-action",
          () => { if (sourceAnalysisState(id).canStart && !state.pendingSourceIDs.has(id)) void createGeminiJob({ sourceID: id }); });
        analyze.dataset.clippingFocusKey = "analyze";
        analyze.setAttribute("aria-label", actionLabel + " for " + name);
        analyze.disabled = Boolean(!analysisState.canStart || state.geminiRequestPending || state.pendingSourceIDs.has(id) || deleting || !geminiReady());
        if (!geminiReady()) analyze.title = "Gemini must be configured and enabled before requesting analysis.";
        if (analysisState.resend) analyze.title = "Resend the original request with the same idempotency key.";
        else if (analysisState.otherUnknown) analyze.title = "Resend the previous request before starting another analysis.";
        else if (analysisState.unknown) analyze.title = "The provider outcome is unknown. This analysis cannot be submitted again.";
        else if (analysisState.loading) analyze.title = "Saved analysis status is loading. Refresh before requesting another analysis.";
        else if (job && !analysisState.canStart) analyze.title = retrySafetyCopy(job)
          || "A previous analysis is still active or has ended without a reusable result.";
        actions.append(analyze);
        if (job && analysisState.canStart) main.append(element("p", "clipping-source-meta", "A new analysis may incur provider charges."));
      }
      const removeLabel = isReference ? "Delete reference" : "Delete video";
      const remove = button(deleting ? "Deleting…" : removeLabel, "button secondary clipping-action clipping-delete-action",
        () => { void deleteSource(id); });
      remove.dataset.clippingFocusKey = "delete-source";
      remove.setAttribute("aria-label", (deleting ? "Deleting saved source " : "Delete saved source ") + name);
      remove.setAttribute("aria-disabled", String(deleting || state.pendingSourceIDs.has(id) || analysisLockedForSource(id)));
      if (deleting) remove.setAttribute("aria-busy", "true");
      remove.disabled = deleting || state.pendingSourceIDs.has(id) || analysisLockedForSource(id);
      actions.append(remove);
      row.append(main, actions);
      fragment.append(row);
    }
    ui.sources.replaceChildren(fragment);
    restoreFocus(ui.sources, "sourceId", focus);
  }

  function statusProgress(job) {
    const value = Number(job && job.progress);
    if (!Number.isFinite(value)) return 0;
    return Math.max(0, Math.min(100, Math.round(value <= 1 ? value * 100 : value)));
  }

  function safeText(value, maxLength) {
    if (value === null || value === undefined) return "";
    if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
      return String(value).slice(0, maxLength || 1000);
    }
    try { return JSON.stringify(value).slice(0, maxLength || 1000); } catch (_error) { return ""; }
  }

  function sourceTime(start, end) {
    const from = Number(start);
    const to = Number(end);
    if (!Number.isSafeInteger(from) || from < 0) return "Source time unavailable";
    const timestamp = (milliseconds) => {
      const base = formatDuration(milliseconds);
      return (milliseconds < 3_600_000 ? base.padStart(5, "0") : base) + "." + String(milliseconds % 1000).padStart(3, "0");
    };
    return timestamp(from) + (Number.isSafeInteger(to) && to > from ? "–" + timestamp(to) : "");
  }

  function renderMetadata(parent, title, value) {
    if (!value || typeof value !== "object" || Array.isArray(value)) return;
    const entries = Object.entries(value).filter(([, item]) => item !== null && item !== undefined).slice(0, 40);
    if (!entries.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", title));
    const list = element("dl", "clipping-artifact-metadata");
    for (const [key, item] of entries) list.append(element("dt", "", key), element("dd", "", safeText(item, 1000)));
    section.append(list);
    parent.append(section);
  }

  function renderEvidence(parent, evidence) {
    if (!Array.isArray(evidence) || !evidence.length) return;
    const list = element("ul", "clipping-artifact-list");
    for (const item of evidence.slice(0, 100)) {
      if (typeof item === "string") {
        list.append(element("li", "", item));
        continue;
      }
      if (!item || typeof item !== "object") continue;
      const parts = [item.kind, sourceTime(item.start_ms, item.end_ms), item.text, item.detail]
        .filter((part) => part !== null && part !== undefined && String(part).trim());
      if (parts.length) list.append(element("li", "", parts.join(" · ")));
    }
    if (list.children.length) parent.append(list);
  }

  function renderTranscript(parent, data) {
    const transcript = data && data.transcript && typeof data.transcript === "object" ? data.transcript : data;
    const segments = Array.isArray(transcript && transcript.segments) ? transcript.segments : [];
    const hasStatus = transcript && (typeof transcript.available === "boolean" || typeof transcript.status === "string");
    const audioStatus = typeof data.audio_analysis_status === "string" ? data.audio_analysis_status : "";
    if (!segments.length && !hasStatus && !audioStatus) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", segments.length ? "Original-script transcript" : "Transcript availability"));
    if (hasStatus || audioStatus) {
      const noAudio = transcript.available === false && transcript.status === "unavailable_no_audio_stream"
        && audioStatus === "skipped_no_audio_stream";
      let description = "";
      if (noAudio) description = "No audio stream was available; audio analysis and transcription were skipped.";
      else if (!segments.length && transcript.available === true) description = "Transcript is available, but no segments were saved.";
      else if (!segments.length && transcript.available === false) description = "Transcript is unavailable.";
      else if (!segments.length && audioStatus === "skipped_no_audio_stream") description = "Audio analysis was skipped because no audio stream was available.";
      if (description) section.append(element("p", "clipping-artifact-note", description));
    }
    if (segments.length) {
      section.append(element("p", "clipping-artifact-note", "Transcript text and source timestamps are shown as stored."));
      const list = element("ol", "clipping-transcript-list");
      for (const segment of segments.slice(0, 5000)) {
        if (!segment || typeof segment !== "object") continue;
        const line = element("li", "clipping-transcript-line");
        line.append(element("time", "clipping-artifact-time", sourceTime(segment.start_ms, segment.end_ms)));
        line.append(element("span", "clipping-transcript-text", segment.text || ""));
        const meta = [];
        if (typeof segment.speaker_id === "string" && segment.speaker_id) meta.push(segment.speaker_id);
        if (Number.isFinite(Number(segment.confidence))) meta.push("confidence " + segment.confidence + " (uncalibrated)");
        if (meta.length) line.append(element("span", "clipping-artifact-note", meta.join(" · ")));
        list.append(line);
      }
      section.append(list);
    }
    parent.append(section);
  }

  function renderContext(parent, context) {
    if (!context || typeof context !== "object") return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", "Global context"));
    let hasContent = false;
    if (typeof context.summary === "string" && context.summary) {
      section.append(element("p", "clipping-artifact-copy", context.summary));
      hasContent = true;
    }
    for (const [key, heading] of [["topics", "Topics"], ["timeline", "Timeline"]]) {
      if (!Array.isArray(context[key]) || !context[key].length) continue;
      const list = element("ul", "clipping-artifact-list");
      for (const entry of context[key].slice(0, 500)) {
        if (!entry || typeof entry !== "object") continue;
        const parts = [sourceTime(entry.start_ms, entry.end_ms), entry.label, entry.summary];
        if (Array.isArray(entry.keywords)) parts.push(entry.keywords.filter((word) => typeof word === "string").join(", "));
        const text = parts.filter((part) => part !== null && part !== undefined && String(part).trim()).join(" · ");
        if (text) list.append(element("li", "", text));
      }
      if (list.children.length) {
        section.append(element("h6", "", heading), list);
        hasContent = true;
      }
    }
    if (hasContent) parent.append(section);
  }

  function renderEvents(parent, heading, events) {
    if (!Array.isArray(events) || !events.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", heading));
    const list = element("ul", "clipping-artifact-list");
    for (const event of events.slice(0, 1000)) {
      if (!event || typeof event !== "object") continue;
      const parts = [sourceTime(event.start_ms, event.end_ms), event.kind, event.evidence, event.text, event.detail, event.description];
      if (event.score !== null && event.score !== undefined) parts.push("score " + safeText(event.score));
      const text = parts.filter((part) => part !== null && part !== undefined && String(part).trim()).join(" · ");
      if (text) list.append(element("li", "", text));
    }
    if (list.children.length) {
      section.append(list);
      parent.append(section);
    }
  }

  function renderCandidates(parent, candidates, heading) {
    if (!Array.isArray(candidates) || !candidates.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", heading || "Clip candidates"));
    section.append(element("p", "clipping-artifact-note", "Scores are heuristic editorial rankings, not virality predictions. Confidence values are uncalibrated."));
    for (const candidate of candidates.slice(0, 100)) {
      if (!candidate || typeof candidate !== "object") continue;
      const item = element("article", "clipping-candidate");
      const rank = Number.isSafeInteger(candidate.rank) ? "#" + candidate.rank + " · " : "";
      const title = typeof candidate.title === "string" && candidate.title ? candidate.title : "Candidate " + safeText(candidate.id);
      item.append(element("strong", "", rank + title));
      const duration = Number(candidate.duration_ms) > 0 ? " · " + formatDuration(candidate.duration_ms) : "";
      item.append(element("p", "clipping-artifact-note", sourceTime(candidate.start_ms, candidate.end_ms) + duration));
      if (typeof candidate.hook === "string" && candidate.hook) item.append(element("p", "clipping-artifact-copy", "Hook: " + candidate.hook));
      if (Array.isArray(candidate.title_variants) && candidate.title_variants.length) {
        const variants = element("ul", "clipping-artifact-list");
        for (const variant of candidate.title_variants.slice(0, 20)) {
          const titleText = typeof variant === "string" ? variant : variant && (variant.title || variant.text);
          if (titleText) variants.append(element("li", "", "Title: " + titleText));
        }
        if (variants.children.length) {
          item.append(element("h6", "", "Title options"), variants);
        }
      }
      if (candidate.editorial_score !== undefined) {
        const score = candidate.editorial_score && typeof candidate.editorial_score === "object"
          ? candidate.editorial_score.total : candidate.editorial_score;
        item.append(element("p", "clipping-artifact-note", "Editorial score: " + safeText(score)));
      }
      renderMetadata(item, "Score components", candidate.scores || candidate.editorial_score?.components);
      if (candidate.confidence !== undefined || candidate.confidence_label) {
        const confidence = candidate.confidence === undefined ? "" : " " + safeText(candidate.confidence);
        const label = candidate.confidence_label ? " (" + candidate.confidence_label + ")" : "";
        item.append(element("p", "clipping-artifact-note", "Confidence" + confidence + label + " · uncalibrated"));
      }
      if (Array.isArray(candidate.evidence_refs) && candidate.evidence_refs.length) {
        item.append(element("p", "clipping-artifact-note", "Evidence references: " + candidate.evidence_refs.join(", ")));
      }
      renderEvidence(item, candidate.evidence);
      section.append(item);
    }
    if (section.children.length > 2) parent.append(section);
  }

  function renderGeminiArtifact(parent, artifact, analysis, job) {
    const panel = element("section", "clipping-artifact clipping-gemini-artifact");
    const header = element("div", "clipping-gemini-header");
    header.append(element("h4", "", "Gemini analysis"));
    header.append(element("span", "badge", GEMINI_SCHEMA_VERSION));
    panel.append(header);

    const sourceDuration = Number(analysis.source_duration_ms);
    if (Number.isSafeInteger(sourceDuration) && sourceDuration > 0) {
      panel.append(element("p", "clipping-artifact-note", "Video duration reported by Gemini: " + sourceTime(sourceDuration) + " (" + sourceDuration.toLocaleString() + " ms). It was not independently measured."));
    }
    if (typeof analysis.summary === "string" && analysis.summary) {
      const summary = element("section", "clipping-artifact-section");
      summary.append(element("h5", "", "Summary"), element("p", "clipping-gemini-summary", analysis.summary));
      panel.append(summary);
    }
    if (Array.isArray(analysis.language_codes) && analysis.language_codes.length) {
      panel.append(element("p", "clipping-artifact-note", "Languages reported by Gemini: " + analysis.language_codes.join(", ")));
    }
    panel.append(element("p", "clipping-gemini-source-note", "Clip times are proposed source boundaries. Evidence and scores are model estimates, not independently verified facts."));

    const options = job && job.options && typeof job.options === "object" ? job.options : {};
    const requested = Number(options.candidate_limit);
    const candidates = Array.isArray(analysis.candidates) ? analysis.candidates : [];
    if (Number.isSafeInteger(requested) && requested >= 0 && candidates.length < requested) {
      const reason = typeof analysis.fewer_candidates_reason === "string" ? analysis.fewer_candidates_reason : "";
      panel.append(element("p", "clipping-artifact-note", "Gemini returned " + candidates.length + " of " + requested + " requested suggestions." + (reason ? " " + reason : "")));
    } else if (typeof analysis.fewer_candidates_reason === "string" && analysis.fewer_candidates_reason.trim()) {
      panel.append(element("p", "clipping-artifact-note", "Fewer suggestions: " + analysis.fewer_candidates_reason));
    }

    if (candidates.length) {
      const section = element("section", "clipping-artifact-section clipping-gemini-candidates");
      section.append(element("h5", "", "Clip suggestions · original rank order"));
      section.append(element("p", "clipping-artifact-note", "Editorial scores are estimates, not calibrated probabilities or guarantees."));
      for (const candidate of candidates) {
        if (!candidate || typeof candidate !== "object") continue;
        const item = element("article", "clipping-gemini-candidate");
        const variants = Array.isArray(candidate.title_variants) ? candidate.title_variants.filter((title) => typeof title === "string" && title) : [];
        const title = variants[0] || "Clip suggestion";
        const rank = Number.isSafeInteger(candidate.rank) ? "#" + candidate.rank + " · " : "";
        item.append(element("h5", "", rank + title));
        const start = Number(candidate.start_ms);
        const end = Number(candidate.end_ms);
        if (Number.isSafeInteger(start) && Number.isSafeInteger(end) && end > start) {
          item.append(element("p", "clipping-gemini-boundary", "Proposed source time: " + sourceTime(start, end) + " · " + (end - start).toLocaleString() + " ms"));
        }
        if (typeof candidate.hook === "string" && candidate.hook) item.append(element("p", "", "Hook suggestion (not a verified quote): " + candidate.hook));
        if (typeof candidate.rationale === "string" && candidate.rationale) item.append(element("p", "", "Why this moment: " + candidate.rationale));
        if (typeof candidate.boundary_rationale === "string" && candidate.boundary_rationale) item.append(element("p", "", "Boundary rationale: " + candidate.boundary_rationale));
        if (typeof candidate.context === "string" && candidate.context) item.append(element("p", "", "Context: " + candidate.context));
        if (Number.isSafeInteger(candidate.editorial_score)) {
          item.append(element("p", "", "Editorial score estimate: " + candidate.editorial_score + " / 100"));
        }
        renderMetadata(item, "Score estimates by dimension", candidate.scores);
        if (variants.length > 1) {
          const alternatives = element("ul", "clipping-artifact-list");
          for (const variant of variants.slice(1)) alternatives.append(element("li", "", "Alternative title: " + variant));
          item.append(alternatives);
        }
        if (Array.isArray(candidate.evidence) && candidate.evidence.length) {
          item.append(element("p", "clipping-artifact-note", "Evidence described by Gemini; not independently verified."));
          renderEvidence(item, candidate.evidence);
        }
        section.append(item);
      }
      panel.append(section);
    } else {
      panel.append(element("p", "clipping-artifact-note", "Gemini returned no clip suggestions."));
    }

    if (Array.isArray(analysis.limitations) && analysis.limitations.length) {
      const section = element("section", "clipping-artifact-section");
      section.append(element("h5", "", "Limitations"));
      const list = element("ul", "clipping-gemini-limitations");
      for (const limitation of analysis.limitations) {
        if (typeof limitation === "string" && limitation.trim()) list.append(element("li", "", limitation));
      }
      if (list.children.length) {
        section.append(list);
        panel.append(section);
      }
    }
    parent.append(panel);
  }

  function renderArtifact(parent, artifact, job) {
    const panel = element("section", "clipping-artifact");
    panel.append(element("h4", "", "Saved analysis"));
    let envelope;
    try { envelope = JSON.parse(String(artifact.payload_json || "")); }
    catch (_error) {
      panel.append(element("p", "clipping-row-error", "This saved analysis could not be displayed."));
      parent.append(panel);
      return;
    }
    if (!envelope || typeof envelope !== "object" || Array.isArray(envelope)) {
      panel.append(element("p", "clipping-artifact-note", "No displayable analysis fields were saved."));
      parent.append(panel);
      return;
    }
    const schemaVersion = String(artifact.schema_version || "");
    if (schemaVersion === GEMINI_SCHEMA_VERSION) {
      if (envelope.version !== GEMINI_SCHEMA_VERSION) {
        panel.append(element("p", "clipping-row-error", "The saved Gemini analysis schema did not match its artifact version."));
        parent.append(panel);
        return;
      }
      renderGeminiArtifact(parent, artifact, envelope, job);
      return;
    }
    const data = envelope.payload && typeof envelope.payload === "object" && !Array.isArray(envelope.payload)
      ? envelope.payload : envelope;
    if (data.content_type) panel.append(element("p", "clipping-artifact-note", "Analysis type: " + safeText(data.content_type)));
    if (data.language && typeof data.language === "object") {
      const language = [data.language.code, data.language.script].filter(Boolean).join(" · ");
      if (language) panel.append(element("p", "clipping-artifact-note", "Language: " + language));
    }
    renderTranscript(panel, data);
    renderContext(panel, data.context);
    renderEvents(panel, "Audio events", data.audio_events);
    renderEvents(panel, "Visual events", data.visual_events);
    if (Array.isArray(data.evidence_events)) renderEvents(panel, "Detected events", data.evidence_events);
    renderCandidates(panel, data.candidates, "Selected clip candidates");
    if (data.candidate_pools && typeof data.candidate_pools === "object" && !Array.isArray(data.candidate_pools)) {
      for (const [profile, candidates] of Object.entries(data.candidate_pools).slice(0, 5)) {
        renderCandidates(panel, candidates, "Saved candidate pool: " + contentType(profile));
      }
    }
    parent.append(panel);
  }

  function renderResults(parent, jobID) {
    const result = state.resultDetails.get(jobID);
    const error = state.resultErrors.get(jobID);
    const job = state.jobs.find((item) => safeID(item && item.id) === jobID);
    if (error) {
      parent.append(element("p", "clipping-row-error", error));
      const retry = button("Try loading again", "button secondary clipping-action", () => { void loadResults(jobID); });
      retry.dataset.clippingFocusKey = "reload-results";
      parent.append(retry);
    }
    if (state.loadingResults.has(jobID)) {
      parent.append(element("p", "clipping-artifact-note", "Loading saved analysis…"));
      return;
    }
    const artifacts = result && Array.isArray(result.artifacts) ? result.artifacts : [];
    if (!artifacts.length && !error) {
      parent.append(element("p", "clipping-artifact-note", "No saved analysis results are available for this job."));
    }
    for (const artifact of artifacts) {
      if (artifact && typeof artifact === "object") renderArtifact(parent, artifact, job);
    }
  }

  function renderJob(job) {
    const id = safeID(job && job.id);
    if (!id) return null;
    const row = element("article", "clipping-job-row");
    row.dataset.jobId = id;
    const header = element("div", "clipping-job-heading");
    const source = state.sources.find((item) => String(item.id) === String(job.source_id));
    header.append(element("strong", "clipping-job-title", isGeminiJob(job) ? "YouTube reference" : source ? sourceName(source) : "YouTube analysis"));
    header.append(element("span", "badge clipping-job-status", jobStatus(job)));
    row.append(header);
    const status = String(job.status || "").toLowerCase();
    if (!isGeminiJob(job) && ["queued", "running"].includes(status)) renderProgress(row, "Analysis progress", statusProgress(job));
    if (isGeminiJob(job) && ["queued", "running", "submitting", "outcome_unknown"].includes(status)) {
      row.append(element("p", "clipping-gemini-status", status === "running"
        ? "Analysis is in progress. A percentage is not available."
        : status === "outcome_unknown" ? "The provider outcome is unresolved. Refresh for an update." : "Analysis request saved. Waiting for provider status."));
    }
    const error = state.jobActionErrors.get(id) || (isGeminiJob(job)
      ? job.error_code || job.error ? geminiErrorCopy(job) : ""
      : String(job.error || "").trim());
    if (error) row.append(element("p", "clipping-row-error", error));
    const safetyCopy = retrySafetyCopy(job);
    if (safetyCopy) row.append(element("p", "clipping-gemini-status", safetyCopy));
    const footer = element("div", "clipping-job-actions");
    footer.append(element("span", "clipping-job-meta", isGeminiJob(job)
      ? geminiOptionsCopy(job.options)
      : "Legacy analysis · " + (CONTENT_TYPE_LABELS[contentType(job.content_type)] || "General")));
    if (status === "completed" || state.resultDetails.has(id) || state.resultErrors.has(id)) {
      const expanded = state.expandedResults.has(id);
      const toggle = button(
        state.loadingResults.has(id) ? "Loading results…" : expanded ? "Hide saved analysis" : "View saved analysis",
        "button secondary clipping-action",
        () => { void toggleResults(id); }
      );
      toggle.dataset.clippingFocusKey = "results";
      toggle.setAttribute("aria-expanded", String(expanded));
      if (state.loadingResults.has(id)) toggle.setAttribute("aria-busy", "true");
      footer.append(toggle);
    }
    const canceling = state.pendingCancelJobIDs.has(id);
    const deleting = state.pendingDeleteJobIDs.has(id) || state.pendingDeleteSourceIDs.has(safeID(job.source_id));
    if (["queued", "running", "submitting", "rate_limited_wait"].includes(status)) {
      const cancel = button(canceling ? "Canceling…" : "Cancel analysis", "button secondary clipping-action", () => { void cancelJob(id); });
      cancel.dataset.clippingFocusKey = "cancel-job";
      cancel.disabled = canceling || deleting;
      footer.append(cancel);
    }
    const remove = button(deleting ? "Deleting…" : "Delete analysis", "button secondary clipping-action clipping-delete-action",
      () => { void deleteJob(id); });
    remove.dataset.clippingFocusKey = "delete-job";
    const sourceNameForJob = source ? sourceName(source) : "this video";
    remove.setAttribute("aria-label", (deleting ? "Deleting analysis for " : "Delete analysis for ") + sourceNameForJob);
    remove.setAttribute("aria-disabled", String(deleting));
    if (deleting) remove.setAttribute("aria-busy", "true");
    remove.disabled = deleting || canceling;
    footer.append(remove);
    row.append(footer);
    if (state.expandedResults.has(id)) {
      const results = element("div", "clipping-job-results");
      renderResults(results, id);
      row.append(results);
    }
    return row;
  }

  function renderJobs() {
    const focus = captureFocus(ui.jobs, "jobId");
    const fragment = document.createDocumentFragment();
    ui.jobCount.textContent = String(state.jobs.length);
    if (!state.jobs.length) fragment.append(element("p", "empty", "Analysis jobs will appear here after you choose Analyze."));
    for (const job of state.jobs) {
      const row = renderJob(job);
      if (row) fragment.append(row);
    }
    ui.jobs.replaceChildren(fragment);
    restoreFocus(ui.jobs, "jobId", focus);
  }

  async function load(kind, endpoint, render, errorNode, recordName, notifyError) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence[kind];
    if (kind === "sources") state.sourcesLoaded = false;
    if (kind === "jobs") state.jobsLoaded = false;
    errorNode.textContent = "";
    try {
      const response = await api(endpoint);
      if (!current(lifecycle) || sequence !== state.requestSequence[kind]) return;
      const records = Array.isArray(response) ? response : response && Array.isArray(response[recordName]) ? response[recordName] : [];
      if (kind === "sources") {
        for (const source of records) {
          const id = safeID(source && source.id);
          const status = String(source && source.status || "").toLowerCase();
          if (id && state.sourceConsoleStatuses.get(id) !== status) {
            const reason = String(source && source.error || "");
            console.info("[clipper/youtube-import] source status", { source_id: id, status, reason_code: /^[a-z0-9_]{1,100}$/.test(reason) ? reason : "" });
            state.sourceConsoleStatuses.set(id, status);
          }
          if (status === "deleted") markSourceDeleted(source.id);
        }
        state.sources = records.filter((source) => !state.deletedSourceIDs.has(safeID(source && source.id))
          && String(source && source.status || "").toLowerCase() !== "deleted");
        state.sourcesLoaded = true;
      }
      if (kind === "jobs") {
        const visible = [];
        for (const job of records) {
          const id = safeID(job && job.id);
          const sourceID = safeID(job && job.source_id);
          if (!id) continue;
          if (job.analysis_deleted === true || state.deletedJobIDs.has(id) || state.deletedSourceIDs.has(sourceID)) {
            markJobDeleted(id, sourceID);
            continue;
          }
          observeJobStatus(job);
          visible.push(job);
        }
        state.jobs = visible;
        state.jobsLoaded = true;
      }
      render();
      if (kind === "jobs") renderSources();
      updateImportState();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence[kind] || error.name === "AbortError") return;
      if (kind === "sources") console.warn("[clipper/youtube-import] source refresh failed", { error_type: error && error.name || "unknown" });
      errorNode.textContent = error.message || "Could not load clipping information.";
      if (notifyError) toastError(error, errorNode.textContent);
    }
  }

  function renderGeminiReadiness() {
    const config = geminiConfig();
    const limits = geminiLimits();
    if (!config) {
      ui.geminiReadiness.textContent = "Gemini provider settings could not be loaded. Refresh before requesting an analysis.";
      ui.geminiReadiness.dataset.state = "unknown";
      ui.importLimits.textContent = "Provider limits are unavailable until settings load.";
    } else if (config.configured !== true) {
      ui.geminiReadiness.textContent = "Gemini is not configured. Ask the operator to configure the Google provider.";
      ui.geminiReadiness.dataset.state = "blocked";
      ui.importLimits.textContent = "The configured clip and source limits will appear here when Gemini is set up.";
    } else if (config.enabled !== true) {
      ui.geminiReadiness.textContent = "Gemini is configured but disabled. An operator must enable it before you can request analysis.";
      ui.geminiReadiness.dataset.state = "blocked";
      ui.importLimits.textContent = "The configured clip and source limits will appear here when Gemini is enabled.";
    } else if (!limits) {
      ui.geminiReadiness.textContent = "Gemini is configured and enabled, but its limits could not be read. Refresh provider settings before requesting analysis.";
      ui.geminiReadiness.dataset.state = "unknown";
      ui.importLimits.textContent = "Provider limits are unavailable until settings load.";
    } else {
      const duration = formatDuration(limits.maxSourceDurationMS);
      ui.geminiReadiness.textContent = "Gemini is configured and enabled. Connection and YouTube video access have not been tested; a public video may be unavailable.";
      ui.geminiReadiness.dataset.state = config.verification === "unverified" ? "unverified" : "configured";
      const minSeconds = Math.ceil(limits.minClipMS / 1000);
      const maxSeconds = Math.floor(limits.maxClipMS / 1000);
      const maxCandidates = limits.maxCandidates;
      ui.minClipSeconds.min = String(minSeconds);
      ui.minClipSeconds.max = String(maxSeconds);
      ui.maxClipSeconds.min = String(minSeconds);
      ui.maxClipSeconds.max = String(maxSeconds);
      for (const option of Array.from(ui.candidateLimit.options || [])) {
        option.disabled = Number(option.value) > maxCandidates;
      }
      if (Number(ui.candidateLimit.value) > maxCandidates) ui.candidateLimit.value = String(maxCandidates);
      ui.importLimits.textContent = "Clip lengths: " + minSeconds + "–" + maxSeconds + " seconds. Up to " + maxCandidates + " suggestions." + (duration ? " Source duration ceiling: " + duration + " as reported by Gemini, not independently measured." : "");
    }
    updateImportState();
  }

  async function loadConfig(notifyError) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence.config;
    try {
      const config = await api("/api/clipping/config");
      if (!current(lifecycle) || sequence !== state.requestSequence.config) return;
      state.config = config;
      renderGeminiReadiness();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence.config || error.name === "AbortError") return;
      state.config = null;
      renderGeminiReadiness();
      if (notifyError) toastError(error, "Gemini provider settings could not be loaded.");
    }
  }

  async function toggleResults(jobID) {
    const id = safeID(jobID);
    if (!id || state.deletedJobIDs.has(id) || state.loadingResults.has(id)) return;
    if (state.expandedResults.has(id)) {
      state.expandedResults.delete(id);
      renderJobs();
      return;
    }
    state.expandedResults.add(id);
    renderJobs();
    if (!state.resultDetails.has(id)) await loadResults(id);
  }

  async function loadResults(jobID) {
    const id = safeID(jobID);
    if (!id || state.deletedJobIDs.has(id) || !state.active || state.loadingResults.has(id)) return;
    const lifecycle = state.lifecycle;
    const sequence = (state.resultSequences.get(id) || 0) + 1;
    state.resultSequences.set(id, sequence);
    state.loadingResults.add(id);
    state.resultErrors.delete(id);
    renderJobs();
    try {
      const response = await api("/api/clipping/jobs/" + encodeURIComponent(id) + "/results");
      if (!current(lifecycle) || state.deletedJobIDs.has(id) || state.resultSequences.get(id) !== sequence) return;
      const result = response && response.results ? response.results : response;
      if (result && result.job_id && safeID(result.job_id) !== id) throw new Error("Saved results did not match this analysis job.");
      state.resultDetails.set(id, result || {});
    } catch (error) {
      if (current(lifecycle) && !state.deletedJobIDs.has(id) && state.resultSequences.get(id) === sequence && error.name !== "AbortError") {
        const message = error.message || "Saved analysis could not be loaded.";
        state.resultErrors.set(id, message);
        toastError(error, message);
      }
    } finally {
      if (current(lifecycle) && state.resultSequences.get(id) === sequence) {
        state.loadingResults.delete(id);
        renderJobs();
      }
    }
  }

  function clearFieldError(field) {
    field.removeAttribute("aria-invalid");
  }

  function validateGeminiOptions() {
    ui.optionsError.textContent = "";
    for (const field of [ui.contentType, ui.minClipSeconds, ui.maxClipSeconds, ui.candidateLimit]) clearFieldError(field);
    const limits = geminiLimits();
    if (!limits) {
      ui.optionsError.textContent = "Gemini limits are unavailable. Refresh provider settings before submitting.";
      ui.contentType.setAttribute("aria-invalid", "true");
      ui.contentType.focus();
      return null;
    }
    const minClipSeconds = Number(ui.minClipSeconds.value);
    const maxClipSeconds = Number(ui.maxClipSeconds.value);
    const candidateLimit = Number(ui.candidateLimit.value);
    const minAllowedSeconds = Math.ceil(limits.minClipMS / 1000);
    const maxAllowedSeconds = Math.floor(limits.maxClipMS / 1000);
    const maxCandidates = limits.maxCandidates;
    if (!Number.isInteger(minClipSeconds) || minClipSeconds < minAllowedSeconds || minClipSeconds > maxAllowedSeconds) {
      ui.optionsError.textContent = "Choose a shortest clip from " + minAllowedSeconds + " to " + maxAllowedSeconds + " seconds.";
      ui.minClipSeconds.setAttribute("aria-invalid", "true");
      ui.minClipSeconds.focus();
      return null;
    }
    if (!Number.isInteger(maxClipSeconds) || maxClipSeconds < minAllowedSeconds || maxClipSeconds > maxAllowedSeconds) {
      ui.optionsError.textContent = "Choose a longest clip from " + minAllowedSeconds + " to " + maxAllowedSeconds + " seconds.";
      ui.maxClipSeconds.setAttribute("aria-invalid", "true");
      ui.maxClipSeconds.focus();
      return null;
    }
    if (minClipSeconds > maxClipSeconds) {
      ui.optionsError.textContent = "The longest clip must be at least as long as the shortest clip.";
      ui.maxClipSeconds.setAttribute("aria-invalid", "true");
      ui.maxClipSeconds.focus();
      return null;
    }
    if (!Number.isInteger(candidateLimit) || candidateLimit < 1 || candidateLimit > maxCandidates) {
      ui.optionsError.textContent = "Choose between 1 and " + maxCandidates + " suggested clips.";
      ui.candidateLimit.setAttribute("aria-invalid", "true");
      ui.candidateLimit.focus();
      return null;
    }
    const type = String(ui.contentType.value || "").toLowerCase();
    if (!CONTENT_TYPES.includes(type)) {
      ui.optionsError.textContent = "Choose a supported content type.";
      ui.contentType.setAttribute("aria-invalid", "true");
      ui.contentType.focus();
      return null;
    }
    return {
      content_type: type,
      min_clip_ms: minClipSeconds * 1000,
      max_clip_ms: maxClipSeconds * 1000,
      candidate_limit: candidateLimit
    };
  }

  function makeIdempotencyKey() {
    if (root.crypto && typeof root.crypto.randomUUID === "function") return root.crypto.randomUUID();
    if (root.crypto && typeof root.crypto.getRandomValues === "function") {
      const bytes = new Uint8Array(16);
      root.crypto.getRandomValues(bytes);
      return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    }
    return "gemini-" + Date.now() + "-" + Math.random().toString(16).slice(2);
  }

  async function createGeminiJob(request) {
    if (state.geminiRequestPending || !geminiReady()) return;
    const sourceID = safeID(request && request.sourceID);
    const source = sourceID ? state.sources.find((item) => safeID(item && item.id) === sourceID) : null;
    if (sourceID && (!source || String(source.status).toLowerCase() !== "ready" || !["youtube_url", "youtube_original_file"].includes(String(source.kind || ""))
      || source.rights_attested !== true || state.pendingSourceIDs.has(sourceID) || state.deletedSourceIDs.has(sourceID)
      || !sourceAnalysisState(sourceID).canStart)) return;
    const youtubeURL = sourceID ? "" : String(request && request.youtubeURL || "").trim();
    const videoID = youtubeVideoID(youtubeURL);
    if (!sourceID && !videoID) {
      ui.importError.textContent = "Paste a valid link to one public YouTube video.";
      ui.link.setAttribute("aria-invalid", "true");
      ui.link.focus();
      return;
    }
    const identity = sourceID ? "source:" + sourceID : "youtube:" + videoID;
    const unresolved = state.unknownRequests.get(identity);
    if (state.unknownRequests.size && !unresolved) {
      ui.importError.textContent = "Resolve the previous request by resending it before starting another analysis.";
      return;
    }
    if (!sourceID && !unresolved && !ui.permission.checked) {
      ui.importError.textContent = "Confirm that you have permission to reuse and process this video.";
      ui.permission.setAttribute("aria-invalid", "true");
      ui.permission.focus();
      return;
    }
    ui.importError.textContent = "";
    clearFieldError(ui.link);
    clearFieldError(ui.permission);
    const mappedSource = !sourceID && state.urlSourceIDs.get(videoID);
    if (mappedSource && !unresolved && (!state.sourcesLoaded
      || !state.sources.some((item) => safeID(item && item.id) === mappedSource)
      || !sourceAnalysisState(mappedSource).canStart)) {
      ui.importError.textContent = "This video already has an active or unresolved analysis. Review its saved status below.";
      return;
    }
    const options = unresolved ? null : validateGeminiOptions();
    if (!unresolved && !options) return;
    const body = unresolved ? unresolved.body : JSON.stringify(sourceID
      ? { source_id: sourceID, options } : { youtube_url: youtubeURL, rights_attested: true, options });
    const idempotencyKey = unresolved ? unresolved.key : makeIdempotencyKey();
    const lifecycle = state.lifecycle;
    invalidateListLoads();
    state.geminiRequestPending = true;
    if (sourceID) {
      state.pendingSourceIDs.add(sourceID);
      state.sourceActionErrors.delete(sourceID);
      state.pendingJobBaselines.set(sourceID, new Set(state.jobs.map((job) => safeID(job && job.id)).filter(Boolean)));
    } else {
      ui.importButton.textContent = "Saving request…";
      ui.link.disabled = true;
      ui.permission.disabled = true;
      ui.contentType.disabled = true;
      ui.minClipSeconds.disabled = true;
      ui.maxClipSeconds.disabled = true;
      ui.candidateLimit.disabled = true;
      ui.importForm.setAttribute("aria-busy", "true");
      ui.importStatus.textContent = "Saving the analysis request…";
    }
    renderSources();
    updateImportState();
    try {
      const response = await api("/api/clipping/gemini/jobs", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body
      });
      if (!current(lifecycle)) return;
      const job = response && response.job;
      const jobID = safeID(job && job.id);
      if (!jobID) throw new Error("The analysis request response did not include a saved job. Refresh to check its status.");
      if (!state.jobTracking.has(jobID)) observeJobStatus(Object.assign({}, job, { status: job.status || "queued" }));
      observeJobStatus(job);
      state.unknownRequests.delete(identity);
      if (videoID && safeID(job.source_id)) state.urlSourceIDs.set(videoID, safeID(job.source_id));
      if (!state.jobs.some((item) => safeID(item && item.id) === jobID)) state.jobs.unshift(job);
      renderJobs();
      if (!sourceID) {
        ui.link.value = "";
        ui.permission.checked = false;
        clearFieldError(ui.link);
        clearFieldError(ui.permission);
      }
      ui.importStatus.textContent = response.duplicate === true
        ? "This analysis request was already saved. Its current status appears below."
        : "Analysis request saved. Provider status will appear below.";
      delete ui.importStatus.dataset.recovery;
      showToast(response.duplicate === true ? "Analysis request was already saved." : "Analysis request saved.", false);
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]);
      ui.analysisHeading.focus?.({ preventScroll: true });
    } catch (error) {
      if (!current(lifecycle)) return;
      const definiteRejection = !unresolved && [400, 401, 403, 404, 413, 415, 422].includes(Number(error && error.status));
      if (!definiteRejection) state.unknownRequests.set(identity, { body, key: idempotencyKey });
      const message = definiteRejection
        ? error && error.message || "The analysis request could not be saved."
        : "The request outcome could not be confirmed. Resend the same request to check it safely; no automatic retry was made.";
      if (sourceID && !state.deletedSourceIDs.has(sourceID)) {
        state.sourceActionErrors.set(sourceID, message);
        if (!error || error.status !== 401) showToast(message, true);
        renderSources();
      } else if (!sourceID) {
        ui.importError.textContent = message;
        if (!error || error.status !== 401) showToast(message, true);
        ui.importStatus.textContent = "";
        ui.link.focus?.({ preventScroll: true });
      }
    } finally {
      if (!current(lifecycle)) return;
      state.geminiRequestPending = false;
      if (sourceID) {
        state.pendingSourceIDs.delete(sourceID);
        state.pendingJobBaselines.delete(sourceID);
      } else {
        ui.link.disabled = false;
        ui.permission.disabled = false;
        ui.contentType.disabled = false;
        ui.minClipSeconds.disabled = false;
        ui.maxClipSeconds.disabled = false;
        ui.candidateLimit.disabled = false;
        ui.importForm.removeAttribute("aria-busy");
        updateImportState();
      }
      renderSources();
      updateImportState();
    }
  }

  async function submitGeminiRequest(event) {
    event.preventDefault();
    if (!current(state.lifecycle)) return;
    ui.importError.textContent = "";
    if (!youtubeVideoID(String(ui.link.value || "").trim())) {
      ui.importError.textContent = "Paste a valid link to one public YouTube video.";
      ui.link.setAttribute("aria-invalid", "true");
      ui.link.focus();
      return;
    }
    if (!ui.permission.checked && !state.unknownRequests.has("youtube:" + youtubeVideoID(ui.link.value))) {
      ui.importError.textContent = "Confirm that you have permission to reuse and process this video.";
      ui.permission.setAttribute("aria-invalid", "true");
      ui.permission.focus();
      return;
    }
    if (!geminiReady()) {
      ui.importError.textContent = "Gemini must be configured and enabled before an analysis request can be saved.";
      ui.geminiReadiness.focus?.({ preventScroll: true });
      return;
    }
    await createGeminiJob({ youtubeURL: String(ui.link.value || "").trim() });
  }

  async function cancelJob(jobID) {
    const id = safeID(jobID);
    const job = state.jobs.find((item) => safeID(item && item.id) === id);
    if (!id || !job || state.pendingCancelJobIDs.has(id) || state.pendingDeleteJobIDs.has(id)
      || !["queued", "running", "submitting", "rate_limited_wait"].includes(String(job.status || "").toLowerCase())) return;
    const description = isGeminiJob(job)
      ? "Cancel this analysis? If Google has already started work, cancellation does not confirm that Google stopped processing it. The saved source remains."
      : "Cancel this analysis? The saved source remains.";
    const confirmed = await confirmAction("Cancel analysis?", description, () => performJobCancel(id), "Cancel analysis");
    if (!confirmed) focusTarget(Array.from(ui.jobs.children).find((row) => row.dataset.jobId === id), "cancel-job")?.focus?.({ preventScroll: true });
    else ui.analysisHeading.focus?.({ preventScroll: true });
  }

  async function performJobCancel(id) {
    const lifecycle = state.lifecycle;
    if (!state.jobs.some((job) => safeID(job && job.id) === id) || state.deletedJobIDs.has(id)) {
      return { ok: false, message: "This analysis is no longer available. Refresh saved analyses." };
    }
    invalidateListLoads();
    state.pendingCancelJobIDs.add(id);
    state.jobActionErrors.delete(id);
    renderJobs();
    try {
      const response = await api("/api/clipping/jobs/" + encodeURIComponent(id) + "/cancel", { method: "POST" });
      if (!current(lifecycle)) return { ok: false };
      const job = response && response.job;
      if (!job || safeID(job.id) !== id) throw new Error("Cancellation could not be confirmed. Refresh to check the saved status.");
      state.jobs = state.jobs.map((item) => safeID(item && item.id) === id ? job : item);
      observeJobStatus(job);
      renderJobs();
      showToast(isGeminiJob(job)
        ? "Analysis canceled locally. The provider may still be processing work already submitted."
        : "Analysis canceled.", false);
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]);
      return { ok: true };
    } catch (error) {
      if (!current(lifecycle)) return { ok: false };
      const message = "Cancellation could not be confirmed. Refresh to check the saved status.";
      state.jobActionErrors.set(id, message);
      renderJobs();
      if (!error || error.status !== 401) showToast(message, true);
      return { ok: false, message };
    } finally {
      if (current(lifecycle)) {
        state.pendingCancelJobIDs.delete(id);
        renderJobs();
        renderSources();
      }
    }
  }

  async function deleteSource(sourceID) {
    const id = safeID(sourceID);
    const source = state.sources.find((item) => safeID(item && item.id) === id);
    if (!id || !source || state.deletedSourceIDs.has(id) || state.pendingDeleteSourceIDs.has(id) || analysisLockedForSource(id)) return;
    const name = sourceName(source);
    let description = source.kind === "youtube_url"
      ? "Delete the saved YouTube reference and its analysis records? This removes the saved reference; it does not delete a local video file."
      : "Delete “" + name + "” and its saved analyses? This removes the imported video and its analysis records.";
    if (state.jobs.some((job) => safeID(job.source_id) === id && isGeminiJob(job) && activeJobStatus(job.status))) {
      description += " Work already submitted to Google may continue processing.";
    }
    const confirmed = await confirmAction("Delete saved source?", description, () => performSourceDelete(id));
    if (!confirmed) {
      focusTarget(Array.from(ui.sources.children).find((row) => row.dataset.sourceId === id), "delete-source")?.focus?.({ preventScroll: true });
      return;
    }
    focusFirstAction(ui.sources, "delete-source");
    if (!state.sources.length) ui.link.focus?.({ preventScroll: true });
  }

  async function performSourceDelete(id) {
    const source = state.sources.find((item) => safeID(item && item.id) === id);
    if (!source || state.deletedSourceIDs.has(id)) return { ok: false, message: "This source is no longer available. Refresh saved sources." };
    const lifecycle = state.lifecycle;
    invalidateListLoads();
    state.pendingDeleteSourceIDs.add(id);
    state.sourceActionErrors.delete(id);
    renderSources();
    renderJobs();
    try {
      const response = await api("/api/clipping/sources/" + encodeURIComponent(id), { method: "DELETE" });
      if (!current(lifecycle)) return;
      markSourceDeleted(id);
      invalidateListLoads();
      renderSources();
      renderJobs();
      showToast(response && response.cleanup_pending === true && source.kind !== "youtube_url"
        ? "Saved source and analyses removed. File cleanup is still finishing."
        : "Saved source and analyses deleted.", false);
      await Promise.allSettled([
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources"),
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs")
      ]);
      return { ok: true };
    } catch (error) {
      if (current(lifecycle) && !state.deletedSourceIDs.has(id)) {
        const message = error && error.message || "The saved source could not be deleted.";
        state.sourceActionErrors.set(id, message);
        renderSources();
        toastError(error, message);
        return { ok: false, message };
      }
      return { ok: false, message: "The session changed before deletion was confirmed. Refresh saved sources." };
    } finally {
      if (current(lifecycle)) {
        state.pendingDeleteSourceIDs.delete(id);
        renderSources();
        renderJobs();
      }
    }
  }

  async function deleteJob(jobID) {
    const id = safeID(jobID);
    const job = state.jobs.find((item) => safeID(item && item.id) === id);
    if (!id || !job || state.deletedJobIDs.has(id) || state.pendingDeleteJobIDs.has(id)
      || state.pendingDeleteSourceIDs.has(safeID(job.source_id))) return;
    const source = state.sources.find((item) => safeID(item && item.id) === safeID(job.source_id));
    const name = source ? sourceName(source) : "this video";
    const wasActive = activeJobStatus(job.status);
    const geminiActive = isGeminiJob(job) && wasActive;
    const description = geminiActive
      ? "Delete the analysis for “" + name + "” and its saved results? The source will remain. If Google work has already started, this does not confirm that Google stopped processing it."
      : wasActive
        ? "Delete the analysis for “" + name + "” and its saved results? This requests cancellation and keeps the saved source."
        : "Delete the saved analysis for “" + name + "” and its results? The saved source will remain.";
    const confirmed = await confirmAction("Delete analysis?", description, () => performJobDelete(id, job, wasActive));
    if (!confirmed) {
      focusTarget(Array.from(ui.jobs.children).find((row) => row.dataset.jobId === id), "delete-job")?.focus?.({ preventScroll: true });
      return;
    }
    focusFirstAction(ui.jobs, "delete-job");
    if (!state.jobs.length) ui.analysisHeading.focus?.({ preventScroll: true });
  }

  async function performJobDelete(id, job, wasActive) {
    if (state.deletedJobIDs.has(id)) return { ok: false, message: "This analysis is no longer available. Refresh saved analyses." };
    const lifecycle = state.lifecycle;
    invalidateListLoads();
    state.pendingDeleteJobIDs.add(id);
    state.jobActionErrors.delete(id);
    renderJobs();
    renderSources();
    try {
      await api("/api/clipping/jobs/" + encodeURIComponent(id), { method: "DELETE" });
      if (!current(lifecycle)) return;
      markJobDeleted(id, job.source_id);
      invalidateListLoads();
      renderJobs();
      renderSources();
      showToast(isGeminiJob(job) && wasActive
        ? "Saved analysis deleted. The provider may still be processing work already submitted."
        : wasActive ? "Analysis canceled and deleted." : "Analysis and saved results deleted.", false);
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]);
      return { ok: true };
    } catch (error) {
      if (current(lifecycle) && !state.deletedJobIDs.has(id)) {
        const message = error && error.message || "The analysis could not be deleted.";
        state.jobActionErrors.set(id, message);
        renderJobs();
        toastError(error, message);
        return { ok: false, message };
      }
      return { ok: false, message: "The session changed before deletion was confirmed. Refresh saved analyses." };
    } finally {
      if (current(lifecycle)) {
        state.pendingDeleteJobIDs.delete(id);
        renderJobs();
        renderSources();
      }
    }
  }

  function updateImportState() {
    const value = String(ui.link.value || "").trim();
    const hasLink = value.length > 0;
    const videoID = youtubeVideoID(value);
    const validLink = Boolean(videoID);
    const unresolved = state.unknownRequests.get("youtube:" + videoID);
    const mappedSource = state.urlSourceIDs.get(videoID);
    const otherUnknown = state.unknownRequests.size > 0 && !unresolved;
    const blocked = Boolean(otherUnknown || (mappedSource && !unresolved && (!state.sourcesLoaded
      || !state.sources.some((item) => safeID(item && item.id) === mappedSource)
      || !sourceAnalysisState(mappedSource).canStart)));
    ui.importButton.textContent = state.geminiRequestPending ? "Saving request…" : unresolved ? "Resend same request" : "Analyze video";
    ui.importButton.disabled = !hasLink || !validLink || (!ui.permission.checked && !unresolved)
      || !geminiReady() || state.geminiRequestPending || blocked;
    if (unresolved) {
      const original = JSON.parse(unresolved.body).options;
      ui.importStatus.textContent = "The previous request is unresolved. Resending uses its original settings: " + geminiOptionsCopy(original) + ".";
      ui.importStatus.dataset.recovery = "true";
    } else if (otherUnknown) {
      ui.importStatus.textContent = "A previous request is unresolved. Return to it and resend the same request before starting another analysis.";
      ui.importStatus.dataset.recovery = "true";
    } else if (blocked) {
      ui.importStatus.textContent = "This video already has an active or unresolved analysis. Review its saved status below.";
      ui.importStatus.dataset.recovery = "true";
    } else if (ui.importStatus.dataset.recovery === "true") {
      ui.importStatus.textContent = "";
      delete ui.importStatus.dataset.recovery;
    }
    if (hasLink && !validLink) {
      ui.importError.textContent = "Use one public YouTube video link.";
    }
  }

  function bind() {
    ui.refresh.addEventListener("click", () => { void refresh({ notify: true }); });
    ui.importForm.addEventListener("submit", (event) => { void submitGeminiRequest(event); });
    ui.link.addEventListener("input", () => {
      ui.importError.textContent = "";
      clearFieldError(ui.link);
      updateImportState();
    });
    ui.permission.addEventListener("change", () => { clearFieldError(ui.permission); updateImportState(); });
    for (const field of [ui.contentType, ui.minClipSeconds, ui.maxClipSeconds, ui.candidateLimit]) {
      field.addEventListener("input", () => { ui.optionsError.textContent = ""; clearFieldError(field); });
      field.addEventListener("change", () => { ui.optionsError.textContent = ""; clearFieldError(field); });
    }
    ui.confirmDelete.addEventListener("click", () => { void runConfirmation(); });
    ui.confirmCancel.addEventListener("click", () => closeConfirmation(false));
    ui.confirmDialog.addEventListener("cancel", cancelConfirmation);
    updateImportState();
  }

  function configure(options) {
    state.requestImpl = options && typeof options.request === "function" ? options.request : null;
    state.toastImpl = options && typeof options.toast === "function" ? options.toast : null;
  }

  function start() {
    if (!state.requestImpl) return Promise.resolve();
    if (!state.active) {
      state.active = true;
      state.lifecycle++;
    }
    return refresh();
  }

  async function refresh(options) {
    if (!state.active) return;
    const lifecycle = state.lifecycle;
    const notifyError = Boolean(options && typeof options === "object" && options.notify);
    await Promise.allSettled([
      loadConfig(notifyError),
      load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources", notifyError),
      load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs", notifyError)
    ]);
    if (current(lifecycle)) {
      renderSources();
      renderJobs();
    }
  }

  function stop(clear) {
    if (state.pendingConfirmation) closeConfirmation(false);
    state.active = false;
    state.lifecycle++;
    if (clear !== false) {
      state.config = null;
      state.sources = [];
      state.sourceConsoleStatuses.clear();
      state.jobs = [];
      state.pendingSourceIDs.clear();
      state.pendingDeleteSourceIDs.clear();
      state.pendingDeleteJobIDs.clear();
      state.pendingCancelJobIDs.clear();
      state.pendingJobBaselines.clear();
      state.deletedSourceIDs.clear();
      state.deletedJobIDs.clear();
      state.jobTracking.clear();
      state.notifiedFailures.clear();
      state.sourceActionErrors.clear();
      state.jobActionErrors.clear();
      state.expandedResults.clear();
      state.resultDetails.clear();
      state.resultErrors.clear();
      state.loadingResults.clear();
      state.resultSequences.clear();
      state.unknownRequests.clear();
      state.urlSourceIDs.clear();
      state.uncertainSources.clear();
      state.sourcesLoaded = false;
      state.jobsLoaded = false;
      state.geminiRequestPending = false;
      ui.importError.textContent = "";
      ui.optionsError.textContent = "";
      ui.importStatus.textContent = "";
      delete ui.importStatus.dataset.recovery;
      ui.sourceError.textContent = "";
      ui.jobsError.textContent = "";
      ui.sources.replaceChildren(element("p", "empty", "Saved references and legacy sources will appear here."));
      ui.jobs.replaceChildren(element("p", "empty", "Saved analysis requests will appear here."));
      ui.sourceCount.textContent = "0";
      ui.jobCount.textContent = "0";
      ui.importButton.textContent = "Analyze video";
      ui.link.value = "";
      ui.permission.checked = false;
      ui.link.disabled = false;
      ui.permission.disabled = false;
      ui.contentType.disabled = false;
      ui.minClipSeconds.disabled = false;
      ui.maxClipSeconds.disabled = false;
      ui.candidateLimit.disabled = false;
      ui.importForm.removeAttribute("aria-busy");
      ui.contentType.value = "general";
      ui.minClipSeconds.value = "15";
      ui.maxClipSeconds.value = "180";
      ui.candidateLimit.value = "10";
      ui.confirmDialog.removeAttribute("aria-busy");
      ui.confirmError.textContent = "";
      ui.confirmError.hidden = true;
      ui.confirmCancel.disabled = false;
      ui.confirmCancel.textContent = "Cancel";
      ui.confirmDelete.disabled = false;
      ui.confirmDelete.textContent = "Delete";
      updateImportState();
    }
  }

  function handleEvent(kind, event) {
    if (!state.active || !event || typeof event.data !== "string") return;
    let payload;
    try { payload = JSON.parse(event.data); } catch (_error) { return; }
    const lifecycle = state.lifecycle;
    if (kind === "clipping_source") {
      // Events invalidate the list. Only authenticated GETs or acknowledged
      // mutations can change records or establish a deletion tombstone.
      void Promise.allSettled([
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources"),
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs")
      ]);
      return;
    }
    if (kind === "clipping_job") {
      const job = payload && payload.job && typeof payload.job === "object" ? payload.job : payload;
      const id = safeID(job && job.id) || safeID(payload && payload.id);
      if (id) {
        state.resultSequences.set(id, (state.resultSequences.get(id) || 0) + 1);
        state.loadingResults.delete(id);
        state.resultDetails.delete(id);
        state.resultErrors.delete(id);
      }
      void Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]).then(() => {
        if (current(lifecycle) && id && !state.deletedJobIDs.has(id) && state.expandedResults.has(id)
          && state.jobs.some((item) => safeID(item && item.id) === id)) void loadResults(id);
      });
    }
  }

  bind();
  root.ClippingUI = { configure, start, stop, refresh, handleEvent, hasSources: () => state.sources.length > 0 };
})(typeof window !== "undefined" ? window : globalThis);
