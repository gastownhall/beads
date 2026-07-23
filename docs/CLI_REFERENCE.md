# bd — 전체 명령어 참조

bd 최신 버전에 대한 참조입니다. `bd help --all`에서 생성되었습니다.

## 목차

### 이슈 작업:

- [bd assign](#bd-assign) — 이슈를 누군가에게 할당
- [bd children](#bd-children) — 부모의 하위 bead 목록
- [bd close](#bd-close) — 하나 이상의 이슈 닫기
- [bd comment](#bd-comment) — 이슈에 댓글 추가
- [bd comments](#bd-comments) — 이슈의 댓글 조회 또는 관리
  - [bd comments add](#bd-comments-add) — 이슈에 댓글 추가
  - [bd comments list](#bd-comments-list) — 유효하지 않음 — 댓글 목록을 보려면 bd comments &lt;issue-id&gt;를 사용하세요
- [bd create](#bd-create) — 새 이슈 생성(Markdown/그래프 JSON에서 일괄 생성 가능)
- [bd create-form](#bd-create-form) — 대화형 폼으로 새 이슈 생성
- [bd delete](#bd-delete) — 하나 이상의 이슈를 삭제하고 참조를 정리
- [bd edit](#bd-edit) — $EDITOR에서 이슈 필드 편집
- [bd gate](#bd-gate) — 비동기 조정 게이트 관리
  - [bd gate add-waiter](#bd-gate-add-waiter) — 게이트에 대기자 추가
  - [bd gate check](#bd-gate-check) — 게이트를 평가하고 해결된 게이트를 닫기
  - [bd gate create](#bd-gate-create) — 이슈를 차단하는 게이트 생성
  - [bd gate discover](#bd-gate-discover) — gh:run 게이트의 await_id 검색
  - [bd gate list](#bd-gate-list) — 게이트 이슈 목록
  - [bd gate resolve](#bd-gate-resolve) — 게이트를 수동으로 해결(닫기)
  - [bd gate show](#bd-gate-show) — 게이트 이슈 표시
- [bd label](#bd-label) — 이슈 라벨 관리
  - [bd label add](#bd-label-add) — 하나 이상의 이슈에 라벨 추가
  - [bd label list](#bd-label-list) — 이슈의 라벨 목록 조회
  - [bd label list-all](#bd-label-list-all) — 데이터베이스의 모든 고유 라벨 목록 조회
  - [bd label propagate](#bd-label-propagate) — 상위 이슈의 라벨을 모든 하위 이슈로 전파
  - [bd label remove](#bd-label-remove) — 하나 이상의 이슈에서 라벨 제거
- [bd link](#bd-link) — 두 이슈를 종속성으로 연결
- [bd list](#bd-list) — 이슈 목록
- [bd merge-slot](#bd-merge-slot) — 순차적 충돌 해결을 위한 병합 슬롯 게이트 관리
  - [bd merge-slot acquire](#bd-merge-slot-acquire) — 병합 슬롯 획득
  - [bd merge-slot check](#bd-merge-slot-check) — 병합 슬롯 사용 가능 여부 확인
  - [bd merge-slot create](#bd-merge-slot-create) — 현재 rig의 병합 슬롯 bead 생성
  - [bd merge-slot release](#bd-merge-slot-release) — 병합 슬롯 해제
- [bd note](#bd-note) — 이슈에 노트 추가
- [bd priority](#bd-priority) — 이슈 우선순위 설정
- [bd promote](#bd-promote) — wisp를 영구 bead로 승격
- [bd q](#bd-q) — 빠른 캡처: 이슈 생성 후 ID만 출력
- [bd query](#bd-query) — 간단한 쿼리 언어로 이슈 조회
- [bd reopen](#bd-reopen) — 하나 이상의 닫힌 이슈를 재오픈
- [bd search](#bd-search) — 텍스트 쿼리로 이슈 검색
- [bd set-state](#bd-set-state) — 운영 상태 설정(이벤트 생성 + 라벨 업데이트)
- [bd show](#bd-show) — 이슈 상세 보기
- [bd state](#bd-state) — state dimension의 현재 값 조회
  - [bd state list](#bd-state-list) — 이슈의 모든 state dimension 목록
- [bd tag](#bd-tag) — 이슈에 라벨 추가
- [bd todo](#bd-todo) — TODO 항목 관리(task issue 편의 wrapper)
  - [bd todo add](#bd-todo-add) — 새 TODO 항목 추가
  - [bd todo done](#bd-todo-done) — TODO(s)를 완료 처리
  - [bd todo list](#bd-todo-list) — TODO 항목 목록 조회
- [bd update](#bd-update) — 하나 이상의 이슈 업데이트

### 뷰 및 보고서:

- [bd count](#bd-count) — 필터와 일치하는 이슈 개수 세기
- [bd diff](#bd-diff) — 두 커밋 또는 브랜치 간의 변경 사항 표시
- [bd find-duplicates](#bd-find-duplicates) — 텍스트 분석이나 AI를 사용해 의미적으로 유사한 이슈 찾기
- [bd history](#bd-history) — 이슈의 버전 이력 표시
- [bd lint](#bd-lint) — 이슈에서 누락된 템플릿 섹션 확인
- [bd stale](#bd-stale) — 오래된 이슈 표시(최근에 업데이트되지 않음)
- [bd status](#bd-status) — 이슈 데이터베이스 개요 및 통계 표시
- [bd statuses](#bd-statuses) — 유효한 이슈 상태 목록
- [bd types](#bd-types) — 유효한 이슈 유형 목록

### 의존성 및 구조:

- [bd dep](#bd-dep) — 의존성 관리
  - [bd dep add](#bd-dep-add) — 의존성 추가
  - [bd dep cycles](#bd-dep-cycles) — 의존성 순환 감지
  - [bd dep list](#bd-dep-list) — 하나 이상의 이슈에 대한 의존성 또는 의존자 목록 표시
  - [bd dep relate](#bd-dep-relate) — 이슈 간 양방향 relates_to 링크 생성
  - [bd dep remove](#bd-dep-remove) — 의존성 제거
  - [bd dep tree](#bd-dep-tree) — 의존성 트리 표시
  - [bd dep unrelate](#bd-dep-unrelate) — 이슈 간 relates_to 링크 제거
- [bd duplicate](#bd-duplicate) — 이슈를 다른 이슈의 중복으로 표시
- [bd duplicates](#bd-duplicates) — 중복된 이슈를 찾고 선택적으로 병합
- [bd epic](#bd-epic) — 에픽 관리 명령어
  - [bd epic close-eligible](#bd-epic-close-eligible) — 모든 하위 항목이 완료된 에픽 닫기
  - [bd epic status](#bd-epic-status) — 에픽 완료 상태 표시
- [bd graph](#bd-graph) — 이슈 의존성 그래프 표시
  - [bd graph check](#bd-graph-check) — 의존성 그래프 무결성 검사
- [bd supersede](#bd-supersede) — 이슈를 더 최신 이슈에 의해 대체됨으로 표시
- [bd swarm](#bd-swarm) — 구조화된 에픽을 위한 Swarm 관리
  - [bd swarm create](#bd-swarm-create) — 에픽에서 Swarm 분자 생성
  - [bd swarm list](#bd-swarm-list) — 모든 Swarm 분자 목록 표시
  - [bd swarm status](#bd-swarm-status) — 현재 Swarm 상태 표시
  - [bd swarm validate](#bd-swarm-validate) — Swarm을 위한 에픽 구조 검증

### 동기화 및 데이터:

- [bd backup](#bd-backup) — beads 데이터베이스를 백업합니다
  - [bd backup init](#bd-backup-init) — Dolt 백업 대상을 설정합니다
  - [bd backup remove](#bd-backup-remove) — 설정된 백업 대상을 제거합니다
  - [bd backup restore](#bd-backup-restore) — Dolt 백업에서 데이터베이스 복원
  - [bd backup status](#bd-backup-status) — 마지막 백업 상태를 표시합니다
  - [bd backup sync](#bd-backup-sync) — 설정된 Dolt 백업으로 데이터베이스 푸시
- [bd branch](#bd-branch) — 브랜치를 나열하거나 생성합니다
- [bd export](#bd-export) — 이슈를 JSONL 형식으로 내보냅니다
- [bd federation](#bd-federation) — 피어-투-피어 페더레이션을 관리합니다 (CGO 필요)
- [bd import](#bd-import) — JSONL 파일 또는 stdin에서 이슈를 데이터베이스로 가져옵니다
- [bd restore](#bd-restore) — 컴팩션된 이슈의 컴팩션 이전 내용을 복원합니다
- [bd vc](#bd-vc) — 버전 제어 작업
  - [bd vc commit](#bd-vc-commit) — 모든 스테이징된 변경 사항으로 커밋을 생성합니다
  - [bd vc merge](#bd-vc-merge) — 현재 브랜치에 브랜치를 병합합니다
  - [bd vc status](#bd-vc-status) — 현재 브랜치와 커밋되지 않은 변경 사항을 표시합니다

### 설정 및 구성:

- [bd bootstrap](#bd-bootstrap) — 신규 클론 및 복구를 위한 비파괴적 데이터베이스 설정
- [bd config](#bd-config) — 구성 설정 관리
  - [bd config apply](#bd-config-apply) — 시스템 상태를 구성에 맞게 조정
  - [bd config drift](#bd-config-drift) — 구성 대 실제 간 불일치 감지
  - [bd config get](#bd-config-get) — 구성 값 조회
  - [bd config list](#bd-config-list) — 모든 구성 목록
  - [bd config set](#bd-config-set) — 구성 값 설정
  - [bd config set-many](#bd-config-set-many) — 한 번의 작업으로 여러 구성 값 설정
  - [bd config show](#bd-config-show) — 출처 정보와 함께 모든 유효 구성 표시
  - [bd config unset](#bd-config-unset) — 구성 값 삭제
  - [bd config validate](#bd-config-validate) — 동기화 관련 구성 유효성 검사
- [bd context](#bd-context) — 유효한 백엔드 식별자와 저장소 컨텍스트 표시
- [bd dolt](#bd-dolt) — Dolt 데이터베이스 설정 구성
  - [bd dolt clean-databases](#bd-dolt-clean-databases) — Dolt 서버에서 오래된 테스트 데이터베이스 삭제
  - [bd dolt commit](#bd-dolt-commit) — 대기 중인 변경사항으로 Dolt 커밋 생성
  - [bd dolt killall](#bd-dolt-killall) — 고아 Dolt 서버 프로세스 모두 종료
  - [bd dolt pull](#bd-dolt-pull) — Dolt 원격에서 커밋 가져오기
  - [bd dolt push](#bd-dolt-push) — Dolt 원격으로 커밋 푸시
  - [bd dolt remote](#bd-dolt-remote) — Dolt 원격 저장소 관리
  - [bd dolt set](#bd-dolt-set) — Dolt 구성 값 설정
  - [bd dolt show](#bd-dolt-show) — 연결 상태와 함께 현재 Dolt 구성 표시
  - [bd dolt start](#bd-dolt-start) — 이 프로젝트의 Dolt SQL 서버 시작
  - [bd dolt status](#bd-dolt-status) — Dolt 엔진 상태 표시
  - [bd dolt stop](#bd-dolt-stop) — 이 프로젝트의 Dolt SQL 서버 중지
  - [bd dolt test](#bd-dolt-test) — Dolt 서버 연결 테스트
- [bd forget](#bd-forget) — 영구 메모리 삭제
- [bd hooks](#bd-hooks) — beads 통합을 위한 git 훅 관리
  - [bd hooks install](#bd-hooks-install) — bd git 훅 설치
  - [bd hooks list](#bd-hooks-list) — 설치된 git 훅 상태 목록 표시
  - [bd hooks run](#bd-hooks-run) — git 훅 실행 (thin shims에서 호출)
  - [bd hooks uninstall](#bd-hooks-uninstall) — bd git 훅 제거
- [bd human](#bd-human) — 인간 사용자용 필수 명령 표시
  - [bd human dismiss](#bd-human-dismiss) — 인간 개입이 필요한 bead 해제
  - [bd human list](#bd-human-list) — 인간 개입이 필요한 모든 bead 목록
  - [bd human respond](#bd-human-respond) — 인간 개입이 필요한 bead에 응답
  - [bd human stats](#bd-human-stats) — 인간 개입이 필요한 bead의 요약 통계 표시
- [bd info](#bd-info) — 데이터베이스 정보 표시
- [bd init](#bd-init) — 현재 디렉터리에서 bd 초기화
- [bd kv](#bd-kv) — 키-값 저장소 명령
  - [bd kv clear](#bd-kv-clear) — 키-값 쌍 삭제
  - [bd kv get](#bd-kv-get) — 키로 값 조회
  - [bd kv list](#bd-kv-list) — 모든 키-값 쌍 목록
  - [bd kv set](#bd-kv-set) — 키-값 쌍 설정
- [bd memories](#bd-memories) — 영구 메모리 목록 또는 검색
- [bd onboard](#bd-onboard) — 에이전트 지침 파일용 최소 스니펫 표시
- [bd prime](#bd-prime) — AI 최적화 워크플로우 컨텍스트 출력
- [bd quickstart](#bd-quickstart) — bd 빠른 시작 가이드
- [bd recall](#bd-recall) — 특정 메모리 조회
- [bd remember](#bd-remember) — 영구 메모리 저장
- [bd setup](#bd-setup) — AI 편집기 통합 설정
- [bd where](#bd-where) — 활성 beads 위치 표시

### 유지보수:

- [bd batch](#bd-batch) — 단일 데이터베이스 트랜잭션에서 여러 쓰기 작업 실행
- [bd compact](#bd-compact) — 오래된 Dolt 커밋을 스쿼시해 이력 크기 줄이기
- [bd doctor](#bd-doctor) — beads 설치 상태 확인 및 수정(시작하기)
- [bd flatten](#bd-flatten) — 모든 Dolt 이력을 단일 커밋으로 스쿼시
- [bd gc](#bd-gc) — 가비지 수집: 오래된 이슈 정리, Dolt 커밋 압축, Dolt GC 실행
- [bd migrate](#bd-migrate) — 데이터베이스 마이그레이션 명령
  - [bd migrate hooks](#bd-migrate-hooks) — 마커 관리 형식으로 Git 훅 마이그레이션 계획 또는 적용
  - [bd migrate issues](#bd-migrate-issues) — 저장소 간 이슈 이동
  - [bd migrate schema](#bd-migrate-schema) — 대기 중인 스키마 마이그레이션 적용(멱등적)
  - [bd migrate sync](#bd-migrate-sync) — 다중 클론 구성을 위한 sync.branch 워크플로우 설정
- [bd ping](#bd-ping) — 데이터베이스 연결 확인
- [bd preflight](#bd-preflight) — PR 준비 체크리스트 표시
- [bd prune](#bd-prune) — 공간 회수 및 오래된 닫힌 beads 삭제, export 축소
- [bd purge](#bd-purge) — 공간 회수를 위해 닫힌 임시 beads 삭제
- [bd recompute-blocked](#bd-recompute-blocked) — 모든 이슈의 is_blocked 재계산(풀 후 오래된 플래그 수리)
- [bd rename-prefix](#bd-rename-prefix) — 데이터베이스의 모든 이슈 접두사 이름 변경
- [bd rules](#bd-rules) — Claude 규칙 감사 및 압축
  - [bd rules audit](#bd-rules-audit) — 모순 및 병합 기회 점검을 위해 규칙 스캔
  - [bd rules compact](#bd-rules-compact) — 관련 규칙을 복합 규칙으로 병합
- [bd sql](#bd-sql) — beads 데이터베이스에 대한 원시 SQL 실행
- [bd upgrade](#bd-upgrade) — bd 버전 업그레이드 확인 및 관리
  - [bd upgrade ack](#bd-upgrade-ack) — 현재 bd 버전 승인
  - [bd upgrade review](#bd-upgrade-review) — 마지막 bd 버전 이후 변경사항 검토
  - [bd upgrade status](#bd-upgrade-status) — bd 버전 변경 여부 확인
- [bd worktree](#bd-worktree) — 병렬 개발을 위한 git worktree 관리
  - [bd worktree create](#bd-worktree-create) — worktree 생성
  - [bd worktree info](#bd-worktree-info) — 현재 디렉터리의 worktree 정보 표시
  - [bd worktree list](#bd-worktree-list) — 모든 git worktree 목록 표시
  - [bd worktree remove](#bd-worktree-remove) — 안전 점검과 함께 worktree 제거

### 통합 및 고급:

- [bd admin](#bd-admin) — 데이터베이스 유지 관리를 위한 관리 명령어
  - [bd admin cleanup](#bd-admin-cleanup) — 데이터베이스 크기 감소를 위해 닫힌 이슈를 삭제
  - [bd admin compact](#bd-admin-compact) — 공간을 절약하기 위해 오래된 닫힌 이슈를 압축
  - [bd admin reset](#bd-admin-reset) — 모든 beads 데이터와 설정을 제거
- [bd jira](#bd-jira) — Jira 통합 명령어
  - [bd jira pull](#bd-jira-pull) — Jira에서 특정 항목을 가져오기
  - [bd jira push](#bd-jira-push) — 특정 beads를 Jira로 푸시
  - [bd jira status](#bd-jira-status) — Jira 동기화 상태 표시
  - [bd jira sync](#bd-jira-sync) — Jira와 이슈 동기화
- [bd linear](#bd-linear) — Linear 통합 명령어
  - [bd linear pull](#bd-linear-pull) — Linear에서 특정 항목을 가져오기
  - [bd linear push](#bd-linear-push) — 특정 beads를 Linear로 푸시
  - [bd linear status](#bd-linear-status) — Linear 동기화 상태 표시
  - [bd linear sync](#bd-linear-sync) — Linear와 이슈 동기화
  - [bd linear teams](#bd-linear-teams) — 사용 가능한 Linear 팀 목록
- [bd repo](#bd-repo) — 여러 리포지토리 구성 관리
  - [bd repo add](#bd-repo-add) — 동기화에 추가할 리포지토리 추가
  - [bd repo list](#bd-repo-list) — 구성된 모든 리포지토리 목록 표시
  - [bd repo remove](#bd-repo-remove) — 동기화 구성에서 리포지토리 제거
  - [bd repo sync](#bd-repo-sync) — 다중 리포지토리 동기화를 수동으로 트리거

### 기타 명령:

- [bd ado](#bd-ado) — Azure DevOps 통합 명령
  - [bd ado projects](#bd-ado-projects) — 액세스 가능한 Azure DevOps 프로젝트 목록 표시
  - [bd ado pull](#bd-ado-pull) — Azure DevOps에서 특정 항목 가져오기
  - [bd ado push](#bd-ado-push) — 특정 beads를 Azure DevOps로 푸시
  - [bd ado status](#bd-ado-status) — Azure DevOps 동기화 상태 표시
  - [bd ado sync](#bd-ado-sync) — Azure DevOps와 이슈 동기화
- [bd audit](#bd-audit) — 에이전트 상호작용을 기록하고 레이블 지정 (append-only JSONL)
  - [bd audit label](#bd-audit-label) — 기존 상호작용을 참조하는 레이블 항목 추가
  - [bd audit record](#bd-audit-record) — 감사 상호작용 항목 추가
- [bd blocked](#bd-blocked) — 차단된 이슈 표시
- [bd completion](#bd-completion) — 지정한 쉘용 자동 완성 스크립트 생성
  - [bd completion bash](#bd-completion-bash) — bash용 자동 완성 스크립트 생성
  - [bd completion fish](#bd-completion-fish) — fish용 자동 완성 스크립트 생성
  - [bd completion powershell](#bd-completion-powershell) — powershell용 자동 완성 스크립트 생성
  - [bd completion zsh](#bd-completion-zsh) — zsh용 자동 완성 스크립트 생성
- [bd cook](#bd-cook) — formula를 proto로 컴파일(기본값은 임시)
- [bd defer](#bd-defer) — 하나 이상의 이슈 연기
- [bd formula](#bd-formula) — workflow formula 관리
  - [bd formula convert](#bd-formula-convert) — JSON에서 TOML로 formula 변환
  - [bd formula list](#bd-formula-list) — 사용 가능한 formula 목록 표시
  - [bd formula show](#bd-formula-show) — formula 세부 정보 표시
- [bd github](#bd-github) — GitHub 통합 명령
  - [bd github pull](#bd-github-pull) — GitHub에서 특정 항목 가져오기
  - [bd github push](#bd-github-push) — 특정 beads를 GitHub로 푸시
  - [bd github repos](#bd-github-repos) — 액세스 가능한 GitHub 저장소 목록 표시
  - [bd github status](#bd-github-status) — GitHub 동기화 상태 표시
  - [bd github sync](#bd-github-sync) — GitHub와 이슈 동기화
- [bd gitlab](#bd-gitlab) — GitLab 통합 명령
  - [bd gitlab projects](#bd-gitlab-projects) — 액세스 가능한 GitLab 프로젝트 목록 표시
  - [bd gitlab pull](#bd-gitlab-pull) — GitLab에서 특정 항목 가져오기
  - [bd gitlab push](#bd-gitlab-push) — 특정 beads를 GitLab로 푸시
  - [bd gitlab status](#bd-gitlab-status) — GitLab 동기화 상태 표시
  - [bd gitlab sync](#bd-gitlab-sync) — GitLab와 이슈 동기화
- [bd help](#bd-help) — 모든 명령에 대한 도움말
- [bd init-safety](#bd-init-safety) — bd init 플래그 의미와 destroy-token 형식 설명
- [bd mail](#bd-mail) — 메일 제공자에 위임 (예: gt mail)
- [bd metrics](#bd-metrics) — 익명 사용량 메트릭 설정 보기 또는 변경
  - [bd metrics example](#bd-metrics-example) — bd가 전송하는 익명 사용 메트릭의 실제 예시 보기
  - [bd metrics off](#bd-metrics-off) — 익명 사용량 메트릭 끄기
  - [bd metrics on](#bd-metrics-on) — 익명 사용량 메트릭 켜기
- [bd mol](#bd-mol) — Molecule 명령 (작업 템플릿)
  - [bd mol bond](#bd-mol-bond) — 두 proto 또는 molecule을 서로 결합
  - [bd mol burn](#bd-mol-burn) — digest를 만들지 않고 molecule 삭제
  - [bd mol current](#bd-mol-current) — molecule 워크플로에서 현재 위치 표시
  - [bd mol distill](#bd-mol-distill) — 기존 epic에서 formula 추출
  - [bd mol last-activity](#bd-mol-last-activity) — molecule의 마지막 활동 타임스탬프 표시
  - [bd mol pour](#bd-mol-pour) — proto를 영속 mol로 인스턴스화 (고체 -&gt; 액체)
  - [bd mol progress](#bd-mol-progress) — molecule 진행 요약 표시
  - [bd mol ready](#bd-mol-ready) — gate-resume 디스패치 준비가 된 molecule 찾기
  - [bd mol seed](#bd-mol-seed) — formula 접근성 확인
  - [bd mol show](#bd-mol-show) — molecule 세부 정보 표시
  - [bd mol squash](#bd-mol-squash) — molecule 실행을 digest로 압축
  - [bd mol stale](#bd-mol-stale) — 완료되었지만 닫히지 않은 molecule 감지
  - [bd mol wisp](#bd-mol-wisp) — wisps 생성 또는 관리 (임시 molecule)
- [bd notion](#bd-notion) — Notion 통합 명령
  - [bd notion connect](#bd-notion-connect) — 기존 Notion 데이터베이스 또는 데이터 소스에 bd 연결
  - [bd notion init](#bd-notion-init) — Notion에 전용 Beads 데이터베이스 생성
  - [bd notion pull](#bd-notion-pull) — Notion에서 특정 항목 가져오기
  - [bd notion push](#bd-notion-push) — 특정 beads를 Notion으로 푸시
  - [bd notion status](#bd-notion-status) — Notion 동기화 상태 표시
  - [bd notion sync](#bd-notion-sync) — Notion과 이슈 동기화
- [bd orphans](#bd-orphans) — 고아 이슈 식별 (커밋에서 참조되었지만 아직 열려 있음)
- [bd ready](#bd-ready) — 준비된 작업 표시 (열림, 활성 블로커 없음)
- [bd rename](#bd-rename) — 이슈 ID 이름 변경
- [bd ship](#bd-ship) — 교차 프로젝트 의존성을 위한 기능 게시
- [bd undefer](#bd-undefer) — 하나 이상의 이슈 연기 해제 (열린 상태로 복원)
- [bd version](#bd-version) — 버전 정보 출력

---

## 전역 플래그

이 플래그들은 모든 명령에 적용됩니다:

```
      --actor string              감사 추적용 행위자 이름(기본값: $BEADS_ACTOR, git user.name, $USER)
      --db string                 데이터베이스 경로(기본값: .beads/*.db 자동 탐색)
  -C, --directory string          명령 실행 전에 이 디렉터리로 이동(git -C와 유사)
      --dolt-auto-commit string   Dolt 자동 커밋 정책(off|on|batch). 'on': 쓰기마다 커밋. 'batch': bd dolt commit까지 커밋을 연기하며, 그동안 커밋되지 않은 변경 사항은 작업 집합에 유지. SIGTERM/SIGHUP 발생 시 대기 중인 batch 커밋 반영. 기본값: off. 구성 키 dolt.auto-commit으로 재정의
      --global                    전역 공유 서버 데이터베이스 사용(beads_global)
      --ignore-schema-skew        향후 스키마 드리프트가 있어도 계속 진행(일부 쿼리 실패 가능)
      --json                      JSON 형식으로 출력
      --profile                   성능 분석용 CPU 프로필 생성
  -q, --quiet                     필수적이지 않은 출력 억제(오류만)
      --readonly                  읽기 전용 모드: 쓰기 작업 차단(워커 샌드박스용)
      --sandbox                   샌드박스 모드: Dolt 자동 푸시 비활성화
  -v, --verbose                   상세/디버그 출력 활성화
```

---

## 이슈 작업:

### bd assign

이슈를 누군가에게 할당합니다.

'bd update &lt;id&gt; --assignee &lt;name&gt;'의 축약형입니다.

예시:
  bd assign bd-123 alice
  bd assign bd-123 ""      # 할당 해제

```
bd assign <id> <name>
```

### bd children

지정된 상위 비드의 모든 하위 비드를 나열합니다.

이것은 'bd list --parent &lt;id&gt; --status all'의 편의용 별칭입니다.
일반 'bd list'와 달리 children는 기본적으로 닫힌 이슈를 포함하며,
이는 주요 사용 사례가 상위 항목 아래의 모든 작업을 검사하는 것이기 때문입니다.

예시:
  bd children hq-abc123        # hq-abc123의 모든 하위 항목을 나열합니다
  bd children hq-abc123 --json # JSON 형식으로 하위 항목을 출력합니다
  bd children hq-abc123 --pretty # 트리 형식으로 하위 항목을 표시합니다

```
bd children <parent-id> [flags]
```

**플래그:**

```
      --pretty   하위 항목을 트리 형식으로 표시
```

### bd close

하나 이상의 이슈를 닫습니다.

문제 ID가 지정되지 않으면 마지막으로 다룬 이슈를 닫습니다(가장 최근의
create, update, show 또는 close 작업에서).

여러 개의 이슈를 닫을 때는 모든 ID에 대해 하나의 --reason을 제공하거나
ID당 --reason을 한 번씩 반복하세요. 이유는 위치에 따라 매핑됩니다. 첫 번째 --reason은
첫 번째 ID에 적용되고, 두 번째 --reason은 두 번째 ID에 적용되며,
플래그가 명령줄 어디에 나타나든 상관없이 적용됩니다.

```
bd close [id...] [flags]
```

**별칭:** done

**플래그:**

```
      --claim-next           사용 가능한 이슈 중 다음으로 우선순위가 높은 이슈를 자동으로 맡기
      --continue             molecule의 다음 단계로 자동 진행
  -f, --force                고정된 이슈 또는 충족되지 않은 gate를 강제로 닫기
      --no-auto              --continue와 함께 다음 단계를 표시하되 맡지 않음
  -r, --reason string        닫는 사유
      --reason-file string   파일에서 닫는 사유 읽기(stdin은 - 사용)
      --session string       Claude Code 세션 ID(또는 CLAUDE_SESSION_ID 환경 변수 설정)
      --suggest-next         닫은 후 새로 차단 해제된 이슈 표시
```

### bd comment

이슈에 코멘트를 추가합니다.

'bd comments add &lt;id&gt; "text"'의 약식 표현입니다.

예시:
  bd comment bd-123 "현재 진행 중입니다"
  bd comment bd-123 현재 진행 중입니다
  echo "파이프에서 보낸 코멘트" | bd comment bd-123 --stdin
  bd comment bd-123 --file notes.txt

```
bd comment <id> [text...] [flags]
```

**플래그:**

```
      --file string   파일에서 댓글 텍스트 읽기
      --stdin         stdin에서 댓글 텍스트 읽기
```

### bd comments

이슈의 댓글을 조회하거나 관리합니다.

예시:
  # 이슈의 모든 댓글을 나열합니다(이슈 ID가 필요합니다 — \"comments list\"는 없습니다)
  bd comments bd-123

  # JSON 형식으로 댓글 목록 보기
  bd comments bd-123 --json

  # 댓글 추가
  bd comments add bd-123 "이것은 댓글입니다"

  # 파일에서 댓글 추가
  bd comments add bd-123 -f notes.txt

```
bd comments [issue-id] [flags]
```

**플래그:**

```
      --local-time   UTC 대신 현지 시간으로 타임스탬프 표시
```

#### bd comments add

이슈에 댓글을 추가합니다.

예시:
  # 댓글 추가
  bd comments add bd-123 "지금 작업 중입니다"

  # 파일에서 코멘트 추가
  bd comments add bd-123 -f notes.txt

```
bd comments add [issue-id] [text] [flags]
```

**플래그:**

```
  -a, --author string   댓글에 작성자 추가
  -f, --file string     파일에서 댓글 텍스트 읽기
```

#### bd comments list

유효하지 않음 — 댓글을 나열하려면 bd comments &lt;issue-id&gt;를 사용하세요

```
bd comments list
```

### bd create

새 이슈를 생성합니다(Markdown/그래프 JSON에서 일괄 생성 가능)

```
bd create [title] [flags]
```

**별칭:** new

**플래그:**

```
      --acceptance string       인수 기준
      --append-notes string     기존 메모에 추가(줄바꿈 구분자 포함)
  -a, --assignee string         담당자
      --body-file string        파일에서 설명 읽기(stdin은 - 사용)
      --context string          이슈의 추가 컨텍스트
      --defer string            지정 날짜까지 연기(그때까지 bd ready에서 이슈 숨김). --due와 같은 형식
      --deps strings            'type:id' 또는 'id' 형식의 의존성(예: 'discovered-from:bd-20,blocks:bd-15' 또는 'bd-20')
  -d, --description string      이슈 설명
      --design string           설계 메모
      --design-file string      파일에서 설계 읽기(stdin은 - 사용)
      --dry-run                 실제 생성하지 않고 생성될 항목 미리 보기
      --due string              마감 날짜/시간. 형식: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15
      --ephemeral               임시로 생성(단기 유지, TTL 압축 대상)
  -e, --estimate int            예상 시간(분)(예: 1시간은 60)
      --event-actor string      이 이벤트를 발생시킨 엔터티 URI(--type=event 필요)
      --event-category string   이벤트 범주(예: patrol.muted, agent.started)(--type=event 필요)
      --event-payload string    이벤트별 JSON 데이터(--type=event 필요)
      --event-target string     영향을 받은 엔터티 URI 또는 bead ID(--type=event 필요)
      --external-ref string     외부 참조(예: 'gh-9', 'jira-ABC', Linear URL)
  -f, --file string             Markdown 파일에서 여러 이슈 생성
      --force                   접두사가 데이터베이스 접두사와 일치하지 않아도 강제로 생성
      --graph string            JSON 계획 파일에서 의존성이 있는 이슈 그래프 생성
      --id string               명시적 이슈 ID(예: 파티셔닝용 'bd-42')
  -l, --labels strings          레이블(쉼표로 구분)
      --metadata string         사용자 정의 메타데이터 설정(JSON 문자열 또는 읽을 @file.json)
      --mol-type string         molecule 유형: swarm(다중 에이전트), patrol(반복 작업), work(기본값)
      --no-history              GC 대상이 되지 않게 하면서 Dolt 커밋 이력 건너뛰기(영구 에이전트 bead용)
      --no-inherit-labels       상위 이슈에서 레이블을 상속하지 않음
      --notes string            추가 메모
      --parent string           계층형 하위 이슈의 상위 이슈 ID(예: 'bd-a3f8e9')
  -p, --priority string         우선순위(0-4 또는 P0-P4, 0이 가장 높음)(기본값 "2")
      --repo string             이슈의 대상 저장소(자동 라우팅 재정의)
      --silent                  이슈 ID만 출력(스크립팅용)
      --skills string           이 이슈에 필요한 skill
      --spec-id string          사양 문서에 연결
      --stdin                   stdin에서 설명 읽기(--body-file -의 별칭)
      --title string            이슈 제목(위치 인수 대신 사용)
  -t, --type string             이슈 유형(bug|feature|task|epic|chore|decision), 사용자 정의 유형에는 types.custom 구성 필요, 별칭: enhancement/feat→feature, dec/adr→decision(기본값 "task")
      --validate                설명에 이슈 유형별 필수 섹션이 있는지 검증
      --waits-for string        기다릴 스폰 이슈 ID(fanout gate용 waits-for 의존성 생성)
      --waits-for-gate string   gate 유형: all-children(모두 기다림) 또는 any-children(첫 항목 기다림)(기본값 "all-children")
      --wisp-type string        TTL 기반 압축용 wisp 유형: heartbeat, ping, patrol, gc_report, recovery, error, escalation
```

### bd create-form

인터랙티브 터미널 양식을 사용해 새 이슈를 생성합니다.

이 명령은 이슈를 생성하기 위한 사용자 친화적인 양식 인터페이스를 제공합니다,
제목, 설명, 유형, 우선순위, 레이블 및 기타 필드를 포함합니다.

--parent를 사용하여 기존 상위 이슈 아래에 하위 이슈를 생성합니다.
하위 이슈는 자동 생성된 계층 ID를 받게 됩니다(예: parent-id.1).

이 양식은 키보드 탐색을 사용합니다:
  - Tab/Shift+Tab: 필드 사이를 이동합니다
  - Enter: 마지막 필드 또는 제출 버튼에서 양식을 제출합니다
  - Ctrl+C: 취소하고 종료합니다
  - 화살표 키: 선택 필드 내에서 이동합니다

```
bd create-form [flags]
```

**플래그:**

```
      --parent string   계층형 하위 이슈를 생성할 상위 이슈 ID(예: 'bd-a3f8e9')
```

### bd delete

하나 이상의 이슈를 삭제하고 해당 이슈에 대한 모든 참조를 정리합니다.
이 명령은 다음을 수행합니다:
1. 해당 이슈와 관련된 모든 의존성 링크(모든 유형, 양방향)를 제거합니다.
2. 직접 연결된 이슈의 텍스트 참조를 "[deleted:ID]"로 업데이트합니다.
3. 데이터베이스에서 해당 이슈를 영구적으로 삭제합니다.

이는 취소할 수 없는 파괴적 작업입니다. 주의해서 사용하세요.

일괄 삭제:
여러 이슈를 한 번에 삭제합니다:
  bd delete bd-1 bd-2 bd-3 --force

파일에서 삭제(한 줄에 ID 하나씩):
  bd delete --from-file deletions.txt --force

삭제 전 미리보기:
  bd delete --from-file deletions.txt --dry-run

의존성 처리:
기본값: 삭제 집합에 포함되지 않은 종속 항목이 있는 경우 실패
  bd delete bd-1 bd-2

연쇄: 모든 종속 항목을 재귀적으로 삭제
  bd delete bd-1 --cascade --force

강제: 종속 항목을 삭제하고 고아 처리
  bd delete bd-1 --force

```
bd delete <issue-id> [issue-id...] [flags]
```

**플래그:**

```
      --cascade            모든 종속 이슈를 재귀적으로 삭제
      --dry-run            변경하지 않고 삭제될 항목 미리 보기
  -f, --force              실제 삭제(이 플래그가 없으면 미리 보기 표시)
      --from-file string   파일에서 이슈 ID 읽기(한 줄에 하나)
```

### bd edit

설정한 $EDITOR를 사용해 이슈 필드를 편집합니다.

기본적으로 설명을 편집합니다. 다른 필드는 플래그를 사용하여 편집하세요.

예제:
  bd edit bd-42                    # 설명 편집
  bd edit bd-42 --title            # 제목 편집
  bd edit bd-42 --design           # 디자인 노트 편집
  bd edit bd-42 --notes            # 노트 편집
  bd edit bd-42 --acceptance       # 승인 기준 편집

```
bd edit [id] [flags]
```

**플래그:**

```
      --acceptance    인수 기준 편집
      --description   설명 편집(기본값)
      --design        설계 메모 편집
      --notes         메모 편집
      --title         제목 편집
```

### bd gate

게이트는 워크플로우 단계를 차단하는 비동기 대기 조건입니다.

수식 단계에 게이트 필드가 있으면 게이트가 자동으로 생성됩니다.
차단된 단계가 진행되려면 게이트를 닫아야 합니다(수동으로 또는 감시자를 통해).

게이트 유형:
  human   - 수동으로 bd close가 필요합니다 (1단계)
  timer   - 타임아웃 후 만료됩니다 (2단계)
  gh:run  - GitHub 워크플로우를 기다립니다 (3단계)
  gh:pr   - PR 병합을 기다립니다 (3단계)
  bead    - rig 간 bead가 닫히기를 기다립니다 (4단계)

bead gates의 경우 await_id 형식은 &lt;rig&gt;:&lt;bead-id&gt;(예: "other-project:op-abc123")와 같습니다.

예시:
  bd gate list           # 열려 있는 모든 게이트 표시
  bd gate list --all     # 닫힌 게이트를 포함한 모든 게이트 표시
  bd gate check          # 열려 있는 모든 게이트 평가
  bd gate check --type=bead  # bead 게이트만 평가
  bd gate resolve &lt;id&gt;   # 게이트를 수동으로 닫기

```
bd gate
```

#### bd gate add-waiter

게이트 비드에 에이전트를 웨이터로 등록합니다.

게이트가 닫히면 대기자는 'bd gate wake'를 통해 깨우기 알림을 받게 됩니다.
대기자는 일반적으로 작업자의 주소(예: "my-project/workers/agent-1")입니다.

이것은 'bd done --phase-complete'에서 게이트 깨우기 알림을 등록하기 위해 사용됩니다.

```
bd gate add-waiter <gate-id> <waiter>
```

#### bd gate check

게이트 조건을 평가하고 해결된 게이트를 자동으로 닫습니다.

기본적으로 모든 열린 게이트를 확인합니다. 게이트 유형으로 필터링하려면 --type을 사용하세요.

게이트 유형:
  gh       - 모든 GitHub 게이트 확인(gh:run 및 gh:pr)
  gh:run   - GitHub Actions 워크플로우 실행 확인
  gh:pr    - 풀 리퀘스트 병합 상태 확인
  timer    - 타이머 게이트 확인(시간 초과를 기반으로 자동 만료)
  bead     - rig 간 bead 게이트 확인
  all      - 모든 게이트 유형 확인

GitHub 게이트는 상태를 조회하기 위해 'gh' CLI를 사용합니다:
  - gh:run은 'gh run view &lt;id&gt; --json status,conclusion'를 확인합니다
  - gh:pr은 'gh pr view &lt;id&gt; --json state,title'를 확인합니다

게이트는 다음과 같은 경우 해결됩니다:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - timer: 현재 시간 &gt; created_at + timeout
  - bead: 대상 bead status=closed

게이트는 다음과 같은 경우에 에스컬레이션됩니다:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED

예시:
  bd gate check              # 모든 게이트를 확인합니다
  bd gate check --type=gh    # GitHub 게이트만 확인합니다
  bd gate check --type=gh:run # 워크플로우 실행 게이트만 확인합니다
  bd gate check --type=timer # 타이머 게이트만 확인합니다
  bd gate check --type=bead  # rig 간 bead 게이트만 확인합니다
  bd gate check --dry-run    # 변경 사항 없이 발생할 동작을 표시합니다
  bd gate check --escalate   # 만료/실패한 게이트를 에스컬레이션합니다

```
bd gate check [flags]
```

**플래그:**

```
      --dry-run       변경 없이 수행될 작업 표시
  -e, --escalate      실패/만료한 gate 에스컬레이션
  -l, --limit int     결과 수 제한(기본값 100)(기본값 100)
  -t, --type string   검사할 gate 유형(gh, gh:run, gh:pr, timer, bead, all)
```

#### bd gate create

다른 이슈가 해결될 때까지 차단하는 임시 게이트 이슈를 생성합니다.

차단된 이슈는 'bd gate resolve'로 게이트가 해결될 때까지 'bd ready'에 표시되지 않습니다
'bd gate resolve'를 통해 해결됩니다.

게이트 유형:
  human   - 'bd gate resolve'를 수동으로 실행해야 합니다 (기본값)
  timer   - --timeout 기간 후 자동으로 해결됩니다
  gh:run  - GitHub Actions 워크플로우를 기다립니다
  gh:pr   - PR 병합을 기다립니다

예시:
  bd gate create --blocks bd-abc
  bd gate create --type=human --blocks bd-abc --reason="디자인 검토가 필요함"
  bd gate create --type=timer --blocks bd-abc --timeout=2h
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42

```
bd gate create [flags]
```

**플래그:**

```
      --await-id string   조건 식별자(실행 ID, PR 번호 등)
      --blocks string     차단할 이슈 ID(필수)
  -r, --reason string     gate 사유
      --timeout string    timeout 기간(예: 2h, 30m)
  -t, --type string       gate 유형(human, timer, gh:run, gh:pr)(기본값 "human")
```

#### bd gate discover

GitHub 워크플로 실행 ID를 찾습니다( CI/CD 완료를 기다리는 게이트 대상).

이 명령은 await_id가 없는 await_type="gh:run"인 열린 게이트를 찾고,
최근 GitHub 워크플로우 실행을 조회한 뒤 휴리스틱을 사용해 매칭합니다:
  - 브랜치 이름 매칭
  - 커밋 SHA 매칭
  - 시간 근접성 (게이트 생성 시점으로부터 5분 이내의 실행)

일치하면 게이트의 await_id가 GitHub run ID로 업데이트되어,
이후 폴링에서 run의 상태를 확인할 수 있게 됩니다.

예시:
  bd gate discover           # 일치하는 모든 게이트의 실행 ID를 자동으로 검색합니다
  bd gate discover --dry-run # 매칭될 항목을 미리 보여줍니다 (업데이트 없음)
  bd gate discover --branch main --limit 10  # 'main' 브랜치에서만 실행을 매칭합니다

```
bd gate discover [flags]
```

**플래그:**

```
  -b, --branch string      브랜치로 실행 필터링(기본값: 현재 브랜치)
  -n, --dry-run            미리 보기 모드: 업데이트하지 않고 일치 항목 표시
  -l, --limit int          GitHub에서 쿼리할 최대 실행 수(기본값 10)
  -a, --max-age duration   gate/실행 일치의 최대 나이(기본값 30m0s)
```

#### bd gate list

현재 beads 데이터베이스의 모든 게이트 이슈를 나열합니다.

기본적으로 열린 게이트만 표시합니다. 닫힌 게이트를 포함하려면 --all을 사용하세요.

```
bd gate list [flags]
```

**플래그:**

```
  -a, --all         닫힌 gate를 포함해 모두 표시
  -n, --limit int   결과 수 제한(기본값 50)(기본값 50)
```

#### bd gate resolve

게이트 이슈를 닫아 그 위에서 대기 중이던 단계를 해제합니다.

이는 'bd close &lt;gate-id&gt;'와 동등하지만 더 명시적인 이름입니다.
--reason을(를) 사용하여 게이트가 해결된 이유에 대한 맥락을 제공하세요.

```
bd gate resolve <gate-id> [flags]
```

**플래그:**

```
  -r, --reason string   gate 해결 사유
```

#### bd gate show

대기자를 포함한 게이트 이슈의 세부 정보를 표시합니다.

이는 'bd show'와 유사하지만 이슈가 게이트인지 검증합니다.

```
bd gate show <gate-id>
```

### bd label

이슈 라벨 관리

```
bd label
```

#### bd label add

하나 이상의 이슈에 레이블을 추가

```
bd label add [issue-id...] [label]
```

#### bd label list

이슈의 레이블 목록 보기

```
bd label list [issue-id]
```

#### bd label list-all

데이터베이스의 모든 고유 레이블을 나열합니다

```
bd label list-all
```

#### bd label propagate

상위 항목의 레이블을 아직 갖지 않은 모든 직접 하위 항목에 전파합니다. epic의 하위 작업 전체에 branch: 레이블을 적용할 때 유용합니다.

```
bd label propagate [parent-id] [label]
```

#### bd label remove

하나 이상의 이슈에서 레이블을 제거합니다

```
bd label remove [issue-id...] [label]
```

### bd link

두 개의 이슈를 의존성으로 연결합니다.

'bd dep add &lt;id1&gt; &lt;id2&gt;'의 약칭입니다. 기본적으로 "blocks"
종속성(id2 blocks id1)을 생성합니다. 다른 관계를 지정하려면 --type을 사용하세요.

예시:
  bd link bd-123 bd-456                    # bd-456가 bd-123을 차단
  bd link bd-123 bd-456 --type related     # bd-123이 bd-456와 관련됨
  bd link bd-123 bd-456 --type parent-child

```
bd link <id1> <id2> [flags]
```

**플래그:**

```
  -t, --type string   의존성 유형(blocks|tracks|related|parent-child|discovered-from)(기본값 "blocks")
```

### bd list

이슈 목록

```
bd list [flags]
```

**플래그:**

```
      --all                          닫힌 이슈를 포함해 모든 이슈 표시(기본 필터 재정의)
  -a, --assignee string              담당자로 필터링
      --closed-after string          지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string         지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string         지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string        지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --defer-after string           지정 날짜 이후 연기된 이슈 필터링(상대값 지원: +6h, tomorrow)
      --defer-before string          지정 날짜 이전 연기된 이슈 필터링(상대값 지원: +6h, tomorrow)
      --deferred                     defer_until이 설정된 이슈만 표시
      --desc-contains string         설명 부분 문자열로 필터링(대소문자 구분 안 함)
      --due-after string             지정 날짜 이후 마감인 이슈 필터링(상대값 지원: +6h, tomorrow)
      --due-before string            지정 날짜 이전 마감인 이슈 필터링(상대값 지원: +6h, tomorrow)
      --empty-description            설명이 비어 있거나 없는 이슈 필터링
      --exclude-label strings        지정 레이블 중 하나라도 있는 이슈 제외
      --exclude-type strings         결과에서 이슈 유형 제외(쉼표로 구분하거나 반복 가능, 예: --exclude-type=convoy,epic)
      --flat                         트리 형식을 비활성화하고 기존 평면 목록 출력 사용
      --format string                출력 형식: 'digraph'(golang.org/x/tools/cmd/digraph용), 'dot'(Graphviz) 또는 Go 템플릿
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
      --id string                    특정 이슈 ID로 필터링(쉼표로 구분, 예: bd-1,bd-5,bd-10)
      --include-gates                출력에 gate 이슈 포함(일반적으로 숨김)
      --include-infra                출력에 인프라 beads(agent/role/message) 포함
      --include-templates            출력에 템플릿 molecule 포함
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
      --label-pattern string         레이블 glob 패턴으로 필터링(예: 'tech-*'는 tech-debt, tech-legacy와 일치)
      --label-regex string           레이블 정규식 패턴으로 필터링(예: 'tech-(debt|legacy)')
  -n, --limit int                    결과 수 제한(기본값 50, 무제한은 0 사용)(기본값 50)
      --long                         각 이슈의 상세한 여러 줄 출력 표시
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --mol-type string              molecule 유형으로 필터링: swarm, patrol 또는 work
      --no-assignee                  담당자가 없는 이슈 필터링
      --no-labels                    레이블이 없는 이슈 필터링
      --no-pager                     페이저 출력 비활성화
      --no-parent                    하위 이슈 제외(최상위 이슈만 표시)
      --no-pinned                    고정된 이슈 제외
      --notes-contains string        메모 부분 문자열로 필터링(대소문자 구분 안 함)
      --offset int                   처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --overdue                      due_at이 과거인 이슈만 표시(닫힌 이슈 제외)
      --parent string                상위 이슈 ID로 필터링(지정한 이슈의 하위 이슈 표시)
      --pinned                       고정된 이슈만 표시
      --pretty                       상태/우선순위 기호가 있는 트리 형식으로 이슈 표시
  -p, --priority string              우선순위(0-4 또는 P0-P4, 0이 가장 높음)
      --priority-max string          최대 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --priority-min string          최소 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --ready                        준비된 이슈만 표시(활성 차단 요소 없음, bd ready와 같은 의미)
  -r, --reverse                      정렬 순서 반전
      --skip-labels                  레이블 로드를 건너뜁니다. 실제 레이블과 관계없이 출력의 labels 필드가 비어 있습니다. 호출자가 레이블 데이터에 의존하지 않을 때만 사용하세요. --label, --label-any, --label-pattern, --label-regex, --exclude-label, --no-labels와 함께 사용할 수 없습니다.
      --sort string                  필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
      --spec string                  spec_id 접두사로 필터링
  -s, --status string                저장된 상태로 필터링(open, in_progress, blocked, deferred, closed). 여러 값은 쉼표로 구분: --status open,in_progress. 참고: -s/--status를 반복하면 이전 값을 알림 없이 덮어씁니다. 여러 상태를 필터링할 때는 항상 쉼표 구분 형식을 사용하세요.
      --title string                 제목 텍스트로 필터링(대소문자 구분 없는 부분 문자열 일치)
      --title-contains string        제목 부분 문자열로 필터링(대소문자 구분 안 함)
      --tree                         계층형 트리 형식(기본값: true, 비활성화하려면 --flat 사용)(기본값 true)
  -t, --type string                  유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate, convoy). 별칭: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision
      --updated-after string         지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string        지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
  -w, --watch                        변경을 감시하고 표시 자동 업데이트(--pretty 포함)
      --wisp-type string             wisp 유형으로 필터링: heartbeat, ping, patrol, gc_report, recovery, error, escalation
```

### bd merge-slot

병합 슬롯 게이트는 병합 큐에서 충돌 해결을 직렬화합니다.

병합 슬롯은 배타적 접근 원시 타입이다: 한 번에 한 에이전트만이 이를 보유할 수 있다.
이는 여러 polecats가 충돌을 해결하기 위해 경주하다가 벌어지는 "원숭이 칼싸움"을 방지하고
연쇄 충돌을 만드는 일을 막는다.

각 rig에는 하나의 병합 슬롯 bead가 있습니다: &lt;prefix&gt;-merge-slot (gt:slot으로 라벨링됨).
이 슬롯은 다음을 사용합니다:
  - status=open: 슬롯이 사용 가능합니다
  - status=in_progress: 슬롯이 점유됨
  - metadata.holder: 현재 슬롯을 보유한 사용자
  - metadata.waiters: 우선순위 정렬된 대기자 큐

예시:
  bd merge-slot create              # 현재 rig에 대한 병합 슬롯 생성
  bd merge-slot check               # 슬롯 사용 가능 여부 확인
  bd merge-slot acquire             # 슬롯 획득 시도
  bd merge-slot release             # 슬롯 해제

```
bd merge-slot
```

#### bd merge-slot acquire

병합 슬롯을 독점 접근하기 위해 획득하려고 시도합니다.

슬롯이 사용 가능한 경우(status=open), 획득됩니다:
  - status가 in_progress로 설정됩니다
  - holder가 요청자로 설정됩니다

슬롯이 점유된 경우(status=in_progress), 명령은 다음 조건이 아니면 실패합니다.
--wait가 전달되면 요청자가 대기자 큐에 추가됩니다.

획득 주체를 지정하려면 --holder를 사용합니다 (기본값: BEADS_ACTOR 환경 변수).

```
bd merge-slot acquire [flags]
```

**플래그:**

```
      --holder string   슬롯 획득자(기본값: BEADS_ACTOR)
      --wait            슬롯을 보유 중이면 대기자 목록에 추가
```

#### bd merge-slot check

병합 슬롯이 사용 가능한지 또는 점유되어 있는지 확인합니다.

반환:
  - 사용 가능: 슬롯을 획득할 수 있습니다
  - &lt;holder&gt;에 의해 점유됨: 슬롯이 현재 점유되어 있습니다
  - 찾을 수 없음: 이 rig에 대한 병합 슬롯이 존재하지 않습니다

```
bd merge-slot check
```

#### bd merge-slot create

직렬화된 충돌 해결을 위한 병합 슬롯 비드를 생성합니다.

슬롯 ID는 beads 접두사(예: gt-merge-slot)를 기준으로 자동으로 생성됩니다.
슬롯은 status=open(사용 가능) 상태로 생성됩니다.

```
bd merge-slot create
```

#### bd merge-slot release

충돌 해결이 완료된 후 병합 슬롯을 해제합니다.

상태를 open으로 다시 설정하고 holder 필드를 지웁니다.
대기자가 있는 경우, 우선순위가 가장 높은 대기자가 이어서 획득해야 합니다.

```
bd merge-slot release [flags]
```

**플래그:**

```
      --holder string   슬롯 해제자(검증용)
```

### bd note

이슈의 notes 필드에 노트를 추가합니다.

다음은 'bd update &lt;id&gt; --append-notes "text"'의 단축 표현입니다.

예시:
  bd note gt-abc "불안정한 테스트를 수정했습니다"
  bd note gt-abc 불안정한 테스트를 수정했습니다
  echo "파이프의 메모" | bd note gt-abc --stdin
  bd note gt-abc --file notes.txt

```
bd note <id> [text...] [flags]
```

**플래그:**

```
      --file string   파일에서 메모 텍스트 읽기
      --stdin         stdin에서 메모 텍스트 읽기
```

### bd priority

이슈의 우선순위를 설정합니다.

'bd update &lt;id&gt; --priority &lt;n&gt;'의 단축 표현입니다.

우선순위 레벨:
  0 - 치명적 (보안, 데이터 손실, 빌드 실패)
  1 - 높음 (주요 기능, 중요한 버그)
  2 - 중간 (기본값)
  3 - 낮음 (폴리싱, 최적화)
  4 - 백로그 (향후 아이디어)

예시:
  bd priority bd-123 0    # 치명적
  bd priority bd-123 2    # 중간

```
bd priority <id> <n>
```

### bd promote

임시 이슈인 wisp를 영구적인 bead로 승격합니다.

이것은 wisps 테이블(dolt_ignored)의 issue를 영구
issues 테이블(Dolt 버전 관리형)로 복사하며 라벨, 종속성, 이벤트, 댓글을
보존합니다. 원래 ID가 유지되어 모든 링크가 계속 작동합니다.

승급 내역과 선택적 사유를 기록하는 코멘트가 추가됩니다.

예시:
  bd promote bd-wisp-abc123
  bd promote bd-wisp-abc123 --reason "장기적으로 추적할 가치가 있습니다"

```
bd promote <wisp-id> [flags]
```

**플래그:**

```
  -r, --reason string   승격 사유
```

### bd q

빠른 캡처는 이슈를 생성하고 이슈 ID만 출력합니다.
스크립팅 및 AI 에이전트 통합을 위해 설계되었습니다.

예시:
  bd q "로그인 버그 수정"           # 출력: bd-a1b2
  ISSUE=$(bd q "새 기능")    # 변수에 ID를 저장
  bd q "작업" | xargs bd show    # 다른 명령으로 파이프

```
bd q [title] [flags]
```

**플래그:**

```
  -l, --labels strings    레이블
  -p, --priority string   우선순위(0-4 또는 P0-P4)(기본값 "2")
  -t, --type string       이슈 유형(기본값 "task")
```

### bd query

복합 필터를 지원하는 간단한 쿼리 언어를 사용해 이슈를 조회하고,
불리언 연산자와 날짜 상대 표현식을 지원합니다.

쿼리 언어는 그렇지 않으면 여러 플래그가 필요하거나
jq를 통해 파이프해야 하는 복잡한 필터링을 가능하게 합니다.

구문:
  field=value       동등 비교
  field!=value      부등 비교
  field&gt;value       보다 큼
  field&gt;=value      크거나 같음
  field&lt;value       보다 작음
  field&lt;=value      작거나 같음

불리언 연산자 (대소문자 구분 안 함):
  expr AND expr     두 조건 모두 일치해야 합니다
  expr OR expr      두 조건 중 하나가 일치할 수 있습니다
  NOT expr          조건을 부정합니다
  (expr)            괄호로 그룹화합니다

지원되는 필드:
  status            저장 상태(open, in_progress, blocked, deferred, closed). 참고: 의존성으로 차단된 이슈는 "open" 상태로 유지됩니다. 찾으려면 'bd blocked'를 사용하세요
  priority          우선순위 수준 (0-4)
  type              이슈 유형 (bug, feature, task, epic, chore, decision)
  assignee          할당된 사용자 (미할당인 경우 "none" 사용)
  owner             이슈 소유자
  label             이슈 라벨 (라벨이 없는 경우 "none" 사용)
  title             제목에서 검색 (포함)
  description       설명에서 검색 (포함, 비어 있는 경우 "none")
  notes             노트에서 검색 (포함)
  created           생성 날짜/시간
  updated           마지막 업데이트 날짜/시간
  started           이슈가 처음으로 in_progress로 전환된 날짜/시간
  closed            종료 날짜/시간
  id                이슈 ID (와일드카드 지원: bd-*)
  spec              스펙 ID (와일드카드 지원)
  pinned            부울 (true/false)
  ephemeral         부울 (true/false)
  template          부울 (true/false)
  parent            상위 이슈 ID
  mol_type          분자 유형 (swarm, patrol, work)

날짜 값:
  상대 기간: 7d (7일 전), 24h (24시간 전), 2w (2주 전)
  절대 날짜: 2025-01-15, 2025-01-15T10:00:00Z
  자연어: tomorrow, "next monday", "in 3 days"

예시:
  bd query "status=open AND priority&gt;1"
  bd query "status=open AND priority&lt;=2 AND updated&gt;7d"
  bd query "(status=open OR status=blocked) AND priority&lt;2"
  bd query "type=bug AND label=urgent"
  bd query "NOT status=closed"
  bd query "assignee=none AND type=task"
  bd query "created&gt;30d AND status!=closed"
  bd query "label=frontend OR label=backend"
  bd query "title=authentication AND priority=0"

```
bd query [expression] [flags]
```

**플래그:**

```
  -a, --all           닫힌 이슈 포함(기본값: 닫힌 이슈 제외)
  -n, --limit int     결과 수 제한(기본값: 50, 0 = 무제한)(기본값 50)
      --long          각 이슈의 상세한 여러 줄 출력 표시
      --offset int    처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --parse-only    쿼리만 구문 분석하고 AST 표시(디버깅용)
  -r, --reverse       정렬 순서 반전
      --sort string   필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
```

### bd reopen

상태를 'open'으로 설정하고 closed_at 타임스탬프를 지워 닫힌 이슈를 다시 엽니다.
이는 'bd update --status open'보다 더 명시적이며 Reopened 이벤트를 발생시킵니다.

```
bd reopen [id...] [flags]
```

**옵션:**

```
  -r, --reason string   다시 여는 사유
```

### bd search

제목과 ID 전체에서 이슈를 검색합니다(기본적으로 닫힌 이슈는 제외됩니다).

ID와 유사한 쿼리(예: "bd-123", "hq-319")는 빠른 정확/접두사 매칭을 사용합니다.
텍스트 쿼리는 제목을 검색합니다. 설명 검색에는 --desc-contains를 사용하세요.
닫힌 이슈를 포함하려면 --status all을 사용하세요.

예시:
  bd search "인증 버그"
  bd search "로그인" --status open
  bd search "데이터베이스" --label backend --limit 10
  bd search --query "성능" --assignee alice
  bd search "bd-5q" # 부분 ID로 검색 (빠른 접두사 일치)
  bd search "보안" --priority-min 0 --priority-max 2
  bd search "버그" --created-after 2025-01-01
  bd search "리팩터링" --status all  # 닫힌 이슈 포함
  bd search "버그" --sort priority
  bd search "작업" --sort created --reverse
  bd search "api" --desc-contains "엔드포인트"
  bd search "정리" --no-assignee --no-labels

```
bd search [query] [flags]
```

**플래그:**

```
  -a, --assignee string              담당자로 필터링
      --closed-after string          지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string         지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string         지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string        지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --desc-contains string         설명 부분 문자열로 필터링(대소문자 구분 안 함)
      --empty-description            설명이 비어 있거나 없는 이슈 필터링
      --external-contains string     외부 참조 부분 문자열로 필터링(대소문자 구분 안 함)
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함)
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함)
  -n, --limit int                    결과 수 제한(기본값: 50)(기본값 50)
      --long                         각 이슈의 상세한 여러 줄 출력 표시
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --no-assignee                  담당자가 없는 이슈 필터링
      --no-labels                    레이블이 없는 이슈 필터링
      --notes-contains string        메모 부분 문자열로 필터링(대소문자 구분 안 함)
      --priority-max string          최대 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --priority-min string          최소 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --query string                 검색 쿼리(위치 인수 대신 사용)
  -r, --reverse                      정렬 순서 반전
      --sort string                  필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
  -s, --status string                저장된 상태로 필터링(open, in_progress, blocked, deferred, closed, all). 기본값은 closed 제외, 포함하려면 'all' 사용. 참고: 의존성으로 차단된 이슈에는 'bd blocked' 사용
  -t, --type string                  유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate)
      --updated-after string         지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string        지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
```

### bd set-state

이슈의 운영 상태를 원자적으로 설정합니다.

이 명령은:
1. 상태 변경을 기록하는 이벤트 bead를 생성합니다 (신뢰할 수 있는 원본)
2. 해당 dimension의 기존 레이블을 제거합니다
3. 새로운 dimension:value 레이블을 추가합니다 (빠른 조회 캐시)

상태 레이블은 &lt;dimension&gt;:&lt;value&gt; 형식을 따르며, 예를 들어:
  patrol:active, patrol:muted
  mode:normal, mode:degraded
  health:healthy, health:failing

예시:
  bd set-state agent-abc patrol=muted --reason "멈춘 워커 조사 중"
  bd set-state agent-abc mode=degraded --reason "높은 오류율이 감지됨"
  bd set-state agent-abc health=healthy

--reason 플래그는 이벤트 비드에 대한 컨텍스트를 제공합니다 (권장).

```
bd set-state <issue-id> <dimension>=<value> [flags]
```

**플래그:**

```
      --reason string   상태 변경 사유(이벤트에 기록)
```

### bd show

이슈 세부 정보 표시

```
bd show [id...] [--id=<id>...] [--current] [flags]
```

**별칭:** view

**플래그:**

```
      --as-of string         특정 커밋 해시 또는 브랜치 시점의 이슈 표시(Dolt 필요)
      --children             이 이슈의 하위 이슈만 표시
      --current              현재 활성 이슈 표시(in-progress, hooked 또는 마지막으로 접근)
      --id stringArray       이슈 ID(플래그처럼 보이는 ID에 사용, 예: --id=gt--xyz)
      --include-comments     JSON 출력에 전체 댓글 본문 스트리밍(--json 전용, 댓글이 많은 이슈에서 느릴 수 있음)
      --include-dependents   JSON 출력에 전체 종속 이슈 스트리밍(--json 전용, hub bead에서 느릴 수 있음)
      --local-time           UTC 대신 현지 시간으로 타임스탬프 표시
      --long                 사용 가능한 모든 필드 표시(확장 메타데이터, 에이전트 신원, gate 필드 등)
      --refs                 이 이슈를 참조하는 이슈 표시(역방향 조회)
      --short                이슈별 간결한 한 줄 출력 표시
      --thread               전체 대화 스레드 표시(메시지용)
  -w, --watch                변경을 감시하고 표시 자동 새로 고침
```

### bd state

이슈의 라벨에서 상태 차원의 현재 값을 조회합니다.

상태 레이블은 &lt;dimension&gt;:&lt;value&gt; 형식을 따르며, 예를 들어:
  patrol:active
  mode:degraded
  health:healthy

이 명령은 주어진 차원의 값을 추출합니다.

예제:
  bd state witness-abc patrol     # 출력: active
  bd state witness-abc mode       # 출력: normal
  bd state witness-abc health     # 출력: healthy

```
bd state <issue-id> <dimension>
```

#### bd state list

이슈에서 모든 상태 레이블을 (dimension:value 형식)으로 나열합니다.

이것은 상태 규칙을 따르는 라벨만 표시하도록 라벨을 필터링합니다.

예시:
  bd state list witness-abc
  # 출력:
  #   patrol: active
  #   mode: normal
  #   health: healthy

```
bd state list <issue-id>
```

### bd tag

이슈에 레이블을 추가합니다.

다음은 'bd update &lt;id&gt; --add-label &lt;label&gt;'의 약칭입니다.

예시:
  bd tag bd-123 bug
  bd tag bd-123 needs-review

```
bd tag <id> <label>
```

### bd todo

TODO 항목을 가벼운 작업 이슈로 관리합니다.

TODOs는 task 유형의 일반적인 이슈로, 편리한 바로 가기를 제공합니다:
  bd todo add "제목"    -&gt; bd create "제목" -t task -p 2
  bd todo                -&gt; bd list --type task --status open
  bd todo done &lt;id&gt;      -&gt; bd close &lt;id&gt;

TODO 항목은 유형 또는 우선순위를 변경해서 정식 이슈로 승격할 수 있습니다:
  bd update todo-123 --type bug --priority 0

```
bd todo
```

#### bd todo add

새 TODO 항목을 추가하기

```
bd todo add <title> [flags]
```

**플래그:**

```
  -d, --description string   설명
  -p, --priority int         우선순위(0-4, 기본값 2)(기본값 2)
```

#### bd todo done

TODO(s)를 완료로 표시

```
bd todo done <id> [<id>...] [flags]
```

**플래그:**

```
      --reason string   닫는 사유(기본값: Completed)
```

#### bd todo list

TODO 항목 목록

```
bd todo list [flags]
```

**플래그:**

```
      --all   완료된 항목을 포함해 모든 TODO 표시
```

### bd update

하나 이상의 이슈를 업데이트합니다.

이슈 ID가 제공되지 않으면 마지막으로 수정한 이슈(가장 최근의
create, update, show, 또는 close 작업에서) 업데이트합니다.

```
bd update [id...] [flags]
```

**플래그:**

```
      --acceptance string            인수 기준
      --add-label strings            레이블 추가(반복 가능)
      --allow-empty-description      stdin 또는 파일에서 읽을 때 빈 설명으로 교체 허용
      --append-notes string          기존 메모에 추가(줄바꿈 구분자 포함)
  -a, --assignee string              담당자
      --await-id string              gate await_id 설정(예: gh:run gate의 GitHub 실행 ID)
      --body-file string             파일에서 설명 읽기(stdin은 - 사용)
      --claim                        이슈를 원자적으로 맡기(담당자를 자신으로, 상태를 in_progress로 설정, 이미 자신이 맡았다면 멱등)
      --defer string                 지정 날짜까지 연기(지우려면 빈 값). 그때까지 이슈가 bd ready에서 숨겨짐
  -d, --description string           이슈 설명
      --design string                설계 메모
      --design-file string           파일에서 설계 읽기(stdin은 - 사용)
      --due string                   마감 날짜/시간(지우려면 빈 값). 형식: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15
      --ephemeral                    이슈를 임시(wisp)로 표시 - JSONL로 내보내지 않음
  -e, --estimate int                 예상 시간(분)(예: 1시간은 60)
      --external-ref string          외부 참조(예: 'gh-9', 'jira-ABC', Linear URL)
      --history                      no-history 플래그 지우기(Dolt 커밋 이력 다시 활성화)
      --metadata string              사용자 정의 메타데이터 설정(JSON 문자열 또는 읽을 @file.json)
      --no-history                   이슈를 no-history로 표시(Dolt 커밋 건너뛰기, GC 대상 아님)
      --notes string                 추가 메모
      --parent string                새 상위 이슈 ID(이슈 상위 항목 변경, 제거하려면 빈 문자열 사용)
      --persistent                   이슈를 영구로 표시(wisp를 일반 이슈로 승격)
  -p, --priority string              우선순위(0-4 또는 P0-P4, 0이 가장 높음)
      --remove-label strings         레이블 제거(반복 가능)
      --session string               status=closed용 Claude Code 세션 ID(또는 CLAUDE_SESSION_ID 환경 변수 설정)
      --set-labels strings           모든 기존 레이블을 교체하여 설정(반복 가능)
      --set-metadata stringArray     메타데이터 key=value 설정(반복 가능, 예: --set-metadata team=platform)
      --spec-id string               사양 문서에 연결
  -s, --status string                새 상태
      --stdin                        stdin에서 설명 읽기(--body-file -의 별칭)
      --title string                 새 제목
  -t, --type string                  새 유형(bug|feature|task|epic|chore|decision), 사용자 정의 유형에는 types.custom 구성 필요
      --unset-metadata stringArray   메타데이터 키 제거(반복 가능, 예: --unset-metadata team)
```

## 보기 및 보고서:

### bd count

지정된 필터와 일치하는 이슈의 개수를 계산합니다.

기본적으로 필터와 일치하는 이슈의 총 개수를 반환합니다.
--by-* 플래그를 사용하여 다양한 속성별로 개수를 그룹화합니다.

예시:
  bd count                          # 모든 이슈를 카운트
  bd count --status open            # 열린 이슈를 카운트
  bd count --by-status              # 상태별로 그룹화해 카운트
  bd count --by-priority            # 우선순위별로 그룹화해 카운트
  bd count --by-type                # 이슈 유형별로 그룹화해 카운트
  bd count --by-assignee            # 담당자별로 그룹화해 카운트
  bd count --by-label               # 레이블별로 그룹화해 카운트
  bd count --assignee alice --by-status  # alice의 이슈를 상태별로 카운트
  bd count --include-infra          # 이슈 + wisps tier를 카운트 ('bd list --include-infra --all'의 cardinality와 일치)


```
bd count [flags]
```

**플래그:**

```
  -a, --assignee string         담당자로 필터링
      --by-assignee             담당자별 수 그룹화
      --by-label                레이블별 수 그룹화
      --by-priority             우선순위별 수 그룹화
      --by-status               상태별 수 그룹화
      --by-type                 이슈 유형별 수 그룹화
      --closed-after string     지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string    지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string    지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string   지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --desc-contains string    설명 부분 문자열로 필터링
      --empty-description       설명이 비어 있는 이슈 필터링
      --id string               특정 이슈 ID로 필터링(쉼표로 구분)
      --include-infra           인프라 bead와 wisp 계층 포함('bd list --include-infra --all'의 카디널리티와 일치)
  -l, --label strings           레이블로 필터링(AND: 모두 있어야 함)
      --label-any strings       레이블로 필터링(OR: 하나 이상 있어야 함)
      --no-assignee             담당자가 없는 이슈 필터링
      --no-labels               레이블이 없는 이슈 필터링
      --notes-contains string   메모 부분 문자열로 필터링
  -p, --priority int            우선순위로 필터링(0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)
      --priority-max int        최대 우선순위로 필터링(경계값 포함)
      --priority-min int        최소 우선순위로 필터링(경계값 포함)
  -s, --status string           저장된 상태로 필터링(open, in_progress, blocked, deferred, closed). 참고: 의존성으로 차단된 이슈에는 'bd blocked' 사용
      --title string            제목 텍스트로 필터링(대소문자 구분 없는 부분 문자열 일치)
      --title-contains string   제목 부분 문자열로 필터링
  -t, --type string             유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate)
      --updated-after string    지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string   지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
```

### bd diff

두 커밋 또는 브랜치 간의 이슈 차이를 보여줍니다.

refs는 다음과 같습니다:
- 커밋 해시(예: abc123def)
- 브랜치 이름(예: main, feature-branch)
- HEAD, HEAD~1 같은 특수 refs

예제:
  bd diff main feature-branch   # main을 feature 브랜치와 비교
  bd diff HEAD~5 HEAD           # 마지막 5개의 커밋에서 변경사항 표시
  bd diff abc123 def456         # 두 개의 특정 커밋 비교

```
bd diff <from-ref> <to-ref>
```

### bd find-duplicates

정확히 중복되지는 않지만 의미적으로 유사한 이슈를 찾습니다.

정확한 내용 일치를 찾는 'bd duplicates'와 달리 find-duplicates는
텍스트 유사도 또는 AI를 사용해
동일한 주제를 서로 다른 문구로 다루는 이슈를 찾습니다.

접근 방식:
  mechanical  토큰 기반 텍스트 유사도 (기본값, API 키가 필요하지 않음)
  ai          LLM 기반 의미 기반 비교 (ANTHROPIC_API_KEY 또는 ai.api_key가 필요함)

기계적 접근은 제목과 설명을 토큰화한 다음
모든 이슈 쌍 간의 Jaccard 유사도를 계산한다. 빠르고 무료이지만
의미적으로 유사한데 문구가 매우 다른 이슈를 놓칠 수 있다.

AI 접근 방식은 Claude로 후보 쌍을 보내 의미론적 비교를 수행합니다.
먼저 기계적 사전 필터링을 사용해 API 호출 횟수를 줄인 뒤,
그런 다음 LLM에 나머지 쌍이 실제 중복인지 판별하도록 요청합니다.)

예시:
  bd find-duplicates                       # 기계적 유사성(기본값)
  bd find-duplicates --threshold 0.4       # 임계값이 낮을수록 더 많은 결과
  bd find-duplicates --method ai           # 의미론적 비교에 AI 사용
  bd find-duplicates --status open         # 열린 이슈만 확인
  bd find-duplicates --limit 20            # 상위 20개 쌍 표시
  bd find-duplicates --json                # JSON 출력

```
bd find-duplicates [flags]
```

**별칭:** find-dups

**플래그:**

```
  -n, --limit int         표시할 최대 쌍 수(기본값 50)
      --method string     감지 방법: mechanical, ai(기본값 "mechanical")
      --model string      사용할 AI 모델(--method ai에서만 사용, 기본값은 ai.model 구성)
  -s, --status string     상태로 필터링(기본값: 닫히지 않음)
      --threshold float   유사도 임곗값(0.0-1.0, 낮을수록 결과 증가)(기본값 0.5)
```

### bd history

이슈의 전체 버전 기록을 표시합니다. 여기에
이슈가 수정된 모든 커밋이 포함됩니다.

예시:
  bd history bd-123           # 이슈 bd-123의 모든 이력 보기
  bd history bd-123 --limit 5 # 마지막 5개 변경사항 표시

```
bd history <id> [flags]
```

**플래그:**

```
      --limit int   이력 항목 수 제한(0 = 전체)
```

### bd lint

이슈 유형에 따라 누락된 권장 섹션을 확인합니다.

기본적으로 열린 모든 이슈를 린트합니다. 특정 이슈를 린트하려면 이슈 ID를 지정하세요.

유형별 섹션 요구사항:
  bug:      재현 단계, 수락 기준
  task:     수락 기준
  feature:  수락 기준
  epic:     성공 기준
  chore:    (없음)

예시:
  bd lint                    # 열린 이슈를 모두 린트
  bd lint bd-abc             # 특정 이슈 린트
  bd lint bd-abc bd-def      # 여러 개 이슈 린트
  bd lint --type bug         # 버그만 린트
  bd lint --status all       # 모든 이슈를 린트(닫힌 항목 포함)


```
bd lint [issue-id...] [flags]
```

**플래그:**

```
  -s, --status string   상태로 필터링(기본값: open, 전체에는 'all' 사용)
  -t, --type string     이슈 유형으로 필터링(bug, task, feature, epic)
```

### bd stale

최근에 최근 업데이트되지 않은 이슈를 표시하여 주의가 필요할 수 있는 이슈를 보여줍니다.
이는 다음을 식별하는 데 도움을 줍니다.
- 최근 활동이 없는 진행 중 이슈(방치되어 중단됐을 수 있음)
- 잊혀진 오픈 이슈
- 오래되었거나 더 이상 관련이 없는 이슈

```
bd stale [flags]
```

**플래그:**

```
  -d, --days int        이 일수 동안 업데이트되지 않은 이슈(기본값 30)
  -n, --limit int       표시할 최대 이슈 수(기본값 50)
  -s, --status string   상태로 필터링(open|in_progress|blocked|deferred)
```

### bd status

이슈 데이터베이스의 상태와 통계를 간략하게 빠르게 표시합니다.

이 명령은 상태별 이슈 개수(open, in_progress,
blocked, closed), 준비 작업, 확장 통계(고정된 이슈,
평균 리드 타임), 그리고 Git 이력을 기반으로 지난 24시간의 최근 활동 요약을 제공합니다.

마치 'git status'가 작업 트리 상태를 보여주는 방식처럼, 'bd status'는
여러 번의 쿼리가 필요 없는 이슈 데이터베이스의 빠른 개요를 제공합니다.

사용 사례:
  - 신속한 프로젝트 상태 점검
  - 신규 기여자 온보딩
  - 셸 프롬프트 또는 CI/CD와 통합
  - 일일 스탠드업 참고자료

예제:
  bd status                    # 활동 요약 표시
  bd status --no-activity      # Git 활동 건너뛰기 (더 빠름)
  bd status --json             # JSON 형식 출력
  bd status --assigned         # 현재 사용자에게 할당된 이슈 표시
  bd stats                     # bd status의 별칭

```
bd status [flags]
```

**별칭:** stats

**플래그:**

```
      --all           모든 이슈 표시(기본 동작)
      --assigned      현재 사용자에게 할당된 이슈 표시
      --no-activity   git 활동 추적 건너뛰기(더 빠름)
```

### bd statuses

모든 유효한 이슈 상태와 해당 카테고리를 나열합니다.

기본 제공 상태(open, in_progress, blocked 등)는 항상 유효합니다.
추가 상태는 status.custom를 통해 구성할 수 있습니다:

  bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen"

카테고리는 동작을 제어합니다:
  active  — 'bd ready'와 기본 'bd list'에 표시됩니다
  wip     — 'bd ready'에서 제외되며, 기본 'bd list'에는 표시됩니다
  done    — 'bd ready'와 기본 'bd list'에서 제외됩니다
  frozen  — 'bd ready'와 기본 'bd list'에서 제외됩니다

카테고리가 없는 상태(레거시 형식)는 유효하지만 'bd ready'에서는 제외됩니다.

예시:
  bd statuses            # 아이콘과 카테고리를 사용해 모든 상태를 나열
  bd statuses --json     # JSON으로 출력


```
bd statuses
```

### bd types

bd create --type에서 사용할 수 있는 모든 유효한 이슈 유형을 나열합니다.

핵심 작업 유형(bug, task, feature, chore, epic, decision)은 항상 유효합니다.
추가 유형은 .beads/config.yaml의 types.custom을 통해 구성해야 합니다.

예시:
  bd types              # 설명과 함께 모든 타입을 나열
  bd types --json       # JSON 형식으로 출력


```
bd types
```

## 의존성 및 구조:

### bd dep

이슈 간의 종속성을 관리합니다.

이슈 ID와 --blocks 플래그로 호출되면 블로킹 의존성을 생성합니다:
  bd dep &lt;blocker-id&gt; --blocks &lt;blocked-id&gt;

이것은 다음과 동일합니다:
  bd dep add &lt;blocked-id&gt; &lt;blocker-id&gt;

예제:
  bd dep bd-xyz --blocks bd-abc    # bd-xyz가 bd-abc를 차단
  bd dep add bd-abc bd-xyz         # 위와 동일함 (bd-abc는 bd-xyz에 의존)

```
bd dep [issue-id] [flags]
```

**플래그:**

```
  -b, --blocks string    이 이슈가 차단하는 이슈 ID(단축 명령: bd dep add <blocked> <blocker>)
      --no-cycle-check   속도를 위해 간선별 순환 검사 건너뛰기(일괄 연결), 일괄 --file 추가는 커밋 전 마지막 전체 그래프 검사를 계속 실행
```

#### bd dep add

두 개의 이슈 간에 종속성을 추가합니다.

depends-on-id는 다음과 같이 제공할 수 있습니다:
  - 위치 인수: bd dep add issue-123 issue-456
  - 플래그: bd dep add issue-123 --blocked-by issue-456
  - 플래그: bd dep add issue-123 --depends-on issue-456

--blocked-by 및 --depends-on 플래그는 별칭이며 둘 다 "issue-123
는 지정된 이슈에 의해 의존(차단됨)합니다."

depends-on-id는 다음과 같을 수 있습니다:
  - 로컬 이슈 ID(예: bd-xyz)
  - 외부 참조: external:&lt;project&gt;:&lt;capability&gt;

대량 와이어링의 경우 --file을 사용해 줄바꿈 구분 JSON을 전달합니다. 각 줄은
\"from\"과 \"to\" 필드가 있는 객체여야 하며 \"type\"을 포함할 수 있습니다. 별칭
\"issue_id\" 및 \"depends_on_id\"도 허용됩니다. stdin을 읽으려면 --file - 를 사용하세요.

외부 참조는 그대로 저장되며 쿼리 시점에 external_projects config를 사용해
해결됩니다. 이들은 대상 프로젝트에서 기능이 "shipped"될 때까지
이슈를 차단합니다.

예시:
  bd dep add bd-42 bd-41                              # 위치 인수
  bd dep add bd-42 --blocked-by bd-41                 # 플래그 구문 (동일한 효과)
  bd dep add bd-42 --depends-on bd-41                 # 별칭 (동일한 효과)
  bd dep add gt-xyz external:beads:mol-run-assignee   # 프로젝트 간 종속성
  bd dep add bd-42 bd-41 --no-cycle-check             # 순환 검사 건너뛰기 (일괄 와이어링)
  bd dep add --file deps.jsonl                        # 대량 JSONL: &#123;"from":"bd-42","to":"bd-41"&#125;

```
bd dep add [issue-id] [depends-on-id] [flags]
```

**플래그:**

```
      --blocked-by string   첫 이슈를 차단하는 이슈 ID(위치 인수 대신 사용)
      --depends-on string   첫 이슈가 의존하는 이슈 ID(--blocked-by의 별칭)
      --file string         JSONL 파일에서 의존성 간선 읽기, stdin은 '-'
      --no-cycle-check      속도를 위해 간선별 순환 검사 건너뛰기(일괄 연결), 일괄 --file 추가는 커밋 전 마지막 전체 그래프 검사를 계속 실행
  -t, --type string         의존성 유형(blocks|tracks|related|parent-child|discovered-from|until|caused-by|validates|relates-to|supersedes)(기본값 "blocks")
```

#### bd dep cycles

의존성 순환 감지

```
bd dep cycles
```

#### bd dep list

선택적 유형 필터링을 사용하여 하나 이상의 이슈의 의존성 또는 의존 항목을 나열합니다.

기본적으로 종속성(어떤 이슈가 이에 의존하는지)을 표시합니다. 제어하려면 --direction을 사용하세요:
  - down: 종속성(이 이슈가 의존하는 항목)을 표시합니다 - 기본값
  - up:   종속 항목(이 이슈에 의존하는 항목)을 표시합니다

여러 ID를 배치 dep 목록 조회에 제공할 수 있습니다. --json을 사용하면 출력
은(는) 요청된 모든 이슈에 걸친 의존성 레코드의 평면 배열입니다.

의존성 유형(예: tracks, blocks, parent-child)로 필터링하려면 --type을 사용하세요.

예제:
  bd dep list gt-abc                     # gt-abc가 의존하는 항목 표시
  bd dep list gt-abc gt-def              # 배치: 두 이슈 모두의 종속성 표시
  bd dep list gt-abc --direction=up      # gt-abc에 의존하는 항목 표시
  bd dep list gt-abc --direction=up -t tracks  # gt-abc를 추적하는 항목 표시(컨보이 추적)

```
bd dep list [issue-id...] [flags]
```

**플래그:**

```
      --direction string   방향: 'down'(의존 항목), 'up'(종속 항목)(기본값 "down")
  -t, --type string        의존성 유형으로 필터링(예: tracks, blocks, parent-child)
```

#### bd dep relate

두 이슈 사이에 느슨한 'see also' 관계를 만듭니다.

relates_to 링크는 양방향입니다 - 두 이슈가 서로 참조합니다.
이는 차단이나 계층 관계 없이 지식 그래프 연결을 가능하게 합니다.

예제:
  bd relate bd-abc bd-xyz    # 두 개의 관련 이슈를 연결합니다
  bd relate bd-123 bd-456    # see-also 연결을 생성합니다

```
bd dep relate <id1> <id2>
```

#### bd dep remove

종속성 제거

```
bd dep remove [issue-id] [depends-on-id]
```

**별칭:** rm

#### bd dep tree

주어진 이슈를 루트로 하는 의존성 트리를 표시합니다.

기본적으로 종속 항목을 표시합니다(이 이슈를 차단하는 항목). --direction을 사용해 제어하세요:
  - down: 종속 항목 표시(이 이슈를 차단하는 항목) - 기본값
  - up:   종속 항목(이 이슈가 차단하는 항목) 표시
  - both: 양방향 모두에서 전체 그래프 표시

예시:
  bd dep tree gt-0iqq                    # gt-0iqq을(를) 차단하는 항목을 표시
  bd dep tree gt-0iqq --direction=up     # gt-0iqq이(가) 차단하는 항목을 표시
  bd dep tree gt-0iqq --status=open      # 열린 이슈만 표시
  bd dep tree gt-0iqq --depth=3          # 깊이를 3단계로 제한

```
bd dep tree [issue-id] [flags]
```

**플래그:**

```
      --direction string   트리 방향: 'down'(의존 항목), 'up'(종속 항목) 또는 'both'
      --format string      출력 형식: Mermaid.js 순서도용 'mermaid'
  -d, --max-depth int      표시할 최대 트리 깊이(안전 제한)(기본값 50)
      --reverse            종속 트리 표시(사용 중단: --direction=up 사용)
      --show-all-paths     노드로 향하는 모든 경로 표시(다이아몬드 의존성 중복 제거 안 함)
      --status string      이 상태의 이슈만 표시하도록 필터링(open, in_progress, blocked, deferred, closed)
```

#### bd dep unrelate

두 이슈 간의 relates_to 관계를 제거합니다.

링크를 양방향으로 제거합니다.

예시:
  bd unrelate bd-abc bd-xyz

```
bd dep unrelate <id1> <id2>
```

### bd duplicate

이슈를 표준 이슈의 중복으로 표시합니다.

중복 이슈는 canonical을 참조하는 방식으로 자동으로 종료됩니다.
이는 유사한 보고서가 많은 대규모 이슈 데이터베이스에서 필수적입니다.

예시:
  bd duplicate bd-abc --of bd-xyz    # bd-abc을 bd-xyz의 중복으로 표시합니다

```
bd duplicate <id> --of <canonical> [flags]
```

**플래그:**

```
      --of string   기준 이슈 ID(필수)
```

### bd duplicates

동일한 내용(제목, 설명, 디자인, 수락 기준)인 이슈를 찾습니다.
내용 해시로 이슈를 그룹화하고 제안된 병합 대상으로 중복 항목을 보고합니다.
병합 대상은 다음과 같이 선택됩니다:
1. 참조 횟수(가장 많이 참조된 이슈가 우선)
2. 참조 횟수가 같으면 사전식으로 가장 작은 ID
상태가 일치하는 이슈만 그룹화합니다(open는 open끼리, closed는 closed끼리).
예시:
  bd duplicates                    # 모든 중복 그룹 표시
  bd duplicates --auto-merge       # 모든 중복 항목을 자동으로 병합
  bd duplicates --dry-run          # 병합될 항목 미리보기

```
bd duplicates [flags]
```

**플래그:**

```
      --auto-merge   모든 중복 자동 병합
      --dry-run      변경하지 않고 병합될 항목 표시
```

### bd epic

에픽 관리 명령어

```
bd epic
```

#### bd epic close-eligible

모든 하위 항목이 완료된 에픽 닫기

```
bd epic close-eligible [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 닫힐 항목 미리 보기
```

#### bd epic status

에픽 완료 상태 표시

```
bd epic status [flags]
```

**플래그:**

```
      --eligible-only   닫을 수 있는 epic만 표시
```

### bd graph

이슈의 종속성 그래프를 시각화하여 표시합니다.

에픽의 경우, 모든 하위 항목과 해당 종속성을 표시합니다.
일반 이슈의 경우, 이슈와 그 직접적인 종속성을 표시합니다.

--all을 사용하면 연결된 구성 요소별로 그룹화된 모든 열린 이슈를 표시합니다.

표시 형식:
  (기본값)         열이 있는 DAG와 박스 그리기 간선(터미널 기본)
  --box            계층을 보여 주는 ASCII 상자, 더 상세하게
  --compact        트리 형식, 이슈당 한 줄, 더 읽기 쉬움
  --dot            Graphviz DOT 형식(dot -Tsvg &gt; graph.svg로 파이프)
  --html           D3.js 시각화를 포함한 자체 포함형 대화형 HTML

그래프는 실행 순서를 보여줍니다:
- 레이어 0 / 왼쪽 끝 = 의존성이 없음(즉시 시작 가능)
- 상위 레이어는 하위 레이어에 의존함
- 동일한 레이어의 노드는 병렬로 실행될 수 있음

상태 아이콘: ○ 열림  ◐ 진행 중  ● 차단됨  ✓ 완료  ❄ 보류됨

예시:
  bd graph issue-id              # 터미널 DAG 시각화 (기본값)
  bd graph --box issue-id        # 레이어 그룹화가 적용된 ASCII 박스
  bd graph --dot issue-id | dot -Tsvg &gt; graph.svg  # Graphviz를 통해 SVG 생성
  bd graph --dot issue-id | dot -Tpng &gt; graph.png  # Graphviz를 통해 PNG 생성
  bd graph --html issue-id &gt; graph.html  # 대화형 브라우저 뷰
  bd graph --all --html &gt; all.html       # 모든 이슈, 대화형 뷰

```
bd graph [issue-id] [flags]
```

**플래그:**

```
      --all       모든 열린 이슈의 그래프 표시
      --box       계층을 표시하는 ASCII 상자
      --compact   트리 형식, 이슈당 한 줄, 더 빠르게 훑어볼 수 있음
      --dot       Graphviz DOT 형식 출력(다음으로 파이프: dot -Tsvg > graph.svg)
      --html      독립형 대화형 HTML 출력(파일로 리디렉션)
```

#### bd graph check

의존성 그래프에서 순환, 고아 항목 및 기타 무결성 문제를 확인합니다.

그래프가 깨끗하면 종료 코드 0을 반환하고, 문제가 발견되면 종료 코드 1을 반환합니다.

```
bd graph check
```

### bd supersede

더 최신 버전으로 인해 해당 이슈를 폐기 처리합니다.

대체된 이슈는 대체 항목에 대한 참조와 함께 자동으로 닫힙니다.
설계 문서, 사양서, 그리고 계속 진화하는 산출물에 유용합니다.

예시:
  bd supersede bd-old --with bd-new    # bd-old를 bd-new로 대체됨으로 표시합니다

```
bd supersede <id> --with <new> [flags]
```

**플래그:**

```
      --with string   대체 이슈 ID(필수)
```

### bd swarm

에픽에서 병렬 작업을 조정하기 위한 Swarm 관리 명령어들입니다.

스웜은 에픽과 그 하위 항목으로 정의되는 구조화된 작업 단위이며,
의존성은 작업의 DAG(방향성 비순환 그래프)로 형성됩니다.

```
bd swarm
```

#### bd swarm create

에픽에서 병렬 작업을 조율하기 위해 swarm molecule을 생성합니다.

스웜 분자:
- 조율하는 에픽에 연결됩니다
- 탐색을 위해 mol_type=swarm을 사용합니다
- 코디네이터(선택 사항)를 지정합니다
- 어떤 코디네이터 에이전트든 작업할 수 있습니다

만약 단일 이슈(에픽이 아닌)가 주어지면 자동으로 래핑됩니다:
- 해당 이슈를 유일한 하위 항목으로 하는 에픽을 생성합니다
- 그런 다음 해당 에픽의 swarm molecule을 생성합니다

예시:
  bd swarm create bd-epic-123                          # 에픽용 스웜 생성
  bd swarm create bd-epic-123 --coordinator=observer/   # 특정 코디네이터로 생성
  bd swarm create bd-task-456                          # 단일 이슈 자동 래핑

```
bd swarm create [epic-id] [flags]
```

**플래그:**

```
      --coordinator string   coordinator 주소(예: my-project/witness)
      --force                이미 있어도 새 swarm 생성
```

#### bd swarm list

모든 swarm 분자의 상태를 나열합니다.

각 swarm molecule을 다음 항목과 함께 표시합니다:
- 진행 상황(완료/총 이슈)
- 활성 작업자
- Epic ID 및 제목

예시:
  bd swarm list         # 모든 swarm 목록 보기
  bd swarm list --json  # 기계 판독 가능한 출력

```
bd swarm list
```

#### bd swarm status

beads로 계산된 swarm의 현재 상태를 표시합니다.

다음 중 하나를 받습니다:
- 에픽 ID(해당 에픽의 하위 항목 상태를 표시)
- 스웜 분자 ID(링크를 따라 에픽을 찾음)

상태별로 이슈를 그룹화하여 표시:
- 완료됨: 종료된 이슈
- 활성: 현재 in_progress인 이슈(담당자 있음)
- 준비됨: 모든 종속성이 충족된 열린 이슈
- 차단됨: 종속성 대기 중인 열린 이슈

상태는 별도로 저장되지 않고 beads에서 계산됩니다.
beads가 변경되면 상태도 변경됩니다.

예시:
  bd swarm status gt-epic-123       # 에픽별 swarm 상태 표시
  bd swarm status gt-swarm-456      # swarm molecule을 통해 상태 표시
  bd swarm status gt-epic-123 --json  # 기계 판독용 출력

```
bd swarm status [epic-or-swarm-id]
```

#### bd swarm validate

에픽의 구조를 검증하여 스웜 실행 준비가 되었는지 확인합니다.

다음 항목을 확인합니다:
- 올바른 의존성 방향(요구사항 기반, 시간 기반 아님)
- 고아 이슈(의존 대상이 없는 루트)
- 누락된 의존성(무언가에 의존해야 하는 리프)
- 순환(해결할 수 없음)
- 분리된 하위 그래프

보고서:
- 준비 프론트(병렬 작업의 웨이브)
- 예상 작업자 세션
- 최대 병렬성
- 잠재적 문제에 대한 경고

예시:
  bd swarm validate gt-epic-123           # 에픽 구조를 검증
  bd swarm validate gt-epic-123 --verbose # 자세한 이슈 그래프를 포함

```
bd swarm validate [epic-id] [flags]
```

**플래그:**

```
      --verbose   출력에 상세 이슈 그래프 포함
```

## 동기화 및 데이터:

### bd backup

오프 머신 복구를 위해 beads 데이터베이스를 백업하세요.

이는 Dolt-native 데이터베이스 백업입니다. 데이터베이스 상태를 보존합니다,
테이블, 브랜치, 커밋 기록, 작업 세트 데이터를 포함합니다. 이는
'bd export'와는 다릅니다. 'bd export'는 마이그레이션 및 상호운용성을 위해 이슈 레코드를
JSONL로 기록합니다.

명령어:
  bd backup init &lt;path&gt;    백업 대상(파일 시스템 또는 DoltHub) 설정
  bd backup sync           설정된 백업 대상으로 푸시
  bd backup restore [path] 백업 디렉터리에서 복원
  bd backup remove         백업 대상 제거
  bd backup status         백업 상태 표시

DoltHub는 클라우드 백업에 권장됩니다:
  bd backup init https://doltremoteapi.dolthub.com/&lt;user&gt;/&lt;repo&gt;
  인증을 위해 DOLT_REMOTE_USER와 DOLT_REMOTE_PASSWORD를 설정합니다.

```
bd backup
```

#### bd backup init

파일 시스템 경로나 URL을 백업 대상으로 구성합니다.

이 경로는 로컬 디렉터리(외장 드라이브, NAS, Dropbox 폴더) 또는
DoltHub 원격 URL일 수 있습니다. 대상이 이전에 구성되어 있었다면
새 경로로 업데이트됩니다.

파일 시스템 예시:
  bd backup add /mnt/usb/beads-backup
  bd backup add ~/Dropbox/beads-backup

클라우드 백업에 권장되는 DoltHub:
  bd backup add https://doltremoteapi.dolthub.com/myuser/beads-backup

추가한 후 데이터 푸시를 위해 'bd backup sync'를 실행하세요.

```
bd backup init <path>
```

**별칭:** add

#### bd backup remove

설정된 백업 대상을 제거합니다.

이는 Dolt에서 백업 원격을 등록 해제하고 로컬
백업 구성을 제거합니다. 대상의 백업 데이터는 삭제되지 않습니다.

```
bd backup remove
```

**별칭:** rm

#### bd backup restore

Dolt-native 백업에서 beads 데이터베이스를 복원합니다.

기본적으로 .beads/backup/ (또는 설정된 백업 디렉터리)에서 읽습니다.
선택적으로 Dolt 백업이 들어 있는 디렉터리 경로를 지정할 수 있습니다.

이는 'bd backup sync' 또는 동등한 Dolt 백업으로 생성된 전체 데이터베이스 백업을 복원합니다.
'bd export'로 생성된 JSONL 파일은 이슈 내보내기이며, 이 명령의 복원 대상으로는 사용할 수 없습니다.
이 명령의 복원 대상이 아닙니다.

기존 데이터베이스를 백업 내용으로 덮어쓰려면 --force를 사용하세요.

데이터베이스는 이미 초기화되어 있어야 합니다(필요한 경우 먼저 'bd init'를 실행하세요).
한 번에 초기화하고 복원하려면 다음을 사용하세요: bd init &amp;&amp; bd backup restore

```
bd backup restore [path] [flags]
```

**플래그:**

```
      --force   기존 데이터베이스를 백업 콘텐츠로 덮어쓰기
```

#### bd backup status

마지막 백업 상태 표시

```
bd backup status
```

#### bd backup sync

현재 beads 데이터베이스를 구성된 Dolt 백업 대상 위치로 동기화합니다.

이 명령은 전체 데이터베이스 상태(모든 브랜치, 전체 히스토리)를
'bd backup init'으로 구성된 백업 위치로 푸시합니다.

이 백업은 원자적입니다 — 동기화가 실패하면 이전 백업 상태가 유지됩니다.

대상 위치를 구성하려면 먼저 'bd backup init &lt;path&gt;'를 실행하세요.

```
bd backup sync
```

### bd branch

모든 브랜치를 나열하거나 새 브랜치를 생성합니다.

이 명령은 Dolt 저장소 백엔드를 필요로 합니다. 인수가 없으면,
모든 브랜치를 나열합니다. 인수가 있으면 새 브랜치를 생성합니다.

예시:
  bd branch                    # 모든 브랜치 목록 표시
  bd branch feature-xyz        # feature-xyz라는 새 브랜치를 생성함

```
bd branch [name]
```

### bd export

모든 이슈를 JSONL(줄 바꿈으로 구분된 JSON) 형식으로 내보냅니다.

각 줄은 하나의 이슈를 나타내는 완전한 JSON 객체로, 해당 이슈의
라벨, 종속성 및 코멘트를 포함합니다.

이 명령은 이슈 내보내기, 마이그레이션 및 상호 운용성을 위한 것입니다. issues 테이블에서
레코드를 내보냅니다; 이는 전체 데이터베이스 백업이 아니며 Dolt 브랜치, 커밋 이력, working-set 상태 또는
비이슈 테이블을 캡처하지 않습니다.
전체 백업/복원 플로우를 지원하려면 'bd backup init', 'bd backup sync',
및 'bd backup restore'를 사용하세요.

기본적으로 일반 이슈만 내보냅니다(에이전트, 역할, 메시지 같은 인프라 Beads 제외).
모든 항목을 포함하려면 --all을 사용하세요.

메모리('bd remember'에서 유래한 항목)는 기본적으로 제외됩니다.
이는 민감한 에이전트 컨텍스트를 포함할 수 있기 때문입니다. --include-memories 또는 --all을
포함하려면 사용하세요.

예시:
  bd export                              # 이슈를 stdout으로 내보내기
  bd export -o issues.jsonl              # 이슈를 파일로 내보내기
  bd export --include-memories           # 이슈 + 메모리 내보내기
  bd export --all -o full.jsonl          # 인프라 + 템플릿 + 게이트 + 메모리 포함
  bd export --scrub -o clean.jsonl       # 테스트/오염 레코드 제외

```
bd export [flags]
```

**플래그:**

```
      --all                모든 레코드 포함(인프라, 템플릿, gate, 메모리)
      --include-infra      인프라 bead 포함(agent, role, message)
      --include-memories   내보내기에 영구 메모리('bd remember'에서 생성) 포함
  -o, --output string      출력 파일 경로(기본값: stdout)
      --scrub              테스트/오염 레코드 제외
```

### bd federation

Federation 명령은 CGO와 Dolt 스토리지 백엔드가 필요합니다.

이 바이너리는 CGO 지원 없이 빌드되었습니다. 연합 기능을 사용하려면:
  1. GitHub 릴리스에서 미리 빌드된 바이너리를 사용하거나,
  2. CGO가 활성화된 상태로 소스에서 빌드하십시오

Federation은 여러 워크스페이스에서 동기화된 이슈 추적을 가능하게 하며,
각 워크스페이스는 자체 Dolt 데이터베이스를 유지하면서 remotes를 통해 업데이트를 공유합니다.

```
bd federation
```

### bd import

JSONL 파일(줄 바꿈으로 구분된 JSON)에서 이슈를 데이터베이스로 가져옵니다.

파일이 지정되지 않으면 .beads/ 아래의 구성된 import.path에서 가져옵니다
(기본값: issues.jsonl). 표준 입력에서 읽으려면 "-"를 사용합니다. 이는 'bd export'의
증분 대응으로, 새 이슈가 생성되고 기존 이슈가 업데이트됩니다 (upsert
의미론).

메모리 레코드("_type":"memory"가 있는 줄)는 자동으로 감지되어
지속형 메모리로 가져오며( 'bd remember'와 동일). 이것으로 인해
'bd export | bd import'는 이슈와 메모리 모두에 대해 완전한 라운드트립을 수행합니다.

각 JSONL 줄은 하나의 이슈와 매핑되어야 합니다. 임포터는
'bd export'가 내보내는 모든 필드를 수락합니다 — 표준 스키마는 'bd export' 출력을 확인하세요. 오직
"title"은(는) 필수이며, 나머지는 모두 선택 사항입니다.

일반 필드:
  title                  필수. 짧은 요약.
  description            긴 형식 본문.
  design, notes,         추가 내용 섹션.
    acceptance_criteria
  issue_type             bug | feature | task | epic | chore | ...
  priority               0-4 (0 = 치명적). 0은 보존됩니다 (no omitempty).
  status                 open | in_progress | blocked | closed | ...
                         (상태가 "tombstone"인 행은 건너뜁니다)
  assignee, owner,       소유권 메타데이터.
    created_by
  labels                 문자열 배열.
  dependencies           &#123;issue_id, depends_on_id, type, ...&#125; 배열.
  comments               댓글 객체 배열.
  external_ref,          다른 시스템 식별자(예: "gh-9").
    source_system
  due_at, defer_until    일정 조정을 위한 RFC3339 타임스탬프.
  metadata               임의의 JSON 객체가 원문 그대로 보존됩니다.

타임스탬프(created_at, updated_at, started_at, closed_at)는 JSONL에 존재할 때 보존되며
그렇지 않으면 importer에 의해 채워집니다. 기존
레거시 "wisp" 불리언은 "ephemeral"의 별칭으로 허용됩니다.

기본적으로 행은 기존 로컬 이슈가 있을 때
updated_at가 더 엄격히 최신일 때만 덮어씁니다. 오래된 행은
(stale_skipped_ids)로 건너뛰고, updated_at이 동일한 행은 모든 로컬
컬럼을 유지합니다 — updated_at은 초 단위 정밀도를 가지므로 타임스탬프 동점은
같은 초 내의 서로 다른 두 업데이트가 될 수 있고, 로컬 행이 동점에서 승리하며
(tie_kept_local_ids로 보고됨; 행의 labels/comments/dependencies는
여전히 병합됩니다). 또한 이 가드는 upsert 자체 내부에서도 적용되어, 가져오기가
실행되는 동안 들어온 로컬 업데이트는 덮어쓰기보다 보존됩니다.
가져오기가 실제로 덮어쓴 기존 이슈는
필드 단위 요약(updated_issues)으로 나열되어, 가져오기에 의해 변경된
로컬 상태가 확인됩니다. 오래된 스냅샷을 의도적으로 복원하려면
--allow-stale를 지정하면, 최신 로컬 상태를
덮어쓰더라도 모든 행을 가져옵니다.

예시:
  bd import                        # 구성된 import.path에서 가져오기
  bd import backup.jsonl           # 특정 파일에서 가져오기
  bd import -i backup.jsonl        # 특정 파일의 레거시 별칭
  bd import -                      # stdin에서 JSONL 읽기
  cat issues.jsonl | bd import -   # 다른 도구에서 JSONL 파이프
  bd import --dry-run              # 가져올 항목 보여주기
  bd import --dedup                # 제목이 중복된 이슈 건너뛰기
  bd import --allow-stale old.jsonl # 이전 스냅샷 복원 (최신 로컬 행을 덮어씀)
  bd import --json                 # 생성 및 건너뜀 ID가 포함된 구조화된 출력

```
bd import [file|-] [flags]
```

**플래그:**

```
      --allow-stale    로컬 이슈보다 오래된 행도 가져오기(오래된 스냅샷 복원에 필요)
      --dedup          제목이 기존 열린 이슈와 일치하는 줄 건너뛰기
      --dry-run        실제로 가져오지 않고 가져올 항목 표시
  -i, --input string   특정 파일에서 JSONL 읽기
```

### bd restore

축소된 이슈의 압축 전 내용을 복원합니다.

이슈가 압축되면 description/design/notes/acceptance criteria가
요약되고 원본은 압축 스냅샷에 보관됩니다. 이
명령은 원본 콘텐츠를 복구합니다.

기본적으로 읽기 전용입니다: 데이터베이스를 수정하지 않고 보관된 내용을 표시합니다
원본 내용을 이슈에 다시 쓰려면 --apply를 전달하세요
그리고 compaction level을 한 단계 낮춥니다

보관된 스냅샷이 없으면(예: 이 이슈가 스냅샷 보관 전에 오래된 bd에 의해 압축된 경우),
restore는 Dolt 버전 히스토리에서 최선의 노력으로 재구성하는 방식으로 대체되며,
이는 적용할 수 없고 표시만 가능합니다.

```
bd restore <issue-id> [flags]
```

**플래그:**

```
      --apply   복원된 콘텐츠를 이슈에 다시 쓰기(기본값: 표시만)
      --json    복원 결과를 JSON 형식으로 출력
```

### bd vc

beads 데이터베이스를 위한 버전 제어 작업입니다.

이 명령어는 브랜치, 병합 및
이슈 데이터에 대한 이력 보기를 포함한 git와 유사한 버전 제어를 제공합니다.

참고: 'bd history', 'bd diff', 'bd branch'도 빠른 액세스에 사용할 수 있습니다.
이 하위 명령은 병합 및 커밋과 같은 추가 작업을 제공합니다.

```
bd vc
```

#### bd vc commit

현재 변경된 모든 내용을 포함하는 새로운 Dolt 커밋을 만듭니다.

예시:
  bd vc commit -m "새로운 기능 이슈 추가"
  bd vc commit --message "여러 이슈의 우선순위를 수정함"
  echo "여러 줄 메시지" | bd vc commit --stdin

```
bd vc commit [flags]
```

**플래그:**

```
  -m, --message string   커밋 메시지
      --stdin            stdin에서 커밋 메시지 읽기
```

#### bd vc merge

지정한 브랜치를 현재 브랜치로 병합합니다.

병합 충돌이 있으면 보고됩니다. --strategy로 충돌을
해결할 수 있습니다.

예시:
  bd vc merge feature-xyz                    # feature-xyz를 현재 브랜치에 병합
  bd vc merge feature-xyz --strategy ours    # 병합, 충돌 시 내 변경 사항을 우선합니다
  bd vc merge feature-xyz --strategy theirs  # 병합, 충돌 시 상대 변경 사항을 우선합니다

```
bd vc merge <branch> [flags]
```

**플래그:**

```
      --strategy string   충돌 해결 전략: 'ours' 또는 'theirs'
```

#### bd vc status

현재 브랜치, 커밋 해시, 그리고 커밋되지 않은 변경 사항을 표시합니다.

예제:
  bd vc status

```
bd vc status
```

## 설정 및 구성:

### bd bootstrap

Bootstrap는 기존 데이터를 파괴하지 않고 beads 데이터베이스를 설정합니다.
'bd init --force'와 달리 bootstrap는 기존 이슈를 절대 삭제하지 않습니다.

Bootstrap은 적절한 동작을 자동으로 감지합니다:
  • sync.remote가 구성된 경우: 원격에서 클론합니다
  • git origin에 Dolt 데이터(refs/dolt/data)가 있으면: git에서 클론하고 향후 push/pull을 위해 origin을 연결합니다
  • .beads/backup/*.jsonl이 존재하면: 백업에서 복원합니다
  • .beads/issues.jsonl이 존재하면: git 추적 JSONL에서 가져옵니다
  • 데이터베이스가 존재하지 않으면: 새 데이터베이스를 생성합니다
  • 데이터베이스가 이미 존재하면: 유효성을 검사하고 상태를 보고합니다

이 명령은 다음 용도로 권장됩니다:
  • 새 클론에서 beads 설정
  • 새 컴퓨터로 이동한 뒤 복구
  • 손상된 데이터베이스 구성 복구

비대화형 모드(--non-interactive, --yes/-y, 또는 BD_NON_INTERACTIVE=1):
  부트스트랩 계획을 실행하기 전에 확인 프롬프트를 건너뜁니다.
  또한 stdin이 터미널이 아니거나 CI=true가 설정되어 있으면 자동으로 감지됩니다.

예시:
  bd bootstrap              # 자동으로 감지하여 설정
  bd bootstrap --dry-run    # 수행될 작업을 표시
  bd bootstrap --json       # 계획을 JSON으로 출력
  bd bootstrap --yes        # 확인 프롬프트를 건너뜀


```
bd bootstrap [flags]
```

**플래그:**

```
      --dry-run           실행하지 않고 수행될 작업 표시
      --non-interactive   --yes의 별칭
  -y, --yes               확인 프롬프트 건너뛰기(CI/자동화용)
```

### bd config

외부 통합 및 기본 설정에 대한 구성 설정을 관리합니다.

구성은 프로젝트별로 beads 데이터베이스에 저장되며 버전 제어에 친화적입니다.

공통 네임스페이스:
  - export.*          자동 내보내기 설정(config.yaml에 저장됨)
  - import.*          JSONL 가져오기 설정(config.yaml에 저장됨)
  - jira.*            Jira 통합 설정
  - linear.*          Linear 통합 설정
  - github.*          GitHub 통합 설정
  - custom.*          사용자 정의 통합 설정
  - status.*          이슈 상태 설정
  - doctor.suppress.* 특정 bd doctor 경고를 억제함 (GH#1095)

자동 내보내기 (config.yaml):
  쓰기 명령 후 .beads/issues.jsonl에 JSONL을 선택적으로 내보냅니다(속도 제한 적용).
  뷰어(bv), 인터체인지, 이슈 수준 마이그레이션에 유용하며, 백업은 아닙니다.
  이는 크로스 머신 동기화가 아닙니다; Dolt 원격과 함께 bd dolt push/pull을 사용하세요.
  기본적으로 비활성화됩니다. 최신 JSONL이 필요한 통합에서만 활성화하세요.
  자동 스테이징은 별도로 동작하며 기본적으로 비활성화됩니다.

  키:
    export.auto       자동 내보내기 활성화/비활성화 (기본값: false)
    export.path       .beads/에 상대적인 출력 파일명 (기본값: issues.jsonl)
    export.interval   내보내기 간 최소 시간 (기본값: 60s)
    export.git-add    내보내기 파일을 자동으로 스테이징 (기본값: false)

자동 가져오기 (config.yaml):
  기본적으로 JSONL 가져오기 경로가 지정되면 .beads/issues.jsonl을 읽습니다.
  가져오기가 프로젝트 .beads/
  디렉터리 내에 유지되고 여러 머신에서 이동 가능하도록 상대 파일 이름/경로를 사용하세요.

  키:
    import.path       .beads/ 기준 상대 경로의 입력 파일 이름 (기본값: issues.jsonl)

사용자 지정 상태:
  다단계 파이프라인에 대한 사용자 지정 상태를 status.custom 설정 키를 사용해 정의할 수 있습니다.
  상태는 쉼표로 구분되어야 합니다.

  예시:
    bd config set status.custom "awaiting_review,awaiting_testing,awaiting_docs"

  이는 이슈에서 기본 상태(open, in_progress, blocked, deferred, closed)에 더해
  'awaiting_review'와 같은 상태를 사용할 수 있도록 합니다.

Doctor 경고 억제:
  특정 bd doctor 경고를 체크 이름 슬러그로 억제하려면:
    bd config set doctor.suppress.pending-migrations true
    bd config set doctor.suppress.git-hooks true
  체크 이름은 슬러그로 변환됩니다: "Git Hooks" → "git-hooks".
  경고만 억제됩니다(오류와 통과한 검사 항목은 항상 표시됩니다).
  억제를 해제하려면: bd config unset doctor.suppress.&lt;slug&gt;

예시:
  bd config set export.auto true                       # 뷰어 통합을 위해 자동 내보내기 활성화
  bd config set export.path "beads.jsonl"              # 사용자 지정 내보내기 파일 이름
  bd config set import.path "beads.jsonl"              # 사용자 지정 가져오기 파일 이름
  bd config set export.git-add true                    # 내보내기 파일도 스테이징
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set status.custom "awaiting_review,awaiting_testing"
  bd config set doctor.suppress.pending-migrations true
  bd config set dolt.debug true                        # Dolt sql-server 디버그 모드 활성화 (loglevel=debug, --prof cpu)
  bd config set dolt.local-only true                   # bd init 실행 중 Dolt sync 원격 연결 건너뛰기
  bd config get export.auto
  bd config list
  bd config unset jira.url

```
bd config
```

#### bd config apply

실제 시스템 상태를 선언된 구성과 일치하도록 조정합니다.

드리프트 감지를 실행한 다음 찾은 불일치 항목을 수정합니다:

  - hooks     누락되었거나 오래되었을 때 git 훅을 다시 설치
  - remote    federation.remote에 맞게 Dolt origin remote를 추가/업데이트
  - server    dolt.shared-server가 활성화된 경우 Dolt 서버를 시작

이 명령은 멱등성(idempotent)으로 여러 번 실행해도 안전합니다. --dry-run을 사용하면
수정을 수행하지 않고 변경될 내용을 미리 확인할 수 있습니다.

예시:
  bd config apply
  bd config apply --dry-run
  bd config apply --json

```
bd config apply [flags]
```

**플래그:**

```
      --dry-run   수정하지 않고 변경될 항목 표시
```

#### bd config drift

선언된 구성과 실제 시스템 상태 간의 드리프트를 감지합니다.

이것은 내 환경이 내 구성과 일치하는지에 대한
질문에 대한 답변을 제공하는 읽기 전용 진단입니다 — 변경은 수행되지 않습니다.

확인:
  - hooks     Git hooks가 설치되어 최신 상태입니다
  - remote    Dolt remote가 federation.remote config와 일치합니다
  - server    서버 상태가 dolt.shared-server config와 일치합니다

종료 코드:
  0  드리프트가 감지되지 않음 (모든 검사 ok/info/skipped)
  1  드리프트가 감지됨 (최소한 한 개의 검사가 상태 "drift"를 가짐)

예시:
  bd config drift
  bd config drift --json

```
bd config drift
```

#### bd config get

구성 값 가져오기

```
bd config get <key>
```

#### bd config list

모든 구성을 나열합니다

```
bd config list
```

#### bd config set

구성 값을 설정합니다

```
bd config set <key> <value> [flags]
```

**플래그:**

```
      --force-git-tracked   git 추적 구성 파일에 시크릿 키 쓰기 허용(주의해서 사용)
```

#### bd config set-many

단일 자동 커밋과 자동 푸시로 여러 구성 값을 한 번에 설정합니다.

각 인수는 key=value 형식이어야 합니다. 모든 값은 쓰기 작업이
발생하기 전에 검증됩니다. 이는 별도의 'bd config set' 호출보다 더 빠르고
더 적은 출력으로 덜 시끄럽습니다. 특히 CI에서는 더욱 그렇습니다.

예제:
  bd config set-many ado.state_map.open=New ado.state_map.closed=Closed
  bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ

```
bd config set-many <key=value>... [flags]
```

**플래그:**

```
      --force-git-tracked   git 추적 구성 파일에 시크릿 키 쓰기 허용(주의해서 사용)
```

#### bd config show

모든 소스의 유효한 구성을 통합된 보기로 표시하고
각 값의 출처를 표시하는 주석을 함께 보여 줍니다.

소스 (Viper 관리 키의 우선순위):
  - env          환경 변수 (BD_* 또는 BEADS_*)
  - config.yaml  프로젝트 구성 파일 (.beads/config.yaml)
  - default      내장 기본값

추가 소스:
  - metadata     .beads/metadata.json의 연결 설정
  - database     Dolt 데이터베이스에 저장된 통합 구성
  - git          Git 구성 (예: beads.role)

예시:
  bd config show
  bd config show --json
  bd config show --source config.yaml

```
bd config show [flags]
```

**플래그:**

```
      --source string   소스로 필터링(예: config.yaml, env, default, metadata, database, git)
```

#### bd config unset

구성 값을 삭제

```
bd config unset <key>
```

#### bd config validate

동기화 관련 구성 설정을 검증합니다.

검사:
  - federation.sovereignty가 유효합니다 (T1, T2, T3, T4 또는 비어 있음)
  - Dolt sync를 위해 federation.remote가 설정되어야 합니다
  - 원격 URL 형식이 유효합니다 (dolthub://, gs://, s3://, az://, file://)
  - routing.mode가 유효합니다 (auto, maintainer, contributor, explicit)

	예시:
	  bd config validate
	  bd config validate --json

```
bd config validate
```

### bd context

저장소 경로를 포함한 유효한 백엔드 ID 정보를 표시합니다,
백엔드 구성과 동기화 설정을 포함합니다.

이 명령은 구성 파일에서 직접 읽기 때문에 데이터베이스가 열릴 필요가 없으며,
열화된 상태에서 진단에 유용하게 사용할 수 있습니다.

예시:
  bd context           # 컨텍스트 정보를 표시합니다
  bd context --json    # JSON 형식으로 출력


```
bd context
```

### bd dolt

Dolt 데이터베이스 설정과 서버 라이프사이클을 구성하고 관리합니다.

Beads는 모든 데이터베이스 작업에 dolt sql-server를 사용합니다. 서버는
필요할 때 자동으로 투명하게 시작됩니다. 명시적 제어 또는 진단을 위해
이러한 명령어를 사용하세요.

서버 라이프사이클:
  bd dolt start        이 프로젝트의 Dolt 서버를 시작합니다
  bd dolt stop         이 프로젝트의 Dolt 서버를 중지합니다
  bd dolt status       Dolt 서버 상태를 표시합니다

구성:
  bd dolt show         현재 Dolt 구성을 연결 테스트와 함께 보여줍니다
  bd dolt set &lt;k&gt; &lt;v&gt;  구성 값을 설정합니다
  bd dolt test         서버 연결을 테스트합니다

버전 제어:
  bd dolt commit       보류 중인 변경 사항 커밋
  bd dolt push         Dolt 원격으로 커밋 푸시
  bd dolt pull         Dolt 원격에서 커밋 가져오기

원격 관리:
  bd dolt remote add &lt;name&gt; &lt;url&gt;   Dolt 원격 추가
  bd dolt remote list                구성된 원격 나열
  bd dolt remote remove &lt;name&gt;       Dolt 원격 제거

'bd dolt set'에 대한 구성 키:
  database  데이터베이스 이름 (기본값: issue 접두사 또는 "beads")
  host      서버 호스트 (기본값: 127.0.0.1)
  port      서버 포트 (자동 감지; bd dolt set port &lt;N&gt;로 재정의)
  user      MySQL 사용자 (기본값: root)
  data-dir  사용자 지정 dolt 데이터 디렉터리 (절대 경로; 기본값: .beads/dolt)

'bd dolt set'의 플래그:
  --update-config  또한 팀 전체 기본값을 위해 config.yaml에도 씁니다

예시:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject
  bd dolt test

```
bd dolt
```

#### bd dolt clean-databases

중단된 테스트 실행과 종료된 에이전트로 인해 공유 Dolt 서버에 누적되는
남은 테스트 및 에이전트 데이터베이스를 식별하고 삭제합니다.

오래된 데이터베이스 접두사: testdb_*, doctest_*, doctortest_*, beads_pt*, beads_vr*, beads_t*

이는 서버 메모리를 낭비하고 동시 부하 상태에서 성능을 저하시킬 수 있습니다.
실제로 삭제하지 않고 삭제될 항목을 확인하려면 --dry-run을 사용하세요.

```
bd dolt clean-databases [flags]
```

**플래그:**

```
      --dry-run   삭제하지 않고 삭제될 항목 표시
```

#### bd dolt commit

작업 세트의 모든 커밋되지 않은 변경사항에서 Dolt 커밋을 생성합니다.

이는 배치 모드의 주요 커밋 지점입니다.
auto-commit가 "batch"로 설정되면
작업 집합의 변경 사항이 여러 bd 명령에 걸쳐 누적되어 여기에서 설명용 요약 메시지와 함께 함께 커밋됩니다.

클린 작업 집합이 필요한 push 작업 전에 유용하며, 또는
auto-commit이 꺼져 있거나 변경 사항이 외부에서 이루어진 경우에도 유용합니다.

추가 옵션(--stdin, 사용자 지정 메시지)을 보려면 다음을 참조하세요: bd vc commit

```
bd dolt commit [flags]
```

**플래그:**

```
  -m, --message string   커밋 메시지(기본값: 자동 생성)
```

#### bd dolt killall

현재 저장소의 Dolt 데이터 디렉터리에 대한 표준 PID 파일로 추적되지 않는
고아 dolt sql-server 프로세스를 찾아 종료합니다.

오케스트레이터 하에서 표준 서버는 $GT_ROOT/.beads/.에 위치합니다. 다른
해당 공유 데이터 디렉터리를 사용하는 dolt sql-server 프로세스는 고아로 간주되어
종료됩니다.

독립 실행 모드에서는 현재 사용 중인 dolt sql-server 프로세스만
프로젝트의 Dolt 데이터 디렉토리는 정리 대상입니다. 다른 프로젝트의
서버가 보존됩니다.

```
bd dolt killall
```

#### bd dolt pull

구성된 Dolt 원격 저장소에서 커밋을 로컬 데이터베이스로 가져옵니다.

데이터베이스 디렉토리에 Dolt 원격이 구성되어 있어야 합니다.
Hosted Dolt의 경우 인증을 위해 DOLT_REMOTE_USER 및 DOLT_REMOTE_PASSWORD 환경
변수를 설정하십시오.

기본 원격 대신 특정 이름이 지정된 원격에서 pull하려면 --remote를 사용합니다.
원격은 이미 존재해야 합니다( 'bd dolt remote add' 참조).

```
bd dolt pull [flags]
```

**플래그:**

```
      --remote string   기본값 대신 이름을 지정한 특정 원격에서 pull
```

#### bd dolt push

구성된 원격 저장소로 로컬 Dolt 커밋을 푸시합니다.

데이터베이스 디렉터리에 Dolt 원격이 구성되어 있어야 합니다.
Hosted Dolt의 경우, 인증을 위해 DOLT_REMOTE_USER 및 DOLT_REMOTE_PASSWORD 환경
변수를 설정합니다.

원격 변경 사항을 덮어쓰려면 --force를 사용하세요(예: 원격에
작업 세트에 커밋되지 않은 변경 사항이 있는 경우).

기본값 대신 특정 이름의 원격으로 푸시하려면 --remote를 사용합니다.
원격은 이미 존재해야 합니다('bd dolt remote add').

```
bd dolt push [flags]
```

**플래그:**

```
      --force           강제 push(원격 변경 사항 덮어쓰기)
      --remote string   기본값 대신 이름을 지정한 특정 원격으로 push
```

#### bd dolt remote

Dolt 리모트를 푸시/풀 복제에 사용할 수 있도록 관리합니다.

하위 명령:
  add &lt;name&gt; &lt;url&gt;   새 원격 저장소 추가
  list               구성된 모든 원격 저장소 나열
  remove &lt;name&gt;      원격 저장소 제거

```
bd dolt remote
```

##### bd dolt remote add

Dolt 원격 추가

```
bd dolt remote add <name> <url>
```

##### bd dolt remote list

설정된 Dolt 원격 저장소 목록

```
bd dolt remote list
```

##### bd dolt remote remove

Dolt 원격 저장소 제거

```
bd dolt remote remove <name>
```

#### bd dolt set

metadata.json에서 Dolt 구성 값을 설정합니다.

키:
  database  데이터베이스 이름 (기본값: 이슈 접두사 또는 "beads")
  host      서버 호스트 (기본값: 127.0.0.1)
  port      서버 포트 (자동 감지; bd dolt set port &lt;N&gt;로 덮어쓰기)
  user      MySQL 사용자 (기본값: root)
  data-dir  사용자 지정 dolt 데이터 디렉터리 (절대 경로; 기본값: .beads/dolt)

팀 전체 기본값을 위해 --update-config를 사용하면 config.yaml에도 함께 쓸 수 있습니다.

예시:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100
  bd dolt set port 3307 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject

```
bd dolt set <key> <value> [flags]
```

**플래그:**

```
      --update-config   팀 전체 기본값을 위해 config.yaml에도 쓰기
```

#### bd dolt show

연결 상태와 함께 현재 Dolt 구성을 표시합니다

```
bd dolt show
```

#### bd dolt start

현재 beads 프로젝트에 대해 dolt sql-server를 시작합니다.

서버는 프로젝트 경로에서 파생된 프로젝트별 포트에서 백그라운드로 실행됩니다.
PID와 로그는 .beads/에 저장됩니다.

필요할 때 서버는 자동으로 투명하게 시작되므로 수동 시작이 거의
필요하지 않습니다. 이 명령은 명시적 제어 또는 진단에 사용합니다.

```
bd dolt start
```

#### bd dolt status

현재 프로젝트의 Dolt 엔진 상태를 표시합니다.

임베디드 모드에서는 Dolt 엔진이 프로세스 내에서 실행됨을 보고하고
디스크 상의 데이터 디렉터리를 표시한다. beads 관리(로컬) 서버의 경우
PID, 포트 및 데이터 디렉터리를 로컬 PID 파일에서 표시한다. 외부에서-
관리되는 서버의 경우, 원격 dolt_server_host 또는 로컬 서버
bd 밖에서 관리되는(dolt.auto-start: false, 예: orchestrator-shared
sql-server) — SQL을 통해 구성된 엔드포인트를 ping하고
도달 가능성, 서버 버전 및 데이터베이스를 보고한다.

```
bd dolt status
```

#### bd dolt stop

현재 프로젝트에서 beads가 관리하는 dolt sql-server를 중지합니다.

이는 정상 종료 신호를 전송합니다. 서버는 자동으로 재시작됩니다
auto-start가 비활성화되지 않은 경우 다음 bd 명령에서 서버가 자동으로 다시 시작됩니다.

```
bd dolt stop [flags]
```

**플래그:**

```
      --force   서버 강제 중지
```

#### bd dolt test

설정된 Dolt 서버와의 연결을 테스트합니다.

이것은 다음 항목을 확인합니다:
  1. 구성된 host:port에서 서버에 도달할 수 있는지
  2. 연결을 설정할 수 있는지

서버 모드로 전환하기 전에 서버가 실행 중인지 확인하려면 이 기능을 사용하세요.

```
bd dolt test
```

### bd forget

키로 메모리를 제거합니다.

'bd memories'를 사용하여 사용 가능한 키를 확인하세요.

예시:
  bd forget dolt-phantoms
  bd forget auth-jwt

```
bd forget <key>
```

### bd hooks

Beads 통합을 위해 git 훅을 설치, 제거하거나 나열합니다.

후크 제공 항목:
- pre-commit: 커밋 전에 연결된 훅 실행
- post-merge: pull/merge 후에 연결된 훅 실행
- pre-push: 푸시 전에 연결된 훅 실행
- post-checkout: 브랜치 체크아웃 후 연결된 훅 실행
- prepare-commit-msg: 포렌식 분석을 위한 에이전트 식별자 트레일러 추가

```
bd hooks
```

#### bd hooks install

Beads 통합을 위한 Git 훅을 설치합니다.

기본적으로 훅은 현재 저장소의 .git/hooks/에 설치됩니다.
--beads를 사용하여 .beads/hooks/에 설치합니다(권장: Dolt 백엔드).
--shared를 사용하면 버전 관리 디렉터리(.beads-hooks/)에 설치할 수 있으며
이 디렉터리는 git에 커밋하고 팀 구성원과 공유할 수 있습니다.

Hooks는 기존 훅과 공존할 수 있도록 섹션 마커를 사용합니다 — 마커 밖의 사용자 콘텐츠는
설치 및 업그레이드 전반에서 유지됩니다.

설치된 훅:
  - pre-commit: 커밋 전 체인된 훅 실행
  - post-merge: pull/merge 후 체인된 훅 실행
  - pre-push: 푸시 전 체인된 훅 실행
  - post-checkout: 브랜치 체크아웃 후 체인된 훅 실행
  - prepare-commit-msg: 에이전트 ID 트레일러 추가 (오케스트레이터 에이전트용)

```
bd hooks install [flags]
```

**플래그:**

```
      --beads    .beads/hooks/에 훅 설치(Dolt 백엔드에 권장)
      --chain    기존 훅과 연결(bd 훅 전에 실행)
      --force    백업 없이 기존 훅 덮어쓰기
      --shared   .git/hooks/ 대신 .beads-hooks/(버전 관리)에 훅 설치
```

#### bd hooks list

bd Git 훅의 상태(설치됨, 오래됨, 누락됨)를 표시합니다.

```
bd hooks list
```

#### bd hooks run

Git 훅의 로직을 실행합니다. 이 명령은 보통
.git/hooks/에 설치된 얇은 심 스크립트에 의해 호출됩니다.

지원되는 훅:
  - pre-commit: 커밋 전에 체인된 훅 실행
  - post-merge: pull/merge 후 체인된 훅 실행
  - pre-push: 푸시 전에 체인된 훅 실행
  - post-checkout: 브랜치 체크아웃 후 체인된 훅 실행
  - prepare-commit-msg: 포렌식 분석을 위해 에이전트 신원 트레일러 추가

얇은 심 패턴은 훅 로직이 항상 설치된
bd 버전과 동기화되도록 보장합니다. bd를 업그레이드하면 훅 동작이 자동으로 업데이트됩니다.

```
bd hooks run <hook-name> [args...]
```

#### bd hooks uninstall

.git/hooks/ 디렉터리에서 bd Git 훅을 제거합니다.

```
bd hooks uninstall
```

### bd human

가장 일반적인 명령만 표시하는 집중형 도움말 메뉴를 표시합니다.

bd에는 70개 이상의 명령이 있습니다 - 다수는 AI 에이전트, 통합, 그리고 고급 워크플로우용입니다.
이 명령은 사람이 가장 자주 필요로 하는 약 15개의 필수 명령을 보여줍니다.

전체 명령어 목록은 다음을 실행하세요: bd --help

하위 명령어:
  human list              human 라벨이 지정된 human-needed 비드를 모두 표시합니다 (라벨이 'human'인 이슈)
  human respond &lt;id&gt;      human-needed 비드에 응답합니다(댓글 추가 및 닫기)
  human dismiss &lt;id&gt;      human-needed 비드를 영구적으로 무시합니다
  human stats             human-needed 비드의 요약 통계를 표시합니다

```
bd human
```

#### bd human dismiss

인간이 필요한 bead를 응답 없이 영구적으로 해제합니다.

이 이슈는 "Dismissed" 사유와 선택적 노트로 닫힙니다.

예시:
  bd human dismiss bd-123
  bd human dismiss bd-123 --reason "더 이상 적용되지 않음"

```
bd human dismiss <issue-id> [flags]
```

**플래그:**

```
      --reason string   기각 사유(선택 사항)
```

#### bd human list

'human' 태그가 붙은 모든 이슈를 나열합니다.

이는 사람의 개입이나 입력이 필요한 문제들입니다.

예시:
  bd human list
  bd human list --status=open
  bd human list --json

```
bd human list [flags]
```

**플래그:**

```
  -s, --status string   상태로 필터링(open, closed 등)
```

#### bd human respond

댓글을 추가하고 이를 닫아서 human-needed bead에 응답하세요.

응답이 댓글로 추가되며, 이슈는 사유가 "Responded"로 닫힙니다.

예시:
  bd human respond bd-123 --response "인증에 OAuth2를 사용하세요"
  bd human respond bd-123 -r "승인되었으니 구현을 진행하세요"

```
bd human respond <issue-id> [flags]
```

**플래그:**

```
  -r, --response string   응답 텍스트(필수)
```

#### bd human stats

인간이 필요한 비드에 대한 요약 통계를 표시합니다.

총 개수, 대기(열림), 응답(해제 없이 닫힘),
해제된 beads의 개수를 표시합니다.

예시:
  bd human stats

```
bd human stats
```

### bd info

현재 데이터베이스에 대한 정보를 표시합니다.

이 명령은 bd가 의도하지 않은 데이터베이스를 사용하는 문제를 디버깅하는 데 도움이 됩니다. 다음을 표시합니다:
  - 데이터베이스 파일의 절대 경로
  - 데이터베이스 통계(이슈 수)
  - 스키마 정보(--schema 플래그 사용)
  - 최근 버전의 변경 사항(--whats-new 플래그 사용)

예시:
  bd info
  bd info --json
  bd info --schema --json
  bd info --whats-new
  bd info --whats-new --json
  bd info --thanks

```
bd info [flags]
```

**플래그:**

```
      --json        JSON 형식으로 출력
      --schema      출력에 스키마 정보 포함
      --thanks      기여자 감사 페이지 표시
      --whats-new   최근 버전의 에이전트 관련 변경 사항 표시
```

### bd init

현재 디렉토리에서 .beads/ 디렉토리를 생성하여 bd를 초기화합니다
및 Dolt 데이터베이스를 생성합니다. 필요에 따라 사용자 지정 이슈 접두어를 지정할 수 있습니다.

Dolt는 기본(및 유일하게 지원되는) 스토리지 백엔드입니다. 이전 SQLite
백엔드는 제거되었습니다. --backend=sqlite를 사용하여 마이그레이션 지침을 확인하세요.

기존 서버 데이터베이스 이름을 지정하려면 --database를 사용해 기본
접두사 기반 명명 방식을 덮어씁니다. 이는 외부 도구(예: 오케스트레이터)가
이미 데이터베이스를 생성한 경우에 유용합니다.

--stealth 사용 시: 보이지 않는 beads 사용을 위해 저장소별 Git 설정을 구성합니다:
  • .git/info/exclude: beads 파일이 커밋되지 않도록 방지
  저장소 협업자에게 영향을 주지 않는 개인 사용에 완벽합니다.
  특정 AI 도구를 설정하려면 다음을 실행하세요: bd setup &lt;claude|cursor|aider|...&gt; --stealth

기본적으로 beads는 내장된 Dolt 엔진을 사용합니다(외부 서버가 필요하지 않습니다).
대신 외부 dolt sql-server를 사용하려면 --server를 전달하세요. 서버 모드에서는,
연결 정보를 --server-host, --server-port, --server-user로 설정하세요.
비밀번호는 BEADS_DOLT_PASSWORD 환경 변수로 설정해야 합니다.

자동 내보내기는 선택 사항입니다. 활성화되면 bd는 이슈를
.beads/issues.jsonl로 쓰기 명령 후(60초당 한 번으로 제한됨) 내보냅니다. 이는
뷰어(bv), 상호 운용, 그리고 이슈 수준 마이그레이션용입니다; 백업용이 아닙니다.
기기 간 동기화와 백업은 JSONL 가져오기/내보내기가 아니라 Dolt 원격/백업을 사용합니다.
활성화하려면: bd config set export.auto true

비대화형 모드(--non-interactive 또는 BD_NON_INTERACTIVE=1):
  모든 대화형 프롬프트를 건너뛰고 적절한 기본값을 사용합니다:
  • 역할 기본값은 "maintainer"입니다 (--role로 덮어쓸 수 있음)
  • Fork 제외는 포크가 감지되면 자동으로 구성됩니다
  • Auto-export는 기본값(비활성화)으로 유지됩니다
  • --contributor 및 --team 플래그는 거부됩니다(위저드에는 상호작용이 필요함)
  stdin이 터미널이 아니거나 CI=true가 설정된 경우에도 자동으로 감지됩니다.

```
bd init [flags]
```

**플래그:**

```
      --agents-file string                             에이전트 지침용 사용자 정의 파일 이름(기본값: AGENTS.md)
      --agents-profile string                          AGENTS.md 프로필: 'minimal'(기본값, bd prime 포인터) 또는 'full'(전체 명령 참조)
      --agents-template string                         사용자 정의 AGENTS.md 템플릿 경로(포함된 기본값 재정의)
      --backend string                                 저장소 백엔드(기본값: dolt). --backend=sqlite는 사용 중단 알림 출력.
      --contributor                                    OSS 기여자 설정 마법사 실행
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
      --team                                           팀 워크플로 설정 마법사 실행
```

### bd kv

Beads 키-값 저장소에서 작업하기 위한 명령어입니다.

키-값 저장소는 플래그, 환경 변수,
또는 세션 간에 지속되는 다른 사용자 정의 데이터를 저장하는 데 유용합니다.

예제:
  bd kv set mykey myvalue    # 값 설정
  bd kv get mykey            # 값 가져오기
  bd kv clear mykey          # 키 삭제
  bd kv list                 # 모든 키-값 쌍 목록

```
bd kv
```

#### bd kv clear

beads 키-값 저장소에서 키를 삭제합니다.

예시:
  bd kv clear feature_flag
  bd kv clear api_endpoint

```
bd kv clear <key>
```

#### bd kv get

beads 키-값 저장소에서 값을 가져옵니다.

예시:
  bd kv get feature_flag
  bd kv get api_endpoint

```
bd kv get <key>
```

#### bd kv list

beads 키-값 저장소의 모든 키-값 쌍을 나열합니다.

예시:
  bd kv list
  bd kv list --json

```
bd kv list
```

#### bd kv set

beads 키-값 저장소에 키-값 쌍을 설정합니다.

이는 플래그, 환경 변수 또는 기타
세션 간에 지속되는 사용자 정의 데이터를 저장하는 데 유용합니다.

예시:
  bd kv set feature_flag true
  bd kv set api_endpoint https://api.example.com
  bd kv set max_retries 3

```
bd kv set <key> <value>
```

### bd memories

모든 메모리를 나열하거나 키워드로 검색합니다.

예시:
  bd memories              # 모든 메모리를 나열
  bd memories dolt         # dolt에 대한 메모리를 검색
  bd memories "경쟁 플래그"  # 구문을 검색

```
bd memories [search]
```

### bd onboard

bd 통합을 위해 에이전트 지침 파일에 추가할 최소 스니펫을 표시합니다.

기본적으로 에이전트 지침 파일은 AGENTS.md입니다. 'bd init --agents-file'를 사용하여
다른 파일 이름을 구성하세요(예: BEADS.md).

이는 전체 워크플로우 컨텍스트를 위해 'bd prime'을 가리키는 작은(~10줄) 스니펫을 출력합니다.
이는 'bd init'이 기본으로 생성하는 동일한 최소 프로필입니다.
이 접근 방식:

  • 에이전트 파일을 가볍게 유지합니다(지침으로 불필요하게 부풀리지 않습니다)
  • bd prime는 동적이고 항상 최신 상태의 워크플로우 세부 정보를 제공합니다
  • 훅은 세션 시작 시 bd prime를 자동으로 주입합니다

자동으로 훅 출력을 삽입하지 않는 에이전트나 환경의 경우, 완전한 명령어 참조를 포함하려면
'bd init --agents-profile=full'을(를) 사용하세요.

```
bd onboard
```

### bd prime

핵심 Beads 워크플로우 컨텍스트를 AI 최적화 마크다운 형식으로 출력합니다.

자동으로 MCP 서버가 활성 상태인지 감지하고 출력을 조정합니다:
- MCP 모드: 간단한 워크플로우 알림(~50 토큰)
- CLI 모드: 전체 명령어 참조(~1-2k 토큰)

Claude Code, Gemini CLI 및 Codex SessionStart 훅을 위해 설계되어
컨텍스트 압축 후 에이전트가 bd 워크플로우를 잊어버리는 것을 방지합니다.

구성 옵션:
- no-git-ops: true일 때 스텔스 모드로 출력됩니다(세션 닫기 프로토콜에서 git 명령 없음).
  설정 방법: bd config set no-git-ops true
  커밋이 수동으로 이루어지는 시점을 제어하고 싶을 때 유용합니다.

	워크플로우 사용자 지정:
	- 기본 출력을 완전히 재정의하려면 로컬 클론 또는 해석된 워크스페이스에 .beads/PRIME.md 파일을 배치하세요.
	- 사용자 정의를 위해 기본 내용을 덤프하려면 --export를 사용하세요.
	- 지속형 메모리만 주입해야 하는 훅 컨텍스트에는 --memories-only를 사용하세요.

```
bd prime [flags]
```

**플래그:**

```
      --export          기본 콘텐츠 출력(PRIME.md 재정의 무시)
      --full            전체 CLI 출력 강제(MCP 감지 무시)
      --hook-json       출력을 SessionStart 훅 JSON 봉투로 감싸기(Claude Code, Gemini CLI, Codex)
      --mcp             MCP 모드 강제(최소 출력)
      --memories-only   간결한 훅 컨텍스트에 영구 메모리만 출력
      --stealth         스텔스 모드(git 작업 없이 flush만 수행)
```

### bd quickstart

일반적인 bd 워크플로우와 패턴을 보여주는 빠른 시작 가이드를 표시합니다.

```
bd quickstart
```

### bd recall

키로 메모리의 전체 내용을 검색합니다.

예시:
  bd recall dolt-phantoms
  bd recall auth-jwt

```
bd recall <key>
```

### bd remember

세션과 계정 전환 간에도 지속되는 메모리를 저장합니다.

메모리는 prime 시점(bd prime)에 주입되므로
수동 로딩 없이 모든 세션에서 이를 사용할 수 있습니다.

위치 인수는 메모리 CONTENT입니다 (키는 --key가 지정되지 않으면 거기에서
자동 생성됩니다). 편의상, 인수가 기존 메모리를 가리키는 단순 키인 경우
기존 메모리라면 저장되지 않고 RECALLED됩니다 ('bd recall'와 동일);
아무 것도 가리키지 않는 단순 키는 거부됩니다. slug와 같은 콘텐츠를 저장하려면 --key를 사용하세요.

예시:
  bd remember "항상 -race 플래그로 테스트를 실행하세요"
  bd remember "Dolt 팬텀 DB는 세 군데에 숨어 있습니다" --key dolt-phantoms
  bd remember "인증 모듈은 JWT를 사용하고 세션은 사용하지 않습니다" --key auth-jwt
  bd remember dolt-phantoms        # 기존 키를 그대로 사용: 이를 읽습니다 (= bd recall)

```
bd remember "<insight>" [flags]
```

**플래그:**

```
      --key string   메모리의 명시적 키(설정하지 않으면 콘텐츠에서 자동 생성). 이 키의 메모리가 이미 있으면 제자리에서 업데이트
```

### bd setup

AI 편집기 및 코딩 어시스턴트를 위한 통합 파일을 설정합니다.

레시피는 beads의 워크플로우 지침이 작성되는 위치를 정의합니다. 내장 레시피
에는 cursor, claude, copilot, gemini, aider, factory, codex, mux, opencode, junie, windsurf, cody, 및 kilocode가 포함됩니다.

예시:
  bd setup cursor          # Cursor IDE 통합 설치
  bd setup codex           # Codex 스킬 + AGENTS.md 가이드 + 기본 훅 설치
  bd setup codex --global  # 전역 Codex 스킬 + 가이드 + 기본 훅 설치
  bd setup copilot         # Copilot CLI 플러그인 + 저장소 지침 설치
  bd setup mux --project   # Mux 워크스페이스 레이어(.mux/AGENTS.md) 설치
  bd setup mux --global    # Mux 전역 레이어(~/.mux/AGENTS.md) 설치
  bd setup mux --project --global  # 두 Mux 레이어 모두 설치
  bd setup --list          # 사용 가능한 모든 레시피 표시
  bd setup --print         # 템플릿을 stdout으로 출력
  bd setup -o rules.md     # 템플릿을 사용자 지정 경로에 쓰기
  bd setup --add myeditor .myeditor/rules.md  # 사용자 지정 레시피 추가

설치 상태를 확인하려면 'bd setup &lt;recipe&gt; --check'를(를) 사용하세요.
제거하려면 'bd setup &lt;recipe&gt; --remove'를(를) 사용하세요.

```
bd setup [recipe] [flags]
```

**플래그:**

```
      --add string      지정한 이름으로 사용자 정의 레시피 추가
      --check           통합 설치 여부 확인
      --global          전역 설치(claude/codex/mux, ~/.claude/settings.json, $CODEX_HOME/AGENTS.md 또는 ~/.codex/AGENTS.md, ~/.mux/AGENTS.md에 쓰기)
      --list            사용 가능한 모든 레시피 나열
  -o, --output string   사용자 정의 경로에 템플릿 쓰기
      --print           템플릿을 stdout에 출력
      --project         이 프로젝트에만 설치(gemini/mux)
      --remove          통합 제거
      --stealth         스텔스 모드 사용(claude/gemini)
```

### bd where

활성 beads 데이터베이스 위치를 리디렉션 정보와 함께 표시합니다.

	이 명령은 리디렉션을 사용할 때 실제로 어떤 beads workspace가 사용되는지 이해하여 디버깅하는 데 유용합니다.
	어떤 Beads 작업공간이 실제로 사용되고 있는지 파악할 수 있습니다.

예시:
  bd where           # 활성 Beads 위치 표시
  bd where --json    # JSON 형식으로 출력


```
bd where
```

## 유지보수:

### bd batch

단일 데이터베이스 트랜잭션에서 여러 쓰기 작업을 실행합니다.

명령은 stdin(한 줄당 하나씩)에서 읽거나 -f/--file을 통해 파일에서 읽습니다.
모든 작업은 단일 dolt 트랜잭션 내에서 실행됩니다: 어떤 오류가 있으면
전체 배치가 롤백되며, 그렇지 않으면 하나의 DOLT_COMMIT으로 커밋됩니다.

이것은 현재 루프에서 'bd'를 여러 번 호출하는 쉘 스크립트를 위한 용도로,
btrfs+compression이 적용된 dolt sql-server에서 심각한 쓰기 증폭을 일으킵니다.
배치 처리(Batching)는 N번의 호출을 하나의 트랜잭션으로
병합하고 하나의 dolt 커밋으로 처리합니다.

문법 (한 줄에 하나의 명령):
  close &lt;id&gt; [reason...]
  update &lt;id&gt; &lt;key&gt;=&lt;value&gt; [&lt;key&gt;=&lt;value&gt; ...]
  create &lt;type&gt; &lt;priority&gt; &lt;title...&gt;
  dep add &lt;from-id&gt; &lt;to-id&gt; [type]
  dep remove &lt;from-id&gt; &lt;to-id&gt;
  #comment  (빈 줄과 '# ...' 주석은 무시됩니다)

지원되는 'update' 키: status, priority, title, assignee
지원되는 의존성 유형: 'bd dep add --help' 참조 (기본값: blocks)

토큰은 공백으로 구분됩니다. 큰따옴표 문자열("like this")은
공백을 포함할 수 있습니다; 따옴표를 삽입하려면 \"를 사용하고 백슬래시는 \\로 표현하세요.

예제:
  # 파이프에서
  bd list --status stale -q | awk '&#123;print "close",$1," stale"&#125;' | bd batch

  # 파일에서
  bd batch -f operations.txt

  # 인라인
  printf 'close bd-1 done\nupdate bd-2 status=in_progress\n' | bd batch

성공하면 종료 코드 0으로 끝내고 요약을 출력합니다(--json 사용 시 JSON). 오류가 하나라도 있으면,
전체 트랜잭션을 롤백하고 실패한 줄과 함께 0이 아닌 코드로 종료합니다.

참고: 이것은 제한된 하위 집합입니다. 'show', 'list', 'ready', 'sync',
복잡한 create 흐름이나 위에 나열되지 않은 플래그는 허용되지 않습니다. 사용
일반 'bd' 하위 명령을 대화형/읽기 작업에 사용하세요.

```
bd batch [flags]
```

**플래그:**

```
      --dry-run          입력을 구문 분석하고 실행하지 않은 채 명령 출력
  -f, --file string      stdin 대신 파일에서 명령 읽기
  -m, --message string   DOLT_COMMIT 메시지(기본값: 'bd: batch N ops by <actor>')
```

### bd compact

N일 이상 오래된 Dolt 커밋을 하나의 커밋으로 스쿼시합니다.

최근 커밋(보존 기간 내 커밋)은 cherry-pick을 통해 보존됩니다.
이렇게 하면 자동 커밋 히스토리에서 Dolt 저장 오버헤드가 줄어들면서도
최신 변경 사항 추적 기능을 유지할 수 있습니다.

의미적 이슈 압축(닫힌 이슈 요약)에는 'bd admin compact'를 사용하세요.
전체 이력 스쿼시에 대해서는 'bd flatten'을 사용하세요.

동작 방식:
  1. --days 임계값보다 오래된 커밋을 식별한다
  2. 오래된 모든 이력에서 스쿼시된 기본 커밋을 생성한다
  3. 최근 커밋을 맨 위에 cherry-pick한다
  4. main 브랜치를 압축된 버전으로 전환한다
  5. 공간 회수를 위해 Dolt GC를 실행한다

예시:
  bd compact --dry-run               # 미리보기: 커밋 분해 표시
  bd compact --force                 # 30일 이상 된 커밋 스쿼시
  bd compact --days 7 --force        # 최근 7일의 기록만 유지
  bd compact --days 90 --force       # 보수적: 90일 이상 된 커밋 스쿼시

```
bd compact [flags]
```

**플래그:**

```
      --days int   N일 이내의 새 커밋 유지(기본값 30)
      --dry-run    변경 없이 미리 보기
  -f, --force      커밋 squash 확인
```

### bd doctor

현재 디렉터리 또는 지정된 경로에 대해 beads 설치 상태를 점검합니다.

이 명령은 다음을 확인합니다:
  - .beads/ 디렉토리 존재 여부
  - 데이터베이스 버전 및 마이그레이션 상태
  - 스키마 호환성(필요한 모든 테이블과 열이 존재하는지)
  - 해시 기반 ID와 순차 ID 사용 여부
  - CLI 버전 최신 여부( GitHub 릴리스 확인)
  - Claude Code에서 실행 중일 때 Claude 플러그인 최신 여부
  - 파일 권한
  - 순환 종속성
  - Git 훅(pre-commit, post-merge, pre-push)
  - .beads/.gitignore 최신 상태
  - Metadata.json 버전 추적(LastBdVersion 필드)

성능 모드(--perf):
  데이터베이스에 대한 성능 진단을 실행합니다:
  - 주요 작업의 소요 시간을 측정합니다 (bd ready, bd list, bd show 등)
  - 시스템 정보를 수집합니다 (OS, arch, SQLite 버전, 데이터베이스 통계)
  - 분석을 위해 CPU 프로필을 생성합니다
  - 버그 보고를 위한 공유 가능한 보고서를 출력합니다

내보내기 모드 (--output):
  이력 분석 및 버그 보고를 위해 진단 정보를 JSON 파일로 저장합니다.
  간헐적 문제 추적을 위해 타임스탬프와 플랫폼 정보를 포함합니다.

특정 검사 모드 (--check):
  특정 검사를 자세히 실행합니다. 사용 가능한 검사는 다음과 같습니다:
  - artifacts: beads 클래식 아티팩트를 탐지하고 선택적으로 정리합니다
    (오래된 JSONL, SQLite 파일, 불필요한 .beads 디렉터리). --clean과 함께 사용하세요.
  - conventions: 컨벤션 드리프트(린트 경고, 오래된
    문제, 고아 이슈). 경고만 제공되며, 결코 차단하지 않습니다.
  - pollution: 데이터베이스에서 테스트 이슈를 감지하고 선택적으로 정리합니다
  - validate: 중점적인 데이터 무결성 검사를 실행합니다(중복, 고아
    의존성, 테스트 오염, git 충돌). 자동 수리를 위해 --fix를 사용하세요.

심층 검증 모드 (--deep):
  전체 그래프 무결성을 검증합니다. 대규모 데이터베이스에서는 느릴 수 있습니다.
  추가 검사:
  - 상위-하위 일관성: 모든 상위-하위 의존성은 기존 이슈를 가리킵니다
  - 의존성 무결성: 모든 의존성은 유효한 이슈를 참조합니다
  - 에픽 완전성: 닫을 준비가 된 에픽을 찾습니다(모든 하위 항목이 닫힘)
  - Agent bead 무결성: Agent bead는 유효한 상태 값을 가집니다
  - 메일 스레드 무결성: 스레드 ID는 기존 이슈를 참조합니다
  - 분자 무결성: 분자는 유효한 상위-하위 구조를 가집니다

서버 모드 (--server):
  Dolt 서버 모드 연결에 대한 상태 검사를 실행합니다 (bd-dolt.2.3):
  - 서버 연결 가능: 구성된 host:port에 연결할 수 있습니까?
  - Dolt 버전: Dolt 서버인가요(기본 MySQL이 아님)?
  - 데이터베이스 존재: 'beads' 데이터베이스가 존재합니까?
  - 스키마 호환성: beads 테이블을 쿼리할 수 있습니까?
  - 연결 풀: 풀 상태 메트릭

마이그레이션 검증 모드 (--migration):
  Dolt 마이그레이션 검증 검사를 machine-parseable 출력으로 실행합니다.
  마이그레이션 전에 --migration=pre를 사용하여 준비 상태를 확인합니다:
  - JSONL 파일이 존재하고 유효합니다(파싱 가능, 손상 없음)
  - 모든 JSONL 문제는 SQLite에 존재합니다(또는 불일치를 설명함)
  - 마이그레이션을 차단하는 문제점이 없습니다
  마이그레이션 후 완료를 확인하려면 --migration=post를 사용합니다:
  - Dolt 데이터베이스가 존재하고 상태가 양호합니다
  - JSONL의 모든 문제는 Dolt에 존재합니다
  - 마이그레이션 중 데이터 손실이 발생하지 않았습니다
  - Dolt 데이터베이스에 잠금이나 커밋되지 않은 변경 사항이 없습니다
  자동화를 위해 machine-parseable 출력을 생성하려면 --json과 함께 사용하세요.

에이전트 모드 (--agent):
  AI 에이전트가 소비하도록 설계된 진단을 출력합니다. 간결한
  pass/fail 메시지 대신 각 이슈에는 다음이 포함됩니다:
  - 관찰 상태: 시스템이 실제로 보이는 상태
  - 예상 상태: 시스템이 보여야 하는 상태
  - 설명: 문제에 대한 전체 맥락과 왜 중요한지
  - 명령: 실행할 정확한 복구 명령
  - 소스 파일: 코드베이스에서 추가로 조사할 위치
  - 심각도: blocking (작동을 방해), degraded (부분 기능),
    또는 advisory (정보 제공만)
  ZFC-compliant: Go는 관찰하고 보고하며, 에이전트가 결정하고 실행합니다.
  구조화된 에이전트 대상 출력을 위해 --json과 함께 사용하세요.

경고 억제:
  특정 경고는 doctor.suppress.&lt;check-slug&gt; 설정으로 억제할 수 있습니다:
    bd config set doctor.suppress.pending-migrations true
    bd config set doctor.suppress.git-hooks true
  이름은 슬러그로 변환됩니다: "Git Hooks" → "git-hooks".
  경고만 억제되며, 오류와 통과한 검사는 항상 표시됩니다.
  억제를 해제하려면: bd config unset doctor.suppress.&lt;slug&gt;

예시:
  bd doctor              # 현재 디렉토리 확인
  bd doctor /path/to/repo # 특정 저장소 확인
  bd doctor --json       # 기계 판독 가능한 출력
  bd doctor --agent      # 에이전트 대상 진단 출력
  bd doctor --agent --json  # 구조화된 에이전트 진단(JSON)
  bd doctor --fix        # 문제를 자동으로 수정(확인 필요)
  bd doctor --fix --yes  # 문제를 자동으로 수정(확인 없음)
  bd doctor --fix -i     # 각 수정 작업을 개별적으로 확인
  bd doctor --fix --fix-child-parent  # 하위→상위 의존성도 함께 수정(옵트인)
  bd doctor --fix --force # 데이터베이스를 열 수 없는 경우에도 강제 복구
  bd doctor --fix --source=jsonl # JSONL 내보내기에서 데이터베이스 재구성
  bd doctor --dry-run    # 변경 없이 --fix가 수행할 작업 미리보기
  bd doctor --perf       # 성능 진단
  bd doctor --output diagnostics.json  # 진단 결과를 파일로 내보내기
  bd doctor --check=artifacts           # 기존 아티팩트 표시(JSONL, SQLite, cruft 디렉토리)
  bd doctor --check=artifacts --clean  # 안전하게 삭제 가능한 아티팩트 삭제(확인 필요)
  bd doctor --check=conventions        # 컨벤션 드리프트 검사(lint, stale, orphans)
  bd doctor --check=pollution          # 잠재적인 테스트 문제 표시
  bd doctor --check=pollution --clean  # 테스트 문제 삭제(확인 필요)
  bd doctor --check=validate         # 데이터 무결성 검사만 수행
  bd doctor --check=validate --fix   # 데이터 무결성 문제 자동 수정
  bd doctor --deep             # 전체 그래프 무결성 검증
  bd doctor --server           # Dolt 서버 모드 상태 점검
  bd doctor --migration=pre    # Dolt 마이그레이션 준비 상태 검증
  bd doctor --migration=post   # Dolt 마이그레이션 완료 여부 검증
  bd doctor --migration=pre --json  # 기계 구문 분석 가능한 마이그레이션 검증

```
bd doctor [path] [flags]
```

**플래그:**

```
      --agent                                   에이전트용 진단 모드: AI 에이전트를 위한 풍부한 컨텍스트(ZFC 준수)
      --check string                            특정 검사를 상세히 실행(예: 'pollution')
      --check-health                            Git 훅 빠른 상태 검사(성공 시 출력 없음)
      --clean                                   pollution 검사에서 감지된 테스트 이슈 삭제
      --deep                                    전체 그래프 무결성 검증
      --dry-run                                 변경하지 않고 수정 사항 미리 보기
      --fix                                     가능한 경우 이슈 자동 수정
      --fix-child-parent                        child→parent 의존성 제거(선택 적용)
  -i, --interactive                             각 수정 개별 확인
      --migration string                        Dolt 마이그레이션 검증 실행: 'pre'(마이그레이션 전) 또는 'post'(마이그레이션 후)
      --orchestrator                            오케스트레이터 다중 워크스페이스 모드에서 실행(routes.jsonl 예상, 더 높은 중복 허용치)
      --orchestrator-duplicates-threshold int   오케스트레이터 모드의 중복 허용 임곗값(wisp는 임시)(기본값 1000)
  -o, --output string                           진단을 JSON 파일로 내보내기
      --perf                                    성능 진단 실행 및 CPU 프로필 생성
      --server                                  Dolt 서버 모드 상태 검사 실행(연결, 버전, 스키마)
  -v, --verbose                                 모든 검사 표시(기본값은 경고/오류만 표시)
  -y, --yes                                     확인 프롬프트 건너뛰기(비대화형 사용)
```

### bd flatten

최후의 수단: 모든 Dolt 커밋 히스토리를 하나의 커밋으로 스쿼시합니다.

이는 Tim Sehn 레시피를 사용합니다:
  1. 현재 상태에서 새 브랜치를 생성
  2. 모든 데이터를 보존한 채 초기 커밋으로 Soft-reset
  3. 모든 내용을 단일 스냅샷으로 커밋
  4. main 브랜치를 새로 평탄화된 브랜치로 교체
  5. 이전 히스토리에서 공간을 회수하기 위해 Dolt GC 실행

이는 되돌릴 수 없습니다 — 모든 커밋 이력이 손실됩니다. 결과 데이터베이스
에는 현재 모든 데이터를 포함하는 커밋이 정확히 하나 존재합니다.

이 경우에 사용하세요:
  - .beads/dolt 디렉터리가 매우 커진 경우
  - 커밋 단위 이력(타임 트래블)이 필요하지 않은 경우
  - 최소한의 저장소로 새로 시작하고 싶은 경우

예제:
  bd flatten --dry-run               # 미리보기: 커밋 개수와 디스크 사용량 표시
  bd flatten --force                 # 실제로 모든 히스토리를 squash
  bd flatten --force --json          # JSON 출력

```
bd flatten [flags]
```

**플래그:**

```
      --dry-run   변경 없이 미리 보기
  -f, --force     되돌릴 수 없는 이력 squash 확인
```

### bd gc

독립형 Beads 데이터베이스를 위한 전체 수명 주기 가비지 컬렉션.

세 단계가 순차적으로 실행됩니다:
  1. DECAY   — N일보다 오래된 종료된 이슈를 삭제합니다 (기본값 90)
  2. COMPACT — 오래된 Dolt 커밋을 더 적은 커밋으로 squash 합니다 (bd compact)
  3. GC      — 디스크 공간을 회수하기 위해 Dolt 가비지 컬렉션을 실행합니다

각 단계는 개별적으로 건너뛸 수 있습니다. --dry-run을 사용하면 변경하지 않고 모든 단계를 미리 볼 수 있습니다
변경하지 않습니다.

예시:
  bd gc                              # 기본값(90일 decay)으로 전체 GC 수행
  bd gc --dry-run                    # 발생할 작업 미리 보기
  bd gc --older-than 30              # 30일 이상 전에 닫힌 이슈를 decay 처리
  bd gc --skip-decay                 # 이슈 삭제를 건너뛰고 compact+GC만 실행
  bd gc --skip-dolt                  # Dolt GC를 건너뛰고 decay+compact만 실행
  bd gc --force                      # 확인 프롬프트 건너뛰기

```
bd gc [flags]
```

**플래그:**

```
      --dry-run          변경 없이 미리 보기
  -f, --force            확인 프롬프트 건너뛰기
      --older-than int   N일보다 오래된 닫힌 이슈 삭제(기본값 90)
      --skip-decay       이슈 삭제 단계 건너뛰기
      --skip-dolt        Dolt 가비지 컬렉션 단계 건너뛰기
```

### bd migrate

데이터베이스 마이그레이션 및 데이터 변환 명령입니다.

하위 명령어가 없으면 데이터베이스 메타데이터를 현재 버전으로 확인하고 업데이트합니다.

하위 명령어:
  hooks       마커 관리 형식으로 Git 훅 마이그레이션을 계획
  issues      저장소 간 이슈 이동
  schema      보류 중인 스키마 마이그레이션 적용(멱등)
  sync        다중 클론 설정을 위해 sync.branch 워크플로 설정


```
bd migrate [flags]
```

**플래그:**

```
      --dry-run          변경하지 않고 수행될 작업 표시
      --inspect          AI 에이전트 분석용 마이그레이션 계획과 데이터베이스 상태 표시
      --json             마이그레이션 통계를 JSON 형식으로 출력
      --update-repo-id   저장소 ID 업데이트(git 원격 변경 후 사용)
      --yes              확인 프롬프트 자동 승인
```

#### bd migrate hooks

마커 관리 형식으로 마이그레이션하기 위해 Git 훅 파일과 사이드카 아티팩트를 분석합니다.

모드:
  --dry-run  파일을 변경하지 않고 마이그레이션 작업 미리보기
  --apply    마이그레이션 작업 적용

예시:
  bd migrate hooks --dry-run
  bd migrate hooks --apply
  bd migrate hooks --apply --yes
  bd migrate hooks --dry-run --json

```
bd migrate hooks [path] [flags]
```

**플래그:**

```
      --apply     계획된 훅 마이그레이션 변경 사항 적용
      --dry-run   변경하지 않고 수행될 작업 표시
      --json      JSON 형식으로 출력
      --yes       --apply의 확인 프롬프트 건너뛰기
```

#### bd migrate issues

한 소스 저장소에서 다른 저장소로 이슈를 필터링 및 의존성 보존 상태로 이동합니다.

이 명령은 선택한 이슈의 source_repo 필드를 업데이트하여 다음을 수행할 수 있습니다:
- 기여자 계획 이슈를 업스트림 저장소로 이동
- 다단계 저장소 전반의 이슈를 재구성
- 여러 저장소의 이슈를 통합

예시:
  # 계획 저장소에서 현재 저장소로 마이그레이션 미리보기
  bd migrate-issues --from ~/.beads-planning --to . --dry-run

  # 모든 열린 P1 버그 이동
  bd migrate-issues --from ~/repo1 --to ~/repo2 --priority 1 --type bug --status open

  # 특정 이슈를 종속성과 함께 이동
  bd migrate-issues --from . --to ~/archive --id bd-abc --id bd-xyz --include closure

  # 라벨 필터로 이슈 이동
  bd migrate-issues --from . --to ~/feature-work --label frontend --label urgent

```
bd migrate issues [flags]
```

**플래그:**

```
      --dry-run            변경하지 않고 계획 표시
      --from string        소스 저장소(필수)
      --id strings         마이그레이션할 특정 이슈 ID(여러 개 지정 가능)
      --ids-file string    이슈 ID가 포함된 파일(한 줄에 하나)
      --include string     포함할 의존성: none/upstream/downstream/closure(기본값 "none")
      --label strings      레이블로 필터링(여러 개 지정 가능)
      --priority int       우선순위로 필터링(0-4)(기본값 -1)
      --status string      상태로 필터링(open/closed/all)
      --strict             고립 의존성이나 누락된 저장소가 있으면 실패
      --to string          대상 저장소(필수)
      --type string        이슈 유형으로 필터링(bug/feature/task/epic/chore/decision)
      --within-from-only   소스 저장소의 의존성만 포함(기본값 true)
      --yes                확인 프롬프트 건너뛰기
```

#### bd migrate schema

보류 중인 스키마 마이그레이션을 멱등적으로 적용합니다.

스키마 마이그레이션은 저장소 오픈 시에도 자동으로 실행되므로 이 하위 명령은
일반적으로 no-op입니다. 이는 마이그레이션을 CI, 릴리스 게이트, 복구 시나리오에서
명시적이고 관찰 가능하게 만들기 위해 존재합니다.

예시:
  bd migrate schema
  bd migrate schema --json

```
bd migrate schema [flags]
```

**플래그:**

```
      --json   JSON 형식으로 출력
```

#### bd migrate sync

다중 클론 설정을 위해 별도의 브랜치 워크플로우를 구성합니다.

이 설정은 sync.branch 구성 값을 설정하여 이슈 데이터가
전용 브랜치에 커밋되어 메인 브랜치를 깔끔하게 유지합니다.

예시:
  bd migrate sync beads-sync

```
bd migrate sync <branch> [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 수행될 작업 표시
      --json      JSON 형식으로 출력
```

### bd ping

데이터베이스에 대한 접근이 가능한지 확인하는 가벼운 상태 확인입니다.

단계:
  1. .beads 워크스페이스를 확인한다
  2. 스토어를 엽니다(임베디드 또는 서버)
  3. 간단한 쿼리 실행(이슈 수)
  4. 타이밍을 보고한다

성공하면 종료 코드 0을 반환하고, 실패하면 종료 코드 1을 반환합니다.

예제:
  bd ping              # 빠른 연결 확인
  bd ping --json       # 자동화를 위한 구조화된 출력

```
bd ping
```

### bd preflight

기여자를 위해 일반적인 PR 사전 확인 사항 체크리스트를 표시합니다.

이 명령은 CI로 푸시하기 전에 흔히 발생하는 문제를 잡아줍니다:
- 로컬에서 테스트가 실행되지 않음
- 린트 오류
- 포맷이 맞지 않은 Go 파일
- .beads/issues.jsonl 오염
- 오래된 nix vendorHash
- 버전 불일치

예시:
  bd preflight              # 체크리스트 표시
  bd preflight --check      # 검사를 자동으로 실행
  bd preflight --check --json  # 프로그래밍 방식 사용을 위한 JSON 출력
  bd preflight --check --skip-lint  # 린트 검사를 명시적으로 건너뛰기


```
bd preflight [flags]
```

**플래그:**

```
      --check       검사 자동 실행
      --fix         가능한 경우 이슈 자동 수정(아직 구현되지 않음)
      --json        결과를 JSON으로 출력
      --skip-lint   린트 검사를 명시적으로 건너뛰기
```

### bd prune

닫힌 비일시적 beads와 관련 데이터를 영구적으로 삭제합니다.

이것을 사용해 더 이상 유용하지 않은 닫힌 일반 비드(작업, 기능, 버그, 잡무 등)를 정리합니다.
일반적인 경우는 장기 운영되는 저장소에서
닫힌 작업이 쌓여 자동 내보내기를 비대하게 하거나 쿼리를 느리게 만드는 것입니다.

--older-than 또는 --pattern이 필요합니다. 이 플래그는 안전장치입니다 — 없으면
근육 기억으로 실행한 `--force`가 저장소의 모든 닫힌 bead를 삭제할 수 있습니다.
정말로 닫힌 항목을 모두 정리하려면 `--pattern '*'`를 사용하세요.

삭제: 일치하는 비드의 이슈, 종속성, 라벨, 이벤트 및 댓글을 삭제합니다.
건너뜀: 고정(보호됨) 비드, 진행 중인 비드, 및 임시 비드를 건너뜁니다.

닫힌 일시적 beads (wisps, 순간 분자)를 삭제하려면
`bd purge`를 대신 사용하세요.

많은 행을 삭제한 후 Dolt 저장소를 완전히 회수하려면 `bd flatten`를 실행한 다음
히스토리를 압축하고 오래된 청크를 가비지 컬렉션할 수 있습니다.

예제:
  bd prune --older-than 30d              # 닫힌 beads의 &gt;30d 오래된 항목 미리보기
  bd prune --older-than 30d --force      # 항목을 삭제
  bd prune --older-than 90d --dry-run    # 통계가 포함된 상세 미리보기
  bd prune --pattern "*" --force         # 닫힌 일반 beads 모두 삭제
  bd prune --pattern "gm-temp-*" --force # 패턴을 기준으로 범위 지정

```
bd prune [flags]
```

**옵션:**

```
      --dry-run             정리될 항목을 통계와 함께 미리 보기
  -f, --force               실제 정리(없으면 미리 보기 표시)
      --older-than string   N보다 오래전에 닫힌 bead만 정리(예: 30d, 2w, 60)
      --pattern string      ID glob 패턴과 일치하는 bead만 정리(예: 'gm-old-*')
```

### bd purge

닫힌 임시 비드와 연결된 데이터를 영구적으로 삭제합니다.

닫힌 임시 bead(wisp, 일시적 molecule)는 빠르게 쌓이며
한 번 닫히면 더는 가치가 없습니다. 이 명령은 저장 공간을 회수하기 위해 이를 제거합니다.

삭제: 일치하는 비드의 이슈, 의존성, 라벨, 이벤트 및 댓글을 삭제합니다.
건너뜀: 고정된 비드(보호됨).

닫힌 비일시적 beads(일반 작업, 기능, 버그 등)를 삭제하려면
대신 `bd prune`를 사용하세요.

많은 행을 삭제한 후 Dolt 저장소를 완전히 회수하려면 `bd flatten`를 따르세요
그래야 히스토리를 축소하고 오래된 청크를 가비지 컬렉션할 수 있습니다.

예시:
  bd purge                           # 삭제될 항목 미리보기
  bd purge --force                   # 닫힌 임시 beads를 모두 삭제
  bd purge --older-than 7d --force   # 닫힌 지 7일 이상 지난 항목만 삭제
  bd purge --pattern "*-wisp-*"      # ID 패턴과 일치하는 항목만 삭제
  bd purge --dry-run                 # 통계가 포함된 상세 미리보기

```
bd purge [flags]
```

**플래그:**

```
      --dry-run             제거될 항목을 통계와 함께 미리 보기
  -f, --force               실제 제거(없으면 미리 보기 표시)
      --older-than string   N보다 오래전에 닫힌 bead만 제거(예: 7d, 2w, 30)
      --pattern string      ID glob 패턴과 일치하는 bead만 제거(예: *-wisp-*)
```

### bd recompute-blocked

모든 issue와 wisp에 대해 비정규화된 is_blocked 플래그를 다시 계산합니다.

is_blocked는 의존성 그래프에서 파생되며 로컬 쓰기와
병합이 변경한 범위로 한정된 pull 후 재계산에 의해 자동으로 유지됩니다. 만약
해당 범위 재계산이 건너뛰어지면 — 병합 후 커밋된 재계산이 실패했거나
수동으로 해결한 충돌 pull의 경우 — 플래그가 오래된 값이 될 수 있고, 이후
나중에 아무것도 병합하지 않는 pull은 이를 갱신하지 않습니다 (bd-6dnrw.37). 'bd ready'
는 이 플래그를 신뢰하므로, 오래된 값이 준비된 작업을 조용히 숨기거나 차단된
작업을 표시합니다.

이 명령은 전체 재연산을 조건 없이 실행하고 그 결과를 커밋합니다.
이는 멱등적입니다: 일관된 데이터베이스에서는 아무것도 변경하지 않습니다. 임베디드와
서버 모드 모두에서 작동합니다('bd doctor'는 서버 모드 전용이라는 점과 달리).

예시:
  bd recompute-blocked          # 오래된 is_blocked 플래그를 복구
  bd recompute-blocked --json   # 기계 판독 가능한 &#123;"rows_corrected": N&#125;

```
bd recompute-blocked
```

### bd rename-prefix

데이터베이스의 모든 이슈에 대한 이슈 접두사를 변경합니다.
이는 모든 필드의 모든 이슈 ID와 텍스트 참조를 업데이트합니다.

사용 사례:
- 긴 접두사를 짧게 축약 (예: 'knowledge-work-' → 'kw-')
- 프로젝트 명명 규칙의 리브랜딩
- 데이터베이스 손상 후 여러 접두사 통합
- 팀 명명 표준으로 전환

접두사 유효성 검사 규칙:
- 최대 길이: 8자
- 허용되는 문자: 소문자, 숫자, 하이픈
- 문자로 시작해야 함
- 하이픈으로 끝나야 함 (예: 'kw-', 'work-')
- 비어 있거나 하이픈만으로는 될 수 없음

여러 접두사 감지 및 복구:
이슈가 여러 접두사를 가진 경우(손상된 데이터베이스), --repair를 사용해 이를 통합하세요.
--repair 플래그는 잘못된 접두사를 가진 모든 이슈의 접두사를 새 접두사로 변경하며,
이미 올바른 접두사를 가진 이슈는 유지합니다.

예제:
  bd rename-prefix kw-                # 'knowledge-work-'에서 'kw-'로 이름 바꾸기
  bd rename-prefix mtg- --repair      # 여러 접두사를 'mtg-'로 통합
  bd rename-prefix team- --dry-run    # 적용하지 않고 변경 사항 미리보기

참고: 이것은 드문 작업입니다. 대부분의 사용자는 이 명령이 필요하지 않습니다.

```
bd rename-prefix <new-prefix> [flags]
```

**플래그:**

```
      --dry-run   적용하지 않고 변경 사항 미리 보기
      --repair    여러 접두사가 있는 데이터베이스를 통합하여 복구
```

### bd rules

Claude 규칙 감사 및 압축

```
bd rules
```

#### bd rules audit

규칙의 모순과 병합 기회를 스캔

```
bd rules audit [flags]
```

**플래그:**

```
      --path string       규칙 디렉터리 경로(기본값 ".claude/rules/")
      --threshold float   Jaccard 유사도 임곗값(기본값 0.6)
```

#### bd rules compact

관련 규칙을 복합 규칙으로 병합

```
bd rules compact [flags]
```

**플래그:**

```
      --auto            감사 제안 적용
      --dry-run         적용하지 않고 미리 보기
      --group strings   병합할 규칙 이름
      --path string     규칙 디렉터리 경로(기본값 ".claude/rules/")
```

### bd sql

기반 데이터베이스(SQLite 또는 Dolt)에 대해 원시 SQL 쿼리를 실행합니다.

상위 수준 명령의 디버깅, 유지보수 및 버그 해결을 위한 작업에 유용합니다.

예시:
  bd sql 'SELECT COUNT(*) FROM issues'
  bd sql 'SELECT id, title FROM issues WHERE status = "open" LIMIT 5'
  bd sql 'DELETE FROM dirty_issues WHERE issue_id = "bd-abc123"'
  bd sql --csv 'SELECT id, title, status FROM issues'

쿼리는 데이터베이스로 직접 전달됩니다. SELECT 쿼리는
테이블(또는 --json/--csv 사용 시 JSON/CSV)로 결과를 반환합니다.
SELECT가 아닌 쿼리(INSERT, UPDATE, DELETE)는 영향을 받은 행 수를 보고합니다.

경고: 직접 데이터베이스 액세스는 저장 계층을 우회합니다. 주의해서 사용하세요.

```
bd sql <query> [flags]
```

**플래그:**

```
      --csv   결과를 CSV 형식으로 출력
```

### bd upgrade

bd 버전 업그레이드를 확인하고 변경 사항을 검토하는 명령어입니다.

upgrade 명령은 bd 버전 변경 사항을 인지할 수 있도록 도와줍니다:
  - bd upgrade status: 이전 사용 후 bd 버전이 변경되었는지 확인합니다
  - bd upgrade review: 이전 버전 이후 변경된 내용을 표시합니다
  - bd upgrade ack: 현재 버전을 확인합니다

버전 추적은 자동입니다 - bd는 매 실행마다 metadata.json을 업데이트합니다.

```
bd upgrade
```

#### bd upgrade ack

현재 bd 버전을 승인됨으로 표시합니다.

이 명령은 현재 버전을 확인했음을 기록하기 위해 metadata.json을 업데이트합니다.
주로 업그레이드 변경 사항을 검토한 뒤 향후 업그레이드 알림을
억제하는 데 유용합니다.

참고: 버전 추적은 자동으로 수행되므로
명시적으로 승인 표시를 하려는 경우가 아니면 이 명령을 실행할 필요가 없습니다.

예시:
  bd upgrade ack
  bd upgrade ack --json

```
bd upgrade ack
```

#### bd upgrade review

마지막으로 사용한 버전 이후 bd의 변경사항을 표시합니다.

'bd info --whats-new'가 마지막 3개 버전을 표시하는 것과 달리,
이 명령은 사용자의 특정 마지막 버전 이후의 모든 변경 사항을 표시합니다.

이전 버전에서 업그레이드하면, 그때 이후로 변경된 모든 항목의 완전한
changelog를 확인할 수 있습니다.

예제:
  bd upgrade review
  bd upgrade review --json

```
bd upgrade review
```

#### bd upgrade status

마지막으로 사용한 이후 bd가 업그레이드되었는지 확인합니다.

이 명령은 시작 시 자동으로 수행되는 버전 추적을 사용하여
bd가 업그레이드되었는지 감지합니다.

예시:
  bd upgrade status
  bd upgrade status --json

```
bd upgrade status
```

### bd worktree

적절한 beads 구성으로 git worktree를 관리합니다.

Worktrees는 동일한 git 저장소를 공유하는 여러 작업 디렉터리를 허용해,
병렬 개발(예: 여러 에이전트 또는 기능)을 가능하게 합니다.

Worktrees는 메인 저장소와 동일한 beads 데이터베이스를 자동으로 공유합니다
git 공통 디렉터리 검색을 통해 자동으로 공유하므로 수동 리디렉션 구성 설정이 필요하지 않습니다.

예시:
  bd worktree create feature-auth           # 워크트리 생성
  bd worktree create bugfix --branch fix-1  # 지정된 브랜치 이름으로 생성
  bd worktree list                          # 모든 워크트리 목록
  bd worktree remove feature-auth           # 워크트리 제거 (안전 검사 포함)
  bd worktree info                          # 현재 워크트리의 정보 표시

```
bd worktree
```

#### bd worktree create

병렬 개발을 위해 git worktree를 생성합니다.

이 명령은:
1. ./&lt;name&gt; (또는 지정된 경로)에 git worktree를 생성합니다
2. worktree 경로를 .gitignore에 추가합니다 (저장소 루트 내부에 있는 경우)

worktree는 메인 저장소와 동일한 beads 데이터베이스를 자동으로 공유합니다
git 공통 디렉터리 검색을 통해 — 리디렉션 파일이 필요하지 않습니다.

예시:
  bd worktree create feature-auth           # ./feature-auth에 생성
  bd worktree create bugfix --branch fix-1  # 브랜치 이름으로 생성
  bd worktree create ../agents/worker-1     # 상대 경로에 생성

```
bd worktree create <name> [--branch=<branch>] [flags]
```

**플래그:**

```
      --branch string   워크트리의 브랜치 이름(기본값: 이름과 동일)
```

#### bd worktree info

현재 작업 트리에 대한 정보를 표시합니다.

현재 디렉토리가 git worktree에 있으면 다음을 표시합니다:
- 작업 트리 경로 및 이름
- 브랜치
- Beads 구성 (redirect 또는 main)
- 기본 저장소 위치

예제:
  bd worktree info          # 현재 작업 트리 정보 표시
  bd worktree info --json   # JSON 출력

```
bd worktree info
```

#### bd worktree list

모든 git worktree와 해당 beads 구성 상태를 나열합니다.

각 worktree를 다음 항목으로 표시합니다:
- 이름 (디렉터리 이름)
- 경로 (전체 경로)
- 브랜치
- Beads 상태: "redirect" (공유 db 사용), "shared" (메인 상태), "none" (beads 없음)

예제:
  bd worktree list          # 모든 워크트리를 나열
  bd worktree list --json   # JSON 출력

```
bd worktree list
```

#### bd worktree remove

안전 검사와 함께 git worktree를 삭제합니다.

제거하기 전에 이 명령은 다음을 확인합니다:
- 커밋되지 않은 변경사항
- 푸시되지 않은 커밋
- 스태시

안전 검사를 건너뛰려면 --force를 사용하세요(권장하지 않음).

예시:
  bd worktree remove feature-auth         # 안전 검사와 함께 제거
  bd worktree remove feature-auth --force # 안전 검사 건너뛰기

```
bd worktree remove <name> [flags]
```

**플래그:**

```
      --force   안전 검사 건너뛰기
```

## 통합 및 고급:

### bd admin

beads 데이터베이스 유지 관리를 위한 관리 명령어입니다.

이 명령어들은 고급 사용자를 위한 것이므로 주의해서 사용해야 합니다:
  cleanup   닫힌 이슈 삭제 (이슈 라이프사이클)
  compact   오래된 닫힌 이슈를 압축하여 공간을 절약합니다 (저장소 최적화)
  reset     모든 beads 데이터와 구성을 제거합니다 (전체 초기화)

일상적인 유지보수의 경우, 일반적인 수리를 자동으로 처리하는 'bd doctor --fix'를 선호하세요.
특정 데이터베이스 작업에는 이 관리자 명령어를 사용하십시오.

```
bd admin
```

#### bd admin cleanup

닫힌 이슈를 삭제하여 데이터베이스 크기를 줄입니다.

이 명령은 데이터베이스에서 닫힌 이슈를 영구적으로 제거합니다.

참고: 이 명령은 이슈 라이프사이클(closed -&gt; deleted)만 관리합니다. 일반적인
건강 점검 및 자동 수리를 위해서는 대신 'bd doctor --fix'를 사용하세요.

기본적으로 닫힌 모든 이슈를 삭제합니다. --older-than를 사용하면
특정 날짜 이전에 닫힌 이슈만 삭제할 수 있습니다.

예제:
  bd admin cleanup --force                          # 모든 닫힌 이슈를 삭제
  bd admin cleanup --older-than 30 --force          # 30일 이상 전에 닫힌 이슈만 삭제
  bd admin cleanup --ephemeral --force              # 닫힌 wisps(일시적 분자)만 삭제
  bd admin cleanup --dry-run                        # 삭제될 항목을 미리보기

안전성:
- 실제로 삭제하려면 --force 플래그가 필요합니다 (--dry-run 제외)
- 종속 항목을 삭제하려면 --cascade를 지원합니다
- 삭제될 항목의 미리보기를 표시합니다
- 기계 판독 가능한 출력에 --json을 사용하세요

참고:
  bd doctor --fix    자동 상태 점검 및 복구(정기 유지보수에 권장됨)
  bd admin compact   오래된 닫힌 이슈를 압축해 공간을 절약합니다

```
bd admin cleanup [flags]
```

**플래그:**

```
      --cascade          모든 종속 이슈를 재귀적으로 삭제
      --dry-run          변경하지 않고 삭제될 항목 미리 보기
      --ephemeral        닫힌 wisp(일시적 molecule)만 삭제
  -f, --force            실제 삭제(이 플래그가 없으면 오류 표시)
      --older-than int   N일보다 오래전에 닫힌 이슈만 삭제(0 = 닫힌 이슈 모두)
```

#### bd admin compact

의미론적 요약을 사용하여 오래된 닫힌 이슈를 압축합니다.

압축은 더 이상 활발히 참조되지 않는 종료된 이슈를 요약하여 데이터베이스 크기를 줄입니다.
이는 영구적인 점진적 소멸입니다. 원본 콘텐츠는 폐기됩니다.

모드:
  - Analyze: 에이전트 검토를 위한 후보 내보내기 (API 키가 필요하지 않음)
  - Apply: 에이전트가 제공한 요약 수락 (API 키가 필요하지 않음)
  - Auto: AI 기반 압축(ANTHROPIC_API_KEY 또는 ai.api_key 필요, 레거시)
  - Dolt: Dolt 가비지 컬렉션 실행(Dolt 백엔드 저장소의 경우)

티어:
  - 티어 1: 시맨틱 압축(30일 닫힘, 70% 감소)
  - 티어 2: 울트라 압축(90일 닫힘) - 계획됨, 아직 구현되지 않음

Dolt 가비지 컬렉션:
  변경 작업마다 자동 커밋을 수행하므로 Dolt 커밋 히스토리가 시간이 지남에 따라 증가합니다. 사용해
  --dolt를 사용해 Dolt 가비지 컬렉션을 실행하고 디스크 공간을 회수하세요.

  --dolt: .beads/dolt 디렉터리에서 Dolt GC를 실행하여 디스크 공간을 확보합니다.
          이는 도달할 수 없는 커밋을 제거하고 저장소를 압축합니다.

예시:
  # Dolt 가비지 컬렉션
  bd compact --dolt                        # Dolt GC 실행
  bd compact --dolt --dry-run              # GC를 실행하지 않고 미리보기

  # 에이전트 기반 워크플로우 (권장)
  bd compact --analyze --json              # 전체 콘텐츠가 포함된 후보를 가져옵니다
  bd compact --apply --id bd-42 --summary summary.txt
  bd compact --apply --id bd-42 --summary - &lt; summary.txt

  # 레거시 AI 기반 워크플로우
  bd compact --auto --dry-run              # 후보 미리보기
  bd compact --auto --all                  # 모든 적용 가능한 이슈 압축
  bd compact --auto --id bd-42             # 특정 이슈 압축

  # 통계
  bd compact --stats                       # 통계를 표시합니다


```
bd admin compact [flags]
```

**플래그:**

```
      --actor string     감사 추적용 행위자 이름(기본값 "agent")
      --all              모든 후보 처리
      --analyze          Analyze 모드: 에이전트 검토용 후보 내보내기
      --apply            Apply 모드: 에이전트 제공 요약 수락
      --auto             Auto 모드: AI 기반 압축(레거시)
      --batch-size int   일괄 처리당 이슈 수(기본값 10)
      --dolt             Dolt 모드: .beads/dolt에 Dolt 가비지 컬렉션 실행
      --dry-run          압축하지 않고 미리 보기
      --force            강제 압축(검사 우회, --id 필요)
      --id string        특정 이슈 압축
      --json             JSON 형식 출력
      --limit int        후보 수 제한(0 = 제한 없음)
      --stats            압축 통계 표시
      --summary string   요약 파일 경로(stdin은 '-' 사용)
      --tier int         압축 계층(tier 1만 구현됨)(기본값 1)
      --workers int      병렬 워커(기본값 5)
```

#### bd admin reset

beads를 초기화되지 않은 상태로 재설정하고 모든 로컬 데이터를 제거합니다.

이 명령은 다음을 제거합니다:
  - .beads 디렉터리(데이터베이스, JSONL, 구성)
  - bd가 설치한 Git 훅
  - 동기화 브랜치 작업 트리

기본적으로 삭제될 항목을 표시합니다(dry-run 모드).
실제로 리셋을 수행하려면 --force를 사용하세요.

예시:
  bd reset              # 삭제할 항목을 표시
  bd reset --force      # 실제로 모두 삭제

```
bd admin reset [flags]
```

**플래그:**

```
      --force   실제 초기화 수행(필수)
```

### bd jira

beads와 Jira 간의 이슈를 동기화합니다.

구성:
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set jira.projects "PROJ1,PROJ2"   # 여러 프로젝트
  bd config set jira.api_token "YOUR_TOKEN"
  bd config set jira.username "your_email@company.com"  # Jira Cloud의 경우
  bd config set jira.push_prefix "hippo"       # hippo-* 이슈만 Jira로 푸시
  bd config set jira.push_prefix "proj1,proj2" # 여러 접두사(쉼표로 구분)

환경 변수(설정 대체):
  JIRA_API_TOKEN  - Jira API 토큰
  JIRA_USERNAME   - Jira 사용자 이름/이메일
  JIRA_PROJECTS   - 쉼표로 구분된 프로젝트 키

예시:
  bd jira sync --pull         # Jira에서 이슈 가져오기
  bd jira sync --push         # Jira로 이슈 내보내기
  bd jira sync                # 양방향 동기화(가져오기 후 푸시)
  bd jira sync --dry-run      # 변경 없이 동기화 미리보기
  bd jira status              # 동기화 상태 표시

```
bd jira
```

#### bd jira pull

Jira에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 동일합니다: bd jira sync --pull --issues &lt;refs&gt;

```
bd jira pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

#### bd jira push

하나 이상의 beads 이슈를 Jira로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 동일합니다: bd jira sync --push --issues &lt;ids&gt;

```
bd jira push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd jira status

현재 Jira 동기화 상태를 다음 항목을 포함하여 표시합니다:
  - 마지막 동기화 타임스탬프
  - 구성 상태
  - Jira 링크가 있는 이슈 수
  - 푸시 대기 중인 이슈 (external_ref 없음)

```
bd jira status
```

#### bd jira sync

beads와 Jira 간의 이슈를 동기화합니다.

모드:
  --pull         Jira에서 beads로 이슈 가져오기
  --push         beads에서 Jira로 이슈 내보내기
  (플래그 없음)  양방향 동기화: pull 후 push, 충돌 해결

충돌 해결:
  기본적으로는 더 최신 타임스탬프가 우선합니다. 다음으로 덮어씁니다:
  --prefer-local   항상 로컬 beads 버전을 우선 사용합니다
  --prefer-jira    항상 Jira 버전을 우선 사용합니다

예제:
  bd jira sync --pull                # Jira에서 가져오기
  bd jira sync --push --create-only  # 새 이슈만 푸시
  bd jira sync --dry-run             # 변경 없이 미리보기
  bd jira sync --prefer-local        # 양방향, 로컬이 우선

```
bd jira sync [flags]
```

**플래그:**

```
      --create-only       새 이슈만 생성하고 기존 이슈는 업데이트하지 않음
      --dry-run           변경 없이 동기화 미리 보기
      --issues string     선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string     이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-jira       충돌 시 Jira 버전 우선
      --prefer-local      충돌 시 로컬 버전 우선
      --project strings   동기화할 프로젝트 키(구성된 project/projects 재정의)
      --pull              Jira에서 이슈 pull
      --push              Jira로 이슈 push
      --state string      동기화할 이슈 상태: open, closed, all(기본값 "all")
```

### bd linear

beads와 Linear 간의 이슈를 동기화합니다.

구성:
  bd config set linear.api_key "YOUR_API_KEY"
  bd config set linear.team_id "TEAM_ID"
  bd config set linear.team_ids "TEAM_ID1,TEAM_ID2"  # 여러 팀 (쉼표 구분)
  bd config set linear.project_id "PROJECT_ID"  # 선택 사항: 이 프로젝트만 동기화

환경 변수 (config의 대체):
  LINEAR_API_KEY  - Linear API 키(개별 개발자용)
  LINEAR_TEAM_ID  - Linear 팀 ID (UUID, 단일)
  LINEAR_TEAM_IDS - Linear 팀 ID (쉼표로 구분된 UUID)

OAuth (CI 작업자 / 자동 동기화):
  LINEAR_OAUTH_CLIENT_ID     - OAuth 앱 클라이언트 ID
  LINEAR_OAUTH_CLIENT_SECRET - OAuth 앱 클라이언트 비밀

  OAuth 환경 변수가 모두 설정되면 OAuth client_credentials 흐름이
  API 키 대신 사용됩니다. 이는 CI 워커가 사용자를 가장하지 않고 애플리케이션(actor=application)으로
  인증할 수 있게 합니다.
  우선순위: OAuth &gt; LINEAR_API_KEY &gt; 구성 파일.

데이터 매핑(선택 사항, 합리적인 기본값이 제공됨):
  우선순위 매핑(Linear 0-4에서 Beads 0-4로):
    bd config set linear.priority_map.0 4    # 우선순위 없음 -&gt; Backlog
    bd config set linear.priority_map.1 0    # 긴급 -&gt; Critical
    bd config set linear.priority_map.2 1    # 높음 -&gt; High
    bd config set linear.priority_map.3 2    # 보통 -&gt; Medium
    bd config set linear.priority_map.4 3    # 낮음 -&gt; Low

  상태 매핑 (Linear 상태 유형에서 Beads 상태로):
    bd config set linear.state_map.backlog open
    bd config set linear.state_map.unstarted open
    bd config set linear.state_map.started in_progress
    bd config set linear.state_map.completed closed
    bd config set linear.state_map.canceled closed
    bd config set linear.state_map.my_custom_state in_progress  # 사용자 정의 상태 이름

  라벨에서 이슈 유형으로의 매핑:
    bd config set linear.label_type_map.bug bug
    bd config set linear.label_type_map.feature feature
    bd config set linear.label_type_map.epic epic

  관계 유형 매핑 (Linear 관계를 Beads 의존성으로):
    bd config set linear.relation_map.blocks blocks
    bd config set linear.relation_map.blockedBy blocks
    bd config set linear.relation_map.duplicate duplicates
    bd config set linear.relation_map.related related

  ID 생성(선택 사항, bd/Jira 해시 모드와 일치하는 해시 ID):
    bd config set linear.id_mode "hash"      # 해시(기본값)
    bd config set linear.hash_length "6"     # 해시 길이 3-8(기본값: 6)

예제:
  bd linear sync --pull         # Linear에서 이슈 가져오기
  bd linear sync --push         # Linear로 이슈 내보내기
  bd linear sync                # 양방향 동기화(끌어오기 후 푸시)
  bd linear sync --dry-run      # 변경 사항 없이 동기화 미리보기
  bd create "로그인 수정" --external-ref https://linear.app/team/issue/TEAM-123
                              # 기존 Linear 이슈에 로컬 이슈를 연결
  bd linear status              # 동기화 상태 표시

```
bd linear
```

#### bd linear pull

Linear에서 하나 이상의 항목을 가져옵니다.

위치 인수로 bead ID 또는 외부 참조를 허용합니다.
다음과 동일함: bd linear sync --pull --issues &lt;refs&gt;

```
bd linear pull [refs...] [flags]
```

**플래그:**

```
      --dry-run     변경하지 않고 pull 미리 보기
      --relations   pull할 때 Linear 관계를 bd 의존성으로 가져오기
```

#### bd linear push

하나 이상의 beads 이슈를 Linear로 푸시합니다.

bead ID를 위치 인수로 허용합니다.
다음과 동일합니다: bd linear sync --push --issues &lt;ids&gt;

```
bd linear push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd linear status

현재 Linear 동기화 상태를 다음을 포함하여 표시합니다:
  - 마지막 동기화 타임스탬프
  - 구성 상태
  - Linear 링크가 있는 이슈 수
  - 푸시 대기 중인 이슈 (external_ref 없음)

```
bd linear status
```

#### bd linear sync

beads와 Linear 간의 이슈를 동기화합니다.

모드:
  --pull              Linear에서 이슈를 beads로 가져오기
  --push              beads에서 Linear로 이슈를 내보내기
  --pull-if-stale     데이터가 오래된 경우에만 가져오기 (최신이면 건너뜀)
  (플래그 없음)          충돌 해결과 함께 양방향 동기화: pull 후 push

오래됨 (--pull-if-stale):
  --threshold 20m     데이터를 가져오기 전에 얼마나 오래된 데이터여야 하는지(기본값 20m)
  5분 디바운스는 에이전트 루프를 방지합니다: pull이 5분 이내에 완료된 경우,
  데이터는 임계값에 관계없이 항상 최신으로 간주됩니다.

팀 선택:
  --team ID1,ID2  이 동기화를 위한 구성된 팀 ID를 덮어씁니다
  여러 팀은 linear.team_ids (쉼표로 구분)로 구성할 수 있습니다.
  이전 버전과의 호환성을 위해 linear.team_id로 대체됩니다.
  여러 팀이 구성된 경우 Push에는 명시적인 --team이 필요합니다.

가져오기 옵션:
  --milestones       Linear 프로젝트 마일스톤을 로컬 에픽 부모로 재구성

유형 필터링 (--push 전용):
  --type task,feature       이 유형의 이슈만 동기화
  --exclude-type wisp       이 유형의 이슈를 제외
  --include-ephemeral       임시 이슈(wisp 등)를 포함합니다. 기본값은 제외입니다
  --parent TICKET           이 티켓과 하위 항목만 푸시
  --relations               pull 시 Linear 관계를 bd 의존성으로 가져오기

충돌 해결:
  기본적으로는 최신 타임스탬프가 우선합니다. 다음으로 덮어쓰기:
  --prefer-local    항상 로컬 beads 버전을 우선 사용합니다
  --prefer-linear   항상 Linear 버전을 우선 사용합니다

예시:
  bd linear sync --pull                         # Linear에서 가져오기
  bd linear sync --pull-if-stale                # 데이터가 오래된 경우에만 가져오기
  bd linear sync --pull-if-stale --threshold 5m # 5분보다 오래된 경우 가져오기
  bd linear sync --pull --relations             # Linear 차단 관계를 bd deps로 가져오기
  bd linear sync --push --create-only           # 새 이슈만 푸시
  bd linear sync --push --type=task,feature     # 작업 및 기능만 푸시
  bd linear sync --push --exclude-type=wisp     # wisp를 제외한 모든 항목 푸시
  bd linear sync --push --parent=bd-abc123      # 하나의 티켓 트리 푸시
  bd linear sync --dry-run                      # 변경 없이 미리보기
  bd linear sync --prefer-local                 # 양방향, 로컬이 우선

```
bd linear sync [flags]
```

**플래그:**

```
      --create-only            새 이슈만 생성하고 기존 이슈는 업데이트하지 않음
      --dry-run                변경 없이 동기화 미리 보기
      --exclude-type strings   해당 유형의 이슈 제외(반복 가능)
      --include-ephemeral      Linear로 push할 때 임시 이슈(wisp 등) 포함
      --issues string          선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --milestones             pull할 때 Linear 프로젝트 마일스톤을 로컬 epic 상위 이슈로 재구성
      --no-wait                다른 동기화가 실행 중이면 기다리지 않고 즉시 실패
      --parent string          이 beads 티켓과 하위 항목으로 push 제한
      --prefer-linear          충돌 시 Linear 버전 우선
      --prefer-local           충돌 시 로컬 버전 우선
      --pull                   Linear에서 이슈 pull
      --pull-if-stale          Linear 데이터가 오래된 경우에만 pull(최신이면 건너뜀)
      --push                   Linear로 이슈 push
      --relations              pull할 때 Linear 관계를 bd 의존성으로 가져오기
      --state string           동기화할 이슈 상태: open, closed, all(기본값 "all")
      --team strings           동기화할 팀 ID(구성된 team_id/team_ids 재정의)
      --threshold duration     --pull-if-stale의 오래됨 임곗값(기본값 20m)(기본값 20m0s)
      --type strings           해당 유형의 이슈만 동기화(반복 가능)
      --update-refs            Linear 이슈 생성 후 external_ref 업데이트(기본값 true)
```

#### bd linear teams

Linear API 키로 접근 가능한 모든 팀을 나열합니다.

구성에 필요한 팀 ID(UUID)를 찾으려면 이 값을 사용하세요.

예시:
  bd linear teams
  bd config set linear.team_id "12345678-1234-1234-1234-123456789abc"

```
bd linear teams
```

### bd repo

다중 리포지토리 하이드레이션을 위한 다중 리포지토리 지원을 구성하고 관리합니다.

멀티 레포지토리 지원을 통해 여러 beads 저장소의 이슈를 하나의 데이터베이스로 가져와
단일 데이터베이스에 저장해 저장소 간 이슈 추적을 통합합니다.

구성은 'repos' 섹션 아래의 .beads/config.yaml에 저장됩니다:

  repos:
    primary: "."
    additional:
      - ~/beads-planning
      - ~/work-repo

예시:
  bd repo add ~/beads-planning       # 계획 저장소 추가
  bd repo add ../other-repo          # 상대 경로 저장소 추가
  bd repo list                       # 모든 구성된 저장소 표시
  bd repo remove ~/beads-planning    # 경로로 제거
  bd repo sync                       # 모든 구성된 저장소에서 동기화

```
bd repo
```

#### bd repo add

config.yaml의 repos.additional 목록에 저장소 경로를 추가합니다.

경로는 .beads 폴더가 포함된 디렉터리를 가리켜야 합니다.
경로는 절대 경로 또는 상대 경로일 수 있습니다(그대로 저장됩니다).

이는 버전 관리되며 이 저장소의 모든 클론에서 공유되는
.beads/config.yaml을 수정합니다.

```
bd repo add <path> [flags]
```

**플래그:**

```
      --json   JSON 출력
```

#### bd repo list

.beads/config.yaml에 구성된 모든 저장소를 나열합니다.

기본 저장소(항상 ".")와 추가
하이드레이션을 위해 구성된 저장소를 표시합니다.

```
bd repo list [flags]
```

**플래그:**

```
      --json   JSON 출력
```

#### bd repo remove

config.yaml의 repos.additional 목록에서 저장소 경로를 제거합니다.

경로는 추가된 내용과 정확히 일치해야 합니다(예: "~/foo"를 추가했다면,
"/home/user/foo"가 아니라 "~/foo"를 제거해야 합니다).

이 명령은 또한 데이터베이스에서 이전에 하이드레이트된 이슈도
삭제된 저장소에서 온 이슈를 제거합니다.

```
bd repo remove <path> [flags]
```

**플래그:**

```
      --json   JSON 출력
```

#### bd repo sync

모든 구성된 추가 저장소의 이슈를 동기화합니다.

각 추가 저장소마다 issues.jsonl을 읽어
기본 데이터베이스에 원래 접두사와 source_repo 설정을 유지한 채 가져옵니다.
mtime 캐싱을 사용하여 JSONL이 변경되지 않은 저장소는 건너뜁니다.

또한 원격이 구성되어 있으면 Dolt push/pull도 트리거됩니다.

```
bd repo sync [flags]
```

**플래그:**

```
      --json      JSON 출력
      --verbose   상세한 동기화 진행 상황 표시
```

## 기타 명령어:

### bd ado

beads와 Azure DevOps 간에 이슈를 동기화하기 위한 명령어입니다.

구성은 'bd config' 또는 환경 변수를 통해 설정할 수 있습니다:
  ado.org / AZURE_DEVOPS_ORG              - 조직 이름
  ado.project / AZURE_DEVOPS_PROJECT      - 프로젝트 이름(단일)
  ado.projects / AZURE_DEVOPS_PROJECTS    - 프로젝트 이름(쉼표로 구분)
  ado.pat / AZURE_DEVOPS_PAT              - 개인 액세스 토큰
  ado.url / AZURE_DEVOPS_URL              - 사용자 지정 기본 URL(온-프레미스)

```
bd ado
```

#### bd ado projects

설정된 토큰이 접근할 수 있는 Azure DevOps 프로젝트를 나열합니다.

```
bd ado projects
```

#### bd ado pull

Azure DevOps에서 하나 이상의 항목을 가져옵니다.

위치 인수로 bead ID 또는 외부 참조를 받습니다.
동일함: bd ado sync --pull-only --issues &lt;refs&gt;

```
bd ado pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

#### bd ado push

하나 이상의 beads 이슈를 Azure DevOps에 푸시합니다.

위치 인수로 bead ID를 받습니다.
다음과 동일합니다: bd ado sync --push-only --issues &lt;ids&gt;

```
bd ado push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd ado status

현재 Azure DevOps 구성 및 동기화 상태를 표시합니다.

```
bd ado status
```

#### bd ado sync

beads와 Azure DevOps 간의 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- Azure DevOps에서 새/업데이트된 작업 항목을 beads로 가져옵니다
- 로컬 beads 이슈를 Azure DevOps로 푸시합니다

방향을 제한하려면 --pull-only 또는 --push-only를 사용하세요.

필터(--area-path, --iteration-path, --types, --states)는 동기화되는 작업 항목을 제한합니다.
pull에서는 WIQL 쿼리를 제한합니다. push 시에는
--types와 --states는 ADO로 푸시하기 전에 로컬 beads를 필터링합니다. 사용
--no-create를 push할 때 새 ADO 작업 항목 생성을 건너뛰려면(기존 연결 항목만
업데이트). 필터는 config를 통해서도 영구적으로 저장할 수 있습니다:
  ado.filter.area_path, ado.filter.iteration_path,
  ado.filter.types, ado.filter.states
CLI 플래그는 두 값이 모두 설정되면 구성 값을 덮어씁니다.

```
bd ado sync [flags]
```

**플래그:**

```
      --area-path string        ADO 영역 경로로 필터링(예: "Project\Team")
      --bootstrap-match         최초 동기화용 휴리스틱 일치 활성화
      --dry-run                 변경하지 않고 동기화될 항목 표시
      --issues string           선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --iteration-path string   ADO 반복 경로로 필터링(예: "Project\Sprint 1")
      --no-create               어느 방향에서도 새 항목을 생성하지 않음(pull 또는 push)
      --parent string           이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-ado              충돌 시 Azure DevOps 버전 사용
      --prefer-local            충돌 시 로컬 beads 버전 유지
      --prefer-newer            충돌 시 최신 버전 사용(기본값)
      --project strings         동기화할 프로젝트 이름(구성된 project/projects 재정의)
      --pull-only               Azure DevOps에서 이슈만 pull
      --push-only               Azure DevOps로 이슈만 push
      --reconcile               삭제된 항목의 조정 검사 강제
      --states string           쉼표로 구분한 ADO 상태로 필터링(예: "New,Active,Resolved")
      --types string            쉼표로 구분한 작업 항목 유형으로 필터링(예: "Bug,Task,User Story")
```

### bd audit

감사 로그 항목은 .beads/interactions.jsonl에 추가됩니다.

각 줄은 하나의 이벤트입니다. 이 파일은 Git에서 버전 관리되어 다음 용도로 사용됩니다:
- 감사(\"에이전트가 왜 그렇게 했을까요?\")
- 데이터셋 생성(SFT/RL 미세 조정)

항목은 추가 전용입니다. 라벨링은 부모 항목을 참조하는 새로운 "label" 항목을 생성합니다.

```
bd audit
```

#### bd audit label

기존 상호작용을 참조하는 레이블 항목을 추가합니다

```
bd audit label <entry-id> [flags]
```

**플래그:**

```
      --label string    레이블 값(예: "good" 또는 "bad")
      --reason string   레이블 지정 사유
```

#### bd audit record

감사 상호작용 항목을 추가합니다

```
bd audit record [flags]
```

**플래그:**

```
      --error string       오류 문자열(llm_call/tool_call)
      --exit-code int      종료 코드(tool_call)(기본값 -1)
      --issue-id string    관련 이슈 ID(bd-...)
      --kind string        항목 종류(예: llm_call, tool_call, label)
      --model string       모델 이름(llm_call)
      --prompt string      프롬프트 텍스트(llm_call)
      --response string    응답 텍스트(llm_call)
      --stdin              stdin에서 JSON 객체 읽기(audit.Entry 스키마와 일치해야 함)
      --tool-name string   도구 이름(tool_call)
```

### bd blocked

차단된 이슈 보기

```
bd blocked [flags]
```

**플래그:**

```
      --parent string   이 bead/epic의 하위 항목으로 필터링
```

### bd completion

지정된 셸에 대한 bd의 자동 완성 스크립트를 생성합니다.
생성된 스크립트의 사용 방법에 대한 자세한 내용은 각 하위 명령의 도움말을 참조하세요.


```
bd completion
```

#### bd completion bash

bash 셸에 대한 자동 완성 스크립트를 생성합니다.

이 스크립트는 'bash-completion' 패키지에 의존합니다.
아직 설치되지 않은 경우 OS의 패키지 관리자를 통해 설치할 수 있습니다.

현재 셸 세션에 자동 완성을 로드하려면:

	source &lt;(bd completion bash)

매 새 세션마다 보완 기능을 로드하려면 한 번 실행하세요:

#### 리눅스:

	bd completion bash &gt; /etc/bash_completion.d/bd

#### macOS:

	bd completion bash &gt; $(brew --prefix)/etc/bash_completion.d/bd

이 설정이 적용되려면 새 셸을 시작해야 합니다.


```
bd completion bash
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

#### bd completion fish

fish shell용 자동 완성 스크립트를 생성합니다.

현재 셸 세션에 자동 완성 기능을 로드하려면:

	bd completion fish | source

모든 새 세션에서 자동 완성을 로드하려면 한 번 실행하세요:

	bd completion fish &gt; ~/.config/fish/completions/bd.fish

이 설정이 적용되려면 새 셸을 시작해야 합니다.


```
bd completion fish [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

#### bd completion powershell

PowerShell의 자동 완성 스크립트를 생성합니다.

현재 셸 세션에서 자동 완성 기능을 로드하려면:

	bd completion powershell | Out-String | Invoke-Expression

모든 새 세션에서 자동 완성을 로드하려면, 위 명령의 출력을
powershell 프로필에 추가하세요.


```
bd completion powershell [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

#### bd completion zsh

zsh 셸에 대한 자동 완성 스크립트를 생성합니다.

셸 자동 완성이 환경에서 이미 활성화되어 있지 않으면
이를 활성화해야 합니다.  다음을 한 번 실행할 수 있습니다:

	echo "autoload -U compinit; compinit" &gt;&gt; ~/.zshrc

현재 셸 세션에서 completions를 로드하려면:

	source &lt;(bd completion zsh)

모든 새 세션에서 자동 완성을 로드하려면 한 번 실행하세요:

#### 리눅스:

	bd completion zsh &gt; "$&#123;fpath[1]&#125;/_bd"

#### macOS:

	bd completion zsh &gt; $(brew --prefix)/share/zsh/site-functions/_bd

이 설정이 적용되려면 새 셸을 시작해야 합니다.


```
bd completion zsh [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

### bd cook

Cook는 .formula.json 파일을 proto로 변환합니다.

기본적으로 cook는 해결된 formula를 JSON으로 stdout에 출력하여 임시로 사용합니다.
출력은 검사하거나, 파이프 처리하거나, 파일로 저장할 수 있습니다.

두 가지 조리 모드가 사용 가능합니다:

  COMPILE-TIME (기본값, --mode=compile):
    &#123;&#123;variable&#125;&#125; placeholder를 그대로 유지한 상태의 proto를 생성합니다.
    용도: 모델링, 추정, 계약자 인수인계, 계획.
    변수는 치환되지 않습니다 - 출력은 템플릿 구조를 보여줍니다.

  RUNTIME (--mode=runtime 또는 --var 플래그가 제공될 때):
    변수 치환이 완료된 완전히 해석된 proto를 생성합니다.
    사용: pour 전 최종 검증, 정확한 출력 확인용.
    모든 변수가 값이 있어야 합니다 (--var 또는 기본값을 통해).

Formulas는 다음을 지원하는 고수준 워크플로우 템플릿입니다:
  - 기본값과 유효성 검사를 포함한 변수 정의
  - 이슈 계층이 되는 단계 정의
  - 여러 Formula를 결합하기 위한 구성 규칙
  - extends를 통한 상속

--persist 플래그는 proto를 데이터베이스에 쓰는 레거시 동작을 활성화합니다.
이는 동일한 proto를
다시 조리하지 않고 여러 번 재사용하려는 경우에 유용합니다.

대부분의 워크플로에서는 임시 proto를 선호하세요: pour 및 wisp 명령은
formula 이름을 직접 받아 인라인으로 처리합니다.

예시:
  bd cook mol-feature.formula.json                    # 컴파일 타임: vars 유지 &#123;vars&#123;&#125;&#125;
  bd cook mol-feature --var name=auth                 # 런타임: vars 치환
  bd cook mol-feature --mode=runtime --var name=auth  # 명시적 런타임 모드
  bd cook mol-feature --dry-run                       # 단계 미리보기
  bd cook mol-release.formula.json --persist          # 데이터베이스에 쓰기
  bd cook mol-release.formula.json --persist --force  # 기존 항목 대체

출력(기본값):
  해결된 formula의 모든 단계를 포함하는 JSON 표현입니다.

출력 (--persist):
  데이터베이스에서 proto bead를 생성합니다:
  - formula 이름과 일치하는 ID (예: mol-feature)
  - proto 식별용 "template" 레이블
  - 각 단계에 대한 하위 이슈
  - depends_on 관계에 맞는 의존성

```
bd cook <formula-file> [flags]
```

**플래그:**

```
      --dry-run               생성될 항목 미리 보기
      --force                 기존 proto가 있으면 교체(--persist 필요)
      --mode string           cook 모드: compile(자리표시자 유지) 또는 runtime(변수 치환)
      --persist               proto를 데이터베이스에 영구 저장(레거시 동작)
      --prefix string         proto ID 앞에 붙일 접두사(예: 'gt-'는 'gt-mol-feature' 생성)
      --search-path strings   formula 상속을 검색할 추가 경로
      --var stringArray       변수 치환(key=value), 런타임 모드 활성화
```

### bd defer

이슈를 나중에 처리할 수 있도록 보류합니다.

연기된 이슈는 특정한 어떤 항목에도 특별히 막히지 않고 의도적으로 보류됩니다,
향후 고려를 위해 단순히 미루어 둔 것입니다. 차단된 이슈와 달리, 이들을
작업하지 못하게 막는 의존성이 없습니다. 닫힌 이슈와 달리, 이슈는
다시 검토될 것입니다.

연기된 이슈는 'bd ready'에는 표시되지 않지만 'bd list'에는 여전히 표시됩니다.

예시:
  bd defer bd-abc                  # 단일 이슈를 연기합니다 (상태 기반)
  bd defer bd-abc --until=tomorrow # 특정 시간까지 연기합니다
  bd defer bd-abc --reason="API 접근 대기 중"
  bd defer bd-abc bd-def           # 여러 이슈를 연기합니다

```
bd defer [id...] [flags]
```

**플래그:**

```
      --reason string   이 이슈를 연기하는 이유 기록(notes에 추가)
      --until string    특정 시간까지 연기(예: +1h, tomorrow, next monday)
```

### bd formula

워크플로우 수식 관리 - molecule 템플릿의 소스 계층입니다.

Formulas는 조합 규칙으로 워크플로우를 정의하는 TOML/JSON 파일입니다.
Formula를 정의하고 이를 proto로 cook한 뒤, 그 다음 pour 또는 wisp를 이용해 work로 이동합니다.

검색 경로 (순서):
  1. &lt;resolved-beads-dir&gt;/formulas/ (활성 프로젝트)
  2. &lt;checkout-root&gt;/.beads/formulas/ (저장소 로컬 formulas)
  3. ~/.beads/formulas/ (사용자)
  4. $GT_ROOT/.beads/formulas/ (공유 작업공간 루트, GT_ROOT가 설정된 경우)

명령:
  list   모든 검색 경로의 사용 가능한 formula를 나열
  show   formula의 세부 정보, 단계 및 구성 규칙을 표시

```
bd formula
```

#### bd formula convert

JSON에서 formula 파일을 TOML 형식으로 변환합니다.

TOML 형식이 더 나은 사용성을 제공합니다:
  - \n 이스케이프 없이 다중 줄 문자열
  - 사람이 읽기 쉬운 diff
  - 주석 허용

convert 명령은 .formula.json 파일을 읽고 .formula.toml을 출력합니다.
원본 JSON 파일은 유지됩니다(삭제하려면 --delete를 사용하세요).

예시:
  bd formula convert shiny              # shiny.formula.json을 .toml로 변환
  bd formula convert ./my.formula.json  # 특정 파일을 변환
  bd formula convert --all              # 모든 JSON formula를 변환
  bd formula convert shiny --delete     # 변환하고 JSON 파일 제거
  bd formula convert shiny --stdout     # TOML을 stdout으로 출력

```
bd formula convert <formula-name|path> [--all] [flags]
```

**플래그:**

```
      --all      모든 JSON formula 변환
      --delete   변환 후 JSON 파일 삭제
      --stdout   파일 대신 stdout에 TOML 출력
```

#### bd formula list

검색 경로에서 모든 formula를 나열합니다.

검색 경로(우선순위 순):
  1. &lt;resolved-beads-dir&gt;/formulas/ (활성 프로젝트 - 최우선 순위)
  2. &lt;checkout-root&gt;/.beads/formulas/ (레포지토리 로컬 formulas)
  3. ~/.beads/formulas/ (사용자)
  4. $GT_ROOT/.beads/formulas/ (공유 워크스페이스 루트, GT_ROOT가 설정된 경우)

이전 경로의 Formula는 나중 경로에 있는 동일한 이름의 Formula를 가립니다.

예제:
  bd formula list
  bd formula list --json
  bd formula list --type workflow
  bd formula list --type convoy

```
bd formula list [flags]
```

**플래그:**

```
      --type string   유형으로 필터링(workflow, expansion, aspect, convoy)
```

#### bd formula show

formula에 대한 자세한 정보를 표시합니다.

표시:
  - Formula 메타데이터 (이름, 유형, 설명)
  - 기본값과 제약 조건을 가진 변수
  - 종속성이 있는 단계
  - 구성 규칙 (extends, aspects, expansions)
  - 외부 구성을 위한 결합 지점

예시:
  bd formula show shiny
  bd formula show rule-of-five
  bd formula show security-audit --json

```
bd formula show <formula-name>
```

### bd github

Beads와 GitHub 간의 이슈를 동기화하기 위한 명령어들입니다.

구성은 'bd config' 또는 환경 변수로 설정할 수 있습니다:
  github.token / GITHUB_TOKEN           - 개인 액세스 토큰
  github.owner / GITHUB_OWNER           - 저장소 소유자
  github.repo / GITHUB_REPO             - 저장소 이름
  github.repository / GITHUB_REPOSITORY - 결합된 "owner/repo" 형식
  github.url / GITHUB_API_URL           - 사용자 지정 API URL (GitHub Enterprise)

```
bd github
```

#### bd github pull

GitHub에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
동일함: bd github sync --pull-only --issues &lt;refs&gt;

```
bd github pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

#### bd github push

GitHub로 하나 이상의 beads 이슈를 푸시합니다.

bead ID를 위치 인수로 받습니다.
동일함: bd github sync --push-only --issues &lt;ids&gt;

```
bd github push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd github repos

구성된 토큰이 접근할 수 있는 GitHub 저장소를 나열합니다.

```
bd github repos
```

#### bd github status

현재 GitHub 구성 및 동기화 상태를 표시합니다.

```
bd github status
```

#### bd github sync

beads와 GitHub 사이의 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- GitHub에서 새/수정된 이슈를 beads로 가져옵니다
- 로컬 beads 이슈를 GitHub로 푸시합니다

방향을 제한하려면 --pull-only 또는 --push-only를 사용합니다.

```
bd github sync [flags]
```

**플래그:**

```
      --dry-run         변경하지 않고 동기화될 항목 표시
      --issues string   선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string   이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-github   충돌 시 GitHub 버전 사용
      --prefer-local    충돌 시 로컬 beads 버전 유지
      --prefer-newer    충돌 시 최신 버전 사용(기본값)
      --pull-only       GitHub에서 이슈만 pull
      --push-only       GitHub로 이슈만 push
```

### bd gitlab

beads와 GitLab 사이의 이슈를 동기화하기 위한 명령어입니다.

구성은 'bd config' 또는 환경 변수로 설정할 수 있습니다:
  gitlab.url / GITLAB_URL                         - GitLab 인스턴스 URL
  gitlab.token / GITLAB_TOKEN                     - 개인 액세스 토큰
  gitlab.project_id / GITLAB_PROJECT_ID           - 프로젝트 ID 또는 경로
  gitlab.group_id / GITLAB_GROUP_ID               - 그룹 수준 동기화를 위한 그룹 ID
  gitlab.default_project_id / GITLAB_DEFAULT_PROJECT_ID - 그룹 모드에서 이슈 생성 대상 프로젝트

```
bd gitlab
```

#### bd gitlab projects

설정된 토큰이 액세스할 수 있는 GitLab 프로젝트를 나열합니다.

```
bd gitlab projects
```

#### bd gitlab pull

GitLab에서 하나 이상의 항목을 가져옵니다.

위치 인수로 bead ID 또는 외부 참조를 받습니다.
다음과 동일합니다: bd gitlab sync --pull-only --issues &lt;refs&gt;

```
bd gitlab pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

#### bd gitlab push

하나 이상의 beads 이슈를 GitLab으로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 동일합니다: bd gitlab sync --push-only --issues &lt;ids&gt;

```
bd gitlab push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd gitlab status

현재 GitLab 구성과 동기화 상태를 표시합니다.

```
bd gitlab status
```

#### bd gitlab sync

Beads와 GitLab 사이의 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- GitLab에서 새로 생성/업데이트된 이슈를 beads로 가져옵니다
- 로컬 beads 이슈를 GitLab로 푸시합니다

방향을 제한하려면 --pull-only 또는 --push-only를 사용하세요.

```
bd gitlab sync [flags]
```

**플래그:**

```
      --assignee string       담당자 사용자 이름으로 필터링
      --dry-run               변경하지 않고 동기화될 항목 표시
      --exclude-type string   동기화에서 해당 이슈 유형 제외(쉼표로 구분)
      --issues string         선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --label string          레이블로 필터링(쉼표로 구분, AND 논리)
      --milestone string      마일스톤 제목으로 필터링
      --no-ephemeral          push에서 임시/wisp 이슈 제외(기본값: true)(기본값 true)
      --parent string         이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-gitlab         충돌 시 GitLab 버전 사용
      --prefer-local          충돌 시 로컬 beads 버전 유지
      --prefer-newer          충돌 시 최신 버전 사용(기본값)
      --project string        이 프로젝트 ID의 이슈로 필터링(그룹 모드)
      --pull-only             GitLab에서 이슈만 pull
      --push-only             GitLab으로 이슈만 push
      --type string           해당 이슈 유형만 동기화(쉼표로 구분, 예: 'epic,feature,task')
```

### bd help

Help는 애플리케이션의 모든 명령에 대한 도움말을 제공합니다.
자세한 내용을 확인하려면 bd help [명령 경로]를 입력하세요.

```
bd help [command] [flags]
```

**플래그:**

```
      --all                   모든 명령의 도움말을 단일 문서에 표시
      --doc string            단일 명령의 Markdown 문서 생성
      --docs-root string      이 루트 아래에 저장소 CLI 문서 생성
      --docs-version string   버전별 웹사이트 CLI 참조 하나도 갱신(예: 1.0.5)
  -h, --help                  help 명령의 도움말
      --list                  사용 가능한 모든 명령 나열
```

### bd init-safety

bd init 플래그의 안전성 계약.

모든 bd init 호출은 정확히 하나의 명시적으로 지정된
소스(로컬 재초기화, 원격 채택, 또는 새로 생성)에서 project_id를 해결합니다. 소스가
애매하면 bd init은 거부됩니다.

플래그 표면

  bd init                       새 신원을 생성합니다. 다음을 기준으로 부트스트랩합니다
                                origin에 refs/dolt/data가 있으면.

  bd init --reinit-local        기존 로컬 데이터 위에 local .beads/를 다시 초기화합니다
                                원격 기록 삭제는 허용되지 않습니다.
                                origin에 Dolt 데이터가 있으면
                                이 동작은 거부됩니다 — --discard-remote와 함께
                                사용해 덮어쓰세요.

  bd init --reinit-local \      원격의 Dolt 히스토리를 폐기하고
      --discard-remote          이를 로컬 재초기화로 대체합니다. 이후에
                                첫 번째 bd dolt push는
                                히스토리를 교체하는 강제 푸시가 됩니다.

  bd init --force               --reinit-local의 더 이상 사용되지 않는 별칭입니다.
                                2개 이상의 릴리스 동안 동작이 유지됩니다.

  bd init --from-jsonl          설정된 import.path에서 가져옵니다. 만약
                                origin에 Dolt 데이터가 있으면, 이 명령은
                                --discard-remote가 해당 원격 히스토리 교체를
                                허용하지 않으면 거부됩니다.

원격 채택

  원격의 기존 히스토리를 사용하려면 다음을 사용하세요:

      bd bootstrap

  원격이 감지되면 bd init이 자동으로 이것을 제안합니다.

DESTROY-TOKEN (비대화형 전용)

  TTY가 없는 상태로 실행할 때(CI, 에이전트, 파이프 입력)에는 --discard-remote가
  명시적인 --destroy-token 값이 필요합니다. 토큰 형식은:

      DESTROY-&lt;issue-prefix&gt;

  예를 들어, 이슈 접두사가 "bd"라면 토큰은 "DESTROY-bd"입니다:

      bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd

  대화형(TTY) 모드에서는 입력한 프롬프트를 통해 대신 확인합니다. 토큰은
  bd의 런타임 오류 메시지에 출력되지 않습니다 — 이는
  패턴 일치 한 줄 명령을 의도적으로 방지하기 위한 보안 조치입니다 (참고:
  docs/adr/0002-init-safety-invariants.md).

종료 코드

  10    거부: 원격에 Dolt 이력이 있고 로컬 이력을 선택했지만
        --discard-remote를 지정하지 않음
  11    거부: 기존 로컬 데이터가 있고 삭제 확인을 거부함
  12    거부: 유효한 --destroy-token 없이 --discard-remote를 전달함
        (비대화형 모드)

복구

  거절이 발생하면 각 종료 코드에 대한 단계별 복구
  플레이북은 docs/RECOVERY.md에서 확인하세요.


```
bd init-safety
```

### bd mail

메일 작업을 외부 메일 제공자에게 위임합니다.

에이전트들은 beads로 작업할 때 종종 'bd mail'을 입력하지만,
메일 기능은 일반적으로 오케스트레이터가 제공합니다. 이 명령은
설정된 메일 제공자에게 위임하여 그 간극을 메웁니다.

구성(확인 순서):
  1. BEADS_MAIL_DELEGATE 또는 BD_MAIL_DELEGATE 환경 변수
  2. 'mail.delegate' 구성 설정 (bd config set mail.delegate "gt mail")

예제:
  # 위임 구성 (일회성 설정)
  `export BEADS_MAIL_DELEGATE="gt mail"`
  # 또는
  bd config set mail.delegate "gt mail"

  # 그런 다음 bd mail을 gt mail처럼 사용하세요
  bd mail inbox                    # 인박스 목록
  bd mail send mayor/ -s "안녕하세요"      # 메일을 보냅니다
  bd mail read msg-123             # 메시지를 읽습니다

```
bd mail [subcommand] [args...]
```

### bd metrics

익명 사용 지표가 켜져 있는지 확인하고, 전송되는 내용을 정확히 확인하고,
켜거나 끌 수 있습니다.

bd는 사람들이 실제로 그것을 어떻게 사용하는지 알기 위해 익명 사용 지표를 공유합니다 — 단지
실행되는 명령어와 bd 버전 및 OS 플랫폼을 포함합니다. 이것이 우리가 다음으로 다듬을 항목을
결정하는 방법입니다. 우리는 여러분의 이슈, 경로, 리모트, 신원 또는
사용자가 제공한 어떤 텍스트도 수집하지 않습니다.

  bd metrics            현재 상태와 수집되는 항목을 표시합니다
  bd metrics on         메트릭을 켭니다
  bd metrics off        메트릭을 끕니다
  bd metrics example    bd가 전송하는 이벤트의 실제 예시를 표시합니다

```
bd metrics
```

#### bd metrics example

bd가 익명 메트릭을 보내는 실제 예시를 표시합니다

```
bd metrics example
```

#### bd metrics off

익명 사용 메트릭 비활성화

```
bd metrics off
```

#### bd metrics on

익명 사용 지표를 켭니다

```
bd metrics on
```

### bd mol

molecules 관리 - 에이전트 워크플로우를 위한 작업 템플릿.

Protos는 "template" 레이블이 붙은 템플릿 에픽입니다. 그것들은 실제 이슈(분자)를 생성하기 위해
생성될 수 있는 작업의 DAG를 정의합니다.

분자 은유:
  - 프로토는 미인스턴스화된 템플릿(재사용 가능한 작업 패턴)입니다
  - 스폰닝은 프로토에서 분자(실제 이슈)를 생성합니다
  - 변수 (&#123;&#123;key&#125;&#125;)는 스폰닝 중에 대체됩니다
  - 결합은 프로토 또는 분자를 화합물로 결합합니다
  - 증류는 임시 에픽에서 프로토를 추출합니다

명령어:
  show       proto/molecule 구조와 변수를 표시합니다
  pour       proto를 영구 mol(액체 상태)로 인스턴스화
  wisp       proto를 일회성 wisp(기체 상태)로 인스턴스화
  bond       다형성 결합: proto+proto, proto+mol, mol+mol
  squash     분자를 digest로 응축
  burn       wisp를 폐기
  distill    임시 epic에서 proto를 추출

사용 가능한 공식 목록을 나열하려면 "bd formula list"를 사용하세요.

```
bd mol
```

**별칭:** protomolecule

#### bd mol bond

두 개의 protos 또는 분자를 결합하여 화합물을 생성합니다.

bond 명령은 다형적입니다 - 다양한 피연산자 유형을 처리합니다:

  formula + formula → 둘 다 처리하고, proto를 복합화
  formula + proto   → formula를 처리하고, proto를 복합화
  formula + mol     → formula를 처리하고 스폰 후 분자에 부착
  proto + proto     → proto를 복합화 (재사용 가능한 템플릿)
  proto + mol       → proto를 스폰하고 분자에 부착
  mol + proto       → proto를 스폰하고 분자에 부착
  mol + mol         → 결합해 복합 분자를 생성

Formula 이름(예: mol-polecat-arm)은 임시 proto로 인라인에서 조리됩니다.
이는 데이터베이스에서 사전 조리된 proto beads를 사용할 필요가 없게 합니다.

결합 유형:
  sequential (기본값) - B는 A가 완료된 후 실행됩니다
  parallel            - B는 A와 병렬로 실행됩니다
  conditional         - B는 A가 실패할 경우에만 실행됩니다

단계 제어:
  기본적으로 생성된 proto는 대상의 phase를 따른다:
  - mol에 연결할 때 (Ephemeral=false) → 영구로 생성됨 (Ephemeral=false)
  - 임시 이슈에 연결할 때 (Ephemeral=true) → 임시로 생성됨 (Ephemeral=true)

  다음으로 대체:
  --pour  액체 상태로 강제 생성 (영구, Ephemeral=false)
  --ephemeral  기체 상태로 강제 생성 (일시적, Ephemeral=true, dolt_ignore를 통해 Dolt 동기화에서 제외)

동적 바인딩(크리스마스 오너먼트 패턴):
  --ref를 사용해 변수 치환이 적용된 사용자 지정 하위 참조를 지정합니다.
  이는 임의의 해시 대신 "parent.child-ref"와 같은 ID를 생성합니다.

  예시:
    bd mol bond mol-worker-arm bd-patrol --ref arm-&#123;&#123;worker_name&#125;&#125; --var worker_name=ace
    # 생성됨: bd-patrol.arm-ace (및 bd-patrol.arm-ace.capture와 같은 하위 항목)

사용 사례:
  - 순찰 중에 중요한 버그를 발견했나요? 그 버그를 영구적으로 기록하려면 --pour를 사용하세요
  - 영구 기능에서 일시적인 진단이 필요하신가요? --ephemeral 사용
  - 순찰에서 작업자별 arm를 생성하나요? 가독성 있는 ID를 위해 --ref 사용

예시:
  bd mol bond mol-feature mol-deploy                    # 복합 프로토
  bd mol bond mol-feature mol-deploy --type parallel    # 병렬 실행
  bd mol bond mol-feature bd-abc123                     # 분자에 프로토 연결
  bd mol bond bd-abc123 bd-def456                       # 두 분자 결합
  bd mol bond mol-critical-bug wisp-patrol --pour       # 발견된 버그 영구 저장
  bd mol bond mol-temp-check bd-feature --ephemeral          # 임시 진단
  bd mol bond mol-arm bd-patrol --ref arm-&#123;&#123;name&#125;&#125; --var name=ace  # 동적 하위 ID

```
bd mol bond <A> <B> [flags]
```

**별칭:** fart

**플래그:**

```
      --as string         compound proto의 사용자 정의 제목(proto+proto 전용)
      --dry-run           생성될 항목 미리 보기
      --ephemeral         기체로 스폰 강제(임시, Ephemeral=true)
      --pour              액체로 스폰 강제(영구, Ephemeral=false)
      --ref string        {{var}} 치환이 있는 사용자 정의 하위 참조(예: arm-{{polecat_name}})
      --type string       bond 유형: sequential, parallel 또는 conditional(기본값 "sequential")
      --var stringArray   스폰된 proto의 변수 치환(key=value)
```

#### bd mol burn

분자를 삭제하면서 다이제스트를 생성하지 않습니다.

삭제 전에 영구 다이제스트를 생성한 뒤 삭제하는 squash와는 달리 burn
는 분자를 완전히 제거해 흔적을 남기지 않습니다. 다음과 같이 사용합니다:
  - 버려진 patrol 사이클
  - 크래시되었거나 실패한 워크플로우
  - 보존하지 않으려는 테스트/디버그 molecule

소각 작업은 분자 단계에 따라 다릅니다:
  - Wisp (일시적): 직접 삭제
  - Mol (영구적): 연쇄 삭제(원격으로 동기화)

주의: 이는 파괴적인 작업입니다. molecule의 데이터는
영구적으로 손실됩니다. 요약을 보존하려면 'bd mol squash'를 사용하세요.

예시:
  bd mol burn bd-abc123              # 흔적 없이 분자 삭제
  bd mol burn bd-abc123 --dry-run    # 삭제될 항목 미리보기
  bd mol burn bd-abc123 --force      # 확인 건너뛰기
  bd mol burn bd-a1 bd-b2 bd-c3      # 여러 wisp를 한 번에 삭제

```
bd mol burn <molecule-id> [molecule-id...] [flags]
```

**플래그:**

```
      --dry-run   삭제될 항목 미리 보기
      --force     확인 프롬프트 건너뛰기
```

#### bd mol current

molecule 워크플로우에서 현재 위치를 표시합니다.

molecule-id가 주어지면 해당 분자의 상태를 표시합니다.
주어지지 않으면 현재 agent에 할당된 in_progress 이슈에서 유추합니다.

이 출력은 상태 표시기가 있는 모든 단계를 보여줍니다:
  [done]     - 단계가 완료되었습니다 (닫힘)
  [current]  - 단계가 진행 중입니다 (여기가 여기입니다)
  [ready]    - 단계가 시작할 준비가 되어 있습니다 (차단되지 않음)
  [blocked]  - 단계가 의존성에 의해 차단됩니다
  [pending]  - 단계가 대기 중입니다

대형 분자(&gt;100 단계)의 경우, 대신 요약이 표시됩니다.
특정 단계를 보려면 --limit 또는 --range를 사용하십시오:
  bd mol current &lt;id&gt; --limit 50       # 처음 50단계 표시
  bd mol current &lt;id&gt; --range 100-150  # 100-150단계 표시

```
bd mol current [molecule-id] [flags]
```

**플래그:**

```
      --for string     특정 에이전트/담당자의 molecule 표시
      --limit int      표시할 최대 단계 수(0 = 자동, 'all' 임곗값 사용)
      --range string   특정 단계 범위 표시(예: '1-50', '100-150')
```

#### bd mol distill

기존 에픽에서 재사용 가능한 공식(formula)을 추출해 분자를 증류합니다.

이는 pour의 반대입니다: formula → molecule 대신 molecule → formula입니다.

distill 명령은 다음과 같습니다:
  1. 기존 에픽과 모든 하위 항목을 로드합니다
  2. 구조를 .formula.json 파일로 변환합니다
  3. --var 플래그로 구체적인 값을 &#123;&#123;variable&#125;&#125; 플레이스홀더로 바꿉니다

사용 사례:
  - 팀이 좋은 워크플로를 자연스럽게 개발해 재사용하고자 하는 경우
  - 실행 가능한 템플릿으로 팀의 노하우를 캡처
  - 유사한 미래 작업의 시작점 생성

변수 구문(둘 다 작동함 - 어떤 쪽이 구체적인 값인지 감지함):
  --var branch=feature-auth    생성 스타일: variable=value (권장)
  --var feature-auth=branch    치환 스타일: value=variable

출력 위치(쓰기 가능한 첫 번째 항목이 우선됨):
  1. &lt;resolved-beads-dir&gt;/formulas/ (프로젝트 수준, 기본값)
  2. &lt;checkout-root&gt;/.beads/formulas/ (레포지토리 로컬 formulas)
  3. ~/.beads/formulas/     (사용자 수준, 프로젝트가 쓰기 가능하지 않은 경우)

예시:
  bd mol distill bd-o5xe my-workflow
  bd mol distill bd-abc release-workflow --var feature_name=auth-refactor

```
bd mol distill <epic-id> [formula-name] [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --output string     formula 파일 출력 디렉터리
      --var stringArray   값을 {{variable}} 자리표시자로 교체(variable=value)
```

#### bd mol last-activity

분자에 대한 가장 최근 활동 타임스탬프를 표시합니다.

molecule의 어떤 step이든 가장 최근에 변경된 타임스탬프를 반환하여,
오래되었거나 멈춘 molecule을 쉽게 감지할 수 있게 합니다.

활동 소스:
  step_closed      - 단계가 닫혔습니다
  step_updated     - 단계가 업데이트되었습니다(할당됨, 편집됨 등)
  molecule_updated - molecule 루트 자체가 업데이트되었습니다

예시:
  bd mol last-activity hq-wisp-0laki
  bd mol last-activity hq-wisp-0laki --json

```
bd mol last-activity <molecule-id>
```

#### bd mol pour

프로토를 영구적인 mol에 붓는다 - 녹은 금속을 거푸집에 부어 넣는 것처럼.

이는 템플릿을 통해 PERSISTENT 작업을 만드는 화학에서 영감을 받은 명령입니다.
결과 mol은 .beads/(영구 저장소)에 존재하며 git과 동기화됩니다.

상태 전이: Proto (고체) -&gt; pour -&gt; Mol (액체)

POUR와 WISP를 사용하는 경우:
  pour (liquid): 감사 추적이 필요한 지속적인 작업
    - 여러 세션에 걸친 기능 구현
    - 나중에 참조해야 할 수 있는 작업
    - Git 이력에 보존할 가치가 있는 모든 항목

  wisp (vapor): 자동으로 정리되는 임시 작업
    - 릴리스 워크플로우(일회성 실행)
    - 운영 루프 및 반복 주기
    - 상태 검사 및 진단
    - 감사 가치가 없는 모든 운영 워크플로우

팁: Formula는 phase:"vapor"를 지정하여 wisp 사용을 권장할 수 있습니다.
     기체 단계 formula를 배치하면 경고가 표시됩니다.

예시:
  bd mol pour mol-feature --var name=auth    # 지속적인 기능 작업
  bd mol pour mol-review --var pr=123        # 지속적인 코드 리뷰

```
bd mol pour <proto-id> [flags]
```

**플래그:**

```
      --assignee string      루트 이슈를 이 에이전트/사용자에게 할당
      --attach strings       스폰 후 연결할 proto(반복 가능)
      --attach-type string   연결할 bond 유형: sequential, parallel 또는 conditional(기본값 "sequential")
      --dry-run              생성될 항목 미리 보기
      --var stringArray      변수 치환(key=value)
```

#### bd mol progress

분자에 대한 효율적인 진행 상황 요약을 표시합니다.

이 명령은 모든 단계를 로드하지 않고 인덱스 쿼리를 사용하여 진행 상태를 계산하므로,
매우 큰 분자(수백만 단계)에서도 적합합니다.

molecule-id가 지정되지 않은 경우, 작업 중인 어떤 분자의 진행 상태를 표시합니다.

출력 항목:
  - 진행률: 완료 / 전체 (퍼센트)
  - 현재 단계: 진행 중인 단계(있는 경우)
  - 속도: 마감 시간을 기반으로 한 steps/hour
  - ETA: 완료까지 예상 시간

예시:
  bd mol progress bd-hanoi-xyz

```
bd mol progress [molecule-id]
```

#### bd mol ready

게이트가 닫혔고 워크플로우가 재개할 준비가 된 분자들을 찾습니다.

이 명령은 게이트 단계에서 대기 중인 분자를 다음 조건에서 찾아냅니다:
1. 분자에 단계를 차단하는 게이트 비드가 있다
2. 게이트 비드가 이제 닫혀 있다(조건 충족)
3. 차단된 단계가 이제 진행할 준비가 되었다
4. 현재 어떤 에이전트도 이 분자를 훅하지 않았다

이것은 명시적인 waiter 추적 없이 discovery-based resume를 가능하게 합니다.
패트롤 시스템은 이를 사용해 gate-ready 분자를 찾아 배포합니다.

예시:
  bd mol ready --gated           # 게이트 준비 분자 모두 찾기
  bd mol ready --gated --json    # 자동화를 위한 JSON 출력

```
bd mol ready --gated
```

#### bd mol seed

포뮬러에 접근할 수 있고 빌드할 수 있는지 확인합니다.

seed 명령은 formula 검색 경로를 검사하여 formula가 존재하고
그리고 로드될 수 있는지 확인합니다. 이는 formula에서 작업을
시작하기 전에 시스템 상태를 확인하는 데 유용합니다.

수식 검색 경로(순서대로 확인):
  1. &lt;resolved-beads-dir&gt;/formulas/ (활성 프로젝트)
  2. &lt;checkout-root&gt;/.beads/formulas/ (저장소 로컬 수식)
  3. ~/.beads/formulas/ (사용자 수준)
  4. $GT_ROOT/.beads/formulas/ (공유 워크스페이스 루트, GT_ROOT가 설정된 경우)

예시:
  bd mol seed mol-feature                 # 특정 수식 검증
  bd mol seed mol-review --var name=test  # 변수 치환으로 검증

```
bd mol seed <formula-name> [flags]
```

**플래그:**

```
      --var stringArray   조건 필터링용 변수 치환(key=value)
```

#### bd mol show

분자 구조와 세부 정보를 표시합니다.

--parallel 플래그는 병렬 처리 가능한 단계를 강조 표시합니다:
  - 차단하는 의존성이 없는 단계는 병렬로 실행할 수 있습니다
  - 어떤 단계가 지금 바로 시작할 수 있는지 표시합니다
  - 병렬 그룹(동시에 실행될 수 있는 단계)을 식별합니다

예시:
  bd mol show bd-patrol --parallel

```
bd mol show <molecule-id> [flags]
```

**플래그:**

```
  -p, --parallel   병렬 단계 분석 표시
```

#### bd mol squash

분자의 임시 자식들을 하나의 다이제스트 이슈로 스쿼시합니다.

이 명령은 molecule의 모든 임시 하위 이슈(Ephemeral=true)를 수집하고,
요약 다이제스트를 생성한 뒤 wisps를 영구 항목으로 승격하기 위해
그들의 Wisp 플래그를 해제합니다(또는 선택적으로 삭제).

squash 작업:
  1. molecule과 모든 하위 항목을 로드합니다
  2. wisps만 필터링합니다 (Ephemeral=true인 임시 이슈)
  3. digest를 생성합니다 (작업 완료 요약)
  4. 영구적인 digest 이슈를 생성합니다 (Ephemeral=false)
  5. 자식 항목의 Wisp 플래그를 해제합니다 (영구 항목으로 승격)
     또는 --keep-children로 유지합니다 (기본값: delete)

에이전트 통합:
--summary를 사용하면 AI가 생성한 요약을 제공합니다. 이는 bd를 순수한
도구로 유지합니다. 호출 에이전트(오케스트레이터 워커, Claude Code, 등)가
지능적인 요약을 생성할 책임이 있습니다. --summary가 없으면 기본적으로 자식
이슈 내용의 기본 연결이 사용됩니다.

이는 wisp 워크플로의 일부입니다. spawn이 wisps를 생성하고,
실행이 진행되면 squash가 흔적을 결과물(digest)로 압축합니다.

예시:
  bd mol squash bd-abc123                    # 자식 항목을 squash하고 승격
  bd mol squash bd-abc123 --dry-run          # squash될 항목을 미리 보기
  bd mol squash bd-abc123 --keep-children    # digest 후 wisps 유지
  bd mol squash bd-abc123 --summary "에이전트가 생성한 완료된 작업 요약"

```
bd mol squash <molecule-id> [flags]
```

**플래그:**

```
      --dry-run          squash될 항목 미리 보기
      --keep-children    squash 후 임시 하위 이슈를 삭제하지 않음
      --summary string   에이전트 제공 요약(자동 생성 우회)
```

#### bd mol stale

완료되었지만 여전히 열려 있는 분자(자식이 있는 에픽)를 감지합니다.

molecule은 다음 조건을 충족하면 stale로 간주됩니다:
  1. 모든 하위 항목이 닫힘(Completed == Total)
  2. 루트 이슈가 아직 열려 있음
  3. 누구에게도 할당되지 않음(선택 사항, --unassigned 사용)
  4. 다른 작업을 차단함(선택 사항, --blocking 사용)

기본적으로 닫히지 않았지만 완료된 모든 분자를 표시합니다.

예시:
  bd mol stale              # 모든 오래된 분자 나열
  bd mol stale --json       # 기계 판독 가능한 출력
  bd mol stale --blocking   # 다른 작업을 차단하는 분자만 표시
  bd mol stale --unassigned # 할당되지 않은 분자만 표시
  bd mol stale --all        # 자식이 0개인 분자 포함

```
bd mol stale [flags]
```

**플래그:**

```
      --all          하위 이슈가 0개인 molecule 포함
      --blocking     다른 작업을 차단하는 molecule만 표시
      --unassigned   할당되지 않은 molecule만 표시
```

#### bd mol wisp

운영 워크플로우를 위한 EPHEMERAL 분자인 wisps를 생성하거나 관리합니다.

proto-id 인수로 호출되면 해당 proto로부터 wisp를 생성합니다.
하위 명령(list, gc)으로 호출되면 기존 wisp를 관리합니다.

Wisps는 주 데이터베이스에서 Ephemeral=true인 이슈입니다. 로컬에 저장되지만
git을 통해 동기화되지 않습니다.

WISP와 POUR 사용 시기:
  wisp (vapor): 자동으로 정리되는 임시 작업
    - 릴리스 워크플로우(일회성 실행)
    - 운영 루프 및 반복 주기
    - 헬스 체크 및 진단
    - 감사 가치가 없는 모든 운영 워크플로우

  pour (liquid): 감사 추적이 필요한 지속적인 작업
    - 여러 세션에 걸친 기능 구현
    - 나중에 참조가 필요할 수 있는 작업
    - git 히스토리에 보존할 가치가 있는 모든 것

팁: Formula는 phase:"vapor"를 지정하여 wisp 사용을 권장할 수 있습니다.
     기체 단계 formula에 pour를 사용하면 경고가 표시됩니다.

wisp 수명 주기:
  1. 생성: bd mol wisp &lt;proto&gt; 또는 bd create --ephemeral
  2. 실행: 일반 bd 작업은 wisp 이슈에서 작동합니다
  3. 스쿼시: bd mol squash &lt;id&gt; (Ephemeral 플래그를 해제하고 영구 상태로 승격)
  4. 또는 소각: bd mol burn &lt;id&gt; (다이제스트를 생성하지 않고 삭제)

예시:
  bd mol wisp beads-release --var version=1.0  # 릴리스 워크플로우
  bd mol wisp mol-my-workflow                  # 임시 운영 사이클
  bd mol wisp list                             # 모든 wisp 나열
  bd mol wisp gc                               # 오래된 wisp 정리

하위 명령어:
  list  현재 컨텍스트의 모든 wisps를 나열합니다
  gc    고아 wisps를 가비지 컬렉션합니다

```
bd mol wisp [proto-id] [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --root-only         루트 이슈만 생성(하위 단계 이슈 없음)
      --var stringArray   변수 치환(key=value)
```

##### bd mol wisp create

프로토에서 한 줄기 연무를 생성합니다 - 고체가 증기로 승화되는 과정입니다.

이것은 템플릿에서 일시적인 작업을 생성하기 위한 화학에서 영감을 받은 명령입니다.
결과 wisp는 Ephemeral=true로 메인 데이터베이스에 저장되며 git을 통해 동기화되지 않습니다.

상전이: Proto (고체) -&gt; Wisp (증기)

wisp는 다음 용도로 사용합니다:
  - 운영 루프 및 반복 주기
  - 상태 점검 및 모니터링
  - 일회성 오케스트레이션 실행
  - 감사 가치가 없는 정기 작업

wisp는:
  - Ephemeral=true 플래그와 함께 메인 데이터베이스에 저장됩니다
  - git을 통해 동기화되지 않습니다
  - 증발(연소)하거나 다이제스트로 응축(squash)됩니다

예제:
  bd mol wisp create mol-patrol                    # 일시적인 순찰 주기
  bd mol wisp create mol-health-check              # 일회성 상태 점검
  bd mol wisp create mol-diagnostics --var target=db  # 진단 실행

```
bd mol wisp create <proto-id> [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --root-only         루트 이슈만 생성(하위 단계 이슈 없음)
      --var stringArray   변수 치환(key=value)
```

##### bd mol wisp gc

데이터베이스에서 오래되었거나 방치된 wisps를 가비지 수집합니다.

Wisp가 다음 조건에서 버려진 것으로 간주됩니다:
  - --age 기간 동안 업데이트되지 않았고 닫히지 않은 경우

버려진 wisps는 다이제스트를 생성하지 않고 삭제됩니다. 'bd mol squash'
가비지 컬렉션 전에 요약을 보존하려면 이를 사용하세요.

모든 닫힌 wisp를(나이와 관계없이) 삭제하려면 --closed를 사용하세요. 이는
누적된 wisp 팽창으로 인한 공간 점유를 회수하는 가장 빠른 방법입니다. 기본적으로는
안전합니다: 실제로 삭제하려면 --force가 필요합니다.

참고: 이는 시간 기반 정리를 사용하므로 일시적인 wisps에 적합합니다.
그래프 압력 staleness 감지(다른 작업 차단)에는 'bd mol stale'를 참조하세요.

예시:
  bd mol wisp gc                                    # 버려진 wisp 정리 (기본값: 1h 임계값)
  bd mol wisp gc --dry-run                          # 정리될 항목 미리보기
  bd mol wisp gc --age 24h                          # 사용자 지정 연령 임계값
  bd mol wisp gc --all                              # 임계값보다 오래된 닫힌 wisp도 정리
  bd mol wisp gc --closed                           # 닫힌 wisp 삭제 미리보기
  bd mol wisp gc --closed --force                   # 모든 닫힌 wisp 삭제
  bd mol wisp gc --closed --dry-run                 # 명시적 dry-run (no --force와 동일)
  bd mol wisp gc --exclude-type agent,rig           # GC에서 agent 및 rig wisp 보호
  bd mol wisp gc --closed --force --exclude-type mol # mol 타입을 제외한 닫힌 wisp 삭제

```
bd mol wisp gc [flags]
```

**플래그:**

```
      --age string             중단된 wisp 감지용 나이 임곗값(기본값 "1h")
      --all                    임곗값보다 오래된 닫힌 wisp도 정리
      --closed                 닫힌 모든 wisp 삭제(--age 임곗값 무시)
      --dry-run                정리될 항목 미리 보기
      --exclude-type strings   해당 유형의 wisp를 GC에서 제외(쉼표로 구분, 예: agent,rig)
  -f, --force                  실제 삭제(기본값: 미리 보기만)
```

##### bd mol wisp list

현재 컨텍스트의 모든 wisp(일시적 분자)를 나열합니다.

Wisps는 기본 데이터베이스에서 Ephemeral=true인 이슈입니다.
로컬에 저장되지만 git을 통해 동기화되지 않습니다.

목록은 다음을 보여줍니다:
  - ID: wisp의 이슈 ID
  - 제목: wisp 제목
  - Status: 현재 상태 (open, in_progress, closed)
  - Started: wisp가 생성된 시점
  - Updated: 마지막 수정 시간

오래된 wisp 감지:
  - 오래된 wisp는 24시간 이상 업데이트되지 않았습니다
  - 오래되었거나 방치된 wisp를 정리하려면 'bd mol wisp gc'를 사용하세요

예제:
  bd mol wisp list              # 모든 wisp를 목록으로 표시
  bd mol wisp list --json       # 프로그래밍 방식 사용을 위한 JSON 출력
  bd mol wisp list --all        # 닫힌 wisp를 포함하십시오

```
bd mol wisp list [flags]
```

**플래그:**

```
      --all           닫힌 wisp 포함
      --type string   이슈 유형으로 필터링(예: agent, task, patrol)
```

### bd notion

beads와 Notion 간 이슈 동기화를 위한 명령어입니다.

```
bd notion
```

#### bd notion connect

기존 Notion 데이터베이스 또는 데이터 소스에 bd를 연결합니다

```
bd notion connect [flags]
```

**플래그:**

```
      --url string   기존 Notion 데이터베이스 또는 데이터 소스 URL
```

#### bd notion init

Notion에서 전용 Beads 데이터베이스를 생성하세요

```
bd notion init [flags]
```

**플래그:**

```
      --parent string   상위 페이지 ID
      --title string    데이터베이스 제목(기본값 "Beads Issues")
```

#### bd notion pull

Notion에서 하나 이상의 항목을 가져옵니다.

비드 ID 또는 외부 참조를 위치 인수로 받습니다.
동일함: bd notion sync --pull --issues &lt;refs&gt;

```
bd notion pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

#### bd notion push

하나 이상의 beads 이슈를 Notion에 푸시합니다.

위치 인수로 bead ID를 받습니다.
다음과 동일합니다: bd notion sync --push --issues &lt;ids&gt;

```
bd notion push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

#### bd notion status

Notion 동기화 상태를 표시합니다

```
bd notion status
```

#### bd notion sync

beads와 Notion 간의 이슈를 동기화합니다.

기본적으로 이는 양방향 동기화를 수행합니다. 방향을 제한하려면 --pull 또는 --push를 사용하세요.

```
bd notion sync [flags]
```

**플래그:**

```
      --create-only     누락된 원격 페이지만 생성하고 기존 페이지는 업데이트하지 않음
      --dry-run         변경을 적용하지 않고 미리 보기
      --issues string   선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string   이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-local    충돌 시 로컬 beads 버전 유지
      --prefer-notion   충돌 시 Notion 버전 사용
      --pull            Notion에서 이슈만 pull
      --push            Notion으로 이슈만 push
      --state string    동기화할 이슈 상태: open, closed 또는 all(기본값 "all")
```

### bd orphans

커밋 메시지에서 참조되지만 데이터베이스에 open 또는 in_progress로 남은 고립 이슈를 식별합니다.

이것은 구현은 되었지만 공식적으로 닫히지 않은 작업을 식별하는 데 도움이 됩니다.

예시:
  bd orphans              # 고아 이슈 표시
  bd orphans --json       # 기계 판독 가능한 출력
  bd orphans --details    # 전체 커밋 정보 표시
  bd orphans --fix        # 확인 후 고아 이슈 닫기
  bd orphans --label theme:personal             # 이 라벨이 있는 고아 이슈만 표시
  bd orphans --label-any theme:personal,theme:ventures  # 이 라벨 중 하나라도 해당되는 고아 이슈

```
bd orphans [flags]
```

**플래그:**

```
      --details             전체 커밋 정보 표시
  -f, --fix                 확인 후 고립 이슈 닫기
  -l, --label strings       레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings   레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
```

### bd ready

준비된 작업 표시(활성 차단 항목이 없는 열린 이슈).

in_progress, blocked, deferred, 그리고 hooked 이슈를 제외합니다. 이 기능은
GetReadyWork API를 사용하여 차단 요소 인식 의미론을 적용해 실제로 클레임 가능한 작업을 찾습니다.

참고: 'bd list --ready'는 동일한 차단 요소 인식 준비 작업 의미론을 사용합니다.

특정 molecule의 단계를 필터링하려면 --mol을 사용하세요:
  bd ready --mol bd-patrol   # 분자 내 준비 단계 표시

--gated를 사용해 gate-resume dispatch에 준비된 분자를 찾습니다:
  bd ready --gated           # 게이트가 닫힌 분자를 찾기

필터와 일치하는 첫 번째 준비 상태의 이슈를 원자적으로 클레임하려면 --claim을 사용하세요:
  bd ready --claim --json

이것은 molecules를 실행하는 에이전트가 다음에 실행할 수 있는 단계를 확인하는 데 유용합니다.

```
bd ready [flags]
```

**플래그:**

```
  -a, --assignee string              담당자로 필터링
      --claim                        필터와 일치하는 첫 준비 이슈를 원자적으로 맡기
      --exclude-label strings        지정 레이블 중 하나라도 있는 이슈 제외
      --exclude-type strings         결과에서 이슈 유형 제외(쉼표로 구분하거나 반복 가능, 예: --exclude-type=convoy,epic)
      --explain                      이슈가 준비되거나 차단된 이유를 의존성 인식 방식으로 표시
      --gated                        gate-resume 디스패치 준비가 된 molecule 찾기
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
      --include-deferred             향후 defer_until 타임스탬프가 있는 이슈 포함
      --include-ephemeral            결과에 임시 이슈(wisp) 포함
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
  -n, --limit int                    표시할 최대 이슈 수(무제한은 0 사용)(기본값 100)
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --mol string                   특정 molecule 내 단계로 필터링
      --mol-type string              molecule 유형으로 필터링: swarm, patrol 또는 work
      --offset int                   처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --parent string                이 bead/epic의 하위 항목으로 필터링
      --plain                        이슈를 일반 번호 목록으로 표시
      --pretty                       상태/우선순위 기호가 있는 트리 형식으로 이슈 표시(기본값 true)
  -p, --priority int                 우선순위로 필터링
  -s, --sort string                  정렬 정책: priority(기본값), hybrid, oldest(기본값 "priority")
  -t, --type string                  이슈 유형으로 필터링(task, bug, feature, epic, decision, merge-request). 별칭: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision
  -u, --unassigned                   담당자 없는 이슈만 표시
```

### bd rename

이슈의 ID를 다른 ID로 변경합니다.

이 항목은 다음을 업데이트합니다:
- 이슈의 기본 ID
- 다른 이슈의 모든 참조(설명, 제목, 노트, 등)
- 이 이슈에 대한/에서의 의존성
- 라벨, 댓글 및 이벤트

예제:
  bd rename bd-w382l bd-dolt     # 기억에 남는 ID로 이름 변경
  bd rename gt-abc123 gt-auth    # 설명적인 ID 사용

참고: 새 ID는 이 데이터베이스에 대한 유효한 접두사를 사용해야 합니다.

```
bd rename <old-id> <new-id>
```

### bd ship

프로젝트 간 의존성을 충족할 수 있는 기능을 제공합니다.

이 명령은:
  1. export:&lt;capability&gt; 레이블이 있는 이슈를 찾습니다
  2. 이슈가 닫혔는지 확인합니다 (--force를 사용해 덮어쓰기)
  3. provides:&lt;capability&gt; 레이블을 추가합니다

외부 프로젝트는 이 기능에 의존하기 위해 다음을 사용할 수 있습니다:
  bd dep add &lt;issue&gt; external:&lt;project&gt;:&lt;capability&gt;

해당 기능은 외부 프로젝트에 닫힌 이슈가 있을 때 해결됩니다
해당 이슈에는 provides:&lt;capability&gt; 라벨이 있어야 합니다.

예시:
  bd ship mol-run-assignee              # mol-run-assignee 기능을 배포
  bd ship mol-run-assignee --force      # 이슈가 닫히지 않았더라도 배포
  bd ship mol-run-assignee --dry-run    # 변경 없이 미리보기

```
bd ship <capability> [flags]
```

**플래그:**

```
      --dry-run   변경 없이 미리 보기
      --force     이슈가 닫히지 않아도 배포
```

### bd undefer

이슈를 연기 해제하여 열린 상태로 복원합니다.

이것은 이슈를 icebox로부터 다시 가져와서 다시 작업할 수 있게 합니다.
차단 요소가 없으면 이슈가 'bd ready'에 표시됩니다.

예제:
  bd undefer bd-abc        # 단일 이슈를 연기 해제
  bd undefer bd-abc bd-def # 여러 개의 이슈를 연기 해제

```
bd undefer [id...]
```

### bd version

버전 정보를 출력합니다

```
bd version
```
