package com.sahand.zimzim

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.media.AudioDeviceInfo
import android.media.AudioManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.webkit.JavascriptInterface
import android.webkit.WebView

/** Holds the walkie WebView so it can keep running after the screen is closed. */
object Walkie {
    var web: WebView? = null
    var link: String? = null
    var activity: MainActivity? = null
    @Volatile var powered = false
    private var earOn = false
    private var prevMode = AudioManager.MODE_NORMAL

    fun keepAlive(c: Context) = c.getSharedPreferences("zimzim", Context.MODE_PRIVATE).getBoolean("keep_alive", true)

    fun shutdown(c: Context) {
        powered = false
        c.applicationContext.stopService(Intent(c.applicationContext, TalkService::class.java))
        setEarpiece(c, false)
        web?.let { w ->
            (w.parent as? android.view.ViewGroup)?.removeView(w)
            w.destroy()
        }
        web = null
        link = null
    }

    @Suppress("DEPRECATION")
    private fun speakerOn(am: AudioManager) { am.isSpeakerphoneOn = true }

    fun setEarpiece(c: Context, on: Boolean) {
        val am = c.applicationContext.getSystemService(AudioManager::class.java) ?: return
        try {
            if (on) {
                if (!earOn) prevMode = am.mode
                earOn = true
                am.mode = AudioManager.MODE_IN_COMMUNICATION
                if (Build.VERSION.SDK_INT >= 31) {
                    am.availableCommunicationDevices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_EARPIECE }
                        ?.let { am.setCommunicationDevice(it) }
                } else {
                    @Suppress("DEPRECATION")
                    am.isSpeakerphoneOn = false
                }
            } else if (earOn) {
                // Only undo what we changed; never touch audio routing otherwise.
                earOn = false
                if (Build.VERSION.SDK_INT >= 31) am.clearCommunicationDevice()
                else speakerOn(am)
                am.mode = prevMode
            }
        } catch (_: Exception) {}
    }
}

class Bridge(private val app: Context) {
    private val ui = Handler(Looper.getMainLooper())
    private val prefs = app.getSharedPreferences("zimzim", Context.MODE_PRIVATE)

    // Knob on/off: keep the app alive with the screen off.
    @JavascriptInterface fun power(on: Boolean) = ui.post {
        Walkie.powered = on
        val i = Intent(app, TalkService::class.java)
        if (on) {
            if (app.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) return@post
            try { app.startForegroundService(i) } catch (_: Exception) {}
        } else {
            app.stopService(i)
            Walkie.setEarpiece(app, false)
        }
    }
    @JavascriptInterface fun changeLink() = ui.post { Walkie.activity?.confirmChangeLink() }
    @JavascriptInterface fun isApp() = true
    @JavascriptInterface fun setEarpiece(on: Boolean) = ui.post { Walkie.setEarpiece(app, on) }
    @JavascriptInterface fun getKeepAlive() = Walkie.keepAlive(app)
    @JavascriptInterface fun setKeepAlive(on: Boolean) { prefs.edit().putBoolean("keep_alive", on).apply() }

    @JavascriptInterface fun page(from: String) = alert("📟 ${from.take(40)} is paging you", "Tap to open ZimZim")

    // Loud notification even if the phone is in a pocket (pages, video requests).
    @JavascriptInterface fun alert(title: String, text: String) = ui.post {
        val nm = app.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(PAGE_CH, "Pages", NotificationManager.IMPORTANCE_HIGH).apply {
            enableVibration(true)
            vibrationPattern = longArrayOf(0, 400, 150, 400, 150, 400)
        })
        val open = PendingIntent.getActivity(app, 2, Intent(app, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(app, PAGE_CH)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title.take(60))
            .setContentText(text.take(80))
            .setCategory(Notification.CATEGORY_CALL)
            .setAutoCancel(true)
            .setContentIntent(open)
            .build()
        try { nm.notify(PAGE_ID, n) } catch (_: Exception) {}
    }

    companion object {
        const val PAGE_CH = "page"
        const val PAGE_ID = 8
    }
}
