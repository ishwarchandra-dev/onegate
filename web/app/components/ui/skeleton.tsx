import { cn } from "cn"

/**
 * Base skeleton primitive. Renders a direction-neutral pulse block
 * (RTL-safe by construction — no gradient axis to mirror; see
 * `.og-skeleton` in app.css). Respect prefers-reduced-motion.
 */
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("og-skeleton", className)}
      {...props}
    />
  )
}

export { Skeleton }
