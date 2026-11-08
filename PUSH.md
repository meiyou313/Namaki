# Push this repo and get the APK

## 1. Create the repository on GitHub

Create a **new, empty** repository (no README, no .gitignore, no
license — this folder already has them). Note its URL, for example:

```
https://github.com/meiyou313/merged-vpn.git
```

## 2. Push from your computer

In a terminal, inside this folder:

```bash
git init -b main
git add -A
git commit -m "Merged VPN 2.0.0: Arman Flutter UI + PredatorVPN core"
git remote add origin https://github.com/meiyou313/merged-vpn.git
git push -u origin main
```

(If GitHub asks for a password, use a personal access token, not
your account password.)

## 3. Run the build

GitHub > your repo > **Actions** > **Build Android** > **Run workflow**
> **Run workflow**. The first run takes roughly 15–25 minutes
(it downloads Go, Flutter, the Android SDK and the NDK, and compiles
the Go core).

When it is green, open the run and download the **android** artifact
under "Artifacts". Install the `arm64-v8a` APK on most phones.

## 4. If the run fails

The log names the failing step. The most likely failures, in order:

| Failure | What it means | Fix |
|---|---|---|
| `test` job: `go vet` / `go test ./mobile` | A Go error in the adapter (`dashboard.go`) or the bridge's Go surface | Read the error, fix that file, push again |
| `test` job: `flutter analyze` | A Dart error (usually a patch that did not apply cleanly) | Fix the named file, push again |
| `android` job: `gomobile bind` | The Go core did not bind (NDK / Go version / sing-box tags) | Check the bind command in the workflow; Predator's own CI uses the same command and Go 1.26.9 |
| `android` job: `flutter build apk` | A Kotlin/Java error (`PredatorBridge`, `PredatorApp`, `FoxVpnService`) or a missing `foxvpn.aar` | The error names the file and line; fix it, push again |
| `android` job: `android-notices.cjs` | A Go dependency without a license file | Unlikely (Predator's CI runs the same script); check the named module |

Each push re-runs the workflow automatically.

## What was checked before this delivery (static only)

- `dashboard.go`: token auth added (`StartDashboard(listen, token)`,
  `X-Local-Token` required on every request); the Flutter UI reads
  that token from `<dataDir>/local-api.token`.
- `PredatorApp.java`: writes a fresh token + `config.json` before
  starting the dashboard; implements the gomobile `Host` interface
  (`save / changed / protect / dnsAddress / certificates`).
- `PredatorBridge.kt`: every MethodChannel method the Dart UI calls
  (`dataDir`, `startCore`, `setOn`, `isRunning`, `connectionMode`,
  `showSpeed`, `setShowSpeed`, `setConnectionMode`, `installedApps`,
  `appIcon`, `appRouting`, `setAppRouting`, `setWebViewProxy`,
  `saveFile`, `openFile`, `abi`, `isTv`, `openUrl`) is implemented
  or inherited from Arman's `MainActivity.kt`.
- No references to Arman's `CoreService` / `mobile.Mobile` /
  `Tun2Socks` / `hev-socks5-tunnel` remain in the Android sources.
- `app/android/app/build.gradle.kts`: package `com.mergedvpn.client`,
  NDK 28.0.13004108, `libs/foxvpn.aar`, desugaring enabled.
- `AndroidManifest.xml`: application `.PredatorApp`, service
  `.FoxVpnService`, tile `.QuickTile`, widget `.ConnectWidget`.

## What was NOT checked (the first CI run will tell)

- The code was **not compiled**: no Go, Flutter, JDK or Android SDK
  exists in the workspace where it was written. `go vet`,
  `flutter analyze` and the APK build all run in CI first.
- The APK was **not run on a phone**: sign-in, connect, split
  tunneling and the tile still need a real device test.
- `Backend.startDashboard` is assumed to be exposed by gomobile as
  a throwing Java method (Go's `error` return). If the generated
  binding names it differently, the `test` job's `go vet` will not
  catch it — the `flutter build apk` step will, at the Java compile.
