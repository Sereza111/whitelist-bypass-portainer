# Архитектура протокола

Зафиксировано по upstream `feature/kcp-over-vp8`, commit
`64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943`.

## Путь трафика

```mermaid
flowchart LR
    App["Приложения ПК/Android"] --> TUN["Wintun / Android VpnService"]
    App2["Приложение с SOCKS5"] --> SOCKS
    TUN --> T2S["tun2socks"] --> SOCKS["Локальный SOCKS5\nRelayBridge: joiner"]
    SOCKS --> MUX["Встроенный mux\nconnID + тип + payload"]
    MUX --> MODE{"Транспорт"}
    MODE -->|DC| SCTP["WebRTC SCTP DataChannel\nreliable + ordered"]
    MODE -->|Video| OBF["XChaCha20-Poly1305\n+ псевдо-VP8 framing"]
    OBF --> KCP["Negotiated KCP reliability\nmatching VK / WB"]
    OBF --> RAW["Raw VP8\nlegacy fallback / другие providers"]
    KCP --> RTP["WebRTC RTP video"]
    RAW --> RTP
    SCTP --> SFU["VK / Telemost / WB / Dion SFU"]
    RTP --> SFU
    SFU --> CREATOR["Headless Creator на сервере"]
    CREATOR --> DEMUX["RelayBridge: creator\ndemux по connID"]
    DEMUX --> NET["DNS + TCP/UDP egress в интернет"]
```

## Уровни

### 1. TUN и SOCKS5

На Windows Joiner создаёт Wintun `10.99.0.2`, устанавливает две половины
default route (`0.0.0.0/1`, `128.0.0.0/1`) и передаёт пакеты в `tun2socks`.
`tun2socks` преобразует потоки в локальный SOCKS5 на `127.0.0.1:1080`.

SOCKS-only режим полезен для диагностики: он исключает Wintun, маршруты, MTU и
системный DNS из тестируемой цепочки.

Android может явно открыть тот же authenticated SOCKS5 listener на
`0.0.0.0:<port>` для доверенной LAN/точки доступа. Windows phone-gateway mode
не создаёт второй Joiner и не входит в звонок: локальный Wintun направляет
системный трафик в SOCKS5 телефона, а дальше используется уже активный Android
Joiner. До установки split-default routes Windows проверяет SOCKS5
username/password. Адрес телефона остаётся на исходном физическом маршруте,
чтобы соединение tun2socks с собственным upstream не зациклилось через Wintun.

Начиная с `0.5.0-alpha.11`, SOCKS-only является пользовательским режимом, а не
скрытым диагностическим флагом. Windows и Android показывают переключатель
`VPN / Proxy` на основном экране. В Proxy режиме локальный SOCKS5 продолжает
работать, но Wintun / Android VpnService и системные маршруты не создаются.
Поэтому трафик пойдёт через звонок только у приложений, которым явно задан
SOCKS5 endpoint. Обычный Speedtest без настройки proxy измеряет прямую сеть.

### 2. Встроенный multiplexing

Проект уже мультиплексирует множество TCP/UDP-соединений внутри одного звонка.
Wire frame:

```text
uint32 frame_length | uint32 conn_id | uint8 message_type | payload
```

Типы: CONNECT, CONNECT_OK/ERR, DATA, CLOSE, UDP/UDP_REPLY и CONFIG/ACK.
VLESS-mux или yamux поверх этого слоя не устраняют ограничения нижнего
транспорта и могут добавить второй уровень head-of-line blocking.

### 3. Обфускация Video

Ключ получается из token в join link через SHA-256. Payload шифруется
XChaCha20-Poly1305 и помещается после заголовка, похожего на VP8 interframe.
Каждая посылка добавляет примерно 61 байт:

- 17 байт VP8-подобного заголовка;
- 4 байта epoch;
- 24 байта XChaCha nonce;
- 16 байт Poly1305 tag.

Ссылка является одновременно секретом подключения и материалом ключа. Любой,
кто получил ссылку, способен подключиться к сессии; её нельзя публиковать.

### 4. VP8 pacing

`VP8DataTunnel` имеет одну глобальную очередь на 128 элементов и отправляет не
больше одного элемента за tick:

