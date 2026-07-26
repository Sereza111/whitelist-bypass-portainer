# v0.5.0-alpha.20

## WB post-login completion

Alpha.19 успешно открыл device-assisted WB WebView и позволил войти в аккаунт,
но оставался на странице **Профиль** с исходной подсказкой. Клиент ожидал три
cookie, опрашивая только корневые URL. Cookie с ограниченным `Path` не обязана
возвращаться для `/`, поэтому отправка в Manager не начиналась.

Alpha.20:

- распознаёт `/profile` и видимую кнопку `Выйти` как успешный вход;
- один раз открывает корень WB Stream для инициализации stream-сессии;
- опрашивает текущий URL, login/profile и точный `slide-v3` endpoint;
- flush-ит Android CookieManager после определения аккаунта;
- показывает безопасный прогресс `N/3`, не раскрывая имена или значения cookies;
- автоматически продолжает прежнюю allowlisted HTTPS-отправку при `3/3`.

Manager по-прежнему проверяет полученную сессию через `slide-v3` до сохранения.
Pairing bearer, cookie values, телефон и OTP не попадают в UI, события и логи.

Branch и immutable-tag CI успешно собрали Android, Windows и Docker.

- APK SHA-256:
  `cdde5180216e9c85bee71d7d0199f300035e4edc7f75d99387180dfb32e7a5c9`.
- EXE SHA-256:
  `2e30a4e653ba698eff4a89fe47a90e29ba58c898b9573304da7488289bfd589f`.
