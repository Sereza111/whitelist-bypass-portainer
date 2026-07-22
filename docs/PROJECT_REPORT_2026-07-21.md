# Whitelist Bypass: отчёт о проделанной работе

- **Дата среза:** 22 июля 2026 года
- **Репозиторий:** `Sereza111/whitelist-bypass-portainer`
- **Upstream:** `kulikov0/whitelist-bypass`, ветка `feature/kcp-over-vp8`, commit `64aa77a`
- **Актуальный matching-релиз:** `v0.5.0-alpha.9`
- **Release commit:** `3534d9f`
- **Примечание:** тег `v0.5.0-alpha.8` неполный (только source archives),
  поэтому переносить его нельзя; APK/EXE/Docker выпускаются новым тегом.
- **Статус:** рабочая alpha-версия для полевых тестов, не завершённый production VPN.

## 1. Цель проекта

Исходный проект переносит пользовательский трафик через звонок или видеопоток
разрешённой платформы. В текущем варианте серверный Creator входит в звонок и
открывает исходящие TCP/UDP-соединения, а клиентский Joiner подключает приложения
через SOCKS5 либо весь IPv4-трафик устройства через TUN.

Работа в этом репозитории была направлена на превращение эксперимента в
управляемую систему:

- единый Docker-образ и развёртывание в Portainer;
- matching server/client builds из одного репозитория;
- надёжный транспорт поверх VK Video;
- Windows и Android клиенты;
- многосессионная серверная панель;
- автоматическое восстановление звонка;
- вход серверного VK через QR без ручного экспорта cookies;
- диагностика, метрики и защита от известных аварийных сценариев.

## 2. Итоговая схема

```text
Приложения Windows / Android
        |
        +-- TUN (Wintun / Android VpnService)
        |          |
        |       tun2socks
        |          |
        +----------+-- локальный SOCKS5 Joiner
                           |
                    connID multiplexing
                           |
              handshake + KCP + priority lane
                           |
                 VP8-подобные RTP frames
                           |
                 VK / другая разрешённая SFU
                           |
                    Creator на сервере
                           |
                  TCP/UDP egress в интернет

Администратор -> Web panel :9200 -> Manager -> отдельные Creator-процессы
Android notification listener <- WLB2 + новая VK call link <- серверный VK
Windows <- authenticated SOCKS5 по LAN <- Android (режим Phone Gateway)
```

### Основные компоненты

| Компонент | Назначение |
|---|---|
| `relay/` | SOCKS5, mux, transport handshake, KCP, метрики и recovery carrier |
| `headless/vk` | Создание/подключение VK-звонка и серверный Creator |
| `headless/manager` | REST API, панель, профили, supervisor и QR-вход VK |
| `android-app/` | Android VpnService, локальный SOCKS5 и автоподхват WLB2 |
| `joiner-desktop-app/` | Windows UI, Joiner, Wintun и Phone Gateway |
| `headless/docker/` | Multi-arch production image |
| `portainer-stack.yml` | Рекомендуемый единый стек Manager + Creator supervisor |

## 3. Что было реализовано

### 3.1 Docker, GitHub и Portainer

- Добавлены Dockerfile, multi-stage build и непривилегированный runtime user.
- Образ публикуется в `ghcr.io/sereza111/whitelist-bypass-portainer`.
- Поддерживаются `linux/amd64`, `linux/arm64` и legacy `linux/386`.
- Добавлены Portainer stacks для основной панели, legacy direct mode и VK-бота.
- Основная панель доступна на фиксированном порту `9200`.
- Настроены GitHub Actions для Docker, Android APK и Windows EXE.
- Tagged releases содержат APK, EXE и SHA256-файлы.
- Добавлены `LICENSE`, `NOTICE` и ссылка на upstream.

### 3.2 Совместимость и диагностика протокола

- Добавлен `MsgHello/MsgHelloAck` handshake с wire version и capabilities.
- Старые peers переходят в явно измеримый legacy fallback.
- Build version и commit видны в server/client logs.
- `METRICS` показывает throughput, carrier frames, очереди, KCP WaitSnd,
  backpressure, drops, stalls и ACK idle.
