import assert from "node:assert/strict"
import { mkdtemp, rm } from "node:fs/promises"
import test from "node:test"
import { tmpdir } from "node:os"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

function createPi(execResult = { stdout: "# Beads Workflow Context\n", stderr: "", code: 0, killed: false }) {
  const executions = []
  const handlers = new Map()
  const messages = []

  return {
    executions,
    handlers,
    messages,
    pi: {
      async exec(command, args, options) {
        executions.push({ command, args, options })
        return execResult
      },
      on(name, callback) {
        handlers.set(name, callback)
      },
      sendMessage(message) {
        messages.push(message)
      },
    },
  }
}

test("discovers the Beads skill and refreshes lifecycle context", async (t) => {
  const originalCwd = process.cwd()
  const unrelatedCwd = await mkdtemp(resolve(tmpdir(), "beads-pi-cwd-"))

  try {
    process.chdir(unrelatedCwd)
    const extensionUrl = new URL(`./beads.ts?cwd=${Date.now()}`, import.meta.url)
    const { default: beadsPiExtension } = await import(extensionUrl)

    await t.test("registers the shared skills directory for resource discovery", async () => {
      const { handlers, pi } = createPi()
      beadsPiExtension(pi)

      const handler = handlers.get("resources_discover")
      assert.equal(typeof handler, "function")

      const result = await handler({ cwd: unrelatedCwd, reason: "startup" }, {})
      const extensionDir = dirname(fileURLToPath(import.meta.url))
      const expectedSkillsDir = resolve(extensionDir, "../..", "skills")
      assert.deepEqual(result, { skillPaths: [expectedSkillsDir] })
    })

    await t.test("injects fresh bd prime context when a session starts", async () => {
      const { executions, handlers, messages, pi } = createPi()
      beadsPiExtension(pi)

      const handler = handlers.get("session_start")
      assert.equal(typeof handler, "function")

      await handler({ type: "session_start", reason: "startup" }, { cwd: unrelatedCwd, signal: undefined })
      assert.deepEqual(executions, [
        {
          command: "bd",
          args: ["prime"],
          options: { cwd: unrelatedCwd, signal: undefined, timeout: 10_000 },
        },
      ])
      assert.deepEqual(messages, [
        {
          customType: "beads-context",
          content: "# Beads Workflow Context\n",
          display: false,
          details: { event: "session_start", reason: "startup" },
        },
      ])
    })

    await t.test("refreshes bd prime context after every compaction mode", async () => {
      for (const reason of ["manual", "threshold", "overflow"]) {
        const { handlers, messages, pi } = createPi()
        beadsPiExtension(pi)

        const handler = handlers.get("session_compact")
        assert.equal(typeof handler, "function")

        await handler({ type: "session_compact", reason }, { cwd: unrelatedCwd, signal: undefined })
        assert.deepEqual(messages, [
          {
            customType: "beads-context",
            content: "# Beads Workflow Context\n",
            display: false,
            details: { event: "session_compact", reason },
          },
        ])
      }
    })

    await t.test("does not inject unusable bd prime output", async () => {
      const unusableResults = [
        {
          stdout: "unexpected output",
          stderr: "no Beads workspace found",
          code: 1,
          killed: false,
        },
        {
          stdout: "partial output",
          stderr: "",
          code: 0,
          killed: true,
        },
        {
          stdout: " \n",
          stderr: "",
          code: 0,
          killed: false,
        },
      ]

      for (const result of unusableResults) {
        const { handlers, messages, pi } = createPi(result)
        beadsPiExtension(pi)

        await handlers.get("session_start")(
          { type: "session_start", reason: "startup" },
          { cwd: unrelatedCwd },
        )
        assert.deepEqual(messages, [])
      }
    })
  } finally {
    process.chdir(originalCwd)
    await rm(unrelatedCwd, { recursive: true, force: true })
  }
})
