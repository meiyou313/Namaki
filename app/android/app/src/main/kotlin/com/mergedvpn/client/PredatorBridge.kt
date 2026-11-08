package com.mergedvpn.client

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import java.io.File
import org.json.JSONArray
import org.json.JSONObject

/**
 * The bridge between Arman's Flutter UI (MethodChannel "app/android")
 * and Predator's Go core (Backend + FoxVpnService).
 *
 * This replaces Arman's CoreService.kt + mobile.Mobile. What changed:
 *
 * - The core is started by PredatorApp (Application.onCreate): it creates
 *   the Backend with the encrypted vault and starts the HTTP dashboard
 *   on 127.0.0.1:10820. So startCore() here only makes sure that
 *   happened, and turns the VPN on when asked.
 * - The VPN itself is FoxVpnService (sing-box / gVisor TUN), not
 *   Arman's CoreService + hev-socks5-tunnel. turnOn() starts that
 *   service; it calls Backend.Call("connect") and brings the TUN up.
 * - Split tunneling lives in the Backend snapshot
 *   (settings.splitMode / settings.excludedApps), not in
 *   SharedPreferences. appMode/appSet read it from there and
 *   setAppRouting writes the whole settings object back through
 *   Backend.Call("settings", ...).
 * - There is no region app list in Predator, so regionAppList() is
 *   empty and suggest() never adds apps by itself.
 * - Proxy mode is Predator's settings.proxyOnly, not a separate
 *   connection mode in the core.
 */
object PredatorBridge {
    private const val TAG = "PredatorBridge"
    private const val PREFS = "vpn"
    private const val SHOW_SPEED = "show_speed"

    const val MODE_EXCLUDE = "exclude"
    const val MODE_INCLUDE = "include"
    const val MODE_VPN = "vpn"
    const val MODE_PROXY = "proxy"
    const val BYPASS = "bypass_apps"
    const val ONLY = "only_apps"
    const val DECLINED = "declined_apps"

    private val main = Handler(Looper.getMainLooper())

    /** The VPN is on: the foreground service runs. */
    @JvmStatic
    @Volatile
    var on = false
        private set

    /** On and through: the exit is known. */
    @JvmStatic
    @Volatile
    var connected = false
        private set

    private fun app(): PredatorApp? = PredatorApp.instance

    /** The core's data folder: the token file, config.json, and the app's own files. */
    @JvmStatic
    fun dataDir(context: Context): File = File(context.filesDir, "core")

    /** Whether the Backend exists and started without error. */
    @JvmStatic
    fun isRunning(): Boolean {
        val a = app() ?: return false
        return a.backend != null && a.initializationError == null
    }

    @JvmStatic
    fun isOn(): Boolean = on

    @JvmStatic
    fun isConnected(): Boolean = connected

    /**
     * The service sets this when it stops itself (FoxVpnService
     * requestStop). The setter of `on` is private, so Java callers
     * use this instead of PredatorBridge.setOn(...).
     */
    @JvmStatic
    fun setOnValue(value: Boolean) {
        on = value
        if (!value) connected = false
    }

    /**
     * Makes sure the core is up, and turns the VPN on when [on] is true.
     * The Backend itself is created in PredatorApp.onCreate (the process
     * starts there before any Activity), so a missing Backend here means
     * the core failed to start — see PredatorApp.initializationError.
     */
    @JvmStatic
    @Synchronized
    fun startCore(context: Context, on: Boolean) {
        if (!isRunning()) {
            Log.e(TAG, "the core did not start: ${app()?.initializationError}")
            return
        }
        if (on) turnOn(context)
    }

    /** Turns the VPN on. The service asks the Backend to connect. */
    @JvmStatic
    fun turnOn(context: Context) {
        if (!isRunning()) throw IllegalStateException("The VPN core did not start")
        FoxVpnService.connect(context)
        on = true
        connected = false
        QuickTile.refresh(context)
        ConnectWidget.updateAll(context)
    }

    /** Turns the VPN off; the core and its dashboard keep running for the UI. */
    @JvmStatic
    fun turnOff(context: Context) {
        app()?.stopVpn()
        on = false
        connected = false
        QuickTile.refresh(context)
        ConnectWidget.updateAll(context)
    }

