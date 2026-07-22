# Whitelist Bypass 0.5.0-alpha.10

## Что изменилось

### Панель

- отдельные разделы: обзор, клиенты, сессии, провайдеры, события и настройки;
- desktop sidebar и мобильная нижняя навигация;
- компактный реестр клиентов с поиском, дублированием и контекстным меню;
- глобальный получатель VK Recovery настраивается в панели;
- отдельный клиент может переопределить глобального получателя;
- тестовое VK-сообщение с ограничением частоты и безопасной ошибкой;
- bounded event log без cookies, токенов, call links и recovery keys;
- QR-вход серверного VK остался в панели и не принимает пароль.

Приоритет получателя: настройка клиента → настройка панели → устаревший
`VK_PEER_ID`. Новое значение используется новой сессией или при следующем
автоматическом перезапуске существующей.

### Транспорт

- bounded очередь до 256 KiB на logical flow;
- общий лимит очереди 8 MiB;
- Deficit Round Robin не позволяет одному bulk-потоку постоянно занимать всю
  отправку;
- CONNECT, negotiated reliable DNS и handshake продолжают идти по priority
  control lane;
- DATA, ordered CLOSE и UDP сохраняют порядок внутри своего flow;
- добавлены `fair_flows`, `fair_queue`, `fair_queue_max`,
  `fair_avg_wait_ms` и `fair_max_wait_ms` в строку `METRICS`.

Это улучшение fairness и loaded latency. Оно не отменяет физический предел
VP8 carrier и не обещает автоматически увеличить максимальную скорость.

## Обновление

1. В Portainer укажите
   `ghcr.io/sereza111/whitelist-bypass-portainer:v0.5.0-alpha.10`.
2. Выполните Pull and redeploy, persistent `/data` volume не удаляйте.
3. Установите Android APK и Windows EXE из GitHub Release `v0.5.0-alpha.10`.
4. Если на Android ещё стоит debug-signed alpha.8, удалите её один раз. Все
   подписанные alpha.9+ после этого обновляются поверх предыдущей версии.
5. В логах клиента и сервера проверьте одинаковую версию alpha.10.

## Полевой тест

Используйте `balanced`, затем одновременно запустите bulk download и несколько
коротких HTTPS/DNS запросов. Сохраните с обеих сторон только очищенные строки
`METRICS`: throughput, `kcp_wait_snd`, drops, reliable DNS и новые fair queue
показатели. Не публикуйте join link, IP назначения, cookies или SOCKS password.
