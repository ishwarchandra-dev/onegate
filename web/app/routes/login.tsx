import { useEffect, useState } from "react"
import { useNavigate } from "react-router"
import { Button } from "~/components/ui/button"
import { Input } from "~/components/ui/input"
import { Label } from "~/components/ui/label"
import { api, ApiError, refreshSession } from "~/lib/api"

/**
 * Login / first-run setup. The gateway tells us which flow to render:
 * setup_required = the first-run wizard (creates the admin account),
 * otherwise the login form.
 */
export default function Login() {
  const navigate = useNavigate()
  const [mode, setMode] = useState<"loading" | "setup" | "login">("loading")
  const [username, setUsername] = useState("admin")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    // Already signed in? Straight to the dashboard.
    refreshSession()
      .then((s) => {
        if (s) navigate("/providers", { replace: true })
        else return api.getSetupStatus()
      })
      .then((status) => {
        if (status) setMode(status.setup_required ? "setup" : "login")
      })
      .catch(() => setMode("login"))
  }, [navigate])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (mode === "setup") {
        await api.createAdmin({ username, password })
      } else {
        await api.login({ username, password })
      }
      await refreshSession()
      navigate("/providers", { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong")
    } finally {
      setBusy(false)
    }
  }

  if (mode === "loading") {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <p className="text-sm text-muted-foreground">Connecting to OneGate…</p>
      </div>
    )
  }

  return (
    <div className="flex min-h-svh items-center justify-center p-6" dir="ltr">
      <form
        onSubmit={submit}
        className="flex w-full max-w-sm flex-col gap-5 rounded-lg border bg-card p-6"
      >
        <div className="flex flex-col gap-1">
          <h1 className="text-lg font-semibold">OneGate</h1>
          <p className="text-xs leading-relaxed text-muted-foreground">
            {mode === "setup"
              ? "First run: create the administrator account for this gateway. This can only be done once."
              : "Sign in to the gateway control plane."}
          </p>
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="username">Username</Label>
          <Input
            id="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            required
          />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="password">Password</Label>
          <Input
            id="password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={
              mode === "setup" ? "new-password" : "current-password"
            }
            placeholder={
              mode === "setup" ? "At least 12 characters" : undefined
            }
            required
          />
        </div>

        {error ? (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        ) : null}

        <Button type="submit" disabled={busy}>
          {busy
            ? "Working…"
            : mode === "setup"
              ? "Create admin account"
              : "Sign in"}
        </Button>
      </form>
    </div>
  )
}
