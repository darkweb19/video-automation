const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const pure = require("../internal/webui/static/clipping.js");

const staticDir = path.join(__dirname, "..", "internal", "webui", "static");
const markup = fs.readFileSync(path.join(staticDir, "index.html"), "utf8");
const source = fs.readFileSync(path.join(staticDir, "clipping.js"), "utf8");
const styles = fs.readFileSync(path.join(staticDir, "clipping.css"), "utf8");
const dashboardSource = fs.readFileSync(path.join(staticDir, "app.js"), "utf8");

class Element {
  constructor(tag, id, attributes) {
    this.tagName = String(tag || "div").toLowerCase();
    this.id = id || "";
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    this.dataset = {};
    this.style = {};
    this.className = "";
    this.parent = null;
    this.parentElement = null;
    this.disabled = false;
    this.checked = false;
    this.hidden = false;
    this.open = false;
    this.type = "";
    this.files = [];
    this._textContent = "";
    this.value = "";
    this.defaultValue = "";
    this.classList = {
      contains: (name) => this.className.split(/\s+/).filter(Boolean).includes(name),
      add: (name) => this.classList.toggle(name, true),
      remove: (name) => this.classList.toggle(name, false),
      toggle: (name, enabled) => {
        const names = new Set(this.className.split(/\s+/).filter(Boolean));
        enabled = enabled === undefined ? !names.has(name) : enabled;
        if (enabled) names.add(name);
        else names.delete(name);
        this.className = Array.from(names).join(" ");
        return enabled;
      }
    };
    for (const [name, value] of Object.entries(attributes || {})) this.setAttribute(name, value);
  }
  get textContent() { return this._textContent; }
  set textContent(value) {
    this._textContent = String(value === undefined || value === null ? "" : value);
    this.children = [];
  }
  append(...nodes) {
    for (const node of nodes) {
      if (node.tagName === "#fragment") {
        this.append(...node.children);
        node.children = [];
        continue;
      }
      node.parent = this;
      node.parentElement = this;
      this.children.push(node);
    }
  }
  replaceChildren(...nodes) {
    this.children = [];
    this.append(...nodes);
  }
  addEventListener(name, callback) {
    const list = this.listeners.get(name) || [];
    list.push(callback);
    this.listeners.set(name, list);
  }
  emit(name, event) {
    const value = Object.assign({ preventDefault() {} }, event || {});
    return (this.listeners.get(name) || []).map((callback) => callback(value));
  }
  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name.startsWith("data-")) {
      const key = name.slice(5).replace(/-([a-z])/g, (_match, char) => char.toUpperCase());
      this.dataset[key] = String(value);
    }
  }
  removeAttribute(name) {
    this.attributes.delete(name);
    if (name.startsWith("data-")) delete this.dataset[name.slice(5).replace(/-([a-z])/g, (_m, c) => c.toUpperCase())];
  }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  reset() {
    const resetNode = (node) => {
      if (["input", "textarea", "select"].includes(node.tagName)) {
        node.value = node.defaultValue;
        node.checked = node.defaultChecked;
      }
      for (const child of node.children) resetNode(child);
    };
    resetNode(this);
  }
  focus() { if (this.disabled) return; this.focused = true; this.ownerDocument.activeElement = this; }
  click() { this.emit("click"); }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (node) => {
      for (const child of node.children) {
        if (selector === "button" && child.tagName === "button") matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
}

function parseAttributes(raw) {
  const attributes = {};
  for (const match of raw.matchAll(/([a-zA-Z_:][a-zA-Z0-9_:.-]*)(?:="([^"]*)")?/g)) {
    attributes[match[1]] = match[2] === undefined ? "" : match[2];
  }
  return attributes;
}

function findNode(node, predicate) {
  if (predicate(node)) return node;
  for (const child of node.children || []) {
    const found = findNode(child, predicate);
    if (found) return found;
  }
  return null;
}

function findNodes(node, predicate, output) {
  output = output || [];
  if (predicate(node)) output.push(node);
  for (const child of node.children || []) findNodes(child, predicate, output);
  return output;
}

function treeText(node) {
  return [node._textContent || "", ...(node.children || []).map(treeText)].filter(Boolean).join("\n");
}

function createUI(handler) {
  const elements = new Map();
  const document = {
    activeElement: null,
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      const node = new Element(tag);
      node.ownerDocument = document;
      return node;
    },
    createDocumentFragment() {
      const node = new Element("#fragment");
      node.ownerDocument = document;
      return node;
    }
  };
  const idPattern = /<([a-z][a-z0-9-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi;
  for (const match of markup.matchAll(idPattern)) {
    const attributes = parseAttributes(match[2]);
    const node = new Element(match[1], match[3], attributes);
    node.ownerDocument = document;
    node.type = attributes.type || "";
    node.value = attributes.value || "";
    node.defaultValue = node.value;
    node.checked = Object.hasOwn(attributes, "checked");
    node.defaultChecked = node.checked;
    node.disabled = Object.hasOwn(attributes, "disabled");
    node.hidden = Object.hasOwn(attributes, "hidden");
    node.open = Object.hasOwn(attributes, "open");
    for (const [name, value] of Object.entries(attributes)) {
      if (name.startsWith("data-")) node.dataset[name.slice(5).replace(/-([a-z])/g, (_m, c) => c.toUpperCase())] = value;
    }
    elements.set(match[3], node);
  }
  const requests = [];
  const request = async (endpoint, options) => {
    const call = { endpoint, options: options || {} };
    requests.push(call);
    if (handler) return handler(endpoint, options || {}, requests);
    return {};
  };
  const toasts = [];
  const confirmations = [];
  let confirmResult = true;
  const window = {
    document,
    location: { origin: "http://localhost" },
    console: { log() {} },
    confirm(message) {
      confirmations.push(message);
      return confirmResult;
    }
  };
  const context = vm.createContext({ window, document, URL, AbortController, module: undefined });
  vm.runInContext(source, context, { filename: "static/clipping.js" });
  window.ClippingUI.configure({
    request,
    toast: (message, isError) => toasts.push({ message, isError }),
    confirm: (message) => window.confirm(message)
  });
  return {
    api: window.ClippingUI,
    document,
    elements,
    requests,
    request,
    toasts,
    confirmations,
    window,
    setConfirmResult(value) { confirmResult = value; }
  };
}

async function settle() {
  for (let index = 0; index < 8; index++) {
    await Promise.resolve();
    await new Promise((resolve) => setImmediate(resolve));
  }
}

function defaultHandler(data) {
  const state = Object.assign({
    config: { max_source_bytes: 20 * 1024 * 1024 * 1024, max_duration_ms: 4 * 60 * 60 * 1000 },
    worker: { configured: false, enabled: false, endpoint_host: "", pipeline_revision: "", max_compute_seconds: 43200 },
    sources: [],
    jobs: []
  }, data || {});
  return async (endpoint) => {
    if (endpoint === "/api/clipping/config") return state.config;
    if (endpoint === "/api/clipping/config/worker") return state.worker;
    if (endpoint === "/api/clipping/sources") return { sources: state.sources };
    if (endpoint === "/api/clipping/jobs") return { jobs: state.jobs };
    throw new Error("Unexpected request to " + endpoint);
  };
}

test("YouTube link parsing accepts one public video and rejects unsupported or ambiguous links", () => {
  for (const url of [
    "https://www.youtube.com/watch?v=abcdefghijk",
    "youtu.be/abcdefghijk",
    "youtube.com/shorts/abcdefghijk",
    "https://m.youtube.com/embed/abcdefghijk",
    "abcdefghijk"
  ]) {
    assert.equal(pure.youtubeVideoID(url), "abcdefghijk");
  }
  for (const url of [
    "https://drive.google.com/file/d/abc/view",
    "https://dropbox.com/s/abc/video.mp4",
    "https://youtube.com/playlist?list=PL123",
    "https://youtube.com/watch?v=abcdefghijk&v=mnopqrstuvw",
    "https://attacker.example/watch?v=abcdefghijk",
    "https://user:password@youtube.com/watch?v=abcdefghijk"
  ]) {
    assert.equal(pure.youtubeVideoID(url), "");
  }
  assert.equal(pure.safeID("../source"), "");
  assert.equal(pure.formatDuration(3661000), "1:01:01");
});

test("YouTube import requires rights confirmation, sends only the import contract, and shows live source progress", async () => {
  let sources = [];
  const handler = async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return { max_source_bytes: 20 * 1024 ** 3, max_duration_ms: 4 * 60 * 60 * 1000 };
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false, max_compute_seconds: 43200 };
    if (endpoint === "/api/clipping/sources") return { sources };
    if (endpoint === "/api/clipping/jobs") return { jobs: [] };
    if (endpoint === "/api/clipping/sources/import") {
      const body = JSON.parse(options.body);
      assert.deepEqual(body, { url: "https://youtu.be/abcdefghijk", rights_attested: true });
      assert.equal(options.method, "POST");
      const imported = { id: "clipsrc_1", kind: "youtube_original_file", status: "importing", title: "Example video", progress: 0.25 };
      sources = [imported];
      return { source: imported };
    }
    throw new Error("Unexpected request to " + endpoint);
  };
  const ui = createUI(handler);
  await ui.api.start();
  assert.match(ui.elements.get("clipping-import-limits").textContent, /4:00:00.*20 GiB/);

  ui.elements.get("clipping-link").value = "https://youtu.be/abcdefghijk";
  ui.elements.get("clipping-link").emit("input");
  assert.equal(ui.elements.get("clipping-link-submit").disabled, true);
  ui.elements.get("clipping-link-permission-check").checked = true;
  ui.elements.get("clipping-link-permission-check").emit("change");
  assert.equal(ui.elements.get("clipping-link-submit").disabled, false);
  ui.elements.get("clipping-link-form").emit("submit");
  await settle();

  const importCall = ui.requests.find((call) => call.endpoint === "/api/clipping/sources/import");
  assert.ok(importCall);
  assert.equal(ui.elements.get("clipping-link-permission-check").checked, false);
  assert.equal(ui.elements.get("clipping-link-submit").disabled, true);
  assert.match(treeText(ui.elements.get("clipping-sources")), /Example video/);
  assert.match(treeText(ui.elements.get("clipping-sources")), /25%/);
  assert.match(ui.elements.get("clipping-workflow-status").textContent, /Import accepted/);
});

