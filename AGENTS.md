# Persistent project context

Before changing the transport, read:

1. `docs/PROTOCOL_ARCHITECTURE.md`
2. `docs/PERFORMANCE_ROADMAP.md`
3. `docs/TARGET_ARCHITECTURE.md`
4. `docs/PRODUCT_ROADMAP.md`
5. upstream source at commit `64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943`

## Current objective

Turn the experimental whitelist-bypass tunnel into a measurable, stable
server/client system. The current deployment uses the direct VK creator in
Portainer and a headless Joiner in Video mode.

## Active handoff (2026-07-26, alpha.19 device-assisted WB completion)

- The user explicitly required mobile WB onboarding; do not regress to asking
  for phone/OTP in the server panel. The VPS independently returns `HTTP 498`,
  `server: wbaas`, `status-no-id: PG-13-XS` for `stream.wb.ru/login`. This
  proves server Chromium timeout/retry is not the primary solution.
- Alpha.19 replaces the panel's main WB flow with a 10-minute phone QR. The QR
  contains an HTTPS `/wb-device#TOKEN` landing URL; fragments are not sent to
  Nginx. Manager stores only SHA-256(token), and the Android upload sends the
  token in the Authorization header rather than the request path.
- Android handles `wlb://wb-login`, opens WB in an in-app WebView on the phone
  network, reads only the WB allowlist plus device id and uploads it over HTTPS.
  Manager still validates `slide-v3` before atomically saving mode-0600 managed
  cookies. Never log the pairing URL, Authorization header, cookies, phone or
  OTP. The regular guest client onboarding remains separate and cookie-free.
- Panel QR generation is local (`go-qrcode`); no external QR service receives
  the bearer. Public `/wb-device`, JS and CSS contain no server credential and
  are served without Basic Auth so a scanned phone can open the app.
- Manager tests/vet/build, panel JS syntax and Windows TypeScript pass locally.
  Relay tests passed but Windows antivirus denied cleanup of the already-passed
  temporary `tunnel.test.exe`; standalone relay vet passed. Branch and immutable
  tagged Android, Windows and Docker workflows all passed.
- `v0.5.0-alpha.19` is published from immutable commit `61f5943`; do not move
  the tag. Release APK SHA-256 is
  `f0f711387e9ef376380e04ad910001b931347466d177340ff64e632799ffc494` and
  EXE SHA-256 is
  `37dacece8f8f0708b50e49667334d90a217f390d2bf8a37a044f3bcb3d2a03e4`.

## Active handoff (2026-07-26, alpha.16 completion)

- Matching alpha.15 Android field logs `relay (17).log` and `relay (18).log`
  are external-only and must never be committed. Captcha completion is fixed.
- DC control ran at only 20–51 kbps and proved the exact compatibility split:
  `mode=dc`, handshake timeout, `wire=0 caps=0x0 legacy=true`, no KCP and empty
  scheduler queues. Creator DC still used its standalone manual mux instead of
  RelayBridge, so it ignored `MsgHello` and modern capabilities.
- Video control negotiated matching server `0.5.0-alpha.15+e6f7bc7`, `wire=1`,
  `caps=0x3b`, reliable DNS/control KCP and reached 668.5 kbps. `WaitSnd` was
  only 16/256 with zero queue drops, ACK stalls or carrier stalls. The measured
  ~0.8 Mbps limit is currently the VK VP8 carrier, not a saturated KCP window.
- Alpha.16 removes the separate Creator DC mux and passes DC through DCTunnel
  plus RelayBridge. Do not wrap reliable SCTP in KCP. DC output observes the
  configured DataChannel buffered-amount bound. Video remains the default and
  DC remains experimental pending matching field verification.
- Panel Android onboarding now creates a random in-memory 15-minute
  `/join/<token>` bearer link. The unauthenticated no-cache landing page opens
  `wlb://import`; Android validates the provider-bound VK/WB/Telemost/Dion call
  invite, asks for confirmation, imports it in Video mode, then uses the
  existing VpnService permission flow. No client VK cookies are collected.
  Recommend HTTPS before sharing invites over untrusted networks; never log or
  publish the invite URL.
- Relay, Creator and Manager tests/vet plus panel JS syntax pass locally.
  Desktop tests/vet and TypeScript build passed; a Windows antivirus briefly
  delayed deletion of already-passed temporary test binaries.
- `v0.5.0-alpha.16` is published at immutable commit `c0db283`. Branch and tag
  Android, Windows and Docker workflows passed. APK SHA-256 is
  `e812349458843e52b31c9f16070c4bad3e9d114e1b4163ae35a961c654398936`;
  EXE SHA-256 is
  `71dba811e3a010f0aab1a0d3dcba00517d559ae541277a4274563ef5faebf73a`.
  Both checksum files match GitHub asset digests. GHCR contains `linux/amd64`,
  `linux/arm64` and `linux/386`. Do not move or replace the published tag.

