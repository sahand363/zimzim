package com.sahand.zimzim

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.content.Context
import android.content.Intent
import android.content.MutableContextWrapper
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.webkit.JavascriptInterface
import android.webkit.PermissionRequest
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView

class MainActivity : Activity() {

    private var web: WebView?
        get() = Walkie.web
        set(v) { Walkie.web = v }
    private var pendingPermission: PermissionRequest? = null
    private val prefs by lazy { getSharedPreferences("zimzim", Context.MODE_PRIVATE) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        if (intent?.getBooleanExtra(EXTRA_QUIT, false) == true) { quit(); return }
        val link = prefs.getString(KEY_URL, null)
        Walkie.activity = this
        Crash.install(applicationContext)
        Crash.take(this)?.let { report ->
            AlertDialog.Builder(this)
                .setTitle("ZimZim closed unexpectedly last time")
                .setMessage("Copy the report and send it to the developer.\n\n" + report.take(600))
                .setPositiveButton("Copy") { _, _ ->
                    getSystemService(android.content.ClipboardManager::class.java)
                        .setPrimaryClip(android.content.ClipData.newPlainText("ZimZim crash", report))
                }
                .setNegativeButton("Close", null)
                .show()
        }
        if (link.isNullOrBlank()) showSetup(null)
        else if (Walkie.web != null && Walkie.link == link) attach(Walkie.web!!)
        else openWalkie(link)
    }

    /** Put the (possibly still running) walkie WebView on screen. */
    private fun attach(w: WebView) {
        (w.context as? MutableContextWrapper)?.baseContext = this
        (w.parent as? ViewGroup)?.removeView(w)
        val frame = FrameLayout(this)
        frame.addView(w, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        setContentView(frame)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        if (intent.getBooleanExtra(EXTRA_QUIT, false)) quit()
    }

    private fun quit() {
        Walkie.shutdown(this)
        finishAndRemoveTask()
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    // ---------- setup screen ----------
    private fun showSetup(error: String?) {
        Walkie.shutdown(this)
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(28), dp(24), dp(28), dp(24))
            setBackgroundColor(Color.parseColor("#0F1110"))
        }
        val title = TextView(this).apply {
            text = "ZimZim"
            textSize = 30f
            setTextColor(Color.parseColor("#E9EDE5"))
            paint.isFakeBoldText = true
        }
        val sub = TextView(this).apply {
            text = "Paste your walkie's secret link."
            textSize = 15f
            setTextColor(Color.parseColor("#8E968A"))
            setPadding(0, dp(6), 0, dp(20))
        }
        val input = EditText(this).apply {
            hint = "https://…/w/…/"
            setText(prefs.getString(KEY_URL, "") ?: "")
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine()
            setTextColor(Color.WHITE)
            setHintTextColor(Color.parseColor("#5C6458"))
            textSize = 15f
            setPadding(dp(14), dp(14), dp(14), dp(14))
            background = GradientDrawable().apply {
                cornerRadius = dp(12).toFloat()
                setColor(Color.parseColor("#171A16"))
                setStroke(dp(1), Color.parseColor("#2C3129"))
            }
        }
        val err = TextView(this).apply {
            text = error ?: ""
            setTextColor(Color.parseColor("#FF8A70"))
            textSize = 13f
            setPadding(0, dp(8), 0, dp(8))
        }
        val go = Button(this).apply {
            text = "Open walkie"
            isAllCaps = false
            textSize = 16f
            setTextColor(Color.WHITE)
            background = GradientDrawable().apply {
                cornerRadius = dp(12).toFloat()
                setColor(Color.parseColor("#E2552F"))
            }
            setOnClickListener {
                var u = input.text.toString().trim()
                if (u.isNotEmpty() && !u.startsWith("http")) u = "https://$u"
                val parsed = Uri.parse(u)
                if (parsed.host.isNullOrBlank() || !(parsed.path ?: "").contains("/w/")) {
                    err.text = "That doesn't look like a walkie link."
                    return@setOnClickListener
                }
                if (!u.endsWith("/")) u += "/"
                prefs.edit().putString(KEY_URL, u).apply()
                openWalkie(u)
            }
        }
        root.addView(title)
        root.addView(sub)
        root.addView(input, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(err)
        root.addView(go, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(52)))
        setContentView(root)
    }

