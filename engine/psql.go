package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
)

const (
	psqlTimeout      = 60 * time.Second
	statementTimeout = "55s"
	pipeWaitDelay    = 5 * time.Second
	stdoutLimit      = 4 << 20
	defaultDatabase  = "postgres"
	postgresUID      = 64999
	postgresHome     = "/var/lib/postgresql"

	exitFatal      = 1
	exitConnection = 2
	exitStatement  = 3

	varEnvPrefix = "DATABASIFY_VAR_"
)

var (
	tupleFlags      = []string{"-A", "-t"}
	varName         = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	positionLine    = regexp.MustCompile(`^LINE \d+:`)
	caretLine       = regexp.MustCompile(`^\s*\^+\s*$`)
	missingDatabase = regexp.MustCompile(`FATAL:\s+(database ".*" does not exist)`)
	psqlPosition    = regexp.MustCompile(`^psql:(?:[^:]*:\d+:)? `)
)

type Psql interface {
	Query(ctx context.Context, database string, script string, vars map[string]string) (string, error)
}

type SQLError struct {
	Message string
}

func (e *SQLError) Error() string {
	return e.Message
}

type MissingDatabaseError struct {
	Message string
}

func (e *MissingDatabaseError) Error() string {
	return e.Message
}

type output struct {
	stdout string
	stderr string
	exit   int
}

type execPsql struct {
	settings Settings
	run      func(*exec.Cmd) (output, error)
}

func newExecPsql(settings Settings) execPsql {
	return execPsql{settings: settings, run: runCommand}
}

func (p execPsql) Query(ctx context.Context, database string, script string, vars map[string]string) (string, error) {
	return p.execute(ctx, tupleFlags, database, script, vars)
}

func (p execPsql) execute(ctx context.Context, flags []string, database string, script string, vars map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, psqlTimeout)
	defer cancel()
	cmd, err := commandFor(ctx, p.settings, flags, database, script, vars)
	if err != nil {
		return "", err
	}
	result, err := p.run(cmd)
	if ctx.Err() != nil {
		return "", interruption(ctx.Err())
	}
	if err != nil {
		return "", err
	}

	return result.stdout, failureOf(result)
}

func interruption(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("psql was cancelled: %w", err)
	}

	return command.Unavailable("psql timed out after 60s")
}

func failureOf(result output) error {
	switch result.exit {
	case 0:
		return nil
	case exitConnection:
		if match := missingDatabase.FindStringSubmatch(result.stderr); match != nil {
			return &MissingDatabaseError{Message: match[1]}
		}

		return command.Unavailable(strings.TrimSpace(result.stderr))
	case exitStatement:
		return &SQLError{Message: serverMessage(result.stderr)}
	case exitFatal:
		return fmt.Errorf("psql exited %d: %s", result.exit, serverMessage(result.stderr))
	}

	return fmt.Errorf("psql exited %d: %s", result.exit, strings.TrimSpace(result.stderr))
}

func serverMessage(stderr string) string {
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		line = psqlPosition.ReplaceAllString(line, "")
		if len(kept) == 0 {
			if isErrorLine(line) {
				kept = append(kept, line)
			}

			continue
		}
		if endsMessage(line) {
			break
		}
		if !isErrorContext(line) {
			kept = append(kept, line)
		}
	}
	message := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if message == "" {
		return "the statement failed"
	}

	return message
}

func isErrorLine(line string) bool {
	for _, prefix := range []string{"ERROR:", "FATAL:", "error:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

func endsMessage(line string) bool {
	for _, prefix := range []string{"HINT:", "QUERY:", "CONTEXT:", "STATEMENT:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

func isErrorContext(line string) bool {
	return positionLine.MatchString(line) || caretLine.MatchString(line)
}

func commandFor(ctx context.Context, settings Settings, flags []string, database string, script string, vars map[string]string) (*exec.Cmd, error) {
	names := slices.Sorted(maps.Keys(vars))
	for _, name := range names {
		if !varName.MatchString(name) {
			return nil, fmt.Errorf("invalid psql variable name %q", name)
		}
	}
	args := append([]string{"-X"}, flags...)
	args = append(args, "-q", "-v", "ON_ERROR_STOP=1", "-h", settings.SocketDirectory, "-p", strconv.Itoa(settings.Port), "-f", "-")
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Stdin = strings.NewReader(getenvLines(names) + script)
	cmd.Env = environmentFor(database, vars, names)
	cmd.WaitDelay = pipeWaitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: postgresUID, Gid: postgresUID}}

	return cmd, nil
}

func getenvLines(names []string) string {
	var lines strings.Builder
	for _, name := range names {
		lines.WriteString("\\getenv " + name + " " + varEnvPrefix + name + "\n")
	}

	return lines.String()
}

func environmentFor(database string, vars map[string]string, names []string) []string {
	env := []string{"HOME=" + postgresHome, "USER=postgres", "PATH=" + os.Getenv("PATH"), "PGDATABASE=" + database, "PGOPTIONS=-c statement_timeout=" + statementTimeout}
	for _, name := range names {
		env = append(env, varEnvPrefix+name+"="+vars[name])
	}

	return env
}

type failingPsql struct {
	err error
}

func (p failingPsql) Query(context.Context, string, string, map[string]string) (string, error) {
	return "", p.err
}

func runCommand(cmd *exec.Cmd) (output, error) {
	stdout := cappedBuffer{limit: stdoutLimit}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		return output{}, err
	}

	return output{stdout: stdout.buffer.String(), stderr: stderr.String(), exit: cmd.ProcessState.ExitCode()}, nil
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(chunk []byte) (int, error) {
	room := b.limit - b.buffer.Len()
	b.buffer.Write(chunk[:min(len(chunk), room)])

	return len(chunk), nil
}
