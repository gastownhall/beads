import assert from "node:assert/strict"
import { mkdtemp, rm } from "node:fs/promises"
import test from "node:test"
import { tmpdir } from "node:os"
import { resolve } from "node:path"

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
        if (execResult instanceof Error) throw execResult
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

test("refreshes Beads lifecycle context from an unrelated directory", async (t) => {
  const originalCwd = process.cwd()
  const unrelatedCwd = await mkdtemp(resolve(tmpdir(), "beads-pi-cwd-"))

  try {
    process.chdir(unrelatedCwd)
    const extensionUrl = new URL(`./beads.ts?cwd=${Date.now()}`, import.meta.url)
    const { default: beadsPiExtension } = await import(extensionUrl)

    await t.test("leaves skill discovery to the package manifest", () => {
      const { handlers, pi } = createPi()
      beadsPiExtension(pi)

      assert.equal(handlers.has("resources_discover"), false)
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

    await t.test("stays quiet when bd cannot be spawned", async () => {
      for (const type of ["session_start", "session_compact"]) {
        const error = Object.assign(new Error("spawn bd ENOENT"), { code: "ENOENT" })
        const { executions, handlers, messages, pi } = createPi(error)
        beadsPiExtension(pi)

        await handlers.get(type)({ type, reason: "manual" }, { cwd: unrelatedCwd })
        assert.equal(executions.length, 1)
        assert.deepEqual(messages, [])
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
