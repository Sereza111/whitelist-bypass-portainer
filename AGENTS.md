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
- The official Windows Joiner v0.3.7 is four upstream commits behind the server
  baseline. Build matching client artifacts before relying on new wire changes.

## Next implementation order

1. Add observability and a repeatable SOCKS-only benchmark.
2. Compare VK DC versus Video and isolate TUN/DNS/HTTP3 behavior.
3. Add a versioned transport capability handshake.
4. Prototype reliable Video for VK, align KCP MTU/read sizes, and benchmark.
5. Add per-connection queues, flow control, and fair scheduling.
6. Extract the existing VK bot process supervisor into `wlb-manager` only after
   transport compatibility and metrics are in place.
