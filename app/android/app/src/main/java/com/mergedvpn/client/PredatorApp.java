// SPDX-License-Identifier: GPL-3.0-or-later
// Merged project: PredatorVPN core (GPL-3.0-or-later, Erfan Tahvilian)
// + Arman VPN Flutter UI (GPL-3.0, armanlife).
//
// PredatorApp is FoxApp.java from PredatorVPN v2.0.0, adapted:
//  - package com.mergedvpn.client, application name "Namaki"
//  - the WebView MainActivity is gone: the UI is Arman's Flutter
//    MainActivity (same package). State changes go to PredatorBridge
//    instead of MainActivity.receive().
//  - the Quick Settings tile is Arman's QuickTile, not FoxTileService.
//  - on start it writes a fresh local-api.token and starts the
//    Arman-compatible HTTP dashboard (Backend.StartDashboard) so the
//    Flutter UI can reach the Predator core on 127.0.0.1:10820.
//
// NOTE on the base class: this extends plain android.app.Application,
// like FoxApp did. The Flutter engine is created by FlutterActivity;
// no FlutterApplication base is needed.
package com.mergedvpn.client;

import android.app.Application;
import android.content.ComponentName;
import android.content.res.Configuration;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.os.Handler;
import android.os.Looper;
import android.service.quicksettings.TileService;
import android.util.Base64;
import com.foxvpn.nativebridge.mobile.Backend;
import com.foxvpn.nativebridge.mobile.Host;
import com.foxvpn.nativebridge.mobile.Mobile;
import com.foxvpn.nativebridge.libbox.Libbox;
import com.foxvpn.nativebridge.libbox.SetupOptions;
import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.security.SecureRandom;
import java.util.Enumeration;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONObject;

public final class PredatorApp extends Application implements Host {
    static PredatorApp instance;

    final Handler main = new Handler(Looper.getMainLooper());
    final ExecutorService actions = Executors.newSingleThreadExecutor();
    final ExecutorService catalog = Executors.newSingleThreadExecutor();
    final ExecutorService probes = Executors.newSingleThreadExecutor();
    Backend backend;
    volatile FoxVpnService vpn;
    volatile Network network;
    volatile LinkProperties physicalLinks;
    volatile JSONObject state = new JSONObject();
    volatile String initializationError;
    private CryptoVault vault;
    private ConnectivityManager connectivity;
    private String certificates;
    private boolean libboxDone;

