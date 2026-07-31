# План стабильности и скорости

## Сначала локализовать проблему

Не смешивать transport и Windows TUN в одном тесте.

### Матрица первого прогона

| Тест | Режим | TUN | Цель |
|---|---|---:|---|
| A | VK DC | нет, SOCKS-only | базовая надёжность SCTP |
| B | VK Video 24×30 | нет, SOCKS-only | чистый VP8 transport |
| C | VK DC | да | влияние Wintun/DNS/routes |
| D | VK Video 24×30 | да | воспроизведение текущей проблемы |
| E | VK Video 24×16 | нет | проверить loss/перегруз SFU |
| F | VK Video dual-track | нет | оценить масштабирование каналов |

Для каждого прогона записывать:

- commit server и Joiner;
- время до `TUNNEL CONNECTED`;
- успешность 20 коротких HTTPS-запросов;
- TTFB и throughput 10 MB;
- CPU/RAM обеих сторон;
- RTP sequence gaps, queue depth, KCP retransmits/RTT;
- количество зависших сайтов и ошибки DNS.

Пример SOCKS-only проверки на Windows:

```powershell
curl.exe --socks5-hostname 127.0.0.1:1080 https://api.ipify.org
curl.exe --socks5-hostname 127.0.0.1:1080 -o NUL `
  -w "time=%{time_total}s speed=%{speed_download}B/s`n" `
  "https://speed.cloudflare.com/__down?bytes=10485760"
```

Повторяемый прогон из корня репозитория:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\benchmark-socks.ps1 `
  -Mode vk-video `
  -ClientCommit <commit-из-лога-клиента> `
  -ServerCommit <commit-из-лога-сервера> `
  -FPS 24 -Batch 30
