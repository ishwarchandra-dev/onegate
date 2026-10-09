import { useEffect, useRef, useState } from "react"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select"
import { Input } from "~/components/ui/input"
import { LogStreamSkeleton } from "~/components/skeletons"
import { StreamGapIndicator } from "~/components/states"
import { api } from "~/lib/api"
import type { T } from "~/lib/api"

const LEVELS = ["debug", "info", "warn", "error"] as const
const MAX_ENTRIES = 500

/**
 * Live logs: SSE tail with pause/resume, level + trace filters, and a
 * trace drill-down. Backpressure drops arrive as `dropped` events and
 * render as a visible gap indicator — the feed never freezes (the
 * p6.view-logs acceptance).
 */
export default function LogsView() {
  type Row = { kind: "log"; entry: T.LogEntry } | { kind: "gap"; count: number }

  const [entries, setEntries] = useState<Row[]>([])
  const [gaps, setGaps] = useState<{ id: number; count: number }[]>([])
  const [connected, setConnected] = useState(false)
  const [paused, setPaused] = useState(false)
  const [minLevel, setMinLevel] = useState<string>("info")
  const [traceFilter, setTraceFilter] = useState("")
  const [applied, setApplied] = useState({ minLevel: "info", trace: "" })
  const [selected, setSelected] = useState<T.LogEntry | null>(null)

  const pausedRef = useRef(false)
  pausedRef.current = paused

  // One SSE connection per applied-filter set.
  useEffect(() => {
    const params = new URLSearchParams()
    if (applied.minLevel && applied.minLevel !== "debug")
      params.set("min_level", applied.minLevel)
    if (applied.trace) params.set("trace_id", applied.trace)
    const qs = params.toString()
    const url = `/api/logs/live${qs ? `?${qs}` : ""}`

    const es = new EventSource(url, { withCredentials: true })
    let gapId = 0

    es.addEventListener("open", () => setConnected(true))
    es.addEventListener("log", (ev) => {
      if (pausedRef.current) return // pause = keep connection, drop render
      try {
        const entry = JSON.parse((ev as MessageEvent).data) as T.LogEntry
        setEntries((prev) => {
          const next: Row[] = [...prev, { kind: "log", entry }]
          return next.length > MAX_ENTRIES ? next.slice(-MAX_ENTRIES) : next
        })
      } catch {
        // Malformed frame: skip rather than break the stream.
      }
    })
    es.addEventListener("dropped", (ev) => {
      try {
        const { count } = JSON.parse((ev as MessageEvent).data) as {
          count: number
        }
        gapId++
        setGaps((prev) => [...prev.slice(-9), { id: gapId, count }])
        // The ring replays what it can after a drop; the indicator marks
        // the discontinuity in the rendered list.
        setEntries((prev) => [...prev, { kind: "gap", count }])
      } catch {
        // ignore
      }
    })
    es.addEventListener("error", () => setConnected(false))

    return () => {
      es.close()
      setConnected(false)
    }
  }, [applied])

  // Seed with the ring-buffer snapshot so the view starts populated.
  useEffect(() => {
    api
      .recentLogs({
        limit: 100,
        min_level: applied.minLevel as T.RecentLogsParams["min_level"],
      })
      .then((r) =>
        setEntries(r.entries.map((entry) => ({ kind: "log" as const, entry })))
      )
      .catch(() => {})
  }, [applied])

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Badge
            variant={connected ? "default" : "outline"}
            className={connected ? "bg-success/10 text-success" : ""}
          >
            {connected ? "live" : "disconnected"}
          </Badge>
          <Select value={minLevel} onValueChange={setMinLevel}>
            <SelectTrigger
              className="h-7 w-28 text-xs"
              aria-label="Minimum level"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LEVELS.map((l) => (
                <SelectItem key={l} value={l}>
                  ≥ {l}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={traceFilter}
            onChange={(e) => setTraceFilter(e.target.value)}
            placeholder="filter by trace id"
            className="h-7 w-52 text-xs"
            aria-label="Trace filter"
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => setApplied({ minLevel, trace: traceFilter.trim() })}
          >
            Apply
          </Button>
        </div>
        <div className="flex gap-2">
          <Button
            variant={paused ? "default" : "outline"}
            size="sm"
            onClick={() => setPaused((p) => !p)}
          >
            {paused ? "Resume" : "Pause"}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setEntries([])}>
            Clear
          </Button>
        </div>
      </div>

      {paused ? (
        <p
          className="rounded-md border border-dashed bg-muted/50 px-3 py-1.5 text-xs text-muted-foreground"
          role="status"
        >
          Paused — events keep streaming but are not rendered until resume.
        </p>
      ) : null}

      {entries.length === 0 ? (
        <LogStreamSkeleton lines={10} />
      ) : (
        <div className="flex flex-col-reverse overflow-hidden rounded-lg border bg-card font-mono text-xs">
          {/* Column-reverse: newest at the visual top; scroll stays put. */}
          <div className="flex max-h-[60vh] flex-col-reverse overflow-y-auto">
            {entries
              .slice()
              .reverse()
              .map((row, i) =>
                row.kind === "gap" ? (
                  <StreamGapIndicator key={`gap-${i}`} count={row.count} />
                ) : (
                  <LogRow
                    key={`${row.entry.timestamp}-${i}`}
                    entry={row.entry}
                    onOpen={() => setSelected(row.entry)}
                  />
                )
              )}
          </div>
        </div>
      )}

      {selected ? (
        <TraceDrawer entry={selected} onClose={() => setSelected(null)} />
      ) : null}
    </div>
  )
}

