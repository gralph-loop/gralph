#!/usr/bin/env python3
"""Deterministic linter for gralph profiles.

Reproduces gralph's load-time validation (so you catch rejections before
building/running the binary) and adds style + anti-pattern lints aimed at the
deterministic-verification doctrine.

Usage:
    python3 lint_profile.py path/to/profile.yaml

Exit codes:
    0  no errors (warnings may be present)
    1  one or more ERRORs (gralph would reject this, or a serious smell)
    2  could not read/parse the profile

Output lines are prefixed ERROR / WARN / INFO. ERRORs mirror gralph's loader
or flag gates that verify nothing; WARNs are advisory.
"""
import os
import re
import sys

try:
    import yaml
except ImportError:
    sys.stderr.write("lint_profile: PyYAML is required (pip install pyyaml)\n")
    sys.exit(2)

RESERVED = "DONE"


def main(argv):
    if len(argv) != 2:
        sys.stderr.write("usage: lint_profile.py <profile.yaml>\n")
        return 2
    path = argv[1]
    try:
        with open(path, "r", encoding="utf-8") as fh:
            raw = fh.read()
    except OSError as exc:
        sys.stderr.write(f"lint_profile: cannot read {path}: {exc}\n")
        return 2
    try:
        prof = yaml.safe_load(raw) or {}
    except yaml.YAMLError as exc:
        sys.stderr.write(f"lint_profile: YAML parse error: {exc}\n")
        return 2

    profile_dir = os.path.dirname(os.path.abspath(path))
    errors, warns, infos = [], [], []

    def err(m):
        errors.append(m)

    def warn(m):
        warns.append(m)

    def info(m):
        infos.append(m)

    # ---- top-level shape -------------------------------------------------
    if not isinstance(prof, dict):
        err("profile is not a YAML mapping")
        return _report(path, errors, warns, infos)

    commands = prof.get("commands")
    if not commands:
        err("profile: at least one command is required")
        return _report(path, errors, warns, infos)
    if not isinstance(commands, list):
        err("profile: 'commands' must be a list")
        return _report(path, errors, warns, infos)

    ft = prof.get("fail_threshold")
    if ft is not None and (not isinstance(ft, int) or ft <= 0):
        warn("profile: fail_threshold should be a positive integer (gralph "
             "defaults to 5 when <= 0)")

    agent = prof.get("agent") or {}
    agent_cmd = agent.get("command") if isinstance(agent, dict) else None
    if not agent_cmd:
        info("agent.command is absent — fine for in-session use, but "
             "`gralph run` (the loop) requires it.")
    elif isinstance(agent_cmd, list):
        if not any("{{prompt}}" in str(a) for a in agent_cmd):
            warn("agent.command has no {{prompt}} placeholder; the agent will "
                 "not receive the ralph prompt.")

    # ---- per-command, mirroring config.go validate() --------------------
    by_name = {}
    for i, c in enumerate(commands):
        if not isinstance(c, dict):
            err(f"command #{i + 1} is not a mapping")
            continue
        name = c.get("name", "")
        if not name:
            err(f"command #{i + 1} has no name")
            continue
        if name == RESERVED:
            err(f'"{RESERVED}" is a reserved command name')
        if name in by_name:
            err(f'duplicate command name "{name}"')
        by_name[name] = c

    for name, c in by_name.items():
        nxt = c.get("next") or []
        if not isinstance(nxt, list):
            err(f'command "{name}": next must be a list')
            nxt = []
        for n in nxt:
            if n not in by_name:
                err(f'command "{name}" lists unknown successor "{n}"')
        lua = c.get("lua")
        if len(nxt) > 1 and not lua:
            err(f'command "{name}" has {len(nxt)} successors but no lua to '
                f"route them (gralph will refuse to load this)")

        # ---- doctrine + hygiene lints ----------------------------------
        if lua:
            lua_path = lua if os.path.isabs(lua) else os.path.join(profile_dir, lua)
            if not os.path.exists(lua_path):
                err(f'command "{name}": lua script not found at {lua_path}')
            else:
                _lint_lua(name, lua_path, nxt, warn, info)
        else:
            if len(nxt) <= 1:
                warn(f'command "{name}" has no lua — it verifies nothing and '
                     f"will always succeed. Add a deterministic gate unless "
                     f"this node truly needs no check.")

        guidance = c.get("guidance") or ""
        if guidance and "RUN:" not in guidance:
            warn(f'command "{name}": guidance has no "RUN:" line; the agent may '
                 f"not know which command closes the node.")

        # self-attestation smell: a boolean/ack arg the gate likely just trusts
        for a in (c.get("args") or []):
            an = (a.get("name") or "").lower() if isinstance(a, dict) else ""
            if an in {"ok", "done", "success", "passed", "confirm", "confirmed"}:
                warn(f'command "{name}": arg --{an} looks like self-attestation. '
                     f"Gate on an artifact or a re-run check, not the agent's claim.")

    # entry node info
    first = commands[0].get("name") if isinstance(commands[0], dict) else "?"
    info(f"entry node: {first}")
    terminals = [n for n, c in by_name.items() if not (c.get("next") or [])]
    if not terminals:
        warn("no terminal command (every node has a successor) — the cursor can "
             "never reach DONE and the loop will not finish on its own.")
    else:
        info(f"terminal node(s) -> DONE: {', '.join(terminals)}")

    return _report(path, errors, warns, infos)


def _lint_lua(name, lua_path, nxt, warn, info):
    try:
        with open(lua_path, "r", encoding="utf-8") as fh:
            src = fh.read()
    except OSError:
        return
    has_fail = "gralph.fail" in src
    has_route = "gralph.route" in src
    runs_check = bool(re.search(r"os\.execute|io\.popen|io\.open", src))

    if len(nxt) > 1 and not has_route:
        warn(f'command "{name}": {len(nxt)} successors but lua never calls '
             f"gralph.route() — that's a SCRIPT ERROR at runtime.")
    if len(nxt) <= 1 and has_route:
        warn(f'command "{name}": lua calls gralph.route() but the node has '
             f"{len(nxt)} successor(s); routing only applies with >= 2.")
    if not has_fail and not runs_check:
        warn(f'command "{name}": lua never calls gralph.fail and runs no '
             f"os.execute/io check — it cannot reject bad work. Is this a real "
             f"gate?")
    # route target sanity
    for m in re.finditer(r'gralph\.route\(\s*["\']([^"\']+)["\']', src):
        if m.group(1) not in nxt:
            warn(f'command "{name}": lua routes to "{m.group(1)}" which is not '
                 f"in next: {nxt} — runtime error when hit.")


def _report(path, errors, warns, infos):
    for m in infos:
        print(f"INFO  {m}")
    for m in warns:
        print(f"WARN  {m}")
    for m in errors:
        print(f"ERROR {m}")
    n_e, n_w = len(errors), len(warns)
    print(f"\n{path}: {n_e} error(s), {n_w} warning(s)")
    return 1 if n_e else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
