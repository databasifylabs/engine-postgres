package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/ctl"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	migrationUnit  = "databasify-postgres-migration"
	migrationState = "migration/state"
	sourceTimeout  = 30 * time.Second
	dumpTimeout    = 72 * time.Hour
)

var copyableName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

const listSourceDatabases = "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY datname"

func sourceEnv(source dtos.MigrationSource, database string) []string {
	mode := "prefer"
	if source.Tls {
		mode = "require"
	}

	return []string{"PGHOST=" + source.Host, fmt.Sprintf("PGPORT=%d", source.Port), "PGUSER=" + source.Username, "PGPASSWORD=" + source.Password, "PGSSLMODE=" + mode, "PGDATABASE=" + database, "PGCONNECT_TIMEOUT=10"}
}

func (e Engine) onSource(ctx context.Context, source dtos.MigrationSource, database string, query string) (string, error) {
	out, err := e.tools.Run(ctx, "psql", toolRun{args: []string{"-X", "-A", "-t", "-c", query}, env: sourceEnv(source, database), timeout: sourceTimeout})

	return strings.TrimSpace(out), err
}

func (e migration) Check(ctx context.Context, request requests.MigrationCheck) (responses.MigrationCheck, error) {
	source := request.Source
	version, err := e.onSource(ctx, source, defaultDatabase, "SHOW server_version")
	if err != nil {
		message := lastLines(err.Error(), 1)

		return responses.MigrationCheck{Error: &message, Databases: []string{}, Unreadable: []string{}, Unowned: []string{}, Occupied: []string{}}, nil
	}
	walLevel, _ := e.onSource(ctx, source, defaultDatabase, "SHOW wal_level")
	replicates, _ := e.onSource(ctx, source, defaultDatabase, "SELECT rolsuper OR rolreplication FROM pg_roles WHERE rolname = current_user")
	size, _ := e.onSource(ctx, source, defaultDatabase, "SELECT round(COALESCE(sum(pg_database_size(datname)), 0) / 1048576) FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'")
	listed, _ := e.onSource(ctx, source, defaultDatabase, listSourceDatabases)
	wanted := request.Databases
	if len(wanted) == 0 {
		wanted = copyable(nonEmptyLines(listed))
	}
	sizeMb := number(size)
	cut := strings.Fields(version)[0]
	reply := responses.MigrationCheck{
		Reachable: true, Version: &cut, WalLevel: &walLevel, CanReplicate: replicates == "t", SizeMb: &sizeMb,
		Databases: nonEmptyLines(listed), Unreadable: []string{}, Unowned: []string{}, Occupied: []string{},
	}
	for _, database := range wanted {
		reply.Unreadable = append(reply.Unreadable, e.sourceTables(ctx, source, database, "'r', 'p', 'S'", "NOT (has_schema_privilege(n.oid, 'USAGE') AND has_table_privilege(c.oid, 'SELECT'))")...)
		reply.Unowned = append(reply.Unowned, e.sourceTables(ctx, source, database, "'r', 'p'", "NOT pg_has_role(c.relowner, 'USAGE')")...)
		if e.holdsRows(ctx, database) {
			reply.Occupied = append(reply.Occupied, database)
		}
	}

	return reply, nil
}

func copyable(names []string) []string {
	kept := []string{}
	for _, name := range names {
		if copyableName.MatchString(name) {
			kept = append(kept, name)
		}
	}

	return kept
}

func (e Engine) sourceTables(ctx context.Context, source dtos.MigrationSource, database, kinds, condition string) []string {
	query := "SELECT format('%I.%I.%I', current_database(), n.nspname, c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN (" + kinds + ") AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND " + condition + " ORDER BY 1"
	out, err := e.onSource(ctx, source, database, query)
	if err != nil {
		return nil
	}

	return nonEmptyLines(out)
}

