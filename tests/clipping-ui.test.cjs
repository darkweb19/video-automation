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
  click() { if (!this.disabled) this.emit("click"); }
  showModal() { this.previousFocus = this.ownerDocument.activeElement; this.open = true; }
  close() { this.open = false; this.previousFocus?.focus(); }
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
  elements.get("clipping-content-type").value = "general";
  elements.get("clipping-candidate-limit").value = "10";
  const requests = [];
  const request = async (endpoint, options) => {
    const call = { endpoint, options: options || {} };
    requests.push(call);
    if (handler) return handler(endpoint, options || {}, requests);
    return {};
  };
  const toasts = [];
  const window = {
    document,
    location: { origin: "http://localhost" },
    console: { log() {}, info() {}, warn() {} },
    crypto: { randomUUID: () => "test-key-" + requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs").length }
  };
  const context = vm.createContext({ window, document, URL, AbortController, console: window.console, module: undefined });
  vm.runInContext(source, context, { filename: "static/clipping.js" });
  window.ClippingUI.configure({
    request,
    toast: (message, isError) => toasts.push({ message, isError })
  });
  return {
    api: window.ClippingUI,
    document,
    elements,
    requests,
    request,
    toasts,
    window
  };
}

async function settle() {
  for (let index = 0; index < 8; index++) {
    await Promise.resolve();
    await new Promise((resolve) => setImmediate(resolve));
  }
}

const READY = { gemini: { configured: true, enabled: true, verification: "unverified", limits: {
  min_clip_ms: 15000, max_clip_ms: 180000, max_candidates: 10, max_source_duration_ms: 2400000
} } };
const OPTIONS = { content_type: "general", min_clip_ms: 15000, max_clip_ms: 180000, candidate_limit: 10 };
const reference = (id = "source1") => ({ id, kind: "youtube_url", status: "ready", rights_attested: true });
const job = (overrides = {}) => Object.assign({ id: "job1", source_id: "source1", provider: "gemini", status: "queued", retry_safety: "none", options: OPTIONS }, overrides);

function mockAPI(initial = {}) {
  const state = Object.assign({ config: READY, sources: [], jobs: [], results: {}, post: null }, initial);
  const handler = async (endpoint, options) => {
    if (endpoint === "/api/clipping/config") return state.config;
    if (endpoint === "/api/clipping/sources") return { sources: state.sources };
    if (endpoint === "/api/clipping/jobs") return { jobs: state.jobs };
    if (endpoint === "/api/clipping/gemini/jobs" && state.post) return state.post(options);
    const match = endpoint.match(/^\/api\/clipping\/jobs\/([^/]+)\/results$/);
    if (match) return state.results[match[1]] || { job_id: match[1], artifacts: [] };
    if (state.extra) return state.extra(endpoint, options);
    throw new Error("Unexpected request to " + endpoint);
  };
  return { state, ui: createUI(handler) };
}
function el(ui, id) { return ui.elements.get("clipping-" + id); }
function buttonIn(node, label) { return findNode(node, (item) => item.tagName === "button" && item.textContent === label); }
function fillLink(ui, value = "https://youtu.be/abcdefghijk") {
  el(ui, "link").value = value;
  el(ui, "link").emit("input");
  el(ui, "link-permission-check").checked = true;
  el(ui, "link-permission-check").emit("change");
}
function submit(ui) { el(ui, "link-form").emit("submit"); }
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("public YouTube parsing accepts one video and rejects ambiguous or unsafe links", () => {
  for (const url of ["https://www.youtube.com/watch?v=abcdefghijk", "youtu.be/abcdefghijk", "youtube.com/shorts/abcdefghijk", "abcdefghijk",
    "https://youtube.com:443/watch?v=abcdefghijk&t=12", "youtube.com:443/watch?v=abcdefghijk", "https://youtu.be/abcde%66ghijk?si=share"]) {
    assert.equal(pure.youtubeVideoID(url), "abcdefghijk");
  }
  for (const url of ["http://youtube.com/watch?v=abcdefghijk", "https://youtube.com/watch?v=abcdefghijk#fragment",
    "https://youtube.com/watch?v=abcdefghijk&list=PL123", "https://youtube.com/watch?v=abcdefghijk&%6cist=PL123",
    "https://youtube.com/watch?v=abcdefghijk&index=1", "https://youtube.com/playlist?list=PL123",
    "https://youtube.com/watch?v=abcdefghijk&v=mnopqrstuvw", "https://youtube.com/watch?v=abcdefghijk;t=12",
    "https://youtube.com:444/watch?v=abcdefghijk", "https://youtube.com:0443/watch?v=abcdefghijk", "https://youtube.com./watch?v=abcdefghijk",
    "https://attacker.example/watch?v=abcdefghijk", "https://user:pass@youtube.com/watch?v=abcdefghijk",
    "https://@youtube.com/watch?v=abcdefghijk", "https://youtube.com/watch?v=abcdefghijk&bad=%xx"]) {
    assert.equal(pure.youtubeVideoID(url), "");
  }
  assert.equal(pure.safeID("../source"), "");
});

