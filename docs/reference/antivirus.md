---
title: 바이러스 백신 오탐
description: 바이러스 백신 도구가 bd 바이너리를 오탐하는 이유와 체크섬 확인, 예외 추가 및 신고 방법
---

## 개요

일부 바이러스 백신 소프트웨어는 beads(`bd` 또는 `bd.exe`)를 악성으로 표시할 수 있습니다. 이는 **오탐**입니다. beads는 이슈 추적을 위한 합법적인 오픈 소스 명령줄 도구입니다.

이제 Beads 릴리스 설치 프로그램은 설치 전에 다운로드한 아카이브를 릴리스 `checksums.txt`와 대조해 확인합니다. 바이너리를 수동으로 설치하는 사용자는 `bd`를 실행하거나 바이러스 백신 예외를 만들기 전에 먼저 체크섬을 확인해야 합니다.

## 발생 이유

beads를 포함한 Go 바이너리는 다음 이유로 바이러스 백신 소프트웨어에 표시될 수 있습니다.

1. **휴리스틱 감지**: 일부 악성 코드가 Go로 작성되어 바이러스 백신 ML 모델이 Go 특유의 바이너리 패턴을 의심스러운 것으로 표시할 수 있습니다.
2. **행동 분석**: 파일을 수정하고 git과 상호 작용하는 CLI 도구가 행동 감지를 트리거할 수 있습니다.
3. **서명되지 않은 바이너리**: 코드 서명이 없으면 새 실행 파일을 의심스럽게 취급할 수 있습니다.

