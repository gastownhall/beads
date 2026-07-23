---
title: 설치
description: Homebrew, npm 또는 go install로 macOS, Linux, Windows, FreeBSD에 bd CLI, Claude Code 플러그인, MCP 서버 설치
---

모든 플랫폼을 위한 전체 설치 가이드입니다.

## 구성 요소 개요

Beads에는 여러 구성 요소가 있습니다. 각 구성 요소와 필요한 경우는 다음과 같습니다.

| 구성 요소 | 설명 | 필요한 경우 |
|-----------|------------|------------------|
| **bd CLI** | 핵심 명령줄 도구 | 항상 필요. Beads의 기반 |
| **Claude Code 플러그인** | slash 명령 및 향상된 UX | `/beads:ready`, `/beads:create` 명령을 원할 때 선택적으로 설치 |
| **MCP 서버(beads-mcp)** | Model Context Protocol 인터페이스 | MCP 전용 환경(Claude Desktop, Amp)에서만 필요 |

**구성 요소의 관계:**

- **bd CLI**가 핵심입니다. Homebrew, npm 또는 script로 먼저 설치하세요.
- **플러그인**은 slash 명령으로 Claude Code를 확장하지만 CLI가 설치되어 있어야 합니다.
- **MCP 서버**는 shell에 접근할 수 없는 환경에서 CLI를 대신합니다.

**중요:** Beads는 프로젝트에 클론하는 것이 아니라 시스템 전체에 설치합니다.
프로젝트의 `.beads/` 디렉터리에는 이슈 데이터베이스만 들어 있습니다.

**일반적인 설정:**

| 환경 | 설치 항목 |
|-------------|-----------------|
| Claude Code, Cursor, Windsurf | bd CLI 및 선택적 Claude Code 플러그인 |
| GitHub Copilot(VS Code) | bd CLI 및 MCP 서버 |
| Claude Desktop(shell 없음) | MCP 서버만 |
| 터미널/script | bd CLI만 |
| CI/CD 파이프라인 | bd CLI만 |

**서로 함께 사용할 수 없나요?** 아닙니다. CLI, 플러그인, MCP를 모두 설치해도
충돌하지 않습니다. 다만 대부분의 사용자는 CLI만 필요합니다.

## 빠른 설치(권장)

### Homebrew(macOS/Linux)

```bash
brew install beads
```

