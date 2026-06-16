# 계획서: 통합 Launcher 모델 + Gralph Agent Launcher Protocol V1 (GALP V1)

> 이 문서는 **다른 에이전트가 그대로 실행**할 수 있도록 작성된 구현 계획서다.
> 현재 코드(브랜치 `claude/agent-tmux-gralph-integration-6pxdpd`) 기준으로 작성되었으며,
> 인용된 파일·라인 번호는 작업 시작 시점에 다시 확인할 것.

---

## 1. 배경과 동기

gralph는 ralph 루프 오케스트레이터로, 매 세션마다 비대화형 에이전트를 새로 띄우고
에이전트가 세션 안에서 `gralph next` / `gralph do`를 호출하면 Lua 게이트로 결정론적
검증을 수행한다. 현재 에이전트 실행은 `loop.go`의 `launchAgent`가 `exec.CommandContext`로
직접 서브프로세스를 띄우는 단일 경로다.

**문제:** 에이전트 생태계(대화형 자동화, 할당량 리셋 대기, 권한 프롬프트 자동 응답,
TTY 요구 에이전트 등)는 gralph의 릴리스 주기보다 빠르게 변하고 다양하다. 이를 모두
gralph 빌드에 박으면 "새 에이전트 지원 = gralph 릴리스"라는 병목이 생기고, 유저가
자기 에이전트에 맞춰 신속히 커스터마이즈할 수 없다.

**해법:** 에이전트 실행부를 **프로세스 경계 너머의 플러그인(launcher)** 으로 일반화한다.
gralph는 **계약(GALP V1, 언어 중립 wire protocol)만 고정**하고, 구현부는 유저/커뮤니티
영역에 둔다. 주력 에이전트용 공식 launcher도 동일한 계약을 따르는, **유저가 수정 가능한**
형태로 제공한다.

### 핵심 설계 원칙 (이전 논의에서 확정됨)

1. **단일 메커니즘으로 통합.** "인프로세스 기본 실행"과 "외부 플러그인" 두 경로를 두지
   않는다. gralph 코어는 **항상 "launcher 명령 한 줄을 exec하고 구조화 결과를 읽는"**
   단일 경로만 갖는다. 변이성은 Go 타입이 아니라 **"어떤 명령을 exec하는가"라는 데이터**로
   표현된다.
2. **특권 빌트인 없음.** 기본 launcher도 다른 launcher와 완전히 동일한 경로/계약을 따른다.
   유저는 언제든 자기 파일로 갈아끼울 수 있다.
3. **제로컨피그 동작 보장.** launcher 파일을 따로 설치하지 않아도 gralph가 즉시 동작해야
   한다 → 기본 launcher는 **gralph 자기호출**로 제공(아래 4절).
4. **책임 분할(계층 분리) 유지.**
   - **launcher(플러그인)** = 세션을 *어떻게* 띄우고 구동하는가(spawn/drive) + 할당량 등
     특수 상태의 *감지·신호*.
   - **gralph 루프(코어)** = 세션을 *언제* 띄우는가, retry 예산, 타임아웃 범위,
     할당량 *대기*. 둘을 잇는 seam이 `rate_limited{retry_after}` 같은 구조화 결과다.

### 비목표 (Non-goals)

- 원격 launcher 자동 fetch/실행, launcher 레지스트리, 마켓플레이스. (로컬·명시적 설치만.)
- launcher를 별도 Git 레포로 분리하는 것. (이번 작업은 gralph 내부에서 계약+기본 구현만
  확립. 레포 분리는 두 번째 소비자가 생기거나 풍부한 통합이 필요할 때의 후속 과제.)
- 기존 프로파일 동작 변경. (하위호환 필수 — 4.6절.)
- Lua 게이트/상태/커서/store 계층 변경. (이 계층은 에이전트 구동 방식과 무관하며 그대로
  둔다.)

---

## 2. 현재 코드 기준점 (작업 전 재확인 필수)

