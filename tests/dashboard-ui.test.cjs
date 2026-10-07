const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const staticDir = path.join(__dirname, '..', 'internal', 'webui', 'static');
const markup = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
const source = fs.readFileSync(path.join(staticDir, 'app.js'), 'utf8');

// This DOM stub tests application state and events, not browser layout.
let requestIDSeed = 0;
function createApp({ sessionData = new Map(), storageThrows = false, stubHistory = true } = {}) {
  let document;
  let compact = false;
  const timers = [];
  const eventSources = [];
  class EventSourceStub {
    constructor(url, options) {
      this.url = url;
      this.options = options;
      this.listeners = new Map();
      this.closed = false;
      eventSources.push(this);
    }
    addEventListener(name, callback) {
      const callbacks = this.listeners.get(name) ?? [];
      callbacks.push(callback);
      this.listeners.set(name, callbacks);
    }
    emit(name, event = {}) {
      for (const callback of this.listeners.get(name) ?? []) callback(event);
    }
    close() { this.closed = true; }
  }
  const sessionStorage = {
    getItem: (key) => { if (storageThrows) throw new Error('Storage unavailable'); return sessionData.get(key) ?? null; },
    setItem: (key, value) => { if (storageThrows) throw new Error('Storage unavailable'); sessionData.set(key, String(value)); },
    removeItem: (key) => { if (storageThrows) throw new Error('Storage unavailable'); sessionData.delete(key); },
  };
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
      this.detachments = 0;
      this.replacements = 0;
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
      nodes.forEach((node) => {
        node.remove();
        this.children.push(node);
        node.parent = this;
      });
      if (this.tagName === 'select' && !this.value && nodes[0]) this.value = nodes[0].value;
    }
    replaceChildren(...nodes) {
      this.replacements++;
      [...this.children].forEach((child) => child.remove());
      this.append(...nodes);
    }
    insertBefore(node, reference) {
      if (node === reference) return;
      node.remove();
      const index = this.children.indexOf(reference);
      node.parent = this;
      this.children.splice(index < 0 ? this.children.length : index, 0, node);
    }
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
    click() { this.emit('click'); }
    showModal() { this.open = true; }
    close() { this.open = false; this.emit('close'); }
    remove() {
      if (!this.parent) return;
      this.detachments++;
      this.parent.children = this.parent.children.filter((child) => child !== this);
      this.parent = null;
    }
    dispatchEvent() {}
  }
  const nodes = new Map([...markup.matchAll(/<([a-z][a-z0-9-]*)\b[^>]*\bid="([^"]+)"[^>]*>/g)]
    .map((match) => [match[2], new Element(match[1])]));
  nodes.get('password-form').parentElement = new Element('div');
  const sidebar = new Element('aside');
  const tabs = ['project', 'single'].map((mode) => {
    const tab = new Element('button');
    tab.dataset.mode = mode;
    return tab;
  });
  const createModeButtons = [...markup.matchAll(/<button\b[^>]*\bdata-create-mode="([^"]+)"[^>]*>/g)].map((match) => {
    const button = new Element('button');
    button.dataset.createMode = match[1];
    return button;
  });
  document = {
    activeElement: null,
    querySelector: (selector) => selector === '.sidebar' ? sidebar : nodes.get(selector.slice(1)) ?? null,
    querySelectorAll: (selector) => selector === '.mode-option' ? tabs
      : selector === '[data-create-mode]' ? createModeButtons : [],
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
      sessionStorage,
      crypto: { randomUUID: () => `fixture-project-request-${++requestIDSeed}` },
      EventSource: EventSourceStub,
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
    ${stubHistory ? 'loadHistory = async () => {};' : ''}
    globalThis.ui = {
      state, elements, setGenerationMode, syncGenerationModeDisplay, applyPasswordGate,
      submitGeneration, submitProject, generateRandomPrompt, updateModelOptions,
      syncNavigationAccessibility, setNavigationOpen,
      handleJobSnapshot, bindEvents, renderRecent, renderHistory, renderProject,
      startJobEventStream, stopJobEventStream,
      setStatusRecord, renderVaultItems, openYouTubeUpload, closeYouTubeComposer, renderYouTubeUpload,
      renderYouTubeSettings, loadYouTubeStatus, renderVaultLocked, showLoggedOut, generateYouTubeMetadata, submitYouTubeUpload, retryYouTubeUpload,
      abandonYouTubeUpload, restartYouTubeUpload, moveHistoryItem, openVaultVideo, loadProjects, loadHistory
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
    eventSources,
    setCompact: (value) => { compact = value; },
    setRequest: (request) => { context.uiTestRequest = request; },
    setFetch: (fetcher) => { context.fetch = fetcher; },
  };
}

