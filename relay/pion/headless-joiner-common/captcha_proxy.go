package joiner

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var activeCaptchaProxy struct {
	sync.Mutex
	listener net.Listener
	port     int
	keyCh    chan string
	doneCh   chan struct{}
}

var successTokenPattern = regexp.MustCompile(`(?i)["']success[_-]?token["']\s*:\s*["']([^"'\\\s]+)["']`)

func StartCaptchaProxy(redirectURI string, resolveFn ResolveFunc, logFn func(string, ...any)) int {
	StopCaptchaProxy()

	targetURL, err := url.Parse(redirectURI)
	if err != nil {
		captchaLog(logFn, "captcha proxy: invalid redirect URL")
		return 0
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		captchaLog(logFn, "captcha proxy: loopback listener failed")
		return 0
	}
	port := listener.Addr().(*net.TCPAddr).Port
	localOrigin := fmt.Sprintf("http://127.0.0.1:%d", port)
	upstreamOrigin := targetURL.Scheme + "://" + targetURL.Host

	keyCh := make(chan string, 1)
	var responseLogCount atomic.Int32

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	if resolveFn != nil {
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)
			resolvedIP, err := resolveFn(host)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, resolvedIP+":"+port)
		}
	}

	deliverToken := func(token, source string) {
		token = normalizeSuccessToken(token)
		if token == "" {
			return
		}
		select {
		case keyCh <- token:
			captchaLog(logFn, "captcha proxy: completion captured via %s", source)
		default:
		}
	}

	inspectResponse := func(res *http.Response, source string) error {
		rewriteProxyCookies(res)
		for _, headerName := range []string{"X-Success-Token", "X-Captcha-Token", "X-Captcha-Success-Token"} {
			deliverToken(res.Header.Get(headerName), "response header")
		}

		if res.StatusCode >= 300 && res.StatusCode < 400 {
			if location := res.Header.Get("Location"); location != "" {
				deliverToken(extractSuccessTokenFromURL(location), "redirect")
				if rewritten, ok := rewriteCaptchaRedirect(location, res.Request.URL, localOrigin, upstreamOrigin); ok {
					res.Header.Set("Location", rewritten)
				}
			}
		}

		contentType := strings.ToLower(res.Header.Get("Content-Type"))
		requestPath := strings.ToLower(res.Request.URL.Path)
		if responseLogCount.Add(1) <= 16 {
			captchaLog(logFn, "captcha proxy: %s response status=%d type=%s bytes=%d",
				source, res.StatusCode, captchaContentClass(contentType), res.ContentLength)
		}
		contentLength := res.ContentLength
		textLike := strings.HasPrefix(contentType, "text/") || isJSONLike(contentType) ||
			strings.Contains(contentType, "xml") || strings.Contains(contentType, "x-www-form-urlencoded") ||
			contentType == "" || strings.Contains(contentType, "octet-stream")
		smallEnough := contentLength < 0 || contentLength <= 1<<20
		shouldInspect := isHTMLLike(contentType) || isJSONLike(contentType) ||
			strings.Contains(requestPath, "captcha") || strings.Contains(requestPath, "notrobot") ||
			(textLike && smallEnough)
		if !shouldInspect {
			return nil
		}

		reader := res.Body
		decompressed := false
		if res.Header.Get("Content-Encoding") == "gzip" {
			gzReader, err := gzip.NewReader(res.Body)
			if err == nil {
				reader = gzReader
				decompressed = true
				defer gzReader.Close()
			}
		}

		bodyBytes, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		res.Body.Close()
		deliverToken(extractSuccessToken(bodyBytes), "response")

		if isHTMLLike(contentType) {
			for _, h := range []string{
				"Content-Security-Policy", "Content-Security-Policy-Report-Only",
				"X-Content-Security-Policy", "X-WebKit-CSP",
				"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
				"Cross-Origin-Resource-Policy", "X-Frame-Options",
				"Strict-Transport-Security", "Alt-Svc",
			} {
				res.Header.Del(h)
			}
			bodyBytes = []byte(rewriteCaptchaHTML(string(bodyBytes), localOrigin, res.Request.URL.String()))
		}

		if decompressed {
			res.Header.Del("Content-Encoding")
		}
		res.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		res.ContentLength = int64(len(bodyBytes))
		res.Header.Set("Content-Length", fmt.Sprint(len(bodyBytes)))
		return nil
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			req.Out.URL.Scheme = targetURL.Scheme
			req.Out.URL.Host = targetURL.Host
			if req.Out.URL.Path == "" {
				req.Out.URL.Path = targetURL.Path
			}
			req.Out.Host = targetURL.Host
			req.Out.Header.Del("Accept-Encoding")
			req.Out.Header.Del("TE")
			for _, headerName := range []string{"Origin", "Referer"} {
				val := req.Out.Header.Get(headerName)
				if val != "" {
					req.Out.Header.Set(headerName, strings.ReplaceAll(val, localOrigin, upstreamOrigin))
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			return inspectResponse(res, "primary")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			captchaLog(logFn, "captcha proxy: upstream request failed: %s", safeCaptchaError(err))
			http.Error(w, "captcha upstream unavailable", http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/local-captcha-result", func(w http.ResponseWriter, r *http.Request) {
		token := r.FormValue("token")
		deliverToken(token, "page hook")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/generic_proxy", func(w http.ResponseWriter, r *http.Request) {
		proxyURL := r.URL.Query().Get("proxy_url")
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Error(w, "Bad URL", http.StatusBadRequest)
			return
		}
		genericProxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				req.Out.URL.Scheme = parsed.Scheme
				req.Out.URL.Host = parsed.Host
				req.Out.URL.Path = parsed.Path
				req.Out.URL.RawQuery = parsed.RawQuery
				req.Out.Host = parsed.Host
				req.Out.Header.Del("Accept-Encoding")
				for _, headerName := range []string{"Origin", "Referer"} {
					value := req.Out.Header.Get(headerName)
					if value != "" {
						req.Out.Header.Set(headerName, strings.ReplaceAll(value, localOrigin, parsed.Scheme+"://"+parsed.Host))
					}
				}
			},
			ModifyResponse: func(res *http.Response) error {
				return inspectResponse(res, "secondary")
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				captchaLog(logFn, "captcha proxy: secondary upstream failed: %s", safeCaptchaError(err))
				http.Error(w, "captcha secondary upstream unavailable", http.StatusBadGateway)
			},
		}
		genericProxy.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && targetURL.Path != "" && targetURL.Path != "/" && r.URL.RawQuery == "" {
			localPath := targetURL.Path
			if targetURL.RawQuery != "" {
				localPath += "?" + targetURL.RawQuery
			}
			http.Redirect(w, r, localPath, http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	activeCaptchaProxy.Lock()
	activeCaptchaProxy.listener = listener
	activeCaptchaProxy.port = port
	activeCaptchaProxy.keyCh = keyCh
	activeCaptchaProxy.doneCh = make(chan struct{})
	activeCaptchaProxy.Unlock()

	go http.Serve(listener, mux)
	captchaLog(logFn, "captcha proxy: ready for VK verification")

	return port
}

func GetCaptchaResult() string {
	activeCaptchaProxy.Lock()
	ch := activeCaptchaProxy.keyCh
	done := activeCaptchaProxy.doneCh
	activeCaptchaProxy.Unlock()
	if ch == nil || done == nil {
		return ""
	}
	select {
	case token := <-ch:
		return token
	case <-done:
		return ""
	case <-time.After(300 * time.Second):
		return ""
	}
}

func StopCaptchaProxy() {
	activeCaptchaProxy.Lock()
	ln := activeCaptchaProxy.listener
	done := activeCaptchaProxy.doneCh
	activeCaptchaProxy.listener = nil
	activeCaptchaProxy.port = 0
	activeCaptchaProxy.keyCh = nil
	activeCaptchaProxy.doneCh = nil
	activeCaptchaProxy.Unlock()
	if done != nil {
		close(done)
	}
	if ln != nil {
		ln.Close()
	}
}

func rewriteProxyCookies(res *http.Response) {
	cookies := res.Cookies()
	if len(cookies) == 0 {
		return
	}
	res.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		cookie.Domain = ""
		cookie.Secure = false
		cookie.Partitioned = false
		if cookie.SameSite == http.SameSiteNoneMode || cookie.SameSite == http.SameSiteStrictMode {
			cookie.SameSite = http.SameSiteLaxMode
		}
		res.Header.Add("Set-Cookie", cookie.String())
	}
}

func isHTMLLike(contentType string) bool {
	return strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml")
}

func isJSONLike(contentType string) bool {
	return strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "text/json") ||
		strings.Contains(contentType, "+json")
}

