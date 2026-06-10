# Example: release-notes pipeline (structured-evidence gates)

A **non-coding** workflow, included to show how to gate work whose quality is
subjective. You can't deterministically check that prose is good — but you can
deterministically check that the agent did the falsifiable work and didn't
fabricate anything.

## Graph

```
gather → draft → verify_citations → publish
```

- **gather** — collect changes into a source file; gate confirms it exists and
  contains real PR refs (`#NNN`), and records `pr_count` (parse-the-report,
  recipe 2, via pattern counting).
- **draft** — write the notes; gate confirms the file exists, is non-trivial,
  and cites at least one PR (artifact-exists + content check).
- **verify_citations** — the showcase gate: **cross-checks every `#NNN` cited
  in the notes against the source**, failing with the list of fabricated refs.
  This is the structured-evidence pattern (recipe 5): it can't judge whether the
  notes are *well written*, but it mechanically catches hallucinated citations.
- **publish** — semver version validation; terminal → `DONE`.

## Run it without a model

`agent.sh` writes real files. Its first draft deliberately cites a fabricated
PR (`#999`) so `verify_citations` fails; the agent then rewrites the notes
correctly and the gate passes — all within one session.

```sh
gralph run profile.yaml --max-iterations 10
```

Expected: `gather → draft → verify_citations` (fails once on `#999`, then passes
in the same session) `→ publish → DONE`, 4 iterations. Watch for:

```
[cmd] FAILED `verify_citations` (failure 1): ... notes cite PRs not in the source: #999 ...
[cmd] OK: `verify_citations` succeeded. ...
```

That FAILED-then-OK inside one iteration is the in-session retry: a fixable
problem doesn't burn a whole session.

## The point

The gate that matters here — `verify_citations` — proves a property the agent
cannot talk its way around (every citation resolves to a real source entry),
while staying honest about what it *can't* check (writing quality). That's the
right shape for "soft" work: pin down the falsifiable part, leave the rest to
the agent, and keep an audit trail (`cited_prs` lands in the store).
