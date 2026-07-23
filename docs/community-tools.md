---
title: 커뮤니티 도구
description: bd CLI와 함께 사용할 수 있는 커뮤니티 제작 UI, 편집기 확장, 통합을 성숙도순으로 소개합니다.
---

커뮤니티에서 만든 Beads용 UI, 확장, 통합을 선별한 목록입니다. 활동성과 성숙도를 기준으로 정렬했습니다.

> **참고:** Beads는 Dolt SQL 데이터베이스를 저장소로 사용합니다. 도구는 데이터에
> 접근할 때 `bd` CLI(`bd list --json` 등)를 사용해야 합니다. 이전
> `.beads/issues.jsonl` 형식을 직접 읽는 도구는 현재 버전과 호환되지 않습니다.

## 터미널 UI

- **[Mardi Gras](https://github.com/quietpublish/mardi-gras)** - 실시간 업데이트, 다중 에이전트 오케스트레이션, tmux 통합, Claude Code dispatch를 제공하는 퍼레이드 테마 터미널 UI입니다. `bd list --json`을 사용합니다. [@matt-wright86](https://github.com/matt-wright86)가 만들었습니다. (Go)

- **[perles](https://github.com/zjrosen/perles)** - 커스텀 BQL(Beads Query Language)로 구동되는 터미널 UI 검색, 의존성 및 칸반 뷰어입니다. [@zjrosen](https://github.com/zjrosen)이 만들었습니다. (Go)

## 웹 UI

- **[bd-board](https://github.com/jeanpfs/bd-board)** - Beads 프로젝트 탐색, 상태별 또는 epic swimlane별 칸반 보드 보기, 우선순위·텍스트 검색·정렬 순서 필터링을 제공하는 로컬 우선 웹 대시보드입니다. Dolt 호환성을 위해 `bd` CLI를 사용하며, 명시적으로 활성화하지 않으면 쓰기는 비활성화됩니다. [@jeanpfs](https://github.com/jeanpfs)가 만들었습니다. (TanStack Start/React)

- **[beads-ui](https://github.com/mantoni/beads-ui)** - 실시간 업데이트와 칸반 보드를 제공하는 로컬 웹 인터페이스입니다. Dolt 호환성을 위해 `bd` CLI를 사용합니다. `npx beads-ui start`로 실행하세요. [@mantoni](https://github.com/mantoni)가 만들었습니다. (Node.js)

- **[BeadBoard](https://github.com/zenchantlive/beadboard)** - 실시간 대시보드를 갖춘 다중 에이전트 오케스트레이션 및 통신 시스템입니다. 에이전트 간 메시지(HANDOFF/BLOCKED/DECISION/INFO), DAG 의존성 그래프, 아키타입과 템플릿을 활용한 swarm 조정, 범위 기반 작업 예약, [Pi](https://github.com/badlogic/pi-mono)를 기반으로 유형별 worker 에이전트를 생성하는 내장 실행 런타임(bb-pi)을 제공합니다. macOS, Linux, Windows를 지원합니다. 에이전트 통합용 `beadboard-driver` skill(`npx skills add zenchantlive/beadboard --skill beadboard-driver`)이 포함됩니다. [@zenchantlive](https://github.com/zenchantlive)가 만들었습니다. (Next.js/TypeScript)

- **[beads-web](https://github.com/weselow/beads-web)** - 활발히 유지보수되는 beads-kanban-ui fork입니다. 크로스 플랫폼 단일 바이너리 배포(macOS, Linux, Windows), 7가지 시각 테마, Dolt 직접 SQL 통합, Windows 다중 드라이브 경로 지원, 드래그 앤 드롭 상태 업데이트를 제공합니다. [GitHub Releases](https://github.com/weselow/beads-web/releases)에서 다운로드하세요. [@weselow](https://github.com/weselow)가 만들었습니다. (TypeScript/Rust)

- **[Bead Me Up, Scotty](https://github.com/brendan-appstart/bead-me-up-scotty)** - 모든 저장소의 Beads를 한곳에서 생성·업데이트·우선순위 지정할 수 있는 완성도 높은 다중 프로젝트 웹 UI입니다. 드래그 앤 드롭 상태 변경과 재정렬이 가능한 칸반 보드, 목록, 진행률 표시줄이 있는 epic, 의존성 그래프 보기, 다면 필터와 전체 텍스트 검색, `.beads/` 변경 즉시 반응하는 SSE 실시간 업데이트, 사람과 에이전트 구분 표시를 제공합니다. 완전한 Dolt 호환성을 위해 `bd` CLI를 사용합니다. 전역 설치한 `scotty`는 현재 디렉터리의 프로젝트를 브라우저에서 열며, 내장 Publish 보기는 Beads로부터 공유 가능한 정적 쇼케이스 사이트를 생성합니다. [beadmeupscotty.com](https://beadmeupscotty.com)에서 데모를 볼 수 있습니다. [@brendan-appstart](https://github.com/brendan-appstart)가 만들었습니다. (Next.js/TypeScript)

## 편집기 확장

- **[vscode-beads](https://marketplace.visualstudio.com/items?itemName=planet57.vscode-beads)** - 이슈 패널과 서버 관리를 제공하는 VS Code 확장입니다. [@jdillon](https://github.com/jdillon)이 만들었습니다. (TypeScript)

- **[opencode-beads](https://github.com/joshuadavidthomas/opencode-beads)** - 자동 컨텍스트 주입, `/bd-*` slash 명령, 자율 작업 에이전트를 제공하는 OpenCode 플러그인입니다. [@joshuadavidthomas](https://github.com/joshuadavidthomas)가 만들었습니다. (Node.js)

- **[Lista Beads](https://marketplace.visualstudio.com/items?itemName=ListaDev.lista-beads)** - 필터 가능한 트리 보기, 이슈 상세 패널, 지표 대시보드, 의존성 그래프, Dolt push/pull, Bead 참조용 CodeLens, Wisp 및 Formula 지원, 오래된 이슈 관리, 다중 추적기 동기화(Azure DevOps, GitHub, Jira, Linear, GitLab)를 제공하는 완전한 기능의 VS Code 확장입니다. [@harry-miller-trimble](https://github.com/harry-miller-trimble)이 만들었습니다. (TypeScript)

- **[nvim-beads (fancypantalons)](https://github.com/fancypantalons/nvim-beads)** - Beads 이슈를 관리하는 Neovim 플러그인입니다. [@fancypantalons](https://github.com/fancypantalons)가 만들었습니다. (Lua)
- **[beads.nvim](https://github.com/tomfordweb/beads.nvim)** - 준비 큐, 편집 가능한 floating 상세 보기, 생성/빠른 캡처, Telescope picker, 메모리, 의존성 그래프를 제공하는 Beads 이슈 관리용 Neovim UI입니다. Dolt 호환성을 위해 `bd` CLI를 사용합니다. [@tomfordweb](https://github.com/tomfordweb)가 만들었습니다. (Lua)

- **[beads-manager](https://plugins.jetbrains.com/plugin/30089-beads-manager)** - Bead 상세 정보를 관리하고 보는 JetBrains IDE 플러그인입니다. [@developmeh](https://github.com/developmeh)가 유지보수합니다. (Kotlin)

## 네이티브 앱

- **[Beads Task-Issue Tracker](https://github.com/w3dev33/beads-task-issue-tracker)** - 시각적 인터페이스로 Beads 이슈를 탐색·생성·관리하는 크로스 플랫폼 데스크톱 애플리케이션(macOS, Windows, Linux)입니다. 즐겨찾기가 있는 다중 프로젝트 지원, 이미지 첨부, 통계 대시보드, 고급 필터, 다크/라이트 테마를 제공합니다. [@w3dev33](https://github.com/w3dev33)이 만들었습니다. (Tauri/Vue)

- **[Beadbox](https://github.com/beadbox/beadbox)** - 실시간 동기화, epic 트리 진행률 표시줄, 다중 워크스페이스 지원, 인라인 편집을 제공하는 네이티브 macOS 대시보드입니다. `brew tap beadbox/cask && brew install --cask beadbox`로 설치하세요. [@nmelo](https://github.com/nmelo)가 만들었습니다. (Tauri/Next.js)

- **[BeadSpec](https://github.com/boardthatpowder/BeadSpec)** - [OpenSpec](https://github.com/gastownhall/openspec)를 일급으로 통합한 크로스 플랫폼 네이티브 데스크톱 앱(macOS, Windows, Linux)입니다. 진행 중인 변경 제안을 작업 목록과 함께 보여 주고, spec 산출물을 나란한 탭에서 열고, 변경의 작업 목록을 한 번에 Beads로 가져오며, 각 이슈가 어느 OpenSpec 변경에서 들어왔는지 보여 주는 유일한 Beads GUI입니다. 대화형 의존성 그래프(React Flow + Cytoscape), 분할 창이 있는 IDE 스타일 워크스페이스 탭, 전역 빠른 캡처 단축키, 시스템 tray, 명령 팔레트, TipTap Markdown 설명 편집기, 사람 결정 큐, 선택적 [Ruflo](https://github.com/gastownhall/ruflo) 메모리 통합도 제공합니다. 속도를 위해 Dolt SQL을 직접 읽고, hook 논리와 ID 할당을 보존하기 위해 `bd`를 통해 씁니다. [GitHub Releases](https://github.com/boardthatpowder/BeadSpec/releases)에서 다운로드하세요. [@boardthatpowder](https://github.com/boardthatpowder)가 만들었습니다. (Tauri/React/TypeScript)

## 데이터 소스 미들웨어

- **[stringer](https://github.com/davetashner/stringer)** - Git 저장소에서 TODO, 변경 집중 지점, 핵심 인력 의존 위험 파일, 의존성 상태 등을 찾아내는 코드베이스 고고학 CLI입니다. `bd init --from-jsonl`과 호환되는 JSONL을 출력합니다. `brew install davetashner/tap/stringer`로 설치하세요. [@davetashner](https://github.com/davetashner)가 만들었습니다. (Go)

## 분석 및 관측성

* **[Thread](https://github.com/jklenk/thread)** - Beads용 읽기 전용 포렌식 및 분석 계층입니다. 로컬 Dolt 기록을 읽어 충실도 점수, 재작업 비용 지표, 세션 규정 준수 점수, 독립 실행형 HTML 보고서를 생성합니다. 에이전트가 첫 Bead를 claim하기 전에 프로젝트 상태 컨텍스트를 받게 하려면 `AGENTS.md`에 `Run 'thread prime --json' at session start`를 추가하세요.

`uv tool install git+https://github.com/jklenk/thread`로 설치하세요. [@jklenk](https://github.com/jklenk)가 만들었습니다. (Python/DuckDB)

## SDK 및 라이브러리

- **[beads-sdk](https://github.com/HerbCaudill/beads-sdk)** - 런타임 의존성이 없는 typed TypeScript SDK입니다. CRUD, 필터링, 검색, 레이블, 의존성, 댓글, epic, 동기화를 위한 고수준 `BeadsClient`를 제공합니다. `pnpm add @herbcaudill/beads-sdk`로 설치하세요. [@HerbCaudill](https://github.com/HerbCaudill)이 만들었습니다. (TypeScript)

## Claude Code 오케스트레이션

- **[Foolery](https://github.com/acartine/foolery)** - Beads 위에서 AI 에이전트 작업을 구성·오케스트레이션·검토할 수 있는 시각적 제어 화면을 제공하는 로컬 웹 UI입니다. 의존성을 인식하는 wave 계획, 실시간 에이전트 모니터링용 내장 터미널, 완료된 beat 검토용 검증 큐, 키보드 우선 탐색을 제공합니다. `curl -fsSL https://raw.githubusercontent.com/acartine/foolery/main/scripts/install.sh | bash`로 설치하세요. [@acartine](https://github.com/acartine)가 만들었습니다. (Next.js/TypeScript)

- **[beads-compound](https://github.com/roberto-mello/beads-compound-plugin)** - 영구 메모리와 compound-engineering 워크플로를 제공하는 Claude Code 플러그인 marketplace입니다. 세션 종료 시 hook이 `bd comments add`에서 지식을 자동 캡처하고, 세션 시작 시 열린 Beads를 기준으로 관련 항목을 주입합니다. 계획, 검토, 조사, 병렬 작업을 위한 전문 에이전트 28개, 명령 26개, skill 15개를 포함합니다. OpenCode와 Gemini CLI도 지원합니다. [@roberto-mello](https://github.com/roberto-mello)가 만들었습니다. (Bash/TypeScript)

- **[claude-handoff](https://github.com/REMvisual/claude-handoff)** - Claude Code용 세션 handoff skill입니다. 결정, 실패한 접근법, 측정값, 다음 단계를 구조화된 파일로 캡처하여 다음 세션이 중단 지점부터 이어 가게 합니다. 다중 세션 연속성을 위해 Bead ID를 chain tag로 사용하고, 활성 Beads를 자동 감지하며, 종료 시 Bead notes를 업데이트합니다. `/handoff`, `/handoffplan`, PreCompact 안전망 hook을 포함합니다. [@REMvisual](https://github.com/REMvisual)이 만들었습니다. (Markdown/Bash)
- **[claude-workspace-snapshot](https://github.com/REMvisual/claude-workspace-snapshot)** - 실행 중인 Claude Code 세션을 이름과 색상이 지정된 Windows Terminal 탭으로 snapshot하고 복원합니다. 프로세스 검사와 `.jsonl` 파일 활동으로 실행 세션을 감지하고, 다시 시작한 뒤 탭 배치를 복원합니다. claude-handoff와 함께 사용하면 세션 연속성을 완전히 유지할 수 있습니다. [@REMvisual](https://github.com/REMvisual)이 만들었습니다. (PowerShell/Batch)
- **[claude-protocol](https://github.com/weselow/claude-protocol)** - 활발히 유지보수되는 beads-orchestration fork입니다. Claude 4.6 계열 모델에 최적화하도록 처음부터 다시 작성했습니다. trigger 기반 개발 규칙(TDD, logging, resilience), 크로스 플랫폼 Node.js hook(19개 Bash script를 8개 `.cjs` hook으로 대체), 필수 체크리스트 검증, 세션 시작 대시보드, 자동 캡처 지식 기반을 제공합니다. `npx claude-protocol init`으로 설치하세요. [@weselow](https://github.com/weselow)가 만들었습니다. (Node.js/Python)

- **[LoopTroop](https://github.com/looptroop-ai/LoopTroop)** - 자동 작업 계획·실행·피드백 loop를 위한 로컬 AI 코딩 orchestrator입니다. LLM Council 합의와 워크트리 격리를 결합한 Beads 기반 방법론을 사용합니다. [@looptroop-ai](https://github.com/looptroop-ai)가 만들었습니다. (Node.js/TypeScript)

## 조정 서버

- **[BeadHub](https://github.com/beadhub/beadhub)** - Beads를 사용하는 AI 에이전트 팀용 오픈 소스 조정 서버입니다. `bdh` CLI는 `bd`를 투명하게 감싸면서 작업 claim, 파일 예약, 접속 상태 인식, 에이전트 간 메시지(비동기 mail 및 동기 chat)를 추가합니다. 웹 대시보드를 포함하며 오픈 소스 프로젝트에는 beadhub.ai에서 무료로 호스팅됩니다. [@juanre](https://github.com/juanre)가 만들었습니다. (Python/TypeScript)

## 과거 프로젝트 및 중단된 프로젝트

- **[bdui](https://github.com/assimelha/bdui)** - 트리 보기, 의존성 그래프, Vim 스타일 탐색을 제공하는 실시간 터미널 UI입니다. [@assimelha](https://github.com/assimelha)가 만들었습니다. (Node.js)

- **[beads.el](https://codeberg.org/ctietze/beads.el)** - Beads를 탐색·편집·관리하는 Emacs UI입니다. [@ctietze](https://codeberg.org/ctietze)가 만들었습니다. (Elisp)

- **[lazybeads](https://github.com/codegangsta/lazybeads)** - Beads 이슈를 탐색하고 관리할 수 있도록 Bubble Tea로 만든 가벼운 터미널 UI입니다. [@codegangsta](https://github.com/codegangsta)가 만들었습니다. (Go)

- **[bsv](https://github.com/bglenden/bsv)** - epic/task/sub-task로 구성된 트리 탐색, Markdown 렌더링, 마우스 지원을 제공하는 간단한 2패널 터미널(TUI) 뷰어입니다. [@bglenden](https://github.com/bglenden)이 만들었습니다. (Rust)

- **[abacus](https://github.com/ChrisEdwards/abacus)** - Beads 이슈 추적 데이터베이스를 시각화하고 탐색하는 강력한 터미널 UI입니다.

- **[beads-viz-prototype](https://github.com/mattbeane/beads-viz-prototype)** - `bd export` 결과에서 대화형 HTML을 생성하는 웹 기반 시각화 도구입니다. [@mattbeane](https://github.com/mattbeane)가 만들었습니다. (Python)

- **[beads-dashboard](https://github.com/rhydlewis/beads-dashboard)** - Beads 데이터를 위한 가벼운 로컬 지표 대시보드입니다. lead time, throughput 및 기타 지속적 개선 지표에 대한 통찰을 제공합니다. "모든 이슈"를 볼 수 있는 필터 가능한 표가 포함됩니다. [@rhydlewis](https://github.com/rhydlewis)가 만들었습니다. (Node.js/React)

- **[beads-kanban-ui](https://github.com/AvivK5498/Beads-Kanban-UI)** - Git 브랜치 상태 추적, epic/subtask 관리, 설계 문서 뷰어, 활동 timeline을 제공하는 시각적 칸반 보드입니다. `npm install -g beads-kanban-ui`로 설치하세요. [@AvivK5498](https://github.com/AvivK5498)가 만들었습니다. (TypeScript/Rust)

- **[beads-pm-ui](https://github.com/qosha1/beads-pm-ui)** - Gantt chart timeline 보기, 폴더 구조를 통한 프로젝트/팀 기반 필터링, 분기별 목표 설정, 의존성 chain 시각화를 제공합니다. 인라인 편집이 가능합니다. [@qosha1](https://github.com/qosha1)가 만들었습니다. (Nextjs/Typscript)

- **[Beadspace](https://github.com/cameronsjo/beadspace)** - triage 제안, 우선순위/상태 분석, 검색 가능한 이슈 표를 제공하는 바로 적용 가능한 GitHub Pages 대시보드입니다. 단일 HTML 파일이며 build 의존성이 없고 GitHub Action으로 자동 배포됩니다. [@cameronsjo](https://github.com/cameronsjo)가 만들었습니다. (HTML/CSS/JS)

- **[beadsmap](https://github.com/dariye/beadsmap)** - timeline(Gantt), 목록, 표 보기를 제공하는 대화형 roadmap 시각화 도구입니다. 다중 소스, 의존성 화살표, milestone 그룹화, OAuth device flow를 통한 GitHub 통합, light/dark/system 테마를 지원합니다. 단일 `index.html`로 배포됩니다. [@dariye](https://github.com/dariye)가 만들었습니다. (Svelte/TypeScript)

- **[Agent Native Abstraction Layer for Beads](https://marketplace.visualstudio.com/items?itemName=AgentNativeAbstractionLayer.agent-native-kanban)** (ANAL Beads) - VS Code 칸반 보드입니다. [@sebcook-ctrl](https://github.com/sebcook-ctrl)이 유지보수합니다. (Node.js)

- **[Beads-Kanban](https://github.com/davidcforbes/Beads-Kanban)** - Beads 이슈 추적용 VS Code 칸반 보드입니다. [@davidcforbes](https://github.com/davidcforbes)가 유지보수합니다. (TypeScript)

- **[nvim-beads](https://github.com/joeblubaugh/nvim-beads)** - Beads 관리용 Neovim 플러그인입니다. [@joeblubaugh](https://github.com/joeblubaugh)가 만들었습니다. (Lua)

- **[Beadster](https://github.com/beadster/beadster)** - Git 저장소의 `.beads/` 디렉터리에 있는 이슈를 탐색하고 관리하는 macOS 앱입니다. [@podviaznikov](https://github.com/podviaznikov)가 만들었습니다. (Swift)

-  **[Parade](https://github.com/JeremyKalmus/parade)** - 시각적 칸반 보드, discovery wizard, 작업 시각화를 제공하는 워크플로 오케스트레이션용 Electron 앱입니다. `npx parade-init`으로 실행하세요. [@JeremyKalmus](https://github.com/JeremyKalmus)가 만들었습니다. (Electron/React)

- **[jira-beads-sync](https://github.com/conallob/jira-beads-sync)** - Jira 작업을 Beads로 동기화하고 Beads 작업 상태를 다시 Jira에 게시하는 CLI 도구 및 Claude Code 플러그인입니다. [@conallob](https://github.com/conallob)가 만들었습니다. (Go)

- **[beads-orchestration](https://github.com/AvivK5498/Claude-Code-Beads-Orchestration)** - Claude Code용 다중 에이전트 오케스트레이션 skill입니다. Orchestrator가 이슈를 조사하고 Beads 작업을 자동 관리하며 격리된 브랜치의 기술별 supervisor에게 위임합니다. 워크플로 강제, epic/subtask 지원, 선택적 외부 provider 위임(Codex/Gemini)을 위한 hook을 포함합니다. `npm install -g @avivkaplan/beads-orchestration`으로 설치하세요. [@AvivK5498](https://github.com/AvivK5498)가 만들었습니다. (Node.js/Python)

- **[beads_viewer](https://github.com/Dicklesworthstone/beads_viewer)** - 트리 탐색과 Vim 스타일 명령을 제공하는 터미널 인터페이스입니다. Dolt 기반 Beads(v0.50 이상)와 호환되지 않습니다. [이슈 #121](https://github.com/Dicklesworthstone/beads_viewer/issues/121)을 참고하세요. [@Dicklesworthstone](https://github.com/Dicklesworthstone)가 만들었습니다. (Go)

- **[beady](https://github.com/maphew/beady)** - 현재 중단된 초기 프로토타입입니다. [@maphew](https://github.com/maphew)가 만들었습니다. (Go)

## 토론

진행 중인 UI 개발 대화, 설계 결정, 커뮤니티 기여는 [GitHub Discussions #276](https://github.com/gastownhall/beads/discussions/276)을 참고하세요.

## 기여

도구를 발견했거나 만들었나요? 이 목록에 추가하는 PR을 열거나 discussion #276에 댓글을 남겨 주세요.
