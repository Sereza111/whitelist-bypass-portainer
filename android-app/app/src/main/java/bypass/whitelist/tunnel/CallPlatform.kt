package bypass.whitelist.tunnel

import java.net.URI

enum class CallPlatform(val id: String, val urlMarker: String) {
    VK("vk", ""),
    TELEMOST("telemost", "telemost"),
    WBSTREAM("wbstream", "wbstream://"),
    DION("dion", "dion://");

    companion object {
		fun fromId(id: String): CallPlatform? = entries.firstOrNull { it.id == id.lowercase().trim() }

        fun fromUrl(url: String): CallPlatform {
			val normalized = url.lowercase()
			return when {
            normalized.contains(DION.urlMarker) || normalized.contains("dion.vc/event/") -> DION
            normalized.contains(WBSTREAM.urlMarker) || normalized.contains("stream.wb.ru/room/") -> WBSTREAM
            normalized.contains(TELEMOST.urlMarker) -> TELEMOST
            else -> VK
			}
        }

		fun isSafeInviteLink(platform: CallPlatform, value: String): Boolean {
			if (value.length !in 1..2048) return false
			val parsed = runCatching { URI(value) }.getOrNull() ?: return false
			if (parsed.userInfo != null || parsed.port != -1) return false
			val scheme = parsed.scheme?.lowercase() ?: return false
			val host = parsed.host?.lowercase().orEmpty()
			return when (platform) {
				VK -> scheme == "https" && (host == "vk.com" || host.endsWith(".vk.com")) &&
					parsed.rawPath.orEmpty().startsWith("/call")
				TELEMOST -> scheme == "https" && host == "telemost.yandex.ru" &&
					parsed.rawPath.orEmpty().startsWith("/j/")
				WBSTREAM -> when {
					scheme == "wbstream" -> parsed.rawPath.orEmpty().isEmpty() && parsed.rawQuery == null &&
						parsed.rawFragment == null && safeOpaqueId(host)
					scheme == "https" && host == "stream.wb.ru" && parsed.rawQuery == null && parsed.rawFragment == null -> {
						val parts = parsed.rawPath.orEmpty().trim('/').split('/')
						parts.size == 2 && parts[0] == "room" && safeOpaqueId(parts[1])
					}
					else -> false
				}
				DION -> when {
					scheme == "dion" -> parsed.rawPath.orEmpty().isEmpty() && parsed.rawQuery == null &&
						parsed.rawFragment == null && safeOpaqueId(host)
					scheme == "https" && host == "dion.vc" && parsed.rawPath.orEmpty().startsWith("/event/") ->
						safeOpaqueId(parsed.rawPath.orEmpty().removePrefix("/event/").trim('/'))
					else -> false
				}
			}
		}

		private fun safeOpaqueId(value: String): Boolean = value.length in 3..256 &&
			value.all { it.isLetterOrDigit() || it == '-' || it == '_' }

        fun extractRoomId(url: String): String {
            val trimmed = url.trim()
            if (trimmed.startsWith(WBSTREAM.urlMarker)) return trimmed.removePrefix(WBSTREAM.urlMarker).trim()
			if (fromUrl(trimmed) == WBSTREAM) {
				val parsed = runCatching { URI(trimmed) }.getOrNull()
				val parts = parsed?.path.orEmpty().trim('/').split('/')
				if (parsed?.host?.equals("stream.wb.ru", ignoreCase = true) == true &&
					parts.size >= 2 && parts[0] == "room" && safeOpaqueId(parts[1])) {
					return parts[1]
				}
			}
            if (trimmed.startsWith(DION.urlMarker)) return trimmed.removePrefix(DION.urlMarker).trim()
            val dionPrefix = "dion.vc/event/"
            val idx = trimmed.indexOf(dionPrefix)
            if (idx >= 0) {
                var slug = trimmed.substring(idx + dionPrefix.length)
                val q = slug.indexOf('?')
                if (q >= 0) slug = slug.substring(0, q)
                val s = slug.indexOf('/')
                if (s >= 0) slug = slug.substring(0, s)
                return slug.trim()
            }
            return trimmed
        }
    }
}
