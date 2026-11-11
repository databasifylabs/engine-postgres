package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type loadTimePsql struct{ reads int }

func (p *loadTimePsql) Query(_ context.Context, _ string, _ string, _ map[string]string) (string, error) {
	p.reads++

	return strconv.Itoa(min(p.reads, 3)), nil
}

func TestApplyConfigWaitsForPatroniToReloadPostgres(t *testing.T) {
	root, calls := t.TempDir(), []string{}
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "patroni.yml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	psql := &loadTimePsql{}
	engine := Engine{psql: psql, root: root, supervisor: recordingSupervisor{&calls}}

	if err := engine.Nodes().ApplyConfig(t.Context()); err != nil {
		t.Fatal(err)
	}

	if psql.reads < 2 || calls[0] != "reload "+patroniUnit {
		t.Fatalf("reads = %d, calls = %v", psql.reads, calls)
	}
}
