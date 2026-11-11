package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/databasifylabs/engine-sdk/requests"
)

const (
	locksUnit   = "databasify-postgres-locks"
	etcdUnit    = "databasify-etcd"
	serverUnit  = "databasify-postgres"
	poolerUnit  = "databasify-pgbouncer"
	backupUnit  = "databasify-postgres-backup"
	stopTimeout = "2m"
	restartWait = "3s"
	wrapperPath = "/usr/local/bin/databasify"

	locksScript       = "pgrep -x postgres > /dev/null || rm -f /srv/postgres/run/.s.PGSQL.* /srv/postgres/data/postmaster.pid"
	initialClusterAwk = `{ name = ($4 == peer) ? self : $3; printf "%s%s=%s", separator, name, $4; separator = "," }`
	etcdScript        = `set -a; . /srv/postgres/conf/etcd.env; for extra in /srv/postgres/conf/etcd-initial.env /srv/postgres/conf/etcd-force.env; do [ ! -f "$extra" ] || . "$extra"; done; set +a
peers() { awk -F "'" '/^  hosts: / { print $2 }' /srv/postgres/conf/patroni.yml 2>/dev/null | tr ',' '\n' | grep -v "^$ETCD_NAME:"; }
if [ "$ETCD_INITIAL_CLUSTER_STATE" = new ] && [ ! -d "$ETCD_DATA_DIR/member" ]; then
    for endpoint in $(peers); do
        if timeout 10 etcdctl --endpoints="http://$endpoint" member list 2>/dev/null | awk -F ', ' -v peer="$ETCD_INITIAL_ADVERTISE_PEER_URLS" '$4 == peer { listed = 1 } END { exit !listed }'; then
            echo "etcd on $ETCD_NAME lost its data while http://$endpoint still lists it, waiting until it is registered again"
            exit 1
        fi
    done
fi
if [ "$ETCD_INITIAL_CLUSTER_STATE" = existing ] && [ ! -d "$ETCD_DATA_DIR/member" ]; then
    for endpoint in $(peers); do
        cluster=$(timeout 10 etcdctl --endpoints="http://$endpoint" member list 2>/dev/null | awk -F ', ' -v peer="$ETCD_INITIAL_ADVERTISE_PEER_URLS" -v self="$ETCD_NAME" '` + initialClusterAwk + `')
        if [ -n "$cluster" ]; then
            export ETCD_INITIAL_CLUSTER="$cluster"
            break
        fi
    done
fi
exec /usr/local/bin/etcd`
)

type process struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	User        string   `json:"user"`
	After       []string `json:"after,omitempty"`
	Restart     string   `json:"restart,omitempty"`
	RestartSec  string   `json:"restartSec,omitempty"`
	StopSignal  string   `json:"stopSignal,omitempty"`
	StopTimeout string   `json:"stopTimeout,omitempty"`
	Argv        []string `json:"argv"`
	Enabled     bool     `json:"enabled"`
}

type schedule struct {
	Name       string   `json:"name"`
	OnCalendar string   `json:"onCalendar"`
	Timeout    string   `json:"timeout"`
	Argv       []string `json:"argv"`
}

type manifest struct {
	Processes []process  `json:"processes"`
	Schedules []schedule `json:"schedules,omitempty"`
}

func manifestOf(request requests.NodeConfigure, line string) ([]byte, error) {
	m := manifest{Processes: processesOf(request, line)}
	if request.Archive != nil {
		m.Schedules = []schedule{{
			Name: backupUnit, OnCalendar: fmt.Sprintf("%02d:00", request.Archive.BackupHour), Timeout: "24h",
			Argv: []string{wrapperPath, "engine-task", "backup"},
		}}
	}

	return json.MarshalIndent(m, "", "  ")
}

func processesOf(request requests.NodeConfigure, line string) []process {
	locks := process{Name: locksUnit, Type: "oneshot", User: "postgres", Argv: []string{"sh", "-c", locksScript}, Enabled: true}
	pooler := daemon(poolerUnit, []string{"/usr/sbin/pgbouncer", confDir + "/pgbouncer.ini"}, serverUnit, patroniUnit)
	if !request.Clustered {
		server := daemon(serverUnit, []string{"/usr/lib/postgresql/" + line + "/bin/postgres", "-D", defaultRoot + "/data", "-c", "config_file=" + confDir + "/postgresql.conf"}, locksUnit)
		server.StopSignal, server.StopTimeout = "SIGINT", stopTimeout

		return []process{locks, server, pooler}
	}
	patroni := daemon(patroniUnit, []string{"/usr/bin/patroni", confDir + "/patroni.yml"}, locksUnit, etcdUnit)
	patroni.StopSignal, patroni.StopTimeout = "SIGTERM", stopTimeout
	if request.ReadOnly {
		return []process{locks, patroni, pooler}
	}

	return []process{locks, daemon(etcdUnit, []string{"sh", "-c", etcdScript}), patroni, pooler}
}

func daemon(name string, argv []string, after ...string) process {
	return process{Name: name, User: "postgres", After: after, Restart: "always", RestartSec: restartWait, Argv: argv}
}
