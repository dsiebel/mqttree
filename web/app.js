const $ = (id) => document.getElementById(id);

const state = {
  connections: [],
  statuses: {},
  settings: null,
  stats: null,
  activeId: 0,
  topics: new Map(),
  nodes: new Map(),
  selected: "",
  history: [],
  oldestId: 0,
  expanded: new Set(),
  ws: null,
  wsAttempt: 0,
  filter: "",
};

// ---------------------------------------------------------------- utilities

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (response.status === 204) return null;
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `request failed (${response.status})`);
  return body;
}

function toast(message, isError = false) {
  const node = $('toast');
  node.textContent = message;
  node.classList.toggle('err', isError);
  node.classList.remove('hidden');
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => node.classList.add('hidden'), isError ? 6000 : 2600);
}

function fmtTime(ms) {
  const date = new Date(ms);
  const today = new Date();
  const sameDay = date.toDateString() === today.toDateString();
  return sameDay ? date.toLocaleTimeString() : date.toLocaleString();
}

const COPY_ICON = '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1"/><path d="M10.5 5.5v-2a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2"/></svg>';
const SUN_ICON = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"><circle cx="8" cy="8" r="2.6"/><path d="M8 1.5v1.2M8 13.3v1.2M1.5 8h1.2M13.3 8h1.2M3.4 3.4l.9.9M11.7 11.7l.9.9M12.6 3.4l-.9.9M4.3 11.7l-.9.9"/></svg>';
const MOON_ICON = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"><path d="M13 9.6A5.6 5.6 0 0 1 6.4 3a5.6 5.6 0 1 0 6.6 6.6z"/></svg>';

const THEME_KEY = 'mqttree.theme';

function resolvedTheme() {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored === 'dark' || stored === 'light') return stored;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function renderThemeButton(theme) {
  const button = $('theme-toggle');
  const dark = theme === 'dark';
  button.innerHTML = dark ? SUN_ICON : MOON_ICON;
  const label = dark ? 'Switch to light theme' : 'Switch to dark theme';
  button.title = label;
  button.setAttribute('aria-label', label);
}

function initTheme() {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored === 'dark' || stored === 'light') document.documentElement.dataset.theme = stored;
  renderThemeButton(resolvedTheme());
  $('theme-toggle').addEventListener('click', () => {
    const next = resolvedTheme() === 'dark' ? 'light' : 'dark';
    localStorage.setItem(THEME_KEY, next);
    document.documentElement.dataset.theme = next;
    renderThemeButton(next);
  });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (!localStorage.getItem(THEME_KEY)) renderThemeButton(resolvedTheme());
  });
}

async function copyText(text, message) {
  try {
    await navigator.clipboard.writeText(text);
    toast(message);
  } catch {
    toast('clipboard unavailable (requires HTTPS)', true);
  }
}

function escapeHtml(value) {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function highlightText(text, query) {
  if (!query) return escapeHtml(text);
  const needle = query.toLowerCase();
  const haystack = text.toLowerCase();
  let out = '';
  let index = 0;
  for (;;) {
    const at = haystack.indexOf(needle, index);
    if (at === -1) {
      out += escapeHtml(text.slice(index));
      return out;
    }
    out += escapeHtml(text.slice(index, at));
    out += `<mark>${escapeHtml(text.slice(at, at + needle.length))}</mark>`;
    index = at + needle.length;
  }
}

const SIDEBAR_KEY = 'mqttree.sidebar';
const SIDEBAR_MIN = 220;
const SIDEBAR_MAX = 720;

function setSidebarWidth(width) {
  const clamped = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, Math.round(width)));
  document.documentElement.style.setProperty('--sidebar-width', `${clamped}px`);
  return clamped;
}

