const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const staticDir = path.join(__dirname, '..', 'static');
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
      this.style = {};
      this.value = '';
      this.textContent = '';
      this.className = '';
      this.hidden = false;
      this.disabled = false;
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
      if (this.tagName === 'select' && !this.value && nodes[0]) this.value = nodes[0].value;
    }
    replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
    querySelector() { return null; }
    querySelectorAll() { return []; }
    contains(node) { return node === this || this.children.some((child) => child.contains(node)); }
    closest() { return this.parent ??= new Element(); }
    focus() { document.activeElement = this; }
    reset() { this.onReset?.(); }
    load() {}
    pause() {}
    addEventListener() {}
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
      matchMedia: () => ({ matches: compact }),
      setTimeout: (callback) => { timers.push(callback); return timers.length; },
      clearTimeout() {},
      requestAnimationFrame: (callback) => callback(),
      addEventListener() {},
      scrollTo() {},
    },
    uiTestRequest: () => Promise.reject(new Error('Unexpected request in UI test')),
  });
  const instrumented = source.replace('  bootstrap();', `
    const testPollGeneration = pollGeneration;
    const testPollProject = pollProject;
    request = (...args) => globalThis.uiTestRequest(...args);
    pollGeneration = () => {};
    pollProject = () => {};
    loadHistory = async () => {};
    loadProjects = async () => {};
    globalThis.ui = {
      state, elements, setGenerationMode, syncGenerationModeDisplay,
      submitGeneration, submitProject, updateModelOptions,
      syncNavigationAccessibility, setNavigationOpen,
      pollGeneration: testPollGeneration, pollProject: testPollProject
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

for (const kind of ['Generation', 'Project']) {
  test(`late ${kind.toLowerCase()} poll responses cannot replace a newer job`, async () => {
    for (const fails of [false, true]) {
      const app = createApp();
      app.state.authenticated = true;
      const pending = deferred();
      app.setRequest(() => pending.promise);
      await app[`poll${kind}`]('fixture-old');
      const polling = app.takeTimer()();
      app.state[`current${kind}ID`] = 'fixture-new';
      if (fails) pending.reject(new Error('Fixture old poll failed'));
      else pending.resolve({ id: 'fixture-old', status: 'failed', scenes: [], events: [] });
      await polling;
      assert.equal(app.state[`current${kind}ID`], 'fixture-new');
      assert.equal(app.state[`displayed${kind}ID`], '');
      assert.equal(app.elements[kind === 'Project' ? 'projectError' : 'statusError'].textContent, '');
    }
  });
}
