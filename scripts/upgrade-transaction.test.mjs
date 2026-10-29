import assert from 'node:assert/strict';
import test from 'node:test';

for (const frontend of ['vite-frontend', 'vite-frontend-v2']) {
  const { createUpgradeController } = await import(`../${frontend}/src/utils/upgrade-transaction.ts`);
  const accepted = { status: 202, code: 0, data: { id: 'bounded', state: 'QUEUED' } };
  for (const state of ['FAILED', 'ROLLBACK_FAILED', 'RECOVERY_REQUIRED']) {
    test(`${frontend}: ${state} preserves backend failure details`, async () => {
      const result = await createUpgradeController().run({
        intervalMs: 1, deadlineMs: 100, onProgress() {},
        request: async ({ method }) => method === 'POST' ? accepted : {
          status: 200, code: 0, data: { id: 'bounded', state, error: 'activation failed', errors: ['restore failed'], logs: ['worker stopped'] },
        },
      });
      assert.equal(result.state, state);
      assert.equal(result.error, 'activation failed');
      assert.deepEqual(result.errors, ['restore failed']);
    });
  }
  for (const mode of ['disconnect', '401', 'pending', 'wrong-id', 'wrong-code', 'unknown-state', 'hung']) {
    test(`${frontend}: ${mode} expires unconfirmed with ID, never success`, { timeout: 1000 }, async () => {
      let posts = 0;
      const started = Date.now();
      let aborted = false;
      const result = await createUpgradeController().run({
        intervalMs: 1, deadlineMs: 35, requestTimeoutMs: 8, onProgress() {},
        request: async ({ method, signal }) => {
          if (method === 'POST') { posts++; return accepted; }
          if (mode === 'disconnect') throw new Error('EOF');
          if (mode === '401') return { status: 401, code: 401 };
          if (mode === 'hung') return new Promise(() => signal.addEventListener('abort', () => { aborted = true; }));
          return { status: 200, code: mode === 'wrong-code' ? 9 : 0, data: {
            id: mode === 'wrong-id' ? 'other' : 'bounded', state: mode === 'pending' ? 'VERIFYING' : mode === 'unknown-state' ? 'done' : 'COMMITTED',
          } };
        },
      });
      assert.equal(result.state, 'UNCONFIRMED');
      assert.equal(result.id, 'bounded');
      assert.match(result.error, /bounded/);
      assert.equal(posts, 1);
      assert.ok(Date.now() - started < 500);
      if (mode === 'hung') assert.equal(aborted, true);
    });
  }
  test(`${frontend}: Docker HTTP409 explains external image upgrade without polling`, async () => {
    let calls = 0;
    const result = await createUpgradeController().run({
      onProgress() {}, request: async () => { calls++; return { status: 409, code: 409, msg: 'image deployment', data: { state: 'IMAGE_UPGRADE_REQUIRED' } }; },
    });
    assert.equal(result.state, 'IMAGE_UPGRADE_REQUIRED');
    assert.match(result.error, /Docker.*镜像/);
    assert.equal(calls, 1);
  });
  for (const response of [
    { status: 500, code: 0, msg: 'server failure' },
    { status: 202, code: 9, msg: 'worker unavailable' },
    { status: 200, code: 0, data: { id: 'bad', state: 'COMMITTED' } },
  ]) {
    test(`${frontend}: rejected POST ${JSON.stringify(response)} never polls`, async () => {
      let calls = 0;
      const result = await createUpgradeController().run({ onProgress() {}, request: async () => { calls++; return response; } });
      assert.notEqual(result.state, 'COMMITTED');
      assert.equal(calls, 1);
      assert.equal(result.error, response.msg || '启动升级失败 (HTTP 200)');
    });
  }
  test(`${frontend}: cancellation before the scheduled transport cannot send a POST`, async () => {
    const controller = createUpgradeController();
    let calls = 0;
    const run = controller.run({ onProgress() {}, request: async () => { calls++; return accepted; } });
    controller.cancel();
    assert.equal((await run).state, 'CANCELLED');
    assert.equal(calls, 0);
  });
  test(`${frontend}: a response arriving beyond the absolute deadline cannot commit`, async () => {
    let poll = false;
    const clock = Date.now;
    let offset = 0;
    Date.now = () => clock() + offset;
    let result;
    try {
      result = await createUpgradeController().run({
        intervalMs: 1, deadlineMs: 1000, onProgress() {},
        request: async ({ method }) => {
          if (method === 'POST') return accepted;
          poll = true;
          offset = 2000; // response wins the microtask race, but not the absolute deadline
          return { status: 200, code: 0, data: { id: 'bounded', state: 'COMMITTED' } };
        },
      });
    } finally { Date.now = clock; }
    assert.equal(poll, true);
    assert.equal(result.state, 'UNCONFIRMED');
  });
  test(`${frontend}: lost POST acknowledgement never automatically resubmits`, async () => {
    let calls = 0;
    const result = await createUpgradeController().run({ onProgress() {}, request: async () => { calls++; throw new Error('EOF'); } });
    assert.equal(result.state, 'UNCONFIRMED');
    assert.equal(calls, 1);
  });
  test(`${frontend}: cancellation aborts in-flight HTTP and suppresses late progress`, async () => {
    const controller = createUpgradeController();
    const progress = [];
    let aborted = false;
    let resolvePoll;
    const run = controller.run({
      intervalMs: 1, deadlineMs: 1000, onProgress: p => progress.push(p),
      request: ({ method, signal }) => method === 'POST' ? Promise.resolve(accepted) : new Promise(resolve => {
        resolvePoll = resolve;
        signal.addEventListener('abort', () => { aborted = true; });
      }),
    });
    while (!resolvePoll) await new Promise(resolve => setTimeout(resolve, 1));
    const busy = await controller.run({ onProgress() { assert.fail('duplicate progress'); }, request() { assert.fail('duplicate POST'); } });
    assert.equal(busy.state, 'BUSY');
    controller.cancel();
    assert.equal((await run).state, 'CANCELLED');
    assert.equal(aborted, true);
    resolvePoll({ status: 200, code: 0, data: { id: 'bounded', state: 'COMMITTED' } });
    await new Promise(resolve => setTimeout(resolve, 5));
    assert.deepEqual(progress.map(p => p.state), ['QUEUED']);
  });
  test(`${frontend}: cancellation during polling delay prevents another GET`, async () => {
    const controller = createUpgradeController();
    let calls = 0;
    const run = controller.run({ intervalMs: 50, onProgress() {}, request: async () => { calls++; return accepted; } });
    await new Promise(resolve => setTimeout(resolve, 5));
    controller.cancel();
    assert.equal((await run).state, 'CANCELLED');
    await new Promise(resolve => setTimeout(resolve, 60));
    assert.equal(calls, 1);
  });
  test(`${frontend}: disconnect and restart 401 stay VERIFYING until a durable commit`, async () => {
    const progress = [];
    let polls = 0;
    let posts = 0;
    const result = await createUpgradeController().run({
      intervalMs: 1, deadlineMs: 100, onProgress: p => progress.push(p),
      request: async ({ method }) => {
        if (method === 'POST') {
          posts++;
          return { status: 202, code: 0, data: { id: 'restart', state: 'PREPARING' } };
        }
        polls++;
        if (polls === 1) throw new Error('connection refused');
        if (polls === 2) return { status: 401, code: 401, msg: 'token unavailable during restart' };
        return { status: 200, code: 0, data: { id: 'restart', state: 'COMMITTED' } };
      },
    });
    assert.equal(result.state, 'COMMITTED');
    assert.equal(posts, 1);
    assert.deepEqual(progress.map(p => p.state), ['PREPARING', 'VERIFYING', 'VERIFYING', 'COMMITTED']);
  });
  test(`${frontend}: rollback is a failure, never continues to a later COMMITTED`, { timeout: 1000 }, async () => {
    let polls = 0;
    const result = await createUpgradeController().run({
      intervalMs: 1, deadlineMs: 100, onProgress() {},
      request: async ({ method }) => method === 'POST'
        ? { status: 202, code: 0, data: { id: 'tx-rollback', state: 'QUEUED' } }
        : { status: 200, code: 0, data: { id: 'tx-rollback', state: ++polls === 1 ? 'ROLLED_BACK' : 'COMMITTED', error: 'health probe failed' } },
    });
    assert.equal(result.state, 'ROLLED_BACK');
    assert.equal(result.error, 'health probe failed');
    assert.equal(polls, 1);
  });
  test(`${frontend}: POST once, poll durable status, only COMMITTED succeeds`, async () => {
    const calls = [];
    const states = ['PREPARED', 'ACTIVATING', 'VERIFYING', 'COMMITTED'];
    const progress = [];
    const controller = createUpgradeController();
    const result = await controller.run({
      tag: 'v9.1', intervalMs: 1, deadlineMs: 1000,
      onProgress: value => progress.push(value),
      request: async ({ method, path, body, signal, timeoutMs }) => {
        calls.push({ method, path, body });
        assert.ok(signal instanceof AbortSignal);
        assert.ok(timeoutMs > 0);
        return method === 'POST'
          ? { status: 202, code: 0, data: { id: 'tx-123', state: 'QUEUED', tag: 'v9.1' } }
          : { status: 200, code: 0, data: { id: 'tx-123', state: states.shift(), tag: 'v9.1' } };
      },
    });
    assert.equal(result.state, 'COMMITTED');
    assert.equal(calls.filter(c => c.method === 'POST').length, 1);
    assert.deepEqual(calls[0], { method: 'POST', path: 'version/upgrade', body: { tag: 'v9.1' } });
    assert.ok(calls.slice(1).every(c => c.path === 'version/upgrade-status?id=tx-123'));
    assert.deepEqual(progress.map(p => p.state), ['QUEUED', 'PREPARED', 'ACTIVATING', 'VERIFYING', 'COMMITTED']);
  });
}
