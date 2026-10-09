import { useId, useState } from "react"
import { cn } from "cn"

/**
 * Lightweight SVG charts — no chart library (bundle-lean charter rule).
 * Colors come from the categorical palette tokens (chart-1..8); axis
 * text uses chart-axis; gridlines chart-grid. Every chart is paired
 * with a keyboard-accessible data table via `showTable`.
 */

export type Series = {
  label: string
  color: string // token class name, e.g. "chart-1"
  values: number[] // one per bucket
}

export type Bucket = { label: string; values: number[] }

export function BarChart({
  buckets,
  series,
  valueFormat = (v) => String(v),
  height = 220,
}: {
  buckets: Bucket[]
  series: { label: string; className: string }[]
  valueFormat?: (v: number) => string
  height?: number
}) {
  const [hidden, setHidden] = useState<Set<number>>(new Set())
  const active = series.filter((_, i) => !hidden.has(i))
  const max = Math.max(
    1,
    ...buckets.flatMap((b) => b.values.filter((_, i) => !hidden.has(i)))
  )
  const barW = buckets.length > 0 ? 100 / buckets.length : 100

  return (
    <div className="w-full">
      <div
        className="mb-3 flex flex-wrap gap-3"
        role="group"
        aria-label="Chart series"
      >
        {series.map((s, i) => (
          <button
            key={s.label}
            type="button"
            onClick={() =>
              setHidden((prev) => {
                const next = new Set(prev)
                if (next.has(i)) next.delete(i)
                else next.add(i)
                return next
              })
            }
            className={cn(
              "flex items-center gap-1.5 text-xs",
              hidden.has(i) ? "opacity-40" : "opacity-100"
            )}
            aria-pressed={!hidden.has(i)}
          >
            <span
              aria-hidden="true"
              className={cn("size-2 rounded-sm", s.className)}
            />
            {s.label}
          </button>
        ))}
      </div>
      <div className="flex" style={{ height }}>
        <div className="flex flex-col justify-between pe-2 text-end">
          {[max, max * 0.75, max * 0.5, max * 0.25, 0].map((v, i) => (
            <span key={i} className="tnum text-[11px] text-chart-axis">
              {valueFormat(Math.round(v))}
            </span>
          ))}
        </div>
        <div className="relative flex-1 border-b border-chart-grid">
          {[0.25, 0.5, 0.75].map((f) => (
            <div
              key={f}
              aria-hidden="true"
              className="absolute inset-x-0 border-t border-chart-grid"
              style={{ bottom: `${f * 100}%` }}
            />
          ))}
          <div className="absolute inset-0 flex items-end">
            {buckets.map((b, bi) => (
              <div
                key={bi}
                className="group relative flex h-full items-end justify-center gap-px"
                style={{ width: `${barW}%` }}
              >
                {b.values.map((v, si) =>
                  hidden.has(si) ? null : (
                    <div
                      key={si}
                      title={`${b.label} · ${series[si].label}: ${valueFormat(v)}`}
                      className={cn(
                        "min-h-[2px] rounded-t-sm",
                        series[si].className
                      )}
                      style={{
                        height: `${(v / max) * 100}%`,
                        width: `${Math.max(4, (100 / series.length) * 0.7)}%`,
                      }}
                    />
                  )
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
      <div className="mt-2 flex">
        {buckets.map((b, i) => (
          <span
            key={i}
            className="tnum truncate text-center text-[11px] text-chart-axis"
            style={{ width: `${barW}%` }}
          >
            {b.label}
          </span>
        ))}
      </div>
    </div>
  )
}

/**
 * Accessible data table for a chart (keyboard fallback, collapsed by
 * default per the design system).
 */
export function ChartTable({
  buckets,
  series,
  valueFormat = (v) => String(v),
}: {
  buckets: Bucket[]
  series: { label: string }[]
  valueFormat?: (v: number) => string
}) {
  const id = useId()
  return (
    <details className="mt-4">
      <summary className="cursor-pointer text-xs text-muted-foreground">
        Show data table
      </summary>
      <table className="tnum mt-2 w-full text-xs" aria-labelledby={id}>
        <thead>
          <tr className="border-b text-start">
            <th scope="col" className="py-1.5 text-start font-medium">
              Bucket
            </th>
            {series.map((s) => (
              <th
                scope="col"
                key={s.label}
                className="py-1.5 text-end font-medium"
              >
                {s.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {buckets.map((b, i) => (
            <tr key={i} className="border-b border-dashed">
              <td className="py-1.5 text-start">{b.label}</td>
              {b.values.map((v, j) => (
                <td key={j} className="py-1.5 text-end">
                  {valueFormat(v)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  )
}
