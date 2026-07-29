package bypass.whitelist.tunnel

import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

data class CallConfig(
    val id: String,
    val name: String,
    val url: String,
    val tunnelMode: TunnelMode? = null,
	val tunnelModeExplicit: Boolean = false,
    val vp8Fps: Int? = null,
    val vp8Batch: Int? = null,
    val dualTrack: Boolean? = null,
	val recoveryProfile: String? = null,
	val recoveryKey: String? = null,
	val recoveryGeneration: Int = 0,
	val recoveryPending: Boolean = false,
	val recoverySyncUrl: String? = null,
) {
    val platform: CallPlatform get() = CallPlatform.fromUrl(url)

	fun migrateManagedWbTransportDefault(): CallConfig {
		if (platform != CallPlatform.WBSTREAM || recoveryProfile.isNullOrBlank() || tunnelModeExplicit) return this
		// Field tests show WB's reliable DataChannel opens but remains one-way.
		// Unmarked Smart values came from the old automatic default, so migrate
		// those to the proven Video path. Explicit user A/B choices are preserved.
		return if (tunnelMode == TunnelMode.SMART) copy(tunnelMode = TunnelMode.VIDEO) else this
	}

    val platformGlyph: String get() = when (platform) {
        CallPlatform.VK -> "VK"
        CallPlatform.TELEMOST -> "TM"
        CallPlatform.WBSTREAM -> "WB"
        CallPlatform.DION -> "DN"
    }

    val platformLabel: String get() = when (platform) {
        CallPlatform.VK -> "VK"
        CallPlatform.TELEMOST -> "Telemost"
        CallPlatform.WBSTREAM -> "WB Stream"
        CallPlatform.DION -> "DION"
    }

    fun toJson(): JSONObject = JSONObject().apply {
        put("id", id)
        put("name", name)
        put("url", url)
        tunnelMode?.let { put("tunnelMode", it.name) }
		if (tunnelModeExplicit) put("tunnelModeExplicit", true)
        vp8Fps?.let { put("vp8Fps", it) }
        vp8Batch?.let { put("vp8Batch", it) }
        dualTrack?.let { put("dualTrack", it) }
		recoveryProfile?.let { put("recoveryProfile", it) }
		recoveryKey?.let { put("recoveryKey", it) }
		put("recoveryGeneration", recoveryGeneration)
		put("recoveryPending", recoveryPending)
		recoverySyncUrl?.let { put("recoverySyncUrl", it) }
    }

    companion object {
		fun selectVKControlBootstrap(items: List<CallConfig>, activeID: String): CallConfig? {
			val candidates = items.filter {
				it.platform == CallPlatform.VK && CallPlatform.isSafeInviteLink(CallPlatform.VK, it.url)
			}
			return candidates.firstOrNull { it.id == activeID }
				?: candidates.firstOrNull { !it.recoveryProfile.isNullOrBlank() }
				?: candidates.firstOrNull()
		}

        fun newWith(name: String, url: String): CallConfig {
			val isWB = CallPlatform.fromUrl(url) == CallPlatform.WBSTREAM
			return CallConfig(
				id = UUID.randomUUID().toString(),
				name = name,
				url = url,
				tunnelMode = if (isWB) TunnelMode.VIDEO else null,
				dualTrack = if (isWB) true else null,
			)
		}

		fun managedInvite(
			existing: CallConfig?,
			name: String,
			url: String,
			profile: String,
			key: String,
			generation: Int,
			syncUrl: String?,
		): CallConfig {
			val platform = CallPlatform.fromUrl(url)
			val mode = when {
				existing?.tunnelModeExplicit == true -> existing.tunnelMode
				// DC was never an automatic WB default, so a legacy saved DC is
				// necessarily a user's A/B choice even before the explicit marker.
				existing?.tunnelMode == TunnelMode.DC -> TunnelMode.DC
				platform == CallPlatform.WBSTREAM -> TunnelMode.VIDEO
				else -> existing?.tunnelMode ?: TunnelMode.VIDEO
			}
			return (existing ?: newWith(name, url)).copy(
				name = name,
				url = url,
				// A refreshed Manager invite changes the room, not the user's
				// transport choice. In particular, do not silently turn WB DC
				// experiments back into Video on every creator handoff.
				// Smart used to be the unmarked automatic default. Current field
				// evidence proves its DC candidate is one-way, so new and unmarked
				// managed profiles use Video; explicit Smart/DC choices remain intact.
				tunnelMode = mode,
				dualTrack = existing?.dualTrack ?: (platform == CallPlatform.WBSTREAM),
				recoveryProfile = profile,
				recoveryKey = key,
				recoveryGeneration = generation,
				recoveryPending = false,
				recoverySyncUrl = syncUrl,
			)
		}

        fun fromJson(obj: JSONObject): CallConfig = CallConfig(
            id = obj.getString("id"),
            name = obj.getString("name"),
            url = obj.getString("url"),
            tunnelMode = if (obj.has("tunnelMode")) try { TunnelMode.valueOf(obj.getString("tunnelMode")) } catch(e: Exception) { null } else null,
			tunnelModeExplicit = obj.optBoolean("tunnelModeExplicit", false),
            vp8Fps = if (obj.has("vp8Fps")) obj.getInt("vp8Fps") else null,
            vp8Batch = if (obj.has("vp8Batch")) obj.getInt("vp8Batch") else null,
            dualTrack = if (obj.has("dualTrack")) obj.getBoolean("dualTrack") else null,
			recoveryProfile = obj.optString("recoveryProfile").takeIf { it.isNotBlank() },
			recoveryKey = obj.optString("recoveryKey").takeIf { it.isNotBlank() },
			recoveryGeneration = obj.optInt("recoveryGeneration", 0),
			recoveryPending = obj.optBoolean("recoveryPending", false),
			recoverySyncUrl = obj.optString("recoverySyncUrl").takeIf { it.isNotBlank() },
        )

        fun listToJson(items: List<CallConfig>): String {
            val arr = JSONArray()
            items.forEach { arr.put(it.toJson()) }
            return arr.toString()
        }

        fun listFromJson(raw: String): List<CallConfig> {
            if (raw.isBlank()) return emptyList()
            return try {
                val arr = JSONArray(raw)
                buildList(arr.length()) {
                    for (i in 0 until arr.length()) add(fromJson(arr.getJSONObject(i)))
                }
            } catch (_: Exception) {
                emptyList()
            }
        }

        fun suggestNameFor(url: String): String {
            val platform = CallPlatform.fromUrl(url)
            val label = when (platform) {
                CallPlatform.VK -> "VK call"
                CallPlatform.TELEMOST -> "Telemost"
                CallPlatform.WBSTREAM -> "WB Stream"
                CallPlatform.DION -> "DION"
            }
            return label
        }
    }
}
