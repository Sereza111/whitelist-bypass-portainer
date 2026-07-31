package bypass.whitelist.util

import java.io.File
import java.io.FileWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class LogWriter(
    private val cacheDir: File,
    private val maxDisplayLines: Int = 300,
    private val maxRetainedFileLines: Int = 6000,
    private val previousSessionLines: Int = 250,
) {

    private val logFile = File(cacheDir, "relay.log")
    private var writer: FileWriter? = null
    private var fileLineCount: Int = 0
    private val displayLines = ArrayDeque<String>()
    private val dateFormat = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)
    private var revisionCounter: Long = 0L

    val file: File get() = logFile

    @Synchronized
    fun reset() {
        writer?.close()
        writer = FileWriter(logFile, false)
        fileLineCount = 0
        displayLines.clear()
        revisionCounter++
    }

    @Synchronized
    fun beginSession() {
        writer?.close()
        val retained = if (logFile.exists()) {
            logFile.readLines().takeLast(
                minOf(previousSessionLines, maxRetainedFileLines - 1).coerceAtLeast(0)
            )
        } else emptyList()
        displayLines.clear()
        retained.takeLast((maxDisplayLines - 1).coerceAtLeast(0)).forEach { displayLines.addLast(it) }
        writer = FileWriter(logFile, false)
        retained.forEach { writer?.write("$it\n") }
        fileLineCount = retained.size
        writer?.flush()
        append(SESSION_MARKER)
    }

    @Synchronized
    fun append(msg: String) {
        val ts = dateFormat.format(Date())
        val line = "$ts $msg"
        writer?.apply { write("$line\n"); flush() }
        if (writer != null) {
            fileLineCount++
            compactCurrentSessionIfNeeded()
        }
        displayLines.addLast(line)
        if (displayLines.size > maxDisplayLines) displayLines.removeFirst()
        revisionCounter++
    }

    @Synchronized
    fun revision(): Long = revisionCounter

    @Synchronized
    fun displayLines(): List<String> = displayLines.toList()

    @Synchronized
    fun displayText(): String = displayLines.joinToString("\n")

    /**
     * Returns a stable point-in-time copy. Sharing relay.log directly lets the
     * receiving app copy it while the tunnel is still appending, which can
     * produce an export that ends before the first METRICS interval.
     */
    @Synchronized
    fun createShareSnapshot(): File {
        writer?.flush()
        val snapshot = File.createTempFile("relay-export-$revisionCounter-", ".log", cacheDir)
        if (logFile.exists()) {
            logFile.copyTo(snapshot, overwrite = true)
        } else {
            snapshot.writeText(displayText())
        }
        cacheDir.listFiles { file ->
            file.isFile && file.name.startsWith(EXPORT_PREFIX) && file.extension == "log"
        }?.filter { it != snapshot }
            ?.sortedByDescending { it.lastModified() }
            ?.drop((MAX_EXPORT_SNAPSHOTS - 1).coerceAtLeast(0))
            ?.forEach { it.delete() }
        return snapshot
    }

    @Synchronized
    fun snapshotText(): String {
        writer?.flush()
        return if (logFile.exists()) logFile.readText() else displayText()
    }

    @Synchronized
    fun close() {
        writer?.close()
        writer = null
    }

    private fun compactCurrentSessionIfNeeded() {
        if (fileLineCount <= maxRetainedFileLines || maxRetainedFileLines <= 0) return
        writer?.close()
        val lines = logFile.readLines()
        val sessionStart = lines.indexOfLast { it.contains(SESSION_MARKER) }.coerceAtLeast(0)
        val session = lines.drop(sessionStart)
        // Compact below the hard limit so normal appends do not rewrite the
        // whole file on every line once a long diagnostic session fills it.
        val targetCount = (maxRetainedFileLines * COMPACT_TARGET_PERCENT / 100)
            .coerceAtLeast(2)
            .coerceAtMost(maxRetainedFileLines)
        val prefixCount = minOf(SESSION_PREFIX_LINES, session.size, (targetCount / 4).coerceAtLeast(1))
        val tailCount = (targetCount - prefixCount).coerceAtLeast(0)
        val kept = if (session.size <= targetCount) {
            session
        } else {
            session.take(prefixCount) + session.takeLast(tailCount)
        }
        writer = FileWriter(logFile, false)
        kept.forEach { writer?.write("$it\n") }
        writer?.flush()
        fileLineCount = kept.size
    }

    companion object {
        private const val SESSION_MARKER = "========== NEW CARRIER SESSION =========="
        private const val SESSION_PREFIX_LINES = 40
        private const val COMPACT_TARGET_PERCENT = 75
        private const val EXPORT_PREFIX = "relay-export-"
        private const val MAX_EXPORT_SNAPSHOTS = 3
    }
}