function configuredApp(mode, compatible = true, options = {}) {
  const app = createApp(options);
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

test('an invalid random project idea response preserves the topic and allows a safe retry', async () => {
  const app = configuredApp('project');
  app.elements.projectTopic.value = 'Keep this idea while retrying';
  let attempts = 0;
  app.setRequest((requestPath, options = {}) => {
    assert.equal(requestPath, '/api/prompts/random');
    assert.equal(JSON.parse(options.body).mode, 'project');
    attempts++;
    if (attempts === 1) return Promise.resolve({ prompt: '  ' });
    if (attempts === 2) return Promise.reject(new Error('The prompt model did not return a complete, valid prompt. Try again shortly.'));
    return Promise.resolve({ prompt: 'A new project idea' });
  });

  await app.generateRandomPrompt();
  assert.equal(app.elements.projectTopic.value, 'Keep this idea while retrying');
  assert.match(app.elements.toast.textContent, /No prompt was returned/);
  assert.equal(app.elements.randomPrompt.disabled, false);

  await app.generateRandomPrompt();
  assert.equal(app.elements.projectTopic.value, 'Keep this idea while retrying');
  assert.equal(app.elements.toast.textContent, 'The prompt model did not return a complete, valid prompt. Try again shortly.');
  assert.equal(app.elements.randomPrompt.disabled, false);

  await app.generateRandomPrompt();
  assert.equal(app.elements.projectTopic.value, 'A new project idea');
  assert.equal(app.elements.toast.classList.contains('error'), false);
  assert.equal(attempts, 3);
});

test('duplicate random idea requests are ignored while the first request is pending', async () => {
  const app = configuredApp('project');
  const pending = deferred();
  let attempts = 0;
  app.setRequest(() => { attempts++; return pending.promise; });
  const first = app.generateRandomPrompt();
  const second = app.generateRandomPrompt();
  assert.equal(attempts, 1);
  assert.equal(app.elements.randomPrompt.disabled, true);
  pending.resolve({ prompt: 'One generated idea' });
  await Promise.all([first, second]);
  assert.equal(app.elements.projectTopic.value, 'One generated idea');
  assert.equal(attempts, 1);
  assert.equal(app.elements.randomPrompt.disabled, false);
});

test('a refreshed dashboard restores a project using its exact persisted request id', async () => {
  const sessionData = new Map();
  const firstApp = configuredApp('project', true, { sessionData });
  const response = deferred();
  let submittedBody;
  firstApp.setRequest((requestPath, options = {}) => {
    assert.equal(requestPath, '/api/projects');
    submittedBody = JSON.parse(options.body);
    return response.promise;
  });
  const submission = firstApp.submitProject();
  await Promise.resolve();
  assert.equal(sessionData.has('framevault.pendingProjectSubmission'), true);
  assert.equal(submittedBody.request_id, firstApp.state.pendingProjectSubmission.request_id);

  const project = {
    id: 'fixture-restored-project', request_id: submittedBody.request_id,
    topic: submittedBody.topic, status: 'planning', scenes: [], updated_at: 20,
  };
  const refreshed = configuredApp('project', true, { sessionData });
  refreshed.state.authenticated = true;
  refreshed.setRequest((requestPath) => {
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [] });
    if (requestPath === `/api/projects?request_id=${encodeURIComponent(submittedBody.request_id)}`) {
      return Promise.resolve({ projects: [project] });
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await refreshed.loadProjects(false);
  assert.equal(refreshed.state.currentProjectID, project.id);
  assert.equal(refreshed.state.displayedProjectID, project.id);
  assert.equal(refreshed.elements.statusBadge.textContent, 'planning');
  assert.equal(refreshed.state.pendingProjectSubmission, null);
  assert.equal(sessionData.has('framevault.pendingProjectSubmission'), false);

  response.resolve(project);
  await submission;
});

test('a safe post-refresh retry reuses the stored request body and id even when loaded defaults differ', async () => {
  const sessionData = new Map([['framevault.pendingProjectSubmission', JSON.stringify({
    request_id: 'stored-project-request-0001',
    body: {
      topic: 'Stored project idea', category: 'Nature', model: 'previous/video-model',
      modal_account_id: 'previous-account',
    },
  })]]);
  const app = configuredApp('project', true, { sessionData });
  app.elements.projectTopic.value = '';
  app.state.authenticated = true;
  let submitted;
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') {
      submitted = JSON.parse(options.body);
      return Promise.resolve({
        id: 'fixture-safe-retry', request_id: submitted.request_id,
        topic: submitted.topic, status: 'planning', scenes: [],
      });
    }
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [] });
    if (requestPath === '/api/projects?request_id=stored-project-request-0001') {
      return Promise.resolve({ projects: [] });
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });

  await app.loadProjects(false);
  assert.equal(app.elements.projectTopic.value, 'Stored project idea');
  app.elements.model.value = 'fixture/video';
  await app.submitProject();
  assert.equal(submitted.request_id, 'stored-project-request-0001');
  assert.equal(submitted.topic, 'Stored project idea');
  assert.equal(submitted.category, 'Nature');
  assert.equal(submitted.model, 'previous/video-model');
  assert.equal(submitted.modal_account_id, 'previous-account');
});

