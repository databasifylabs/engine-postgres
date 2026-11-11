package postgres

import (
	"strconv"

	"github.com/databasifylabs/engine-sdk/version"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/settings"
)

const (
	maxInt                 = 2147483647
	pageKb                 = 8
	minConnections         = 100
	connectionMb           = 32
	minSlotWalMb           = 1024
	reloadsAutovacuumSince = "18"
	timeZonePattern        = `[A-Za-z_]{1,20}(/[A-Za-z_+-]{1,30}){0,2}`
	slotWalPattern         = `-1|[0-9]{1,9}(MB|GB|TB)`
)

var onOff = []string{"on", "off"}

func catalogFor(line string) settings.Catalog {
	autovacuumWorkers := settings.Integer("autovacuum_max_workers", "Autovacuum workers", "Most autovacuum processes running at once.", 1, 262143, "3")
	if version.NewerThan(reloadsAutovacuumSince, line) {
		autovacuumWorkers = autovacuumWorkers.Restarting()
	}

	return settings.Catalog{Tune: tunedFor, Fields: settings.Fields(
		settings.Integer("max_connections", "Max connections", "Most client connections accepted at once.", 1, 262143, "100").Restarting().TunedFromServer(),
		settings.Size("shared_buffers", "Shared buffers", "Memory PostgreSQL uses to cache data pages, such as 512MB.", 16*pageKb, 1073741823*pageKb, "128MB").Restarting().TunedFromServer(),
		settings.Size("effective_cache_size", "Effective cache size", "Memory the planner assumes the OS and PostgreSQL use to cache data.", pageKb, maxInt*pageKb, "4GB").TunedFromServer(),
		settings.Size("work_mem", "Work memory", "Memory each sort or hash step may use before spilling to disk.", 64, maxInt, "4MB").TunedFromServer(),
		settings.Size("maintenance_work_mem", "Maintenance memory", "Memory VACUUM, CREATE INDEX and similar commands may use.", 64, maxInt, "64MB").TunedFromServer(),
		settings.Integer("max_worker_processes", "Max background workers", "Most background worker processes.", 0, 262143, "8").Restarting().TunedFromServer(),
		settings.Integer("max_parallel_workers", "Max parallel workers", "Most workers all parallel operations may use at once.", 0, 1024, "8").TunedFromServer(),
		settings.Integer("max_parallel_workers_per_gather", "Parallel workers per query", "Workers one query may use for a parallel scan.", 0, 1024, "2").TunedFromServer(),
		settings.Integer("max_parallel_maintenance_workers", "Parallel maintenance workers", "Workers one CREATE INDEX or VACUUM may use.", 0, 1024, "2").TunedFromServer(),
		settings.Decimal("random_page_cost", "Random page cost", "Planner cost of a random page read. Lower suits SSDs.", 0, 1.79769e308, "4").TunedFromServer(),
		settings.Integer("effective_io_concurrency", "I/O concurrency", "Concurrent disk reads the planner expects the storage to handle.", 0, 1000, "16").TunedFromServer(),
		settings.Text("max_slot_wal_keep_size", "Slot WAL retention", "WAL a replication slot may hold before the replica must re-clone, such as 4096MB. -1 is unlimited.", slotWalPattern, "-1").TunedFromServer(),
		settings.Integer("autovacuum_freeze_max_age", "Autovacuum freeze age", "Transaction age that forces a VACUUM to prevent wraparound.", 100000, 2000000000, "200000000").Restarting(),
		autovacuumWorkers,
		settings.Integer("autovacuum_naptime", "Autovacuum naptime", "Seconds between autovacuum runs on a database.", 1, 2147483, "60").In("s"),
		settings.Integer("autovacuum_vacuum_threshold", "Vacuum threshold", "Updated or deleted rows needed to trigger a VACUUM on a table.", 0, maxInt, "50"),
		settings.Integer("autovacuum_analyze_threshold", "Analyze threshold", "Changed rows needed to trigger an ANALYZE on a table.", 0, maxInt, "50"),
		settings.Decimal("autovacuum_vacuum_scale_factor", "Vacuum scale factor", "Share of a table added to the vacuum threshold.", 0, 100, "0.2"),
		settings.Decimal("autovacuum_analyze_scale_factor", "Analyze scale factor", "Share of a table added to the analyze threshold.", 0, 100, "0.1"),
		settings.Decimal("autovacuum_vacuum_cost_delay", "Vacuum cost delay", "Milliseconds autovacuum sleeps when it reaches its cost limit. -1 uses vacuum_cost_delay.", -1, 100, "2").In("ms"),
		settings.Integer("autovacuum_vacuum_cost_limit", "Vacuum cost limit", "Cost that makes autovacuum sleep. -1 uses vacuum_cost_limit.", -1, 10000, "-1"),
		settings.Integer("bgwriter_delay", "Background writer delay", "Milliseconds between background writer rounds.", 10, 10000, "200").In("ms"),
		settings.Size("bgwriter_flush_after", "Background writer flush", "Data written by the background writer before it asks the OS to flush, such as 512kB.", 0, 256*pageKb, "512kB"),
		settings.Integer("bgwriter_lru_maxpages", "Background writer pages", "Most pages the background writer writes per round.", 0, 1073741823, "100"),
		settings.Decimal("bgwriter_lru_multiplier", "Background writer multiplier", "How far ahead of demand the background writer cleans pages.", 0, 10, "2"),
		settings.Integer("deadlock_timeout", "Deadlock timeout", "Milliseconds to wait on a lock before checking for a deadlock.", 1, maxInt, "1000").In("ms"),
		settings.Choice("default_toast_compression", "TOAST compression", "Compression for large column values.", []string{"pglz", "lz4"}, "pglz"),
		settings.Choice("default_transaction_isolation", "Isolation level", "Isolation level of new transactions.", []string{"read uncommitted", "read committed", "repeatable read", "serializable"}, "read committed"),
		settings.Integer("idle_in_transaction_session_timeout", "Idle transaction timeout", "Milliseconds a session may sit idle inside a transaction before it is closed. 0 never closes.", 0, maxInt, "0").In("ms"),
		settings.Integer("statement_timeout", "Statement timeout", "Milliseconds a statement may run before it is cancelled. 0 never cancels.", 0, maxInt, "0").In("ms"),
		settings.Integer("lock_timeout", "Lock timeout", "Milliseconds a statement waits for a lock before giving up. 0 waits forever.", 0, maxInt, "0").In("ms"),
		settings.Choice("jit", "JIT compilation", "Compile parts of expensive queries to machine code.", onOff, "on"),
		settings.Integer("log_autovacuum_min_duration", "Log slow autovacuum", "Autovacuum runs slower than this many milliseconds are logged. -1 turns it off.", -1, maxInt, "600000").In("ms"),
		settings.Choice("log_error_verbosity", "Error log detail", "How much detail each logged message includes.", []string{"terse", "default", "verbose"}, "default"),
		settings.Integer("log_min_duration_statement", "Slow query log", "Statements slower than this many milliseconds are logged. -1 turns it off.", -1, maxInt, "-1").In("ms"),
		settings.Integer("max_files_per_process", "Max files per process", "Most files each server process may keep open.", 64, maxInt, "1000").Restarting(),
		settings.Integer("max_locks_per_transaction", "Locks per transaction", "Average object locks each transaction may hold.", 10, maxInt, "64").Restarting(),
		settings.Integer("max_pred_locks_per_transaction", "Predicate locks per transaction", "Average predicate locks each serializable transaction may hold.", 10, maxInt, "64").Restarting(),
		settings.Integer("max_prepared_transactions", "Prepared transactions", "Most transactions in the prepared state at once.", 0, 262143, "0").Restarting(),
		settings.Integer("max_logical_replication_workers", "Logical replication workers", "Most logical replication workers.", 0, 262143, "4").Restarting(),
		settings.Size("max_stack_depth", "Max stack depth", "Deepest execution stack a session may use, such as 2MB.", 100, maxInt, "2MB"),
		settings.Integer("max_standby_archive_delay", "Standby archive delay", "Milliseconds a standby waits before cancelling queries that conflict with archived WAL. -1 waits forever.", -1, maxInt, "30000").In("ms"),
		settings.Integer("max_standby_streaming_delay", "Standby streaming delay", "Milliseconds a standby waits before cancelling queries that conflict with streamed WAL. -1 waits forever.", -1, maxInt, "30000").In("ms"),
		settings.Integer("temp_file_limit", "Temporary file limit", "Kilobytes of temporary files each process may use. -1 is unlimited.", -1, maxInt, "-1").In("kB"),
		settings.Text("timezone", "Time zone", "Default time zone, such as UTC or Asia/Dhaka.", timeZonePattern, "UTC"),
		settings.Integer("track_activity_query_size", "Tracked query size", "Bytes of each running query kept for pg_stat_activity.", 100, 1048576, "1024").In("B").Restarting(),
		settings.Choice("track_commit_timestamp", "Track commit time", "Record the commit time of each transaction.", onOff, "off").Restarting(),
		settings.Choice("track_functions", "Track functions", "Which function calls are counted in statistics.", []string{"none", "pl", "all"}, "none"),
		settings.Choice("track_io_timing", "Track I/O timing", "Time disk reads and writes in statistics.", onOff, "off"),
		settings.Choice("wal_level", "WAL level", "Logical writes the extra WAL that logical replication and continuous migrations from this database need.", []string{"replica", "logical"}, "replica").Restarting(),
		settings.Integer("wal_sender_timeout", "WAL sender timeout", "Milliseconds before an inactive replication connection is closed.", 0, maxInt, "60000").In("ms"),
		settings.Integer("wal_writer_delay", "WAL writer delay", "Milliseconds between WAL writer flushes.", 1, 10000, "200").In("ms"),
	)}
}

