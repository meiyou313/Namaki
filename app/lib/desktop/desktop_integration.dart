import 'dart:async';
import 'dart:io';
import 'dart:ui';

import 'package:flutter/material.dart';

import '../app_settings.dart';
import '../core/core_controller.dart';
import '../core/updates.dart';
import '../core/core_process.dart';
import '../core/models.dart';
import '../core/tun_service.dart';
import '../l10n/app_localizations.dart';
import '../ui/connection_mode.dart';
import '../ui/widgets.dart';
import 'close_dialog.dart';
import 'desktop_bridge.dart';

/// The desktop behavior around the window: the tray icon and its short
/// menu (Open, Set system proxy, Clear system proxy, Quit, as in v2rayN),
/// notifications while the window is hidden, and what the close button
/// does (ask, minimize to the tray, or quit).
class DesktopIntegration {
  DesktopIntegration({
    required this.core,
    required this.settings,
    required this.process,
    required this.navigatorKey,
    this.startHidden = false,
    this.autostarted = false,
    this.manageAutostart = true,
  }) : _hidden = startHidden;

  final CoreController core;
  final AppSettings settings;
  final CoreProcess process;
  final GlobalKey<NavigatorState> navigatorKey;

  /// The window stays in the tray: started at sign-in, or by an update in
  /// the tray.
  final bool startHidden;

  /// Started at sign-in ([startHidden] too): it may connect by itself.
  final bool autostarted;

  /// Whether this copy owns the Run entry; not for a copy started with
  /// another data folder (development and tests).
  final bool manageAutostart;

  late final DesktopBridge _bridge = DesktopBridge(
    onCloseRequested: _onClose,
    onTrayClicked: show,
    onTrayMenu: _onMenu,
    onShown: () => _hidden = false,
  );

  // Menu ids.
  static const _open = 1, _set = 2, _clear = 3, _quit = 4, _tun = 5;

  String _lastTray = '';
  bool _closing = false;
  bool _quitting = false;

  /// The window is hidden in the tray; notifications show only then.
  bool _hidden;

  bool? _autostartApplied;

  /// Auto-connect was asked for, or is not wanted, in this run.
  bool _autoConnectDone = false;

  // What the last status showed, for notifications on changes.
  Phase? _lastPhase;
  String? _lastAccount;
  int _lastReauth = 0;

  void start() {
    // A version found before this start was announced then.
    _notifiedUpdate = settings.updateVersion;
    Updates.available.addListener(_notifyUpdate);
    Updates.quitApp = quit;
    Updates.windowHidden = () => _hidden;
    // An install at the start that did not happen: connect as usual.
    Updates.installingAtStart.addListener(_onChange);
    core.addListener(_onChange);
    settings.addListener(_updateTray);
    settings.addListener(_applyAutostart);
    _updateTray();
    _applyAutostart();
    // On Linux the Flutter engine takes the window's close before the
    // runner's own handler (DesktopBridge's closeRequested) and asks to
    // exit; without an answer the app ended at once, VPN and all. The
    // binding keeps the listener for the app's life.
    if (Platform.isLinux) {
      AppLifecycleListener(onExitRequested: onExitRequested);
    }
  }

  /// The close button, as the engine asks it on Linux: what the setting or
  /// the close dialog says, unless the app is quitting already.
  @visibleForTesting
  Future<AppExitResponse> onExitRequested() async {
    if (_quitting) return AppExitResponse.exit;
    unawaited(_onClose());
    return AppExitResponse.cancel;
  }

  /// Keeps the Run entry equal to the setting, pointing at this copy of the
  /// app (a new version takes it over when it runs).
  void _applyAutostart() {
    if (!manageAutostart || _autostartApplied == settings.autostart) return;
    _autostartApplied = settings.autostart;
    _bridge.setAutostart(
      enabled: settings.autostart,
      command: '"${Platform.resolvedExecutable}" --autostart',
    );
  }

  AppLocalizations get _l =>
      lookupAppLocalizations(settings.locale ?? _systemLocale());

  static Locale _systemLocale() => AppSettings.localeOf(
    AppSettings.languageFor(PlatformDispatcher.instance.locale),
  );