## Active handoff (2026-07-26, provider-aware Android pairing in progress)

- The field result from `relay (20).log` is not a valid 1.2 Mbps tunnel result:
  Proxy-only used a loopback SOCKS listener but the log contained no `SOCKS
  CONNECT`, `tcp=0`, `tunnel_tx=0` and `tunnel_rx=0`. Use Tunnel for phone
  system traffic, or enable Android **Share SOCKS5 over LAN** and configure the
  PC explicitly. Never commit that external log.
- Provider choice can change the carrier ceiling. Current preferred A/B order is
  VK Video/DC, WB Stream DC/Video/dual-track, then Telemost/Dion. WB Stream has
  reliable ordered DataChannel and Video+KCP in the current implementation;
  Telemost and Dion Video remain raw VP8 without extra ARQ.
- The panel previously rejected **В телефон** for every non-VK profile even
  though manager, Windows and Android already supported WB Stream/Telemost/Dion.
  The working tree now removes that restriction. Mobile payloads remain `v=1`
  for compatibility and add optional `provider`; manager and Android bind each
  provider to an allowlisted scheme/host before importing. Old VK payloads with
  no provider still work. Automatic VK recovery messages remain VK-only.
- Added `docs/PROVIDER_COMPARISON.md`, provider invite Go tests and Android
  `CallPlatformTest`. Manager `go test ./...` and `go vet ./...` passed with
  portable Go 1.26.5. Android Gradle/JDK are not installed locally; CI must run
  the Android unit tests and release build.
- `v0.5.0-alpha.17` is now published from commit `35dcab5`; do not move the
  immutable tag. Branch Docker, Windows and Android workflows all passed.
  Release APK SHA-256 is
  `3d8c087c534c4f0158414d40b3142c008f5d9c72af61a9cebbc3a2bf557abed4` and
  EXE SHA-256 is
  `53fff074ec1b72947de28f9379dd01bcc7e5904dda3c86d201c8fe147be579d3`.
  Before declaring WB transport faster, field-test the release with actual
  `SOCKS CONNECT`, `tcp>0`, and nonzero `tunnel_tx/rx`.
- Panel-managed WB onboarding is now implemented in the working tree. It opens
  `stream.wb.ru/login` in an isolated Chromium, advances to the phone form,
  accepts the phone and one-time code through same-origin authenticated API,
  validates the resulting allowlisted cookies against WB `slide-v3`, adds the
  browser `wb_auth_api_device_id` as `__wb_device_id`, and atomically stores
  `/data/managed-secrets/cookies-wbstream.json` with mode `0600`. Phone/code are
  transient and never enter status, events, logs or control-plane JSON.
- The Providers page has a WB card and phone/code wizard. Starting a profile
  whose provider credentials are missing redirects to Providers with a clear
  error. Provider readiness now parses required cookie names instead of treating
  an empty `[]` JSON file as configured. Local API/browser smoke testing reached
  WB state `phone`; no real phone or OTP was submitted during development.

## Active handoff (2026-07-26, alpha.18 HTTPS/WB hardening candidate)

- User created `panel.yozik.ru` with A `93.189.230.198`; external DNS resolves
  correctly. The VPS already runs Nginx on TCP/80 and TCP/443. HTTPS currently
  presents a certificate for the wrong principal and returns Nginx 502, while
  HTTP returns Nginx 404. Do not add Caddy or bind another service to 80/443.
- Candidate Stack binds manager only to `127.0.0.1:9200:8080`. The included
  `docs/nginx-panel.yozik.ru.conf` is an HTTP bootstrap proxy to loopback; on
  the VPS install it, validate/reload Nginx, then run
  `certbot --nginx -d panel.yozik.ru` so Certbot adds the correct certificate
  and redirect. Keep `/data` volume.
- Field alpha.17 WB login failed before any phone input with the generic phone
  form timeout. Candidate raises startup wait from 45s to 2m, repeatedly looks
  for the visible login action/phone input without depending on one placeholder,
  and captures a pre-credential diagnostic screenshot on failure. Screenshot
  API remains Basic-Auth protected and no screenshot is taken after phone/code.
- `v0.5.0-alpha.18` is published from immutable commit `a48b1d3`; do not move
  the tag. Branch and tag Docker, Windows and Android workflows passed. Release
  APK SHA-256 is
  `b620ef68a312839e7e3844c4862c4e8eb9a85c32e4a753da2dde218d862c69ef` and
  EXE SHA-256 is
  `285abcf9f244d7751f82220d28914fa3cf00c5f30da768727eb9af26449fd781`.

