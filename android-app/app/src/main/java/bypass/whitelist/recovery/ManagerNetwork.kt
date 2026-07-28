package bypass.whitelist.recovery

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import java.net.HttpURLConnection
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

	fun open(context: Context, url: URL): HttpURLConnection {
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
