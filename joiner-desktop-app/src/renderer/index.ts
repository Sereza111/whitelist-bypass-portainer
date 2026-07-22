type JoinerPlatform = 'wbstream' | 'telemost' | 'vk' | 'dion';

function detectPlatform(url: string): JoinerPlatform | null {
  const u = url.toLowerCase();
  if (!u) return null;
  if (u.includes('wbstream://') || u.includes('stream.wb.ru')) return 'wbstream';
  if (u.includes('telemost.yandex')) return 'telemost';
  if (u.includes('dion://') || u.includes('dion.vc')) return 'dion';
  return 'vk';
}

function platformLabel(p: JoinerPlatform | null): string {
  switch (p) {
    case 'wbstream': return 'WB Stream';
    case 'telemost': return 'Telemost';
    case 'vk': return 'VK';
    case 'dion': return 'DION';
    default: return '-';
  }
}

interface Bridge {
  start(settings: any): Promise<{ ok: boolean; error?: string }>;
  stop(): Promise<{ ok: boolean }>;
  copyText(value: string): Promise<{ ok: boolean }>;
  onLog(cb: (text: string) => void): void;
  onStatus(cb: (status: string) => void): void;
  onRunning(cb: (running: boolean) => void): void;
}
declare const bridge: Bridge;

const $ = (id: string) => document.getElementById(id) as HTMLElement;
const input = (id: string) => document.getElementById(id) as HTMLInputElement;
const select = (id: string) => document.getElementById(id) as HTMLSelectElement;

const logEl = $('log') as HTMLPreElement;
const statusEl = $('status');
const startBtn = $('start') as HTMLButtonElement;
const stopBtn = $('stop') as HTMLButtonElement;
const downloadLogsBtn = $('downloadLogs') as HTMLButtonElement;
const platformHint = $('platformHint');
const linkInput = input('link');
const phoneGateway = input('phoneGateway');
const phoneGatewayFields = $('phoneGatewayFields');
const summaryPlatform = $('summaryPlatform');
const summaryRoute = $('summaryRoute');
const summaryProfile = $('summaryProfile');
const kcpSafety = $('kcpSafety');
const routingModePanel = $('routingModePanel');
const routeTun = input('routeTun');
const routeProxy = input('routeProxy');
const routeTunChoice = $('routeTunChoice');
const routeProxyChoice = $('routeProxyChoice');
const proxyModeDetails = $('proxyModeDetails');
const proxyEndpoint = $('proxyEndpoint');
const proxyAuthSummary = $('proxyAuthSummary');
const copyProxyConfig = $('copyProxyConfig') as HTMLButtonElement;
const proxyCopyStatus = $('proxyCopyStatus');
const callOnlyInputIds = [
	'link', 'name', 'socksPort', 'socksUser', 'socksPass', 'tunnelMode',
	'videoReliability', 'kcpProfile', 'vp8Fps', 'vp8Batch', 'dualTrack',
];

stopBtn.disabled = true;

downloadLogsBtn.addEventListener('click', () => {
  const blob = new Blob([logEl.textContent || ''], { type: 'text/plain' });
  const anchor = document.createElement('a');
  anchor.href = URL.createObjectURL(blob);
  anchor.download = 'joiner-logs-' + new Date().toISOString().replace(/[:.]/g, '-') + '.txt';
  anchor.click();
  URL.revokeObjectURL(anchor.href);
});

function refreshPlatformHint() {
	if (phoneGateway.checked) {
		platformHint.textContent = 'Phone SOCKS gateway mode';
		platformHint.dataset.detected = '';
		refreshConnectionSummary();
		return;
	}
  const p = detectPlatform(linkInput.value.trim());
  platformHint.textContent = `Detected platform: ${platformLabel(p)}`;
  platformHint.dataset.detected = p ?? '';
	refreshConnectionSummary();
}
linkInput.addEventListener('input', refreshPlatformHint);
refreshPlatformHint();

