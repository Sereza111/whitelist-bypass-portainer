# WLB Manager panel (MVP)

Панель запускает Direct Creator как дочерний непривилегированный процесс внутри
контейнера. Docker socket ей не передаётся. Текущий MVP управляет одной сессией
одновременно: start, stop, выбор платформы, resources, VK reliability, получение
join link и просмотр ограниченного журнала.

## Portainer

Создайте новый Stack из этого репозитория:

```text
Compose path: portainer-stack-panel.yml
```

Обязательные environment variables:

| Variable | Value |
|---|---|
| `PANEL_PASSWORD` | уникальный пароль длиной от 12 символов |
| `PANEL_USERNAME` | по умолчанию `admin` |
| `WLB_SECRETS_DIR` | `/opt/whitelist-bypass/secrets` |
| `WLB_IMAGE` | `ghcr.io/sereza111/whitelist-bypass-portainer:latest` |

Порт панели зафиксирован в Compose, чтобы развёртывание не зависело от
переменных Portainer:

```text
0.0.0.0:9200 -> container:8080
```

После Deploy зайдите на `http://SERVER_IP:9200`. Браузер запросит
`PANEL_USERNAME` и `PANEL_PASSWORD`. Разрешите входящий TCP/9200 в firewall
VPS.

Basic Auth без HTTPS передаёт пароль без транспортного шифрования. Для
постоянного публичного доступа поставьте перед портом 9200 TLS reverse proxy и
ограничьте доступ firewall.

## Cookies

Панель не загружает cookies через браузер. Она читает существующие read-only
файлы:

- `cookies-vk.json`;
- `cookies-yandex.json`;
- `cookies-wbstream.json`;
- `cookies-dion.json`.

Они остаются в `WLB_SECRETS_DIR` Docker host и не попадают в Git, API или
панельные логи.

## Ограничения MVP

- одна Creator-сессия;
- Basic Auth вместо полноценной session auth;
- metadata пока без SQLite;
- нет cookie vault/upload;
- нет TLS termination внутри manager;
- после стабилизации transport manager будет расширен до profiles, нескольких
  процессов, SSE metrics и audit log.