test('a project that appears after an empty refresh lookup is recovered from its live snapshot', async () => {
  const requestID = 'late-project-request-0001';
  const sessionData = new Map([['framevault.pendingProjectSubmission', JSON.stringify({
    request_id: requestID,
    body: { topic: 'Late accepted idea', category: 'Nature', model: 'fixture/video' },
  })]]);
  const app = configuredApp('project', true, { sessionData });
  app.state.authenticated = true;
  app.setRequest((requestPath) => {
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [] });
    if (requestPath === `/api/projects?request_id=${requestID}`) return Promise.resolve({ projects: [] });
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await app.loadProjects(false);
  assert.equal(app.state.pendingProjectSubmission.request_id, requestID);
  assert.equal(app.state.projectRecoveryAttemptedID, requestID);

  const project = {
    id: 'late-accepted-project', request_id: requestID,
    topic: 'Late accepted idea', status: 'planning', scenes: [], updated_at: 50,
  };
  app.handleJobSnapshot('project', {
    data: JSON.stringify({ id: project.id, project }),
  });
  await flushUI();
  assert.equal(app.state.currentProjectID, project.id);
  assert.equal(app.state.pendingProjectSubmission, null);
  assert.equal(sessionData.has('framevault.pendingProjectSubmission'), false);
});

test('project submission still works if sessionStorage is unavailable and refresh restores from history', async () => {
  const app = configuredApp('project', true, { storageThrows: true });
  app.state.authenticated = true;
  const project = {
    id: 'fixture-no-storage-project', topic: 'Test project topic', status: 'planning', scenes: [],
  };
  let submitted;
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') {
      submitted = JSON.parse(options.body);
      return Promise.resolve({ ...project, request_id: submitted.request_id });
    }
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [project] });
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await app.submitProject();
  assert.equal(typeof submitted.request_id, 'string');
  assert.ok(submitted.request_id.length >= 16);
  assert.equal(app.state.pendingProjectSubmission, null);

  app.state.projects = [];
  app.state.currentProjectID = '';
  app.state.displayedProjectID = '';
  app.state.currentProjectRecord = null;
  await app.loadProjects(false);
  assert.equal(app.state.currentProjectID, project.id);
  assert.equal(app.state.displayedProjectID, project.id);
});

test('uncertain project submission retries with the same request id and does not attach a same-topic project', async () => {
  const app = configuredApp('project');
  app.elements.promptCategory.value = '0';
  app.bindEvents();
  const requestIDs = [];
  let attempt = 0;
  const intendedProject = {
    id: 'fixture-intended-project', request_id: '', topic: 'Test project topic', status: 'planning', scenes: [],
  };
  const otherProject = {
    id: 'fixture-other-project', request_id: 'another-request-id-0001', topic: 'Test project topic', status: 'queued', scenes: [],
  };
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') {
      const body = JSON.parse(options.body);
      requestIDs.push(body.request_id);
      if (attempt++ === 0) return Promise.reject(new TypeError('Connection interrupted'));
      return Promise.resolve({ ...intendedProject, request_id: body.request_id });
    }
    if (requestPath.startsWith('/api/projects?request_id=')) {
      if (attempt === 1) return Promise.resolve({ projects: [otherProject] });
      throw new Error('Unexpected recovery lookup after the accepted response');
    }
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [] });
    throw new Error(`Unexpected request: ${requestPath}`);
  });

  await app.submitProject();
  assert.equal(app.state.currentProjectID, '');
  assert.equal(app.state.pendingProjectSubmission.request_id, requestIDs[0]);
  assert.notEqual(app.state.currentProjectID, otherProject.id);
  assert.equal(app.elements.projectTopic.value, 'Test project topic');

  app.elements.promptCategory.value = '2';
  app.elements.promptCategory.emit('input');
  app.elements.promptCategory.value = '0';
  app.elements.promptCategory.emit('input');
  assert.equal(app.state.pendingProjectFormEdited, true);
  app.elements.projectTopic.value = 'Test project topic';
  await app.submitProject();
  assert.equal(requestIDs[1], requestIDs[0]);
  assert.equal(app.state.currentProjectID, intendedProject.id);
});

