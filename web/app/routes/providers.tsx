import { useEffect, useState } from "react"
import { Badge } from "~/components/ui/badge"
import { Button } from "~/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select"
import { Switch } from "~/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table"
import { TableSkeleton } from "~/components/skeletons"
import { api, ApiError, formatMS } from "~/lib/api"
import type { T } from "~/lib/api"

const PROTOCOLS = ["openai", "anthropic", "gemini", "openai-compat"] as const

/**
 * Providers view: list, add/edit (masked keys), enable/disable, test
 * connection. The test probe surfaces per-protocol error detail from
 * the gateway (ProviderTestResult.error.error is the provider's own
 * status/type, mapped onto the canonical envelope).
 */
export default function ProvidersView() {
  const [items, setItems] = useState<T.Provider[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<T.Provider | "new" | null>(null)
  const [probe, setProbe] = useState<Record<string, ProbeState>>({})

  function reload() {
    setLoading(true)
    api
      .listProviders({ limit: 200 })
      .then((page) => {
        setItems(page.items)
        setError(null)
      })
      .catch((e) =>
        setError(e instanceof ApiError ? e.message : "Request failed")
      )
      .finally(() => setLoading(false))
  }

  useEffect(reload, [])

  async function runTest(p: T.Provider) {
    setProbe((s) => ({ ...s, [p.id]: { status: "loading" } }))
    try {
      const result = await api.testProvider(p.id)
      setProbe((s) => ({ ...s, [p.id]: { status: "done", result } }))
    } catch (e) {
      setProbe((s) => ({
        ...s,
        [p.id]: {
          status: "done",
          result: {
            ok: false,
            latency_ms: 0,
            error: {
              error: {
                status: 0,
                type: "api_error",
                message: e instanceof ApiError ? e.message : "probe failed",
                retryable: false,
              },
            },
          },
        },
      }))
    }
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-lg font-semibold">Providers</h1>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => setEditing("new")}>
            Add provider
          </Button>
          <Button variant="outline" size="sm" onClick={reload}>
            Refresh
          </Button>
        </div>
      </div>

      {loading && items.length === 0 ? (
        <TableSkeleton rows={4} columns={5} />
      ) : error ? (
        <p
          role="alert"
          className="rounded-lg border bg-card p-4 text-sm text-destructive"
        >
          {error}
        </p>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed bg-card/50 px-6 py-14 text-center">
          <p className="text-sm font-medium">No providers configured</p>
          <p className="max-w-sm text-xs leading-relaxed text-muted-foreground">
            Add an OpenAI, Anthropic, Gemini, or OpenAI-compatible provider to
            route requests through the gateway.
          </p>
          <Button size="sm" className="mt-1" onClick={() => setEditing("new")}>
            Add provider
          </Button>
        </div>
      ) : (
        <div className="rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Protocol</TableHead>
                <TableHead>Base URL</TableHead>
                <TableHead>API key</TableHead>
                <TableHead>Enabled</TableHead>
                <TableHead className="text-end">Updated</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((p) => (
                <TableRow key={p.id}>
                  <TableCell className="font-medium">{p.name}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{p.protocol}</Badge>
                  </TableCell>
                  <TableCell className="max-w-48 truncate text-muted-foreground">
                    {p.base_url}
                  </TableCell>
                  <TableCell className="tnum text-muted-foreground">
                    {p.masked_key ?? "—"}
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={p.enabled}
                      aria-label={`Enable ${p.name}`}
                      onCheckedChange={async (checked) => {
                        // Optimistic flip; revert on failure.
                        setItems((prev) =>
                          prev.map((x) =>
                            x.id === p.id ? { ...x, enabled: checked } : x
                          )
                        )
                        try {
                          await api.updateProvider(p.id, { enabled: checked })
                        } catch {
                          setItems((prev) =>
                            prev.map((x) =>
                              x.id === p.id ? { ...x, enabled: !checked } : x
                            )
                          )
                        }
                      }}
                    />
                  </TableCell>
                  <TableCell className="text-end">
                    <div className="flex items-center justify-end gap-2">
                      <ProbeResult state={probe[p.id]} />
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => runTest(p)}
                        disabled={probe[p.id]?.status === "loading"}
                      >
                        {probe[p.id]?.status === "loading"
                          ? "Testing…"
                          : "Test"}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setEditing(p)}
                      >
                        Edit
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-destructive"
                        onClick={async () => {
                          if (!confirm(`Delete provider "${p.name}"?`)) return
                          await api.deleteProvider(p.id)
                          reload()
                        }}
                      >
                        Delete
                      </Button>
                      <span className="tnum w-32 text-xs text-muted-foreground">
                        {formatMS(p.updated_ms)}
                      </span>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {editing ? (
        <ProviderDialog
          provider={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={reload}
        />
      ) : null}
    </div>
  )
}

type ProbeState =
  { status: "loading" } | { status: "done"; result: T.ProviderTestResult }

/** Per-protocol probe outcome: green ok, or the provider's error detail. */
function ProbeResult({ state }: { state: ProbeState | undefined }) {
  if (!state || state.status === "loading") return null
  const r = state.result
  if (r.ok) {
    return (
      <span className="tnum text-xs text-success" role="status">
        ✓ {r.latency_ms}ms
      </span>
    )
  }
  const e = r.error?.error
  return (
    <span
      className="text-xs text-destructive"
      title={e ? `${e.status} ${e.type}: ${e.message}` : "probe failed"}
      role="alert"
    >
      ✗ {e ? `${e.status} ${e.type}` : "failed"}
    </span>
  )
}

function ProviderDialog({
  provider,
  onClose,
  onSaved,
}: {
  provider: T.Provider | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(provider?.name ?? "")
  const [protocol, setProtocol] = useState<string>(
    provider?.protocol ?? "openai"
  )
  const [baseURL, setBaseURL] = useState(provider?.base_url ?? "")
  const [apiKey, setApiKey] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (provider) {
        // Omitting api_key keeps the stored credential (spec semantics).
        await api.updateProvider(provider.id, {
          name,
          base_url: baseURL,
          ...(apiKey ? { api_key: apiKey } : {}),
        })
      } else {
        await api.createProvider({
          name,
          protocol: protocol as T.ProviderCreate["protocol"],
          base_url: baseURL,
          api_key: apiKey || undefined,
        })
      }
      onSaved()
      onClose()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Save failed")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {provider ? "Edit provider" : "Add provider"}
          </DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="p-name">Name</Label>
            <Input
              id="p-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label>Protocol</Label>
            <Select
              value={protocol}
              onValueChange={setProtocol}
              disabled={!!provider}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PROTOCOLS.map((p) => (
                  <SelectItem key={p} value={p}>
                    {p}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="p-url">Base URL</Label>
            <Input
              id="p-url"
              type="url"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder="https://api.openai.com/v1"
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="p-key">
              API key {provider ? "(leave blank to keep current)" : ""}
            </Label>
            <Input
              id="p-key"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={
                provider ? (provider.masked_key ?? "unchanged") : "sk-…"
              }
              autoComplete="new-password"
            />
            <p className="text-xs text-muted-foreground">
              Stored encrypted (AES-GCM); only a masked form is ever shown.
            </p>
          </div>
          {error ? (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