function initSplitter() {
  const stored = Number(localStorage.getItem(SIDEBAR_KEY));
  if (stored) setSidebarWidth(stored);
  const splitter = $('splitter');
  let dragging = false;
  const finish = () => {
    if (!dragging) return;
    dragging = false;
    document.body.classList.remove('resizing');
    const width = splitter.previousElementSibling.getBoundingClientRect().width;
    localStorage.setItem(SIDEBAR_KEY, String(Math.round(width)));
  };
  splitter.addEventListener('pointerdown', (event) => {
    dragging = true;
    splitter.setPointerCapture(event.pointerId);
    document.body.classList.add('resizing');
    event.preventDefault();
  });
  splitter.addEventListener('pointermove', (event) => {
    if (dragging) setSidebarWidth(event.clientX);
  });
  splitter.addEventListener('pointerup', finish);
  splitter.addEventListener('pointercancel', finish);
  splitter.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    event.preventDefault();
    const current = splitter.previousElementSibling.getBoundingClientRect().width;
    const width = setSidebarWidth(current + (event.key === 'ArrowLeft' ? -16 : 16));
    localStorage.setItem(SIDEBAR_KEY, String(width));
  });
  splitter.addEventListener('dblclick', () => {
    document.documentElement.style.removeProperty('--sidebar-width');
    localStorage.removeItem(SIDEBAR_KEY);
  });
}

function bytesOf(b64) {
  if (!b64) return 0;
  const padding = b64.endsWith('==') ? 2 : b64.endsWith('=') ? 1 : 0;
  return Math.floor((b64.length * 3) / 4) - padding;
}

function sizeLabel(b64) {
  const bytes = bytesOf(b64);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function highlightJson(text) {
  return escapeHtml(text).replace(/("(?:\\u[a-fA-F0-9]{4}|\\[^u]|[^\\"])*"(?:\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (match) => {
    let kind = 'j-num';
    if (match.startsWith('"')) kind = /:\s*$/.test(match) ? 'j-key' : 'j-str';
    else if (match === 'true' || match === 'false') kind = 'j-bool';
    else if (match === 'null') kind = 'j-null';
    return `<span class="${kind}">${match}</span>`;
  });
}

function renderPayloadHtml(text, b64, truncated) {
  if (text) {
    const trimmed = text.trim();
    if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
      try {
        const pretty = JSON.stringify(JSON.parse(trimmed), null, 2) + (truncated ? '\n… truncated' : '');
        return highlightJson(pretty);
      } catch {
        /* not JSON, fall through */
      }
    }
    return escapeHtml(text) + (truncated ? '\n… truncated' : '');
  }
  return escapeHtml(`binary payload, ${sizeLabel(b64)}\nbase64: ${b64.slice(0, 512)}${b64.length > 512 ? '…' : ''}`);
}

// ------------------------------------------------------------------- tree

function insertSorted(container, element, name, leaf) {
  const siblings = [...container.children];
  for (const sibling of siblings) {
    const siblingName = sibling.dataset.name || '';
    const siblingLeaf = sibling.dataset.leaf === '1';
    if (siblingName > name || (siblingName === name && !leaf && siblingLeaf)) {
      container.insertBefore(element, sibling);
      return;
    }
  }
  container.appendChild(element);
}

function expandedStorageKey() { return `mqttree.expanded.${state.activeId}`; }

function loadExpanded() {
  try {
    const stored = JSON.parse(localStorage.getItem(expandedStorageKey()) || '[]');
    state.expanded = new Set(Array.isArray(stored) ? stored : []);
  } catch {
    state.expanded = new Set();
  }
}

function saveExpanded() {
  localStorage.setItem(expandedStorageKey(), JSON.stringify([...state.expanded]));
}

function setOpen(node, open) {
  node.el.classList.toggle('open', open);
  node.row.setAttribute('aria-expanded', String(open));
}

