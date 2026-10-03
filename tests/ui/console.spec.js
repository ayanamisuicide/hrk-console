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
      '/api/diagnostics': { checks: [] },
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
    for (const view of ['overview', 'logs', 'incidents', 'system']) {
      await page.locator(`[data-view="${view}"]`).click();
      await expect(page.locator(`#${view}-view`)).toBeVisible();
      await page.screenshot({ path: testInfo.outputPath(`${view}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' });
    }
    await expect(page.locator('#history-chart svg')).toBeVisible();
    await page.locator('[data-view="logs"]').click();
    await page.locator('.line-bookmark').first().click();
    await page.locator('#bookmarks-only').click();
    await expect(page.locator('#log .line')).toHaveCount(1);
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
  for (const view of ['overview', 'logs', 'incidents', 'system']) {
    await page.locator(`[data-view="${view}"]`).click();
    await expect(page.locator(`#${view}-view`)).toBeVisible();
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
