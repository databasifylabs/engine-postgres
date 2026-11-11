package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDisableTurnsAClusteredNodeIntoAStandaloneServer(t *testing.T) {
	t.Setenv("DATABASIFY_LINE", "18")
	root, calls := t.TempDir(), []string{}
	clustered := configureRequest(true, false)
	clustered.Settings = nil
	request, err := json.Marshal(clustered)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"conf/request.json": string(request), "conf/patroni.yml": "", "data/PG_VERSION": "18"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	psql := &recordingPsql{replies: map[string]string{"pg_is_in_recovery": "f"}}
	engine := Engine{psql: psql, root: root, supervisor: recordingSupervisor{&calls}}

	if _, err := engine.Cluster().Disable(t.Context()); err != nil {
		t.Fatal(err)
	}

	manifest, err := os.ReadFile(filepath.Join(root, "conf", "processes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"name": "`+serverUnit+`"`) || strings.Contains(string(manifest), `"name": "`+patroniUnit+`"`) {
		t.Fatalf("manifest = %s", manifest)
	}
	if _, err := os.Stat(filepath.Join(root, "conf", "postgresql.conf")); err != nil {
		t.Fatal(err)
	}
	reload, enable := slices.Index(calls, "daemon-reload "), slices.Index(calls, "enable "+serverUnit)
	if reload < 0 || enable < reload {
		t.Fatalf("calls = %v", calls)
	}
}
