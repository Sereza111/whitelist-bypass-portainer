package bypass.whitelist.recovery

import android.content.Context
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
						Authenticator.setDefault(null)
					}
				}
			} catch (_: IOException) {
				// The saved carrier may still be starting or may have just ended.
				// Fall back to the physical path for ordinary, non-whitelist networks.
			} catch (_: SecurityException) {
			}
		}
		return request(openSystem(url))
	}

	private fun openSystem(url: URL): HttpURLConnection = url.openConnection() as HttpURLConnection
}