- Join links, recovery keys и SOCKS passwords скрываются при экспорте логов.

### 3.3 Надёжный VK Video transport

- VK Video обёрнут в согласованный KCP transport.
- KCP segment выровнен с безопасным payload одного VP8 carrier frame.
- Реализованы профили `stable`, `balanced` и `fast`.
- `balanced` является рекомендуемым и безопасным профилем.
- Добавлена bounded non-blocking output queue и WaitSnd backpressure.
- Полный silent stall инициирует ограниченный carrier reconnect.
- Односторонний ACK/UNA stall определяется независимо от живого входящего
  потока и тоже вызывает восстановление carrier.
- Creator и Joiner обмениваются профилями в обе стороны; каждый выбирает более
  безопасную комбинацию. `fast` на сервере + `balanced` на клиенте теперь
  сходятся к `balanced` на обеих сторонах.
- Отдельная reliable KCP lane приоритизирует `CONNECT` и `CONNECT_OK/ERR`.
- Matching alpha.9 выносит DNS query/reply в эту же надёжную priority lane;
  legacy-клиенты сохраняют старый UDP-путь с повторами.
- Метрики показывают reliable DNS queries/replies и average/max DNS latency.
- Поздний handshake после reconnect теперь может безопасно повысить уже
  включённый raw fallback до KCP. Раньше стороны расходились (`Joiner=raw`,
  `Creator=KCP`) и все новые CONNECT завершались 20-секундным timeout.
- `CLOSE` пока остаётся в основной ordered lane, чтобы не обгонять DATA.

### 3.4 Windows Joiner

- Добавлена matching Windows-сборка из текущего commit.
- Реализованы SOCKS-only и full-TUN режимы.
- Full-TUN ограничивает опасный профиль `fast` до `balanced`.
- Watchdog удаляет stale split-default routes до запуска и после аварии.
- Ошибка Joiner не должна оставлять Windows без обычного интернета.
- Добавлен Argent/Sable Gothic UI, понятная сводка provider/route/reliability и
  сворачиваемый блок `Advanced transport` для технических параметров.
- Реализован режим Phone Gateway: Windows использует SOCKS5 телефона и не
  создаёт второй звонок.

### 3.5 Android Joiner и раздача на ПК

- Собирается matching Android APK с VpnService.
- Android может открыть authenticated SOCKS5 на `0.0.0.0` для доверенной LAN.
- LAN sharing выключен по умолчанию.
- Логин и случайный пароль SOCKS сохраняются и показываются пользователю.
- Windows сначала проверяет SOCKS authentication, затем меняет маршруты.
- Маршрут к IP телефона закрепляется через физическую сеть и не зацикливается
  через собственный Wintun.
- После трёх провалов health-check Windows удаляет TUN routes.
- Launcher, заголовки основных Android-экранов и notification icons приведены
  к единому VL/fleur стилю Argent/Sable.
- Tagged APK теперь подписывается постоянным release-сертификатом. Старый APK,
  подписанный случайным debug-ключом GitHub runner, нужно удалить один раз;
  затем alpha.9+ смогут обновляться поверх установленной версии.

### 3.6 Многосессионная серверная панель

- Manager хранит профили клиентов в `/data/control-plane.json`.
- Каждый профиль имеет лимит сессий, срок действия и состояние enabled.
- Каждая сессия запускается отдельным Creator subprocess.
- Есть глобальный `MAX_SESSIONS` и per-client limit.
- Ссылки, логи и метрики разделены по каталогам сессий.
- Реализованы start, stop, delete, automatic restart и capped backoff.
- Панель использует Gothic visual language и адаптирована для телефона.
- Профили и сессии получили компактную кнопку `⋮` и контекстное меню по правому
  клику: start/stop, копирование, редактирование, enable/disable и удаление.
- Основной стек больше не требует отдельного стека панели.

### 3.7 Автоматическое восстановление VK

- Для профиля генерируются recovery profile, generation и HMAC key.
- После обрыва Manager перезапускает Creator и создаёт новый звонок.
- Серверный VK отправляет новую ссылку пользователю, заданному в `VK_PEER_ID`.
- Android принимает обновление только при совпадении профиля, корректном HMAC,
  свежем timestamp и строго большем generation.
