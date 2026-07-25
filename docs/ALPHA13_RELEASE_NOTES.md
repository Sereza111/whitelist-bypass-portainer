# v0.5.0-alpha.13

Alpha.13 исправляет зависание Android на VK captcha, добавляет прозрачную
маршрутизацию выбранных приложений и переводит matching VK transport на
ограниченный адаптивный профиль Auto.

## VK captcha

Полевые логи разделили проблему на два состояния:

- успешная сессия прошла captcha, `OK.ru anonymLogin` и работала `7h10m50s`;
- сломанные запуски останавливались сразу после `vk-auth: captcha required` и
  никогда не писали `captcha solved`.

Серверный звонок в этот момент исправен и поэтому панель показывает «Жду
устройство». Блокируется локальная авторизация Joiner, перезапуск панели не
устраняет причину.

Captcha proxy теперь:

- ищет `success_token` во всём JSON captcha, включая вложенные структуры и
  варианты имени поля;
- анализирует ответы дополнительных доменов, проходящие через `generic_proxy`;
- принимает token из query/hash redirect;
- наблюдает XHR, Fetch, навигацию и DOM с ограниченной глубиной разбора;
- не пишет token, ссылку или cookies в журнал.

В Android добавлена кнопка **Retry**, перезагружающая страницу captcha без
перезапуска серверного звонка.

## Android: Device / Apps / SOCKS5

- **Device** — обычный прозрачный VPN всего телефона.
- **Apps** — Happ-подобный прозрачный per-app режим через `VpnService`; SOCKS5
  внутри выбранного приложения настраивать не нужно.
- **SOCKS5** — существующий ручной localhost/LAN gateway без `VpnService`.

Старые `proxyOnly` и Split Tunneling preferences мигрируют вычислением, без
очистки пользовательских настроек. Пустой или полностью удалённый список Apps
завершает запуск безопасной ошибкой вместо захвата всего устройства.

## KCP Auto

Новый рекомендуемый профиль Auto:

- начинает с send window 256;
- измеряет подтверждённые KCP segments отдельно по направлению каждые 2 секунды;
- держит приблизительно одну секунду доставленного трафика, но не выходит за
  256–512 segments;
- растёт по 32 только при реальном спросе и уменьшается по 64;
- в idle возвращается к 256.

Capability `kcp_auto` защищает совместимость: новый peer не отправляет wire
profile Auto старому peer и использует Balanced fallback. METRICS дополнены
`kcp_window` и `kcp_auto_changes`. Stable, Balanced и Fast сохранены как ручные
advanced/diagnostic overrides.

## Проверка

Локально пройдены:

- `go test ./...` и `go vet ./...` для relay, VK Creator, manager и Windows
  Joiner;
- новый integration test captcha через второй upstream JSON endpoint;
- KCP Auto controller/profile/capability tests;
- TypeScript build Windows Joiner;
- XML/resource static validation.

Android APK, Windows EXE и multi-arch Docker image дополнительно собираются и
проверяются release workflows из одного immutable tag.

