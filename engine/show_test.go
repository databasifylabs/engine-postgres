package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShowReportsEverySettingWaitingOnARestart(t *testing.T) {
	t.Setenv("DATABASIFY_LINE", "18")
	root := t.TempDir()
	request, err := json.Marshal(configureRequest(false, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "request.json"), request, 0o600); err != nil {
		t.Fatal(err)
	}
	psql := &recordingPsql{replies: map[string]string{"pending_restart": "archive_mode|on|t"}}
	engine := Engine{psql: psql, root: root}

	shown, err := engine.Config().Show(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(psql.scripts()[0], "OR s.pending_restart") || len(shown.Values) != 1 || !shown.Values[0].PendingRestart {
		t.Fatalf("query = %s, values = %+v", psql.scripts()[0], shown.Values)
	}
}
