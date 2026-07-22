# UI-редизайн и хендофф — 21 июля 2026

> Обновление 22 июля (`v0.5.0-alpha.9`): описанные ниже пункты «не сделано»
> являются историческим состоянием на конец сессии 21 июля. После неё панель
> получила контекстные меню, Windows — упрощённую сводку подключения и
> `Advanced transport`, Android — фирменные заголовки, launcher и иконки
> уведомлений. Постоянная Android release-подпись добавлена в workflow; старый
> debug APK придётся удалить один последний раз. Подробности — в актуальном
> разделе `AGENTS.md`.

Сессия была **только про интерфейс** (плюс один фикс отзывчивости панели).
Транспорт, протокол, wire и Go-логика **не менялись**. Правила из `AGENTS.md`
про транспорт остаются в силе.

## Общий дизайн-язык

Ушли от старой «кроваво-графитовой» гаммы (бордо `#772136` / near-black) к
**классическому готическому мрамору**:

- бренд **«VL»** с геральдической **fleur-de-lis** (inline SVG, рисуется золотом);
- две темы:
  - **Argent** — белый каррарский мрамор, чёрный текст, золотые тонкие линии;
  - **Sable** — чёрный мрамор, слоновая кость + золото;
- переключатель day/night, выбор темы хранится в `localStorage`.

Гербовые токены: `argent` (мрамор фон) · `or`/gold (акцент) · `sable` (чёрный)
· `ivory` (текст). Заголовки — serif (`Cormorant Garamond`/Georgia), управление
— sans, диагностика — monospace.

### Ключи localStorage

| Ключ | Значения | Где |
|---|---|---|
| `wlb-theme` | `light` \| `dark` | панель, Windows-клиент |
| `wlb-forge` | `open` \| `collapsed` | только панель (свёрнутость формы) |

## 1. Панель (`headless/manager/web/`) — на `main`, commit `4c87603`

Статика вшита в Go через `//go:embed web/*` и раздаётся `http.FileServer`
(см. `headless/manager/main.go:33` и `:470`). Go-код **не трогался** — только
три файла в `web/`.

- **`index.html`**: бренд VL + `<symbol id="fleur">` (SVG), кнопка `#themeToggle`
  с `#themeLabel`, анти-flash inline-скрипт установки темы в `<head>`. Форма
  «Client Forge» обёрнута в `.forge-body` с кнопкой сворачивания `#forgeToggle`.
- **`app.css`**: полностью переписан. Две темы через
  `html[data-theme="light"]` / `[data-theme="dark"]` с CSS-переменными.
  Мраморные текстуры (`--veins`), готическая арка за шапкой, золотые уголки
  карточек, щитовидные глифы профилей. `color-mix()` для состояний рун.
- **`app.js`**: добавлены `applyTheme()` + `initTheme()` + `initForgeToggle()`;
  в клик по сессии добавлен `scrollIntoView` к `.diagnostics`.

### Фикс «профили появляются только после нескольких перезагрузок»

Был реальный баг в `app.js`, не только нагрузка CPU:

1. `refresh()` использовал `Promise.all` на 3 запросах → профили не рисовались,
   пока не ответит тяжёлый `/api/sessions` (читает метрики каждого Creator).
2. `fetch` без таймаута + `if (app.refreshing) return`: одно зависшее соединение
   навсегда оставляло флаг `refreshing`, и панель «умирала» до ручного reload.

Исправлено: `api()` получил `AbortController` с таймаутом 9с; `refresh()`
рисует каждую секцию независимо через `.then().catch()` и `Promise.allSettled`.

### ⚠️ Инварианты панели (не сломать)

`app.js` обращается к элементам по `id` через `byId()` и к классам
(`.session-open`, `.profile-card`, `.status-rune`, `.metrics-grid` и т.д.).
**При правке HTML/CSS сохранять все эти id и имена классов.** Быстрая проверка:

```bash
# каждый byId(...) из JS должен существовать в HTML
grep -oE "byId\('[a-zA-Z]+'\)" app.js | sed -E "s/byId\('//;s/'\)//" | sort -u
grep -oE 'id="[a-zA-Z]+"' index.html | sed -E 's/id="//;s/"//' | sort -u
# node --check app.js  → синтаксис
```

## 2. Android (`android-app/`) — ветка `release/v0.5.0-alpha.8`, commit `6db24a7`

