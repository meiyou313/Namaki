import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../app_settings.dart';
import '../core/core_api.dart';
import '../core/core_controller.dart';
import '../core/models.dart';
import '../l10n/app_localizations.dart';
import 'format.dart';
import 'widgets.dart';

/// The proxy's connections, as the core lists them (memory only): the open
/// ones and the last closed ones, in the order they started (new ones at
/// the bottom, followed unless the user turns that off), read once a
/// second. The user picks the columns (kept in [settings]); the destination
/// is always shown, and the table scrolls sideways when the window is too
/// narrow.
class ConnectionsView extends StatefulWidget {
  const ConnectionsView({super.key, required this.core, this.settings});

  final CoreController core;
  final AppSettings? settings;

  @override
  State<ConnectionsView> createState() => _ConnectionsViewState();
}

/// A column: its key in AppSettings.hiddenConnectionColumns, the least
/// width it gets, and its share of the room left over.
class _Col {
  const _Col(this.key, this.width, this.flex);

  final String key;
  final double width;
  final double flex;
}

const _destination = 'destination';
const _columns = [
  _Col('time', 72, 0.6),
  _Col(_destination, 200, 4),
  _Col('route', 84, 0.6),
  _Col('inbound', 64, 0.5),
  _Col('protocol', 84, 0.6),
  _Col('traffic', 156, 1),
  _Col('duration', 72, 0.5),
];

String _title(AppLocalizations l, String key) => switch (key) {
  'time' => l.connTime,
  'route' => l.connRoute,
  'inbound' => l.connInbound,
  'protocol' => l.connProtocol,
  'traffic' => l.connTraffic,
  'duration' => l.connDuration,
  _ => l.connDestination,
};

class _ConnectionsViewState extends State<ConnectionsView> {
  List<ProxyConnection> _all = const [];
  Timer? _timer;
  bool _reading = false;
  String _text = '';
  bool _follow = true;
  final _sideways = ScrollController();
  final _down = ScrollController();

  /// Hidden columns when there are no settings to keep them in.
  Set<String> _hidden = {};

  Set<String> get _hiddenColumns =>
      widget.settings?.hiddenConnectionColumns ?? _hidden;

  @override
  void initState() {
    super.initState();
    _read();
    _timer = Timer.periodic(const Duration(seconds: 1), (_) => _read());
  }

  @override
  void dispose() {
    _timer?.cancel();
    _sideways.dispose();
    _down.dispose();
    super.dispose();
  }

