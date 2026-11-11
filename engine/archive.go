package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
	"github.com/databasifylabs/engine-sdk/units"
)

const (
	archiveWork     = defaultRoot + "/archive"
	heartbeatsKept  = 50
	fullBackupAfter = 6 * 24 * time.Hour
)

var walName = regexp.MustCompile(`^[0-9A-F]{24}`)

const archiveProblemScript = `SELECT CASE WHEN last_failed_time IS NOT NULL AND (last_archived_time IS NULL OR last_failed_time > last_archived_time) THEN 'WAL ' || last_failed_wal || ' failed to archive at ' || to_char(last_failed_time AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI') || ' UTC.' ELSE '' END FROM pg_stat_archiver`

type backrestInfo []struct {
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Backup []struct {
		Type      string `json:"type"`
		Timestamp struct {
			Stop int64 `json:"stop"`
		} `json:"timestamp"`
	} `json:"backup"`
}

func (e Engine) backrest(ctx context.Context, config string, args ...string) (string, error) {
	return e.tools.Run(ctx, "pgbackrest", toolRun{args: append([]string{"--config=" + config, "--stanza=" + stanza, "--log-level-console=off"}, args...), timeout: 24 * time.Hour})
}

func (e Engine) archiveInfo(ctx context.Context) (backrestInfo, error) {
	out, err := e.backrest(ctx, archiveConf, "info", "--output=json")
	if err != nil {
		return nil, err
	}
	var info backrestInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil || len(info) == 0 {
		return nil, fmt.Errorf("unreadable pgBackRest info")
	}

	return info, nil
}

func (e archive) Status(ctx context.Context, request requests.ArchiveStatus) (responses.ArchiveStatus, error) {
	if !e.exists("conf/pgbackrest.conf") {
		return problemOnly("point-in-time recovery is not configured on this node"), nil
	}
	info, err := e.archiveInfo(ctx)
	if err != nil {
		return problemOnly("pgBackRest could not read the archive: " + err.Error()), nil
	}
	stops := []int64{}
	for _, backup := range info[0].Backup {
		stops = append(stops, backup.Timestamp.Stop)
	}
	sort.Slice(stops, func(i, j int) bool { return stops[i] < stops[j] })
	earliest, lastBackup := firstAndLast(stops)
	latest := e.latestRecoverable(ctx, earliest)
	problem := e.archiveProblem(ctx, info[0].Status.Code, info[0].Status.Message)
	result := e.supervisor.Property(backupUnit, "Result")
	if units.Failed(result) && problem == "" {
		problem = "The last base backup stopped with " + result + ", see the " + backupUnit + " log."
	}
	if lastBackup != nil && (latest == nil || *latest < *lastBackup) {
		latest = lastBackup
	}
	if request.BackUpNow || (len(stops) == 0 && !units.Failed(result)) {
		_ = e.supervisor.DoNoBlock("start", backupUnit)
	}

	return responses.ArchiveStatus{Backups: int64(len(stops)), Earliest: moment(earliest), Latest: moment(latest), LastBackup: moment(lastBackup), Problem: textOrNull(problem)}, nil
}

func problemOnly(problem string) responses.ArchiveStatus {
	return responses.ArchiveStatus{Problem: &problem}
}

func firstAndLast(sorted []int64) (*int64, *int64) {
	if len(sorted) == 0 {
		return nil, nil
	}

	return &sorted[0], &sorted[len(sorted)-1]
}

func moment(epoch *int64) *time.Time {
	if epoch == nil {
		return nil
	}
	at := time.Unix(*epoch, 0).UTC()

	return &at
}

func textOrNull(text string) *string {
	if text == "" {
		return nil
	}

	return &text
}

func (e Engine) archiveProblem(ctx context.Context, code int, message string) string {
	if code == 1 {
		_, _ = e.backrest(ctx, archiveConf, "stanza-create")

		return ""
	}
	problem, _ := e.psql.Query(ctx, defaultDatabase, archiveProblemScript, nil)
	if problem = strings.TrimSpace(problem); problem != "" {
		return problem
	}
	switch code {
	case 0, 2, 6:
		return ""
	}

	return "pgBackRest reports: " + message
}

func (e Engine) latestRecoverable(ctx context.Context, since *int64) *int64 {
	beats := e.recordHeartbeat(ctx)
	archived, _ := e.psql.Query(ctx, defaultDatabase, "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver", nil)
	archived = strings.TrimSpace(archived)
	if !walName.MatchString(archived) || since == nil {
		return nil
	}
	segment, whole := archived[8:24], len(archived) == 24
	var latest *int64
	for _, beat := range beats {
		epoch, wal, found := strings.Cut(beat, " ")
		at := number(epoch)
		if !found || len(wal) < 24 || at < *since {
			continue
		}
		if (wal[8:] < segment || (whole && wal[8:] == segment)) && (latest == nil || at > *latest) {
			latest = &at
		}
	}

	return latest
}

func (e Engine) recordHeartbeat(ctx context.Context) []string {
	path := filepath.Join(e.root, "archive", "state", "heartbeats")
	beats := nonEmptyLines(e.readFile("archive/state/heartbeats"))
	row, err := e.psql.Query(ctx, defaultDatabase, "SELECT floor(extract(epoch FROM now()))::bigint, pg_walfile_name(pg_current_wal_insert_lsn())", nil)
	if err == nil {
		beats = append(beats, strings.ReplaceAll(strings.TrimSpace(row), "|", " "))
	}
	if len(beats) > heartbeatsKept {
		beats = beats[len(beats)-heartbeatsKept:]
	}
	_ = os.WriteFile(path, []byte(strings.Join(beats, "\n")+"\n"), 0o600)

	return beats
}