Ключевой факт: **Kotlin нигде не хардкодит цвета**, только `R.color.*`
(проверено grep'ом — ни `Color.parseColor`, ни сырых hex). Поэтому перекраска
свелась к смене палитры.

- `values/colors.xml` → **Argent** (светлый мрамор).
- `values-night/colors.xml` → **Sable** (чёрный мрамор).
- Тема уже `Theme.Material3.DayNight` → телефон сам переключает day/night по
  системной теме. Имена цветов сохранены → 34 drawable с `@color/` и весь
  Kotlin адаптируются автоматически.
- 5 drawable с вшитым hex (`bg_captcha_pill`, `bg_log_box_err/warn`,
  `bg_ping_result_fail`, `bg_settings_row_icon_danger`) переведены на новые
  токены `warn_amber_soft` / `error_red_soft` (добавлены в обе палитры).
- `windowLightStatusBar` / `windowLightNavigationBar` вынесены в bool-ресурс
  `light_system_bars` (`values/bools.xml`=true, `values-night/bools.xml`=false),
  чтобы иконки статус-бара были читаемы в обеих темах.
- `ic_launcher_*` (иконка приложения, зелёный `#3DDC84`) **не трогалась** —
  отдельная задача.

**Проверено статически**: parity палитр (14 цветов одинаковы в обеих),
все `@color/`/`R.color.` разрешаются. **Локально НЕ собрано** (нет Gradle/JDK) —
проверять сборку APK в CI (`.github/workflows/android-joiner.yml`).

## 3. Windows Joiner (`joiner-desktop-app/`) — ветка `release/v0.5.0-alpha.8`, commit `7b56dc5`

Electron-клиент. CSP в `index.html` — `script-src 'self'`, поэтому inline-скрипты
нельзя; логика темы живёт в renderer-бандле.

- `styles/app.css` переписан на Argent/Sable CSS-переменные (как в панели).
- `index.html`: fleur-de-lis в `.sigil` вместо буквы «W», кнопка `#themeToggle`
  + `#themeLabel` в шапке, `data-theme="dark"` на `<html>`.
- `src/renderer/index.ts`: добавлены `applyTheme()` + IIFE-инициализация с
  `localStorage`.
- **Проверено**: `npx tsc --noEmit` → exit 0.

## Гит-состояние на конец сессии

```
main                        4c87603  (панель, запушено пользователем)
release/v0.5.0-alpha.8       7b56dc5  Windows Joiner marble theme
                             6db24a7  Android marble theme
                             (branched from 4c87603, НЕ запушено)
```

`.gitignore` дополнен: `*.log`, `relay*.log`, `logpanel*.txt`, `photo_*` —
чтобы полевые логи (с IP назначения / данными сессии) и присланные скрины
не попадали в коммит и не срабатывал secret-scan хук.

### Следующие шаги по релизу

1. `git push -u origin release/v0.5.0-alpha.8`
2. PR `release/v0.5.0-alpha.8 → main`, слить.
3. Тег `v0.5.0-alpha.8` → триггерит release CI (APK + EXE + Docker multi-arch,
   GHCR + GitHub Release с SHA256).
4. Убедиться, что **Android Gradle build** в CI прошёл (локально не собирался).

## Открытые задачи (подняты пользователем, НЕ сделаны)

### Скорость / нет исходящего (upload)

Полевой лог показал: сервер шёл на профиле **`fast`** →
`tunnel=adaptive-kcp-active-fast`, очередь забита `kcp_wait_snd=2048/2048`,
`kcp_dropped=2956`, `tx_kbps` обвалился 1989→0, upload `rx_kbps≈2`, и звонок
оборвался на середине speedtest (`requesting carrier reconnect` → `Rejoining`).
Это задокументированный односторонний ACK-stall, который `fast` резко усугубляет.

- **Немедленно**: пользователю сказано переключить KCP-профиль клиента на
  **Balanced** и переснять тест. Ждём свежий redacted серверный лог.
- **Настоящее лечение**: P1 fair-mux — per-flow queues + DRR scheduler + DNS
  priority (см. `docs/PERFORMANCE_ROADMAP.md`, раздел P2). Не включать без
  matching-клиента и без измерений (правило `AGENTS.md`).
- Пользователь подтвердил, что **может снимать полевые замеры** (VPS + телефон/ПК),
  скрипт `scripts/benchmark-socks.ps1`.

### Подпись клиента

Новые APK/EXE не ставятся поверх старых («signatures do not match») — надо
удалять старое приложение. Нужна постоянная release-подпись в
`android-joiner.yml` / `windows-joiner.yml`. Пока не сделано.

### Глубина UX («как в 3X-UI»)

Пользователю всё ещё «неудобно» — сделан только визуал + свёрнутая форма +
автоскролл к диагностике. Полноценной переработки информационной архитектуры
(компактные строки клиентов, инлайн-действия) **не было**.

**Контекстное меню / инлайн-действия — реализованы после этой сессии.** Правый
клик или кнопка `⋮` на профиле/сессии открывает готическое меню. Профиль:
запустить, передать на телефон, изменить, включить/выключить, удалить. Сессия:
диагностика, копировать ссылку, остановить/удалить. Реализация находится в
`renderProfiles()`, `renderSessions()` и обработчиках `app.js`; прямые кнопки
Start/Stop сохранены. Не переименовывать существующие классы действий.
