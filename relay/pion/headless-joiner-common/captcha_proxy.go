package joiner

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

var activeCaptchaProxy struct {
	sync.Mutex
	listener net.Listener
	port     int
	keyCh    chan string
	doneCh   chan struct{}
}

func StartCaptchaProxy(redirectURI string, resolveFn ResolveFunc) int {
	StopCaptchaProxy()

	targetURL, err := url.Parse(redirectURI)
	if err != nil {
		return 0
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := listener.Addr().(*net.TCPAddr).Port
	localOrigin := fmt.Sprintf("http://127.0.0.1:%d", port)
	upstreamOrigin := targetURL.Scheme + "://" + targetURL.Host

	keyCh := make(chan string, 1)

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

	deliverToken := func(token string) {
		if token == "" {
			return
		}
		select {
		case keyCh <- token:
		default:
		}
	}

	inspectResponse := func(res *http.Response, responseUpstreamOrigin string, rewriteHTML bool) error {
		rewriteProxyCookies(res)

		if res.StatusCode >= 300 && res.StatusCode < 400 {
			if location := res.Header.Get("Location"); location != "" {
				deliverToken(extractSuccessTokenFromURL(location))
				if rewriteHTML {
					res.Header.Set("Location", strings.ReplaceAll(location, responseUpstreamOrigin, localOrigin))
				}
			}
		}

		contentType := strings.ToLower(res.Header.Get("Content-Type"))
		requestPath := strings.ToLower(res.Request.URL.Path)
		shouldInspect := isHTMLLike(contentType) || isJSONLike(contentType) ||
			strings.Contains(requestPath, "captcha") || strings.Contains(requestPath, "notrobot")
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
		deliverToken(extractSuccessToken(bodyBytes))

		if rewriteHTML && isHTMLLike(contentType) {
			for _, h := range []string{
				"Content-Security-Policy", "Content-Security-Policy-Report-Only",
				"X-Content-Security-Policy", "X-WebKit-CSP",
				"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
				"Cross-Origin-Resource-Policy", "X-Frame-Options",
				"Strict-Transport-Security", "Alt-Svc",
			} {
				res.Header.Del(h)
			}
			bodyBytes = []byte(rewriteCaptchaHTML(string(bodyBytes), localOrigin, responseUpstreamOrigin))
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
			return inspectResponse(res, upstreamOrigin, true)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/local-captcha-result", func(w http.ResponseWriter, r *http.Request) {
		token := r.FormValue("token")
		deliverToken(token)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/generic_proxy", func(w http.ResponseWriter, r *http.Request) {
		proxyURL := r.URL.Query().Get("proxy_url")
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" {
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
				return inspectResponse(res, parsed.Scheme+"://"+parsed.Host, false)
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

func extractSuccessToken(body []byte) string {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return findSuccessToken(payload, 0)
}

func findSuccessToken(value interface{}, depth int) string {
	if depth > 8 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if normalized == "success_token" || normalized == "successtoken" {
				if token, ok := child.(string); ok {
					return token
				}
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

func rewriteCaptchaHTML(html, localOrigin, upstreamOrigin string) string {
	html = strings.ReplaceAll(html, upstreamOrigin, localOrigin)

	script := fmt.Sprintf(`
<script>
(function() {
    var localOrigin = %q;
    var upstreamOrigin = %q;

    function rewriteUrl(urlStr) {
        if (!urlStr || typeof urlStr !== 'string') return urlStr;
        if (urlStr.indexOf(localOrigin) === 0) return urlStr;
        if (urlStr.indexOf(upstreamOrigin) === 0) return localOrigin + urlStr.slice(upstreamOrigin.length);
        if (urlStr.indexOf('//') === 0) {
            return '/generic_proxy?proxy_url=' + encodeURIComponent(window.location.protocol + urlStr);
        }
        if (urlStr.indexOf('http://') === 0 || urlStr.indexOf('https://') === 0) {
            return '/generic_proxy?proxy_url=' + encodeURIComponent(urlStr);
        }
        return urlStr;
    }

    function rewriteElementAttr(el, attr) {
        if (!el || !el.getAttribute) return;
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
        for (var k = 0; k < keys.length; k++) {
            var normalized = keys[k].toLowerCase().replace(/-/g, '_');
            if ((normalized === 'success_token' || normalized === 'successtoken') &&
                typeof value[keys[k]] === 'string') {
                return value[keys[k]];
            }
        }
        for (var j = 0; j < keys.length; j++) {
            var nestedToken = findSuccessToken(value[keys[j]], depth + 1);
            if (nestedToken) return nestedToken;
        }
        return '';
    }

    function inspectText(text) {
        if (!text || typeof text !== 'string') return;
        try { handleSuccessToken(findSuccessToken(JSON.parse(text), 0)); } catch (e) {}
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
`, localOrigin, upstreamOrigin)

	if idx := strings.Index(html, "</head>"); idx >= 0 {
		return html[:idx] + script + html[idx:]
	}
	if idx := strings.Index(html, "</body>"); idx >= 0 {
		return html[:idx] + script + html[idx:]
	}
	return html + script
}
