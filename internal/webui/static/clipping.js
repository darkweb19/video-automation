(function (root) {
  "use strict";

  const MAX_UPLOAD_CHUNK_BYTES = 16 * 1024 * 1024;
  const MAX_SAFE_MICRO_USD = Number.MAX_SAFE_INTEGER;
  const MAX_JOB_BUDGET_MICRO_USD = 4_000_000;
  const MAX_BATCH_BUDGET_MICRO_USD = 400_000_000;
  const CONTENT_TYPES = ["general", "podcast", "comedy", "gaming", "movie"];
  const MEDIA_EXTENSIONS = new Set([
    "3gp", "aac", "aif", "aiff", "avi", "flac", "m2ts", "m2v", "m4a", "m4v", "mkv", "mov", "mp3",
    "mp4", "mpeg", "mpg", "mts", "ogg", "ogv", "opus", "ts", "wav", "webm", "wma", "wmv"
  ]);
  const WORKER_CAPABILITY_LABELS = {
    full_timeline_asr: "Full-timeline ASR",
    original_script: "Original-script transcript",
    audio_events: "Audio events",
    scene_motion_events: "Scene and motion events",
    candidate_selection: "Candidate selection"
  };

  function parseUSDToMicroUSD(value) {
    const text = String(value ?? "").trim();
    const match = /^(\d{1,9})(?:\.(\d{1,6}))?$/.exec(text);
    if (!match) return null;
    const whole = Number(match[1]);
    const fraction = (match[2] || "").padEnd(6, "0");
    const amount = whole * 1_000_000 + Number(fraction || 0);
    return Number.isSafeInteger(amount) && amount > 0 && amount <= MAX_SAFE_MICRO_USD ? amount : null;
  }

  function parseActualCostUSDToMicroUSD(value) {
    const text = String(value ?? "").trim();
    const match = /^(\d{1,9})(?:\.(\d{1,6}))?$/.exec(text);
    if (!match) return null;
    const whole = Number(match[1]);
    const fraction = (match[2] || "").padEnd(6, "0");
    const amount = whole * 1_000_000 + Number(fraction || 0);
    return Number.isSafeInteger(amount) && amount >= 0 && amount <= MAX_SAFE_MICRO_USD ? amount : null;
  }

  function utf8ByteLength(value) {
    return new TextEncoder().encode(String(value)).length;
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
    clippingView: $("clipping-view"),
    settingsDisclosure: $("clipping-settings-disclosure"),
    refresh: $("clipping-refresh"),
    workerDisclosure: $("clipping-worker-disclosure"),
    workerNote: $("clipping-worker-note"),
    workerForm: $("clipping-worker-form"),
    workerEndpoint: $("clipping-worker-endpoint"),
    workerToken: $("clipping-worker-token"),
    workerPipelineRevision: $("clipping-worker-pipeline-revision"),
    workerRate: $("clipping-worker-rate"),
    workerEnabled: $("clipping-worker-enabled"),
    workerSave: $("clipping-worker-save"),
    workerStatus: $("clipping-worker-status"),
    workerMetadata: $("clipping-worker-metadata"),
    callbackOrigin: $("clipping-callback-origin"),
    workerError: $("clipping-worker-error"),
    workerSaveNote: $("clipping-worker-save-note"),
    uploadForm: $("clipping-upload-form"),
    uploadDropZone: $("clipping-drop-zone"),
    uploadFile: $("clipping-file"),
    uploadFileName: $("clipping-file-name"),
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
    jobMinSeconds: $("clipping-job-min-seconds"),
    jobMaxSeconds: $("clipping-job-max-seconds"),
    jobCandidateLimit: $("clipping-job-candidate-limit"),
    jobSettingsDisclosure: $("clipping-job-settings-disclosure"),
    batchDisclosure: $("clipping-batch-disclosure"),
    sourcesError: $("clipping-sources-error"),
    sourceSummary: $("clipping-source-summary"),
    workGrid: $("clipping-work-grid"),
    sourcesPanel: $("clipping-sources-panel"),
    jobsPanel: $("clipping-jobs-panel"),
    sources: $("clipping-sources"),
    batchJobBudget: $("clipping-batch-job-budget"),
    batchBudget: $("clipping-batch-budget"),
    batchContentType: $("clipping-batch-content-type"),
    batchMinSeconds: $("clipping-batch-min-seconds"),
    batchMaxSeconds: $("clipping-batch-max-seconds"),
    batchCandidateLimit: $("clipping-batch-candidate-limit"),
    batchBudgetNote: $("clipping-batch-budget-note"),
    createBatch: $("clipping-create-batch"),
    batchError: $("clipping-batch-error"),
    jobsError: $("clipping-jobs-error"),
    jobs: $("clipping-jobs"),
    jobsSection: $("clipping-jobs-section"),
    jobCount: $("clipping-job-count"),
    batchesError: $("clipping-batches-error"),
    batches: $("clipping-batches"),
    batchesSection: $("clipping-batches-section"),
    batchCount: $("clipping-batch-count"),
    retentionForm: $("clipping-retention-form"),
    retentionDisclosure: $("clipping-retention-disclosure"),
    retentionDays: $("clipping-retention-days"),
    retentionSave: $("clipping-retention-save"),
    retentionError: $("clipping-retention-error"),
    retentionNote: $("clipping-retention-note")
  };

  const state = {
    active: false,
    lifecycle: 0,
    requestSequence: { config: 0, workerConfig: 0, sources: 0, jobs: 0, batches: 0 },
    requestImpl: null,
    controllers: new Set(),
    sources: [],
    jobs: [],
    batches: [],
    workerConfig: null,
    sourceContentTypes: new Map(),
    expandedJobIDs: new Set(),
    jobDetails: new Map(),
    jobDetailErrors: new Map(),
    loadingJobDetails: new Set(),
    selectedSourceIDs: new Set(),
    pendingJobKeys: new Map(),
    pendingBatchKeys: new Map(),
    linkConsentValue: "",
    config: null,
    upload: null,
    selectedUploadFile: null,
    dropDepth: 0,
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

  function focusInDisclosures(disclosures, target) {
    for (const disclosure of disclosures || []) if (disclosure) disclosure.open = true;
    target?.focus?.();
  }

  function isMediaFile(file) {
    if (!file || typeof file !== "object") return false;
    const type = String(file.type || "").toLowerCase();
    if (type.startsWith("video/") || type.startsWith("audio/")) return true;
    const name = String(file.name || "").toLowerCase();
    const extension = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
    return MEDIA_EXTENSIONS.has(extension);
  }

  function isFileTransfer(event) {
    const types = event?.dataTransfer?.types;
    return Boolean(event?.dataTransfer?.files?.length)
      || Boolean(types && Array.from(types).includes("Files"));
  }

  function setDropActive(active) {
    if (!ui.uploadDropZone?.classList) return;
    ui.uploadDropZone.classList.toggle("is-dragging", active);
  }

  function syncResumeActions() {
    const disabled = Boolean(state.upload);
    for (const action of ui.sources.querySelectorAll("button")) {
      if (action.textContent === "Resume upload") action.disabled = disabled;
    }
  }

  function selectedUploadFile() {
    const files = Array.from(ui.uploadFile.files || []);
    if (files.length > 1) return null;
    return state.selectedUploadFile || files[0] || null;
  }

  function updateUploadReadiness() {
    const file = selectedUploadFile();
    ui.uploadFileName.textContent = file ? `Selected file: ${String(file.name || "video")}` : "No file selected.";
    ui.uploadSubmit.disabled = Boolean(state.upload) || !file || !ui.uploadPermission.checked;
  }

  function clearSelectedUpload() {
    state.selectedUploadFile = null;
    ui.uploadFile.value = "";
    ui.uploadPermission.checked = false;
    ui.uploadError.textContent = "";
    updateUploadReadiness();
  }

  function selectUploadFile(file) {
    if (!state.active) return false;
    if (state.upload) {
      ui.uploadError.textContent = "An upload is already active. Pause it before choosing another file.";
      return false;
    }
    if (!isMediaFile(file)) {
      clearSelectedUpload();
      ui.uploadError.textContent = "Choose one audio or video file.";
      return false;
    }
    if (!Number.isSafeInteger(file.size) || file.size <= 0) {
      clearSelectedUpload();
      ui.uploadError.textContent = "Choose one non-empty audio or video file.";
      return false;
    }
    state.selectedUploadFile = file;
    ui.uploadFile.value = file === ui.uploadFile.files?.[0] ? ui.uploadFile.value : "";
    ui.uploadPermission.checked = false;
    ui.uploadError.textContent = "";
    updateUploadReadiness();
    return true;
  }

  function syncWorkVisibility() {
    const hasSources = state.sources.length > 0;
    const hasJobs = state.jobs.length > 0;
    const hasBatches = state.batches.length > 0;
    const hasSourcesMessage = Boolean(ui.sourcesError.textContent);
    const hasJobsMessage = Boolean(ui.jobsError.textContent);
    const hasBatchesMessage = Boolean(ui.batchesError.textContent);
    const hasJobsPanel = hasJobs || hasBatches || hasJobsMessage || hasBatchesMessage;
    ui.sourcesPanel.hidden = !hasSources && !hasSourcesMessage;
    ui.jobsSection.hidden = !hasJobs && !hasJobsMessage;
    ui.batchesSection.hidden = !hasBatches && !hasBatchesMessage;
    ui.jobsPanel.hidden = !hasJobsPanel;
    ui.workGrid.hidden = !(hasSources || hasSourcesMessage || hasJobsPanel);
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

  function contentType(value) {
    const normalized = String(value || "general").trim().toLowerCase();
    return CONTENT_TYPES.includes(normalized) ? normalized : "general";
  }

  function safeWorkerConfig(value) {
    const input = value && typeof value === "object" && !Array.isArray(value) ? value : {};
    const config = {
      configured: input.configured === true,
      enabled: input.enabled === true,
      rate_micro_usd_per_second: Number.isSafeInteger(input.rate_micro_usd_per_second) && input.rate_micro_usd_per_second >= 0
        ? input.rate_micro_usd_per_second : null
    };
    for (const key of ["endpoint_host", "provider", "model", "pipeline_revision", "readiness", "billing_basis"]) {
      if (typeof input[key] === "string") config[key] = input[key].slice(0, 1000);
    }
    if (input.capabilities && typeof input.capabilities === "object" && !Array.isArray(input.capabilities)) {
      config.capabilities = Object.entries(WORKER_CAPABILITY_LABELS)
        .filter(([key]) => input.capabilities[key] === true)
        .map(([, label]) => label);
    }
    return config;
  }

  function workerIsEnabled() {
    if (state.workerConfig) return state.workerConfig.configured === true && state.workerConfig.enabled === true;
    return state.config?.worker_available === true;
  }

  function rateInputValue(microUSD) {
    if (!Number.isSafeInteger(microUSD) || microUSD <= 0) return "";
    return (microUSD / 1_000_000).toFixed(6).replace(/0+$/, "").replace(/\.$/, "");
  }

  function shortValue(value) {
    if (value === null || value === undefined) return "";
    if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
    try { return JSON.stringify(value); } catch (_error) { return ""; }
  }

  function sourceTime(start, end) {
    const from = Number(start);
    const to = Number(end);
    if (!Number.isSafeInteger(from) || from < 0) return "Source time unavailable";
    const timestamp = (milliseconds) => {
      const base = formatDuration(milliseconds);
      const clock = milliseconds < 3_600_000 ? base.padStart(5, "0") : base;
      return `${clock}.${String(milliseconds % 1000).padStart(3, "0")}`;
    };
    const left = timestamp(from);
    return Number.isSafeInteger(to) && to > from ? `${left}–${timestamp(to)}` : left;
  }

  function renderMetadataMap(parent, title, value) {
    if (!value || typeof value !== "object" || Array.isArray(value)) return;
    const entries = Object.entries(value).filter(([key, item]) => key && item !== null && item !== undefined).slice(0, 40);
    if (!entries.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", title));
    const list = element("dl", "clipping-artifact-metadata");
    for (const [key, item] of entries) {
      list.append(element("dt", "", key), element("dd", "", shortValue(item).slice(0, 1000)));
    }
    section.append(list);
    parent.append(section);
  }

  function renderEvidenceList(parent, evidence) {
    if (!Array.isArray(evidence) || evidence.length === 0) return;
    const list = element("ul", "clipping-artifact-list");
    for (const item of evidence.slice(0, 100)) {
      if (typeof item === "string") {
        list.append(element("li", "", item));
        continue;
      }
      if (!item || typeof item !== "object") continue;
      const details = [item.kind, sourceTime(item.start_ms, item.end_ms), item.text, item.detail]
        .filter((part) => part !== null && part !== undefined && String(part).trim() !== "");
      if (details.length) list.append(element("li", "", details.join(" · ")));
    }
    if (list.children.length) parent.append(list);
  }

  function renderTranscript(parent, payload) {
    const transcript = payload && typeof payload.transcript === "object" && payload.transcript ? payload.transcript : payload;
    const segments = Array.isArray(transcript && transcript.segments) ? transcript.segments : [];
    const transcriptHasStatus = transcript && (typeof transcript.available === "boolean" || typeof transcript.status === "string");
    const audioStatus = typeof payload?.audio_analysis_status === "string" ? payload.audio_analysis_status : "";
    if (!segments.length && !transcriptHasStatus && !audioStatus) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", segments.length ? "Original-script transcript" : "Transcript availability"));
    if (transcriptHasStatus || audioStatus) {
      const noAudioStream = transcript.available === false
        && transcript.status === "unavailable_no_audio_stream"
        && audioStatus === "skipped_no_audio_stream";
      if (noAudioStream) {
        section.append(element("p", "clipping-artifact-note", "No audio stream; audio analysis and transcription were skipped."));
      } else {
        const statuses = [];
        if (typeof transcript.available === "boolean") statuses.push(`Transcript availability: ${transcript.available ? "available" : "unavailable"}`);
        if (typeof transcript.status === "string" && transcript.status) statuses.push(`Transcript status: ${transcript.status}`);
        if (audioStatus) statuses.push(`Audio analysis status: ${audioStatus}`);
        if (statuses.length) section.append(element("p", "clipping-artifact-note", statuses.join(" · ")));
      }
    }
    if (!segments.length) {
      parent.append(section);
      return;
    }
    const note = transcript.original_script === true || transcript.original_script_preserved === true || payload.original_script_preserved === true
      ? "Original script preserved; source timestamps shown."
      : "Transcript text is shown as stored with source timestamps.";
    section.append(element("p", "clipping-artifact-note", note));
    const list = element("ol", "clipping-transcript-list");
    for (const segment of segments.slice(0, 5000)) {
      if (!segment || typeof segment !== "object") continue;
      const line = element("li", "clipping-transcript-line");
      line.append(element("time", "clipping-artifact-time", sourceTime(segment.start_ms, segment.end_ms)));
      const text = element("span", "clipping-transcript-text", segment.text || "");
      const speaker = typeof segment.speaker_id === "string" && segment.speaker_id ? ` · ${segment.speaker_id}` : "";
      const confidence = Number(segment.confidence);
      const confidenceText = Number.isFinite(confidence) ? ` · confidence ${confidence} (uncalibrated)` : "";
      const meta = element("span", "clipping-artifact-note", `${speaker}${confidenceText}`);
      line.append(text);
      if (speaker || confidenceText) line.append(meta);
      list.append(line);
    }
    section.append(list);
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
    for (const [key, title] of [["topics", "Topics"], ["timeline", "Timeline"]]) {
      if (!Array.isArray(context[key]) || !context[key].length) continue;
      const list = element("ul", "clipping-artifact-list");
      for (const entry of context[key].slice(0, 500)) {
        if (!entry || typeof entry !== "object") continue;
        const parts = [sourceTime(entry.start_ms, entry.end_ms), entry.label, entry.summary];
        if (Array.isArray(entry.keywords)) parts.push(entry.keywords.filter((word) => typeof word === "string").join(", "));
        const line = parts.filter((part) => part !== null && part !== undefined && String(part).trim() !== "").join(" · ");
        if (line) list.append(element("li", "", line));
      }
      if (list.children.length) {
        section.append(element("h6", "", title), list);
        hasContent = true;
      }
    }
    if (hasContent) parent.append(section);
  }

  function renderEvents(parent, title, events) {
    if (!Array.isArray(events) || !events.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", title));
    const list = element("ul", "clipping-artifact-list");
    for (const event of events.slice(0, 1000)) {
      if (!event || typeof event !== "object") continue;
      const details = [sourceTime(event.start_ms, event.end_ms), event.kind, event.evidence, event.text, event.detail, event.description];
      if (event.score !== null && event.score !== undefined) details.push(`score ${shortValue(event.score)}`);
      const line = details.filter((part) => part !== null && part !== undefined && String(part).trim() !== "").join(" · ");
      if (line) list.append(element("li", "", line));
    }
    if (list.children.length) {
      section.append(list);
      parent.append(section);
    }
  }

  function renderCandidates(parent, candidates, title = "Clip candidates") {
    if (!Array.isArray(candidates) || !candidates.length) return;
    const section = element("section", "clipping-artifact-section");
    section.append(element("h5", "", title));
    section.append(element("p", "clipping-artifact-note", "Scores are heuristic editorial rankings, not virality predictions. Confidence values are uncalibrated."));
    for (const candidate of candidates.slice(0, 100)) {
      if (!candidate || typeof candidate !== "object") continue;
      const item = element("article", "clipping-candidate");
      const rank = Number.isSafeInteger(candidate.rank) ? `#${candidate.rank} · ` : "";
      const title = typeof candidate.title === "string" && candidate.title ? candidate.title : `Candidate ${shortValue(candidate.id || "")}`;
      item.append(element("strong", "", `${rank}${title}`));
      item.append(element("p", "clipping-artifact-note", `${sourceTime(candidate.start_ms, candidate.end_ms)}${candidate.duration_ms ? ` · ${formatDuration(candidate.duration_ms)}` : ""}`));
      if (typeof candidate.hook === "string" && candidate.hook) item.append(element("p", "clipping-artifact-copy", `Hook: ${candidate.hook}`));
      if (Array.isArray(candidate.title_variants) && candidate.title_variants.length) {
        const variants = element("ul", "clipping-artifact-list");
        for (const variant of candidate.title_variants.slice(0, 20)) {
          const text = typeof variant === "string" ? variant : variant && (variant.title || variant.text);
          if (text) variants.append(element("li", "", `Title: ${text}`));
        }
        if (variants.children.length) {
          item.append(element("h6", "", "Title options"), variants);
        }
      }
      if (candidate.editorial_score !== undefined && (typeof candidate.editorial_score === "string" || typeof candidate.editorial_score === "number")) {
        item.append(element("p", "clipping-artifact-note", `Editorial score: ${shortValue(candidate.editorial_score)}`));
      } else if (candidate.editorial_score && typeof candidate.editorial_score === "object" && candidate.editorial_score.total !== undefined) {
        item.append(element("p", "clipping-artifact-note", `Editorial score: ${shortValue(candidate.editorial_score.total)}`));
      }
      const scores = candidate.scores || candidate.editorial_score?.components;
      renderMetadataMap(item, "Score components", scores);
      if (candidate.confidence !== undefined || candidate.confidence_label) {
        const confidence = candidate.confidence === undefined ? "" : ` ${shortValue(candidate.confidence)}`;
        const label = candidate.confidence_label ? ` (${candidate.confidence_label})` : "";
        item.append(element("p", "clipping-artifact-note", `Confidence${confidence}${label} · uncalibrated`));
      }
      if (Array.isArray(candidate.evidence_refs) && candidate.evidence_refs.length) {
        item.append(element("p", "clipping-artifact-note", `Evidence references: ${candidate.evidence_refs.join(", ")}`));
      }
      renderEvidenceList(item, candidate.evidence);
      section.append(item);
    }
    if (section.children.length > 2) parent.append(section);
  }

  function renderArtifact(parent, artifact) {
    const section = element("section", "clipping-artifact");
    const label = [artifact.type, artifact.schema_version, artifact.version ? `v${artifact.version}` : ""]
      .filter(Boolean).join(" · ") || "Saved analysis artifact";
    section.append(element("h4", "", label));
    let payload;
    try {
      payload = JSON.parse(String(artifact.payload_json || ""));
    } catch (_error) {
      section.append(element("p", "clipping-row-error", "This saved artifact could not be displayed."));
      parent.append(section);
      return;
    }
    if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
      section.append(element("p", "clipping-artifact-note", "The saved artifact has no displayable analysis fields."));
      parent.append(section);
      return;
    }
    const data = payload.payload && typeof payload.payload === "object" && !Array.isArray(payload.payload) ? payload.payload : payload;
    if (data.analysis_version) section.append(element("p", "clipping-artifact-note", `Analysis version: ${shortValue(data.analysis_version)}`));
    if (data.content_type) section.append(element("p", "clipping-artifact-note", `Content type: ${shortValue(data.content_type)}`));
    if (data.language && typeof data.language === "object") {
      const language = [data.language.code, data.language.script, data.language.probability !== undefined ? `probability ${shortValue(data.language.probability)} (uncalibrated)` : ""]
        .filter((part) => part !== null && part !== undefined && String(part).trim() !== "").join(" · ");
      if (language) section.append(element("p", "clipping-artifact-note", `Language: ${language}`));
    }
    const estimatedCost = Number(data.cost_estimate_micro_usd);
    if (Number.isSafeInteger(estimatedCost) || data.cost_basis || data.compute_seconds !== undefined) {
      const estimate = element("section", "clipping-artifact-section");
      estimate.append(element("h5", "", "Worker cost estimate"));
      estimate.append(element("p", "clipping-artifact-note", "Operator estimate from the configured rate, not a provider quote. Modal invoice totals are not reconciled automatically and require manual settlement."));
      if (Number.isSafeInteger(estimatedCost) && estimatedCost >= 0) estimate.append(element("p", "clipping-artifact-copy", `Estimated cost: ${formatUSD(estimatedCost)}`));
      if (data.cost_basis) estimate.append(element("p", "clipping-artifact-note", `Basis: ${shortValue(data.cost_basis)}`));
      if (data.rate_micro_usd_per_second !== undefined) estimate.append(element("p", "clipping-artifact-note", `Rate: ${formatUSD(Number(data.rate_micro_usd_per_second))} per second`));
      if (Number.isFinite(Number(data.compute_seconds))) estimate.append(element("p", "clipping-artifact-note", `Compute time: ${shortValue(data.compute_seconds)} seconds`));
      section.append(estimate);
    }
    renderMetadataMap(section, "Model and algorithm versions", { ...(data.model_versions || {}), ...(data.algorithm_versions || {}) });
    renderMetadataMap(section, "Prompt versions", data.prompt_versions);
    renderTranscript(section, data);
    renderContext(section, data.context);
    renderEvents(section, "Audio events", data.audio_events);
    renderEvents(section, "Visual events", data.visual_events);
    if (Array.isArray(data.evidence_events)) renderEvents(section, "Detected events", data.evidence_events);
    renderCandidates(section, data.candidates, "Selected clip candidates");
    if (data.candidate_pools && typeof data.candidate_pools === "object" && !Array.isArray(data.candidate_pools)) {
      for (const [profile, candidates] of Object.entries(data.candidate_pools).slice(0, 5)) {
        renderCandidates(section, candidates, `Saved candidate pool: ${contentType(profile)}`);
      }
    }
    parent.append(section);
  }

  function renderJobArtifacts(job, jobID) {
    const detail = state.jobDetails.get(jobID);
    const artifacts = detail && Array.isArray(detail.artifacts) ? detail.artifacts : Array.isArray(job.artifacts) ? job.artifacts : [];
    const panel = element("div", "clipping-job-details");
    const error = state.jobDetailErrors.get(jobID);
    if (error) panel.append(element("p", "clipping-row-error", error));
    renderJobStages(panel, detail || job, jobID);
    if (!artifacts.length && !error) {
      const message = state.loadingJobDetails.has(jobID)
        ? "Loading saved analysis details…"
        : "No saved analysis artifacts are available for this job.";
      panel.append(element("p", "clipping-artifact-note", message));
    }
    for (const artifact of artifacts) {
      if (artifact && typeof artifact === "object") renderArtifact(panel, artifact);
    }
    return panel;
  }

  function renderJobStages(parent, job, jobID) {
    const stages = Array.isArray(job && job.stages) ? job.stages : [];
    if (!stages.length) return;
    const section = element("section", "clipping-artifact-section clipping-stage-section");
    section.append(element("h5", "", "Analysis stages and cost"));
    for (const stage of stages) {
      if (!stage || typeof stage !== "object") continue;
      const card = element("article", "clipping-stage-card");
      card.append(element("strong", "", `${shortValue(stage.name || "Analysis")} · ${shortValue(stage.status || "Status unavailable")}`));
      const summary = element("dl", "clipping-artifact-metadata");
      const estimate = Number(stage.estimated_micro_usd);
      const actual = Number(stage.actual_micro_usd);
      const reserved = Number(stage.reserved_micro_usd);
      const reconciled = stage.cost_reconciled === true;
      const actualIsValid = Number.isSafeInteger(actual) && actual >= 0;
      const remaining = Number.isSafeInteger(reserved) && reserved >= 0
        ? !reconciled ? reserved : actualIsValid ? Math.max(0, reserved - actual) : null
        : null;
      const fields = [
        ["Estimate", Number.isSafeInteger(estimate) && estimate >= 0 ? formatUSD(estimate) : "Estimate unavailable"],
        ["Recorded actual", !reconciled ? "Not recorded" : actualIsValid ? formatUSD(actual) : "Actual unavailable"],
        ["Reserved", Number.isSafeInteger(reserved) && reserved >= 0 ? formatUSD(reserved) : "Reservation unavailable"],
        ["Remaining reserved hold", remaining === null ? "Unavailable" : formatUSD(remaining)],
        ["Cost reconciliation", reconciled ? "Reconciled" : "Not reconciled"]
      ];
      for (const [label, value] of fields) summary.append(element("dt", "", label), element("dd", "", value));
      card.append(summary);
      const attemptID = safeID(stage.attempt_id);
      if (stage.cost_reconciled === false && attemptID) {
        card.append(renderCostReconciliationForm(jobID, attemptID));
      }
      section.append(card);
    }
    if (section.children.length > 1) parent.append(section);
  }

  function renderCostReconciliationForm(jobID, attemptID) {
    const form = element("form", "clipping-cost-reconciliation");
    form.append(element("h6", "", "Reconcile from the Modal invoice"));
    form.append(element("p", "clipping-artifact-note", "Enter the actual invoice amount for this attempt. This records the amount in FrameVault; it does not change or settle the Modal invoice."));
    const amountLabel = element("label", "clipping-cost-field", "Entered invoice actual cost (USD)");
    const amount = element("input");
    amount.type = "number";
    amount.min = "0";
    amount.step = "0.000001";
    amount.inputMode = "decimal";
    amount.required = true;
    amount.setAttribute("aria-label", `Entered invoice actual cost for attempt ${attemptID}`);
    amountLabel.append(amount);
    const referenceLabel = element("label", "clipping-cost-field", "Reconciliation reference");
    const reference = element("input");
    reference.type = "text";
    reference.maxLength = 256;
    reference.required = true;
    reference.setAttribute("aria-label", `Reconciliation reference for attempt ${attemptID}`);
    referenceLabel.append(reference, element("small", "clipping-help", "Required; maximum 256 UTF-8 bytes."));
    const error = element("p", "form-error");
    error.setAttribute("role", "alert");
    const submit = (event) => {
      event?.preventDefault?.();
      const actualCost = parseActualCostUSDToMicroUSD(amount.value);
      const reconciliationReference = String(reference.value || "").trim();
      if (actualCost === null) {
        error.textContent = "Enter a nonnegative invoice amount in USD with up to six decimal places.";
        amount.focus();
        return;
      }
      if (!reconciliationReference || utf8ByteLength(reconciliationReference) > 256) {
        error.textContent = "Enter a reconciliation reference of at most 256 UTF-8 bytes.";
        reference.focus();
        return;
      }
      void saveCostReconciliation(jobID, attemptID, actualCost, reconciliationReference, error, saveButton);
    };
    form.addEventListener("submit", submit);
    const saveButton = button("Record invoice actual", "button secondary clipping-action", () => submit());
    form.append(amountLabel, referenceLabel, error, saveButton);
    return form;
  }

  function renderSelectionEditor(job, jobID) {
    const editor = element("form", "clipping-selection-form");
    editor.append(element("h5", "", "Selection options"));
    editor.append(element("p", "clipping-artifact-note", "Changing these options selects from the saved full-timeline analysis; it does not rerun transcription."));
    const typeLabel = element("label", "clipping-selection-field", "Selection profile");
    const typeSelect = makeContentTypeSelect(job.content_type, `Selection profile for job ${jobID}`);
    typeLabel.append(typeSelect);
    const minLabel = element("label", "clipping-selection-field", "Minimum clip seconds");
    const minInput = element("input");
    minInput.type = "number";
    minInput.min = "15";
    minInput.max = "180";
    minInput.step = "1";
    minInput.value = String(Number.isInteger(Number(job.min_clip_seconds)) ? job.min_clip_seconds : 15);
    minLabel.append(minInput);
    const maxLabel = element("label", "clipping-selection-field", "Maximum clip seconds");
    const maxInput = element("input");
    maxInput.type = "number";
    maxInput.min = "15";
    maxInput.max = "180";
    maxInput.step = "1";
    maxInput.value = String(Number.isInteger(Number(job.max_clip_seconds)) ? job.max_clip_seconds : 180);
    maxLabel.append(maxInput);
    const limitLabel = element("label", "clipping-selection-field", "Maximum candidates");
    const limitInput = element("input");
    limitInput.type = "number";
    limitInput.min = "7";
    limitInput.max = "10";
    limitInput.step = "1";
    limitInput.value = String(Number.isInteger(Number(job.candidate_limit)) ? job.candidate_limit : 10);
    limitLabel.append(limitInput);
    const error = element("p", "form-error");
    error.setAttribute("role", "alert");
    const submitSelection = (event) => {
      event?.preventDefault?.();
      const options = selectionOptions(minInput.value, maxInput.value, limitInput.value);
      if (!options) {
        error.textContent = "Use clip lengths from 15 to 180 seconds, with minimum no greater than maximum, and choose 7 to 10 candidates.";
        return;
      }
      void saveJobSelection(jobID, contentType(typeSelect.value), options, error, save);
    };
    editor.addEventListener("submit", submitSelection);
    const save = button("Update selection", "button secondary clipping-action", () => submitSelection());
    editor.append(typeLabel, minLabel, maxLabel, limitLabel, error, save);
    return editor;
  }

  async function loadJobDetails(jobID, force = false) {
    const id = safeID(jobID);
    if (!id || !current(state.lifecycle)) return;
    if (!force && state.jobDetails.has(id)) {
      state.expandedJobIDs.add(id);
      renderJobs();
      return;
    }
    if (state.loadingJobDetails.has(id)) return;
    const lifecycle = state.lifecycle;
    state.loadingJobDetails.add(id);
    state.jobDetailErrors.delete(id);
    renderJobs();
    try {
      const response = await api(`/api/clipping/jobs/${encodeURIComponent(id)}`);
      if (!current(lifecycle)) return;
      const detail = response && response.job && typeof response.job === "object" ? response.job : response;
      if (!detail || (detail.id && safeID(detail.id) !== id)) throw new Error("Saved job details did not match this job.");
      state.jobDetails.set(id, detail);
      state.expandedJobIDs.add(id);
    } catch (error) {
      if (current(lifecycle) && error.name !== "AbortError") state.jobDetailErrors.set(id, error.message || "Saved analysis details could not be loaded.");
    } finally {
      state.loadingJobDetails.delete(id);
      if (current(lifecycle)) renderJobs();
    }
  }

  async function saveJobSelection(jobID, profile, options, errorElement, buttonElement) {
    const id = safeID(jobID);
    if (!id || !current(state.lifecycle)) return;
    const lifecycle = state.lifecycle;
    errorElement.textContent = "";
    buttonElement.disabled = true;
    try {
      await api(`/api/clipping/jobs/${encodeURIComponent(id)}/selection`, {
        method: "POST",
        body: JSON.stringify({ content_type: contentType(profile), ...options })
      });
      if (!current(lifecycle)) return;
      state.jobDetails.delete(id);
      await Promise.allSettled([
        loadJobDetails(id, true),
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError)
      ]);
    } catch (error) {
      if (current(lifecycle)) errorElement.textContent = error.message || "Selection could not be updated.";
    } finally {
      if (current(lifecycle)) buttonElement.disabled = false;
    }
  }

  async function saveCostReconciliation(jobID, attemptID, actualCostMicroUSD, reconciliationReference, errorElement, buttonElement) {
    const id = safeID(jobID);
    const attempt = safeID(attemptID);
    if (!id || !attempt || !current(state.lifecycle)) return;
    const lifecycle = state.lifecycle;
    errorElement.textContent = "";
    buttonElement.disabled = true;
    try {
      await api(`/api/clipping/jobs/${encodeURIComponent(id)}/reconcile-cost`, {
        method: "POST",
        body: JSON.stringify({
          attempt_id: attempt,
          actual_cost_micro_usd: actualCostMicroUSD,
          reconciliation_reference: reconciliationReference
        })
      });
      if (!current(lifecycle)) return;
      state.jobDetails.delete(id);
      await Promise.allSettled([
        loadJobDetails(id, true),
        load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError),
        load("batches", "/api/clipping/batches", renderBatches, ui.batchesError)
      ]);
    } catch (error) {
      if (current(lifecycle)) errorElement.textContent = error.message || "Invoice cost could not be recorded.";
    } finally {
      if (current(lifecycle)) buttonElement.disabled = false;
    }
  }

  function toggleJobDetails(jobID) {
    const id = safeID(jobID);
    if (!id) return;
    if (state.expandedJobIDs.has(id)) {
      state.expandedJobIDs.delete(id);
      renderJobs();
      return;
    }
    if (state.jobDetails.has(id)) {
      state.expandedJobIDs.add(id);
      renderJobs();
      return;
    }
    state.expandedJobIDs.add(id);
    void loadJobDetails(id);
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

  function makeContentTypeSelect(value, label) {
    const select = element("select", "clipping-content-type-select");
    select.setAttribute("aria-label", label);
    for (const type of CONTENT_TYPES) {
      const option = element("option", "", type[0].toUpperCase() + type.slice(1));
      option.value = type;
      select.append(option);
    }
    select.value = contentType(value);
    return select;
  }

  function selectionOptions(minValue, maxValue, limitValue) {
    const min = Number(minValue);
    const max = Number(maxValue);
    const limit = Number(limitValue);
    if (!Number.isInteger(min) || min < 15 || min > 180
      || !Number.isInteger(max) || max < 15 || max > 180 || min > max
      || !Number.isInteger(limit) || limit < 7 || limit > 10) return null;
    return { min_clip_seconds: min, max_clip_seconds: max, candidate_limit: limit };
  }

  function renderWorkerNote() {
    const config = state.config || {};
    const available = workerIsEnabled();
    const note = available
      ? "The analysis worker is configured and enabled. Its connection has not been live checked; adding or uploading a source does not start analysis."
      : "You can prepare sources and queue analysis jobs. The worker is disabled or not configured, so queued jobs wait. Adding media does not start paid analysis.";
    ui.workerNote.textContent = note;
    ui.workerNote.hidden = false;
  }

  function renderWorkerMetadata() {
    const config = state.workerConfig;
    const fragment = document.createDocumentFragment();
    if (!config) {
      ui.workerMetadata.replaceChildren(fragment);
      return;
    }
    const status = config.configured
      ? `${config.enabled ? "Enabled" : "Disabled"} · configured, not live checked`
      : "Not configured · worker disabled by default";
    ui.workerStatus.textContent = status;
    const fields = [
      ["Endpoint host", config.endpoint_host],
      ["Provider", config.provider],
      ["Model", config.model],
      ["Pipeline revision", config.pipeline_revision],
      ["Operator rate", config.rate_micro_usd_per_second === null ? "" : `${formatUSD(config.rate_micro_usd_per_second)} per second`],
      ["Readiness", config.configured ? "Configured, not live checked" : config.readiness],
      ["Billing basis", config.billing_basis]
    ].filter(([, value]) => value !== null && value !== undefined && value !== "");
    const list = element("dl", "clipping-artifact-metadata clipping-worker-fields");
    for (const [label, value] of fields) list.append(element("dt", "", label), element("dd", "", String(value)));
    if (list.children.length) fragment.append(list);
    if (Array.isArray(config.capabilities) && config.capabilities.length) {
      const capabilities = element("p", "clipping-worker-capabilities", `Supported capabilities: ${config.capabilities.join(", ")}`);
      fragment.append(capabilities);
    }
    ui.workerMetadata.replaceChildren(fragment);
  }

  async function loadWorkerConfig() {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence.workerConfig;
    ui.workerError.textContent = "";
    try {
      const response = await api("/api/clipping/config/worker");
      if (!current(lifecycle) || sequence !== state.requestSequence.workerConfig) return;
      const payload = response && response.worker ? response.worker : response;
      state.workerConfig = safeWorkerConfig(payload);
      ui.workerEnabled.checked = state.workerConfig.enabled;
      ui.workerPipelineRevision.value = state.workerConfig.pipeline_revision || "";
      if (state.workerConfig.rate_micro_usd_per_second !== null && state.workerConfig.rate_micro_usd_per_second > 0) {
        ui.workerRate.value = rateInputValue(state.workerConfig.rate_micro_usd_per_second);
      }
      ui.workerToken.value = "";
      renderWorkerMetadata();
      renderWorkerNote();
    } catch (error) {
      if (!current(lifecycle) || sequence !== state.requestSequence.workerConfig || error.name === "AbortError") return;
      state.workerConfig = null;
      ui.workerStatus.textContent = "Worker configuration could not be loaded.";
      ui.workerMetadata.replaceChildren();
      ui.workerError.textContent = error.message || "Could not load worker settings.";
      ui.workerSaveNote.textContent = "Configuration status is unavailable. No worker connection test was attempted.";
      renderWorkerNote();
    }
  }

  async function saveWorkerConfig(event) {
    event.preventDefault();
    const lifecycle = state.lifecycle;
    if (!current(lifecycle)) return;
    ui.workerError.textContent = "";
    const endpoint = String(ui.workerEndpoint.value || "").trim();
    const bearerToken = String(ui.workerToken.value || "");
    const pipelineRevision = String(ui.workerPipelineRevision.value || "").trim();
    const rate = parseUSDToMicroUSD(ui.workerRate.value);
    let parsedEndpoint;
    try { parsedEndpoint = new URL(endpoint); } catch (_error) { parsedEndpoint = null; }
    if (!parsedEndpoint || parsedEndpoint.protocol !== "https:" || !parsedEndpoint.hostname
      || parsedEndpoint.username || parsedEndpoint.password || parsedEndpoint.search || parsedEndpoint.hash) {
      ui.workerError.textContent = "Enter the full HTTPS Modal worker endpoint without credentials.";
      focusInDisclosures([ui.settingsDisclosure, ui.workerDisclosure], ui.workerEndpoint);
      return;
    }
    if (rate === null) {
      ui.workerError.textContent = "Enter a positive operator-estimated rate in USD per second with up to six decimal places.";
      focusInDisclosures([ui.settingsDisclosure, ui.workerDisclosure], ui.workerRate);
      return;
    }
    if (!/^[A-Za-z0-9._-]{1,64}$/.test(pipelineRevision)) {
      ui.workerError.textContent = "Enter a pipeline revision of 1–64 letters, digits, dots, underscores, or hyphens that matches the Modal secret.";
      focusInDisclosures([ui.settingsDisclosure, ui.workerDisclosure], ui.workerPipelineRevision);
      return;
    }
    if (bearerToken && !/^[0-9a-f]{64}$/.test(bearerToken)) {
      ui.workerError.textContent = "Enter a worker bearer token with exactly 64 lowercase hexadecimal characters.";
      focusInDisclosures([ui.settingsDisclosure, ui.workerDisclosure], ui.workerToken);
      return;
    }
    if (state.workerConfig?.configured !== true && !bearerToken) {
      ui.workerError.textContent = "A worker bearer token is required for initial setup.";
      focusInDisclosures([ui.settingsDisclosure, ui.workerDisclosure], ui.workerToken);
      return;
    }
    ui.workerSave.disabled = true;
    try {
      await api("/api/clipping/config/worker", {
        method: "PUT",
        body: JSON.stringify({
          enabled: ui.workerEnabled.checked,
          endpoint,
          bearer_token: bearerToken,
          rate_micro_usd_per_second: rate,
          pipeline_revision: pipelineRevision
        })
      });
      if (!current(lifecycle)) return;
      ui.workerToken.value = "";
      ui.workerSaveNote.textContent = "Worker settings saved. The endpoint was not live checked; this status reports configuration only.";
      await Promise.allSettled([loadWorkerConfig(), loadConfig()]);
    } catch (error) {
      if (current(lifecycle)) ui.workerError.textContent = error.message || "Worker settings could not be saved.";
    } finally {
      if (current(lifecycle)) ui.workerSave.disabled = false;
    }
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

  function setUploadIdle(keepResumeActionsDisabled = false) {
    state.upload = null;
    ui.uploadSubmit.disabled = false;
    ui.uploadFile.disabled = false;
    ui.uploadCancel.disabled = false;
    updateUploadReadiness();
    if (!keepResumeActionsDisabled) syncResumeActions();
  }

  function handleUploadFileChange() {
    if (!state.active) {
      clearSelectedUpload();
      return;
    }
    if (state.upload) {
      ui.uploadError.textContent = "An upload is already active. Pause it before choosing another file.";
      return;
    }
    const files = Array.from(ui.uploadFile.files || []);
    if (files.length !== 1) {
      clearSelectedUpload();
      if (files.length > 1) ui.uploadError.textContent = "Choose one audio or video file at a time.";
      return;
    }
    selectUploadFile(files[0]);
  }

  function handleUploadDrop(event) {
    event.preventDefault();
    state.dropDepth = 0;
    setDropActive(false);
    if (!state.active) {
      clearSelectedUpload();
      return;
    }
    if (state.upload) {
      ui.uploadError.textContent = "An upload is already active. Pause it before choosing another file.";
      return;
    }
    const files = Array.from(event.dataTransfer?.files || []);
    if (files.length !== 1) {
      clearSelectedUpload();
      ui.uploadError.textContent = files.length > 1
        ? "Drop one audio or video file at a time."
        : "Drop one audio or video file here.";
      return;
    }
    selectUploadFile(files[0]);
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
        if (!state.sourceContentTypes.has(id)) state.sourceContentTypes.set(id, "general");
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
        const options = element("details", "clipping-source-options");
        options.append(element("summary", "", "Options"));
        const typeLabel = element("label", "clipping-content-type", "Content type");
        const typeSelect = makeContentTypeSelect(state.sourceContentTypes.get(id), `Content type for ${sourceName(source)}`);
        typeSelect.addEventListener("change", () => state.sourceContentTypes.set(id, contentType(typeSelect.value)));
        typeLabel.append(typeSelect);
        options.append(typeLabel);
        actions.append(options);
        actions.append(button("Start analysis", "button secondary clipping-action", () => { void createJob(id); }));
      } else if (status === "uploading") {
        const resume = button("Resume upload", "button secondary clipping-action", () => chooseResumeFile(id));
        resume.disabled = Boolean(state.upload);
        actions.append(resume);
      }
      actions.append(button("Remove", "text-button clipping-remove", () => { void removeSource(id); }));
      row.append(details, actions);
      fragment.append(row);
    }
    ui.sources.replaceChildren(fragment);
    syncWorkVisibility();
    renderBatchReadiness();
  }

  function renderCostDetails(spent, budget, label = "Cost details") {
    const details = element("details", "clipping-cost-details");
    details.append(element("summary", "", label));
    details.append(element("p", "clipping-cost-copy", `${formatUSD(spent)} of ${formatUSD(budget)}`));
    return details;
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
      : status === "queued" && !workerIsEnabled()
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
    if (job.content_type) footer.append(element("span", "", `Profile: ${contentType(job.content_type)}`));
    footer.append(renderCostDetails(job.spent_micro_usd, job.budget_micro_usd));
    const actions = element("div", "clipping-row-actions");
    if (["queued", "running", "paused_budget"].includes(status)) {
      actions.append(button("Cancel job", "button secondary clipping-action", () => { void cancelJob(job.id); }));
    }
    row.append(header, track, footer);
    const errorCopy = jobErrorCopy(job);
    if (errorCopy) row.append(element("p", "clipping-row-error", errorCopy));
    const id = safeID(job.id);
    const hasArtifacts = Array.isArray(job.artifacts) && job.artifacts.length > 0;
    const hasCostStageDetails = Array.isArray(job.stages)
      && job.stages.some((stage) => stage
        && (stage.cost_reconciled === false || stage.cost_reconciled === true)
        && safeID(stage.attempt_id));
    if (id && (status === "completed" || hasArtifacts || hasCostStageDetails)) {
      const expanded = state.expandedJobIDs.has(id);
      const detailsButton = button(
        state.loadingJobDetails.has(id) ? "Loading job details…" : expanded ? "Hide job details" : status === "completed" || hasArtifacts ? "View saved analysis" : "View job details",
        "button secondary clipping-action",
        () => toggleJobDetails(id)
      );
      detailsButton.disabled = state.loadingJobDetails.has(id);
      actions.append(detailsButton);
      if (expanded) {
        row.append(renderJobArtifacts(job, id));
        const detail = state.jobDetails.get(id);
        if (status === "completed" && detail) row.append(renderSelectionEditor(detail, id));
      }
    }
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
    syncWorkVisibility();
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
      const statusText = status === "queued" && !workerIsEnabled()
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
      footer.append(renderCostDetails(batch.spent_micro_usd, batch.budget_micro_usd));
      if (["queued", "running", "paused_budget"].includes(status)) {
        footer.append(button("Cancel batch", "button secondary clipping-action", () => { void cancelBatch(batch.id); }));
      }
      row.append(header, track, footer);
      if (batch.error) row.append(element("p", "clipping-row-error", batch.error));
      fragment.append(row);
    }
    ui.batches.replaceChildren(fragment);
    syncWorkVisibility();
  }

  function renderBatchReadiness() {
    const readyIDs = new Set(state.sources.filter((source) => String(source.status).toLowerCase() === "ready").map((source) => safeID(source.id)));
    for (const id of Array.from(state.selectedSourceIDs)) if (!readyIDs.has(id)) state.selectedSourceIDs.delete(id);
    const count = state.selectedSourceIDs.size;
    const perJob = parseBudgetUSD(ui.batchJobBudget.value, MAX_JOB_BUDGET_MICRO_USD);
    const aggregate = parseBudgetUSD(ui.batchBudget.value, MAX_BATCH_BUDGET_MICRO_USD);
    const options = selectionOptions(ui.batchMinSeconds.value, ui.batchMaxSeconds.value, ui.batchCandidateLimit.value);
    const required = perJob === null ? null : perJob * count;
    const fits = count > 0 && perJob !== null && aggregate !== null && options !== null && Number.isSafeInteger(required) && required <= aggregate;
    ui.createBatch.disabled = !fits;
    if (count === 0) ui.batchBudgetNote.textContent = "Select ready sources. The combined job limits must fit within the batch limit.";
    else if (!options) ui.batchBudgetNote.textContent = "Use clip lengths from 15 to 180 seconds, with minimum no greater than maximum, and choose 7 to 10 candidates.";
    else if (perJob === null || aggregate === null) ui.batchBudgetNote.textContent = "Enter positive budget amounts in US dollars.";
    else if (!fits) ui.batchBudgetNote.textContent = `${count} selected ${count === 1 ? "source needs" : "sources need"} at least ${formatUSD(required)} in aggregate budget.`;
    else ui.batchBudgetNote.textContent = `${count} selected ${count === 1 ? "source" : "sources"} · at least ${formatUSD(required)} reserved within a ${formatUSD(aggregate)} batch limit.`;
  }

  async function load(kind, path, render, errorElement) {
    const lifecycle = state.lifecycle;
    const sequence = ++state.requestSequence[kind];
    errorElement.textContent = "";
    syncWorkVisibility();
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
      if (kind === "sources" && state.sources.length === 0) ui.sources.replaceChildren();
      if (kind === "jobs" && state.jobs.length === 0) ui.jobs.replaceChildren();
      if (kind === "batches" && state.batches.length === 0) ui.batches.replaceChildren();
      syncWorkVisibility();
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
      loadWorkerConfig(),
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
    if (!existingSource && !isMediaFile(file)) {
      ui.uploadError.textContent = "Choose one audio or video file.";
      return;
    }
    if (!existingSource && !ui.uploadPermission.checked) {
      ui.uploadError.textContent = "Confirm that you own or have permission to reuse this video and have it processed.";
      updateUploadReadiness();
      return;
    }
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
    syncResumeActions();
    state.selectedUploadFile = file;
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
      clearSelectedUpload();
      setUploadIdle(true);
      if (existingSource) setResumeHelp(false);
      await refresh();
      syncResumeActions();
      const prepared = finalized && (finalized.source || finalized);
      ui.uploadDetail.textContent = statusCopy(prepared && prepared.status, sourceStatusCopy);
    } catch (error) {
      if (!current(lifecycle) || state.upload !== upload || error.name === "AbortError") return;
      const message = error.message || "Upload paused. Use Resume upload to continue from saved progress.";
      ui.uploadDetail.textContent = "Progress is saved. Choose Resume upload to continue.";
      if (upload.sourceID) {
        clearSelectedUpload();
      }
      ui.uploadError.textContent = message;
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
      focusInDisclosures([ui.settingsDisclosure, ui.jobSettingsDisclosure], ui.jobBudget);
      return;
    }
    const options = selectionOptions(ui.jobMinSeconds.value, ui.jobMaxSeconds.value, ui.jobCandidateLimit.value);
    if (!options) {
      ui.sourcesError.textContent = "Use clip lengths from 15 to 180 seconds, with minimum no greater than maximum, and choose 7 to 10 candidates.";
      focusInDisclosures([ui.settingsDisclosure, ui.jobSettingsDisclosure], ui.jobMinSeconds);
      return;
    }
    const profile = contentType(state.sourceContentTypes.get(safeID(sourceID)));
    ui.sourcesError.textContent = "";
    const buttonNode = Array.from(ui.sources.querySelectorAll("button")).find((node) => node.textContent === "Start analysis" && node.closest("article")?.dataset.sourceId === sourceID);
    if (buttonNode) buttonNode.disabled = true;
    const fingerprint = `${sourceID}:${budget}:${profile}:${options.min_clip_seconds}:${options.max_clip_seconds}:${options.candidate_limit}`;
    const idempotencyKey = state.pendingJobKeys.get(fingerprint) || newIdempotencyKey();
    state.pendingJobKeys.set(fingerprint, idempotencyKey);
    try {
      await api("/api/clipping/jobs", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ source_id: sourceID, budget_micro_usd: budget, content_type: profile, ...options })
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
    const options = selectionOptions(ui.batchMinSeconds.value, ui.batchMaxSeconds.value, ui.batchCandidateLimit.value);
    const profile = contentType(ui.batchContentType.value);
    const total = perJob === null ? null : perJob * ids.length;
    if (!ids.length || perJob === null || aggregate === null || !options || !Number.isSafeInteger(total) || total > aggregate) {
      const invalidField = perJob === null ? ui.batchJobBudget
        : aggregate === null || !Number.isSafeInteger(total) || total > aggregate ? ui.batchBudget
          : !options ? ui.batchMinSeconds : null;
      if (invalidField) focusInDisclosures([ui.settingsDisclosure, ui.batchDisclosure], invalidField);
      renderBatchReadiness();
      return;
    }
    ui.batchError.textContent = "";
    ui.createBatch.disabled = true;
    const fingerprint = `${ids.join(",")}:${perJob}:${aggregate}:${profile}:${options.min_clip_seconds}:${options.max_clip_seconds}:${options.candidate_limit}`;
    const idempotencyKey = state.pendingBatchKeys.get(fingerprint) || newIdempotencyKey();
    state.pendingBatchKeys.set(fingerprint, idempotencyKey);
    try {
      await api("/api/clipping/batches", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ source_ids: ids, per_job_budget_micro_usd: perJob, budget_micro_usd: aggregate, content_type: profile, ...options })
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
        syncResumeActions();
        ui.uploadSubmit.disabled = false;
        ui.uploadFile.disabled = false;
      }
      await api(`/api/clipping/sources/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (!current(lifecycle)) return;
      state.selectedSourceIDs.delete(id);
      state.sourceContentTypes.delete(id);
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
      focusInDisclosures([ui.settingsDisclosure, ui.retentionDisclosure], ui.retentionDays);
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
    if (ui.callbackOrigin) ui.callbackOrigin.textContent = String(root.location?.origin || "Dashboard origin unavailable");
    ui.refresh.addEventListener("click", () => { void refresh(); });
    ui.workerForm.addEventListener("submit", (event) => saveWorkerConfig(event));
    ui.uploadForm.addEventListener("submit", (event) => {
      event.preventDefault();
      if (state.upload) {
        ui.uploadError.textContent = "An upload is already active. Pause it before starting another.";
        return;
      }
      const files = Array.from(ui.uploadFile.files || []);
      if (files.length > 1) {
        clearSelectedUpload();
        ui.uploadError.textContent = "Choose one audio or video file at a time.";
        return;
      }
      const file = state.selectedUploadFile || files[0] || null;
      if (!file) {
        ui.uploadError.textContent = "Choose one audio or video file to upload.";
        return;
      }
      if (!isMediaFile(file)) {
        clearSelectedUpload();
        ui.uploadError.textContent = "Choose one audio or video file.";
        return;
      }
      if (!ui.uploadPermission.checked) {
        ui.uploadError.textContent = "Confirm that you own or have permission to reuse this video and have it processed.";
        ui.uploadPermission.focus();
        return;
      }
      void startUpload(file);
    });
    ui.uploadFile.addEventListener("change", handleUploadFileChange);
    ui.uploadPermission.addEventListener("change", () => {
      if (ui.uploadPermission.checked) ui.uploadError.textContent = "";
      updateUploadReadiness();
    });
    ui.uploadDropZone.addEventListener("dragenter", (event) => {
      event.preventDefault();
      if (!state.active) return;
      state.dropDepth++;
      setDropActive(true);
    });
    ui.uploadDropZone.addEventListener("dragover", (event) => {
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
      if (state.active) setDropActive(true);
    });
    ui.uploadDropZone.addEventListener("dragleave", (event) => {
      event.preventDefault();
      state.dropDepth = Math.max(0, state.dropDepth - 1);
      if (state.dropDepth === 0) setDropActive(false);
    });
    ui.uploadDropZone.addEventListener("drop", handleUploadDrop);
    ui.clippingView.addEventListener("dragover", (event) => {
      if (isFileTransfer(event)) event.preventDefault();
    });
    ui.clippingView.addEventListener("drop", (event) => {
      if (isFileTransfer(event)) event.preventDefault();
    });
    ui.uploadCancel.addEventListener("click", () => {
      if (!state.upload) return;
      state.upload.controller?.abort();
      const hadSource = Boolean(state.upload.sourceID);
      if (!hadSource) setResumeHelp(false);
      state.upload = null;
      ui.uploadFile.disabled = false;
      ui.uploadDetail.textContent = "Upload paused. Progress is saved; choose Resume upload to continue.";
      clearSelectedUpload();
      setUploadIdle();
      void load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
    });
    ui.resumeFile.addEventListener("change", () => {
      const files = Array.from(ui.resumeFile.files || []);
      const file = files.length === 1 ? files[0] : null;
      const source = state.sources.find((item) => String(item.id) === state.resumeSourceID);
      state.resumeSourceID = "";
      if (files.length > 1) ui.uploadError.textContent = "Choose the exact original file to resume this upload.";
      else if (file && source) void startUpload(file, source);
    });
    ui.link.addEventListener("input", updateLinkDisclosure);
    ui.link.addEventListener("change", updateLinkDisclosure);
    ui.linkPermissionCheck.addEventListener("change", updateLinkDisclosure);
    ui.linkForm.addEventListener("submit", submitImport);
    [ui.batchJobBudget, ui.batchBudget, ui.batchMinSeconds, ui.batchMaxSeconds, ui.batchCandidateLimit]
      .forEach((input) => input.addEventListener("input", renderBatchReadiness));
    ui.batchContentType.addEventListener("change", renderBatchReadiness);
    ui.createBatch.addEventListener("click", () => { void createBatch(); });
    ui.retentionForm.addEventListener("submit", saveRetention);
    updateUploadReadiness();
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
    clearSelectedUpload();
    syncResumeActions();
    state.dropDepth = 0;
    setDropActive(false);
    ui.uploadFile.disabled = false;
    ui.uploadCancel.disabled = false;
    if (clear) {
      state.sources = [];
      state.jobs = [];
      state.batches = [];
      state.workerConfig = null;
      state.sourceContentTypes.clear();
      state.expandedJobIDs.clear();
      state.jobDetails.clear();
      state.jobDetailErrors.clear();
      state.loadingJobDetails.clear();
      state.selectedSourceIDs.clear();
      state.config = null;
      ui.sourcesError.textContent = "";
      ui.jobsError.textContent = "";
      ui.batchesError.textContent = "";
      ui.batchError.textContent = "";
      ui.sources.replaceChildren(element("p", "empty", "Sources will appear here after you add footage."));
      ui.jobs.replaceChildren(element("p", "empty", "No analysis jobs yet."));
      ui.batches.replaceChildren(element("p", "empty", "No batches yet."));
      syncWorkVisibility();
      ui.workerNote.hidden = true;
      ui.workerStatus.textContent = "Worker configuration has not been loaded.";
      ui.workerMetadata.replaceChildren();
      ui.workerError.textContent = "";
      ui.workerSaveNote.textContent = "The worker starts disabled. Configuration status is not a live connection check.";
      ui.workerEndpoint.value = "";
      ui.workerToken.value = "";
      ui.workerPipelineRevision.value = "";
      ui.workerRate.value = "0.000001";
      ui.workerEnabled.checked = false;
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
    let changedJobID = "";
    try {
      const payload = JSON.parse(event.data);
      const id = safeID(payload && payload.id);
      if (!id) return;
      if (kind === "clipping_job") {
        changedJobID = id;
        state.jobDetails.delete(id);
        state.jobDetailErrors.delete(id);
      }
    } catch (_error) {
      return;
    }
    if (key === "sources") void load("sources", "/api/clipping/sources", renderSources, ui.sourcesError);
    if (key === "jobs") void Promise.allSettled([load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError), load("batches", "/api/clipping/batches", renderBatches, ui.batchesError)])
      .then(() => { if (changedJobID && state.expandedJobIDs.has(changedJobID)) void loadJobDetails(changedJobID, true); });
    if (key === "batches") void Promise.allSettled([load("batches", "/api/clipping/batches", renderBatches, ui.batchesError), load("jobs", "/api/clipping/jobs", renderJobs, ui.jobsError)]);
  }

  bind();
  root.ClippingUI = { configure, start, stop, refresh, handleEvent, hasSources: () => state.sources.length > 0 };
})(typeof window !== "undefined" ? window : globalThis);
