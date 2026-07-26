# Сравнение провайдеров звонка

Провайдер — это не только способ создать ссылку. VK, WB Stream, Telemost и
Dion используют разные SFU, лимиты bitrate, обработку DataChannel, RTP pacing
и правила длительности звонка. Поэтому замена VK на WB Stream действительно
может изменить скорость и стабильность, но результат нужно измерять на том же
устройстве и том же интернет-соединении.

## Текущее состояние

| Провайдер | DC | Video | Приоритет теста |
|---|---|---|---|
| VK | reliable ordered SCTP; unified RelayBridge с alpha.16 | negotiated adaptive KCP | текущий baseline |
| WB Stream | reliable ordered DataChannel | KCP, включая dual-track | первый A/B кандидат |
| Telemost | нет пользовательского DC-режима | raw VP8 без дополнительной ARQ | после WB |
| Dion | нет пользовательского DC-режима | raw VP8 без дополнительной ARQ | после WB |

Raw VP8 не гарантирует доставку каждого relay frame. Для TCP это может выглядеть
как частично загруженная страница или зависший HTTPS-поток. Поэтому один высокий
burst в Speedtest не считается успешным результатом.

## Что требуется на сервере

- VK: `cookies-vk.json` или управляемый QR-вход панели;
- WB Stream: `cookies-wbstream.json`, содержащий `__wb_device_id`;
- Telemost: `cookies-yandex.json`;
- Dion: `cookies-dion.json`.

Cookie-файлы принадлежат только Creator на сервере. Android/Windows Joiner
входит в созданную комнату как гость. Cookies, полные call links, SOCKS password,
IP сервера и внешние полевые логи нельзя коммитить в Git.

## Контролируемый A/B

Сравнивать в таком порядке:

1. VK Video;
2. VK DC на matching alpha.16+;
3. WB Stream DC;
4. WB Stream Video с одним track;
5. WB Stream Video dual-track;
6. Telemost/Dion только после стабильного baseline.

Для каждого прогона:

- использовать одинаковые server/client commit, устройство, сеть и endpoint;
- выбрать Android/Windows **Proxy**, затем направить `curl.exe` явно в SOCKS5;
- проверить в логе `SOCKS CONNECT`, `tcp>0` и ненулевые `tunnel_tx/tunnel_rx`;
- сделать 20 коротких HTTPS-запросов и одну загрузку 10 MiB;
- держать соединение минимум 60 секунд и сохранить только очищенные METRICS;
- записать фактические B/s, TTFB, число успешных запросов, queue/wait, reconnect
  и паузы, а не стартовую оценку приложения Speedtest.

Пример для WB DC из корня репозитория:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\benchmark-socks.ps1 `
  -Mode wb-dc `
  -ClientCommit <commit-из-лога-клиента> `
  -ServerCommit <commit-из-лога-сервера>
```

Обычный Speedtest в Proxy-режиме не использует SOCKS автоматически и измеряет
прямой интернет. В Tunnel-режиме он подходит как дополнительный системный тест,
но не заменяет SOCKS benchmark.

## Как трактовать результат

- WB быстрее и очереди пусты: лимит был в VK carrier/SFU; развиваем WB.
- WB DC стабилен, WB Video хуже: использовать DataChannel и не наращивать KCP.
- WB Video dual-track почти удваивает фактические counters: исследовать
  адаптивное число tracks и лимит SFU.
- Все провайдеры дают одинаковый потолок: проверять uplink устройства, TURN,
  CPU/thermal throttling и серверный egress.
- Высокий Speedtest при нулевых relay counters: тест прошёл напрямую и
  недействителен.

VLESS/Xray на выходе Creator может поменять точку выхода, но не расширит
участок Joiner → SFU → Creator. Вложенный VLESS/KCP добавит overhead и второй
уровень retransmission; это не первая мера для скорости.
