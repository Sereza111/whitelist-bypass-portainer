# Android Joiner

Android-клиент собирается из тех же исходников relay, что Docker Creator и
Windows Joiner. В репозитории намеренно отсутствуют готовые mobile.aar,
librelay.so, APK и keystore.

Workflow **Build Android Joiner** выполняет:

1. тесты общего transport;
2. gomobile bind для Android VPN/tun2socks bindings;
3. cross-compile Go relay для armeabi-v7a и arm64-v8a;
4. сборку подписанного стандартным временным debug key APK;
5. проверку подписи, ABI и SHA-256.

## Скачать

Для обычной проверки откройте GitHub **Actions → Build Android Joiner**,
выберите успешный run и скачайте artifact whitelist-bypass-android-....
После создания тега APK также прикрепляется к GitHub Release.

На Android может потребоваться разрешить установку приложений из браузера или
файлового менеджера. Debug-подпись годится для alpha-тестов, но перед публичным
stable-релизом понадобится постоянный release key, хранящийся только в GitHub
Secrets.

## Подключение

1. В панели Creator создайте отдельную сессию для телефона.
2. Добавьте выданную ссылку в Android-приложение.
3. Оставьте headless и Video; для VK приложение использует negotiated
   reliability auto.
4. Разрешите Android создать VPN-подключение.

Одна Creator-сессия рассчитана на один Joiner. Не подключайте ПК и телефон к
одной ссылке одновременно.

VK Video включает KCP только когда сервер подтвердил capability video_kcp1.
Со старым или raw сервером приложение остаётся в degraded raw режиме.
Исправление повторных DNS-запросов присутствует и в мобильном relay.

