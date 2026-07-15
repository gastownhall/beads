import assert from "node:assert/strict"
import { mkdtemp, rm } from "node:fs/promises"
import test from "node:test"
import { tmpdir } from "node:os"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

test("registers the shared skills directory for resource discovery", async () => {
  const originalCwd = process.cwd()
  const unrelatedCwd = await mkdtemp(resolve(tmpdir(), "beads-pi-cwd-"))
  let eventName
  let handler

  try {
    process.chdir(unrelatedCwd)
    const extensionUrl = new URL(`./beads.ts?cwd=${Date.now()}`, import.meta.url)
    const { default: beadsPiExtension } = await import(extensionUrl)
    const pi = {
      on(name, callback) {
        eventName = name
        handler = callback
      },
    }

    beadsPiExtension(pi)

    assert.equal(eventName, "resources_discover")
    assert.equal(typeof handler, "function")

    const result = await handler({ cwd: unrelatedCwd, reason: "startup" }, {})
    const extensionDir = dirname(fileURLToPath(import.meta.url))
    const expectedSkillsDir = resolve(extensionDir, "../..", "skills")
    assert.deepEqual(result, { skillPaths: [expectedSkillsDir] })
  } finally {
    process.chdir(originalCwd)
    await rm(unrelatedCwd, { recursive: true, force: true })
  }
})
