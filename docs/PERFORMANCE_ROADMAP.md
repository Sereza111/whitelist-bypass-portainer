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
- исправить SOCKS5 parser на `io.ReadFull`, а не предполагать один `Read` на
  handshake/request.

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
- доступны профили `stable`, `balanced`, `fast`;
- METRICS содержит throughput, KCP output queue, drops и backpressure.

## Рекомендуемый первый кодовый спринт

1. Metrics + benchmark harness.
2. Capability/version handshake.
3. VK Video KCP prototype с выровненным MTU.
4. Matching Windows Joiner artifact из того же commit.
5. A/B: DC, raw Video, KCP Video; только после этого fair scheduler.
