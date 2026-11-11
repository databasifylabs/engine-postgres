package postgres

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/requests"
)

func TestEnableStartsPatroniWithoutEtcdOnAReadOnlyNode(t *testing.T) {
	patroni := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"role":"replica","state":"streaming"}`))
	}))
	defer patroni.Close()
	root, calls := t.TempDir(), []string{}
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "patroni.yml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := Engine{root: root, supervisor: recordingSupervisor{&calls}, settings: Settings{PatroniURL: patroni.URL}}

	if err := engine.Cluster().Enable(t.Context(), requests.ClusterEnable{}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if slices.ContainsFunc(calls, func(call string) bool { return strings.HasSuffix(call, etcdUnit) }) || !slices.Contains(calls, "start "+patroniUnit) {
		t.Fatalf("calls = %v", calls)
	}
}