function createNode(path, name, depth, leaf, parentChildren) {
  const wrap = document.createElement('div');
  wrap.className = 'node' + (leaf ? ' leaf' : '');
  wrap.dataset.name = name;
  wrap.dataset.leaf = leaf ? '1' : '0';
  wrap.dataset.path = path;

  const row = document.createElement('div');
  row.className = 'row';
  row.tabIndex = 0;
  row.setAttribute('role', 'treeitem');
  row.style.setProperty('--depth', String(depth));
  row.dataset.path = path;

  const chevron = document.createElement('span');
  chevron.className = 'chev';
  chevron.textContent = '›';
  const chevronButton = document.createElement('button');
  chevronButton.type = 'button';
  chevronButton.className = 'chev-btn';
  chevronButton.setAttribute('aria-label', `Toggle ${name}`);
  chevronButton.append(chevron);
  const label = document.createElement('span');
  label.className = 'name';
  label.textContent = name;

  row.append(chevronButton, label);
  const children = document.createElement('div');
  children.className = 'children';
  children.setAttribute('role', 'group');
  wrap.append(row, children);
  insertSorted(parentChildren, wrap, name, leaf);

  const node = { path, name, depth, leaf, el: wrap, row, chevron, nameEl: label, childrenEl: children, topic: null, childNodes: [] };
  const activate = () => {
    // A topic that also has children must stay inspectable: the row selects it,
    // the chevron is the only thing that expands or collapses the branch.
    if (node.topic) {
      selectTopic(node.path);
      return;
    }
    if (children.childElementCount > 0) toggleCollapse(node);
  };
  chevronButton.addEventListener('click', (event) => {
    event.stopPropagation();
    toggleCollapse(node);
  });
  row.addEventListener('click', activate);
  row.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      activate();
    }
  });
  state.nodes.set(path, node);
  setOpen(node, state.expanded.has(path));
  return node;
}

function toggleCollapse(node) {
  const open = !node.el.classList.contains('open');
  setOpen(node, open);
  if (open) state.expanded.add(node.path);
  else state.expanded.delete(node.path);
  saveExpanded();
  updateToggleAll();
}

function expandAncestors(topic) {
  const segments = topic.split('/');
  let path = '';
  for (let index = 0; index < segments.length - 1; index += 1) {
    path = path ? `${path}/${segments[index]}` : segments[index];
    const node = state.nodes.get(path);
    if (node) {
      setOpen(node, true);
      state.expanded.add(path);
    }
  }
  saveExpanded();
  updateToggleAll();
}

function ensureTopicNode(topic) {
  const segments = topic.split('/');
  let parentChildren = $('tree');
  let path = '';
  let node = null;
  let parentNode = null;
  for (let index = 0; index < segments.length; index += 1) {
    path = path ? `${path}/${segments[index]}` : segments[index];
    const leaf = index === segments.length - 1;
    node = state.nodes.get(path);
    if (!node) {
      node = createNode(path, segments[index], index, leaf, parentChildren);
      if (parentNode) parentNode.childNodes.push(node);
    }
    parentNode = node;
    parentChildren = node.childrenEl;
  }
  return node;
}

function updateLeaf(node, message) {
  node.topic = message;
  let value = node.row.querySelector('.value');
  if (!value) {
    value = document.createElement('span');
    value.className = 'value';
    node.row.appendChild(value);
  }
  const preview = (message.text || '').replace(/\s+/g, ' ').slice(0, 48);
  value.textContent = preview;

  let retained = node.row.querySelector('.retained');
  if (message.retained) {
    if (!retained) {
      retained = document.createElement('span');
      retained.className = 'retained';
      retained.textContent = 'R';
      node.row.appendChild(retained);
    }
  } else if (retained) {
    retained.remove();
  }
}

function resetTree() {
  for (const timer of hotTimers.values()) clearTimeout(timer);
  hotTimers.clear();
  state.nodes.clear();
  state.topics.clear();
  $('tree').replaceChildren();
  updateTreeEmpty();
}

function updateTreeEmpty() {
  const hasTopics = state.nodes.size > 0;
  $('tree-empty').classList.toggle('hidden', hasTopics);
  $('tree-count').textContent = state.topics.size > 0 ? `${state.topics.size} topics` : '';
}

