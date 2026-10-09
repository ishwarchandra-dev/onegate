import { useEffect, useMemo, useState } from "react"
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table"
import { TableSkeleton } from "~/components/skeletons"
import { api, ApiError } from "~/lib/api"
import type { T } from "~/lib/api"

const POLICIES = ["ordered", "weighted", "cost", "latency"] as const

/**
 * Models & routing view: registry, fallback-chain editor, health states.
 * Invalid chains are rejected client-side (validation before submit)
 * AND server-side (the gateway 400s unknown provider refs) — the
 * acceptance criterion for p6.view-routing.
 */
export default function RoutingView() {
  const [models, setModels] = useState<T.Model[]>([])
  const [providers, setProviders] = useState<T.Provider[]>([])
  const [rules, setRules] = useState<T.RoutingRule[]>([])
  const [health, setHealth] = useState<T.GetRoutingHealthResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<T.Model | "new" | null>(null)
  const [editRule, setEditRule] = useState<string | null>(null)

  function reload() {
    setLoading(true)
    Promise.all([
      api.listModels({ limit: 200 }),
      api.listProviders({ limit: 200 }),
      api.listRoutingRules({ limit: 200 }),
      api.getRoutingHealth({}),
    ])
      .then(([m, p, r, h]) => {
        setModels(m.items)
        setProviders(p.items)
        setRules(r.items)
        setHealth(h)
        setError(null)
      })
      .catch((e) =>
        setError(e instanceof ApiError ? e.message : "Request failed")
      )
      .finally(() => setLoading(false))
  }

  useEffect(reload, [])

  const providerName = useMemo(
    () => new Map(providers.map((p) => [p.id, p])),
    [providers]
  )
  const ruleByModel = useMemo(
    () => new Map(rules.filter((r) => r.enabled).map((r) => [r.model_id, r])),
    [rules]
  )
  const healthKey = (p: string, m: string) => `${p}|${m}`
  const healthByTarget = useMemo(
    () =>
      new Map(
        (health?.targets ?? []).map((t) => [
          healthKey(t.provider_id, t.model),
          t,
        ])
      ),
    [health]
  )

  if (loading && models.length === 0)
    return <TableSkeleton rows={5} columns={4} />
  if (error)
    return (
      <p
        role="alert"
        className="rounded-lg border bg-card p-4 text-sm text-destructive"
      >
        {error}
      </p>
    )

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-lg font-semibold">Models & Routing</h1>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => setEditing("new")}>
            Add model
          </Button>
          <Button variant="outline" size="sm" onClick={reload}>
            Refresh
          </Button>
        </div>
      </div>

      {models.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed bg-card/50 px-6 py-14 text-center">
          <p className="text-sm font-medium">No models registered</p>
          <p className="max-w-sm text-xs leading-relaxed text-muted-foreground">
            Register a canonical model and map it to provider targets — the
            ordered target list is its fallback chain.
          </p>
          <Button size="sm" className="mt-1" onClick={() => setEditing("new")}>
            Add model
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          {models.map((m) => {
            const rule = ruleByModel.get(m.id)
            return (
              <div key={m.id} className="rounded-lg border bg-card">
                <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold">{m.id}</span>
                    {m.aliases?.length ? (
                      <span className="text-xs text-muted-foreground">
                        aka {m.aliases.join(", ")}
                      </span>
                    ) : null}
                    <Select
                      value={rule?.policy ?? "ordered"}
                      onValueChange={async (policy) => {
                        try {
                          if (rule) {
                            await api.replaceRoutingRule(rule.id, {
                              model_id: m.id,
                              policy: policy as (typeof POLICIES)[number],
                            })
                          } else {
                            await api.createRoutingRule({
                              model_id: m.id,
                              policy: policy as (typeof POLICIES)[number],
                            })
                          }
                          reload()
                        } catch (e) {
                          alert(
                            e instanceof ApiError ? e.message : "Save failed"
                          )
                        }
                      }}
                    >
                      <SelectTrigger
                        className="h-6 w-32 text-xs"
                        aria-label={`Policy for ${m.id}`}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {POLICIES.map((p) => (
                          <SelectItem key={p} value={p}>
                            {p}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex gap-2">
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setEditing(m)}
                    >
                      Edit chain
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-destructive"
                      onClick={async () => {
                        if (!confirm(`Delete model "${m.id}"?`)) return
                        await api.deleteModel(m.id)
                        reload()
                      }}
                    >
                      Delete
                    </Button>
                  </div>
                </div>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10">#</TableHead>
                      <TableHead>Provider</TableHead>
                      <TableHead>Provider model</TableHead>
                      <TableHead>Weight</TableHead>
                      <TableHead>Health</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {m.targets.map((t, i) => {
                      const h = healthByTarget.get(
                        healthKey(t.provider_id, t.provider_model)
                      )
                      const dangling = !providerName.has(t.provider_id)
                      return (
                        <TableRow key={`${t.provider_id}/${t.provider_model}`}>
                          <TableCell className="tnum text-muted-foreground">
                            {t.position || i}
                          </TableCell>
                          <TableCell>
                            <span className={dangling ? "text-warning" : ""}>
                              {providerName.get(t.provider_id)?.name ??
                                t.provider_id}
                            </span>
                            {dangling ? (
                              <span className="ms-2 text-xs text-warning">
                                (provider missing)
                              </span>
                            ) : null}
                          </TableCell>
                          <TableCell className="text-muted-foreground">
                            {t.provider_model}
                          </TableCell>
                          <TableCell className="tnum">{t.weight}</TableCell>
                          <TableCell>
                            <HealthBadge state={h?.state ?? "closed"} />
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>
            )
          })}
        </div>
      )}

      {editing ? (
        <ModelDialog
          model={editing === "new" ? null : editing}
          providers={providers}
          onClose={() => setEditing(null)}
          onSaved={reload}
        />
      ) : null}
    </div>
  )
}

function HealthBadge({ state }: { state: string }) {
  if (state === "open")
    return <Badge className="bg-destructive/10 text-destructive">open</Badge>
  if (state === "half_open")
    return <Badge className="bg-warning/10 text-warning">half-open</Badge>
  return <Badge className="bg-success/10 text-success">closed</Badge>
}

type TargetDraft = {
  provider_id: string
  provider_model: string
  weight: number
  position: number
}

function ModelDialog({
  model,
  providers,
  onClose,
  onSaved,
}: {
  model: T.Model | null
  providers: T.Provider[]
  onClose: () => void
  onSaved: () => void
}) {
  const [id, setId] = useState(model?.id ?? "")
  const [aliases, setAliases] = useState((model?.aliases ?? []).join(", "))
  const [targets, setTargets] = useState<TargetDraft[]>(() =>
    (model?.targets ?? []).map((t, i) => ({
      provider_id: t.provider_id,
      provider_model: t.provider_model,
      weight: t.weight ?? 1,
      position: t.position ?? i,
    }))
  )
  const hasTargets = targets.length > 0
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  /** Client-side chain validation — mirrors the gateway's server-side
   * checks (unknown provider refs, empty targets, missing fields). */
  function validate(): string | null {
    if (!id.trim()) return "Model id is required"
    if (targets.length === 0) return "At least one target is required"
    for (const [i, t] of targets.entries()) {
      if (!t.provider_id) return `Target ${i + 1}: provider is required`
      if (!t.provider_model.trim())
        return `Target ${i + 1}: provider model is required`
      if (!providers.some((p) => p.id === t.provider_id))
        return `Target ${i + 1}: unknown provider "${t.provider_id}"`
      if (t.weight < 1) return `Target ${i + 1}: weight must be >= 1`
    }
    return null
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const verr = validate()
    if (verr) {
      setError(verr)
      return
    }
    setBusy(true)
    setError(null)
    const body: T.ModelInput = {
      id: id.trim(),
      aliases: aliases
        .split(",")
        .map((a) => a.trim())
        .filter(Boolean),
      targets: targets.map((t, i) => ({
        provider_id: t.provider_id,
        provider_model: t.provider_model.trim(),
        weight: t.weight,
        position: i,
        cost_multiplier: 100,
      })),
      capabilities: model?.capabilities ?? {
        tools: false,
        vision: false,
        json_mode: false,
        stream: true,
      },
    }
    try {
      if (model) await api.replaceModel(model.id, body)
      else await api.createModel(body)
      onSaved()
      onClose()
    } catch (err) {
      // Server-side rejection (invalid chain) surfaces here too.
      setError(err instanceof ApiError ? err.message : "Save failed")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{model ? `Edit ${model.id}` : "Add model"}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="m-id">Model ID</Label>
              <Input
                id="m-id"
                value={id}
                onChange={(e) => setId(e.target.value)}
                placeholder="gpt-4o"
                disabled={!!model}
                required
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="m-aliases">Aliases (comma-separated)</Label>
              <Input
                id="m-aliases"
                value={aliases}
                onChange={(e) => setAliases(e.target.value)}
                placeholder="gpt4o, gpt-4-o"
              />
            </div>
          </div>

          <div className="flex flex-col gap-2">
            <Label>Fallback chain (order = priority)</Label>
            <div className="flex flex-col gap-2">
              {targets.map((t, i) => (
                <div key={i} className="flex items-center gap-2">
                  <span className="tnum w-6 text-xs text-muted-foreground">
                    {i + 1}
                  </span>
                  <Select
                    value={t.provider_id}
                    onValueChange={(v) =>
                      setTargets((prev) =>
                        prev.map((x, j) =>
                          j === i ? { ...x, provider_id: v } : x
                        )
                      )
                    }
                  >
                    <SelectTrigger className="w-40">
                      <SelectValue placeholder="Provider" />
                    </SelectTrigger>
                    <SelectContent>
                      {providers.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Input
                    value={t.provider_model}
                    onChange={(e) =>
                      setTargets((prev) =>
                        prev.map((x, j) =>
                          j === i ? { ...x, provider_model: e.target.value } : x
                        )
                      )
                    }
                    placeholder="provider model name"
                    className="flex-1"
                    required
                  />
                  <Input
                    type="number"
                    min={1}
                    value={t.weight}
                    onChange={(e) =>
                      setTargets((prev) =>
                        prev.map((x, j) =>
                          j === i ? { ...x, weight: Number(e.target.value) } : x
                        )
                      )
                    }
                    className="tnum w-20"
                    aria-label={`Weight for target ${i + 1}`}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Move up target ${i + 1}`}
                    disabled={i === 0}
                    onClick={() =>
                      setTargets((prev) => {
                        const next = [...prev]
                        ;[next[i - 1], next[i]] = [next[i], next[i - 1]]
                        return next
                      })
                    }
                  >
                    ↑
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Remove target ${i + 1}`}
                    onClick={() =>
                      setTargets((prev) => prev.filter((_, j) => j !== i))
                    }
                  >
                    ✕
                  </Button>
                </div>
              ))}
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="self-start"
              onClick={() =>
                setTargets((prev) => [
                  ...prev,
                  {
                    provider_id: providers[0]?.id ?? "",
                    provider_model: "",
                    weight: 1,
                    position: prev.length,
                  },
                ])
              }
            >
              Add target
            </Button>
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
