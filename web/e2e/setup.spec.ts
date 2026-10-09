import { test as setup } from "@playwright/test"
import { signIn } from "./helpers"

/**
 * One-time authentication for the whole suite: performs first-run setup
 * (or login) once and persists the session cookie as storage state, so
 * journeys don't each burn an auth-class request (10/min per IP).
 */
setup("authenticate", async ({ page }) => {
  await signIn(page)
  await page.context().storageState({ path: ".auth/state.json" })
})
