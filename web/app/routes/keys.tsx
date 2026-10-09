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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table"
import { TableSkeleton } from "~/components/skeletons"
import { api, ApiError, formatMS, formatMicroUSD } from "~/lib/api"
import type { T } from "~/lib/api"

/**
 * Virtual keys view. The raw key is displayed exactly once — in a
 * copy-to-clipboard dialog right after minting, with an explicit
 * warning — and is unrecoverable afterwards (show-once semantics).
 */
export default function KeysView() {
  const [items, setItems] = useState<T.VirtualKey[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [minted, setMinted] = useState<{
    raw: string
    key: T.VirtualKey
  } | null>(null)

  function reload() {
    setLoading(true)
    api
      .listKeys({ limit: 200 })
      .then((p) => {
        setItems(p.items)
        setError(null)
      })
      .catch((e) =>
        setError(e instanceof ApiError ? e.message : "Request failed")
      )
      .finally(() => setLoading(false))
  }

  useEffect(reload, [])

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-lg font-semibold">Virtual Keys</h1>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => setCreating(true)}>
            Mint key
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
          <p className="text-sm font-medium">No virtual keys</p>
          <p className="max-w-sm text-xs leading-relaxed text-muted-foreground">
            Mint a key to let clients call the gateway. The raw key is shown
            exactly once at creation.
          </p>
          <Button size="sm" className="mt-1" onClick={() => setCreating(true)}>
            Mint key
          </Button>
        </div>
      ) : (
        <div className="rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Prefix</TableHead>
                <TableHead>Limits</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="text-end">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="tnum text-muted-foreground">
                    {k.prefix}…
                  </TableCell>
                  <TableCell className="tnum text-xs text-muted-foreground">
                    {keyLimitsLabel(k.limits)}
                  </TableCell>
                  <TableCell>
                    {k.status === "active" ? (
                      <Badge className="bg-success/10 text-success">
                        active
                      </Badge>
                    ) : (
                      <Badge className="bg-destructive/10 text-destructive">
                        revoked
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="tnum text-xs text-muted-foreground">
                    {formatMS(k.last_used_ms ?? undefined)}
                  </TableCell>
                  <TableCell className="text-end">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={k.status !== "active"}
                        onClick={async () => {
                          if (
                            !confirm(
                              `Revoke key "${k.name}"? Clients using it will get 403.`
                            )
                          )
                            return
                          await api.revokeKey(k.id)
                          reload()
                        }}
                      >
                        Revoke
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-destructive"
                        onClick={async () => {
                          if (
                            !confirm(
                              `Permanently delete "${k.name}"? Its usage history link is lost (revoke keeps it).`
                            )
                          )
                            return
                          await api.deleteKey(k.id)
                          reload()
                        }}
                      >
                        Delete
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {creating ? (
        <CreateKeyDialog
          onClose={() => setCreating(false)}
          onMinted={(raw, key) => {
            setCreating(false)
            setMinted({ raw, key })
          }}
          onDone={reload}
        />
      ) : null}

      {minted ? (
        <ShowOnceDialog minted={minted} onClose={() => setMinted(null)} />
      ) : null}
    </div>
  )
}

function keyLimitsLabel(l: T.KeyLimits): string {
  const parts: string[] = []
  if (l.rpm) parts.push(`${l.rpm} rpm`)
  if (l.tpm) parts.push(`${l.tpm} tpm`)
  if (l.concurrency) parts.push(`${l.concurrency} concurrent`)
  if (l.max_spend_usd_micros)
    parts.push(`cap ${formatMicroUSD(l.max_spend_usd_micros)}`)
  return parts.length ? parts.join(" · ") : "unlimited"
}

function CreateKeyDialog({
  onClose,
  onMinted,
  onDone,
}: {
  onClose: () => void
  onMinted: (raw: string, key: T.VirtualKey) => void
  onDone: () => void
}) {
  const [name, setName] = useState("")
  const [rpm, setRpm] = useState("")
  const [tpm, setTpm] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await api.createKey({
        name,
        limits: {
          rpm: rpm ? Number(rpm) : 0,
          tpm: tpm ? Number(tpm) : 0,
        },
      })
      onMinted(res.raw_key, res.key)
      onDone()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Mint failed")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Mint virtual key</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="k-name">Name</Label>
            <Input
              id="k-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="production-app"
              required
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="k-rpm">RPM limit (0 = unlimited)</Label>
              <Input
                id="k-rpm"
                type="number"
                min={0}
                value={rpm}
                onChange={(e) => setRpm(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="k-tpm">TPM limit (0 = unlimited)</Label>
              <Input
                id="k-tpm"
                type="number"
                min={0}
                value={tpm}
                onChange={(e) => setTpm(e.target.value)}
              />
            </div>
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
              {busy ? "Minting…" : "Mint key"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The show-once dialog: raw key + copy button + warning. This is the
 * ONLY place the raw key ever appears (p6.view-keys acceptance).
 */
function ShowOnceDialog({
  minted,
  onClose,
}: {
  minted: { raw: string; key: T.VirtualKey }
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(minted.raw)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard unavailable (insecure context): select-all fallback.
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Key created — copy it now</DialogTitle>
        </DialogHeader>
        <p role="alert" className="text-xs leading-relaxed text-warning">
          This is the only time the raw key is shown. It is stored as a one-way
          hash and cannot be recovered — if you lose it, mint a new key.
        </p>
        <div className="flex items-center gap-2">
          <code className="flex-1 overflow-x-auto rounded-md border bg-muted p-2 text-xs">
            {minted.raw}
          </code>
          <Button size="sm" onClick={copy}>
            {copied ? "Copied ✓" : "Copy"}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Key: <strong>{minted.key.name}</strong> · clients pass it as the API
          credential on /v1 endpoints.
        </p>
        <DialogFooter>
          <Button onClick={onClose}>I saved it — close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
