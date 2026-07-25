# v0.5.0-alpha.15

Alpha.15 fixes the remaining VK captcha redirect failure observed with the
matching alpha.14 Android client.

## Captcha redirect containment

The field log reached `Captcha page ready`, but never reached
`captcha proxy: completion captured` or `vk-auth: captcha solved`. It also
showed a single HTTP 301 followed by no proxied responses. The VK challenge
had redirected its WebView to another origin, outside the local captcha proxy,
so solving the visible page could not return a completion token to the app.

The local proxy now:

- resolves relative and absolute HTTP redirects against the actual upstream
  response URL;
- keeps redirects to the original VK origin on the loopback origin;
- sends cross-origin redirects through the generic loopback proxy;
- injects the completion hook into HTML returned by both primary and secondary
  origins;
- preserves the secondary page base URL and resolves relative Fetch, XHR,
  link, form and resource URLs through the generic proxy;
- accepts only HTTP(S) generic-proxy targets and continues to avoid logging
  URLs, query strings, tokens, links or cookies.

An integration test reproduces the exact two-origin 301 flow and verifies that
the final browser URL is still loopback and the secondary HTML contains the
completion hook. Existing generic JSON completion-token coverage remains.

## Deployment

Update both the Android APK and the Portainer image to alpha.15. The captcha
proxy runs in the Android Joiner, while matching client/server builds keep
runtime diagnosis unambiguous.