test("failed import displays the redacted backend message and keeps the link available for correction", async () => {
  const handler = async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return { sources: [] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [] };
    if (endpoint === "/api/clipping/sources/import") throw new Error("This video is unavailable without signing in.");
    throw new Error("Unexpected request to " + endpoint);
  };
  const ui = createUI(handler);
  await ui.api.start();
  ui.elements.get("clipping-link").value = "https://www.youtube.com/watch?v=abcdefghijk";
  ui.elements.get("clipping-link-permission-check").checked = true;
  ui.elements.get("clipping-link-form").emit("submit");
  await settle();
  assert.equal(ui.elements.get("clipping-link-error").textContent, "This video is unavailable without signing in.");
  assert.equal(ui.elements.get("clipping-link").value, "https://www.youtube.com/watch?v=abcdefghijk");
  assert.equal(ui.elements.get("clipping-link").disabled, false);
  assert.equal(ui.document.activeElement, ui.elements.get("clipping-link"));
  assert.equal(ui.toasts.at(-1).message, "This video is unavailable without signing in.");
  assert.equal(ui.toasts.at(-1).isError, true);
});

test("ready sources expose a bounded analysis profile and queue a budget-free Modal job", async () => {
  let jobs = [];
  const source = { id: "clipsrc_ready", kind: "youtube_original_file", status: "ready", title: "Ready video", duration_ms: 60000 };
  const handler = async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false, max_compute_seconds: 43200 };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs };
    if (endpoint === "/api/clipping/jobs" && options.method === "POST") return {};
    if (endpoint === "/api/clipping/jobs" && options.method === "POST") return {};
    throw new Error("Unexpected request to " + endpoint);
  };
  let started = false;
  const ui = createUI(async (endpoint, options, requests) => {
    if (endpoint === "/api/clipping/jobs" && options.method === "POST") {
      started = true;
      const body = JSON.parse(options.body);
      assert.deepEqual(body, { source_id: "clipsrc_ready", content_type: "general" });
      assert.equal(Object.keys(options.headers || {}).includes("Idempotency-Key"), true);
      assert.equal(JSON.stringify(body).includes("budget"), false);
      jobs = [{ id: "clipjob_1", source_id: "clipsrc_ready", content_type: "general", status: "queued", progress: 0 }];
      return { job: jobs[0] };
    }
    return handler(endpoint, options, requests);
  });
  await ui.api.start();
  const analyze = findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Analyze");
  assert.ok(analyze);
  analyze.click();
  await settle();
  assert.equal(started, true);
  assert.match(treeText(ui.elements.get("clipping-jobs")), /Waiting for Modal worker setup/);
  assert.equal(ui.document.activeElement, ui.elements.get("clipping-analysis-heading"));
});

