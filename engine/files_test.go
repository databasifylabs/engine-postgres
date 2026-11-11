package postgres

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

func configureRequest(clustered, readOnly bool) requests.NodeConfigure {
	return requests.NodeConfigure{
		Clustered: clustered, ReadOnly: readOnly, Ip: "10.0.0.2", DatabaseId: "db1",
		Members:  []dtos.Member{{Ip: "10.0.0.1", Position: 1, Voting: true}, {Ip: "10.0.0.2", Position: 2, Voting: true}, {Ip: "10.0.0.3", Position: 3, Voting: true}},
		Settings: map[string]string{"max_connections": "300", "work_mem": "8MB", "search_path": "it's"},
		Secrets:  dtos.Secrets{Admin: secret("p'w"), Replication: secret("r"), Agent: secret("a")},
	}
}

func secret(value string) *string {
	return &value
}

func renderedFiles(t *testing.T, request requests.NodeConfigure) map[string]string {
	t.Helper()
	files, err := filesOf(request, "18")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, file := range files {
		byName[file.path[len(confDir)+1:]] = file.content
	}

	return byName
}

func TestStandaloneNodeGetsServerAndHbaFiles(t *testing.T) {
	files := renderedFiles(t, configureRequest(false, false))
	for _, name := range []string{"postgresql.conf", "pg_hba.conf", "pgbouncer.ini", "pgbouncer-users.txt"} {
		if _, found := files[name]; !found {
			t.Fatalf("missing %s in %v", name, files)
		}
	}
	conf := files["postgresql.conf"]
	for _, line := range []string{"port = '25060'", "max_connections = '300'", "work_mem = '8MB'", "search_path = 'it''s'", "wal_level = 'replica'", "ssl = 'on'"} {
		if !strings.Contains(conf, line+"\n") {
			t.Fatalf("postgresql.conf lacks %q:\n%s", line, conf)
		}
	}
	if strings.Contains(conf, "archive_mode") {
		t.Fatal("archiving is on without an archive")
	}
}

func TestClusteredMemberSplitsClusterWideAndLocalSettings(t *testing.T) {
	files := renderedFiles(t, configureRequest(true, false))
	patroni := files["patroni.yml"]
	if !strings.Contains(patroni, "      parameters:\n") || !strings.Contains(patroni, "        max_connections: '300'\n") {
		t.Fatalf("cluster-wide settings missing:\n%s", patroni)
	}
	if !strings.Contains(patroni, "    work_mem: '8MB'\n") || !strings.Contains(patroni, "password: 'p''w'") {
		t.Fatalf("local settings or quoting wrong:\n%s", patroni)
	}
	if !strings.Contains(patroni, "hosts: '10.0.0.1:2379,10.0.0.2:2379,10.0.0.3:2379'") || !strings.Contains(patroni, "nofailover: false") {
		t.Fatalf("hosts or tags wrong:\n%s", patroni)
	}
	etcd := files["etcd.env"]
	if !strings.Contains(etcd, "ETCD_INITIAL_CLUSTER=10.0.0.1=http://10.0.0.1:2380,10.0.0.2=http://10.0.0.2:2380\n") || !strings.Contains(etcd, "ETCD_INITIAL_CLUSTER_STATE=existing\n") {
		t.Fatalf("etcd.env wrong:\n%s", etcd)
	}
	if _, found := files["postgresql.conf"]; found {
		t.Fatal("clustered node got postgresql.conf")
	}
}

func TestReadOnlyMemberHasNoEtcdAndIsTagged(t *testing.T) {
	files := renderedFiles(t, configureRequest(true, true))
	if _, found := files["etcd.env"]; found {
		t.Fatal("read-only node got etcd.env")
	}
	if !strings.Contains(files["patroni.yml"], "nofailover: true") {
		t.Fatal("read-only node not tagged")
	}
}

func TestArchiveTurnsOnArchivingAndTheBackupSchedule(t *testing.T) {
	request := configureRequest(false, false)
	request.Archive = &dtos.ArchiveSettings{Storage: dtos.Storage{Endpoint: "https://s3.example.com", Bucket: "b", Region: "r", AccessKey: "k", SecretKey: "s", VerifyTls: true, Path: "db1"}, RetentionDays: 7, BackupHour: 3}
	files := renderedFiles(t, request)
	if !strings.Contains(files["postgresql.conf"], "archive_mode = 'on'") || !strings.Contains(files["pgbackrest.conf"], "repo1-s3-key=k\n") || !strings.Contains(files["pgbackrest.conf"], "repo1-retention-full=7\n") {
		t.Fatalf("archive files wrong:\n%s\n%s", files["postgresql.conf"], files["pgbackrest.conf"])
	}
	raw, err := manifestOf(request, "18")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	_ = json.Unmarshal(raw, &m)
	if len(m.Schedules) != 1 || m.Schedules[0].OnCalendar != "03:00" || m.Schedules[0].Argv[1] != "engine-task" {
		t.Fatalf("schedules = %+v", m.Schedules)
	}
}