func (e Engine) holdsRows(ctx context.Context, database string) bool {
	exists, err := e.psql.Query(ctx, defaultDatabase, "SELECT 1 FROM pg_database WHERE datname = :'db'", map[string]string{"db": database})
	if err != nil || strings.TrimSpace(exists) != "1" {
		return false
	}
	probe, err := e.psql.Query(ctx, database, "SELECT string_agg(format('SELECT 1 FROM %I.%I', schemaname, tablename), ' UNION ALL ') FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')", nil)
	if err != nil || strings.TrimSpace(probe) == "" {
		return false
	}
	found, err := e.psql.Query(ctx, database, "SELECT 1 FROM ("+strings.TrimSpace(probe)+") found LIMIT 1", nil)

	return err == nil && strings.TrimSpace(found) == "1"
}

func (e migration) Start(ctx context.Context, request requests.MigrationStart) error {
	if request.Owner == nil {
		return command.Invalid("PostgreSQL migrations need an owner")
	}
	if request.Continuous && request.Publication == nil {
		return command.Invalid("continuous PostgreSQL migrations need a publication")
	}
	_ = e.supervisor.Do("stop", migrationUnit)
	_ = e.supervisor.Do("reset-failed", migrationUnit)
	state := filepath.Join(e.root, migrationState)
	if err := os.RemoveAll(state); err != nil {
		return err
	}
	if err := e.directories(0o700, migrationState); err != nil {
		return err
	}
	env := map[string]string{
		"MIGRATION_DATABASES": strings.Join(request.Databases, " "), "MIGRATION_OWNER": *request.Owner,
		"MIGRATION_CONTINUOUS": fmt.Sprint(request.Continuous),
	}
	if request.Publication != nil {
		env["MIGRATION_PUBLICATION"] = *request.Publication
	}
	for _, pair := range sourceEnv(request.Source, defaultDatabase) {
		key, value, _ := strings.Cut(pair, "=")
		env["SOURCE_"+key] = value
	}
	transient := ctl.Transient{Unit: migrationUnit, Argv: []string{wrapperPath, "engine-task", "migration-copy"}, Env: env, RemainAfterExit: true}

	return e.supervisor.Run(transient)
}

func sourceFromEnvironment() dtos.MigrationSource {
	return dtos.MigrationSource{
		Host: os.Getenv("SOURCE_PGHOST"), Port: number(os.Getenv("SOURCE_PGPORT")), Username: os.Getenv("SOURCE_PGUSER"),
		Password: os.Getenv("SOURCE_PGPASSWORD"), Tls: os.Getenv("SOURCE_PGSSLMODE") == "require",
	}
}

func (e migration) Copy(ctx context.Context) error {
	err := e.copyDatabases(ctx, sourceFromEnvironment())
	status := "0"
	if err != nil {
		status = "1"
		fmt.Println(err)
	}
	_ = os.WriteFile(filepath.Join(e.root, migrationState, "copy.exit"), []byte(status+"\n"), 0o600)

	return err
}

func (e Engine) copyDatabases(ctx context.Context, source dtos.MigrationSource) error {
	databases := strings.Fields(os.Getenv("MIGRATION_DATABASES"))
	if len(databases) == 0 {
		listed, err := e.onSource(ctx, source, defaultDatabase, listSourceDatabases)
		if err != nil {
			return err
		}
		databases = copyable(nonEmptyLines(listed))
	}
	for _, database := range databases {
		fmt.Println("copying " + database)
		if err := e.copyDatabase(ctx, source, database); err != nil {
			return fmt.Errorf("%s: %w", database, err)
		}
		fmt.Println(database + " done")
	}

	return nil
}

func (e Engine) copyDatabase(ctx context.Context, source dtos.MigrationSource, database string) error {
	owner := os.Getenv("MIGRATION_OWNER")
	if _, err := (accounts{e}).CreateDatabase(ctx, requests.DatabaseCreate{Name: database}); err != nil {
		return err
	}
	if err := e.grantOn(ctx, owner, dtos.Grant{Database: database, Access: enums.AccessWrite}); err != nil {
		return err
	}
	continuous := os.Getenv("MIGRATION_CONTINUOUS") == "true"
	if err := e.dumpAndRestore(ctx, source, database, owner, continuous); err != nil {
		return err
	}
	if !continuous {
		return nil
	}

	return e.subscribe(ctx, source, database)
}

