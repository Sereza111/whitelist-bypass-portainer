const byId = (id) => document.getElementById(id);
const app = {
  profiles: [], sessions: [], selected: null, refreshing: false,
  profileSignature: '', sessionSignature: '',
};

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
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
    return `<article class="profile-card ${state}">
      <div class="profile-glyph">${escapeHTML(profile.name.slice(0, 1).toUpperCase())}</div>
      <div class="profile-copy">
        <div class="profile-heading"><h3>${escapeHTML(profile.name)}</h3><span>${profile.enabled ? 'ACTIVE' : 'LOCKED'}</span></div>
		<p>${escapeHTML(profile.config.mode.toUpperCase())} · ${profile.autoRestart ? 'AUTO RECOVERY' : 'MANUAL'} · limit ${profile.maxSessions}</p>
      </div>
      <div class="profile-actions">
        <button class="small start-client" data-id="${profile.id}" ${profile.enabled ? '' : 'disabled'}>Запустить</button>
		<button class="small mobile-client" data-id="${profile.id}">В телефон</button>
        <button class="small toggle-client" data-id="${profile.id}">${profile.enabled ? 'Отключить' : 'Включить'}</button>
        <button class="icon edit-client" data-id="${profile.id}" title="Изменить">✎</button>
        <button class="icon danger delete-client" data-id="${profile.id}" title="Удалить">×</button>
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
    return `<article class="session-card ${selected}" data-session-id="${session.id}" data-state="${escapeHTML(status.state)}">
      <button class="session-open" data-id="${session.id}">
        <span class="status-rune"></span>
		<span><strong>${escapeHTML(session.clientName)}</strong><small>${escapeHTML(friendlyState(status))}</small></span>
      </button>
      <div class="session-rate"><span class="session-tx">TX ${escapeHTML(tx)}</span><span class="session-rx">RX ${escapeHTML(rx)} kbps</span></div>
      <div class="session-actions">
        ${activeState(status.state) ? `<button class="small stop-session" data-id="${session.id}">Стоп</button>` : `<button class="small delete-session" data-id="${session.id}">Убрать</button>`}
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
    app.selected = button.dataset.id; renderSessions(); run(renderDetail);
  });
  document.querySelectorAll('.stop-session').forEach((button) => button.onclick = () => run(() => stopSession(button.dataset.id)));
  document.querySelectorAll('.delete-session').forEach((button) => button.onclick = () => run(() => deleteSession(button.dataset.id)));
}

async function run(action) {
  showError();
  try { await action(); } catch (error) { showError(error.message); }
}

async function refresh() {
  if (app.refreshing) return;
  app.refreshing = true;
  try {
    const [overview, profiles, sessions] = await Promise.all([
      api('/api/overview'), api('/api/profiles'), api('/api/sessions'),
    ]);
    app.profiles = profiles;
    app.sessions = sessions;
    if (app.selected && !sessions.some((session) => session.id === app.selected)) {
      app.selected = null;
      app.sessionSignature = '';
    }
    byId('build').textContent = `${overview.buildVersion} / ${(overview.buildCommit || '').slice(0, 7)}`;
    byId('activeCount').textContent = overview.activeSessions;
    byId('sessionLimit').textContent = `из ${overview.maxSessions}`;
    byId('clientCount').textContent = overview.clientCount;
    const vk = overview.providers.find((provider) => provider.id === 'vk');
	byId('vkProvider').textContent = !vk?.configured ? 'missing' : (overview.recoveryDelivery ? 'ready + recovery' : 'set VK_PEER_ID');
	byId('vkProvider').className = vk?.configured && overview.recoveryDelivery ? 'good' : 'bad';
    renderProfiles();
    renderSessions();
    bindDynamicActions();
    await renderDetail();
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

run(refresh);
setInterval(() => run(refresh), 2000);
