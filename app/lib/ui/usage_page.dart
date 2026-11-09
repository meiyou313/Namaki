import 'package:flutter/material.dart';

import '../core/core_controller.dart';
import '../core/usage_history.dart';
import '../l10n/app_localizations.dart';
import 'format.dart';

/// The data usage page: the account's remaining volume at the top,
/// then the usage history as animated bar charts — daily, weekly and
/// monthly — and the current session's numbers at the bottom. The
/// history comes from [UsageHistory], which samples the core's
/// cumulative totals on every status poll.
class UsagePage extends StatefulWidget {
  const UsagePage({super.key, required this.core});

  final CoreController core;

  @override
  State<UsagePage> createState() => _UsagePageState();
}

enum _Range { daily, weekly, monthly }

class _UsagePageState extends State<UsagePage> {
  _Range _range = _Range.daily;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    // No Scaffold of its own: the shell's _push provides the top bar,
    // like the accounts and servers pages.
    return ListenableBuilder(
        listenable: Listenable.merge([UsageHistory.days, widget.core]),
        builder: (context, _) {
          final status = widget.core.status;
          final days = switch (_range) {
            _Range.daily => UsageHistory.lastDays(7),
            _Range.weekly => UsageHistory.lastWeeks(8),
            _Range.monthly => UsageHistory.lastMonths(6),
          };
          final total = days.fold<int>(0, (s, d) => s + d.total);
          final up = days.fold<int>(0, (s, d) => s + d.up);
          final down = days.fold<int>(0, (s, d) => s + d.down);
          final today = UsageHistory.lastDays(1).firstOrNull?.total ?? 0;
          final week = UsageHistory.lastWeeks(1).firstOrNull?.total ?? 0;
          final month = UsageHistory.lastMonths(1).firstOrNull?.total ?? 0;
          final traffic = status?.traffic;
          final usage = status?.activeAccount?.usage;
          return ListView(
            padding: const EdgeInsets.all(16),
            children: [
              if (usage != null && !usage.unlimited) ...[
                _SummaryCard(
                  title: l.dataUsedTitle,
                  value: formatBytes(usage.usedBytes),
                  detail: l.dataLeftOf(formatBytes(usage.limitBytes)),
                  fraction: usage.limitBytes == 0
                      ? 0
                      : usage.usedBytes.clamp(0, usage.limitBytes) /
                          usage.limitBytes,
                ),
                const SizedBox(height: 16),
              ],
              Row(
                children: [
                  _TotalChip(label: l.usageToday, value: formatBytes(today)),
                  const SizedBox(width: 8),
                  _TotalChip(label: l.usageThisWeek, value: formatBytes(week)),
                  const SizedBox(width: 8),
                  _TotalChip(
                    label: l.usageThisMonth,
                    value: formatBytes(month),
                  ),
                ],
              ),
              const SizedBox(height: 20),
              SegmentedButton<_Range>(
                segments: [
                  ButtonSegment(value: _Range.daily, label: Text(l.usageDaily)),
                  ButtonSegment(
                    value: _Range.weekly,
                    label: Text(l.usageWeekly),
                  ),
                  ButtonSegment(
                    value: _Range.monthly,
                    label: Text(l.usageMonthly),
                  ),
                ],
                selected: {_range},
                onSelectionChanged: (s) => setState(() => _range = s.first),
              ),
              const SizedBox(height: 20),
              if (total == 0)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 48),
                  child: Text(
                    l.usageNoData,
                    textAlign: TextAlign.center,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                _Bars(days: days, range: _range),
              const SizedBox(height: 8),
              if (total > 0)
                Text(
                  '${l.usageDownload} ${formatBytes(down)} · ${l.usageUpload} ${formatBytes(up)}',
                  textAlign: TextAlign.center,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              const SizedBox(height: 24),
              if (traffic != null) ...[
                Text(l.usageSession, style: theme.textTheme.titleMedium),
                const SizedBox(height: 8),
                _SessionRow(
                  label: l.usageDownload,
                  value: formatBytes(traffic.down),
                  icon: Icons.download_rounded,
                ),
                _SessionRow(
                  label: l.usageUpload,
                  value: formatBytes(traffic.up),
                  icon: Icons.upload_rounded,
                ),
                _SessionRow(
                  label: l.usageTotal,
                  value: formatBytes(traffic.totalUp + traffic.totalDown),
                  icon: Icons.pie_chart_rounded,
                ),
              ],
            ],
          );
        },
      );
  }
}

