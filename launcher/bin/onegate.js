#!/usr/bin/env node
// `onegate` npm launcher — bin entry (p9.npx-launcher).
//
// Thin wrapper: all logic lives in lib/main.js so it stays unit-testable.
// `npx onegate [args...]` behaves exactly like running the gateway binary
// with those args (the download/install step happens first, then the
// process is replaced in-place via stdio-inheriting spawn).

"use strict"

const { main } = require("../lib/main")

main(process.argv.slice(2), process.env).then(
  (code) => process.exit(code),
  (err) => {
    // main() never rejects; this is a crash guard.
    console.error(`onegate: internal launcher error: ${err && err.stack ? err.stack : err}`)
    process.exit(1)
  },
)
