package postgres

import (
	"reflect"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
)

func TestCapabilitiesListThePostgresPorts(t *testing.T) {
	pool := 25061
	want := dtos.Ports{Client: 25060, Pool: &pool, Peers: []int{8008, 2379, 2380}}
	if got := (Engine{}).Capabilities().Ports; !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %+v", got)
	}
}
