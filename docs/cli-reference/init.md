---
title: "bd init"
description: "현재 디렉터리에 .beads/ 디렉터리와 Dolt 데이터베이스를 생성하여 bd를 초기화합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc init`에서 생성되었습니다.

현재 디렉터리에 .beads/ 디렉터리와 Dolt 데이터베이스를 생성하여 bd를 초기화합니다.
사용자 정의 이슈 접두사를 선택적으로 지정할 수 있습니다.

Dolt는 기본이자 유일하게 지원되는 저장소 백엔드입니다. 레거시 SQLite 백엔드는
제거되었습니다. 마이그레이션 지침을 보려면 --backend=sqlite를 사용하세요.

기본 접두사 기반 명명을 재정의하여 기존 서버 데이터베이스 이름을 지정하려면
--database를 사용하세요. 외부 도구(예: 오케스트레이터)가 데이터베이스를 이미
생성한 경우 유용합니다.

--stealth 사용 시 보이지 않는 beads 사용을 위한 저장소별 git 설정을 구성합니다:
  • beads 파일이 커밋되지 않도록 .git/info/exclude 설정
  저장소 협업자에게 영향을 주지 않는 개인 사용에 적합합니다.
  특정 AI 도구를 설정하려면 다음을 실행하세요: bd setup &lt;claude|cursor|aider|...&gt; --stealth

기본적으로 beads는 임베디드 Dolt 엔진을 사용합니다(외부 서버 불필요). 대신 외부
dolt sql-server를 사용하려면 --server를 전달하세요. 서버 모드에서는 --server-host,
--server-port, --server-user로 연결 세부 정보를 설정합니다. 비밀번호는
BEADS_DOLT_PASSWORD 환경 변수로 설정해야 합니다.

자동 내보내기는 선택 사항입니다. 활성화하면 bd는 쓰기 명령 후 이슈를
.beads/issues.jsonl로 내보냅니다(60초당 한 번으로 스로틀링). 뷰어(bv), 교환,
이슈 수준 마이그레이션용이며 백업이 아닙니다. 컴퓨터 간 동기화와 백업에는 JSONL
가져오기/내보내기가 아닌 Dolt 원격/백업을 사용합니다.
활성화: bd config set export.auto true

비대화형 모드(--non-interactive 또는 BD_NON_INTERACTIVE=1):
  적절한 기본값을 사용하며 모든 대화형 프롬프트를 건너뜁니다:
  • 역할 기본값은 "maintainer"(--role로 재정의)
  • fork 감지 시 fork 제외 자동 구성
  • 자동 내보내기는 기본값(비활성화) 유지
  • --contributor와 --team 플래그 거부(wizard에 상호작용 필요)
  stdin이 터미널이 아니거나 CI=true가 설정된 경우에도 자동 감지됩니다.

```
bd init [flags]
```

**플래그:**

