package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintopia/trainboard/internal/update"
)

// fakeExec records what would have been exec()d and optionally fails for
// specific binaries (simulating a missing/corrupt slot binary — a crash
// AFTER exec is simulated by just running the launcher again, since a real
// exec never returns).
type fakeExec struct {
	calls   []string   // binaries exec'd, in order
	argvs   [][]string // full argv per call
	failFor []string   // substrings of paths whose exec fails
}

func (f *fakeExec) exec(bin string, argv, _ []string) error {
	f.calls = append(f.calls, bin)
	f.argvs = append(f.argvs, argv)
	for _, s := range f.failFor {
		if strings.Contains(bin, s) {
			return errors.New("exec format error")
		}
	}
	return nil
}

// launcherEnv points the launcher at a temp slots/state layout.
func launcherEnv(t *testing.T, seed *update.State) (slots, statePath string) {
	t.Helper()
	dir := t.TempDir()
	slots = filepath.Join(dir, "slots")
	statePath = filepath.Join(dir, "state.json")
	if seed != nil {
		if err := update.SaveState(statePath, *seed); err != nil {
			t.Fatal(err)
		}
	}
	return slots, statePath
}

func TestLaunchActiveSlotAndBurnAttempt(t *testing.T) {
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	wantBin := filepath.Join(slots, "b", "trainboard")
	if len(fe.calls) != 1 || fe.calls[0] != wantBin {
		t.Fatalf("exec'd %v, want [%s]", fe.calls, wantBin)
	}
	// argv[0] = binary, then passthrough args, no --recovery.
	wantArgv := []string{wantBin, "--production"}
	if len(fe.argvs[0]) != 2 || fe.argvs[0][0] != wantArgv[0] || fe.argvs[0][1] != wantArgv[1] {
		t.Errorf("argv = %v, want %v", fe.argvs[0], wantArgv)
	}
	// Attempt burned BEFORE exec.
	st, err := update.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.BootAttempts != 1 {
		t.Errorf("BootAttempts = %d, want 1", st.BootAttempts)
	}
}

func TestFullRollbackStory(t *testing.T) {
	// A freshly-applied update in slot b that crashes on every boot: three
	// launches burn attempts 1..3 in slot b, the fourth flips back to a.
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	for boot := 1; boot <= 3; boot++ {
		fe := &fakeExec{}
		if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
			t.Fatalf("boot %d: %v", boot, err)
		}
		if want := filepath.Join(slots, "b", "trainboard"); fe.calls[0] != want {
			t.Fatalf("boot %d exec'd %s, want %s", boot, fe.calls[0], want)
		}
	}
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("boot 4: %v", err)
	}
	if want := filepath.Join(slots, "a", "trainboard"); fe.calls[0] != want {
		t.Fatalf("boot 4 exec'd %s, want rollback to %s", fe.calls[0], want)
	}
	st, _ := update.LoadState(statePath)
	if st.Active != "a" || st.RolledBackFrom != "v0.2.0" || st.BootAttempts != 1 {
		t.Errorf("state after rollback: %+v", st)
	}
	// No --recovery on a plain rollback.
	for _, a := range fe.argvs[0] {
		if a == "--recovery" {
			t.Error("rollback boot wrongly passed --recovery")
		}
	}
}

func TestDoubleFaultEntersRecovery(t *testing.T) {
	seed := update.State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	argv := fe.argvs[0]
	if argv[len(argv)-1] != "--recovery" {
		t.Errorf("double fault argv = %v, want trailing --recovery", argv)
	}
	st, _ := update.LoadState(statePath)
	if st.BootAttempts != 0 {
		t.Errorf("BootAttempts = %d, want 0 (operator restart retries a normal boot)", st.BootAttempts)
	}
}

func TestExecFailureFallsBackToKnownGood(t *testing.T) {
	// Slot b's binary is missing/corrupt (exec itself fails): fall back to
	// known-good a in the SAME launch, no --recovery.
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{failFor: []string{filepath.Join("slots", "b")}}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(fe.calls) != 2 || !strings.Contains(fe.calls[1], filepath.Join("a", "trainboard")) {
		t.Fatalf("calls = %v, want [b-bin, a-bin]", fe.calls)
	}
	// The fast-fallback shortcut MUST flip persisted state to known-good the
	// same way the three-strikes rollback branch in Decide does — otherwise
	// a later Promote (health check passes on the fallback-exec'd known-good
	// payload) does s.KnownGood = s.Active and blesses the CORRUPT slot b as
	// known-good, bricking the device on the next apply.
	st, err := update.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	want := update.State{
		Active: "a", ActiveVersion: "v0.1.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 1, RolledBackFrom: "v0.2.0",
	}
	if st != want {
		t.Errorf("persisted state after fast-fallback = %+v, want %+v", st, want)
	}
}

func TestExecFailureOfKnownGoodErrors(t *testing.T) {
	seed := update.State{Active: "a", KnownGood: "a"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{failFor: []string{"trainboard"}}
	if err := launch(slots, statePath, []string{}, fe.exec); err == nil {
		t.Error("known-good exec failure must return an error (systemd Restart=always retries)")
	}
}

func TestMissingStateBootsSlotA(t *testing.T) {
	slots, statePath := launcherEnv(t, nil) // no state file at all
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if want := filepath.Join(slots, "a", "trainboard"); fe.calls[0] != want {
		t.Errorf("exec'd %s, want default slot a", fe.calls[0])
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file not created on first boot: %v", err)
	}
}
