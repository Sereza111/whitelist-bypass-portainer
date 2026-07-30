package bypass.whitelist.util

import android.content.Context

/**
 * Process-wide relay log. Activities and foreground services run in the same
 * Android process and must never hold independent FileWriter instances for the
 * same cache file.
 */
object AppLog {
    @Volatile
    private var instance: LogWriter? = null

    fun init(context: Context) {
        if (instance != null) return
        synchronized(this) {
            if (instance == null) {
                instance = LogWriter(context.applicationContext.cacheDir)
            }
        }
    }

    val writer: LogWriter
        get() = checkNotNull(instance) { "AppLog.init must be called from Application.onCreate" }
}
