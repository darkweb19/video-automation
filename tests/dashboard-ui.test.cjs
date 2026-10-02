const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const staticDir = path.join(__dirname, '..', 'internal', 'webui', 'static');
const markup = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
const source = fs.readFileSync(path.join(staticDir, 'app.js'), 'utf8');

// This DOM stub tests application state and events, not browser layout.
function createApp() {
  let document;
  let compact = false;
  const timers = [];
  class Element {
    constructor(tag = 'div') {
      this.tagName = tag;
      this.dataset = {};
      this.attributes = new Map();
      this.children = [];
      this.listeners = new Map();
      this.style = {};
      this.value = '';
      this.textContent = '';
      this.className = '';
      this.hidden = false;
      this.disabled = false;
      this.checked = false;
      this.open = false;
      this.classList = {
        contains: (name) => this.className.split(/\s+/).includes(name),
        toggle: (name, enabled) => {
          const classes = new Set(this.className.split(/\s+/).filter(Boolean));
          enabled ??= !classes.has(name);
          if (enabled) classes.add(name);
          else classes.delete(name);
          this.className = [...classes].join(' ');
          return enabled;
        },
        add: (name) => this.classList.toggle(name, true),
        remove: (name) => this.classList.toggle(name, false),
      };
    }
    get options() { return this.children; }
    setAttribute(name, value) { this.attributes.set(name, String(value)); }
    getAttribute(name) { return this.attributes.get(name) ?? null; }
    removeAttribute(name) { this.attributes.delete(name); }
    toggleAttribute(name, enabled) {
      if (enabled) this.setAttribute(name, '');
      else this.removeAttribute(name);
    }
    append(...nodes) {
      this.children.push(...nodes);
      nodes.forEach((node) => { node.parent = this; });
      if (this.tagName === 'select' && !this.value && nodes[0]) this.value = nodes[0].value;
    }
    replaceChildren(...nodes) { this.children.forEach((child) => { child.parent = null; }); this.children = []; this.append(...nodes); }
    querySelector() { return null; }
    querySelectorAll() { return []; }
    contains(node) { return node === this || this.children.some((child) => child.contains(node)); }
    closest() { return this.parent ??= new Element(); }
    focus() { document.activeElement = this; }
    reset() { this.onReset?.(); }
    load() {}
    pause() {}
    addEventListener(name, callback) {
      const callbacks = this.listeners.get(name) ?? [];
      callbacks.push(callback);
      this.listeners.set(name, callbacks);
    }
    emit(name, event = {}) {
      const emitted = { preventDefault() {}, ...event };
      return (this.listeners.get(name) ?? []).map((callback) => callback(emitted));
    }
    showModal() { this.open = true; }
    close() { this.open = false; this.emit('close'); }
    remove() {
      if (!this.parent) return;
      this.parent.children = this.parent.children.filter((child) => child !== this);
      this.parent = null;
    }
    dispatchEvent() {}
  }
  const nodes = new Map([...markup.matchAll(/<([a-z][a-z0-9-]*)\b[^>]*\bid="([^"]+)"[^>]*>/g)]
    .map((match) => [match[2], new Element(match[1])]));
  const sidebar = new Element('aside');
  const tabs = ['project', 'single'].map((mode) => {
    const tab = new Element('button');
    tab.dataset.mode = mode;
    return tab;
  });
  document = {
    activeElement: null,
    querySelector: (selector) => selector === '.sidebar' ? sidebar : nodes.get(selector.slice(1)) ?? null,
    querySelectorAll: (selector) => selector === '.mode-option' ? tabs : [],
    createElement: (tag) => new Element(tag),
    createDocumentFragment: () => new Element('fragment'),
    addEventListener() {},
  };
  const context = vm.createContext({
    document,
    Event,
    AbortController,
    window: {
      location: { href: 'http://localhost/' },
      history: { replaceState() {} },
      matchMedia: () => ({ matches: compact }),
      setTimeout: (callback) => { timers.push(callback); return timers.length; },
      clearTimeout() {},
      requestAnimationFrame: (callback) => callback(),
      addEventListener() {},
      scrollTo() {},
    },
    URL,
    URLSearchParams,
    TextEncoder,
    Blob,
    fetch: () => Promise.reject(new Error('Unexpected fetch in UI test')),
    uiTestRequest: () => Promise.reject(new Error('Unexpected request in UI test')),
  });
  const instrumented = source.replace('  bootstrap();', `
    request = (...args) => globalThis.uiTestRequest(...args);
    loadHistory = async () => {};
    globalThis.ui = {
      state, elements, setGenerationMode, syncGenerationModeDisplay,
      submitGeneration, submitProject, updateModelOptions,
      syncNavigationAccessibility, setNavigationOpen,
      handleJobSnapshot, bindEvents, renderRecent, renderHistory, renderProject,
      setStatusRecord, renderVaultItems, openYouTubeUpload, closeYouTubeComposer, renderYouTubeUpload,
      renderYouTubeSettings, loadYouTubeStatus, renderVaultLocked, showLoggedOut, generateYouTubeMetadata, submitYouTubeUpload, retryYouTubeUpload,
      abandonYouTubeUpload, restartYouTubeUpload, moveHistoryItem, openVaultVideo, loadProjects
    };
  `);
  assert.notEqual(instrumented, source, 'bootstrap instrumentation must match');
  vm.runInContext(instrumented, context, { filename: 'static/app.js' });
  context.ui.elements.generatorForm.onReset = () => {
    for (const key of ['projectTopic', 'prompt', 'model', 'duration', 'resolution', 'aspectRatio']) {
      context.ui.elements[key].value = '';
    }
  };
  return {
    ...context.ui,
    document,
    takeTimer: () => timers.shift(),
    setCompact: (value) => { compact = value; },
    setRequest: (request) => { context.uiTestRequest = request; },
    setFetch: (fetcher) => { context.fetch = fetcher; },
  };
}

