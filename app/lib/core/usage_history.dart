import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';

import 'core_controller.dart';

/// One bucket of used bytes: a day, a week (from its Saturday) or a
/// month (from its first day). [up] and [down] are kept apart for the
/// labels; [total] is what the bars show.
class UsageDay {
  const UsageDay(this.date, this.up, this.down);

  final DateTime date;
  final int up;
  final int down;

  int get total => up + down;
}

class _Bucket {
  int up = 0;
  int down = 0;
}

/// The data usage history: the core's cumulative totals
/// (Traffic.totalUp/totalDown, kept since the app was first installed)
/// are sampled on every status poll and the deltas are added to the
/// local calendar day they happened in, so the usage page can draw
/// daily, weekly and monthly bar charts. Kept in a JSON file beside
/// the settings file, so it survives restarts. A counter that went
/// backwards (the core's data was wiped) starts a fresh baseline
/// instead of adding a negative day.
abstract final class UsageHistory {
  /// The days with usage, oldest first; the usage page listens to this.
  static final days = ValueNotifier<List<UsageDay>>([]);

  static String? _file;
  static CoreController? _core;
  static Timer? _saveTimer;
  static int? _lastUp;
  static int? _lastDown;
  static final _byDay = <String, _Bucket>{};

  static void start(String dataDir, CoreController core) {
    if (_core != null) return;
    _core = core;
    _file = '$dataDir${Platform.pathSeparator}usage-history.json';
    _load();
    core.addListener(_sample);
    _sample();
  }

  @visibleForTesting
  static void reset() {
    _saveTimer?.cancel();
    _core?.removeListener(_sample);
    _core = null;
    _file = null;
    _saveTimer = null;
    _lastUp = _lastDown = null;
    _byDay.clear();
    days.value = [];
  }

  static String _key(DateTime d) =>
      '${d.year}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';

  static DateTime _day(String key) {
    final p = key.split('-');
    return DateTime(int.parse(p[0]), int.parse(p[1]), int.parse(p[2]));
  }

  static void _sample() {
    final t = _core?.status?.traffic;
    if (t == null) return;
    final up = t.totalUp, down = t.totalDown;
    final lastUp = _lastUp, lastDown = _lastDown;
    _lastUp = up;
    _lastDown = down;
    if (lastUp == null || lastDown == null) return;
    // A counter that went backwards starts a fresh baseline.
    var du = up - lastUp;
    var dd = down - lastDown;
    if (du < 0) du = 0;
    if (dd < 0) dd = 0;
    if (du == 0 && dd == 0) return;
    final b = _byDay.putIfAbsent(_key(DateTime.now()), () => _Bucket());
    b.up += du;
    b.down += dd;
    _publish();
    _saveSoon();
  }

  static void _publish() {
    final keys = _byDay.keys.toList()..sort();
    days.value = [
      for (final k in keys) UsageDay(_day(k), _byDay[k]!.up, _byDay[k]!.down),
    ];
  }

  /// The last [n] days, today included, with empty days filled in.
  static List<UsageDay> lastDays(int n) {
    final today = DateTime.now();
    final byKey = {for (final d in days.value) _key(d.date): d};
    return [
      for (var i = n - 1; i >= 0; i--)
        byKey[_key(today.subtract(Duration(days: i)))] ??
            UsageDay(_atMidnight(today.subtract(Duration(days: i))), 0, 0),
    ];
  }

  /// The last [n] weeks (Saturday to Friday), this week included.
  static List<UsageDay> lastWeeks(int n) {
    final start = _weekStart(DateTime.now());
    final out = <UsageDay>[];
    for (var i = n - 1; i >= 0; i--) {
      final s = start.subtract(Duration(days: 7 * i));
      var up = 0, down = 0;
      for (var j = 0; j < 7; j++) {
        final d = days.value.firstWhere(
          (d) => _key(d.date) == _key(s.add(Duration(days: j))),
          orElse: () => UsageDay(s, 0, 0),
        );
        up += d.up;
        down += d.down;
      }
      out.add(UsageDay(s, up, down));
    }
    return out;
  }

  /// The last [n] calendar months, this month included.
  static List<UsageDay> lastMonths(int n) {
    final now = DateTime.now();
    final out = <UsageDay>[];
    for (var i = n - 1; i >= 0; i--) {
      final m = DateTime(now.year, now.month - i);
      final start = DateTime(m.year, m.month);
      final end = DateTime(m.year, m.month + 1);
      var up = 0, down = 0;
      for (final d in days.value) {
        if (!d.date.isBefore(start) && d.date.isBefore(end)) {
          up += d.up;
          down += d.down;
        }
      }
      out.add(UsageDay(start, up, down));
    }
    return out;
  }

  /// The bytes used in the current calendar month, from the locally
  /// sampled history. The account quota's remaining (from Mozilla) does
  /// not always move with real usage — it can sit at the full limit
  /// while the device has transferred data — so this is the floor for
  /// the "used" number the UI shows.
  static int get thisMonthTotal =>
      lastMonths(1).firstOrNull?.total ?? 0;

  static DateTime _atMidnight(DateTime d) => DateTime(d.year, d.month, d.day);

  /// The Saturday of [d]'s week (Persian weeks start on Saturday).
  static DateTime _weekStart(DateTime d) {
    final at = _atMidnight(d);
    return at.subtract(Duration(days: (at.weekday + 1) % 7));
  }

  static void _load() {
    final file = _file;
    if (file == null) return;
    try {
      final j = jsonDecode(File(file).readAsStringSync());
      if (j is Map<String, Object?>) {
        final stored = j['days'];
        if (stored is Map<String, Object?>) {
          for (final e in stored.entries) {
            if (e.value is Map<String, Object?>) {
              final m = e.value as Map<String, Object?>;
              final b = _Bucket()
                ..up = (m['up'] as num?)?.toInt() ?? 0
                ..down = (m['down'] as num?)?.toInt() ?? 0;
              if (b.up > 0 || b.down > 0) _byDay[e.key] = b;
            }
          }
        }
      }
    } catch (_) {
      // No history yet, or a broken file: start empty.
    }
    // Keep a year; older days are gone for good.
    final cutoff = _key(DateTime.now().subtract(const Duration(days: 365)));
    _byDay.removeWhere((k, _) => k.compareTo(cutoff) < 0);
    _publish();
  }

  static void _saveSoon() {
    _saveTimer ??= Timer(const Duration(seconds: 5), () {
      _saveTimer = null;
      _save();
    });
  }

  static void _save() {
    final file = _file;
    if (file == null) return;
    try {
      File(file).writeAsStringSync(jsonEncode({
        'days': {
          for (final e in _byDay.entries)
            e.key: {'up': e.value.up, 'down': e.value.down},
        },
      }));
    } catch (_) {
      // The history is best-effort; a failed write loses at most a
      // few minutes of samples.
    }
  }
}
