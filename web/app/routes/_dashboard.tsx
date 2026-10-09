import { useEffect, useState } from "react"
import { NavLink, Outlet, useNavigate } from "react-router"
import { Button } from "~/components/ui/button"
import { api, refreshSession, type T } from "~/lib/api"

const NAV = [
  { to: "/providers", label: "Providers" },
  { to: "/routing", label: "Models & Routing" },
  { to: "/keys", label: "Virtual Keys" },
  { to: "/usage", label: "Usage" },
  { to: "/logs", label: "Live Logs" },
  { to: "/settings", label: "Settings" },
]

/**
 * Dashboard layout: session gate + shell. Unauthenticated visitors are
 * routed to /login; every child route renders inside the shell.
 */
export default function Dashboard() {
  const navigate = useNavigate()
  const [session, setSession] = useState<T.SessionInfo | null>(null)
  const [ready, setReady] = useState(false)

  useEffect(() => {
    refreshSession()
      .then((s) => {
        if (!s) {
          navigate("/", { replace: true })
          return
        }
        setSession(s)
      })
      .finally(() => setReady(true))
  }, [navigate])

  if (!ready) {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }
  if (!session) return null

  return (
    <div className="flex min-h-svh" dir="ltr">
      <aside className="hidden w-56 shrink-0 flex-col border-e bg-sidebar text-sidebar-foreground md:flex">
        <div className="flex items-center gap-2 px-4 py-4">
          <span
            className="size-2 rounded-full bg-sidebar-primary"
            aria-hidden="true"
          />
          <span className="text-sm font-semibold">OneGate</span>
        </div>
        <nav
          className="flex flex-1 flex-col gap-0.5 px-2"
          aria-label="Dashboard"
        >
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                `rounded-md px-3 py-1.5 text-sm transition-colors ${
                  isActive
                    ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                    : "text-muted-foreground hover:bg-sidebar-accent/50 hover:text-sidebar-foreground"
                }`
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t p-3">
          <p className="truncate text-xs text-muted-foreground">
            {session.user.username}
          </p>
          <Button
            variant="ghost"
            size="sm"
            className="mt-1 w-full justify-start"
            onClick={async () => {
              await api.logout()
              navigate("/", { replace: true })
            }}
          >
            Sign out
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile nav: horizontal, logical scroll */}
        <nav
          className="flex gap-1 overflow-x-auto border-b px-3 py-2 md:hidden"
          aria-label="Dashboard"
        >
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                `rounded-md px-3 py-1 text-xs whitespace-nowrap ${
                  isActive
                    ? "bg-muted font-medium text-foreground"
                    : "text-muted-foreground"
                }`
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <main className="min-w-0 flex-1 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
