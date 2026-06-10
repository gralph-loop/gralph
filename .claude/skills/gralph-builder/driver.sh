#!/usr/bin/env bash
# gralph 워크플로 작성/검증 하네스 (Git Bash / Linux 공용).
# 에이전트 없이 프로파일을 검증하고 노드를 한 칸씩 수동 실행한다.
#
#   driver.sh validate <profile>                 프로파일 YAML 구조 검증 (상태 부작용 없음)
#   driver.sh next     <profile>                 현재 커서 노드의 안내문 출력
#   driver.sh call     <profile> <cmd> [--a v]   노드 1개 수동 실행 후 상태 출력
#   driver.sh status   <profile>                 커서 / 실패 카운트 / store 출력
#   driver.sh reset    <profile>                 상태 디렉터리 삭제 (처음부터 다시)
#   driver.sh smoke    <profile> [max-iter]      실제 루프 실행 (기본 20회 제한)
#
# 바이너리 탐색 순서: $GRALPH_BIN > PATH의 gralph(.exe) > 개발 저장소의 dist/
set -u

SKILL_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) WINDOWS=1 ;;
  *)                    WINDOWS=0 ;;
esac

resolve_bin() {
  if [ -n "${GRALPH_BIN:-}" ]; then
    echo "$GRALPH_BIN"
    return 0
  fi
  # Windows에서는 확장자 없는 gralph(Linux ELF일 수 있음)보다 gralph.exe를 우선한다.
  if [ "$WINDOWS" = 1 ]; then
    names="gralph.exe gralph"
  else
    names="gralph"
  fi
  for n in $names; do
    if command -v "$n" >/dev/null 2>&1; then
      command -v "$n"
      return 0
    fi
  done
  # 개발 저장소 안에서 실행 중이라면 dist/ 폴백.
  for c in "$ROOT/dist/gralph.exe" "$ROOT/dist/gralph"; do
    if [ "$WINDOWS" = 1 ] && [ "${c##*.}" != "exe" ]; then continue; fi
    if [ -x "$c" ]; then
      echo "$c"
      return 0
    fi
  done
  return 1
}

BIN="$(resolve_bin)" || {
  echo "[driver] gralph 바이너리를 찾을 수 없습니다." >&2
  echo "[driver] gralph를 PATH에 추가하거나 GRALPH_BIN=<경로>로 지정하세요." >&2
  exit 1
}

# 프로파일의 state_dir(기본 .gralph-state, 프로파일 기준 상대경로)을 절대경로로.
# 주의: 단순 grep 파싱이므로 최상위 state_dir 키만 인식한다.
state_dir_of() {
  prof="$1"
  pdir="$(cd "$(dirname "$prof")" && pwd)"
  sd="$(grep -E '^state_dir:' "$prof" 2>/dev/null | head -1 \
        | sed -e 's/^state_dir:[[:space:]]*//' -e 's/[[:space:]]*#.*$//' -e "s/[\"']//g")"
  [ -z "$sd" ] && sd=".gralph-state"
  case "$sd" in
    /*|[A-Za-z]:*) echo "$sd" ;;
    *)             echo "$pdir/$sd" ;;
  esac
}

show_status() {
  sd="$(state_dir_of "$1")"
  echo "[state_dir] $sd"
  if [ -f "$sd/state.json" ]; then
    echo "--- state.json (커서/실패 카운트) ---"; cat "$sd/state.json"; echo
  else
    echo "(state.json 없음: 아직 시작 전)"
  fi
  if [ -f "$sd/store.json" ]; then
    echo "--- store.json (유저 store) ---"; cat "$sd/store.json"; echo
  else
    echo "(store.json 없음)"
  fi
}

cmd="${1:-}"; shift || true
case "$cmd" in
  validate)
    prof="${1:?usage: driver.sh validate <profile>}"
    # 존재하지 않는 커맨드 이름으로 호출하면 LoadProfile(검증)만 수행되고
    # 상태 파일은 쓰지 않는다. "unknown command" 응답 = 프로파일 자체는 유효.
    out="$("$BIN" __builder_probe__ --profile "$prof" 2>&1)"
    if echo "$out" | grep -q 'unknown command\|already complete'; then
      echo "[driver] OK: profile is valid: $prof"
    else
      echo "[driver] INVALID profile:"
      echo "$out"
      exit 1
    fi
    ;;

  next)
    prof="${1:?usage: driver.sh next <profile>}"
    "$BIN" next --profile "$prof"
    ;;

  call)
    prof="${1:?usage: driver.sh call <profile> <command> [--arg value ...]}"
    node="${2:?usage: driver.sh call <profile> <command> [--arg value ...]}"
    shift 2
    "$BIN" "$node" --profile "$prof" "$@"
    code=$?
    echo "[driver] exit=$code"
    show_status "$prof"
    exit $code
    ;;

  status)
    prof="${1:?usage: driver.sh status <profile>}"
    show_status "$prof"
    ;;

  reset)
    prof="${1:?usage: driver.sh reset <profile>}"
    sd="$(state_dir_of "$prof")"
    rm -rf "$sd"
    echo "[driver] removed $sd"
    ;;

  smoke)
    prof="${1:?usage: driver.sh smoke <profile> [max-iter]}"
    iters="${2:-20}"
    "$BIN" run "$prof" --max-iterations "$iters"
    ;;

  *)
    sed -n '2,12p' "$0"
    exit 2
    ;;
esac
