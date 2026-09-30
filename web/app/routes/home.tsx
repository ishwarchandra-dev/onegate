import { Button } from "~/components/ui/button"

export default function Home() {
  return (
    <div className="flex min-h-svh p-6" dir="ltr">
      <div className="flex max-w-md min-w-0 flex-col gap-4 text-sm leading-loose">
        <div>
          <h1 className="text-lg font-semibold">OneGate</h1>
          <p>
            LLM gateway control plane — OmniRoute v3.8.52, rebuilt in Go as a
            single binary with this dashboard embedded.
          </p>
          <p className="text-muted-foreground">
            Phase 0 scaffold. Provider management, virtual keys, routing
            rules, usage analytics and live logs arrive in later phases — see{" "}
            <code>tasks/</code> in the repo root.
          </p>
          <div className="mt-2 flex gap-2">
            <Button>Get started</Button>
            <Button variant="outline">Docs</Button>
          </div>
        </div>
      </div>
    </div>
  )
}
