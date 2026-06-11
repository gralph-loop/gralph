package main

import (
	"strings"
	"testing"
)

const graphProfile = `commands:
  - name: plan
    next: [verify]
  - name: verify
    lua: route.lua
    next: [fix, finish]
  - name: fix
    next: [verify]
  - name: finish
    subcommands:
      - name: check
        count: 3
        key: k
        args:
          - name: k
      - name: lint
`

func TestRenderGraph(t *testing.T) {
	p := writeProfile(t, graphProfile, map[string]string{"route.lua": ""})

	out := renderGraph(p, "")
	for _, want := range []string{
		"flowchart TD",
		`c0["plan"]`,
		`c1["verify"]`,
		`c3["finish [check x3, lint]"]`, // subcommand quotas in the label
		`DONE(["DONE"])`,                // terminal sentinel node
		"c0 --> c1",                     // single successor: plain edge
		"c1 -->|route| c2",              // branching: lua-routed edges
		"c1 -->|route| c3",
		"c2 --> c1",   // cycle back into verify
		"c3 --> DONE", // last command flows into DONE
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("graph output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "style ") {
		t.Fatalf("no highlight expected without --state:\n%s", out)
	}
}

func TestRenderGraphHighlightsCursor(t *testing.T) {
	p := writeProfile(t, graphProfile, map[string]string{"route.lua": ""})

	// Untouched state: the cursor-to-be is the first command.
	cursor, err := graphCursor(p)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "plan" {
		t.Fatalf("cursor = %q, want plan", cursor)
	}
	if out := renderGraph(p, cursor); !strings.Contains(out, "style c0 ") {
		t.Fatalf("graph output missing plan highlight:\n%s", out)
	}

	// After the first command succeeds the highlight follows the cursor.
	run(t, p, "plan")
	cursor, err = graphCursor(p)
	if err != nil {
		t.Fatal(err)
	}
	if out := renderGraph(p, cursor); !strings.Contains(out, "style c1 ") {
		t.Fatalf("graph output missing verify highlight:\n%s", out)
	}

	// A finished graph highlights the DONE node.
	if out := renderGraph(p, DoneCursor); !strings.Contains(out, "style DONE ") {
		t.Fatalf("graph output missing DONE highlight:\n%s", out)
	}
}