test("source SSE refresh preserves focus on a control that remains in the updated row", async () => {
  const source = { id: "clipsrc_focus", kind: "youtube_original_file", status: "ready", title: "Focus video" };
  const ui = createUI(defaultHandler({ sources: [source] }));
  await ui.api.start();
  const select = findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "select");
  assert.ok(select);
  select.focus();
  ui.api.handleEvent("clipping_source", { data: JSON.stringify({ id: "clipsrc_focus", status: "ready" }) });
  await settle();
  assert.equal(ui.document.activeElement.dataset.clippingFocusKey, "content-type");
  assert.equal(ui.document.activeElement.getAttribute("aria-label"), "Analysis type for Focus video");
});

test("saved results show persisted transcript, context, candidate evidence, and remain open through job events", async () => {
  const job = { id: "clipjob_done", source_id: "clipsrc_done", content_type: "podcast", status: "completed", progress: 1 };
  const source = { id: "clipsrc_done", kind: "youtube_original_file", status: "ready", title: "Finished conversation" };
  const artifact = {
    type: "ai-clip.analysis",
    schema_version: "v1",
    version: 1,
    payload_json: JSON.stringify({ payload: {
      content_type: "podcast",
      transcript: { available: true, original_script: true, segments: [{ start_ms: 1000, end_ms: 2400, text: "<b>Hello there</b>", confidence: 0.8 }] },
      context: { summary: "The host explains a surprising result.", topics: [{ start_ms: 5000, end_ms: 9000, label: "Unexpected result", keywords: ["test"] }] },
      audio_events: [{ start_ms: 10000, end_ms: 12000, kind: "pause", evidence: "long silence" }],
      candidates: [{
        rank: 1, title: "A surprising result", start_ms: 5000, end_ms: 18000, duration_ms: 13000,
        hook: "The result changes the conversation.", editorial_score: { total: 0.72, components: { context: 0.8 } },
        confidence: 0.6, evidence: [{ kind: "transcript", start_ms: 6000, end_ms: 9000, text: "That was unexpected." }]
      }]
    } })
  };
  let resultCalls = 0;
  const handler = async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true, endpoint_host: "worker.modal.run", pipeline_revision: "rev1" };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [job] };
    if (endpoint === "/api/clipping/jobs/clipjob_done/results") {
      resultCalls++;
      return { job_id: "clipjob_done", source_id: "clipsrc_done", artifacts: [artifact] };
    }
    throw new Error("Unexpected request to " + endpoint);
  };
  const ui = createUI(handler);
  await ui.api.start();
  const toggle = findNode(ui.elements.get("clipping-jobs"), (node) => node.tagName === "button" && node.textContent === "View saved analysis");
  assert.ok(toggle);
  toggle.focus();
  toggle.click();
  await settle();
  assert.equal(ui.document.activeElement.dataset.clippingFocusKey, "results");
  let text = treeText(ui.elements.get("clipping-jobs"));
  assert.match(text, /Saved analysis/);
  assert.doesNotMatch(text, /framevault\.clipping|ai-clip\.analysis/);
  assert.doesNotMatch(text, /Transcript status: complete/);
  assert.match(text, /Original-script transcript/);
  assert.match(text, /The host explains a surprising result/);
  assert.match(text, /A surprising result/);
  assert.match(text, /That was unexpected/);
  assert.match(text, /Scores are heuristic editorial rankings/);
  assert.equal(text.includes("<b>Hello there</b>"), true);
  assert.equal(treeText(ui.elements.get("clipping-jobs")).includes("clipping-cost"), false);

  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ job: Object.assign({}, job, { progress: 1 }) }) });
  await settle();
  text = treeText(ui.elements.get("clipping-jobs"));
  assert.match(text, /A surprising result/);
  assert.equal(resultCalls, 2);
  assert.equal(ui.document.activeElement.dataset.clippingFocusKey, "results");
});

