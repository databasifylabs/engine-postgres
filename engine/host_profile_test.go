package postgres

import (
	"reflect"
	"testing"

	"github.com/databasifylabs/engine-sdk/contract"
)

func TestHostProfileTunesTheHostForPostgres(t *testing.T) {
	want := contract.HostProfile{Settings: map[string]string{
		"vm.swappiness":                 "1",
		"net.core.somaxconn":            "4096",
		"net.ipv4.tcp_keepalive_time":   "60",
		"net.ipv4.tcp_keepalive_intvl":  "10",
		"net.ipv4.tcp_keepalive_probes": "6",
	}}
	if got := (Engine{}).HostProfile(); !reflect.DeepEqual(got, want) {
		t.Fatalf("host profile = %+v", got)
	}
}
