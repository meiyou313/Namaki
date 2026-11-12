import 'dart:async';

import 'package:flutter/material.dart';

import '../app_settings.dart';
import '../core/core_api.dart';
import '../core/core_controller.dart';
import '../core/models.dart';
import '../l10n/app_localizations.dart';
import 'format.dart';
import 'signin_dialog.dart';
import 'widgets.dart';

/// The server list, one choice in one list: Recommended and Fastest, which
/// the app picks, then the locations with their real delay. A tap on a row
/// chooses it (`proxy.country`: empty for Mozilla's recommended location,
/// `fastest` for the lowest real delay, otherwise a country).
class ServersPage extends StatefulWidget {
  const ServersPage({super.key, required this.core, required this.settings});

  final CoreController core;
  final AppSettings settings;

  @override
  State<ServersPage> createState() => _ServersPageState();
}

/// How old measured delays are before the page measures them again when
/// it opens.
const _pingStale = Duration(minutes: 10);

class _ServersPageState extends State<ServersPage> {
  List<Location>? _locations;
  DateTime? _pingedAt;
  String _country = '';
  bool _testing = false;

  /// The locations' poll while a delay test runs.
  Timer? _pollTimer;

  /// The locations measured in the test now running, so their numbers show
  /// at once instead of the spinner.
  final _measured = <String>{};

  /// The choice being saved ('' recommended, 'fastest', or a code).
  String? _busy;
  String? _summary;

  CoreController get _core => widget.core;