async function loadTree() {
  resetTree();
  loadExpanded();
  if (!state.activeId) {
    updateTreeEmpty();
    updateToggleAll();
    return;
  }
  try {
    const data = await api(`/api/tree?connection=${state.activeId}&limit=5000`);
    for (const topic of data.topics) {
      state.topics.set(topic.topic, topic);
      const node = ensureTopicNode(topic.topic);
      updateLeaf(node, { ...topic });
    }
    if (state.selected) {
      highlightSelection();
    }
    applyFilter(state.filter);
  } catch (error) {
    toast(error.message, true);
  }
  updateTreeEmpty();
  updateToggleAll();
}

function syncSearchClear() {
  $('clear-search').classList.toggle('hidden', $('search').value.length === 0);
}

function applyFilter(query) {
  state.filter = query.trim();
  const match = state.filter.toLowerCase();
  // Searching temporarily overrides the saved collapse state; clearing the
  // filter restores it without touching what the user stored.
  $('tree').classList.toggle('searching', Boolean(match));
  for (const node of state.nodes.values()) {
    node.nameEl.innerHTML = highlightText(node.name, state.filter);
  }
  const visit = (node) => {
    let keeps = !match || node.path.toLowerCase().includes(match);
    for (const child of node.childNodes) {
      if (visit(child)) keeps = true;
    }
    node.el.hidden = !keeps;
    return keeps;
  };
  for (const element of $('tree').children) {
    const node = state.nodes.get(element.dataset.path);
    if (node) visit(node);
  }
}

const hotTimers = new Map();

function markHot(node) {
  node.el.classList.add('hot');
  clearTimeout(hotTimers.get(node.path));
  hotTimers.set(node.path, setTimeout(() => {
    node.el.classList.remove('hot');
    hotTimers.delete(node.path);
  }, 1400));
}

// A message marks its topic and every ancestor, so activity is visible even
// when the branch that received it is collapsed.
function markTopicHot(topic) {
  let node = state.nodes.get(topic);
  while (node) {
    markHot(node);
    const separator = node.path.lastIndexOf('/');
    node = separator === -1 ? null : state.nodes.get(node.path.slice(0, separator));
  }
}

function highlightSelection() {
  for (const node of state.nodes.values()) {
    node.row.classList.toggle('selected', node.path === state.selected);
  }
}

// ------------------------------------------------------------- topic detail

async function selectTopic(topic) {
  state.selected = topic;
  expandAncestors(topic);
  document.body.classList.add('detail-open');
  highlightSelection();
  $('detail-empty').classList.add('hidden');
  $('detail-body').classList.remove('hidden');
  $('detail').classList.add('open');
  $('topic-path').textContent = topic;
  $('publish-topic').value = topic;

  const topicState = state.topics.get(topic);
  renderDetailHead(topicState);
  $('latest-payload').innerHTML = topicState
    ? renderPayloadHtml(topicState.text || '', topicState.payload_b64 || '', topicState.truncated)
    : 'no message stored yet';
  $('payload-meta').textContent = topicState
    ? `${sizeLabel(topicState.payload_b64 || '')} · ${topicState.retained ? 'retained · ' : ''}${fmtTime(topicState.received_at)}`
    : '';
  await loadHistory(topic);
}

function renderDetailHead(topicState) {
  const badges = $('topic-badges');
  badges.replaceChildren();
  if (!topicState) return;
  const add = (text, className) => {
    const span = document.createElement('span');
    span.className = `badge-pill ${className || ''}`;
    span.textContent = text;
    badges.appendChild(span);
  };
  if (topicState.retained) add('retained', 'retained');
  if (topicState.truncated) add('truncated', 'truncated');
  add(`qos ${topicState.qos ?? 0}`);
}

function historyItem(message, live = false) {
  const item = document.createElement('li');
  item.className = 'msg';
  const head = document.createElement('div');
  head.className = 'msg-head';
  const time = document.createElement('time');
  time.textContent = fmtTime(message.ts);
  const meta = document.createElement('span');
  meta.textContent = `${sizeLabel(message.payload_b64 || '')} · qos ${message.qos ?? 0}${message.retained ? ' · retained' : ''}${live ? ' · live' : ''}`;
  head.append(time, meta);
  const pre = document.createElement('pre');
  pre.innerHTML = renderPayloadHtml(message.text || '', message.payload_b64 || '', message.truncated);
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'btn icon copy-btn';
  copy.title = 'Copy payload';
  copy.setAttribute('aria-label', 'Copy payload');
  copy.innerHTML = COPY_ICON;
  copy.addEventListener('click', () => copyText(pre.textContent, 'payload copied'));
  item.append(head, pre, copy);
  return item;
}