## Active handoff (2026-07-25, alpha.15 completion)

- Matching Android alpha.14 field log `relay (15).log` is external-only and
  must never be committed. It reaches `Captcha page ready`, but never logs
  `captcha proxy: completion captured` or `vk-auth: captcha solved`.
- The decisive evidence is one proxied HTTP 301 followed by no primary or
  secondary responses. VK redirected the WebView to another origin; the old
  proxy rewrote only redirects containing the original origin, so the visible
  challenge escaped loopback and its successful result was invisible locally.
- Alpha.15 resolves every redirect against the response request URL. Original-
  origin redirects map to loopback paths; cross-origin redirects map to
  `generic_proxy`. Secondary HTML now receives the same completion hook, an
  upstream base URL, and relative Fetch/XHR/DOM URLs are routed through the
  generic proxy. Never log the rewritten URL, query, token, link or cookies.
- Integration coverage reproduces a 301 between two upstream servers and
  asserts the final client host remains `127.0.0.1` and the secondary HTML has
  the completion hook. Relay tests/vet, manager tests/vet/JS syntax, desktop
  Joiner tests/vet and TypeScript build passed locally.
- `v0.5.0-alpha.15` is published at immutable commit `490d8d0`. Branch and tag
  Android, Windows and Docker workflows passed. The APK checksum is
  `04056e97bf2e14e32529e67f1212d7b3f643f73649527de73dca117abc305bf3`;
  the EXE checksum is
  `c43c3f9bb2f1a5348f2564de422051f31ae5917ac49ec42cc7d5ab215fc4ff9d`.
  Both checksum-file values match GitHub asset digests. GHCR contains
  `linux/amd64`, `linux/arm64` and `linux/386`. Do not move or replace the tag.

## Active handoff (2026-07-25, alpha.14 completion)

- Matching Android alpha.13 field log `relay (14).log` is external-only and
  must never be committed. It proves build `d58a01a` ran, then stopped at
  `vk-auth: captcha required`. The user pressed Retry repeatedly, but the log
  contained neither `Captcha page ready` nor `captcha solved`.
- Alpha.14 candidate broadens captcha completion beyond endpoint-specific JSON:
  cross-origin `postMessage`, nested success events, JSONP, form/text bodies,
  query/hash, XHR/Fetch and DOM are inspected. WebView enables third-party
  cookies and no-cache loading. Safe logs expose page lifecycle and response
  class/status/size only; never URLs, query, tokens, links or cookies.
- Per user request Android main now shows only Tunnel and Proxy. Transparent
  selected-app routing remains under Settings -> Split tunneling -> Only and
  continues to use VpnService. Internal RoutingMode migration/safety checks are
  preserved.
- Do not implement client VK cookie extraction/upload. Android cannot read the
  VK app's cookies under the normal sandbox, and uploading them would hand the
  personal account to the server. Normal users already join anonymously: one
  server VK creates isolated panel sessions, then admin uses New client ->
  Start -> Copy to phone. Panel text now explains this. A future multi-identity
  provider pool must use isolated server-side QR slots.
- `v0.5.0-alpha.14` is published at immutable commit `6b6fd7c`. Local Go
  tests/vet, manager checks and TypeScript passed; branch and tag Android,
  Windows and Docker workflows passed. Release APK/EXE checksum values match
  GitHub asset digests. GHCR has `linux/amd64`, `linux/arm64` and `linux/386`.
  Do not move or replace the tag.

## Active handoff (2026-07-25, alpha.13 completion)

- User field logs `relay (11).log`, `(12).log`, `(13).log` remain outside the
  repository. The successful control passed captcha and ran `7h10m50s` with
  `kcp_wait_snd=7`, zero drops/stalls and bounded fair wait. Broken starts stop
  at `vk-auth: captcha required`; no `captcha solved`, OK session or auth done.
  Panel «Жду устройство» is expected because Creator is alive while Android is
  blocked locally. Restarting the panel does not repair this state.
- Root captcha bug: `captcha_proxy.go` inspected only the legacy
  `captchaNotRobot.check` path. Cross-origin/new VK JSON used `generic_proxy`,
  whose responses were not inspected. Candidate inspects any captcha/JSON
  response, nested token field, query/hash redirects, XHR/Fetch/navigation/DOM,
  and has an Android Retry page action. Never log tokens or full URLs.
- Android routing is now three first-class modes: Device (full VpnService),
  Apps (transparent selected-app VpnService, Happ-style), and SOCKS5 (manual
  local/LAN gateway, no VpnService). Legacy proxy/split preferences derive the
  new mode without rewriting. Empty/uninstalled Apps selection fails closed.
