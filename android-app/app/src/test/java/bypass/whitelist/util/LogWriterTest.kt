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

    @Test
    fun newSessionKeepsOnlySmallPreviousTail() {
        val dir = Files.createTempDirectory("relay-log-session-budget-test").toFile()
        val writer = LogWriter(
            dir,
            maxDisplayLines = 10,
            maxRetainedFileLines = 20,
            previousSessionLines = 3,
        )
        writer.reset()
        repeat(12) { writer.append("old-$it") }
        writer.beginSession()
        writer.append("current-metrics")
        writer.close()

        val lines = dir.resolve("relay.log").readLines()
        assertEquals(5, lines.size)
        assertTrue(lines.first().endsWith("old-9"))
        assertTrue(lines.last().endsWith("current-metrics"))
    }

    @Test
    fun shareSnapshotDoesNotChangeAfterMoreLogsArrive() {
        val dir = Files.createTempDirectory("relay-log-snapshot-test").toFile()
        val writer = LogWriter(dir, maxRetainedFileLines = 20)
        writer.reset()
        writer.append("before-share")
        val snapshot = writer.createShareSnapshot()
        writer.append("after-share")
        writer.close()

        assertTrue(snapshot.readText().contains("before-share"))
        assertTrue(!snapshot.readText().contains("after-share"))
        assertTrue(dir.resolve("relay.log").readText().contains("after-share"))
    }

    @Test
    fun currentSessionRollsWithoutLosingHeaderOrNewestMetrics() {
        val dir = Files.createTempDirectory("relay-log-roll-test").toFile()
        val writer = LogWriter(
            dir,
            maxDisplayLines = 5,
            maxRetainedFileLines = 50,
            previousSessionLines = 4,
        )
        writer.reset()
        repeat(10) { writer.append("previous-$it") }
        writer.beginSession()
        repeat(80) { writer.append("current-$it") }
        writer.close()

        val lines = dir.resolve("relay.log").readLines()
        assertTrue(lines.size <= 50)
        assertTrue(lines.any { it.contains("NEW CARRIER SESSION") })
        assertTrue(lines.last().endsWith("current-79"))
        assertTrue(lines.none { it.contains("previous-") })
    }
}
