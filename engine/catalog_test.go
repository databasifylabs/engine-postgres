package postgres

import (
	"os"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
)

func TestTuningFollowsTheNodeSize(t *testing.T) {
	tuned := tunedFor(dtos.Member{Cpus: 4, MemoryMb: 8192, DiskGb: 80})

	want := map[string]string{"max_connections": "256", "shared_buffers": "2048MB", "effective_cache_size": "6144MB", "max_worker_processes": "8", "max_parallel_workers_per_gather": "2", "max_slot_wal_keep_size": "20480MB"}
	for key, value := range want {
		if tuned[key] != value {
			t.Errorf("%s = %s, want %s", key, tuned[key], value)
		}
	}
}

func TestOnlyPostgresLimitsAreEnforced(t *testing.T) {
	catalog := catalogFor("18")

	if err := catalog.Check(map[string]string{"max_files_per_process": "100000", "max_connections": "5000", "work_mem": "1GB"}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"max_connections": "262144", "wal_level": "minimal", "max_wal_senders": "5", "work_mem": "32kB"} {
		if catalog.Check(map[string]string{key: value}) == nil {
			t.Errorf("%s=%s was accepted", key, value)
		}
	}
}

func TestAutovacuumWorkersReloadFromPostgresEighteen(t *testing.T) {
	for line, restarts := range map[string]bool{"15": true, "16": true, "17": true, "18": false, "100": false} {
		for _, field := range catalogFor(line).Fields {
			if field.Key == "autovacuum_max_workers" && field.Restart != restarts {
				t.Errorf("line %s: restart = %v", line, field.Restart)
			}
		}
	}
}

func TestConfigureRefusesUnknownSettingsBeforeWriting(t *testing.T) {
	engine := Engine{root: t.TempDir()}
	request := configureRequest(false, false)
	request.Settings = map[string]string{"fsync": "off"}

	_, err := engine.withTunedSettings(request)

	if failureCode(t, err) != enums.ErrorCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(engine.path(requestState)); statErr == nil {
		t.Fatal("a refused request was saved")
	}
}