test("worker setup uses only the simplified secret-safe worker fields and a bounded time limit", async () => {
  let worker = { configured: false, enabled: false, max_compute_seconds: 43200 };
  let saved;
  const handler = async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker" && options.method === "PUT") {
      saved = JSON.parse(options.body);
      worker = { configured: true, enabled: saved.enabled, endpoint_host: "worker.modal.run", pipeline_revision: saved.pipeline_revision, max_compute_seconds: saved.max_compute_seconds };
      return { worker };
    }
    if (endpoint === "/api/clipping/config/worker") return worker;
    if (endpoint === "/api/clipping/sources") return { sources: [] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [] };
    throw new Error("Unexpected request to " + endpoint);
  };
  const ui = createUI(handler);
  await ui.api.start();
  const form = ui.elements.get("clipping-worker-form");
  ui.elements.get("clipping-worker-endpoint").value = "https://worker.modal.run/dispatch";
  ui.elements.get("clipping-worker-pipeline-revision").value = "m3-test-v1";
  ui.elements.get("clipping-worker-max-compute").value = "45";
  ui.elements.get("clipping-worker-enabled").checked = true;
  form.emit("submit");
  await settle();
  assert.match(ui.elements.get("clipping-worker-error").textContent, /token for initial setup/);
  assert.equal(saved, undefined);

  ui.elements.get("clipping-worker-token").value = "a".repeat(64);
  form.emit("submit");
  await settle();
  assert.deepEqual(saved, {
    enabled: true,
    endpoint: "https://worker.modal.run/dispatch",
    bearer_token: "a".repeat(64),
    pipeline_revision: "m3-test-v1",
    max_compute_seconds: 2700
  });
  assert.equal(Object.hasOwn(saved, "rate_micro_usd_per_second"), false);
  assert.equal(Object.hasOwn(saved, "budget_micro_usd"), false);
  assert.equal(ui.elements.get("clipping-worker-token").value, "");
  assert.match(ui.elements.get("clipping-worker-status").textContent, /configured, not live checked/);
  assert.equal(ui.elements.get("clipping-worker-status").textContent.includes("a".repeat(64)), false);
});

