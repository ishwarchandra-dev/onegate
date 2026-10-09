import { expect, type Page } from "@playwright/test"

/**
 * Sign in through the UI. Handles all three states:
 *  - fresh gateway: the first-run setup wizard (creates the admin)
 *  - logged out: the login form
 *  - already authenticated (playwright storage state): the app routes
 *    straight to the dashboard — no form to fill
 */
export async function signIn(page: Page) {
  await page.goto("/")
  const submit = page.getByRole("button", { name: /Create admin account|Sign in/ })
  try {
    await submit.waitFor({ state: "visible", timeout: 5_000 })
  } catch {
    // Already authenticated: the login route redirected to the app.
    await expect(page).toHaveURL(/providers/)
    return
  }
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill("correct-horse-battery")
  await submit.click()
  await expect(page).toHaveURL(/providers/)
}