Homebrew core의 `beads` formula가 지원되는 Homebrew package입니다. 이전 tap
formula를 `bd`로 설치했다면 [이전 Homebrew tap에서
마이그레이션](/getting-started/upgrading#homebrew)을 참고하여 core formula로
전환하세요.

**Homebrew를 사용하는 이유:**

- 명령 하나로 간단히 설치합니다.
- `brew upgrade`로 자동 업데이트합니다.
- Go를 설치할 필요가 없습니다.
- PATH 설정을 자동으로 처리합니다.

### Mise-en-place(macOS/Linux/Windows)

[mise](https://mise.jdx.dev)를 사용하여 최신 GitHub release에서 Beads를 설치할 수 있습니다.

```bash
mise install github:gastownhall/beads
mise use -g github:gastownhall/beads
```

`-g`는 Beads를 전역으로 활성화합니다. 프로젝트별 버전을 활성화하려면 생략하세요.

**Mise를 사용하는 이유:**

- Homebrew처럼 간단하고 `mise up`으로 업데이트하며 Go 없이 작동하고 PATH를 처리합니다.
- 모든 플랫폼을 지원합니다.
- 항상 최신 release를 설치합니다.
- 특정 프로젝트에 다른 release 버전을 선택적으로 사용할 수 있습니다.

Mise의 Go backend에는 `go install`과 같은 주의 사항이 있습니다. 위의 release backend를 권장합니다.

### 빠른 설치 script(macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

설치 프로그램은 다음 작업을 수행합니다.

- 플랫폼 감지(macOS/Linux/FreeBSD, amd64/arm64)
- 다운로드한 release archive를 release의 `checksums.txt`로 검증
- Go가 있으면 지원되는 `go install` 모드로 대체
- 필요한 경우 소스 build로 대체
- 필요한 경우 PATH 설정 안내

macOS에서 script는 기본적으로 다운로드한 바이너리 signature를 보존합니다. 임시 로컬
재서명을 명시적으로 원한다면 다음과 같이 활성화하세요.

```bash
BEADS_INSTALL_RESIGN_MACOS=1 curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### 설치 방식 비교

| 방식 | 적합한 대상 | 업데이트 | 사전 요구 사항 | 참고 |
|--------|----------|---------|---------------|-------|
| **Homebrew** | macOS/Linux 사용자 | `brew upgrade beads` | Homebrew | 권장. 모든 작업을 자동 처리 |
| **Mise** | 모든 플랫폼 | `mise up` | mise | 최신 GitHub release 설치 |
| **npm** | JS/Node.js 프로젝트 | `npm update -g @beads/bd` | Node.js | npm 생태계를 사용한다면 편리함 |
| **bun** | JS/Bun.js 프로젝트 | `bun install -g --trust @beads/bd` | Bun.js | Bun 생태계를 사용한다면 편리함 |
| **설치 script** | 빠른 설정, CI/CD | script 다시 실행 | curl, bash | 자동화와 한 줄 설치에 적합 |
| **go install(nocgo)** | Go 개발자, 가장 간단한 설치 | 명령 다시 실행 | Go 1.24 이상 | **server 모드 전용**(embedded Dolt 없음) |
| **go install(cgo)** | embedded 모드를 원하는 Go 개발자 | 명령 다시 실행 | Go 1.24 이상, C compiler | embedded Dolt 완전 지원 |
| **소스에서 설치** | contributor 전용 | `git pull && go build` | Go, Git | 완전한 제어, 코드 수정 가능 |
| **AUR(Arch)** | Arch Linux 사용자 | `yay -Syu` | yay/paru | 커뮤니티 유지보수 |

**요약:** Homebrew를 사용할 수 있으면 사용하세요. Node.js 환경이라면 npm을
사용하세요. 빠른 일회성 설치나 CI에는 script를 사용하세요.

## Go 설치 및 build 의존성

`go install`이 꼭 필요하지 않다면 Homebrew, npm 또는 설치 script를 사용하세요.

`go install`은 기능이 서로 다른 두 가지 지원 모드를 제공합니다.

- **server 모드 전용(nocgo, 가장 간단함):** `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest`. Go toolchain이 있는 모든 머신에서 작동하며 C compiler가 필요 없습니다. server 모드 전용 바이너리를 생성하므로 외부 `dolt sql-server`를 실행하고 `bd init --server`를 사용해야 합니다. server 모드 설정은 [Dolt](/architecture/dolt)를 참고하세요.
- **embedded 지원(cgo):** `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest`. C compiler(Unix에서는 gcc/clang, Windows에서는 MinGW)가 필요합니다. 기본 embedded Dolt backend가 있는 바이너리를 생성하므로 `bd init`이 바로 작동합니다.

ICU header는 필요하지 않습니다. embedded 지원 명령은 `gms_pure_go`를 사용하므로
go-mysql-server가 ICU 대신 Go 표준 라이브러리의 regexp를 사용합니다.

`go install`에는 `github.com/steveyegge/beads` 경로를 사용하세요. 저장소는 현재
`gastownhall/beads`에 있지만 release된 Go module은 호환성을 위해 여전히
`github.com/steveyegge/beads`를 선언합니다.

선호하는 방식이 없다면 `brew install beads` 또는 설치 script로 embedded 지원
build를 간편하게 설치할 수 있습니다.

### build 의존성(contributor 전용)

<Note>
이 의존성은 소스에서 build할 때만 필요합니다. Homebrew, npm 또는 설치 script로
설치했다면 이 섹션 전체를 건너뛰세요.
</Note>

소스에서 build하려면 CGO 및 embedded Dolt용 C compiler가 필요합니다. ICU는 필요하지
않습니다. 모든 build는 ICU regex 대신 Go 표준 라이브러리 `regexp`를 선택하는
`gms_pure_go` tag를 사용합니다. 자세한 내용은
[ICU-POLICY.md](https://github.com/gastownhall/beads/blob/main/engdocs/ICU-POLICY.md)
를 참고하세요.

macOS (Homebrew):
```bash
brew install zstd
```

Linux (Debian/Ubuntu):
```bash
sudo apt-get install -y libzstd-dev
```

Linux (Fedora/RHEL):
```bash
sudo dnf install -y libzstd-devel
```

maintainer 전용: 남아 있는 ICU 코드 경로를 실행하는
[scripts/test-icu-path.sh](https://github.com/gastownhall/beads/blob/main/scripts/test-icu-path.sh)
를 의도적으로 실행해야 한다면 ICU 헤더를 설치하세요.
`brew install icu4c`(macOS) 또는 `sudo apt-get install -y libicu-dev`(Linux).
일반 개발에는 필요하지 않습니다.

## 플랫폼별 설치

### macOS

**Homebrew 사용**(권장):
```bash
brew install beads
```

**go install 사용**(server 모드 전용):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

**go install 사용**(embedded 지원, Xcode CLI 도구 필요):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

**소스에서 설치**:
```bash
git clone https://github.com/gastownhall/beads
cd beads
make build
sudo mv bd /usr/local/bin/
```

### Linux

**Homebrew 사용**(Linux에서도 작동):
```bash
brew install beads
```

**Arch Linux**(AUR):
```bash
# AUR에서 설치
yay -S beads-git
# 또는
paru -S beads-git
```

AUR package를 유지보수하는 [@v4rgas](https://github.com/v4rgas)에게 감사드립니다.

**go install 사용**(server 모드 전용):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

**go install 사용**(embedded 지원, gcc 필요):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### FreeBSD

**빠른 설치 script 사용**:
```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

**go install 사용**(server 모드 전용):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

### Windows 11

Beads는 Windows를 native로 지원하므로 MSYS나 MinGW가 필요하지 않습니다.

**사전 요구 사항:**

- [Go 1.24 이상](https://go.dev/dl/) 설치(`%USERPROFILE%\go\bin`을 `PATH`에 추가)
- Git for Windows

**PowerShell script 사용**:
```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

script는 prebuilt Windows release가 있으면 설치하고 다운로드한 ZIP checksum을
release의 `checksums.txt`로 검증합니다. Go는 `go install` 또는 소스 build에만
필요합니다.

**go install 사용**(server 모드 전용):
```pwsh
$env:CGO_ENABLED="0"; go install github.com/steveyegge/beads/cmd/bd@latest
```

C compiler가 필요 없는 server 모드 전용 바이너리를 생성합니다. Windows에서
작동하는 `bd`를 설치하는 가장 빠른 방법입니다.

**go install 사용**(embedded 지원, PATH에 MinGW-w64 gcc 필요):
```pwsh
$env:CGO_ENABLED="1"; $env:GOFLAGS="-tags=gms_pure_go"; go install github.com/steveyegge/beads/cmd/bd@latest
```

**소스에서 설치**:
```pwsh
git clone https://github.com/gastownhall/beads
cd beads
make build
Move-Item bd.exe $env:USERPROFILE\AppData\Local\Microsoft\WindowsApps\
```

**Windows 참고 사항:**

- Dolt 서버는 loopback TCP endpoint에서 수신합니다.
- 모든 host firewall에서 `bd.exe` loopback traffic을 허용하세요.

## IDE 및 편집기 통합

### CLI 및 hook(권장)

Claude Code, Cursor, Windsurf 등 shell에 접근할 수 있는 편집기에는 다음 방식을 권장합니다.

```bash
# 1. bd CLI 설치(위 빠른 설치 참고)
brew install beads

# 2. 프로젝트에서 초기화
cd your-project
bd init --quiet

# 3. 편집기 통합 설정(하나 선택)
bd setup claude   # Claude Code - SessionStart hook 설치
bd setup copilot  # GitHub Copilot CLI - .copilot-plugin/plugin.json + .github/copilot-instructions.md 생성
bd setup cursor   # Cursor IDE - .cursor/rules/beads.mdc 생성
bd setup aider    # Aider - .aider.conf.yml 생성
bd setup codex    # Codex CLI - Beads skill, AGENTS.md 지침, native hook 설치
bd setup factory  # Factory.ai Droid - AGENTS.md 생성/업데이트
bd setup mux      # Mux - AGENTS.md 생성/업데이트
```

**작동 방식:**

- `--skip-agents` 또는 `--stealth`를 사용하지 않으면 `bd init`은 기본적으로 `AGENTS.md`를 생성하거나 업데이트하고 프로젝트 Claude/Codex 통합을 설치합니다.
- 편집기 hook/규칙은 세션 시작 시 `bd prime`을 자동으로 주입합니다.
- Codex 0.129.0 이상은 native `/hooks`를 사용합니다. SessionStart가 `bd prime`을 주입하고 compact hook이 컨텍스트를 오래된 상태로 표시하며 압축 후 다음 prompt가 Beads 컨텍스트를 한 번 갱신합니다.
- `bd prime`은 약 1~2천 token의 워크플로 컨텍스트를 제공합니다.
- `bd` CLI 명령을 직접 사용합니다.
- `bd init`이 설치한 Git hook은 export와 legacy fallback을 갱신하고, `bd dolt push/pull`은 데이터베이스를 동기화합니다.
- `bd onboard`는 지원되지 않는 에이전트나 커스텀 지침 파일에 사용할 짧은 수동 snippet을 출력합니다.

**이 방식을 권장하는 이유:**

- **컨텍스트 효율** - MCP 도구 schema의 1만~5만 token보다 적은 약 1~2천 token
- **짧은 지연 시간** - MCP 프로토콜 오버헤드 없는 직접 CLI 호출
- **범용성** - shell에 접근할 수 있는 모든 편집기에서 작동

**설치 확인:** 모든 레시피는 `bd setup claude --check`, `bd setup copilot --check`
같은 check flag를 지원합니다.

### Claude Code 플러그인(선택 사항)

slash 명령으로 향상된 UX를 사용하려면 다음을 실행하세요.

```bash
# Claude Code에서 실행
/plugin marketplace add gastownhall/beads
/plugin install beads
# Claude Code 다시 시작
```

플러그인은 다음 기능을 추가합니다.

- slash 명령: `/beads:ready`, `/beads:create`, `/beads:show`, `/beads:update`, `/beads:close` 등
- 자율 실행용 작업 에이전트

전체 플러그인 문서는 [Claude Code 플러그인](/integrations/claude-code-plugin)을 참고하세요.

### GitHub Copilot

**GitHub Copilot을 사용하는 VS Code**에서는 MCP 서버(`uv tool install beads-mcp`)를
설치하고 프로젝트에 `.vscode/mcp.json`을 생성하세요. 모든 프로젝트에서
활성화하려면 VS Code 사용자 수준 MCP 설정에 추가할 수도 있습니다. 플랫폼별 사용자
수준 설정 경로를 포함한 전체 가이드는 [GitHub Copilot](/integrations/github-copilot)을
참고하세요.

**GitHub Copilot CLI** 터미널 통합에는 다음을 실행하세요.

```bash
bd setup copilot         # 프로젝트 Copilot 플러그인 및 저장소 지침 설치
bd setup copilot --check # 프로젝트 통합 파일이 있는지 확인
```

이 설정은 현재 프로젝트 범위만 지원합니다. `.copilot-plugin/plugin.json`과
`.github/copilot-instructions.md`를 작성합니다. 현재 Copilot에는 별도의 `--global`
또는 `--project` 모드가 없으며 `~/.copilot/...` 경로를 관리하지 않습니다. 전체
가이드는 [Copilot CLI](/integrations/copilot-cli)를 참고하세요.

### MCP 서버(대안)

CLI를 사용할 수 없는 환경(Claude Desktop, shell 없는 Sourcegraph Amp)에서만 MCP를 사용하세요.

```bash
# uv 사용(권장)
uv tool install beads-mcp

# 또는 pip 사용
pip install beads-mcp
```

**Claude Desktop 설정**(macOS):

`~/Library/Application Support/Claude/claude_desktop_config.json`에 추가하세요.

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

Sourcegraph Amp 설정과 자세한 MCP 서버 문서는 [MCP 서버](/integrations/mcp-server)를 참고하세요.

## 설치 확인

설치 후 `bd`가 작동하는지 확인하세요.

```bash
bd version
bd help
```

## 문제 해결

추가 문제 해결 방법은 [문제 해결](/reference/troubleshooting)을 참고하세요.

### `bd: command not found`

`bd`가 PATH에 없습니다.

```bash
# 설치 여부 확인
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Go bin을 PATH에 추가(~/.bashrc 또는 ~/.zshrc에 추가)
export PATH="$PATH:$(go env GOPATH)/bin"

# 또는 권장 설치 프로그램으로 다시 설치
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### macOS에서 `zsh: killed bd` 또는 crash 발생

일반적으로 CGO/SQLite 호환성 문제로 발생합니다.

```bash
# embedded 지원 build 설치
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

Homebrew formula는 이미 CGO를 활성화하므로 Homebrew로 설치했다면 이 작업이 필요하지
않습니다. Homebrew 버전에서도 계속 crash가 발생하면 [이슈를
등록하세요](https://github.com/gastownhall/beads/issues).

### MCP 서버 시작 실패(독립 실행형 beads-mcp)

Claude Code 플러그인 자체에는 MCP 서버가 포함되지 않습니다. 독립 실행형
`beads-mcp` 서버([MCP 서버](/integrations/mcp-server) 참고)를 설정했는데 즉시
실패한다면 `uv`가 설치되지 않았거나 PATH에 없을 가능성이 큽니다.

**증상:**

- 플러그인 slash 명령은 작동하지만 MCP 도구를 사용할 수 없습니다.
- 오류 log에 `command not found: uv`가 표시됩니다.
- 서버가 시작할 때 조용히 실패합니다.

**해결 방법:**
```bash
# uv 설치
curl -LsSf https://astral.sh/uv/install.sh | sh

# shell을 다시 시작하거나 PATH 업데이트
source ~/.local/bin/env

# uv 사용 가능 여부 확인
which uv

# Claude Code 다시 시작
```

다른 설치 방식은 [Claude Code 플러그인](/integrations/claude-code-plugin)을 참고하세요.

## bd 업데이트

업그레이드 체크리스트:

1. 새 바이너리를 설치하기 전에 현재 `bd`로 원격 기반 데이터베이스를 동기화합니다.
   `bd dolt push`
   `bd dolt pull`
2. 마이그레이션 전에 backup합니다.
   `bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl`
3. 아래에서 설치 방식에 맞는 명령으로 업그레이드합니다.
4. 업그레이드 후 다음 명령을 실행합니다.
   `bd info --whats-new`
   `bd hooks install`
   `bd version`
5. 원격 기반 데이터베이스의 schema 마이그레이션을 거치는 경우 지정된 migrator만
   다음 명령을 실행합니다.
   `bd migrate`
   `bd dolt push`

다른 클론은 독립적으로 마이그레이션하지 말고 새 바이너리를 설치한 뒤 `bd
bootstrap`을 실행해야 합니다. 전체 절차는
[업그레이드](/getting-started/upgrading)를 참고하세요.

### 빠른 설치 script(macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell 설치 프로그램(Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

### Homebrew

```bash
brew upgrade beads
```

### npm

```bash
npm update -g @beads/bd
```

### bun

```bash
bun install -g --trust @beads/bd
```

### go install

처음 설치할 때 사용한 모드를 사용하세요.

```bash
# server 모드 전용
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# embedded 지원
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### 소스에서 설치

```bash
cd beads
git pull
make build
sudo mv bd /usr/local/bin/
```

prerelease(예: release candidate)는 GitHub prerelease로만 게시되고 stable
Homebrew/npm/PyPI channel에는 push되지 않습니다. 따라서 `brew upgrade` 등의
명령으로는 설치되지 않으므로 prerelease build를 명시적으로 가져오세요.

업그레이드 후 단계(hook, 마이그레이션)는 [업그레이드](/getting-started/upgrading)를 참고하세요.

## 제거

저장소에서 Beads를 완전히 제거하려면 [제거](/recovery/uninstalling)를 참고하세요.

## 다음 단계

설치 후 다음 단계를 진행하세요.

1. **프로젝트 초기화**: `cd your-project && bd init`
2. **기본 사용법 학습**: [빠른 시작](/getting-started/quickstart) 참고
3. **에이전트 설정**: [IDE 설정](/getting-started/ide-setup)을 참고하거나 `bd setup --list` 실행
4. **예제 살펴보기**: [examples/](https://github.com/gastownhall/beads/tree/main/examples) 디렉터리 확인