  /// The icon file inside the build's flutter_assets: an .ico on Windows,
  /// a .png on Linux and macOS (inside the app bundle there).
  static String _icon(String name) {
    final sep = Platform.pathSeparator;
    final dir = File(Platform.resolvedExecutable).parent.path;
    return [
      dir,
      if (Platform.isMacOS) ...[
        '..',
        'Frameworks',
        'App.framework',
        'Resources',
      ] else
        'data',
      'flutter_assets',
      'assets',
      'tray',
      Platform.isWindows ? '$name.ico' : '$name.png',
    ].join(sep);
  }

  String? _notifiedUpdate;

  /// What the update before this start did was told.
  bool _toldUpdateResult = false;

  /// After an update that started the app in the tray: whether it
  /// installed (the home page says so otherwise).
  void _notifyUpdateResult() {
    if (_toldUpdateResult) return;
    final last = core.status?.update?.last;
    if (!core.connected || core.status == null) return;
    _toldUpdateResult = true;
    if (last == null || !_hidden) return;
    _bridge.notify(
      _l.appTitle,
      last.ok ? _l.updateDone(last.to) : _l.updateRolledBack(last.to),
    );
  }

  /// A newer version found while the window is in the tray (the home page
  /// shows it otherwise): one notification per version, unless closed.
  void _notifyUpdate() {
    final release = Updates.available.value;
    if (release == null || !_hidden) return;
    if (release.version == _notifiedUpdate ||
        release.version == settings.dismissedUpdate) {
      return;
    }
    _notifiedUpdate = release.version;
    _bridge.notify(_l.appTitle, _l.updateAvailable(release.version));
  }

  void _onChange() {
    _autoConnect();
    _notifyChanges();
    _notifyUpdateResult();
    _updateTray();
  }

  /// Started with the computer and set to connect: turns the proxy on once,
  /// as soon as the core answers with it off. The core waits for a network
  /// and, with the system proxy set, for a connection that works. Not while
  /// a new version installs at the start: the new version connects.
  void _autoConnect() {
    if (_autoConnectDone || Updates.installingAtStart.value) return;
    if (!autostarted || !settings.autostart || !settings.autoConnect) {
      _autoConnectDone = true;
      return;
    }
    final status = core.status;
    if (!core.connected || status == null) return;
    _autoConnectDone = true;
    if (status.state.phase != Phase.off || status.totalAccounts == 0) return;
    core.autoConnect().catchError((_) {});
  }

  void _notifyChanges() {
    final status = core.status;
    if (status == null || !core.connected) return;
    final l = _l;
    // Off while the computer's apps do not use the VPN, as the home page
    // says.
    final phase =
        status.state.phase == Phase.running &&
            (!status.computerUsesVpn || status.autoConnecting)
        ? Phase.unknown
        : status.state.phase;
    final account = status.activeAccount?.id;
    final reauth = status.accountCounts['reauth_required'] ?? 0;
    final seen = _lastPhase != null;
    if (seen && _hidden) {
      if (phase != _lastPhase) {
        if (phase == Phase.running) {
          // As the home page names it: the exit's country for the
          // recommended location.
          final place = homeLocation(l, status, connected: true).name;
          _bridge.notify(l.stateConnected, place);
        } else if (phase == Phase.error) {
          final look = PhaseLook.of(l, status);
          _bridge.notify(l.stateError, look.hint);
        }
      }
      if (account != null && _lastAccount != null && account != _lastAccount) {
        _bridge.notify(
          l.notifySwitchedTitle,
          l.notifySwitchedBody(status.activeAccount!.name),
        );
      }
      if (reauth > _lastReauth) {
        _bridge.notify(l.notifySignInTitle, l.notifySignInBody);
      }
    }
    _lastPhase = phase;
    _lastAccount = account;
    _lastReauth = reauth;
  }

  /// While connecting, the tray icon turns through its four frames; while
  /// waiting, it shows the first.
  Timer? _spin;
  int _frame = 1;