test("logout invalidates a pending source response and clears the clipping workspace", async () => {
  let release;
  const pending = new Promise((resolve) => { release = resolve; });
  const handler = async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return pending;
    if (endpoint === "/api/clipping/jobs") return { jobs: [] };
    return {};
  };
  const ui = createUI(handler);
  const start = ui.api.start();
  ui.api.stop(true);
  release({ sources: [{ id: "clipsrc_old", status: "ready", title: "Old session video" }] });
  await start;
  await settle();
  assert.doesNotMatch(treeText(ui.elements.get("clipping-sources")), /Old session video/);
  assert.equal(ui.elements.get("clipping-source-count").textContent, "0");
});

test("source deletion confirms, prevents duplicate requests, accepts cleanup-pending 202, and removes analyses", async () => {
  const source = { id: "clipsrc_remove", kind: "youtube_original_file", status: "ready", title: "Video to remove" };
  const job = { id: "clipjob_remove", source_id: source.id, status: "running", content_type: "general" };
  let sources = [source];
  let jobs = [job];
  let finishDelete;
  const pendingDelete = new Promise((resolve) => { finishDelete = resolve; });
  const ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return { sources };
    if (endpoint === "/api/clipping/jobs") return { jobs };
    if (endpoint === "/api/clipping/sources/clipsrc_remove" && options.method === "DELETE") {
      return pendingDelete.then((response) => {
        sources = [];
        jobs = [];
        return response;
      });
    }
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();

  const remove = findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Delete video");
  assert.ok(remove);
  assert.equal(remove.getAttribute("aria-label"), "Delete imported video Video to remove");
  ui.setConfirmResult(false);
  remove.click();
  await settle();
  assert.match(ui.confirmations[0], /delete.*Video to remove.*analyses.*stops any analysis/i);
  assert.equal(ui.requests.filter((call) => call.options.method === "DELETE").length, 0);
  assert.equal(ui.toasts.length, 0);

  ui.setConfirmResult(true);
  remove.click();
  await settle();
  const pendingButton = findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Deleting…");
  assert.ok(pendingButton);
  assert.equal(pendingButton.getAttribute("aria-disabled"), "true");
  pendingButton.click();
  await settle();
  assert.equal(ui.requests.filter((call) => call.options.method === "DELETE").length, 1);

  finishDelete({ cleanup_pending: true });
  await settle();
  const deleteCall = ui.requests.find((call) => call.endpoint === "/api/clipping/sources/clipsrc_remove" && call.options.method === "DELETE");
  assert.ok(deleteCall);
  assert.equal(deleteCall.options.body, undefined);
  assert.equal(ui.elements.get("clipping-source-count").textContent, "0");
  assert.equal(ui.elements.get("clipping-job-count").textContent, "0");
  assert.doesNotMatch(treeText(ui.elements.get("clipping-jobs")), /clipjob_remove|Video to remove/);
  assert.equal(ui.toasts.at(-1).message, "Video and analyses removed. File cleanup is still finishing.");
  assert.equal(ui.toasts.at(-1).isError, false);
});

