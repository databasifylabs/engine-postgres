package postgres

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const quotedPassword = "p'a\"s\\s\nwörd"

func userCreateInput(t *testing.T, extra string) []byte {
	t.Helper()

	return []byte(`{"name":"alice","password":"p'a\"s\\s\nwörd","grants":[{"database":"shop","access":"write"},{"database":"audit","access":"write"}]` + extra + `}`)
}

func TestUserCreateEnsuresRoleSetsPasswordAndGrants(t *testing.T) {
	for reply, want := range map[string]bool{"t\n": true, "f\n": false} {
		engine, psql := primaryWith(map[string]string{"pg_roles": reply, "datistemplate": "shop\naudit\n"})

		got, err := engine.Accounts().CreateUser(context.Background(), request[requests.UserCreate](t, userCreateInput(t, `,"encryption":null`)))

		if err != nil || got != (responses.UserCreate{Created: want}) {
			t.Fatalf("reply %q: got %#v err %v", reply, got, err)
		}
		first := psql.writes()[1]
		if first.vars["name"] != "alice" || first.vars["password"] != quotedPassword {
			t.Fatalf("vars = %#v", first.vars)
		}
		for _, want := range []string{`CREATE ROLE :"name" LOGIN`, `ALTER ROLE :"name" LOGIN PASSWORD :'password'`, `\if :created`} {
			if !strings.Contains(first.script, want) {
				t.Fatalf("script lacks %q: %s", want, first.script)
			}
		}
		if strings.Contains(first.script, quotedPassword) {
			t.Fatal("the password is inlined in the script")
		}
	}
}

func TestUserCreateGrantsEveryDatabase(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\naudit\n"})

	if _, err := engine.Accounts().CreateUser(context.Background(), request[requests.UserCreate](t, userCreateInput(t, ""))); err != nil {
		t.Fatal(err)
	}

	var schemaGrants []string
	for _, c := range psql.writes() {
		if strings.Contains(c.script, "GRANT ALL ON SCHEMA public") {
			schemaGrants = append(schemaGrants, c.database)
		}
	}
	if !slices.Equal(schemaGrants, []string{"shop", "audit"}) {
		t.Fatalf("schema grants on %v", schemaGrants)
	}
}

func TestEncryptionIsRejected(t *testing.T) {
	engine, psql := primaryWith(nil)
	inputs := map[string]handler{
		`{"name":"a","password":"b","grants":[],"encryption":"scram"}`: userCreate,
		`{"name":"a","password":"b","encryption":"scram"}`:             userSetPassword,
	}
	for input, handler := range inputs {
		_, err := handler(engine, context.Background(), []byte(input))

		if failureCode(t, err) != enums.ErrorCodeInvalidRequest || !strings.Contains(err.Error(), "no password encryption choice") {
			t.Fatalf("%s: err = %v", input, err)
		}
	}
	if len(psql.writes()) != 0 {
		t.Fatalf("writes = %#v", psql.writes())
	}
}

func TestUserSetPasswordOfMissingRoleIsNotFound(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "f\n"})

	err := engine.Accounts().SetPassword(context.Background(), request[requests.UserSetPassword](t, []byte(`{"name":"ghost","password":"x"}`)))

	if failureCode(t, err) != enums.ErrorCodeNotFound {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(psql.scripts(), "\n"), "ALTER ROLE") {
		t.Fatal("altered a missing role")
	}
}

func TestUserSetPasswordAltersRole(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n"})

	err := engine.Accounts().SetPassword(context.Background(), request[requests.UserSetPassword](t, []byte(`{"name":"alice","password":"p'w"}`)))

	if err != nil {
		t.Fatal(err)
	}
	last := psql.calls[len(psql.calls)-1]
	if !strings.Contains(last.script, `ALTER ROLE :"name" PASSWORD :'password'`) || last.vars["password"] != "p'w" || last.vars["name"] != "alice" {
		t.Fatalf("call = %#v", last)
	}
}

func TestUserGrantOfMissingRoleIsNotFound(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "f\n"})

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"ghost","grants":[{"database":"shop","access":"write"}],"replication":false}`)))

	if failureCode(t, err) != enums.ErrorCodeNotFound || strings.Contains(strings.Join(psql.scripts(), "\n"), "GRANT") {
		t.Fatalf("err = %v", err)
	}
}

func TestUserGrantOnMissingDatabaseIsNotFound(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\n"})

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"alice","grants":[{"database":"ghost","access":"write"}],"replication":false}`)))

	if failureCode(t, err) != enums.ErrorCodeNotFound {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(psql.scripts(), "\n"), "GRANT") {
		t.Fatal("granted with a missing database")
	}
}