function proxyConfigText(): string {
	const port = parseInt(input('socksPort').value, 10) || 1080;
	const user = input('socksUser').value.trim();
	const pass = input('socksPass').value;
	const lines = [
		'WhitelistBypass local SOCKS5',
		'Host: 127.0.0.1',
		`Port: ${port}`,
	];
	if (user) lines.push(`User: ${user}`);
	if (pass) lines.push(`Password: ${pass}`);
	return lines.join('\n');
}

function refreshRoutingMode(): void {
	const proxy = routeProxy.checked;
	routeTunChoice.dataset.active = String(!proxy);
	routeProxyChoice.dataset.active = String(proxy);
	proxyModeDetails.hidden = !proxy || phoneGateway.checked;
	try { localStorage.setItem('wlb-route-mode', proxy ? 'proxy' : 'tun'); } catch (_) { /* ignore */ }
	refreshProxyDetails();
	refreshConnectionSummary();
}

function refreshProxyDetails(): void {
	const port = parseInt(input('socksPort').value, 10) || 1080;
	const user = input('socksUser').value.trim();
	proxyEndpoint.textContent = `127.0.0.1:${port}`;
	proxyAuthSummary.textContent = user
		? `Localhost only · authentication: ${user}`
		: 'Localhost only · no system routes';
}

routeTun.addEventListener('change', refreshRoutingMode);
routeProxy.addEventListener('change', refreshRoutingMode);
for (const id of ['socksPort', 'socksUser', 'socksPass']) {
	document.getElementById(id)?.addEventListener('input', refreshProxyDetails);
}
copyProxyConfig.addEventListener('click', async () => {
	try {
		const result = await bridge.copyText(proxyConfigText());
		proxyCopyStatus.textContent = result.ok ? 'Copied' : 'Clipboard unavailable';
	} catch (_) {
		proxyCopyStatus.textContent = 'Clipboard unavailable';
	}
	window.setTimeout(() => { proxyCopyStatus.textContent = ''; }, 2200);
});

try {
	if (localStorage.getItem('wlb-route-mode') === 'proxy') routeProxy.checked = true;
} catch (_) { /* ignore */ }

function refreshConnectionMode() {
	const usePhone = phoneGateway.checked;
	phoneGatewayFields.hidden = !usePhone;
	routingModePanel.hidden = usePhone;
	for (const id of callOnlyInputIds) {
		input(id).closest('label')?.toggleAttribute('hidden', usePhone);
	}
	document.querySelectorAll<HTMLDivElement>('.form .row').forEach((row) => {
		const children = Array.from(row.children) as HTMLElement[];
		row.hidden = children.length > 0 && children.every((child) => child.hidden);
	});
	proxyModeDetails.hidden = usePhone || !routeProxy.checked;
	refreshPlatformHint();
}

function refreshConnectionSummary(): void {
	const usePhone = phoneGateway.checked;
	const detected = usePhone ? null : detectPlatform(linkInput.value.trim());
	summaryPlatform.textContent = usePhone ? 'Android' : platformLabel(detected);
	summaryRoute.textContent = usePhone ? 'Phone gateway' : (routeProxy.checked ? 'Local SOCKS5' : 'System TUN');
	const reliability = select('videoReliability').value;
	const profile = select('kcpProfile').value;
	summaryProfile.textContent = usePhone ? 'Phone managed' : (reliability === 'raw' ? 'Legacy raw' : profile[0].toUpperCase() + profile.slice(1));
	const unsafeFast = !usePhone && reliability !== 'raw' && profile === 'fast' && !routeProxy.checked;
	kcpSafety.hidden = !unsafeFast;
	kcpSafety.textContent = unsafeFast
		? 'Fast is unsafe for full TUN and will be clamped to Balanced. Use Fast only for a controlled SOCKS-only test.'
		: '';
}

phoneGateway.addEventListener('change', refreshConnectionMode);
refreshConnectionMode();
refreshRoutingMode();

for (const id of ['tunnelMode', 'videoReliability', 'kcpProfile', 'routeTun', 'routeProxy']) {
	document.getElementById(id)?.addEventListener('change', refreshConnectionSummary);
}

