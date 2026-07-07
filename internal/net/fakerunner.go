package net

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// FakeRunner scripts command responses for tests. Keys are joined argv
// prefixes; the longest matching prefix wins. Unscripted commands error —
// a test must declare every side effect it expects.
//
// FakeRunner is an exported test double kept in the main tree deliberately
// (like display/fake.go) for use across all packages' tests that need to
// mock external commands.
type FakeRunner struct {
	mu      sync.Mutex
	scripts []fakeScript // insertion order; matched by longest prefix
	calls   []string
}

type fakeScript struct {
	prefix string
	out    string
	err    error
}

// NewFakeRunner returns an empty scripted runner.
func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

// Script registers a response for any command whose joined argv starts with
// argvPrefix. Longest prefix wins; later registrations of an equal prefix
// replace earlier ones.
func (f *FakeRunner) Script(argvPrefix, out string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.scripts {
		if s.prefix == argvPrefix {
			f.scripts[i] = fakeScript{argvPrefix, out, err}
			return
		}
	}
	f.scripts = append(f.scripts, fakeScript{argvPrefix, out, err})
}

// Calls returns every executed command as its joined argv, in order.
func (f *FakeRunner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Run records the call and returns the longest-prefix scripted response.
func (f *FakeRunner) Run(_ context.Context, argv ...string) (string, error) {
	joined := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, joined)
	best := -1
	for i, s := range f.scripts {
		if strings.HasPrefix(joined, s.prefix) && (best < 0 || len(s.prefix) > len(f.scripts[best].prefix)) {
			best = i
		}
	}
	if best < 0 {
		return "", fmt.Errorf("net: unscripted command %q", joined)
	}
	return f.scripts[best].out, f.scripts[best].err
}
