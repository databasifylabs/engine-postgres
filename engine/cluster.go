package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const localEtcd = "http://127.0.0.1:" + etcdClientPort

var hostsLine = regexp.MustCompile(`(?m)^  hosts: '.*'$`)

const positionScript = `SELECT pg_wal_lsn_diff(CASE WHEN pg_is_in_recovery() THEN GREATEST(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) ELSE pg_current_wal_lsn() END, '0/0')::bigint`

const dropInactiveSlotsScript = `SELECT count(pg_drop_replication_slot(slot_name)) FROM pg_replication_slots WHERE slot_type = 'physical' AND NOT active`

type etcdMember struct {
	ID        uint64   `json:"ID"`
	PeerURLs  []string `json:"peerURLs"`
	IsLearner bool     `json:"isLearner"`
}

func (e Engine) etcdMembers(ctx context.Context, endpoint string) ([]etcdMember, error) {
	out, err := e.etcd(ctx, endpoint, "member", "list", "-w", "json", "--consistency=s")
	if err != nil {
		return nil, err
	}
	var list struct {
		Members []etcdMember `json:"members"`
	}

	return list.Members, json.Unmarshal([]byte(out), &list)
}

func (e Engine) memberWithPeer(ctx context.Context, ip string) (etcdMember, bool, error) {
	return e.memberWithPeerAt(ctx, localEtcd, ip)
}

func (e Engine) memberWithPeerAt(ctx context.Context, endpoint string, ip string) (etcdMember, bool, error) {
	members, err := e.etcdMembers(ctx, endpoint)
	if err != nil {
		return etcdMember{}, false, err
	}
	peer := "http://" + ip + ":" + etcdPeerPort
	for _, member := range members {
		for _, url := range member.PeerURLs {
			if url == peer {
				return member, true, nil
			}
		}
	}

	return etcdMember{}, false, nil
}

func hexID(id uint64) string {
	return strconv.FormatUint(id, 16)
}

func (e cluster) Admit(ctx context.Context, request requests.MemberAdmit) error {
	if request.ReadOnly {
		return nil
	}
	if _, found, err := e.memberWithPeer(ctx, request.Ip); found || err != nil {
		return err
	}
	_, err := e.etcd(ctx, localEtcd, "member", "add", request.Ip, "--peer-urls=http://"+request.Ip+":"+etcdPeerPort, "--learner")

	return err
}

func (e cluster) Promote(ctx context.Context, request requests.MemberPromote) error {
	if request.ReadOnly {
		return nil
	}
	var lastErr error
	promoted := waitFor(ctx, 5*time.Minute, 3*time.Second, func() bool {
		member, found, err := e.memberWithPeer(ctx, request.Ip)
		if err != nil || !found {
			lastErr = errors.Join(err, errors.New(request.Ip+" is not an etcd member"))

			return false
		}
		if !member.IsLearner {
			return true
		}
		_, lastErr = e.etcd(ctx, localEtcd, "member", "promote", hexID(member.ID))

		return lastErr == nil
	})
	if !promoted {
		return command.Unavailable("could not promote " + request.Ip + " to a voting etcd member: " + errText(lastErr))
	}

	return nil
}

func (e cluster) Forget(ctx context.Context, request requests.MemberForget) error {
	member, found, err := e.memberWithPeer(ctx, request.Ip)
	if err != nil || !found {
		return nil
	}
	_, err = e.etcd(ctx, localEtcd, "member", "remove", hexID(member.ID))

	return err
}

func (e cluster) Quorum(ctx context.Context) (responses.ClusterQuorum, error) {
	hosts := e.votingHosts(ctx)
	online := int64(0)
	for _, host := range hosts {
		if e.memberOnline(ctx, host) {
			online++
		}
	}

	return responses.ClusterQuorum{Total: int64(len(hosts)), Online: online}, nil
}

func (e Engine) votingHosts(ctx context.Context) []string {
	hosts := []string{}
	if members, err := e.etcdMembers(ctx, localEtcd); err == nil {
		for _, member := range members {
			if !member.IsLearner && len(member.PeerURLs) > 0 {
				hosts = append(hosts, hostOf(member.PeerURLs[0]))
			}
		}
	}
	if len(hosts) > 0 {
		return hosts
	}
	for _, host := range strings.Split(patroniValueIn(strings.Split(e.readFile("conf/patroni.yml"), "\n"), "etcd3.hosts"), ",") {
		if host != "" {
			hosts = append(hosts, strings.Split(host, ":")[0])
		}
	}

	return hosts
}

