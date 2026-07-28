package bypass.whitelist

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
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
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.util.Log
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.button.MaterialButton
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

class WBLoginActivity : AppCompatActivity(R.layout.activity_wb_login) {

    private lateinit var webView: WebView
    private lateinit var status: TextView
	private lateinit var diagnostics: TextView
    private lateinit var close: MaterialButton
    private lateinit var invitePanel: LinearLayout
    private lateinit var inviteInput: EditText
    private lateinit var inviteSubmit: MaterialButton
    private val mainHandler = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private val requestRunning = AtomicBoolean(false)
    private var serverOrigin = ""
    private var creatorId = ""
    private var deviceSecret = ""
    private var pendingRequestId = ""
    private var destroyed = false
	private var pollCount = 0
	private val diagnosticLines = ArrayDeque<String>()

    private val commandPoll = object : Runnable {
        override fun run() {
            if (destroyed || isFinishing || isDestroyed) return
            if (creatorId.isNotEmpty() && deviceSecret.isNotEmpty() && pendingRequestId.isEmpty()) pollCommand()
            mainHandler.postDelayed(this, POLL_INTERVAL_MS)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        webView = findViewById(R.id.wbLoginWebView)
        status = findViewById(R.id.wbLoginStatus)
		diagnostics = findViewById(R.id.wbLoginDiagnostics)
        close = findViewById(R.id.wbLoginClose)
        invitePanel = findViewById(R.id.wbInvitePanel)
        inviteInput = findViewById(R.id.wbInviteInput)
        inviteSubmit = findViewById(R.id.wbInviteSubmit)
        close.setOnClickListener { finish() }
        inviteSubmit.setOnClickListener { submitInvite() }

        val input = intent?.data
        val server = input?.getQueryParameter("server").orEmpty().trimEnd('/')
        val token = input?.getQueryParameter("token").orEmpty()
        val prefs = getSharedPreferences(PREFS, MODE_PRIVATE)
        val freshPairing = validPairing(server, token)
		trace("boot", "version=${BuildConfig.VERSION_NAME} mode=${if (freshPairing) "pair" else "resume"}")
        if (freshPairing) {
            serverOrigin = server
        } else {
            serverOrigin = prefs.getString(KEY_SERVER, "").orEmpty()
            creatorId = prefs.getString(KEY_CREATOR_ID, "").orEmpty()
            deviceSecret = prefs.getString(KEY_DEVICE_SECRET, "").orEmpty()
            if (!validServer(serverOrigin) || !CREATOR_RE.matches(creatorId) || !TOKEN_RE.matches(deviceSecret)) {
				trace("binding", "missing-or-invalid")
                fail(getString(R.string.wb_login_invalid_pairing))
                return
            }
        }
        configureWebView()
        if (freshPairing) {
            pairCreator(token)
            status.setText(R.string.wb_creator_pairing)
        } else {
            status.setText(R.string.wb_creator_ready_help)
            mainHandler.post(commandPoll)
        }
        webView.loadUrl(WB_LOGIN_URL)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun configureWebView() {
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.settings.cacheMode = WebSettings.LOAD_DEFAULT
        webView.settings.javaScriptCanOpenWindowsAutomatically = true
        webView.settings.setSupportMultipleWindows(false)
        CookieManager.getInstance().setAcceptCookie(true)
        CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true)
        webView.webViewClient = object : WebViewClient() {
            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
				trace("webview", "page-start")
                if (pendingRequestId.isEmpty()) status.setText(R.string.wb_creator_ready_help)
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
				if (request.isForMainFrame) {
					trace("webview", "main-frame-error=${error.errorCode}")
					status.setText(R.string.wb_login_page_error)
				}
            }
        }
    }