이는 많은 합법적인 Go 프로젝트에 영향을 주는 **업계 전반의 알려진 문제**입니다. 예시는 [Go 프로젝트 이슈](https://github.com/golang/go/issues/16292)를 참조하세요.

## 알려진 문제

### Kaspersky 바이러스 백신

**탐지명**: `PDM:Trojan.Win32.Generic`
**영향받는 버전**: bd.exe v0.23.1 및 잠재적으로 다른 버전
**구성 요소**: 시스템 감시기(System Watcher, Proactive Defense Module)

Kaspersky의 PDM(Proactive Defense Module)은 Go 실행 파일에서 흔히 오탐을 일으키는 행동 분석을 사용합니다.

## 사용자 해결 방법

### 옵션 1: 파일 무결성 확인(가장 먼저 권장)

다운로드한 바이너리를 실행하거나 바이러스 백신 예외를 추가하기 전에 파일이 정품인지 확인하세요.

1. [공식 GitHub 릴리스](https://github.com/gastownhall/beads/releases)에서 beads를 다운로드합니다.
2. SHA256 체크섬이 릴리스의 `checksums.txt` 파일과 일치하는지 확인합니다.
3. 릴리스에 코드 서명이 포함되어 있다면 해당 서명도 확인합니다.

**체크섬 확인(Windows PowerShell):**
```powershell
Get-FileHash bd.exe -Algorithm SHA256
```

**체크섬 확인(macOS/Linux):**
```bash
shasum -a 256 bd
```

출력을 릴리스 페이지의 `checksums.txt`에 있는 체크섬과 비교합니다.

### 옵션 2: 예외 추가(확인 후)

바이러스 백신 예외 목록에 beads를 추가합니다.

**Kaspersky:**
1. Kaspersky를 열고 설정(Settings)으로 이동합니다.
2. 위협 및 제외(Threats and Exclusions) → 제외 관리(Manage Exclusions)로 이동합니다.
3. 추가(Add) → 제외 경로 추가(Add path to exclusion)를 클릭합니다.
4. `bd.exe`가 있는 디렉터리를 추가합니다(예: `C:\Users\YourName\AppData\Local\bd\`).
5. 예외를 적용할 구성 요소(검사, 모니터링 등)를 선택합니다.

**Windows Defender:**
1. Windows 보안(Windows Security)을 엽니다.
2. 바이러스 및 위협 방지(Virus & threat protection) → 설정 관리(Manage settings)로 이동합니다.
3. 제외(Exclusions) → 제외 추가 또는 제거(Add or remove exclusions)로 스크롤합니다.
4. beads 설치 디렉터리 또는 특정 `bd.exe` 파일을 추가합니다.

**기타 바이러스 백신 소프트웨어:**
- 제외(Exclusions), 허용 목록(Whitelist) 또는 신뢰할 수 있는 애플리케이션(Trusted Applications) 설정을 찾습니다.
- beads 설치 디렉터리 또는 실행 파일을 추가합니다.

### 옵션 3: 오탐 신고

오탐을 신고해 탐지 정확도 향상에 기여하세요.

**Kaspersky:**
1. [Kaspersky Threat Intelligence Portal](https://opentip.kaspersky.com/)을 방문합니다.
2. 분석할 `bd.exe` 파일을 업로드합니다.
3. 오탐으로 표시합니다.
4. 참고: beads는 오픈 소스 CLI 도구입니다(https://github.com/gastownhall/beads).

**Windows Defender:**
1. [Microsoft Security Intelligence](https://www.microsoft.com/en-us/wdsi/filesubmission)로 이동합니다.
2. 파일을 오탐으로 제출합니다.
3. 합법적인 소프트웨어에 대한 세부 정보를 제공합니다.

**기타 공급업체:**
- 웹사이트에서 오탐 제출 양식을 확인합니다.
- 대부분의 주요 공급업체에는 표시된 파일을 검토하는 절차가 있습니다.

## 개발자/배포자용

소스에서 beads를 빌드하거나 배포하는 경우 다음을 참조하세요.

### 현재 빌드 구성

Beads 릴리스는 오탐을 줄이기 위한 여러 최적화와 함께 빌드됩니다.

```yaml
ldflags:
  - -s -w  # 디버그 심볼과 DWARF 정보 제거
```

**Windows PE 버전 정보**: 릴리스 빌드는 `go-winres`를 사용해 적법한 PE 리소스
메타데이터(회사명, 제품명, 파일 설명, 버전, 저작권, 애플리케이션 매니페스트)를 Windows
바이너리에 포함합니다. 이는 AV 오탐에 가장 효과적인 조치 중 하나입니다. 합법적인
소프트웨어에는 거의 항상 PE 메타데이터가 있으며 AV 휴리스틱은 그 부재를 의심 신호로
사용합니다.

이 최적화는 공식 릴리스 빌드에 자동으로 적용됩니다.

### 코드 서명

가능한 경우 Windows 릴리스는 Authenticode 인증서로 서명됩니다. 코드 서명은 다음 효과가 있습니다.
- 시간이 지남에 따라 오탐률 감소
- SmartScreen/바이러스 백신 공급업체에서 평판 구축
- 변조 확인 제공

**서명된 바이너리 확인(Windows PowerShell):**
```powershell
# 바이너리 서명 여부 확인
Get-AuthenticodeSignature .\bd.exe

# 서명된 바이너리의 예상 출력:
# SignerCertificate: [인증서 세부 정보]
# Status: Valid
```

**서명된 바이너리 확인(osslsigncode를 사용하는 Linux/macOS):**
```bash
# osslsigncode가 없으면 설치
# Ubuntu/Debian: apt-get install osslsigncode
# macOS: brew install osslsigncode

osslsigncode verify -in bd.exe
```

**참고:** 코드 서명에는 확인 절차가 필요한 EV(Extended Validation) 인증서가 필요합니다. 릴리스가 서명되지 않았다면 빌드 시점에 인증서를 사용할 수 없었다는 뜻입니다. 위의 체크섬 확인 단계에 따라 진위를 확인하세요.

### 대체 빌드 방법

일부 사용자는 다음 방법으로 성공했다고 보고합니다.
```bash
go build -ldflags "-s -w" -o bd ./cmd/bd
```

하지만 결과는 바이러스 백신 공급업체와 버전에 따라 다릅니다.

## 자주 묻는 질문

### beads는 안전한가요?

예. Beads는 다음과 같습니다.
- 오픈 소스(모든 코드를 [GitHub](https://github.com/gastownhall/beads)에서 감사 가능)
- 릴리스에 확인용 체크섬 포함
- 전 세계 개발자가 사용
- 이슈 추적을 위한 단순한 CLI 도구

### 감지를 피하도록 코드를 수정하면 되지 않나요?

이 문제는 beads 코드에 국한되지 않고 일반적인 Go 바이너리의 특성입니다. 코드를 바꿔도 휴리스틱/행동 감지를 안정적으로 방지할 수 없습니다. 올바른 해결 방법은 다음과 같습니다.
1. 코드 서명(시간이 지나면서 신뢰 구축)
2. 바이러스 백신 공급업체의 애플리케이션 허용 목록 등록
3. 사용자의 오탐 신고

### 향후 릴리스에서 해결되나요?

다음을 구현했습니다.
- 바이너리에 포함된 **Windows PE 버전 정보**(회사명, 제품명, 버전, 매니페스트)
- Windows 릴리스용 **코드 서명 인프라**(EV 인증서 필요)
- 휴리스틱 트리거를 줄이는 **빌드 최적화**(`-s -w` ldflags)
- 사용자가 예외를 추가하고 오탐을 신고할 수 있는 **문서**

아직 진행 중인 항목:
- EV 코드 서명 인증서 취득
- 바이러스 백신 공급업체 허용 목록에 beads 제출

인증서가 바이러스 백신 공급업체에서 평판을 쌓을 때까지 새 릴리스에서도 오탐이 발생할 수 있습니다. 일반적으로 지속적으로 서명된 릴리스를 몇 달간 제공해야 합니다.

### 바이러스 백신을 비활성화해야 하나요?

**아니요.** 대신 다음을 수행하세요.
1. 처음 실행하기 전에 릴리스 체크섬을 확인합니다.
2. 다른 위협에 대비해 바이러스 백신을 활성화된 상태로 유지합니다.
3. 탐지가 계속되면 확인 후에만 beads를 바이러스 백신 예외에 추가합니다.

## 문제 신고

새로운 바이러스 백신 오탐이 발생하면 다음을 수행하세요.

1. [GitHub](https://github.com/gastownhall/beads/issues)에 이슈를 엽니다.
2. 다음을 포함합니다.
   - 바이러스 백신 소프트웨어 이름과 버전
   - 탐지/위협 이름
   - Beads 버전(`bd version`)
   - 운영 체제

이를 통해 여러 바이러스 백신 공급업체의 오탐을 추적하고 해결할 수 있습니다.

## 참고 자료

- [Kaspersky 오탐 가이드](https://support.kaspersky.com/1870)
- [Go 바이너리 오탐 토론](https://www.linkedin.com/pulse/go-false-positives-melle-boudewijns)
- [Go 프로젝트 이슈 트래커](https://github.com/golang/go/issues/16292)
- [Kaspersky 커뮤니티 포럼](https://forum.kaspersky.com/topic/pdmtrojanwin32generic-54425/)