| 위치 | 역할 |
|---|---|
| `loop.go:47` `runLoop` | 오케스트레이터 루프. 세션 로테이션, retry/backoff, 포기 예산. |
| `loop.go:103` | `agentErr := launchAgent(ctx, p, p.AgentCommandFor(node), p.PromptFor(node))` — 실행 호출 지점. |
| `loop.go:107-140` | 에이전트 실패 처리: `exec.ErrNotFound`면 즉시 포기, 아니면 커서 미진전 시 backoff + `consecutiveFailures++`, `MaxConsecutiveAgentFailures`(=5) 도달 시 포기. |
| `loop.go:118-124` | 실행 후 `resolveNext`로 커서 진전 여부 재확인. 진전했으면 `consecutiveFailures=0`. |
| `loop.go:169` `launchAgent` | `exec.CommandContext`로 에이전트 spawn. `{{prompt}}` 치환(176-178), `cmd.Dir`(180), stdout/stderr 상속(181-182), env 주입 `GRALPH_PROFILE`/`GRALPH_INSTANCE_NAME`(183-188), SIGTERM→`WaitDelay` hard-kill(189-196), `agent.timeout` context(170-174). |
| `config.go:88` `AgentSpec` | `Command []string`(91), `Timeout string`(96) + 파싱된 `timeout`(99). |
| `config.go:103` `Profile` | `Agent AgentSpec`(104), `Prompt`(105), `Dir`/`Path`(120-123). |
| `config.go:415` `AgentCommandFor` / `426` `PromptFor` | 노드별 override 해석. |
| `main.go:43` | 서브커맨드 디스패치 `switch os.Args[1]`. |

---

## 3. 목표 아키텍처 (통합형)

```
runLoop (loop.go)
  │  node = p.Command(cursor)
  │  agentArgv = p.AgentCommandFor(node)   // {{prompt}} 미치환 그대로 전달
  │  prompt    = p.PromptFor(node)
  ▼
runLauncher(ctx, p, node, agentArgv, prompt) → (SessionResult, error)
  │  1. launcher argv 해석 (5.4 해석 순서)
  │  2. 요청 파일/결과 파일/프롬프트 파일 준비
  │  3. exec: <launcher argv...> -- <agentArgv...>   (GALP env 주입)
  │  4. 결과 파일 JSON 파싱 → SessionResult
  ▼
runLoop: SessionResult.Outcome 으로 분기
  - completed  → 기존과 동일(커서 진전 재확인)
  - crashed/timed_out → 기존 비정상 종료 처리(backoff + 예산)
  - rate_limited → 신규: retry_after 까지 대기(예산/타임아웃 미적용), 동일 커서 재시도
```

`runLauncher`가 실제로 호출하는 launcher는 전부 동일 경로다:

```
launcher argv 예시
  미지정(기본)  → ["<gralph 실행파일 경로>", "__galp-exec"]   (자기호출, 4절)
  tmux         → [".gralph/launchers/tmux"]                  (스캐폴딩된 편집가능 파일)
  quota        → [".gralph/launchers/quota"]                 (스캐폴딩된 편집가능 파일)
  커스텀        → 유저가 launcher: 로 지정한 임의 경로
```

gralph 코어는 launcher 종류를 **전혀 모른다.** 모든 지식은 GALP V1 계약과
"exec할 명령 한 줄"에 응집된다.

---

## 4. 기본 launcher = gralph 자기호출

제로컨피그·이식성(bash 등 외부 의존 0)·FS 흔적 0을 보장하기 위해, 기본 launcher는
**gralph 자신을 launcher 모드로 재실행**한다.

- 신규 숨김 서브커맨드: `gralph __galp-exec` (`main.go:43` switch에 추가).
- 이 서브커맨드는 GALP V1 **launcher 측 레퍼런스 구현**이다:
  1. `GRALPH_REQUEST_FILE` JSON을 읽는다.
  2. agent argv의 `{{prompt}}`를 `GRALPH_PROMPT_FILE` 내용으로 치환한다.
  3. `exec.CommandContext`로 에이전트를 띄운다 — **현재 `launchAgent`의 spawn 로직을
     그대로 이 서브커맨드로 이전**한다(`cmd.Dir`, stdout/stderr 상속, SIGTERM→WaitDelay
     hard-kill, `GRALPH_TIMEOUT_MS` 기반 타임아웃).
  4. 결과를 `GRALPH_RESULT_FILE`에 기록:
     - 에이전트 exit 0 → `{"protocol":1,"outcome":"completed"}`
     - exit ≠ 0 → `{"protocol":1,"outcome":"crashed","message":"..."}`
     - 타임아웃 만료 → `{"protocol":1,"outcome":"timed_out"}`
  5. launcher 자신은 정상 동작했으면 exit 0으로 종료(전송 계층 정상).

