package com.sahand.zimzim

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.webkit.CookieManager
import android.webkit.MimeTypeMap
import android.widget.Toast
import java.io.File
import java.io.OutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.cert.X509Certificate
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManager
import javax.net.ssl.X509TrustManager

/** Saves a file from the walkie's server into the phone's Downloads folder. */
object Files {
    private const val CH = "files"

    fun download(app: Context, url: String, name: String) {
        val ui = Handler(Looper.getMainLooper())
        val host = Uri.parse(Walkie.link ?: "").host
        val u = Uri.parse(url)
        if (u.host == null || u.host != host) return
        val safe = name.replace(Regex("[/\\\\:*?\"<>|\\x00-\\x1f]"), "_").take(120).ifBlank { "file" }
        ui.post { Toast.makeText(app, "Downloading $safe…", Toast.LENGTH_SHORT).show() }
        Thread {
            var saved: Uri? = null
            var err: String? = null
            try {
                val c = URL(url).openConnection() as HttpURLConnection
                if (c is HttpsURLConnection) trustSelfSigned(c) // same rule as the WebView: only your own server
                CookieManager.getInstance().getCookie(url)?.let { c.setRequestProperty("Cookie", it) }
                c.connectTimeout = 15000; c.readTimeout = 30000
                if (c.responseCode != 200) throw Exception("Server said ${c.responseCode}")
                val ext = safe.substringAfterLast('.', "").lowercase()
                val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
                val (out, uri) = open(app, safe, mime)
                c.inputStream.use { inp -> out.use { inp.copyTo(it, 64 * 1024) } }
                finish(app, uri)
                saved = uri
                notify(app, safe, uri, mime)
            } catch (e: Exception) { err = e.message ?: "error" }
            ui.post {
                Toast.makeText(app, if (saved != null) "Saved to Downloads: $safe" else "Download failed: $err", Toast.LENGTH_LONG).show()
            }
        }.start()
    }

    private fun open(app: Context, name: String, mime: String): Pair<OutputStream, Uri> {
        if (Build.VERSION.SDK_INT >= 29) {
            val v = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/ZimZim")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = app.contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, v) ?: throw Exception("Can't write to Downloads")
            return Pair(app.contentResolver.openOutputStream(uri)!!, uri)
        }
        // Android 8–9: the app's own Downloads folder (no storage permission needed)
        val dir = File(app.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), "ZimZim").apply { mkdirs() }
        var f = File(dir, name); var i = 1
        while (f.exists()) f = File(dir, name.substringBeforeLast('.') + " ($i)" + (if ('.' in name) "." + name.substringAfterLast('.') else "")).also { i++ }
        return Pair(f.outputStream(), Uri.fromFile(f))
    }

    private fun finish(app: Context, uri: Uri) {
        if (Build.VERSION.SDK_INT >= 29 && uri.scheme == "content") {
            app.contentResolver.update(uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null)
        }
    }

    private fun notify(app: Context, name: String, uri: Uri, mime: String) {
        if (uri.scheme != "content") return
        val nm = app.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CH, "Downloads", NotificationManager.IMPORTANCE_DEFAULT))
        val view = Intent(Intent.ACTION_VIEW).setDataAndType(uri, mime)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        val pi = PendingIntent.getActivity(app, name.hashCode(), Intent.createChooser(view, name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(app, CH)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle("Downloaded")
            .setContentText(name)
            .setAutoCancel(true)
            .setContentIntent(pi)
            .build()
        try { nm.notify(name.hashCode(), n) } catch (_: Exception) {}
    }

    private fun trustSelfSigned(c: HttpsURLConnection) {
        val tm = object : X509TrustManager {
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
            override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
        }
        val ctx = SSLContext.getInstance("TLS")
        ctx.init(null, arrayOf<TrustManager>(tm), null)
        c.sslSocketFactory = ctx.socketFactory
        val host = c.url.host
        c.setHostnameVerifier { h, _ -> h == host }
    }
}
