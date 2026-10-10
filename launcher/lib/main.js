// Launcher main: resolve → (override | ensureInstalled) → spawn.
// p9.npx-launcher.
//
// Contract (documented in launcher/README.md and the CLI reference):
//
//   - `onegate [gateway args...]` runs the gateway binary with those
//     args; the child's exit code is forwarded verbatim, signals become
//     128+N.
//   - ONEGATE_BINARY: run that binary instead of downloading (system
//     install / offline / distro packagers). No download, no cache.
//   - ONEGATE_VERSION: pin a version other than this package's own
//     (default: package.json version, kept in lockstep with releases).
//   - ONEGATE_DOWNLOAD_ROOT: mirror or artifact server instead of the
//     official GitHub Releases URL (used by tests; http:// roots are
//     permitted only when explicitly configured).
//   - ONEGATE_LAUNCHER_CACHE: cache dir override (default ~/.onegate/launcher).
//
// Launcher exit codes (self-failures; child codes pass through):
//
//   1  unsupported platform / bad env / spawn failure
//   2  network error (unreachable, HTTP != 200, stall)
//   3  verification failure (corrupt download refuses to run)
//   4  extraction / install failure

"use strict"

const { spawn } = require("child_process")
const fs = require("fs")

const platform = require("./platform")
const install = require("./install")

const DEFAULT_ROOT = "https://github.com/ishwarchandra-dev/onegate/releases/download"
const pkg = require("../package.json")

// UsageError: bad env/flags before any network work; exit 1.
class UsageError extends Error {
  constructor(message) {
    super(message)
    this.name = "UsageError"
    this.exitCode = 1
  }
}

// main(argv, env) resolves and runs; resolves with the process exit code
// to use (never rejects — all failures are mapped to codes).
async function main(argv, env, io = process) {
  const stderr = (msg) => io.stderr.write(`onegate: ${msg}\n`)

  // --- explicit binary override (bundle/system mode) ------------------
  const override = env.ONEGATE_BINARY
  if (override) {
    try {
      await fs.promises.access(override)
    } catch {
      stderr(`ONEGATE_BINARY is set but ${override} does not exist`)
      return 1
    }
    return exec(override, argv, io)
  }

  // --- version + root --------------------------------------------------
  const rawVersion = env.ONEGATE_VERSION || pkg.version
  const version = platform.normalizeVersion(rawVersion)
  if (!version) {
    stderr(
      `ONEGATE_VERSION=${JSON.stringify(rawVersion)} is not a recognizable version ` +
        `(expected e.g. 1.0.0 or v1.0.0)`,
    )
    return 1
  }
  const root = env.ONEGATE_DOWNLOAD_ROOT || DEFAULT_ROOT
  if (!/^https?:\/\//.test(root)) {
    stderr("ONEGATE_DOWNLOAD_ROOT must be an http(s) URL")
    return 1
  }
  const cacheDir = env.ONEGATE_LAUNCHER_CACHE || install.defaultCacheDir()

  // --- platform --------------------------------------------------------
  let target
  try {
    target = platform.resolve()
  } catch (err) {
    stderr(err.message)
    return err.exitCode || 1
  }

  // --- install (download → verify → extract → cache) --------------------
  let placed
  try {
    placed = await install.ensureInstalled({
      root,
      version,
      cacheDir,
      target,
      log: (line) => io.stderr.write(`${line}\n`),
    })
  } catch (err) {
    stderr(err.message)
    return err.exitCode || 4
  }

  // --- run --------------------------------------------------------------
  return exec(placed.binPath, argv, io)
}

// exec spawns bin with argv. Production mode inherits stdio (the launcher
// is transparent); test mode (io.captureStdout array present) pipes the
// child's output into arrays so assertions can read it.
//
// SIGINT/SIGTERM delivered to the launcher are forwarded to the child
// (and, on child death, the listeners are removed) so supervisors that
// kill only the launcher never orphan the gateway.
function exec(bin, argv, io) {
  return new Promise((resolvePromise) => {
    const capture = Array.isArray(io.captureStdout)
    const child = spawn(bin, argv, {
      stdio: capture ? ["ignore", "pipe", "pipe"] : "inherit",
      windowsHide: true,
    })
    if (capture) {
      child.stdout.setEncoding("utf8")
      child.stdout.on("data", (d) => io.captureStdout.push(d))
      if (Array.isArray(io.captureStderr)) {
        child.stderr.setEncoding("utf8")
        child.stderr.on("data", (d) => io.captureStderr.push(d))
      }
    }
    const forward = (sig) => {
      try {
        child.kill(sig)
      } catch {
        /* already gone */
      }
    }
    const onTerm = () => forward("SIGTERM")
    const onInt = () => forward("SIGINT")
    process.on("SIGTERM", onTerm)
    process.on("SIGINT", onInt)
    const done = (code, signal) => {
      process.removeListener("SIGTERM", onTerm)
      process.removeListener("SIGINT", onInt)
      if (code !== null) return resolvePromise(code)
      if (signal) return resolvePromise(128 + signalNumber(signal))
      resolvePromise(1)
    }
    child.on("error", (err) => {
      io.stderr.write(`onegate: cannot run ${bin}: ${err.message}\n`)
      done(1, null)
    })
    child.on("close", done)
  })
}

// signalNumber maps the few signals Node names (SIGINT…) to numbers for
// the 128+N convention; unknown names get a stable 255.
function signalNumber(name) {
  const map = {
    SIGHUP: 1,
    SIGINT: 2,
    SIGQUIT: 3,
    SIGKILL: 9,
    SIGTERM: 15,
  }
  return map[name] || 255
}

module.exports = { main, DEFAULT_ROOT, UsageError }