test('refresh selects an accepted active project ahead of older terminal projects', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  app.setRequest((requestPath) => {
    if (requestPath === '/api/projects') {
      return Promise.resolve({ projects: [
        { id: 'fixture-new-failed', status: 'failed', error: 'Fixture failure', scenes: [], updated_at: 30 },
        { id: 'fixture-active', status: 'planning', scenes: [], updated_at: 20 },
        { id: 'fixture-completed', status: 'completed', scenes: [], updated_at: 10 },
      ] });
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await app.loadProjects(false);
  assert.equal(app.state.currentProjectID, 'fixture-active');
  assert.equal(app.state.displayedProjectID, 'fixture-active');
  assert.equal(app.elements.projectStatus.hidden, false);
  assert.match(app.elements.projectStatusDetail.textContent, /in progress/i);
});

test('a list refresh during submission does not replace the local submitting card', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  const post = deferred();
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') return post.promise;
    if (requestPath === '/api/projects') {
      return Promise.resolve({ projects: [{ id: 'older-active-project', status: 'planning', scenes: [] }] });
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  const submission = app.submitProject();
  const requestID = app.state.pendingProjectSubmission.request_id;
  await app.loadProjects(false);
  assert.equal(app.state.currentProjectID, '');
  assert.equal(app.state.displayedProjectID, '');
  assert.equal(app.elements.statusBadge.textContent, 'Submitting');
  assert.equal(app.elements.projectStatusDetail.textContent, 'Generating story and five-scene script...');

  post.resolve({ id: 'accepted-project', request_id: requestID, status: 'planning', scenes: [] });
  await submission;
  assert.equal(app.state.currentProjectID, 'accepted-project');
});

test('project refresh verifies omitted selections and clears items deleted or moved to Vault', async () => {
  const app = configuredApp('project');
  const oldProject = { id: 'older-project', status: 'completed', topic: 'Older', scenes: [] };
  app.state.authenticated = true;
  app.renderProject(oldProject, true);
  let detailExists = true;
  app.setRequest((requestPath) => {
    if (requestPath === '/api/projects') return Promise.resolve({ projects: [] });
    if (requestPath === `/api/projects/${oldProject.id}`) {
      return detailExists
        ? Promise.resolve(oldProject)
        : Promise.reject(Object.assign(new Error('Project not found'), { status: 404 }));
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });
  await app.loadProjects(false);
  assert.equal(app.state.currentProjectID, oldProject.id, 'projects beyond the 24-row list remain open after detail verification');
  assert.equal(app.state.projects[0].id, oldProject.id);

  detailExists = false;
  await app.loadProjects(false);
  assert.equal(app.state.currentProjectID, '');
  assert.equal(app.state.displayedProjectID, '');
  assert.equal(app.state.projects.length, 0);
  assert.equal(app.elements.projectStatus.hidden, true);
});

test('an older list response cannot reinsert a project deleted by the newest response', async () => {
  const app = configuredApp('project');
  const oldProject = { id: 'deleted-after-list-start', status: 'completed', scenes: [] };
  app.state.authenticated = true;
  app.renderProject(oldProject, true);
  const oldList = deferred();
  let listCount = 0;
  app.setRequest((requestPath) => {
    if (requestPath === '/api/projects') {
      listCount++;
      return listCount === 1 ? oldList.promise : Promise.resolve({ projects: [] });
    }
    if (requestPath === `/api/projects/${oldProject.id}`) {
      return Promise.reject(Object.assign(new Error('Project not found'), { status: 404 }));
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });

  const older = app.loadProjects(false);
  const newer = app.loadProjects(false);
  await newer;
  oldList.resolve({ projects: [oldProject] });
  await older;
  assert.equal(app.state.projects.length, 0);
  assert.equal(app.state.currentProjectID, '');
  assert.equal(app.elements.projectStatus.hidden, true);
});

test('late project-list responses cannot restore state after the auth epoch changes', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  const list = deferred();
  app.setRequest(() => list.promise);
  const loading = app.loadProjects(false);
  app.state.authenticated = false;
  app.state.authGeneration++;
  app.state.projects = [];
  app.state.currentProjectID = '';
  app.state.currentProjectRecord = null;
  list.resolve({ projects: [{ id: 'late-project', status: 'failed', scenes: [] }] });
  await loading;
  assert.equal(app.state.projects.length, 0);
  assert.equal(app.state.currentProjectID, '');
});

test('late random idea responses cannot replace a prompt after logout and a new session', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  app.state.authGeneration = 4;
  const oldResponse = deferred();
  const newResponse = deferred();
  let calls = 0;
  app.setRequest(() => {
    calls++;
    return calls === 1 ? oldResponse.promise : newResponse.promise;
  });

  const oldRequest = app.generateRandomPrompt();
  assert.equal(app.elements.randomPrompt.disabled, true);
  app.showLoggedOut();
  assert.equal(app.elements.randomPrompt.disabled, false);
  app.state.authenticated = true;
  app.state.authGeneration++;
  const newRequest = app.generateRandomPrompt();
  assert.equal(app.elements.randomPrompt.disabled, true);
  oldResponse.resolve({ prompt: 'Old session idea' });
  await oldRequest;
  assert.equal(app.elements.randomPrompt.disabled, true, 'the old request cannot clear the new request busy state');
  newResponse.resolve({ prompt: 'New session idea' });
  await newRequest;
  assert.equal(app.elements.projectTopic.value, 'New session idea');
  assert.equal(app.elements.randomPrompt.disabled, false);
});

test('late project POST responses cannot overwrite a pending request after logout and login', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  app.state.authGeneration = 10;
  const response = deferred();
  app.setRequest(() => response.promise);
  const submission = app.submitProject();
  const oldRequestID = app.state.pendingProjectSubmission.request_id;

  app.state.authenticated = false;
  app.state.authGeneration++;
  app.state.submissionPending = false;
  app.state.projectSubmissionPending = false;
  app.state.pendingProjectSubmission = {
    request_id: 'new-session-project-request-0001',
    body: { topic: 'A different session idea', category: 'Nature', model: 'fixture/video' },
  };
  app.state.authenticated = true;
  app.state.authGeneration++;
  app.state.submissionPending = true;
  app.state.projectSubmissionPending = true;
  app.state.currentProjectID = 'new-session-project';
  app.state.displayedProjectID = 'new-session-project';
  app.state.currentProjectRecord = { id: 'new-session-project', status: 'planning', scenes: [] };

  response.resolve({ id: 'old-session-project', request_id: oldRequestID, status: 'planning', scenes: [] });
  await submission;
  assert.equal(app.state.currentProjectID, 'new-session-project');
  assert.equal(app.state.pendingProjectSubmission.request_id, 'new-session-project-request-0001');
  assert.equal(app.state.submissionPending, true);
});

test('logout during an uncertain-submission lookup cannot show a stale project failure', async () => {
  const app = configuredApp('project');
  app.state.authenticated = true;
  const lookup = deferred();
  let lookupStarted = false;
  app.setRequest((requestPath, options = {}) => {
    if (requestPath === '/api/projects' && options.method === 'POST') {
      return Promise.reject(new TypeError('Connection interrupted'));
    }
    if (requestPath.startsWith('/api/projects?request_id=')) {
      lookupStarted = true;
      return lookup.promise;
    }
    throw new Error(`Unexpected request: ${requestPath}`);
  });

  const submission = app.submitProject();
  await flushUI();
  assert.equal(lookupStarted, true);
  app.showLoggedOut();
  lookup.resolve({ projects: [] });
  await submission;
  assert.equal(app.state.authenticated, false);
  assert.equal(app.state.projectSubmissionFailed, false);
  assert.equal(app.state.currentProjectID, '');
  assert.equal(app.elements.projectStatus.hidden, true);
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

test('overview creation actions select their format while preserving ideas and saved results', () => {
  const app = configuredApp('project');
  app.elements.promptCategory.value = '2';
  const project = { id: 'fixture-overview-project', topic: 'Saved project', status: 'processing', scenes: [], events: [] };
  const clip = { id: 'fixture-overview-clip', prompt: 'Saved clip', status: 'failed', events: [] };
  app.renderProject(project);
  app.setStatusRecord(clip);
  app.bindEvents();
  assert.deepEqual(Array.from(app.elements.createModeButtons, (button) => button.dataset.createMode).sort(), ['project', 'single']);

  for (const mode of ['single', 'project']) {
    app.elements.createModeButtons.find((button) => button.dataset.createMode === mode).emit('click');
    assert.equal(app.state.mode, mode);
    assert.equal(app.state.currentView, 'generate');
    assert.equal(app.elements.prompt.value, 'Test single clip prompt');
    assert.equal(app.elements.projectTopic.value, 'Test project topic');
    assert.equal(app.elements.promptCategory.value, '2');
    assert.equal(app.state.displayedProjectID, project.id);
    assert.equal(app.state.displayedGenerationID, clip.id);
  }
});

test('overview creation actions respect the password gate and signed-out session', () => {
  const app = configuredApp('project');
  app.bindEvents();
  app.applyPasswordGate(true);
  assert.ok(app.elements.createModeButtons.length);
  for (const button of app.elements.createModeButtons) {
    assert.equal(button.disabled, true);
    button.emit('click');
  }
  assert.equal(app.state.mode, 'project');
  assert.equal(app.state.currentView, '');
  app.applyPasswordGate(false);
  app.state.authenticated = false;
  app.elements.createModeButtons.find((button) => button.dataset.createMode === 'single').emit('click');
  assert.equal(app.state.mode, 'project');
  assert.equal(app.state.currentView, '');
});

test('recent activity combines real projects and clips in creation order and opens the correct result', () => {
  const app = configuredApp('single');
  const project = { id: 'fixture-recent-project', topic: 'Newest project', status: 'processing', model: 'fixture/video', created_at: 9, scenes: [], events: [] };
  app.state.projects = [project, { ...project, id: 'fixture-earlier-project', topic: 'Earlier project', created_at: 3 }];
  app.state.generations = [8, 7, 6, 5, 4].map((created) => ({
    id: `fixture-recent-clip-${created}`, prompt: `Clip ${created}`, status: 'processing', created_at: created, model: 'fixture/video', events: [],
  }));
  app.renderRecent();
  assert.deepEqual(Array.from(app.elements.recentList.children, (row) => row.dataset.id), [
    project.id, 'fixture-recent-clip-8', 'fixture-recent-clip-7', 'fixture-recent-clip-6', 'fixture-recent-clip-5',
  ]);
  assert.match(collectText(app.elements.recentList.children[0]), /30-second project/);
  assert.match(collectText(app.elements.recentList.children[1]), /Single clip/);
  assert.match(collectText(app.elements.recentList.children[0]), /Not recorded/);
  findTextButton(app.elements.recentList, 'Newest project').emit('click');
  assert.equal(app.state.mode, 'project');
  assert.equal(app.state.currentView, 'generate');
  assert.equal(app.state.displayedProjectID, project.id);
  findTextButton(app.elements.recentList, 'Clip 8').emit('click');
  assert.equal(app.state.mode, 'single');
  assert.equal(app.state.displayedGenerationID, 'fixture-recent-clip-8');
  assert.equal(app.elements.projectTopic.value, 'Test project topic');
  assert.equal(app.elements.prompt.value, 'Test single clip prompt');
});

test('recent project activity updates on list loads and accepted live snapshots', async () => {
  const app = configuredApp('project');
  const project = { id: 'fixture-recent-live', topic: 'Live project', status: 'planning', model: 'fixture/video', created_at: 9, updated_at: 10, scenes: [], events: [] };
  app.setRequest(() => Promise.resolve({ projects: [project] }));
  await app.loadProjects(false);
  assert.match(collectText(app.elements.recentList), /Live project/);
  assert.match(collectText(app.elements.recentList), /planning/);
  app.handleJobSnapshot('project', { data: JSON.stringify({ id: project.id, project: { ...project, status: 'generating', updated_at: 12 } }) });
  assert.match(collectText(app.elements.recentList), /generating/);
  app.handleJobSnapshot('project', { data: JSON.stringify({ id: project.id, project }) });
  assert.match(collectText(app.elements.recentList), /generating/);
});

function historyFixtures(app) {
  app.state.projects = [
    { id: 'fixture-nature-project', topic: 'River landscape', category: 'Nature', status: 'completed', model: 'fixture/cinema', created_at: 3, scenes: [] },
    { id: 'fixture-active-project', topic: 'Mountain expedition', status: 'combining', model: 'fixture/action', created_at: 2, scenes: [] },
    { id: 'fixture-error-project', topic: 'City sunset', status: 'error', model: 'fixture/cinema', created_at: 1, scenes: [] },
  ];
  app.state.generations = [
    { id: 'fixture-nature-clip', prompt: 'River wildlife', status: 'completed', model: 'fixture/nature', created_at: 3, events: [] },
    { id: 'fixture-active-clip', prompt: 'Ocean waves', status: 'download_failed', model: 'fixture/action', created_at: 2, events: [] },
    { id: 'fixture-failed-clip', prompt: 'City skyline', status: 'failed', model: 'fixture/cinema', created_at: 1, events: [] },
  ];
}

test('History search and status filters cover loaded projects and clips without provider requests', () => {
  const app = configuredApp('project');
  historyFixtures(app);
  let requests = 0;
  app.setRequest(() => { requests++; return Promise.resolve({}); });
  app.bindEvents();
  app.elements.historySearch.value = '  RIVER  ';
  app.elements.historySearch.emit('input');
  assert.match(collectText(app.elements.projectHistory), /River landscape/);
  assert.match(collectText(app.elements.historyGrid), /River wildlife/);
  assert.doesNotMatch(collectText(app.elements.projectHistory), /Mountain expedition/);
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 2 of 6 loaded items: 1 project, 1 clip/);

  app.elements.historySearch.value = '';
  app.elements.historyStatus.value = 'active';
  app.elements.historyStatus.emit('change');
  assert.match(collectText(app.elements.projectHistory), /Mountain expedition/);
  assert.match(collectText(app.elements.historyGrid), /Ocean waves/);
  assert.doesNotMatch(collectText(app.elements.historyGrid), /River wildlife/);
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 2 of 6 loaded items/);

  app.elements.historyStatus.value = 'failed';
  app.elements.historyStatus.emit('change');
  assert.match(collectText(app.elements.projectHistory), /City sunset/);
  assert.match(collectText(app.elements.historyGrid), /City skyline/);
  assert.equal(requests, 0);
});

test('History format filters and reset preserve loaded data, pagination, creation input and result selection', () => {
  const app = configuredApp('project');
  historyFixtures(app);
  Object.assign(app.state, { historyHasMore: true, historyNextCursor: 'fixture-cursor', historyNextOffset: 24, displayedProjectID: 'fixture-selected-project' });
  app.bindEvents();
  app.elements.historySearch.value = 'nature';
  app.elements.historyStatus.value = 'completed';
  app.elements.historyFormat.value = 'project';
  app.elements.historyFormat.emit('change');
  assert.match(collectText(app.elements.projectHistory), /River landscape/);
  assert.equal(app.elements.historyGrid.hidden, true);
  assert.match(app.elements.historyResultsSummary.textContent, /1 project, 0 clips/);
  assert.equal(app.elements.historyClearFilters.disabled, false);

  app.elements.historyFormat.value = 'single';
  app.elements.historyFormat.emit('change');
  assert.equal(app.elements.projectHistory.hidden, true);
  assert.match(collectText(app.elements.historyGrid), /River wildlife/);
  app.elements.historyClearFilters.emit('click');
  assert.equal(app.elements.historySearch.value, '');
  assert.equal(app.elements.historyStatus.value, 'all');
  assert.equal(app.elements.historyFormat.value, 'all');
  assert.equal(app.elements.historyClearFilters.disabled, true);
  assert.equal(app.document.activeElement, app.elements.historySearch);
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 6 of 6 loaded items/);
  assert.equal(app.state.projects.length, 3);
  assert.equal(app.state.generations.length, 3);
  assert.equal(app.state.historyNextCursor, 'fixture-cursor');
  assert.equal(app.state.historyNextOffset, 24);
  assert.equal(app.state.displayedProjectID, 'fixture-selected-project');
  assert.equal(app.elements.projectTopic.value, 'Test project topic');
});

