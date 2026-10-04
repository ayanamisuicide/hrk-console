const { test, expect } = require('@playwright/test');

const sampleLog = [
  '2026-10-03 10:00:00 [INFO] Core: started',
  '2026-10-03 10:00:01 [ERROR] Core: failed request',
  '2026-10-03 10:00:02 [ERROR] Core: retry failed',
];

test.beforeEach(async ({ page }) => {
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const response = {
      '/api/auth/me': { username: 'tester', role: 'operator' },
      '/api/version': { version: 'v2.2.17' },
      '/api/status': { running: true, pid: 123, uptime: '1ч', version: '2.2.0', herokuDir: '/srv/Heroku', logReady: true },
      '/api/logs': { lines: sampleLog },
      '/api/insights': { logCounts: { error: 2, warning: 0 } },
      '/api/metrics': { rssBytes: 104857600 },
      '/api/system': { supported: true, cpuPercent: 20, cpuCores: 8, memoryTotalBytes: 1000, memoryUsedBytes: 500, memoryAvailableBytes: 500, diskTotalBytes: 1000, diskUsedBytes: 300, diskFreeBytes: 700, os: 'linux', arch: 'amd64', hostname: 'test', kernel: 'test', uptimeSeconds: 1000, sampledAt: new Date().toISOString() },
      '/api/system/history': { points: [{ at: '2026-10-03T10:00:00Z', cpu: 20, memory: 50, disk: 30, pid: 1 }, { at: '2026-10-03T10:00:30Z', cpu: 40, memory: 55, disk: 30, pid: 2 }] },
      '/api/incidents': { incidents: [{ start: '2026-10-03 10:00:01', end: '2026-10-03 10:00:02', level: 'ERROR', module: 'Core', title: 'failed request', count: 2, context: sampleLog[0] }] },
    };
    if (path === '/api/events') return route.abort();
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(response[path] || {}) });
  });
});