func captchaContentClass(contentType string) string {
	switch {
	case isHTMLLike(contentType):
		return "html"
	case isJSONLike(contentType):
		return "json"
	case strings.HasPrefix(contentType, "text/"):
		return "text"
	case strings.Contains(contentType, "image/"):
		return "image"
	case contentType == "":
		return "unknown"
	default:
		return "binary"
	}
}

func extractSuccessToken(body []byte) string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	var payload interface{}
	if json.Unmarshal([]byte(trimmed), &payload) == nil {
		if token := findSuccessToken(payload, 0); token != "" {
			return normalizeSuccessToken(token)
		}
	}
	if first, last := strings.IndexByte(trimmed, '{'), strings.LastIndexByte(trimmed, '}'); first >= 0 && last > first {
		if json.Unmarshal([]byte(trimmed[first:last+1]), &payload) == nil {
			if token := findSuccessToken(payload, 0); token != "" {
				return normalizeSuccessToken(token)
			}
		}
	}
	if values, err := url.ParseQuery(trimmed); err == nil {
		for _, key := range []string{"success_token", "successToken"} {
			if token := normalizeSuccessToken(values.Get(key)); token != "" {
				return token
			}
		}
	}
	if match := successTokenPattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return normalizeSuccessToken(match[1])
	}
	return ""
}