func (e Engine) dumpAndRestore(ctx context.Context, source dtos.MigrationSource, database string, owner string, schemaOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, dumpTimeout)
	defer cancel()
	dumpArgs := []string{"--no-owner", "--no-acl", "-Fc"}
	if schemaOnly {
		dumpArgs = append(dumpArgs, "--schema-only")
	}
	dump := e.asPostgres(ctx, "pg_dump", dumpArgs, sourceEnv(source, database))
	restore := e.asPostgres(ctx, "pg_restore", []string{"-h", e.settings.SocketDirectory, "-p", fmt.Sprint(e.settings.Port), "-U", "postgres", "--role=" + owner, "-d", database, "--no-owner", "--no-acl", "--exit-on-error"}, nil)
	pipe, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	restore.Stdin = pipe
	var dumpErr, restoreErr strings.Builder
	dump.Stderr, restore.Stderr = &dumpErr, &restoreErr
	restore.Stdout = io.Discard
	if err := dump.Start(); err != nil {
		return err
	}
	if err := restore.Run(); err != nil {
		_ = dump.Process.Kill()
		_ = dump.Wait()

		return fmt.Errorf("pg_restore: %w: %s", err, lastLines(restoreErr.String(), 3))
	}
	if err := dump.Wait(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, lastLines(dumpErr.String(), 3))
	}

	return nil
}

func (e Engine) asPostgres(ctx context.Context, name string, args []string, env []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + postgresHome}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: postgresUID, Gid: postgresUID}}

	return cmd
}

const subscribeScript = `SELECT :'publication' || '_' || left(:'db', 20) || '_' || left(md5(:'db'), 8) AS name \gset
CREATE SUBSCRIPTION :"name" CONNECTION :'conninfo' PUBLICATION :"publication" WITH (copy_data = true);`

func (e Engine) subscribe(ctx context.Context, source dtos.MigrationSource, database string) error {
	tables, err := e.onSource(ctx, source, database, "SELECT string_agg(format('%I.%I', schemaname, tablename), ', ') FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')")
	if err != nil {
		return err
	}
	if tables == "" {
		fmt.Println(database + " has no tables to replicate")

		return nil
	}
	publication := os.Getenv("MIGRATION_PUBLICATION")
	if _, err := e.onSource(ctx, source, database, `DROP PUBLICATION IF EXISTS "`+publication+`"; CREATE PUBLICATION "`+publication+`" FOR TABLE `+tables); err != nil {
		return err
	}
	vars := map[string]string{"publication": publication, "db": database, "conninfo": conninfoOf(source, database)}
	_, err = e.psql.Query(ctx, database, subscribeScript, vars)

	return err
}

