# gralph

랄프 루프(ralph loop)를 실행하는 Go 도구.

랄프 루프란 비대화형 에이전트를 같은 프롬프트로 반복 기동하는 단순한 루프를 말한다.
매 반복이 새 세션·새 컨텍스트라 컨텍스트 오염이 없는 대신, 보통은 "끝났는지"를
에이전트의 자기보고에 의존하게 된다. gralph는 여기에 **커맨드 그래프와 Lua 결정론
게이트**를 더해, 루프의 전진을 자기보고가 아니라 기계 판정으로만 허용한다.

도구는 두 부분으로 구성된다.

1. **오케스트레이터** (`gralph run`) — 비대화형 에이전트 세션을 거듭 기동하는 루프.
2. **세션 내 커맨드** — 에이전트가 세션 안에서 호출하는 내장 커맨드 `gralph next`와,
   YAML로 정의해 `gralph do <name>`으로 호출하는 커스텀 커맨드들.

루프마다 실행할 커맨드, 인자, 결정론적 로직(Lua), 안내문은 전부 YAML 프로파일
(+외부 Lua)로 사용자가 정의한다.

이 문서에서 쓰는 용어:

| 용어 | 뜻 |
|---|---|
| 커맨드(노드) | YAML `commands:`의 항목. 그래프의 노드이며 `gralph do <name>`으로 호출된다 |
| 커서 | "지금 해야 할 커맨드"를 가리키는 포인터. 상태 디렉터리에 영속된다 |
| 안내문 | `gralph next`가 에이전트에게 출력하는 현재 노드의 지시문 (`guidance:`) |
| 게이트 | 커맨드에 붙은 Lua 스크립트. 성공/실패와 라우팅을 결정론적으로 판정한다 |
| store | Lua만 읽고 쓰는 사용자 KV. 세션을 넘어 값을 전달한다 |
| 세션 회전 | 반복마다 새 에이전트 세션(새 컨텍스트)을 기동하는 것 |
| 서브커맨드 | 커맨드 아래 `subcommands:`로 정의하는 fork/join 자식 작업 단위 |

## 설치

태그마다 GitHub Releases에 플랫폼별 바이너리(`gralph-<os>-<arch>`,
linux/windows/darwin × amd64/arm64)가 첨부된다. 소스에서 빌드하려면:

```sh
go build -o gralph .
```

