# Запуск Whitelist Bypass через Portainer

Эта версия основана на ветке upstream `feature/kcp-over-vp8`, commit
`64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943`. Серверная часть работает как
VK-бот: бот получает команды через VK Long Poll и запускает отдельный headless
Creator для каждой сессии.

> Один Creator обслуживает ровно один Joiner. Для каждого устройства создавайте
> отдельную сессию через бота.

## Что требуется

- Linux-сервер с Docker и Portainer;
- публичное или приватное GitHub-репо с этими файлами;
- VK-сообщество, access token сообщества и числовой ID сообщества;
- минимум 1 vCPU и 1 ГБ RAM; для нескольких одновременных сессий нужно больше;
- исходящие HTTPS/WebSocket/WebRTC-соединения. Входящие порты контейнер не
  публикует.

## 1. Опубликовать образ в GHCR

После push в ветку `main` workflow `.github/workflows/docker-bot.yml` собирает
образы `linux/amd64`, `linux/arm64` и `linux/386` и публикует:

```text
ghcr.io/<github-user>/<repository>:latest
```

Откройте на GitHub вкладку **Actions**, дождитесь зелёного workflow
`Build & push bot Docker image`, затем в настройках Package сделайте образ
публичным. Если образ должен остаться приватным, добавьте в Portainer registry
`ghcr.io` с GitHub username и PAT, у которого есть `read:packages`.

## 2. Создать Stack в Portainer

1. Откройте **Stacks -> Add stack -> Repository**.
2. Укажите URL вашего GitHub-репозитория и ветку `main`.
3. В поле **Compose path** укажите `portainer-stack.yml`.
4. Добавьте переменные окружения:

| Переменная | Обязательно | Пример | Назначение |
|---|---:|---|---|
| `WLB_IMAGE` | да | `ghcr.io/user/repo:latest` | образ из GHCR |
| `VK_TOKEN` | да | `vk1.a...` | access token VK-сообщества |
| `VK_GROUP_ID` | да | `123456789` | числовой ID сообщества |
| `VK_USER_IDS` | настоятельно рекомендуется | `111,222` | кто может управлять ботом |
| `RESOURCES` | нет | `default` | `moderate`, `default` или `unlimited` |
| `UPSTREAM_SOCKS` | нет | `host.docker.internal:1080` | дополнительный SOCKS5 с UDP ASSOCIATE |
| `UPSTREAM_USER` | нет | | логин upstream SOCKS5 |
| `UPSTREAM_PASS` | нет | | пароль upstream SOCKS5 |

Не оставляйте `VK_USER_IDS` пустым на публичном VK-сообществе: пустое значение
разрешает запускать сессии любому пользователю, который может написать боту.

5. Нажмите **Deploy the stack**.
6. В логах дождитесь успешного запуска Long Poll. Напишите со своего разрешённого
   VK-аккаунта в сообщения сообщества `/start`.

Команды бота: `/vk`, `/tm`, `/wb`, `/dion`, `/list`, `/close <id>`.

## Сборка прямо в Portainer

Если образ ещё не опубликован, используйте `portainer-stack-build.yml`. Portainer
должен клонировать весь репозиторий и поддерживать Compose `build`. Сборка Go
бинарников произойдёт на Docker host и займёт больше времени и памяти. Для
регулярных обновлений предпочтительнее GHCR-вариант.

## Cookies для VK, Telemost и Dion

WB Stream использует анонимные guest token. Для остальных платформ экспортируйте
cookies штатными кнопками desktop Creator и скопируйте JSON в постоянный volume:

```sh
docker cp cookies-vk.json whitelist-bypass-bot:/data/cookies-vk.json
docker cp cookies-yandex.json whitelist-bypass-bot:/data/cookies-yandex.json
docker cp cookies-dion.json whitelist-bypass-bot:/data/cookies-dion.json
docker exec -u 0 whitelist-bypass-bot sh -c 'chown wlb:wlb /data/cookies-*.json && chmod 600 /data/cookies-*.json'
docker restart whitelist-bypass-bot
```

Не добавляйте cookies, `VK_TOKEN` или `.env` в Git. Данные и логи сессий хранятся
в named volume `whitelist-bypass-data` и переживают пересоздание контейнера.

## Если WebRTC не соединяется

Сначала проверьте, что firewall разрешает исходящий UDP и TCP. Если Docker bridge
мешает WebRTC, добавьте сервису `bot` в stack:

```yaml
network_mode: host
```

На Linux для обращения к SOCKS5 на Docker host можно добавить:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

После изменения нажмите **Update the stack**.

## Обновление и откат

- Обновление: в Portainer включите **Re-pull image and redeploy**.
- Откат: замените `:latest` в `WLB_IMAGE` на immutable-тег `:sha-<commit>` из
  GHCR и redeploy stack.
- Резервная копия: сохраните Docker volume `whitelist-bypass-data`.

## Риски

Платформа может заметить звонок с IP дата-центра и заблокировать аккаунт.
Используйте отдельный аккаунт или запускайте Creator с домашнего подключения.
Соблюдайте местное законодательство и правила используемых платформ.