func tunedFor(resources dtos.Member) map[string]string {
	memory := max(resources.MemoryMb, 1024)
	cpus := max(resources.Cpus, 1)
	sharedBuffers := memory * 25 / 100
	connections := max(minConnections, memory/connectionMb)

	return map[string]string{
		"max_connections":                  strconv.FormatInt(connections, 10),
		"shared_buffers":                   strconv.FormatInt(sharedBuffers, 10) + "MB",
		"effective_cache_size":             strconv.FormatInt(memory*75/100, 10) + "MB",
		"work_mem":                         strconv.FormatInt(min(max((memory-sharedBuffers)/(connections*3), 4), 256), 10) + "MB",
		"maintenance_work_mem":             strconv.FormatInt(min(memory/16, 2048), 10) + "MB",
		"max_worker_processes":             strconv.FormatInt(max(8, cpus), 10),
		"max_parallel_workers":             strconv.FormatInt(max(2, cpus), 10),
		"max_parallel_workers_per_gather":  strconv.FormatInt(min(max(cpus/2, 1), 8), 10),
		"max_parallel_maintenance_workers": strconv.FormatInt(min(max(cpus/2, 1), 8), 10),
		"random_page_cost":                 "1.1",
		"effective_io_concurrency":         "200",
		"max_slot_wal_keep_size":           strconv.FormatInt(max(minSlotWalMb, max(resources.DiskGb, 1)*1024/4), 10) + "MB",
	}
}
