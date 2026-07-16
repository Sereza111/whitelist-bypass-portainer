# Product roadmap

## Definition of done

Система считается продуктом, когда администратор через защищённую panel может
создать несколько независимых сессий, получить pairing URI, увидеть состояние и
метрики, а matching desktop/Android Joiner стабильно передаёт web-трафик при
типичной потере SFU без зависания сайтов.

## Phase 0 — baseline и совместимость

- pin server/client upstream commit;
- build matching Windows Joiner в нашем CI;
- добавить build/version в binaries и panel;
- создать SOCKS-only benchmark и log sanitizer;
- зафиксировать baseline raw Video и legacy DC.

**Gate:** одинаковый commit виден в server и client logs, benchmark повторяем.

## Phase 1 — transport v2

- structured hello/capabilities;
- independent PSK вместо ключа только из call link;
- KCP reliability для VK Video;
- MTU alignment;
- transport metrics;
- compatibility refusal/fallback;
- loss/reorder integration tests.

**Gate:** 10 MB HTTPS download проходит при 1–3% искусственной потери без
повреждения; 20/20 коротких HTTPS-запросов успешны.

## Phase 2 — manager core

- вынести session spawning из VK bot в reusable package;
- state machine и structured creator events;
- REST API;
- SQLite metadata;
- bounded logs;
- health/readiness;
- graceful process-group shutdown;
- limits на concurrent sessions.

**Gate:** API создаёт, перечисляет и останавливает несколько Creator; restart
manager не теряет историю и корректно отмечает orphaned sessions.

## Phase 3 — защищённая web panel

- initial admin setup;
- login/session/CSRF/rate limit;
- provider cookies status и затем encrypted vault upload;
- profiles;
- create/stop session;
- pairing URI/QR;
- live events и diagnostics;
- reverse-proxy/TLS deployment profile;
- audit log.

**Gate:** panel не требует Docker socket, secrets отсутствуют в browser logs и
API responses, security checklist пройден.

## Phase 4 — fair mux и performance

- per-flow queues;
- DRR scheduler;
- flow control;
- control/DNS priority;
- adaptive pacing;
- UDP/443 fallback policy;
- IPv6/DNS/TUN cleanup;
- dual-track benchmark;
- QUIC carrier prototype.

**Gate:** большой download не блокирует DNS и открытие новой страницы; p95
CONNECT latency остаётся в заданном бюджете.

## Phase 5 — desktop client redesign

- перенести client source в matching monorepo snapshot;
- normalised connection state model;
- pairing URI/QR;
- simple/advanced split;
- gothic design tokens и component library;
- diagnostics dashboard;
- safe log export;
- signed Windows builds и auto-update policy;
- accessibility and reduced-motion pass.

**Gate:** новый пользователь подключается без ручной настройки FPS/SOCKS/DNS;
advanced options не мешают основному сценарию.

## Phase 6 — Android parity

- transport v2 capability;
- pairing QR/deep link;
- matching metrics;
- consistent profiles and split tunneling;
- shared visual tokens where platform-appropriate;
- battery/thermal pacing policy.

## Первый практический спринт

1. CI job matching Windows Joiner из pinned upstream commit.
2. `protocol hello` и transport metrics без изменения payload path.
3. KCP VK Video за capability flag.
4. SOCKS-only loss/throughput tests.
5. Только после прохождения gate — scaffold `wlb-manager`.

Панель и gothic UI не начинаются до version handshake: иначе они будут красиво
управлять нестабильным и несовместимым data plane.