- Старый формат `WLB1` остаётся совместимым.
- Новый формат `WLB2` содержит обычную ссылку и короткую подпись:

```text
WhitelistBypass · Phone
https://vk.com/call/join/...
WLB2.<profile>.<generation>.<timestamp>.<hmac>
```

Длинное сообщение `WLB1...` было техническим подписанным конвертом, а не
ссылкой для ручного открытия. В `WLB2` ссылка видна и человеку, и Android.

### 3.8 Вход серверного VK через QR

- Manager запускает изолированный Chromium не более чем на четыре минуты.
- В панели появилась кнопка **Войти через QR**.
- Пользователь сканирует код телефоном под отдельным серверным VK-аккаунтом.
- Пароль VK через панель не вводится и сервером не сохраняется.
- После подтверждения cookies проверяются через VK `web_token`.
- Cookies атомарно сохраняются в
  `/data/managed-secrets/cookies-vk.json` с mode `0600`.
- API, screenshots, logs и errors не возвращают значения cookies.
- Управляемая сессия приоритетнее старого read-only bind mount.
- Удаление управляемой сессии возвращает bind-mounted файл как fallback.
- На `386` Chromium отсутствует; там остаётся ручной импорт cookies.

### 3.9 Исправление обновления `alpha.6 -> alpha.7`

Chromium создаёт системных пользователей при установке. В `alpha.6` Docker
создавал `wlb` после Chromium, поэтому его UID изменился с `999` на `997`.
Существующий Docker volume остался владельцем `999:999`, и Manager падал:

```text
control plane: open /data/control-plane.json: permission denied
```

В `alpha.7` пользователь `wlb` создаётся до Chromium с жёстко закреплёнными
UID/GID `999:999`. Docker build отдельно проверяет оба значения. Старый volume
не требуется удалять, профили сохраняются.

## 4. История основных релизов

| Релиз | Ключевой результат |
|---|---|
| `v0.4.0-alpha.1` | Tagged Windows releases |
| `v0.4.0-alpha.3` | Повтор DNS в legacy raw transport |
| `v0.4.0-alpha.4` | Полный экран Android settings |
| `v0.4.0-alpha.5` | Стабилизация KCP и Gothic clients |
| `v0.5.0-alpha.1` | Multi-session Manager и KCP recovery |
| `v0.5.0-alpha.2` | Исправление reordered handshake ACK |
| `v0.5.0-alpha.3` | ACK-stall recovery и priority control lane |
| `v0.5.0-alpha.4` | Android LAN SOCKS gateway для Windows |
| `v0.5.0-alpha.5` | Signed VK call recovery |
| `v0.5.0-alpha.6` | Panel-managed VK QR login и WLB2 |
| `v0.5.0-alpha.7` | Стабильный Docker UID/GID для persistent volumes |
| `v0.5.0-alpha.8` | Неполный тег: UI commits, без собранных APK/EXE/Docker assets |
| `v0.5.0-alpha.9` | Полный Gothic UX, постоянная APK-подпись, двусторонний KCP profile, reliable DNS и late-handshake recovery |

## 5. Проверки и подтверждённые результаты

Для `v0.5.0-alpha.9` выполнены:

- все Go tests и `go vet` для relay, Manager, VK Creator и Windows Joiner;
- TypeScript build и JavaScript syntax check;
- разбор всех 112 Android XML и проверка color-resource references;
- тест capability fallback, двустороннего KCP profile, priority DNS frames и
  DNS latency metrics;
- `git diff --check`;

Публикационная проверка завершена:

- Android signing secrets установлены после подтверждения владельца;
- branch CI и tag CI для Android/Windows/Docker прошли;
- APK собран через `assembleRelease`, fingerprint совпал с offline key;
- скачанные APK/EXE совпали с опубликованными SHA256;
- GHCR содержит `amd64`, `arm64` и `386` manifests;

Перед `v0.5.0-alpha.7` также выполнялись:

