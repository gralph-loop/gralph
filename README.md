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
gralph run --max-iterations N profile.yaml   # 플래그가 앞에 와도 동일
```

오케스트레이터는 매 반복 진입 시 내부 `resolveNext()`를 **함수로 직접 호출**해 커서가 `DONE`이면 break, 아니면:

1. 세션 id를 갱신하고 커맨드별 실패 카운터를 리셋 (실패 수는 세션 스코프),
2. YAML의 `agent.command`를 기동 (`{{prompt}}` 자리에 랄프 프롬프트 치환, `$GRALPH_PROFILE` 환경변수 주입).

매 반복은 새 세션·새 컨텍스트이며, 에이전트는 루프 종료 신호를 볼 일이 없다.

### 종료 동작

- 커서가 `DONE`이면 정상 종료. `--max-iterations` 도달 시 중단.
- 에이전트 바이너리 자체를 기동할 수 없으면(binary not found 등) 재시도가 무의미하므로 즉시 에러로 종료.
- 커서 전진 없이 에이전트가 **연속 5회** 비정상 종료하면 에러로 종료. 비정상 종료가 이어지는 동안에는
  지수 백오프(2s → 4s → 8s → …, 상한 30s)로 대기 후 재시도하며, 커서가 전진하면 카운터는 리셋된다.
- `agent.timeout` 설정 시 세션이 제한 시간을 넘기면 프로세스를 종료(SIGTERM 후 유예, 이후 kill)하고
  비정상 종료와 동일하게 취급한다 — 커서는 유지되어 다음 반복에서 재시도.
- SIGINT/SIGTERM 수신 시 진행 중인 에이전트 프로세스에 시그널을 전파(유예 후 kill)하고
  `[gralph] interrupted at iteration N (cursor: X)` 형태로 stderr에 보고한 뒤 종료한다.
  커서는 보존되므로 `gralph run`을 다시 실행하면 중단 지점부터 이어진다.

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

## 서브커맨드 (fork/join 쿼터)

커맨드에 `subcommands:`를 두면 그 노드는 fork/join이 된다: 커서가 부모에 머무는 동안
각 서브커맨드를 **서로 다른 작업 항목 key당 한 번씩, `count`회** 성공시켜야 하고,
모든 쿼터가 차면 비로소 부모 커맨드 자신이 실행 가능해진다(집계 검증 + 라우팅을 맡는
finalize 게이트). 부모가 성공해야 커서가 전진한다.

```yaml
commands:
  - name: build-all
    guidance: |
      남은 작업: {{subprogress}}
      서브에이전트를 병렬로 띄워 각 항목을 처리하라.
    subcommands:
      - name: make-part
        count: 3                  # 서로 다른 key 3개가 성공해야 함 (기본 1)
        key: part                 # 작업 항목 식별 인자 (count>1이면 필수, 자동 required)
        args:
          - { name: part }
        lua: scripts/part.lua     # 항목 단위 검증 (gralph.route 사용 불가)
        fail_threshold: 3         # (선택) (서브커맨드, key) 단위 실패 예산
    lua: scripts/finalize.lua     # 쿼터 충족 후 부모 호출 시 실행되는 집계 게이트
    next: [wrap]
