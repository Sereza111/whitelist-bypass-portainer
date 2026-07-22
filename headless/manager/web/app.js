const byId = (id) => document.getElementById(id);
const app = {
  profiles: [], sessions: [], selected: null, refreshing: false,
  profileSignature: '', sessionSignature: '', vkLoginStatus: null,
  overview: null, recoverySettings: null, events: [], eventFilter: 'all', section: 'dashboard',
};

const pageMeta = {
  dashboard: ['Обзор', 'Состояние сервера и быстрые действия'],
  clients: ['Клиенты', 'Профили доступа, ограничения и восстановление'],
  sessions: ['Сессии', 'Активные звонки и живая диагностика транспорта'],
  providers: ['Провайдеры', 'Серверный VK и доставка новых ссылок'],
  events: ['События', 'Безопасный аудит действий панели'],
  settings: ['Настройки', 'Состояние и рекомендации по эксплуатации'],
};

function setSection(section, updateHash = true) {
  if (!pageMeta[section]) section = 'dashboard';
  app.section = section;
  document.querySelectorAll('[data-page]').forEach((page) => page.classList.toggle('active', page.dataset.page === section));
  document.querySelectorAll('[data-nav]').forEach((item) => item.classList.toggle('active', item.dataset.nav === section));
  byId('pageTitle').textContent = pageMeta[section][0];
  byId('pageSubtitle').textContent = pageMeta[section][1];
  if (updateHash) history.replaceState(null, '', `#${section}`);
  closeContextMenu();
  if (section === 'events') run(refreshEvents);
  if (section === 'providers') run(refreshRecoverySettings);
  window.scrollTo({ top: 0, behavior: 'smooth' });
}

function openClientEditor() {
  setSection('clients');
  byId('clientEditor').classList.add('open');
  setTimeout(() => byId('profileName').focus(), 0);
}

function closeClientEditor() {
  byId('clientEditor').classList.remove('open');
}

async function api(path, options = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), options.timeout || 9000);
  let response;
  try {
    response = await fetch(path, {
      ...options,
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    });
  } catch (error) {
    if (error.name === 'AbortError') throw new Error('Сервер не ответил вовремя — попробуй ещё раз.');
    throw error;
  } finally {
    clearTimeout(timer);
  }
  if (response.status === 204) return null;
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

function escapeHTML(value) {
  const node = document.createElement('span');
  node.textContent = value == null ? '' : String(value);
  return node.innerHTML;
}

function showError(message = '') {
  byId('error').textContent = message;
  byId('error').hidden = !message;
}

async function copyText(value) {
	if (navigator.clipboard?.writeText) {
		try { await navigator.clipboard.writeText(value); return; } catch (_) {}
	}
	const area = document.createElement('textarea');
	area.value = value;
	area.style.position = 'fixed';
	area.style.opacity = '0';
	document.body.appendChild(area);
	area.select();
	const copied = document.execCommand('copy');
	area.remove();
	if (!copied) throw new Error('Браузер запретил копирование — включи HTTPS или скопируй вручную.');
}

function activeState(state) {
  return ['starting', 'running', 'link-ready', 'waiting-for-client', 'connected', 'degraded', 'recovering', 'stopping'].includes(state);
}

function profilePayload() {
  const expiresAt = byId('expiresAt').value;
  const recoveryRecipient = byId('profileRecoveryRecipient').value.trim();
  return {
    name: byId('profileName').value.trim(),
    enabled: byId('enabled').checked,
	autoRestart: byId('autoRestart').checked,
    maxSessions: Number(byId('maxSessions').value),
    expiresAt: expiresAt ? new Date(expiresAt).toISOString() : null,
    recoveryRecipient: recoveryRecipient || null,
    config: {
      mode: byId('mode').value,
      resources: byId('resources').value,
      displayName: byId('displayName').value.trim(),
      videoReliability: 'auto',
      kcpProfile: byId('kcpProfile').value,
    },
  };
}

function resetForm() {
  byId('profileForm').reset();
  byId('editingId').value = '';
  byId('displayName').value = 'Headless';
  byId('resources').value = 'default';
  byId('kcpProfile').value = 'balanced';
  byId('enabled').checked = true;
	byId('autoRestart').checked = true;
  byId('expiresAt').value = '';
  byId('profileRecoveryRecipient').value = '';
  byId('cancelEdit').hidden = true;
  byId('saveProfile').textContent = 'Сохранить профиль';
  byId('editorTitle').textContent = 'Новый клиент';
}

