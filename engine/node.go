package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/nodefs"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

var standbyLimits = []string{"max_connections", "max_locks_per_transaction", "max_prepared_transactions", "max_wal_senders", "max_worker_processes"}

func (e Engine) manifestUnits() ([]string, error) {
	raw, err := os.ReadFile(e.path(manifestPath))
	if err != nil {
		return nil, command.Unavailable("this node has no configuration yet; run node configure first")
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m.Processes))
	for _, p := range m.Processes {
		names = append(names, p.Name)
	}

	return names, nil
}

func (e Engine) clustered() bool {
	return e.exists("conf/patroni.yml") && !e.exists("conf/postgresql.conf")
}

func (e nodes) Start(ctx context.Context) error {
	names, err := e.manifestUnits()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := e.supervisor.Do("enable", name); err != nil {
			return err
		}
		if err := e.supervisor.Do("restart", name); err != nil {
			return err
		}
	}

	return nil
}

func (e nodes) Wake(ctx context.Context, request requests.NodeWake) error {
	if e.clustered() && !request.All {
		return e.supervisor.Do("start", etcdUnit)
	}
	names, err := e.manifestUnits()
	if err != nil {
		return err
	}
	for _, name := range names {
		_ = e.supervisor.Do("start", name)
	}

	return nil
}

func (e nodes) ApplyConfig(ctx context.Context) error {
	if err := e.reloadServer(ctx); err != nil {
		return err
	}

	return e.supervisor.Do("reload", poolerUnit)
}

func (e Engine) reloadServer(ctx context.Context) error {
	if e.clustered() {
		return e.reloadThroughPatroni(ctx)
	}
	_, err := e.psql.Query(ctx, defaultDatabase, "SELECT pg_reload_conf()", nil)

	return err
}

const (
	confLoadTimeQuery = "SELECT pg_conf_load_time()"
	patroniReloadWait = 30 * time.Second
)

func (e Engine) reloadThroughPatroni(ctx context.Context) error {
	loaded := func() string {
		reply, _ := e.psql.Query(ctx, defaultDatabase, confLoadTimeQuery, nil)

		return reply
	}
	before := loaded()
	if err := e.supervisor.Do("reload", patroniUnit); err != nil {
		return err
	}
	waitFor(ctx, patroniReloadWait, time.Second, func() bool { return loaded() != before })

	return nil
}

func (e nodes) InstallTLS(ctx context.Context, request requests.TlsInstall) error {
	return nodefs.Root(e.root).InstallTLS(ctx, request, false, e.ReloadTLS)
}

func (e nodes) Restart(context.Context) error {
	if e.clustered() {
		return e.supervisor.Do("restart", patroniUnit)
	}

	return e.supervisor.Do("restart", serverUnit)
}

func (e nodes) ReloadTLS(ctx context.Context) error {
	if !e.supervisor.Active(serverUnit) && !e.supervisor.Active(patroniUnit) {
		return nil
	}
	if _, err := e.psql.Query(ctx, defaultDatabase, "SELECT pg_reload_conf()", nil); err != nil {
		return err
	}

	return e.supervisor.Do("reload", poolerUnit)
}

const metricsScript = `SELECT (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'),
  (SELECT COALESCE(sum(xact_commit + xact_rollback), 0) FROM pg_stat_database),
  (SELECT round(COALESCE(sum(pg_database_size(datname)), 0) / 1048576) FROM pg_database)`

func (e nodes) Metrics(ctx context.Context) (responses.NodeMetrics, error) {
	row, err := e.psql.Query(ctx, defaultDatabase, metricsScript, nil)
	if err != nil {
		return responses.NodeMetrics{}, err
	}
	fields := strings.Split(strings.TrimSpace(row), "|")
	if len(fields) != 3 {
		return responses.NodeMetrics{}, command.Unavailable("unexpected metrics row: " + row)
	}

	return responses.NodeMetrics{Connections: number(fields[0]), Queries: number(fields[1]), DataMb: number(fields[2])}, nil
}

func number(text string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(text), 10, 64)

	return value
}

