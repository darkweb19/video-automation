const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const pure = require('../internal/webui/static/clipping.js');

const staticDir = path.join(__dirname, '..', 'internal', 'webui', 'static');
const markup = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
const source = fs.readFileSync(path.join(staticDir, 'clipping.js'), 'utf8');
const dashboardSource = fs.readFileSync(path.join(staticDir, 'app.js'), 'utf8');

class Element {
  constructor(tag = 'div', id = '') {
    this.tagName = tag.toLowerCase();
    this.id = id;
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    this.style = {};
    this.dataset = {};
    this.value = '';
    this.textContent = '';
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.files = [];
    this.type = '';
    this.href = '';
    this.clicked = false;
    this.defaultValue = this.value;
  }
  append(...nodes) {
    for (const node of nodes) {
      if (node.tagName === '#fragment') {
        this.append(...node.children);
        node.children = [];
        continue;
      }
      node.parent = this;
      this.children.push(node);
    }
  }
  replaceChildren(...nodes) {
    this.children = [];
    this.append(...nodes);
  }
  addEventListener(name, callback) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(callback);
    this.listeners.set(name, listeners);
  }
  emit(name, event = {}) {
    const value = { preventDefault() {}, ...event };
    return (this.listeners.get(name) || []).map((callback) => callback(value));
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  querySelector(selector) {
    if (selector === '.field small') return null;
    return null;
  }
  querySelectorAll(selector) {
    const all = [];
    const visit = (node) => {
      for (const child of node.children) {
        if (selector === 'button' && child.tagName === 'button') all.push(child);
        visit(child);
      }
    };
    visit(this);
    return all;
  }
  closest(selector) {
    let node = this;
    const tag = selector.toLowerCase();
    while (node) {
      if (node.tagName === tag) return node;
      node = node.parent;
    }
    return null;
  }
  reset() { this.onReset?.(); }
  click() { this.clicked = true; this.emit('click'); }
  focus() { this.focused = true; }
}