- Matching VK defaults move from Balanced to Auto KCP. Auto samples ACKed
  segments every 2s, varies send window 256–512, grows by 32 only under demand,
  shrinks by 64, and returns to 256 idle. Metrics add `kcp_window` and
  `kcp_auto_changes`. New capability `kcp_auto` prevents wire code 4 from being
  sent to alpha.12/older peers; they receive bounded Balanced fallback.
- Local `go test ./...` and `go vet ./...` pass for relay, VK Creator, manager
  and desktop Joiner. Windows TypeScript build passes. `go test -race` is not
  locally available because CGO/compiler is absent. Android requires CI.
- `v0.5.0-alpha.13` is published at immutable commit `d58a01a`. Branch and tag
  Android/Windows/Docker workflows passed. Release APK/EXE checksum-file values
  match GitHub asset SHA-256 digests. GHCR contains `linux/amd64`,
  `linux/arm64` and `linux/386`. Do not move or replace the tag. Published
  `v0.5.0-alpha.12` at `680966f` also remains immutable.

## Active handoff (2026-07-23, alpha.12 completion)

- New matching alpha.11 field data is in user-supplied logs only; never commit
  those logs or screenshots. Android reported `caps=0x1b`, balanced KCP, zero
  KCP drops/stalls and live ACK progress. Actual relay peak was about 1.1Mbps,
  while Speedtest loaded ping reached 7064ms. A transient UI estimate near
  270Mbps was not supported by relay byte counters.
- Root cause is bufferbloat, not a silent carrier failure: Joiner reached
  `fair_queue_max≈1.05MiB`, `fair_max_wait_ms≈15.9s` and `WaitSnd=1024` for
  more than a minute. Creator history reached `fair_queue_max≈4.19MiB`,
  `fair_max_wait_ms≈38.6s` and about 52.8s cumulative KCP backpressure.
- Alpha.12 reduces balanced KCP from 1024 to 512 segments, DRR
  staging from 256KiB/flow + 8MiB total to 64KiB/flow + 512KiB total, exposes
  both limits in METRICS, cancels unsent flow backlog after remote CLOSE, and
  sends/logs only one NACK for repeated DATA on an unknown Creator flow.
- The Android VPN/Proxy selector did exist in alpha.11, but both option rows
  used `match_parent` inside a `wrap_content` parent. Huawei rendered them as
  clipped dashes. Both rows now use `wrap_content`, and Android CI parses the
  XML to prevent regression.
- Portable Go 1.26.1 in the local temp directory ran `go test ./...`, `go vet
  ./...` and repeated tunnel tests successfully. Android still requires CI
  because the workstation has no Java/Android SDK or Gradle wrapper.
- `v0.5.0-alpha.12` is published at immutable commit `680966f`. Branch and tag
  Android/Windows/Docker workflows passed. Release APK/EXE checksums match the
  GitHub asset digests. GHCR contains `linux/amd64`, `linux/arm64` and
  `linux/386`. Do not move or replace the tag.
- Next field gate: matching alpha.12 APK + Docker, first verify that VPN/Proxy
  cards are visible while disconnected, then compare VPN Speedtest and a
  SOCKS-only benchmark. Record `fair_queue_limit`, `fair_flow_limit`,
  `fair_max_wait_ms`, `kcp_wait_snd`, loaded ping and actual relay kbps.

## Active handoff (2026-07-22, alpha.11 completion)

- `v0.5.0-alpha.11` is published at commit `04d278a`. Branch and tagged
  Android, Windows and Docker workflows passed. Release APK/EXE plus checksum
  assets exist, the persistent Android signer check passed, the APK checksum
  matches GitHub's asset digest, and GHCR `amd64`/`arm64`/`386` manifests were
  verified. Do not move or replace the published tag.
- Windows and Android now expose first-class `VPN / Proxy` routing selectors.
  Proxy mode uses the existing authenticated local SOCKS5 listener and skips
  Wintun/Android VpnService. Windows shows/copies `127.0.0.1:<port>`; Android
  shows the same endpoint and can open its detailed proxy settings. Split-TUN
  scope is visible on the Android selector.
- Field `relay.log` reported `caps=0x1b`, balanced KCP, zero KCP drops,
  `fair_max_wait_ms <= 2.3` and only 618 kbps maximum relay RX. A 51 Mbps
  Speedtest screenshot therefore measured direct traffic, consistent with the
  user's observation that blocking bypass was inactive. A normal Speedtest app
  does not use SOCKS5 without explicit support/configuration.
- Repeating `wait_snd=2` is not an error code. The server log showed an old KCP
  instance surviving a Pion peer replacement while the replacement offer never
  reached connected state. `TunnelRelay.Close` now closes the RelayBridge,
  adaptive data/control KCP loops and flow state exactly once.