function configuredApp(mode, compatible = true) {
  const app = createApp();
  app.state.authenticated = true;
  // Synthetic model capabilities are fixtures only; no provider is contacted.
  app.state.models = [{
    id: 'fixture/video', name: 'Fixture model',
    durations: compatible ? [6] : [8],
    resolutions: ['480p'], aspect_ratios: ['9:16'], audio: false,
  }];
  app.elements.model.value = 'fixture/video';
  app.elements.projectTopic.value = 'Test project topic';
  app.elements.prompt.value = 'Test single clip prompt';
  app.setGenerationMode(mode);
  return app;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test('project submission carries the selected category into story planning', async () => {
  const app = configuredApp('project');
  app.elements.promptCategory.value = '2';
  let submitted;
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') {
      submitted = JSON.parse(options.body);
      return Promise.reject(new Error('Fixture stops before generation'));
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await app.submitProject();
  assert.equal(submitted.topic, 'Test project topic');
  assert.equal(submitted.category, 'Nature');
});

function collectText(node) {
  return [node.textContent, ...(node.children || []).map(collectText)].filter(Boolean).join(' ');
}

function findTextButton(node, text) {
  if (node.tagName === 'button' && node.textContent === text) return node;
  for (const child of node.children || []) {
    const found = findTextButton(child, text);
    if (found) return found;
  }
  return null;
}

async function flushUI() {
  await new Promise((resolve) => setImmediate(resolve));
}

function connectedYouTubeApp({ uploads = [], projects = [] } = {}) {
  const app = createApp();
  app.state.authenticated = true;
  const calls = [];
  app.setRequest((requestPath, options = {}) => {
    calls.push({ path: requestPath, options });
    if (requestPath === '/api/youtube/status') {
      return Promise.resolve({ configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' } });
    }
    if (requestPath.startsWith('/api/youtube/uploads?')) return Promise.resolve({ uploads });
    if (requestPath === '/api/projects') return Promise.resolve({ projects });
    if (requestPath === '/api/youtube/uploads') {
      const body = JSON.parse(options.body);
      return Promise.resolve({ upload: { id: 'upload-fixture', status: 'queued', ...body } });
    }
    if (requestPath.includes('/restart')) return Promise.resolve({ upload: { id: 'upload-fixture', status: 'queued' } });
    if (requestPath.includes('/cancel')) return Promise.resolve({ upload: { id: 'upload-fixture', status: 'canceled', error: 'Abandoned; check YouTube Studio.', outcome_uncertain: true } });
    if (requestPath.includes('/retry')) return Promise.resolve({ upload: { id: 'upload-fixture', status: 'queued' } });
    if (requestPath === '/api/youtube/metadata') return Promise.resolve({ title: 'Fixture title', description: 'Fixture description' });
    return Promise.reject(new Error(`Unexpected UI request: ${requestPath}`));
  });
  app.setFetch(async () => ({ ok: true, blob: async () => new Blob(['test-only video bytes'], { type: 'video/mp4' }) }));
  return { app, calls };
}

async function openFixtureComposer(app, id = 'fixture-generation', kind = 'generation') {
  await app.openYouTubeUpload({
    kind, id, label: `Fixture ${kind}`, titleSuggestion: 'Fixture title',
    descriptionSuggestion: 'Fixture description', videoURL: `/fixture/${id}`,
  });
}

test('initial HTML IDs are unique and JavaScript bindings resolve', () => {
  const ids = [...markup.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);
  assert.equal(new Set(ids).size, ids.length);
  const bindings = source.split('const state =')[0];
  for (const match of bindings.matchAll(/\$\("#([a-z0-9-]+)"\)/g)) {
    assert.ok(ids.includes(match[1]), `missing initial binding: ${match[1]}`);
  }
});

for (const mode of ['project', 'single']) {
  test(`${mode} submission stays busy across format changes and restores its error`, async () => {
    const app = configuredApp(mode);
    const pending = deferred();
    app.setRequest(() => pending.promise);
    const submission = app.submitGeneration({ preventDefault() {} });
    assert.equal(app.elements.generate.disabled, true);
    assert.equal(app.elements.statusBadge.textContent.toLowerCase(), 'submitting');
    const otherMode = mode === 'project' ? 'single' : 'project';
    app.setGenerationMode(otherMode);
    app.updateModelOptions();
    assert.equal(app.elements.generate.disabled, true, 'model refresh cannot allow another paid submission');
    app.setGenerationMode(mode);
    assert.equal(app.elements.statusEmpty.hidden, true, 'pending request survives switching away and back');
    assert.equal(app.elements.statusBadge.textContent.toLowerCase(), 'submitting');
    app.setGenerationMode(otherMode);
    pending.reject(new Error('Fixture submission failed'));
    await submission;
    app.setGenerationMode(mode);
    const panel = mode === 'project' ? app.elements.projectStatus : app.elements.statusActive;
    const error = mode === 'project' ? app.elements.projectError : app.elements.statusError;
    assert.equal(panel.hidden, false);
    assert.equal(error.hidden, false);
    assert.equal(error.textContent, 'Fixture submission failed');
    assert.equal(app.elements.statusBadge.textContent.toLowerCase(), 'failed');
    assert.equal(app.elements.generate.disabled, false);
    assert.equal(app.elements.generate.getAttribute('aria-busy'), null);
  });
}

test('failed single submission cannot enable an incompatible project', async () => {
  const app = configuredApp('single', false);
  const pending = deferred();
  app.setRequest(() => pending.promise);
  const submission = app.submitGeneration({ preventDefault() {} });
  app.setGenerationMode('project');
  pending.reject(new Error('Fixture submission failed'));
  await submission;
  assert.equal(app.elements.generate.disabled, true);
  assert.equal(app.elements.generate.textContent, 'Generate 30-second project');
});

test('a repeated form submission cannot send a second request while busy', async () => {
  const app = configuredApp('single');
  const pending = deferred();
  let requests = 0;
  app.setRequest(() => { requests++; return pending.promise; });
  const first = app.submitGeneration({ preventDefault() {} });
  app.setGenerationMode('project');
  const repeated = app.submitGeneration({ preventDefault() {} });
  pending.reject(new Error('Fixture submission failed'));
  await Promise.all([first, repeated]);
  assert.equal(requests, 1);
});

test('a successful clip preserves a project idea entered while it was submitting', async () => {
  const app = configuredApp('single');
  const pending = deferred();
  app.setRequest(() => pending.promise);
  const submission = app.submitGeneration({ preventDefault() {} });
  app.setGenerationMode('project');
  app.elements.projectTopic.value = 'New project idea entered while the clip is submitting';
  pending.resolve({ id: 'fixture-clip', status: 'queued', events: [] });
  await submission;
  assert.equal(app.elements.projectTopic.value, 'New project idea entered while the clip is submitting');
  assert.equal(app.elements.prompt.value, '');
  assert.equal(app.state.displayedGenerationID, 'fixture-clip');
  assert.equal(app.elements.generate.textContent, 'Generate 30-second project');
});

test('completed and failed saved results remain scoped to their format', () => {
  const app = configuredApp('project');
  Object.assign(app.state, {
    displayedProjectID: 'fixture-project', displayedProjectStatus: 'completed',
    displayedGenerationID: 'fixture-clip', displayedGenerationStatus: 'failed',
  });
  app.syncGenerationModeDisplay();
  assert.equal(app.elements.projectStatus.hidden, false);
  assert.equal(app.elements.statusActive.hidden, true);
  assert.equal(app.elements.statusBadge.textContent, 'completed');
  app.setGenerationMode('single');
  assert.equal(app.elements.projectStatus.hidden, true);
  assert.equal(app.elements.statusActive.hidden, false);
  assert.equal(app.elements.statusBadge.textContent, 'failed');
});

test('closed mobile navigation is inert and desktop resize restores it', () => {
  const app = createApp();
  app.setCompact(true);
  app.syncNavigationAccessibility();
  assert.equal(app.elements.sidebar.getAttribute('inert'), '');
  assert.equal(app.elements.menuButton.getAttribute('aria-expanded'), 'false');
  app.setNavigationOpen(true);
  assert.equal(app.elements.sidebar.getAttribute('inert'), null);
  assert.equal(app.elements.menuButton.getAttribute('aria-expanded'), 'true');
  app.setNavigationOpen(false, { returnFocus: true });
  assert.equal(app.document.activeElement, app.elements.menuButton);
  app.setCompact(false);
  app.syncNavigationAccessibility();
  assert.equal(app.elements.sidebar.getAttribute('inert'), null);
  assert.equal(app.elements.sidebar.getAttribute('aria-hidden'), 'false');
});

test('resizing to mobile moves focus out of a newly hidden navigation rail', () => {
  const app = createApp();
  const navButton = app.document.createElement('button');
  app.elements.sidebar.append(navButton);
  navButton.focus();
  app.setCompact(true);
  app.syncNavigationAccessibility();
  assert.equal(app.document.activeElement, app.elements.menuButton);
  assert.equal(app.elements.sidebar.getAttribute('inert'), '');
});

for (const kind of ['generation', 'project']) {
  test(`${kind} live snapshots reject stale jobs and accept newer selected updates`, () => {
    const app = createApp();
    app.state.authenticated = true;
    app.state.mode = kind === 'project' ? 'project' : 'single';
    const current = {
      id: `fixture-${kind}-new`,
      status: 'processing',
      updated_at: 2000,
      progress: 70,
      scenes: [],
      events: [],
    };
    if (kind === 'generation') {
      app.state.currentGenerationID = current.id;
      app.state.displayedGenerationID = current.id;
      app.state.currentGenerationRecord = current;
      app.state.generations = [current];
      app.state.displayedGenerationStatus = current.status;
    } else {
      app.state.currentProjectID = current.id;
      app.state.displayedProjectID = current.id;
      app.state.currentProjectRecord = current;
      app.state.projects = [current];
      app.state.displayedProjectStatus = current.status;
    }

    const older = { ...current, status: 'failed', updated_at: 1000, progress: 100 };
    app.handleJobSnapshot(kind, {
      data: JSON.stringify({ id: current.id, [kind]: older }),
    });

    const idKey = kind === 'project' ? 'currentProjectID' : 'currentGenerationID';
    const displayedIDKey = kind === 'project' ? 'displayedProjectID' : 'displayedGenerationID';
    const recordKey = kind === 'project' ? 'currentProjectRecord' : 'currentGenerationRecord';
    const statusKey = kind === 'project' ? 'displayedProjectStatus' : 'displayedGenerationStatus';
    assert.equal(app.state[idKey], current.id);
    assert.equal(app.state[displayedIDKey], current.id);
    assert.equal(app.state[recordKey], current);
    assert.equal(app.state[statusKey], 'processing');

    const unrelated = { ...current, id: `fixture-${kind}-old`, status: 'failed', updated_at: 3000 };
    app.handleJobSnapshot(kind, {
      data: JSON.stringify({ id: unrelated.id, [kind]: unrelated }),
    });
    assert.equal(app.state[idKey], current.id);
    assert.equal(app.state[displayedIDKey], current.id);
    assert.equal(app.state[recordKey], current);

    const newer = { ...current, updated_at: 4000, progress: 85 };
    app.handleJobSnapshot(kind, {
      data: JSON.stringify({ id: current.id, [kind]: newer }),
    });
    assert.equal(app.state[idKey], current.id);
    assert.equal(app.state[recordKey].updated_at, 4000);
    assert.equal(app.state[recordKey].progress, 85);
    assert.equal(app.state[statusKey], 'processing');
  });
}

test('completed videos expose Upload to YouTube from Overview, Generate, History, and both Vault surfaces', async () => {
  const project = {
    id: 'fixture-project', status: 'completed', video_ready: true, final_video_ready: true,
    topic: 'Fixture project topic', story: 'Fixture story', scenes: [], created_at: 2,
  };
  const generation = {
    id: 'fixture-generation', status: 'completed', video_ready: true,
    prompt: 'Fixture generated clip', created_at: 1,
  };
  const { app, calls } = connectedYouTubeApp({ projects: [project] });
  app.bindEvents();

  const assertOpens = async (button, expectedKind, expectedID) => {
    assert.ok(button, `missing ${expectedKind} upload action`);
    button.emit('click');
    await flushUI();
    assert.equal(app.elements.youtubeDialog.open, true);
    assert.equal(app.state.youtubeSource.kind, expectedKind);
    assert.equal(app.state.youtubeSource.id, expectedID);
    app.closeYouTubeComposer(false);
  };

  app.state.generations = [generation];
  app.renderRecent();
  await assertOpens(findTextButton(app.elements.recentList, 'Upload to YouTube'), 'generation', generation.id);

  app.setStatusRecord(generation);
  await assertOpens(app.elements.singleYouTubeUpload, 'generation', generation.id);

  app.renderProject(project, true);
  await assertOpens(app.elements.projectYouTubeUpload, 'project', project.id);

  app.state.generations = [generation];
  app.renderHistory();
  await assertOpens(findTextButton(app.elements.historyGrid, 'Upload to YouTube'), 'generation', generation.id);

  await app.loadProjects(false);
  await assertOpens(findTextButton(app.elements.projectHistory, 'Upload to YouTube'), 'project', project.id);

  const vaultItem = { kind: 'generation', id: 'fixture-vault-generation', title: 'Protected fixture', created_at: 3 };
  app.state.vaultToken = 'vault-grant-fixture';
  app.state.vaultItems = [vaultItem];
  app.renderVaultItems();
  await assertOpens(findTextButton(app.elements.vaultGrid, 'Upload to YouTube'), 'generation', vaultItem.id);
  const gridVaultList = calls.findLast((call) => call.path.includes('source_id=fixture-vault-generation'));
  assert.equal(gridVaultList.options.headers.Authorization, 'Vault vault-grant-fixture');
  assert.equal(app.elements.youtubeTitle.value.includes('vault-grant-fixture'), false);

  const view = findTextButton(app.elements.vaultGrid, 'View video');
  assert.ok(view);
  view.emit('click');
  await flushUI();
  assert.equal(app.elements.vaultPlayerDialog.open, true);
  app.elements.vaultPlayerYouTubeUpload.emit('click');
  await flushUI();
  assert.equal(app.state.youtubeSource.kind, 'generation');
  assert.equal(app.state.youtubeSource.id, vaultItem.id);
  assert.equal(app.state.youtubeSource.isVault, true);
  const playerVaultList = calls.findLast((call) => call.path.includes('source_id=fixture-vault-generation'));
  assert.equal(playerVaultList.options.headers.Authorization, 'Vault vault-grant-fixture');
  app.closeYouTubeComposer(false);
});

test('upload waits for fresh channel status and lists only records tied to that channel', async () => {
  const staleUpload = { id: 'old-channel-upload', channel_id: 'old-channel', status: 'failed', title: 'Old title' };
  const currentUpload = { id: 'current-channel-upload', channel_id: 'channel-fixture', status: 'failed', title: 'Current title' };
  const { app, calls } = connectedYouTubeApp({ uploads: [staleUpload, currentUpload] });
  app.state.youtubeStatus = { configured: true, connected: true, channel: { id: 'old-channel', title: 'Old channel' } };
  await app.openYouTubeUpload({ kind: 'generation', id: 'fixture-generation', titleSuggestion: 'Fresh source title', descriptionSuggestion: '' });
  assert.equal(app.state.youtubeDialogReady, true);
  assert.equal(app.state.youtubeStatus.channel.id, 'channel-fixture');
  assert.equal(app.state.youtubeUpload.id, currentUpload.id);
  assert.equal(app.elements.youtubeTitle.value, 'Current title');
  assert.equal(calls.some((call) => call.path === '/api/youtube/uploads?source_kind=generation&source_id=fixture-generation'), true);
  app.closeYouTubeComposer(false);
});

test('fresh status failure keeps the composer upload disabled despite a cached connected channel', async () => {
  const app = createApp();
  app.state.authenticated = true;
  app.state.youtubeStatus = { configured: true, connected: true, channel: { id: 'cached-channel', title: 'Cached channel' } };
  app.setRequest((path) => {
    if (path === '/api/youtube/status') return Promise.reject(new Error('Fixture status unavailable'));
    throw new Error(`Stale status must not trigger another request: ${path}`);
  });
  await app.openYouTubeUpload({ kind: 'generation', id: 'fixture-generation', titleSuggestion: 'Fixture title' });
  assert.equal(app.state.youtubeDialogReady, false);
  assert.equal(app.elements.youtubeUploadSubmit.disabled, true);
  assert.equal(app.elements.youtubeUploadError.textContent, 'Fixture status unavailable');
  app.closeYouTubeComposer(false);
});

test('the form handler enforces rendered upload eligibility and waits for the source upload list', async () => {
  const app = createApp();
  app.state.authenticated = true;
  const list = deferred();
  let createRequests = 0;
  app.setRequest((path, options = {}) => {
    if (path === '/api/youtube/status') return Promise.resolve({ configured: true, connected: true, channel: { id: 'channel-a', title: 'Channel A' } });
    if (path.startsWith('/api/youtube/uploads?')) return list.promise;
    if (path === '/api/youtube/uploads') { createRequests += 1; return Promise.resolve({ upload: { id: 'new', status: 'queued' } }); }
    throw new Error(`Unexpected UI request: ${path}`);
  });
  const opening = app.openYouTubeUpload({ kind: 'generation', id: 'guarded-source', label: 'Guarded source', titleSuggestion: 'Guarded title' });
  await flushUI();
  app.elements.youtubeTitle.value = 'Guarded title';
  app.elements.youtubeMadeForKidsNo.checked = true;
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.equal(createRequests, 0, 'the form handler cannot bypass pending upload-history review');
  assert.equal(app.elements.youtubeUploadSubmit.disabled, true);
  list.resolve({ uploads: [] });
  await opening;
  assert.equal(app.state.youtubeDialogReady, true);

  app.state.youtubeUpload = { id: 'already-done', status: 'completed' };
  await app.submitYouTubeUpload({ preventDefault() {} });
  app.state.youtubeUpload = { id: 'failed', status: 'failed' };
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.equal(createRequests, 0, 'completed and failed rows must use their safe status actions');

  app.state.youtubeUpload = { id: 'safe-cancel', status: 'canceled' };
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.equal(createRequests, 1, 'a safely canceled row can be re-enqueued');
  app.closeYouTubeComposer(false);
});

test('a channel change after review closes the dialog instead of submitting stale channel history', async () => {
  const { app, calls } = connectedYouTubeApp();
  await openFixtureComposer(app, 'channel-bound-source');
  assert.equal(app.state.youtubeReviewedChannelID, 'channel-fixture');
  app.setRequest((path) => {
    if (path === '/api/youtube/status') return Promise.resolve({ configured: true, connected: true, channel: { id: 'channel-b', title: 'Channel B' } });
    throw new Error(`Unexpected request: ${path}`);
  });
  await app.loadYouTubeStatus();
  assert.equal(app.elements.youtubeDialog.open, false);
  assert.equal(app.state.youtubeSource, null);
  assert.equal(calls.some((call) => call.path === '/api/youtube/uploads'), false);
});

test('YouTube settings never fill the write-only client secret from status data', () => {
  const app = createApp();
  app.elements.youtubeClientSecret.value = 'typed-only-secret';
  app.state.youtubeConfigDirty = false;
  app.renderYouTubeSettings({
    configured: true, connected: false, client_id: 'fixture-client', base_url: 'https://framevault.test',
    redirect_uri: 'https://framevault.test/api/youtube/oauth/callback', has_client_secret: true,
  });
  assert.equal(app.elements.youtubeClientID.value, 'fixture-client');
  assert.equal(app.elements.youtubeRedirectURI.value, 'https://framevault.test/api/youtube/oauth/callback');
  assert.equal(app.elements.youtubeClientSecret.value, '');
  assert.match(app.elements.youtubeSecretHelp.textContent, /never sent back/);
});

test('manual upload sends explicit metadata, disclosures, chosen visibility, and reviewed channel once', async () => {
  const { app, calls } = connectedYouTubeApp();
  await openFixtureComposer(app, 'fixture-project-source', 'project');
  const pending = deferred();
  let uploadRequests = 0;
  app.setRequest((path, options = {}) => {
    if (path === '/api/youtube/uploads') {
      uploadRequests += 1;
      calls.push({ path, options });
      return pending.promise;
    }
    if (path === '/api/youtube/status') return Promise.resolve({ configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' } });
    if (path.startsWith('/api/youtube/uploads?')) return Promise.resolve({ uploads: [] });
    throw new Error(`Unexpected UI request: ${path}`);
  });
  app.elements.youtubeTitle.value = 'Reviewed project title';
  app.elements.youtubeDescription.value = 'Reviewed project description';
  app.elements.youtubeVisibility.value = 'public';
  app.elements.youtubeMadeForKidsNo.checked = true;
  app.elements.youtubeSynthetic.checked = false;
  const first = app.submitYouTubeUpload({ preventDefault() {} });
  const second = app.submitYouTubeUpload({ preventDefault() {} });
  assert.equal(uploadRequests, 1, 'the per-dialog guard blocks Enter or repeated submit while POST is pending');
  const body = JSON.parse(calls.at(-1).options.body);
  assert.deepEqual(body, {
    source_kind: 'project', source_id: 'fixture-project-source', channel_id: 'channel-fixture',
    title: 'Reviewed project title', description: 'Reviewed project description', privacy_status: 'public',
    made_for_kids: false, contains_synthetic_media: false,
  });
  pending.resolve({ upload: { id: 'upload-fixture', status: 'queued', channel_id: 'channel-fixture' } });
  await Promise.all([first, second]);
  assert.equal(app.state.youtubeUpload.status, 'queued');
  app.closeYouTubeComposer(false);
});

test('manual upload validates Unicode title points, description UTF-8 bytes, disclosures, and angle brackets', async () => {
  const { app, calls } = connectedYouTubeApp();
  await openFixtureComposer(app);
  app.elements.youtubeTitle.value = 'Valid title';
  app.elements.youtubeDescription.value = '';
  app.elements.youtubeMadeForKidsNo.checked = true;
  app.elements.youtubeTitle.value = '😀'.repeat(101);
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.match(app.elements.youtubeUploadError.textContent, /100 characters/);
  app.elements.youtubeTitle.value = 'Valid title';
  app.elements.youtubeDescription.value = '😀'.repeat(1251);
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.match(app.elements.youtubeUploadError.textContent, /5,000 UTF-8 bytes/);
  app.elements.youtubeDescription.value = 'safe <tag>';
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.match(app.elements.youtubeUploadError.textContent, /angle brackets/);
  app.elements.youtubeDescription.value = '';
  app.elements.youtubeMadeForKidsNo.checked = false;
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.match(app.elements.youtubeUploadError.textContent, /Choose whether this video is made for kids/);
  assert.equal(calls.some((call) => call.path === '/api/youtube/uploads'), false);
  app.closeYouTubeComposer(false);
});

test('completed, processing, retry, reconnect, and uncertain uploads expose only safe actions', () => {
  const { app } = connectedYouTubeApp();
  app.bindEvents();
  app.state.authenticated = true;
  app.state.youtubeStatus = { configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' } };
  app.state.youtubeSource = { kind: 'generation', id: 'fixture-generation' };
  app.elements.youtubeDialog.open = true;
  app.state.youtubeDialogReady = true;

  app.renderYouTubeUpload({ id: 'done', status: 'completed', youtube_video_id: 'video123', youtube_url: 'https://youtu.be/video123' });
  assert.equal(app.elements.youtubeUploadStatusLabel.textContent, 'Ready on YouTube');
  assert.match(app.elements.youtubeUploadNote.textContent, /finished processing/);
  assert.equal(app.elements.youtubeUploadSubmit.hidden, true);
  assert.equal(app.elements.youtubeUploadRetry.hidden, true);
  assert.equal(app.elements.youtubeOpenVideo.hidden, false);

  app.renderYouTubeUpload({ id: 'processing', status: 'processing' });
  assert.equal(app.elements.youtubeUploadSubmit.hidden, true);
  assert.equal(app.elements.youtubeUploadCancel.hidden, true);
  assert.equal(app.elements.youtubeTitle.disabled, true);

  app.renderYouTubeUpload({ id: 'failed', status: 'failed' });
  assert.equal(app.elements.youtubeUploadRetry.hidden, false);
  app.renderYouTubeUpload({ id: 'failed-known', status: 'failed', youtube_video_id: 'already-known' });
  assert.equal(app.elements.youtubeUploadRetry.hidden, true);

  app.renderYouTubeUpload({ id: 'reconnect', status: 'needs_reconnect' });
  assert.equal(app.elements.youtubeGoSettings.textContent, 'Reconnect in Settings');
  assert.equal(app.elements.youtubeUploadAbandon.hidden, false);
  assert.equal(app.elements.youtubeDuplicateRecovery.hidden, true);

  app.renderYouTubeUpload({ id: 'attention', status: 'attention_required' });
  assert.equal(app.elements.youtubeOpenStudio.hidden, false);
  assert.equal(app.elements.youtubeDuplicateRecovery.hidden, false);
  assert.equal(app.elements.youtubeUploadRestart.disabled, true);
  app.elements.youtubeDuplicateConfirm.checked = true;
  app.elements.youtubeDuplicateConfirm.emit('change');
  assert.equal(app.elements.youtubeUploadRestart.disabled, false);

  app.renderYouTubeUpload({ id: 'attention-known', status: 'attention_required', youtube_video_id: 'already-known' });
  assert.equal(app.elements.youtubeDuplicateRecovery.hidden, false);
  assert.equal(app.elements.youtubeRecoveryConfirmControls.hidden, true);
  assert.equal(app.elements.youtubeUploadRestart.disabled, true);
  app.closeYouTubeComposer(false);
});

test('duplicate-risk restart requires confirmation, uses the explicit endpoint, and ignores stale responses', async () => {
  const { app, calls } = connectedYouTubeApp();
  await openFixtureComposer(app);
  app.renderYouTubeUpload({ id: 'uncertain', status: 'canceled', outcome_uncertain: true });
  assert.equal(app.elements.youtubeUploadSubmit.hidden, true);
  assert.equal(app.elements.youtubeDuplicateRecovery.hidden, false);
  await app.restartYouTubeUpload();
  assert.equal(calls.some((call) => call.path.endsWith('/restart')), false, 'no restart before explicit Studio confirmation');

  app.elements.youtubeDuplicateConfirm.checked = true;
  const restartRequest = app.restartYouTubeUpload();
  const restartCall = calls.find((call) => call.path.endsWith('/uploads/uncertain/restart'));
  assert.ok(restartCall);
  assert.deepEqual(JSON.parse(restartCall.options.body), { confirm_not_uploaded: true });
  await restartRequest;
  assert.equal(app.state.youtubeUpload.status, 'queued');

  app.renderYouTubeUpload({ id: 'audited-restart', status: 'queued', manual_restart_note: 'The prior resumable session was checked first.' });
  assert.match(app.elements.youtubeUploadNote.textContent, /prior resumable session was checked first/);

  app.renderYouTubeUpload({ id: 'attention', status: 'attention_required' });
  const pending = deferred();
  app.setRequest((path) => path.endsWith('/restart') ? pending.promise : Promise.reject(new Error(`Unexpected request: ${path}`)));
  app.elements.youtubeDuplicateConfirm.checked = true;
  const staleRestart = app.restartYouTubeUpload();
  app.closeYouTubeComposer(false);
  await app.openYouTubeUpload({ kind: 'generation', id: 'second-source', titleSuggestion: 'Second source' });
  pending.resolve({ upload: { id: 'old-upload', status: 'queued' } });
  await staleRestart;
  assert.equal(app.state.youtubeSource.id, 'second-source');
  assert.equal(app.state.youtubeUpload, null, 'a response from the closed dialog cannot replace the new source state');
  app.closeYouTubeComposer(false);
});

test('abandon records an unresolved outcome without presenting it as remote deletion', async () => {
  const { app, calls } = connectedYouTubeApp();
  await openFixtureComposer(app, 'abandoned-source');
  app.renderYouTubeUpload({ id: 'needs-reconnect', status: 'needs_reconnect' });
  assert.equal(app.elements.youtubeUploadAbandon.hidden, false);
  await app.abandonYouTubeUpload();
  assert.ok(calls.some((call) => call.path.endsWith('/uploads/needs-reconnect/cancel')));
  assert.equal(app.state.youtubeUpload.status, 'canceled');
  assert.equal(app.state.youtubeUpload.outcome_uncertain, true);
  assert.equal(app.elements.youtubeUploadSubmit.hidden, true);
  assert.equal(app.elements.youtubeUploadNote.textContent.includes('delete'), false);
  app.closeYouTubeComposer(false);
});

test('a late create response cannot replace the next source or re-enable its controls', async () => {
  const { app } = connectedYouTubeApp();
  await openFixtureComposer(app, 'first-source');
  app.elements.youtubeTitle.value = 'First title';
  app.elements.youtubeMadeForKidsNo.checked = true;
  const pending = deferred();
  app.setRequest((path) => path === '/api/youtube/uploads' ? pending.promise : Promise.resolve({
    configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' }, uploads: [],
  }));
  const oldSubmission = app.submitYouTubeUpload({ preventDefault() {} });
  app.closeYouTubeComposer(false);
  await openFixtureComposer(app, 'second-source');
  assert.equal(app.elements.youtubeUploadSubmit.disabled, false);
  pending.resolve({ upload: { id: 'old-upload', status: 'queued' } });
  await oldSubmission;
  assert.equal(app.state.youtubeSource.id, 'second-source');
  assert.equal(app.state.youtubeUpload, null);
  assert.equal(app.elements.youtubeUploadSubmit.disabled, false);
  assert.equal(app.elements.youtubeUploadSubmit.textContent, 'Upload video');
  app.closeYouTubeComposer(false);
});

test('metadata failures preserve edits, and an old dialog cannot reset a new metadata request', async () => {
  const { app } = connectedYouTubeApp();
  await openFixtureComposer(app, 'first-source');
  app.elements.youtubeTitle.value = 'User edited title';
  app.elements.youtubeDescription.value = 'User edited description';
  app.setRequest((path) => path === '/api/youtube/metadata' ? Promise.reject(new Error('Metadata unavailable')) : Promise.resolve({
    configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' }, uploads: [],
  }));
  await app.generateYouTubeMetadata();
  assert.equal(app.elements.youtubeTitle.value, 'User edited title');
  assert.equal(app.elements.youtubeDescription.value, 'User edited description');
  assert.equal(app.elements.youtubeMetadataError.textContent, 'Metadata unavailable');

  const pending = deferred();
  app.setRequest((path) => path === '/api/youtube/metadata' ? pending.promise : Promise.resolve({
    configured: true, connected: true, channel: { id: 'channel-fixture', title: 'Fixture channel' }, uploads: [],
  }));
  const oldMetadata = app.generateYouTubeMetadata();
  app.closeYouTubeComposer(false);
  await openFixtureComposer(app, 'second-source');
  assert.equal(app.elements.youtubeGenerateMetadata.disabled, false);
  assert.equal(app.elements.youtubeGenerateMetadata.textContent, 'Generate title and description');
  pending.resolve({ title: 'Late title', description: 'Late description' });
  await oldMetadata;
  assert.equal(app.state.youtubeSource.id, 'second-source');
  assert.equal(app.elements.youtubeTitle.value, 'Fixture title');
  assert.equal(app.elements.youtubeGenerateMetadata.disabled, false);
  assert.equal(app.elements.youtubeGenerateMetadata.textContent, 'Generate title and description');
  app.closeYouTubeComposer(false);
});

test('metadata resolving after manual upload cannot change queued details or unlock the composer', async () => {
  const { app } = connectedYouTubeApp();
  await openFixtureComposer(app, 'metadata-race-source');
  const pendingMetadata = deferred();
  let submittedBody;
  app.setRequest((path, options = {}) => {
    if (path === '/api/youtube/metadata') return pendingMetadata.promise;
    if (path === '/api/youtube/uploads') {
      submittedBody = JSON.parse(options.body);
      return Promise.resolve({ upload: { id: 'queued-upload', status: 'queued', ...submittedBody } });
    }
    throw new Error(`Unexpected request: ${path}`);
  });

  const metadataRequest = app.generateYouTubeMetadata();
  app.elements.youtubeTitle.value = 'Manual title';
  app.elements.youtubeDescription.value = 'Manual description';
  app.elements.youtubeMadeForKidsNo.checked = true;
  await app.submitYouTubeUpload({ preventDefault() {} });
  assert.equal(submittedBody.title, 'Manual title');
  assert.equal(submittedBody.description, 'Manual description');
  assert.equal(app.state.youtubeUpload.status, 'queued');
  assert.equal(app.elements.youtubeGenerateMetadata.disabled, true);

  pendingMetadata.resolve({ title: 'Late generated title', description: 'Late generated description' });
  await metadataRequest;
  assert.equal(app.elements.youtubeTitle.value, 'Manual title');
  assert.equal(app.elements.youtubeDescription.value, 'Manual description');
  assert.equal(app.elements.youtubeTitle.disabled, true);
  assert.equal(app.elements.youtubeDescription.disabled, true);
  assert.equal(app.elements.youtubeGenerateMetadata.disabled, true);
  assert.equal(app.elements.youtubeGenerateMetadata.textContent, 'Generate title and description');
  app.closeYouTubeComposer(false);
});

test('a successful move to Vault invalidates a matching non-Vault YouTube composer', async () => {
  const { app } = connectedYouTubeApp();
  await openFixtureComposer(app, 'moving-generation');
  app.setRequest((path) => {
    if (path.startsWith('/api/vault/items/generation/moving-generation/move')) return Promise.resolve({});
    throw new Error(`Unexpected request: ${path}`);
  });
  await app.moveHistoryItem('generation', 'moving-generation', app.document.createElement('button'));
  assert.equal(app.elements.youtubeDialog.open, false);
  assert.equal(app.state.youtubeSource, null);
  assert.equal(app.elements.youtubePreview.getAttribute('src'), null);
});

test('locking the Vault aborts a protected preview and closes its upload composer', async () => {
  const { app } = connectedYouTubeApp();
  app.state.vaultToken = 'vault-grant-fixture';
  const item = { kind: 'generation', id: 'protected-generation', title: 'Protected title' };
  await app.openYouTubeUpload({
    kind: item.kind, id: item.id, label: item.title, isVault: true, vaultItem: item,
    titleSuggestion: item.title, descriptionSuggestion: '',
  });
  assert.equal(app.elements.youtubeDialog.open, true);
  assert.match(app.elements.youtubePreview.src, /^blob:/);
  app.renderVaultLocked('Vault locked for test.');
  assert.equal(app.elements.youtubeDialog.open, false);
  assert.equal(app.state.youtubeSource, null);
  assert.equal(app.state.vaultToken, '');
  assert.equal(app.elements.youtubePreview.getAttribute('src'), null);
});
