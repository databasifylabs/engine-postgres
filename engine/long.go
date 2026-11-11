package postgres

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

const (
	readyDeadline  = 21000 * time.Second
	crashRestarts  = 3
	poolerAddress  = "127.0.0.1:" + poolPortNumber
	leaderDeadline = 10 * time.Minute
)

func (e Engine) mainUnit() string {
	if e.clustered() {
		return patroniUnit
	}

	return serverUnit
}

func (e Engine) restartsOf(unit string) int64 {
	return number(e.supervisor.Property(unit, "NRestarts"))
}

func (e Engine) crashLooping(unit string, baseline int64) error {
	if e.restartsOf(unit)-baseline > crashRestarts {
		return fmt.Errorf("%s keeps crashing, see its log", unit)
	}

	return nil
}

func (e nodes) Ready(ctx context.Context, request requests.NodeReady, report contract.Report) error {
	unit := e.mainUnit()
	if e.clustered() && e.exists("conf/etcd.env") {
		report("joining etcd")
		e.promoteOwnLearner(ctx)
	}
	report("waiting for PostgreSQL")
	var crash error
	baseline := e.restartsOf(unit)
	ready := waitFor(ctx, readyDeadline, 5*time.Second, func() bool {
		if crash = e.crashLooping(unit, baseline); crash != nil {
			return true
		}

		return e.serves(ctx, request.ExpectPrimary)
	})
	if crash != nil {
		return crash
	}
	if !ready {
		return fmt.Errorf("PostgreSQL was not ready in time")
	}
	report("waiting for the pooler")
	if !waitFor(ctx, 5*time.Minute, 2*time.Second, poolerListening) {
		return fmt.Errorf("pgbouncer is not listening on %s", poolerAddress)
	}
	if inRecovery, err := e.inRecovery(ctx); err == nil && !inRecovery {
		if _, err := e.psql.Query(ctx, defaultDatabase, createStatementsExtension, nil); err != nil {
			return err
		}
	}
	if request.ExpectPrimary && e.exists("conf/pgbackrest.conf") {
		report("switching WAL")
		_, err := e.psql.Query(ctx, defaultDatabase, "SELECT pg_switch_wal(); CHECKPOINT;", nil)

		return err
	}

	return nil
}

func (e Engine) serves(ctx context.Context, expectPrimary bool) bool {
	if !e.clustered() {
		inRecovery, err := e.inRecovery(ctx)

		return err == nil && (!expectPrimary || !inRecovery)
	}
	state := e.patroniState(ctx)
	role := roleNamed(state.Role)
	if expectPrimary {
		return role == enums.NodeRolePrimary && state.State == "running"
	}

	return role != enums.NodeRoleUnknown && (state.State == "running" || state.State == "streaming")
}

func poolerListening() bool {
	connection, err := net.DialTimeout("tcp", poolerAddress, time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()

	return true
}

func (e Engine) promoteOwnLearner(ctx context.Context) {
	ip := e.ownIP()
	waitFor(ctx, 5*time.Minute, 3*time.Second, func() bool {
		for _, endpoint := range e.membershipEndpoints(ip) {
			if e.promotedThrough(ctx, endpoint, ip) {
				return true
			}
		}

		return false
	})
}

func (e Engine) membershipEndpoints(self string) []string {
	return append([]string{localEtcd}, e.peerEtcdEndpoints(self)...)
}

func (e Engine) peerEtcdEndpoints(self string) []string {
	var endpoints []string
	for _, host := range strings.Split(patroniValueIn(strings.Split(e.readFile("conf/patroni.yml"), "\n"), "etcd3.hosts"), ",") {
		if host != "" && !strings.HasPrefix(host, self+":") {
			endpoints = append(endpoints, "http://"+host)
		}
	}

	return endpoints
}

func (e Engine) promotedThrough(ctx context.Context, endpoint string, ip string) bool {
	member, found, err := e.memberWithPeerAt(ctx, endpoint, ip)
	if err != nil || !found {
		return false
	}
	if !member.IsLearner {
		return true
	}
	_, err = e.etcd(ctx, endpoint, "member", "promote", hexID(member.ID))

	return err == nil
}

func (e cluster) Enable(ctx context.Context, _ requests.ClusterEnable, report contract.Report) error {
	if err := e.supervisor.Do("daemon-reload"); err != nil {
		return err
	}
	if e.exists("conf/etcd.env") {
		if err := e.joinEtcd(ctx, report); err != nil {
			return err
		}
	}
	report("starting Patroni")
	_ = e.supervisor.Do("reset-failed", patroniUnit)
	for _, step := range [][2]string{{"enable", patroniUnit}, {"start", patroniUnit}} {
		if err := e.supervisor.Do(step[0], step[1]); err != nil {
			return err
		}
	}
	var crash error
	baseline := e.restartsOf(patroniUnit)
	led := waitFor(ctx, leaderDeadline, 3*time.Second, func() bool {
		if crash = e.crashLooping(patroniUnit, baseline); crash != nil {
			return true
		}

		return settled(e.patroniState(ctx))
	})
	if crash != nil {
		return crash
	}
	if !led {
		return fmt.Errorf("Patroni did not lead or follow the cluster in time")
	}

	return e.supervisor.Do("restart", poolerUnit)
}

func (e cluster) joinEtcd(ctx context.Context, report contract.Report) error {
	report("starting etcd")
	for _, step := range [][2]string{{"enable", etcdUnit}, {"start", etcdUnit}} {
		if err := e.supervisor.Do(step[0], step[1]); err != nil {
			return err
		}
	}
	report("joining etcd")
	e.promoteOwnLearner(ctx)
	if !waitFor(ctx, 2*time.Minute, 3*time.Second, e.etcdHealthy(ctx)) {
		return fmt.Errorf("etcd did not become healthy")
	}

	return nil
}