input('phoneConfig').addEventListener('input', () => {
	const config = input('phoneConfig').value;
	const read = (key: string) => config.match(new RegExp(`^${key}:\\s*(.+)$`, 'mi'))?.[1].trim() || '';
	const host = read('Host');
	const port = read('Port');
	const user = read('User');
	const pass = read('Password');
	if (host) input('phoneHost').value = host;
	if (port) input('phonePort').value = port;
	if (user) input('phoneUser').value = user;
	if (pass) input('phonePass').value = pass;
});

function appendLog(text: string) {
  logEl.textContent += text;
  logEl.scrollTop = logEl.scrollHeight;
}

bridge.onLog((text) => appendLog(text));
bridge.onStatus((s) => {
  statusEl.textContent = s;
  statusEl.dataset.state = s;
});
bridge.onRunning((running) => {
  startBtn.disabled = running;
  stopBtn.disabled = !running;
  routeTun.disabled = running;
  routeProxy.disabled = running;
  routeTunChoice.dataset.locked = String(running);
  routeProxyChoice.dataset.locked = String(running);
});

startBtn.addEventListener('click', async () => {
  appendLog('\n[ui] starting joiner...\n');
	const usePhone = phoneGateway.checked;
  const link = linkInput.value.trim();
	if (!usePhone && !link) {
    appendLog('[ui] link is required\n');
    return;
  }
	const platform = usePhone ? 'vk' : detectPlatform(link);
	if (!platform) {
    appendLog('[ui] link does not look like a WB Stream or Telemost call\n');
    return;
  }
	const phoneHost = input('phoneHost').value.trim();
	const phonePort = parseInt(input('phonePort').value, 10) || 0;
	const phoneUser = input('phoneUser').value.trim();
	const phonePass = input('phonePass').value;
	if (usePhone && (!/^\d{1,3}(?:\.\d{1,3}){3}$/.test(phoneHost) || phonePort < 1 || phonePort > 65535)) {
		appendLog('[ui] enter the phone IPv4 address and SOCKS5 port copied from Android\n');
		return;
	}
	if (usePhone && (!phoneUser || !phonePass)) {
		appendLog('[ui] phone SOCKS5 username and password are required\n');
		return;
	}
  const settings = {
	connectionMode: usePhone ? 'phone' : 'call',
    platform,
    link,
    displayName: input('name').value.trim() || 'Joiner',
    socksPort: parseInt(input('socksPort').value, 10) || 1080,
    socksUser: input('socksUser').value,
    socksPass: input('socksPass').value,
    tunnelMode: select('tunnelMode').value,
    videoReliability: select('videoReliability').value,
    kcpProfile: select('kcpProfile').value,
    vp8Fps: parseInt(input('vp8Fps').value, 10) || 24,
    vp8Batch: parseInt(input('vp8Batch').value, 10) || 30,
    resources: select('resources').value,
    dns: input('dns').value.trim() || '1.1.1.1,8.8.8.8',
    noTun: routeProxy.checked,
    dualTrack: input('dualTrack').checked,
	phoneHost,
	phonePort,
	phoneUser,
	phonePass,
  };
  const r = await bridge.start(settings);
  if (!r.ok) appendLog(`[ui] start failed: ${r.error}\n`);
});

stopBtn.addEventListener('click', async () => {
  appendLog('\n[ui] stopping joiner...\n');
  await bridge.stop();
});

// Theme toggle: Argent (light marble) / Sable (black marble), persisted.
function applyTheme(theme: string): void {
  const next = theme === 'light' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  try { localStorage.setItem('wlb-theme', next); } catch (_) { /* ignore */ }
  const label = document.getElementById('themeLabel');
  if (label) label.textContent = next === 'light' ? 'Argent' : 'Sable';
}
(() => {
  let stored = 'dark';
  try { stored = localStorage.getItem('wlb-theme') || 'dark'; } catch (_) { /* ignore */ }
  applyTheme(stored);
  document.getElementById('themeToggle')?.addEventListener('click', () => {
    const current = document.documentElement.getAttribute('data-theme');
    applyTheme(current === 'light' ? 'dark' : 'light');
  });
})();