test("link submission sends rights and exact Gemini options, never imports media, and focuses the saved job", async () => {
  const { state, ui } = mockAPI();
  const pending = deferred();
  state.post = () => pending.promise;
  await ui.api.start();
  assert.match(el(ui, "gemini-readiness").textContent, /not been tested/);
  fillLink(ui);
  assert.equal(el(ui, "link-submit").disabled, false);
  submit(ui);
  submit(ui);
  const calls = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.equal(calls.length, 1);
  assert.deepEqual(JSON.parse(calls[0].options.body), { youtube_url: "https://youtu.be/abcdefghijk", rights_attested: true, options: OPTIONS });
  assert.ok(calls[0].options.headers["Idempotency-Key"]);
  assert.equal(el(ui, "link").disabled, true);
  assert.match(el(ui, "workflow-status").textContent, /Saving/);
  state.sources = [reference()]; state.jobs = [job()];
  pending.resolve({ job: state.jobs[0] });
  await settle();
  assert.equal(el(ui, "link").value, "");
  assert.equal(el(ui, "link-permission-check").checked, false);
  assert.equal(ui.document.activeElement, el(ui, "analysis-heading"));
  assert.ok(!ui.requests.some((call) => /sources\/import|config\/worker/.test(call.endpoint)));
  assert.doesNotMatch(treeText(el(ui, "jobs")), /\d+%|download/i);
});

test("provider readiness and validation block submission with accessible correction", async () => {
  const { state, ui } = mockAPI({ config: { gemini: { ...READY.gemini, enabled: false } } });
  await ui.api.start();
  fillLink(ui);
  assert.equal(el(ui, "link-submit").disabled, true);
  assert.match(el(ui, "gemini-readiness").textContent, /disabled/);
  state.config = READY; await ui.api.refresh();
  el(ui, "min-clip-seconds").value = "50";
  el(ui, "max-clip-seconds").value = "20";
  submit(ui); await settle();
  assert.match(el(ui, "options-error").textContent, /longest clip/);
  assert.equal(el(ui, "max-clip-seconds").getAttribute("aria-invalid"), "true");
  assert.equal(ui.document.activeElement, el(ui, "max-clip-seconds"));
  assert.ok(!ui.requests.some((call) => call.endpoint === "/api/clipping/gemini/jobs"));
});

test("uncertain HTTP submission locks the video to the original body and key, including after changed options", async () => {
  const { state, ui } = mockAPI();
  let calls = 0;
  state.post = async () => { calls++; if (calls === 1) throw new Error("connection lost"); return { job: job() }; };
  await ui.api.start(); fillLink(ui); submit(ui); await settle();
  assert.match(el(ui, "link-error").textContent, /could not be confirmed/);
  assert.equal(el(ui, "link-submit").textContent, "Resend same request");
  el(ui, "min-clip-seconds").value = "45";
  el(ui, "link").value = "https://www.youtube.com/watch?v=abcdefghijk&t=30";
  el(ui, "link").emit("input");
  el(ui, "link-permission-check").checked = false;
  state.sources = [reference()]; state.jobs = [job()];
  submit(ui); await settle();
  const posts = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.equal(posts.length, 2);
  assert.equal(posts[0].options.body, posts[1].options.body);
  assert.equal(posts[0].options.headers["Idempotency-Key"], posts[1].options.headers["Idempotency-Key"]);
  assert.equal(el(ui, "link").value, "");
});

