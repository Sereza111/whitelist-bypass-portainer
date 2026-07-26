# v0.5.0-alpha.16

Alpha.16 separates the healthy VK Video path from the degraded legacy
DataChannel path and adds link-based Android onboarding.

## Field evidence and transport

Matching alpha.15 Android logs are external-only and must not be committed.

- `relay (17).log` used VK DataChannel. Its handshake timed out and fell back
  to `wire=0 caps=0x0 legacy=true`; measured relay traffic stayed around
  20–51 kbps, with no KCP because DC bypassed the modern RelayBridge.
- `relay (18).log` used VK Video. It negotiated `wire=1 caps=0x3b`, reliable
  DNS, a separate KCP control lane and stable KCP. Relay RX reached
  668.5 kbps. Queues remained bounded and empty, `WaitSnd` reached only 16/256,
  and there were no drops, ACK stalls or carrier stalls.

Video remains the safe default. The current approximately 0.8 Mbps field
ceiling is in the VK VP8 carrier, not a full KCP window or the SOCKS scheduler;
blindly increasing the KCP window would add latency without proving more
carrier capacity.

The Creator's old standalone DC mux has been removed. DC now uses the same
RelayBridge, handshake, flow lifecycle, DNS implementation and metrics as
Video. KCP is not stacked over reliable SCTP. DataChannel buffered output is
bounded. DC remains experimental until matching alpha.16 field logs prove the
handshake and throughput.

## Android invite links

The panel now creates a random 15-minute `/join/<token>` URL for a running VK
profile. This normal HTTP(S) link can be sent through VK, Telegram or a QR
created by the browser/device. It opens a minimal no-cache landing page, then
launches the installed Android app through `wlb://import`.

Android validates the invite version, VK call host, profile id and recovery
key shape, shows a confirmation dialog, imports or updates the profile in
Video mode and begins connection. Android then displays the normal system VPN
permission dialog when required.

The user's VK password or cookies are never included. The temporary URL is a
bearer secret and must not be published or attached to logs. Use an HTTPS
reverse proxy before giving these links to users over untrusted networks.

