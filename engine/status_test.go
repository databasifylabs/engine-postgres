package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/responses"
)

type scriptPsql struct {
	replies map[string]string
	err     error
}

func (s scriptPsql) Query(_ context.Context, _ string, script string, _ map[string]string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	for fragment, reply := range s.replies {
		if strings.Contains(script, fragment) {
			return reply, nil
		}
	}

	return "", errors.New("unexpected script: " + script)
}

func statusOf(t *testing.T, engine Engine) responses.NodeStatus {
	t.Helper()
	reply, err := engine.Nodes().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestStatusReportsPrimaryAndVersion(t *testing.T) {
	psql := scriptPsql{replies: map[string]string{"pg_is_in_recovery": "f\n", "server_version": "18.6 (Debian 18.6-1.pgdg13+1)\n"}}

	status := statusOf(t, Engine{psql: psql, units: fakeUnits{}})

	if status.Role != enums.NodeRolePrimary {
		t.Fatalf("role = %q", status.Role)
	}
	if status.Version == nil || *status.Version != "18.6" {
		t.Fatalf("version = %v", status.Version)
	}
}

func TestStatusIsOfflineWhenUnreachable(t *testing.T) {
	psql := scriptPsql{err: command.Unavailable("connection refused")}

	status := statusOf(t, Engine{psql: psql, units: fakeUnits{}})

	if status.Role != enums.NodeRoleOffline || status.Version != nil {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusIsOfflineWhenOnlyTheVersionQueryIsUnavailable(t *testing.T) {
	url := patroniServer(t, `{"role":"master"}`)
	psql := scriptPsql{err: command.Unavailable("connection refused")}

	status := statusOf(t, Engine{psql: psql, units: fakeUnits{active: true}, settings: Settings{PatroniURL: url}})

	if status.Role != enums.NodeRoleOffline || status.Version != nil {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusFailsInternallyOnOtherErrors(t *testing.T) {
	engine := Engine{psql: scriptPsql{err: errors.New("boom")}, units: fakeUnits{}}

	_, err := engine.Nodes().Status(context.Background())

	if err == nil || isUnavailable(err) {
		t.Fatalf("err = %v", err)
	}
}
