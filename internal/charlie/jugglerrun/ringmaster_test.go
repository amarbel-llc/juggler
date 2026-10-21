package jugglerrun

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

func TestCheckRingmasterProtocol_MatchesByDefault(t *testing.T) {
	h := newHarness(t)
	if err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster); err != nil {
		t.Errorf("err = %v, want the fake's default protocol to match jobwake.ProtocolVersion (%d)", err, jobwake.ProtocolVersion)
	}
}

func TestCheckRingmasterProtocol_MismatchRefuses(t *testing.T) {
	h := newHarness(t)
	h.fakes.SetRingmasterProtocolVersion(t, "2")
	err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster)
	if err == nil || !strings.Contains(err.Error(), "protocol 2") || !strings.Contains(err.Error(), "protocol 4") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRingmasterProtocol_UnparseableOutputRefuses(t *testing.T) {
	h := newHarness(t)
	h.fakes.SetRingmasterProtocolVersion(t, "not-a-number")
	if err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRingmasterProtocol_MissingBinaryRefuses(t *testing.T) {
	if err := CheckRingmasterProtocol(context.Background(), "/nonexistent/ringmaster"); err == nil {
		t.Fatal("want an error when the binary cannot be run at all")
	}
}

// startServiceForTest runs ringmaster's host service in this process (S7,
// RFC-0020 §7.3: jobwake.StartService) over a fresh store and a SHORT runtime
// directory (a unix socket path is limited to ~108 bytes, and t.TempDir() under
// a nix build sandbox is longer than that), and returns when it is accepting.
func startServiceForTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runtimeDir, err := os.MkdirTemp("/tmp", "jr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	stop, err := jobwake.StartService(nil)
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	t.Cleanup(stop)
}

// TestJobwakeAcquireJobLock_AliveWhileHeldGoneAfterRelease is clown#256's
// finding 6 lock-identity test, at the jobwake level rather than through
// `ringmaster status`'s liveness field. It calls the REAL
// jobwake.AcquireJobLock (the same function cmd/juggler's `juggler run` wires)
// against a REAL in-process host service (S7: the hold lives in the service,
// over a socket, no longer in a lock file). A job another connection holds is
// "alive" (a second AcquireJobLock gets ErrAlreadyLocked); once released it is
// "gone" and can be taken again. The real bats version (the real ringmaster
// binary and a `juggler run` subprocess) is the reaper test in
// zz-tests_bats/juggler_reaper.bats.
func TestJobwakeAcquireJobLock_AliveWhileHeldGoneAfterRelease(t *testing.T) {
	startServiceForTest(t)
	job, err := jobwake.Start(jobwake.StartOpts{Target: testRoot, Label: "test", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}

	release, err := jobwake.AcquireJobLock(testRoot, job)
	if err != nil {
		t.Fatalf("AcquireJobLock: %v", err)
	}
	if _, err := jobwake.AcquireJobLock(testRoot, job); !errors.Is(err, jobwake.ErrAlreadyLocked) {
		t.Fatalf("second acquire while the first is held: err = %v, want jobwake.ErrAlreadyLocked (alive)", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	release2, err := jobwake.AcquireJobLock(testRoot, job)
	if err != nil {
		t.Fatalf("acquire after release: err = %v, want success (gone)", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}

// TestProtocolCheckedRingmaster_IgnoresACancelledCallerContext: the FIRST
// call through a ProtocolCheckedRingmaster instance runs the once-only check
// under its own context.Background()-derived timeout, never the caller's
// ctx (clown#256 review finding 2). Without that, a caller's ctx that is
// already cancelled when the first call lands — WaitCancelRequested's own
// ctx is exactly this, deliberately cancelled by a SIGTERM or a holder's
// cancel — would latch context.Canceled into checkErr via sync.Once,
// failing every later call sharing this instance, including finish's Done
// under a fresh ctx, and losing the agent's own terminal record.
func TestProtocolCheckedRingmaster_IgnoresACancelledCallerContext(t *testing.T) {
	h := newHarness(t)
	pc := &ProtocolCheckedRingmaster{Bin: h.fakes.Ringmaster}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pc.check(cancelledCtx); err != nil {
		t.Fatalf("err = %v, want the protocol check to ignore the caller's own (already cancelled) ctx entirely", err)
	}
}