function renderHistory() {
  const list = $('history');
  list.replaceChildren();
  for (const message of state.history) {
    list.appendChild(historyItem(message));
  }
  $('history-meta').textContent = `${state.history.length} message${state.history.length === 1 ? '' : 's'}`;
}

async function loadHistory(topic, before = 0) {
  try {
    const data = await api(`/api/messages?connection=${state.activeId}&topic=${encodeURIComponent(topic)}&limit=200${before ? `&before=${before}` : ''}`);
    if (before) {
      state.history = state.history.concat(data.messages);
    } else {
      state.history = data.messages;
      $('history').replaceChildren();
    }
    if (data.messages.length > 0) {
      const ids = data.messages.map((message) => message.id);
      state.oldestId = before ? Math.min(state.oldestId || Infinity, ...ids) : Math.min(...ids);
    }
    renderHistory();
    $('load-older').classList.toggle('hidden', data.messages.length < 200);
  } catch (error) {
    toast(error.message, true);
  }
}

function prependLiveHistory(message) {
  state.history.unshift(message);
  if (state.history.length > 400) state.history.pop();
  $('history').prepend(historyItem(message, true));
  $('history-meta').textContent = `${state.history.length} message${state.history.length === 1 ? '' : 's'}`;
}

// ---------------------------------------------------------------- routing

function activeConnection() {
  return state.connections.find((connection) => connection.id === state.activeId) || null;
}

function statusOf(id) {
  return state.statuses[String(id)] || { running: false, connected: false };
}

function renderConnectionSelect() {
  const select = $('connection-select');
  select.replaceChildren();
  if (state.connections.length === 0) {
    const option = document.createElement('option');
    option.textContent = 'No connections';
    option.value = '';
    select.appendChild(option);
    select.disabled = true;
    return;
  }
  select.disabled = false;
  for (const connection of state.connections) {
    const option = document.createElement('option');
    option.value = String(connection.id);
    option.textContent = connection.name;
    option.selected = connection.id === state.activeId;
    select.appendChild(option);
  }
}

function renderStatus() {
  const connection = activeConnection();
  const dot = $('status-dot');
  const button = $('toggle-connection');
  if (!connection) {
    dot.className = 'dot';
    dot.title = 'no connection selected';
    button.textContent = 'Connect';
    button.disabled = true;
    return;
  }
  const status = statusOf(connection.id);
  const connected = status.connected;
  const running = status.running;
  dot.className = `dot${connected ? ' on' : status.error ? ' err' : ''}`;
  dot.title = connected ? 'connected' : status.error || (running ? 'connecting…' : 'disconnected');
  button.disabled = false;
  button.textContent = running ? 'Disconnect' : 'Connect';
}

function renderSettings() {
  if (!state.settings) return;
  $('retention-hours').value = String(state.settings.retention_hours);
  $('max-db-mib').value = String(Math.round(state.settings.max_db_bytes / (1024 * 1024)));
  $('max-payload-kib').value = String(Math.round(state.settings.max_payload_bytes / 1024));
  $('data-meta').textContent = state.stats
    ? `${state.stats.topics} topics · ${state.stats.messages} messages · ${(state.stats.db_bytes / (1024 * 1024)).toFixed(1)} MiB`
    : '';
}

