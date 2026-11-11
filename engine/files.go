package postgres

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/requests"
)

const (
	confDir          = defaultRoot + "/conf"
	archiveConf      = confDir + "/pgbackrest.conf"
	restoreConf      = confDir + "/pgbackrest-restore.conf"
	stanza           = "main"
	clientPortNumber = "25060"
	poolPortNumber   = "25061"
)

var (
	clusterWideKeys = []string{"max_connections", "max_locks_per_transaction", "max_worker_processes", "max_prepared_transactions", "max_wal_senders", "max_replication_slots", "track_commit_timestamp", "wal_level"}

	defaultMaxConnections int64 = 100
	minReplicationSlots   int64 = 20
	backupSlots           int64 = 4
	minClientConnections  int64 = 5000

	hbaRules = []string{
		"local all postgres peer",
		"local all all scram-sha-256",
		"host all all 127.0.0.1/32 scram-sha-256",
		"host all all ::1/128 scram-sha-256",
		"host replication replicator 127.0.0.1/32 scram-sha-256",
		"host replication replicator ::1/128 scram-sha-256",
		"hostssl replication replicator 0.0.0.0/0 scram-sha-256",
		"hostssl all all 0.0.0.0/0 scram-sha-256",
	}

	replicationParameters = map[string]string{
		"wal_level": "replica", "hot_standby": "on", "wal_log_hints": "on", "max_wal_senders": "20",
		"max_replication_slots": "20", "wal_keep_size": "1GB", "password_encryption": "scram-sha-256",
		"shared_preload_libraries": "pg_stat_statements", "pg_stat_statements.track": "top", "compute_query_id": "auto",
	}

	localParameters = map[string]string{
		"ssl": "on", "ssl_cert_file": defaultRoot + "/tls/server.crt", "ssl_key_file": defaultRoot + "/tls/server.key",
		"ssl_min_protocol_version": "TLSv1.2", "unix_socket_directories": defaultRoot + "/run",
		"log_destination": "stderr", "logging_collector": "off",
	}

	archiveParameters = map[string]string{
		"archive_mode": "on", "archive_command": "pgbackrest --config=" + archiveConf + " --stanza=" + stanza + " archive-push %p", "archive_timeout": "60",
	}
)

type option struct {
	Key   string
	Value string
}

type nodeFile struct {
	path    string
	content string
	mode    os.FileMode
}

type fileView struct {
	requests.NodeConfigure
	Line                 string
	ClusterWide          []option
	Local                []option
	Rules                []string
	EtcdHosts            string
	InitialCluster       string
	EtcdState            string
	Repository           []option
	Passwords            passwords
	ClientPort           string
	PoolPort             string
	MaxClientConnections int64
	DefaultPoolSize      int64
}