for (const width of [390, 1440]) for (const theme of ['dark', 'light']) {
  test(`screens and interactions ${width}px ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.addInitScript((selectedTheme) => localStorage.setItem('hkc-theme', selectedTheme), theme);
    await page.goto('/');
    await expect(page.locator('#version')).toContainText('Панель v2.2.17');
    await expect(page.locator('[data-view="overview"], #overview-view')).toHaveCount(0);
    await expect(page.locator('#logs-view')).toBeVisible();
    for (const view of ['logs', 'incidents', 'system']) {
      if (view === 'incidents' && await page.locator('#journal-nav-toggle').getAttribute('aria-expanded') === 'false') await page.locator('#journal-nav-toggle').click();
      await page.locator(`[data-view="${view}"]`).click();
      await expect(page.locator(`#${view}-view`)).toBeVisible();
      if (view === 'incidents') await expect(page.locator('.incident-card')).toBeVisible();
      await page.screenshot({ path: testInfo.outputPath(`${view}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' });
    }
    await expect(page.locator('#history-chart svg')).toBeVisible();
    await page.locator('[data-view="logs"]').click();
    await page.locator('.line-bookmark').first().click();
    await page.locator('#bookmarks-only').click();
    await expect(page.locator('#log .line')).toHaveCount(1);
    if (await page.locator('#journal-nav-toggle').getAttribute('aria-expanded') === 'false') await page.locator('#journal-nav-toggle').click();
    await page.locator('[data-view="incidents"]').click();
    await page.locator('.incident-card').first().click();
    await expect(page.locator('#logs-view')).toBeVisible();
    await expect(page.locator('#time-from')).not.toHaveValue('');
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
    expect(overflow).toBe(false);
  });
}

test('visual regression of main views', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-03T10:00:00Z') });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/');
  await expect(page.locator('#version')).toContainText('v2.2.17');
  for (const view of ['logs', 'incidents', 'system']) {
    if (view === 'incidents' && await page.locator('#journal-nav-toggle').getAttribute('aria-expanded') === 'false') await page.locator('#journal-nav-toggle').click();
    await page.locator(`[data-view="${view}"]`).click();
    await expect(page.locator(`#${view}-view`)).toBeVisible();
    if (view === 'incidents') await expect(page.locator('.incident-card')).toBeVisible();
    await expect(page.locator(`#${view}-view`)).toHaveScreenshot(`${view}.png`, { animations: 'disabled', maxDiffPixelRatio: 0.01 });
  }
});

test('admin terminal requires confirmation and shows execution metadata', async ({ page }) => {
  let executions = 0;
  await page.addInitScript(() => {
    sessionStorage.setItem('hkc-admin-token', 'test-token');
    localStorage.setItem('hkc-admin-tree', JSON.stringify(['service']));
  });
  await page.route('**/api/admin/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const body = path === '/api/admin/overview' ? { users: [], invites: [], bot: { running: false, herokuDir: '/srv/Heroku' } }
      : path === '/api/admin/audit' ? { events: [] }
      : path === '/api/admin/backups' ? { backups: [] }
      : path === '/api/admin/terminal' ? { output: 'ok', actor: 'tester', exitCode: 0, durationMs: 12 } : {};
    if (path === '/api/admin/terminal') {
      executions++;
      expect(route.request().postDataJSON()).toEqual({ command: 'pwd', confirmed: true });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.goto('/admin/');
  await page.locator('#terminal-command').fill('pwd');
  await page.locator('#terminal-run').click();
  await expect(page.locator('#confirm-dialog')).toBeVisible();
  expect(executions).toBe(0);
  await page.locator('#confirm-dialog [value="cancel"]').click();
  expect(executions).toBe(0);
  await page.locator('#terminal-run').click();
  await page.locator('#confirm-accept').click();
  await expect(page.locator('#terminal-output')).toContainText('ok');
  await expect(page.locator('#terminal-meta')).toContainText('tester · exit 0 · 12 мс');
  expect(executions).toBe(1);
});

test('maintenance mode and scheduler are manageable from admin', async ({ page }) => {
  let maintenance = {enabled: false, message: ''};
  const schedules = [];
  await page.addInitScript(() => {
    sessionStorage.setItem('hkc-admin-token', 'test-token');
    localStorage.setItem('hkc-admin-tree', JSON.stringify(['operations']));
  });
  await page.route('**/api/admin/**', async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let status = 200;
    let body = path === '/api/admin/overview' ? {users: [], invites: [], bot: {running: false, herokuDir: '/srv/Heroku'}}
      : path === '/api/admin/audit' ? {events: []}
      : path === '/api/admin/backups' ? {backups: []}
      : path === '/api/admin/config/history' ? {history: []}
      : path === '/api/admin/security' ? {rateLimiter: {}, features: {}}
      : path === '/api/admin/maintenance' ? maintenance
      : path === '/api/admin/schedules' ? {schedules} : {};
    if (path === '/api/admin/maintenance' && request.method() === 'PUT') {
      maintenance = request.postDataJSON();
      body = maintenance;
    }
    if (path === '/api/admin/schedules' && request.method() === 'POST') {
      const input = request.postDataJSON();
      schedules.push({id: 'schedule-1', action: input.action, runAt: input.runAt, status: 'pending'});
      body = schedules[0]; status = 201;
    }
    return route.fulfill({status, contentType: 'application/json', body: JSON.stringify(body)});
  });
  await page.goto('/admin/');
  await page.locator('#maintenance-enabled').check();
  await page.locator('#maintenance-message').fill('Плановые работы');
  await page.locator('#maintenance-form button[type="submit"]').click();
  await expect(page.locator('#maintenance-state')).toHaveText('Включён');
  await expect(page.locator('[data-bot-action="start"]')).toBeDisabled();

  const future = new Date(Date.now() + 10 * 60 * 1000);
  const local = new Date(future.getTime() - future.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  await page.locator('#schedule-action').selectOption('stop');
  await page.locator('#schedule-run-at').fill(local);
  await page.locator('#schedule-form button[type="submit"]').click();
  await expect(page.locator('#schedule-list')).toContainText('Остановка');
  await expect(page.locator('#schedule-list')).toContainText('ожидает');
});

test('config changes are previewed without exposing values', async ({ page }) => {
  let savedPayload;
  await page.addInitScript(() => {
    sessionStorage.setItem('hkc-admin-token', 'test-token');
    localStorage.setItem('hkc-admin-tree', JSON.stringify(['history']));
  });
  await page.route('**/api/admin/**', async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body = path === '/api/admin/overview' ? {users: [], invites: [], bot: {running: false}}
      : path === '/api/admin/audit' ? {events: []}
      : path === '/api/admin/backups' ? {backups: []}
      : path === '/api/admin/config/history' ? {history: []}
      : path === '/api/admin/security' ? {rateLimiter: {}, features: {}}
      : path === '/api/admin/maintenance' ? {enabled: false}
      : path === '/api/admin/schedules' ? {schedules: []}
      : path === '/api/admin/config' && request.method() === 'GET' ? {configured: {api_id: true, api_hash: true, redis_uri: false, db_uri: false, app_name: true}}
      : path === '/api/admin/config/validate' ? {valid: true, changes: [{key: 'api_hash', change: 'changed'}], restartRequired: true}
      : {};
    if (path === '/api/admin/config' && request.method() === 'PATCH') {
      savedPayload = request.postDataJSON();
      body = {ok: true, message: 'saved'};
    }
    return route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify(body)});
  });
  await page.goto('/admin/');
  await expect(page.locator('#config-status')).toContainText('API hash');
  await page.locator('[name="api_hash"]').fill('0123456789abcdef0123456789abcdef');
  await page.locator('#config-preview').click();
  await expect(page.locator('#config-preview-output')).toContainText('api_hash: будет изменён');
  await expect(page.locator('body')).not.toContainText('0123456789abcdef0123456789abcdef');
  await page.locator('#config-form button[type="submit"]').click();
  await expect(page.locator('#confirm-dialog')).toBeVisible();
  await page.locator('#confirm-accept').click();
  await expect.poll(() => savedPayload).toEqual({api_hash: '0123456789abcdef0123456789abcdef'});
  await expect(page.locator('[name="api_hash"]')).toHaveValue('');
});

test('live resource endpoints refresh every second', async ({ page }) => {
  const hits = {status: 0, system: 0, history: 0};
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const key = path === '/api/system/history' ? 'history' : path.slice('/api/'.length);
    if (key in hits) hits[key]++;
    return route.fallback();
  });
  await page.clock.install({ time: new Date('2026-10-03T10:00:00Z') });
  await page.goto('/');
  await expect(page.locator('#status-label')).toContainText('бот запущен');
  await page.locator('[data-view="system"]').click();
  await expect(page.locator('#history-chart svg')).toBeVisible();
  await page.locator('#history-chart svg').evaluate((svg) => { svg.dataset.identity = 'persistent'; });
  const before = { ...hits };
  await page.clock.runFor(2200);
  await expect.poll(() => hits.history).toBeGreaterThan(before.history);
  for (const key of Object.keys(hits)) expect(hits[key], key).toBeGreaterThan(before[key]);
  await expect(page.locator('#history-chart svg[data-identity="persistent"]')).toHaveCount(1);
});

test('journal incident drawer opens, persists and activates its nested view', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#journal-nav-toggle')).toHaveAttribute('aria-expanded', 'false');
  await page.locator('#journal-nav-toggle').click();
  await expect(page.locator('#journal-nav-toggle')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.nav-subitem')).toBeVisible();
  await page.locator('.nav-subitem').click();
  await expect(page.locator('#incidents-view')).toBeVisible();
  await expect(page.locator('#journal-nav')).toHaveClass(/section-active/);
  await page.reload();
  await expect(page.locator('#journal-nav-toggle')).toHaveAttribute('aria-expanded', 'true');
});
