package bypass.whitelist.recovery

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.os.Build
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import android.util.Base64
import androidx.core.app.NotificationCompat
import bypass.whitelist.MainActivity
import bypass.whitelist.R
import bypass.whitelist.util.Prefs
import org.json.JSONObject
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import kotlin.math.abs

class RecoveryNotificationListener : NotificationListenerService() {

    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        val notification = sbn?.notification ?: return
        val text = buildString {
            notification.extras.keySet().forEach { key ->
				when (val value = notification.extras.get(key)) {
					is CharSequence -> appendLine(value)
					is Array<*> -> value.filterIsInstance<CharSequence>().forEach { appendLine(it) }
					is Iterable<*> -> value.filterIsInstance<CharSequence>().forEach { appendLine(it) }
				}
            }
        }
		val update = verifyCompact(text) ?: LEGACY_TOKEN.find(text)?.value?.let(::verifyLegacy) ?: return
        val destinations = Prefs.savedDestinations.toMutableList()
        val index = destinations.indexOfFirst { it.recoveryProfile == update.profile }
        if (index < 0) return
        val current = destinations[index]
        if (update.generation <= current.recoveryGeneration) return
		destinations[index] = current.copy(
			url = update.link,
			recoveryGeneration = update.generation,
			recoveryPending = true,
		)
        Prefs.savedDestinations = destinations

        sendBroadcast(Intent(ACTION_RECOVERY_UPDATE).setPackage(packageName).apply {
            putExtra(EXTRA_DESTINATION_ID, current.id)
        })
        showRecoveryNotification(current.id, update.generation)
    }

	private fun verifyCompact(text: String): RecoveryUpdate? {
		val match = COMPACT_TOKEN.find(text) ?: return null
		val profile = match.groupValues[1]
		val generation = match.groupValues[2].toIntOrNull()?.takeIf { it > 0 } ?: return null
		val issuedAt = match.groupValues[3].toLongOrNull() ?: return null
		if (!isRecent(issuedAt)) return null
		val link = LINK.findAll(text)
			.map { it.value.trimEnd('.', ',', ';', ')', ']', '}') }
			.firstOrNull { it.contains("/call/") } ?: return null
		if (!isValidLink(link)) return null
		val config = Prefs.savedDestinations.firstOrNull { it.recoveryProfile == profile } ?: return null
		val key = config.recoveryKey?.takeIf { it.length in 16..256 } ?: return null
		val signature = decodeSignature(match.groupValues[4]) ?: return null
		val signed = listOf(profile, generation.toString(), issuedAt.toString(), link).joinToString("\n")
		if (!validSignature(key, signed, signature)) return null
		return RecoveryUpdate(profile, generation, link)
	}

    private fun verifyLegacy(token: String): RecoveryUpdate? {
        val parts = token.split('.')
        if (parts.size != 3 || parts[0] != "WLB1") return null
        val encoded = parts[1]
        val signature = runCatching { Base64.decode(parts[2], Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING) }.getOrNull() ?: return null
        val payloadBytes = runCatching { Base64.decode(encoded, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING) }.getOrNull() ?: return null
        val payload = runCatching { JSONObject(String(payloadBytes, Charsets.UTF_8)) }.getOrNull() ?: return null
		if (payload.optInt("v") != 1 || payload.optString("provider") != "vk") return null
        val profile = payload.optString("profile")
		if (profile.isBlank() || profile.length > 128) return null
        val config = Prefs.savedDestinations.firstOrNull { it.recoveryProfile == profile } ?: return null
        val key = config.recoveryKey ?: return null
		if (key.length !in 16..256) return null
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key.toByteArray(Charsets.UTF_8), "HmacSHA256"))
		if (!java.security.MessageDigest.isEqual(mac.doFinal(encoded.toByteArray(Charsets.UTF_8)), signature)) return null
        val issuedAt = payload.optLong("issuedAt")
		if (!isRecent(issuedAt)) return null
        val link = payload.optString("link").trim()
		if (!isValidLink(link)) return null
		val generation = payload.optInt("generation")
		if (generation < 1) return null
		return RecoveryUpdate(profile, generation, link)
    }

	private fun decodeSignature(encoded: String): ByteArray? = runCatching {
		Base64.decode(encoded, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
	}.getOrNull()

	private fun validSignature(key: String, signed: String, signature: ByteArray): Boolean {
		val mac = Mac.getInstance("HmacSHA256")
		mac.init(SecretKeySpec(key.toByteArray(Charsets.UTF_8), "HmacSHA256"))
		return java.security.MessageDigest.isEqual(mac.doFinal(signed.toByteArray(Charsets.UTF_8)), signature)
	}

	private fun isRecent(issuedAt: Long): Boolean =
		abs(System.currentTimeMillis() / 1000L - issuedAt) <= MAX_MESSAGE_AGE_SECONDS

	private fun isValidLink(link: String): Boolean =
		link.startsWith("https://") && link.length <= 2048

    private fun showRecoveryNotification(destinationId: String, generation: Int) {
		val manager = getSystemService(NotificationManager::class.java) ?: return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            manager.createNotificationChannel(NotificationChannel(
                CHANNEL_ID, getString(R.string.recovery_notification_title), NotificationManager.IMPORTANCE_HIGH,
            ))
        }
        val intent = Intent(this, MainActivity::class.java).apply {
            action = MainActivity.ACTION_RECOVERY_UPDATE
            putExtra(EXTRA_DESTINATION_ID, destinationId)
            addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        }
        val pending = PendingIntent.getActivity(this, generation, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
		runCatching {
			manager.notify(NOTIFICATION_ID, NotificationCompat.Builder(this, CHANNEL_ID)
				.setSmallIcon(R.drawable.ic_power)
				.setContentTitle(getString(R.string.recovery_notification_title))
				.setContentText(getString(R.string.recovery_notification_text))
				.setContentIntent(pending)
				.setAutoCancel(true)
				.build())
		}
    }

    private data class RecoveryUpdate(val profile: String, val generation: Int, val link: String)

    companion object {
        const val ACTION_RECOVERY_UPDATE = "bypass.whitelist.RECOVERY_UPDATE"
        const val EXTRA_DESTINATION_ID = "destination_id"
        private const val CHANNEL_ID = "wlb_recovery"
        private const val NOTIFICATION_ID = 9412
        private const val MAX_MESSAGE_AGE_SECONDS = 24L * 60L * 60L
		private val COMPACT_TOKEN = Regex("WLB2\\.([A-Za-z0-9_-]{1,128})\\.([0-9]{1,10})\\.([0-9]{10})\\.([A-Za-z0-9_-]{43})")
		private val LEGACY_TOKEN = Regex("WLB1\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+")
		private val LINK = Regex("https://[^\\s]+")
    }
}
