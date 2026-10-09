import { useEffect, useState } from "react"
import { api, ApiError } from "~/lib/api"

/**
 * Settings: read-only view of the effective runtime configuration
 * (flags > env > file > defaults), as resolved by the gateway. No
 * secrets — paths only.
 */
export default function SettingsView() {
  const [status, setStatus] = useState<Awaited<
    ReturnType<typeof api.getSystemStatus>
  > | null>(null)
  const [config, setConfig] = useState<Awaited<
    ReturnType<typeof api.getEffectiveConfig>
  > | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    Promise.all([api.getSystemStatus(), api.getEffectiveConfig()])
      .then(([s, c]) => {
        setStatus(s)
        setConfig(c)
      })
      .catch((e) =>
        setError(e instanceof ApiError ? e.message : "Request failed")
      )
  }, [])

  if (error)
    return (
      <p
        role="alert"
        className="rounded-lg border bg-card p-4 text-sm text-destructive"
      >
        {error}
      </p>
    )
  if (!status || !config)
    return <p className="text-sm text-muted-foreground">Loading…</p>

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <h1 className="text-lg font-semibold">Settings</h1>

      <section className="rounded-lg border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold">Gateway</h2>
        <dl className="grid grid-cols-1 gap-x-8 gap-y-2 text-xs sm:grid-cols-2">
          <Row label="Version" value={status.version} />
          <Row label="Commit" value={status.git_commit || "—"} />
          <Row label="Schema version" value={String(status.schema_version)} />
          <Row label="Uptime" value={formatDuration(status.uptime_ms)} />
        </dl>
      </section>

      <section className="rounded-lg border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold">Listener</h2>
        <dl className="grid grid-cols-1 gap-x-8 gap-y-2 text-xs sm:grid-cols-2">
          <Row label="Bind address" value={`${config.host}:${config.port}`} />
          <Row label="Log level" value={config.log_level} />
          <Row label="Data directory" value={config.data_dir} />
          <Row
            label="Config hot-reload"
            value={
              config.reload.enabled
                ? `on (poll ${config.reload.poll_ms}ms)`
                : "off"
            }
          />
        </dl>
      </section>

      <section className="rounded-lg border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold">HTTP timeouts (ms)</h2>
        <dl className="grid grid-cols-2 gap-x-8 gap-y-2 text-xs sm:grid-cols-4">
          <Row
            label="Read header"
            value={String(config.http.read_header_timeout_ms)}
          />
          <Row
            label="Read"
            value={
              config.http.read_timeout_ms
                ? String(config.http.read_timeout_ms)
                : "off"
            }
          />
          <Row
            label="Write"
            value={
              config.http.write_timeout_ms
                ? String(config.http.write_timeout_ms)
                : "off (streams never cut)"
            }
          />
          <Row label="Idle" value={String(config.http.idle_timeout_ms)} />
        </dl>
      </section>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2 border-b border-dashed py-1.5">
      <dt className="w-32 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-all">{value}</dd>
    </div>
  )
}

function formatDuration(ms: number): string {
  const s = Math.floor(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}
