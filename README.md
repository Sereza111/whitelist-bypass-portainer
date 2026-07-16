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
- `portainer-stack.yml` — рекомендуемый stack с образом из GHCR;
- `portainer-stack-direct.yml` — direct Creator без VK-сообщества;
- `portainer-stack-build.yml` — сборка образа прямо из Git-репозитория;
- `.github/workflows/docker-bot.yml` — multi-arch публикация образа в GHCR;
- `PORTAINER.md` — подробная инструкция на русском.

Android, iOS и готовые мобильные бинарники удалены: они не участвуют в сборке
серверного контейнера. Исходники Electron Joiner возвращены без старых
prebuilt-файлов, чтобы сервер и Windows-клиент всегда собирались из одного
коммита.

## Быстрый запуск в Portainer

1. Опубликуйте этот репозиторий на GitHub в ветку `main`.
2. Дождитесь workflow **Build & push bot Docker image** во вкладке Actions.
3. В Portainer создайте Stack из Git repository с Compose path
   `portainer-stack.yml`.
4. Задайте `WLB_IMAGE`, `VK_TOKEN`, `VK_GROUP_ID` и обязательно ограничьте
   доступ через `VK_USER_IDS`.
5. Deploy stack и напишите `/start` в сообщения VK-сообщества.

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

Исследование wire protocol и план доработки:

- [Архитектура протокола](docs/PROTOCOL_ARCHITECTURE.md)
- [План стабильности и скорости](docs/PERFORMANCE_ROADMAP.md)
- [Целевая архитектура продукта](docs/TARGET_ARCHITECTURE.md)
- [Product roadmap](docs/PRODUCT_ROADMAP.md)

Если VK-сообщества нет, используйте `portainer-stack-direct.yml`. Он запускает
Creator напрямую и читает cookies из закрытой папки Docker host. Режимы:
`vk`, `telemost`, `wbstream` и `dion`. Каждый режим требует соответствующий
экспортированный файл cookies; один контейнер обслуживает одного Joiner.

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

## Команды VK-бота

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
