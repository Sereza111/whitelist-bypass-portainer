# Whitelist Bypass

Система туннелирования трафика через звонки VK, Yandex Telemost, WB Stream и
Dion. Это уже не минимальная Docker-обёртка upstream: репозиторий содержит
серверную панель, supervisor нескольких Creator, matching Android/Windows
клиенты, SOCKS5/TUN, автоматическое восстановление звонков, диагностику и
multi-arch релизный pipeline.

Текущий проверенный релиз: **[v0.5.0-alpha.12](https://github.com/Sereza111/whitelist-bypass-portainer/releases/tag/v0.5.0-alpha.12)**.

Docker image:

```text
ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.12
```

Проект основан на
[`kulikov0/whitelist-bypass`](https://github.com/kulikov0/whitelist-bypass),
ветка `feature/kcp-over-vp8`, исходная точка
`64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943`. Лицензия и происхождение кода
зафиксированы в [LICENSE](LICENSE) и [NOTICE](NOTICE).

> Проект экспериментальный. Он зависит от поведения сторонних платформ и не
> является обычным WireGuard/VLESS VPN с гарантированной полосой пропускания.

## Что уже реализовано

### Сервер и панель

- единый Portainer Stack `panel + Creator supervisor`;
- разделы панели: обзор, клиенты, сессии, провайдеры, события и настройки;
- desktop sidebar и мобильная нижняя навигация;
- постоянные клиентские профили с поиском, копированием и контекстным меню;
- отдельные лимиты, срок действия, enabled/disabled и KCP-профиль клиента;
- несколько независимых Creator-процессов с общим лимитом `MAX_SESSIONS`;
- отдельные каталоги, bounded logs и METRICS каждой сессии;
- VK-вход серверного аккаунта через QR без передачи пароля панели;
- глобальный VK recovery recipient и override для отдельного профиля;
- тестовое VK-сообщение с rate limit и безопасными ошибками;
- bounded structured event log без cookies, токенов, ссылок и recovery keys;
- непривилегированный runtime UID/GID `999:999`, без Docker socket.

### Транспорт

- version/capability handshake и безопасный legacy fallback;
- negotiated KCP для VK Video;
- отдельный reliable priority lane для CONNECT и DNS;
- reliable DNS request/reply вместо слепых повторов на matching клиентах;
- согласование более безопасного KCP-профиля с обеих сторон;
- детектор ACK/UNA stall и ограниченное переподключение carrier;
- bounded очередь `64 KiB` на logical flow и общий staging-лимит `512 KiB`;
- Deficit Round Robin, чтобы bulk download не занимал отправку навсегда;
- отдельные метрики KCP, DNS, fairness, queue, drops и backpressure;
- peer watchdog: зависший PeerConnection приводит к реальному restart Creator;
- полное закрытие старых RelayBridge, data/control KCP и flow state при reset.
- отмена неотправленного хвоста закрытого flow и единичный NACK для stale data;

### Клиенты

- matching portable Windows Joiner с Wintun и автоматической очисткой маршрутов;
- matching Android APK для `armeabi-v7a` и `arm64-v8a`;
- одинаковый gothic marble интерфейс Argent/Sable;
- основной выбор маршрутизации **VPN / Proxy**;
- локальный authenticated SOCKS5, по умолчанию `127.0.0.1:1080`;
- Android LAN SOCKS5 для раздачи активного туннеля на ПК;
- Windows Phone Gateway — ПК использует уже подключённый Android;
- Android Split Tunneling, custom/automatic DNS и фильтрация private carrier DNS;
- signed VK recovery с HMAC, generation и защитой от повторного сообщения;
- постоянная Android release-подпись: подписанные `alpha.9+` обновляются поверх
  друг друга.

## Как проходит трафик

```text
Приложение
  ├─ VPN: TUN → tun2socks ─┐
  └─ Proxy: local SOCKS5 ──┤
                           ↓
                     logical flow mux
                           ↓
               priority control + fair DRR
                           ↓
                  KCP / VP8 / WebRTC call
                           ↓
                    Creator на сервере
                           ↓
                        Интернет
```

Серверная сторона должна иметь обычный доступ в интернет. Клиент получает
ссылку звонка, входит как guest/anonym participant и не получает cookies
серверного аккаунта.

## Важное ограничение: один звонок — один клиент

Один VK Creator в topology `DIRECT` обслуживает один активный Joiner. Если к
той же ссылке подключится второй Joiner, он заменит первый PeerConnection.

Для нескольких пользователей или устройств:

1. создайте отдельный профиль в панели;
2. запустите отдельную сессию;
3. передайте клиенту только его ссылку/pairing block;
4. при необходимости задайте его VK ID как recovery recipient профиля.

Клиенту не нужно входить в серверный VK. Для первоначального подключения
администратор выдаёт ссылку из панели. Для последующей автоматической ротации
сервер отправляет подписанный `WLB2` в VK получателя конкретного профиля.

Один серверный VK-аккаунт может создавать несколько звонков, но реальные
лимиты и anti-abuse правила VK не контролируются проектом. Начинайте с
`MAX_SESSIONS=4`, используйте отдельный технический аккаунт и увеличивайте
лимит только после полевого теста CPU/RAM и устойчивости платформы.

## Быстрый запуск через Portainer

### Требования

- Linux VPS/сервер с Docker и Portainer;
- минимум 1 vCPU и 1 GB RAM для небольшого числа сессий;
- доступ к GitHub Container Registry;
- TCP/9200 до панели либо TLS reverse proxy;
- отдельный аккаунт выбранной платформы.

### Stack

1. Откройте **Portainer → Stacks → Add stack → Repository**.
2. Укажите этот репозиторий и ветку `main`.
3. Compose path: `portainer-stack.yml`.
4. Добавьте переменные:

   Готовый шаблон без секретов: [.env.manager.example](.env.manager.example).

| Переменная | Рекомендуемое значение |
|---|---|
| `WLB_IMAGE` | `ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.12` |
| `PANEL_USERNAME` | новый логин, по умолчанию `admin` |
| `PANEL_PASSWORD` | уникальный пароль длиной от 12 символов |
| `WLB_SECRETS_DIR` | `/opt/whitelist-bypass/secrets` |
| `MAX_SESSIONS` | `4` |
| `AUTO_START` | `false` |
| `VIDEO_RELIABILITY` | `auto` |
| `KCP_PROFILE` | `balanced` |
| `VK_PEER_ID` | необязательный legacy/global fallback получателя |

5. Нажмите **Deploy the stack**.
6. Откройте `http://SERVER_IP:9200`.
7. В разделе **Провайдеры** выполните VK QR-вход или подключите cookies.

Не удаляйте volume `whitelist-bypass-manager-data` при обновлении: там лежат
профили, managed VK cookies и данные сессий.

Полная инструкция: [PORTAINER.md](PORTAINER.md).

## Cookies и QR-вход

Панель поддерживает read-only fallback-файлы из `WLB_SECRETS_DIR`:

| Провайдер | Файл |
|---|---|
| VK | `cookies-vk.json` |
| Telemost | `cookies-yandex.json` |
| WB Stream | `cookies-wbstream.json` |
| Dion | `cookies-dion.json` |

Для VK на `amd64`/`arm64` предпочтителен вход через QR:

1. откройте **Провайдеры → VK → Войти через QR**;
2. отсканируйте код отдельным серверным аккаунтом;
3. подтвердите вход на телефоне;
4. дождитесь статуса готовности.

Manager сохраняет cookies в `/data/managed-secrets/cookies-vk.json` с правами
`0600` и удаляет временный Chromium profile. Пароль VK в панель не вводится.

## Создание клиента

1. Откройте раздел **Клиенты**.
2. Создайте профиль устройства/пользователя.
3. Оставьте reliability `auto`, KCP `balanced`, auto recovery включённым.
4. При необходимости укажите персональный VK recovery recipient.
5. Нажмите **Запустить**.
6. Дождитесь состояния «Ждёт устройство» и появления ссылки.
7. Передайте ссылку Windows-клиенту или блок **В телефон** Android-клиенту.

Recovery key является секретом устройства. Не публикуйте pairing block и не
прикладывайте его к логам.

## Windows и Android

Готовые matching клиенты находятся в
[GitHub Releases](https://github.com/Sereza111/whitelist-bypass-portainer/releases).
Для APK и EXE публикуются `.sha256`.

В логах клиента и сервера должны совпадать:

```text
[build] version=0.5.0-alpha.12 commit=... built=...
```

Если на телефоне осталась старая debug-signed `alpha.8`, её нужно удалить один
раз. Подписанные `alpha.9+` используют постоянный сертификат и обновляются без
удаления приложения.

Android: [docs/ANDROID.md](docs/ANDROID.md).

## VPN и Proxy

| Режим | Что направляется | TUN/маршруты | Подходит для |
|---|---|---|---|
| **VPN** | весь или выбранный Split Tunneling трафик | да | обычное использование устройства |
| **Proxy** | только приложения с SOCKS5-настройкой | нет | браузер, Telegram, диагностика, снижение фоновой нагрузки |

В Proxy режиме настройте приложение на:

```text
SOCKS5 host: 127.0.0.1
SOCKS5 port: 1080
username/password: скопировать из клиента
```

Обычный Speedtest не использует SOCKS5 автоматически и в Proxy режиме может
измерять прямой интернет. Для повторяемой проверки используйте приложение с
явной proxy-настройкой или:

```powershell
curl.exe --socks5-hostname 127.0.0.1:1080 https://api.ipify.org
```

## Раздача туннеля телефона на ПК

1. Подключите Android к его Creator-сессии.
2. Откройте **Settings → Proxy**.
3. Включите **Share SOCKS5 over LAN** и сохраните настройки.
4. Скопируйте PC configuration.
5. Подключите ПК к той же Wi-Fi сети/точке доступа.
6. В Windows включите **Android Phone Gateway** и вставьте блок.

Windows не входит во второй звонок: он использует SOCKS5 уже подключённого
телефона. LAN sharing всегда требует авторизацию и должен использоваться только
в доверенной сети.

## Автоматическое восстановление

Recovery работает в два уровня:

1. Creator пытается восстановить WebSocket/PeerConnection старого звонка.
2. После исчерпания попыток процесс завершается, а manager создаёт новый звонок
   с backoff `2s → 5s → 10s → 30s → 1m → 2m → 5m`.

Для зависшей замены peer действуют отдельные проверки:

- offer должен перейти в connected за 30 секунд;
- disconnected получает 15 секунд grace period;
- failed/closed запускают recovery сразу;
- три неудачных peer recovery завершают Creator.

Новая ссылка получает увеличенный generation и подписанный HMAC envelope.
Android принимает только свежую подпись сопряжённого профиля.

## Транспортные профили

| Профиль | Назначение |
|---|---|
| `balanced` | рекомендуемый режим |
| `stable` | сильная потеря/нестабильная мобильная сеть |
| `fast` | эксперимент SOCKS-only на чистом carrier |
| `raw` | legacy rollback, не нормальный режим для web |

Matching peers согласуют более безопасный профиль. Full TUN не должен
использовать `fast`: Windows ограничивает его до `balanced`.

## Диагностика

Основная строка:

```text
METRICS mode=... tx_kbps=... rx_kbps=... fair_queue=... kcp_wait_snd=...
```

Полезные поля:

- `tx_kbps`, `rx_kbps` — фактический трафик внутри relay;
- `kcp_wait_snd` — неподтверждённые KCP-сегменты, не код ошибки;
- `kcp_dropped`, `kcp_backpressure_ms`, `kcp_ack_stalls`;
- `dns_reliable_queries`, `dns_reliable_replies`, `dns_avg_ms`;
- `fair_queue`, `fair_avg_wait_ms`, `fair_max_wait_ms`;
- `queue`, `queue_max`, `kcp_input_idle_ms`, `kcp_ack_idle_ms`.

Не оценивайте transport только по одному Speedtest. Сравнивайте bulk download,
upload и короткие HTTPS/DNS запросы под одновременной нагрузкой.

## Обновление

1. Измените `WLB_IMAGE` на нужный version tag.
2. Выполните **Pull and redeploy**.
3. Не удаляйте `/data` volume.
4. Установите matching APK/EXE этого же релиза.
5. Перезапустите клиентскую сессию.
6. Проверьте одинаковую build version в обоих логах.

## Структура репозитория

| Путь | Назначение |
|---|---|
| `relay/` | общий protocol, SOCKS5, mux, KCP, DRR, DNS и TUN bindings |
| `headless/manager/` | panel API, профили и Creator supervisor |
| `headless/vk/` | VK Creator |
| `headless/*` | остальные providers и headless Joiners |
| `joiner-desktop-app/` | Electron UI и Go desktop engine |
| `android-app/` | Android UI, VpnService и ProxyService |
| `portainer-stack.yml` | рекомендуемое развёртывание |
| `.github/workflows/` | Docker, Android и Windows release CI |
| `docs/` | архитектура, отчёты и roadmaps |

## Локальные проверки

Требуются Go, Node.js/npm и Docker. Android APK полностью собирается в GitHub
Actions с Java 17, Android SDK/NDK и gomobile.

```powershell
cd relay
go test ./...
go vet ./...

cd ..\headless\manager
go test ./...

cd ..\..\joiner-desktop-app
npm ci
npm run build
```

Локальная Docker-сборка legacy VK-бота:

```sh
docker compose --env-file .env.portainer -f portainer-stack-build.yml up -d --build
```

## Legacy deployments

- `portainer-stack-bot.yml` — старое управление через VK-сообщество;
- `portainer-stack-direct.yml` — одиночный Creator без панели;
- `portainer-stack-panel.yml` — совместимый alias panel Stack;
- `portainer-stack-build.yml` — локальная сборка на Docker host.

Не запускайте panel/direct/bot stacks одновременно для одной и той же сессии.

## Безопасность

- не коммитьте `.env`, cookies, access tokens, call links и pairing blocks;
- полная join link является секретом подключения;
- не публикуйте SOCKS5 password и recovery key в issue/log;
- Basic Auth на TCP/9200 требует TLS reverse proxy для публичного доступа;
- ограничьте 9200 firewall или VPN администрирования;
- используйте отдельный технический аккаунт платформы;
- учитывайте местное законодательство и правила VK/Telemost/WB/Dion.

## Основные этапы выполненной работы

- Docker/Portainer и multi-arch GHCR image;
- matching Windows и Android сборки из одного relay snapshot;
- multi-session manager и постоянные профили;
- gothic redesign панели, Android и Windows;
- Android LAN gateway и Windows Phone Gateway;
- signed VK recovery и QR login серверного аккаунта;
- постоянная Android release-подпись;
- KCP profile negotiation, priority control и reliable DNS;
- reconnect race и directional ACK stall recovery;
- bounded per-flow queues и DRR fairness;
- полноценный panel control center;
- first-class VPN/Proxy routing;
- peer health watchdog, KCP lifecycle cleanup и carrier DNS fallback.

Подробные изменения последнего релиза:
[docs/ALPHA12_RELEASE_NOTES.md](docs/ALPHA12_RELEASE_NOTES.md).

## Дополнительная документация

- [Запуск в Portainer](PORTAINER.md)
- [Панель](docs/PANEL.md)
- [Android](docs/ANDROID.md)
- [Архитектура протокола](docs/PROTOCOL_ARCHITECTURE.md)
- [План стабильности и скорости](docs/PERFORMANCE_ROADMAP.md)
- [Целевая архитектура](docs/TARGET_ARCHITECTURE.md)
- [Product roadmap](docs/PRODUCT_ROADMAP.md)
- [Полный проектный отчёт](docs/PROJECT_REPORT_2026-07-21.md)

## Лицензия

MIT. См. [LICENSE](LICENSE) и [NOTICE](NOTICE).
