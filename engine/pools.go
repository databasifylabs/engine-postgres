package postgres

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	poolsState = confDir + "/pools.json"
	poolsIni   = confDir + "/pgbouncer-pools.ini"
)

var reservedPools = []string{"postgres", "pgbouncer"}

type pools struct{ Engine }

func (e Engine) Pools() contract.Pools { return pools{e} }

func poolsIniOf(saved []dtos.Pool) string {
	var lines strings.Builder
	for _, pool := range saved {
		lines.WriteString(pool.Name + " = host=127.0.0.1 port=" + clientPortNumber + " dbname=" + pool.Database + " pool_size=" + strconv.FormatInt(pool.Size, 10) + " pool_mode=" + pool.Mode + "\n")
	}

	return lines.String()
}

func (e Engine) savedPools() []dtos.Pool {
	saved := []dtos.Pool{}
	if data, err := os.ReadFile(e.path(poolsState)); err == nil {
		_ = json.Unmarshal(data, &saved)
	}

	return saved
}

func (e Engine) ensurePoolsFile() error {
	if _, err := os.Stat(e.path(poolsIni)); err == nil {
		return nil
	}
	_, err := e.write(nodeFile{path: poolsIni, content: poolsIniOf(e.savedPools()), mode: 0o640})

	return err
}

func (e pools) List(context.Context) (responses.PoolList, error) {
	return responses.PoolList{Pools: e.savedPools()}, nil
}

func (e pools) Create(ctx context.Context, request requests.PoolCreate) error {
	saved := e.savedPools()
	if slices.Contains(reservedPools, request.Name) {
		return command.Invalid(request.Name + " is reserved")
	}
	if indexOfPool(saved, request.Name) >= 0 {
		return command.Failure{Code: enums.ErrorCodeAlreadyExists, Message: "pool " + request.Name + " already exists"}
	}
	if err := e.requireDatabase(ctx, request.Database); err != nil {
		return err
	}

	return e.savePools(append(saved, dtos.Pool(request)))
}

func (e pools) Update(ctx context.Context, request requests.PoolUpdate) error {
	saved := e.savedPools()
	index := indexOfPool(saved, request.Name)
	if index < 0 {
		return command.NotFound("pool " + request.Name + " does not exist")
	}
	if err := e.requireDatabase(ctx, request.Database); err != nil {
		return err
	}
	saved[index] = dtos.Pool(request)

	return e.savePools(saved)
}

func (e pools) Delete(_ context.Context, request requests.PoolDelete) error {
	saved := e.savedPools()
	index := indexOfPool(saved, request.Name)
	if index < 0 {
		return command.NotFound("pool " + request.Name + " does not exist")
	}

	return e.savePools(slices.Delete(saved, index, index+1))
}

func indexOfPool(saved []dtos.Pool, name string) int {
	return slices.IndexFunc(saved, func(pool dtos.Pool) bool { return pool.Name == name })
}

func (e Engine) requireDatabase(ctx context.Context, database string) error {
	databases, err := e.Accounts().List(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(databases.Databases, database) {
		return command.NotFound("database " + database + " does not exist")
	}

	return nil
}

func (e Engine) savePools(saved []dtos.Pool) error {
	encoded, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if _, err := e.write(nodeFile{path: poolsState, content: string(encoded), mode: 0o600}); err != nil {
		return err
	}
	if _, err := e.write(nodeFile{path: poolsIni, content: poolsIniOf(saved), mode: 0o640}); err != nil {
		return err
	}
	if !e.supervisor.Active(poolerUnit) {
		return nil
	}

	return e.supervisor.Kill("HUP", poolerUnit)
}