  Future<void> _read([
    Future<List<ProxyConnection>> Function(CoreApi api)? request,
  ]) async {
    if (_reading && request == null) return;
    _reading = true;
    try {
      final list = await widget.core.call(
        request ?? (api) => api.connections(),
      );
      if (!mounted) return;
      setState(() => _all = list);
      if (_follow) {
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (_down.hasClients) _down.jumpTo(_down.position.maxScrollExtent);
        });
      }
    } on CoreApiException {
      // The next tick tries again.
    } on CoreUnavailable {
      // The shell shows it.
    } finally {
      _reading = false;
    }
  }

  /// Clears the list: the core forgets the closed connections, and this
  /// tab leaves out the open ones it listed.
  Future<void> _clear() async {
    final last = _all.isEmpty ? 0 : _all.last.id;
    setState(() {
      widget.core.connectionsClearedAfter = math.max(
        widget.core.connectionsClearedAfter,
        last,
      );
    });
    await _read((api) => api.clearConnections());
  }

  void _toggle(String key) {
    final hidden = {..._hiddenColumns};
    if (!hidden.remove(key)) hidden.add(key);
    if (widget.settings case final settings?) {
      settings.update(hiddenConnectionColumns: hidden);
    }
    setState(() => _hidden = hidden);
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final text = _text.trim().toLowerCase();
    final after = widget.core.connectionsClearedAfter;
    final shown = [
      for (final c in _all)
        if (c.id > after) c,
    ];
    final visible = [
      for (final c in shown)
        if (text.isEmpty ||
            c.target.toLowerCase().contains(text) ||
            c.client.toLowerCase().contains(text))
          c,
    ];
    final open = shown.where((c) => c.open).length;
    final hidden = _hiddenColumns;
    final columns = [
      for (final c in _columns)
        if (c.key == _destination || !hidden.contains(c.key)) c,
    ];
    final now = DateTime.now();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        // One row: the filter, how many are open, auto-scroll, then the
        // other tools as icons (their names in tooltips).
        Row(
          children: [
            Flexible(
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 320),
                child: TextField(
                  textDirection: TextDirection.ltr,
                  decoration: InputDecoration(
                    isDense: true,
                    prefixIcon: const Icon(Icons.filter_list_rounded, size: 20),
                    hintText: l.filter,
                    border: const OutlineInputBorder(),
                  ),
                  onChanged: (v) => setState(() => _text = v),
                ),
              ),
            ),
            const SizedBox(width: 16),
            Text(
              l.connectionsOpen(open),
              style: TextStyle(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
              ),
            ),
            const Spacer(),
            LabeledSwitch(
              label: l.follow,
              value: _follow,
              onChanged: (v) => setState(() => _follow = v),
            ),
            const SizedBox(width: 4),
            MenuAnchor(
              menuChildren: [
                for (final c in _columns)
                  if (c.key != _destination)
                    CheckboxMenuButton(
                      value: !hidden.contains(c.key),
                      closeOnActivate: false,
                      onChanged: (_) => _toggle(c.key),
                      child: Text(_title(l, c.key)),
                    ),
              ],
              builder: (context, menu, _) => IconButton(
                tooltip: l.connColumns,
                icon: const Icon(Icons.view_column_outlined),
                onPressed: () => menu.isOpen ? menu.close() : menu.open(),
              ),
            ),
            IconButton(
              tooltip: l.clear,
              icon: const Icon(Icons.delete_sweep_outlined),
              onPressed: _clear,
            ),
          ],
        ),
        const SizedBox(height: 16),
        Expanded(
          child: Card(
            margin: EdgeInsets.zero,
            clipBehavior: Clip.antiAlias,
            child: visible.isEmpty
                ? Center(child: Text(l.connectionsEmpty))
                : LayoutBuilder(
                    builder: (context, box) {
                      // Each column gets its least width and its share of
                      // what is left; when the least widths do not fit,
                      // the table scrolls sideways.
                      final least =
                          columns.fold(0.0, (sum, c) => sum + c.width) +
                          2 * _sidePadding;
                      final width = math.max(box.maxWidth, least);
                      final flex = columns.fold(0.0, (sum, c) => sum + c.flex);
                      final widths = {
                        for (final c in columns)
                          c.key: c.width + (width - least) * c.flex / flex,
                      };
                      final table = SizedBox(
                        width: width,
                        height: box.maxHeight,
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            _Header(columns: columns, widths: widths),
                            const Divider(height: 1),
                            Expanded(
                              child: SelectionArea(
                                child: ListView.builder(
                                  controller: _down,
                                  padding: const EdgeInsets.symmetric(
                                    vertical: 4,
                                  ),
                                  itemCount: visible.length,
                                  itemBuilder: (context, i) => _Row(
                                    connection: visible[i],
                                    now: now,
                                    columns: columns,
                                    widths: widths,
                                  ),
                                ),
                              ),
                            ),
                          ],
                        ),
                      );
                      if (width <= box.maxWidth) return table;
                      return Scrollbar(
                        controller: _sideways,
                        thumbVisibility: true,
                        child: SingleChildScrollView(
                          controller: _sideways,
                          scrollDirection: Axis.horizontal,
                          child: table,
                        ),
                      );
                    },
                  ),
          ),
        ),
      ],
    );
  }
}

const _sidePadding = 12.0;

const _mono = TextStyle(fontFamily: 'Consolas', fontSize: 12.5, height: 1.4);

