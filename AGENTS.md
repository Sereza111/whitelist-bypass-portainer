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
- VK Creator sends `WLB1.<base64url-json>.<base64url-hmac>` to `VK_PEER_ID`
  after creating a fresh call. Never log the envelope, link or recovery key.
- Android accepts recovery notifications only for a paired profile, valid
  HMAC, recent timestamp and strictly increasing generation.
- The intended deployment uses a separate server VK account for cookies and
  the user's personal VK id as `VK_PEER_ID`; self-messages are not a reliable
  notification channel.

## Current transport status

1. ACK/UNA progress is measured independently from inbound traffic. Sustained
   `WaitSnd >= 75%` without progress requests a bounded carrier reconnect and
   reports `kcp_ack_stalls` / `kcp_ack_idle_ms`.
2. Creator sends its KCP profile after capability negotiation. Joiners select
   the safer local/remote profile and log the effective value.
3. Capability `priority_control` enables a second reliable KCP conversation for
   CONNECT and CONNECT_OK/ERR. CLOSE deliberately remains ordered with bulk
   data until drain/sequence semantics exist, so it cannot truncate a stream.
4. Next: add reliable DNS control messages, bounded per-flow queues and DRR;
   prioritize control/DNS/interactive flows and cap UDP fan-out.
5. Add directional metrics: ACK/UNA progress, KCP RTT/RTO/retransmits,
   per-direction carrier frames, per-class queued bytes, CONNECT p50/p95.
6. Re-test Android with matching `balanced/balanced`, then controlled profiles
   and pacing. Do not use Speedtest as the only benchmark: also run one bulk
   download, one upload, and concurrent short HTTPS/DNS probes.

## Next implementation order

1. Ship and field-test ACK-progress recovery, profile negotiation and the
   separate CONNECT lane together as `0.5.0-alpha.3`.
2. Add per-flow queues, flow control and DRR with control/DNS priority.
3. Capture repeatable directional Android and SOCKS-only benchmarks.
4. Only then tune windows/pacing or prototype multi-track/QUIC alternatives.