- VK Creator now arms a 30-second offer watchdog, gives disconnected peers a
  15-second grace period, and escalates failed/closed peers immediately. Three
  failed peer recovery cycles terminate Creator so manager auto-restart creates
  a new call and signed recovery generation. A successful connection resets
  the counter. Manager derives peer recovery logs as degraded.
- Android filters private, link-local, CGNAT and IPv6 carrier DNS addresses in
  automatic mode. If no server-reachable IPv4 resolver remains it falls back
  to the configured public defaults. The field log had 34 reliable DNS queries
  and zero replies, plus attempts to a carrier-private address on TCP/853.
- One VK Creator call currently supports one active Joiner. A second registered
  peer replaces the first. Scale by creating one panel profile/session per
  client; joining the public call link is guest/anonym-token based and does not
  require the client to log into the server VK account. Android signed recovery
  still needs the recipient's VK peer id if automatic DM delivery is desired.
- Runtime and CI defaults are aligned at `0.5.0-alpha.11`. Deploy
  `ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.11` without
  deleting persistent `/data`, and field-test matching alpha.11 clients.

## Active handoff (2026-07-22, alpha.10 completion)

- `v0.5.0-alpha.10` is published at commit `18fcb28`. Branch and tagged
  Android, Windows and Docker workflows passed. The APK/EXE release digests,
  persistent Android signer check and GHCR `amd64`/`arm64`/`386` manifests
  were verified. Do not move or replace the published tag.
- `main` now contains the complete panel control-center redesign. Commit
  `9d5dd6a` adds dashboard/clients/sessions/providers/events/settings sections,
  desktop sidebar, mobile bottom navigation, dense profile registry, VK QR
  identity, panel-managed global recovery recipient, per-profile override,
  safe test messages, profile duplication and a bounded structured event log.
- Recovery recipient precedence is profile -> panel -> legacy `VK_PEER_ID`.
  Recipient changes affect new Creator processes and the next supervised
  restart. Cookie/token/signed WLB2 content must never enter events or API
  errors. `/api/profiles` still returns each recovery key because Android
  pairing currently depends on it; do not copy that field into diagnostics.
- The panel was checked in Argent and Sable, with navigation, client creation,
  context menus, recovery settings/error states and a 390x844 responsive pass.
  Go tests/vet and JS syntax checks pass.
- Merge commit `3e9430e` integrates bounded per-flow queues and DRR from
  `codex/transport-fair-queue`. `MsgData`, ordered `MsgClose`, `MsgUDP` and
  `MsgUDPReply` use the per-conn scheduler; CONNECT/DNS/hello remain on the
  negotiated priority path. This changes scheduling only, not the wire format.
  Metrics now include fair flows, queued frames/bytes and average/max wait.
- Version defaults and CI metadata are aligned at `0.5.0-alpha.10`. The
  published deployment image is
  `ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.10`; preserve the
  persistent `/data` volume while redeploying it.
- Field gate: uninstall the old debug-signed alpha.8 once if it is still on the
  phone, install signed alpha.10, redeploy the matching tagged Docker image and
  confirm both logs report alpha.10. Compare `fair_queue`, `fair_avg_wait_ms`,
  DNS latency, CONNECT latency and loaded latency during concurrent bulk +
  short HTTPS probes; DRR is intended to improve fairness/latency, not raise the
  VP8 carrier's physical throughput ceiling.

## Active handoff (2026-07-22, alpha.9 completion)

- `v0.5.0-alpha.9` is published at commit `3534d9f`. Android, Windows and
  Docker tag CI passed; APK/EXE release checksums, the persistent APK signer,
  and GHCR `amd64`/`arm64`/`386` manifests were verified. The existing
  `v0.5.0-alpha.8` tag remains historical and must not be moved or deleted.
- UI: panel profile/session `⋮` and right-click context menus are implemented;
  Windows has a user-facing connection summary and collapsible advanced
  transport; Android has branded fleur headers, launcher artwork and
  notification icons. Preserve all existing panel action classes/ids.
- Android update conflict root cause was ephemeral GitHub debug signing. Tagged
  builds now require a persistent PKCS12 release key and verify its public
  SHA-256 certificate fingerprint. The key is outside the repository. Never
  commit, print or log it. A one-time uninstall of the old debug-signed APK is
  unavoidable; alpha.9 and later can update in place when signed by this key.
- Network root cause from matched client/server logs: Creator stayed on `fast`
  (`WaitSnd=2048/2048`, drops and TX collapse) after Joiner selected
  `balanced`. KCP profile exchange is now bidirectional and both peers apply
  `PreferSaferKCPProfile`, so `fast + balanced` converges to `balanced`.