test("active analysis deletion confirms cancellation and keeps its imported video", async () => {
  const source = { id: "clipsrc_keep", kind: "youtube_original_file", status: "ready", title: "Keep this video" };
  const job = { id: "clipjob_active", source_id: source.id, status: "running", content_type: "podcast" };
  let jobs = [job];
  const ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs };
    if (endpoint === "/api/clipping/jobs/clipjob_active" && options.method === "DELETE") {
      jobs = [];
      return null;
    }
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  const remove = findNode(ui.elements.get("clipping-jobs"), (node) => node.tagName === "button" && node.textContent === "Delete analysis");
  assert.ok(remove);
  assert.equal(remove.getAttribute("aria-label"), "Delete analysis for Keep this video");
  remove.click();
  await settle();
  assert.match(ui.confirmations[0], /stops the analysis in progress and keeps the imported video/);
  assert.ok(ui.requests.some((call) => call.endpoint === "/api/clipping/jobs/clipjob_active" && call.options.method === "DELETE"));
  assert.equal(ui.elements.get("clipping-source-count").textContent, "1");
  assert.equal(ui.elements.get("clipping-job-count").textContent, "0");
  assert.match(ui.toasts.at(-1).message, /stopped and deleted/);
});

test("delete failures toast and remain visible inline for both sources and analyses", async () => {
  const source = { id: "clipsrc_error", kind: "youtube_original_file", status: "ready", title: "Protected video" };
  const job = { id: "clipjob_error", source_id: source.id, status: "completed", content_type: "general" };
  const ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [job] };
    if (options.method === "DELETE") {
      const error = new Error(endpoint.includes("sources") ? "Source deletion failed." : "Analysis deletion failed.");
      error.status = 500;
      throw error;
    }
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Delete video").click();
  await settle();
  assert.match(treeText(ui.elements.get("clipping-sources")), /Source deletion failed/);
  assert.equal(ui.toasts.at(-1).message, "Source deletion failed.");
  assert.equal(ui.toasts.at(-1).isError, true);
  findNode(ui.elements.get("clipping-jobs"), (node) => node.tagName === "button" && node.textContent === "Delete analysis").click();
  await settle();
  assert.match(treeText(ui.elements.get("clipping-jobs")), /Analysis deletion failed/);
  assert.equal(ui.toasts.at(-1).message, "Analysis deletion failed.");
  assert.equal(ui.toasts.filter((toast) => toast.isError).length, 2);
});

test("deleting an ambiguously created analysis clears its idempotency key before the next analysis", async () => {
  const source = { id: "clipsrc_ambiguous", kind: "youtube_original_file", status: "ready", title: "Ambiguous analysis video" };
  const existingJob = { id: "clipjob_ambiguous", source_id: source.id, status: "completed", content_type: "general" };
  const newJob = { id: "clipjob_after_delete", source_id: source.id, status: "queued", content_type: "general" };
  let jobs = [];
  const idempotencyKeys = [];
  let postCount = 0;
  const ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs" && options.method === "POST") {
      postCount++;
      idempotencyKeys.push(options.headers["Idempotency-Key"]);
      if (postCount === 1) {
        jobs = [existingJob];
        throw new Error("The connection was interrupted after queueing.");
      }
      jobs = [newJob];
      return { job: newJob };
    }
    if (endpoint === "/api/clipping/jobs/clipjob_ambiguous" && options.method === "DELETE") {
      jobs = [];
      return null;
    }
    if (endpoint === "/api/clipping/jobs") return { jobs };
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Analyze").click();
  await settle();
  assert.equal(postCount, 1);
  await ui.api.refresh();
  assert.ok(findNode(ui.elements.get("clipping-jobs"), (node) => node.dataset.jobId === existingJob.id));
  findNode(ui.elements.get("clipping-jobs"), (node) => node.tagName === "button" && node.textContent === "Delete analysis").click();
  await settle();
  findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Analyze").click();
  await settle();
  assert.equal(postCount, 2);
  assert.notEqual(idempotencyKeys[1], idempotencyKeys[0]);
  assert.ok(findNode(ui.elements.get("clipping-jobs"), (node) => node.dataset.jobId === newJob.id));
});

