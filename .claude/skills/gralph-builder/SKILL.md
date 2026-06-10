---
name: gralph-builder
description: gralph 워크플로(프로파일 YAML + lua 스크립트)를 구성, 작성, 검증, 실행한다. gralph 빌드(build), 워크플로/프로파일 작성, dry-run 검증(validate), 단계 실행, 루프 실행(run, smoke test) 요청 시 사용.
---

# gralph-builder

gralph는 랄프 루프 오케스트레이터다. 워크플로 하나 = 프로파일 YAML 1개 + 노드별 lua 스크립트들.
이 스킬은 워크플로를 작성하고, 에이전트 없이 드라이버로 노드를 한 칸씩 수동 실행(dry-run)해
검증하는 방법을 다룬다. 모든 경로는 저장소 루트(`gralph/`) 기준이다.

드라이버: `.claude/skills/gralph-builder/driver.sh` (Git Bash로 실행)

## 전제 조건

- gralph 바이너리. 드라이버가 다음 순서로 찾는다: `$GRALPH_BIN` 환경변수 > PATH의
  `gralph`(Windows에서는 `gralph.exe` 우선) > 개발 저장소의 `dist/` 폴백.
- Git Bash (Windows) 또는 bash

## 드라이버 사용법 (에이전트 경로 - 이것부터)

```sh
D="$PWD/.claude/skills/gralph-builder/driver.sh"   # 저장소 루트에서 (cd해도 깨지지 않게 절대경로)
bash $D validate <profile.yaml>             # YAML 구조 검증 (상태 부작용 없음)
bash $D next     <profile.yaml>             # 현재 커서 노드의 안내문
bash $D call     <profile.yaml> <cmd> --arg v   # 노드 1개 수동 실행 + 상태 출력
bash $D status   <profile.yaml>             # 커서 / 실패 카운트 / store
bash $D reset    <profile.yaml>             # 상태 삭제 (처음부터)
bash $D smoke    <profile.yaml> [max-iter]  # 실제 루프 실행 (기본 20회 제한)
```

검증된 예 (예제 워크플로):

```sh
bash $D reset example/profile.windows.yaml
bash $D next example/profile.windows.yaml
bash $D call example/profile.windows.yaml plan --goal demo
```

`call`은 성공 시 `OK ... End the session now.`와 함께 커서가 전진한 state.json,
커밋된 store.json을 출력한다.

## 워크플로 작성 절차

1. 그래프 설계: 노드(커맨드) 목록과 `next` 후보. 후보 0개 = 마지막(성공 시 DONE),
   1개 = 무조건 이동, 2개 이상 = lua가 `gralph.route()`로 선택해야 함.
2. 프로파일 YAML 작성 (아래 레퍼런스).
3. 노드별 lua 작성: 인자 검증, store 기록, 라우팅.
4. `validate` -> `call`로 전 노드 수동 통과 -> `reset` -> `smoke` 또는 실제 에이전트로 실행.

### 프로파일 YAML 레퍼런스 (검증된 최소형)

```yaml
agent:
  command: ["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"]
state_dir: .gralph-state        # 프로파일 기준 상대경로 (기본값 .gralph-state)
fail_threshold: 3               # 매 n회째 실패에 세션 종료 지시 (기본 5)

commands:
  - name: triage
    guidance: |                 # next가 렌더링. {{store "key"}}만 사용 가능
      Pick one open bug and register it.
      RUN: gralph triage --bug <id>
    args:
      - { name: bug, required: true }
    lua: scripts/triage.lua     # 프로파일 기준 상대경로. 생략 시 항상 성공
    next: [judge]
```

- `prompt:`를 생략하면 기본 랄프 프롬프트가 쓰인다 (next 안내 -> 작업 -> 커맨드 실행 -> 세션 종료 지시 준수).
- guidance에 에이전트가 실행할 커맨드 라인을 명시하라. 없는 store 키는 빈 문자열로 렌더링된다.

### lua 브리지

```lua
gralph.args.<name>            -- YAML 선언 인자 (문자열, 미전달 시 nil)
gralph.store.get("key")       -- 유저 KV 읽기 (없으면 nil)
gralph.store.set("key", val)  -- 유저 KV 쓰기. 커맨드 성공 시에만 커밋됨
gralph.route("name")          -- next 후보가 2개 이상일 때 필수. 후보 밖 이름은 SCRIPT ERROR
gralph.fail("reason: ...")    -- 검증 실패. 미호출이면 성공
```

### 실행 계약 (검증된 동작)

- 성공: 커서 즉시 전진, store 커밋, 응답은 항상 세션 종료 지시. exit 0.
- `gralph.fail`: `FAILED <cmd> (failure N): reason...` + 같은 세션 재시도 안내. exit 1.
  실패 N이 threshold의 배수일 때만 `End the session now.`로 바뀐다.