func findSuccessToken(value interface{}, depth int) string {
	if depth > 8 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		completionState := ""
		for _, key := range []string{"status", "state", "type", "result", "event", "action", "name"} {
			if state, ok := typed[key].(string); ok {
				completionState += " " + strings.ToLower(state)
			}
		}
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if normalized == "success_token" || normalized == "successtoken" {
				if token, ok := child.(string); ok {
					return token
				}
			}
		}
		if strings.Contains(completionState, "success") || strings.Contains(completionState, "solved") ||
			strings.Contains(completionState, "complete") || strings.Contains(completionState, "done") {
			if token := findCompletionToken(typed, 0); token != "" {
				return token
			}
		}
		for _, child := range typed {
			if token := findSuccessToken(child, depth+1); token != "" {
				return token
			}
		}
	case []interface{}:
		for _, child := range typed {
			if token := findSuccessToken(child, depth+1); token != "" {
				return token
			}
		}
	}
	return ""
}

func findCompletionToken(value interface{}, depth int) string {
	if depth > 5 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if normalized == "token" || normalized == "captchatoken" || normalized == "captcha_token" ||
				normalized == "responsetoken" || normalized == "response_token" {
				if token, ok := child.(string); ok {
					return token
				}
			}
		}
		for _, child := range typed {
			if token := findCompletionToken(child, depth+1); token != "" {
				return token
			}
		}
	case []interface{}:
		for _, child := range typed {
			if token := findCompletionToken(child, depth+1); token != "" {
				return token
			}
		}
	}
	return ""
}

func extractSuccessTokenFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, values := range []url.Values{parsed.Query(), parseURLFragment(parsed.Fragment)} {
		for _, key := range []string{"success_token", "successToken"} {
			if token := values.Get(key); token != "" {
				return token
			}
		}
	}
	return ""
}

func normalizeSuccessToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) < 8 || len(token) > 8192 || strings.ContainsAny(token, "\r\n\t <>\"'") {
		return ""
	}
	return token
}

func captchaLog(logFn func(string, ...any), format string, args ...any) {
	if logFn != nil {
		logFn(format, args...)
	}
}

func safeCaptchaError(err error) string {
	if err == nil {
		return "unknown error"
	}
	if networkErr, ok := err.(net.Error); ok {
		if networkErr.Timeout() {
			return "network timeout"
		}
		return "network error"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "certificate") || strings.Contains(message, "x509"):
		return "TLS certificate error"
	case strings.Contains(message, "context canceled"):
		return "request canceled"
	case strings.Contains(message, "no such host"):
		return "DNS lookup failed"
	default:
		return "request failed"
	}
}

func parseURLFragment(fragment string) url.Values {
	fragment = strings.TrimPrefix(fragment, "?")
	if queryAt := strings.IndexByte(fragment, '?'); queryAt >= 0 {
		fragment = fragment[queryAt+1:]
	}
	values, err := url.ParseQuery(fragment)
	if err != nil {
		return url.Values{}
	}
	return values
}

func rewriteCaptchaRedirect(location string, requestURL *url.URL, localOrigin, primaryUpstreamOrigin string) (string, bool) {
	if requestURL == nil {
		return "", false
	}
	redirectURL, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	redirectURL = requestURL.ResolveReference(redirectURL)
	if redirectURL.Scheme != "http" && redirectURL.Scheme != "https" {
		return "", false
	}

	redirectOrigin := redirectURL.Scheme + "://" + redirectURL.Host
	if strings.EqualFold(redirectOrigin, primaryUpstreamOrigin) {
		return localOrigin + redirectURL.RequestURI(), true
	}
	return localOrigin + "/generic_proxy?proxy_url=" + url.QueryEscape(redirectURL.String()), true
}

