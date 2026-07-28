package bypass.whitelist.recovery

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.util.Net
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuth
import java.io.IOException
import java.net.Authenticator
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.PasswordAuthentication
import java.net.Proxy
import java.net.URL

/**
 * Opens control-plane HTTPS outside this app's own VPN.
 *
 * Creator polling and profile recovery must remain reachable when the current
 * call carrier is dead. Sending those requests through the tunnel creates a
 * dependency cycle: a fresh invitation cannot be requested until the broken
 * invitation works again.
 */
object ManagerNetwork {

	private val proxyLock = Any()

	fun routeHint(context: Context): String =
		if (TunnelServiceState.isAnyTunnelComponentRunning(context)) "local-socks-first" else "physical"

	fun <T> execute(context: Context, url: URL, request: (HttpURLConnection) -> T): T {
		if (TunnelServiceState.isAnyTunnelComponentRunning(context)) {
			try {
				return synchronized(proxyLock) {
					val previous = Authenticator.getDefault()
					try {
						Authenticator.setDefault(object : Authenticator() {
							override fun getPasswordAuthentication(): PasswordAuthentication? =
								if (requestingProtocol?.startsWith("SOCKS", ignoreCase = true) == true) {
									PasswordAuthentication(SocksAuth.user, SocksAuth.pass.toCharArray())
								} else null
						})
						val proxy = Proxy(
							Proxy.Type.SOCKS,
							InetSocketAddress(Net.LOCALHOST, Prefs.socksPort.toInt()),
						)
						request(url.openConnection(proxy) as HttpURLConnection)
					} finally {
						Authenticator.setDefault(previous)
					}
				}
			} catch (_: IOException) {
				// The saved carrier may still be starting or may have just ended.
				// Fall back to the physical path for ordinary, non-whitelist networks.
			} catch (_: SecurityException) {
			}
		}
		return request(openPhysical(context, url))
	}

	private fun openPhysical(context: Context, url: URL): HttpURLConnection {
		val physical = selectPhysicalNetwork(context)
		val connection = physical?.openConnection(url) ?: url.openConnection()
		return connection as HttpURLConnection
	}

	private fun selectPhysicalNetwork(context: Context): Network? = runCatching {
		val manager = context.applicationContext.getSystemService(ConnectivityManager::class.java)
			?: return@runCatching null
		manager.allNetworks
			.mapNotNull { network ->
				val capabilities = manager.getNetworkCapabilities(network) ?: return@mapNotNull null
				if (!capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) ||
					capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
				) return@mapNotNull null
				val validated = capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
				network to validated
			}
			.sortedByDescending { (_, validated) -> if (validated) 1 else 0 }
			.firstOrNull()
			?.first
	}.getOrNull()
}