test("initial failed analyses stay silent; SSE failure toasts once per analysis attempt", async () => {
  const source = { id: "clipsrc_toast", kind: "youtube_original_file", status: "ready", title: "Toast video" };
  const oldFailure = {
    id: "clipjob_old_failure", source_id: source.id, status: "failed", error: "old failure",
    stages: [{ name: "analysis", attempt_id: "attempt_old" }]
  };
  const active = {
    id: "clipjob_sse_failure", source_id: source.id, status: "running",
    stages: [{ name: "analysis", attempt_id: "attempt_1" }]
  };
  let jobs = [oldFailure];
  const ui = createUI(async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs };
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  assert.equal(ui.toasts.length, 0);
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: oldFailure.id, job: oldFailure }) });
  await settle();
  assert.equal(ui.toasts.length, 0);

  jobs = [oldFailure, active];
  await ui.api.refresh();
  jobs = [oldFailure, Object.assign({}, active, { status: "failed", updated_at: "2026-10-15T12:00:00Z", error: "safe worker error" })];
  const failed = jobs[1];
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: failed.id, job: failed }) });
  await settle();
  assert.equal(ui.toasts.length, 1);
  assert.equal(ui.toasts[0].message, "Analysis failed. See the job for details.");
  assert.equal(ui.toasts[0].isError, true);
  assert.equal(ui.toasts[0].message.includes("safe worker error"), false);

  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: failed.id, job: Object.assign({}, failed, { updated_at: "2026-10-15T12:01:00Z" }) }) });
  await settle();
  assert.equal(ui.toasts.length, 1);

  const retry = Object.assign({}, failed, { status: "running", stages: [{ name: "analysis", attempt_id: "attempt_2" }] });
  jobs = [oldFailure, retry];
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: retry.id, job: retry }) });
  await settle();
  const retryFailure = Object.assign({}, retry, { status: "failed" });
  jobs = [oldFailure, retryFailure];
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: retry.id, job: retryFailure }) });
  await settle();
  assert.equal(ui.toasts.length, 2);
});

test("refresh detects a failure only after observing the analysis as active", async () => {
  const source = { id: "clipsrc_refresh_failure", kind: "youtube_original_file", status: "ready", title: "Refresh video" };
  let jobs = [{ id: "clipjob_refresh_failure", source_id: source.id, status: "queued", stages: [{ name: "analysis", attempt_id: "attempt_refresh" }] }];
  const ui = createUI(async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs };
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  jobs = [{ id: "clipjob_refresh_failure", source_id: source.id, status: "failed", error: "safe failure", stages: [{ name: "analysis", attempt_id: "attempt_refresh" }] }];
  await ui.api.refresh({ notify: true });
  assert.equal(ui.toasts.length, 1);
  assert.equal(ui.toasts[0].message, "Analysis failed. See the job for details.");
  await ui.api.refresh({ notify: true });
  assert.equal(ui.toasts.length, 1);
});

test("a fast failed POST response is tracked and toasted before the job list refresh", async () => {
  const source = { id: "clipsrc_fast_failure", kind: "youtube_original_file", status: "ready", title: "Fast failure video" };
  let jobs = [];
  const failed = {
    id: "clipjob_fast_failure", source_id: source.id, status: "failed", error: "safe Go preflight message",
    stages: [{ name: "analysis", attempt_id: "attempt_fast" }]
  };
  let ui;
  ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: true, enabled: true };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs" && options.method === "POST") {
      jobs = [failed];
      ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: failed.id, job: failed }) });
      return { job: failed };
    }
    if (endpoint === "/api/clipping/jobs") return { jobs };
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Analyze").click();
  await settle();
  assert.equal(ui.toasts.length, 1);
  assert.equal(ui.toasts[0].message, "Analysis failed. See the job for details.");
  assert.equal(ui.toasts[0].message.includes("safe Go preflight message"), false);
  assert.match(treeText(ui.elements.get("clipping-jobs")), /safe Go preflight message/);
});

