package postgres

import (
	"testing"

	"github.com/databasifylabs/engine-sdk/contract"
)

func TestPostgresImplementsEveryGroup(t *testing.T) {
	engine := New()
	groups := []any{engine.Nodes(), engine.Accounts(), engine.Cluster(), engine.Archive(), engine.Migration()}
	for _, group := range groups {
		if _, unsupported := group.(contract.Unsupported); unsupported {
			t.Fatalf("postgres leaves %T unsupported", group)
		}
	}
	if engine.Name() != "postgres" || len(engine.Capabilities().ReservedUsers) == 0 {
		t.Fatalf("engine = %s, capabilities = %+v", engine.Name(), engine.Capabilities())
	}
}
