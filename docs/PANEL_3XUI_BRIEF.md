# ТЗ: панель уровня 3X-UI без изменения transport

## Цель

Переработать `headless/manager` в удобную адаптивную control-plane панель с
информационной архитектурой уровня 3X-UI, сохранив оригинальный Argent/Sable
готический стиль проекта. Не копировать код, тексты или графические assets
3X-UI: это ориентир по плотности, навигации и удобству, а не шаблон.

## Изоляция работы

Разрешённая область:

- `headless/manager/**`;
- `docs/PANEL.md` и этот документ;
- при необходимости только manager-related поля в `portainer-stack.yml` и
  `.env.panel.example` для backward-compatible migration.

Не менять:

- `relay/**`, wire protocol, KCP, DNS, SOCKS/TUN и WebRTC;
- `headless/vk/**` transport/reconnect logic;
- Android/Windows clients;
- release workflows, signing и версии.

Перед началом прочитать `AGENTS.md`, `docs/PANEL.md` и manager tests. Работать
в отдельной ветке от актуального `main`. Не коммитить cookies, tokens, VK call
links, recovery keys, panel password, IP или пользовательские логи.

## Новая структура интерфейса

1. **Dashboard**: состояние Manager, VK login, recovery delivery, активные
   сессии, лимиты, ошибки и последние события.
2. **Clients**: компактная таблица/список профилей с поиском, фильтрами,
   enable/disable, start, edit, duplicate, pairing QR и delete.
3. **Sessions**: provider, generation, uptime, transport/profile, throughput,
   reconnect state, stop/restart и безопасный экспорт диагностики.
4. **Providers / VK**: QR login серверного аккаунта, статус cookies и отдельная
   настройка получателя recovery-сообщений.
5. **Settings**: глобальные лимиты и безопасные параметры; advanced-поля
   скрыты по умолчанию.
6. **Events**: структурированная временная шкала без секретов вместо поиска по
   сырым логам.

Desktop: боковая навигация и плотные строки. Mobile: нижняя навигация,
карточки без горизонтального скролла. Сохранить Argent/Sable, day/night,
клавиатурную навигацию, видимый focus, reduced motion и существующие action
classes/ids либо добавить совместимый слой.

## Убрать обязательный `VK_PEER_ID` из env

Сделать panel-managed recovery recipient без перезапуска стека.

### Модель данных

- глобальный recipient по умолчанию в persistent control-plane storage;
- необязательный recipient override на client profile;
- `VK_PEER_ID` остаётся только legacy fallback для существующих установок;
- при первом сохранении из панели persisted value получает приоритет над env;
- atomic write, migration старого `control-plane.json`, безопасные file modes;
- ID не является паролем, но не включать его в публичный diagnostics export.

### UX

- поле принимает цифровой VK ID; дополнительно можно распознать обычную ссылку
  профиля, если это делается через уже авторизованный серверный VK без утечки
  cookies/token;
- показать effective source: `panel`, `profile override` или `legacy env`;
- предупреждать, если recipient совпадает с серверным VK-аккаунтом;
- кнопки `Сохранить` и `Отправить тестовое сообщение`;
- тестовое сообщение содержит только название проекта, профиль и время, без
  call link/recovery key;
- результат: delivered/failed, timestamp и безопасная причина ошибки;
- для профиля показывать `Recovery verified` после успешного теста.

### Предлагаемый API

- `GET /api/settings/recovery` — masked/effective config и статус;
- `PATCH /api/settings/recovery` — глобальный recipient;
- `POST /api/settings/recovery/test` — тест глобального recipient;
- profile create/update — nullable `recoveryRecipient` override;
- `POST /api/profiles/{id}/recovery/test` — тест effective recipient профиля.

Все endpoints используют существующую panel auth, строгую валидацию,
ограничение частоты тестов и не возвращают cookies/token. Для отправки
использовать текущую managed VK session; не просить VK password в панели.

## Поведение и совместимость

- существующие profiles/sessions не теряются;
- старый env deployment продолжает работать;
- direct stack и panel stack не запускаются одновременно;
- медленный `/api/sessions` не должен блокировать clients/settings;
- все destructive actions требуют подтверждения;
- ошибки одной секции не ломают refresh остальных секций.

## Проверки готовности

- manager Go tests, `go vet`, JS syntax и `git diff --check`;
- migration tests: no setting, env fallback, persisted override, profile
  override, malformed old JSON;
- API tests: auth, validation, rate limit, test-send success/failure redaction;
- browser QA: desktop Argent/Sable и mobile 390x844;
- ни один ответ API, UI toast или diagnostics export не содержит cookies,
  access token, recovery key или call link;
- приложить screenshots и краткий handoff с перечнем изменённых API/schema.

## Definition of done

Администратор может войти через VK QR, указать получателя, отправить тест,
создать несколько client profiles, назначить им overrides, запускать и
диагностировать сессии без редактирования `.env` и без перезапуска Portainer.