func TestUserGrantGrantsDatabaseAndSchema(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\naudit\n"})

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"alice","grants":[{"database":"shop","access":"write"}],"replication":false}`)))

	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(psql.scripts(), "\n")
	for _, want := range []string{`GRANT ALL PRIVILEGES ON DATABASE :"db" TO :"name"`, `GRANT ALL ON SCHEMA public TO :"name"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scripts lack %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "REPLICATION") {
		t.Fatal("replication granted when false")
	}
	last := psql.calls[len(psql.calls)-1]
	if last.database != "shop" || last.vars["name"] != "alice" {
		t.Fatalf("schema grant call = %#v", last)
	}
}

func TestUserGrantReplicationAltersRole(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n"})

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"alice","grants":[],"replication":true}`)))

	if err != nil || !strings.Contains(strings.Join(psql.scripts(), "\n"), `ALTER ROLE :"name" REPLICATION`) {
		t.Fatalf("err=%v scripts=%v", err, psql.scripts())
	}
}

func TestUserDropOfMissingRoleReportsNotDropped(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "f\n"})

	got, err := engine.Accounts().DropUser(context.Background(), request[requests.UserDrop](t, []byte(`{"name":"ghost"}`)))

	if err != nil || got != (responses.UserDrop{Dropped: false}) {
		t.Fatalf("got %#v err %v", got, err)
	}
	if strings.Contains(strings.Join(psql.scripts(), "\n"), "DROP") {
		t.Fatal("dropped something")
	}
}

func TestUserDropReassignsInEveryDatabaseBeforeDroppingTheRole(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "postgres\nshop\naudit\n"})

	got, err := engine.Accounts().DropUser(context.Background(), request[requests.UserDrop](t, []byte(`{"name":"alice"}`)))

	if err != nil || got != (responses.UserDrop{Dropped: true}) {
		t.Fatalf("got %#v err %v", got, err)
	}
	var reassigned []string
	for _, c := range psql.calls {
		if strings.Contains(c.script, `REASSIGN OWNED BY :"name" TO postgres`) && strings.Contains(c.script, `DROP OWNED BY :"name"`) {
			reassigned = append(reassigned, c.database)
		}
	}
	if !slices.Equal(reassigned, []string{"postgres", "shop", "audit"}) {
		t.Fatalf("reassigned in %v", reassigned)
	}
	last := psql.calls[len(psql.calls)-1]
	if !strings.Contains(last.script, `DROP ROLE :"name"`) || last.vars["name"] != "alice" {
		t.Fatalf("last call = %#v", last)
	}
}

func TestUserCommandsRefuseOnReplica(t *testing.T) {
	inputs := map[string]handler{
		`{"name":"a","password":"b","grants":[]}`:      userCreate,
		`{"name":"a","password":"b"}`:                  userSetPassword,
		`{"name":"a","grants":[],"replication":false}`: userGrant,
		`{"name":"a"}`: userDrop,
	}
	for input, handler := range inputs {
		engine, psql := replica()

		_, err := handler(engine, context.Background(), []byte(input))

		if failureCode(t, err) != enums.ErrorCodeNotPrimary || len(psql.calls) != 1 {
			t.Fatalf("%s: err=%v calls=%d", input, err, len(psql.calls))
		}
	}
}

func TestUserStatementFailureReachesTheCallerAsInternalWithTheReducedMessage(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n"})
	engine.psql = &failingAfterPrimary{inner: psql, err: &SQLError{Message: "ERROR:  role \"alice\" cannot be dropped"}}
	var stdout bytes.Buffer

	exit := command.Run(context.Background(), engine, "0.2.0", []string{"user", "drop"}, strings.NewReader(`{"name":"alice"}`), &stdout)

	if exit != 70 || !strings.Contains(stdout.String(), `"code":"internal"`) || !strings.Contains(stdout.String(), "cannot be dropped") {
		t.Fatalf("exit=%d stdout=%s", exit, stdout.String())
	}
}

type failingAfterPrimary struct {
	inner *recordingPsql
	err   error
}

func (f *failingAfterPrimary) Query(ctx context.Context, database string, script string, vars map[string]string) (string, error) {
	if strings.Contains(script, "DROP") || strings.Contains(script, "datistemplate") {
		return "", f.err
	}

	return f.inner.Query(ctx, database, script, vars)
}

func TestUserCreateWithAMissingDatabaseWritesNothing(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"datistemplate": "shop\n"})

	_, err := engine.Accounts().CreateUser(context.Background(), request[requests.UserCreate](t, []byte(`{"name":"alice","password":"x","grants":[{"database":"shop","access":"write"},{"database":"ghost","access":"write"}]}`)))

	if failureCode(t, err) != enums.ErrorCodeNotFound {
		t.Fatalf("err = %v", err)
	}
	joined := strings.Join(psql.scripts(), "\n")
	for _, forbidden := range []string{"CREATE ROLE", "ALTER ROLE", "GRANT"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("wrote %q before failing: %s", forbidden, joined)
		}
	}
}

func TestUserGrantWithAMissingDatabaseWritesNothing(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\n"})

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"alice","grants":[{"database":"shop","access":"write"},{"database":"ghost","access":"write"}],"replication":true}`)))

	joined := strings.Join(psql.scripts(), "\n")
	if failureCode(t, err) != enums.ErrorCodeNotFound || strings.Contains(joined, "GRANT") || strings.Contains(joined, "REPLICATION") {
		t.Fatalf("err=%v scripts=%s", err, joined)
	}
}