```
      --agents-file string                             에이전트 지침용 사용자 정의 파일 이름(기본값: AGENTS.md)
      --agents-profile string                          AGENTS.md 프로필: 'minimal'(기본값, bd prime 포인터) 또는 'full'(전체 명령 참조)
      --agents-template string                         사용자 정의 AGENTS.md 템플릿 경로(포함된 기본값 재정의)
      --backend string                                 저장소 백엔드(기본값: dolt). --backend=sqlite는 사용 중단 알림 출력.
      --contributor                                    OSS 기여자 설정 wizard 실행
      --database string                                기존 서버 데이터베이스 이름 사용(접두사 기반 명명 재정의)
      --debug                                          --loglevel=debug 및 CPU 프로파일링(--prof cpu)으로 관리형 Dolt sql-server 실행. config.yaml에 dolt.debug로 영구 저장. 외부 관리 서버에는 영향 없음.
      --destroy-token string                           비대화형 모드에서 파괴적 재초기화를 위한 명시적 확인 토큰(형식: 'DESTROY-<prefix>')
      --discard-remote                                 재초기화 시 구성된 원격의 Dolt 이력 폐기 승인. 비대화형 모드에는 --destroy-token 필요, 'bd help init-safety' 참조.
      --external                                       서버가 외부에서 관리됨(서버 시작 건너뛰기), --shared-server 또는 --server와 함께 사용
      --force                                          --reinit-local의 사용 중단된 별칭. 로컬 데이터 안전 보호만 우회하며 원격 분기를 승인하지 않음('bd help init-safety' 참조).
      --from-jsonl                                     구성된 import.path에서 이슈 가져오기, --discard-remote가 교체를 승인하지 않으면 원격 이력 거부
      --init-if-missing                                워크스페이스가 이미 초기화된 경우 실패하지 않고 init을 건너뛰며 0으로 종료(scaffold용 멱등 init)
      --non-interactive                                모든 대화형 프롬프트 건너뛰기(CI 또는 비TTY 환경에서 자동 감지)
  -p, --prefix string                                  이슈 접두사(기본값: 현재 디렉터리 이름)
      --proxied-server                                 [실험적] .beads/proxieddb를 루트로 하는 워크스페이스별 프록시 dolt sql-server(프록시 + 하위 dolt) 사용
      --proxied-server-config-path string              [실험적] 기존 dolt sql-server YAML 구성의 절대 경로(proxied-server 모드 전용). 설정 시 자동 생성 대신 이 파일 사용. 상대 경로 거부.
      --proxied-server-external-host string            [실험적] 프록시가 앞단에 위치할 외부 관리 dolt sql-server의 호스트 이름 또는 IP(proxied-server 모드 전용). --proxied-server-external-socket-path와 함께 사용할 수 없음.
      --proxied-server-external-keep-alive duration    [실험적] 프록시→외부 연결의 TCP keepalive 기간. 0은 패키지 기본값(30s) 사용.
      --proxied-server-external-port int               [실험적] 외부 관리 dolt sql-server의 TCP 포트(proxied-server 모드 전용). --proxied-server-external-host 설정 시 필수.
      --proxied-server-external-socket-path string     [실험적] 외부 관리 dolt sql-server의 절대 unix 소켓 경로(proxied-server 모드 전용). --proxied-server-external-host와 함께 사용할 수 없음. 상대 경로 거부.
      --proxied-server-external-tls                    [실험적] 외부 관리 dolt sql-server에 연결할 때 TLS 요구(proxied-server 모드 전용).
      --proxied-server-external-tls-cert-path string   [실험적] 클라이언트 TLS 인증서의 절대 경로(외부 관리 dolt sql-server에 대한 mTLS용). --proxied-server-external-tls-key-path와 함께 사용해야 함. 상대 경로 거부.
      --proxied-server-external-tls-key-path string    [실험적] 클라이언트 TLS 개인 키의 절대 경로(외부 관리 dolt sql-server에 대한 mTLS용). --proxied-server-external-tls-cert-path와 함께 사용해야 함. 상대 경로 거부.
      --proxied-server-external-user string            [실험적] 외부 관리 dolt sql-server의 MySQL 사용자(proxied-server 모드 전용). 비어 있으면 "root"가 기본값. 비밀번호는 런타임에 $BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD 환경 변수에서 읽고 디스크에 영구 저장하지 않음.
      --proxied-server-log-path string                 [실험적] 프록시 dolt sql-server 로그 파일의 절대 경로(proxied-server 모드 전용). 기본값: <beadsDir>/proxieddb/server.log. 상대 경로 거부.
      --proxied-server-root-path string                [실험적] 프록시 dolt sql-server의 lockfile, pidfile, 하위 .dolt 저장소를 보관하는 절대 디렉터리(proxied-server 모드 전용). 기본값: <beadsDir>/proxieddb. 아직 없어도 bd가 생성. 상대 경로 거부.
  -q, --quiet                                          출력 숨기기(quiet 모드)
      --reinit-local                                   기존 로컬 데이터 위에 로컬 .beads/ 재초기화. 원격 분기를 승인하지 않음, --discard-remote 참조.
      --remote string                                  클론하고 sync.remote로 영구 저장할 Dolt 원격 URL
      --role string                                    프롬프트 없이 beads 역할 설정: "maintainer" 또는 "contributor"
      --server                                         임베디드 엔진 대신 외부 dolt sql-server 사용
      --server-host string                             Dolt 서버 호스트(기본값: 127.0.0.1)
      --server-port int                                Dolt 서버 포트(기본값: 3307)
      --server-socket string                           Unix 도메인 소켓 경로(host/port 재정의)
      --server-user string                             Dolt 서버 MySQL 사용자(기본값: root)
      --setup-exclude                                  beads 파일을 로컬에 유지하도록 .git/info/exclude 구성(fork용)
      --shared-server                                  공유 Dolt 서버 모드 활성화(모든 프로젝트가 ~/.beads/shared-server/의 서버 하나 공유)
      --skip-agents                                    AGENTS.md 및 Claude/Codex 설정 생성 건너뛰기
      --skip-hooks                                     git 훅 설치 건너뛰기
      --stealth                                        스텔스 모드 활성화: 전역 gitattributes와 gitignore, 로컬 저장소 추적 없음
      --team                                           팀 워크플로 설정 wizard 실행
```