  /// No account yet: the delays cannot be measured (Mozilla's servers
  /// answer only an account's pass), so the page says to add one instead.
  bool get _noAccounts => _core.status?.totalAccounts == 0;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _pollTimer?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final list = await _core.call((api) => api.locations());
      final config = await _core.settings();
      if (!mounted) return;
      setState(() {
        _locations = list.locations;
        _pingedAt = list.pingedAt;
        if (config['proxy'] case {'country': final String country}) {
          _country = country;
        }
      });
      // Delays measured a while ago, or never: measured again at once (the
      // core does it also while the proxy is off).
      final at = _pingedAt;
      if (!_noAccounts &&
          (at == null || DateTime.now().difference(at) > _pingStale)) {
        unawaited(_test());
      }
    } on CoreApiException catch (e) {
      if (mounted) showError(context, e.message);
    } on CoreUnavailable {
      // The shell shows it.
    }
  }

  Future<void> _choose(String country) async {
    if (_busy != null || _country.toLowerCase() == country.toLowerCase()) {
      return;
    }
    setState(() => _busy = country);
    final ok = await runShowingErrors(
      context,
      () => _core.setLocation(country),
    );
    if (!mounted) return;
    setState(() {
      _busy = null;
      if (ok) _country = country;
    });
  }

  Future<void> _test() async {
    if (_testing) return;
    final l = AppLocalizations.of(context);
    final codes = [
      for (final loc in _locations ?? const <Location>[]) loc.code,
    ];
    setState(() {
      _testing = true;
      _summary = null;
      _measured.clear();
    });
    // The core measures the countries and answers only when all are done;
    // meanwhile its locations answer shows each number the moment it was
    // measured, so poll it while the test runs.
    final probe = _core.call(
      (api) => api.pingLocations(codes, target: widget.settings.pingTarget),
    );
    _pollTimer?.cancel();
    _pollTimer = Timer.periodic(const Duration(milliseconds: 1500), (_) {
      unawaited(_poll());
    });
    try {
      final result = await probe;
      if (!mounted) return;
      _pollTimer?.cancel();
      _pollTimer = null;
      final tested = {for (final loc in result.locations) loc.code: loc};
      setState(() {
        _locations = [
          for (final loc in _locations ?? const <Location>[])
            tested[loc.code] ?? loc,
        ];
        _pingedAt = result.pingedAt ?? DateTime.now();
        final answered = result.locations.where((loc) => loc.answered).length;
        _summary = l.delaySummary(answered, result.locations.length);
      });
    } on CoreApiException {
      // The core's own words are for the log; the user gets what to do.
      if (mounted) showError(context, l.delayTestFailed);
    } on CoreUnavailable catch (e) {
      if (mounted) showError(context, describeProblem(l, e));
    } finally {
      _pollTimer?.cancel();
      _pollTimer = null;
      if (mounted) setState(() => _testing = false);
    }
  }

  /// Merges the locations the core has measured so far into the list, so
  /// each row shows its number the moment it lands. A failed poll is
  /// silent; the test's own answer carries the error, if any.
  Future<void> _poll() async {
    if (!mounted || !_testing) return;
    try {
      final list = await _core.call((api) => api.locations());
      if (!mounted || !_testing) return;
      final measured = {for (final loc in list.locations) loc.code: loc};
      final answered = {
        for (final loc in list.locations)
          if (loc.answered) loc.code,
      };
      setState(() {
        _locations = [
          for (final loc in _locations ?? const <Location>[])
            measured[loc.code] ?? loc,
        ];
        _measured.addAll(answered);
      });
    } on CoreApiException {
      // Silent: the next poll tries again.
    } on CoreUnavailable {
      // Silent: same.
    }
  }

  /// The probe address for the delay test: saved, then measured at once
  /// with it, so the menu shows that address's numbers.
  void _pickTarget(String target) {
    if (target == widget.settings.pingTarget || _testing) return;
    widget.settings.update(pingTarget: target);
    unawaited(_test());
  }

  /// Every country, without Mozilla's recommended location (the choice
  /// above), fastest first, then by name in the app's language.
  List<Location> _countries(AppLocalizations l) {
    final list = [
      for (final loc in _locations ?? const <Location>[])
        if (!isRecommendedPlace(loc.code)) loc,
    ];
    list.sort((a, b) {
      final da = a.answered ? a.pingMs : 1 << 30;
      final db = b.answered ? b.pingMs : 1 << 30;
      if (da != db) return da.compareTo(db);
      return nameSortKey(
        l.localeName,
        locationName(l, a.code, a.name),
      ).compareTo(nameSortKey(l.localeName, locationName(l, b.code, b.name)));
    });
    return list;
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final chosen = _country.toLowerCase();
    final countries = _countries(l);
    final recommended = (_locations ?? const <Location>[])
        .where((loc) => isRecommendedPlace(loc.code))
        .firstOrNull;
    final delayLine = _testing
        ? l.testingDelay
        : _summary ??
            (_pingedAt == null ? null : l.testedAgo(formatAgo(l, _pingedAt!)));
    final noAccounts = _noAccounts;
    return ListView(
      padding: pagePadding(context),
      children: [
        PageTitle(l.navServers),
        if (noAccounts) ...[
          NoAccountsPanel(
            onAdd: () => startAddAccount(context, _core, widget.settings),
          ),
          const SizedBox(height: 12),
        ],
        _SectionHeader(title: l.autoSection),
        _ChoiceCard(
          children: [
            _ChoiceRow(
              leading: const CountryFlag(code: 'REC', width: 36),
              title: l.modeRecommended,
              subtitle: l.modeRecommendedHint,
              delay: recommended == null
                  ? null
                  : _Delay(
                      location: recommended,
                      testing: _testing,
                      fresh: _measured.contains(recommended.code),
                    ),
              selected: chosen == '',
              busy: _busy == '',
              onTap: () => _choose(''),
            ),
            _ChoiceRow(
              leading: const _IconTile(icon: Icons.bolt_rounded),
              title: l.modeFastest,
              subtitle: l.modeFastestHint,
              selected: chosen == 'fastest',
              busy: _busy == 'fastest',
              onTap: () => _choose('fastest'),
            ),
          ],
        ),
        const SizedBox(height: 20),
        _SectionHeader(
          title: l.locationsSection,
          action: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              _PingTargetMenu(
                current: widget.settings.pingTarget,
                enabled: !_testing && _locations != null && !noAccounts,
                onSelected: _pickTarget,
              ),
              // While a test runs the button is just a spinner: the long
              // "Testing…" label squeezed the Locations title into three
              // lines. The status lives in the line under the header.
              if (_testing)
                const Padding(
                  padding: EdgeInsets.all(11),
                  child: SizedBox.square(
                    dimension: 18,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                )
              else
                TextButton.icon(
                  onPressed: _locations != null && !noAccounts ? _test : null,
                  icon: const Icon(Icons.speed_rounded, size: 18),
                  label: Text(l.testDelay),
                ),
            ],
          ),
        ),
        if (delayLine != null)
          Padding(
            padding: const EdgeInsetsDirectional.fromSTEB(4, 0, 4, 8),
            child: Text(delayLine, style: muted),
          ),
        if (_locations == null)
          const Padding(
            padding: EdgeInsets.all(24),
            child: Center(child: CircularProgressIndicator()),
          )
        else if (countries.isEmpty)
          Panel(child: Text(l.noServers, style: muted))
        else
          _ChoiceCard(
            children: [
              for (final loc in countries)
                _ChoiceRow(
                  leading: CountryFlag(code: loc.code, width: 36),
                  title: locationName(l, loc.code, loc.name),
                  subtitle: _subtitle(loc),
                  delay: _Delay(
                    location: loc,
                    testing: _testing,
                    fresh: _measured.contains(loc.code),
                  ),
                  selected: chosen == loc.code.toLowerCase(),
                  busy: _busy?.toLowerCase() == loc.code.toLowerCase(),
                  onTap: () => _choose(loc.code),
                ),
            ],
          ),
      ],
    );
  }

  /// A location's second line: its city when it says more than the
  /// country.
  String? _subtitle(Location loc) =>
      loc.city.isNotEmpty && loc.city != loc.name ? loc.city : null;
}