    @Override public void onCreate() {
        super.onCreate();
        instance = this;
        vault = new CryptoVault(this);
        connectivity = getSystemService(ConnectivityManager.class);
        network = connectivity.getActiveNetwork();
        physicalLinks = connectivity.getLinkProperties(network);
        connectivity.registerNetworkCallback(new NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build(),
            new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network value) {
                network = value; physicalLinks = connectivity.getLinkProperties(value); updateNetwork();
            }
            @Override public void onLinkPropertiesChanged(Network value, LinkProperties links) {
                if (value.equals(network)) { physicalLinks = links; updateNetwork(); }
            }
            @Override public void onLost(Network value) {
                if (value.equals(network)) {
                    network = null; physicalLinks = null;
                    for (Network candidate : connectivity.getAllNetworks()) {
                        NetworkCapabilities caps = connectivity.getNetworkCapabilities(candidate);
                        if (caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                            && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) {
                            network = candidate; physicalLinks = connectivity.getLinkProperties(candidate); break;
                        }
                    }
                    updateNetwork();
                }
            }
            private void updateNetwork() { FoxVpnService service = vpn; if (service != null) service.networkChanged(); }
        });
        initBackend();
    }

    /**
     * Creates the Backend and starts its dashboard. Retryable: if the
     * first start fails (a taken dashboard port, a storage error, ...),
     * PredatorBridge.startCore calls this again instead of leaving the
     * app on "Starting the core…" forever. The real error is kept in
     * initializationError so the UI can show it.
     */
    public void initBackend() {
        if (backend != null && initializationError == null) return;
        initializationError = null;
        try {
            certificates = loadCertificates();
            SetupOptions options = new SetupOptions();
            options.setBasePath(getFilesDir().getAbsolutePath());
            options.setWorkingPath(getFilesDir().getAbsolutePath());
            options.setTempPath(getCacheDir().getAbsolutePath());
            options.setFixAndroidStack(true);
            if (!libboxDone) {
                Libbox.setup(options);
                libboxDone = true;
            }
            Libbox.setLocale("en");
            backend = Mobile.newBackend(this, vault.read());
            backend.setDark(isDark());
            state = new JSONObject(backend.snapshot());
            startDashboard();
            catalog.execute(() -> backend.call("servers", "[]"));
        } catch (Exception e) {
            backend = null;
            initializationError = "The core could not start (" + e + "). Restart Namaki or reinstall if the problem persists.";
            android.util.Log.e("PredatorApp", "backend init failed", e);
        }
    }

    /** The folder the Flutter UI uses as its data folder (AndroidCore.dataDir). */
    public File coreDataDir() { return new File(getFilesDir(), "core"); }

    /**
     * Writes a fresh local-api.token and starts the dashboard.
     * The token is regenerated on every app start, BEFORE the dashboard
     * listens, so Flutter's CoreLocator (which reads the same file on
     * every poll) always gets the current token, and a stale listener
     * can never be trusted with an old one. The dashboard also refuses
     * requests without that token (dashboard.go: requireToken).
     *
     * The dashboard's port (10820) can already be taken — another VPN
     * app on the phone (Arman, Predator, ...) listens on the same
     * address. Then the first start fails and the app used to stay on
     * "Starting the core…" forever. So try the next ports, and write
     * the one that worked to config.json, so the Flutter UI finds the
     * dashboard wherever it listens.
     */
    private void startDashboard() throws Exception {
        File dir = coreDataDir();
        if (!dir.mkdirs() && !dir.isDirectory()) throw new java.io.IOException("Could not create core data folder");
        byte[] raw = new byte[32];
        new SecureRandom().nextBytes(raw);
        StringBuilder hex = new StringBuilder(64);
        for (byte b : raw) hex.append(String.format("%02x", b));
        String token = hex.toString();
        try (FileOutputStream out = new FileOutputStream(new File(dir, "local-api.token"))) {
            out.write(token.getBytes(StandardCharsets.UTF_8));
        }
        Exception last = null;
        for (int port = 10820; port <= 10829; port++) {
            try {
                // gomobile maps Go's (error) return to a Java method that throws.
                backend.startDashboard("127.0.0.1:" + port, token);
            } catch (Exception e) {
                last = e;
                continue;
            }
            try (FileOutputStream out = new FileOutputStream(new File(dir, "config.json"))) {
                out.write(("{\n  \"dashboard\": {\n    \"listen\": \"127.0.0.1:" + port + "\"\n  }\n}\n").getBytes(StandardCharsets.UTF_8));
            }
            return;
        }
        throw new java.io.IOException("The dashboard could not listen on ports 10820-10829", last);
    }

    boolean isDark() {
        return (getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK)
            == Configuration.UI_MODE_NIGHT_YES;
    }

    @Override public void onConfigurationChanged(Configuration config) {
        super.onConfigurationChanged(config);
        if (backend != null) backend.setDark(isDark());
    }

    @Override public void save(String value) throws Exception { vault.write(value); }

    @Override public void changed(String value) {
        try { state = new JSONObject(value); } catch (Exception e) { return; }
        main.post(() -> {
            PredatorBridge.onBackendStateChanged(this);
            TileService.requestListeningState(this, new ComponentName(this, QuickTile.class));
            FoxVpnService service = vpn;
            if (service != null) service.stateChanged();
        });
    }

    @Override public boolean protect(int fd) {
        FoxVpnService service = vpn;
        return service == null || service.protect(fd);
    }

    @Override public String dnsAddress() {
        LinkProperties links = physicalLinks;
        if (links == null || links.getDnsServers().isEmpty()) return "";
        return links.getDnsServers().get(0).getHostAddress();
    }

    @Override public String certificates() { return certificates == null ? "" : certificates; }

    String loadCertificates() throws Exception {
        StringBuilder pem = new StringBuilder();
        try {
            KeyStore store = KeyStore.getInstance("AndroidCAStore");
            store.load(null);
            Enumeration<String> aliases = store.aliases();
            while (aliases.hasMoreElements()) {
                java.security.cert.Certificate cert = store.getCertificate(aliases.nextElement());
                if (cert != null) pem.append("-----BEGIN CERTIFICATE-----\n")
                    .append(Base64.encodeToString(cert.getEncoded(), Base64.NO_WRAP))
                    .append("\n-----END CERTIFICATE-----\n");
            }
        } catch (Exception ignored) {
            // Some devices restrict the AndroidCAStore; fall back to the bundled list below.
        }
        java.io.InputStream bundled = getResources().openRawResource(R.raw.cacert);
        java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
        byte[] buffer = new byte[8192];
        int read;
        while ((read = bundled.read(buffer)) != -1) out.write(buffer, 0, read);
        bundled.close();
        pem.append(out.toString("UTF-8"));
        return pem.toString();
    }

    String connection() { return state.optString("connection", "disconnected"); }

    void stopVpn() {
        if (backend != null) backend.disconnect();
        FoxVpnService service = vpn;
        if (service != null) service.requestStop();
    }
}