function createUI(request) {
  const elements = new Map([...markup.matchAll(/<([a-z][a-z0-9-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)]
    .map((match) => {
      const node = new Element(match[1], match[3]);
      const value = match[2].match(/\bvalue="([^"]*)"/);
      if (value) node.value = value[1];
      return [match[3], node];
    }));
  const document = {
    getElementById: (id) => elements.get(id) || null,
    createElement: (tag) => new Element(tag),
    createDocumentFragment: () => new Element('#fragment')
  };
  const window = { document, location: { origin: 'http://localhost' }, confirm: () => true };
  elements.get('clipping-link-form').onReset = () => {
    elements.get('clipping-link').value = '';
    elements.get('clipping-link-permission-check').checked = false;
  };
  const context = vm.createContext({
    window,
    document,
    URL,
    AbortController,
    module: undefined
  });
  vm.runInContext(source, context, { filename: 'static/clipping.js' });
  window.ClippingUI.configure({ request });
  return { api: window.ClippingUI, elements };
}

function treeText(node) {
  return [node.textContent, ...node.children.flatMap(treeText)].filter(Boolean).join('\n');
}

function treeTags(node) {
  return [node.tagName, ...node.children.flatMap(treeTags)];
}

class StudioElement {
  constructor(tag, attributes = {}) {
    this.tagName = tag.toLowerCase();
    this.attributes = new Map(Object.entries(attributes));
    this.id = attributes.id || '';
    this.className = attributes.class || '';
    this.dataset = {};
    Object.entries(attributes).forEach(([key, value]) => {
      if (key.startsWith('data-')) this.dataset[key.slice(5).replace(/-([a-z])/g, (_, char) => char.toUpperCase())] = value;
    });
    this.listeners = new Map();
    this.textContent = '';
    this.value = attributes.value || '';
    this.hidden = Object.hasOwn(attributes, 'hidden');
    this.disabled = false;
    this.style = {};
    this.classList = {
      contains: (name) => this.className.split(/\s+/).filter(Boolean).includes(name),
      toggle: (name, enabled) => {
        const names = new Set(this.className.split(/\s+/).filter(Boolean));
        enabled ??= !names.has(name);
        if (enabled) names.add(name);
        else names.delete(name);
        this.className = [...names].join(' ');
        return enabled;
      },
      add: (name) => this.classList.toggle(name, true),
      remove: (name) => this.classList.toggle(name, false)
    };
  }
  addEventListener(name, callback) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(callback);
    this.listeners.set(name, listeners);
  }
  emit(name, event = {}) {
    return (this.listeners.get(name) || []).map((callback) => callback({ preventDefault() {}, ...event }));
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  removeAttribute(name) { this.attributes.delete(name); }
  toggleAttribute(name, enabled) { if (enabled) this.setAttribute(name, ''); else this.removeAttribute(name); }
  contains(node) { return node === this; }
  focus() { this.focused = true; }
}

function studioMarkupAttributes(fragment) {
  const attributes = {};
  for (const match of fragment.matchAll(/([a-zA-Z_:][a-zA-Z0-9_:.-]*)(?:="([^"]*)")?/g)) {
    attributes[match[1]] = match[2] ?? '';
  }
  return attributes;
}

function createDashboardNavigation() {
  const byID = new Map();
  for (const match of markup.matchAll(/<([a-z][a-z0-9-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)) {
    byID.set(match[3], new StudioElement(match[1], studioMarkupAttributes(match[2])));
  }
  const views = [...markup.matchAll(/<section\b([^>]*\bdata-page="([^"]+)"[^>]*)>/gi)]
    .map((match) => new StudioElement('section', studioMarkupAttributes(match[1])));
  const navItems = [...markup.matchAll(/<button\b([^>]*\bdata-view="([^"]+)"[^>]*)>/gi)]
    .map((match) => new StudioElement('button', studioMarkupAttributes(match[1])));
  const modeOptions = [...markup.matchAll(/<button\b([^>]*\bclass="[^"]*mode-option[^"]*"[^>]*)>/gi)]
    .map((match) => new StudioElement('button', studioMarkupAttributes(match[1])));
  const createButtons = [...markup.matchAll(/<button\b([^>]*\bdata-create-mode="([^"]+)"[^>]*)>/gi)]
    .map((match) => new StudioElement('button', studioMarkupAttributes(match[1])));
  const goButtons = [...markup.matchAll(/<button\b([^>]*\bdata-go="([^"]+)"[^>]*)>/gi)]
    .map((match) => new StudioElement('button', studioMarkupAttributes(match[1])));
  const sidebar = byID.get('workspace-navigation');
  const document = {
    title: '',
    activeElement: null,
    querySelector(selector) {
      if (selector.startsWith('#')) return byID.get(selector.slice(1)) || null;
      if (selector === '.sidebar') return sidebar;
      if (selector === '.view.active') return views.find((view) => view.classList.contains('active')) || null;
      if (selector.includes('.nav-item[')) return navItems.find((item) => item.getAttribute('aria-current') === 'page' && !item.disabled) || null;
      if (selector === '.nav-item:not(:disabled)') return navItems.find((item) => !item.disabled) || null;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '.view') return views;
      if (selector === '.nav-item') return navItems;
      if (selector === '.mode-option') return modeOptions;
      if (selector === '[data-create-mode]') return createButtons;
      if (selector === '[data-go]') return goButtons;
      return [];
    },
    createElement: (tag) => new StudioElement(tag),
    addEventListener() {}
  };
  class EventSourceStub {
    constructor(url, options) {
      this.url = url;
      this.options = options;
      this.readyState = 0;
      this.listeners = new Map();
      EventSourceStub.instances.push(this);
    }
    addEventListener(name, callback) {
      const callbacks = this.listeners.get(name) || [];
      callbacks.push(callback);
      this.listeners.set(name, callbacks);
    }
    emit(name, event = {}) {
      if (name === 'open') this.readyState = 1;
      (this.listeners.get(name) || []).forEach((callback) => callback(event));
    }
    close() { this.readyState = 2; }
  }
  EventSourceStub.instances = [];
  const clipping = {
    configuredRequest: null,
    refreshCount: 0,
    events: [],
    configure(options) { this.configuredRequest = options.request; },
    refresh() { this.refreshCount++; },
    handleEvent(kind, event) { this.events.push({ kind, data: event.data }); },
    stop() {},
    start() {}
  };
  const window = {
    document,
    ClippingUI: clipping,
    EventSource: EventSourceStub,
    location: { href: 'http://localhost/' },
    history: { replaceState() {} },
    navigator: { onLine: true },
    matchMedia: () => ({ matches: false }),
    requestAnimationFrame: (callback) => callback(),
    scrollTo() {},
    addEventListener() {},
    setTimeout: () => 1,
    clearTimeout() {}
  };
  const context = vm.createContext({
    document,
    window,
    URL,
    URLSearchParams,
    TextEncoder,
    Blob,
    AbortController,
    Event,
    fetch: () => Promise.reject(new Error('Unexpected fetch in navigation test'))
  });
  const instrumented = dashboardSource.replace('  bootstrap();', `
    refreshJobState = () => Promise.resolve();
    globalThis.__dashboard = { bindEvents, state, elements, startJobEventStream };
  `);
  assert.notEqual(instrumented, dashboardSource, 'dashboard test instrumentation must match');
  vm.runInContext(instrumented, context, { filename: 'static/app.js' });
  return { ...context.__dashboard, navItems, views, clipping, EventSourceStub, document };
}

test('budget values become exact integer microUSD and reject invalid decimals', () => {
  assert.equal(pure.parseUSDToMicroUSD('1'), 1_000_000);
  assert.equal(pure.parseUSDToMicroUSD('0.01'), 10_000);
  assert.equal(pure.parseUSDToMicroUSD('12.345678'), 12_345_678);
  assert.equal(pure.parseBudgetUSD('4.00', 4_000_000), 4_000_000);
  assert.equal(pure.parseBudgetUSD('4.01', 4_000_000), null);
  assert.equal(pure.parseUSDToMicroUSD('0'), null);
  assert.equal(pure.parseUSDToMicroUSD('1.0000001'), null);
  assert.equal(pure.parseUSDToMicroUSD('-1'), null);
  assert.equal(pure.parseUSDToMicroUSD('1e2'), null);
  assert.equal(pure.formatUSD(1_000_000), '$1.00');
});

test('resumable upload ranges honor the configured limit and never exceed 16 MiB', () => {
  const maxChunk = 16 * 1024 * 1024;
  assert.equal(pure.uploadChunkLimit({ max_upload_chunk_bytes: maxChunk * 2 }), maxChunk);
  assert.equal(pure.uploadChunkLimit({ max_upload_chunk_bytes: 1024 }), 1024);
  assert.deepEqual(pure.nextUploadRange(0, maxChunk + 1, { max_upload_chunk_bytes: maxChunk * 2 }), { start: 0, end: maxChunk });
  assert.deepEqual(pure.nextUploadRange(maxChunk, maxChunk + 1, { max_upload_chunk_bytes: maxChunk }), { start: maxChunk, end: maxChunk + 1 });
  assert.equal(pure.validResumeOffset(maxChunk, maxChunk + 1), maxChunk);
  assert.equal(pure.validResumeOffset(maxChunk + 2, maxChunk + 1), null);
});

test('source names and errors render as inert text', async () => {
  const maliciousName = '<img src=x onerror=alert(1)>';
  const maliciousError = '<script>alert(1)</script>';
  const app = createUI(async (route) => {
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [{ id: 'src_1', status: 'failed', original_name: maliciousName, error: maliciousError }];
    if (route === '/api/clipping/jobs') return [];
    if (route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const rendered = treeText(app.elements.get('clipping-sources'));
  assert.match(rendered, /<img src=x onerror=alert\(1\)>/);
  assert.match(rendered, /<script>alert\(1\)<\/script>/);
  assert.equal(treeTags(app.elements.get('clipping-sources')).includes('img'), false);
  assert.equal(treeTags(app.elements.get('clipping-sources')).includes('script'), false);
});

test('a late source-list response cannot repopulate the clipping view after logout', async () => {
  let resolveSources;
  let sourceSignal;
  const app = createUI((route, options = {}) => {
    if (route === '/api/clipping/config') return Promise.resolve({ worker_available: false });
    if (route === '/api/clipping/sources') return new Promise((resolve) => {
      resolveSources = resolve;
      sourceSignal = options.signal;
    });
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return Promise.resolve([]);
    throw new Error(`Unexpected request: ${route}`);
  });
  const pending = app.api.start();
  app.api.stop(true);
  assert.equal(sourceSignal.aborted, true, 'logout aborts the pending authenticated source request');
  resolveSources([{ id: 'src_late', status: 'ready', original_name: 'private video.mp4' }]);
  await pending;
  assert.doesNotMatch(treeText(app.elements.get('clipping-sources')), /private video/);
});

test('logout aborts an in-flight job submission without painting an old-session error', async () => {
  let rejectJob;
  let jobSignal;
  const app = createUI((route, options = {}) => {
    if (route === '/api/clipping/config') return Promise.resolve({ worker_available: false });
    if (route === '/api/clipping/sources') return Promise.resolve([{ id: 'src_ready', status: 'ready', original_name: 'source.mp4' }]);
    if (route === '/api/clipping/jobs' && options.method === 'POST') {
      jobSignal = options.signal;
      return new Promise((_resolve, reject) => { rejectJob = reject; });
    }
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return Promise.resolve([]);
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  app.elements.get('clipping-sources').querySelectorAll('button').find((node) => node.textContent === 'Start analysis').click();
  assert.ok(jobSignal);
  app.api.stop(true);
  assert.equal(jobSignal.aborted, true);
  const aborted = new Error('Request aborted');
  aborted.name = 'AbortError';
  rejectJob(aborted);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(app.elements.get('clipping-sources-error').textContent, '');
});

test('the authenticated dashboard opens Clipping from navigation and refreshes it on SSE reconnect/events', () => {
  const app = createDashboardNavigation();
  app.bindEvents();
  const clippingNav = app.navItems.find((item) => item.dataset.view === 'clipping');
  clippingNav.emit('click');
  assert.equal(app.views.find((view) => view.dataset.page === 'clipping').classList.contains('active'), true);
  assert.equal(clippingNav.getAttribute('aria-current'), 'page');
  assert.equal(app.elements.pageTitle.textContent, 'Clipping');
  assert.equal(app.clipping.refreshCount, 1);

  app.state.authenticated = true;
  app.state.mustChangePassword = true;
  app.startJobEventStream();
  assert.equal(app.EventSourceStub.instances.length, 0, 'the password gate prevents opening the authenticated event stream');
  app.state.mustChangePassword = false;
  app.startJobEventStream();
  assert.equal(app.EventSourceStub.instances.length, 1);
  const source = app.EventSourceStub.instances[0];
  source.emit('open');
  source.emit('clipping_source', { data: JSON.stringify({ id: 'clipsrc_1', source: { id: 'clipsrc_1' } }) });
  source.emit('clipping_job', { data: JSON.stringify({ id: 'clipjob_1', job: { id: 'clipjob_1' } }) });
  assert.equal(app.clipping.refreshCount, 2, 'reopening the stream refreshes all clipping records');
  assert.deepEqual(app.clipping.events.map((event) => event.kind), ['clipping_source', 'clipping_job']);
});

test('public imports require consent and send the permission attestation to the authenticated API', async () => {
  const requests = [];
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/sources/import') {
      if (JSON.parse(options.body).url.includes('youtube.com')) throw new Error('The selected source route is unavailable.');
      return { id: 'src_import', status: 'importing' };
    }
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const link = app.elements.get('clipping-link');
  const permission = app.elements.get('clipping-link-permission-check');
  const submit = app.elements.get('clipping-link-submit');
  link.value = 'https://drive.google.com/file/d/fixture/view';
  link.emit('input');
  assert.equal(submit.disabled, true);
  permission.checked = true;
  permission.emit('change');
  assert.equal(submit.disabled, false);
  await app.elements.get('clipping-link-form').emit('submit')[0];
  const request = requests.find((entry) => entry.route === '/api/clipping/sources/import');
  assert.ok(request);
  assert.deepEqual(JSON.parse(request.options.body), { url: 'https://drive.google.com/file/d/fixture/view', rights_attested: true });

  link.value = 'https://videos.example.com/clip.mp4';
  link.emit('input');
  permission.checked = true;
  permission.emit('change');
  await app.elements.get('clipping-link-form').emit('submit')[0];
  assert.match(app.elements.get('clipping-link-error').textContent, /Use a public Google Drive or Dropbox/);
  assert.equal(requests.filter((entry) => entry.route === '/api/clipping/sources/import').length, 1, 'unsupported direct links never reach the API');

  link.value = 'https://www.youtube.com/watch?v=example';
  link.emit('input');
  assert.equal(permission.checked, false, 'changing the link requires a fresh confirmation');
  assert.match(app.elements.get('clipping-link-permission-copy').textContent, /permission to reuse this YouTube footage/);
  assert.equal(app.elements.get('clipping-link-help').hidden, false);
  permission.checked = true;
  permission.emit('change');
  await app.elements.get('clipping-link-form').emit('submit')[0];
  assert.match(app.elements.get('clipping-link-error').textContent, /YouTube import is not available here yet/);
  assert.equal(JSON.parse(requests.at(-1).options.body).rights_attested, true);
});

test('individual and batch submissions send exact microUSD limits and stable idempotency keys', async () => {
  const submissions = [];
  let failFirstJob = true;
  const app = createUI(async (route, options = {}) => {
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [{ id: 'src_ready', status: 'ready', original_name: 'source.mp4', duration_ms: 12_000, media_url: '/api/clipping/sources/src_ready/media' }];
    if (route === '/api/clipping/jobs' && !options.method) return [];
    if (route === '/api/clipping/batches' && !options.method) return [];
    if (route === '/api/clipping/jobs' && options.method === 'POST') {
      submissions.push({ route, options });
      if (failFirstJob) {
        failFirstJob = false;
        throw new Error('Connection interrupted');
      }
      return { id: 'job_1', status: 'queued' };
    }
    if (route === '/api/clipping/batches' && options.method === 'POST') {
      submissions.push({ route, options });
      return { id: 'batch_1', status: 'queued' };
    }
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  assert.match(treeText(app.elements.get('clipping-sources')), /Media ready/);
  const sourceButtons = app.elements.get('clipping-sources').querySelectorAll('button');
  const startButton = sourceButtons.find((node) => node.textContent === 'Start analysis');
  app.elements.get('clipping-job-budget').value = '1.25';
  startButton.click();
  for (let attempt = 0; attempt < 30 && submissions.length < 1; attempt++) await new Promise((resolve) => setImmediate(resolve));
  for (let attempt = 0; attempt < 30 && !app.elements.get('clipping-sources-error').textContent; attempt++) await new Promise((resolve) => setImmediate(resolve));
  startButton.click();
  for (let attempt = 0; attempt < 30 && submissions.length < 2; attempt++) await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(JSON.parse(submissions[0].options.body), { source_id: 'src_ready', budget_micro_usd: 1_250_000 });
  assert.equal(submissions[0].options.headers['Idempotency-Key'], submissions[1].options.headers['Idempotency-Key']);

  const batchCheck = app.elements.get('clipping-sources').children[0].children[1].children[0].children[0];
  batchCheck.checked = true;
  batchCheck.emit('change');
  app.elements.get('clipping-batch-job-budget').value = '1.25';
  app.elements.get('clipping-batch-budget').value = '1.24';
  app.elements.get('clipping-batch-budget').emit('input');
  assert.equal(app.elements.get('clipping-create-batch').disabled, true);
  app.elements.get('clipping-batch-budget').value = '1.25';
  app.elements.get('clipping-batch-budget').emit('input');
  assert.equal(app.elements.get('clipping-create-batch').disabled, false);
  app.elements.get('clipping-create-batch').click();
  for (let attempt = 0; attempt < 30 && submissions.length < 3; attempt++) await new Promise((resolve) => setImmediate(resolve));
  const batch = submissions[2];
  assert.equal(batch.route, '/api/clipping/batches');
  assert.deepEqual(JSON.parse(batch.options.body), {
    source_ids: ['src_ready'], per_job_budget_micro_usd: 1_250_000, budget_micro_usd: 1_250_000
  });
  assert.ok(batch.options.headers['Idempotency-Key']);
});

test('source removal, job and batch cancellation, and retention settings use their authenticated routes', async () => {
  const requests = [];
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30, retention_configurable: true };
    if (route === '/api/clipping/config/retention' && options.method === 'PUT') return { retention_days: JSON.parse(options.body).retention_days };
    if (route === '/api/clipping/sources') return [{ id: 'src_ready', status: 'ready', original_name: 'source.mp4', duration_ms: 12_000 }];
    if (route === '/api/clipping/sources/src_ready' && options.method === 'DELETE') return null;
    if (route === '/api/clipping/jobs' && !options.method) return [{ id: 'job_live', source_id: 'src_ready', status: 'running', progress: 0.25, budget_micro_usd: 1_000_000, spent_micro_usd: 250_000 }];
    if (route === '/api/clipping/jobs/job_live/cancel') return { id: 'job_live', status: 'canceled' };
    if (route === '/api/clipping/batches' && !options.method) return [{ id: 'batch_live', source_ids: ['src_ready'], status: 'running', progress: 0.25, budget_micro_usd: 1_000_000, spent_micro_usd: 250_000 }];
    if (route === '/api/clipping/batches/batch_live/cancel') return { id: 'batch_live', status: 'canceled' };
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  app.elements.get('clipping-retention-days').value = '60';
  await app.elements.get('clipping-retention-form').emit('submit')[0];
  const retention = requests.find((entry) => entry.route === '/api/clipping/config/retention');
  assert.equal(retention.options.method, 'PUT');
  assert.deepEqual(JSON.parse(retention.options.body), { retention_days: 60 });

  const remove = app.elements.get('clipping-sources').querySelectorAll('button').find((node) => node.textContent === 'Remove');
  remove.click();
  const cancelJob = app.elements.get('clipping-jobs').querySelectorAll('button').find((node) => node.textContent === 'Cancel job');
  cancelJob.click();
  const cancelBatch = app.elements.get('clipping-batches').querySelectorAll('button').find((node) => node.textContent === 'Cancel batch');
  cancelBatch.click();
  for (let attempt = 0; attempt < 30 && requests.filter((entry) => entry.route.endsWith('/cancel')).length < 2; attempt++) await new Promise((resolve) => setImmediate(resolve));
  assert.ok(requests.some((entry) => entry.route === '/api/clipping/sources/src_ready' && entry.options.method === 'DELETE'));
  assert.ok(requests.some((entry) => entry.route === '/api/clipping/jobs/job_live/cancel'));
  assert.ok(requests.some((entry) => entry.route === '/api/clipping/batches/batch_live/cancel'));
});

test('paused jobs use neutral copy and identify an unresolved attempt without hiding the saved reason', async () => {
  const app = createUI(async (route) => {
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs') return [{
      id: 'job_paused', source_id: 'src_missing', status: 'paused_budget', budget_micro_usd: 1_000_000,
      spent_micro_usd: 100_000, error: 'Dispatch outcome is unknown; budget remains reserved.',
      stages: [{ name: 'analysis', status: 'uncertain' }]
    }, {
      id: 'job_budget', source_id: 'src_other', status: 'paused_budget', budget_micro_usd: 1_000_000,
      spent_micro_usd: 1_000_000, error: 'Budget limit reached', stages: [{ name: 'analysis', status: 'paused_budget' }]
    }];
    if (route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const rendered = treeText(app.elements.get('clipping-jobs'));
  assert.match(rendered, /Waiting for the previous attempt to be resolved/);
  assert.match(rendered, /The previous attempt has not reported its result yet/);
  assert.match(rendered, /Analysis paused/);
  assert.match(rendered, /job paused after reaching its budget limit/);
  assert.doesNotMatch(rendered, /Dispatch outcome/);
});

test('interrupted uploads resume from the saved offset and send chunks within the limit', async () => {
  const chunkBytes = 16 * 1024 * 1024;
  const fileSize = chunkBytes + 1;
  let savedOffset = 0;
  let failSecondChunk = true;
  let uploadInit;
  const chunks = [];
  let releaseFinalize;
  const finalized = new Promise((resolve) => { releaseFinalize = resolve; });
  const app = createUI(async (route, options = {}) => {
    if (route === '/api/clipping/config') return { worker_available: false, max_upload_chunk_bytes: chunkBytes * 2, max_source_bytes: fileSize + 1 };
    if (route === '/api/clipping/sources') return [{ id: 'src_upload', status: 'uploading', original_name: 'long.mp4', declared_size_bytes: fileSize, upload_offset: savedOffset }];
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/sources/upload') {
      uploadInit = JSON.parse(options.body);
      return { id: 'src_upload', status: 'uploading', original_name: 'long.mp4', declared_size_bytes: fileSize, upload_offset: 0 };
    }
    if (route === '/api/clipping/sources/src_upload/upload') {
      const size = options.body.size;
      const offset = Number(options.headers['Upload-Offset']);
      chunks.push({ size, offset });
      if (failSecondChunk && offset === chunkBytes) {
        failSecondChunk = false;
        throw new Error('Connection interrupted');
      }
      savedOffset = offset + size;
      return { upload_offset: savedOffset };
    }
    if (route === '/api/clipping/sources/src_upload/finalize') {
      releaseFinalize();
      return { id: 'src_upload', status: 'ready' };
    }
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();

  const file = {
    name: 'long.mp4',
    size: fileSize,
    type: 'video/mp4',
    slice(start, end) {
      assert.ok(end - start <= chunkBytes, 'no chunk exceeds 16 MiB');
      return { size: end - start };
    }
  };
  const uploadFile = app.elements.get('clipping-file');
  assert.equal(treeText(app.elements.get('clipping-upload-form')).includes('Matching name and size do not verify its contents.'), false, 'the normal upload form has no resume warning');
  uploadFile.files = [file];
  app.elements.get('clipping-upload-permission').checked = true;
  app.elements.get('clipping-upload-form').emit('submit');
  for (let attempt = 0; attempt < 50 && !app.elements.get('clipping-upload-error').textContent; attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.match(app.elements.get('clipping-upload-error').textContent, /Connection interrupted/);
  assert.equal(uploadInit.rights_attested, true);
  assert.equal(savedOffset, chunkBytes);
  for (let attempt = 0; attempt < 20 && !app.elements.get('clipping-sources').querySelectorAll('button').some((node) => node.textContent === 'Resume upload'); attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }

  const resumeButton = app.elements.get('clipping-sources').querySelectorAll('button').find((node) => node.textContent === 'Resume upload');
  assert.ok(resumeButton, 'an interrupted source has a resume control');
  resumeButton.click();
  const resumeHelp = app.elements.get('clipping-upload-form').children.find((node) => node.textContent.includes('Matching name and size do not verify its contents.'));
  assert.ok(resumeHelp && !resumeHelp.hidden, 'choosing Resume explains that the exact original file is needed');
  const resumeFile = app.elements.get('clipping-resume-file');
  assert.equal(resumeFile.clicked, true);
  resumeFile.files = [file];
  resumeFile.emit('change');
  await finalized;
  for (let attempt = 0; attempt < 20 && !resumeHelp.hidden; attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(resumeHelp.hidden, true, 'the resume-only note hides when the upload completes');
  for (let attempt = 0; attempt < 20 && app.elements.get('clipping-upload-progress').hidden; attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.deepEqual(chunks.map((chunk) => chunk.size), [chunkBytes, 1, 1]);
  assert.deepEqual(chunks.map((chunk) => chunk.offset), [0, chunkBytes, chunkBytes]);
});
