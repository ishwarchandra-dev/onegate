// HTTPS downloads for the launcher (p9.npx-launcher).
//
// Zero-dependency: node:http/node:https with manual redirect handling.
// Scheme policy: the DEFAULT download root is https and stays https across
// every redirect hop (GitHub release downloads redirect within
// github.com / objects.githubusercontent.com — both TLS). An explicitly
// configured root (ONEGATE_DOWNLOAD_ROOT, used for mirrors, tests, and
// offline artifact servers) may opt into plain http; in that case http
// hops are permitted. Integrity does not lean on TLS either way: the
// SHA256 check in lib/install.js is the gate; TLS is transport hygiene.

"use strict"

const http = require("http")
const https = require("https")
const fs = require("fs")

// DownloadError marks network failures; main() maps it to exit code 2.
class DownloadError extends Error {
  constructor(message, status) {
    super(message)
    this.name = "DownloadError"
    this.exitCode = 2
    if (status !== undefined) this.status = status
  }
}

const MAX_REDIRECTS = 5
const DEFAULT_STALL_MS = 30_000

// downloadToFile fetches url into destPath (overwriting), enforcing:
//   - up to MAX_REDIRECTS 3xx hops (Location header; 301/302/303/307/308)
//   - scheme policy: https root → every hop must be https
//   - final status must be 200 (no ranges, no partial content)
//   - a no-progress watchdog (stallMS) that aborts the socket
// Resolves with { bytes, url } once the file is fully written and flushed.
function downloadToFile(url, destPath, options = {}) {
  const allowHttp = options.allowHttp === true
  const stallMS = options.stallMS || DEFAULT_STALL_MS

  return new Promise((resolvePromise, reject) => {
    let hops = 0

    const attempt = (currentUrl) => {
      const parsed = new URL(currentUrl)
      if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && allowHttp)) {
        reject(
          new DownloadError(
            `refusing ${parsed.protocol} hop from an https download root` +
              (allowHttp ? "" : " (only https is allowed for the default root)"),
          ),
        )
        return
      }
      const mod = parsed.protocol === "https:" ? https : http
      const req = mod.get(currentUrl, { timeout: stallMS }, (res) => {
        const status = res.statusCode || 0
        if (status >= 300 && status < 400) {
          const loc = res.headers.location
          res.resume() // drain the (usually tiny) body
          if (typeof loc !== "string" || loc === "") {
            reject(new DownloadError(`redirect without Location at ${currentUrl}`))
            return
          }
          hops += 1
          if (hops > MAX_REDIRECTS) {
            reject(new DownloadError(`too many redirects (>${MAX_REDIRECTS})`))
            return
          }
          attempt(new URL(loc, currentUrl).toString())
          return
        }
        if (status !== 200) {
          res.resume()
          reject(new DownloadError(`HTTP ${status} for ${currentUrl}`, status))
          return
        }

        const file = fs.createWriteStream(destPath, { mode: 0o644 })
        let bytes = 0

        // Watchdog: abort when no bytes arrive for stallMS. refresh()
        // slides the window forward on every chunk.
        const watchdog = setTimeout(() => {
          req.destroy(new Error(`stalled: no data for ${stallMS}ms`))
        }, stallMS)
        res.on("data", (chunk) => {
          watchdog.refresh()
          bytes += chunk.length
          file.write(chunk)
        })
        res.on("end", () => {
          clearTimeout(watchdog)
          file.end(() => {
            resolvePromise({ bytes, url: currentUrl })
          })
        })
        res.on("error", (err) => {
          clearTimeout(watchdog)
          file.destroy()
          reject(new DownloadError(`read error: ${err.message}`))
        })
        file.on("error", (err) => {
          clearTimeout(watchdog)
          reject(new DownloadError(`cannot write ${destPath}: ${err.message}`))
        })
      })
      req.on("timeout", () => {
        req.destroy(new Error(`connect timeout after ${stallMS}ms`))
      })
      req.on("error", (err) => {
        reject(new DownloadError(`${err.message} (${currentUrl})`))
      })
    }

    attempt(url)
  })
}

module.exports = { DownloadError, downloadToFile }
