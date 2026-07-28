package bypass.whitelist.recovery

import android.content.Context
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.CallPlatform
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

object ProfileSyncClient {

    data class Update(val generation: Int, val link: String)

    fun poll(context: Context, config: CallConfig): Update? {
        val syncUrl = config.recoverySyncUrl?.takeIf { it.startsWith("https://") } ?: return null
        val key = config.recoveryKey?.takeIf { it.length in 24..256 } ?: return null
        val profile = config.recoveryProfile?.takeIf { it.length in 8..128 } ?: return null
        val expectedSuffix = "/api/client-profiles/$profile/invite"
        val parsed = runCatching { java.net.URI(syncUrl) }.getOrNull() ?: return null
        if (parsed.scheme != "https" || parsed.host.isNullOrBlank() || parsed.userInfo != null ||
            parsed.rawQuery != null || parsed.rawFragment != null || parsed.rawPath != expectedSuffix ||
            (parsed.port != -1 && parsed.port != 443)
        ) return null

        return runCatching {
			ManagerNetwork.execute(context, URL(syncUrl)) { connection ->
				connection.requestMethod = "POST"
				connection.connectTimeout = 10_000
				connection.readTimeout = 15_000
				connection.doOutput = true
				connection.setRequestProperty("Authorization", "Bearer $key")
				connection.setRequestProperty("Content-Type", "application/json; charset=utf-8")
				connection.setRequestProperty("Cache-Control", "no-cache")
				val payloadBody = JSONObject().put("afterGeneration", config.recoveryGeneration).toString()
				connection.outputStream.use { it.write(payloadBody.toByteArray(Charsets.UTF_8)) }
				val code = connection.responseCode
				if (code != HttpURLConnection.HTTP_OK) {
					connection.disconnect()
					return@execute null
				}
				val responseBody = connection.inputStream.bufferedReader().use { it.readText().take(4096) }
				connection.disconnect()
				val payload = JSONObject(responseBody)
				val provider = payload.optString("provider")
				val generation = payload.optInt("generation", -1)
				val link = payload.optString("link").trim()
				val platform = CallPlatform.fromId(provider) ?: return@execute null
				if (platform != config.platform || generation <= config.recoveryGeneration ||
					!CallPlatform.isSafeInviteLink(platform, link)
				) return@execute null
				Update(generation, link)
			}
        }.getOrNull()
    }
}