    // ---------- walkie ----------
    @SuppressLint("SetJavaScriptEnabled")
    fun openWalkie(link: String) {
        askPermissions()
        val host = Uri.parse(link).host
        Walkie.shutdown(this)
        // Created on the app context so it can outlive this screen when "stay on" is enabled.
        val w = WebView(MutableContextWrapper(applicationContext))
        web = w
        Walkie.link = link
        w.setBackgroundColor(Color.parseColor("#0F1110"))
        w.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = false
            cacheMode = WebSettings.LOAD_DEFAULT
            setSupportZoom(false)
        }
        w.addJavascriptInterface(Bridge(applicationContext), "ZimZimApp")
        w.webViewClient = object : WebViewClient() {
            // Allow the self-signed certificate of YOUR server only (menu option "SSL without a domain").
            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                if (Uri.parse(error.url).host == host) handler.proceed() else handler.cancel()
            }
            override fun shouldOverrideUrlLoading(view: WebView, req: WebResourceRequest): Boolean {
                return req.url.host != host
            }
            // The page's engine crashed or was killed: rebuild it instead of letting Android close the app.
            override fun onRenderProcessGone(view: WebView, detail: android.webkit.RenderProcessGoneDetail): Boolean {
                Crash.note(applicationContext, "render process gone, crashed=${detail.didCrash()}")
                if (Walkie.web === view) {
                    (view.parent as? ViewGroup)?.removeView(view)
                    view.destroy()
                    Walkie.web = null
                    Walkie.powered = false
                    applicationContext.stopService(Intent(applicationContext, TalkService::class.java))
                    Walkie.activity?.let { a -> prefs.getString(KEY_URL, null)?.let { a.openWalkie(it) } }
                }
                return true
            }
            override fun onReceivedError(view: WebView, req: WebResourceRequest, error: android.webkit.WebResourceError) {
                if (req.isForMainFrame) {
                    view.loadDataWithBaseURL(null, offlinePage(error.description?.toString() ?: "No connection"), "text/html", "utf-8", null)
                }
            }
        }
        w.webChromeClient = object : WebChromeClient() {
            override fun onPermissionRequest(request: PermissionRequest) {
                w.post {
                    if (Uri.parse(request.origin.toString()).host != host) { request.deny(); return@post }
                    val res = request.resources.filter {
                        it == PermissionRequest.RESOURCE_AUDIO_CAPTURE || it == PermissionRequest.RESOURCE_VIDEO_CAPTURE
                    }
                    val need = res.map {
                        if (it == PermissionRequest.RESOURCE_VIDEO_CAPTURE) Manifest.permission.CAMERA else Manifest.permission.RECORD_AUDIO
                    }.filter { applicationContext.checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
                    if (res.isEmpty()) { request.deny(); return@post }
                    if (need.isEmpty()) { request.grant(res.toTypedArray()); return@post }
                    val a = Walkie.activity
                    if (a == null) { request.deny(); return@post }
                    a.pendingPermission = request
                    a.requestPermissions(need.toTypedArray(), REQ_MIC)
                }
            }
        }
        attach(w)
        w.loadUrl(link)
    }

    private fun offlinePage(why: String) = """
        <html><body style="background:#0f1110;color:#e9ede5;font-family:sans-serif;display:flex;height:100vh;margin:0;align-items:center;justify-content:center;text-align:center">
        <div><h2 style="margin:0 0 8px">Can't reach the server</h2><p style="color:#8e968a">${why.replace("<", "")}</p>
        <p><button onclick="location.href='${prefs.getString(KEY_URL, "")}'" style="background:#e2552f;color:#fff;border:0;border-radius:12px;padding:12px 20px;font-size:16px">Try again</button></p>
        <p><a href="#" onclick="ZimZimApp.changeLink();return false" style="color:#8e968a">Change link</a></p></div></body></html>
    """.trimIndent()

    private fun askPermissions() {
        val need = mutableListOf<String>()
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) need += Manifest.permission.RECORD_AUDIO
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) need += Manifest.permission.POST_NOTIFICATIONS
        if (need.isNotEmpty()) requestPermissions(need.toTypedArray(), REQ_START)
    }

    override fun onRequestPermissionsResult(code: Int, perms: Array<out String>, results: IntArray) {
        super.onRequestPermissionsResult(code, perms, results)
        val micOk = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
        pendingPermission?.let { req ->
            val camOk = checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED
            val ok = req.resources.filter {
                (it == PermissionRequest.RESOURCE_AUDIO_CAPTURE && micOk) || (it == PermissionRequest.RESOURCE_VIDEO_CAPTURE && camOk)
            }
            if (ok.size == req.resources.size && ok.isNotEmpty()) req.grant(ok.toTypedArray()) else req.deny()
            pendingPermission = null
        }
    }

    fun confirmChangeLink() {
        AlertDialog.Builder(this)
            .setMessage("Change the walkie link?")
            .setPositiveButton("Change") { _, _ -> showSetup(null) }
            .setNegativeButton("Cancel", null)
            .show()
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        // Keep listening in the background instead of closing.
        if (web != null) moveTaskToBack(true) else super.onBackPressed()
    }

    override fun onDestroy() {
        if (Walkie.activity === this) Walkie.activity = null
        val w = Walkie.web
        if (w != null && !isChangingConfigurations) {
            if (Walkie.powered && Walkie.keepAlive(this)) {
                // Keep talking/listening in the background service.
                (w.parent as? ViewGroup)?.removeView(w)
                (w.context as? MutableContextWrapper)?.baseContext = applicationContext
            } else Walkie.shutdown(this)
        }
        super.onDestroy()
    }

    companion object {
        const val KEY_URL = "url"
        const val EXTRA_QUIT = "quit"
        const val REQ_MIC = 1
        const val REQ_START = 2
    }
}
