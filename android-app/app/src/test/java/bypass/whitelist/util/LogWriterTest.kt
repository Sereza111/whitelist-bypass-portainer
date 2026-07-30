package bypass.whitelist.util

import org.junit.Assert.assertEquals
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

    @Test
    fun displayPreviewIsBoundedWithoutTruncatingCurrentFile() {
        val dir = Files.createTempDirectory("relay-log-preview-test").toFile()
        val writer = LogWriter(dir, maxDisplayLines = 3, maxRetainedFileLines = 20)
        writer.reset()
        repeat(8) { writer.append("line-$it") }

        val preview = writer.displayLines()
        val file = writer.file.readLines()
        writer.close()

        assertEquals(3, preview.size)
        assertTrue(preview.first().endsWith("line-5"))
        assertEquals(8, file.size)
        assertTrue(file.first().endsWith("line-0"))
    }
}