function editProfile(id) {
  const profile = app.profiles.find((item) => item.id === id);
  if (!profile) return;
  byId('editingId').value = profile.id;
  byId('profileName').value = profile.name;
  byId('mode').value = profile.config.mode;
  byId('resources').value = profile.config.resources;
  byId('displayName').value = profile.config.displayName;
  byId('kcpProfile').value = profile.config.kcpProfile;
  byId('maxSessions').value = String(profile.maxSessions);
  byId('enabled').checked = profile.enabled;
	byId('autoRestart').checked = profile.autoRestart !== false;
  byId('profileRecoveryRecipient').value = profile.recoveryRecipient || '';
  byId('expiresAt').value = profile.expiresAt ? toLocalDateTime(profile.expiresAt) : '';
  byId('cancelEdit').hidden = false;
  byId('saveProfile').textContent = 'Обновить профиль';
  byId('editorTitle').textContent = `Изменить · ${profile.name}`;
  openClientEditor();
}

function toLocalDateTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

async function toggleProfile(id) {
  const profile = app.profiles.find((item) => item.id === id);
  if (!profile) return;
  await api(`/api/profiles/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify({
      name: profile.name,
      enabled: !profile.enabled,
	  autoRestart: profile.autoRestart,
      maxSessions: profile.maxSessions,
      expiresAt: profile.expiresAt || null,
      recoveryRecipient: profile.recoveryRecipient || null,
      config: profile.config,
    }),
  });
  await refresh();
}

async function startProfile(id) {
  const profile = app.profiles.find((item) => item.id === id);
  if (!profile) return;
  const config = { ...profile.config, existingLink: byId('launchExistingLink').value.trim() };
  await api('/api/sessions', { method: 'POST', body: JSON.stringify({ clientId: id, config }) });
  byId('launchExistingLink').value = '';
  await refresh();
}

async function deleteProfile(id) {
  if (!confirm('Удалить этот клиентский профиль?')) return;
  await api(`/api/profiles/${encodeURIComponent(id)}`, { method: 'DELETE' });
  await refresh();
}

async function duplicateProfile(id) {
  await api(`/api/profiles/${encodeURIComponent(id)}/duplicate`, { method: 'POST', body: '{}' });
  showError('Создана независимая копия профиля с новым ключом восстановления.');
  await refresh();
}

async function testProfileRecovery(id) {
  await api(`/api/profiles/${encodeURIComponent(id)}/recovery/test`, { method: 'POST', body: '{}' });
  showError('Тестовое сообщение для клиента отправлено в VK.');
  await refreshRecoverySettings();
  await refreshEvents();
}

async function stopSession(id) {
  await api(`/api/sessions/${encodeURIComponent(id)}/stop`, { method: 'POST', body: '{}' });
  await refresh();
}

async function deleteSession(id) {
  await api(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (app.selected === id) app.selected = null;
  await refresh();
}

function selectSession(id, scroll = true) {
  app.selected = id;
  setSection('sessions');
  renderSessions();
  run(renderDetail);
  if (scroll) document.querySelector('.diagnostics')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function closeContextMenu() {
  byId('contextMenu').hidden = true;
  byId('contextMenu').innerHTML = '';
}

function contextActions(kind, id) {
  if (kind === 'profile') {
    const profile = app.profiles.find((item) => item.id === id);
    if (!profile) return [];
    return [
      { action: 'start', label: 'Запустить сессию', disabled: !profile.enabled },
      { action: 'mobile', label: 'Скопировать в телефон' },
      { action: 'duplicate', label: 'Создать независимую копию' },
      { action: 'test-recovery', label: 'Проверить доставку VK' },
      { action: 'edit', label: 'Изменить профиль' },
      { action: 'toggle', label: profile.enabled ? 'Отключить профиль' : 'Включить профиль' },
      { action: 'delete-profile', label: 'Удалить профиль', danger: true },
    ];
  }
  const session = app.sessions.find((item) => item.id === id);
  if (!session) return [];
  return [
    { action: 'open-session', label: 'Открыть диагностику' },
    { action: 'copy-session', label: 'Копировать ссылку', disabled: !session.status?.sessionLink },
    activeState(session.status?.state)
      ? { action: 'stop-session', label: 'Остановить сессию', danger: true }
      : { action: 'delete-session', label: 'Убрать сессию', danger: true },
  ];
}

function openContextMenu(kind, id, x, y) {
  const menu = byId('contextMenu');
  const actions = contextActions(kind, id);
  if (!actions.length) return;
  menu.dataset.kind = kind;
  menu.dataset.id = id;
  menu.innerHTML = actions.map((item) => `<button type="button" role="menuitem" data-menu-action="${item.action}" class="${item.danger ? 'danger' : ''}" ${item.disabled ? 'disabled' : ''}>${escapeHTML(item.label)}</button>`).join('');
  menu.hidden = false;
  const bounds = menu.getBoundingClientRect();
  menu.style.left = `${Math.max(8, Math.min(x, window.innerWidth - bounds.width - 8))}px`;
  menu.style.top = `${Math.max(8, Math.min(y, window.innerHeight - bounds.height - 8))}px`;
  menu.querySelector('button:not(:disabled)')?.focus();
}

async function runContextAction(action, kind, id) {
  if (kind === 'profile') {
    if (action === 'start') return startProfile(id);
    if (action === 'mobile') return copyMobileProfile(id);
    if (action === 'duplicate') return duplicateProfile(id);
    if (action === 'test-recovery') return testProfileRecovery(id);
    if (action === 'edit') return editProfile(id);
    if (action === 'toggle') return toggleProfile(id);
    if (action === 'delete-profile') return deleteProfile(id);
  }
  const session = app.sessions.find((item) => item.id === id);
  if (action === 'open-session') return selectSession(id);
  if (action === 'copy-session' && session?.status?.sessionLink) {
    await copyText(session.status.sessionLink);
    showError('Ссылка подключения скопирована.');
    return;
  }
  if (action === 'stop-session') return stopSession(id);
  if (action === 'delete-session') return deleteSession(id);
}

function renderProfiles() {
  const root = byId('profiles');
  const query = (byId('profileSearch')?.value || '').trim().toLowerCase();
  const visible = app.profiles.filter((profile) => `${profile.name} ${profile.config.mode}`.toLowerCase().includes(query));
  const signature = JSON.stringify([visible, query]);
  if (signature === app.profileSignature) return;
  app.profileSignature = signature;
  byId('profileResultCount').textContent = visible.length;
  if (!visible.length) {
    root.innerHTML = `<p class="empty">${query ? 'По этому запросу клиентов нет.' : 'Создай первый профиль — например, для телефона или ноутбука.'}</p>`;
    return;
  }
  root.innerHTML = visible.map((profile) => {
    const state = profile.enabled ? 'enabled' : 'disabled';
    return `<article class="profile-card ${state}" data-menu-kind="profile" data-menu-id="${profile.id}">
      <div class="profile-identity"><div class="profile-glyph">${escapeHTML(profile.name.slice(0, 1).toUpperCase())}</div><span><strong>${escapeHTML(profile.name)}</strong><small>${escapeHTML(profile.id)}</small></span></div>
      <div class="profile-provider">${escapeHTML(profile.config.mode.toUpperCase())}<small>${escapeHTML(profile.config.kcpProfile || 'balanced')}</small></div>
      <div class="profile-meta">${profile.autoRestart ? 'Автовосстановление' : 'Ручной запуск'}<small>лимит ${profile.maxSessions} · ${profile.recoveryRecipient ? 'свой VK' : 'общий VK'}</small></div>
      <span class="state-label">${profile.enabled ? 'Активен' : 'Отключён'}</span>
      <div class="profile-actions">
        <button class="small start-client" data-id="${profile.id}" ${profile.enabled ? '' : 'disabled'}>Запустить</button>
        <button class="icon-button menu-trigger" type="button" data-kind="profile" data-id="${profile.id}" title="Все действия" aria-label="Действия профиля">⋮</button>
      </div>
    </article>`;
  }).join('');
}

function renderDashboard() {
  const sessions = app.sessions.slice(0, 5);
  byId('dashboardSessions').innerHTML = sessions.length ? sessions.map((session) => {
    const status = session.status || {};
    const dot = ['connected', 'link-ready', 'waiting-for-client'].includes(status.state) ? 'good-dot' : (['failed', 'degraded'].includes(status.state) ? 'bad-dot' : '');
    return `<div class="compact-row"><i class="${dot}"></i><span><strong>${escapeHTML(session.clientName)}</strong><small>${escapeHTML(friendlyState(status))}</small></span><span>↓ ${escapeHTML(status.metrics?.rx_kbps || '0')} kbps</span><span>↑ ${escapeHTML(status.metrics?.tx_kbps || '0')} kbps</span><button class="text-button dashboard-session" data-id="${session.id}">Открыть</button></div>`;
  }).join('') : '<p class="empty">Активных каналов пока нет.</p>';
  const profiles = app.profiles.slice(0, 5);
  byId('dashboardClients').innerHTML = profiles.length ? profiles.map((profile) => `<div class="compact-row"><i class="${profile.enabled ? 'good-dot' : 'bad-dot'}"></i><span><strong>${escapeHTML(profile.name)}</strong><small>${escapeHTML(profile.config.mode.toUpperCase())} · ${escapeHTML(profile.config.kcpProfile || 'balanced')}</small></span><span>${profile.autoRestart ? 'Auto recovery' : 'Manual'}</span><span>лимит ${profile.maxSessions}</span><button class="text-button dashboard-profile" data-id="${profile.id}">Изменить</button></div>`).join('') : '<p class="empty">Создай первый клиентский профиль.</p>';
  document.querySelectorAll('.dashboard-session').forEach((button) => button.onclick = () => selectSession(button.dataset.id));
  document.querySelectorAll('.dashboard-profile').forEach((button) => button.onclick = () => editProfile(button.dataset.id));
}

async function copyMobileProfile(id) {
	const profile = app.profiles.find((item) => item.id === id);
	if (!profile) return;
	const session = app.sessions
		.filter((item) => item.clientId === id && item.status?.sessionLink)
		.sort((a, b) => (b.status.generation || 0) - (a.status.generation || 0))[0];
	const block = [
		'WLB Recovery Profile',
		`Name: ${profile.name}`,
		`Profile: ${profile.id}`,
		`Key: ${profile.recoveryKey}`,
		`Generation: ${session?.status?.generation || 0}`,
		`Link: ${session?.status?.sessionLink || '<start profile first>'}`,
	].join('\n');
	await copyText(block);
	showError('Профиль восстановления скопирован — вставь его в Android-клиент.');
}

function friendlyState(status) {
	const labels = {
		starting: 'Создаю защищённый звонок', running: 'Подготавливаю канал',
		'link-ready': 'Ссылка готова', 'waiting-for-client': 'Жду устройство',
		connected: 'Защищённый канал активен', degraded: 'Стабилизирую соединение',
		recovering: 'Восстанавливаю связь', stopping: 'Останавливаю', stopped: 'Остановлен', failed: 'Ожидаю восстановления',
	};
	return labels[status.state] || status.state;
}

function renderSessions() {
  const root = byId('sessions');
  byId('sessionResultCount').textContent = app.sessions.length;
  const signature = JSON.stringify(app.sessions.map((session) => ({
    id: session.id,
    name: session.clientName,
    state: session.status.state,
    selected: session.id === app.selected,
  })));
  if (signature === app.sessionSignature) {
    root.querySelectorAll('.session-card').forEach((card) => {
      const session = app.sessions.find((item) => item.id === card.dataset.sessionId);
      if (!session) return;
      const metrics = session.status.metrics || {};
      card.querySelector('.session-tx').textContent = `TX ${metrics.tx_kbps || '0'}`;
      card.querySelector('.session-rx').textContent = `RX ${metrics.rx_kbps || '0'} kbps`;
    });
    return;
  }
  app.sessionSignature = signature;
  if (!app.sessions.length) {
    root.innerHTML = '<p class="empty">Запусти профиль — здесь появится независимый Creator и его ссылка.</p>';
    return;
  }
  root.innerHTML = app.sessions.map((session) => {
    const status = session.status;
    const tx = status.metrics?.tx_kbps || '0';
    const rx = status.metrics?.rx_kbps || '0';
    const selected = session.id === app.selected ? 'selected' : '';
    return `<article class="session-card ${selected}" data-session-id="${session.id}" data-menu-kind="session" data-menu-id="${session.id}" data-state="${escapeHTML(status.state)}">
      <button class="session-open" data-id="${session.id}">
        <span class="status-rune"></span>
		<span><strong>${escapeHTML(session.clientName)}</strong><small>${escapeHTML(friendlyState(status))}</small></span>
      </button>
      <div class="session-rate"><span class="session-tx">TX ${escapeHTML(tx)}</span><span class="session-rx">RX ${escapeHTML(rx)} kbps</span></div>
      <div class="session-actions">
        ${activeState(status.state) ? `<button class="small stop-session" data-id="${session.id}">Стоп</button>` : `<button class="small delete-session" data-id="${session.id}">Убрать</button>`}
        <button class="icon-button menu-trigger" type="button" data-kind="session" data-id="${session.id}" title="Все действия" aria-label="Действия сессии">⋮</button>
      </div>
    </article>`;
  }).join('');
}

function metricCard(label, value, unit = '') {
  return `<article><span>${label}</span><strong>${escapeHTML(value ?? '—')}</strong><small>${unit}</small></article>`;
}

async function renderDetail() {
  if (!app.selected) {
    byId('detail').hidden = true;
    byId('detailEmpty').hidden = false;
    byId('selectedTitle').textContent = 'Выбери сессию';
    return;
  }
  const session = await api(`/api/sessions/${encodeURIComponent(app.selected)}`);
  const status = session.status;
  const metrics = status.metrics || {};
  byId('detail').hidden = false;
  byId('detailEmpty').hidden = true;
  byId('selectedTitle').textContent = `${session.clientName} · ${friendlyState(status)}`;
  byId('sessionLink').value = status.sessionLink || '';
  byId('metrics').innerHTML = [
    metricCard('Download', metrics.rx_kbps, 'kbps'),
    metricCard('Upload', metrics.tx_kbps, 'kbps'),
    metricCard('KCP window', metrics.kcp_wait_snd, 'segments'),
    metricCard('Carrier queue', metrics.queue || '—', 'frames'),
    metricCard('Dropped', metrics.kcp_dropped || '0', 'segments'),
    metricCard('Recoveries', metrics.kcp_stalls || '0', 'stalls'),
	metricCard('Call generation', status.generation || '1'),
	metricCard('Creator restarts', status.restartCount || '0'),
  ].join('');
  const atBottom = byId('logs').scrollTop + byId('logs').clientHeight >= byId('logs').scrollHeight - 24;
  byId('logs').textContent = (status.logs || []).join('\n') || 'Событий пока нет';
  if (atBottom) byId('logs').scrollTop = byId('logs').scrollHeight;
}

function bindDynamicActions() {
  document.querySelectorAll('.start-client').forEach((button) => button.onclick = () => run(() => startProfile(button.dataset.id)));
	document.querySelectorAll('.mobile-client').forEach((button) => button.onclick = () => run(() => copyMobileProfile(button.dataset.id)));
  document.querySelectorAll('.toggle-client').forEach((button) => button.onclick = () => run(() => toggleProfile(button.dataset.id)));
  document.querySelectorAll('.edit-client').forEach((button) => button.onclick = () => editProfile(button.dataset.id));
  document.querySelectorAll('.delete-client').forEach((button) => button.onclick = () => run(() => deleteProfile(button.dataset.id)));
  document.querySelectorAll('.session-open').forEach((button) => button.onclick = () => {
    selectSession(button.dataset.id);
  });
  document.querySelectorAll('.stop-session').forEach((button) => button.onclick = () => run(() => stopSession(button.dataset.id)));
  document.querySelectorAll('.delete-session').forEach((button) => button.onclick = () => run(() => deleteSession(button.dataset.id)));
  document.querySelectorAll('.menu-trigger').forEach((button) => button.onclick = (event) => {
    event.stopPropagation();
    const bounds = button.getBoundingClientRect();
    openContextMenu(button.dataset.kind, button.dataset.id, bounds.right - 180, bounds.bottom + 6);
  });
  document.querySelectorAll('[data-menu-kind]').forEach((card) => card.oncontextmenu = (event) => {
    event.preventDefault();
    openContextMenu(card.dataset.menuKind, card.dataset.menuId, event.clientX, event.clientY);
  });
}

async function run(action) {
  showError();
  try { await action(); } catch (error) { showError(error.message); }
}

function formatDate(value) {
  if (!value) return 'Никогда';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'Никогда' : date.toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' });
}

function renderRecoverySettings(settings) {
  if (!settings) return;
  app.recoverySettings = settings;
  if (document.activeElement !== byId('recoveryRecipient')) byId('recoveryRecipient').value = settings.recipient || '';
  const sources = { panel: 'Панель', profile: 'Профиль', env: 'Legacy env' };
  byId('recoverySource').textContent = settings.configured ? (sources[settings.source] || settings.source) : 'Не настроено';
  byId('recoverySource').classList.toggle('good-badge', settings.configured);
  byId('recoveryVerified').textContent = formatDate(settings.verifiedAt);
  byId('recoveryAccountWarning').hidden = !settings.sameAccount;
  byId('dashRecoveryState').textContent = settings.configured ? `готово · ${sources[settings.source] || settings.source}` : 'нужно указать получателя';
}

async function refreshRecoverySettings() {
  renderRecoverySettings(await api('/api/settings/recovery'));
}

async function saveRecoverySettings() {
  const recipient = byId('recoveryRecipient').value.trim();
  const settings = await api('/api/settings/recovery', { method: 'PATCH', body: JSON.stringify({ recipient }) });
  renderRecoverySettings(settings);
  showError(recipient ? 'Получатель сохранён. Теперь отправь тестовое сообщение.' : 'Настройка панели очищена; при наличии будет использован legacy env.');
  await refreshEvents();
}

async function sendRecoveryTest() {
  byId('testRecovery').disabled = true;
  try {
    await api('/api/settings/recovery/test', { method: 'POST', body: '{}' });
    showError('Тестовое сообщение отправлено. Проверь диалог VK.');
    await Promise.all([refreshRecoverySettings(), refreshEvents()]);
  } finally {
    byId('testRecovery').disabled = false;
  }
}

function renderEvents() {
  const root = byId('eventsList');
  const visible = app.eventFilter === 'all' ? app.events : app.events.filter((event) => event.kind === app.eventFilter);
  root.innerHTML = visible.length ? visible.map((event) => `<article class="event-row" data-level="${escapeHTML(event.level)}"><time>${escapeHTML(formatDate(event.timestamp))}</time><span class="event-kind">${escapeHTML(event.kind)}</span><p>${escapeHTML(event.message)}</p><small>${escapeHTML(event.reference || '')}</small></article>`).join('') : '<p class="empty">Для этого фильтра событий пока нет.</p>';
}

async function refreshEvents() {
  app.events = await api('/api/events?limit=100');
  renderEvents();
}

function renderVKSummary(status) {
  app.vkLoginStatus = status;
  const ready = status.managed || status.mounted;
  byId('providerAccountBadge').textContent = ready ? 'Подключён' : 'Не подключён';
  byId('providerAccountBadge').classList.toggle('good-badge', ready);
  byId('providerAccountID').textContent = status.accountId ? `ID ${status.accountId}` : (status.managed ? 'Подключён через панель' : (status.mounted ? 'Файл cookies' : 'Не подключён'));
  byId('dashVKState').textContent = ready ? (status.accountId ? `подключён · ID ${status.accountId}` : 'учётные данные готовы') : 'нужно подключить аккаунт';
}

async function refresh() {
  if (app.refreshing) return;
  app.refreshing = true;
  try {
    const overviewP = api('/api/overview').then((overview) => {
      app.overview = overview;
      byId('build').textContent = `${overview.buildVersion} / ${(overview.buildCommit || '').slice(0, 7)}`;
      byId('settingsBuild').textContent = `${overview.buildVersion} · ${(overview.buildCommit || '').slice(0, 7)}`;
      byId('settingsLimit').textContent = `${overview.maxSessions} сессий`;
      byId('activeCount').textContent = overview.activeSessions;
      byId('sessionLimit').textContent = `из ${overview.maxSessions}`;
      byId('clientCount').textContent = overview.clientCount;
      byId('clientNavCount').textContent = overview.clientCount;
      byId('sessionNavCount').textContent = overview.activeSessions;
      const vk = overview.providers.find((provider) => provider.id === 'vk');
      byId('vkProvider').textContent = !vk?.configured ? 'Не готов' : (overview.recoveryDelivery ? 'Готово' : 'Без recovery');
      byId('vkProvider').className = vk?.configured && overview.recoveryDelivery ? 'good' : 'bad';
      byId('vkProviderHint').textContent = !vk?.configured ? 'подключи серверный аккаунт' : (overview.recoveryDelivery ? 'аккаунт и доставка настроены' : 'укажи получателя сообщений');
      byId('vkStatMark').className = `stat-mark ${vk?.configured && overview.recoveryDelivery ? 'good-bg' : ''}`;
    }).catch(() => {});

    const profilesP = api('/api/profiles').then((profiles) => {
      app.profiles = profiles;
      renderProfiles();
      renderDashboard();
      bindDynamicActions();
    }).catch(() => {});

    const sessionsP = api('/api/sessions').then(async (sessions) => {
      app.sessions = sessions;
      if (app.selected && !sessions.some((session) => session.id === app.selected)) {
        app.selected = null;
        app.sessionSignature = '';
      }
      renderSessions();
      renderDashboard();
      bindDynamicActions();
      await renderDetail();
    }).catch(() => {});
    const recoveryP = api('/api/settings/recovery').then(renderRecoverySettings).catch(() => {});
    const vkP = api('/api/vk-login').then(renderVKSummary).catch(() => {});
    const eventsP = app.section === 'events' ? refreshEvents().catch(() => {}) : Promise.resolve();
    await Promise.allSettled([overviewP, profilesP, sessionsP, recoveryP, vkP, eventsP]);
  } finally {
    app.refreshing = false;
  }
}

async function saveProfile() {
  const id = byId('editingId').value;
  await api(id ? `/api/profiles/${encodeURIComponent(id)}` : '/api/profiles', {
    method: id ? 'PATCH' : 'POST', body: JSON.stringify(profilePayload()),
  });
  resetForm();
  closeClientEditor();
  await refresh();
}

function vkLoginActive(state) {
	return ['starting', 'waiting', 'authorizing'].includes(state);
}

function vkLoginStateLabel(state) {
	return {
		idle: 'Не подключён', mounted: 'Импортированный файл', starting: 'Запускаю окно', waiting: 'Жду сканирование',
		authorizing: 'Подтверждаю вход', ready: 'Серверный VK готов', failed: 'Нужна новая попытка',
	}[state] || state;
}

async function refreshVKLogin() {
	const status = await api('/api/vk-login');
	renderVKSummary(status);
	byId('vkLoginState').textContent = `${vkLoginStateLabel(status.state)}${status.accountId ? ` · ID ${status.accountId}` : ''}`;
	byId('vkLoginMessage').textContent = status.message;
	byId('vkLoginRune').dataset.state = status.state;
	byId('vkLoginWarning').textContent = status.warning || '';
	byId('vkLoginWarning').hidden = !status.warning;
	const active = vkLoginActive(status.state);
	byId('vkLoginStart').hidden = active;
	byId('vkLoginStart').textContent = status.state === 'failed' ? 'Создать новый QR' : (status.managed ? 'Сменить аккаунт' : 'Создать QR');
	byId('vkLoginStart').disabled = !status.browserAvailable;
	byId('vkLoginCancel').hidden = !active;
	byId('vkLoginForget').hidden = !status.managed || active;
	byId('vkLoginScreen').hidden = !status.screenshotReady;
	if (status.screenshotReady && !byId('vkLoginModal').hidden) {
		byId('vkLoginScreenshot').src = `/api/vk-login/screenshot?t=${Date.now()}`;
	}
	if (!status.browserAvailable) {
		byId('vkLoginMessage').textContent = 'Этот Docker-образ не содержит QR-браузер';
	}
}

async function openVKLogin() {
	byId('vkLoginModal').hidden = false;
	document.body.classList.add('modal-open');
	await refreshVKLogin();
}

function closeVKLogin() {
	byId('vkLoginModal').hidden = true;
	document.body.classList.remove('modal-open');
}

async function startVKLogin() {
	await api('/api/vk-login/start', { method: 'POST', body: '{}' });
	await refreshVKLogin();
}

async function cancelVKLogin() {
	await api('/api/vk-login/cancel', { method: 'POST', body: '{}' });
	await refreshVKLogin();
}

async function forgetVKLogin() {
	if (!confirm('Отключить серверный VK, сохранённый через панель? Активный звонок продолжит работать до перезапуска.')) return;
	await api('/api/vk-login/credentials', { method: 'DELETE' });
	await refreshVKLogin();
	await refresh();
}

byId('profileForm').addEventListener('submit', (event) => {
  event.preventDefault();
  run(saveProfile);
});
byId('saveProfile').addEventListener('click', (event) => {
  event.preventDefault();
  if (byId('profileForm').reportValidity()) run(saveProfile);
});
byId('cancelEdit').addEventListener('click', () => { resetForm(); closeClientEditor(); });
byId('openClientEditor').addEventListener('click', () => { resetForm(); openClientEditor(); });
byId('closeClientEditor').addEventListener('click', closeClientEditor);
byId('quickNewClient').addEventListener('click', () => { resetForm(); openClientEditor(); });
byId('profileSearch').addEventListener('input', () => { app.profileSignature = ''; renderProfiles(); bindDynamicActions(); });
byId('saveRecovery').addEventListener('click', () => run(saveRecoverySettings));
byId('testRecovery').addEventListener('click', () => run(sendRecoveryTest));
byId('refreshEvents').addEventListener('click', () => run(refreshEvents));
document.querySelectorAll('[data-nav]').forEach((item) => item.addEventListener('click', (event) => {
  event.preventDefault();
  setSection(item.dataset.nav);
}));
document.querySelectorAll('[data-event-filter]').forEach((button) => button.addEventListener('click', () => {
  app.eventFilter = button.dataset.eventFilter;
  document.querySelectorAll('[data-event-filter]').forEach((item) => item.classList.toggle('active', item === button));
  renderEvents();
}));
byId('reveal').addEventListener('click', () => {
  const field = byId('sessionLink');
  field.type = field.type === 'password' ? 'text' : 'password';
  byId('reveal').textContent = field.type === 'password' ? 'Показать' : 'Скрыть';
});
byId('copy').addEventListener('click', async () => {
  if (byId('sessionLink').value) await copyText(byId('sessionLink').value);
});
byId('vkLoginOpen').addEventListener('click', () => run(openVKLogin));
byId('vkLoginClose').addEventListener('click', closeVKLogin);
byId('vkLoginStart').addEventListener('click', () => run(startVKLogin));
byId('vkLoginCancel').addEventListener('click', () => run(cancelVKLogin));
byId('vkLoginForget').addEventListener('click', () => run(forgetVKLogin));
byId('vkLoginModal').addEventListener('click', (event) => {
	if (event.target === byId('vkLoginModal')) closeVKLogin();
});
document.addEventListener('keydown', (event) => {
	if (event.key !== 'Escape') return;
	closeContextMenu();
	if (!byId('vkLoginModal').hidden) closeVKLogin();
});
byId('contextMenu').addEventListener('click', (event) => {
	const button = event.target.closest('[data-menu-action]');
	if (!button || button.disabled) return;
	const menu = byId('contextMenu');
	const { kind, id } = menu.dataset;
	const action = button.dataset.menuAction;
	closeContextMenu();
	run(() => runContextAction(action, kind, id));
});
document.addEventListener('click', (event) => {
	if (!event.target.closest('#contextMenu') && !event.target.closest('.menu-trigger')) closeContextMenu();
});
window.addEventListener('blur', closeContextMenu);
window.addEventListener('resize', closeContextMenu);
window.addEventListener('scroll', closeContextMenu, true);

function applyTheme(theme) {
	const next = theme === 'dark' ? 'dark' : 'light';
	document.documentElement.setAttribute('data-theme', next);
	try { localStorage.setItem('wlb-theme', next); } catch (_) {}
	const label = byId('themeLabel');
	if (label) label.textContent = next === 'dark' ? 'Sable' : 'Argent';
}
(function initTheme() {
	let stored = 'dark';
	try { stored = localStorage.getItem('wlb-theme') || 'dark'; } catch (_) {}
	applyTheme(stored);
	byId('themeToggle')?.addEventListener('click', () => {
		const current = document.documentElement.getAttribute('data-theme');
		applyTheme(current === 'dark' ? 'light' : 'dark');
	});
})();
setSection(location.hash.slice(1) || 'dashboard', false);
run(refresh);
setInterval(() => run(refresh), 2000);
setInterval(() => {
	if (!byId('vkLoginModal').hidden) run(refreshVKLogin);
}, 1700);