> **주의(중요):** 기본 launcher는 `rate_limited`를 보고하지 않는다. 할당량 감지는 *비자명*
> 하며 *공식 quota launcher*(opt-in)의 책임이다. 기본 launcher는 항상 completed/crashed/
> timed_out 중 하나만 보고한다. 이 계층 분리는 의도된 것이다.

> **주의(중요):** `completed`는 "그래프가 진전했다"는 뜻이 아니라 "세션이 정상 종료됐다"는
> 뜻이다. 커서 진전 여부는 **host가 `resolveNext`로 독립 판정**(현재 `loop.go:118-124`
> 로직 유지)한다. launcher는 프로세스 수준 결과만 보고한다.

### 4.6 하위호환 (필수)

- `AgentSpec`에 `Launcher []string` 필드를 **선택적으로** 추가한다. 미지정 시 기본 launcher
  (자기호출)를 사용한다.
- 기존 프로파일(`agent.command` + `agent.timeout`, `launcher` 없음)은 **동작이 100% 동일**
  해야 한다: 자기호출 기본 launcher가 현재 `launchAgent`와 같은 방식으로 서브프로세스를
  띄우므로 외부에서 본 동작은 변하지 않는다.
- 유일한 내부 변화: 기본 케이스에서 **프로세스 홉이 1 증가**(`gralph → gralph __galp-exec
  → agent`). 세션당 fork/exec 1회로 무시 가능. 이는 통합의 의도된 비용이며, 그 대가로
  모든 launcher가 동일 경로/디버깅/계약을 따른다.

---

## 5. Gralph Agent Launcher Protocol V1 (GALP V1) 명세

언어 중립 프로세스 간 계약. gralph(host)가 launcher(plugin)를 exec하고, 양측은 파일 +
env + exit code로 통신한다.

### 5.1 호출 규약 (host → launcher)

```
exec: <launcher argv...> -- <agent command template argv...>
```

- `--` 뒤는 에이전트 실행 템플릿 argv. 토큰에 `{{prompt}}`가 포함될 수 있다. launcher가
  치환 정책을 결정한다(서브프로세스: 프롬프트 텍스트로 치환 / tmux: 치환 대신 send-keys로
  주입).
- launcher의 작업 디렉터리(cwd)는 **프로파일 디렉터리**(`p.Dir`)로 설정된다.

### 5.2 환경 변수 (host → launcher)

| 변수 | 의미 |
|---|---|
| `GRALPH_LAUNCHER_PROTOCOL` | 프로토콜 버전 정수. V1 = `1`. |
| `GRALPH_REQUEST_FILE` | **요청 JSON 파일 경로(정본).** 모든 입력의 권위 있는 출처. |
| `GRALPH_RESULT_FILE` | launcher가 **결과 JSON을 기록해야 하는** 경로. |
| `GRALPH_PROMPT_FILE` | ralph 프롬프트 텍스트 파일 경로(UTF-8). |
| `GRALPH_TIMEOUT_MS` | 능동 작업 권고 한도(ms). `0` = 무제한. 할당량 *대기*에는 적용 안 됨. |
| `GRALPH_SESSION_ID` | 현재 세션 ID. |
| `GRALPH_PROFILE` | 프로파일 절대 경로. (세션 내 `gralph next/do`가 사용 — 통과시킬 것.) |
| `GRALPH_INSTANCE_NAME` | 인스턴스 이름. (세션 내 `gralph next/do`가 사용 — 통과시킬 것.) |

> 편의상 위 스칼라들을 env로 미러링하지만, **정본은 `GRALPH_REQUEST_FILE`의 JSON**이다.
> 풍부/확장 필드는 요청 JSON에만 추가된다(스칼라 env는 V1 고정 집합).

### 5.3 요청 JSON 스키마 (`GRALPH_REQUEST_FILE`)

```json
{
  "protocol": 1,
  "session_id": "1718521200-ab12cd34",
  "instance": "myflow",
  "profile": "/abs/path/profile.yaml",
  "dir": "/abs/path",
  "prompt_file": "/tmp/galp-XXXX/prompt.txt",
  "result_file": "/tmp/galp-XXXX/result.json",
  "agent_command": ["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"],
  "timeout_ms": 1800000,
  "env_passthrough": { "GRALPH_PROFILE": "...", "GRALPH_INSTANCE_NAME": "...", "GRALPH_SESSION_ID": "..." }
}
```