func rewriteCaptchaHTML(documentHTML, localOrigin, upstreamPage string) string {
	base := fmt.Sprintf(`<base href="%s">`, html.EscapeString(upstreamPage))
	localOriginJSON, _ := json.Marshal(localOrigin)
	upstreamPageJSON, _ := json.Marshal(upstreamPage)

	script := fmt.Sprintf(`
<script>
(function() {
    var localOrigin = %s;
    var upstreamPage = %s;

    function rewriteUrl(urlStr) {
        if (!urlStr || typeof urlStr !== 'string') return urlStr;
        if (urlStr.indexOf(localOrigin) === 0) return urlStr;
        if (urlStr.indexOf('/local-captcha-result') === 0 || urlStr.indexOf('/generic_proxy') === 0) return urlStr;
        if (urlStr.charAt(0) === '#' || /^(data|blob|javascript|mailto|tel):/i.test(urlStr)) return urlStr;
        try {
            var absoluteUrl = new URL(urlStr, upstreamPage);
            if (absoluteUrl.protocol !== 'http:' && absoluteUrl.protocol !== 'https:') return urlStr;
            return localOrigin + '/generic_proxy?proxy_url=' + encodeURIComponent(absoluteUrl.href);
        } catch (e) {
            return urlStr;
        }
    }

    function rewriteElementAttr(el, attr) {
        if (!el || !el.getAttribute) return;
        if (el.tagName && el.tagName.toLowerCase() === 'base') return;
        var value = el.getAttribute(attr);
        if (!value) return;
        var rewritten = rewriteUrl(value);
        if (rewritten !== value) el.setAttribute(attr, rewritten);
    }

    function rewriteDocument(root) {
        if (!root || !root.querySelectorAll) return;
        root.querySelectorAll('[href]').forEach(function(el) { rewriteElementAttr(el, 'href'); });
        root.querySelectorAll('[src]').forEach(function(el) { rewriteElementAttr(el, 'src'); });
        root.querySelectorAll('form[action]').forEach(function(el) { rewriteElementAttr(el, 'action'); });
    }

    function handleSuccessToken(token) {
        if (!token) return;
        fetch('/local-captcha-result', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded'},
            body: 'token=' + encodeURIComponent(token)
        }).catch(function() {});
    }

    function findSuccessToken(value, depth) {
        if (!value || depth > 8) return '';
        if (Array.isArray(value)) {
            for (var i = 0; i < value.length; i++) {
                var arrayToken = findSuccessToken(value[i], depth + 1);
                if (arrayToken) return arrayToken;
            }
            return '';
        }
        if (typeof value !== 'object') return '';
        var keys = Object.keys(value);
        var completionState = '';
        ['status', 'state', 'type', 'result', 'event', 'action', 'name'].forEach(function(key) {
            if (typeof value[key] === 'string') completionState += ' ' + value[key].toLowerCase();
        });
        for (var k = 0; k < keys.length; k++) {
            var normalized = keys[k].toLowerCase().replace(/-/g, '_');
            if ((normalized === 'success_token' || normalized === 'successtoken') &&
                typeof value[keys[k]] === 'string') {
                return value[keys[k]];
            }
        }
        if (/success|solved|complete|done/.test(completionState)) {
            var fallbackToken = findCompletionToken(value, 0);
            if (typeof fallbackToken === 'string') return fallbackToken;
        }
        for (var j = 0; j < keys.length; j++) {
            var nestedToken = findSuccessToken(value[keys[j]], depth + 1);
            if (nestedToken) return nestedToken;
        }
        return '';
    }

    function findCompletionToken(value, depth) {
        if (!value || depth > 5) return '';
        if (Array.isArray(value)) {
            for (var i = 0; i < value.length; i++) {
                var arrayToken = findCompletionToken(value[i], depth + 1);
                if (arrayToken) return arrayToken;
            }
            return '';
        }
        if (typeof value !== 'object') return '';
        var keys = Object.keys(value);
        for (var k = 0; k < keys.length; k++) {
            var normalized = keys[k].toLowerCase().replace(/-/g, '_');
            if (/^(token|captchatoken|captcha_token|responsetoken|response_token)$/.test(normalized) &&
                typeof value[keys[k]] === 'string') return value[keys[k]];
        }
        for (var j = 0; j < keys.length; j++) {
            var nestedToken = findCompletionToken(value[keys[j]], depth + 1);
            if (nestedToken) return nestedToken;
        }
        return '';
    }

    function inspectText(text) {
        if (!text || typeof text !== 'string') return;
        try { handleSuccessToken(findSuccessToken(JSON.parse(text), 0)); } catch (e) {}
        try {
            var firstBrace = text.indexOf('{');
            var lastBrace = text.lastIndexOf('}');
            if (firstBrace >= 0 && lastBrace > firstBrace) {
                handleSuccessToken(findSuccessToken(JSON.parse(text.slice(firstBrace, lastBrace + 1)), 0));
            }
        } catch (e) {}
        try {
            var params = new URLSearchParams(text);
            handleSuccessToken(params.get('success_token') || params.get('successToken'));
        } catch (e) {}
    }

    function inspectLocation() {
        try {
            var parts = [window.location.search.substring(1), window.location.hash.substring(1)];
            parts.forEach(function(part) {
                var queryAt = part.indexOf('?');
                if (queryAt >= 0) part = part.substring(queryAt + 1);
                var params = new URLSearchParams(part);
                handleSuccessToken(params.get('success_token') || params.get('successToken'));
            });
        } catch (e) {}
    }

    function inspectDocumentToken() {
        try {
            var tokenNode = document.querySelector(
                'input[name="success_token"], input[name="successToken"], [data-success-token]'
            );
            if (!tokenNode) return;
            handleSuccessToken(tokenNode.value || tokenNode.getAttribute('data-success-token'));
        } catch (e) {}
    }

    var origOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function() {
        if (arguments[1] && typeof arguments[1] === 'string') {
            this._origUrl = arguments[1];
            arguments[1] = rewriteUrl(arguments[1]);
        }
        return origOpen.apply(this, arguments);
    };
    var origSend = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function() {
        var xhr = this;
        xhr.addEventListener('load', function() {
            try { inspectText(xhr.responseText); } catch (e) {}
        });
        return origSend.apply(this, arguments);
    };

    var origFetch = window.fetch;
    if (origFetch) {
        window.fetch = function() {
            var requestInput = arguments[0];
            var urlStr = (typeof requestInput === 'object' && requestInput && requestInput.url) ? requestInput.url : requestInput;
            if (typeof urlStr === 'string') {
                var rewrittenUrl = rewriteUrl(urlStr);
                if (typeof requestInput === 'object' && requestInput && requestInput.url && window.Request) {
                    arguments[0] = new Request(rewrittenUrl, requestInput);
                } else {
                    arguments[0] = rewrittenUrl;
                }
            }
            var p = origFetch.apply(this, arguments);
            p.then(function(r) { return r.clone().text(); }).then(inspectText).catch(function() {});
            return p;
        };
    }

    var origWindowOpen = window.open;
    if (origWindowOpen) {
        window.open = function(url) {
            if (typeof url === 'string') arguments[0] = rewriteUrl(url);
            return origWindowOpen.apply(this, arguments);
        };
    }

    rewriteDocument(document);
    inspectLocation();
    inspectDocumentToken();
    window.addEventListener('hashchange', inspectLocation);
    window.addEventListener('popstate', inspectLocation);
    window.addEventListener('message', function(event) {
        try {
            if (typeof event.data === 'string') {
                inspectText(event.data);
            } else {
                handleSuccessToken(findSuccessToken(event.data, 0));
            }
        } catch (e) {}
    });
    setInterval(function() {
        inspectLocation();
        inspectDocumentToken();
    }, 750);
    if (document.documentElement && window.MutationObserver) {
        new MutationObserver(function(mutations) {
            mutations.forEach(function(mutation) {
                if (mutation.type === 'attributes' && mutation.target) {
                    rewriteElementAttr(mutation.target, mutation.attributeName);
                    return;
                }
                mutation.addedNodes.forEach(function(node) {
                    if (node.nodeType === 1) rewriteDocument(node);
                });
            });
        }).observe(document.documentElement, {
            subtree: true, childList: true, attributes: true,
            attributeFilter: ['href', 'src', 'action']
        });
    }
})();
</script>
`, localOriginJSON, upstreamPageJSON)

	headPattern := regexp.MustCompile(`(?i)<head\b[^>]*>`)
	if head := headPattern.FindStringIndex(documentHTML); head != nil {
		return documentHTML[:head[1]] + base + script + documentHTML[head[1]:]
	}
	if idx := strings.Index(strings.ToLower(documentHTML), "</body>"); idx >= 0 {
		return documentHTML[:idx] + base + script + documentHTML[idx:]
	}
	return base + script + documentHTML
}
