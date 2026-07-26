package bypass.whitelist

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.webkit.CookieManager
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.button.MaterialButton
import org.json.JSONObject
import org.json.JSONTokener
import java.net.HttpURLConnection
import java.net.URL
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

class WBLoginActivity : AppCompatActivity(R.layout.activity_wb_login) {

    private lateinit var webView: WebView
    private lateinit var status: TextView
    private lateinit var close: MaterialButton
    private val mainHandler = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private val uploadRunning = AtomicBoolean(false)
    private var serverOrigin = ""
    private var pairingToken = ""
    private var deviceId = ""
    private var finished = false
    private var nextUploadAt = 0L

    private val cookieProbe = object : Runnable {
        override fun run() {
            if (finished || isFinishing || isDestroyed) return
            probeCredentials()
            mainHandler.postDelayed(this, 1500)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        webView = findViewById(R.id.wbLoginWebView)
        status = findViewById(R.id.wbLoginStatus)
        close = findViewById(R.id.wbLoginClose)
        close.setOnClickListener { finish() }

        val input = intent?.data
        val server = input?.getQueryParameter("server").orEmpty().trimEnd('/')
        val token = input?.getQueryParameter("token").orEmpty()
        if (!validPairing(server, token)) {
            fail(getString(R.string.wb_login_invalid_pairing))
            return
        }
        serverOrigin = server
        pairingToken = token
        configureWebView()
        status.setText(R.string.wb_login_opening)
        webView.loadUrl(WB_LOGIN_URL)
        mainHandler.post(cookieProbe)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun configureWebView() {
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.settings.cacheMode = WebSettings.LOAD_DEFAULT
        webView.settings.javaScriptCanOpenWindowsAutomatically = false
        webView.settings.setSupportMultipleWindows(false)
        CookieManager.getInstance().setAcceptCookie(true)
        CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true)
        webView.webViewClient = object : WebViewClient() {
            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                status.setText(R.string.wb_login_waiting)
            }

            override fun onPageFinished(view: WebView, url: String) {
                val host = Uri.parse(url).host.orEmpty().lowercase()
                if (host == "wb.ru" || host.endsWith(".wb.ru")) ensureDeviceId()
                probeCredentials()
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (request.isForMainFrame) status.setText(R.string.wb_login_page_error)
            }
        }
    }

    private fun ensureDeviceId() {
        if (deviceId.isNotEmpty()) return
        val fallback = UUID.randomUUID().toString()
        val script = """(() => { let value = localStorage.getItem('wb_auth_api_device_id'); if (!value) { value = ${JSONObject.quote(fallback)}; localStorage.setItem('wb_auth_api_device_id', value); } return value; })()"""
        webView.evaluateJavascript(script) { result ->
            val parsed = runCatching { JSONTokener(result).nextValue() as? String }.getOrNull().orEmpty()
            deviceId = if (validDeviceId(parsed)) parsed else fallback
            probeCredentials()
        }
    }

    private fun probeCredentials() {
        if (finished || uploadRunning.get() || System.currentTimeMillis() < nextUploadAt) return
        val cookies = linkedMapOf<String, String>()
        COOKIE_URLS.forEach { url ->
            CookieManager.getInstance().getCookie(url)?.split(';')?.forEach cookie@{ part ->
                val index = part.indexOf('=')
                if (index <= 0) return@cookie
                val name = part.substring(0, index).trim()
                val value = part.substring(index + 1).trim()
                if (name in ALLOWED_COOKIES && value.isNotEmpty()) cookies[name] = value
            }
        }
        if (!REQUIRED_COOKIES.all(cookies::containsKey)) return
        if (deviceId.isEmpty()) {
            ensureDeviceId()
            return
        }
        uploadCredentials(cookies)
    }

    private fun uploadCredentials(cookies: Map<String, String>) {
        if (!uploadRunning.compareAndSet(false, true)) return
        status.setText(R.string.wb_login_uploading)
        val body = JSONObject().apply {
            put("deviceId", deviceId)
            put("cookies", JSONObject(cookies))
        }.toString()
        executor.execute {
            val result = runCatching {
                val connection = URL("$serverOrigin/api/wb-login/device/credentials").openConnection() as HttpURLConnection
                connection.requestMethod = "POST"
                connection.connectTimeout = 15_000
                connection.readTimeout = 25_000
                connection.doOutput = true
                connection.setRequestProperty("Authorization", "Bearer $pairingToken")
                connection.setRequestProperty("Content-Type", "application/json; charset=utf-8")
                connection.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
                val code = connection.responseCode
                connection.disconnect()
                code
            }.getOrDefault(0)
            mainHandler.post {
                uploadRunning.set(false)
                if (result in 200..299) {
                    finished = true
                    mainHandler.removeCallbacks(cookieProbe)
                    CookieManager.getInstance().flush()
                    webView.visibility = View.GONE
                    status.setText(R.string.wb_login_success)
                    close.setText(R.string.wb_login_done)
                } else {
                    nextUploadAt = System.currentTimeMillis() + 10_000
                    status.text = getString(R.string.wb_login_upload_failed, result)
                }
            }
        }
    }

    private fun fail(message: String) {
        finished = true
        webView.visibility = View.GONE
        status.text = message
    }

    override fun onDestroy() {
        finished = true
        mainHandler.removeCallbacksAndMessages(null)
        executor.shutdownNow()
        if (::webView.isInitialized) {
            webView.stopLoading()
            webView.loadUrl("about:blank")
            webView.destroy()
        }
        super.onDestroy()
    }

    companion object {
        private const val WB_LOGIN_URL = "https://stream.wb.ru/login"
        private val TOKEN_RE = Regex("^[A-Za-z0-9_-]{32,128}$")
        private val DEVICE_RE = Regex("^[A-Za-z0-9._-]{8,128}$")
        private val COOKIE_URLS = listOf(
            "https://stream.wb.ru/",
            "https://auth-stream.wb.ru/",
            "https://www.wildberries.ru/",
            "https://wb.ru/",
        )
        private val REQUIRED_COOKIES = setOf("wbx-refresh", "x_wbaas_token", "wbx-validation-key")
        private val ALLOWED_COOKIES = REQUIRED_COOKIES + "_wbauid"

        private fun validPairing(server: String, token: String): Boolean {
            val uri = runCatching { Uri.parse(server) }.getOrNull() ?: return false
            return uri.scheme == "https" && !uri.host.isNullOrBlank() && uri.userInfo == null &&
                (uri.path.isNullOrEmpty() || uri.path == "/") && uri.query == null && uri.fragment == null &&
                TOKEN_RE.matches(token)
        }

        private fun validDeviceId(value: String): Boolean = DEVICE_RE.matches(value)
    }
}