test("deleted analysis tombstones block late result responses and job events from restoring results", async () => {
  const source = { id: "clipsrc_late_result", kind: "youtube_original_file", status: "ready", title: "Deleted result video" };
  const job = { id: "clipjob_late_result", source_id: source.id, status: "completed", content_type: "general" };
  let finishResults;
  const lateResults = new Promise((resolve) => { finishResults = resolve; });
  let resultCalls = 0;
  const ui = createUI(async (endpoint) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [job] };
    if (endpoint === "/api/clipping/jobs/clipjob_late_result/results") {
      resultCalls++;
      return lateResults;
    }
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  findNode(ui.elements.get("clipping-jobs"), (node) => node.tagName === "button" && node.textContent === "View saved analysis").click();
  await settle();
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: job.id, job: Object.assign({}, job, { analysis_deleted: true }) }) });
  await settle();
  finishResults({ job_id: job.id, artifacts: [{ payload_json: JSON.stringify({ payload: { candidates: [{ title: "Must not return" }] } }) }] });
  await settle();
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ id: job.id, job }) });
  await ui.api.refresh();
  assert.equal(ui.elements.get("clipping-job-count").textContent, "0");
  assert.doesNotMatch(treeText(ui.elements.get("clipping-jobs")), /Must not return|Deleted result video/);
  assert.equal(resultCalls, 1);
});

test("logout invalidates a pending clipping deletion response", async () => {
  const source = { id: "clipsrc_logout_delete", kind: "youtube_original_file", status: "ready", title: "Logout deletion" };
  let finishDelete;
  const pendingDelete = new Promise((resolve) => { finishDelete = resolve; });
  const ui = createUI(async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return {};
    if (endpoint === "/api/clipping/config/worker") return { configured: false, enabled: false };
    if (endpoint === "/api/clipping/sources") return { sources: [source] };
    if (endpoint === "/api/clipping/jobs") return { jobs: [] };
    if (endpoint === "/api/clipping/sources/clipsrc_logout_delete" && options.method === "DELETE") return pendingDelete;
    throw new Error("Unexpected request to " + endpoint);
  });
  await ui.api.start();
  findNode(ui.elements.get("clipping-sources"), (node) => node.tagName === "button" && node.textContent === "Delete video").click();
  await settle();
  ui.api.stop(true);
  finishDelete({ cleanup_pending: true });
  await settle();
  assert.equal(ui.elements.get("clipping-source-count").textContent, "0");
  assert.doesNotMatch(treeText(ui.elements.get("clipping-sources")), /Logout deletion/);
  assert.equal(ui.toasts.length, 0);
});

test("Clipping contains only YouTube import and read-only analysis UI and uses authenticated event refreshes", () => {
  const start = markup.indexOf('<section id="clipping-view"');
  const end = markup.indexOf('<section class="view" data-page="vault"', start);
  const clippingMarkup = markup.slice(start, end);
  assert.doesNotMatch(clippingMarkup, /type="file"|clipping-batch|batch|budget|retention|invoice|reconcil|Google Drive|Dropbox|export|publishing|candidate editor/i);
  assert.doesNotMatch(source, /\/api\/clipping\/(batches|sources\/upload|config\/retention)|reconcile-cost|\/selection|\/cancel|\/retry|budget_micro_usd|rate_micro_usd_per_second/i);
  assert.doesNotMatch(styles, /upload|batch|budget|retention|invoice|reconcil/i);
  assert.match(markup, /\/static\/clipping\.js\?v=20261015-delete-abort/);
  assert.match(markup, /\/static\/clipping\.css\?v=20261015-delete-abort/);
  assert.match(markup, /\/static\/app\.js\?v=20261015-delete-abort/);
  assert.doesNotMatch(clippingMarkup, /\/docs\/clipping/);
  assert.doesNotMatch(source, /setInterval|setTimeout|fetch\s*\(/);
  assert.match(dashboardSource, /source\.addEventListener\("clipping_source"/);
  assert.match(dashboardSource, /source\.addEventListener\("clipping_job"/);
  assert.match(dashboardSource, /ClippingUI\?\.configure\([\s\S]*toast:/);
  assert.match(dashboardSource, /confirm: \(message\) => window\.confirm\(message\)/);
  assert.match(dashboardSource, /credentials:\s*"same-origin"/);
  assert.match(source, /\/api\/clipping\/jobs\/" \+ encodeURIComponent\(id\) \+ "\/results"/);
});
