package postgres

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/enums"
)

var testSettings = Settings{Port: 5433, SocketDirectory: "/srv/postgres/run"}

func fakeRunner(result output, err error) func(*exec.Cmd) (output, error) {
	return func(*exec.Cmd) (output, error) { return result, err }
}

func mustCommand(t *testing.T, database string, script string, vars map[string]string) *exec.Cmd {
	t.Helper()
	cmd, err := commandFor(context.Background(), testSettings, tupleFlags, database, script, vars)
	if err != nil {
		t.Fatal(err)
	}

	return cmd
}

func TestVarsTravelInTheEnvironmentNotArgv(t *testing.T) {
	password := "p'a\"s\\s\nwörd"
	script := `ALTER ROLE :"user" PASSWORD :'password'`

	cmd := mustCommand(t, "a b=c", script, map[string]string{"password": password, "user": "bob"})

	for _, arg := range cmd.Args {
		if strings.Contains(arg, password) || strings.Contains(arg, "bob") || arg == "-d" {
			t.Fatalf("argv leaks a value or sets -d: %q", cmd.Args)
		}
	}
	for _, want := range []string{"DATABASIFY_VAR_password=" + password, "DATABASIFY_VAR_user=bob", "PGDATABASE=a b=c"} {
		if !slices.Contains(cmd.Env, want) {
			t.Fatalf("env lacks %q: %q", want, cmd.Env)
		}
	}
	stdin, err := io.ReadAll(cmd.Stdin)
	wantScript := "\\getenv password DATABASIFY_VAR_password\n\\getenv user DATABASIFY_VAR_user\n" + script
	if err != nil || string(stdin) != wantScript {
		t.Fatalf("stdin=%q err=%v", stdin, err)
	}
}

func TestBadVariableNameIsRejected(t *testing.T) {
	for _, name := range []string{"Name", "1a", "a-b", "a b", ""} {
		if _, err := commandFor(context.Background(), testSettings, tupleFlags, "app", "SELECT 1", map[string]string{name: "x"}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestCommandLineAndIdentity(t *testing.T) {
	cmd := mustCommand(t, "app", "SELECT 1", nil)

	want := []string{"psql", "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-h", "/srv/postgres/run", "-p", "5433", "-f", "-"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args=%q", cmd.Args)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil || cmd.SysProcAttr.Credential.Uid != 64999 || cmd.SysProcAttr.Credential.Gid != 64999 {
		t.Fatalf("credential=%+v", cmd.SysProcAttr)
	}
	if !slices.Contains(cmd.Env, "HOME=/var/lib/postgresql") {
		t.Fatalf("env=%q", cmd.Env)
	}
}

func TestQueryReturnsStdout(t *testing.T) {
	psql := execPsql{settings: testSettings, run: fakeRunner(output{stdout: "t\n"}, nil)}

	got, err := psql.Query(context.Background(), "postgres", "SELECT 1", nil)

	if err != nil || got != "t\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestConnectionFailureIsUnavailable(t *testing.T) {
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 2, stderr: "could not connect"}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "SELECT 1", nil)

	var failure command.Failure
	if !errors.As(err, &failure) || failure.Code != enums.ErrorCodeUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestStatementFailureIsSQLError(t *testing.T) {
	message := "ERROR:  role \"x\" does not exist"
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 3, stderr: "psql:<stdin>:1: " + message + "\n"}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "DROP ROLE x", nil)

	var sqlError *SQLError
	if !errors.As(err, &sqlError) || sqlError.Message != message {
		t.Fatalf("err=%v", err)
	}
}

func TestOtherExitCodeIsPlainError(t *testing.T) {
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 1, stderr: "psql: error: out of memory\n"}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "SELECT 1", nil)

	var failure command.Failure
	var sqlError *SQLError
	if err == nil || errors.As(err, &failure) || errors.As(err, &sqlError) || err.Error() != "psql exited 1: error: out of memory" {
		t.Fatalf("err=%v", err)
	}
}

func TestExitOneKeepsOnlyTheServerMessage(t *testing.T) {
	stderr := "psql:<stdin>:2: ERROR:  out of memory\nSTATEMENT:  ALTER ROLE \"bob\" PASSWORD 'secret'\n"
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 1, stderr: stderr}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "x", nil)

	if err == nil || err.Error() != "psql exited 1: ERROR:  out of memory" {
		t.Fatalf("err=%v", err)
	}
}

func TestStartFailureIsReturned(t *testing.T) {
	boom := errors.New("exec: psql not found")
	psql := execPsql{settings: testSettings, run: fakeRunner(output{}, boom)}

	if _, err := psql.Query(context.Background(), "postgres", "SELECT 1", nil); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

func TestTimeoutIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: -1}, nil)}

	_, err := psql.Query(ctx, "postgres", "SELECT 1", nil)

	var failure command.Failure
	if !errors.As(err, &failure) || failure.Code != enums.ErrorCodeUnavailable || failure.Message != "psql timed out after 60s" {
		t.Fatalf("err=%v", err)
	}
}

func TestCallerCancelIsInternalNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	psql := execPsql{settings: testSettings, run: func(*exec.Cmd) (output, error) {
		cancel()

		return output{exit: -1}, nil
	}}

	_, err := psql.Query(ctx, "postgres", "SELECT 1", nil)

	var failure command.Failure
	if err == nil || errors.As(err, &failure) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err=%v", err)
	}
}

func TestServerCancelsTheStatementBeforePsqlIsKilled(t *testing.T) {
	cmd := mustCommand(t, "app", "SELECT 1", nil)

	if !slices.Contains(cmd.Env, "PGOPTIONS=-c statement_timeout=55s") {
		t.Fatalf("env=%q", cmd.Env)
	}
	if cmd.WaitDelay != 5*time.Second {
		t.Fatalf("wait delay=%v", cmd.WaitDelay)
	}
}

func TestRunCommandCapsStdout(t *testing.T) {
	result, err := runCommand(exec.Command("sh", "-c", "head -c 5000000 /dev/zero"))

	if err != nil || len(result.stdout) != stdoutLimit || result.exit != 0 {
		t.Fatalf("len=%d exit=%d err=%v", len(result.stdout), result.exit, err)
	}
}

func TestSQLErrorKeepsOnlyTheServerMessage(t *testing.T) {
	stderr := "psql:<stdin>:3: ERROR:  duplicate key value violates unique constraint \"u_name\"\n" +
		"DETAIL:  Key (name)=(bob) already exists.\n" +
		"LINE 1: CREATE ROLE \"bob\" PASSWORD 'secret'\n" +
		"                                     ^\n" +
		"HINT:  something\n" +
		"CONTEXT:  SQL statement \"x\"\n"
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 3, stderr: stderr}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "x", nil)

	var sqlError *SQLError
	want := "ERROR:  duplicate key value violates unique constraint \"u_name\"\nDETAIL:  Key (name)=(bob) already exists."
	if !errors.As(err, &sqlError) || sqlError.Message != want {
		t.Fatalf("message=%q", err)
	}
}

func TestSQLErrorDropsQueryAndMultiLineContext(t *testing.T) {
	stderr := "psql:<stdin>:1: ERROR:  division by zero\n" +
		"DETAIL:  first\n" +
		"DETAIL:  second\n" +
		"QUERY:  SELECT 1/0 WHERE pw = 'secret'\n" +
		"CONTEXT:  PL/pgSQL function inline_code_block line 3 at SQL statement\n" +
		"    more context with 'secret'\n"
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 3, stderr: stderr}, nil)}

	_, err := psql.Query(context.Background(), "postgres", "x", nil)

	var sqlError *SQLError
	want := "ERROR:  division by zero\nDETAIL:  first\nDETAIL:  second"
	if !errors.As(err, &sqlError) || sqlError.Message != want {
		t.Fatalf("message=%q", err)
	}
}

func statementMessage(t *testing.T, stderr string) string {
	t.Helper()
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 3, stderr: stderr}, nil)}
	_, err := psql.Query(context.Background(), "postgres", "x", nil)
	var sqlError *SQLError
	if !errors.As(err, &sqlError) {
		t.Fatalf("err=%v", err)
	}

	return sqlError.Message
}

func TestServerMessageDropsLineAndCaretKeepsDetail(t *testing.T) {
	stderr := "psql:<stdin>:1: ERROR:  syntax error at or near \"x\"\nLINE 1: SELECT x FROM\n               ^\nDETAIL:  because\nHINT:  try\nSTATEMENT:  SELECT x\n"

	got := statementMessage(t, stderr)

	if got != "ERROR:  syntax error at or near \"x\"\nDETAIL:  because" {
		t.Fatalf("message = %q", got)
	}
}

func TestServerMessageKeepsMultiLineRaiseAndStopsAtContext(t *testing.T) {
	stderr := "psql:<stdin>:3: ERROR:  first line\nsecond line\nthird line\nCONTEXT:  PL/pgSQL function f() line 3 at RAISE\nQUERY:  SELECT 1\n"

	got := statementMessage(t, stderr)

	if got != "ERROR:  first line\nsecond line\nthird line" {
		t.Fatalf("message = %q", got)
	}
}

func TestMissingDatabaseConnectionIsNotUnavailable(t *testing.T) {
	stderr := "psql: error: connection to server on socket \"/run/.s.PGSQL.5432\" failed: FATAL:  database \"ghost\" does not exist\n"
	psql := execPsql{settings: testSettings, run: fakeRunner(output{exit: 2, stderr: stderr}, nil)}

	_, err := psql.Query(context.Background(), "ghost", "SELECT 1", nil)

	var missing *MissingDatabaseError
	if !errors.As(err, &missing) || missing.Message != `database "ghost" does not exist` {
		t.Fatalf("err=%v", err)
	}
}
