package postgres

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/ctl"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

type recordingSupervisor struct{ calls *[]string }

func (s recordingSupervisor) Do(verb string, units ...string) error {
	*s.calls = append(*s.calls, verb+" "+strings.Join(units, " "))

	return nil
}

func (s recordingSupervisor) DoNoBlock(verb string, units ...string) error {
	return s.Do(verb, units...)
}

func (recordingSupervisor) Active(string) bool { return true }

func (recordingSupervisor) Property(string, string) string { return "" }

func (recordingSupervisor) Run(ctl.Transient) error { return nil }

func (s recordingSupervisor) Kill(signal string, units ...string) error {
	return s.Do("kill "+signal, units...)
}

func TestMembershipRewritesPatroniEtcdHosts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "patroni.yml"), []byte("etcd3:\n  hosts: '10.0.0.1:2379'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	engine := Engine{root: root, supervisor: recordingSupervisor{calls: &calls}}
	members := []dtos.Member{{Ip: "10.0.0.1", Position: 1, Voting: true}, {Ip: "10.0.0.2", Position: 2, Voting: true}}

	if err := engine.Cluster().UpdateMembership(context.Background(), requests.ClusterMembership{Members: members}); err != nil {
		t.Fatal(err)
	}

	written, _ := os.ReadFile(filepath.Join(root, "conf", "patroni.yml"))
	if !strings.Contains(string(written), "  hosts: '10.0.0.1:2379,10.0.0.2:2379'\n") || !slices.Contains(calls, "reload "+patroniUnit) {
		t.Fatalf("patroni.yml = %q, calls = %v", written, calls)
	}
}

func TestMigrationStartNeedsAnOwner(t *testing.T) {
	err := Engine{}.Migration().Start(context.Background(), requests.MigrationStart{})

	if failureCode(t, err) != enums.ErrorCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestContinuousMigrationNeedsAPublication(t *testing.T) {
	owner := "app"

	err := Engine{}.Migration().Start(context.Background(), requests.MigrationStart{Owner: &owner, Continuous: true})

	if failureCode(t, err) != enums.ErrorCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestOneShotCopyNeedsNoPublication(t *testing.T) {
	var calls []string
	owner := "app"
	engine := Engine{root: t.TempDir(), supervisor: recordingSupervisor{calls: &calls}}

	err := engine.Migration().Start(context.Background(), requests.MigrationStart{Owner: &owner, Databases: []string{"shop"}})

	if err != nil || !slices.Contains(calls, "stop "+migrationUnit) {
		t.Fatalf("err = %v, calls = %v", err, calls)
	}
}

func TestPeerEtcdEndpointsSkipThisNode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "patroni.yml"), []byte("etcd3:\n  hosts: '10.0.0.1:2379,10.0.0.2:2379,10.0.0.3:2379'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	endpoints := Engine{root: root}.peerEtcdEndpoints("10.0.0.2")

	if !slices.Equal(endpoints, []string{"http://10.0.0.1:2379", "http://10.0.0.3:2379"}) {
		t.Fatalf("endpoints = %v", endpoints)
	}
	if asked := (Engine{root: root}).membershipEndpoints("10.0.0.2"); !slices.Equal(asked, []string{localEtcd, "http://10.0.0.1:2379", "http://10.0.0.3:2379"}) {
		t.Fatalf("a node asks its own etcd first, got %v", asked)
	}
}

func TestInitialClusterComesFromTheLiveMemberList(t *testing.T) {
	list := "48464d3ad43e335a, started, 10.104.0.7, http://10.104.0.7:2380, http://10.104.0.7:2379, true\n" +
		"d942d1c05fd7275b, started, 10.104.0.6, http://10.104.0.6:2380, http://10.104.0.6:2379, false\n" +
		"f64cae8395389650, unstarted, , http://10.104.0.2:2380, , true\n"
	awk := exec.Command("awk", "-F", ", ", "-v", "peer=http://10.104.0.2:2380", "-v", "self=10.104.0.2", initialClusterAwk)
	awk.Stdin = strings.NewReader(list)

	out, err := awk.Output()

	want := "10.104.0.7=http://10.104.0.7:2380,10.104.0.6=http://10.104.0.6:2380,10.104.0.2=http://10.104.0.2:2380"
	if err != nil || string(out) != want {
		t.Fatalf("cluster = %q, err = %v", out, err)
	}
}