const bootstrapScript = `ALTER ROLE postgres PASSWORD :'postgres_password';
SELECT 'CREATE ROLE replicator WITH REPLICATION LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'replicator') \gexec
ALTER ROLE replicator WITH REPLICATION LOGIN PASSWORD :'replication_password';
SELECT 'CREATE ROLE pgbouncer LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'pgbouncer') \gexec
ALTER ROLE pgbouncer LOGIN PASSWORD :'pgbouncer_password';
CREATE SCHEMA IF NOT EXISTS pgbouncer AUTHORIZATION postgres;
CREATE OR REPLACE FUNCTION pgbouncer.get_auth(p_usename text)
    RETURNS TABLE (username text, password text)
    LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog
    AS 'SELECT usename::text, passwd::text FROM pg_catalog.pg_shadow WHERE usename = p_usename';
REVOKE ALL ON FUNCTION pgbouncer.get_auth(text) FROM PUBLIC;
GRANT USAGE ON SCHEMA pgbouncer TO pgbouncer;
GRANT EXECUTE ON FUNCTION pgbouncer.get_auth(text) TO pgbouncer;`

func (e nodes) Bootstrap(ctx context.Context, request requests.NodeBootstrap) error {
	replica, err := e.inRecovery(ctx)
	if err != nil || replica {
		return err
	}
	passwords, err := passwordsOf(request.Secrets)
	if err != nil {
		return err
	}
	vars := map[string]string{
		"postgres_password":    passwords.Admin,
		"replication_password": passwords.Replication,
		"pgbouncer_password":   passwords.Pooler,
	}
	if _, err := e.psql.Query(ctx, defaultDatabase, keepStatementsOutOfLogs+bootstrapScript, vars); err != nil {
		return err
	}

	return contract.CreateDefaultAccount(ctx, e.Accounts(), request.Account)
}

func (e nodes) SyncConfig(ctx context.Context) (responses.ConfigSync, error) {
	if !e.clustered() {
		return responses.ConfigSync{}, nil
	}
	lowers, err := e.lowersStandbyLimits(ctx)
	if err != nil {
		return responses.ConfigSync{}, err
	}
	if _, err := e.patronictl(ctx, "edit-config", "--force", "--replace", confDir+"/dcs.yml"); err != nil {
		return responses.ConfigSync{}, err
	}
	if !lowers || !waitFor(ctx, time.Minute, 2*time.Second, func() bool { return e.patroniState(ctx).PendingRestart }) {
		return responses.ConfigSync{}, nil
	}
	if _, err := e.patronictl(ctx, "restart", patroniScope, "--role", "primary", "--force"); err != nil {
		return responses.ConfigSync{}, err
	}
	waitFor(ctx, 5*time.Minute, 2*time.Second, func() bool { return e.leading(ctx) })

	return responses.ConfigSync{RestartedPrimary: true}, nil
}

func (e Engine) lowersStandbyLimits(ctx context.Context) (bool, error) {
	wanted := wantedLimits(e.readFile("conf/dcs.yml"))
	for _, name := range standbyLimits {
		running, err := e.psql.Query(ctx, defaultDatabase, "SELECT setting, boot_val FROM pg_settings WHERE name = :'name'", map[string]string{"name": name})
		if err != nil {
			return false, err
		}
		fields := strings.Split(strings.TrimSpace(running), "|")
		if len(fields) != 2 {
			continue
		}
		target, found := wanted[name]
		if !found {
			target = fields[1]
		}
		if number(target) < number(fields[0]) {
			return true, nil
		}
	}

	return false, nil
}

func wantedLimits(dcs string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(dcs, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ": ")
		if found {
			values[key] = unquoted(value)
		}
	}

	return values
}

func (e Engine) readFile(name string) string {
	data, _ := os.ReadFile(e.path(defaultRoot + "/" + name))

	return string(data)
}

func (e Engine) patronictl(ctx context.Context, args ...string) (string, error) {
	return e.tools.Run(ctx, "patronictl", toolRun{args: append([]string{"-c", confDir + "/patroni.yml"}, args...), timeout: 5 * time.Minute})
}

func (nodes) Reset(context.Context, requests.NodeReset) error {
	return contract.ErrUnsupported
}
