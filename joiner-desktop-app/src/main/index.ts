import { app, BrowserWindow, ipcMain } from 'electron';
import { spawn, spawnSync, ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { IPC, JoinerSettings } from '../constants';

// Single global joiner process. We never run two tunnels at once: the
// wintun adapter and the route table are exclusive resources.
let joinerProcess: ChildProcess | null = null;
let mainWindow: BrowserWindow | null = null;
let captchaWindow: BrowserWindow | null = null;
let userRequestedStop = false;
let reconnectTimer: NodeJS.Timeout | null = null;
let retryCount = 0;
let lastSettings: JoinerSettings | null = null;
const MAX_RETRIES = 8;

function openCaptchaWindow(url: string) {
  if (captchaWindow && !captchaWindow.isDestroyed()) {
    captchaWindow.loadURL(url);
    captchaWindow.focus();
    return;
  }
  captchaWindow = new BrowserWindow({
    width: 520,
    height: 640,
    title: 'Solve the captcha',
    parent: mainWindow ?? undefined,
    autoHideMenuBar: true,
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
  });
  captchaWindow.loadURL(url);
  captchaWindow.on('closed', () => { captchaWindow = null; });
}

function closeCaptchaWindow() {
  if (captchaWindow && !captchaWindow.isDestroyed()) {
    captchaWindow.close();
  }
  captchaWindow = null;
}

function resolveJoinerExe(): string {
  // When packaged, electron-builder copies the backend binary into
  // resources/ under the OS-appropriate name. In dev, fall back to
  // the per-arch artifact next to the Go source.
  const exeName = process.platform === 'win32' ? 'desktop-joiner.exe' : 'desktop-joiner';
  const packaged = join(process.resourcesPath || '', exeName);
  if (existsSync(packaged)) return packaged;

  const baseDir = join(__dirname, '..', '..', 'desktop-joiner');
  if (process.platform === 'darwin') {
    return join(baseDir, 'desktop-joiner-darwin');
  }
  const archMap: Record<string, string> = { x64: 'x64', arm64: 'arm64', ia32: 'ia32' };
  const archTag = archMap[process.arch] ?? 'x64';
  const suffix = process.platform === 'win32' ? '.exe' : '';
  const platTag = process.platform === 'win32' ? 'windows' : 'linux';
  return join(baseDir, `desktop-joiner-${platTag}-${archTag}${suffix}`);
}

function send(channel: string, payload: unknown) {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.webContents.send(channel, payload);
  }
}

