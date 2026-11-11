package postgres

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRestartRestartsPostgresOnASingleNode(t *testing.T) {
	root, calls := t.TempDir(), []string{}
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "postgresql.conf"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := Engine{supervisor: recordingSupervisor{&calls}, root: root}

	if err := engine.Nodes().Restart(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(calls, []string{"restart " + serverUnit}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRestartRestartsPatroniInACluster(t *testing.T) {
	root, calls := t.TempDir(), []string{}
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "patroni.yml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := Engine{supervisor: recordingSupervisor{&calls}, root: root}

	if err := engine.Nodes().Restart(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(calls, []string{"restart " + patroniUnit}) {
		t.Fatalf("calls = %v", calls)
	}
}
