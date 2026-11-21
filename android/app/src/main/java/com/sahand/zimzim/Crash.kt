package com.sahand.zimzim

import android.content.Context
import android.os.Build

/** Saves the last crash so the next start can show it (helps fix bugs we can't see). */
object Crash {
    private var installed = false

    fun install(app: Context) {
        if (installed) return
        installed = true
        val prev = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { t, e ->
            try {
                note(app, "thread ${t.name}: " + android.util.Log.getStackTraceString(e))
            } catch (_: Throwable) {}
            prev?.uncaughtException(t, e)
        }
    }

    fun note(c: Context, text: String) {
        val v = "ZimZim ${BuildConfig.VERSION_NAME} · Android ${Build.VERSION.RELEASE} · ${Build.MANUFACTURER} ${Build.MODEL}\n$text"
        c.getSharedPreferences("zimzim", Context.MODE_PRIVATE).edit().putString("crash", v.take(6000)).commit()
    }

    fun take(c: Context): String? {
        val p = c.getSharedPreferences("zimzim", Context.MODE_PRIVATE)
        val v = p.getString("crash", null) ?: return null
        p.edit().remove("crash").apply()
        return v
    }
}