func TestManifestUnitsFollowTheNodeShape(t *testing.T) {
	cases := map[string]struct {
		clustered, readOnly bool
		units               []string
	}{
		"standalone": {false, false, []string{locksUnit, serverUnit, poolerUnit}},
		"member":     {true, false, []string{locksUnit, etcdUnit, patroniUnit, poolerUnit}},
		"read-only":  {true, true, []string{locksUnit, patroniUnit, poolerUnit}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			names := []string{}
			for _, p := range processesOf(configureRequest(c.clustered, c.readOnly), "18") {
				names = append(names, p.Name)
			}
			if strings.Join(names, ",") != strings.Join(c.units, ",") {
				t.Fatalf("units = %v", names)
			}
		})
	}
}

func TestStorageBecomesPgBackRestOptions(t *testing.T) {
	storage := dtos.Storage{Endpoint: "https://10.0.0.9:9000", Bucket: "backups", Region: "us-east-1", AccessKey: "k", SecretKey: "s", PathStyle: true, Path: "/db1/"}
	got := map[string]string{}
	for _, option := range repositoryOf(storage) {
		got[option.Key] = option.Value
	}
	want := map[string]string{
		"repo1-type": "s3", "repo1-storage-port": "9000", "repo1-s3-endpoint": "10.0.0.9", "repo1-s3-bucket": "backups", "repo1-s3-region": "us-east-1",
		"repo1-s3-key": "k", "repo1-s3-key-secret": "s", "repo1-s3-uri-style": "path", "repo1-path": "/db1", "repo1-storage-verify-tls": "n",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("options = %v", got)
	}
}

func TestConfigureRefusesMissingSecrets(t *testing.T) {
	request := configureRequest(false, false)
	request.Secrets.Agent = nil
	_, err := filesOf(request, "18")
	if failureCode(t, err) != enums.ErrorCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestPgbouncerIncludesItsPoolsAndSetsItsOwnLimits(t *testing.T) {
	files := renderedFiles(t, configureRequest(false, false))

	if !strings.Contains(files["pgbouncer.ini"], "%include /srv/postgres/conf/pgbouncer-pools.ini\n") || !strings.Contains(files["pgbouncer.ini"], "max_client_conn = 5000\n") {
		t.Fatalf("pgbouncer.ini:\n%s", files["pgbouncer.ini"])
	}
	pools := poolsIniOf([]dtos.Pool{{Name: "app_pool", Database: "app", Size: 20, Mode: "transaction"}})
	if pools != "app_pool = host=127.0.0.1 port=25060 dbname=app pool_size=20 pool_mode=transaction\n" {
		t.Fatalf("pools = %q", pools)
	}
	if clients, size := poolLimitsOf(map[string]string{"max_connections": "800"}, 64); clients != 8000 || size != 100 {
		t.Fatalf("limits = %d, %d", clients, size)
	}
	if clients, size := poolLimitsOf(map[string]string{}, 2); clients != 5000 || size != 10 {
		t.Fatalf("limits = %d, %d", clients, size)
	}
}

func TestPoolerPasswordComesFromTheAgentSecret(t *testing.T) {
	first, second := poolerPasswordOf("agent-secret"), poolerPasswordOf("agent-secret")

	if first != second || first == "agent-secret" || len(first) != 64 || poolerPasswordOf("other") == first {
		t.Fatalf("pooler password = %s", first)
	}
}

func TestEtcdAcceptsSeveralJoinersAtOnce(t *testing.T) {
	files := renderedFiles(t, configureRequest(true, false))

	if !strings.Contains(files["etcd.env"], "ETCD_MAX_LEARNERS=7\n") {
		t.Fatalf("etcd.env:\n%s", files["etcd.env"])
	}
}

func TestReplicationSlotsGrowWithReadOnlyNodes(t *testing.T) {
	request := configureRequest(true, false)
	for index := range 20 {
		request.Members = append(request.Members, dtos.Member{Ip: "10.0.1." + strconv.Itoa(index), Position: int64(10 + index)})
	}
	files := renderedFiles(t, request)
	small := renderedFiles(t, configureRequest(true, false))

	if !strings.Contains(files["dcs.yml"], "max_wal_senders: '50'") || !strings.Contains(files["dcs.yml"], "max_replication_slots: '50'") {
		t.Fatalf("dcs.yml:\n%s", files["dcs.yml"])
	}
	if !strings.Contains(small["dcs.yml"], "max_wal_senders: '20'") {
		t.Fatalf("a small cluster keeps 20 slots:\n%s", small["dcs.yml"])
	}
}
