package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	createDatabaseScript = `SELECT NOT EXISTS (SELECT FROM pg_database WHERE datname = :'name') AS created \gset
\if :created
CREATE DATABASE :"name";
\endif
SELECT :'created'`

	dropDatabaseScript = `SELECT EXISTS (SELECT FROM pg_database WHERE datname = :'name') AS dropped \gset
DROP DATABASE IF EXISTS :"name" WITH (FORCE);
SELECT :'dropped'`
)

func (e Engine) refuseReserved(reserved []string, names ...string) error {
	for _, name := range names {
		if slices.Contains(reserved, name) {
			return reservedNameError(name)
		}
	}

	return nil
}

func reservedNameError(name string) error {
	return command.Invalid(name + " is reserved")
}

func (e Engine) flag(ctx context.Context, script string, vars map[string]string) (bool, error) {
	reply, err := e.psql.Query(ctx, defaultDatabase, script, vars)
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(reply) == "t", nil
}

func (e accounts) CreateDatabase(ctx context.Context, request requests.DatabaseCreate) (responses.DatabaseCreate, error) {
	if err := e.requirePrimary(ctx); err != nil {
		return responses.DatabaseCreate{}, err
	}
	if err := e.refuseReserved(e.Capabilities().ReservedDatabases, request.Name); err != nil {
		return responses.DatabaseCreate{}, err
	}
	created, err := e.flag(ctx, createDatabaseScript, map[string]string{"name": request.Name})
	if err != nil {
		return responses.DatabaseCreate{}, err
	}

	return responses.DatabaseCreate{Created: created}, nil
}

func (e accounts) DropDatabase(ctx context.Context, request requests.DatabaseDrop) (responses.DatabaseDrop, error) {
	if err := e.requirePrimary(ctx); err != nil {
		return responses.DatabaseDrop{}, err
	}
	if err := e.refuseReserved(e.Capabilities().ReservedDatabases, request.Name); err != nil {
		return responses.DatabaseDrop{}, err
	}
	dropped, err := e.flag(ctx, dropDatabaseScript, map[string]string{"name": request.Name})
	if err != nil {
		return responses.DatabaseDrop{}, err
	}

	return responses.DatabaseDrop{Dropped: dropped}, nil
}