  void _updateTray() {
    final l = _l;
    final status = core.status;
    final connected = core.connected && status != null;
    String icon;
    final String state;
    if (!connected) {
      icon = 'off';
      state = core.unavailable != null
          ? describeProblem(l, core.unavailable!)
          : l.coreConnecting;
    } else {
      state = PhaseLook.of(l, status).label;
      icon = switch (status.state.phase) {
        Phase.running when status.autoConnecting => 'busy',
        Phase.running when status.proxy?.sleepingSince != null => 'idle',
        Phase.running => 'on',
        Phase.error => 'error',
        Phase.off => 'off',
        // A wait can last hours (no internet, or no account until the
        // monthly reset): one still frame rather than a spin.
        Phase.waiting => 'busy1',
        _ => 'busy',
      };
    }
    if (icon == 'busy') {
      _spin ??= Timer.periodic(const Duration(milliseconds: 300), (_) {
        _frame = _frame % 4 + 1;
        _updateTray();
      });
      icon = 'busy$_frame';
    } else {
      _spin?.cancel();
      _spin = null;
    }
    final place = status == null
        ? ''
        : homeLocation(l, status, connected: true).name;
    final tooltip = [
      l.appTitle,
      state,
      if (connected && place.isNotEmpty) place,
    ].join(' · ');
    final canSet = connected && status.systemProxySupported;
    // TUN mode also where the desktop has no system proxy setting.
    final canTun = connected && status.tunSupported;
    final items = [
      TrayItem(_open, l.trayOpen),
      const TrayItem.separator(),
      TrayItem(
        _clear,
        l.trayProxyOnly,
        enabled: canSet || canTun,
        checked:
            (canSet || canTun) && !status.systemProxySet && !status.tunMode,
      ),
      if (status?.systemProxySupported ?? true)
        TrayItem(
          _set,
          l.traySetSystemProxy,
          enabled: canSet,
          checked: canSet && status.systemProxySet,
        ),
      if (canTun)
        TrayItem(
          _tun,
          l.trayTunMode,
          enabled: !core.installingTun,
          checked: status.tunMode,
        ),
      const TrayItem.separator(),
      TrayItem(_quit, l.trayQuit),
    ];
    final rtl = AppSettings.rtlLanguages.contains(l.localeName);
    final key =
        '$icon|$tooltip|$rtl|${items.map((i) => i.signature).join('|')}';
    if (key == _lastTray) return;
    _lastTray = key;
    _bridge.setTray(
      icon: _icon(icon),
      tooltip: tooltip,
      items: items,
      rtl: rtl,
    );
  }

  Future<void> show() async {
    _hidden = false;
    await _bridge.show();
  }

  Future<void> _hide() async {
    _hidden = true;
    await _bridge.hide();
  }

  void _onMenu(int id) {
    switch (id) {
      case _open:
        show();
      case _set:
        core.setSystemProxy('set').catchError((_) {});
      case _clear:
        core.setSystemProxy('clear').catchError((_) {});
      case _tun:
        _chooseTun();
      case _quit:
        quit();
    }
  }

  /// TUN mode from the tray: with the window open, as on the home page (it
  /// says why an install did not happen); hidden, installing the service
  /// first when needed, without a word but the system's prompt.
  Future<void> _chooseTun() async {
    final context = navigatorKey.currentContext;
    if (!_hidden && context != null && context.mounted) {
      await chooseConnectionMode(context, core, 'tun');
      return;
    }
    if (!(core.status?.tun?.installed ?? false) &&
        await core.installTunService(prompt: _l.tunInstallPrompt)
            is! TunServiceDone) {
      return;
    }
    await core.setSystemProxy('tun').catchError((_) {});
  }

  Future<void> _onClose() async {
    if (_closing) return;
    _closing = true;
    try {
      var action = settings.closeAction;
      if (action == CloseAction.ask) {
        final context = navigatorKey.currentContext;
        if (context == null) return;
        final choice = await showCloseDialog(context);
        if (choice == null) return;
        action = choice.action;
        if (choice.remember) settings.update(closeAction: action);
        // A hidden window draws nothing: let the dialog finish closing
        // (showDialog's 150 ms) first, or its last frame shows for a moment
        // when the window opens again from the tray.
        await Future<void>.delayed(const Duration(milliseconds: 200));
        await WidgetsBinding.instance.endOfFrame;
      }
      if (action == CloseAction.quit) {
        await quit();
      } else {
        await _hide();
      }
    } finally {
      _closing = false;
    }
  }

  /// Stops the core this app started (the VPN goes off and the system proxy
  /// comes back), then ends the app.
  Future<void> quit() async {
    if (_quitting) return;
    _quitting = true;
    core.stopPolling();
    await process.stop(core.api);
    await _bridge.quit();
  }
}