- secret scan staged diff;
- GitHub Android build;
- GitHub Windows build;
- GitHub Docker multi-arch build;
- проверка GHCR manifest для `amd64`, `arm64`, `386`;
- проверка GitHub Release и SHA256 assets;
- реальный smoke test VK QR: Chromium открыл VK, панель получила screenshot с
  QR, API вернул `waiting`, screenshot был доступен только через Basic Auth.

Полевые тесты подтвердили:

- matching handshake и KCP negotiation работают;
- `balanced` устойчивее агрессивного `fast`;
- Android tunnel стабилизирован лучше ранних версий;
- Android LAN SOCKS gateway позволяет Windows использовать один звонок;
- прежний one-way stall виден по WaitSnd/ACK progress и теперь имеет recovery;
- абсолютная скорость по-прежнему ограничена SFU, RTP loss, pacing и общей
  очередью, поэтому проект ещё нельзя считать обычным быстрым VPN.

## 6. Развёртывание

`v0.5.0-alpha.9` опубликован и проверен. Сервер, Android и Windows необходимо
обновить вместе, иначе новые negotiated возможности останутся выключены.

Рекомендуемый образ:

```text
ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.9
```

Порядок обновления в Portainer:

1. Открыть основной Stack.
2. Указать образ `v0.5.0-alpha.9`.
3. Нажать **Update the stack** с включённым **Re-pull image**.
4. Не удалять volume `whitelist-bypass-manager-data`.
5. Проверить логи:

```text
[build] version=0.5.0-alpha.9 ...
[manager] panel listening on :8080
```

6. Открыть `http://SERVER_IP:9200`.
7. Войти через QR под отдельным серверным VK.
8. Остановить старую Creator-сессию и запустить профиль заново.
9. В `VK_PEER_ID` оставить ID личного аккаунта, получающего recovery messages.
10. Установить matching `alpha.9` APK/EXE.

Если старые права volume были изменены вручную и ошибка осталась:

```bash
docker run --rm \
  --user 0:0 \
  --entrypoint /bin/sh \
  -v whitelist-bypass-manager-data:/data \
  ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.9 \
  -c 'chown -R 999:999 /data && chmod 700 /data'
```

## 7. Безопасность и секреты

- Не коммитить cookies, `.env`, VK tokens, panel password, IP сервера, recovery
  block, join links и SOCKS passwords.
- Join link является секретом подключения и материалом transport key.
- Cookies должны оставаться mode `0600`.
- QR screenshot доступен только во время короткой авторизованной сессии.
- Для server cookies и `VK_PEER_ID` лучше использовать разные VK-аккаунты.
- Панель сейчас использует HTTP Basic Auth. Публичный порт `9200` необходимо
  закрыть firewall либо поставить за TLS reverse proxy.
- LAN SOCKS должен всегда иметь username/password.
- Перед публикацией логов запускать redaction и дополнительно просматривать их.

## 8. Известные ограничения

1. Скорость ограничена VP8 pacing и поведением SFU; теоретический потолок не
   является реальной гарантированной скоростью.
2. Один bulk download всё ещё может увеличивать задержку других потоков.
3. DNS вынесен в priority reliable lane только при matching alpha.9 peers;
   legacy fallback намеренно остаётся прежним.
4. Нет полноценного per-flow credit, bounded queue и DRR scheduler.
5. `CLOSE` нельзя приоритизировать без sequence/drain semantics.
6. Политика IPv6 и HTTP/3/UDP требует дальнейшей работы.
7. Panel metadata остаётся atomic JSON, а не SQLite.
8. Нет SSE live events, полноценного audit log и encrypted cookie vault.
9. Basic Auth по HTTP недостаточен для открытого production deployment.
10. Мобильный и desktop UI улучшены, но product polish и accessibility не
    завершены.
11. Дополнительные providers не дают автоматический failover сами по себе:
    для этого нужны заранее подготовленные credentials и отдельная стратегия.

## 9. Почему не добавлены VLESS/xHTTP/Hysteria2 внутрь звонка

