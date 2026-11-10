/**
 * URL state for the Logs tabs: time range, facet filters, search, live and the
 * open row live in the query string (`?range=last-1h&filter=method:GET&q=…`),
 * so a refresh or a shared link reopens the same view.
 *
 * Keys come from datum-ui's `useLogsUrlState` (`q`, `range`, `from`, `to`,
 * `filter`, `live`, `log`) and don't overlap the plugin's other params
 * (`timeRange`, `regions`, `instances`).
 */
import {
  lastThirtyMinutes,
  resolveLogTimeRange,
  useLogsUrlState,
  writeLogsUrlState,
  type LogsUrlStateProps,
  type LogTimeRange,
  type SetLogsSearchParams,
} from '@datum-cloud/datum-ui/logs';
import { useCallback, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';

export interface ComputeLogsUrlState extends LogsUrlStateProps {
  /**
   * Re-run the query for "now": slides a preset window forward, and swaps a
   * fixed range for the last 30 minutes (what Refresh did before).
   */
  refreshWindow: () => void;
}

export function useComputeLogsUrlState(): ComputeLogsUrlState {
  // Go through the host's router (shared singleton) so its location stays
  // current; replace, so the back button leaves the tab rather than stepping
  // through every filter click.
  const [searchParams, setRouterSearchParams] = useSearchParams();
  const setSearchParams = useCallback<SetLogsSearchParams>(
    (update, options) =>
      setRouterSearchParams(update, {
        replace: options?.replace ?? true,
        preventScrollReset: true,
      }),
    [setRouterSearchParams]
  );
  const logsState = useLogsUrlState({ searchParams, setSearchParams });

  // The URL holds a preset by key, so writing the re-anchored window back
  // changes nothing. A refresh re-anchors locally instead, and only for the
  // range it was made for: the hook keeps one `timeRange` object until a logs
  // param changes, and any change re-resolves the preset to now anyway.
  const [refresh, setRefresh] = useState<{ base: LogTimeRange; at: number } | null>(null);
  const timeRange = useMemo(
    () =>
      refresh?.base === logsState.timeRange && logsState.timeRange.preset
        ? resolveLogTimeRange(logsState.timeRange, new Date(refresh.at))
        : logsState.timeRange,
    [logsState.timeRange, refresh]
  );

  const refreshWindow = useCallback(() => {
    if (logsState.timeRange.preset) {
      setRefresh({ base: logsState.timeRange, at: Date.now() });
      return;
    }
    setSearchParams((prev) => writeLogsUrlState(prev, { timeRange: lastThirtyMinutes() }));
  }, [logsState.timeRange, setSearchParams]);

  return { ...logsState, timeRange, refreshWindow };
}