- 인자 누락/오타는 `usage error:`로 응답하며 실패 카운트에 포함되지 않는다.
- lua의 `error()`와 후보 밖 `route()`는 `SCRIPT ERROR`로 분류되고 실패 카운트에 포함된다.
- 커서가 아닌 커맨드 호출: "`X` is not the current command" 거부 (카운트 미포함).
- 실패 카운트는 세션 스코프: 오케스트레이터가 반복마다 리셋. `call`로 수동 실행할 때는
  리셋되지 않으므로 누적이 거슬리면 `reset`.

## 새 워크플로 작성 예 (이 절차 그대로 검증됨)

빈 디렉터리에 triage -> judge -(route)-> { patch -> judge | close } 그래프를 만들고
드라이버만으로 DONE까지 진행한 절차:

```sh
mkdir -p mywf/scripts && cd mywf
# profile.yaml: 위 레퍼런스 형태로 4개 노드 작성 (judge의 next: [patch, close])
# scripts/judge.lua: verdict 인자에 따라 gralph.route("patch") 또는 ("close")
bash $D validate profile.yaml                       # OK: profile is valid
bash $D call profile.yaml triage --bug BUG-42       # OK, 커서 judge로
bash $D call profile.yaml judge --verdict close     # lua가 fail로 거부 (가드 동작 확인)
bash $D call profile.yaml judge --verdict fix       # route -> patch
echo x > fix.diff
bash $D call profile.yaml patch --diff fix.diff     # store.patched = true
bash $D call profile.yaml judge --verdict close     # route -> close
bash $D call profile.yaml close --resolution "fixed in fix.diff"   # cursor = DONE
```

## 실제 루프 실행

```sh
bash $D smoke example/profile.windows.yaml 10
```

가짜 에이전트(example/test/agent.ps1)가 6회 반복으로 plan -> implement -> verify ->
fix -> verify -> finish를 완주하고 `cursor is DONE`으로 끝난다. 직접 실행은
`dist/gralph.exe run <profile.yaml> [--max-iterations N]` 동일.

실제 에이전트는 프로파일의 `agent.command`만 바꾸면 된다. 세션 안에서는 오케스트레이터가
`$GRALPH_PROFILE`을 주입하므로 에이전트는 `--profile` 없이 `gralph next` / `gralph <cmd>`를 호출한다.

## Gotchas

- Git Bash에서 bare `gralph`는 확장자 없는 `dist/gralph`(build.ps1이 WSL용으로 크로스 빌드한
  Linux ELF)로 먼저 해석되어 "cannot execute binary file: Exec format error"가 난다.
  Windows에서는 항상 `dist/gralph.exe`를 명시하거나 드라이버를 쓸 것.
- 같은 이유로 `example/profile.yaml`(bash 가짜 에이전트)은 Windows Git Bash에서 실패한다.
  Windows에서는 `example/profile.windows.yaml`을, Linux/WSL에서는 `example/profile.yaml`을 쓸 것.
  bash 프로파일은 README대로 `example/gralph` 바이너리가 필요하다 (RUN 라인이 `./gralph`).
  WSL 검증 예: `wsl -e bash -c 'cd /mnt/d/release/utils/gralph && cp dist/gralph example/gralph && ./dist/gralph run example/profile.yaml --max-iterations 10'`
- `gralph next`는 상태가 없으면 state.json을 만들며 커서를 첫 커맨드로 초기화한다.
  부작용 없는 프로파일 검증은 드라이버의 `validate`(존재하지 않는 커맨드 호출 기법)를 쓸 것.
- 프로파일의 커맨드 구성을 바꾼 뒤에는 `reset` 필수: 옛 커서가 남아 있으면
  "state cursor does not match any command" 류 오류나 엉뚱한 노드에서 재개된다.
- store 값은 성공한 커맨드에서만 커밋된다. lua에서 set 후 fail하면 값이 남지 않는다
  (verify의 attempts 카운터처럼 실패에도 남기고 싶은 값은 만들 수 없음에 주의).
- `next:` 후보가 2개 이상인데 lua가 없으면 validate 단계에서 거부된다
  ("has multiple successors but no lua to route them").

## Troubleshooting

| 증상 | 해결 |
|---|---|
| `cannot execute binary file: Exec format error` | Linux용 `dist/gralph`를 실행함. `dist/gralph.exe` 사용 |
| `gralph: no profile: set $GRALPH_PROFILE or pass --profile` | 세션 밖 수동 실행은 `--profile <yaml>` 필요. 드라이버 `call` 사용 |
| `` `X` is not the current command `` | 커서 불일치. `status`로 커서 확인, 필요 시 `reset` |
| `SCRIPT ERROR ... gralph.route("z"): not a successor candidate [a b]` | lua의 route 인자가 해당 노드의 `next:` 목록에 없음 |
| `profile: command "a" lists unknown successor "nope"` | `next:`에 적은 이름의 커맨드가 미정의 |
| `usage error: missing required argument --x` | 인자 누락. 실패 카운트에는 미포함이므로 그냥 다시 호출 |
