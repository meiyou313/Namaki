# Namaki

An Android, Windows, and Linux VPN app. Its interface comes from the Arman VPN client and
its engine from PredatorVPN — the two are combined here into one app.
The browser sign-in flow follows the approach of FoxyVPN
(https://github.com/Vauth/FoxyVPN).

This is an unofficial, GPL-licensed app. It is not affiliated with
Mozilla or with either of the projects above. See `NOTICE.md` for the
sources, the license, and what was changed.

## Get the app

Open the **Releases** tab and download the file for your device:

- `namaki-3.3.0-Android-arm64-v8a.apk` — most modern phones (install this one)
- `namaki-3.3.0-Android-armeabi-v7a.apk` — older 32-bit phones
- `namaki-3.3.0-Android-x86_64.apk` — emulators / x86 phones
- `namaki-3.3.0-Android-universal.apk` — all architectures, bigger
- `namaki-3.3.0-Windows-x64.zip` — Windows: unpack it anywhere and run the app inside the `Namaki` folder
- `namaki-3.3.0-Linux-amd64.tar.gz` — Linux: unpack it and run the app inside the `Namaki` folder

The Windows and Linux apps carry their core with them (a `core` folder
beside the app) — keep it with the app.

Copy the APK to your phone and install it (allow "install unknown
apps" for the file manager / browser once). Installing over an older
Namaki version keeps your accounts — no need to uninstall first.

Without signing secrets the APKs are signed with the **debug key** —
fine for installing on your own phone, not for publishing. For a
release key, add the `ANDROID_KEYSTORE_BASE64` and
`ANDROID_KEYSTORE_PASSWORD` secrets (see the workflow file).

## First run

1. Open the app. If you have no account yet, it opens the **Accounts**
   screen first — sign in with an **existing** Firefox account
   (email + password). New-account sign-in may fail with HTTP 406;
   that is a limitation of the core this app is built on.
2. Pick a location, turn the VPN on, and check your IP changed.
3. The home screen shows your data usage (daily / weekly / monthly),
   and the widget on your home screen turns the VPN on and off.

## Updates

The app checks this repository (`meiyou313/Namaki`) for new releases
and shows an update notice on the home screen when one is published.
Every push to `main` builds the APKs, the Windows zip, and the Linux
archive in GitHub Actions and publishes them on the new release.

## Layout

```
app/                  Flutter UI (Android, Windows, Linux)
  android/app/...     Kotlin/Java bridge and VPN service
  windows/ linux/     Desktop runners
predator-core/        Go core + HTTP adapter (dashboard on 127.0.0.1:10820)
.github/workflows/   build-android.yml — the CI that builds every platform
scripts/              android-notices.cjs — third-party Go notices
```

The Windows and Linux apps run a desktop core built from the Arman VPN
client repository (pinned commit, see the workflow file).

## Build locally (optional)

Install Go 1.26.9, JDK 17, Flutter 3.47.7, Android SDK 36 +
NDK 28.0.13004108, then follow the steps in
`.github/workflows/build-android.yml` (gomobile bind, then
`flutter build apk --release --split-per-abi`). The same file builds
the Windows and Linux apps (Flutter desktop plus the desktop core
from the Arman VPN client repository).
