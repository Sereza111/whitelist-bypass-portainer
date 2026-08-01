package bypass.whitelist.util

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

class DnsDefaultsTest {
    @Test
    fun automaticDnsUsesDistinctRemoteSafeResolvers() {
        assertEquals("Automatic (remote-safe)", DnsMode.SYSTEM.label)
        assertEquals("1.1.1.1", Vpn.DNS_PRIMARY)
        assertEquals("8.8.8.8", Vpn.DNS_SECONDARY)
        assertNotEquals(Vpn.DNS_PRIMARY, Vpn.DNS_SECONDARY)
    }
}