```

Скрипт выполняет 20 коротких HTTPS-запросов и загрузку 10 МБ только через
`127.0.0.1:1080`, затем пишет JSON в игнорируемый каталог
`benchmark-results/`. Join link, cookies и содержимое ответов в отчёт не
попадают. Одновременно сохраните соответствующие строки `METRICS` с клиента и
сервера.

Если SOCKS-only стабилен, а TUN нет — исправлять DNS/routes/IPv6/MTU. Если оба
режима нестабильны только в Video — исправлять VP8 reliability/scheduler.

Отдельный тест: временно отключить HTTP/3/QUIC в браузере. Если сайты начинают
открываться, UDP/443 нужно либо стабилизировать, либо принудительно отправлять в
TCP fallback, не ломая DNS UDP.

## P0 — наблюдаемость

До изменения алгоритма добавить периодические transport metrics:

- bytes/sec и frames/sec по направлениям;
- RTP received, lost, reordered и dropped frame assemblies;
- VP8 queue length, время блокировки SendData и максимальную очередь;
- KCP RTT/RTO, WaitSnd, retransmits и input/output segments;
- число TCP/UDP flow, connect latency, DNS latency;
- per-connection queued bytes и write latency.

Acceptance: один лог/JSON snapshot позволяет отличить SFU loss, CPU saturation,
queue starvation, DNS failure и egress timeout.

## P1 — надёжный Video transport

1. Добавить protocol version и capabilities в control frame.
2. Согласовать capability `video_arq=kcp1` до включения KCP.
3. Обернуть VK Video в KCP с обеих сторон; затем Telemost и Dion.
4. Подобрать KCP MTU так, чтобы типичный relay frame помещался в один segment и
   после XChaCha/VP8 overhead не превышал безопасный RTP payload.
5. Добавить recovery после peer restart/epoch change.

Acceptance: 1–3% искусственной потери RTP не повреждают HTTPS downloads; старый
клиент получает понятный отказ или fallback, а не немой зависший туннель.

## P2 — fair mux и backpressure

Текущий connID mux оставляем, но усиливаем:

- отдельная bounded queue на connection;
- weighted round-robin/DRR scheduler вместо одной FIFO;
- приоритет control, DNS и коротких интерактивных потоков;
- credit/window messages, чтобы reader не производил данные быстрее получателя;
- асинхронные writer goroutine на creator/joiner;
- лимит queued bytes и корректное закрытие вместо silent byte drop.

Acceptance: один большой download не увеличивает DNS/CONNECT latency более чем
на заданный порог и не блокирует остальные вкладки.

## P3 — adaptive pacing

Статические `fps × batch` заменить или дополнить feedback loop:

- повышать rate при пустой очереди и низкой потере;
- снижать при росте queue/RTT/loss;
- ограничить burst и tick rate;
- сравнить один и два track;
- учитывать лимиты каждой SFU отдельно.

Не считать максимальные `fps`/`batch` целью: слишком высокий rate увеличивает
drop, retransmit и итоговую задержку.

## P4 — TUN, DNS, UDP и IPv6

- сравнить MTU 1500, 1400 и 1280;
- добавить явную политику IPv6: полноценный route либо отключение/blackhole без
  утечки;
- проверить DNS UDP > 1232 bytes и TCP fallback;
- измерить HTTP/3; при нестабильном Video дать опцию блокировать UDP/443, чтобы
  браузер переходил на HTTP/2/TCP;
- SOCKS5 auth и variable-length request переведены на `io.ReadFull`; тесты
  намеренно дробят handshake/request на отдельные TCP fragments.

## Сжатие

Не включать глобально. Большая часть web payload уже является TLS ciphertext,
JPEG/PNG/video или gzip/brotli и практически не сжимается. Компрессия после TLS:

- расходует CPU;
- добавляет latency и framing;
- иногда увеличивает размер;
- может создавать compression side-channel.

Допустим только адаптивный эксперимент до шифрования: сжимать frame, если
быстрая entropy/size probe обещает заметный выигрыш, и всегда иметь per-frame
флаг. Решение принимать по p50/p95 gain на реальном capture без хранения
содержимого.

## VLESS/Xray

VLESS не исправляет loss, pacing или head-of-line blocking на VP8 участке.
Проект уже имеет mux по `connID`, поэтому ещё один mux обычно лишний.

Разумные варианты:

1. **Egress sidecar:** Xray подключается к VLESS-серверу и поднимает SOCKS5;
   Creator получает `UPSTREAM_SOCKS=xray:1080`. Это меняет точку выхода, но
   добавляет hop и не ускоряет звонок.
2. **Control/auth plane:** использовать отдельную систему пользователей и
   выдачи ссылок, не меняя data plane.
3. **Долгосрочно:** исследовать QUIC streams поверх custom datagram carrier как
   замену KCP + самописному mux. Это отдельный прототип, а не быстрый патч.

### Почему не xHTTP/Hysteria2 внутри звонка

- xHTTP — HTTP transport Xray и требует уже доступного HTTP(S) endpoint;
- Hysteria2 требует доступного UDP/QUIC endpoint;
- если такие endpoints доступны Joiner напрямую, carrier через звонок не нужен;
- если вложить их внутрь VP8/KCP, лимит и loss SFU остаются, а две независимые
  системы retransmit создают congestion collapse.

Их можно использовать как внешний egress после Creator, но это меняет точку
выхода, а не скорость участка звонка.

## Реализованный balanced KCP pass

- KCP output отделён от блокирующей VP8 queue bounded-очередью;
- переполнение output queue учитывается как loss вместо удержания KCP mutex;
- `WaitSnd` ограничивает producer и создаёт измеримый backpressure;
- balanced/stable включают congestion control;
- окна отправки/приёма настроены как 256/512/2048 для
  stable/balanced/fast после измеренного заполнения `WaitSnd=256`;
- bounded output queue увеличена до 1024 segments;
- 12-секундный silent-stall detector срабатывает только при полном `WaitSnd`
  и отсутствии входящих KCP segments, после чего запрашивает штатный reconnect
  carrier;
- доступны профили `stable`, `balanced`, `fast`;
- METRICS содержит throughput, KCP output queue, drops, backpressure,
  `kcp_stalls` и `kcp_input_idle_ms`.

## WB multi-track throughput pass (`0.5.0-alpha.35`)

Полевой лог alpha.32 показал `dualTrack=true`, но при нагрузке direct WB KCP
оставался примерно на 0.5–0.8 Мбит/с и заполнял bounded окно. Причина найдена
выше SFU: `MultiTrackTunnel` пытался извлечь mux `connID` из байтов 4..8 уже
сформированного KCP packet. В этом месте находятся KCP command/window fields,
поэтому bulk segments систематически выбирали один carrier track.

Alpha.35 включает round-robin только когда `MultiTrackTunnel` обёрнут KCP.
Raw mux сохраняет прежнюю per-connection affinity, а KCP самостоятельно
восстанавливает порядок пришедших с разных tracks segments. Android WB Video
с включённым multi-track теперь запрашивает четыре независимо paced VP8
tracks; параметр `trackCount` опционален, ограничен диапазоном 1..4, а старый
`dualTrack` без него по-прежнему означает два tracks.

Метрики теперь содержат `tracks`, per-track TX/RX bytes, frames и queue depth.
Цель — убрать доказанный software bottleneck и приблизиться к 3 Мбит/с без
раздувания KCP/DRR очередей. Это не обещание фиксированной полосы: если четыре
tracks распределены равномерно, но их сумма остаётся ниже цели, ограничение уже
на стороне WB SFU/carrier и следующий эксперимент — независимые KCP lanes с
явной capability, а не увеличение окон.

### Alpha.38 field result and alpha.39 diagnostic gate

Matching alpha.38 Android logs confirm that direct WB now subscribes and moves
payload over all four VP8 tracks. Per-track byte/frame counters are balanced,
queues are empty in the available sample, `kcp_wait_snd=0/512` and there are no
KCP drops. The measured aggregate speed is still approximately 1.57 Mbps, but
the WB session ended before a second saturated ten-second metrics interval.

Alpha.39 therefore does not increase KCP/DRR buffers. It records interval
`track_tx_kbps`/`track_rx_kbps` and per-track average/max `WriteSample` latency
and errors. A useful next field run keeps one WB download active for at least
30 seconds and retains matching client and Creator `METRICS`. Roughly equal
~0.4 Mbps rates on every track with low write latency indicate a carrier/SFU
ceiling; high write latency or growing track queues indicate a local publisher
bottleneck. Only that distinction determines whether the next reversible
experiment should change packetization/pacing or track topology.

### Alpha.41 wide WB carrier candidate

The retained alpha.40-era `relay (38)` sample does not contain saturated WB
metrics: its 0.9-1.1 Mbps interval belongs to the one-track VK bootstrap, and WB
connects only at the end of the file. It therefore cannot justify another KCP
window increase or duplicate ACK mechanism. KCP already ACKs and retransmits;
more control packets would reduce useful payload capacity.

Alpha.41 makes a topology change instead. Matching WB peers start eight
independently paced VP8 tracks rather than four, and advertise each carrier as
1920x1080 instead of 1280x720 so the SFU can allocate a larger video profile.
The existing KCP conversation continues to stripe segments round-robin, so the
wire format and reordering semantics do not change. VK stays single-track for
bootstrap compatibility.

The aggregate theoretical pacer ceiling rises from roughly 26 to 52 Mbps before
WebRTC/KCP overhead, loss and SFU policy. This is a scaling experiment, not a
20 Mbps guarantee: WB may enforce a participant-level cap independent of track
count. The field gate is matching `tracks=8` metrics from both sides during at
least 30 seconds of continuous WB load. If all eight tracks are balanced with
low write latency but aggregate throughput does not rise, the next architecture
candidate is capability-negotiated independent KCP lanes/flow sharding rather
than larger KCP or DRR queues.

## Полевой результат `0.5.0-alpha.2`: односторонний stall

Matching Android-клиент и сервер успешно согласовали `wire=1`, `caps=0x3` и
KCP. Во время Speedtest входящий поток продолжал получать VP8/KCP без пауз, но
обратное направление перестало подтверждаться:

| Uptime | RX, kbps | TX, kbps | WaitSnd | input idle |
|---:|---:|---:|---:|---:|
| 30s | 32.6 | 19.8 | 7/1024 | 11 ms |
| 40s | 703.3 | 33.3 | 516/1024 | 0 ms |
| 50s | 1655.0 | 20.3 | 745/1024 | 0 ms |
| 1m00s | 1704.6 | 12.7 | 839/1024 | 7 ms |
| 1m10s | 1135.5 | 10.8 | 972/1024 | 4 ms |
| 1m20s | 1514.8 | 0.5 | 932/1024 | 14 ms |

Новые `CONNECT` не получили `CONNECT_OK` за 20 секунд, поэтому upload-фаза не
началась. Текущий stall detector не сработал закономерно: он ищет полностью
молчаливый carrier, а здесь server→Joiner продолжал работать.

Реализовано для `0.5.0-alpha.3`:

1. ACK/UNA progress и его возраст видны отдельно от `last input`;
2. 75% окна без ACK progress 15 секунд считается односторонним stall и вызывает
   штатный reconnect;
3. KCP profile передаётся Creator → Joiner после capability handshake, Joiner
   выбирает более безопасный профиль;
4. отдельная negotiated reliable KCP lane переносит CONNECT и CONNECT_OK/ERR,
   обходя backlog bulk conversation.

Следующий P2 — DNS control message, per-flow queues, DRR, лимит UDP fan-out и
приоритет коротких интерактивных потоков. `CLOSE` нельзя просто переносить в
priority lane: сначала нужны sequence/drain semantics, иначе он обгонит DATA.

## Полевой результат alpha.11: bufferbloat без потери carrier

Тест от 2026-07-23 подтвердил matching `alpha.11`, `caps=0x1b`, balanced KCP,
нулевые `kcp_dropped`, `kcp_stalls` и `kcp_ack_stalls`. При этом четыре bulk
потока заполнили Joiner KCP до `1024/1024`, а DRR staging queue выросла примерно
до 1 MiB. На Creator исторический максимум staging queue достиг 4.19 MiB,
максимальное ожидание — 38.6s. Loaded ping Speedtest составил 7064ms при
фактическом relay throughput около 1.1Mbps.

Для alpha.12 реализован bounded-latency pass:

1. balanced KCP send/receive window уменьшено с 1024 до 512 segments;
2. per-flow staging limit уменьшен с 256KiB до 64KiB;
3. общий staging limit уменьшен с 8MiB до 512KiB;
4. удалённый CLOSE отменяет ещё не переданные в KCP frames этого flow;
5. Creator отправляет NACK неизвестному flow один раз и подавляет повторный
   stale-data log storm;
6. METRICS теперь явно показывает `fair_queue_limit` и `fair_flow_limit`.

Цель изменения — снизить loaded latency и быстрее восстановить интерактивный
трафик после bulk нагрузки. Оно не обещает увеличить физическую полосу SFU;
это проверяется matching alpha.12 тестом VPN и SOCKS-only отдельно.

## Windows alpha.3: Fast и stale routes

Полевой запуск full-TUN с локальным `fast` против старой Creator-сессии
(`caps=0x3`) за 10 секунд заполнил VP8 queue до `128/128`; `WaitSnd` достиг
1397 при почти отсутствующем обратном трафике. Затем Windows-процесс получил
access violation в socket poll path, а split-default routes могли остаться на
неактивном Wintun и оборвать обычный интернет.

Защита alpha.4:

1. full-TUN принудительно ограничивает Fast до Balanced; Fast остаётся только
   для SOCKS-only A/B;
2. peer без capability profile/control также ограничивается Balanced;
3. desktop watchdog удаляет stale `0.0.0.0/1` и `128.0.0.0/1` перед запуском и
   после выхода child process;
4. Windows artifact собирается свежей patch-версией Go;
5. экспортируемый UI log скрывает join link и SOCKS password.

## Рекомендуемый первый кодовый спринт

1. Metrics + benchmark harness.
2. Capability/version handshake.
3. VK Video KCP prototype с выровненным MTU.
4. Matching Windows Joiner artifact из того же commit.
5. A/B: DC, raw Video, KCP Video; только после этого fair scheduler.