/// Connections are addresses and stay left to right in every language.
Widget _ltrRow(
  List<_Col> columns,
  Map<String, double> widths,
  Widget Function(String key) cell,
) => Directionality(
  textDirection: TextDirection.ltr,
  child: Padding(
    padding: const EdgeInsets.symmetric(horizontal: _sidePadding, vertical: 6),
    child: Row(
      children: [
        for (final c in columns)
          SizedBox(
            width: widths[c.key],
            child: Padding(
              padding: const EdgeInsetsDirectional.only(end: 8),
              child: cell(c.key),
            ),
          ),
      ],
    ),
  ),
);

Text _oneLine(String text, TextStyle? style) =>
    Text(text, maxLines: 1, overflow: TextOverflow.ellipsis, style: style);

class _Header extends StatelessWidget {
  const _Header({required this.columns, required this.widths});

  final List<_Col> columns;
  final Map<String, double> widths;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final style = Theme.of(context).textTheme.labelMedium
        ?.copyWith(color: Theme.of(context).colorScheme.onSurfaceVariant);
    return _ltrRow(columns, widths, (key) => _oneLine(_title(l, key), style));
  }
}

class _Row extends StatelessWidget {
  const _Row({
    required this.connection,
    required this.now,
    required this.columns,
    required this.widths,
  });

  final ProxyConnection connection;
  final DateTime now;
  final List<_Col> columns;
  final Map<String, double> widths;

  /// "12s", "2:05", "1:02:05".
  static String _duration(Duration d) {
    if (d.isNegative) d = Duration.zero;
    String two(int n) => n.toString().padLeft(2, '0');
    if (d.inHours > 0) {
      return '${d.inHours}:${two(d.inMinutes % 60)}:${two(d.inSeconds % 60)}';
    }
    if (d.inMinutes > 0) return '${d.inMinutes}:${two(d.inSeconds % 60)}';
    return '${d.inSeconds}s';
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final c = connection;
    final muted = _mono.copyWith(color: theme.colorScheme.onSurfaceVariant);
    final (label, color) = switch (c.route) {
      'vpn' => (l.connRouteVpn, StatusColors.good),
      'direct' => (l.connRouteDirect, const Color(0xFF3E8AF0)),
      'blocked' => (l.connRouteBlocked, StatusColors.idle),
      'port' => (l.connRoutePort, StatusColors.busy),
      'failed' => (l.connRouteFailed, StatusColors.bad),
      _ => ('…', StatusColors.idle),
    };
    final start = c.start;
    Widget cell(String key) => switch (key) {
      'time' => _oneLine(
        start == null
            ? ''
            : '${formatTime(start)}:${start.second.toString().padLeft(2, '0')}',
        muted,
      ),
      'route' => Align(
        alignment: Alignment.centerLeft,
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
          decoration: BoxDecoration(
            color: color.withValues(alpha: 0.14),
            borderRadius: BorderRadius.circular(6),
          ),
          child: _oneLine(
            label,
            theme.textTheme.labelSmall?.copyWith(
              color: color,
              fontWeight: FontWeight.w700,
            ),
          ),
        ),
      ),
      'inbound' => _oneLine(c.inbound.toUpperCase(), muted),
      'protocol' => _oneLine(c.network.toUpperCase(), muted),
      'traffic' => _oneLine(
        '↑ ${formatBytes(c.up)}  ↓ ${formatBytes(c.down)}',
        muted,
      ),
      'duration' => Row(
        children: [
          if (c.open)
            Container(
              width: 7,
              height: 7,
              margin: const EdgeInsetsDirectional.only(end: 6),
              decoration: const BoxDecoration(
                color: StatusColors.good,
                shape: BoxShape.circle,
              ),
            ),
          Flexible(
            child: _oneLine(
              start == null ? '' : _duration((c.end ?? now).difference(start)),
              muted,
            ),
          ),
        ],
      ),
      _ => Tooltip(
        message: c.client,
        child: _oneLine(
          c.target,
          _mono.copyWith(
            color: theme.colorScheme.onSurface,
            fontWeight: FontWeight.w600,
          ),
        ),
      ),
    };
    return Opacity(
      opacity: c.open ? 1 : 0.6,
      child: _ltrRow(columns, widths, cell),
    );
  }
}