type GapMarker = T.LogEntry & { level: "__gap__"; count: number }

function gapMarker(id: number, count: number): GapMarker {
  return {
    timestamp: new Date().toISOString(),
    level: "__gap__",
    message: "",
    count,
  } as GapMarker
}

const LEVEL_COLORS: Record<string, string> = {
  debug: "text-muted-foreground",
  info: "text-foreground",
  warn: "text-warning",
  error: "text-destructive",
}

function LogRow({ entry, onOpen }: { entry: T.LogEntry; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex items-start gap-3 border-b border-dashed px-3 py-1.5 text-start hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:outline-none"
    >
      <span className="tnum shrink-0 text-muted-foreground">
        {new Date(entry.timestamp).toLocaleTimeString()}
      </span>
      <span
        className={`w-12 shrink-0 font-semibold ${LEVEL_COLORS[entry.level] ?? ""}`}
      >
        {entry.level}
      </span>
      <span
        className="w-24 shrink-0 truncate text-muted-foreground"
        title={entry.provider}
      >
        {entry.provider || "—"}
      </span>
      <span className="min-w-0 flex-1 truncate">{entry.message}</span>
      {entry.trace_id ? (
        <span className="shrink-0 text-muted-foreground">
          {entry.trace_id.slice(0, 12)}…
        </span>
      ) : null}
    </button>
  )
}

/** Trace drill-down drawer: full entry + same-trace filtering. */
function TraceDrawer({
  entry,
  onClose,
}: {
  entry: T.LogEntry
  onClose: () => void
}) {
  return (
    <div
      role="dialog"
      aria-label="Log entry details"
      className="fixed inset-0 z-50 flex justify-end bg-black/30"
      onClick={onClose}
    >
      <div
        className="h-full w-full max-w-md overflow-y-auto border-s bg-card p-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold">Log entry</h2>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Close"
            onClick={onClose}
          >
            ✕
          </Button>
        </div>
        <dl className="flex flex-col gap-2 text-xs">
          <Field
            label="Time"
            value={new Date(entry.timestamp).toLocaleString()}
          />
          <Field label="Level" value={entry.level} />
          <Field label="Trace ID" value={entry.trace_id ?? "—"} />
          <Field label="Provider" value={entry.provider ?? "—"} />
        </dl>
        <p className="mt-3 rounded-md border bg-muted p-2 font-mono text-xs whitespace-pre-wrap">
          {entry.message}
        </p>
        {entry.attrs && Object.keys(entry.attrs).length > 0 ? (
          <pre className="mt-2 overflow-x-auto rounded-md border bg-muted p-2 text-[11px]">
            {JSON.stringify(entry.attrs, null, 2)}
          </pre>
        ) : null}
      </div>
    </div>
  )
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="w-20 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-all">{value}</dd>
    </div>
  )
}
