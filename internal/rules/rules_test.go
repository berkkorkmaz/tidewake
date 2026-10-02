package rules

import "testing"

func TestEmbeddedPackLoads(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if p.Harnesses[name].SessionEnv == "" {
			t.Fatalf("harness %s has no session env", name)
		}
	}
	if p.Harnesses["claude"].SessionEnv != "CLAUDE_CODE_SESSION_ID" {
		t.Fatal("claude session env changed")
	}
}

func TestParseRejectsBadPacks(t *testing.T) {
	cases := map[string]string{
		"unknown harness": `{"harnesses":{},"knownIssues":[{"harness":"x","status":"open"}]}`,
		"bad fixedIn":     `{"harnesses":{"c":{}},"knownIssues":[{"harness":"c","status":"fixed","fixedIn":"soon"}]}`,
		"bad status":      `{"harnesses":{"c":{}},"knownIssues":[{"harness":"c","status":"maybe"}]}`,
		"not json":        `{`,
	}
	for name, data := range cases {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.287 (Claude Code)":       "2.1.287",
		"codex-cli 0.160.0":           "0.160.0",
		"rust-v0.148.0":               "0.148.0",
		"codex-cli 0.155.0-alpha.9.2": "0.155.0",
	}
	for in, want := range cases {
		v, err := ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %s", in, v, err, want)
		}
	}
	if _, err := ParseVersion("no digits here"); err == nil {
		t.Error("expected error")
	}
}

func TestVersionLess(t *testing.T) {
	v := func(s string) Version { x, _ := ParseVersion(s); return x }
	if !v("2.1.99").Less(v("2.1.100")) {
		t.Error("numeric compare, not string compare")
	}
	if v("2.1.283").Less(v("2.1.283")) {
		t.Error("equal is not less")
	}
	if !v("2.1").Less(v("2.1.1")) {
		t.Error("missing part counts as zero")
	}
}

func TestHarnessMatching(t *testing.T) {
	p, _ := Load()
	c := p.Harnesses["claude"]
	if !c.IsHarnessProcess("claude", "claude --resume") {
		t.Error("claude binary not recognised")
	}
	if c.IsHarnessProcess("node", "node /Users/me/mcp-server/index.js") {
		t.Error("an MCP server is not the harness")
	}
	if !c.InAgentPath("/private/tmp/claude-501/x/scratchpad/venv/bin/python") {
		t.Error("scratchpad path not recognised")
	}
}
