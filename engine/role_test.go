package postgres

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

type fakePsql struct {
	reply string
	err   error
}

func (f fakePsql) Query(context.Context, string, string, map[string]string) (string, error) {
	return f.reply, f.err
}

type fakeUnits struct{ active bool }

func (f fakeUnits) IsActive(string) (bool, error) { return f.active, nil }

func patroniServer(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/patroni" {
			http.NotFound(w, r)

			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server.URL
}

func TestReplicaRejectsWrites(t *testing.T) {
	engine := Engine{psql: fakePsql{reply: "t\n"}, units: fakeUnits{}}

	err := engine.requirePrimary(context.Background())

	var failure command.Failure
	if !errors.As(err, &failure) || failure.Code != enums.ErrorCodeNotPrimary {
		t.Fatalf("err=%v", err)
	}
}

func TestPrimaryAcceptsWrites(t *testing.T) {
	engine := Engine{psql: fakePsql{reply: "f\n"}, units: fakeUnits{}}

	if err := engine.requirePrimary(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRequirePrimaryPropagatesUnavailable(t *testing.T) {
	engine := Engine{psql: fakePsql{err: command.Unavailable("down")}, units: fakeUnits{}}

	err := engine.requirePrimary(context.Background())

	var failure command.Failure
	if !errors.As(err, &failure) || failure.Code != enums.ErrorCodeUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestRoleFromPatroni(t *testing.T) {
	cases := map[string]enums.NodeRole{
		`{"role":"master"}`:         enums.NodeRolePrimary,
		`{"role":"primary"}`:        enums.NodeRolePrimary,
		`{"role":"replica"}`:        enums.NodeRoleReplica,
		`{"role":"standby_leader"}`: enums.NodeRoleReplica,
		`{"role":"uninitialized"}`:  enums.NodeRoleUnknown,
	}
	for body, want := range cases {
		engine := Engine{settings: Settings{PatroniURL: patroniServer(t, body)}, units: fakeUnits{active: true}}

		got, err := engine.role(context.Background())

		if err != nil || got != want {
			t.Errorf("%s: got=%q err=%v, want %q", body, got, err, want)
		}
	}
}

func TestUnreachablePatroniIsOffline(t *testing.T) {
	engine := Engine{settings: Settings{PatroniURL: "http://127.0.0.1:1"}, units: fakeUnits{active: true}}

	got, err := engine.role(context.Background())

	if err != nil || got != enums.NodeRoleOffline {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestStandaloneRoleUsesRecoveryState(t *testing.T) {
	cases := map[string]enums.NodeRole{"t\n": enums.NodeRoleReplica, "f\n": enums.NodeRolePrimary}
	for reply, want := range cases {
		engine := Engine{psql: fakePsql{reply: reply}, units: fakeUnits{}}

		got, err := engine.role(context.Background())

		if err != nil || got != want {
			t.Errorf("%q: got=%q err=%v", reply, got, err)
		}
	}
}

func TestStandaloneUnavailableIsOffline(t *testing.T) {
	engine := Engine{psql: fakePsql{err: command.Unavailable("down")}, units: fakeUnits{}}

	got, err := engine.role(context.Background())

	if err != nil || got != enums.NodeRoleOffline {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestStandaloneOtherErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	engine := Engine{psql: fakePsql{err: boom}, units: fakeUnits{}}

	if _, err := engine.role(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

func TestEnableSettlesAsLeaderOrStreamingReplica(t *testing.T) {
	cases := map[patroniStatus]bool{
		{Role: "primary", State: "running"}:   true,
		{Role: "replica", State: "streaming"}: true,
		{Role: "replica", State: "running"}:   true,
		{Role: "replica", State: "starting"}:  false,
		{Role: "", State: ""}:                 false,
	}
	for state, want := range cases {
		if settled(state) != want {
			t.Errorf("%+v: %v", state, !want)
		}
	}
}

func TestBootstrapOnAReplicaDoesNothing(t *testing.T) {
	psql := &recordingPsql{replies: map[string]string{"pg_is_in_recovery": "t"}}
	engine := Engine{psql: psql}

	err := engine.Nodes().Bootstrap(context.Background(), requests.NodeBootstrap{Secrets: dtos.Secrets{Admin: secret("a"), Replication: secret("r"), Agent: secret("g")}})

	if err != nil || len(psql.calls) != 1 {
		t.Fatalf("err = %v, queries = %v", err, psql.calls)
	}
}

func TestBootstrapCreatesTheDefaultAccount(t *testing.T) {
	psql := &recordingPsql{replies: map[string]string{"pg_is_in_recovery": "f", "WHERE NOT datistemplate": "defaultdb"}}
	engine := Engine{psql: psql}

	err := engine.Nodes().Bootstrap(context.Background(), requests.NodeBootstrap{
		Secrets: dtos.Secrets{Admin: secret("a"), Replication: secret("r"), Agent: secret("g")},
		Account: &dtos.DefaultAccount{Database: "defaultdb", User: "databasify", Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	database, user := -1, -1
	for index, c := range psql.calls {
		switch {
		case c.script == createDatabaseScript && c.vars["name"] == "defaultdb":
			database = index
		case c.script == createUserScript && c.vars["name"] == "databasify" && c.vars["password"] == "pw":
			user = index
		}
	}
	if database < 0 || user < database {
		t.Fatalf("queries = %v", psql.scripts())
	}
}
