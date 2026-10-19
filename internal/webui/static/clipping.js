(function (root) {
  "use strict";

  const CONTENT_TYPES = ["general", "podcast", "comedy", "gaming", "movie"];
  const GENERIC_IMPORT_ERROR_COPY = "This YouTube video could not be imported. Check that the public video is available and try again.";
  const IMPORT_ERROR_COPY = {
    apify_config_missing: "Video importing is not configured. Ask the operator to set up the video provider.",
    apify_auth_failed: "The video provider could not authenticate. Ask the operator to check its credentials.",
    apify_actor_failed: "The video provider could not retrieve this video. Confirm it is publicly available and try again later.",
    apify_timeout: "The video provider did not finish before its wait limit. Ask the operator to check the existing run before trying again.",
    apify_rate_limited: "The video provider is busy. Wait a few minutes, then try importing again.",
    apify_artifact_missing: "The video provider did not return a downloadable video for this link. Try another public video or contact the operator.",
    apify_download_failed: "The saved video could not be downloaded. Ask the operator to check the existing run before starting another import.",
    apify_download_expired: "The video download link expired before the file was saved. Ask the operator to check the existing run before retrying.",
    apify_start_uncertain: "The provider may have started this import. Ask the operator to check the run before importing again.",
    apify_actor_mismatch: "The video provider configuration does not match this importer. Ask the operator to check it.",
    apify_invalid_response: "The video provider returned an unsupported response. Ask the operator to check the importer configuration.",
    source_import_canceled: "This import was canceled. Start it again when you are ready.",
    source_import_timed_out: "This import exceeded its time limit. Ask the operator to check the existing run before starting another import.",
    storage_reservation_unavailable: "Not enough free storage is available for this import. Ask the operator to free storage before trying again.",
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
  const IMPORT_STAGE_COPY = {
    import_dispatch: "Queued",
    actor_start: "Starting acquisition",
    actor_poll: "Acquiring video",
    media_download: "Downloading media",
    media_probe: "Validating media",
    media_hash: "Validating media",
    download_output_validation: "Validating media",
    source_ready: "Validating media"
  };
  const JOB_STATUS_COPY = {
    queued: "Queued",
    running: "Analysis in progress",
    completed: "Analysis complete",
    failed: "Analysis failed",
    canceled: "Canceled",
    cancelled: "Canceled",
    uncertain: "Waiting for the previous attempt to resolve"
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
    if (/^[A-Za-z0-9_-]{11}$/.test(raw)) return raw;
    let url;
    try {
      url = new URL(/^[A-Za-z][A-Za-z0-9+.-]*:/.test(raw) ? raw : "https://" + raw);
    } catch (_error) {
      return "";
    }
    const host = url.hostname.toLowerCase();
    const hosts = new Set([
      "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com",
      "youtube-nocookie.com", "www.youtube-nocookie.com", "youtu.be", "www.youtu.be"
    ]);
    if (!hosts.has(host) || !["https:", "http:"].includes(url.protocol)
      || url.username || url.password
      || (url.port && url.port !== "443" && !(url.protocol === "http:" && url.port === "80"))) return "";
    const parts = url.pathname.split("/").filter(Boolean);
    let id = "";
    if (host === "youtu.be" || host === "www.youtu.be") {
      if (parts.length === 1) id = parts[0];
    } else if (parts.length === 1 && parts[0] === "watch") {
      const values = url.searchParams.getAll("v");
      if (values.length === 1) id = values[0];
    } else if (parts.length === 2 && ["shorts", "embed", "live", "v"].includes(parts[0])) {
      id = parts[1];
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
    importButton: $("clipping-link-submit"),
    importError: $("clipping-link-error"),
    importStatus: $("clipping-workflow-status"),
    importLimits: $("clipping-import-limits"),
    sourceError: $("clipping-sources-error"),
    sourceCount: $("clipping-source-count"),
    sources: $("clipping-sources"),
    jobsError: $("clipping-jobs-error"),
    jobCount: $("clipping-job-count"),
    jobs: $("clipping-jobs"),
    workerSummary: $("clipping-worker-summary"),
    settings: $("clipping-settings-disclosure"),
    workerForm: $("clipping-worker-form"),
    workerEndpoint: $("clipping-worker-endpoint"),
    workerRevision: $("clipping-worker-pipeline-revision"),
    workerToken: $("clipping-worker-token"),
    workerMaxCompute: $("clipping-worker-max-compute"),
    workerEnabled: $("clipping-worker-enabled"),
    workerStatus: $("clipping-worker-status"),
    workerError: $("clipping-worker-error"),
    workerSaveNote: $("clipping-worker-save-note"),
    workerSave: $("clipping-worker-save"),
    workerOpen: $("clipping-open-worker-settings"),
    analysisHeading: $("clipping-analysis-heading")
  };

  const state = {
    active: false,
    lifecycle: 0,
    requestImpl: null,
    toastImpl: null,
    confirmImpl: null,
    requestSequence: { config: 0, worker: 0, sources: 0, jobs: 0 },
    config: null,
    worker: null,
    sources: [],
    sourceConsoleStatuses: new Map(),
    jobs: [],
    contentTypes: new Map(),
    pendingSourceIDs: new Set(),
    pendingDeleteSourceIDs: new Set(),
    pendingDeleteJobIDs: new Set(),
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
    pendingKeys: new Map()
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

  function sourceName(source) {
    const name = source && (source.title || source.original_name || source.name);
    return typeof name === "string" && name.trim() ? name.trim() : "YouTube video";
  }

  function importErrorCopy(source) {
    const code = String(source && source.error || "");
    if (Object.hasOwn(IMPORT_ERROR_COPY, code)) return IMPORT_ERROR_COPY[code];
    return code || String(source && source.status || "").toLowerCase() === "failed"
      ? GENERIC_IMPORT_ERROR_COPY
      : "";
  }

  function importStageLabel(source) {
    const diagnostics = Array.isArray(source && source.diagnostics) ? source.diagnostics : [];
    const current = diagnostics.slice().reverse().find((item) => item && Object.hasOwn(IMPORT_STAGE_COPY, String(item.stage || "").toLowerCase()));
    const stage = String(current && current.stage || "").toLowerCase();
    return IMPORT_STAGE_COPY[stage] || "Importing video";
  }

  function sourceStatus(source) {
    const status = String(source && source.status || "").toLowerCase();
    if (status === "importing") return "Preparing video";
    if (status === "ready") return "Ready to analyze";
    if (status === "failed") return "Import failed";
    return status ? status.charAt(0).toUpperCase() + status.slice(1) : "Status unavailable";
  }

  function jobStatus(job) {
    const status = String(job && job.status || "").toLowerCase();
    if (status === "queued" && (!state.worker || !state.worker.configured || !state.worker.enabled)) {
      return "Waiting for Modal worker setup";
    }
    return JOB_STATUS_COPY[status] || "Status unavailable";
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

  function confirmAction(message) {
    return typeof state.confirmImpl === "function" && state.confirmImpl(message) === true;
  }

  function invalidateListLoads() {
    state.requestSequence.sources++;
    state.requestSequence.jobs++;
  }

  function clearPendingKeysForSource(sourceID) {
    const id = safeID(sourceID);
    if (!id) return;
    const prefix = id + ":";
    for (const key of state.pendingKeys.keys()) {
      if (key.startsWith(prefix)) state.pendingKeys.delete(key);
    }
  }

  function activeJobStatus(status) {
    return ["queued", "running", "uncertain"].includes(String(status || "").toLowerCase());
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
    clearPendingKeysForSource(sourceID || existing && existing.source_id);
    state.deletedJobIDs.add(id);
    state.pendingDeleteJobIDs.delete(id);
    state.jobActionErrors.delete(id);
    state.jobTracking.delete(id);
    state.expandedResults.delete(id);
    state.resultDetails.delete(id);
    state.resultErrors.delete(id);
    state.loadingResults.delete(id);
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
    state.contentTypes.delete(id);
    state.sourceActionErrors.delete(id);
    for (const job of state.jobs.filter((item) => safeID(item && item.source_id) === id)) {
      markJobDeleted(job.id, id);
    }
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
    const bar = element("div", "clipping-progress-bar progress-bar");
    if (percent === null) bar.classList.add("indeterminate");
    else bar.style.width = percent + "%";
    bar.setAttribute("role", "progressbar");
    bar.setAttribute("aria-label", label);
    bar.setAttribute("aria-valuemin", "0");
    bar.setAttribute("aria-valuemax", "100");
    bar.setAttribute("aria-valuetext", label);
    if (percent !== null) bar.setAttribute("aria-valuenow", String(percent));
    else bar.setAttribute("aria-busy", "true");
    track.append(bar);
    wrapper.append(track, element("span", "clipping-progress-copy", percent === null ? label : label + " · " + percent + "%"));
    parent.append(wrapper);
  }

  function activeJobForSource(sourceID) {
    return state.jobs.find((job) => String(job && job.source_id) === sourceID
      && ["queued", "running", "uncertain"].includes(String(job.status || "").toLowerCase()));
  }

  function makeContentTypeSelect(sourceID, name) {
    const label = element("label", "clipping-content-type");
    label.append(element("span", "", "Analysis type"));
    const select = element("select", "clipping-content-type-select");
    select.setAttribute("aria-label", "Analysis type for " + name);
    select.dataset.clippingFocusKey = "content-type";
    for (const value of CONTENT_TYPES) {
      const option = element("option", "", CONTENT_TYPE_LABELS[value]);
      option.value = value;
      select.append(option);
    }
    if (!state.contentTypes.has(sourceID)) state.contentTypes.set(sourceID, "general");
    select.value = contentType(state.contentTypes.get(sourceID));
    select.addEventListener("change", () => state.contentTypes.set(sourceID, contentType(select.value)));
    label.append(select);
    return label;
  }

  function renderSources() {
    const focus = captureFocus(ui.sources, "sourceId");
    const fragment = document.createDocumentFragment();
    ui.sourceCount.textContent = String(state.sources.length);
    if (!state.sources.length) {
      fragment.append(element("p", "empty", "Imported videos will appear here. Paste a public YouTube link above to begin."));
    }
    for (const source of state.sources) {
      const id = safeID(source && source.id);
      if (!id) continue;
      const status = String(source.status || "").toLowerCase();
      const name = sourceName(source);
      const row = element("article", "clipping-source-row");
      row.dataset.sourceId = id;
      const main = element("div", "clipping-source-main");
      const heading = element("div", "clipping-source-heading");
      heading.append(element("strong", "clipping-source-title", name));
      const badge = element("span", "badge clipping-source-status", sourceStatus(source));
      badge.dataset.state = status;
      heading.append(badge);
      main.append(heading);
      const duration = formatDuration(source.duration_ms);
      if (duration) main.append(element("p", "clipping-source-meta", "Duration " + duration));
      if (status === "importing") renderProgress(main, importStageLabel(source), null);
      const error = state.sourceActionErrors.get(id) || importErrorCopy(source);
      if (error) main.append(element("p", "clipping-row-error", error));
      const actions = element("div", "clipping-source-actions");
      const deleting = state.pendingDeleteSourceIDs.has(id);
      if (status === "ready") {
        const activeJob = activeJobForSource(id);
        actions.append(makeContentTypeSelect(id, name));
        const analyze = button(activeJob ? jobStatus(activeJob) : "Analyze", "button primary clipping-action",
          () => { if (!activeJob) void createJob(id); });
        analyze.dataset.clippingFocusKey = "analyze";
        analyze.disabled = Boolean(activeJob || state.pendingSourceIDs.has(id) || deleting);
        if (state.pendingSourceIDs.has(id)) analyze.textContent = "Queueing analysis…";
        actions.append(analyze);
      }
      const remove = button(deleting ? "Deleting…" : "Delete video", "button secondary clipping-action clipping-delete-action",
        () => { void deleteSource(id); });
      remove.dataset.clippingFocusKey = "delete-source";
      remove.setAttribute("aria-label", (deleting ? "Deleting imported video " : "Delete imported video ") + name);
      remove.setAttribute("aria-disabled", String(deleting || state.pendingSourceIDs.has(id)));
      if (deleting) remove.setAttribute("aria-busy", "true");
      remove.disabled = state.pendingSourceIDs.has(id);
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

  function renderArtifact(parent, artifact) {
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
    if (error) parent.append(element("p", "clipping-row-error", error));
    if (state.loadingResults.has(jobID)) {
      parent.append(element("p", "clipping-artifact-note", "Loading saved analysis…"));
      return;
    }
    const artifacts = result && Array.isArray(result.artifacts) ? result.artifacts : [];
    if (!artifacts.length && !error) {
      parent.append(element("p", "clipping-artifact-note", "No saved analysis results are available for this job."));
    }
    for (const artifact of artifacts) {
      if (artifact && typeof artifact === "object") renderArtifact(parent, artifact);
    }
  }

  function renderJob(job) {
    const id = safeID(job && job.id);
    if (!id) return null;
    const row = element("article", "clipping-job-row");
    row.dataset.jobId = id;
    const header = element("div", "clipping-job-heading");
    const source = state.sources.find((item) => String(item.id) === String(job.source_id));
    header.append(element("strong", "clipping-job-title", source ? sourceName(source) : "YouTube analysis"));
    header.append(element("span", "badge clipping-job-status", jobStatus(job)));
    row.append(header);
    const status = String(job.status || "").toLowerCase();
    if (["queued", "running"].includes(status)) renderProgress(row, "Analysis progress", statusProgress(job));
    const error = state.jobActionErrors.get(id) || String(job.error || "").trim();
    if (error) row.append(element("p", "clipping-row-error", error));
    const footer = element("div", "clipping-job-actions");
    footer.append(element("span", "clipping-job-meta", "Analysis type: " + (CONTENT_TYPE_LABELS[contentType(job.content_type)] || "General")));
    if (status === "completed" || state.resultDetails.has(id) || state.resultErrors.has(id)) {
      const expanded = state.expandedResults.has(id);
      const toggle = button(
        state.loadingResults.has(id) ? "Loading results…" : expanded ? "Hide saved analysis" : "View saved analysis",
        "button secondary clipping-action",
        () => { void toggleResults(id); }
      );
      toggle.dataset.clippingFocusKey = "results";
      if (state.loadingResults.has(id)) toggle.setAttribute("aria-busy", "true");
      footer.append(toggle);
    }
    const deleting = state.pendingDeleteJobIDs.has(id) || state.pendingDeleteSourceIDs.has(safeID(job.source_id));
    const remove = button(deleting ? "Deleting…" : "Delete analysis", "button secondary clipping-action clipping-delete-action",
      () => { void deleteJob(id); });
    remove.dataset.clippingFocusKey = "delete-job";
    const sourceNameForJob = source ? sourceName(source) : "this video";
    remove.setAttribute("aria-label", (deleting ? "Deleting analysis for " : "Delete analysis for ") + sourceNameForJob);
    remove.setAttribute("aria-disabled", String(deleting));
    if (deleting) remove.setAttribute("aria-busy", "true");
    remove.disabled = false;
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
            console.info("[clipper/youtube-import] source status", { source_id: id, status, reason_code: Object.hasOwn(IMPORT_ERROR_COPY, reason) ? reason : "" });
            state.sourceConsoleStatuses.set(id, status);
          }
          if (status === "deleted") markSourceDeleted(source.id);
        }
        state.sources = records.filter((source) => !state.deletedSourceIDs.has(safeID(source && source.id))
          && String(source && source.status || "").toLowerCase() !== "deleted");
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
      }
      render();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence[kind] || error.name === "AbortError") return;
      if (kind === "sources") console.warn("[clipper/youtube-import] source refresh failed", { error_type: error && error.name || "unknown" });
      errorNode.textContent = error.message || "Could not load clipping information.";
      if (notifyError) toastError(error, errorNode.textContent);
    }
  }

  async function loadConfig(notifyError) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence.config;
    try {
      state.config = await api("/api/clipping/config");
      if (!current(lifecycle) || sequence !== state.requestSequence.config) return;
      const duration = formatDuration(state.config && state.config.max_duration_ms);
      const size = formatBytes(state.config && state.config.max_source_bytes);
      ui.importLimits.textContent = "One public video at a time; up to " + (duration || "four hours") + " and " + size + ".";
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence.config || error.name === "AbortError") return;
      state.config = null;
      if (notifyError) toastError(error, "Clipping settings could not be loaded.");
    }
  }

  function renderWorkerSummary() {
    if (!ui.workerSummary) return;
    if (!state.worker) {
      ui.workerSummary.textContent = "Worker status is unavailable. Open Modal worker settings to check the saved configuration.";
    } else if (!state.worker.configured) {
      ui.workerSummary.textContent = "The Modal worker is not set up yet. Videos can be imported; analysis jobs will wait for setup.";
    } else if (!state.worker.enabled) {
      ui.workerSummary.textContent = "The Modal worker is configured but disabled. Analysis jobs will wait until it is enabled.";
    } else {
      ui.workerSummary.textContent = "The Modal worker is enabled. Starting analysis may incur Modal charges.";
    }
  }

  function renderWorkerStatus() {
    const worker = state.worker;
    if (!worker) {
      ui.workerStatus.textContent = "Worker settings could not be loaded.";
      return;
    }
    if (!worker.configured) {
      ui.workerStatus.textContent = "Not configured · disabled by default";
      return;
    }
    const details = [worker.enabled ? "Enabled" : "Disabled", "configured, not live checked"];
    if (typeof worker.endpoint_host === "string" && worker.endpoint_host) details.push(worker.endpoint_host);
    if (typeof worker.pipeline_revision === "string" && worker.pipeline_revision) details.push("revision " + worker.pipeline_revision);
    ui.workerStatus.textContent = details.join(" · ");
  }

  async function loadWorker(notifyError) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence.worker;
    ui.workerError.textContent = "";
    try {
      const response = await api("/api/clipping/config/worker");
      if (!current(lifecycle) || sequence !== state.requestSequence.worker) return;
      state.worker = response && response.worker ? response.worker : response;
      ui.workerEnabled.checked = Boolean(state.worker && state.worker.enabled);
      ui.workerRevision.value = String(state.worker && state.worker.pipeline_revision || "");
      const seconds = Number(state.worker && state.worker.max_compute_seconds);
      if (Number.isInteger(seconds) && seconds > 0 && seconds <= 43200) {
        ui.workerMaxCompute.value = String(Math.ceil(seconds / 60));
      }
      ui.workerToken.value = "";
      renderWorkerStatus();
      renderWorkerSummary();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence.worker || error.name === "AbortError") return;
      state.worker = null;
      renderWorkerStatus();
      renderWorkerSummary();
      ui.workerError.textContent = error.message || "Worker settings could not be loaded.";
      if (notifyError) toastError(error, ui.workerError.textContent);
    }
  }

  async function saveWorker(event) {
    event.preventDefault();
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    ui.workerError.textContent = "";
    const endpoint = String(ui.workerEndpoint.value || "").trim();
    const revision = String(ui.workerRevision.value || "").trim();
    const token = String(ui.workerToken.value || "");
    let parsed;
    try { parsed = new URL(endpoint); } catch (_error) { parsed = null; }
    if (!parsed || parsed.protocol !== "https:" || !parsed.hostname || parsed.username || parsed.password
      || parsed.search || parsed.hash || !parsed.pathname.endsWith("/dispatch")) {
      ui.workerError.textContent = "Enter the complete HTTPS Modal dispatch URL ending in /dispatch.";
      ui.settings.open = true;
      ui.workerEndpoint.focus();
      return;
    }
    if (!/^[A-Za-z0-9._-]{1,64}$/.test(revision)) {
      ui.workerError.textContent = "Enter the pipeline revision that matches the deployed Modal worker.";
      ui.settings.open = true;
      ui.workerRevision.focus();
      return;
    }
    if (token && !/^[0-9a-f]{64}$/.test(token)) {
      ui.workerError.textContent = "The worker bearer token must contain 64 lowercase hexadecimal characters.";
      ui.settings.open = true;
      ui.workerToken.focus();
      return;
    }
    const maxComputeMinutes = Number(ui.workerMaxCompute.value);
    if (!Number.isInteger(maxComputeMinutes) || maxComputeMinutes < 1 || maxComputeMinutes > 720) {
      ui.workerError.textContent = "Choose a maximum analysis time from 1 to 720 minutes.";
      ui.settings.open = true;
      ui.workerMaxCompute.focus();
      return;
    }
    if (!state.worker?.configured && !token) {
      ui.workerError.textContent = "Enter the worker bearer token for initial setup.";
      ui.settings.open = true;
      ui.workerToken.focus();
      return;
    }
    ui.workerSave.disabled = true;
    try {
      await api("/api/clipping/config/worker", {
        method: "PUT",
        body: JSON.stringify({
          enabled: ui.workerEnabled.checked,
          endpoint,
          bearer_token: token,
          pipeline_revision: revision,
          max_compute_seconds: maxComputeMinutes * 60
        })
      });
      if (!current(lifecycle)) return;
      ui.workerToken.value = "";
      ui.workerSaveNote.textContent = "Settings saved. The endpoint was not live checked.";
      await loadWorker();
    } catch (error) {
      if (current(lifecycle)) ui.workerError.textContent = toastError(error, "Worker settings could not be saved.");
    } finally {
      if (current(lifecycle)) ui.workerSave.disabled = false;
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
    state.loadingResults.add(id);
    state.resultErrors.delete(id);
    renderJobs();
    try {
      const response = await api("/api/clipping/jobs/" + encodeURIComponent(id) + "/results");
      if (!current(lifecycle) || state.deletedJobIDs.has(id)) return;
      const result = response && response.results ? response.results : response;
      if (result && result.job_id && safeID(result.job_id) !== id) throw new Error("Saved results did not match this analysis job.");
      state.resultDetails.set(id, result || {});
    } catch (error) {
      if (current(lifecycle) && !state.deletedJobIDs.has(id) && error.name !== "AbortError") {
        const message = error.message || "Saved analysis could not be loaded.";
        state.resultErrors.set(id, message);
        toastError(error, message);
      }
    } finally {
      state.loadingResults.delete(id);
      if (current(lifecycle)) renderJobs();
    }
  }

  async function createJob(sourceID) {
    const id = safeID(sourceID);
    const source = state.sources.find((item) => safeID(item && item.id) === id);
    if (!id || !source || String(source.status).toLowerCase() !== "ready" || state.pendingSourceIDs.has(id)) return;
    const lifecycle = state.lifecycle;
    const type = contentType(state.contentTypes.get(id));
    const fingerprint = id + ":" + type;
    let idempotencyKey = state.pendingKeys.get(fingerprint);
    if (!idempotencyKey) {
      idempotencyKey = root.crypto && typeof root.crypto.randomUUID === "function"
        ? root.crypto.randomUUID()
        : "clipping-" + Date.now() + "-" + Math.random().toString(16).slice(2);
      state.pendingKeys.set(fingerprint, idempotencyKey);
    }
    state.pendingJobBaselines.set(id, new Set(state.jobs.map((job) => safeID(job && job.id)).filter(Boolean)));
    state.pendingSourceIDs.add(id);
    state.sourceActionErrors.delete(id);
    renderSources();
    try {
      const response = await api("/api/clipping/jobs", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ source_id: id, content_type: type })
      });
      if (!current(lifecycle)) return;
      if (response && response.job && typeof response.job === "object") {
        const createdID = safeID(response.job.id);
        if (createdID && !state.jobTracking.has(createdID)) observeJobStatus(Object.assign({}, response.job, { status: "queued" }));
        observeJobStatus(response.job);
      }
      state.pendingKeys.delete(fingerprint);
      ui.importStatus.textContent = "Analysis was queued for " + sourceName(source) + ".";
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]);
      renderSources();
      ui.analysisHeading.focus?.({ preventScroll: true });
    } catch (error) {
      if (current(lifecycle) && !state.deletedSourceIDs.has(id)) {
        state.sourceActionErrors.set(id, toastError(error, "Analysis could not be queued."));
        renderSources();
        ui.importStatus.textContent = "";
      }
    } finally {
      state.pendingSourceIDs.delete(id);
      state.pendingJobBaselines.delete(id);
      if (current(lifecycle)) renderSources();
    }
  }

  async function submitImport(event) {
    event.preventDefault();
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    ui.importError.textContent = "";
    const value = String(ui.link.value || "").trim();
    if (!youtubeVideoID(value)) {
      ui.importError.textContent = "Paste a valid link to one public YouTube video.";
      ui.link.focus();
      return;
    }
    if (!ui.permission.checked) {
      ui.importError.textContent = "Confirm that you have permission to reuse and process this video.";
      ui.permission.focus();
      return;
    }
    ui.importButton.disabled = true;
    ui.importButton.textContent = "Importing…";
    ui.link.disabled = true;
    ui.permission.disabled = true;
    ui.importForm.setAttribute("aria-busy", "true");
    ui.importStatus.textContent = "The public YouTube link was accepted. Preparing the video…";
    let restoreLinkFocus = false;
    console.info("[clipper/youtube-import] submitting import request");
    try {
      const response = await api("/api/clipping/sources/import", {
        method: "POST",
        body: JSON.stringify({ url: value, rights_attested: true })
      });
      if (!current(lifecycle)) return;
      const source = response && response.source;
      const id = safeID(source && source.id);
      console.info("[clipper/youtube-import] import accepted", { source_id: id, status: String(source && source.status || "") });
      if (id && !state.sources.some((item) => safeID(item && item.id) === id)) state.sources.unshift(source);
      ui.link.value = "";
      ui.permission.checked = false;
      ui.importForm.reset?.();
      ui.importStatus.textContent = "Import accepted. The video will appear below as it becomes ready.";
      await load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources");
      renderSources();
    } catch (error) {
      if (!current(lifecycle)) return;
      console.warn("[clipper/youtube-import] import request failed", { error_type: error && error.name || "unknown", http_status: Number.isInteger(error && error.status) ? error.status : null });
      ui.importError.textContent = toastError(error, "The YouTube video could not be imported.");
      ui.importStatus.textContent = "";
      restoreLinkFocus = true;
    } finally {
      if (current(lifecycle)) {
        ui.importButton.textContent = "Import YouTube video";
        ui.link.disabled = false;
        ui.permission.disabled = false;
        ui.importForm.removeAttribute("aria-busy");
        updateImportState();
        if (restoreLinkFocus) ui.link.focus({ preventScroll: true });
      }
    }
  }

  async function deleteSource(sourceID) {
    const id = safeID(sourceID);
    const source = state.sources.find((item) => safeID(item && item.id) === id);
    if (!id || !source || state.deletedSourceIDs.has(id) || state.pendingDeleteSourceIDs.has(id)) return;
    const name = sourceName(source);
    if (!confirmAction("Delete “" + name + "” and its analyses? This stops any analysis in progress and removes the imported video.")) return;
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
      showToast(response && response.cleanup_pending === true
        ? "Video and analyses removed. File cleanup is still finishing."
        : "Video and its analyses deleted.", false);
      await Promise.allSettled([
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources"),
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs")
      ]);
      focusFirstAction(ui.sources, "delete-source");
      if (!state.sources.length) ui.link.focus?.({ preventScroll: true });
    } catch (error) {
      if (current(lifecycle) && !state.deletedSourceIDs.has(id)) {
        state.sourceActionErrors.set(id, toastError(error, "The imported video could not be deleted."));
        renderSources();
      }
    } finally {
      state.pendingDeleteSourceIDs.delete(id);
      if (current(lifecycle)) {
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
    const confirmation = wasActive
      ? "Delete the analysis for “" + name + "”? This stops the analysis in progress and keeps the imported video."
      : "Delete the saved analysis for “" + name + "”? Its results will be removed and the imported video will remain.";
    if (!confirmAction(confirmation)) return;
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
      showToast(wasActive ? "Analysis stopped and deleted." : "Analysis and saved results deleted.", false);
      await Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]);
      focusFirstAction(ui.jobs, "delete-job");
      if (!state.jobs.length) ui.analysisHeading.focus?.({ preventScroll: true });
    } catch (error) {
      if (current(lifecycle) && !state.deletedJobIDs.has(id)) {
        state.jobActionErrors.set(id, toastError(error, "The analysis could not be deleted."));
        renderJobs();
      }
    } finally {
      state.pendingDeleteJobIDs.delete(id);
      if (current(lifecycle)) {
        renderJobs();
        renderSources();
      }
    }
  }

  function updateImportState() {
    const hasLink = String(ui.link.value || "").trim().length > 0;
    ui.importButton.disabled = !hasLink || !ui.permission.checked;
    if (hasLink && !youtubeVideoID(ui.link.value)) {
      ui.importError.textContent = "Use one public YouTube video link.";
    }
  }

  function bind() {
    ui.refresh.addEventListener("click", () => { void refresh({ notify: true }); });
    ui.importForm.addEventListener("submit", (event) => { void submitImport(event); });
    ui.link.addEventListener("input", () => {
      ui.importError.textContent = "";
      updateImportState();
    });
    ui.permission.addEventListener("change", updateImportState);
    ui.workerForm.addEventListener("submit", (event) => { void saveWorker(event); });
    ui.workerOpen.addEventListener("click", () => {
      ui.settings.open = true;
      ui.workerEndpoint.focus();
    });
    updateImportState();
  }

  function configure(options) {
    state.requestImpl = options && typeof options.request === "function" ? options.request : null;
    state.toastImpl = options && typeof options.toast === "function" ? options.toast : null;
    state.confirmImpl = options && typeof options.confirm === "function" ? options.confirm : null;
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
    const notifyError = Boolean(options && typeof options === "object" && options.notify);
    await Promise.allSettled([
      loadConfig(notifyError),
      loadWorker(notifyError),
      load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources", notifyError),
      load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs", notifyError)
    ]);
    renderSources();
    renderJobs();
  }

  function stop(clear) {
    state.active = false;
    state.lifecycle++;
    if (clear !== false) {
      state.config = null;
      state.worker = null;
      state.sources = [];
      state.sourceConsoleStatuses.clear();
      state.jobs = [];
      state.contentTypes.clear();
      state.pendingSourceIDs.clear();
      state.pendingDeleteSourceIDs.clear();
      state.pendingDeleteJobIDs.clear();
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
      state.pendingKeys.clear();
      ui.importError.textContent = "";
      ui.importStatus.textContent = "";
      ui.sourceError.textContent = "";
      ui.jobsError.textContent = "";
      ui.workerError.textContent = "";
      ui.workerStatus.textContent = "Worker configuration has not been loaded.";
      ui.workerSaveNote.textContent = "Saving settings does not test the worker connection.";
      ui.workerEndpoint.value = "";
      ui.workerRevision.value = "";
      ui.workerToken.value = "";
      ui.workerMaxCompute.value = "720";
      ui.workerEnabled.checked = false;
      ui.sources.replaceChildren(element("p", "empty", "Imported videos will appear here."));
      ui.jobs.replaceChildren(element("p", "empty", "Analysis jobs will appear here."));
      ui.sourceCount.textContent = "0";
      ui.jobCount.textContent = "0";
      renderWorkerSummary();
      ui.importButton.textContent = "Import YouTube video";
      ui.link.value = "";
      ui.permission.checked = false;
      ui.link.disabled = false;
      ui.permission.disabled = false;
      ui.importForm.removeAttribute("aria-busy");
      ui.importForm.reset?.();
      updateImportState();
    }
  }

  function handleEvent(kind, event) {
    if (!state.active || !event || typeof event.data !== "string") return;
    let payload;
    try { payload = JSON.parse(event.data); } catch (_error) { return; }
    if (kind === "clipping_source") {
      const source = payload && payload.source && typeof payload.source === "object" ? payload.source : payload;
      const id = safeID(source && source.id) || safeID(payload && payload.id);
      if (id && String(source && source.status || "").toLowerCase() === "deleted") {
        markSourceDeleted(id);
        renderSources();
        renderJobs();
      }
      if (id && state.deletedSourceIDs.has(id)) return;
      void load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources");
      return;
    }
    if (kind === "clipping_job") {
      const job = payload && payload.job && typeof payload.job === "object" ? payload.job : payload;
      const id = safeID(job && job.id) || safeID(payload && payload.id);
      if (id && job && job.analysis_deleted === true) {
        markJobDeleted(id, job && job.source_id);
        renderJobs();
        renderSources();
      } else if (id && (state.deletedJobIDs.has(id) || state.deletedSourceIDs.has(safeID(job && job.source_id)))) {
        return;
      } else if (id) {
        observeJobStatus(job);
      }
      if (id) {
        state.resultDetails.delete(id);
        state.resultErrors.delete(id);
      }
      void Promise.allSettled([
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError, "jobs"),
        load("sources", "/api/clipping/sources", renderSources, ui.sourceError, "sources")
      ]).then(() => {
        if (id && !state.deletedJobIDs.has(id) && state.expandedResults.has(id)) void loadResults(id);
      });
    }
  }

  bind();
  root.ClippingUI = { configure, start, stop, refresh, handleEvent, hasSources: () => state.sources.length > 0 };
})(typeof window !== "undefined" ? window : globalThis);
