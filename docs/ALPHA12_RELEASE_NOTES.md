# v0.5.0-alpha.12

Alpha.12 — bounded-latency и Android routing-selector release. Он основан на
matching полевом тесте alpha.11 от 2026-07-23.

## Исправления

- карточки **VPN / Proxy** на главном Android-экране используют
  `wrap_content`; на Huawei и других устройствах они больше не схлопываются в
  почти нулевую высоту;
- Android CI статически проверяет присутствие обеих видимых routing-карточек;
- balanced KCP window уменьшено с 1024 до 512 segments;
- DRR staging queue ограничена 64KiB на flow и 512KiB на tunnel;
- удалённый CLOSE отменяет неотправленный хвост logical flow;
- Creator отвечает на повторные DATA неизвестного flow одним NACK вместо
  сотен одинаковых CLOSE/log;
- METRICS сообщает `fair_queue_limit` и `fair_flow_limit`.

## Почему это сделано

В alpha.11 carrier и ACK продолжали работать без KCP drops/stalls, но очередь
Joiner достигла около 1.05MiB и 15.9s ожидания, а Creator — 4.19MiB и 38.6s.
Speedtest показал loaded ping 7064ms. Краткий скачок индикатора до сотен Mbps
не соответствовал relay counters: фактический максимум был около 1.1Mbps.

Alpha.12 уменьшает скрытую задержку и быстрее освобождает канал после закрытия
bulk-потоков. Это не обещание большей физической полосы звонка.

## Полевой тест

1. Развернуть matching Docker image `v0.5.0-alpha.12` без удаления `/data`.
2. Установить matching signed APK поверх `alpha.9+`.
3. На отключённом главном экране проверить видимые карточки **VPN** и
   **Proxy**.
4. Выполнить отдельный VPN Speedtest и SOCKS-only benchmark.
5. Сохранить matching клиентские и Creator `METRICS`.
6. Сравнить `fair_queue`, `fair_queue_limit`, `fair_max_wait_ms`,
   `kcp_wait_snd`, `kcp_backpressure_ms`, loaded ping и фактические
   `tx_kbps/rx_kbps`.

Call links, IP, cookies, recovery keys и SOCKS credentials в отчёт не включать.