test("unsupported URL forms cannot be repaired into an accepted analysis", async () => {
  const { ui } = mockAPI();
  await ui.api.start();
  for (const url of ["http://youtube.com/watch?v=abcdefghijk", "https://youtube.com/watch?v=abcdefghijk#t=5",
    "https://youtube.com/watch?v=abcdefghijk&list=PL123", "https://youtube.com:444/watch?v=abcdefghijk", "https://youtube.com:0443/watch?v=abcdefghijk",
    "https://@youtube.com/watch?v=abcdefghijk", "https://youtube.com/watch?v=abcdefghijk;foo=bar"]) {
    fillLink(ui, url);
    assert.equal(el(ui, "link-submit").disabled, true, url);
    submit(ui); await settle();
    assert.equal(el(ui, "link").getAttribute("aria-invalid"), "true", url);
  }
  assert.ok(!ui.requests.some((call) => call.endpoint === "/api/clipping/gemini/jobs"));
});

test("a known terminal video starts a fresh request with its entered URL and a new key", async () => {
  const { state, ui } = mockAPI();
  state.post = async () => {
    if (!state.sources.length) {
      state.sources = [reference()]; state.jobs = [job()];
    } else {
      state.sources = [reference(), reference("source2")];
      state.jobs = [state.jobs[0], job({ id: "job2", source_id: "source2" })];
    }
    return { job: state.jobs.at(-1) };
  };
  await ui.api.start(); fillLink(ui); submit(ui); await settle();
  state.jobs = [job({ status: "failed", retry_safety: "terminal", error_code: "credentials_unavailable" })];
  await ui.api.refresh();
  fillLink(ui, "  https://youtube.com:443/watch?v=abcdefghijk&t=30  ");
  assert.equal(el(ui, "link-submit").disabled, false);
  submit(ui); await settle();
  const posts = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.equal(posts.length, 2);
  assert.deepEqual(JSON.parse(posts[1].options.body), { youtube_url: "https://youtube.com:443/watch?v=abcdefghijk&t=30", rights_attested: true, options: OPTIONS });
  assert.notEqual(posts[0].options.headers["Idempotency-Key"], posts[1].options.headers["Idempotency-Key"]);
});

test("a server error retains the lock even when a replay is rejected", async () => {
  const { state, ui } = mockAPI();
  let calls = 0;
  state.post = async () => { const error = new Error("unavailable"); error.status = ++calls === 1 ? 500 : 400; throw error; };
  await ui.api.start(); fillLink(ui); submit(ui); await settle(); submit(ui); await settle();
  assert.equal(el(ui, "link-submit").textContent, "Resend same request");
  assert.match(el(ui, "workflow-status").textContent, /original settings/);
  const posts = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.equal(posts[0].options.headers["Idempotency-Key"], posts[1].options.headers["Idempotency-Key"]);
});

