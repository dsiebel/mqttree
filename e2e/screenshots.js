// Regenerates the README screenshots. Expects a running instance with an empty
// database and a broker reachable at MQTT_BROKER_HOST:1883.
const { chromium } = require('@playwright/test');
const fs = require('fs');
const path = require('path');

const base = process.env.MQTTREE_URL || 'http://127.0.0.1:8080';
const brokerHost = process.env.MQTT_BROKER_HOST || '127.0.0.1';
const outDir = path.resolve(__dirname, '../docs/screenshots');

const samples = [
  ['home/kitchen', '{"summary":true,"lamp":"on","n":3}'],
  ['home/kitchen/temperature', '{"value":21.5,"unit":"C"}'],
  ['home/kitchen/humidity', '{"value":48}'],
  ['home/livingroom/lamp/state', 'ON'],
  ['home/livingroom/lamp/brightness', '180'],
  ['system/network/router/uptime', '865555'],
  ['sensor/outside/temp', '-2.4'],
  ['zigbee2mqtt/bridge/state', 'online'],
  ['zigbee2mqtt/bedroom/sensor', '{"temperature":19.2,"humidity":44}'],
];

async function api(pathname, options = {}) {
  const response = await fetch(base + pathname, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (!response.ok) {
    throw new Error(`${options.method || 'GET'} ${pathname} failed: ${response.status}`);
  }
  return response.status === 204 ? null : response.json();
}

async function seed() {
  const state = await api('/api/bootstrap');
  let connection = state.connections.find((candidate) => candidate.name === 'demo');
  if (!connection) {
    connection = await api('/api/connections', {
      method: 'POST',
      body: JSON.stringify({
        name: 'demo',
        host: brokerHost,
        port: 1883,
        topic_filter: '#',
        qos: 1,
        enabled: true,
      }),
    });
  }
  await new Promise((resolve) => setTimeout(resolve, 800));
  for (const [topic, payload] of samples) {
    await api('/api/publish', {
      method: 'POST',
      body: JSON.stringify({ connection_id: connection.id, topic, payload, qos: 1, retain: true }),
    });
  }
  await new Promise((resolve) => setTimeout(resolve, 500));
}

async function capture(browser, { name, viewport, scheme, mobile }) {
  const context = await browser.newContext({
    viewport,
    colorScheme: scheme,
    deviceScaleFactor: 2,
    isMobile: Boolean(mobile),
    hasTouch: Boolean(mobile),
  });
  const page = await context.newPage();
  await page.goto(base);
  await page.waitForSelector('.node');
  await page.waitForTimeout(500);
  await page.locator('.node[data-path="home"] > .row').click();
  await page.waitForTimeout(200);
  await page.locator('.node[data-path="home/kitchen"] > .row').click();
  await page.waitForTimeout(700);
  await page.screenshot({ path: path.join(outDir, `${name}.png`) });
  await context.close();
}

(async () => {
  fs.mkdirSync(outDir, { recursive: true });
  await seed();
  const browser = await chromium.launch();
  await capture(browser, { name: 'tree-dark', viewport: { width: 1280, height: 800 }, scheme: 'dark' });
  await capture(browser, { name: 'tree-light', viewport: { width: 1280, height: 800 }, scheme: 'light' });
  await capture(browser, { name: 'mobile', viewport: { width: 390, height: 844 }, scheme: 'dark', mobile: true });
  await browser.close();
  console.log('screenshots written to', outDir);
})();