test('History keeps bounded pagination available when no loaded records match', async () => {
  const app = configuredApp('project', true, { stubHistory: false });
  historyFixtures(app);
  Object.assign(app.state, { historyHasMore: true, historyNextCursor: 'fixture-page-2', historyNextOffset: 24 });
  app.elements.historySearch.value = 'Older matching clip';
  const page = deferred();
  const calls = [];
  app.setRequest((requestPath) => { calls.push(requestPath); return page.promise; });
  app.renderHistory();
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 0 of 6 loaded items/);
  assert.match(app.elements.historyResultsSummary.textContent, /older clips/);
  assert.match(collectText(app.elements.historyGrid), /No loaded items match/);
  const more = findTextButton(app.elements.historyGrid, 'Load more');
  assert.ok(more);
  const loading = more.emit('click');
  assert.equal(more.disabled, true);
  assert.equal(calls.length, 1);
  const query = new URL(calls[0], 'http://localhost').searchParams;
  assert.equal(query.get('limit'), '24');
  assert.equal(query.get('before'), 'fixture-page-2');
  assert.equal(query.has('query'), false);
  page.resolve({ generations: [{ id: 'fixture-older-match', prompt: 'Older matching clip', status: 'failed', created_at: 0 }], has_more: true, next_page: 'fixture-page-3' });
  await Promise.all(loading);
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 1 of 7 loaded items/);
  assert.match(collectText(app.elements.historyGrid), /Older matching clip/);
  assert.equal(app.elements.historySearch.value, 'Older matching clip');
  assert.equal(app.state.historyNextCursor, 'fixture-page-3');
  assert.equal(findTextButton(app.elements.historyGrid, 'Load more').disabled, false);
});

