# gralph

랄프 루프(ralph loop)를 실행하는 Go 도구. 두 부분으로 구성된다.

1. **오케스트레이터** (`gralph run`) — 비대화형 에이전트 세션을 반복 기동하는 반복문.
2. **세션 내 서브커맨드** — 에이전트가 호출하는 `gralph next` + YAML로 정의한 커스텀 커맨드들.

루프마다 실행할 커맨드, 인자, 결정론적 로직(lua), 안내문은 전부 YAML 프로파일(+외부 lua)로 사용자가 커스텀한다.

## 빌드

```sh
go build -o gralph .
```

의존성: `gopkg.in/yaml.v3`(GitHub 미러로 replace), `github.com/yuin/gopher-lua`(순수 Go Lua 런타임).

## 실행

```sh
gralph run profile.yaml [--max-iterations N]
```

오케스트레이터는 매 반복 진입 시 내부 `resolveNext()`를 **함수로 직접 호출**해 커서가 `DONE`이면 break, 아니면:

1. 세션 id를 갱신하고 커맨드별 실패 카운터를 리셋 (실패 수는 세션 스코프),
2. YAML의 `agent.command`를 기동 (`{{prompt}}` 자리에 랄프 프롬프트 치환, `$GRALPH_PROFILE` 환경변수 주입).

매 반복은 새 세션·새 컨텍스트이며, 에이전트는 루프 종료 신호를 볼 일이 없다.

## 세션 흐름

```
에이전트 ── gralph next ──▶ 현재 커서 노드의 안내문
            (lua 없이 순수 렌더링, {{store "key"}}로 gralph.store 값만 채움)
에이전트 ── (비결정론적 태스크 수행)
에이전트 ── gralph <command> --arg value ──▶ 노드의 lua가 검증·라우팅·store 기록
```

커스텀 커맨드의 공통 계약:

- **성공** → 커서 즉시 전진, store 커밋, 응답은 항상 "세션을 종료하라".
- **실패** → 같은 세션에서 재시도 가능. 단 매 n회째(기본 5, `fail_threshold`) 실패에는
  세션 종료 응답을 내서 새 세션·새 컨텍스트 재실행을 강제.
- 안내된 커맨드는 세션 안에서 딱 한 번 반드시 성공해야 하며, 커서와 다른 커맨드 호출은 거부된다.

## 커서 전진과 그래프

`next:` 후보 리스트 기준:

| 후보 수 | 동작 |
|---|---|
| 0 | 마지막 커맨드. 성공 시 커서 := `DONE` (루프 종료 조건) |
| 1 | 무조건 이동 |
| ≥2 | lua가 `gralph.route("name")`로 지정. 후보 외 이름·미지정은 런타임 에러(실패로 카운트) |

## 상태 저장 (두 파일 분리)

- `.gralph/state.json` — **프레임워크 내부**(사용자 비접근 영역): 커서, 세션 id, 커맨드별 실패 수.
- `.gralph/store.json` — **유저 store**(lua 전용 KV): 프레임워크는 내용을 건드리지 않는다.
  lua의 `store.set`은 커맨드 **성공 시에만** 커밋되어, 실패한 검증이 값을 남기지 않는다.

## lua 브리지 (`gralph` 헬퍼)

```lua
gralph.args.<name>            -- YAML로 정의된 입력 인자 (문자열)
gralph.store.get("key")       -- 유저 KV 읽기 (스칼라/중첩 테이블)
gralph.store.set("key", val)  -- 유저 KV 쓰기 (성공 시 커밋)
gralph.route("name")          -- 후보 여럿일 때 후속 지정
gralph.fail("reason: ...")    -- 검증 실패 표시. 미호출 시 성공
```

- `fail`의 reason은 실패 응답에 실려 같은 세션에서 무엇을 고칠지 알려준다.
- lua가 `error()`로 죽으면 검증 실패와 구분해 **SCRIPT ERROR**로 분류하되 실패 카운트에는 포함한다.
- lua를 지정하지 않은 커맨드는 항상 성공한다 (후보가 2개 이상이면 프로파일 검증 단계에서 에러).

## 프로파일 YAML 레퍼런스

```yaml
agent:
  command: ["claude", "-p", "{{prompt}}"]   # 비대화형 에이전트 실행 커맨드
prompt: |                                    # 랄프 프롬프트 (생략 시 기본문)
  1. `gralph next`로 다음 할 일을 안내받아라.
  2. 커맨드 응답에서 세션 종료 지시를 받으면 세션을 종료하라.
state_dir: .gralph                           # 상태 디렉터리 (프로파일 기준 상대경로)
fail_threshold: 5                            # 매 n회째 실패에 세션 종료

commands:
  - name: implement
    guidance: |                              # next가 렌더링하는 안내문
      Implement "{{store "goal"}}" and write a JSON report.
      RUN: ./gralph implement --report report.json
    args:
      - { name: report, required: true }
    lua: scripts/implement.lua               # 프로파일 기준 상대경로
    next: [verify]                           # 후속 후보
    fail_threshold: 3                        # (선택) 커맨드별 오버라이드
```

검증 패턴 예시: 작업 리포트를 인자로 제출해 보조 검증, 근거 코드 위치·의견·대안을 구조화 인자로
받아 추론 유도, 안내된 포맷의 JSON 보고서 경로를 인자로 넘겨 lua에서 형식 검증 등.

## 예제

`example/`에 전체 그래프(plan → implement → verify ─route→ {fix → verify | finish})와
가짜 에이전트(`test/agent.sh`)가 들어 있다:

```sh
go build -o example/gralph . && cd example
./gralph run profile.yaml        # 또는: ../gralph run profile.yaml
```

가짜 에이전트는 실제 에이전트의 행동을 흉내 낸다: `next` 호출 → 안내문의 RUN 라인 실행 →
실패 응답이면 같은 세션에서 보완 후 재시도, "End the session" 지시면 즉시 종료.

## 코드 구조

| 파일 | 내용 |
|---|---|
| `main.go` | CLI 디스패치 (`run` / `next` / 동적 커스텀 커맨드) |
| `config.go` | 프로파일 YAML 파싱·검증 |
| `state.go` | 내부 상태(state.json)와 유저 store(store.json) |
| `next.go` | `resolveNext()` + 안내문 순수 렌더링 |
| `command.go` | 커스텀 커맨드 실행: 인자 파싱, 성공/실패/임계치, 커서 전진 |
| `lua.go` | gopher-lua 브리지 (`gralph` 헬퍼 객체) |
| `loop.go` | 오케스트레이터 (랄프 반복문) |