의존성: `gopkg.in/yaml.v3`(GitHub 미러로 replace), `github.com/yuin/gopher-lua`(순수 Go Lua 런타임).
전 플랫폼 빌드는 `build.sh`/`build.ps1`(산출물 `dist/`, 태그에서 버전 스탬핑).
Windows도 지원한다(예제의 Windows 변형은 [예제](#예제) 절 참고).

## 빠른 시작

최소 프로파일은 에이전트 기동 커맨드와 커맨드 한 개면 된다.

```yaml
# profile.yaml
agent:
  command: ["claude", "-p", "{{prompt}}"]   # 아무 비대화형 에이전트나 가능

commands:
  - name: implement
    guidance: |
      TODO.md의 다음 항목을 구현하고, 결과를 report.json으로 남겨라.
      {{usage}}
    lua: check.lua      # 전진 여부를 판정하는 게이트
    # next가 없는 마지막 노드: 성공하면 루프가 끝난다
```

```lua
-- check.lua
local f = io.open(gralph.profile_dir .. "/report.json")
if not f then
  gralph.fail("report.json not found -- finish the work and resubmit")
  return
end
f:close()
```

```sh
gralph run profile.yaml
```

무슨 일이 일어나는가: 오케스트레이터가 에이전트 세션을 기동하면, 에이전트는
`gralph next`로 안내문을 받아 작업한 뒤 `gralph do implement`를 호출한다.
`check.lua`가 실패하면 사유와 함께 같은 세션에서 재시도시키고, 성공하면 커서를
전진시키고 세션을 종료시킨다. 커서가 끝(`DONE`)에 닿을 때까지 오케스트레이터는
새 세션을 반복 기동한다.

상태는 상태 디렉터리(기본 `.gralph/<프로파일 name>/`, 프로파일 기준 상대경로)에
영속된다. `name`은 생략하면 프로파일 파일명(확장자 제외)이므로, 한 워크스페이스에서
여러 프로파일을 돌려도 상태가 자동으로 격리된다. 실행 중간 산출물이므로 프로젝트의
`.gitignore`에 `.gralph/`를 넣기를 권장한다.

## 세션 흐름

```
에이전트 ── gralph next ──▶ 현재 커서 노드의 안내문
            (Lua 없이 순수 렌더링, {{store "key"}}로 store 값만 채움)
에이전트 ── (비결정론적 태스크 수행)
에이전트 ── gralph do <command> --arg value ──▶ 노드의 Lua가 검증·라우팅·store 기록
```

세션 안에서 프로파일 경로는 오케스트레이터가 주입한 `$GRALPH_PROFILE` 환경변수에서
읽는다(`--profile`로 덮어쓸 수 있다).

커스텀 커맨드의 공통 계약:

- 세션에서 호출할 수 있는 것은 커서가 가리키는 커맨드뿐이며, 다른 커맨드 호출은 거부된다.
- **성공** → 커서 즉시 전진, store 커밋. 응답은 항상 "세션을 종료하라"는 지시다.
- **실패** → 같은 세션에서 재시도할 수 있다. 단 매 n회째(기본 5, `fail_threshold`) 실패에는
  세션 종료 응답을 내서 새 세션·새 컨텍스트 재실행을 강제한다. 실패 카운터는 세션
  스코프라, 세션이 바뀌면 0에서 다시 센다.
- 실패 사유는 `failures.json`에 영속되어 새 세션의 `gralph next` 안내문 뒤에 자동으로
  노출된다 — 이전 세션의 실수를 모른 채 반복하지 않게 한다. 해당 노드가 성공하면
  기록은 삭제된다.

매 반복은 새 세션·새 컨텍스트이며, 에이전트는 루프 종료 신호를 볼 일이 없다.

## 프로파일 YAML 레퍼런스

```yaml
name: my-flow                                # (선택) 프로파일 식별자. 생략 시 파일명(확장자 제외).
                                             # 기본 상태 디렉터리(.gralph/<name>)의 키가 된다
agent:
  command: ["claude", "-p", "{{prompt}}"]   # 비대화형 에이전트 실행 커맨드
  timeout: 30m                               # (선택) 세션 제한 시간 (Go duration 문자열).
                                             # 초과 시 프로세스 종료 후 비정상 종료로 취급
                                             # (커서 유지, 다음 반복 재시도). 생략 시 무제한
prompt: |                                    # 랄프 프롬프트 (생략 시 기본문)
  1. `gralph next`로 다음 할 일을 안내받아라.
  2. 커맨드 응답에서 세션 종료 지시를 받으면 세션을 종료하라.
state_dir: .gralph/my-flow                   # (선택) 상태 디렉터리 오버라이드 (프로파일 기준
                                             # 상대경로). 생략 시 .gralph/<name>
fail_threshold: 5                            # 매 n회째 실패에 세션 종료
lua_timeout: 30s                             # (선택) Lua 스크립트 제한 시간 기본값.
                                             # 초과 시 SCRIPT ERROR로 실패 카운트. 생략 시 무제한

commands:
  - name: implement
    guidance: |                              # next가 렌더링하는 안내문
      Implement "{{store "goal"}}" and write a JSON report.
      {{usage}}
    args:
      - { name: report, required: true, desc: "path to the JSON report" }
    lua: scripts/implement.lua               # 프로파일 기준 상대경로
    next: [verify]                           # 후속 후보
    fail_threshold: 3                        # (선택) 커맨드별 오버라이드
    lua_timeout: 5s                          # (선택) 커맨드별 Lua 제한 시간 오버라이드

  - name: verify
    agent:                                   # (선택) 노드별 에이전트 오버라이드
      command: ["claude", "-p", "{{prompt}}", "--model", "haiku"]
    prompt: |                                # (선택) 노드별 랄프 프롬프트 오버라이드
      1. `gralph next`로 안내받고 검증만 수행하라.
      2. 세션 종료 지시를 받으면 즉시 종료하라.
```

### `next:` 라우팅

| 후보 수 | 동작 |
|---|---|
| 0 (생략) | 마지막 커맨드. 성공 시 커서가 `DONE`이 되어 루프가 끝난다 |
| 1 | 무조건 이동 |
| ≥2 | Lua가 `gralph.route("name")`로 지정. 후보 외 이름·미지정은 런타임 에러(실패로 카운트) |

### 노드별 에이전트/프롬프트 오버라이드

커맨드(노드)에 `agent`·`prompt`를 지정하면 **그 노드가 커서일 때** 해당 커맨드·프롬프트로
세션을 기동한다 — 예: implement는 비싼 모델, verify는 싼 모델. 지정하지 않은 노드는
전역 `agent.command`·`prompt`를 그대로 쓴다(하위 호환). 규칙:

- 전역 `agent.command`는 여전히 필수다. 오버라이드는 추가 옵션일 뿐이다.
- 노드에 `agent`를 선언했는데 `command`가 비어 있으면 프로파일 검증 단계에서 에러.

### 인자 설계 팁

검증에 쓸 재료를 인자로 받게 설계하면 게이트가 단단해진다. 예: 안내된 포맷의 JSON
보고서 경로를 제출받아 Lua에서 형식 검증, 근거 코드 위치·의견·대안을 구조화 인자로
받아 추론 유도 등.

## 서브커맨드 (fork/join 쿼터)

커맨드에 `subcommands:`를 두면 그 노드는 fork/join이 된다. 여기서 서브커맨드란 이
YAML 키로 정의하는 자식 작업 단위를 말한다(내장 CLI 커맨드와는 무관한 별개 개념).

커서가 부모 노드에 머무는 동안, 각 서브커맨드를 서로 다른 작업 항목 key당 한 번씩
**`count`회** 성공시켜야 한다. 모든 쿼터가 차면 비로소 부모 커맨드 자신을 호출할 수
있는데, 부모의 Lua는 집계 검증과 라우팅을 맡는 finalize 게이트다. 부모가 성공해야
커서가 전진한다.

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
- 서브커맨드 성공 응답도 "세션을 종료하라"이다. 병렬 서브에이전트라면 그 워커만 끝나고,
  서브에이전트가 없는 에이전트는 세션당 한 항목씩 직렬로 진행해도 이어진다(진행은 영속).
- 진행 상태는 세션 회전에서 살아남고, 부모 성공 시에만 초기화된다(사이클 재방문 시 쿼터 재시작).
- 병렬 워커의 `gralph do <subcommand>` 프로세스들은 상태 디렉터리의 flock으로 직렬화되어
  커밋이 유실되지 않는다. Lua 게이트 자체는 락 밖에서 돌므로 병렬성이 유지된다.
- 부모 finalize Lua에서는 `gralph.progress.keys("sub")` / `gralph.progress.count("sub")`로
  완료된 항목을 읽어 집계 검증할 수 있다.
- 안내문 템플릿에 `{{subprogress}}`(멀티라인 현황), `{{subdone "sub"}}`, `{{subcount "sub"}}`가
  추가로 제공되며, `gralph next`는 현황 블록을 자동으로 덧붙인다. usage 블록에는
  각 서브커맨드의 실행 라인과 쿼터 요약이 함께 생성된다(아래 "usage 자동 생성" 절 참고).
- store 컨벤션: 병렬 게이트는 `gralph.store.set("evidence:" .. gralph.args.part, ...)`처럼
  key로 네임스페이스해서 쓸 것 (커밋은 key 단위 머지라 다른 키끼리는 충돌하지 않는다).

## 안내문과 usage 자동 생성

`gralph next`는 커서 노드의 `args` 스펙에서 실행 usage 블록을 자동 생성한다.
실행 라인을 안내문에 손으로 쓰면(`RUN: ./gralph do implement --report report.txt`)
args 스펙과 이중 관리가 되어 드리프트하기 쉬운데, usage 블록은 항상 스펙에서 파생된다:

```
Command to run when done:
  gralph do implement --report <value>

Arguments:
  --report  (required)  Path to the work report file
```

- required 인자는 `--name <value>`, optional 인자는 `[--name <value>]`로 표기.
- 인자의 `desc`가 있으면 Arguments 설명 칸에 노출, 없으면 생략.
  args가 없는 커맨드는 `Command to run when done:` + 실행 라인만 나온다.
- 안내문 템플릿에서 `{{usage}}`를 호출하면 블록이 그 위치에 들어가고, 호출하지
  않은 안내문에는 `gralph next`가 안내문 뒤에 자동으로 덧붙인다.
- 서브커맨드를 가진 부모 노드면 각 서브커맨드의 usage가 같은 형식으로 먼저 나오고
  count/key 요약 한 줄이 붙으며(`(run once per distinct --part, 3 items total)`),
  부모 자신의 실행 라인은 `Command to run when every quota is met:`로 표기된다.

## Lua 브리지 (`gralph` 헬퍼)

```lua
gralph.args.<name>            -- YAML로 정의된 입력 인자 (문자열)
gralph.store.get("key")       -- 유저 KV 읽기 (스칼라/중첩 테이블)
gralph.store.set("key", val)  -- 유저 KV 쓰기 (성공 시 커밋)
gralph.route("name")          -- 후보 여럿일 때 후속 지정 (서브커맨드 게이트에선 금지)
gralph.fail("reason: ...")    -- 검증 실패 표시. 미호출 시 성공
gralph.profile_dir            -- 프로파일 디렉터리 절대경로 (문자열)
gralph.progress.keys("sub")   -- (finalize 게이트 한정) 완료 key 배열
gralph.progress.count("sub")  -- (finalize 게이트 한정) 완료 항목 수
```

- **주의**: Lua 안의 상대 경로(`io.open` 등)는 커맨드를 호출한 에이전트의 cwd 기준이라
  보장이 없다. 파일 경로는 `gralph.profile_dir`에 이어 붙여 절대경로로 만들 것.
- `fail`은 실패를 표시할 뿐 스크립트를 중단하지 않는다(첫 reason이 유지된다). 중단하려면
  `return`을 함께 쓸 것. reason은 실패 응답에 실려 같은 세션에서 무엇을 고칠지 알려준다.
- Lua가 `error()`로 죽으면 검증 실패와 구분해 **SCRIPT ERROR**로 분류하되 실패 카운트에는 포함한다.
- `lua_timeout`(프로파일 기본값 또는 커맨드별 오버라이드)을 넘긴 스크립트는 중단되며,
  역시 **SCRIPT ERROR**로 분류되어 실패 예산에 포함된다. 설정이 없으면 타임아웃 없음.
- Lua를 지정하지 않은 커맨드는 항상 성공한다 (후보가 2개 이상이면 프로파일 검증 단계에서 에러).

## 운영/디버깅 CLI

```sh
gralph status [--profile p] [--json]                # 커서·세션·실패 수·쿼터 진행 조회
gralph reset  [--profile p] [--force] [--failures]  # 상태 초기화 (--failures: 실패 카운터만)
gralph validate profile.yaml                        # 실행 없이 프로파일 lint
gralph try <command|subcommand> [--profile p] [--arg value ...]  # 게이트 드라이런
gralph graph profile.yaml [--state]                 # 커맨드 그래프를 mermaid로 출력
gralph version                                      # 버전 출력 (Go 빌드 정보 기반)
```

- **`status`** — state.json/progress.json을 읽어 커서, 세션 id, 노드별 실패 수, 그리고 커서
  노드에 서브커맨드가 있으면 쿼터 진행(완료 key 포함)을 출력한다. `--json`이면 기계 판독용 JSON.
- **`reset`** — 상태 디렉터리의 state.json/store.json/progress.json을 삭제해 처음부터 다시
  시작한다. `--failures`만 주면 **실패 카운터만 0으로** 만들고 커서·store·progress는 보존한다.
  `--force`가 없으면 stdin으로 y/N 확인을 받으며, stdin이 TTY가 아니면 `--force`를 요구한다.

  > **수동 세션 주의**: 오케스트레이터(`gralph run`) 없이 커맨드를 직접 실행하면 세션 회전이
  > 없어 실패 카운터가 세션 경계 없이 누적된다(매 n회째마다 세션 종료 응답이 나옴). 필요하면
  > `gralph reset --failures`로 카운터만 초기화하라.

- **`validate`** — 실행 없이 lint: 로더의 모든 검증 규칙에 더해 (1) 각 Lua 파일의 존재,
  (2) Lua 문법(gopher-lua로 컴파일만, 실행 안 함), (3) 그래프 경고 — 첫 커맨드에서 도달
  불가능한 노드, `DONE`(next 없는 노드)에 도달할 수 없어 루프가 끝나지 않는 경우.
  에러가 있으면 exit 1, 경고만 있으면 exit 0.
- **`try`** — 커서 검사 없이 해당 노드/서브커맨드의 Lua를 드라이런한다. store **읽기는 실제
  파일**, **쓰기는 메모리에만** 머물고 절대 커밋되지 않으며 실패 카운터·progress·커서도 변하지
  않는다. 출력: Lua 경로, 결과(SUCCESS / FAILED+reason / SCRIPT ERROR), `gralph.route` 선택
  (있으면), 이번 실행이 store에 쓰려던 key-값 미리보기("(not committed)" 명시). 부모 finalize
  노드는 현재 progress를 그대로 읽어 `gralph.progress.*`가 동작한다(쿼터 미충족이어도 실행되며
  경고 한 줄만 출력).
- **`graph`** — 커맨드 그래프를 mermaid flowchart로 stdout에 출력한다. 노드는 커맨드이고,
  서브커맨드가 있으면 라벨에 `name [sub1 xN, sub2]`로 쿼터를 표기한다. 마지막 커맨드는
  `DONE` 노드로 이어지고, 후보가 2개 이상인 엣지에는 `route`가 표기된다. `--state`를 주면
  상태 디렉터리를 읽어 현재 커서 노드를 강조한다.
- 예약어: 커스텀 커맨드는 `gralph do <name>`으로 호출하므로 내장 커맨드와 네임스페이스가
  분리되어 있다. 커맨드/서브커맨드 이름으로 쓸 수 없는 것은 `do`(네임스페이스 단어)와
  `DONE`(완료 센티널)뿐이며, 내장 단어(`run` `next` 등)는 이름으로 써도 된다 — 새 내장
  커맨드가 추가되어도 기존 프로파일은 깨지지 않는다.
- 평면 호출 `gralph <name>`은 지원하지 않는다 — `gralph do <name>`을 안내하는 에러로
  거부된다. `validate`가 안내문 속 평면 호출을 경고로 잡아준다.

## `gralph run`의 종료와 복구

```sh
gralph run profile.yaml [--max-iterations N]
gralph run --max-iterations N profile.yaml   # 플래그가 앞에 와도 동일
```

- 커서가 `DONE`이면 정상 종료. `--max-iterations` 도달 시 중단.
- 매 반복 진입 시 세션 id를 갱신하고 커맨드별 실패 카운터를 리셋한 뒤(실패 수는 세션
  스코프), YAML의 `agent.command`를 기동한다 — `{{prompt}}` 자리에 랄프 프롬프트 치환,
  `$GRALPH_PROFILE` 환경변수 주입.
- 에이전트 바이너리 자체를 기동할 수 없으면(binary not found 등) 재시도가 무의미하므로 즉시 에러로 종료.
- 커서 전진 없이 에이전트가 **연속 5회** 비정상 종료하면 에러로 종료. 비정상 종료가 이어지는 동안에는
  지수 백오프(2s → 4s → 8s → …, 상한 30s)로 대기 후 재시도하며, 커서가 전진하면 카운터는 리셋된다.
- `agent.timeout` 설정 시 세션이 제한 시간을 넘기면 프로세스를 종료(SIGTERM 후 유예, 이후 kill)하고
  비정상 종료와 동일하게 취급한다 — 커서는 유지되어 다음 반복에서 재시도.
- SIGINT/SIGTERM 수신 시 진행 중인 에이전트 프로세스에 시그널을 전파(유예 후 kill)하고
  `[gralph] interrupted at iteration N (cursor: X)` 형태로 stderr에 보고한 뒤 종료한다.
  커서는 보존되므로 `gralph run`을 다시 실행하면 중단 지점부터 이어진다.

## 예제

예제 둘 다 실제 에이전트 대신 행동을 흉내 내는 가짜 에이전트(`test/agent.sh`)를 쓴다.

- `example/` — 기본 그래프: plan → implement → verify ─route→ {fix → verify | finish}.
  `implement` 노드는 usage 자동 덧붙임을, `fix` 노드는 `{{usage}}` 직접 배치를 시연한다.
- `example/subcommands/` — fork/join 그래프: build-all [make-part x3, write-doc] → wrap.
  가짜 에이전트가 남은 항목마다 백그라운드 워커를 띄워 병렬 서브에이전트를 흉내 낸다.
- Windows에서는 기본 예제의 `profile.windows.yaml` + `test/agent.ps1` 짝을 쓴다.

```sh
go build -o example/gralph . && cd example
./gralph run profile.yaml        # 또는: ../gralph run profile.yaml
```

가짜 에이전트의 동작: `next` 호출 → 안내문의 RUN 라인 실행
(RUN 라인이 없으면 자동 생성된 usage 블록의 실행 라인 사용) →
실패 응답이면 같은 세션에서 보완 후 재시도, "End the session" 지시면 즉시 종료.

## 내부 구조

프로파일을 작성하는 데는 필요 없는, gralph 자체의 구현 설명이다.

### 상태 디렉터리 파일

`<state_dir>`은 기본 `.gralph/<프로파일 name>/`이다. 구버전 기본값(`.gralph-state/`)에
상태가 남아 있으면 로더가 마이그레이션 안내와 함께 실행을 거부한다(엔트리부터의
조용한 재시작 방지). `state_dir`을 명시하면 그 경로가 그대로 쓰인다.

- `<state_dir>/state.json` — **프레임워크 내부**(사용자 비접근 영역): 커서, 세션 id, 커맨드별 실패 수.
- `<state_dir>/store.json` — **유저 store**(Lua 전용 KV): 프레임워크는 내용을 건드리지 않는다.
  Lua의 `store.set`은 커맨드 **성공 시에만** 커밋되어, 실패한 검증이 값을 남기지 않는다.
  커밋은 이번 실행이 변경한 key만 머지하므로 병렬 워커가 서로의 값을 덮어쓰지 않는다.
- `<state_dir>/progress.json` — **프레임워크 내부**: 서브커맨드 완료 항목(key별 시각·세션).
  실패 카운터(세션 스코프)·커서(노드 스코프)와 수명이 달라 별도 파일이다. 부모 성공 시
  progress를 먼저 비우고 커서를 전진시키는 쓰기 순서로, 중간에 죽어도 stale 쿼터가
  재방문에 이월되지 않는다(보수적으로 재작업).
- `<state_dir>/failures.json` — **프레임워크 내부**: 노드 라벨별 최근 실패 사유
  (라벨은 커맨드 이름, 서브커맨드는 `name:key`; 라벨당 최대 3개, 누적 번호·RFC3339 시각).
  실패 카운터(세션 스코프)와 달리 세션 회전에도 보존되며, `gralph next`가 현재 노드의
  기록(서브커맨드 라벨 포함)을 안내문 뒤에 덧붙인다. 노드 성공 시 그 라벨의 기록이
  삭제되고, 부모 finalize 성공 시 그 노드의 서브커맨드 기록도 전부 삭제된다.
- `<state_dir>/lock` — 병렬 `gralph` 프로세스 간 read-modify-write 직렬화용 flock 파일.
- `<state_dir>/journal.jsonl` — **append-only 이벤트 저널**(JSON Lines): 주요 전이를 한 줄씩 기록해
  사후 분석을 가능하게 한다. 이벤트는 `session_start`(세션 id·커서·iteration),
  `command_succeeded`(커맨드·다음 커서·Lua 게이트 소요 ms), `command_failed`(라벨·실패 번호·사유),
  `subitem_recorded`(서브커맨드·key), `loop_done`이고 각 라인에 `at`(RFC3339)이 붙는다.
  커밋류 이벤트는 state lock 안에서 기록되어 순서가 커밋 순서와 일치한다.
  저널 쓰기는 **best-effort**: 실패해도 본 흐름을 막지 않는다(stderr 경고 후 무시).

### 코드 구조

| 파일 | 내용 |
|---|---|
| `main.go` | CLI 디스패치 (`run` / `graph` / `next` / `status` / `reset` / `validate` / `try` / `version` / `do <커스텀 커맨드>`) |
| `config.go` | 프로파일 YAML 파싱·검증 (서브커맨드 규칙·예약어 포함) |
| `state.go` | 내부 상태(state.json)와 유저 store(store.json, key 단위 머지 커밋) |
| `progress.go` | 서브커맨드 진행 상태(progress.json): 쿼터 판정, stale 무효화 |
| `failures.go` | 실패 사유 기록(failures.json): 세션 간 전달, 성공 시 삭제 |
| `lock.go` | 상태 디렉터리 flock (병렬 워커 직렬화) |
| `fsretry_*.go` | Windows의 일시적 파일시스템 오류(sharing violation) 재시도 판정 |
| `next.go` | `resolveNext()` + 안내문 순수 렌더링 (`{{usage}}`·`{{subprogress}}` 등, usage 자동 생성) |
| `command.go` | 커스텀 커맨드 실행: 인자 파싱, 성공/실패/임계치, 서브커맨드 fork/join, 커서 전진 |
| `lua.go` | gopher-lua 브리지 (`gralph` 헬퍼 객체) |
| `journal.go` | append-only 이벤트 저널(journal.jsonl, best-effort) |
| `graph.go` | `gralph graph`: 커맨드 그래프 mermaid 렌더링 |
| `loop.go` | 오케스트레이터 (랄프 반복문): 매 반복 진입 시 `resolveNext()`를 함수로 직접 호출해 커서를 확인 |
| `ops.go` | 운영 커맨드: `status` / `reset` / `validate`(lint) |
| `try.go` | `try` — 커밋 없는 게이트 드라이런 |