func hostOf(url string) string {
	host := strings.TrimPrefix(url, "http://")

	return host[:strings.LastIndex(host, ":")]
}

func (e Engine) memberOnline(ctx context.Context, host string) bool {
	var state patroniStatus
	peer := e
	peer.settings.PatroniURL = "http://" + host + ":" + patroniPort
	if err := peer.patroniGet(ctx, "/patroni", &state); err != nil || (state.State != "running" && state.State != "streaming") {
		return false
	}
	_, err := e.etcd(ctx, "http://"+host+":"+etcdClientPort, "endpoint", "health")

	return err == nil
}

func (e cluster) Position(ctx context.Context) (responses.ClusterPosition, error) {
	if e.supervisor.Active(patroniUnit) || e.supervisor.Active(serverUnit) {
		if reply, err := e.psql.Query(ctx, defaultDatabase, positionScript, nil); err == nil {
			position := number(reply)

			return responses.ClusterPosition{Position: &position}, nil
		}
	}

	return responses.ClusterPosition{Position: e.controlDataPosition(ctx)}, nil
}

func (e Engine) controlDataPosition(ctx context.Context) *int64 {
	out, err := e.tools.Run(ctx, "/usr/lib/postgresql/"+os.Getenv("DATABASIFY_LINE")+"/bin/pg_controldata", toolRun{args: []string{defaultRoot + "/data"}})
	if err != nil {
		return nil
	}
	var best *int64
	for _, line := range strings.Split(out, "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found || (label != "Latest checkpoint location" && label != "Minimum recovery ending location") {
			continue
		}
		if bytes, ok := lsnBytes(strings.TrimSpace(value)); ok && (best == nil || bytes > *best) {
			best = &bytes
		}
	}

	return best
}

func lsnBytes(lsn string) (int64, bool) {
	high, low, found := strings.Cut(lsn, "/")
	h, errHigh := strconv.ParseInt(high, 16, 64)
	l, errLow := strconv.ParseInt(low, 16, 64)

	return h<<32 + l, found && errHigh == nil && errLow == nil
}

func (e cluster) Failover(ctx context.Context, request requests.ClusterFailover) error {
	if _, err := e.patronictl(ctx, "switchover", "--candidate", request.Target, "--force"); err != nil {
		return err
	}
	if !waitFor(ctx, 2*time.Minute, 2*time.Second, func() bool { return roleNamed(e.patroniState(ctx).Role) == enums.NodeRoleReplica }) {
		return command.Unavailable("this node is still primary after the switchover to " + request.Target)
	}

	return nil
}

func (e cluster) Disable(ctx context.Context) (responses.ClusterDisable, error) {
	for _, unit := range []string{patroniUnit, etcdUnit} {
		_ = e.supervisor.Do("disable", unit)
		_ = e.supervisor.Do("stop", unit)
	}
	for _, name := range []string{"etcd", "conf/patroni.yml", "conf/etcd.env", "conf/etcd-initial.env", "conf/etcd-force.env", "conf/dcs.yml", "conf/pgpass", "data/standby.signal", "data/patroni.dynamic.json"} {
		_ = os.RemoveAll(filepath.Join(e.root, name))
	}
	if err := e.configureStandalone(ctx); err != nil {
		return responses.ClusterDisable{}, err
	}
	for _, step := range [][2]string{{"enable", serverUnit}, {"restart", serverUnit}, {"restart", poolerUnit}} {
		if err := e.supervisor.Do(step[0], step[1]); err != nil {
			return responses.ClusterDisable{}, err
		}
	}
	if !waitFor(ctx, 5*time.Minute, 2*time.Second, func() bool { inRecovery, err := e.inRecovery(ctx); return err == nil && !inRecovery }) {
		return responses.ClusterDisable{}, command.Unavailable("PostgreSQL did not come back as a standalone primary")
	}
	dropped, err := e.psql.Query(ctx, defaultDatabase, dropInactiveSlotsScript, nil)
	if err != nil {
		return responses.ClusterDisable{}, err
	}

	return responses.ClusterDisable{DroppedSlots: number(dropped)}, nil
}

func (e cluster) configureStandalone(ctx context.Context) error {
	request, err := e.savedRequest()
	if err != nil {
		return err
	}
	request.Clustered, request.ReadOnly = false, false
	_, err = e.Nodes().Configure(ctx, request)

	return err
}

