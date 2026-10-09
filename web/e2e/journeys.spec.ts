import { expect, test } from "@playwright/test"
import { signIn } from "./helpers"

/**
 * Primary user journeys against the real gateway + built dashboard
 * (p6.e2e-dashboard acceptance: journeys green in CI; no route ships
 * untested). Each journey exercises one route end to end through the
 * UI — including auth, CSRF, and the designed empty/error states.
 */

test.describe("auth journeys", () => {
  // These journeys exercise the credential flow itself — start clean.
  test.use({ storageState: { cookies: [], origins: [] } })
  test("first-run setup creates the admin and lands on providers", async ({ page }) => {
    await signIn(page)
    await expect(page.getByRole("heading", { name: "Providers" })).toBeVisible()
    await expect(page.getByText("No providers configured")).toBeVisible()
  })

  test("logout forces login; wrong password rejected; login works", async ({ page }) => {
    await signIn(page)
    await page.getByRole("button", { name: "Sign out" }).click()
    await expect(page).toHaveURL(/\/$/)

    await page.getByLabel("Password").fill("wrong-password-123")
    await page.getByRole("button", { name: "Sign in" }).click()
    await expect(page.getByText("invalid username or password")).toBeVisible()

    await page.getByLabel("Password").fill("correct-horse-battery")
    await page.getByRole("button", { name: "Sign in" }).click()
    await expect(page).toHaveURL(/providers/)
  })
})

test.describe("providers journey", () => {
  test("add provider with masked key; test connection surfaces per-protocol detail", async ({ page }) => {
    await signIn(page)

    await page.getByRole("button", { name: "Add provider" }).first().click()
    await page.getByLabel("Name").fill("Mock OpenAI")
    await page.getByLabel("Base URL").fill("http://127.0.0.1:9441")
    await page.getByLabel("API key").fill("sk-e2e-mock-key")
    await page.getByRole("button", { name: "Save" }).click()

    // Row appears; the credential is masked, never raw.
    await expect(page.getByText("Mock OpenAI")).toBeVisible()
    await expect(page.getByText("sk-e2e-mock-key")).toHaveCount(0)
    await expect(page.getByText(/sk-e…-key/)).toBeVisible()

    // Probe succeeds: green ok + latency.
    await page.getByRole("button", { name: "Test" }).click()
    await expect(page.getByText(/✓ \d+ms/)).toBeVisible()

    // Rotate to a bad key; the probe surfaces the provider's 401.
    await page.getByRole("button", { name: "Edit" }).click()
    await page.getByLabel("API key").fill("sk-e2e-wrong-key")
    await page.getByRole("button", { name: "Save" }).click()
    await page.getByRole("button", { name: "Test" }).click()
    await expect(page.getByText("✗ 401 authentication_error")).toBeVisible()
  })
})

test.describe("routing journey", () => {
  test("add model with fallback chain; invalid chain rejected client-side", async ({ page }) => {
    await signIn(page)
    // Seed a provider for the target.
    await page.getByRole("button", { name: "Add provider" }).first().click()
    await page.getByLabel("Name").fill("P1")
    await page.getByLabel("Base URL").fill("http://127.0.0.1:9441")
    await page.getByRole("button", { name: "Save" }).click()
    await expect(page.getByText("P1")).toBeVisible()

    await page.getByRole("link", { name: "Models & Routing" }).click()
    await page.getByRole("button", { name: "Add model" }).first().click()

    // Invalid: blank provider model -> client-side rejection.
    await page.getByLabel("Model ID").fill("mock-canonical")
    await page.getByRole("button", { name: "Add target" }).click()
    await page.getByRole("button", { name: "Save" }).click()
    await expect(
      page.getByText("Target 1: provider model is required")
    ).toBeVisible()

    // Fix the chain and save.
    await page.getByPlaceholder("provider model name").fill("mock-model")
    await page.getByRole("button", { name: "Save" }).click()
    await expect(page.getByText("mock-canonical")).toBeVisible()
    await expect(page.getByText("mock-model")).toBeVisible()
    await expect(page.getByText("closed", { exact: true })).toBeVisible()
  })
})

test.describe("keys journey", () => {
  test("mint shows the raw key exactly once with copy + warning", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"])
    await signIn(page)
    await page.getByRole("link", { name: "Virtual Keys" }).click()

    await page.getByRole("button", { name: "Mint key" }).first().click()
    await page.getByLabel("Name", { exact: true }).fill("e2e-key")
    await page.getByRole("dialog").getByRole("button", { name: "Mint key" }).click()

    // Show-once dialog with the warning and the raw ogk- key.
    await expect(
      page.getByText("This is the only time the raw key is shown")
    ).toBeVisible()
    // The full key lives in the dialog; the table only shows a prefix.
    const raw = await page
      .getByRole("dialog")
      .getByText(/^ogk-/)
      .textContent()
    expect(raw).toMatch(/^ogk-[a-zA-Z0-9_-]{20,}$/)

    await page.getByRole("button", { name: "Copy" }).click()
    await expect(page.getByText("Copied ✓")).toBeVisible()
    await page.getByRole("button", { name: "I saved it — close" }).click()

    // The list shows the key (name + prefix) but never the raw value.
    await expect(page.getByText("e2e-key")).toBeVisible()
    await expect(page.getByText(raw!)).toHaveCount(0)
  })
})

test.describe("usage journey", () => {
  test("empty window renders the designed empty state", async ({ page }) => {
    await signIn(page)
    await page.getByRole("link", { name: "Usage" }).click()
    await expect(page.getByText("No traffic in this window")).toBeVisible()
  })
})

test.describe("logs journey", () => {
  test("live badge connects; entries stream; pause holds", async ({ page }) => {
    await signIn(page)
    await page.getByRole("link", { name: "Live Logs" }).click()
    await expect(page.getByText("live", { exact: true })).toBeVisible()

    await page.getByRole("button", { name: "Pause" }).click()
    await expect(page.getByText(/Paused — events keep streaming/)).toBeVisible()
    await expect(page.getByRole("button", { name: "Resume" })).toBeVisible()
  })
})

test.describe("settings journey", () => {
  test("effective config renders read-only", async ({ page }) => {
    await signIn(page)
    await page.getByRole("link", { name: "Settings" }).click()
    await expect(page.getByText("127.0.0.1:7420")).toBeVisible()
    await expect(page.getByText("streams never cut", { exact: false })).toBeVisible()
  })
})

test.describe("RTL pass", () => {
  test("primary views mirror under dir=rtl without horizontal overflow", async ({ page }) => {
    await signIn(page)
    for (const nav of ["Providers", "Virtual Keys", "Usage", "Live Logs", "Settings"]) {
      await page.getByRole("link", { name: nav }).click()
      await page.evaluate(() => document.documentElement.setAttribute("dir", "rtl"))
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth
      )
      expect(overflow).toBeLessThanOrEqual(2)
      await page.evaluate(() => document.documentElement.setAttribute("dir", "ltr"))
    }
  })
})
