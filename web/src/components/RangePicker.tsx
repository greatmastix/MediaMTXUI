import { historyRanges, type HistoryRange } from '@/api/history'

/** Chooses the charts' range: the last hour (live), 24 hours, 7 or 30 days. */
export function RangePicker({
  value,
  onChange,
}: {
  value: HistoryRange
  onChange: (r: HistoryRange) => void
}) {
  return (
    <div
      role="group"
      aria-label="Chart range"
      className="flex rounded-md border p-0.5 text-[12.5px]"
    >
      {historyRanges.map((r) => (
        <button
          key={r.value}
          type="button"
          aria-pressed={value === r.value}
          className="rounded px-2 py-0.5 text-muted-foreground tabular-nums aria-pressed:bg-accent aria-pressed:font-medium aria-pressed:text-foreground"
          onClick={() => {
            onChange(r.value)
          }}
        >
          {r.label}
        </button>
      ))}
    </div>
  )
}
