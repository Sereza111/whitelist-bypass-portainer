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

По умолчанию порт публикуется только на loopback Docker host:

```text
127.0.0.1:8080
```

Откройте SSH tunnel с ПК:

```powershell
ssh -L 8080:127.0.0.1:8080 root@SERVER_IP
```

Пока SSH открыт, зайдите на <http://127.0.0.1:8080>. Браузер запросит
`PANEL_USERNAME` и `PANEL_PASSWORD`.

Для прямой публикации можно задать `PANEL_BIND_IP=0.0.0.0`, но Basic Auth без
HTTPS передаёт пароль без транспортного шифрования. Такой режим разрешайте
только за TLS reverse proxy и firewall.

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
