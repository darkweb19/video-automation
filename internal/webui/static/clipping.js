(function (root) {
  "use strict";

  const MAX_UPLOAD_CHUNK_BYTES = 16 * 1024 * 1024;
  const MAX_SAFE_MICRO_USD = Number.MAX_SAFE_INTEGER;
  const MAX_JOB_BUDGET_MICRO_USD = 4_000_000;
  const MAX_BATCH_BUDGET_MICRO_USD = 400_000_000;

  function parseUSDToMicroUSD(value) {
    const text = String(value ?? "").trim();
    const match = /^(\d{1,9})(?:\.(\d{1,6}))?$/.exec(text);
    if (!match) return null;
    const whole = Number(match[1]);
    const fraction = (match[2] || "").padEnd(6, "0");
    const amount = whole * 1_000_000 + Number(fraction || 0);
    return Number.isSafeInteger(amount) && amount > 0 && amount <= MAX_SAFE_MICRO_USD ? amount : null;
  }

  function parseBudgetUSD(value, maximum) {
    const amount = parseUSDToMicroUSD(value);
    return amount !== null && amount <= maximum ? amount : null;
  }

  function uploadChunkLimit(config) {
    const configured = Number(config && config.max_upload_chunk_bytes);
    if (!Number.isSafeInteger(configured) || configured <= 0) return MAX_UPLOAD_CHUNK_BYTES;
    return Math.min(configured, MAX_UPLOAD_CHUNK_BYTES);
  }

  function formatDuration(milliseconds) {
    const duration = Number(milliseconds);
    if (!Number.isFinite(duration) || duration < 0) return "Duration not available";
    const total = Math.floor(duration / 1000);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const seconds = total % 60;
    return hours
      ? `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`
      : `${minutes}:${String(seconds).padStart(2, "0")}`;
  }

  function formatUSD(microUSD) {
    const amount = Number(microUSD);
    if (!Number.isSafeInteger(amount) || amount < 0) return "Budget unavailable";
    return `$${(amount / 1_000_000).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 6 })}`;
  }

  function nextUploadRange(offset, fileSize, config) {
    if (!Number.isSafeInteger(offset) || !Number.isSafeInteger(fileSize) || offset < 0 || fileSize < offset) return null;
    return { start: offset, end: Math.min(fileSize, offset + uploadChunkLimit(config)) };
  }

  function validResumeOffset(offset, fileSize) {
    const value = Number(offset);
    return Number.isSafeInteger(value) && value >= 0 && Number.isSafeInteger(fileSize) && value <= fileSize ? value : null;
  }

  const pure = { parseUSDToMicroUSD, parseBudgetUSD, uploadChunkLimit, formatDuration, formatUSD, nextUploadRange, validResumeOffset };
  if (typeof module !== "undefined" && module.exports) module.exports = pure;
  if (!root || !root.document) return;

  const document = root.document;
  const $ = (id) => document.getElementById(id);
  const ui = {
    refresh: $("clipping-refresh"),
    workerNote: $("clipping-worker-note"),
    uploadForm: $("clipping-upload-form"),
    uploadFile: $("clipping-file"),
    uploadSubmit: $("clipping-upload-submit"),
    uploadPermission: $("clipping-upload-permission"),
    uploadError: $("clipping-upload-error"),
    uploadProgress: $("clipping-upload-progress"),
    uploadLabel: $("clipping-upload-label"),
    uploadPercent: $("clipping-upload-percent"),
    uploadBar: $("clipping-upload-bar"),
    uploadDetail: $("clipping-upload-detail"),
    uploadCancel: $("clipping-upload-cancel"),
    resumeFile: $("clipping-resume-file"),
    linkForm: $("clipping-link-form"),
    link: $("clipping-link"),
    linkPermissionCopy: $("clipping-link-permission-copy"),
    linkPermissionCheck: $("clipping-link-permission-check"),
    linkHelp: $("clipping-link-help"),
    linkError: $("clipping-link-error"),
    linkSubmit: $("clipping-link-submit"),
    jobBudget: $("clipping-job-budget"),
    sourcesError: $("clipping-sources-error"),
    sourceSummary: $("clipping-source-summary"),
    sources: $("clipping-sources"),
    batchJobBudget: $("clipping-batch-job-budget"),
    batchBudget: $("clipping-batch-budget"),
    batchBudgetNote: $("clipping-batch-budget-note"),
    createBatch: $("clipping-create-batch"),
    batchError: $("clipping-batch-error"),
    jobsError: $("clipping-jobs-error"),
    jobs: $("clipping-jobs"),
    jobCount: $("clipping-job-count"),
    batchesError: $("clipping-batches-error"),
    batches: $("clipping-batches"),
    batchCount: $("clipping-batch-count"),
    retentionForm: $("clipping-retention-form"),
    retentionDays: $("clipping-retention-days"),
    retentionSave: $("clipping-retention-save"),
    retentionError: $("clipping-retention-error"),
    retentionNote: $("clipping-retention-note")
  };

  const state = {
    active: false,
    lifecycle: 0,
    requestSequence: { config: 0, sources: 0, jobs: 0, batches: 0 },
    requestImpl: null,
    controllers: new Set(),
    sources: [],
    jobs: [],
    batches: [],
    selectedSourceIDs: new Set(),
    pendingJobKeys: new Map(),
    pendingBatchKeys: new Map(),
    linkConsentValue: "",
    config: null,
    upload: null,
    resumeSourceID: "",
    resumeHelp: null
  };

  const sourceStatusCopy = {
    uploading: "Upload paused or in progress",
    importing: "Getting the source ready",
    ready: "Ready for analysis",
    failed: "Could not prepare this source",
    deleted: "Removed"
  };
  const youtubeSourceFailureCopy = {
    youtube_import_unavailable: "This YouTube import could not be completed by the available public importer. Upload an original video file you have permission to reuse.",
    youtube_video_unavailable: "This YouTube video could not be fetched through its public link; it may require sign-in or be restricted. Upload an original video file you have permission to reuse.",
    youtube_format_unavailable: "No supported audio and video format is available for this YouTube video. Upload an original video file you have permission to reuse.",
    youtube_duration_limit_exceeded: "This YouTube video exceeds the four-hour limit. Upload an original video file you have permission to reuse.",
    youtube_size_limit_exceeded: "This YouTube video exceeds the 20 GiB limit. Upload an original video file you have permission to reuse.",
    youtube_media_invalid: "This source is not a supported video. Upload an original video file you have permission to reuse."
  };
  const jobStatusCopy = {
    queued: "Queued for analysis",
    running: "Analysis in progress",
    paused_budget: "Analysis paused",
    completed: "Analysis complete",
    failed: "Analysis stopped with an error",
    canceled: "Canceled"
  };

  function current(lifecycle) {
    return state.active && lifecycle === state.lifecycle;
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function button(text, className, action) {
    const node = element("button", className || "button secondary", text);
    node.type = "button";
    node.addEventListener("click", action);
    return node;
  }

  function safeID(value) {
    const id = String(value ?? "").trim();
    return /^[A-Za-z0-9_-]{1,128}$/.test(id) ? id : "";
  }

  function safeProgress(value) {
    const progress = Number(value);
    if (!Number.isFinite(progress)) return 0;
    return Math.max(0, Math.min(100, progress <= 1 ? progress * 100 : progress));
  }

  function sourceFailureCopy(source) {
    const failure = String(source && source.error || "");
    if (!failure) return "";
    if (source && source.kind === "youtube_original_file") {
      return youtubeSourceFailureCopy[failure]
        || "This public YouTube import could not be completed. The video may be unavailable, restricted, or missing a supported format. Upload an original video file you have permission to reuse.";
    }
    return failure;
  }

  function sourceName(source) {
    return String(source.original_name || source.name || source.title || "Untitled source");
  }

  function sourceSize(source) {
    const value = Number(source.declared_size_bytes ?? source.size_bytes);
    if (!Number.isSafeInteger(value) || value < 0) return "Size unavailable";
    if (value < 1024 * 1024) return `${Math.max(1, Math.round(value / 1024))} KB`;
    return `${(value / (1024 * 1024)).toFixed(value >= 10 * 1024 * 1024 ? 0 : 1)} MB`;
  }

  function statusCopy(status, table) {
    return table[String(status || "").toLowerCase()] || "Status unavailable";
  }

  function jobErrorCopy(job) {
    const message = String(job && job.error || "").trim();
    if (/dispatch outcome is unknown/i.test(message)) {
      return "The previous attempt has not reported its result yet. Any reserved amount will be reconciled when it is resolved.";
    }
    if (/budget limit reached/i.test(message)) return "The job paused after reaching its budget limit.";
    return message;
  }

  function safeMediaURL(value) {
    if (!value) return "";
    try {
      const parsed = new URL(String(value), root.location.origin);
      if (parsed.origin !== root.location.origin || !parsed.pathname.startsWith("/api/clipping/sources/")
        || !parsed.pathname.endsWith("/media")) return "";
      return parsed.pathname + parsed.search;
    } catch (_error) {
      return "";
    }
  }

  function api(path, options = {}) {
    if (!state.active || typeof state.requestImpl !== "function") {
      return Promise.reject(new Error("Sign in to use the clipping workspace."));
    }
    const lifecycle = state.lifecycle;
    const controller = options.controller instanceof AbortController ? options.controller : new AbortController();
    state.controllers.add(controller);
    const requestOptions = { ...options, signal: options.signal || controller.signal };
    delete requestOptions.controller;
    return state.requestImpl(path, requestOptions).finally(() => {
      state.controllers.delete(controller);
      if (!current(lifecycle)) return;
    });
  }

  function renderWorkerNote() {
    const config = state.config || {};
    const available = config.worker_available === true;
    const note = available
      ? "Sources are ready for analysis. Adding or uploading a source does not start analysis."
      : "You can prepare sources and queue analysis jobs. They will wait until analysis is ready to run. Adding media does not start paid analysis.";
    ui.workerNote.textContent = note;
    ui.workerNote.hidden = false;
  }

  function updateUploadProgress(upload, offset, message) {
    const size = Math.max(1, Number(upload.size) || 1);
    const percent = Math.max(0, Math.min(100, Math.floor((offset / size) * 100)));
    ui.uploadProgress.hidden = false;
    ui.uploadLabel.textContent = upload.name;
    ui.uploadPercent.textContent = `${percent}%`;
    ui.uploadBar.style.width = `${percent}%`;
    ui.uploadBar.setAttribute("aria-valuenow", String(percent));
    ui.uploadDetail.textContent = message || `${sourceSize({ size_bytes: offset })} of ${sourceSize({ size_bytes: size })} sent.`;
  }

  function setUploadIdle() {
    state.upload = null;
    ui.uploadSubmit.disabled = false;
    ui.uploadFile.disabled = false;
    ui.uploadCancel.disabled = false;
  }

  function renderSources() {
    const fragment = document.createDocumentFragment();
    const readySources = state.sources.filter((source) => String(source.status).toLowerCase() === "ready");
    ui.sourceSummary.textContent = `${state.sources.length} ${state.sources.length === 1 ? "source" : "sources"} · ${readySources.length} ready`;
    if (!state.sources.length) {
      fragment.append(element("p", "empty", "Sources will appear here after you add footage."));
    }
    for (const source of state.sources) {
      const id = safeID(source.id);
      if (!id) continue;
      const status = String(source.status || "").toLowerCase();
      const row = element("article", "clipping-row");
      row.dataset.sourceId = id;
      const details = element("div", "clipping-row-main");
      const title = element("strong", "clipping-row-title", sourceName(source));
      const meta = element("div", "clipping-row-meta");
      const duration = Number(source.duration_ms) > 0 ? formatDuration(source.duration_ms) : "Duration pending";
      meta.append(element("span", "", `${duration} · ${sourceSize(source)}`));
      if (status === "ready") meta.append(element("span", "", safeMediaURL(source.media_url) ? "Media ready" : "Media unavailable"));
      const stateLine = element("p", "clipping-row-state", statusCopy(status, sourceStatusCopy));
      details.append(title, meta, stateLine);
      if (status === "uploading") {
        const total = Number(source.declared_size_bytes ?? source.size_bytes);
        const uploaded = Number(source.upload_offset);
        if (Number.isFinite(total) && total > 0 && Number.isFinite(uploaded)) {
          const percent = Math.max(0, Math.min(100, Math.floor((uploaded / total) * 100)));
          const progress = element("div", "clipping-inline-progress");
          const track = element("div", "clipping-progress-track");
          const bar = element("div", "clipping-progress-bar");
          bar.style.width = `${percent}%`;
          bar.setAttribute("role", "progressbar");
          bar.setAttribute("aria-label", `Upload progress for ${sourceName(source)}`);
          bar.setAttribute("aria-valuemin", "0");
          bar.setAttribute("aria-valuemax", "100");
          bar.setAttribute("aria-valuenow", String(percent));
          track.append(bar);
          progress.append(track, element("span", "", `${percent}% uploaded`));
          details.append(progress);
        }
      } else if (status === "importing" && Number.isFinite(Number(source.progress))) {
        const percent = Math.round(safeProgress(source.progress));
        const progress = element("div", "clipping-inline-progress");
        const track = element("div", "clipping-progress-track");
        const bar = element("div", "clipping-progress-bar");
        bar.style.width = `${percent}%`;
        bar.setAttribute("role", "progressbar");
        bar.setAttribute("aria-label", `Source import progress for ${sourceName(source)}`);
        bar.setAttribute("aria-valuemin", "0");
        bar.setAttribute("aria-valuemax", "100");
        bar.setAttribute("aria-valuenow", String(percent));
        track.append(bar);
        progress.append(track, element("span", "", `${percent}% ready`));
        details.append(progress);
      }
      const failureCopy = sourceFailureCopy(source);
      if (failureCopy) details.append(element("p", "clipping-row-error", failureCopy));
      const actions = element("div", "clipping-row-actions");
      if (status === "ready") {
        const checkLabel = element("label", "clipping-check clipping-select");
        const check = element("input");
        check.type = "checkbox";
        check.checked = state.selectedSourceIDs.has(id);
        check.setAttribute("aria-label", `Select ${sourceName(source)} for a batch`);
        check.addEventListener("change", () => {
          if (check.checked) state.selectedSourceIDs.add(id);
          else state.selectedSourceIDs.delete(id);
          renderBatchReadiness();
        });
        checkLabel.append(check, element("span", "", "Batch"));
        actions.append(checkLabel);
        actions.append(button("Start analysis", "button secondary clipping-action", () => { void createJob(id); }));
      } else if (status === "uploading") {
        actions.append(button("Resume upload", "button secondary clipping-action", () => chooseResumeFile(id)));
      }
      actions.append(button("Remove", "text-button clipping-remove", () => { void removeSource(id); }));
      row.append(details, actions);
      fragment.append(row);
    }
    ui.sources.replaceChildren(fragment);
    renderBatchReadiness();
  }

  function renderJob(job) {
    const row = element("article", "clipping-record");
    const header = element("div", "clipping-record-header");
    const source = state.sources.find((item) => String(item.id) === String(job.source_id));
    header.append(element("strong", "", source ? sourceName(source) : "Source analysis"));
    const status = String(job.status || "").toLowerCase();
    const hasUncertainStage = Array.isArray(job.stages)
      && job.stages.some((stage) => String(stage && stage.status || "").toLowerCase() === "uncertain");
    const statusText = hasUncertainStage
      ? "Waiting for the previous attempt to be resolved"
      : status === "queued" && state.config?.worker_available === false
        ? "Waiting for analysis to be configured"
        : statusCopy(status, jobStatusCopy);
    header.append(element("span", "badge clipping-status", statusText));
    const progress = safeProgress(job.progress);
    const track = element("div", "clipping-progress-track");
    const bar = element("div", "clipping-progress-bar");
    bar.style.width = `${progress}%`;
    bar.setAttribute("role", "progressbar");
    bar.setAttribute("aria-label", "Analysis job progress");
    bar.setAttribute("aria-valuemin", "0");
    bar.setAttribute("aria-valuemax", "100");
    bar.setAttribute("aria-valuenow", String(Math.round(progress)));
    track.append(bar);
    const footer = element("div", "clipping-record-footer");
    const spent = Number(job.spent_micro_usd);
    const budget = Number(job.budget_micro_usd);
    const spendLabel = Number.isSafeInteger(budget) ? `${formatUSD(spent)} of ${formatUSD(budget)}` : "Budget recorded";
    footer.append(element("span", "", spendLabel));
    const actions = element("div", "clipping-row-actions");
    if (["queued", "running", "paused_budget"].includes(status)) {
      actions.append(button("Cancel job", "button secondary clipping-action", () => { void cancelJob(job.id); }));
    }
    row.append(header, track, footer);
    const errorCopy = jobErrorCopy(job);
    if (errorCopy) row.append(element("p", "clipping-row-error", errorCopy));
    if (actions.children.length) row.append(actions);
    return row;
  }

  function renderJobs() {
    const fragment = document.createDocumentFragment();
    const jobs = state.jobs;
    ui.jobCount.textContent = String(jobs.length);
    if (!jobs.length) fragment.append(element("p", "empty", "No analysis jobs yet."));
    jobs.forEach((job) => fragment.append(renderJob(job)));
    ui.jobs.replaceChildren(fragment);
  }

  function renderBatches() {
    const fragment = document.createDocumentFragment();
    const batches = state.batches;
    ui.batchCount.textContent = String(batches.length);
    if (!batches.length) fragment.append(element("p", "empty", "No batches yet."));
    for (const batch of batches) {
      const row = element("article", "clipping-record");
      const header = element("div", "clipping-record-header");
      const count = Array.isArray(batch.job_ids) ? batch.job_ids.length : Array.isArray(batch.source_ids) ? batch.source_ids.length : 0;
      header.append(element("strong", "", `${count} ${count === 1 ? "source" : "sources"}`));
      const status = String(batch.status || "").toLowerCase();
      const statusText = status === "queued" && state.config?.worker_available === false
        ? "Waiting for analysis to be configured"
        : statusCopy(status, jobStatusCopy);
      header.append(element("span", "badge clipping-status", statusText));
      const progress = safeProgress(batch.progress);
      const track = element("div", "clipping-progress-track");
      const bar = element("div", "clipping-progress-bar");
      bar.style.width = `${progress}%`;
      bar.setAttribute("role", "progressbar");
      bar.setAttribute("aria-label", "Batch progress");
      bar.setAttribute("aria-valuemin", "0");
      bar.setAttribute("aria-valuemax", "100");
      bar.setAttribute("aria-valuenow", String(Math.round(progress)));
      track.append(bar);
      const footer = element("div", "clipping-record-footer");
      footer.append(element("span", "", `${formatUSD(batch.spent_micro_usd)} of ${formatUSD(batch.budget_micro_usd)}`));
      if (["queued", "running", "paused_budget"].includes(status)) {
        footer.append(button("Cancel batch", "button secondary clipping-action", () => { void cancelBatch(batch.id); }));
      }
      row.append(header, track, footer);
      if (batch.error) row.append(element("p", "clipping-row-error", batch.error));
      fragment.append(row);
    }
    ui.batches.replaceChildren(fragment);
  }

  function renderBatchReadiness() {
    const readyIDs = new Set(state.sources.filter((source) => String(source.status).toLowerCase() === "ready").map((source) => safeID(source.id)));
    for (const id of Array.from(state.selectedSourceIDs)) if (!readyIDs.has(id)) state.selectedSourceIDs.delete(id);
    const count = state.selectedSourceIDs.size;
    const perJob = parseBudgetUSD(ui.batchJobBudget.value, MAX_JOB_BUDGET_MICRO_USD);
    const aggregate = parseBudgetUSD(ui.batchBudget.value, MAX_BATCH_BUDGET_MICRO_USD);
    const required = perJob === null ? null : perJob * count;
    const fits = count > 0 && perJob !== null && aggregate !== null && Number.isSafeInteger(required) && required <= aggregate;
    ui.createBatch.disabled = !fits;
    if (count === 0) ui.batchBudgetNote.textContent = "Select ready sources. The combined job limits must fit within the batch limit.";
    else if (perJob === null || aggregate === null) ui.batchBudgetNote.textContent = "Enter positive budget amounts in US dollars.";
    else if (!fits) ui.batchBudgetNote.textContent = `${count} selected ${count === 1 ? "source needs" : "sources need"} at least ${formatUSD(required)} in aggregate budget.`;
    else ui.batchBudgetNote.textContent = `${count} selected ${count === 1 ? "source" : "sources"} · at least ${formatUSD(required)} reserved within a ${formatUSD(aggregate)} batch limit.`;
  }

  async function load(kind, path, render, errorElement) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence[kind];
    errorElement.textContent = "";
    try {
      const payload = await api(path);
      if (!current(lifecycle) || sequence !== state.requestSequence[kind]) return;
      const record = Array.isArray(payload) ? payload : payload && (payload[kind] || payload.items) || [];
      if (kind === "sources") state.sources = Array.isArray(record) ? record : [];
      if (kind === "jobs") state.jobs = Array.isArray(record) ? record : [];
      if (kind === "batches") state.batches = Array.isArray(record) ? record : [];
      render();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence[kind] || error.name === "AbortError") return;
      errorElement.textContent = error.message || "Could not load clipping information.";
    }
  }

  async function loadConfig() {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence.config;
    ui.retentionError.textContent = "";
    try {
      const payload = await api("/api/clipping/config");
      if (!current(lifecycle) || sequence !== state.requestSequence.config) return;
      state.config = payload || {};
      renderWorkerNote();
      const days = Number(state.config.retention_days ?? state.config.default_retention_days);
      if (Number.isInteger(days) && days > 0 && days <= 365) ui.retentionDays.value = String(days);
      const canChange = state.config.retention_configurable !== false;
      ui.retentionDays.disabled = !canChange;
      ui.retentionSave.disabled = !canChange;
      const policy = canChange
        ? `Sources are kept for ${Number(ui.retentionDays.value) || days || 30} days. Save changes to apply them to new and existing sources.`
        : `Sources are kept for ${days || 30} days. Remove an individual source below when you no longer need it.`;
      ui.retentionNote.textContent = policy;
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence.config || error.name === "AbortError") return;
      ui.workerNote.textContent = "Analysis availability could not be checked. You can still prepare sources; no analysis starts automatically.";
      ui.workerNote.hidden = false;
      ui.retentionNote.textContent = "Current retention settings could not be loaded.";
      ui.retentionError.textContent = error.message || "Could not load retention settings.";
    }
  }

  function refresh() {
    if (!state.active) return Promise.resolve();
    return Promise.allSettled([
      loadConfig(),
      load("sources", "/api/clipping/sources", renderSources, ui.sourcesError),
      load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError),
      load("batches", "/api/clipping/batches", renderBatches, ui.batchesError)
    ]);
  }

  function friendlyError(error, kind) {
    if (error && error.message) return error.message;
    if (kind === "youtube" || kind === "youtube_invalid") {
      return "This public YouTube video could not be imported. It may require sign-in or lack a supported audio/video format. Upload an original file you have permission to reuse.";
    }
    return "The request could not be completed.";
  }

  function classifyYouTubeLink(url, raw) {
    const host = url.hostname.toLowerCase();
    const videoHosts = new Set([
      "youtube.com", "www.youtube.com", "m.youtube.com",
      "youtube-nocookie.com", "www.youtube-nocookie.com"
    ]);
    const shortHosts = new Set(["youtu.be"]);
    if (!videoHosts.has(host) && !shortHosts.has(host)) return null;
    if (url.protocol !== "https:" || url.username || url.password || (url.port && url.port !== "443") || raw.includes("#")) return "youtube_invalid";
    const playlistSelectors = new Set(["list", "playlist", "playlist_id", "video_ids", "start_radio", "index"]);
    for (const key of url.searchParams.keys()) {
      if (playlistSelectors.has(key.toLowerCase())) return "youtube_invalid";
    }

    const videoIDPattern = /^[A-Za-z0-9_-]{11}$/;
    let videoID = "";
    if (shortHosts.has(host)) {
      const match = /^\/([A-Za-z0-9_-]{11})$/.exec(url.pathname);
      if (match) videoID = match[1];
    } else if (url.pathname === "/watch") {
      const ids = url.searchParams.getAll("v");
      if (ids.length === 1) videoID = ids[0];
    } else {
      const match = /^\/(?:shorts|embed)\/([A-Za-z0-9_-]{11})$/.exec(url.pathname);
      if (match) videoID = match[1];
    }
    return videoIDPattern.test(videoID) ? "youtube" : "youtube_invalid";
  }

  function classifyLink(value) {
    const raw = String(value || "").trim();
    let url;
    try { url = new URL(raw); } catch (_error) { return "invalid"; }
    const youtubeKind = classifyYouTubeLink(url, raw);
    if (youtubeKind) return youtubeKind;
    if (url.protocol !== "https:") return "invalid";
    const host = url.hostname.toLowerCase().replace(/^www\./, "");
    if (host === "drive.google.com" || host === "docs.google.com") return "google_drive";
    if (host === "dropbox.com" || host.endsWith(".dropboxusercontent.com")) return "dropbox";
    return "unsupported";
  }

  function updateLinkDisclosure() {
    const kind = classifyLink(ui.link.value);
    const currentURL = ui.link.value.trim();
    if (currentURL !== state.linkConsentValue) {
      state.linkConsentValue = currentURL;
      ui.linkPermissionCheck.checked = false;
    }
    const youtube = kind === "youtube" || kind === "youtube_invalid";
    ui.linkHelp.hidden = !youtube;
    if (youtube) {
      ui.linkHelp.textContent = "Public YouTube import supports one HTTPS watch, shorts, embed, or youtu.be video that is available without sign-in. Playlists, live or upcoming streams, restricted videos, and unsupported formats require an original-file upload you are permitted to reuse.";
    }
    ui.linkPermissionCopy.textContent = youtube
      ? "I have permission to reuse this YouTube footage and have it processed."
      : "I have permission to reuse this video and have it processed.";
    ui.linkSubmit.disabled = !ui.linkPermissionCheck.checked;
    const note = ui.linkForm.querySelector(".field small");
    if (note) note.textContent = youtube
      ? "Use one HTTPS public YouTube watch?v=, /shorts/, /embed/, or youtu.be/{id} link. Access and supported formats can vary."
      : kind === "unsupported"
        ? "Use a public Google Drive or Dropbox link, or a supported single-video YouTube link."
        : "Google Drive and Dropbox links must be publicly accessible.";
  }

  async function submitImport(event) {
    event.preventDefault();
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    ui.linkError.textContent = "";
    const kind = classifyLink(ui.link.value);
    if (kind === "invalid" || kind === "unsupported" || kind === "youtube_invalid") {
      ui.linkError.textContent = kind === "invalid"
        ? "Enter a valid HTTPS link."
        : kind === "youtube_invalid"
          ? "Use one HTTPS public YouTube watch, shorts, embed, or youtu.be video link. Playlists and live or upcoming streams are not supported; restricted videos or unsupported formats need an original-file upload you are permitted to reuse."
          : "Use a public Google Drive or Dropbox link, or a supported single-video YouTube link.";
      return;
    }
    if (!ui.linkPermissionCheck.checked) {
      ui.linkError.textContent = kind === "youtube" || kind === "youtube_invalid"
        ? "Confirm that you have permission to reuse and process this YouTube footage."
        : "Confirm that you have permission to reuse this video and have it processed.";
      return;
    }
    ui.linkSubmit.disabled = true;
    const label = ui.linkSubmit.textContent;
    ui.linkSubmit.textContent = "Checking link…";
    try {
      await api("/api/clipping/sources/import", {
        method: "POST",
        body: JSON.stringify({ url: ui.link.value.trim(), rights_attested: true })
      });
      if (!current(lifecycle)) return;
      ui.linkForm.reset();
      updateLinkDisclosure();
      await refresh();
    } catch (error) {
      if (current(lifecycle)) ui.linkError.textContent = friendlyError(error, kind);
    } finally {
      if (current(lifecycle)) {
        ui.linkSubmit.textContent = label;
        updateLinkDisclosure();
      }
    }
  }

  function chooseResumeFile(sourceID) {
    state.resumeSourceID = sourceID;
    setResumeHelp(true);
    ui.resumeFile.value = "";
    ui.resumeFile.click();
  }

  function setResumeHelp(visible) {
    if (visible && !state.resumeHelp) {
      state.resumeHelp = element("p", "clipping-help", "Choose the exact original file to resume. Matching name and size do not verify its contents.");
      state.resumeHelp.setAttribute("role", "note");
      ui.uploadForm.append(state.resumeHelp);
    }
    if (state.resumeHelp) state.resumeHelp.hidden = !visible;
  }

  async function startUpload(file, existingSource = null) {
    const lifecycle = state.lifecycle;
    if (!file || !current(lifecycle) || state.upload) return;
    setResumeHelp(Boolean(existingSource));
    if (!Number.isSafeInteger(file.size) || file.size <= 0) {
      ui.uploadError.textContent = "Choose a non-empty video file.";
      return;
    }
    const maxBytes = Number(state.config && state.config.max_source_bytes);
    if (Number.isSafeInteger(maxBytes) && maxBytes > 0 && file.size > maxBytes) {
      ui.uploadError.textContent = `This file is larger than the ${sourceSize({ size_bytes: maxBytes })} limit.`;
      return;
    }
    let source = existingSource;
    if (source) {
      const expectedName = sourceName(source);
      const expectedSize = Number(source.declared_size_bytes ?? source.size_bytes);
      if (file.name !== expectedName || file.size !== expectedSize) {
        ui.uploadError.textContent = "Choose the same original file to resume this upload.";
        return;
      }
    }
    const upload = {
      name: file.name,
      size: file.size,
      sourceID: source ? safeID(source.id) : "",
      controller: null
    };
    state.upload = upload;
    ui.uploadError.textContent = "";
    ui.uploadSubmit.disabled = true;
    ui.uploadFile.disabled = true;
    ui.uploadProgress.hidden = false;
    updateUploadProgress(upload, Number(source && source.upload_offset) || 0, source ? "Resuming from saved progress…" : "Preparing upload…");
    try {
      if (!source) {
        const initController = new AbortController();
        upload.controller = initController;
        const result = await api("/api/clipping/sources/upload", {
          method: "POST",
          signal: initController.signal,
          controller: initController,
          body: JSON.stringify({
            original_name: file.name,
            media_type: file.type || "application/octet-stream",
            size_bytes: file.size,
            rights_attested: true
          })
        });
        if (!current(lifecycle) || state.upload !== upload) return;
        source = result && (result.source || result);
        upload.sourceID = safeID(source && source.id);
        if (!upload.sourceID) throw new Error("The upload could not be started. Refresh the source list and try again.");
        await load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
      }
      let offset = validResumeOffset(source.upload_offset ?? 0, file.size);
      if (offset === null) {
        throw new Error("Saved upload progress was invalid. Remove this source and start again.");
      }
      while (offset < file.size) {
        if (!current(lifecycle) || state.upload !== upload) return;
        const range = nextUploadRange(offset, file.size, state.config);
        const chunk = file.slice(range.start, range.end);
        const controller = new AbortController();
        upload.controller = controller;
        const chunkResult = await api(`/api/clipping/sources/${encodeURIComponent(upload.sourceID)}/upload`, {
          method: "PUT",
          body: chunk,
          headers: { "Content-Type": "application/octet-stream", "Upload-Offset": String(offset) },
          signal: controller.signal,
          controller
        });
        if (!current(lifecycle) || state.upload !== upload) return;
        const updated = chunkResult && (chunkResult.source || chunkResult);
        const nextOffset = Number(updated && updated.upload_offset);
        offset = Number.isSafeInteger(nextOffset) ? nextOffset : range.end;
        if (offset < range.end || offset > file.size) throw new Error("Upload progress could not be confirmed. Resume this source to continue safely.");
        source = { ...source, ...(updated || {}), upload_offset: offset };
        updateUploadProgress(upload, offset, `${sourceSize({ size_bytes: offset })} of ${sourceSize({ size_bytes: file.size })} sent.`);
      }
      if (!current(lifecycle) || state.upload !== upload) return;
      ui.uploadLabel.textContent = "Checking video…";
      const finalizeController = new AbortController();
      upload.controller = finalizeController;
      const finalized = await api(`/api/clipping/sources/${encodeURIComponent(upload.sourceID)}/finalize`, {
        method: "POST",
        body: JSON.stringify({}),
        signal: finalizeController.signal,
        controller: finalizeController
      });
      if (!current(lifecycle) || state.upload !== upload) return;
      updateUploadProgress(upload, file.size, "Upload complete. Checking that the video can be used.");
      setUploadIdle();
      ui.uploadFile.value = "";
      ui.uploadPermission.checked = false;
      if (existingSource) setResumeHelp(false);
      await refresh();
      const prepared = finalized && (finalized.source || finalized);
      ui.uploadDetail.textContent = statusCopy(prepared && prepared.status, sourceStatusCopy);
    } catch (error) {
      if (!current(lifecycle) || state.upload !== upload || error.name === "AbortError") return;
      ui.uploadError.textContent = error.message || "Upload paused. Use Resume upload to continue from saved progress.";
      ui.uploadDetail.textContent = "Progress is saved. Choose Resume upload to continue.";
      if (upload.sourceID) {
        ui.uploadFile.value = "";
        ui.uploadPermission.checked = false;
      }
      await load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
    } finally {
      if (current(lifecycle) && state.upload === upload && !upload.controller?.signal.aborted) setUploadIdle();
    }
  }

  async function createJob(sourceID) {
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const budget = parseBudgetUSD(ui.jobBudget.value, MAX_JOB_BUDGET_MICRO_USD);
    if (budget === null) {
      ui.sourcesError.textContent = "Enter a per-job budget from $0.01 to $4.00.";
      ui.jobBudget.focus();
      return;
    }
    ui.sourcesError.textContent = "";
    const buttonNode = Array.from(ui.sources.querySelectorAll("button")).find((node) => node.textContent === "Start analysis" && node.closest("article")?.dataset.sourceId === sourceID);
    if (buttonNode) buttonNode.disabled = true;
    const fingerprint = `${sourceID}:${budget}`;
    const idempotencyKey = state.pendingJobKeys.get(fingerprint) || newIdempotencyKey();
    state.pendingJobKeys.set(fingerprint, idempotencyKey);
    try {
      await api("/api/clipping/jobs", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ source_id: sourceID, budget_micro_usd: budget })
      });
      if (!current(lifecycle)) return;
      state.pendingJobKeys.delete(fingerprint);
      await Promise.allSettled([load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError), load("batches", "/api/clipping/batches", renderBatches, ui.batchesError)]);
    } catch (error) {
      if (current(lifecycle)) ui.sourcesError.textContent = error.message || "The analysis job could not be queued.";
    } finally {
      if (current(lifecycle) && buttonNode) buttonNode.disabled = false;
    }
  }

  async function createBatch() {
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const ids = Array.from(state.selectedSourceIDs);
    ids.sort();
    const perJob = parseBudgetUSD(ui.batchJobBudget.value, MAX_JOB_BUDGET_MICRO_USD);
    const aggregate = parseBudgetUSD(ui.batchBudget.value, MAX_BATCH_BUDGET_MICRO_USD);
    if (!ids.length || perJob === null || aggregate === null || !Number.isSafeInteger(perJob * ids.length) || perJob * ids.length > aggregate) {
      renderBatchReadiness();
      return;
    }
    ui.batchError.textContent = "";
    ui.createBatch.disabled = true;
    const fingerprint = `${ids.join(",")}:${perJob}:${aggregate}`;
    const idempotencyKey = state.pendingBatchKeys.get(fingerprint) || newIdempotencyKey();
    state.pendingBatchKeys.set(fingerprint, idempotencyKey);
    try {
      await api("/api/clipping/batches", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ source_ids: ids, per_job_budget_micro_usd: perJob, budget_micro_usd: aggregate })
      });
      if (!current(lifecycle)) return;
      state.pendingBatchKeys.delete(fingerprint);
      state.selectedSourceIDs.clear();
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError),
        load("batches", "/api/clipping/batches", renderBatches, ui.batchesError),
        load("sources", "/api/clipping/sources", renderSources, ui.sourcesError)
      ]);
    } catch (error) {
      if (current(lifecycle)) ui.batchError.textContent = error.message || "The batch could not be queued.";
    } finally {
      if (current(lifecycle)) renderBatchReadiness();
    }
  }

  async function cancelJob(jobID) {
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const id = safeID(jobID);
    if (!id) return;
    try {
      await api(`/api/clipping/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: JSON.stringify({}) });
      if (!current(lifecycle)) return;
      await load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError);
      await load("batches", "/api/clipping/batches", renderBatches, ui.batchesError);
    } catch (error) {
      if (current(lifecycle)) ui.jobsError.textContent = error.message || "The job could not be canceled.";
    }
  }

  async function cancelBatch(batchID) {
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const id = safeID(batchID);
    if (!id) return;
    try {
      await api(`/api/clipping/batches/${encodeURIComponent(id)}/cancel`, { method: "POST", body: JSON.stringify({}) });
      if (!current(lifecycle)) return;
      await load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError);
      await load("batches", "/api/clipping/batches", renderBatches, ui.batchesError);
    } catch (error) {
      if (current(lifecycle)) ui.batchesError.textContent = error.message || "The batch could not be canceled.";
    }
  }

  async function removeSource(sourceID) {
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const id = safeID(sourceID);
    if (!id) return;
    if (typeof root.confirm === "function" && !root.confirm("Remove this source and its stored media?")) return;
    try {
      if (state.upload?.sourceID === id) {
        state.upload.controller?.abort();
        state.upload = null;
        ui.uploadSubmit.disabled = false;
        ui.uploadFile.disabled = false;
      }
      await api(`/api/clipping/sources/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (!current(lifecycle)) return;
      state.selectedSourceIDs.delete(id);
      await refresh();
    } catch (error) {
      if (current(lifecycle)) ui.sourcesError.textContent = error.message || "The source could not be removed.";
    }
  }

  async function saveRetention(event) {
    event.preventDefault();
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    const days = Number(ui.retentionDays.value);
    if (!Number.isInteger(days) || days < 1 || days > 365) {
      ui.retentionError.textContent = "Choose a retention period from 1 to 365 days.";
      return;
    }
    ui.retentionError.textContent = "";
    ui.retentionSave.disabled = true;
    try {
      await api("/api/clipping/config/retention", { method: "PUT", body: JSON.stringify({ retention_days: days }) });
      if (!current(lifecycle)) return;
      ui.retentionNote.textContent = `Sources are kept for ${days} days. This applies to new and existing sources.`;
      await Promise.allSettled([loadConfig(), load("sources", "/api/clipping/sources", renderSources, ui.sourcesError)]);
    } catch (error) {
      if (current(lifecycle)) ui.retentionError.textContent = error.message || "Retention settings could not be saved.";
    } finally {
      if (current(lifecycle)) ui.retentionSave.disabled = state.config?.retention_configurable === false;
    }
  }

  let idempotencyCounter = 0;
  function newIdempotencyKey() {
    if (root.crypto && typeof root.crypto.randomUUID === "function") return root.crypto.randomUUID();
    idempotencyCounter++;
    return `clipping-${Date.now()}-${idempotencyCounter}-${Math.random().toString(16).slice(2)}`;
  }

  function bind() {
    ui.refresh.addEventListener("click", () => { void refresh(); });
    ui.uploadForm.addEventListener("submit", (event) => {
      event.preventDefault();
      const file = ui.uploadFile.files && ui.uploadFile.files[0];
      if (!ui.uploadPermission.checked) {
        ui.uploadError.textContent = "Confirm that you own or have permission to reuse this video and have it processed.";
        return;
      }
      if (file) void startUpload(file);
      else ui.uploadError.textContent = "Choose a video file to upload.";
    });
    ui.uploadFile.addEventListener("change", () => {
      ui.uploadPermission.checked = false;
    });
    ui.uploadCancel.addEventListener("click", () => {
      if (!state.upload) return;
      state.upload.controller?.abort();
      const hadSource = Boolean(state.upload.sourceID);
      if (!hadSource) setResumeHelp(false);
      state.upload = null;
      ui.uploadSubmit.disabled = false;
      ui.uploadFile.disabled = false;
      ui.uploadDetail.textContent = "Upload paused. Progress is saved; choose Resume upload to continue.";
      if (hadSource) {
        ui.uploadFile.value = "";
        ui.uploadPermission.checked = false;
      }
      void load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
    });
    ui.resumeFile.addEventListener("change", () => {
      const file = ui.resumeFile.files && ui.resumeFile.files[0];
      const source = state.sources.find((item) => String(item.id) === state.resumeSourceID);
      state.resumeSourceID = "";
      if (file && source) void startUpload(file, source);
    });
    ui.link.addEventListener("input", updateLinkDisclosure);
    ui.link.addEventListener("change", updateLinkDisclosure);
    ui.linkPermissionCheck.addEventListener("change", updateLinkDisclosure);
    ui.linkForm.addEventListener("submit", submitImport);
    [ui.batchJobBudget, ui.batchBudget].forEach((input) => input.addEventListener("input", renderBatchReadiness));
    ui.createBatch.addEventListener("click", () => { void createBatch(); });
    ui.retentionForm.addEventListener("submit", saveRetention);
    updateLinkDisclosure();
  }

  function configure(options) {
    state.requestImpl = options && typeof options.request === "function" ? options.request : null;
  }

  function start() {
    if (!state.requestImpl) return Promise.resolve();
    if (!state.active) {
      state.active = true;
      state.lifecycle++;
    }
    return refresh();
  }

  function stop(clear = true) {
    state.active = false;
    state.lifecycle++;
    state.controllers.forEach((controller) => controller.abort());
    state.controllers.clear();
    if (state.upload) state.upload.controller?.abort();
    state.upload = null;
    ui.uploadSubmit.disabled = false;
    ui.uploadFile.disabled = false;
    ui.uploadCancel.disabled = false;
    if (clear) {
      state.sources = [];
      state.jobs = [];
      state.batches = [];
      state.selectedSourceIDs.clear();
      state.config = null;
      ui.sources.replaceChildren(element("p", "empty", "Sources will appear here after you add footage."));
      ui.jobs.replaceChildren(element("p", "empty", "No analysis jobs yet."));
      ui.batches.replaceChildren(element("p", "empty", "No batches yet."));
      ui.workerNote.hidden = true;
      ui.uploadProgress.hidden = true;
      ui.uploadError.textContent = "";
      ui.uploadFile.value = "";
      ui.uploadPermission.checked = false;
      ui.resumeFile.value = "";
      state.resumeSourceID = "";
      setResumeHelp(false);
      ui.link.value = "";
      ui.linkPermissionCheck.checked = false;
      ui.linkConsentValue = "";
      ui.linkError.textContent = "";
      ui.linkSubmit.textContent = "Import link";
      updateLinkDisclosure();
    }
  }

  function handleEvent(kind, event) {
    if (!state.active) return;
    const key = kind === "clipping_source" ? "sources" : kind === "clipping_job" ? "jobs" : kind === "clipping_batch" ? "batches" : "";
    if (!key) return;
    try {
      const payload = JSON.parse(event.data);
      const id = safeID(payload && payload.id);
      if (!id) return;
    } catch (_error) {
      return;
    }
    if (key === "sources") void load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
    if (key === "jobs") void Promise.allSettled([load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError), load("batches", "/api/clipping/batches", renderBatches, ui.batchesError)]);
    if (key === "batches") void Promise.allSettled([load("batches", "/api/clipping/batches", renderBatches, ui.batchesError), load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError)]);
  }

  bind();
  root.ClippingUI = { configure, start, stop, refresh, handleEvent, hasSources: () => state.sources.length > 0 };
})(typeof window !== "undefined" ? window : globalThis);
