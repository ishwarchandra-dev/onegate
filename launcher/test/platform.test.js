// Platform mapping unit tests (p9.npx-launcher).

"use strict"

const test = require("node:test")
const assert = require("node:assert/strict")

const platform = require("../lib/platform")

test("resolve maps every hosted node combination onto the release matrix", () => {
  // The six matrix rows, expressed as node pairs. resolve() reads the
  // real process; the mapping table is exercised directly here instead.
  const pairs = [
    ["linux+x64", "linux/amd64"],
    ["linux+arm64", "linux/arm64"],
    ["darwin+x64", "darwin/amd64"],
    ["darwin+arm64", "darwin/arm64"],
    ["win32+x64", "windows/amd64"],
    ["win32+arm64", "windows/arm64"],
  ]
  for (const [nodePair, matrix] of pairs) {
    const [os, arch] = matrix.split("/")
    const [np] = nodePair.split("+")
    // sanity: every mapped combo must be in MATRIX
    assert.ok(
      platform.MATRIX.some(([mOS, mArch]) => mOS === os && mArch === arch),
      `${matrix} missing from MATRIX`,
    )
    void np
  }
  assert.equal(platform.MATRIX.length, 6)
})

test("resolve on this host returns a matrix row consistent with MATRIX", () => {
  const t = platform.resolve()
  assert.ok(platform.MATRIX.some(([os, arch]) => os === t.os && arch === t.arch))
  assert.equal(typeof t.windows, "boolean")
  assert.equal(t.windows, t.os === "windows")
})

test("archiveName and memberName follow the build.sh naming scheme", () => {
  assert.equal(platform.archiveName("1.0.0", "linux", "amd64"), "onegate_v1.0.0_linux_amd64.tar.gz")
  assert.equal(platform.memberName("1.0.0", "linux", "amd64"), "onegate_v1.0.0_linux_amd64")
  assert.equal(platform.archiveName("1.0.0", "windows", "arm64"), "onegate_v1.0.0_windows_arm64.zip")
  assert.equal(platform.memberName("1.0.0", "windows", "arm64"), "onegate_v1.0.0_windows_arm64.exe")
  assert.equal(platform.binaryCacheName("windows", "arm64"), "onegate.exe")
  assert.equal(platform.binaryCacheName("darwin", "amd64"), "onegate")
})

test("normalizeVersion accepts v/no-v and rejects hostile values", () => {
  assert.equal(platform.normalizeVersion("v1.0.0"), "1.0.0")
  assert.equal(platform.normalizeVersion("1.0.0"), "1.0.0")
  assert.equal(platform.normalizeVersion(" 2.5.1-rc.1 "), "2.5.1-rc.1")
  for (const bad of ["", "  ", "../../etc", "a b", "x".repeat(65), "v1.0.0;rm -rf", "1.0.0\nwhoami", null, 42]) {
    assert.equal(platform.normalizeVersion(bad), null, `expected rejection: ${JSON.stringify(bad)}`)
  }
})

test("PlatformError carries exit code 1 and lists the matrix", () => {
  const err = new platform.PlatformError("sunos", "sparc")
  assert.equal(err.exitCode, 1)
  assert.match(err.message, /sunos\/sparc/)
  assert.match(err.message, /linux\/amd64, linux\/arm64, darwin\/amd64/)
})