```text
ticks_per_second = fps × batch
theoretical_payload ≈ fps × batch × 1126 bytes
```

При `24 × 30` получается около 810 KB/s или 6.5 Mbps до overhead, потерь,
повторов, ограничений SFU и CPU. Это потолок, а не гарантированная скорость.

Большой download способен заполнить общую очередь и задержать DNS, новые TCP
CONNECT и интерактивный трафик.

### 5. Надёжность

| Платформа/режим | Нижний транспорт | Надёжность в baseline |
|---|---|---|
| VK DC | SCTP DataChannel | reliable/ordered |
| VK Video, matching peers | VP8/RTP + negotiated KCP | KCP |
| VK Video, legacy peer | VP8/RTP | raw fallback без дополнительной ARQ |
| Telemost Video | VP8/RTP | нет дополнительной ARQ |
| WB Stream DC | SCTP DataChannel | reliable/ordered |
| WB Stream Video | VP8/RTP + KCP | KCP |
| Dion Video | VP8/RTP | нет дополнительной ARQ |

Для TCP потеря raw VP8 frame означает потерю байтов внутри логического TCP
потока. Внешний TCP не знает, что байты потеряны внутри proxy, поэтому страница
может зависнуть вместо нормальной TCP retransmission. Это главный кандидат на
причину частично загружающихся сайтов в VK Video.

Legacy WB KCP сохраняет быстрый профиль. Negotiated VK KCP по умолчанию
использует balanced profile: `NoDelay(1,20,2,0)`, окна `512/512`, bounded
output queue и WaitSnd backpressure. Stable использует `256/256`, fast —
`2048/2048`. MTU adaptive-сегмента выровнен так, чтобы обычный relay frame
помещался в один carrier frame.

Над KCP работает DRR scheduler. После полевого alpha.11 теста его буферы
ограничены `64 KiB` на logical flow и `512 KiB` суммарно. При удалённом CLOSE
ещё не отправленный хвост flow отменяется; повторные DATA для уже закрытого ID
получают один NACK вместо бесконечного шторма CLOSE/log.

### 6. Server egress

Creator после demux самостоятельно открывает TCP/UDP к конечному адресу. Опция
`UPSTREAM_SOCKS` отправляет egress через другой SOCKS5 с UDP ASSOCIATE.
Это уже позволяет подключить Xray/VLESS как внешний sidecar: Xray предоставляет
локальный SOCKS5, а Creator использует его как upstream. Такая цепочка меняет
точку выхода, но не ускоряет участок Joiner ↔ SFU ↔ Creator.

## Текущие архитектурные риски

1. Raw Video не гарантирует доставку и порядок TCP payload.
2. Одна глобальная send queue создаёт head-of-line blocking между потоками.
3. В receive path есть синхронные `conn.Write`; медленный socket может задержать
   обработку всех tunnel frames.
4. Нет per-stream credit/window и ограничения памяти на активный поток.
5. Нет достаточных метрик: RTP loss/reorder, queue depth, blocked time, RTT,
   retransmits и effective Mbps не видны одновременно.
6. Windows TUN маршрутизирует только IPv4; DNS/IPv6/HTTP3 могут давать задержки
   или утечки вне туннеля.
7. Legacy fallback всё ещё разрешает peer без новых capabilities; для KCP,
   reliable DNS, priority control и recovery нужны matching server/client.

## VK Direct session lifecycle

Один Creator в topology `DIRECT` обслуживает один активный Joiner. Новый
registered peer заменяет старый PeerConnection. Для нескольких пользователей
manager создаёт отдельный профиль и отдельную Creator session/link на клиента.
Joiner входит по guest/anonym token из ссылки и не получает cookies серверного
VK аккаунта.

С `0.5.0-alpha.11` сервер контролирует не только жизнь процесса, но и жизнь
PeerConnection: offer должен перейти в connected за 30 секунд, disconnected
имеет 15 секунд grace period, failed/closed запускают recovery немедленно.
После трёх неудачных внутренних циклов Creator завершается, manager создаёт
новый звонок и увеличивает signed recovery generation. При reset старый
RelayBridge, data/control KCP loops и его flow state закрываются полностью.