func (e archive) Backup(ctx context.Context) error {
	inRecovery, err := e.inRecovery(ctx)
	if err != nil || inRecovery {
		return err
	}
	if _, err := e.backrest(ctx, archiveConf, "stanza-create"); err != nil {
		return err
	}
	kind := "diff"
	if info, err := e.archiveInfo(ctx); err == nil && time.Since(time.Unix(lastFull(info), 0)) >= fullBackupAfter {
		kind = "full"
	}
	_, err = e.backrest(ctx, archiveConf, "--type="+kind, "backup")

	return err
}

func lastFull(info backrestInfo) int64 {
	last := int64(0)
	for _, backup := range info[0].Backup {
		if backup.Type == "full" && backup.Timestamp.Stop > last {
			last = backup.Timestamp.Stop
		}
	}

	return last
}

var raisedLimits = map[string]string{
	"max_connections setting":      "max_connections",
	"max_worker_processes setting": "max_worker_processes",
	"max_wal_senders setting":      "max_wal_senders",
	"max_prepared_xacts setting":   "max_prepared_transactions",
	"max_locks_per_xact setting":   "max_locks_per_transaction",
}

func (e archive) Restore(ctx context.Context, request requests.RestoreStart, report contract.Report) error {
	line := os.Getenv("DATABASIFY_LINE")
	content, err := restoreConfigOf(request.Storage)
	if err != nil {
		return err
	}
	if _, err := e.write(nodeFile{path: restoreConf, content: content, mode: 0o600}); err != nil {
		return err
	}
	defer func() { _ = os.Remove(e.path(restoreConf)) }()
	report("stopping")
	names, _ := e.manifestUnits()
	for _, name := range names {
		_ = e.supervisor.Do("stop", name)
	}
	if err := e.emptyData(); err != nil {
		return err
	}
	report("restoring")
	target := request.Target.UTC().Format("2006-01-02 15:04:05")
	restoreCommand := "restore_command=pgbackrest --config=" + restoreConf + " --stanza=" + stanza + ` archive-get %f "%p"`
	if _, err := e.backrest(ctx, restoreConf, "--type=time", "--target="+target+"+00", "--target-action=promote", "--recovery-option="+restoreCommand, "restore"); err != nil {
		return err
	}
	raised, err := e.raiseLimitsToSource(ctx, line)
	if err != nil {
		return err
	}
	report("replaying")
	_ = e.supervisor.Do("reset-failed", serverUnit)
	if err := e.supervisor.Do("start", serverUnit); err != nil {
		return err
	}
	promoted := waitFor(ctx, 21000*time.Second, 5*time.Second, func() bool {
		inRecovery, err := e.inRecovery(ctx)

		return err == nil && !inRecovery
	})
	if !promoted {
		return fmt.Errorf("recovery did not reach %s UTC", target)
	}
	report("resetting")
	for _, setting := range append([]string{"restore_command", "recovery_target_time", "recovery_target_action", "recovery_target_timeline"}, raised...) {
		if _, err := e.psql.Query(ctx, defaultDatabase, "ALTER SYSTEM RESET "+setting, nil); err != nil {
			return err
		}
	}
	_, err = e.psql.Query(ctx, defaultDatabase, "SELECT pg_reload_conf()", nil)

	return err
}

func restoreConfigOf(storage dtos.Storage) (string, error) {
	var content strings.Builder
	view := fileView{Repository: repositoryOf(storage), ClientPort: clientPortNumber}
	err := templates.ExecuteTemplate(&content, "pgbackrest.conf", view)

	return content.String(), err
}

func (e Engine) emptyData() error {
	entries, err := os.ReadDir(filepath.Join(e.root, "data"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(e.root, "data", entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

func (e Engine) raiseLimitsToSource(ctx context.Context, line string) ([]string, error) {
	control, err := e.tools.Run(ctx, "/usr/lib/postgresql/"+line+"/bin/pg_controldata", toolRun{args: []string{defaultRoot + "/data"}})
	if err != nil {
		return nil, err
	}
	raised := []string{}
	extra := ""
	for _, row := range strings.Split(control, "\n") {
		label, value, found := strings.Cut(row, ":")
		name, known := raisedLimits[strings.TrimSpace(label)]
		if !found || !known {
			continue
		}
		current, err := e.tools.Run(ctx, "/usr/lib/postgresql/"+line+"/bin/postgres", toolRun{args: []string{"-D", defaultRoot + "/data", "-c", "config_file=" + confDir + "/postgresql.conf", "-C", name}})
		if err != nil {
			return nil, err
		}
		if wanted, _ := strconv.Atoi(strings.TrimSpace(value)); wanted > int(number(current)) {
			extra += fmt.Sprintf("%s = '%d'\n", name, wanted)
			raised = append(raised, name)
		}
	}
	if extra == "" {
		return raised, nil
	}
	auto, err := os.OpenFile(filepath.Join(e.root, "data", "postgresql.auto.conf"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = auto.Close() }()
	_, err = auto.WriteString(extra)

	return raised, err
}

func (archive) Stream(context.Context) error {
	return contract.ErrUnsupported
}

func (archive) Ship(context.Context) error {
	return contract.ErrUnsupported
}
