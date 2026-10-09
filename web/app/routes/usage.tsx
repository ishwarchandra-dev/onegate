import { useEffect, useMemo, useState } from "react"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table"
import { BarChart, ChartTable, type Bucket } from "~/components/chart"
import { TableSkeleton, StatCardSkeleton } from "~/components/skeletons"
import { api, ApiError, formatMicroUSD, timeOnly } from "~/lib/api"
import type { T } from "~/lib/api"

const RANGES = [
  { id: "24h", hours: 24, step: "hour" as const },
  { id: "7d", hours: 24 * 7, step: "hour" as const },
  { id: "30d", hours: 24 * 30, step: "day" as const },
]

/**
 * Usage analytics: summary cards, request/token/cost charts over the
 * rollup API, recent requests table, CSV export. Buckets stream from
 * /api/usage/range; empty and error states are the designed patterns.
 */
export default function UsageView() {
  const [rangeId, setRangeId] = useState("24h")
  const range = RANGES.find((r) => r.id === rangeId) ?? RANGES[0]
  const endMS = useMemo(() => Date.now(), [rangeId])
  const startMS = endMS - range.hours * 3_600_000

  const [buckets, setBuckets] = useState<T.UsageBucket[]>([])
  const [summary, setSummary] = useState<T.UsageTotals | null>(null)
  const [requests, setRequests] = useState<T.RequestRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  function reload() {
    setLoading(true)
    Promise.all([
      api.queryUsageRange({
        start_ms: startMS,
        end_ms: endMS,
        step: range.step,
      }),
      api.queryUsageSummary({ start_ms: startMS, end_ms: endMS }),
      api.listRequests({ limit: 20 }),
    ])
      .then(([r, s, q]) => {
        setBuckets(r.buckets)
        setSummary(s)
        setRequests(q.items)
        setError(null)
      })
      .catch((e) =>
        setError(e instanceof ApiError ? e.message : "Request failed")
      )
      .finally(() => setLoading(false))
  }

  useEffect(reload, [rangeId])

  /** Zero-fill missing buckets so gaps read as zero traffic, not errors
   * (design-system chart language). */
  const filled = useMemo(() => zeroFill(buckets, range), [buckets, range])

  const requestBuckets: Bucket[] = filled.map((b) => ({
    label: bucketLabel(b.bucket_start_ms, range.step),
    values: [b.requests, b.errors],
  }))
  const tokenBuckets: Bucket[] = filled.map((b) => ({
    label: bucketLabel(b.bucket_start_ms, range.step),
    values: [b.prompt_tokens, b.completion_tokens],
  }))
  const costBuckets: Bucket[] = filled.map((b) => ({
    label: bucketLabel(b.bucket_start_ms, range.step),
    values: [b.cost_usd_micros],
  }))

  function exportCSV() {
    const rows = [
      [
        "bucket_start_ms",
        "requests",
        "errors",
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "cost_usd_micros",
      ],
      ...filled.map((b) => [
        String(b.bucket_start_ms),
        String(b.requests),
        String(b.errors),
        String(b.prompt_tokens),
        String(b.completion_tokens),
        String(b.total_tokens),
        String(b.cost_usd_micros),
      ]),
    ]
    const csv = rows.map((r) => r.join(",")).join("\n")
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }))
    const a = document.createElement("a")
    a.href = url
    a.download = `onegate-usage-${rangeId}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-lg font-semibold">Usage</h1>
        <div className="flex gap-2">
          <Select value={rangeId} onValueChange={setRangeId}>
            <SelectTrigger className="h-7 w-28 text-xs" aria-label="Time range">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANGES.map((r) => (
                <SelectItem key={r.id} value={r.id}>
                  Last {r.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="outline" size="sm" onClick={exportCSV}>
            Export CSV
          </Button>
          <Button variant="outline" size="sm" onClick={reload}>
            Refresh
          </Button>
        </div>
      </div>

      {error ? (
        <p
          role="alert"
          className="rounded-lg border bg-card p-4 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}

      {loading && !summary ? (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatCardSkeleton />
          <StatCardSkeleton />
          <StatCardSkeleton />
          <StatCardSkeleton />
        </div>
      ) : summary ? (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <Stat
            label="Requests"
            value={summary.requests.toLocaleString()}
            sub={`${summary.errors} errors`}
          />
          <Stat
            label="Tokens"
            value={summary.total_tokens.toLocaleString()}
            sub={`${summary.prompt_tokens.toLocaleString()} in / ${summary.completion_tokens.toLocaleString()} out`}
          />
          <Stat
            label="Cost"
            value={formatMicroUSD(summary.cost_usd_micros)}
            sub="integer micro-USD"
          />
          <Stat
            label="Error rate"
            value={
              summary.requests
                ? `${((summary.errors / summary.requests) * 100).toFixed(1)}%`
                : "—"
            }
            sub={`${rangeId} window`}
          />
        </div>
      ) : null}

      {loading && buckets.length === 0 ? (
        <TableSkeleton rows={6} columns={6} />
      ) : filled.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed bg-card/50 px-6 py-14 text-center">
          <p className="text-sm font-medium">No traffic in this window</p>
          <p className="max-w-sm text-xs leading-relaxed text-muted-foreground">
            Requests appear here as clients call the gateway through virtual
            keys.
          </p>
        </div>
      ) : (
        <>
          <ChartCard title="Requests & errors">
            <BarChart
              buckets={requestBuckets}
              series={[
                { label: "requests", className: "bg-chart-1" },
                { label: "errors", className: "bg-chart-6" },
              ]}
            />
            <ChartTable
              buckets={requestBuckets}
              series={[{ label: "requests" }, { label: "errors" }]}
            />
          </ChartCard>

          <ChartCard title="Tokens">
            <BarChart
              buckets={tokenBuckets}
              series={[
                { label: "prompt", className: "bg-chart-2" },
                { label: "completion", className: "bg-chart-3" },
              ]}
              valueFormat={(v) => v.toLocaleString()}
            />
          </ChartCard>

          <ChartCard title="Cost (micro-USD)">
            <BarChart
              buckets={costBuckets}
              series={[{ label: "cost", className: "bg-chart-4" }]}
              valueFormat={(v) => v.toLocaleString()}
            />
          </ChartCard>
        </>
      )}

      <div className="rounded-lg border bg-card">
        <div className="border-b px-4 py-3 text-sm font-semibold">
          Recent requests
        </div>
        {loading && requests.length === 0 ? (
          <div className="p-4">
            <TableSkeleton rows={5} columns={6} />
          </div>
        ) : requests.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">
            No requests recorded yet.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-end">Tokens</TableHead>
                <TableHead className="text-end">Cost</TableHead>
                <TableHead className="text-end">Latency</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {requests.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="tnum text-xs text-muted-foreground">
                    {timeOnly(r.created_ms)}
                  </TableCell>
                  <TableCell>{r.model_served || r.model_requested}</TableCell>
                  <TableCell>
                    <StatusBadge status={r.status} />
                  </TableCell>
                  <TableCell className="tnum text-end">
                    {(r.total_tokens ?? 0).toLocaleString()}
                  </TableCell>
                  <TableCell className="tnum text-end">
                    {formatMicroUSD(r.cost_usd_micros ?? 0)}
                  </TableCell>
                  <TableCell className="tnum text-end">
                    {r.stream && r.ttft_ms
                      ? `${r.ttft_ms}ms ttft`
                      : `${r.latency_ms}ms`}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  )
}

function Stat({
  label,
  value,
  sub,
}: {
  label: string
  value: string
  sub?: string
}) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border bg-card p-4">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="tnum text-xl font-semibold">{value}</span>
      {sub ? (
        <span className="text-xs text-muted-foreground">{sub}</span>
      ) : null}
    </div>
  )
}

function ChartCard({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <h2 className="mb-4 text-sm font-semibold">{title}</h2>
      {children}
    </div>
  )
}

function StatusBadge({ status }: { status: string }) {
  if (status === "success")
    return <Badge className="bg-success/10 text-success">ok</Badge>
  if (status === "cancelled") return <Badge variant="outline">cancelled</Badge>
  return <Badge className="bg-destructive/10 text-destructive">error</Badge>
}

/** Fill absent buckets with zeros across the window. */
function zeroFill(
  buckets: T.UsageBucket[],
  range: (typeof RANGES)[number]
): T.UsageBucket[] {
  if (buckets.length === 0) return []
  const stepMS = range.step === "day" ? 86_400_000 : 3_600_000
  const byStart = new Map(buckets.map((b) => [b.bucket_start_ms, b]))
  // Aggregate per-bucket across the (vkey, model, provider) split so the
  // chart shows one series of totals.
  const agg = new Map<number, T.UsageBucket>()
  for (const b of buckets) {
    const cur = agg.get(b.bucket_start_ms) ?? emptyBucket(b.bucket_start_ms)
    cur.requests += b.requests
    cur.errors += b.errors
    cur.prompt_tokens += b.prompt_tokens
    cur.completion_tokens += b.completion_tokens
    cur.total_tokens += b.total_tokens
    cur.cost_usd_micros += b.cost_usd_micros
    agg.set(b.bucket_start_ms, cur)
  }
  const out: T.UsageBucket[] = []
  const first = Math.floor(buckets[0].bucket_start_ms / stepMS) * stepMS
  const last = buckets[buckets.length - 1].bucket_start_ms
  for (let t = first; t <= last; t += stepMS) {
    out.push(agg.get(t) ?? emptyBucket(t))
  }
  return out
}

function emptyBucket(start: number): T.UsageBucket {
  return {
    bucket_start_ms: start,
    virtual_key_id: "",
    model_id: "",
    provider_id: "",
    requests: 0,
    errors: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    total_tokens: 0,
    cost_usd_micros: 0,
  }
}

function bucketLabel(ms: number, step: "hour" | "day"): string {
  const d = new Date(ms)
  if (step === "day")
    return d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
  return d.toLocaleTimeString(undefined, { hour: "numeric" })
}
