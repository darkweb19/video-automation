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
    TextEncoder,
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

function findNode(node, predicate) {
  if (predicate(node)) return node;
  for (const child of node.children) {
    const found = findNode(child, predicate);
    if (found) return found;
  }
  return null;
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

test('failed YouTube sources turn durable reason codes into actionable original-file fallbacks', async () => {
  const failures = [
    'youtube_import_unavailable',
    'youtube_video_unavailable',
    'youtube_format_unavailable',
    'youtube_duration_limit_exceeded',
    'youtube_size_limit_exceeded',
    'youtube_media_invalid'
  ];
  const recoveryFailure = 'Import attempt limit reached';
  const app = createUI(async (route) => {
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [...failures, recoveryFailure].map((error, index) => ({
      id: `src_youtube_failed_${index}`,
      kind: 'youtube_original_file',
      status: 'failed',
      original_name: 'YouTube video',
      error
    }));
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const rendered = treeText(app.elements.get('clipping-sources'));
  assert.match(rendered, /This YouTube import could not be completed by the available public importer/);
  assert.match(rendered, /This YouTube video could not be fetched through its public link; it may require sign-in or be restricted/);
  assert.match(rendered, /No supported audio and video format is available/);
  assert.match(rendered, /exceeds the four-hour limit/);
  assert.match(rendered, /exceeds the 20 GiB limit/);
  assert.match(rendered, /This source is not a supported video/);
  assert.match(rendered, /This public YouTube import could not be completed/);
  assert.equal((rendered.match(/Upload an original video file you have permission to reuse/g) || []).length, failures.length + 1);
  assert.doesNotMatch(rendered, /Import attempt limit reached/);
  for (const failure of failures) assert.doesNotMatch(rendered, new RegExp(failure));
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

test('Drive and Dropbox public imports require consent and send the permission attestation to the authenticated API', async () => {
  const requests = [];
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/sources/import') {
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
});

test('YouTube imports accept supported single-video links, require rights attestation, and show safe backend fallbacks', async () => {
  const requests = [];
  const fallback = 'This public YouTube video is unavailable without sign-in. Upload an original file you have permission to reuse.';
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/sources/import') {
      const payload = JSON.parse(options.body);
      if (payload.url.includes('/watch?')) throw new Error(fallback);
      return { source: { id: 'src_youtube', status: 'importing', kind: 'youtube_original_file', rights_attested: true } };
    }
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const link = app.elements.get('clipping-link');
  const permission = app.elements.get('clipping-link-permission-check');
  const submit = app.elements.get('clipping-link-submit');
  const supportedLinks = [
    'https://www.youtube.com/watch?v=dQw4w9WgXcQ&si=tracking',
    'https://m.youtube.com/shorts/abcdefghijk',
    'https://www.youtube-nocookie.com/embed/abcdefghijk',
    'https://youtu.be/abcdefghijk?feature=share'
  ];

  link.value = supportedLinks[0];
  link.emit('input');
  assert.equal(permission.checked, false, 'changing a YouTube URL requires a fresh permission confirmation');
  assert.match(app.elements.get('clipping-link-permission-copy').textContent, /permission to reuse this YouTube footage/);
  assert.equal(app.elements.get('clipping-link-help').hidden, false);
  assert.match(app.elements.get('clipping-link-help').textContent, /one HTTPS watch, shorts, embed, or youtu\.be video/);
  assert.match(app.elements.get('clipping-link-help').textContent, /without sign-in/);
  assert.match(app.elements.get('clipping-link-help').textContent, /original-file upload/);
  await app.elements.get('clipping-link-form').emit('submit')[0];
  assert.match(app.elements.get('clipping-link-error').textContent, /Confirm that you have permission/);
  assert.equal(requests.filter((entry) => entry.route === '/api/clipping/sources/import').length, 0, 'an unattested YouTube import never reaches the API');
  permission.checked = true;
  permission.emit('change');
  await app.elements.get('clipping-link-form').emit('submit')[0];
  assert.equal(app.elements.get('clipping-link-error').textContent, fallback, 'the UI preserves the API’s sanitized upload fallback');

  for (const supported of supportedLinks.slice(1)) {
    link.value = supported;
    link.emit('input');
    assert.equal(permission.checked, false, 'changing a YouTube URL requires a fresh permission confirmation');
    assert.match(app.elements.get('clipping-link-help').textContent, /restricted videos, and unsupported formats require an original-file upload/);
    permission.checked = true;
    permission.emit('change');
    await app.elements.get('clipping-link-form').emit('submit')[0];
  }

  const imports = requests.filter((entry) => entry.route === '/api/clipping/sources/import');
  assert.equal(imports.length, supportedLinks.length);
  for (let index = 0; index < imports.length; index++) {
    assert.deepEqual(JSON.parse(imports[index].options.body), { url: supportedLinks[index], rights_attested: true });
  }

  const invalidLinks = [
    'https://www.youtube.com/playlist?list=PL1234567890',
    'https://www.youtube.com/watch?v=abcdefghijk&PlAyLiSt=PL1234567890',
    'https://www.youtube.com/watch?v=abcdefghijk&playlist=PL1234567890',
    'https://www.youtube.com/watch?v=abcdefghijk&playlist_id=PL1234567890',
    'https://www.youtube.com/watch?v=abcdefghijk&video_ids=abcdefghijk,12345678901',
    'https://www.youtube.com/watch?v=abcdefghijk&start_radio=1',
    'https://www.youtube.com/watch?v=abcdefghijk&INDEX=2',
    'https://www.youtube.com/live/abcdefghijk',
    'https://youtu.be/abcdefghijk/extra'
  ];
  for (const invalid of invalidLinks) {
    link.value = invalid;
    link.emit('input');
    permission.checked = true;
    permission.emit('change');
    await app.elements.get('clipping-link-form').emit('submit')[0];
    assert.match(app.elements.get('clipping-link-error').textContent, /one HTTPS public YouTube|Playlists and live/);
  }
  assert.equal(requests.filter((entry) => entry.route === '/api/clipping/sources/import').length, supportedLinks.length, 'playlist, live, and malformed YouTube links never reach the API');
  assert.match(markup, /Public YouTube import supports one HTTPS watch, shorts, embed, or youtu\.be video/);
  assert.match(markup, /restricted videos, and unsupported formats require an original-file upload/);
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
  const sourceType = findNode(app.elements.get('clipping-sources'), (node) => node.tagName === 'select');
  sourceType.value = 'podcast';
  sourceType.emit('change');
  app.elements.get('clipping-job-budget').value = '1.25';
  app.elements.get('clipping-job-min-seconds').value = '20';
  app.elements.get('clipping-job-max-seconds').value = '90';
  app.elements.get('clipping-job-candidate-limit').value = '8';
  startButton.click();
  for (let attempt = 0; attempt < 30 && submissions.length < 1; attempt++) await new Promise((resolve) => setImmediate(resolve));
  for (let attempt = 0; attempt < 30 && !app.elements.get('clipping-sources-error').textContent; attempt++) await new Promise((resolve) => setImmediate(resolve));
  startButton.click();
  for (let attempt = 0; attempt < 30 && submissions.length < 2; attempt++) await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(JSON.parse(submissions[0].options.body), {
    source_id: 'src_ready', budget_micro_usd: 1_250_000, content_type: 'podcast',
    min_clip_seconds: 20, max_clip_seconds: 90, candidate_limit: 8
  });
  assert.equal(submissions[0].options.headers['Idempotency-Key'], submissions[1].options.headers['Idempotency-Key']);

  const batchCheck = app.elements.get('clipping-sources').children[0].children[1].children[0].children[0];
  batchCheck.checked = true;
  batchCheck.emit('change');
  app.elements.get('clipping-batch-job-budget').value = '1.25';
  app.elements.get('clipping-batch-budget').value = '1.24';
  app.elements.get('clipping-batch-content-type').value = 'gaming';
  app.elements.get('clipping-batch-min-seconds').value = '30';
  app.elements.get('clipping-batch-max-seconds').value = '120';
  app.elements.get('clipping-batch-candidate-limit').value = '9';
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
    source_ids: ['src_ready'], per_job_budget_micro_usd: 1_250_000, budget_micro_usd: 1_250_000,
    content_type: 'gaming', min_clip_seconds: 30, max_clip_seconds: 120, candidate_limit: 9
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

test('Modal worker settings require a token at setup and display only secret-free, config-only status', async () => {
  const requests = [];
  const secret = 'never-render-this-bearer-token';
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false, default_retention_days: 30 };
    if (route === '/api/clipping/config/worker' && !options.method) {
      return {
        configured: true,
        enabled: false,
        endpoint_host: 'clip-worker.modal.run',
        provider: 'openai-compatible',
        model: 'analysis-model-v2',
        pipeline_revision: 'm3-2026-10-09-v1',
        rate_micro_usd_per_second: 3,
        readiness: 'configured_not_live_checked',
        capabilities: {
          full_timeline_asr: true,
          original_script: true,
          audio_events: true,
          scene_motion_events: false,
          candidate_selection: true
        },
        billing_basis: 'operator_estimate_per_compute_second',
        bearer_token: secret
      };
    }
    if (route === '/api/clipping/config/worker' && options.method === 'PUT') return { configured: true, enabled: JSON.parse(options.body).enabled };
    if (route === '/api/clipping/sources' || route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const status = treeText(app.elements.get('clipping-worker-status')) + treeText(app.elements.get('clipping-worker-metadata'));
  assert.match(status, /configured, not live checked/);
  assert.match(status, /clip-worker\.modal\.run/);
  assert.match(status, /analysis-model-v2/);
  assert.match(status, /m3-2026-10-09-v1/);
  assert.match(status, /Full-timeline ASR/);
  assert.match(status, /Original-script transcript/);
  assert.match(status, /Candidate selection/);
  assert.doesNotMatch(status, /Scene and motion events/, 'disabled capabilities are not advertised as enabled');
  assert.doesNotMatch(status, new RegExp(secret));
  assert.equal(app.elements.get('clipping-worker-token').value, '', 'a secret returned by a broken server is never copied into the password field');
  assert.equal(app.elements.get('clipping-worker-enabled').checked, false, 'a configured worker stays disabled by default');
  assert.equal(app.elements.get('clipping-callback-origin').textContent, 'http://localhost', 'Modal callback origin matches the current dashboard origin');

  const endpoint = app.elements.get('clipping-worker-endpoint');
  const token = app.elements.get('clipping-worker-token');
  const revision = app.elements.get('clipping-worker-pipeline-revision');
  const rate = app.elements.get('clipping-worker-rate');
  endpoint.value = 'https://clip-worker.modal.run/dispatch';
  assert.equal(revision.value, 'm3-2026-10-09-v1');
  rate.value = '0.000004';
  app.elements.get('clipping-worker-enabled').checked = true;
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  const update = requests.find((request) => request.route === '/api/clipping/config/worker' && request.options.method === 'PUT');
  assert.ok(update, 'worker settings use the authenticated same-origin configuration route');
  assert.deepEqual(JSON.parse(update.options.body), {
    enabled: true,
    endpoint: 'https://clip-worker.modal.run/dispatch',
    bearer_token: '',
    rate_micro_usd_per_second: 4,
    pipeline_revision: 'm3-2026-10-09-v1'
  }, 'a blank bearer preserves an existing configured secret');
  assert.equal(token.value, '');
  assert.ok(requests.every((request) => request.route.startsWith('/api/')), 'the browser never calls the Modal endpoint directly');
  assert.match(markup, /FRAMEVAULT_CLIPPING_WORKER_SHARED_SECRET=-/);
  assert.doesNotMatch(markup, /FRAMEVAULT_CLIPPING_WORKER_SHARED_SECRET=.*your-random-worker-token/);
  assert.match(markup, /three separate least-privilege Modal secrets/);
  assert.match(markup, /runtime and model-prewarm secrets omit the dispatch bearer/);
  assert.match(markup, /FRAMEVAULT_CALLBACK_ORIGIN/);
  assert.match(markup, /FRAMEVAULT_PIPELINE_REVISION/);
  assert.match(markup, /FRAMEVAULT_WHISPER_MODEL_REVISION/);
  assert.match(markup, /immutable 40-character lowercase Hugging Face commit/);
  assert.match(markup, /Change the pipeline revision whenever the model commit, worker, runtime, or analysis algorithm changes/);
  assert.match(markup, /Must exactly match/);
  assert.match(markup, /worker rejects media or callback transfer URLs outside it/);
  assert.match(markup, /worker rejects media or callback transfer URLs outside it/);
  assert.match(markup, /modal run workers\/modal\/clipping_m3_worker\.py::app\.clipping_model_prewarm_task/);
  assert.match(markup, /worker URL ending in/);
  assert.match(markup, /provider charges/);
});

test('initial Modal worker setup requires a bearer and keeps the worker disabled by default', async () => {
  const writes = [];
  const app = createUI(async (route, options = {}) => {
    if (route === '/api/clipping/config') return { worker_available: false };
    if (route === '/api/clipping/config/worker' && !options.method) return { configured: false, enabled: false, readiness: 'not_configured' };
    if (route === '/api/clipping/config/worker' && options.method === 'PUT') {
      writes.push(JSON.parse(options.body));
      return { configured: true, enabled: false };
    }
    if (route === '/api/clipping/sources' || route === '/api/clipping/jobs' || route === '/api/clipping/batches') return [];
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  assert.equal(app.elements.get('clipping-worker-enabled').checked, false);
  app.elements.get('clipping-worker-endpoint').value = 'https://clip-worker.modal.run/dispatch';
  app.elements.get('clipping-worker-rate').value = '0.000001';
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 0, 'initial setup cannot be saved without a pipeline revision');
  assert.match(app.elements.get('clipping-worker-error').textContent, /pipeline revision/);
  app.elements.get('clipping-worker-pipeline-revision').value = 'm3 test revision';
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 0, 'pipeline revisions reject characters outside the configured identifier alphabet');
  assert.match(app.elements.get('clipping-worker-error').textContent, /1–64 letters/);
  app.elements.get('clipping-worker-pipeline-revision').value = 'r'.repeat(65);
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 0, 'pipeline revisions longer than 64 characters are rejected');
  app.elements.get('clipping-worker-pipeline-revision').value = 'm3-test-revision';
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 0, 'initial setup still requires a bearer token');
  assert.match(app.elements.get('clipping-worker-error').textContent, /required for initial setup/);
  app.elements.get('clipping-worker-token').value = 'too-short';
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 0, 'a short bearer token is rejected before POST');
  assert.match(app.elements.get('clipping-worker-error').textContent, /exactly 64 lowercase hexadecimal characters/);
  app.elements.get('clipping-worker-token').value = 'a'.repeat(64);
  await app.elements.get('clipping-worker-form').emit('submit')[0];
  assert.equal(writes.length, 1);
  assert.equal(writes[0].enabled, false);
  assert.equal(writes[0].bearer_token, 'a'.repeat(64));
  assert.equal(writes[0].pipeline_revision, 'm3-test-revision');
  assert.equal(app.elements.get('clipping-worker-token').value, '', 'the token field clears after saving');
});

test('saved analysis artifacts render original timestamps, events, context, candidates, evidence, and versions as text', async () => {
  const requests = [];
  const candidate = {
    id: 'candidate_1', rank: 1, start_ms: 1_250, end_ms: 8_500, duration_ms: 7_250,
    title_variants: ['Alternate title', '<script>title</script>'], hook: 'A surprising reveal',
    editorial_score: 19,
    scores: { hook: 5, emotional_impact: 4, retention_potential: 4, shareability: 3, standalone_context: 3 },
    confidence: 0.62, confidence_label: 'medium',
    evidence: [{ kind: 'spoken_line', start_ms: 2_000, end_ms: 3_000, text: '<svg onload=alert(1)>', detail: 'A source-backed quote' }]
  };
  const savedJob = {
    id: 'clipjob_1', source_id: 'clipsrc_1', status: 'completed', content_type: 'podcast',
    min_clip_seconds: 15, max_clip_seconds: 60, candidate_limit: 7,
    artifacts: [{
      type: 'clipping.analysis', schema_version: '2', version: 1,
      source_duration_ms: 60_000, time_ranges: [{ start_ms: 1_250, end_ms: 8_500 }],
      payload_json: JSON.stringify({
        analysis_version: 'analysis-v2', content_type: 'podcast',
        model_versions: { asr: 'transcriber-v3' }, algorithm_versions: { selection: 'ranker-v2' },
        prompt_versions: { context: 'context-prompt-v5' },
        language: { code: 'hi', probability: 0.91, script: 'Devanagari' },
        cost_basis: 'operator_rate', cost_estimate_micro_usd: 2_000,
        rate_micro_usd_per_second: 4, compute_seconds: 500,
        transcript: { original_script: true, segments: [{ start_ms: 1_250, end_ms: 3_500, text: '<img src=x onerror=alert(1)>', confidence: 0.71, speaker_id: 'speaker_1' }] },
        context: {
          summary: '<script>global story</script>',
          topics: [{ start_ms: 2_000, end_ms: 5_000, label: 'The reveal', keywords: ['reveal', 'reaction'] }],
          timeline: [{ start_ms: 5_000, end_ms: 8_500, summary: 'The point resolves', keywords: ['payoff'] }]
        },
        audio_events: [{ start_ms: 3_000, end_ms: 4_000, kind: 'laughter', score: 0.8, evidence: 'Audience laughter' }],
        visual_events: [{ start_ms: 4_000, end_ms: 6_000, kind: 'scene_change', score: 0.7, evidence: 'Camera cuts to host' }],
        candidates: [candidate]
      })
    }]
  };
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false };
    if (route === '/api/clipping/config/worker') return { configured: false, enabled: false };
    if (route === '/api/clipping/sources') return [{ id: 'clipsrc_1', status: 'ready', original_name: 'episode.mp4' }];
    if (route === '/api/clipping/jobs' && !options.method) return [savedJob];
    if (route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/jobs/clipjob_1' && !options.method) return { job: savedJob };
    if (route === '/api/clipping/jobs/clipjob_1/selection' && options.method === 'POST') return { job: savedJob };
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const view = app.elements.get('clipping-jobs').querySelectorAll('button').find((node) => node.textContent === 'View saved analysis');
  assert.ok(view);
  view.click();
  for (let attempt = 0; attempt < 30 && !treeText(app.elements.get('clipping-jobs')).includes('Original-script transcript'); attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  const rendered = treeText(app.elements.get('clipping-jobs'));
  for (const expected of [
    'Original-script transcript', '00:01.250–00:03.500', '<img src=x onerror=alert(1)>',
    'Global context', '<script>global story</script>', 'Audio events', 'Audience laughter',
    'Visual events', 'Camera cuts to host', 'Selected clip candidates', 'A surprising reveal',
    'Alternate title', '<script>title</script>', 'Score components', 'hook', 'retention_potential',
    'Confidence 0.62 (medium) · uncalibrated', '<svg onload=alert(1)>',
    'transcriber-v3', 'context-prompt-v5', 'analysis-v2', 'Worker cost estimate', 'not a provider quote'
  ]) assert.ok(rendered.includes(expected), `saved analysis should render ${expected}`);
  const tags = treeTags(app.elements.get('clipping-jobs'));
  assert.equal(tags.includes('img'), false, 'stored transcript text is inert');
  assert.equal(tags.includes('script'), false, 'stored context and title text are inert');
  assert.equal(tags.includes('svg'), false, 'stored evidence text is inert');
  const detailsRequest = requests.find((request) => request.route === '/api/clipping/jobs/clipjob_1');
  assert.ok(detailsRequest, 'details use a safe same-origin job ID route');

  let selectionForm = findNode(app.elements.get('clipping-jobs'), (node) => node.className === 'clipping-selection-form');
  for (let attempt = 0; attempt < 30 && !selectionForm; attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
    selectionForm = findNode(app.elements.get('clipping-jobs'), (node) => node.className === 'clipping-selection-form');
  }
  assert.ok(selectionForm, 'completed jobs expose saved selection controls after loading their detail');
  const selectionInputs = selectionForm.children.filter((node) => node.tagName === 'label').map((label) => label.children[0]);
  selectionInputs[0].value = 'comedy';
  selectionInputs[1].value = '15';
  selectionInputs[2].value = '45';
  selectionInputs[3].value = '7';
  selectionForm.querySelectorAll('button');
  const update = selectionForm.children.find((node) => node.tagName === 'button' && node.textContent === 'Update selection');
  update.click();
  const selectionRequest = requests.find((request) => request.route === '/api/clipping/jobs/clipjob_1/selection');
  assert.ok(selectionRequest, 'saved selection updates use the authenticated same-origin API');
  assert.deepEqual(JSON.parse(selectionRequest.options.body), {
    content_type: 'comedy', min_clip_seconds: 15, max_clip_seconds: 45, candidate_limit: 7
  });
});

test('video-only artifacts show persisted transcript availability without inventing transcript text', async () => {
  const job = {
    id: 'clipjob_video_only', source_id: 'clipsrc_video_only', status: 'completed',
    artifacts: [{
      type: 'clipping.analysis', schema_version: '2', version: 1,
      payload_json: JSON.stringify({
        transcript: { available: false, status: 'unavailable_no_audio_stream', original_script: true, segments: [] },
        audio_analysis_status: 'skipped_no_audio_stream',
        media_streams: { has_audio: false, has_video: true }
      })
    }]
  };
  const app = createUI(async (route, options = {}) => {
    if (route === '/api/clipping/config') return { worker_available: false };
    if (route === '/api/clipping/config/worker') return { configured: false, enabled: false };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs' && !options.method) return [job];
    if (route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/jobs/clipjob_video_only' && !options.method) return { job };
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const view = app.elements.get('clipping-jobs').querySelectorAll('button').find((node) => node.textContent === 'View saved analysis');
  assert.ok(view);
  view.click();
  for (let attempt = 0; attempt < 30 && !treeText(app.elements.get('clipping-jobs')).includes('Transcript availability'); attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  const rendered = treeText(app.elements.get('clipping-jobs'));
  assert.match(rendered, /No audio stream; audio analysis and transcription were skipped/);
  assert.equal(treeTags(app.elements.get('clipping-jobs')).includes('ol'), false, 'no transcript segments are fabricated when none were persisted');
  assert.doesNotMatch(rendered, /Original-script transcript/);
});

test('uncertain worker attempts show separate estimate and hold and require a reference for zero invoice actual', async () => {
  const requests = [];
  const stages = [
    {
      name: 'analysis', status: 'uncertain', attempt_id: 'attempt_uncertain',
      estimated_micro_usd: 0, actual_micro_usd: 0, reserved_micro_usd: 1_000_000, cost_reconciled: false
    },
    {
      name: 'analysis-reconciled', status: 'completed', attempt_id: 'attempt_reconciled',
      estimated_micro_usd: 700_000, actual_micro_usd: 500_000, reserved_micro_usd: 900_000, cost_reconciled: true
    },
    {
      name: 'analysis-no-attempt', status: 'queued', estimated_micro_usd: 0,
      actual_micro_usd: 0, reserved_micro_usd: 0, cost_reconciled: false
    }
  ];
  const job = { id: 'clipjob_cost', source_id: 'clipsrc_cost', status: 'paused_budget', stages, artifacts: [] };
  const app = createUI(async (route, options = {}) => {
    requests.push({ route, options });
    if (route === '/api/clipping/config') return { worker_available: false };
    if (route === '/api/clipping/config/worker') return { configured: false, enabled: false };
    if (route === '/api/clipping/sources') return [];
    if (route === '/api/clipping/jobs' && !options.method) return [job];
    if (route === '/api/clipping/batches') return [];
    if (route === '/api/clipping/jobs/clipjob_cost' && !options.method) return { job };
    if (route === '/api/clipping/jobs/clipjob_cost/reconcile-cost' && options.method === 'POST') {
      const input = JSON.parse(options.body);
      const stage = stages.find((candidate) => candidate.attempt_id === input.attempt_id);
      stage.actual_micro_usd = input.actual_cost_micro_usd;
      stage.cost_reconciled = true;
      return { job };
    }
    throw new Error(`Unexpected request: ${route}`);
  });
  await app.api.start();
  const view = app.elements.get('clipping-jobs').querySelectorAll('button').find((node) => node.textContent === 'View job details');
  assert.ok(view, 'uncertain attempted jobs can open details without artifacts');
  view.click();
  for (let attempt = 0; attempt < 30 && !treeText(app.elements.get('clipping-jobs')).includes('Entered invoice actual cost (USD)'); attempt++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  const rendered = treeText(app.elements.get('clipping-jobs'));
  assert.match(rendered, /Estimate\n\$0\.00/);
  assert.match(rendered, /Recorded actual\nNot recorded/);
  assert.match(rendered, /Remaining reserved hold\n\$1\.00/);
  assert.match(rendered, /Not reconciled/);
  assert.match(rendered, /Entered invoice actual cost \(USD\)/);
  assert.match(rendered, /Modal invoice/);

  const reconciliationForm = findNode(app.elements.get('clipping-jobs'), (node) => node.className === 'clipping-cost-reconciliation');
  assert.ok(reconciliationForm);
  const fields = reconciliationForm.children.filter((node) => node.tagName === 'label').map((label) => label.children[0]);
  const amount = fields[0];
  const reference = fields[1];
  const submit = reconciliationForm.children.find((node) => node.tagName === 'button');
  assert.equal(amount.min, '0');
  assert.equal(amount.required, true);
  assert.equal(reference.required, true);
  assert.equal(reference.maxLength, 256);
  assert.match(treeText(reference.parent), /maximum 256 UTF-8 bytes/);
  submit.click();
  assert.equal(requests.some((request) => request.route.endsWith('/reconcile-cost')), false, 'a blank amount and reference are rejected');
  assert.match(treeText(reconciliationForm), /nonnegative invoice amount/);
  amount.value = '0';
  submit.click();
  assert.equal(requests.some((request) => request.route.endsWith('/reconcile-cost')), false, 'zero actual still requires a reference');
  assert.match(treeText(reconciliationForm), /reconciliation reference/);
  reference.value = `${'é'.repeat(128)}a`;
  submit.click();
  assert.equal(requests.some((request) => request.route.endsWith('/reconcile-cost')), false, 'references over 256 UTF-8 bytes are rejected even when under 256 JS code units');
  assert.match(treeText(reconciliationForm), /at most 256 UTF-8 bytes/);
  reference.value = '😀'.repeat(64);
  submit.click();
  const request = requests.find((entry) => entry.route === '/api/clipping/jobs/clipjob_cost/reconcile-cost');
  assert.ok(request, 'manual cost reconciliation uses the safe same-origin job route');
  assert.deepEqual(JSON.parse(request.options.body), {
    attempt_id: 'attempt_uncertain', actual_cost_micro_usd: 0,
    reconciliation_reference: '😀'.repeat(64)
  });
  for (let attempt = 0; attempt < 30; attempt++) {
    const stageSection = findNode(app.elements.get('clipping-jobs'), (node) => node.className?.includes('clipping-stage-section'));
    const cards = stageSection?.children.filter((node) => node.tagName === 'article') || [];
    if (cards[0] && /Recorded actual\n\$0\.00/.test(treeText(cards[0]))) break;
    await new Promise((resolve) => setImmediate(resolve));
  }
  const stageSection = findNode(app.elements.get('clipping-jobs'), (node) => node.className?.includes('clipping-stage-section'));
  assert.ok(stageSection, treeText(app.elements.get('clipping-jobs')));
  const firstStageCard = stageSection.children.find((node) => node.tagName === 'article');
  assert.match(treeText(firstStageCard), /Recorded actual\n\$0\.00/, 'zero is shown only after explicit operator reconciliation');
  assert.match(treeText(firstStageCard), /Cost reconciliation\nReconciled/);
});
