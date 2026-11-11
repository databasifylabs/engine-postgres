package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/databasifylabs/engine-sdk/ctl"
	"github.com/databasifylabs/engine-sdk/units"
)

const (
	defaultToolTimeout = 120 * time.Second
	etcdTimeout        = 30 * time.Second
	etcdClientPort     = "2379"
	etcdPeerPort       = "2380"
	patroniPort        = "8008"
	patroniScope       = "main"
)

type toolRun struct {
	args    []string
	env     []string
	stdin   string
	timeout time.Duration
	asRoot  bool
}

type Tools interface {
	Run(ctx context.Context, name string, run toolRun) (string, error)
}

type execTools struct{}

func (execTools) Run(ctx context.Context, name string, run toolRun) (string, error) {
	timeout := run.timeout
	if timeout == 0 {
		timeout = defaultToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, run.args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + postgresHome}, run.env...)
	cmd.Stdin = strings.NewReader(run.stdin)
	cmd.WaitDelay = pipeWaitDelay
	if !run.asRoot {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: postgresUID, Gid: postgresUID}}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, firstArg(run.args), err, strings.TrimSpace(lastLines(string(out), 5)))
	}

	return string(out), nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}

	return args[0]
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}

	return strings.Join(lines, "\n")
}

type Supervisor interface {
	Do(verb string, units ...string) error
	DoNoBlock(verb string, units ...string) error
	Active(unit string) bool
	Property(unit, property string) string
	Run(transient ctl.Transient) error
	Kill(signal string, units ...string) error
}

var _ Supervisor = units.Control{}

func waitFor(ctx context.Context, timeout time.Duration, every time.Duration, done func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}
