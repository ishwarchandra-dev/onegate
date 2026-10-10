// SHA256SUMS parsing and file hashing (p9.npx-launcher).
//
// The release pipeline (scripts/release/build.sh) publishes SHA256SUMS in
// GNU sha256sum format:
//
//     <64 lowercase hex chars>  <filename>
//
// one space-prefixed `*` before the filename marks binary mode. The parser
// is strict: any line that is neither blank nor a well-formed entry is a
// VerifyError — a malformed manifest must refuse the install (exit 3),
// never guess.

"use strict"

const crypto = require("crypto")
const fs = require("fs")

// VerifyError marks integrity failures; main() maps it to exit code 3
// ("corrupt download refuses to run").
class VerifyError extends Error {
  constructor(message) {
    super(message)
    this.name = "VerifyError"
    this.exitCode = 3
  }
}

// parseSums parses a SHA256SUMS document into a Map<filename, hex>.
// Throws VerifyError on malformed lines or duplicate entries.
function parseSums(text) {
  const map = new Map()
  const lines = String(text).split(/\r?\n/)
  for (const raw of lines) {
    const line = raw.trim()
    if (line === "") continue
    // `<hex>` then two spaces (text mode) or space+`*` (binary mode).
    const m = /^([0-9a-fA-F]{64})[ ]\*?(.+)$/.exec(line)
    if (!m) {
      throw new VerifyError(`SHA256SUMS: malformed line: ${JSON.stringify(raw.slice(0, 80))}`)
    }
    const hex = m[1].toLowerCase()
    // normalize a leading "./" (GNU sha256sum writes the path form it
    // was given; build.sh globs as ./*.tar.gz, so the published manifest
    // carries ./-prefixed names — caught by the real-release npx smoke
    // on v1.0.0, which refused a valid manifest)
    const name = m[2].trim().replace(/^\.\//, "")
    if (name === "" || name.includes("\0")) {
      throw new VerifyError("SHA256SUMS: empty or unsafe file name")
    }
    if (map.has(name)) {
      throw new VerifyError(`SHA256SUMS: duplicate entry for ${name}`)
    }
    map.set(name, hex)
  }
  if (map.size === 0) {
    throw new VerifyError("SHA256SUMS: no entries")
  }
  return map
}

// sha256File streams path through SHA-256; resolves with the lowercase hex
// digest. Rejects with an Error carrying exitCode 3 when the file cannot
// be read (a partial download behaves like corruption: refuse, never
// fall back to running unverified bytes).
function sha256File(path) {
  return new Promise((resolvePromise, reject) => {
    const hash = crypto.createHash("sha256")
    const stream = fs.createReadStream(path)
    stream.on("data", (chunk) => hash.update(chunk))
    stream.on("error", (err) => {
      const e = new VerifyError(`cannot hash ${path}: ${err.message}`)
      reject(e)
    })
    stream.on("end", () => resolvePromise(hash.digest("hex")))
  })
}

// hexDigest is a tiny helper for tests/fixtures (hash a buffer).
function hexDigest(buf) {
  return crypto.createHash("sha256").update(buf).digest("hex")
}

module.exports = { VerifyError, parseSums, sha256File, hexDigest }