    private fun pairCreator(pairingToken: String) {
		trace("pair", "request")
        val prefs = getSharedPreferences(PREFS, MODE_PRIVATE)
        val deviceId = prefs.getString(KEY_DEVICE_ID, null) ?: UUID.randomUUID().toString().also {
            prefs.edit().putString(KEY_DEVICE_ID, it).apply()
        }
        val body = JSONObject().apply {
            put("deviceId", deviceId)
            put("name", "${Build.MANUFACTURER} ${Build.MODEL}".trim().take(80))
        }.toString()
        executor.execute {
            val response = post("/api/wb-creator/pair", body, pairingToken, "")
            mainHandler.post {
				trace("pair", "http=${response.code}${response.transportError.takeIf { it.isNotEmpty() }?.let { " error=$it" }.orEmpty()}")
                if (response.code !in 200..299) {
                    fail(response.message.ifBlank { getString(R.string.wb_creator_pair_failed) })
                    return@post
                }
                val json = runCatching { JSONObject(response.body) }.getOrNull()
                creatorId = json?.optString("creatorId").orEmpty()
                deviceSecret = json?.optString("deviceSecret").orEmpty()
                if (!TOKEN_RE.matches(deviceSecret) || !CREATOR_RE.matches(creatorId)) {
                    fail(getString(R.string.wb_creator_pair_failed))
                    return@post
                }
                prefs.edit()
                    .putString(KEY_SERVER, serverOrigin)
                    .putString(KEY_CREATOR_ID, creatorId)
                    .putString(KEY_DEVICE_SECRET, deviceSecret)
                    .apply()
				trace("pair", "stored creator=${creatorId.takeLast(8)}")
                status.setText(R.string.wb_creator_ready_help)
                mainHandler.post(commandPoll)
            }
        }
    }

    private fun pollCommand() {
        if (!requestRunning.compareAndSet(false, true)) return
        executor.execute {
            val response = post("/api/wb-creator/commands/next", "", deviceSecret, creatorId)
            mainHandler.post {
                requestRunning.set(false)
				pollCount++
				if (response.code == HttpURLConnection.HTTP_NO_CONTENT) {
					if (pollCount == 1 || pollCount % 10 == 0) trace("poll", "http=204 count=$pollCount")
					return@post
				}
				trace("poll", "http=${response.code}${response.transportError.takeIf { it.isNotEmpty() }?.let { " error=$it" }.orEmpty()}")
                if (response.code == HttpURLConnection.HTTP_UNAUTHORIZED) {
                    status.setText(R.string.wb_creator_unpaired)
                    return@post
                }
                if (response.code !in 200..299) return@post
                val json = runCatching { JSONObject(response.body) }.getOrNull() ?: return@post
                val requestId = json.optString("id")
                if (!CREATOR_RE.matches(requestId)) return@post
                pendingRequestId = requestId
				trace("command", "received request=${requestId.takeLast(8)}")
                val profileName = json.optString("profileName").take(80)
                status.text = getString(R.string.wb_creator_call_requested, profileName)
                invitePanel.visibility = View.VISIBLE
                inviteInput.setText("")
            }
        }
    }

    private fun submitInvite() {
        if (pendingRequestId.isEmpty() || requestRunning.get()) return
        val candidate = inviteInput.text.toString().trim().ifEmpty { webView.url.orEmpty() }
        if (!isSafeInvite(candidate)) {
			trace("invite", "rejected-locally")
            status.setText(R.string.wb_creator_invalid_invite)
            return
        }
        if (!requestRunning.compareAndSet(false, true)) return
        inviteSubmit.isEnabled = false
        status.setText(R.string.wb_creator_sending_invite)
        val requestId = pendingRequestId
		trace("invite", "submit request=${requestId.takeLast(8)}")
        val body = JSONObject().put("inviteLink", candidate).toString()
        executor.execute {
            val response = post("/api/wb-creator/commands/$requestId/invite", body, deviceSecret, creatorId)
            mainHandler.post {
				trace("invite", "http=${response.code}${response.transportError.takeIf { it.isNotEmpty() }?.let { " error=$it" }.orEmpty()}")
                requestRunning.set(false)
                inviteSubmit.isEnabled = true
                if (response.code in 200..299) {
                    pendingRequestId = ""
                    invitePanel.visibility = View.GONE
                    status.setText(R.string.wb_creator_invite_sent)
                } else {
                    status.text = response.message.ifBlank { getString(R.string.wb_creator_invite_failed) }
                }
            }
        }
    }

