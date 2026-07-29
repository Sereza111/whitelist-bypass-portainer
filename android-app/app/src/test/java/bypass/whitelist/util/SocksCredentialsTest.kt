package bypass.whitelist.util

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SocksCredentialsTest {
    @Test
    fun bothEmptyMeansNoAuthentication() {
        val credentials = SocksCredentials.manual("", "")
        assertTrue(credentials.isComplete)
        assertFalse(credentials.requiresAuthentication)
        assertEquals("", credentials.user)
        assertEquals("", credentials.pass)
    }

    @Test
    fun bothFieldsEnableAuthentication() {
        val credentials = SocksCredentials.manual(" user ", "pass")
        assertTrue(credentials.isComplete)
        assertTrue(credentials.requiresAuthentication)
        assertEquals("user", credentials.user)
    }

    @Test
    fun partialCredentialsAreRejected() {
        assertFalse(SocksCredentials.manual("user", "").isComplete)
        assertFalse(SocksCredentials.manual("", "pass").isComplete)
    }
}