Начальную ссылку администратор выдаёт из panel. Для автоматических последующих
обновлений у профиля задаётся VK recipient конкретного пользователя; он имеет
приоритет над общим recipient. Доставка VK может зависеть от privacy/диалога,
поэтому pairing key и recovery envelope остаются подписанными и не должны
появляться в логах.

Дополнение после matching Android-теста `0.5.0-alpha.2`: двунаправленный
WebRTC carrier может деградировать только в одну сторону. При живом
server→Joiner потоке метрика общего `last input` остаётся свежей, хотя
Joiner→server ACK-прогресс прекращается и `WaitSnd` приближается к пределу.
Следовательно, health-check должен учитывать отдельный прогресс ACK/UNA для
исходящего направления, а не только наличие любых входящих KCP packets.

В `0.5.0-alpha.3` capability `priority_control` включает вторую KCP
conversation с marker `WKC2`. Через неё проходят `CONNECT` и
`CONNECT_OK/ERR`, поэтому создание нового TCP-потока больше не стоит за bulk
`MsgData` основной `WKC1` conversation. `CLOSE` пока намеренно остаётся в
основной ordered lane: без drain/sequence semantics приоритетный CLOSE мог бы
обогнать последние DATA и обрезать поток. DNS priority и полноценный fair mux
остаются следующим этапом.

Тот же релиз считает прогресс KCP ACK/UNA отдельно от любых входящих packets.
При устойчивом заполнении 75% send window без ACK-прогресса в течение 15 секунд
запрашивается штатный reconnect carrier. Creator передаёт выбранный KCP profile
после handshake, а Joiner применяет более безопасный из локального и серверного
вариантов; Android `balanced` и server `fast` теперь дают effective `balanced`.

## Реализованный compatibility handshake

В текущей ветке control plane mux расширен сообщениями:

- `MsgHello = 0x0A`;
- `MsgHelloAck = 0x0B`.

`Hello` передаёт magic `WLB2`, wire version, build version/commit,
capability bitmask, max carrier payload, текущий reliability mode, число tracks
и случайный 128-bit nonce. `HelloAck` связывается с запросом через nonce и
возвращает выбранную wire version и пересечение capabilities.

Старый v0.3.7 peer игнорирует неизвестный control frame; после трёх попыток
новая сторона фиксирует `legacyCompatibility=true` и не включает optional
features.

Для VK Video matching server/client рекламируют `video_kcp1`. Adaptive wrapper
оставляет control frames в raw, маркирует KCP segments magic `WKC1` и во время
перехода принимает оба вида payload. Обычные data frames блокируются до
результата handshake, поэтому raw и KCP не могут поменять порядок байтов одного
TCP stream. При старом peer wrapper через timeout открывает raw data path.

KCP segment MTU равен 1122 байтам: вместе с 4-byte marker он точно помещается в
1126-byte VP8 carrier payload. Relay read buffer уменьшен до 1089 байт, поэтому
`9-byte mux frame + 24-byte KCP header + marker` не создают второй segment для
обычного TCP read.

RelayBridge каждые 10 секунд пишет строку `METRICS` с relay bytes/frames,
control frames, временем блокировки `SendData`, активными TCP/UDP flows,
результатом handshake и transport-specific queue/KCP counters.

## Полевой результат alpha.11: живой carrier с чрезмерной очередью

Matching Android и Creator согласовали `caps=0x1b`, `legacy=false` и
`adaptive-kcp-active-balanced`. Потерь KCP и ACK-stall не было, но Speedtest
открыл несколько bulk flows и обнаружил bufferbloat:

- Joiner: `fair_queue_max≈1.05 MiB`, `fair_max_wait_ms≈15908`,
  `kcp_wait_snd=1024` более минуты;
- Creator: `fair_queue_max≈4.19 MiB`, `fair_max_wait_ms≈38641`,
  `kcp_backpressure_ms≈52780`;
- фактический relay throughput был около `1.1 Mbps`, loaded ping достигал
  `7064 ms`;
- краткий индикатор Speedtest около `270 Mbps` не подтверждается byte counters
  и является стартовой оценкой на маленьком burst.

Это не silent carrier failure: входящие segments и ACK продолжали двигаться,
поэтому reconnect watchdog закономерно не сработал. Alpha.12 уменьшает буферы
и отменяет stale flow backlog, сохраняя backpressure вместо byte drop.
