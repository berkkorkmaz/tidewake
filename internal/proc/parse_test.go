package proc

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"
)

func TestParsePSKeepsCommandSpacingAndStart(t *testing.T) {
	out := []byte("  501     1  7657   501 S      71680 Tue Sep 29 07:55:55 2026     /tmp/venv/bin/python -m http.server  8000\n" +
		"  502   501   502   501 Ss       100 Fri Oct  2 15:12:34 2026     /Applications/Brave Browser.app/Contents/MacOS/Brave Browser --headless=new\n")
	procs, err := ParsePS(out, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	p := procs[501]
	if p == nil || p.PPID != 1 || p.PGID != 7657 || p.RSSKB != 71680 || p.Stat != "S" {
		t.Fatalf("pid 501 parsed wrong: %+v", p)
	}
	if p.Command != "/tmp/venv/bin/python -m http.server  8000" {
		t.Fatalf("command = %q", p.Command)
	}
	if want := time.Date(2026, 9, 29, 7, 55, 55, 0, time.UTC); !p.Started.Equal(want) {
		t.Fatalf("started = %v, want %v", p.Started, want)
	}
	if got := procs[502].Started.Day(); got != 2 {
		t.Fatalf("single-digit day parsed as %d", got)
	}
}

func TestParsePSRejectsTruncatedLine(t *testing.T) {
	if _, err := ParsePS([]byte("501 1 7657 501 S 100\n"), time.UTC); err == nil {
		t.Fatal("expected an error for a line without lstart and command")
	}
}

func TestParseLsofPathByPID(t *testing.T) {
	out := []byte("p100\nfcwd\nn/Users/me/repo\np200\nfcwd\nn/private/tmp/x y\n")
	got := ParseLsofPathByPID(out)
	want := map[int]string{100: "/Users/me/repo", 200: "/private/tmp/x y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseLsofPortsDedupesIPv4AndIPv6(t *testing.T) {
	out := []byte("p100\nf5\nn*:8000\nf6\nn[::1]:8000\nf7\nn127.0.0.1:3000\np200\nf3\nnlocalhost:garbage\n")
	got := ParseLsofPorts(out)
	want := map[int][]int{100: {8000, 3000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func procArgs(argc uint32, parts ...string) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, argc)
	for _, p := range parts {
		buf = append(buf, p...)
		buf = append(buf, 0)
	}
	return buf
}

func TestParseProcArgs2KeepsOnlyAllowListedEnv(t *testing.T) {
	buf := procArgs(2,
		"/usr/bin/node", "", "", // exec path then padding NULs
		"node", "server.js", // argv
		"CLAUDE_CODE_SESSION_ID=abc", "CLAUDE_CODE_MESSAGING_TOKEN=secret", "PATH=/bin", "CODEX_THREAD_ID=t1",
	)
	got, err := ParseProcArgs2(buf)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CLAUDE_CODE_SESSION_ID": "abc", "CODEX_THREAD_ID": "t1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseProcArgs2DoesNotReadArgvAsEnv(t *testing.T) {
	buf := procArgs(2, "/bin/sh", "sh", "CLAUDE_CODE_SESSION_ID=from-argv")
	got, err := ParseProcArgs2(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("argv leaked into env: %v", got)
	}
}

func TestParseProcArgs2ShortBuffer(t *testing.T) {
	if _, err := ParseProcArgs2([]byte{1, 0}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSnapshotTreeHelpers(t *testing.T) {
	s := &Snapshot{Procs: map[int]*Process{
		1:  {PID: 1},
		10: {PID: 10, PPID: 1},
		11: {PID: 11, PPID: 10},
		12: {PID: 12, PPID: 11},
		13: {PID: 13, PPID: 10},
	}}
	anc := s.Ancestors(12)
	if len(anc) != 2 || anc[0].PID != 11 || anc[1].PID != 10 {
		t.Fatalf("ancestors = %v", anc)
	}
	if got := len(s.Descendants(10)); got != 3 {
		t.Fatalf("descendants of 10 = %d, want 3", got)
	}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Procs[10].Started = started
	if !s.AliveSince(10, started) || s.AliveSince(10, started.Add(time.Second)) {
		t.Fatal("AliveSince must detect PID reuse")
	}
}

func TestParseLaunchctlList(t *testing.T) {
	out := []byte("PID\tStatus\tLabel\n70704\t0\tapplication.dev.kdrag0n.MacVirt\n-\t0\tcom.apple.idle\n123\t-9\tcom.example.svc\n")
	got := ParseLaunchctlList(out)
	if !got[70704] || !got[123] || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
