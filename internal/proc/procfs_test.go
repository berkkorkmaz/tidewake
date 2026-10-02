package proc

import (
	"testing"
	"time"
)

func TestParseProcStatHandlesTrickyNames(t *testing.T) {
	// Real line from a Debian container, with the name swapped for one
	// containing spaces and a ")".
	line := "4242 (node (mcp) x) S 1 4200 1 0 -1 4194304 45 0 1 0 250 125 0 0 20 0 1 0 25863844 2646016 260 18446744073709551615 0"
	st, err := ParseProcStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 4242 || st.Comm != "node (mcp) x" || st.State != "S" || st.PPID != 1 || st.PGID != 4200 {
		t.Fatalf("got %+v", st)
	}
	if st.CPUTicks != 375 || st.StartTicks != 25863844 || st.RSSPages != 260 {
		t.Fatalf("got %+v", st)
	}
	if TicksToDuration(st.CPUTicks) != 3750*time.Millisecond {
		t.Fatalf("ticks = %v", TicksToDuration(st.CPUTicks))
	}
	for _, bad := range []string{"", "12 noname S 1", "x (a) S 1 2"} {
		if _, err := ParseProcStat(bad); err == nil {
			t.Errorf("ParseProcStat(%q): expected error", bad)
		}
	}
}

func TestParseStatusAndBootTime(t *testing.T) {
	uid, err := ParseStatusUID("Name:\tcat\nPPid:\t1\nUid:\t1000\t1000\t1000\t1000\n")
	if err != nil || uid != 1000 {
		t.Fatalf("uid = %d, %v", uid, err)
	}
	if _, err := ParseStatusUID("Name:\tcat\n"); err == nil {
		t.Fatal("expected error")
	}
	bt, err := ParseBootTime("cpu 1 2 3\nbtime 1790695267\nprocesses 9\n")
	if err != nil || bt.Unix() != 1790695267 {
		t.Fatalf("btime = %v, %v", bt, err)
	}
}

func TestParseCmdlineAndEnviron(t *testing.T) {
	if got := ParseCmdline([]byte("python3\x00-m\x00http.server\x008765\x00")); got != "python3 -m http.server 8765" {
		t.Fatalf("cmdline = %q", got)
	}
	env := ParseEnviron([]byte("PATH=/bin\x00CLAUDE_CODE_SESSION_ID=abc\x00CLAUDE_CODE_MESSAGING_TOKEN=secret\x00"))
	if len(env) != 1 || env["CLAUDE_CODE_SESSION_ID"] != "abc" {
		t.Fatalf("env = %v", env)
	}
}

func TestIsServiceCgroup(t *testing.T) {
	cases := map[string]bool{
		"0::/system.slice/postgresql.service\n":                                                     true,
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/syncthing.service\n":            true,
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-terminal-abc.scope\n": false,
		"0::/user.slice/user-1000.slice/session-3.scope\n":                                          false,
		"0::/\n": false,
	}
	for in, want := range cases {
		if got := IsServiceCgroup(in); got != want {
			t.Errorf("IsServiceCgroup(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseNetTCP(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:223D 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 41001 1 0000000000000000 100 0 0 10 0
   1: 0100007F:2240 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 41002 1 0000000000000000 100 0 0 10 0
   2: 0100007F:9C40 0100007F:223D 01 00000000:00000000 00:00000000 00000000  1000        0 41003 1 0000000000000000 100 0 0 10 0
`
	got := ParseNetTCP(tcp)
	if len(got) != 2 {
		t.Fatalf("want 2 listeners (established socket skipped), got %+v", got)
	}
	if got[0] != (Listener{Inode: 41001, Port: 8765, Wildcard: true}) || got[1] != (Listener{Inode: 41002, Port: 8768, Wildcard: false}) {
		t.Fatalf("got %+v", got)
	}
	tcp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 51001 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0BB9 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 51002 1 0000000000000000 100 0 0 10 0
`
	got6 := ParseNetTCP(tcp6)
	if len(got6) != 2 || !got6[0].Wildcard || got6[0].Port != 3000 || got6[1].Wildcard {
		t.Fatalf("tcp6 = %+v", got6)
	}
}

func TestSocketInode(t *testing.T) {
	if n, ok := SocketInode("socket:[41001]"); !ok || n != 41001 {
		t.Fatalf("got %d %v", n, ok)
	}
	if _, ok := SocketInode("/dev/null"); ok {
		t.Fatal("not a socket")
	}
}
