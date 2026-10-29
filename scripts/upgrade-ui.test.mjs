import assert from 'node:assert/strict';
import test from 'node:test';
import vm from 'node:vm';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';

for (const frontend of ['vite-frontend', 'vite-frontend-v2']) {
  const root = path.resolve(frontend);
  const require = createRequire(path.join(root, 'package.json'));
  const ts = require('typescript');
  const axios = require('axios').default;
  function fixture({ states = ['COMMITTED'], deployment, postReply, hung = false } = {}) {
    let cursor = 0;
    const cells = [];
    const effects = [];
    const cleanups = [];
    const events = {};
    const storage = new Map([['token', 'secret-token'], ['admin', 'true'], ['panel_addresses', JSON.stringify([{ name: 'remote', address: 'https://remote.example/proxy', inx: true }])], ['panel_address_current', 'remote']]);
    const localStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
    const window = { localStorage, location: { hostname: 'frontend.example', pathname: '/dashboard' }, innerWidth: 1200, addEventListener: (name, callback) => { events[name] = callback; }, removeEventListener() {}, dispatchEvent() {} };
    const calls = [];
    const toasts = [];
    const cache = new Map();
    const React = {
      useState(initial) { const index = cursor++; if (!(index in cells)) cells[index] = initial; return [cells[index], value => { cells[index] = typeof value === 'function' ? value(cells[index]) : value; }]; },
      useRef(initial) { const index = cursor++; if (!(index in cells)) cells[index] = { current: initial }; return cells[index]; },
      useEffect(effect) { const index = cursor++; if (!(index in cells)) { cells[index] = true; effects.push(effect); } },
    };
    const jsx = (type, props) => ({ type, props });
    const external = {
      react: { __esModule: true, default: React, ...React },
      'react/jsx-runtime': { jsx, jsxs: jsx, Fragment: 'Fragment' },
      'react-router-dom': { useNavigate: () => () => {}, useLocation: () => ({ pathname: '/dashboard' }) },
      'react-hot-toast': { toast: { success: msg => toasts.push(['success', msg]), error: msg => toasts.push(['error', msg]) } },
      '@/api': { getVersionInfo: async () => ({ code: 0, data: { server: 'v1', deployment, onlineUpgradeSupported: deployment === 'docker' ? false : undefined } }), getLatestVersionInfo: async () => ({ code: 0, data: { tag: 'v2' } }) },
      '@/config/site': { siteConfig: {}, getCachedConfig: async () => 'false' },
      '@/utils/logout': { safeLogout() {} }, '@/components/icons': { Logo: 'Logo' },
    };
    function load(file) {
      if (cache.has(file)) return cache.get(file);
      const module = { exports: {} };
      cache.set(file, module.exports);
      const source = fs.readFileSync(file, 'utf8').replaceAll('import.meta.env', '({ VITE_API_BASE: "https://remote.example/proxy" })').replaceAll('import.meta', '({ env: {} })');
      const code = ts.transpileModule(source, { fileName: file, compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2020, esModuleInterop: true } }).outputText;
      const localRequire = id => {
        if (external[id]) return external[id];
        if (id.startsWith('@heroui/')) return new Proxy({}, { get: (_, name) => name === 'useDisclosure' ? () => ({ isOpen: false, onOpen() {}, onOpenChange() {} }) : String(name) });
        if (id.startsWith('@/') || id.startsWith('./')) {
          const base = id.startsWith('@/') ? path.join(root, 'src', id.slice(2)) : path.resolve(path.dirname(file), id);
          return load(fs.existsSync(base + '.ts') ? base + '.ts' : base + '.tsx');
        }
        return require(id);
      };
      vm.runInNewContext(`(function(require,module,exports){${code}\n})`, { window, localStorage, setTimeout, clearTimeout, AbortController, console, CustomEvent: class {} })(localRequire, module, module.exports);
      cache.set(file, module.exports);
      return module.exports;
    }
    const realController = load(path.join(root, 'src/utils/upgrade-transaction.ts')).createUpgradeController;
    external['@/utils/upgrade-transaction'] = { createUpgradeController: () => {
      const controller = realController();
      return { cancel: controller.cancel, run: opts => controller.run({ ...opts, intervalMs: 1, deadlineMs: states.includes('COMMITTED') ? 1500 : 200, requestTimeoutMs: 100 }) };
    } };
    axios.defaults.adapter = async config => {
      calls.push(config);
      if (hung && config.method === 'get' && config.url.startsWith('version/upgrade-status')) return new Promise(() => {});
      let reply;
      if (config.method === 'post') reply = postReply || { status: 202, data: { code: 0, data: { id: 'ui-transaction', state: 'QUEUED' } } };
      else {
        const state = states.length > 1 ? states.shift() : states[0];
        reply = state === '401' ? { status: 401, data: { code: 401, msg: '无效的token或token已过期' } } : { status: 200, data: { code: 0, data: { id: 'ui-transaction', state, error: state === 'COMMITTED' ? undefined : 'real backend failure' } } };
      }
      return { ...reply, config, statusText: '', headers: {} };
    };
    const Admin = load(path.join(root, 'src/layouts/admin.tsx')).default;
    function render() { cursor = 0; return Admin({ children: null }); }
    const find = (node, predicate) => {
      if (!node || typeof node !== 'object') return undefined;
      if (predicate(node)) return node;
      const children = node.props?.children;
      for (const child of [children].flat(Infinity)) { const result = find(child, predicate); if (result) return result; }
    };
    render();
    for (const effect of effects.splice(0)) { const cleanup = effect(); if (cleanup) cleanups.push(cleanup); }
    return { calls, toasts, storage, events, window, axios, load, root, render, find, cleanups,
      async ready() { await new Promise(resolve => setTimeout(resolve, 0)); },
      button() { return find(render(), n => n.type === 'Button' && n.props.color === 'warning'); },
      log() { return find(render(), n => n.type === 'pre').props.children; },
    };
  }
  test(`${frontend}: actual admin entry double-click submits once, sets success only on COMMITTED`, async () => {
    const f = fixture(); await f.ready();
    const press = f.button().props.onPress;
    await Promise.all([press(), press()]);
    assert.equal(f.calls.filter(c => c.method === 'post').length, 1);
    assert.deepEqual(JSON.parse(f.calls[0].data), { tag: 'v2' });
    assert.equal(f.calls[0].baseURL, 'https://remote.example/proxy/api/v1/');
    assert.equal(f.calls[0].headers.Authorization, 'secret-token');
    assert.deepEqual(f.toasts.map(t => t[0]), ['success']);
    assert.match(f.log(), /COMMITTED/);
    f.cleanups.forEach(fn => fn());
  });
  for (const state of ['ROLLED_BACK', 'FAILED', 'ROLLBACK_FAILED', 'RECOVERY_REQUIRED', '401', 'VERIFYING']) {
    test(`${frontend}: admin ${state} never success and retains transaction ID`, async () => {
      const f = fixture({ states: [state] }); await f.ready(); await f.button().props.onPress();
      assert.equal(f.toasts.filter(t => t[0] === 'success').length, 0);
      assert.match(f.log(), /ui-transaction/);
      if (state === 'ROLLED_BACK') assert.match(f.log(), /已确认回滚/);
      if (state === '401' || state === 'VERIFYING') { assert.match(f.log(), /UNCONFIRMED/); assert.equal(f.button().props.isDisabled, true); }
      assert.equal(f.storage.get('token'), 'secret-token');
      f.cleanups.forEach(fn => fn());
    });
  }
  test(`${frontend}: Docker version capability shows image instructions, no POST`, async () => {
    const f = fixture({ deployment: 'docker' }); await f.ready();
    assert.equal(f.button().props.children, 'Docker 镜像升级说明');
    await f.button().props.onPress();
    assert.match(f.log(), /拉取新版镜像/);
    assert.equal(f.calls.length, 0);
    assert.equal(f.toasts.length, 0);
  });
  test(`${frontend}: HTTP409 Docker rejection preserves instructions, no success`, async () => {
    const f = fixture({ postReply: { status: 409, data: { code: 409, msg: 'external image only', data: { state: 'IMAGE_UPGRADE_REQUIRED' } } } });
    await f.ready(); await f.button().props.onPress();
    assert.equal(f.calls.length, 1);
    assert.match(f.log(), /Docker.*镜像/);
    assert.equal(f.toasts.filter(t => t[0] === 'success').length, 0);
  });
  for (const cancellation of ['unmount', 'pagehide']) {
    test(`${frontend}: ${cancellation} cancels actual admin polling and late UI effects`, async () => {
      const f = fixture({ hung: true }); await f.ready();
      const run = f.button().props.onPress();
      while (f.calls.length < 2) await new Promise(resolve => setTimeout(resolve, 1));
      if (cancellation === 'unmount') f.cleanups.forEach(fn => fn()); else f.events.pagehide();
      await run;
      assert.equal(f.calls.at(-1).signal.aborted, true);
      const count = f.calls.length;
      await new Promise(resolve => setTimeout(resolve, 50));
      assert.equal(f.calls.length, count);
      assert.equal(f.toasts.length, 0);
    });
  }
  test(`${frontend}: transport snapshots selected panel and token, preserves 401 without logout`, async () => {
    const f = fixture({ states: ['401'] }); await f.ready();
    const request = f.load(path.join(f.root, 'src/utils/upgrade-request.ts')).createUpgradeRequest();
    f.axios.defaults.baseURL = 'https://other.example/api/v1/';
    f.storage.set('token', 'other-token');
    const reply = await request({ method: 'GET', path: 'version/upgrade-status?id=old', signal: new AbortController().signal, timeoutMs: 10 });
    assert.equal(reply.status, 401);
    assert.equal(f.calls.at(-1).baseURL, 'https://remote.example/proxy/api/v1/');
    assert.equal(f.calls.at(-1).headers.Authorization, 'secret-token');
    assert.equal(f.storage.get('token'), 'other-token');
    assert.equal(f.window.location.href, undefined);
  });
}
