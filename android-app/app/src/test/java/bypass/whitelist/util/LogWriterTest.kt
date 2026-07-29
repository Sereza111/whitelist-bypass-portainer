package bypass.whitelist.util

import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.file.Files

class LogWriterTest {
    @Test
    fun beginSessionRetainsPreviousCarrierLog() {
        val dir = Files.createTempDirectory("relay-log-test").toFile()
        val first = LogWriter(dir, 20)
        first.reset()
        first.append("WB carrier metrics")
        first.close()

        val second = LogWriter(dir, 20)
        second.beginSession()
        second.append("VK carrier metrics")
        second.close()

        val log = dir.resolve("relay.log").readText()
        assertTrue(log.contains("WB carrier metrics"))
        assertTrue(log.contains("NEW CARRIER SESSION"))
        assertTrue(log.contains("VK carrier metrics"))
    }
}