var templates = template.Must(template.New("files").Funcs(template.FuncMap{"quoted": quoted}).Parse(`
{{define "postgresql.conf"}}listen_addresses = '*'
port = '{{.ClientPort}}'
hba_file = '/srv/postgres/conf/pg_hba.conf'
{{range .ClusterWide}}{{.Key}} = {{quoted .Value}}
{{end}}{{range .Local}}{{.Key}} = {{quoted .Value}}
{{end}}{{end}}
{{define "pg_hba.conf"}}{{range .Rules}}{{.}}
{{end}}{{end}}
{{define "patroni.yml"}}scope: 'main'
name: '{{.Ip}}'
restapi:
  listen: '0.0.0.0:8008'
  connect_address: '{{.Ip}}:8008'
  authentication:
    username: patroni
    password: {{quoted .Passwords.Agent}}
etcd3:
  hosts: '{{.EtcdHosts}}'
bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
    failsafe_mode: true
    postgresql:
      use_pg_rewind: true
      use_slots: true
      parameters:
{{range .ClusterWide}}        {{.Key}}: {{quoted .Value}}
{{end}}  initdb:
    - encoding: UTF8
    - data-checksums
postgresql:
  listen: '*:{{.ClientPort}}'
  connect_address: '{{.Ip}}:{{.ClientPort}}'
  data_dir: '/srv/postgres/data'
  bin_dir: '/usr/lib/postgresql/{{.Line}}/bin'
  pgpass: '/srv/postgres/conf/pgpass'
  use_unix_socket: true
  use_unix_socket_repl: false
  remove_data_directory_on_rewind_failure: true
  remove_data_directory_on_diverged_timelines: true
  authentication:
    superuser:
      username: postgres
      password: {{quoted .Passwords.Admin}}
    replication:
      username: replicator
      password: {{quoted .Passwords.Replication}}
    rewind:
      username: postgres
      password: {{quoted .Passwords.Admin}}
  parameters:
{{range .Local}}    {{.Key}}: {{quoted .Value}}
{{end}}  pg_hba:
{{range .Rules}}    - '{{.}}'
{{end}}tags:
  nofailover: {{.ReadOnly}}
  noloadbalance: {{.ReadOnly}}
  nosync: {{.ReadOnly}}
  clonefrom: {{.ReadOnly}}
{{end}}
{{define "etcd.env"}}ETCD_NAME={{.Ip}}
ETCD_DATA_DIR=/srv/postgres/etcd
ETCD_LISTEN_CLIENT_URLS=http://{{.Ip}}:2379,http://127.0.0.1:2379
ETCD_ADVERTISE_CLIENT_URLS=http://{{.Ip}}:2379
ETCD_LISTEN_PEER_URLS=http://{{.Ip}}:2380
ETCD_INITIAL_ADVERTISE_PEER_URLS=http://{{.Ip}}:2380
ETCD_INITIAL_CLUSTER={{.InitialCluster}}
ETCD_INITIAL_CLUSTER_STATE={{.EtcdState}}
ETCD_INITIAL_CLUSTER_TOKEN=databasify-{{.DatabaseId}}
ETCD_MAX_LEARNERS=7
ETCD_AUTO_COMPACTION_MODE=periodic
ETCD_AUTO_COMPACTION_RETENTION=1h
ETCD_QUOTA_BACKEND_BYTES=2147483648
{{end}}
{{define "dcs.yml"}}ttl: 30
loop_wait: 10
retry_timeout: 10
maximum_lag_on_failover: 1048576
failsafe_mode: true
postgresql:
  use_pg_rewind: true
  use_slots: true
  parameters:
{{range .ClusterWide}}    {{.Key}}: {{quoted .Value}}
{{end}}{{end}}
{{define "pgbouncer.ini"}}[databases]
postgres = host=127.0.0.1 port={{.ClientPort}} dbname=postgres pool_size=2
%include /srv/postgres/conf/pgbouncer-pools.ini

[pgbouncer]
listen_addr = *
listen_port = {{.PoolPort}}
unix_socket_dir =
auth_type = scram-sha-256
auth_file = /srv/postgres/conf/pgbouncer-users.txt
auth_user = pgbouncer
auth_dbname = postgres
auth_query = SELECT username, password FROM pgbouncer.get_auth($1)
client_tls_sslmode = require
client_tls_cert_file = /srv/postgres/tls/server.crt
client_tls_key_file = /srv/postgres/tls/server.key
server_tls_sslmode = require
max_client_conn = {{.MaxClientConnections}}
default_pool_size = {{.DefaultPoolSize}}
ignore_startup_parameters = extra_float_digits,options
{{end}}
{{define "pgbouncer-users.txt"}}"pgbouncer" "{{.Passwords.Pooler}}"
{{end}}
{{define "pgbackrest.conf"}}[global]
{{range .Repository}}{{.Key}}={{.Value}}
{{end}}{{if .Archive}}repo1-retention-full-type=time
repo1-retention-full={{.Archive.RetentionDays}}
{{end}}repo1-bundle=y
compress-type=zst
process-max=2
start-fast=y
archive-async=y
archive-push-queue-max=4GiB
spool-path=/srv/postgres/archive/spool
lock-path=/srv/postgres/archive/lock
log-path=/srv/postgres/archive/log
log-level-console=warn
log-level-file=info

[main]
pg1-path=/srv/postgres/data
pg1-port={{.ClientPort}}
pg1-socket-path=/srv/postgres/run
{{end}}`))

func quoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func viewOf(request requests.NodeConfigure, line string) (fileView, error) {
	passwords, err := passwordsOf(request.Secrets)
	if err != nil {
		return fileView{}, err
	}
	clusterWide, local := parametersOf(request)
	clients, size := poolLimitsOf(request.Settings, runtime.NumCPU())
	view := fileView{
		NodeConfigure: request, Line: line, ClusterWide: clusterWide, Local: local, Rules: hbaRules, Passwords: passwords,
		EtcdHosts: etcdHostsOf(request.Members), ClientPort: clientPortNumber, PoolPort: poolPortNumber,
		MaxClientConnections: clients, DefaultPoolSize: size,
	}
	view.InitialCluster, view.EtcdState = initialClusterOf(request)
	if request.Archive != nil {
		view.Repository = repositoryOf(request.Archive.Storage)
	}

	return view, nil
}

type passwords struct {
	Admin       string
	Replication string
	Agent       string
	Pooler      string
}

func passwordsOf(secrets dtos.Secrets) (passwords, error) {
	if slices.Contains([]*string{secrets.Admin, secrets.Replication, secrets.Agent}, nil) {
		return passwords{}, command.Invalid("PostgreSQL needs the admin, replication and agent secrets")
	}

	return passwords{Admin: *secrets.Admin, Replication: *secrets.Replication, Agent: *secrets.Agent, Pooler: poolerPasswordOf(*secrets.Agent)}, nil
}

