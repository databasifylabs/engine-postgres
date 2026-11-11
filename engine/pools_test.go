package postgres

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

func poolEngine(t *testing.T) (Engine, *[]string) {
	t.Helper()
	calls := []string{}
	psql := scriptPsql{replies: map[string]string{"pg_roles": "app\n", "pg_database": "postgres\nshop\n"}}

	return Engine{psql: psql, root: t.TempDir(), supervisor: recordingSupervisor{calls: &calls}}, &calls
}

func TestPoolsAreSavedAndPgbouncerReloads(t *testing.T) {
	engine, calls := poolEngine(t)
	ctx := context.Background()

	if err := engine.Pools().Create(ctx, requests.PoolCreate{Name: "shop_pool", Database: "shop", Size: 20, Mode: "transaction"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Pools().Update(ctx, requests.PoolUpdate{Name: "shop_pool", Database: "shop", Size: 40, Mode: "session"}); err != nil {
		t.Fatal(err)
	}

	ini, _ := os.ReadFile(engine.path(poolsIni))
	if string(ini) != "shop_pool = host=127.0.0.1 port=25060 dbname=shop pool_size=40 pool_mode=session\n" {
		t.Fatalf("pools ini = %q", ini)
	}
	if !slices.Contains(*calls, "kill HUP "+poolerUnit) {
		t.Fatalf("calls = %v", *calls)
	}
	listed, _ := engine.Pools().List(ctx)
	if len(listed.Pools) != 1 || listed.Pools[0].Size != 40 {
		t.Fatalf("pools = %+v", listed.Pools)
	}
	if err := engine.Pools().Delete(ctx, requests.PoolDelete{Name: "shop_pool"}); err != nil {
		t.Fatal(err)
	}
	if listed, _ := engine.Pools().List(ctx); len(listed.Pools) != 0 {
		t.Fatalf("pools = %+v", listed.Pools)
	}
}

func TestPoolMistakesAreRefused(t *testing.T) {
	engine, _ := poolEngine(t)
	ctx := context.Background()
	_ = engine.Pools().Create(ctx, requests.PoolCreate{Name: "shop_pool", Database: "shop", Size: 20, Mode: "transaction"})

	cases := map[enums.ErrorCode]error{
		enums.ErrorCodeAlreadyExists:  engine.Pools().Create(ctx, requests.PoolCreate{Name: "shop_pool", Database: "shop", Size: 5, Mode: "session"}),
		enums.ErrorCodeInvalidRequest: engine.Pools().Create(ctx, requests.PoolCreate{Name: "pgbouncer", Database: "shop", Size: 5, Mode: "session"}),
		enums.ErrorCodeNotFound:       engine.Pools().Create(ctx, requests.PoolCreate{Name: "other", Database: "missing", Size: 5, Mode: "session"}),
	}
	for want, err := range cases {
		if failureCode(t, err) != want {
			t.Errorf("want %s, got %v", want, err)
		}
	}
	if failureCode(t, engine.Pools().Delete(ctx, requests.PoolDelete{Name: "nope"})) != enums.ErrorCodeNotFound {
		t.Fatal("deleting a missing pool must be not found")
	}
}
