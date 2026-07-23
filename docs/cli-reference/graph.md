---
title: "bd graph"
description: "이슈의 의존성 그래프 시각화를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc graph`에서 생성되었습니다.

이슈의 의존성 그래프 시각화를 표시합니다.

epic에는 모든 하위 이슈와 그 의존성을 표시합니다.
일반 이슈에는 해당 이슈와 직접 의존성을 표시합니다.

--all을 사용하면 모든 열린 이슈를 연결 요소별로 그룹화하여 표시합니다.

표시 형식:
  (기본값)         열과 상자 그리기 간선이 있는 DAG(터미널 네이티브)
  --box            계층을 표시하는 ASCII 상자, 더 상세함
  --compact        트리 형식, 이슈당 한 줄, 더 빠르게 훑어볼 수 있음
  --dot            Graphviz DOT 형식(dot -Tsvg &gt; graph.svg로 파이프)
  --html           D3.js 시각화가 포함된 독립형 대화형 HTML

그래프는 실행 순서를 표시합니다:
- 계층 0/맨 왼쪽 = 의존성 없음(즉시 시작 가능)
- 상위 계층은 하위 계층에 의존
- 같은 계층의 노드는 병렬 실행 가능

상태 아이콘: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred

예시:
  bd graph issue-id              # 터미널 DAG 시각화(기본값)
  bd graph --box issue-id        # 계층 그룹이 있는 ASCII 상자
  bd graph --dot issue-id | dot -Tsvg &gt; graph.svg  # Graphviz를 통한 SVG
  bd graph --dot issue-id | dot -Tpng &gt; graph.png  # Graphviz를 통한 PNG
  bd graph --html issue-id &gt; graph.html  # 대화형 브라우저 보기
  bd graph --all --html &gt; all.html       # 모든 이슈, 대화형

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

## bd graph check

의존성 그래프에서 순환, 고립 항목, 기타 무결성 문제를 검사합니다.

그래프가 정상적이면 종료 코드 0, 문제가 발견되면 1을 반환합니다.

```
bd graph check [flags]
```
