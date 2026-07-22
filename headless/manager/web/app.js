const byId = (id) => document.getElementById(id);
const app = {
  profiles: [], sessions: [], selected: null, refreshing: false,
  profileSignature: '', sessionSignature: '', vkLoginStatus: null,
};

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
  return {
    name: byId('profileName').value.trim(),
    enabled: byId('enabled').checked,
	autoRestart: byId('autoRestart').checked,
    maxSessions: Number(byId('maxSessions').value),
    expiresAt: expiresAt ? new Date(expiresAt).toISOString() : null,
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
  byId('cancelEdit').hidden = true;
  byId('saveProfile').textContent = 'Сохранить профиль';
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
  byId('expiresAt').value = profile.expiresAt ? toLocalDateTime(profile.expiresAt) : '';
  byId('cancelEdit').hidden = false;
  byId('saveProfile').textContent = 'Обновить профиль';
  byId('profileName').focus();
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
  const signature = JSON.stringify(app.profiles);
  if (signature === app.profileSignature) return;
  app.profileSignature = signature;
  if (!app.profiles.length) {
    root.innerHTML = '<p class="empty">Создай первый профиль слева — например, для телефона или ноутбука.</p>';
    return;
  }
  root.innerHTML = app.profiles.map((profile) => {
    const state = profile.enabled ? 'enabled' : 'disabled';
    return `<article class="profile-card ${state}" data-menu-kind="profile" data-menu-id="${profile.id}">
      <div class="profile-glyph">${escapeHTML(profile.name.slice(0, 1).toUpperCase())}</div>
      <div class="profile-copy">
        <div class="profile-heading"><h3>${escapeHTML(profile.name)}</h3><span>${profile.enabled ? 'ACTIVE' : 'LOCKED'}</span></div>
		<p>${escapeHTML(profile.config.mode.toUpperCase())} · ${profile.autoRestart ? 'AUTO RECOVERY' : 'MANUAL'} · limit ${profile.maxSessions}</p>
      </div>
      <div class="profile-actions">
        <button class="small start-client" data-id="${profile.id}" ${profile.enabled ? '' : 'disabled'}>Запустить</button>
        <button class="icon menu-trigger" type="button" data-kind="profile" data-id="${profile.id}" title="Все действия" aria-label="Действия профиля">⋮</button>
      </div>
    </article>`;
  }).join('');
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
        <button class="icon menu-trigger" type="button" data-kind="session" data-id="${session.id}" title="Все действия" aria-label="Действия сессии">⋮</button>
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

async function refresh() {
  if (app.refreshing) return;
  app.refreshing = true;
  try {
    // Each section renders independently: a slow /api/sessions must never
    // hide profiles that are already available, and one hung request must
    // not wedge the whole panel.
    const overviewP = api('/api/overview').then((overview) => {
      byId('build').textContent = `${overview.buildVersion} / ${(overview.buildCommit || '').slice(0, 7)}`;
      byId('activeCount').textContent = overview.activeSessions;
      byId('sessionLimit').textContent = `из ${overview.maxSessions}`;
      byId('clientCount').textContent = overview.clientCount;
      const vk = overview.providers.find((provider) => provider.id === 'vk');
      byId('vkProvider').textContent = !vk?.configured ? 'missing' : (overview.recoveryDelivery ? 'ready + recovery' : 'set VK_PEER_ID');
      byId('vkProvider').className = vk?.configured && overview.recoveryDelivery ? 'good' : 'bad';
    }).catch(() => {});

    const profilesP = api('/api/profiles').then((profiles) => {
      app.profiles = profiles;
      renderProfiles();
      bindDynamicActions();
    }).catch(() => {});

    const sessionsP = api('/api/sessions').then(async (sessions) => {
      app.sessions = sessions;
      if (app.selected && !sessions.some((session) => session.id === app.selected)) {
        app.selected = null;
        app.sessionSignature = '';
      }
      renderSessions();
      bindDynamicActions();
      await renderDetail();
    }).catch(() => {});

    await Promise.allSettled([overviewP, profilesP, sessionsP]);
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
	app.vkLoginStatus = status;
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
byId('cancelEdit').addEventListener('click', resetForm);
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
	let stored = 'light';
	try { stored = localStorage.getItem('wlb-theme') || 'light'; } catch (_) {}
	applyTheme(stored);
	byId('themeToggle')?.addEventListener('click', () => {
		const current = document.documentElement.getAttribute('data-theme');
		applyTheme(current === 'dark' ? 'light' : 'dark');
	});
})();

(function initForgeToggle() {
	const forge = document.querySelector('.forge');
	const toggle = byId('forgeToggle');
	if (!forge || !toggle) return;
	let collapsed = false;
	try { collapsed = localStorage.getItem('wlb-forge') === 'collapsed'; } catch (_) {}
	const apply = () => {
		forge.classList.toggle('collapsed', collapsed);
		toggle.textContent = collapsed ? '+' : '−';
		toggle.setAttribute('aria-expanded', String(!collapsed));
	};
	apply();
	toggle.addEventListener('click', () => {
		collapsed = !collapsed;
		try { localStorage.setItem('wlb-forge', collapsed ? 'collapsed' : 'open'); } catch (_) {}
		apply();
	});
})();

run(refresh);
setInterval(() => run(refresh), 2000);
setInterval(() => {
	if (!byId('vkLoginModal').hidden) run(refreshVKLogin);
}, 1700);