- Capability `reliable_dns` adds `MsgDNSQuery` / `MsgDNSReply`. When both peers
  also negotiate `priority_control`, DNS uses the separate reliable control KCP
  conversation instead of the congested bulk conversation and disables legacy
  blind retry duplication. Legacy peers retain `MsgUDP` plus retries. Metrics
  include reliable DNS request/reply counts and average/max latency.
- Field log `relay (6).log` exposed a reconnect race: the two-second handshake
  fallback selected raw, then a valid capability handshake arrived late. The
  adaptive tunnel now permits the safe one-way state upgrade raw fallback ->
  KCP; receive-side frame markers already support this mixed transition. Never
  allow a late timeout to downgrade an already active KCP tunnel.
- Next gate is a matching alpha.9 field test: redeploy the tagged Docker image,
  install the alpha.9 APK after the one-time uninstall, recreate the session,
  and confirm both logs report alpha.9, caps `0x1b`, balanced on both peers,
  reliable DNS counters and recovery back to active KCP after reconnect.

## Historical handoff (2026-07-21, UI redesign session)

This session was UI/UX only plus one panel resilience fix. No transport,
protocol, wire, or Go logic was touched. See `docs/UI_REDESIGN_2026-07-21.md`
for the full detail. Summary:

- Design language changed from the old blood/graphite palette to **classic
  gothic marble**: brand "VL" with a fleur-de-lis, two themes — **Argent**
  (white Carrara marble) and **Sable** (black marble) — with a day/night
  toggle persisted in `localStorage` under key `wlb-theme`.
