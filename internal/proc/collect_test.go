package proc

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSystemCollectorSeesARealChild runs the live collector (ps/lsof on
// macOS, /proc on Linux) against a child process this test starts.
func TestSystemCollectorSeesARealChild(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no collector on " + runtime.GOOS)
	}
	dir := t.TempDir()
	// The child is this test binary, not /bin/sleep: macOS hides the
	// environment of Apple's own binaries even from ps -E.
	child := exec.Command(os.Args[0], "-test.run=TestHelperSleep")
	child.Dir = dir
	child.Env = append(os.Environ(), helperEnv+"=1", "CLAUDE_CODE_SESSION_ID=collector-test", "CLAUDE_CODE_MESSAGING_TOKEN=secret")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := System{}.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	c := snap.Get(child.Process.Pid)
	if c == nil {
		t.Fatalf("child pid %d not in snapshot", child.Process.Pid)
	}
	if c.PPID != os.Getpid() || c.UID != os.Getuid() || c.Name() != filepath.Base(os.Args[0]) {
		t.Errorf("child = %+v", c)
	}
	if c.Env["CLAUDE_CODE_SESSION_ID"] != "collector-test" || c.Env["CLAUDE_CODE_MESSAGING_TOKEN"] != "" {
		t.Errorf("env must hold the session id and drop the token: %v", c.Env)
	}
	wantCwd, _ := filepath.EvalSymlinks(dir)
	if gotCwd, _ := filepath.EvalSymlinks(c.Cwd); gotCwd != wantCwd {
		t.Errorf("cwd = %q, want %q", c.Cwd, wantCwd)
	}
	if age := time.Since(c.Started); age < -5*time.Second || age > time.Minute {
		t.Errorf("started %v ago; clock or boot-time math is off", age)
	}

	self := snap.Get(os.Getpid())
	if self == nil || !containsInt(self.Ports, port) {
		t.Errorf("test process must listen on :%d, got %+v", port, self)
	}
	if containsInt(self.Exposed, port) {
		t.Errorf("a 127.0.0.1 listener is not exposed: %v", self.Exposed)
	}
	if !snap.IsReaper(InitPID) {
		t.Error("PID 1 must count as a reaper")
	}

	cpu, err := System{}.SampleCPU(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cpu[os.Getpid()]; !ok {
		t.Error("CPU sample misses the test process")
	}
}

const helperEnv = "TIDEWAKE_TEST_HELPER"

// TestHelperSleep is the child process for the collector test.
func TestHelperSleep(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
