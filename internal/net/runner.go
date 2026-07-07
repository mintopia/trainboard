package net

import (
	"context"
	"fmt"
	"os/exec"
)

// Runner executes one external command, returning combined stdout+stderr.
// Every OS side effect in this package goes through a Runner: production
// uses ExecRunner, tests use FakeRunner.
type Runner interface {
	Run(ctx context.Context, argv ...string) (string, error)
}

// ExecRunner runs commands via os/exec. The only place in the codebase that
// may exec.
type ExecRunner struct{}

// NewExecRunner returns the production Runner.
func NewExecRunner() Runner { return ExecRunner{} }

// Run executes argv[0] with argv[1:], combined output.
func (ExecRunner) Run(ctx context.Context, argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("net: empty argv")
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