### 5.4 결과 JSON 스키마 (`GRALPH_RESULT_FILE`, launcher → host)

```json
{
  "protocol": 1,
  "outcome": "completed | crashed | rate_limited | timed_out",
  "retry_after": "2026-06-15T08:00:00Z",
  "message": "사람이 읽을 선택적 메모"
}
```

| 필드 | 필수 | 비고 |
|---|---|---|
| `protocol` | ✅ | host의 `GRALPH_LAUNCHER_PROTOCOL`과 일치해야 함. 불일치 시 host가 명확히 에러. |
| `outcome` | ✅ | 아래 어휘 중 하나. |
| `retry_after` | `rate_limited`일 때만 | RFC3339. host가 이 시각까지 대기. |
| `message` | ❌ | 로그/진단용. |

**outcome 어휘 (V1 고정):**

| outcome | host 동작 |
|---|---|
| `completed` | 세션 정상 종료. host가 `resolveNext`로 커서 진전 독립 판정(현 로직 유지). |
| `crashed` | 비정상 종료. 커서 미진전 시 backoff + `consecutiveFailures++`. |
| `timed_out` | `crashed`와 동일 처리(재시도 가능)하되 로그상 구분. |
| `rate_limited` | **신규.** `retry_after`까지 대기(ctx 취소 존중). 포기 예산 미소모, `agent.timeout` 미적용. 동일 커서 재시도. |

### 5.5 Exit code (전송 계층 건강도)

- `0` = launcher가 정상 동작했고 유효한 결과 파일을 기록함.
- `≠0` = **launcher 자체 고장**(결과 파일 없음/손상 포함). host는 `crashed`로 간주하고
  결과 파일을 무시한다. (에이전트의 실패와 launcher의 실패를 분리하는 장치.)

### 5.6 stdout/stderr

- 에이전트의 실시간 출력 통과 채널. host는 자신의 stdout/stderr를 launcher에 상속한다.
- **제어 신호를 stdout에 의존하지 말 것.** 모든 제어 정보는 결과 파일로만 전달.

### 5.7 버저닝

- `GRALPH_LAUNCHER_PROTOCOL` 정수로 버전 협상. launcher는 결과에 `protocol`을 echo.
- 불일치 → host가 명확한 에러(어떤 버전을 기대했고 무엇을 받았는지) 후 해당 세션 실패 처리.
- 향후 필드는 **가산적(additive)** 으로만 추가. 기존 필드 의미 변경 금지(그러면 V2).

---

## 6. gralph 측 변경 (파일별)

### 6.1 `config.go`
- `AgentSpec`(88)에 `Launcher []string \`yaml:"launcher"\`` 추가. 빈 값 = 기본(자기호출).
- (선택) `CommandSpec`의 노드별 agent override가 launcher도 override할 수 있도록
  `LauncherFor(c)` 헬퍼 추가(`AgentCommandFor` 패턴, 415-422 참고). 없으면 프로파일 레벨 사용.
- `validate`(ops.go) 경로에 launcher 경로 존재성/형식 lint 추가(선택).

### 6.2 `launcher.go` (신규)
- `type SessionOutcome string` + 상수(`OutcomeCompleted` 등).
- `type SessionResult struct { Outcome SessionOutcome; RetryAfter time.Time; Message string }`.
- `func runLauncher(ctx, p, node, agentArgv []string, prompt string) (SessionResult, error)`:
  1. launcher argv 해석(6.5).
  2. 임시 디렉터리 생성, prompt 파일·request JSON 작성, result 파일 경로 예약.
  3. `exec.CommandContext(ctx, launcherArgv[0], launcherArgv[1:]... , "--", agentArgv...)`.
     `cmd.Dir = p.Dir`, stdout/stderr 상속, GALP env 주입(5.2), `GRALPH_PROFILE`/
     `GRALPH_INSTANCE_NAME` 통과(기존 loop.go:183-188과 동등).
  4. SIGTERM→`WaitDelay` hard-kill(기존 189-196 이전).
  5. exit≠0 또는 결과 파싱 실패 → `SessionResult{Outcome: crashed}`.
  6. 결과 JSON 파싱·검증(protocol 일치, outcome 유효, rate_limited면 retry_after 필수).
  7. launcher 바이너리 자체를 못 띄움(`exec.ErrNotFound`) → error 반환(host-side 문제,
     loop가 즉시 포기 판단에 사용).

