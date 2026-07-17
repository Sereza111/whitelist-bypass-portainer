# Whitelist Bypass — server/Portainer edition

Минимальная серверная сборка
[kulikov0/whitelist-bypass](https://github.com/kulikov0/whitelist-bypass),
подготовленная для Docker, Portainer и публикации образа в GitHub Container
Registry (GHCR).

Основа: upstream-ветка `feature/kcp-over-vp8`, commit
`64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943`.

Проект передаёт трафик через звонки VK, Yandex Telemost, WB Stream или Dion.
Серверная сторона (Creator) должна иметь свободный доступ в интернет, а Joiner
подключается к созданной сессии с клиентского устройства.

> Один Creator обслуживает ровно один Joiner. Для каждого устройства нужна
> отдельная сессия.

## Что оставлено в server-only репозитории

- `relay/` — общий Go relay, SOCKS5, multiplexing и DC/VP8/KCP-транспорт;
- `headless/` — creators, Linux joiners, VK-бот и runtime Dockerfile;
- `joiner-desktop-app/` — matching Windows Joiner из того же snapshot;
- `android-app/` — Android UI/VpnService, собираемый с matching Go relay;
- `portainer-stack.yml` — рекомендуемый single Stack: panel + Creator supervisor;
- `portainer-stack-bot.yml` — legacy-управление через VK-сообщество;
- `portainer-stack-direct.yml` — legacy direct Creator без панели;
- `portainer-stack-build.yml` — сборка образа прямо из Git-репозитория;
- `portainer-stack-panel.yml` — совместимый alias основного panel Stack;
- `.github/workflows/docker-bot.yml` — multi-arch публикация образа в GHCR;
- `PORTAINER.md` — подробная инструкция на русском.

Готовые мобильные бинарники, чужой keystore и upstream prebuilt-библиотеки не
хранятся в Git. Android APK и его нативные компоненты собираются GitHub Actions
из текущего коммита, чтобы сервер, Windows и Android использовали один relay.
iOS пока не включён.

## Быстрый запуск в Portainer

1. Опубликуйте этот репозиторий на GitHub в ветку `main`.
2. Дождитесь workflow **Build & push bot Docker image** во вкладке Actions.
3. В Portainer создайте Stack из Git repository с Compose path
   `portainer-stack.yml`.
4. Задайте `WLB_IMAGE`, `PANEL_USERNAME`, новый `PANEL_PASSWORD`,
   `WLB_SECRETS_DIR`, `VIDEO_RELIABILITY=auto` и
   `KCP_PROFILE=balanced`.
5. Deploy stack и откройте `http://SERVER_IP:9200`.

Полная пошаговая инструкция: [PORTAINER.md](PORTAINER.md).

## Matching Windows Joiner

Готовые версии публикуются в
[GitHub Releases](https://github.com/Sereza111/whitelist-bypass-portainer/releases).
Внутри релиза скачайте portable `.exe`; файл `.sha256` позволяет проверить
контрольную сумму.

После push откройте GitHub **Actions → Build Windows Joiner**. Успешный job
публикует artifact `whitelist-bypass-joiner-windows-x64-...` с portable
`.exe` и SHA-256 checksum. Клиент уже содержит Wintun и запрашивает права
администратора.

В начале server/client логов должна совпадать строка:

```text
[build] version=... commit=... built=...
```

Не используйте старый Joiner v0.3.7 с экспериментальными transport features:
новые возможности включаются только после capability handshake. Если peer
старый, tunnel остаётся в legacy compatibility без negotiated features.

## Matching Android Joiner

Workflow **Build Android Joiner** создаёт устанавливаемый alpha APK для
`armeabi-v7a` и `arm64-v8a`. Скачать его можно из artifact успешного GitHub
Actions run, а после tag — прямо из GitHub Release. Подробности:
[Android Joiner](docs/ANDROID.md).

Исследование wire protocol и план доработки:

- [Архитектура протокола](docs/PROTOCOL_ARCHITECTURE.md)
- [План стабильности и скорости](docs/PERFORMANCE_ROADMAP.md)
- [Целевая архитектура продукта](docs/TARGET_ARCHITECTURE.md)
- [Product roadmap](docs/PRODUCT_ROADMAP.md)
- [WLB Manager panel](docs/PANEL.md)

Основной panel Stack уже запускает Creator напрямую и читает cookies из
закрытой папки Docker host. Отдельный `portainer-stack-direct.yml` вместе с
панелью запускать не нужно.

## Локальная сборка Docker

```sh
cp .env.portainer.example .env
# заполните .env
docker compose --env-file .env -f portainer-stack-build.yml up -d --build
docker compose -f portainer-stack-build.yml logs -f
```

Контейнер не публикует входящие порты: VK-бот использует Long Poll, а creators
устанавливают исходящие WebRTC-соединения. Постоянные cookies и логи хранятся в
Docker volume `whitelist-bypass-data`.

## Команды legacy VK-бота

Для этого режима используйте `portainer-stack-bot.yml`.

- `/vk` — VK creator;
- `/tm` — Telemost creator;
- `/wb` — WB Stream creator (может работать без cookies);
- `/dion` — Dion creator;
- `/list` — активные сессии;
- `/close <id>` — закрыть сессию;
- `/start` — главное меню.

## Безопасность

- Не коммитьте `.env`, cookies и токены — они исключены через `.gitignore`.
- Пустой `VK_USER_IDS` разрешает управлять ботом любому пользователю, который
  может написать сообществу.
- Аккаунт платформы может быть заблокирован из-за звонков с IP дата-центра.
  Используйте отдельный аккаунт или домашний IP.
- Используйте проект только в соответствии с местным законодательством и
  правилами платформ.

## Лицензия и upstream

Исходный проект распространяется по лицензии MIT; файл [LICENSE](LICENSE)
сохранён. Происхождение кода и внесённые изменения описаны в [NOTICE](NOTICE).
Оба файла также включаются внутрь Docker-образа. История и полные клиенты находятся в
[upstream-репозитории](https://github.com/kulikov0/whitelist-bypass).
