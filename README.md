# Merged VPN

**Arman VPN's Flutter UI + PredatorVPN's Go core, in one Android app.**

- UI: Arman VPN client (Flutter/Dart) — home, servers, accounts,
  settings, split tunneling, Quick Settings tile, home-screen widget.
- Engine: PredatorVPN v2.0.0 (Go + sing-box / gVisor TUN) — Firefox
  account sign-in, Mozilla VPN (Guardian) servers, per-account quota.
- The two are joined by a bridge (`app/android/.../PredatorBridge.kt`,
  `PredatorApp.java`) and an HTTP adapter
  (`predator-core/mobile/dashboard.go`) that speaks Arman's dashboard
  API on `127.0.0.1:10820`, protected by a fresh `X-Local-Token` on
  every start.

This is an unofficial, GPL-licensed combination. It is not affiliated
with Mozilla, Predator VPN, or Arman VPN. See `NOTICE.md` for the
sources, the license, and what was changed.

## Get the APK (no local toolchain needed)

1. Create a **new, empty, public or private repository** on GitHub
   (for example `merged-vpn`).
2. Push this folder to it — see `PUSH.md` for the exact commands.
3. Open the repo on GitHub: **Actions > Build Android > Run workflow**.
4. When the run finishes (green), open the run and download the
   **android** artifact. It contains:
   - `Merged-VPN-2.0.0-Android-arm64-v8a.apk` — most phones (install this one)
   - `Merged-VPN-2.0.0-Android-armeabi-v7a.apk` — older 32-bit phones
   - `Merged-VPN-2.0.0-Android-x86_64.apk` — emulators / x86 phones
   - `Merged-VPN-2.0.0-Android-universal.apk` — all architectures, bigger
5. Copy the APK to your phone and install it (allow "install unknown
   apps" for the file manager / browser once).

Without signing secrets the APKs are signed with the **debug key** —
fine for installing on your own phone, not for publishing. For a
release key, add the `ANDROID_KEYSTORE_BASE64` and
`ANDROID_KEYSTORE_PASSWORD` secrets (see the workflow file).

## Before you publish anything

- The in-app update check points at `meiyou313/merged-vpn`
  (`app/lib/app_info.dart`).
- This build does **not** publish a GitHub release by itself.

## Test it (first run)

1. Sign in with an **existing** Firefox account (email + password).
   New-account sign-in may fail with HTTP 406 — that is an upstream
   Predator v2.0.0 limitation, not a build problem (see `NOTICE.md`).
2. Pick a location, turn the VPN on, and check your IP changed.
3. Try split tunneling in Settings (exclude / include apps) and the
   Quick Settings tile.

## Layout

```
app/                  Flutter UI (Arman), Android target
  android/app/...     PredatorBridge.kt, PredatorApp.java, FoxVpnService.java
predator-core/        Predator Go core (android/core) + dashboard.go adapter
.github/workflows/   build-android.yml — the CI that builds the APK
scripts/              android-notices.cjs — third-party Go notices
```

## Build locally (optional)

Install Go 1.26.9, JDK 17, Flutter 3.47.7, Android SDK 36 +
NDK 28.0.13004108, then follow the steps in
`.github/workflows/build-android.yml` (gomobile bind, then
`flutter build apk --release --split-per-abi`).

## Status — what was NOT verified here

The bridge and the adapter were written from the two projects'
contracts and checked statically (see `PUSH.md`); they were **not**
compiled or run in this workspace — it has no Go/Flutter/Android
toolchain. The first GitHub Actions run is the real compile check.
If it fails, the log names the file and line; fix that one thing
and push again. Known risks are listed in `PUSH.md`.
