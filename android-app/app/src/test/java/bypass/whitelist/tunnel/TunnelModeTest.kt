package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class TunnelModeTest {

    @Test
    fun smartIsWbSpecificAndOtherProvidersKeepVideo() {
        assertEquals(TunnelMode.SMART, TunnelMode.SMART.forPlatform(CallPlatform.WBSTREAM))
        assertEquals(TunnelMode.VIDEO, TunnelMode.SMART.forPlatform(CallPlatform.VK))
        assertEquals(TunnelMode.VIDEO, TunnelMode.SMART.forPlatform(CallPlatform.TELEMOST))
        assertEquals(TunnelMode.VIDEO, TunnelMode.SMART.forPlatform(CallPlatform.DION))
    }

    @Test
    fun manualDcRemainsAvailableWhereSupported() {
        assertEquals(TunnelMode.DC, TunnelMode.DC.forPlatform(CallPlatform.WBSTREAM))
        assertEquals(TunnelMode.DC, TunnelMode.DC.forPlatform(CallPlatform.VK))
        assertEquals(TunnelMode.VIDEO, TunnelMode.DC.forPlatform(CallPlatform.TELEMOST))
        assertEquals(TunnelMode.VIDEO, TunnelMode.DC.forPlatform(CallPlatform.DION))
    }
}
