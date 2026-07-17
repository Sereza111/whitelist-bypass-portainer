# Запуск Whitelist Bypass через Portainer

Рекомендуемое развёртывание использует один Stack: web panel и запускаемые ею
Direct Creator находятся в одном контейнере. Отдельный Creator Stack вместе с
панелью не нужен.

> Один запущенный Creator обслуживает один Joiner. Для телефона и ПК создайте
> отдельные клиентские профили: manager умеет держать несколько Creator
> одновременно.

## Что требуется

- Linux-сервер с Docker и Portainer;
- публичный образ GHCR;
- каталог `/opt/whitelist-bypass/secrets` с cookies нужной платформы;
- минимум 1 vCPU и 1 ГБ RAM;
- открытый TCP/9200 для текущей HTTP-панели.

## Рекомендуемый Stack

1. Откройте **Stacks → Add stack → Repository**.
2. Укажите репозиторий и ветку `main`.
3. Compose path: `portainer-stack.yml`.
4. Добавьте переменные:

| Переменная | Значение |
|---|---|
| `WLB_IMAGE` | `ghcr.io/sereza111/whitelist-bypass-portainer:latest` |
| `PANEL_USERNAME` | `admin` или другой логин |
| `PANEL_PASSWORD` | уникальный пароль длиной от 12 символов |
| `WLB_SECRETS_DIR` | `/opt/whitelist-bypass/secrets` |
| `VIDEO_RELIABILITY` | `auto` |
| `KCP_PROFILE` | `balanced` |
| `MAX_SESSIONS` | `4` (подберите под CPU/RAM сервера) |
| `AUTO_START` | `false` |

5. Deploy stack.
6. Проверьте Published Ports: `9200:8080`.
7. Откройте `http://SERVER_IP:9200`.

В `Client Forge` создайте профиль устройства. `Лимит сессий` ограничивает
только этот профиль, а `MAX_SESSIONS` — весь сервер. Профиль можно отключить,
ограничить по времени, изменить или удалить. Кнопка `Запустить` создаёт
независимую сессию; ссылка и METRICS находятся в `Diagnostics Nave`.

`portainer-stack-panel.yml` оставлен как совместимый alias того же
развёртывания. Не запускайте одновременно `portainer-stack-direct.yml` и
панель для одного аккаунта/ссылки.

## Cookies

Панель читает файлы только из bind mount:

| Платформа | Файл |
|---|---|
| VK | `cookies-vk.json` |
| Telemost | `cookies-yandex.json` |
| WB Stream | `cookies-wbstream.json` |
| Dion | `cookies-dion.json` |

Cookies, токены и join links нельзя коммитить в Git.

## Transport

Для matching server/client используйте:

- reliability: `auto`;
- KCP profile `balanced` — рекомендуемый;
- `stable` — при сильной потере и обвалах скорости;
- `fast` — только для чистого carrier без заметных потерь;
- `raw` — аварийный legacy rollback, не нормальный режим для web-трафика.

В логах matching пары должны появиться `adaptive-kcp-active-<profile>` и
`legacy=false`. METRICS показывает `tx_kbps`, `rx_kbps`,
`kcp_wait_snd`, `kcp_control_wait_snd`, `kcp_out_queue`, `kcp_dropped`,
`kcp_stalls`, `kcp_ack_stalls`, время простоя входящего потока/ACK и
backpressure. При заполненном KCP-окне без входящих данных или ACK-прогресса
manager/client запрашивает ограниченное переподключение carrier вместо вечного
зависания.

## Дополнительные варианты

- `portainer-stack-bot.yml` — старое управление через VK-сообщество;
- `portainer-stack-direct.yml` — legacy одиночный Creator без панели;
- `portainer-stack-build.yml` — локальная сборка образа на Docker host.

## Безопасность панели

Порт 9200 сейчас использует HTTP Basic Auth. Для постоянного публичного
развёртывания поставьте TLS reverse proxy (Caddy/Nginx), ограничьте порт
firewall и смените любой пароль, попавший в скриншот или переписку.