func (e cluster) ForceQuorum(ctx context.Context, request requests.ClusterForceQuorum) (responses.ClusterForceQuorum, error) {
	self := e.ownIP()
	if !e.clustered() {
		if !waitFor(ctx, 5*time.Minute, 2*time.Second, func() bool { inRecovery, err := e.inRecovery(ctx); return err == nil && !inRecovery }) {
			return responses.ClusterForceQuorum{}, command.Unavailable("PostgreSQL did not come up as primary")
		}

		return responses.ClusterForceQuorum{Leader: self}, nil
	}
	forcedAlone, err := e.ensureEtcdQuorum(ctx, request.OthersDown, self)
	if err != nil {
		return responses.ClusterForceQuorum{}, err
	}
	for _, unit := range []string{patroniUnit, poolerUnit} {
		if err := e.supervisor.Do("start", unit); err != nil {
			return responses.ClusterForceQuorum{}, err
		}
	}
	leader := ""
	found := func() bool { leader = e.clusterLeader(ctx); return leader != "" }
	if !waitFor(ctx, 90*time.Second, 3*time.Second, found) && forcedAlone {
		_, _ = e.patronictl(ctx, "failover", "--candidate", self, "--force")
	}
	if !waitFor(ctx, 5*time.Minute, 3*time.Second, found) {
		return responses.ClusterForceQuorum{}, command.Unavailable("no PostgreSQL leader came up")
	}

	return responses.ClusterForceQuorum{Leader: leader}, nil
}

func (e Engine) ensureEtcdQuorum(ctx context.Context, othersDown bool, self string) (bool, error) {
	wait := 2 * time.Minute
	if othersDown {
		wait = 30 * time.Second
	}
	if waitFor(ctx, wait, 3*time.Second, e.etcdHealthy(ctx)) {
		return false, nil
	}
	if !othersDown {
		return false, command.Unavailable("etcd has no majority and the other members may still be running")
	}
	for _, unit := range []string{patroniUnit, poolerUnit} {
		_ = e.supervisor.Do("stop", unit)
	}
	if _, err := e.write(nodeFile{path: confDir + "/etcd-force.env", content: "ETCD_FORCE_NEW_CLUSTER=true\n", mode: 0o640}); err != nil {
		return false, err
	}
	if err := e.supervisor.Do("restart", etcdUnit); err != nil {
		return false, err
	}
	waitFor(ctx, 2*time.Minute, 3*time.Second, e.etcdHealthy(ctx))
	_ = os.Remove(e.path(confDir + "/etcd-force.env"))
	if err := e.supervisor.Do("restart", etcdUnit); err != nil {
		return false, err
	}
	patroni := hostsLine.ReplaceAllString(e.readFile("conf/patroni.yml"), "  hosts: '"+self+":"+etcdClientPort+"'")
	_, err := e.write(nodeFile{path: confDir + "/patroni.yml", content: patroni, mode: 0o600})

	return true, err
}

func (e Engine) etcdHealthy(ctx context.Context) func() bool {
	return func() bool {
		_, err := e.etcd(ctx, localEtcd, "endpoint", "health")

		return err == nil
	}
}

func (e Engine) clusterLeader(ctx context.Context) string {
	var cluster struct {
		Members []struct {
			Name  string `json:"name"`
			Role  string `json:"role"`
			State string `json:"state"`
		} `json:"members"`
	}
	if err := e.patroniGet(ctx, "/cluster", &cluster); err != nil {
		return ""
	}
	for _, member := range cluster.Members {
		if member.Role == "leader" && member.State == "running" {
			return member.Name
		}
	}

	return ""
}

func (e Engine) ownIP() string {
	lines := strings.Split(e.readFile("conf/patroni.yml"), "\n")
	if name := patroniValueIn(lines, "name"); name != "" {
		return name
	}

	return os.Getenv("DATABASIFY_IP")
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}

	return err.Error()
}

func (e cluster) Rejoin(ctx context.Context, _ requests.ClusterRejoin, report contract.Report) error {
	report("starting Patroni")

	return nodes(e).Wake(ctx, requests.NodeWake{All: true})
}

func (e cluster) UpdateMembership(_ context.Context, request requests.ClusterMembership) error {
	if !e.clustered() {
		return nil
	}
	patroni := hostsLine.ReplaceAllString(e.readFile("conf/patroni.yml"), "  hosts: '"+etcdHostsOf(request.Members)+"'")
	if _, err := e.write(nodeFile{path: confDir + "/patroni.yml", content: patroni, mode: 0o600}); err != nil {
		return err
	}

	return e.supervisor.Do("reload", patroniUnit)
}

func (e cluster) Watch(context.Context) error { return contract.ErrUnsupported }