- **Panel** (`headless/manager/web/`, served via `//go:embed web/*`): fully
  recolored, added theme toggle, collapsible "Client Forge" form
  (`wlb-forge` localStorage key), auto-scroll to diagnostics on session
  select, and a refresh-loop hardening fix (per-section independent render +
  9s `fetch` timeout so one slow `/api/sessions` no longer hides profiles or
  wedges the panel — this was the "profiles appear only after several
  reloads" bug). **Already on `main`, commit `4c87603`, pushed by the user.**
- **Android** (`android-app/`): recolored via `values/colors.xml` (Argent) and
  `values-night/colors.xml` (Sable). Kotlin only uses `R.color.*`, so the
  palette swap propagates automatically. 5 raw-hex drawables moved onto new
  `warn_amber_soft` / `error_red_soft` tokens; system-bar icon contrast driven
  by a `light_system_bars` bool resource. Verified statically (palette parity,
  all `@color`/`R.color` resolve) — **NOT built locally** (no Gradle/JDK).
- **Windows Joiner** (`joiner-desktop-app/`): `styles/app.css` rewritten to
  Argent/Sable CSS variables, fleur-de-lis sigil, header theme toggle (logic
  in renderer bundle — the HTML CSP blocks inline scripts). `tsc --noEmit`
  passes.
- **Android + Windows commits live on branch `release/v0.5.0-alpha.8`**
  (commits `6db24a7` Android, `7b56dc5` Windows) branched from `main@4c87603`.
  **Not yet pushed.** Next agent/user: `git push -u origin
  release/v0.5.0-alpha.8`, open PR to `main`, then tag `v0.5.0-alpha.8` to
  trigger the release CI (APK/EXE/Docker). Verify the Android Gradle build in
  CI since it was not built locally.
- `.gitignore` extended to exclude field logs (`*.log`, `logpanel*.txt`) and
  shared reference screenshots (`photo_*`), because they contain destination
  IPs / session data and would trip the git secret-scan hook.

### Still open (raised by user this session, NOT done)

- **Speed / no-upload**: user's field test used the `fast` KCP profile, which
  filled the queue (`kcp_wait_snd=2048/2048`, `kcp_dropped=2956`) and killed
  the call mid-test — this is the documented one-way ACK stall made worse by
  `fast`. Advised the user to switch the client profile to **Balanced** and
  re-test; awaiting a fresh redacted server log before touching transport.
  Real fix is P1 fair-mux / per-flow queues (see `docs/PERFORMANCE_ROADMAP.md`).
- **Client install signature**: new APK/EXE still fail to install over an
  older build ("signatures do not match"); user must uninstall first. A
  persistent release-signing config in `android-joiner.yml` /
  `windows-joiner.yml` is not yet set up.
- **Panel/client UX depth**: user still finds the layouts not convenient
  enough ("3X-UI"-level). Only visual + the two panel affordances above were
  done; a deeper information-architecture pass was not.



## Historical handoff (2026-07-21)

- Read `docs/PROJECT_REPORT_2026-07-21.md` for the complete implementation,
  deployment, incident, security and next-work summary.
- Current release is `v0.5.0-alpha.7`, commit `96b0735`.
- Android, Windows and Docker tag CI passed. GHCR contains `amd64`, `arm64` and
  `386`; tagged GitHub Release contains APK/EXE plus SHA256 files.
- Recommended deployment is `portainer-stack.yml` with image
  `ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.7`.
- Alpha.6 installed Chromium before creating `wlb`, shifting its runtime UID
  from 999 to 997 and causing existing `/data/control-plane.json` volumes to
  fail with permission denied. Alpha.7 creates `wlb` first and hard-pins both
  UID and GID to `999:999`. Never remove the persistent volume for this error.
- The immediate field gate is redeploying alpha.7 against the existing volume,
  then checking panel QR login, new Creator session, WLB2 delivery, Android
  notification recovery and Windows Phone Gateway.

## Non-negotiable rules

- Never commit cookies, access tokens, call links, server credentials, IPs, or
  generated `.env` files.
- Server and Joiner protocol changes must ship together or be protected by a
  capability/version handshake. Do not silently break v0.3.7 clients.
- Reproduce and measure before tuning. Record mode, client commit, FPS, batch,
  dual-track state, TUN/SOCKS mode, throughput, loss, RTT, CPU, and failures.
- Do not add generic compression or VLESS solely on intuition. Most payload is
  already encrypted TLS data, and the project already has a connection mux.
- Prefer small reversible changes with an explicit compatibility path.

## Most important current findings

- VK Video, Telemost Video, and Dion Video send relay frames over VP8 without a
  reliability layer. A lost/reordered RTP frame can lose TCP bytes.
- Only WB Stream Video is wrapped in `KCPTunnel` in the baseline branch.
- The existing mux is `connID + message type`; it has no per-stream flow control
  or fair scheduler. Blocking writes can stall unrelated connections.
- Default VP8 pacing has a theoretical ceiling near 6.5 Mbps before overhead,
  retransmits, SFU loss, and CPU costs.
- Matching Windows Joiner source and CI now live in `joiner-desktop-app/` and
  `.github/workflows/windows-joiner.yml`.
- `MsgHello/MsgHelloAck` capability negotiation and periodic transport metrics
  are implemented. Unanswered handshakes fall back to legacy mode.
- `headless/manager` and `portainer-stack-panel.yml` provide an authenticated
  multi-session panel. Profiles persist in atomic JSON, every session has an
  isolated Creator subprocess, link/log/metrics directory, and global plus
  per-client limits. SQLite history/vault/SSE work is still pending.
- `portainer-stack.yml` is now the recommended single deployment containing
  the panel and Creator supervisor. The VK community bot moved to
  `portainer-stack-bot.yml`; direct/panel stacks must not run together.
- Adaptive KCP defaults to the balanced profile: bounded non-blocking output
  queue, WaitSnd backpressure, 1024 window and congestion control. Stable uses
  256 and fast 2048. A silent-stall detector requests carrier recovery;
  METRICS reports kbps, output queue, drops, backpressure and recoveries.

## Latest matched field test (Android, 2026-07-17)

Source: user-provided `relay (4).log`; do not commit the log itself because it
contains destination addresses and session-adjacent runtime data.

- Matching client `0.5.0-alpha.2` / commit `3a3f62f` negotiated wire 1 and
  capabilities `0x3`; this was not a version mismatch or legacy fallback.
- Android still hard-codes `--kcp-profile balanced`. Selecting `fast` in the
  server profile therefore creates an asymmetric configuration; panel profile
  settings are not currently propagated to Android.
- The download carrier stayed alive: input KCP segments and VP8 frames kept
  increasing, `kcp_input_idle_ms` stayed near zero, and `kcp_stalls=0`.
- The reverse direction degraded: Joiner `kcp_wait_snd` rose from 7 to 516,
  745, 839 and 972/1024 while Joiner TX fell to 0.5 kbps. New CONNECT messages
  then timed out after 20 seconds and the Speedtest upload phase never began.
- This is a one-way/ACK-progress stall, not the fully silent carrier stall that
  the current detector handles. The existing condition (`WaitSnd` full AND no
  inbound KCP input for 12s) cannot fire while server-to-client video continues.
- The single ordered KCP conversation also gives CONNECT/DNS/control no way to
  bypass delayed bulk segments. Per-flow scheduling above one KCP conversation
  alone will not remove this transport-level head-of-line blocking.

## Windows field result (2026-07-19)

- Client `0.5.0-alpha.3` negotiated only `caps=0x3`, proving the running Creator
  session was still pre-alpha.3 and had no priority/profile capability.
- Full-TUN `fast + unlimited` filled the VP8 carrier queue to `128/128`; after
  10 seconds `WaitSnd=1397` while RX was effectively zero. The Windows process
  then crashed with access violation `0xc0000005` in the socket poll path and
  could leave split-default routes pointing at the dead Wintun adapter.
- A subsequent `balanced + default` run connected in about eight seconds and
  stayed healthy with an empty queue and `WaitSnd` near zero.
- Desktop full-TUN must clamp Fast to Balanced. Fast remains only a controlled
  SOCKS-only experiment. When a peer lacks priority/profile negotiation, both
  sides cap the compatibility profile at Balanced.
- Electron must redact join links/passwords from exported logs and invoke a
  route-cleanup watchdog before start and after every child exit.

## Android LAN gateway (alpha.4)

- Android can explicitly bind its authenticated SOCKS5 listener to
  `0.0.0.0`; LAN sharing defaults off and auto credentials persist across app
  restarts. Never allow an unauthenticated LAN listener.
- Windows phone-gateway mode runs Wintun/tun2socks against the phone SOCKS5
  endpoint and does not start a second call Joiner.
- Validate SOCKS authentication before changing Windows routes. Preserve an
  existing on-link route to the phone; otherwise pin the phone IPv4 outside
  the split defaults. Three failed health checks must tear down Wintun so a
  disappearing phone cannot leave the PC without normal internet.
- Redact both local and remote SOCKS passwords. The Android copied config is a
  secret and must never be committed or included in logs.

## Signed VK recovery (alpha.5)

- Manager profiles persist an auto-restart policy and per-profile recovery
  key. A failed Creator is restarted with capped backoff while retaining the
  logical session and increasing its generation.
- VK Creator sends the compact signed `WLB2` update to `VK_PEER_ID` after
  creating a fresh call; Android still accepts legacy `WLB1`. Never log the
  envelope, link or recovery key.
- Android accepts recovery notifications only for a paired profile, valid
  HMAC, recent timestamp and strictly increasing generation.
- The intended deployment uses a separate server VK account for cookies and
  the user's personal VK id as `VK_PEER_ID`; self-messages are not a reliable
  notification channel.

## Panel-managed VK login (alpha.6)

- Manager may launch an isolated Chromium QR session for at most four minutes.
  Never accept a VK password in the panel or expose browser cookies through an
  API, screenshot, log or error message.
- QR cookies live at `/data/managed-secrets/cookies-vk.json` with mode `0600`
  and take precedence over the read-only mounted secret. Deleting them restores
  the mounted file as fallback.
- Recovery messages use a compact, human-readable `WLB2` envelope so Android
  notification previews are less likely to truncate it. Android keeps `WLB1`
  verification for compatibility.
- The Docker runtime identity is pinned to UID/GID `999:999`. Chromium packages
  must not be allowed to shift this identity: existing `/data` volumes and
  mounted cookie permissions depend on it.

## Current transport status

1. ACK/UNA progress is measured independently from inbound traffic. Sustained
   `WaitSnd >= 75%` without progress requests a bounded carrier reconnect and
   reports `kcp_ack_stalls` / `kcp_ack_idle_ms`.
2. Creator sends its KCP profile after capability negotiation. Joiners select
   the safer local/remote profile and log the effective value.
3. Capability `priority_control` enables a second reliable KCP conversation for
   CONNECT, CONNECT_OK/ERR and negotiated reliable DNS. CLOSE deliberately
   remains ordered with bulk data until drain/sequence semantics exist, so it
   cannot truncate a stream.
4. Capability `reliable_dns` adds explicit DNS request/reply frames and latency
   metrics. It activates only with matching peer support and the priority lane;
   legacy peers keep the old UDP/retry behavior.
5. Next: add bounded per-flow queues and DRR; prioritize interactive flows and
   cap UDP fan-out.
6. Add directional metrics: ACK/UNA progress, KCP RTT/RTO/retransmits,
   per-direction carrier frames, per-class queued bytes, CONNECT p50/p95.
7. Re-test Android with matching `balanced/balanced`, then controlled profiles
   and pacing. Do not use Speedtest as the only benchmark: also run one bulk
   download, one upload, and concurrent short HTTPS/DNS probes.

## Next implementation order

1. Release and field-verify `0.5.0-alpha.9` with matching Android/Windows and
   server builds; record redacted directional metrics including
   `dns_reliable_queries`, `dns_reliable_replies`, `dns_avg_ms`, KCP profile,
   WaitSnd and drops.
2. Add bounded per-flow queues, flow control and DRR with interactive priority.
3. Capture repeatable directional Android, Windows Phone Gateway and SOCKS-only
   benchmarks before changing pacing or KCP windows.
4. Harden the panel with SQLite history, structured events/SSE, encrypted vault,
   session auth/CSRF/audit and a TLS deployment profile.
5. Only then tune pacing or prototype multi-track/QUIC alternatives.
