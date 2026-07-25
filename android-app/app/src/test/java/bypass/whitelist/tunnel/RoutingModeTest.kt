package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class RoutingModeTest {
    @Test
    fun legacyPreferencesMigrateWithoutRewriting() {
        assertEquals(RoutingMode.SOCKS5, RoutingMode.fromLegacy(true, SplitTunnelingMode.NONE))
        assertEquals(RoutingMode.SOCKS5, RoutingMode.fromLegacy(true, SplitTunnelingMode.ONLY))
        assertEquals(RoutingMode.APPS, RoutingMode.fromLegacy(false, SplitTunnelingMode.ONLY))
        assertEquals(RoutingMode.DEVICE, RoutingMode.fromLegacy(false, SplitTunnelingMode.NONE))
        assertEquals(RoutingMode.DEVICE, RoutingMode.fromLegacy(false, SplitTunnelingMode.BYPASS))
    }
}