class _SummaryCard extends StatelessWidget {
  const _SummaryCard({
    required this.title,
    required this.value,
    required this.detail,
    required this.fraction,
  });

  final String title;
  final String value;
  final String detail;
  final double fraction;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              title,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(height: 4),
            Text(
              value,
              style: theme.textTheme.headlineSmall?.copyWith(
                fontWeight: FontWeight.w800,
              ),
            ),
            Text(
              detail,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(height: 12),
            ClipRRect(
              borderRadius: BorderRadius.circular(999),
              child: LinearProgressIndicator(
                value: fraction.clamp(0, 1),
                minHeight: 8,
                backgroundColor: theme.colorScheme.surfaceContainerHighest,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _TotalChip extends StatelessWidget {
  const _TotalChip({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Expanded(
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
        decoration: BoxDecoration(
          color: theme.colorScheme.surfaceContainerHighest.withValues(
            alpha: 0.5,
          ),
          borderRadius: BorderRadius.circular(12),
        ),
        child: Column(
          children: [
            Text(
              label,
              style: theme.textTheme.labelMedium?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(height: 2),
            Text(
              value,
              style: theme.textTheme.titleMedium?.copyWith(
                fontWeight: FontWeight.w800,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The animated bar chart: the bars grow from the bottom when the
/// range changes (keyed by the range, so a new sample while the page
/// is open only moves the heights smoothly instead of replaying).
class _Bars extends StatelessWidget {
  const _Bars({required this.days, required this.range});

  final List<UsageDay> days;
  final _Range range;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final max = days.fold<int>(0, (m, d) => d.total > m ? d.total : m);
    return SizedBox(
      height: 220,
      child: TweenAnimationBuilder<double>(
        key: ValueKey(range),
        tween: Tween(begin: 0, end: 1),
        duration: const Duration(milliseconds: 600),
        curve: Curves.easeOutCubic,
        builder: (context, t, _) => Row(
          crossAxisAlignment: CrossAxisAlignment.end,
          children: [
            for (final d in days)
              Expanded(
                child: Padding(
                  padding: const EdgeInsets.symmetric(horizontal: 3),
                  child: Column(
                    mainAxisAlignment: MainAxisAlignment.end,
                    children: [
                      if (d.total > 0)
                        Text(
                          formatBytes(d.total),
                          style: theme.textTheme.labelSmall,
                          maxLines: 1,
                          overflow: TextOverflow.clip,
                        ),
                      const SizedBox(height: 4),
                      AnimatedContainer(
                        duration: const Duration(milliseconds: 300),
                        height:
                            (max == 0 ? 0 : d.total / max * 140 * t) + 4,
                        decoration: BoxDecoration(
                          gradient: LinearGradient(
                            begin: Alignment.topCenter,
                            end: Alignment.bottomCenter,
                            colors: [
                              theme.colorScheme.primary,
                              theme.colorScheme.primary.withValues(alpha: 0.55),
                            ],
                          ),
                          borderRadius: BorderRadius.circular(8),
                        ),
                      ),
                      const SizedBox(height: 6),
                      Text(
                        switch (range) {
                          _Range.daily => formatDate(l, d.date),
                          _Range.weekly => formatDate(l, d.date),
                          _Range.monthly => formatMonth(l, d.date),
                        },
                        style: theme.textTheme.labelSmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                        textAlign: TextAlign.center,
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }
}

class _SessionRow extends StatelessWidget {
  const _SessionRow({
    required this.label,
    required this.value,
    required this.icon,
  });

  final String label;
  final String value;
  final IconData icon;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          Icon(icon, size: 18, color: theme.colorScheme.onSurfaceVariant),
          const SizedBox(width: 10),
          Text(label, style: theme.textTheme.bodyMedium),
          const Spacer(),
          Text(
            value,
            style: theme.textTheme.bodyMedium?.copyWith(
              fontWeight: FontWeight.w700,
            ),
          ),
        ],
      ),
    );
  }
}