func poolerPasswordOf(agent string) string {
	mac := hmac.New(sha256.New, []byte(agent))
	mac.Write([]byte("pgbouncer"))

	return hex.EncodeToString(mac.Sum(nil))
}

func poolLimitsOf(settings map[string]string, cpus int) (int64, int64) {
	connections, err := strconv.ParseInt(settings["max_connections"], 10, 64)
	if err != nil {
		connections = defaultMaxConnections
	}

	return max(minClientConnections, connections*10), int64(min(max(cpus*2, 10), 100))
}

func repositoryOf(storage dtos.Storage) []option {
	host := strings.TrimPrefix(storage.Endpoint, "https://")
	options := []option{{Key: "repo1-type", Value: "s3"}}
	if name, port, err := net.SplitHostPort(host); err == nil {
		host = name
		options = append(options, option{Key: "repo1-storage-port", Value: port})
	}
	uriStyle := "host"
	if storage.PathStyle {
		uriStyle = "path"
	}
	options = append(options,
		option{Key: "repo1-s3-endpoint", Value: host},
		option{Key: "repo1-s3-bucket", Value: storage.Bucket},
		option{Key: "repo1-s3-region", Value: storage.Region},
		option{Key: "repo1-s3-key", Value: storage.AccessKey},
		option{Key: "repo1-s3-key-secret", Value: storage.SecretKey},
		option{Key: "repo1-s3-uri-style", Value: uriStyle},
		option{Key: "repo1-path", Value: "/" + strings.Trim(storage.Path, "/")},
	)
	if !storage.VerifyTls {
		options = append(options, option{Key: "repo1-storage-verify-tls", Value: "n"})
	}

	return options
}

func slotsFor(members int) int64 {
	return max(minReplicationSlots, int64(members)*2+backupSlots)
}

func parametersOf(request requests.NodeConfigure) ([]option, []option) {
	clusterWide := maps.Clone(replicationParameters)
	slots := strconv.FormatInt(slotsFor(len(request.Members)), 10)
	clusterWide["max_wal_senders"], clusterWide["max_replication_slots"] = slots, slots
	local := maps.Clone(localParameters)
	if request.Archive != nil {
		maps.Copy(local, archiveParameters)
	}
	for key, value := range request.Settings {
		if slices.Contains(clusterWideKeys, key) {
			clusterWide[key] = value

			continue
		}
		local[key] = value
	}

	return optionsOf(clusterWide), optionsOf(local)
}

func optionsOf(values map[string]string) []option {
	options := make([]option, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		options = append(options, option{Key: key, Value: values[key]})
	}

	return options
}

func etcdHostsOf(members []dtos.Member) string {
	hosts := make([]string, 0, len(members))
	for _, member := range members {
		hosts = append(hosts, member.Ip+":"+etcdClientPort)
	}

	return strings.Join(hosts, ",")
}

func initialClusterOf(request requests.NodeConfigure) (string, string) {
	own := positionOf(request.Members, request.Ip)
	peers := make([]string, 0)
	for _, member := range request.Members {
		if member.Voting && member.Position <= own {
			peers = append(peers, member.Ip+"=http://"+member.Ip+":"+etcdPeerPort)
		}
	}
	if len(peers) == 1 {
		return strings.Join(peers, ","), "new"
	}

	return strings.Join(peers, ","), "existing"
}

func positionOf(members []dtos.Member, ip string) int64 {
	for _, member := range members {
		if member.Ip == ip {
			return member.Position
		}
	}

	return 0
}

func filesOf(request requests.NodeConfigure, line string) ([]nodeFile, error) {
	view, err := viewOf(request, line)
	if err != nil {
		return nil, err
	}
	names := map[string]os.FileMode{"pgbouncer.ini": 0o640, "pgbouncer-users.txt": 0o600}
	switch {
	case !request.Clustered:
		names["postgresql.conf"], names["pg_hba.conf"] = 0o640, 0o640
	case request.ReadOnly:
		names["patroni.yml"] = 0o600
	default:
		names["patroni.yml"], names["etcd.env"], names["dcs.yml"] = 0o600, 0o640, 0o640
	}
	if request.Archive != nil {
		names["pgbackrest.conf"] = 0o600
	}
	files := make([]nodeFile, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		var content strings.Builder
		if err := templates.ExecuteTemplate(&content, name, view); err != nil {
			return nil, err
		}
		files = append(files, nodeFile{path: confDir + "/" + name, content: content.String(), mode: names[name]})
	}

	return files, nil
}