    private fun post(path: String, body: String, secret: String, id: String): ApiResponse {
        return runCatching {
            val connection = URL(serverOrigin + path).openConnection() as HttpURLConnection
            connection.requestMethod = "POST"
            connection.connectTimeout = 15_000
            connection.readTimeout = 25_000
            connection.setRequestProperty("Authorization", "Bearer $secret")
            if (id.isNotEmpty()) connection.setRequestProperty("X-WLB-Creator-ID", id)
            if (body.isNotEmpty()) {
                connection.doOutput = true
                connection.setRequestProperty("Content-Type", "application/json; charset=utf-8")
                connection.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            }
            val code = connection.responseCode
            val responseBody = (if (code >= 400) connection.errorStream else connection.inputStream)
                ?.bufferedReader()?.use { it.readText().take(4096) }.orEmpty()
            val message = runCatching { JSONObject(responseBody).optString("error") }.getOrDefault("").take(200)
            connection.disconnect()
			ApiResponse(code, responseBody, message, "")
		}.getOrElse { ApiResponse(0, "", "", it.javaClass.simpleName.take(48)) }
    }

	private fun trace(step: String, detail: String) {
		val line = "$step · $detail"
		if (diagnosticLines.lastOrNull() == line) return
		diagnosticLines.addLast(line)
		while (diagnosticLines.size > MAX_DIAGNOSTIC_LINES) diagnosticLines.removeFirst()
		if (::diagnostics.isInitialized) diagnostics.text = diagnosticLines.joinToString("\n")
		Log.i("WB_CREATOR", line)
	}

    private fun fail(message: String) {
        mainHandler.removeCallbacks(commandPoll)
        invitePanel.visibility = View.GONE
        status.text = message
    }

    override fun onDestroy() {
        destroyed = true
        mainHandler.removeCallbacksAndMessages(null)
        executor.shutdownNow()
        if (::webView.isInitialized) {
            CookieManager.getInstance().flush()
            webView.stopLoading()
            webView.loadUrl("about:blank")
            webView.destroy()
        }
        super.onDestroy()
    }

    companion object {
        private const val WB_LOGIN_URL = "https://stream.wb.ru/login"
        private const val POLL_INTERVAL_MS = 3_000L
		private const val MAX_DIAGNOSTIC_LINES = 7
        private const val PREFS = "wb_creator"
        private const val KEY_SERVER = "server"
        private const val KEY_CREATOR_ID = "creator_id"
        private const val KEY_DEVICE_SECRET = "device_secret"
        private const val KEY_DEVICE_ID = "device_id"
        private val TOKEN_RE = Regex("^[A-Za-z0-9_-]{32,128}$")
        private val CREATOR_RE = Regex("^[A-Za-z0-9._-]{8,128}$")
        private val ROOM_RE = Regex("^[A-Za-z0-9_-]{3,256}$")

        private fun validPairing(server: String, token: String): Boolean {
            return validServer(server) && TOKEN_RE.matches(token)
        }

        private fun validServer(server: String): Boolean {
            val uri = runCatching { Uri.parse(server) }.getOrNull() ?: return false
            return uri.scheme == "https" && !uri.host.isNullOrBlank() && uri.userInfo == null &&
                (uri.path.isNullOrEmpty() || uri.path == "/") && uri.query == null && uri.fragment == null
        }

        fun hasBinding(context: android.content.Context): Boolean {
            val prefs = context.getSharedPreferences(PREFS, android.content.Context.MODE_PRIVATE)
            return validServer(prefs.getString(KEY_SERVER, "").orEmpty()) &&
                CREATOR_RE.matches(prefs.getString(KEY_CREATOR_ID, "").orEmpty()) &&
                TOKEN_RE.matches(prefs.getString(KEY_DEVICE_SECRET, "").orEmpty())
        }

        private fun isSafeInvite(value: String): Boolean {
            if (value.length !in 1..2048) return false
            val uri = runCatching { Uri.parse(value) }.getOrNull() ?: return false
            if (uri.userInfo != null || uri.port != -1 || uri.query != null || uri.fragment != null) return false
            val room = when {
                uri.scheme.equals("wbstream", true) && uri.path.isNullOrEmpty() -> uri.host.orEmpty()
                uri.scheme.equals("https", true) && uri.host.equals("stream.wb.ru", true) -> {
                    val parts = uri.path.orEmpty().trim('/').split('/')
                    if (parts.size == 2 && parts[0] == "room") parts[1] else ""
                }
                else -> ""
            }
            return ROOM_RE.matches(room)
        }
    }

	private data class ApiResponse(val code: Int, val body: String, val message: String, val transportError: String)
}