function renderConnectionList() {
  const list = $('connection-list');
  list.replaceChildren();
  if (state.connections.length === 0) {
    const item = document.createElement('li');
    item.textContent = 'No saved connections yet.';
    list.appendChild(item);
    return;
  }
  for (const connection of state.connections) {
    const item = document.createElement('li');
    const dot = document.createElement('span');
    const status = statusOf(connection.id);
    dot.className = `dot${status.connected ? ' on' : status.error ? ' err' : ''}`;
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = connection.name;
    const target = document.createElement('span');
    target.className = 'target';
    target.textContent = `${connection.tls ? 'mqtts' : 'mqtt'}://${connection.host}:${connection.port} ${connection.topic_filter}`;
    const spacer = document.createElement('span');
    spacer.className = 'spacer';
    const edit = document.createElement('button');
    edit.type = 'button';
    edit.className = 'btn';
    edit.textContent = 'Edit';
    edit.addEventListener('click', () => fillEditor(connection));
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'btn';
    remove.textContent = 'Delete';
    remove.addEventListener('click', () => deleteConnection(connection));
    item.append(dot, name, target, spacer, edit, remove);
    list.appendChild(item);
  }
}

function fillEditor(connection) {
  $('editor-title').textContent = `Edit ${connection.name}`;
  $('conn-id').value = String(connection.id);
  $('conn-name').value = connection.name;
  $('conn-host').value = connection.host;
  $('conn-port').value = String(connection.port);
  $('conn-client-id').value = connection.client_id || '';
  $('conn-username').value = connection.username || '';
  $('conn-password').value = '';
  $('conn-password').placeholder = connection.has_password ? 'unchanged' : 'none stored';
  $('conn-clear-password').checked = false;
  $('conn-tls').checked = connection.tls;
  $('conn-tls-insecure').checked = connection.tls_insecure;
  $('conn-ca').value = connection.ca_pem || '';
  $('conn-filter').value = connection.topic_filter || '#';
  $('conn-qos').value = String(connection.qos || 0);
  $('conn-enabled').checked = connection.enabled;
}

function resetEditor() {
  $('editor-title').textContent = 'Add connection';
  $('connection-form').reset();
  $('conn-id').value = '';
  $('conn-port').value = '1883';
  $('conn-filter').value = '#';
  $('conn-enabled').checked = true;
  $('conn-password').placeholder = 'unchanged';
}

async function deleteConnection(connection) {
  if (!confirm(`Delete connection "${connection.name}" and its stored history?`)) return;
  try {
    await api(`/api/connections/${connection.id}`, { method: 'DELETE' });
    if (state.activeId === connection.id) {
      state.activeId = 0;
      state.selected = '';
      resetTree();
    }
    await loadBootstrap();
    toast('connection deleted');
  } catch (error) {
    toast(error.message, true);
  }
}

async function saveConnection(event) {
  event.preventDefault();
  const id = Number($('conn-id').value || 0);
  const payload = {
    name: $('conn-name').value.trim(),
    host: $('conn-host').value.trim(),
    port: Number($('conn-port').value),
    client_id: $('conn-client-id').value.trim(),
    username: $('conn-username').value,
    password: $('conn-password').value,
    clear_password: $('conn-clear-password').checked,
    tls: $('conn-tls').checked,
    tls_insecure: $('conn-tls-insecure').checked,
    ca_pem: $('conn-ca').value,
    topic_filter: $('conn-filter').value.trim() || '#',
    qos: Number($('conn-qos').value),
    enabled: $('conn-enabled').checked,
  };
  try {
    const saved = id
      ? await api(`/api/connections/${id}`, { method: 'PUT', body: JSON.stringify(payload) })
      : await api('/api/connections', { method: 'POST', body: JSON.stringify(payload) });
    if (!state.activeId) state.activeId = saved.id;
    await loadBootstrap();
    resetEditor();
    toast('connection saved');
  } catch (error) {
    toast(error.message, true);
  }
}

async function saveSettings() {
  const payload = {
    retention_hours: Number($('retention-hours').value),
    max_db_bytes: Number($('max-db-mib').value) * 1024 * 1024,
    max_payload_bytes: Number($('max-payload-kib').value) * 1024,
  };
  try {
    state.settings = await api('/api/settings', { method: 'PUT', body: JSON.stringify(payload) });
    renderSettings();
    toast('retention saved');
  } catch (error) {
    toast(error.message, true);
  }
}

// -------------------------------------------------------------- bootstrap

