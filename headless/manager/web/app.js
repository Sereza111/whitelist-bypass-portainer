const byId = (id) => document.getElementById(id);
const stateEl = byId('state');
const startButton = byId('start');
const stopButton = byId('stop');
const errorEl = byId('error');
const linkEl = byId('sessionLink');
const logsEl = byId('logs');

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

function render(status) {
  const running = ['starting', 'running', 'link-ready', 'stopping'].includes(status.state);
  stateEl.textContent = status.state;
  stateEl.dataset.state = status.state;
  byId('sessionState').textContent = status.state;
  byId('sessionMode').textContent = status.mode || '—';
  byId('build').textContent = `${status.buildVersion} / ${(status.buildCommit || '').slice(0, 7)}`;
  linkEl.value = status.sessionLink || '';
  logsEl.textContent = (status.logs || []).join('\n') || 'No events yet';
  logsEl.scrollTop = logsEl.scrollHeight;
  startButton.disabled = running;
  stopButton.disabled = !running;
  if (status.exitError) showError(status.exitError);
}

function showError(message) {
  errorEl.textContent = message;
  errorEl.hidden = !message;
}

async function refresh() {
  try {
    render(await api('/api/status'));
  } catch (error) {
    showError(error.message);
  }
}

startButton.addEventListener('click', async () => {
  showError('');
  try {
    const status = await api('/api/start', {
      method: 'POST',
      body: JSON.stringify({
        mode: byId('mode').value,
        resources: byId('resources').value,
        displayName: byId('displayName').value,
        existingLink: byId('existingLink').value,
        videoReliability: byId('videoReliability').value,
      }),
    });
    render(status);
  } catch (error) {
    showError(error.message);
  }
});

stopButton.addEventListener('click', async () => {
  showError('');
  try {
    render(await api('/api/stop', { method: 'POST', body: '{}' }));
  } catch (error) {
    showError(error.message);
  }
});

byId('reveal').addEventListener('click', () => {
  linkEl.type = linkEl.type === 'password' ? 'text' : 'password';
  byId('reveal').textContent = linkEl.type === 'password' ? 'Reveal' : 'Hide';
});

byId('copy').addEventListener('click', async () => {
  if (linkEl.value) await navigator.clipboard.writeText(linkEl.value);
});

refresh();
setInterval(refresh, 2000);
