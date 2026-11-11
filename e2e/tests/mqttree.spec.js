const { test, expect } = require('@playwright/test');

// The suite is serial: every test builds on the connection and data created by
// the previous one, which mirrors how the tool is used.
test.describe.configure({ mode: 'serial' });

const BROKER_HOST = process.env.MQTT_BROKER_HOST || '127.0.0.1';
const BROKER_PORT = Number(process.env.MQTT_BROKER_PORT || 1883);

async function bootstrap(page) {
  const response = await page.request.get('/api/bootstrap');
  expect(response.ok()).toBeTruthy();
  return response.json();
}

async function openSettings(page) {
  await page.locator('#open-settings').click();
  await expect.poll(() => page.locator('#settings').evaluate((dialog) => dialog.open)).toBe(true);
}

async function closeSettings(page) {
  await page.locator('#settings').evaluate((dialog) => dialog.close());
}

test('starts with the expected controls', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle('mqttree');
  await expect(page.locator('#tree-empty')).toBeVisible();
  await expect(page.locator('#toggle-all')).toBeVisible();
  await expect(page.locator('#theme-toggle')).toBeVisible();
  await expect(page.locator('.tree-toolbar #search')).toBeVisible();
  await expect(page.locator('#clear-search')).toBeHidden();
  await expect(page.locator('#status-topics')).toBeVisible();
  await expect(page.locator('#status-db')).toContainText(/\d+ (B|KiB|MiB)/);
});

test('creates a connection and shows broker activity', async ({ page }) => {
  await page.goto('/');
  await openSettings(page);
  await page.locator('#conn-name').fill('e2e');
  await page.locator('#conn-host').fill(BROKER_HOST);
  await page.locator('#conn-port').fill(String(BROKER_PORT));
  await page.getByRole('button', { name: 'Save connection' }).click();
  await expect(page.locator('#connection-select')).toHaveValue(/\d+/);
  await closeSettings(page);

  await expect(page.locator('#status-dot')).toHaveClass(/on/, { timeout: 15000 });

  const state = await bootstrap(page);
  const connection = state.connections.find((candidate) => candidate.name === 'e2e');
  expect(connection).toBeTruthy();

  const publish = await page.request.post('/api/publish', {
    data: {
      connection_id: connection.id,
      topic: 'home/kitchen',
      payload: JSON.stringify({ value: 21.5, unit: 'C' }),
      qos: 1,
      retain: false,
    },
  });
  expect(publish.ok()).toBeTruthy();

  await expect(page.locator('.node[data-path="home"]')).toBeVisible();
  await page.locator('.node[data-path="home"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen"] > .row').click();

  await expect(page.locator('#topic-path')).toHaveText('home/kitchen');
  await expect(page.locator('#latest-payload')).toContainText('21.5');
  await expect(page.locator('#latest-payload .j-key').first()).toBeVisible();
  await expect(page.locator('#history .msg')).toHaveCount(1);
});

test('copies a payload to the clipboard', async ({ page }) => {
  await page.goto('/');
  await page.locator('.node[data-path="home"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen"] > .row').click();
  await page.locator('#copy-payload').click();
  await expect(page.locator('#toast')).toContainText('payload copied');
  const copied = await page.evaluate(() => navigator.clipboard.readText());
  expect(copied).toContain('21.5');
});

test('filters with highlighting and clears the filter', async ({ page }) => {
  await page.goto('/');
  await page.locator('#search').fill('kitch');
  await expect(page.locator('.tree mark').first()).toHaveText('kitch');
  await expect(page.locator('#clear-search')).toBeVisible();
  await page.locator('#clear-search').click();
  await expect(page.locator('#search')).toHaveValue('');
  await expect(page.locator('#clear-search')).toBeHidden();
});