### 6.3 `galp_exec.go` (신규) — 기본 launcher 레퍼런스 구현
- `func runGALPExec(args []string) int`: `gralph __galp-exec` 본체.
- 현재 `launchAgent`의 spawn 로직을 이리로 이전(서브프로세스 띄우기, 타임아웃,
  SIGTERM/kill). `{{prompt}}` 치환은 여기서 수행(프롬프트 파일에서 읽음).
- 결과를 `GRALPH_RESULT_FILE`에 기록. completed/crashed/timed_out만 사용.

### 6.4 `main.go`
- `switch`(43)에 `case "__galp-exec": os.Exit(runGALPExec(os.Args[2:]))` 추가.
- `usage`(15)에는 노출하지 않음(숨김 서브커맨드).
- gralph 자기 실행파일 경로는 `os.Executable()`로 취득(6.5에서 사용).

### 6.5 `loop.go`
- `loop.go:103`을 `launchAgent` 호출에서 `runLauncher` 호출로 교체. agent argv는
  `{{prompt}}` **미치환** 상태로 넘긴다(치환은 launcher 책임으로 이동).
- launcher argv 해석:
  - 노드/프로파일의 `launcher` 지정값 우선.
  - 미지정 시 기본값 `[]string{selfExe, "__galp-exec"}` (`selfExe = os.Executable()`).
  - 상대 경로 launcher는 `p.Dir` 기준 해석.
- `loop.go:107-140`의 실패 처리를 `SessionResult.Outcome` 분기로 재작성:
  - `runLauncher`가 host-side error(launcher 못 띄움) → `exec.ErrNotFound`/`fs.ErrNotExist`면
    즉시 포기(기존 109-111 유지).
  - `completed` → 기존 "agentErr == nil" 경로(커서 진전 재확인, backoff 없음).
  - `crashed`/`timed_out` → 기존 "agentErr != nil" 경로(커서 미진전 시 backoff +
    `consecutiveFailures++`, 예산 초과 시 포기).
  - `rate_limited` → **신규 분기**: `consecutiveFailures` 불변, `select`로 `RetryAfter`까지
    대기(`time.After(time.Until(retryAfter))` vs `ctx.Done()`), 그 후 동일 커서로 루프 계속.
    `agent.timeout`은 이 대기에 적용되지 않음(대기는 `runLauncher` 바깥, 루프 레벨).
- 기존 `launchAgent`(169)는 제거하거나 `runGALPExec`가 재사용하는 내부 헬퍼로 이전.
- 저널 이벤트(`journal.go`)에 launcher outcome / rate_limited 대기 기록 추가(선택, 권장).

### 6.6 스캐폴딩: `gralph launchers init`
- 신규 서브커맨드 `gralph launchers init [name]`로 편집 가능한 공식 launcher 파일을
  `.gralph/launchers/<name>`에 복사. Go `embed`로 템플릿 번들.
- 최소 제공 템플릿:
  - `subprocess` — 자기호출 기본값과 동등한 동작의 **편집 가능 셸 레퍼런스**(GALP를
    셸로 구현하는 예시; "기본값도 편집 가능" 원칙 충족).
  - `tmux` — 대화형 에이전트 tmux 자동화 래퍼(이전 논의의 방식 A).
  - `quota` — 할당량 소진 감지 후 `rate_limited{retry_after}` 보고 래퍼.
- 머티리얼라이즈 규칙: **이미 있으면 덮어쓰지 않음.** `--force`로만 갱신.

---

## 7. 작업 순서 (구현 에이전트용 체크리스트)

> 각 단계는 독립 검증 가능하도록 쪼갰다. 단계마다 `go build ./...` + `go test ./...` 통과
> 후 커밋.

- [ ] **T1. 계약 타입 정의.** `launcher.go`에 `SessionOutcome`, `SessionResult`, 요청/결과
      JSON 구조체 + (역)직렬화. 단위 테스트(스키마 round-trip, protocol 불일치, rate_limited
      retry_after 누락 검증).