func conninfoOf(source dtos.MigrationSource, database string) string {
	parts := []string{}
	for _, pair := range sourceEnv(source, database)[:6] {
		key, value, _ := strings.Cut(pair, "=")
		name := map[string]string{"PGHOST": "host", "PGPORT": "port", "PGUSER": "user", "PGPASSWORD": "password", "PGSSLMODE": "sslmode", "PGDATABASE": "dbname"}[key]
		parts = append(parts, name+"='"+strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`)+"'")
	}

	return strings.Join(parts, " ")
}

const lagScript = `SELECT COALESCE((CASE WHEN st.last_msg_send_time IS NULL THEN NULL WHEN st.latest_end_lsn >= st.received_lsn AND now() - st.last_msg_receipt_time < interval '90 seconds' THEN 0 ELSE GREATEST(round(extract(epoch FROM now() - st.last_msg_send_time)), 0) END)::text, 'unknown') FROM pg_subscription s LEFT JOIN pg_stat_subscription st ON st.subid = s.oid AND st.relid IS NULL AND st.worker_type = 'apply' WHERE s.subname LIKE 'databasify_migration_%'`

func (e migration) Progress(ctx context.Context) (responses.MigrationProgress, error) {
	reply := responses.MigrationProgress{Copy: e.copyState(), Log: e.copyLog()}
	databases, _ := e.psql.Query(ctx, defaultDatabase, "SELECT d.datname FROM pg_subscription s JOIN pg_database d ON d.oid = s.subdbid WHERE s.subname LIKE 'databasify_migration_%'", nil)
	for _, database := range nonEmptyLines(databases) {
		row, err := e.psql.Query(ctx, database, "SELECT count(*), count(*) FILTER (WHERE srsubstate = 'r') FROM pg_subscription_rel", nil)
		if fields := strings.Split(strings.TrimSpace(row), "|"); err == nil && len(fields) == 2 {
			reply.SubscribedTables += number(fields[0])
			reply.ReadyTables += number(fields[1])
		}
	}
	lags, _ := e.psql.Query(ctx, defaultDatabase, lagScript, nil)
	for _, lag := range nonEmptyLines(lags) {
		if lag == "unknown" {
			reply.LagUnknown = true

			continue
		}
		if seconds := number(lag); reply.LagSeconds == nil || seconds > *reply.LagSeconds {
			reply.LagSeconds = &seconds
		}
	}

	return reply, nil
}

func (e Engine) copyState() string {
	switch exit := strings.TrimSpace(e.readFile(migrationState + "/copy.exit")); {
	case exit == "0":
		return "succeeded"
	case exit != "":
		return "failed"
	case e.supervisor.Active(migrationUnit):
		return "running"
	}

	return "lost"
}

func (e Engine) copyLog() string {
	return strings.Join(strings.Split(lastLines(e.readFile("logs/"+migrationUnit+".log"), 2), "\n"), " ")
}

const subscriptionsScript = `SELECT d.datname || '|' || s.subname || '|' || array_to_string(s.subpublications, ',') FROM pg_subscription s JOIN pg_database d ON d.oid = s.subdbid WHERE s.subname LIKE 'databasify_migration_%'`

func (e migration) Stop(ctx context.Context) error {
	_ = e.supervisor.Do("stop", migrationUnit)
	rows, _ := e.psql.Query(ctx, defaultDatabase, subscriptionsScript, nil)
	for _, row := range nonEmptyLines(rows) {
		fields := strings.Split(row, "|")
		if len(fields) == 3 {
			e.endSubscription(ctx, fields[0], fields[1], fields[2])
		}
	}

	return os.RemoveAll(filepath.Join(e.root, migrationState))
}

func (e Engine) endSubscription(ctx context.Context, database, subscription, publication string) {
	conninfo, _ := e.psql.Query(ctx, database, "SELECT subconninfo FROM pg_subscription WHERE subname = :'name'", map[string]string{"name": subscription})
	conninfo = strings.TrimSpace(conninfo)
	if values, err := e.onConninfo(ctx, conninfo, "SELECT format('SELECT setval(%L, %s, true);', format('%I.%I', schemaname, sequencename), last_value) FROM pg_sequences WHERE last_value IS NOT NULL"); err == nil && values != "" {
		_, _ = e.psql.Query(ctx, database, values, nil)
	}
	vars := map[string]string{"name": subscription}
	if _, err := e.psql.Query(ctx, database, `DROP SUBSCRIPTION :"name"`, vars); err != nil {
		_, _ = e.psql.Query(ctx, database, `ALTER SUBSCRIPTION :"name" DISABLE; ALTER SUBSCRIPTION :"name" SET (slot_name = NONE); DROP SUBSCRIPTION :"name";`, vars)
	}
	_, _ = e.onConninfo(ctx, conninfo, `DROP PUBLICATION IF EXISTS "`+publication+`"`)
}

func (e Engine) onConninfo(ctx context.Context, conninfo string, query string) (string, error) {
	if conninfo == "" {
		return "", errors.New("no connection string")
	}
	out, err := e.tools.Run(ctx, "psql", toolRun{args: []string{conninfo + " connect_timeout=10", "-X", "-A", "-t", "-c", query}, timeout: sourceTimeout})

	return strings.TrimSpace(out), err
}