test('accepted live snapshots update filtered History counts for both formats', () => {
  const app = configuredApp('project');
  historyFixtures(app);
  app.elements.historyStatus.value = 'completed';
  app.renderHistory();
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 2 of 6 loaded items/);
  const project = app.state.projects[1];
  app.handleJobSnapshot('project', { data: JSON.stringify({ id: project.id, project: { ...project, status: 'completed', updated_at: 10 } }) });
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 3 of 6 loaded items: 2 projects, 1 clip/);
  const clip = app.state.generations[1];
  app.handleJobSnapshot('generation', { data: JSON.stringify({ id: clip.id, generation: { ...clip, status: 'completed', updated_at: 10 } }) });
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 4 of 6 loaded items: 2 projects, 2 clips/);
});

test('unrelated live updates keep completed History media and action focus attached', () => {
  const app = configuredApp('project');
  const completedProject = { id: 'fixture-watched-project', topic: 'Watched project', status: 'completed', final_video_ready: true, scenes: [], created_at: 1 };
  const completedClip = { id: 'fixture-watched-clip', prompt: 'Watched clip', status: 'completed', video_ready: true, created_at: 1, events: [] };
  const activeProject = { id: 'fixture-other-project', topic: 'Other project', status: 'generating', updated_at: 1, scenes: [], created_at: 2 };
  const activeClip = { id: 'fixture-other-clip', prompt: 'Other clip', status: 'processing', updated_at: 1, created_at: 2, events: [] };
  app.state.projects = [activeProject, completedProject];
  app.state.generations = [activeClip, completedClip];
  app.elements.historyStatus.value = 'completed';
  app.renderHistory();
  const projectCard = app.state.historyCards.get(`project:${completedProject.id}`).node;
  const clipCard = app.state.historyCards.get(`single:${completedClip.id}`).node;
  const projectVideo = projectCard.children[0].children[0];
  const clipVideo = clipCard.children[0].children[0];
  assert.equal(projectVideo.tagName, 'video');
  assert.equal(clipVideo.tagName, 'video');
  const upload = findTextButton(clipCard, 'Upload to YouTube');
  upload.focus();
  const ancestry = new Set();
  for (const video of [projectVideo, clipVideo]) {
    for (let node = video; node; node = node.parent) ancestry.add(node);
  }
  const before = new Map([...ancestry].map((node) => [node, { detachments: node.detachments, replacements: node.replacements }]));

  app.handleJobSnapshot('project', { data: JSON.stringify({ id: activeProject.id, project: { ...activeProject, progress: 60, updated_at: 2 } }) });
  app.handleJobSnapshot('generation', { data: JSON.stringify({ id: activeClip.id, generation: { ...activeClip, status: 'completed', video_ready: true, updated_at: 2 } }) });
  app.handleJobSnapshot('project', { data: JSON.stringify({ id: activeProject.id, project: { ...activeProject, status: 'completed', final_video_ready: true, updated_at: 3 } }) });
  assert.equal(app.state.historyCards.get(`project:${completedProject.id}`).node, projectCard);
  assert.equal(app.state.historyCards.get(`single:${completedClip.id}`).node, clipCard);
  assert.equal(app.document.activeElement, upload);
  for (const [node, counts] of before) {
    assert.equal(node.detachments, counts.detachments, 'unchanged video ancestry must stay attached');
    assert.equal(node.replacements, counts.replacements, 'unchanged video ancestry must not be cleared');
  }
  assert.match(app.elements.historyResultsSummary.textContent, /Showing 4 of 4 loaded items/);
  app.state.projects = [];
  app.state.generations = [];
  app.renderHistory();
  assert.equal(app.state.historyCards.size, 0, 'caches must not retain unloaded private records');
});

test('late History pages cannot insert private results or clear a newer session load', async () => {
  const app = configuredApp('project', true, { stubHistory: false });
  const oldPage = deferred();
  app.setRequest(() => oldPage.promise);
  const oldLoad = app.loadHistory(false);
  app.showLoggedOut();
  app.state.authenticated = true;
  const newPage = deferred();
  app.setRequest(() => newPage.promise);
  const newLoad = app.loadHistory(false);
  oldPage.resolve({ generations: [{ id: 'fixture-private-old', prompt: 'Previous session', status: 'completed' }] });
  await oldLoad;
  assert.equal(app.state.generations.length, 0);
  assert.equal(app.state.historyLoading, true);
  newPage.resolve({ generations: [] });
  await newLoad;
  assert.equal(app.state.historyLoading, false);
  assert.equal(app.elements.refreshHistory.disabled, false);
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
    assert.equal(error.textContent, mode === 'project'
      ? 'Fixture submission failed The project may still be running. Retrying this same request is safe.'
      : 'Fixture submission failed');
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
