# v0.5.0-alpha.14

Alpha.14 is a focused Android captcha and routing-UX correction after the
matching alpha.13 field log still stopped at `vk-auth: captcha required`.

## Captcha lifecycle

The alpha.13 field log proved that the matching APK was installed, but showed
only repeated page reload requests and no `Captcha page ready` or
`captcha solved`. Alpha.14 therefore covers the remaining VK widget paths:

- structured `postMessage` completion from cross-origin captcha frames;
- JSON, JSONP, form-encoded and small text responses regardless of the old
  endpoint name;
- nested success events whose token is carried in a child object;
- query/hash redirects and the existing XHR/Fetch/DOM observers;
- third-party WebView cookies and no-cache captcha loading.

Safe diagnostics now report only lifecycle, response class, HTTP status and
body size. Full URLs, query strings, token values, call links and cookies are
never logged. Main-frame WebView load/HTTP failures are visible to the user and
in exported logs.

## Android routing UI

The main screen again contains only **Tunnel** and **Proxy**. Transparent
per-app routing remains available under **Settings → Split tunneling → Only**.
It still uses Android `VpnService` and does not require applications to support
SOCKS5. Empty or uninstalled selections fail closed.

## New users and VK identities

Normal clients do not provide a VK account. The server's technical VK creates
one isolated call per panel profile, and Android joins as a guest. The panel
now states this explicitly in the client editor and provider page.

Uploading personal VK cookies from Android was deliberately not implemented:
Android cannot read another application's cookies within the normal sandbox,
and transferring them would grant the server control of the user's account.
Use **New client → Start → Copy to phone**. A future pool of multiple technical
server identities, if needed for provider limits, must use isolated server-side
QR slots rather than client cookie export.