class _SectionHeader extends StatelessWidget {
  const _SectionHeader({required this.title, this.action});

  final String title;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsetsDirectional.fromSTEB(4, 8, 0, 6),
      child: Row(
        children: [
          Expanded(
            child: Text(
              title,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.labelLarge?.copyWith(
                color: theme.colorScheme.primary,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
          ?action,
        ],
      ),
    );
  }
}

/// Rows of choices in one card, divided by thin lines.
class _ChoiceCard extends StatelessWidget {
  const _ChoiceCard({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) => Card(
    margin: EdgeInsets.zero,
    clipBehavior: Clip.antiAlias,
    child: Column(
      children: [
        for (var i = 0; i < children.length; i++) ...[
          if (i > 0) const Divider(height: 1),
          children[i],
        ],
      ],
    ),
  );
}

/// An icon in the size of a flag, for a choice that is not a country.
class _IconTile extends StatelessWidget {
  const _IconTile({required this.icon});

  final IconData icon;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: 36,
      height: 27,
      decoration: BoxDecoration(
        color: scheme.tertiaryContainer,
        borderRadius: BorderRadius.circular(4),
      ),
      child: Icon(icon, size: 20, color: scheme.onTertiaryContainer),
    );
  }
}

/// One choice: what it is, its delay, and a radio mark; a tap chooses it.
class _ChoiceRow extends StatelessWidget {
  const _ChoiceRow({
    required this.leading,
    required this.title,
    required this.selected,
    required this.busy,
    required this.onTap,
    this.subtitle,
    this.delay,
  });