async function loadBootstrap() {
  const data = await api('/api/bootstrap');
  state.connections = data.connections;
  state.statuses = data.statuses;
  state.settings = data.settings;
  state.stats = data.stats;
  if (!state.connections.some((connection) => connection.id === state.activeId)) {
    const stored = Number(localStorage.getItem('mqttree.active'));
    const candidate = state.connections.find((connection) => connection.id === stored) || state.connections[0];
    state.activeId = candidate ? candidate.id : 0;
  }
  localStorage.setItem('mqttree.active', String(state.activeId));
  renderConnectionSelect();
  renderStatus();
  renderSettings();
  renderConnectionList();
}

async function toggleConnection() {
  const connection = activeConnection();
  if (!connection) return;
  const status = statusOf(connection.id);
  try {
    if (status.running) {
      await api(`/api/connections/${connection.id}/stop`, { method: 'POST' });
    } else {
      await api(`/api/connections/${connection.id}/start`, { method: 'POST' });
    }
    await loadBootstrap();
  } catch (error) {
    toast(error.message, true);
  }
}

function expandableNodes() {
  return [...state.nodes.values()].filter((node) => node.childrenEl.childElementCount > 0);
}

function allExpanded() {
  const expandable = expandableNodes();
  return expandable.length > 0 && expandable.every((node) => state.expanded.has(node.path));
}

function updateToggleAll() {
  const button = $('toggle-all');
  const opened = allExpanded();
  button.disabled = expandableNodes().length === 0;
  button.dataset.state = opened ? 'collapse' : 'expand';
  const label = opened ? 'Collapse all' : 'Expand all';
  button.title = label;
  button.setAttribute('aria-label', label);
}

function expandAll() {
  for (const node of expandableNodes()) {
    state.expanded.add(node.path);
    setOpen(node, true);
  }
  saveExpanded();
  updateToggleAll();
}

function collapseAll() {
  for (const node of state.nodes.values()) {
    state.expanded.delete(node.path);
    setOpen(node, false);
  }
  saveExpanded();
  updateToggleAll();
}

async function clearTree() {
  if (!state.activeId) return;
  if (!confirm('Delete all stored topics and messages for this connection? The connection and its credentials are kept.')) return;
  try {
    await api(`/api/connections/${state.activeId}/clear`, { method: 'POST' });
    state.selected = '';
    state.history = [];
    document.body.classList.remove('detail-open');
    $('detail').classList.remove('open');
    $('detail-body').classList.add('hidden');
    $('detail-empty').classList.remove('hidden');
    $('history').replaceChildren();
    $('latest-payload').textContent = '';
    resetTree();
    await loadTree();
    await loadBootstrap();
    renderSettings();
    toast('stored messages cleared');
  } catch (error) {
    toast(error.message, true);
  }
}

// -------------------------------------------------------------- websocket

function connectWs() {
  if (state.ws) {
    state.ws.onclose = null;
    state.ws.close();
  }
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const socket = new WebSocket(`${scheme}://${location.host}/api/events`);
  state.ws = socket;
  socket.onopen = () => {
    const reconnected = state.wsAttempt > 0;
    state.wsAttempt = 0;
    if (reconnected && state.activeId) loadTree();
  };
  socket.onmessage = (event) => {
    let frame;
    try {
      frame = JSON.parse(event.data);
    } catch {
      return;
    }
    for (const item of frame.events || []) handleEvent(item);
  };
  socket.onclose = () => {
    state.wsAttempt += 1;
    const delay = Math.min(10000, 500 * 2 ** Math.min(state.wsAttempt, 5));
    setTimeout(connectWs, delay);
  };
}