test('expands and collapses all topics', async ({ page }) => {
  await page.goto('/');
  await page.locator('#toggle-all').click();
  await expect(page.locator('#toggle-all')).toHaveAttribute('data-state', 'collapse');
  await expect.poll(() => page.locator('.node.open').count()).toBeGreaterThan(0);
  await page.locator('#toggle-all').click();
  await expect(page.locator('#toggle-all')).toHaveAttribute('data-state', 'expand');
  await expect.poll(() => page.locator('.node.open').count()).toBe(0);
});

test('switches theme and remembers the choice', async ({ page }) => {
  await page.goto('/');
  const initial = await page.evaluate(() =>
    document.documentElement.dataset.theme ||
    (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'));
  const expected = initial === 'dark' ? 'light' : 'dark';
  await page.locator('#theme-toggle').click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', expected);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', expected);
});

test('collapses history and pages it ten at a time', async ({ page }) => {
  const state = await bootstrap(page);
  const connection = state.connections.find((candidate) => candidate.name === 'e2e');
  for (let value = 0; value < 12; value += 1) {
    const response = await page.request.post('/api/publish', {
      data: {
        connection_id: connection.id,
        topic: 'home/kitchen/humidity',
        payload: JSON.stringify({ value }),
        qos: 1,
        retain: false,
      },
    });
    expect(response.ok()).toBeTruthy();
  }
  await expect.poll(async () => (await bootstrap(page)).stats.messages).toBe(13);

  await page.goto('/');
  await page.locator('.node[data-path="home"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen/humidity"] > .row').click();
  await expect(page.locator('#topic-path')).toHaveText('home/kitchen/humidity');

  // Collapsed by default, and the first page stops at ten entries.
  await expect(page.locator('#toggle-history')).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('#history-body')).toBeHidden();
  await page.locator('#toggle-history').click();
  await expect(page.locator('#history-body')).toBeVisible();
  await expect(page.locator('#history .msg')).toHaveCount(10);
  await expect(page.locator('#load-older')).toBeVisible();

  await page.locator('#load-older').click();
  await expect(page.locator('#history .msg')).toHaveCount(12);
  await expect(page.locator('#load-older')).toBeHidden();
});

test('diffs the latest payload on live updates', async ({ page }) => {
  const state = await bootstrap(page);
  const connection = state.connections.find((candidate) => candidate.name === 'e2e');
  await page.goto('/');
  await page.locator('.node[data-path="home"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen"] > .row > .chev-btn').click();
  await page.locator('.node[data-path="home/kitchen/humidity"] > .row').click();
  await expect(page.locator('#latest-payload')).toContainText('11');

  const response = await page.request.post('/api/publish', {
    data: {
      connection_id: connection.id,
      topic: 'home/kitchen/humidity',
      payload: JSON.stringify({ value: 99 }),
      qos: 1,
      retain: false,
    },
  });
  expect(response.ok()).toBeTruthy();

  await expect(page.locator('#latest-payload .diff-add')).toContainText('99');
  await expect(page.locator('#latest-payload .diff-del')).toContainText('11');
  // The highlight fades out and the pane settles on the new payload alone.
  await expect(page.locator('#latest-payload .diff-del')).toHaveCount(0, { timeout: 8000 });
  await expect(page.locator('#latest-payload')).toContainText('99');
  await expect(page.locator('#latest-payload')).not.toContainText('11');
});

test('clears stored data for the connection', async ({ page }) => {
  page.on('dialog', (dialog) => dialog.accept());
  await page.goto('/');
  await expect(page.locator('.node[data-path="home"]')).toBeVisible();
  await openSettings(page);
  await page.locator('#clear-tree').click();
  await expect(page.locator('#toast')).toContainText('stored messages cleared');
  await closeSettings(page);
  await expect(page.locator('#tree-empty')).toBeVisible();
  await expect(page.locator('#status-topics')).toHaveText('0 topics');
  await expect(page.locator('#status-messages')).toHaveText('0 messages');
  const state = await bootstrap(page);
  expect(state.stats.messages).toBe(0);
  expect(state.connections).toHaveLength(1);
});