func TestUserDropOnlyVisitsConnectableNonTemplateDatabases(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n"})

	if _, err := engine.Accounts().DropUser(context.Background(), request[requests.UserDrop](t, []byte(`{"name":"alice"}`))); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.Join(psql.scripts(), "\n"), "NOT datistemplate AND datallowconn") {
		t.Fatalf("scripts = %v", psql.scripts())
	}
}

func TestWritesRefuseReservedNames(t *testing.T) {
	inputs := map[string]handler{
		`{"name":"postgres"}`:                         databaseCreate,
		`{"name":"template1"}`:                        databaseDrop,
		`{"name":"admin","password":"x","grants":[]}`: userCreate,
		`{"name":"alice","password":"x","grants":[{"database":"postgres","access":"write"}]}`:       userCreate,
		`{"name":"replicator","password":"x"}`:                                                      userSetPassword,
		`{"name":"alice","grants":[{"database":"template0","access":"write"}],"replication":false}`: userGrant,
		`{"name":"patroni"}`: userDrop,
	}
	for input, handler := range inputs {
		engine, psql := primaryWith(nil)

		_, err := handler(engine, context.Background(), []byte(input))

		if failureCode(t, err) != enums.ErrorCodeInvalidRequest || !strings.HasSuffix(err.Error(), " is reserved") || len(psql.writes()) != 0 {
			t.Fatalf("%s: err=%v writes=%d", input, err, len(psql.writes()))
		}
	}
}

func TestUserWritesRefuseThePgRolePrefix(t *testing.T) {
	inputs := map[string]handler{
		`{"name":"pg_monitor","password":"x","grants":[]}`:            userCreate,
		`{"name":"pg_read_all_data","password":"x"}`:                  userSetPassword,
		`{"name":"pg_signal_backend","grants":[],"replication":true}`: userGrant,
		`{"name":"pg_custom"}`:                                        userDrop,
	}
	for input, handler := range inputs {
		engine, psql := primaryWith(map[string]string{"pg_roles": "t\n"})

		_, err := handler(engine, context.Background(), []byte(input))

		if failureCode(t, err) != enums.ErrorCodeInvalidRequest || !strings.HasSuffix(err.Error(), " is reserved") || len(psql.writes()) != 0 {
			t.Fatalf("%s: err=%v writes=%d", input, err, len(psql.writes()))
		}
	}
}

func TestUserGrantMapsAMissingDatabaseConnectionToNotFound(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\n"})
	engine.psql = &failingOnSchema{inner: psql, err: &MissingDatabaseError{Message: `database "shop" does not exist`}}

	err := engine.Accounts().Grant(context.Background(), request[requests.UserGrant](t, []byte(`{"name":"alice","grants":[{"database":"shop","access":"write"}],"replication":false}`)))

	if failureCode(t, err) != enums.ErrorCodeNotFound {
		t.Fatalf("err = %v", err)
	}
}

type failingOnSchema struct {
	inner *recordingPsql
	err   error
}

func (f *failingOnSchema) Query(ctx context.Context, database string, script string, vars map[string]string) (string, error) {
	if strings.Contains(script, "ON SCHEMA") {
		return "", f.err
	}

	return f.inner.Query(ctx, database, script, vars)
}

func TestPasswordScriptsSilenceStatementLogging(t *testing.T) {
	engine, psql := primaryWith(map[string]string{"pg_roles": "t\n", "datistemplate": "shop\n"})

	if _, err := engine.Accounts().CreateUser(context.Background(), request[requests.UserCreate](t, []byte(`{"name":"alice","password":"x","grants":[{"database":"shop","access":"write"}]}`))); err != nil {
		t.Fatal(err)
	}
	if err := engine.Accounts().SetPassword(context.Background(), request[requests.UserSetPassword](t, []byte(`{"name":"alice","password":"y"}`))); err != nil {
		t.Fatal(err)
	}

	var checked int
	for _, c := range psql.calls {
		if !strings.Contains(c.script, "PASSWORD :'password'") {
			continue
		}
		checked++
		if !strings.HasPrefix(c.script, "SET log_min_duration_statement = -1; SET log_statement = 'none'; SET log_min_error_statement = panic;\n") {
			t.Fatalf("script does not silence logging first: %s", c.script)
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d password scripts, want 2", checked)
	}
}
