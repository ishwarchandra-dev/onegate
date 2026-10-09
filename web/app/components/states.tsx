import type * as React from "react"
import { cn } from "cn"
import { Button } from "~/components/ui/button"

/*
 * OneGate empty & error states — designed once, reused by every view
 * (design-engineer charter). Each state is a named pattern with a fixed
 * hierarchy: icon -> title -> description -> optional action.
 *
 * RTL: all layout uses logical properties; directional icons must be
 * marked `data-icon="directional"` and mirrored via rtl:rotate-180 in the
 * consuming view.
 */

type StateProps = {
  title: string
  description?: string
  icon?: React.ReactNode
  action?: React.ReactNode
  className?: string
}

function StateShell({
  title,
  description,
  icon,
  action,
  className,
  "data-slot": dataSlot,
}: StateProps & { "data-slot"?: string }) {
  return (
    <div
      data-slot={dataSlot}
      className={cn(
        "flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed bg-card/50 px-6 py-14 text-center",
        className
      )}
    >
      {icon ? (
        <div
          aria-hidden="true"
          className="flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground [&_svg]:size-5"
        >
          {icon}
        </div>
      ) : null}
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">{title}</p>
        {description ? (
          <p className="mx-auto max-w-sm text-xs leading-relaxed text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {action ? <div className="pt-1">{action}</div> : null}
    </div>
  )
}

/** Empty state: no data yet (fresh install, empty filters). */
export function EmptyState(props: StateProps) {
  return <StateShell data-slot="empty-state" {...props} />
}

/**
 * Error state: the API call failed. `onRetry` renders a retry button —
 * the only universal recovery action in the dashboard.
 */
export function ErrorState({
  message,
  onRetry,
  title,
  ...props
}: Omit<StateProps, "description" | "action" | "title"> & {
  message?: string
  onRetry?: () => void
  title?: string
}) {
  return (
    <StateShell
      data-slot="error-state"
      title={title ?? "Something went wrong"}
      description={
        message ?? "The request failed. Check the gateway logs and retry."
      }
      icon={props.icon}
      className={props.className}
      action={
        onRetry ? (
          <Button variant="outline" size="sm" onClick={onRetry}>
            Retry
          </Button>
        ) : undefined
      }
    />
  )
}

/**
 * Live-stream gap indicator: rendered when the SSE feed drops entries
 * because the client fell behind (p6.view-logs acceptance: backpressure
 * drops are visible as a gap indicator, not a freeze).
 */
export function StreamGapIndicator({ count }: { count: number }) {
  return (
    <div
      role="status"
      className="flex items-center gap-2 border-y border-dashed bg-muted/50 px-3 py-1.5 text-start text-xs text-muted-foreground"
    >
      <span
        aria-hidden="true"
        className="inline-block size-1.5 shrink-0 rounded-full bg-warning"
      />
      <span className="tnum">
        {count} event{count === 1 ? "" : "s"} dropped — feed resumed from live
      </span>
    </div>
  )
}
