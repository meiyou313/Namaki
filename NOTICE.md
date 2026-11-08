# Notices

Merged VPN is a combination of two GPL-licensed open-source projects.
It is **not** affiliated with, endorsed by, or connected to Mozilla,
and it is **not** Predator VPN or Arman VPN.

## Sources

- **PredatorVPN v2.0.0** — `https://github.com/erfan-tahvilian/PredatorVPN`
  (tag `v2.0.0`, commit `1ccce204ac9cb6edfaa1ceb228e345f3d5575489`)
  Copyright (C) 2026 Erfan Tahvilian. License: GPL-3.0-or-later.
  Used as: the Android Go core (`predator-core/`), the sing-box TUN
  service (`FoxVpnService.java`), the encrypted vault (`CryptoVault.java`),
  and the application host (`PredatorApp.java`, adapted from `FoxApp.java`).

- **Arman VPN client 1.0.14** — `https://github.com/armanlife/arman-vpn-client`
  (commit `95f8691d45bcf6c7451c677ed8127908537b621b`)
  License: GPL-3.0.
  Used as: the Flutter UI (`app/`), its Android shell (`MainActivity.kt`,
  `QuickTile.kt`, `ConnectWidget.kt`), and the GitHub Actions build
  pattern. Arman's own Go core (`core/`) is **not** used; it was replaced
  by Predator's core plus the adapter in `predator-core/mobile/dashboard.go`.

## License

The combined work is distributed under the **GNU General Public License
v3.0 or later** (`LICENSE`), which covers both sources. The full license
texts of the two projects are the same GPL-3.0 text and are kept in
`LICENSE`. Third-party Go dependency notices are bundled into the APK
by `scripts/android-notices.cjs` (run in CI).

## What was changed, in brief

- New package and app name: `com.mergedvpn.client` / "Merged VPN".
- New bridge (`PredatorBridge.kt`, `PredatorApp.java`): Flutter's
  MethodChannel `app/android` now drives Predator's Backend and
  `FoxVpnService` instead of Arman's `mobile.Mobile` / `CoreService`.
- New adapter (`predator-core/mobile/dashboard.go`): an Arman-compatible
  HTTP dashboard on `127.0.0.1:10820`, protected by a fresh
  `X-Local-Token` on every start.
- Browser sign-in removed (Predator v2.0.0 removed it upstream):
  the Flutter UI offers email/password sign-in only, and
  `/api/signin/browser` answers 501.
- The in-app update feed is disconnected: `lib/app_info.dart`
  points at `YOUR-GITHUB-USER/merged-vpn` — replace it with your own
  repository before publishing any update.

## Known upstream limitation

Predator v2.0.0's own release notes say new-account sign-in may be
rejected with HTTP 406 by the external provider and that browser
sign-in was removed; existing sessions last until their tokens expire.
Merging does not fix that. Test with an existing Firefox account first.