    /**
     * Called when the Backend reports a new snapshot (PredatorApp.changed)
     * and from FoxVpnService.stateChanged: keeps on/connected in step
     * with the core, and refreshes the widget.
     */
    @JvmStatic
    fun onBackendStateChanged(context: Context) {
        val connection = app()?.connection() ?: "disconnected"
        connected = on && connection == "connected"
        if (connection == "disconnected" && on) {
            // The core gave up (or the service ended it): the service
            // stops itself; do not claim to be on any more.
            on = false
            connected = false
        }
        ConnectWidget.updateAll(context)
    }

    /** Stops the VPN service as well, if it is running. */
    @JvmStatic
    fun stopCore() {
        // The Backend has no stop: it lives as long as the process, and
        // the dashboard with it. Ending the process (the system does it
        // when the app is swiped away) is what stops it.
        Log.i(TAG, "stopCore: the core stays until the process ends")
    }

    private fun prefs(context: Context) =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun showSpeed(context: Context) = prefs(context).getBoolean(SHOW_SPEED, false)

    fun setShowSpeed(context: Context, on: Boolean) {
        prefs(context).edit().putBoolean(SHOW_SPEED, on).apply()
    }

    /** The Backend snapshot's settings object, or an empty one. */
    private fun settings(): JSONObject =
        app()?.state?.optJSONObject("settings") ?: JSONObject()

    /** "vpn" unless the core is in proxy-only mode. */
    @JvmStatic
    fun connectionMode(context: Context): String =
        if (settings().optBoolean("proxyOnly")) MODE_PROXY else MODE_VPN

    fun usesVpn(context: Context) = connectionMode(context) != MODE_PROXY

    /** Sets proxy-only mode in the Backend settings; reconnects when on. */
    @JvmStatic
    fun setConnectionMode(context: Context, mode: String) {
        val next = settings()
        next.put("proxyOnly", mode == MODE_PROXY)
        writeSettings(next)
        reconnect(context)
    }

    /** The split-tunneling mode in the Backend settings. */
    fun appMode(context: Context): String =
        settings().optString("splitMode", MODE_EXCLUDE).takeIf { it == MODE_INCLUDE } ?: MODE_EXCLUDE

    /**
     * The apps for [key]: in "include" mode the ONLY list is the
     * settings' excludedApps; in "exclude" mode the BYPASS list is.
     * DECLINED has no equivalent in Predator and is always empty.
     */
    fun appSet(context: Context, key: String): Set<String> {
        if (key == DECLINED) return emptySet()
        val apps = settings().optJSONArray("excludedApps") ?: return emptySet()
        val out = mutableSetOf<String>()
        for (i in 0 until apps.length()) out.add(apps.optString(i))
        return out
    }

    /** No region app list in Predator: nothing is suggested by itself. */
    fun regionAppList(): List<String> = emptyList()

    fun suggest(context: Context, force: Boolean = false): Boolean = false

    /**
     * Writes the split-tunneling choice to the Backend: the mode and the
     * app list for that mode (the other list is not kept — Predator has
     * one excludedApps list, and its meaning flips with splitMode).
     */
    fun setAppRouting(context: Context, mode: String, sets: Map<String, Set<String>>) {
        val next = settings()
        val chosen = if (mode == MODE_INCLUDE) sets[ONLY] else sets[BYPASS]
        next.put("splitMode", if (mode == MODE_INCLUDE) MODE_INCLUDE else MODE_EXCLUDE)
        val arr = JSONArray()
        for (pkg in chosen.orEmpty()) arr.put(pkg)
        next.put("excludedApps", arr)
        writeSettings(next)
    }

    /** Sends the whole settings object back; Predator validates it. */
    private fun writeSettings(next: JSONObject) {
        val backend = app()?.backend ?: return
        try {
            val args = JSONArray().put(next).toString()
            val answer = JSONObject(backend.call("settings", args))
            if (!answer.optBoolean("ok")) {
                Log.e(TAG, "the core refused the settings: ${answer.optString("error")}")
            }
        } catch (e: Exception) {
            Log.e(TAG, "writing the settings failed", e)
        }
    }

    /** Restarts the VPN with the current settings, when on. */
    fun reconnect(context: Context) {
        if (!on) return
        turnOff(context)
        // The service stops on a worker thread; give it a moment before
        // starting it again, or its start is refused as "already stopping".
        main.postDelayed({ if (isRunning()) turnOn(context) }, 1000)
    }
}