test("an unresolved request blocks fresh submissions through another link or a saved source", async () => {
  const { state, ui } = mockAPI({ sources: [reference()] });
  state.post = async () => { throw new Error("connection lost"); };
  await ui.api.start(); fillLink(ui); submit(ui); await settle();
  assert.equal(buttonIn(el(ui, "sources"), "Resolve previous request").disabled, true);
  el(ui, "link").value = "https://youtu.be/lmnopqrstuv";
  el(ui, "link").emit("input");
  assert.equal(el(ui, "link-submit").disabled, true);
  submit(ui); await settle();
  assert.equal(ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs").length, 1);
  fillLink(ui);
  assert.equal(el(ui, "link-submit").textContent, "Resend same request");
});

test("known video mappings prevent fresh URL requests when the provider outcome becomes unknown", async () => {
  const { state, ui } = mockAPI();
  state.post = async () => { state.sources = [reference()]; state.jobs = [job()]; return { job: state.jobs[0] }; };
  await ui.api.start(); fillLink(ui); submit(ui); await settle();
  state.jobs = [job({ status: "failed", retry_safety: "outcome_unknown_no_replay", error_code: "outcome_unknown" })];
  await ui.api.refresh(); fillLink(ui);
  assert.equal(el(ui, "link-submit").disabled, true);
  assert.match(el(ui, "workflow-status").textContent, /unresolved/);
  submit(ui); await settle();
  assert.equal(ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs").length, 1);
  assert.equal(buttonIn(el(ui, "sources"), "Outcome unknown").disabled, true);
});

test("eligible saved sources use source_id; unresolved saves can only resend the same request", async () => {
  const { state, ui } = mockAPI({ sources: [reference(), { ...reference("no_rights"), rights_attested: false }] });
  let calls = 0;
  state.post = async () => { if (++calls === 1) throw new Error("lost"); state.jobs = [job()]; return { job: state.jobs[0] }; };
  await ui.api.start();
  const sourceRows = el(ui, "sources").children;
  assert.ok(buttonIn(sourceRows[0], "Analyze with Gemini"));
  assert.equal(buttonIn(sourceRows[1], "Analyze with Gemini"), null);
  buttonIn(sourceRows[0], "Analyze with Gemini").click(); await settle();
  assert.ok(buttonIn(el(ui, "sources"), "Resend same request"));
  el(ui, "candidate-limit").value = "3";
  buttonIn(el(ui, "sources"), "Resend same request").click(); await settle();
  const posts = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.deepEqual(JSON.parse(posts[0].options.body), { source_id: "source1", options: OPTIONS });
  assert.equal(posts[0].options.body, posts[1].options.body);
  assert.equal(posts[0].options.headers["Idempotency-Key"], posts[1].options.headers["Idempotency-Key"]);
});

test("known terminal outcomes allow a new source analysis while active and unknown outcomes stay blocked", async () => {
  const terminal = [
    job({ id: "failed", source_id: "failed", status: "failed", retry_safety: "terminal", error_code: "request_not_submitted" }),
    job({ id: "not-submitted", source_id: "not-submitted", status: "failed", retry_safety: "not_submitted_retryable", error_code: "request_not_submitted" }),
    job({ id: "canceled", source_id: "canceled", status: "canceled", retry_safety: "terminal" }),
    job({ id: "completed", source_id: "completed", status: "completed", retry_safety: "response_received_terminal" })
  ];
  const blocked = [
    job({ id: "active", source_id: "active", status: "queued", retry_safety: "not_submitted_retryable" }),
    job({ id: "unknown", source_id: "unknown", status: "failed", retry_safety: "outcome_unknown_no_replay" }),
    job({ id: "cancel-unknown", source_id: "cancel-unknown", status: "canceled", retry_safety: "outcome_unknown_no_replay" })
  ];
  const { state, ui } = mockAPI({ sources: [...terminal, ...blocked].map((item) => reference(item.source_id)), jobs: [...terminal, ...blocked] });
  state.post = async () => {
    state.jobs = [...state.jobs, job({ id: "new-job", source_id: "not-submitted", created_at: 1 })];
    return { job: state.jobs.at(-1) };
  };
  await ui.api.start();
  const rows = el(ui, "sources").children;
  for (const row of rows.slice(0, terminal.length)) {
    assert.equal(buttonIn(row, "Analyze again").disabled, false);
    assert.match(treeText(row), /may incur provider charges/);
  }
  assert.equal(buttonIn(rows[4], "Analysis retrying automatically").disabled, true);
  assert.equal(buttonIn(rows[5], "Outcome unknown").disabled, true);
  assert.equal(buttonIn(rows[6], "Outcome unknown").disabled, true);
  assert.match(treeText(el(ui, "jobs")), /This attempt was not submitted and has ended\./);
  buttonIn(rows[1], "Analyze again").click(); await settle();
  const posts = ui.requests.filter((call) => call.endpoint === "/api/clipping/gemini/jobs");
  assert.equal(posts.length, 1);
  assert.deepEqual(JSON.parse(posts[0].options.body), { source_id: "not-submitted", options: OPTIONS });
  assert.ok(posts[0].options.headers["Idempotency-Key"]);
  assert.ok(!ui.requests.some((call) => /\/retry(?:\/|$)/.test(call.endpoint)));
});

const v3 = {
  version: "framevault.clipping.analysis.v3", source_duration_ms: 61234, source_duration_provenance: "model_reported",
  summary: "A complete summary", language_codes: ["en", "ja"], limitations: ["Audio was unclear"], fewer_candidates_reason: "Only two strong moments",
  candidates: [
    { rank: 2, start_ms: 1123, end_ms: 20245, title_variants: ["Primary first", "Alternative second"], hook: "Proposed hook", rationale: "Why this", boundary_rationale: "Why these edges", context: "Required context", editorial_score: 78,
      scores: { hook: 11, emotional_impact: 22, retention_potential: 33, shareability: 44, standalone_context: 55 }, evidence: [{ kind: "visual", start_ms: 1201, end_ms: 2202, detail: "Visible gesture" }] },
    { rank: 1, start_ms: 25005, end_ms: 50009, title_variants: ["Second in returned order"], hook: "Another hook", rationale: "Another reason", boundary_rationale: "Another boundary", context: "Another context", editorial_score: 88, scores: { hook: 99 }, evidence: [] }
  ]
};

test("v3 results retain model order, exact time boundaries, semantic fields and estimate labels", async () => {
  const done = job({ status: "completed", retry_safety: "response_received_terminal" });
  const artifact = { schema_version: "framevault.clipping.analysis.v3", payload_json: JSON.stringify(v3) };
  const { ui } = mockAPI({ sources: [reference()], jobs: [done], results: { job1: { job_id: "job1", artifacts: [artifact] } } });
  await ui.api.start(); buttonIn(el(ui, "jobs"), "View saved analysis").click(); await settle();
  const text = treeText(el(ui, "jobs"));
  for (const expected of ["A complete summary", "en, ja", "61,234 ms", "00:01.123–00:20.245", "00:01.201–00:02.202", "Primary first", "Alternative second", "Proposed hook", "Why this", "Why these edges", "Required context", "78 / 100", "emotional_impact", "Visible gesture", "Only two strong moments", "Audio was unclear", "not independently measured", "not calibrated"]) assert.ok(text.includes(expected), expected);
  assert.ok(text.indexOf("#2 · Primary first") < text.indexOf("#1 · Second in returned order"));
  assert.doesNotMatch(text, /<script/i);
});

test("legacy results remain readable and a mismatched v3 envelope is not reinterpreted", async () => {
  const legacy = { schema_version: "v1", payload_json: JSON.stringify({ payload: { transcript: { segments: [{ start_ms: 1000, end_ms: 2400, text: "Original transcript" }] }, context: { summary: "Legacy context" }, candidates: [{ rank: 1, title: "Legacy clip", start_ms: 1000, end_ms: 20000 }] } }) };
  const mismatch = { schema_version: "framevault.clipping.analysis.v3", payload_json: JSON.stringify({ payload: v3 }) };
  const { ui } = mockAPI({ sources: [reference()], jobs: [job({ status: "completed" })], results: { job1: { job_id: "job1", artifacts: [legacy, mismatch] } } });
  await ui.api.start(); buttonIn(el(ui, "jobs"), "View saved analysis").click(); await settle();
  const text = treeText(el(ui, "jobs"));
  assert.match(text, /Original transcript/); assert.match(text, /Legacy context/); assert.match(text, /Legacy clip/);
  assert.match(text, /schema did not match/); assert.doesNotMatch(text, /Primary first/);
});

test("retry safety is automatic only, with no misleading Gemini percentages", async () => {
  const jobs = [
    job({ id: "a", status: "queued", retry_safety: "not_submitted_retryable" }),
    job({ id: "b", status: "queued", retry_safety: "rate_limited_wait", retry_at: 1800000000 }),
    job({ id: "c", status: "failed", retry_safety: "response_received_terminal" }),
    job({ id: "d", status: "failed", retry_safety: "outcome_unknown_no_replay" }),
    job({ id: "e", status: "failed", retry_safety: "terminal" })
  ];
  const { ui } = mockAPI({ sources: [reference()], jobs }); await ui.api.start();
  const text = treeText(el(ui, "jobs"));
  for (const expected of [/automatically/, /rate-limited/, /responded/, /Do not resubmit/, /terminal/]) assert.match(text, expected);
  assert.doesNotMatch(text, /\d+%/);
  assert.equal(findNodes(el(ui, "jobs"), (node) => node.tagName === "button" && /retry/i.test(node.textContent)).length, 0);
});

test("SSE is an invalidation only: stale payloads cannot delete, change status, or announce a failure", async () => {
  const { ui } = mockAPI({ sources: [reference()], jobs: [job()] }); await ui.api.start();
  ui.api.handleEvent("clipping_source", { data: JSON.stringify({ source: { id: "source1", status: "deleted" } }) });
  ui.api.handleEvent("clipping_job", { data: JSON.stringify({ job: { id: "job1", source_id: "source1", status: "failed", analysis_deleted: true } }) });
  await settle();
  assert.equal(el(ui, "source-count").textContent, "1"); assert.equal(el(ui, "job-count").textContent, "1");
  assert.match(treeText(el(ui, "jobs")), /Queued/); assert.equal(ui.toasts.length, 0);
});

test("superseded result responses and acknowledged deletion cannot resurrect a result", async () => {
  const first = deferred(); let resultCalls = 0;
  const { state, ui } = mockAPI({ sources: [reference()], jobs: [job({ status: "completed" })] });
  state.extra = async (endpoint, options) => {
    if (endpoint === "/api/clipping/jobs/job1" && options.method === "DELETE") { state.jobs = []; return {}; }
    throw new Error("unexpected " + endpoint);
  };
  const original = ui.api;
  // The default result handler is overridden with a thenable to control the first read.
  state.results.job1 = first.promise;
  await original.start(); buttonIn(el(ui, "jobs"), "View saved analysis").click(); await settle();
  buttonIn(el(ui, "jobs"), "Delete analysis").click();
  assert.equal(el(ui, "confirm-dialog").open, true);
  assert.equal(el(ui, "confirm-cancel").disabled, false);
  assert.equal(ui.document.activeElement, el(ui, "confirm-cancel"));
  assert.equal(el(ui, "confirm-dialog").getAttribute("aria-busy"), null);
  el(ui, "confirm-delete").click(); await settle();
  first.resolve({ job_id: "job1", artifacts: [{ schema_version: "v1", payload_json: JSON.stringify({ candidates: [{ title: "Stale result" }] }) }] });
  await settle();
  assert.equal(el(ui, "job-count").textContent, "0");
  assert.doesNotMatch(treeText(el(ui, "jobs")), /Stale result/);
});

test("cancel and delete use app dialogs, focus the safe action, and do not promise Google stopped", async () => {
  const { state, ui } = mockAPI({ sources: [reference()], jobs: [job({ status: "running" })] });
  state.extra = async (endpoint, options) => {
    if (endpoint === "/api/clipping/jobs/job1/cancel" && options.method === "POST") {
      state.jobs = [job({ status: "canceled", retry_safety: "outcome_unknown_no_replay" })]; return { job: state.jobs[0] };
    }
    if (endpoint === "/api/clipping/jobs/job1" && options.method === "DELETE") { state.jobs = []; return {}; }
    throw new Error("unexpected " + endpoint);
  };
  await ui.api.start();
  const cancel = buttonIn(el(ui, "jobs"), "Cancel analysis"); cancel.focus(); cancel.click();
  assert.equal(el(ui, "confirm-dialog").open, true);
  assert.equal(ui.document.activeElement, el(ui, "confirm-cancel"));
  assert.match(el(ui, "confirm-description").textContent, /does not confirm.*stopped/);
  el(ui, "confirm-delete").click(); await settle();
  assert.equal(el(ui, "confirm-dialog").open, false);
  assert.match(treeText(el(ui, "jobs")), /Outcome unknown/);
  assert.match(ui.toasts.at(-1).message, /may still be processing/);
  buttonIn(el(ui, "jobs"), "Delete analysis").click();
  assert.equal(el(ui, "confirm-dialog").open, true);
  el(ui, "confirm-delete").click(); await settle();
  assert.equal(el(ui, "job-count").textContent, "0");
  assert.equal(buttonIn(el(ui, "sources"), "Outcome unknown").disabled, true);
});

test("logout invalidates pending work and does not retain a URL or idempotency state", async () => {
  const pending = deferred(); const { state, ui } = mockAPI(); state.post = () => pending.promise;
  await ui.api.start(); fillLink(ui); submit(ui); ui.api.stop(true);
  pending.resolve({ job: job() }); await settle();
  assert.equal(el(ui, "link").value, ""); assert.equal(el(ui, "job-count").textContent, "0");
  assert.doesNotMatch(source, /localStorage|sessionStorage/);
  await ui.api.start(); fillLink(ui); assert.equal(el(ui, "link-submit").textContent, "Analyze video");
});

test("clipping markup keeps labeled controls, a native dialog and the authenticated same-origin integration", () => {
  assert.match(markup, /id="clipping-link-form"[^>]*novalidate/);
  assert.match(markup, /<dialog id="clipping-confirm-dialog"/);
  assert.match(dashboardSource, /credentials: "same-origin"/);
  assert.doesNotMatch(source, /(?:window|root)\.confirm\(/);
  assert.match(styles, /prefers-reduced-motion/);
});
