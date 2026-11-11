package postgres

import (
	"strconv"

	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/layout"
	"github.com/databasifylabs/engine-sdk/units"
)

type Engine struct {
	settings   Settings
	psql       Psql
	units      unitProbe
	tools      Tools
	supervisor Supervisor
	root       string
}

func New() Engine {
	settings, err := loadSettings(defaultRoot)

	return Engine{
		settings:   settings,
		psql:       psqlFor(settings, err),
		units:      supervisorUnits{socket: layout.FromEnv().Socket()},
		tools:      execTools{},
		supervisor: units.Control{Socket: layout.FromEnv().Socket()},
		root:       defaultRoot,
	}
}

func psqlFor(settings Settings, loadErr error) Psql {
	if loadErr != nil {
		return failingPsql{err: loadErr}
	}

	return newExecPsql(settings)
}

func (Engine) Name() string { return "postgres" }

func (Engine) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		PasswordEncryptions: []string{},
		ReservedUsers:       []string{"postgres", "replicator", "pgbouncer", "patroni", "admin"},
		ReservedDatabases:   []string{"postgres", "template0", "template1"},
		Ports:               portsOf(),
	}
}

func portsOf() dtos.Ports {
	pool := numberOf(poolPortNumber)

	return dtos.Ports{Client: defaultPort, Pool: &pool, Peers: []int{numberOf(patroniPort), numberOf(etcdClientPort), numberOf(etcdPeerPort)}}
}

func numberOf(port string) int {
	number, _ := strconv.Atoi(port)

	return number
}

func (Engine) HostProfile() contract.HostProfile {
	return contract.HostProfile{Settings: map[string]string{
		"vm.swappiness":                 "1",
		"net.core.somaxconn":            "4096",
		"net.ipv4.tcp_keepalive_time":   "60",
		"net.ipv4.tcp_keepalive_intvl":  "10",
		"net.ipv4.tcp_keepalive_probes": "6",
	}}
}

type nodes struct{ Engine }

type accounts struct{ Engine }

type cluster struct{ Engine }

type archive struct{ Engine }

type migration struct{ Engine }

func (e Engine) Nodes() contract.Nodes { return nodes{e} }

func (e Engine) Accounts() contract.Accounts { return accounts{e} }

func (e Engine) Cluster() contract.Cluster { return cluster{e} }

func (e Engine) Archive() contract.Archive { return archive{e} }

func (e Engine) Migration() contract.Migration { return migration{e} }