function sanitizeLogText(text: string): string {
  return text
    .replace(/(--link\s+)("[^"]*"|\S+)/gi, '$1[REDACTED]')
    .replace(/(--socks-pass\s+)("[^"]*"|\S+)/gi, '$1[REDACTED]')
    .replace(/(vk-auth:\s+okJoinLink=)\S+/gi, '$1[REDACTED]')
	.replace(/(obf\s+key-source=)("[^"]*"|\S+)/gi, '$1[REDACTED]')
    .replace(/((?:sessionKey|anonymToken|access_token|password)[=:]\s*)\S+/gi, '$1[REDACTED]');
}

function safeCommandArgs(args: string[]): string[] {
  const safe = [...args];
  for (let i = 0; i < safe.length - 1; i++) {
    if (safe[i] === '--link' || safe[i] === '--socks-pass') safe[i + 1] = '[REDACTED]';
  }
  return safe;
}

function cleanupStaleWindowsRoutes(exe: string) {
  if (process.platform !== 'win32' || !existsSync(exe)) return;
  const result = spawnSync(exe, ['--cleanup-routes'], {
    windowsHide: true,
    encoding: 'utf8',
    timeout: 10_000,
  });
  if (result.error || result.status !== 0) {
    const detail = result.error?.message || sanitizeLogText(result.stderr || '').trim() || `exit ${result.status}`;
    send(IPC.LOG, `[main] stale-route cleanup warning: ${detail}\n`);
  }
}

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 900,
    height: 600,
    title: 'WhitelistBypass Joiner',
    webPreferences: {
      preload: join(__dirname, '..', 'preload', 'index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
    },
  });
  mainWindow.setMenuBarVisibility(false);
  mainWindow.loadFile(join(__dirname, '..', '..', 'index.html'));
}

app.whenReady().then(() => {
  createWindow();
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  userRequestedStop = true;
  stopJoiner();
  if (process.platform !== 'darwin') app.quit();
});

function spawnJoiner(settings: JoinerSettings): { ok: boolean; error?: string } {
  const exe = resolveJoinerExe();
  if (!existsSync(exe)) {
    return { ok: false, error: `desktop-joiner binary not found at ${exe}` };
  }
  cleanupStaleWindowsRoutes(exe);
  const tunSupported =
    process.platform === 'win32' || process.platform === 'linux' || process.platform === 'darwin';
  const noTun = tunSupported ? settings.noTun : true;
  if (process.platform !== 'win32' && !noTun && process.getuid && process.getuid() !== 0) {
    send(IPC.LOG, `[main] WARNING: ${process.platform} TUN routing needs root; relaunch with sudo or untick the TUN option\n`);
  }
  const args = [
    '--platform', settings.platform,
    '--link', settings.link,
    '--name', settings.displayName,
    '--socks-port', String(settings.socksPort),
    '--tunnel-mode', settings.tunnelMode,
    '--video-reliability', settings.videoReliability,
    '--kcp-profile', settings.kcpProfile,
    '--vp8-fps', String(settings.vp8Fps),
    '--vp8-batch', String(settings.vp8Batch),
    '--resources', settings.resources,
    '--dns', settings.dns,
  ];
  if (settings.socksUser) args.push('--socks-user', settings.socksUser);
  if (settings.socksPass) args.push('--socks-pass', settings.socksPass);
  if (noTun) args.push('--no-tun');
  if (settings.dualTrack && (settings.platform === 'vk' || settings.platform === 'wbstream')) {
    args.push('--dual-track');
  }

  const elevateOnLinux =
    process.platform === 'linux' && !noTun &&
    process.getuid && process.getuid() !== 0;
  const spawnCmd = elevateOnLinux ? 'pkexec' : exe;
  const spawnArgs = elevateOnLinux ? [exe, ...args] : args;
  const commandLine = [spawnCmd, ...safeCommandArgs(spawnArgs)].map((s) => (/\s/.test(s) ? `"${s}"` : s)).join(' ');
  send(IPC.LOG, `[main] spawning: ${commandLine}\n`);
  try {
    joinerProcess = spawn(spawnCmd, spawnArgs, { windowsHide: true });
  } catch (err) {
    return { ok: false, error: `spawn failed: ${(err as Error).message}` };
  }
  send(IPC.RUNNING, true);
  send(IPC.STATUS, 'starting');

  joinerProcess.on('error', (err) => {
    send(IPC.LOG, `[main] spawn error: ${err.message}\n`);
    send(IPC.STATUS, 'stopped');
    send(IPC.RUNNING, false);
    joinerProcess = null;
	cleanupStaleWindowsRoutes(exe);
  });
  const handleOutput = (text: string) => {
	const safeText = sanitizeLogText(text);
	send(IPC.LOG, safeText);
    if (text.includes('TUNNEL ACTIVE')) send(IPC.STATUS, 'active');
    if (text.includes('TUNNEL CONNECTED')) {
      send(IPC.STATUS, 'connected');
      retryCount = 0;
    }
    if (text.includes('adaptive-kcp: reliable data path enabled')) {
      send(IPC.STATUS, 'reliable');
    }
    if (text.includes('adaptive-kcp: legacy raw data path enabled')) {
      send(IPC.STATUS, 'degraded');
    }
    const captchaMatch = text.match(/STATUS:CAPTCHA:(\S+)/);
    if (captchaMatch) {
      openCaptchaWindow(captchaMatch[1]);
    } else if (captchaWindow && /captcha solved|Auth complete|TUNNEL/i.test(text)) {
      closeCaptchaWindow();
    }
  };
  joinerProcess.stdout?.on('data', (b: Buffer) => handleOutput(b.toString()));
  joinerProcess.stderr?.on('data', (b: Buffer) => handleOutput(b.toString()));
  joinerProcess.on('exit', (code, signal) => {
    closeCaptchaWindow();
    send(IPC.LOG, `\n[main] joiner exited code=${code} signal=${signal}\n`);
    send(IPC.STATUS, 'stopped');
    send(IPC.RUNNING, false);
    joinerProcess = null;
	cleanupStaleWindowsRoutes(exe);

    if (userRequestedStop || !lastSettings) return;
    if (retryCount >= MAX_RETRIES) {
      send(IPC.LOG, `[main] auto-reconnect: giving up after ${MAX_RETRIES} attempts\n`);
      return;
    }
    retryCount++;
    const delayMs = Math.min(30_000, 2_000 * 2 ** (retryCount - 1));
    send(IPC.LOG, `[main] auto-reconnect attempt ${retryCount}/${MAX_RETRIES} in ${Math.round(delayMs / 1000)}s\n`);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (userRequestedStop || !lastSettings) return;
      const r = spawnJoiner(lastSettings);
      if (!r.ok) send(IPC.LOG, `[main] auto-reconnect spawn failed: ${r.error}\n`);
    }, delayMs);
  });
  return { ok: true };
}

ipcMain.handle(IPC.START, async (_e, settings: JoinerSettings) => {
  if (joinerProcess) {
    return { ok: false, error: 'joiner already running' };
  }
  userRequestedStop = false;
  retryCount = 0;
  lastSettings = settings;
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  return spawnJoiner(settings);
});

ipcMain.handle(IPC.STOP, async () => {
  userRequestedStop = true;
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  stopJoiner();
  return { ok: true };
});

function stopJoiner() {
  userRequestedStop = true;
  closeCaptchaWindow();
  if (!joinerProcess) {
    cleanupStaleWindowsRoutes(resolveJoinerExe());
    return;
  }
  // On Linux when the Go binary was spawned via pkexec, it runs as
  // root and we (the user) cannot SIGTERM it. The binary watches
  // stdin: writing "QUIT\n" and closing the pipe triggers the same
  // shutdown path as SIGTERM.
  const proc = joinerProcess;
  let gracefulRequested = false;
  try {
    if (proc.stdin?.writable) {
      proc.stdin.write('QUIT\n');
      proc.stdin.end();
      gracefulRequested = true;
    }
  } catch {}
  if (gracefulRequested) {
    setTimeout(() => {
      if (joinerProcess === proc) {
        try { proc.kill('SIGTERM'); } catch {}
      }
    }, 2000);
    return;
  }
  try { proc.kill('SIGTERM'); } catch {}
}
