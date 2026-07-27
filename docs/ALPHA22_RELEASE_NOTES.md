# v0.5.0-alpha.22

## WB HTTP 200 without a parsed token

Safe field logs from alpha.21 prove that Manager reached `slide-v3` and received
HTTP 200, but did not find `payload.access_token`. Alpha.22 fixes three possible
causes without exposing any credential:

- Android prioritises the Cookie header selected for the exact
  `auth-stream.wb.ru/v2/auth/slide-v3` URL. Same-named cookies from later
  Wildberries/root probes no longer overwrite it.
- Android re-reads the final `wb_auth_api_device_id` after authenticated Profile
  detection instead of relying only on the value captured before login.
- Manager and WB Creator accept `access_token` or `accessToken`, nested under
  `payload` or at the response top level.

If WB still returns no access token, the redacted error contains only top-level
and payload JSON key names. Response values/body, cookies, bearer, phone and OTP
remain absent from API, events and logs.

Android now stops after three identical upload failures and asks for a fresh QR,
preventing the prior 10-second infinite retry loop.

## Correct build identity

Branch CI previously hard-coded `0.5.0-alpha.18+SHA` even for newer source.
Android, Docker and Windows workflows now read the base version from the Docker
build default. Tagged releases continue to use the immutable tag version.

Branch and immutable-tag CI successfully built Android, Windows and Docker.

- APK SHA-256:
  `faa71978e8b551ace309ae2b86fd5c74e5a7006df8cca0f12f61a26b4952809f`.
- EXE SHA-256:
  `7ed545f1b9189944eaa6c26b91079054c368f94ef6829346118f0b34fd04338c`.