  final Widget leading;
  final String title;
  final String? subtitle;
  final Widget? delay;
  final bool selected;
  final bool busy;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    return Material(
      color: selected
          ? scheme.primary.withValues(alpha: 0.08)
          : Colors.transparent,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsetsDirectional.fromSTEB(14, 12, 12, 12),
          child: Row(
            children: [
              leading,
              const SizedBox(width: 16),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      style: theme.textTheme.titleSmall?.copyWith(
                        fontWeight: selected
                            ? FontWeight.w700
                            : FontWeight.w500,
                      ),
                      overflow: TextOverflow.ellipsis,
                    ),
                    if (subtitle != null) ...[
                      const SizedBox(height: 2),
                      Text(
                        subtitle!,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: scheme.onSurfaceVariant,
                        ),
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ],
                ),
              ),
              if (delay != null) ...[const SizedBox(width: 10), delay!],
              const SizedBox(width: 6),
              SizedBox.square(
                dimension: 24,
                child: busy
                    ? const Padding(
                        padding: EdgeInsets.all(3),
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : Icon(
                        selected
                            ? Icons.radio_button_checked_rounded
                            : Icons.radio_button_unchecked_rounded,
                        color: selected ? scheme.primary : scheme.outline,
                      ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// A location's real delay as a small badge in its color. While the delays
/// are measured, a small spinner instead of the last results, which may be
/// old (a measurement while offline answers nothing) — except a number
/// measured in this run ([fresh]), which shows at once. A location that did
/// not answer is grey, not red: it says nothing is wrong with the app.
class _Delay extends StatelessWidget {
  const _Delay({required this.location, required this.testing, this.fresh = false});

  final Location location;
  final bool testing;

  /// True when this location was measured in the test now running.
  final bool fresh;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.colorScheme.onSurfaceVariant;
    final loc = location;
    final showValue = !testing || fresh;
    final (text, color) = !showValue
        ? ('', muted)
        : loc.answered
        ? (l.milliseconds(loc.pingMs), delayColor(loc.pingMs))
        : loc.pingError.isNotEmpty
        ? (l.noAnswer, muted)
        : ('—', muted);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(999),
      ),
      child: showValue
          ? Text(
              text,
              style: theme.textTheme.labelMedium?.copyWith(
                color: color,
                fontWeight: FontWeight.w700,
              ),
            )
          : SizedBox.square(
              dimension: 12,
              child: CircularProgressIndicator(strokeWidth: 1.5, color: muted),
            ),
    );
  }
}

/// The probe address the delay test measures through, as a small settings
/// button beside the test button. It opens a bottom sheet — the old popup
/// menu was cramped and plain — with the services to choose from and a
/// note on why the choice matters. The names are the services' own, so no
/// translation.
class _PingTargetMenu extends StatelessWidget {
  const _PingTargetMenu({
    required this.current,
    required this.enabled,
    required this.onSelected,
  });

  final String current;
  final bool enabled;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    return IconButton(
      icon: const Icon(Icons.settings_rounded, size: 20),
      tooltip: l.pingTargetTitle,
      onPressed: enabled ? () => _showSheet(context) : null,
    );
  }

  Future<void> _showSheet(BuildContext context) async {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final picked = await showModalBottomSheet<String>(
      context: context,
      useSafeArea: true,
      isScrollControlled: true,
      backgroundColor: scheme.surface,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(20)),
      ),
      builder: (context) => SafeArea(
        child: SingleChildScrollView(
          child: Padding(
            padding: const EdgeInsets.fromLTRB(20, 12, 20, 20),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Center(
                  child: Container(
                    width: 36,
                    height: 4,
                    decoration: BoxDecoration(
                      color: scheme.onSurfaceVariant.withValues(alpha: 0.4),
                      borderRadius: BorderRadius.circular(2),
                    ),
                  ),
                ),
                const SizedBox(height: 16),
                Text(
                  l.pingTargetTitle,
                  style: theme.textTheme.titleLarge?.copyWith(
                    fontWeight: FontWeight.w700,
                  ),
                ),
                const SizedBox(height: 6),
                Text(
                  l.pingTargetHint,
                  style: theme.textTheme.bodyMedium?.copyWith(
                    color: scheme.onSurfaceVariant,
                  ),
                ),
                const SizedBox(height: 12),
                for (final entry in _pingTargets.entries)
                  _PingTargetRow(
                    name: entry.value,
                    selected: entry.key == current,
                    onTap: () => Navigator.pop(context, entry.key),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
    if (picked != null) onSelected(picked);
  }
}

/// One probe address in the picker: the service's name and a check when
/// it is the one the delay test measures through.
class _PingTargetRow extends StatelessWidget {
  const _PingTargetRow({
    required this.name,
    required this.selected,
    required this.onTap,
  });

  final String name;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    return Material(
      color: selected
          ? scheme.primary.withValues(alpha: 0.08)
          : Colors.transparent,
      borderRadius: BorderRadius.circular(12),
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 13),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  name,
                  style: theme.textTheme.titleMedium?.copyWith(
                    fontWeight: selected ? FontWeight.w700 : FontWeight.w500,
                  ),
                ),
              ),
              Icon(
                selected
                    ? Icons.check_circle_rounded
                    : Icons.circle_outlined,
                color: selected ? scheme.primary : scheme.outline,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// The probe addresses the delay test can measure through: the short id
/// the core knows, and the service's own name.
const _pingTargets = <String, String>{
  'telegram': 'Telegram',
  'youtube': 'YouTube',
  'instagram': 'Instagram',
  'x': 'X',
  'facebook': 'Facebook',
  'tiktok': 'TikTok',
  'bbc': 'BBC',
};