function handleEvent(event) {
  if (event.type === 'status') {
    state.statuses[String(event.connection_id)] = event.status;
    if (event.connection_id === state.activeId) renderStatus();
    renderConnectionList();
    return;
  }
  if (event.type !== 'message' || !event.message) return;
  if (event.connection_id !== state.activeId) return;
  const message = event.message;
  const topic = {
    topic: message.topic,
    qos: message.qos,
    retained: message.retained,
    truncated: message.truncated,
    received_at: message.ts,
    payload_b64: message.payload_b64,
    text: message.text,
  };
  state.topics.set(message.topic, topic);
  const node = ensureTopicNode(message.topic);
  updateLeaf(node, message);
  markTopicHot(message.topic);
  updateTreeEmpty();
  if (state.filter) applyFilter(state.filter);
  if (state.selected === message.topic) {
    $('latest-payload').innerHTML = renderPayloadHtml(message.text || '', message.payload_b64, message.truncated);
    $('payload-meta').textContent = `${sizeLabel(message.payload_b64)} · ${message.retained ? 'retained · ' : ''}${fmtTime(message.ts)}`;
    renderDetailHead(topic);
    prependLiveHistory(message);
  }
}

// -------------------------------------------------------------------- init

function wire() {
  $('connection-select').addEventListener('change', async (event) => {
    state.activeId = Number(event.target.value) || 0;
    localStorage.setItem('mqttree.active', String(state.activeId));
    state.selected = '';
    document.body.classList.remove('detail-open');
    $('detail').classList.remove('open');
    $('detail-body').classList.add('hidden');
    $('detail-empty').classList.remove('hidden');
    state.history = [];
    renderStatus();
    await loadTree();
  });
  $('toggle-connection').addEventListener('click', toggleConnection);
  $('toggle-all').addEventListener('click', () => {
    if (allExpanded()) collapseAll();
    else expandAll();
  });
  $('clear-tree').addEventListener('click', clearTree);
  initSplitter();
  initTheme();

  $('copy-topic').innerHTML = COPY_ICON;
  $('copy-payload').innerHTML = COPY_ICON;
  $('copy-topic').addEventListener('click', () => copyText($('topic-path').textContent, 'topic copied'));
  $('copy-payload').addEventListener('click', () => copyText($('latest-payload').textContent, 'payload copied'));

  $('clear-search').addEventListener('click', () => {
    $('search').value = '';
    applyFilter('');
    syncSearchClear();
    $('search').focus();
  });
  $('search').addEventListener('input', (event) => {
    applyFilter(event.target.value);
    syncSearchClear();
  });
  $('open-settings').addEventListener('click', () => {
    renderSettings();
    renderConnectionList();
    $('settings').showModal();
    // Focus the dialog itself so no control gets a stray focus ring (and a
    // phone does not open the keyboard on the first input).
    $('settings').focus();
  });
  $('save-settings').addEventListener('click', saveSettings);
  $('connection-form').addEventListener('submit', saveConnection);
  $('conn-reset').addEventListener('click', resetEditor);
  $('back').addEventListener('click', () => {
    $('detail').classList.remove('open');
    document.body.classList.remove('detail-open');
  });
  $('copy-topic').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText($('topic-path').textContent);
      toast('topic copied');
    } catch {
      toast('clipboard unavailable on plain HTTP', true);
    }
  });
  $('load-older').addEventListener('click', () => {
    if (!state.selected || !state.oldestId) return;
    loadHistory(state.selected, state.oldestId);
  });
  $('publish-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!state.activeId) return;
    try {
      await api('/api/publish', {
        method: 'POST',
        body: JSON.stringify({
          connection_id: state.activeId,
          topic: $('publish-topic').value,
          payload: $('publish-payload').value,
          qos: Number($('publish-qos').value),
          retain: $('publish-retain').checked,
        }),
      });
      toast('published');
    } catch (error) {
      toast(error.message, true);
    }
  });
}

async function init() {
  wire();
  syncSearchClear();
  try {
    await loadBootstrap();
  } catch (error) {
    toast(`could not load state: ${error.message}`, true);
  }
  await loadTree();
  connectWs();
  setInterval(async () => {
    try {
      const data = await api('/api/bootstrap');
      state.statuses = data.statuses;
      state.stats = data.stats;
      renderStatus();
      renderSettings();
      renderConnectionList();
    } catch {
      /* transient */
    }
  }, 30000);
}

init();
