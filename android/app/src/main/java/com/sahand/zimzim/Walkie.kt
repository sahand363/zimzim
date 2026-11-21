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

    fun setEarpiece(c: Context, on: Boolean) {
        val am = c.applicationContext.getSystemService(AudioManager::class.java) ?: return
        try {
            if (on) {
                am.mode = AudioManager.MODE_IN_COMMUNICATION
                if (Build.VERSION.SDK_INT >= 31) {
                    am.availableCommunicationDevices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_EARPIECE }
                        ?.let { am.setCommunicationDevice(it) }
                } else {
                    @Suppress("DEPRECATION")
                    am.isSpeakerphoneOn = false
                }
            } else {
                if (Build.VERSION.SDK_INT >= 31) {
                    val spk = am.availableCommunicationDevices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }
                    if (am.mode == AudioManager.MODE_IN_COMMUNICATION && spk != null) am.setCommunicationDevice(spk)
                    else am.clearCommunicationDevice()
                } else {
                    @Suppress("DEPRECATION")
                    am.isSpeakerphoneOn = true
                }
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

    // Someone paged me: loud notification even if the phone is in a pocket.
    @JavascriptInterface fun page(from: String) = ui.post {
        val nm = app.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(PAGE_CH, "Pages", NotificationManager.IMPORTANCE_HIGH).apply {
            enableVibration(true)
            vibrationPattern = longArrayOf(0, 400, 150, 400, 150, 400)
        })
        val open = PendingIntent.getActivity(app, 2, Intent(app, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(app, PAGE_CH)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle("📟 ${from.take(40)} is paging you")
            .setContentText("Tap to open ZimZim")
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
