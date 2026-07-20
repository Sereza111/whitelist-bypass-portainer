package bypass.whitelist.util

import java.net.Inet4Address
import java.net.NetworkInterface
import java.util.Collections

object LanProxy {
    private val preferredPrefixes = listOf("wlan", "ap", "swlan", "eth", "rndis")
    private val rejectedPrefixes = listOf("lo", "tun", "wg", "ppp", "rmnet", "clat")

    fun ipv4Addresses(): List<String> {
        val candidates = runCatching {
            Collections.list(NetworkInterface.getNetworkInterfaces()).flatMap { network ->
                if (!runCatching { network.isUp && !network.isLoopback }.getOrDefault(false)) {
                    emptyList()
                } else {
                    val name = network.name.lowercase()
                    if (rejectedPrefixes.any(name::startsWith)) {
                        emptyList()
                    } else {
                        Collections.list(network.inetAddresses)
                            .filterIsInstance<Inet4Address>()
                            .filter { !it.isLoopbackAddress && !it.isLinkLocalAddress && it.isSiteLocalAddress }
                            .map { Triple(interfacePriority(name), name, it.hostAddress ?: "") }
                    }
                }
            }
        }.getOrDefault(emptyList())

        return candidates
            .filter { it.third.isNotBlank() }
            .sortedWith(compareBy<Triple<Int, String, String>> { it.first }.thenBy { it.second }.thenBy { it.third })
            .map { it.third }
            .distinct()
    }

    fun endpoints(port: Long): List<String> = ipv4Addresses().map { "$it:$port" }

    private fun interfacePriority(name: String): Int {
        val index = preferredPrefixes.indexOfFirst(name::startsWith)
        return if (index >= 0) index else preferredPrefixes.size
    }
}
