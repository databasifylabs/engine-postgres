package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/testtls"
)

type stoppedSupervisor struct{ recordingSupervisor }

func (stoppedSupervisor) Active(string) bool { return false }

func TestInstallTlsWhileStoppedOnlyWritesFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tls"), 0o750); err != nil {
		t.Fatal(err)
	}
	certificate, key, err := testtls.Pair()
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	engine := Engine{psql: failingPsql{err: errors.New("PostgreSQL is not running")}, supervisor: stoppedSupervisor{recordingSupervisor{&calls}}, root: root}

	if err := engine.Nodes().InstallTLS(t.Context(), requests.TlsInstall{Certificate: certificate, Key: key, Ca: certificate}); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(filepath.Join(root, "tls", "server.key")); string(data) != key || len(calls) != 0 {
		t.Fatalf("key written = %v, supervisor calls = %v", string(data) == key, calls)
	}
}
