# v0.5.0-alpha.21

## WB mobile browser fingerprint

Alpha.20 successfully detected the logged-in WB account, collected the complete
allowlisted set and attempted the Manager upload. WB nevertheless rejected the
server-side `slide-v3` validation. The mobile WBAAS token was created with the
Android WebView User-Agent, but Manager replayed it with a desktop User-Agent.

Alpha.21 keeps that browser fingerprint consistent:

- Android includes its WebView User-Agent in the one-time HTTPS payload;
- Manager bounds it to 20–1024 characters and rejects control characters;
- `slide-v3` validation uses the same User-Agent as the phone login;
- the verified value is stored as internal `__wb_user_agent` metadata;
- WB Creator uses it for initial bearer refresh and every reconnect;
- legacy cookie files without metadata fall back to the common desktop UA.

Android also displays the Manager's redacted upstream failure reason instead of
only `HTTP 400`. The API never returns the upstream response body, cookies,
pairing bearer, phone or OTP.
