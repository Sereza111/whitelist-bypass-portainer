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
- окна отправки/приёма увеличены до 256/1024/2048 для
  stable/balanced/fast после измеренного заполнения `WaitSnd=256`;
- bounded output queue увеличена до 1024 segments;
- 12-секундный silent-stall detector срабатывает только при полном `WaitSnd`
  и отсутствии входящих KCP segments, после чего запрашивает штатный reconnect
  carrier;
- доступны профили `stable`, `balanced`, `fast`;
- METRICS содержит throughput, KCP output queue, drops, backpressure,
  `kcp_stalls` и `kcp_input_idle_ms`.

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
