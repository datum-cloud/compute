import { type useMetricsTimeRange } from '../lib/metrics-time-range';
import { DateTimeRangePicker, getBrowserTimezone } from '@datum-cloud/datum-ui/picker';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useMemo } from 'react';

type MetricsTimeRange = ReturnType<typeof useMetricsTimeRange>;

export function MetricsTimeRangeToolbar({
  range,
  className,
}: {
  range: MetricsTimeRange;
  className?: string;
}) {
  const timezone = useMemo(() => getBrowserTimezone(), []);

  return (
    <div
      className={cn(
        'flex w-full flex-col gap-3 sm:flex-row sm:items-center sm:justify-end',
        className
      )}>
      <DateTimeRangePicker
        value={range.pickerValue}
        onChange={(value) =>
          range.setPickerValue(
            value
              ? {
                  from: value.from,
                  to: value.to,
                  ...('preset' in value && value.preset ? { preset: String(value.preset) } : {}),
                }
              : null
          )
        }
        timezone={timezone}
        presets={range.presets}
        disableFuture
        placeholder="Select time range"
        className="bg-card w-full sm:w-auto"
        triggerClassName="h-8 min-h-8 py-0 text-sm"
      />
    </div>
  );
}
