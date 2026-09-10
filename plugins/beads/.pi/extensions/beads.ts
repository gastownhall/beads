import type {
  ExtensionAPI,
  ExtensionContext,
  SessionCompactEvent,
  SessionStartEvent,
} from "@earendil-works/pi-coding-agent"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const extensionDir = dirname(fileURLToPath(import.meta.url))
const packageRoot = resolve(extensionDir, "../..")
const skillsDir = resolve(packageRoot, "skills")

type BeadsLifecycleEvent = SessionStartEvent | SessionCompactEvent

async function refreshBeadsContext(
  pi: ExtensionAPI,
  context: ExtensionContext,
  event: BeadsLifecycleEvent,
) {
  const result = await pi.exec("bd", ["prime"], {
    cwd: context.cwd,
    signal: context.signal,
    timeout: 10_000,
  })
  if (result.code !== 0 || result.killed || result.stdout.trim() === "") return

  pi.sendMessage({
    customType: "beads-context",
    content: result.stdout,
    display: false,
    details: { event: event.type, reason: event.reason },
  })
}

export default function beadsPiExtension(pi: ExtensionAPI) {
  pi.on("session_start", async (event, context) => {
    await refreshBeadsContext(pi, context, event)
  })

  pi.on("session_compact", async (event, context) => {
    await refreshBeadsContext(pi, context, event)
  })

  pi.on("resources_discover", async () => ({
    skillPaths: [skillsDir],
  }))
}
