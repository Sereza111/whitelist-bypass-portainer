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
