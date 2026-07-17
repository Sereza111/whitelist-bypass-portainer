# WLB Manager panel

Панель запускает каждый Direct Creator как отдельный дочерний
непривилегированный процесс внутри контейнера. Docker socket ей не передаётся.
Она управляет постоянными клиентскими профилями и несколькими независимыми
сессиями: start/stop, platform, resources, KCP profile, join link, METRICS и
ограниченный журнал.

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
| `MAX_SESSIONS` | общий предел Creator, по умолчанию `4` |

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

## Клиенты и хранение

- профили атомарно сохраняются в `/data/control-plane.json`;
- каждая сессия получает `/data/sessions/<session-id>`;
- у клиента есть enabled/disabled, срок действия и собственный лимит;
- выключение профиля запрещает новые старты, но не обрывает уже работающую
  сессию без явного `Стоп`;
- provider API показывает только наличие cookie-файла, но никогда его
  содержимое.

## Текущие ограничения

- Basic Auth вместо полноценной session auth;
- профили переживают restart, история завершённых сессий пока не сохраняется;
- metadata хранится в JSON, без SQLite;
- нет cookie vault/upload;
- нет TLS termination внутри manager;
- polling metrics вместо SSE;
- audit log пока отсутствует.