- [ ] **T2. 기본 launcher 구현.** `galp_exec.go` + `main.go`에 `__galp-exec` 디스패치.
      현 `launchAgent` spawn 로직 이전. 가짜 에이전트(`test/agent.sh` 패턴)로
      completed/crashed/timed_out 결과 파일 생성 검증.
- [ ] **T3. `runLauncher` 구현.** `launcher.go`. 요청/프롬프트 파일 작성, exec(`-- agentArgv`),
      env 주입, 결과 파싱, exit code/파싱 실패 → crashed 매핑. 자기호출 기본값 해석
      (`os.Executable()`).
- [ ] **T4. `runLoop` 통합.** `loop.go:103` 교체, 107-140 분기 재작성, `rate_limited` 대기
      분기 추가. `launchAgent` 제거/이전.
- [ ] **T5. 설정/하위호환.** `AgentSpec.Launcher` 추가, (선택)`LauncherFor`. 기존 예제
      프로파일(`example/profile.yaml`)이 **변경 없이** 동일 동작하는 E2E 확인.
- [ ] **T6. 스캐폴딩.** `gralph launchers init` + `embed` 템플릿(subprocess/tmux/quota).
- [ ] **T7. 적합성 테스트(conformance).** 가짜 launcher 스크립트들(각 outcome을 결정론적으로
      반환)로 host 분기 전수 검증. 특히 `rate_limited`가 포기 예산을 소모하지 않고
      `retry_after`까지 대기 후 재시도하는지(테스트에선 짧은 시각으로).
- [ ] **T8. 문서.** 이 계획서를 기준으로 `docs/galp-v1.md`(사용자용 프로토콜 레퍼런스)와
      README의 launcher 섹션 작성. 프로파일 `launcher:` 사용 예시 포함.

---

## 8. 수용 기준 (Acceptance Criteria)

1. `launcher` 미지정 기존 프로파일이 **외부 관찰 동작 변화 없이** 동작한다(E2E 통과).
2. 프로파일에 `launcher: ["./my-launcher.sh"]` 지정 시 해당 외부 프로세스가 GALP env/argv를
   받아 실행되고, 결과 JSON으로 host 흐름이 제어된다.
3. launcher가 `rate_limited{retry_after}`를 반환하면: (a) `consecutiveFailures`가 증가하지
   않고, (b) `retry_after`까지 대기하며(ctx 취소로 중단 가능), (c) 그 후 동일 커서로
   재시도한다.
4. launcher가 잘못된/누락된 결과를 쓰거나 exit≠0이면 host가 `crashed`로 처리(backoff +
   예산 적용)한다.
5. `protocol` 불일치 시 host가 명확한 에러 메시지를 낸다.
6. `gralph launchers init tmux`가 편집 가능한 tmux launcher 파일을 생성하고, 그 파일로
   대화형 에이전트가 구동된다(수동 검증 시나리오 문서화).
7. `go build ./...`, `go test ./...`, `gralph validate example/profile.yaml` 모두 통과.

---

## 9. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| 계약 경직성(공개 후 깨기 어려움) | `protocol` 버전 + 가산적 필드 규칙. 정본을 요청/결과 JSON으로 두어 확장 여지 확보. |
| 오작동 launcher | exit≠0/파싱 실패 → `crashed` 폴백. protocol 불일치 명확 에러. |
| 공급망/신뢰 | 원격 자동 fetch 금지. 로컬·명시 경로만. (오늘 `agent.command`와 동일 신뢰 수준.) |
| 기본 케이스 프로세스 홉 +1 | 세션당 fork/exec 1회로 무시 가능. 통합 균일성과의 의도된 트레이드오프. |
| 이식성 vs 편집성(기본 launcher) | 기본=자기호출(이식성), 편집은 `launchers init` 스캐폴딩(온디맨드)으로 양립. |

---

## 10. 참고: 책임 분할 요약 (seam)

```
[ launcher / 플러그인 ]                      [ gralph 루프 / 코어 ]
- 에이전트 spawn/drive (subprocess/tmux/…)   - 세션 로테이션, 커서 진전 판정(resolveNext)
- {{prompt}} 주입 정책                        - retry/backoff, 포기 예산(MaxConsecutiveAgentFailures)
- 할당량 등 특수상태 *감지*                    - 할당량 *대기*(retry_after까지), 타임아웃 범위
        └──────── 구조화 결과(GALP V1 result JSON) ────────┘
                  completed / crashed / rate_limited{retry_after} / timed_out
```
