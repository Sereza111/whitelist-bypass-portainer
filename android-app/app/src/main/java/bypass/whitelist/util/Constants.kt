package bypass.whitelist.util

import java.security.SecureRandom

object Net {
    const val LOCALHOST = "127.0.0.1"
    const val ANY_IPV4 = "0.0.0.0"
}

object Ports {
    const val DEFAULT_SOCKS = 1080L
    const val DC_WS = 9000L
    const val PION_SIGNALING = 9001L
}

enum class SocksAuthMode { AUTO, MANUAL }

object SocksAuth {
	private fun randomString(length: Int): String {
		val random = SecureRandom()
		val chars = "abcdefghijklmnopqrstuvwxyz0123456789"
		return buildString {
			repeat(length) { append(chars[random.nextInt(chars.length)]) }
		}
	}

	@Synchronized
	private fun ensureAutoCredentials(): Pair<String, String> {
		var user = Prefs.socksAutoUser
		var pass = Prefs.socksAutoPass
		if (user.isBlank()) {
			user = randomString(16)
			Prefs.socksAutoUser = user
		}
		if (pass.isBlank()) {
			pass = randomString(24)
			Prefs.socksAutoPass = pass
		}
		return user to pass
	}

	val generatedUser: String get() = ensureAutoCredentials().first
	val generatedPass: String get() = ensureAutoCredentials().second

	val user: String
		get() = if (Prefs.socksAuthMode == SocksAuthMode.MANUAL &&
			Prefs.socksUser.isNotBlank() && Prefs.socksPass.isNotBlank()
		) Prefs.socksUser else generatedUser

	val pass: String
		get() = if (Prefs.socksAuthMode == SocksAuthMode.MANUAL &&
			Prefs.socksUser.isNotBlank() && Prefs.socksPass.isNotBlank()
		) Prefs.socksPass else generatedPass
}

enum class DnsMode(val label: String) {
    SYSTEM("System"),
    CUSTOM("Custom"),
}

enum class ThemeMode(val label: String) {
    SYSTEM("System"),
    LIGHT("Light"),
    DARK("Dark"),
}

object PrefsKeys {
    const val CONNECT_ON_START = "connect_on_start"
    const val TUNNEL_MODE = "tunnel_mode"
    const val SPLIT_TUNNELING_MODE = "split_tunneling_mode"
    const val SPLIT_TUNNELING_PACKAGES = "split_tunneling_packages"
    const val AUTOFILL_ENABLED = "autofill_enabled"
    const val AUTOFILL_NAME = "autofill_name"
    const val HEADLESS = "headless"
    const val SOCKS_HOST = "socks_host"
    const val SOCKS_PORT = "socks_port"
    const val SOCKS_AUTH_MODE = "socks_auth_mode"
    const val SOCKS_USER = "socks_user"
	const val SOCKS_PASS = "socks_pass"
	const val SOCKS_AUTO_USER = "socks_auto_user"
	const val SOCKS_AUTO_PASS = "socks_auto_pass"
	const val ALLOW_LAN = "allow_lan"
    const val PROXY_ONLY = "proxy_only"
    const val DNS_MODE = "dns_mode"
    const val DNS_PRIMARY = "dns_primary"
    const val DNS_SECONDARY = "dns_secondary"
    const val VP8_FPS = "vp8_fps"
    const val VP8_BATCH = "vp8_batch"
    const val DUAL_TRACK = "dual_track"
    const val SAVED_DESTINATIONS = "saved_destinations"
    const val ACTIVE_DESTINATION_ID = "active_destination_id"
    const val THEME_MODE = "theme_mode"
}

object VP8Defaults {
    const val FPS = 24
    const val BATCH = 30
}

const val BLANK_URL = "about:blank"

const val DESKTOP_USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

object Vpn {
    const val ADDRESS = "10.0.0.2"
    const val PREFIX_LENGTH = 32
    const val ROUTE = "0.0.0.0"
    const val MTU = 1500
    const val DNS_PRIMARY = "8.8.8.8"
    const val DNS_SECONDARY = "8.8.4.4"
    const val SESSION_NAME = "WhitelistBypass"
}
