import type * as React from "react"
import { cn } from "cn"
import { Skeleton } from "~/components/ui/skeleton"

/*
 * OneGate loading skeletons — composed, not improvised (design-engineer
 * charter: every view states its empty state and its loading skeleton).
 *
 * Density contract (docs/design-system.md):
 *   - tables compact  : 8 rows x 28px
 *   - analytics airy  : chart plot area 220px with breathing room
 *   - forms comfortable: labeled field rows, 12px gaps
 *
 * All spacing uses logical properties (ps/pe/me) so RTL mirrors
 * automatically.
 */

export function SkeletonText({
  className,
  width = "w-full",
}: {
  className?: string
  width?: string
}) {
  return <Skeleton className={cn("h-3.5 rounded-xs", width, className)} />
}

/** Compact table skeleton: header row + N data rows at table density. */
export function TableSkeleton({
  rows = 8,
  columns = 5,
  className,
}: {
  rows?: number
  columns?: number
  className?: string
}) {
  return (
    <div
      data-slot="table-skeleton"
      role="status"
      aria-label="Loading table"
      className={cn("w-full space-y-2.5", className)}
    >
      <div className="flex items-center gap-4 border-b pb-2">
        {Array.from({ length: columns }).map((_, i) => (
          <SkeletonText key={i} width="w-1/5" className="h-3" />
        ))}
      </div>
      {Array.from({ length: rows }).map((_, r) => (
        <div key={r} className="flex items-center gap-4 py-1.5">
          {Array.from({ length: columns }).map((_, c) => (
            <SkeletonText
              key={c}
              width={c === 0 ? "w-1/6" : "w-1/5"}
              className={c === columns - 1 ? "ms-auto w-12" : undefined}
            />
          ))}
        </div>
      ))}
    </div>
  )
}

/** Stat card skeleton for dashboard summary cards (KPI tiles). */
export function StatCardSkeleton({ className }: { className?: string }) {
  return (
    <div
      data-slot="stat-card-skeleton"
      role="status"
      aria-label="Loading statistic"
      className={cn(
        "flex flex-col gap-2 rounded-lg border bg-card p-4",
        className
      )}
    >
      <SkeletonText width="w-20" className="h-3" />
      <Skeleton className="h-7 w-28" />
      <SkeletonText width="w-16" className="h-3" />
    </div>
  )
}

/**
 * Chart skeleton: airy plot area with axis rails and band placeholders
 * so the layout does not jump when real data arrives.
 */
export function ChartSkeleton({ className }: { className?: string }) {
  return (
    <div
      data-slot="chart-skeleton"
      role="status"
      aria-label="Loading chart"
      className={cn("w-full", className)}
    >
      <div className="mb-4 flex items-center justify-between">
        <SkeletonText width="w-32" />
        <div className="flex gap-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <SkeletonText key={i} width="w-14" className="h-3" />
          ))}
        </div>
      </div>
      <div className="flex h-56 gap-3">
        <div className="flex flex-col justify-between pe-1">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-2 w-8" />
          ))}
        </div>
        <div className="flex flex-1 flex-col justify-end gap-2 border-s-0 border-b border-chart-grid pb-0">
          <div className="flex flex-1 items-end gap-2">
            {[38, 62, 45, 78, 52, 66, 41, 70, 58, 47, 74, 50].map((h, i) => (
              <Skeleton
                key={i}
                style={{ height: `${h}%` }}
                className="min-h-2 flex-1 rounded-sm"
              />
            ))}
          </div>
        </div>
      </div>
      <div className="mt-2 flex justify-between">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-2.5 w-10" />
        ))}
      </div>
    </div>
  )
}

/** Form skeleton: comfortable density — labeled fields with gaps. */
export function FormSkeleton({
  fields = 4,
  className,
}: {
  fields?: number
  className?: string
}) {
  return (
    <div
      data-slot="form-skeleton"
      role="status"
      aria-label="Loading form"
      className={cn("flex flex-col gap-5", className)}
    >
      {Array.from({ length: fields }).map((_, i) => (
        <div key={i} className="flex flex-col gap-2">
          <SkeletonText width="w-24" className="h-3.5" />
          <Skeleton className="h-8 w-full rounded-md" />
        </div>
      ))}
      <div className="flex justify-end gap-2 pt-2">
        <Skeleton className="h-7 w-16 rounded-md" />
        <Skeleton className="h-7 w-20 rounded-md" />
      </div>
    </div>
  )
}

/** Live log stream skeleton: monospace-ish line blocks. */
export function LogStreamSkeleton({
  lines = 12,
  className,
}: {
  lines?: number
  className?: string
}) {
  return (
    <div
      data-slot="log-stream-skeleton"
      role="status"
      aria-label="Loading logs"
      className={cn("flex flex-col gap-1.5", className)}
    >
      {Array.from({ length: lines }).map((_, i) => (
        <div key={i} className="flex items-center gap-3">
          <Skeleton className="h-3 w-16 shrink-0" />
          <Skeleton className="h-3 w-10 shrink-0" />
          {/* Literal class names (Tailwind scans source text). */}
          <Skeleton
            className={cn(
              "h-3 shrink-0",
              ["w-1/2", "w-2/3", "w-5/12", "w-3/4", "w-7/12"][i % 5]
            )}
          />
        </div>
      ))}
    </div>
  )
}

/** Full-page skeleton used during route-level data loading. */
export function PageSkeleton({ className }: { className?: string }) {
  return (
    <div
      data-slot="page-skeleton"
      className={cn("flex flex-col gap-6 p-6", className)}
    >
      <div className="flex items-center justify-between">
        <SkeletonText width="w-40" className="h-5" />
        <Skeleton className="h-7 w-24 rounded-md" />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <StatCardSkeleton key={i} />
        ))}
      </div>
      <div className="rounded-lg border bg-card p-4">
        <ChartSkeleton />
      </div>
    </div>
  )
}
