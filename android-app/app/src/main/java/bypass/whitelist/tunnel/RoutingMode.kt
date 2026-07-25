package bypass.whitelist.tunnel

/** User-facing traffic capture mode. */
enum class RoutingMode {
    DEVICE,
    APPS,
    SOCKS5;

    companion object {
        fun fromLegacy(proxyOnly: Boolean, splitMode: SplitTunnelingMode): RoutingMode = when {
            proxyOnly -> SOCKS5
            splitMode == SplitTunnelingMode.ONLY -> APPS
            else -> DEVICE
        }
    }
}
