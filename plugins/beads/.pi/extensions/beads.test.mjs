import assert from "node:assert/strict"
import test from "node:test"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

import beadsPiExtension from "./beads.ts"

test("registers the shared skills directory for resource discovery", async () => {
  let eventName
  let handler
  const pi = {
    on(name, callback) {
      eventName = name
      handler = callback
    },
  }

  beadsPiExtension(pi)

  assert.equal(eventName, "resources_discover")
  assert.equal(typeof handler, "function")

  const result = await handler({ cwd: "/tmp/unrelated-project", reason: "startup" }, {})
  const extensionDir = dirname(fileURLToPath(import.meta.url))
  const expectedSkillsDir = resolve(extensionDir, "../..", "skills")
  assert.deepEqual(result, { skillPaths: [expectedSkillsDir] })
})
