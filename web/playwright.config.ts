import { defineConfig } from "@playwright/test"
import { join } from "node:path"

/** Repo root (this config lives in web/). */
const ROOT = join(import.meta.dirname, "..")

/**
 * E2E suite for the OneGate dashboard (p6.e2e-dashboard).
 *
 * Three servers boot before the tests:
 *   1. the real gateway binary (built from the repo root) on :7420 with
 *      a fresh temp data dir
 *   2. a mock provider (openai-style /models) on :9441 for the
 *      connection-probe journey
 *   3. the built dashboard via `vite preview` on :4173, proxying /api
 *      to the gateway (mirrors the dev proxy)
 */
export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  // Journeys share one gateway (and its session store); run serially.
  workers: 1,
  fullyParallel: false,
  projects: [
    { name: "setup", testMatch: /setup\.spec\.ts/ },
    {
      name: "journeys",
      testMatch: /journeys\.spec\.ts/,
      dependencies: ["setup"],
      use: { storageState: ".auth/state.json" },
    },
  ],
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"]] : [["list"]],
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: `${ROOT}/bin/onegate -data-dir /tmp/onegate-e2e -port 7420 -log-level warn`,
      url: "http://127.0.0.1:7420/healthz",
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "ignore",
      stderr: "ignore",
    },
    {
      command: "bun run e2e:mock-provider",
      url: "http://127.0.0.1:9441/healthz",
      reuseExistingServer: false,
      timeout: 15_000,
      stdout: "ignore",
    },
    {
      command: "bun run preview",
      url: "http://127.0.0.1:4173/",
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "ignore",
    },
  ],
})