В проекте уже есть собственный multiplexing. Большая часть web traffic после
TLS не сжимается. Вложенный VLESS mux не устраняет loss и head-of-line blocking
нижнего VP8 carrier. xHTTP требует доступного HTTP endpoint, а Hysteria2 -
доступного UDP/QUIC endpoint. Если они доступны напрямую, звонок не нужен; если
вложить их внутрь KCP/VP8, остаются прежние ограничения и появляется двойная
retransmission/congestion control.

Xray/VLESS можно использовать после Creator как внешний SOCKS egress sidecar.
Это меняет точку выхода, но не ускоряет участок Joiner - SFU - Creator.

## 10. Следующие приоритеты

### P0. Выпуск и полевое подтверждение `alpha.9`

- подписать и опубликовать matching APK/EXE/Docker;
- redeploy существующего volume без потери профилей;
- QR-login под отдельным VK;
- новый звонок и доставка WLB2;
- Android auto-recovery без второго VPN и обновление APK поверх alpha.9;
- Windows Phone Gateway через Android SOCKS.
- проверить в парных логах одинаковый `adaptive-kcp-active-balanced`, отсутствие
  роста drops/WaitSnd и поля `dns_reliable_queries/replies`, `dns_avg_ms`.

### P1. Fair mux

- bounded per-flow queues;
- DRR/fair scheduler;
- per-flow backpressure и credit;
- лимит UDP fan-out;
- p50/p95 CONNECT latency и per-class queued bytes.

### P2. Наблюдаемость и повторяемые benchmarks

- отдельные download/upload тесты;
- параллельные короткие HTTPS/DNS probes;
- RTT/RTO/retransmits и directional carrier frames;
- CPU/RAM/thermal measurements;
- сохранение обезличенных benchmark JSON.

### P3. Control plane hardening

- SQLite metadata и session history;
- structured Creator events и SSE;
- encrypted cookie vault;
- session-based auth, CSRF, login rate limiting и audit log;
- Caddy/Nginx TLS deployment profile.

### P4. Product clients

- единый pairing QR/deep link;
- более понятные состояния Connecting/Carrier/Tunnel/Degraded;
- diagnostics dashboard;
- accessibility/reduced motion;
- provider profiles и управляемый failover после измерений.

## 11. Контекст для следующего ИИ

Следующий агент должен начать с `AGENTS.md`, затем прочитать этот отчёт и
четыре архитектурных документа:

1. `docs/PROTOCOL_ARCHITECTURE.md`;
2. `docs/PERFORMANCE_ROADMAP.md`;
3. `docs/TARGET_ARCHITECTURE.md`;
4. `docs/PRODUCT_ROADMAP.md`.

Обязательные правила:

- не менять server wire без matching client или capability gate;
- не включать `fast`, compression, VLESS или новый mux без измерений;
- не удалять persistent volume при permission errors;
- Docker runtime user должен оставаться `999:999`;
- не раскрывать cookies, call links, recovery blocks, IP и credentials;
- не считать успешный Speedtest достаточным тестом стабильности;
- сохранять legacy `WLB1`, пока совместимость явно не снята;
- текущий основной stack - `portainer-stack.yml`; не запускать direct и panel
  одновременно для одного аккаунта;
- текущий рекомендуемый profile - `balanced`; обе стороны обязаны сойтись к
  одному профилю после negotiation;
- release считается готовым только после Android + Windows + Docker CI и
  проверки tagged assets/manifests.

### Быстрый старт работы следующего агента

```text
1. git status и git log -5
2. сверить запущенный server/client version и commit
3. получить redacted client/server METRICS и точное описание режима
4. воспроизвести SOCKS-only до изменения TUN/transport
5. внести маленькое совместимое изменение
6. прогнать tests, vet, builds, secret scan и diff check
7. branch CI -> tag CI -> Release/GHCR verification
8. обновить AGENTS.md и этот отчёт при изменении архитектуры
```

## 12. Ссылки

- Репозиторий: <https://github.com/Sereza111/whitelist-bypass-portainer>
- Актуальный релиз: <https://github.com/Sereza111/whitelist-bypass-portainer/releases/tag/v0.5.0-alpha.9>
- GHCR: `ghcr.io/sereza111/whitelist-bypass-portainer`
- Upstream: <https://github.com/kulikov0/whitelist-bypass>
