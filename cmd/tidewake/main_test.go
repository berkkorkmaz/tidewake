package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunUsageAndVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage") {
		t.Fatalf("no args: code=%d stderr=%q", code, errOut.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "tidewake ") {
		t.Fatalf("version: code=%d out=%q", code, out.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"nuke"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "nuke"`) {
		t.Fatalf("unknown: code=%d stderr=%q", code, errOut.String())
	}
}