```

규칙:

- 서브커맨드 이름은 커맨드와 같은 CLI 네임스페이스를 쓰므로 전역 유일해야 한다.
- 같은 key 재제출, 쿼터 미충족 상태의 부모 호출은 **실패 예산을 소모하지 않고** 거부된다.
- 서브커맨드 성공 응답도 "세션을 종료하라"이다 — 병렬 서브에이전트라면 그 워커만 끝나고,
  서브에이전트가 없는 에이전트는 세션당 한 항목씩 직렬로 진행해도 이어진다(진행은 영속).
- 진행 상태는 세션 회전에서 살아남고, 부모 성공 시에만 초기화된다(사이클 재방문 시 쿼터 재시작).
- 병렬 워커의 `gralph <subcommand>` 프로세스들은 상태 디렉터리의 flock으로 직렬화되어
  커밋이 유실되지 않는다. lua 게이트 자체는 락 밖에서 돌므로 병렬성이 유지된다.
- 부모 finalize lua에서는 `gralph.progress.keys("sub")` / `gralph.progress.count("sub")`로
  완료된 항목을 읽어 집계 검증할 수 있다.
- 안내문 템플릿에 `{{subprogress}}`(멀티라인 현황), `{{subdone "sub"}}`, `{{subcount "sub"}}`가
  추가로 제공되며, `gralph next`는 현황 블록을 자동으로 덧붙인다.
- store 컨벤션: 병렬 게이트는 `gralph.store.set("evidence:" .. gralph.args.part, ...)`처럼
  key로 네임스페이스해서 쓸 것 (커밋은 key 단위 머지라 다른 키끼리는 충돌하지 않는다).

## 상태 저장 (파일 분리)

- `.gralph/state.json` — **프레임워크 내부**(사용자 비접근 영역): 커서, 세션 id, 커맨드별 실패 수.
- `.gralph/store.json` — **유저 store**(lua 전용 KV): 프레임워크는 내용을 건드리지 않는다.
  lua의 `store.set`은 커맨드 **성공 시에만** 커밋되어, 실패한 검증이 값을 남기지 않는다.
  커밋은 이번 실행이 변경한 key만 머지하므로 병렬 워커가 서로의 값을 덮어쓰지 않는다.
- `.gralph/progress.json` — **프레임워크 내부**: 서브커맨드 완료 항목(key별 시각·세션).
  실패 카운터(세션 스코프)·커서(노드 스코프)와 수명이 달라 별도 파일이다. 부모 성공 시
  progress를 먼저 비우고 커서를 전진시키는 쓰기 순서로, 중간에 죽어도 stale 쿼터가
  재방문에 이월되지 않는다(보수적으로 재작업).
- `.gralph/lock` — 병렬 `gralph` 프로세스 간 read-modify-write 직렬화용 flock 파일.

## lua 브리지 (`gralph` 헬퍼)

```lua
gralph.args.<name>            -- YAML로 정의된 입력 인자 (문자열)
gralph.store.get("key")       -- 유저 KV 읽기 (스칼라/중첩 테이블)
gralph.store.set("key", val)  -- 유저 KV 쓰기 (성공 시 커밋)
gralph.route("name")          -- 후보 여럿일 때 후속 지정 (서브커맨드 게이트에선 금지)
gralph.fail("reason: ...")    -- 검증 실패 표시. 미호출 시 성공
gralph.progress.keys("sub")   -- (finalize 게이트 한정) 완료 key 배열
gralph.progress.count("sub")  -- (finalize 게이트 한정) 완료 항목 수
```

- `fail`의 reason은 실패 응답에 실려 같은 세션에서 무엇을 고칠지 알려준다.
- lua가 `error()`로 죽으면 검증 실패와 구분해 **SCRIPT ERROR**로 분류하되 실패 카운트에는 포함한다.
- `lua_timeout`(프로파일 기본값 또는 커맨드별 오버라이드)을 넘긴 스크립트는 중단되며,
  역시 **SCRIPT ERROR**로 분류되어 실패 예산에 포함된다. 설정이 없으면 타임아웃 없음.
- lua를 지정하지 않은 커맨드는 항상 성공한다 (후보가 2개 이상이면 프로파일 검증 단계에서 에러).

## 프로파일 YAML 레퍼런스

```yaml
agent:
  command: ["claude", "-p", "{{prompt}}"]   # 비대화형 에이전트 실행 커맨드
  timeout: 30m                               # (선택) 세션 제한 시간 (Go duration 문자열).
                                             # 초과 시 프로세스 종료 후 비정상 종료로 취급
                                             # (커서 유지, 다음 반복 재시도). 생략 시 무제한
prompt: |                                    # 랄프 프롬프트 (생략 시 기본문)
  1. `gralph next`로 다음 할 일을 안내받아라.
  2. 커맨드 응답에서 세션 종료 지시를 받으면 세션을 종료하라.
state_dir: .gralph                           # 상태 디렉터리 (프로파일 기준 상대경로)
fail_threshold: 5                            # 매 n회째 실패에 세션 종료
lua_timeout: 30s                             # (선택) lua 스크립트 제한 시간 기본값.
                                             # 초과 시 SCRIPT ERROR로 실패 카운트. 생략 시 무제한

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
    lua_timeout: 5s                          # (선택) 커맨드별 lua 제한 시간 오버라이드
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
| `config.go` | 프로파일 YAML 파싱·검증 (서브커맨드 규칙 포함) |
| `state.go` | 내부 상태(state.json)와 유저 store(store.json, key 단위 머지 커밋) |
| `progress.go` | 서브커맨드 진행 상태(progress.json): 쿼터 판정, stale 무효화 |
| `lock.go` | 상태 디렉터리 flock (병렬 워커 직렬화) |
| `next.go` | `resolveNext()` + 안내문 순수 렌더링 (`{{subprogress}}` 등) |
| `command.go` | 커스텀 커맨드 실행: 인자 파싱, 성공/실패/임계치, 서브커맨드 fork/join, 커서 전진 |
| `lua.go` | gopher-lua 브리지 (`gralph` 헬퍼 객체) |
| `loop.go` | 오케스트레이터 (랄프 반복문) |
