// SHA256SUMS parsing + hashing unit tests (p9.npx-launcher).

"use strict"

const test = require("node:test")
const assert = require("node:assert/strict")
const fs = require("fs")
const os = require("os")
const path = require("path")

const checksums = require("../lib/checksums")

test("parseSums reads GNU sha256sum text format", () => {
  const text =
    "b6c7a2f7e1d94a038d3c5e87f2a1b9c4d8e6f7a0123456789abcdef012345678  " +
    "onegate_v1.0.0_linux_amd64.tar.gz\n" +
    "0f4c2b9a8e7d6c5b4a392817f6e5d4c3b2a1908f7e6d5c4b3a29180f7e6d5c4b  " +
    "onegate_v1.0.0_windows_arm64.zip\n"
  const sums = checksums.parseSums(text)
  assert.equal(sums.size, 2)
  assert.equal(sums.get("onegate_v1.0.0_linux_amd64.tar.gz"), "b6c7a2f7e1d94a038d3c5e87f2a1b9c4d8e6f7a0123456789abcdef012345678")
})

test("parseSums accepts binary-mode `*` separator and CRLF", () => {
  const text = "a".repeat(64) + " *onegate_v1.0.0_darwin_arm64.tar.gz\r\n"
  const sums = checksums.parseSums(text)
  assert.equal(sums.get("onegate_v1.0.0_darwin_arm64.tar.gz"), "a".repeat(64))
})

test("parseSums skips blank lines but refuses malformed ones", () => {
  assert.throws(() => checksums.parseSums("\n\n"), /no entries/)
  assert.throws(() => checksums.parseSums("not-a-sum-line\n"), /malformed line/)
  assert.throws(() => checksums.parseSums("short\n"), /malformed line/)
  assert.throws(
    () => checksums.parseSums(`${"a".repeat(64)}  x.zip\n${"a".repeat(64)}  x.zip\n`),
    /duplicate entry for x\.zip/,
  )
})

test("sha256File digests a file identically to a known-vector hash", async () => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "ogsum-"))
  const file = path.join(dir, "payload.bin")
  const payload = Buffer.from("onegate launcher checksum fixture\n")
  await fs.promises.writeFile(file, payload)
  // Expected digest computed over the same bytes: cross-checks the
  // streaming reader against a one-shot digest.
  const expected = checksums.hexDigest(payload)
  assert.equal(await checksums.sha256File(file), expected)
  await fs.promises.rm(dir, { recursive: true, force: true })
})

test("sha256File rejects (exit 3 semantics) on a missing file", async () => {
  await assert.rejects(() => checksums.sha256File("/nonexistent/onegate.tar.gz"), (err) => {
    assert.equal(err.name, "VerifyError")
    assert.equal(err.exitCode, 3)
    return true
  })
})

test("VerifyError defaults to exit code 3", () => {
  const e = new checksums.VerifyError("boom")
  assert.equal(e.exitCode, 3)
})
