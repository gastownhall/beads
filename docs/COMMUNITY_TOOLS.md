# Beads Community Tools

A curated list of community-built UIs, extensions, and integrations for Beads. Ranked by activity and maturity.

## Terminal UIs

- **[beads_viewer](https://github.com/Dicklesworthstone/beads_viewer)** - Elegant, keyboard-driven terminal interface with tree navigation and vim-style commands. Built by [@Dicklesworthstone](https://github.com/Dicklesworthstone). (Go)

- **[bdui](https://github.com/assimelha/bdui)** - Real-time terminal UI with tree view, dependency graph, and vim-style navigation. Built by [@assimelha](https://github.com/assimelha). (Node.js)

- **[perles](https://github.com/zjrosen/perles)** - Terminal UI search, dependency and kanban viewer powered by a custom BQL (Beads Query Language). Built by [@zjrosen](https://github.com/zjrosen). (Go)

- **[beads.el](https://codeberg.org/ctietze/beads.el)** - Emacs UI to browse, edit, and manage beads. Built by [@ctietze](https://codeberg.org/ctietze). (Elisp)

- **[lazybeads](https://github.com/codegangsta/lazybeads)** - Lightweight terminal UI built with Bubble Tea for browsing and managing beads issues. Built by [@codegangsta](https://github.com/codegangsta). (Go)

- **[bsv](https://github.com/bglenden/bsv)** - Simple two-panel terminal (TUI) viewer with tree navigation organized by epic/task/sub-task, markdown rendering, and mouse support. Built by [@bglenden](https://github.com/bglenden). (Rust)

- **[abacus](https://github.com/ChrisEdwards/abacus)** - A powerful terminal UI for visualizing and navigating Beads issue tracking databases.

## Web UIs

- **[beads-ui](https://github.com/mantoni/beads-ui)** - Local web interface with live updates and kanban board. Run with `npx beads-ui start`. Built by [@mantoni](https://github.com/mantoni). (Node.js)

- **[Monitor WebUI](https://github.com/steveyegge/beads/tree/main/examples/monitor-webui)** - Real-time Issue Tracking Dashboard. Standalone web-based monitoring interface with clean, responsive design. Built by Beads core team. (Go)

- **[beads-viz-prototype](https://github.com/mattbeane/beads-viz-prototype)** - Web-based visualization generating interactive HTML from `bd export`. Built by [@mattbeane](https://github.com/mattbeane). (Python)

- **[beads-dashboard](https://github.com/rhydlewis/beads-dashboard)** - A local, lean metrics dashboard for your beads data. Provides insights insights into lead time, throughput and other continuous improvement metrics. Includes a filterable table view of "all issues". Built by [@rhydlewis](https://github.com/rhydlewis). (Node.js/React)


- **[beads-kanban-ui](https://github.com/AvivK5498/Beads-Kanban-UI)** - Visual Kanban board with git branch status tracking, epic/subtask management, design doc viewer, and activity timeline. Install via npm: `npm install -g beads-kanban-ui`. Built by [@AvivK5498](https://github.com/AvivK5498). (TypeScript/Rust)

- **[beads-pm-ui](https://github.com/qosha1/beads-pm-ui)** - Gantt chart timeline view, project / team based filtering (via folder structure), quarterly goal setting and dependency chain visualization. Inline editable. Built by [@qosha1](https://github.com/qosha1). (Nextjs/Typscript)

- **[Beadspace](https://github.com/cameronsjo/beadspace)** - Drop-in GitHub Pages dashboard with triage suggestions, priority/status breakdowns, and searchable issue table. Single HTML file, zero build dependencies, auto-deploys via GitHub Action. Built by [@cameronsjo](https://github.com/cameronsjo). (HTML/CSS/JS)

- **[beadsmap](https://github.com/dariye/beadsmap)** - Interactive roadmap visualization with timeline (Gantt), list, and table views. Multi-source support, dependency arrows, milestone grouping, GitHub integration via OAuth device flow, and light/dark/system themes. Ships as a single `index.html`. Built by [@dariye](https://github.com/dariye). (Svelte/TypeScript)

## Editor Extensions

- **[vscode-beads](https://marketplace.visualstudio.com/items?itemName=planet57.vscode-beads)** - VS Code extension with issues panel and daemon management. Built by [@jdillon](https://github.com/jdillon). (TypeScript)

- **[Agent Native Abstraction Layer for Beads](https://marketplace.visualstudio.com/items?itemName=AgentNativeAbstractionLayer.agent-native-kanban)** (ANAL Beads) - VS Code Kanban board. Maintained by [@sebcook-ctrl](https://github.com/sebcook-ctrl). (Node.js)

- **[Beads-Kanban](https://github.com/davidcforbes/Beads-Kanban)** - VS Code Kanban board for Beads issue tracking. Maintained by [@davidcforbes](https://github.com/davidcforbes). (TypeScript)

- **[opencode-beads](https://github.com/joshuadavidthomas/opencode-beads)** - OpenCode plugin with automatic context injection, `/bd-*` slash commands, and autonomous task agent. Built by [@joshuadavidthomas](https://github.com/joshuadavidthomas). (Node.js)

- **[nvim-beads](https://github.com/joeblubaugh/nvim-beads)** - Neovim plugin for managing beads. Built by [@joeblubaugh](https://github.com/joeblubaugh). (Lua)

- **[beads-manager](https://plugins.jetbrains.com/plugin/30089-beads-manager)** - Jetbrains IDE plugin to manage and view bead details. Maintained by [@developmeh](https://github.com/developmeh). (Kotlin)

## Native Apps

- **[Beads Task-Issue Tracker](https://github.com/w3dev33/beads-task-issue-tracker)** - Cross-platform desktop application (macOS, Windows, Linux) for browsing, creating, and managing Beads issues with a visual interface. Features multi-project support with favorites, image attachments, dashboard with statistics, advanced filtering, and dark/light theme. Built by [@w3dev33](https://github.com/w3dev33). (Tauri/Vue)

- **[Beadster](https://github.com/beadster/beadster)** - macOS app for browsing and managing issues from `.beads/` directories in git repositories. Built by [@podviaznikov](https://github.com/podviaznikov). (Swift)

-  **[Parade](https://github.com/JeremyKalmus/parade)** - Electron app for workflow orchestration with visual Kanban board, discovery wizard, and task visualization. Run with `npx parade-init`. Built by [@JeremyKalmus](https://github.com/JeremyKalmus). (Electron/React)

- **[Beadbox](https://github.com/beadbox/beadbox)** - Native macOS dashboard with real-time sync, epic tree progress bars, multi-workspace support, and inline editing. Install with `brew tap beadbox/cask && brew install --cask beadbox`. Built by [@nmelo](https://github.com/nmelo). (Tauri/Next.js)

## Data Source Middleware

- **[jira-beads-sync](https://github.com/conallob/jira-beads-sync)** - CLI tool & Claude Code plugin to sync tasks from Jira into beads and publish beads task states back to Jira. Built by [@conallob](https://github.com/conallob). (Go)

- **[stringer](https://github.com/davetashner/stringer)** - Codebase archaeology CLI that mines git repos for TODOs, churn hotspots, lottery-risk files, dependency health, and more. Outputs beads JSONL for `bd import`. Install with `brew install davetashner/tap/stringer`. Built by [@davetashner](https://github.com/davetashner). (Go)

## SDKs & Libraries

- **[beads-sdk](https://github.com/HerbCaudill/beads-sdk)** - Typed TypeScript SDK with zero runtime dependencies. High-level `BeadsClient` for CRUD, filtering, search, labels, dependencies, comments, epics, and sync. Includes `DaemonSocket` for real-time mutation watching via the beads daemon. Install with `pnpm add @herbcaudill/beads-sdk`. Built by [@HerbCaudill](https://github.com/HerbCaudill). (TypeScript)

## Claude Code Orchestration

- **[Foolery](https://github.com/acartine/foolery)** - Local web UI that sits on top of Beads, giving you a visual control surface for organizing, orchestrating, and reviewing AI agent work. Features dependency-aware wave planning, a built-in terminal for live agent monitoring, a verification queue for reviewing completed beats, and keyboard-first navigation. Install with `curl -fsSL https://raw.githubusercontent.com/acartine/foolery/main/scripts/install.sh | bash`. Built by [@acartine](https://github.com/acartine). (Next.js/TypeScript)

- **[beads-compound](https://github.com/roberto-mello/beads-compound-plugin)** - Claude Code plugin marketplace with persistent memory and compound-engineering workflows. Hooks auto-capture knowledge from `bd comments add` at session end and inject relevant entries at session start based on open beads. Includes 28 specialized agents, 26 commands, and 15 skills for planning, review, research, and parallel work. Also supports OpenCode and Gemini CLI. Built by [@roberto-mello](https://github.com/roberto-mello). (Bash/TypeScript)

- **[beads-orchestration](https://github.com/AvivK5498/Claude-Code-Beads-Orchestration)** - Multi-agent orchestration skill for Claude Code. Orchestrator investigates issues, manages beads tasks automatically, and delegates to tech-specific supervisors on isolated branches. Includes hooks for workflow enforcement, epic/subtask support, and optional external provider delegation (Codex/Gemini). Install via npm: `npm install -g @avivkaplan/beads-orchestration`. Built by [@AvivK5498](https://github.com/AvivK5498). (Node.js/Python)

## Coordination Servers

- **[BeadHub](https://github.com/beadhub/beadhub)** - Open-source coordination server for AI agent teams running beads. The `bdh` CLI is a transparent wrapper over `bd` that adds work claiming, file reservation, presence awareness, and inter-agent messaging (async mail and sync chat). Includes a web dashboard. Free hosted at beadhub.ai for open-source projects. Built by [@juanre](https://github.com/juanre). (Python/TypeScript)

## Historical / Stale

- **[beady](https://github.com/maphew/beady)** - Early prototype effort, now stale. Built by [@maphew](https://github.com/maphew). (Go)

## Discussion

See [GitHub Discussions #276](https://github.com/steveyegge/beads/discussions/276) for ongoing UI development conversations, design decisions, and community contributions.

## Contributing

Found or built a tool? Open a PR to add it to this list or comment on discussion #276.
