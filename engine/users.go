package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	reservedRolePrefix = "pg_"

	keepStatementsOutOfLogs = `SET log_min_duration_statement = -1; SET log_statement = 'none'; SET log_min_error_statement = panic;
`

	createUserScript = keepStatementsOutOfLogs + `SELECT NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'name') AS created \gset
\if :created
CREATE ROLE :"name" LOGIN;
\endif
ALTER ROLE :"name" LOGIN PASSWORD :'password';
SELECT :'created'`

	roleExistsScript = `SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = :'name')`

	setPasswordScript = keepStatementsOutOfLogs + `ALTER ROLE :"name" PASSWORD :'password'`

	ownerScript = `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = :'db'`

	revokeDatabaseScript = `REVOKE ALL PRIVILEGES ON DATABASE :"db" FROM :"name"`

	revokeObjectsScript = `ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public REVOKE ALL ON TABLES FROM :"name";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public REVOKE ALL ON SEQUENCES FROM :"name";
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM :"name";
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM :"name";
REVOKE ALL ON SCHEMA public FROM :"name"`

	grantReadDatabaseScript = `GRANT CONNECT ON DATABASE :"db" TO :"name"`

	grantReadObjectsScript = `GRANT USAGE ON SCHEMA public TO :"name";
GRANT SELECT ON ALL TABLES IN SCHEMA public TO :"name";
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO :"name";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT ON TABLES TO :"name";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT ON SEQUENCES TO :"name"`

	grantWriteDatabaseScript = `GRANT ALL PRIVILEGES ON DATABASE :"db" TO :"name"`

	grantWriteObjectsScript = `GRANT ALL ON SCHEMA public TO :"name";
GRANT ALL ON ALL TABLES IN SCHEMA public TO :"name";
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO :"name";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT ALL ON TABLES TO :"name";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT ALL ON SEQUENCES TO :"name"`

	grantReplicationScript = `ALTER ROLE :"name" REPLICATION`

	listDatabasesScript = `SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname`

	reassignScript = `REASSIGN OWNED BY :"name" TO postgres;
DROP OWNED BY :"name"`

	dropRoleScript = `DROP ROLE :"name"`
)

func rejectEncryption(encryption *string) error {
	if encryption == nil {
		return nil
	}

	return command.Invalid("PostgreSQL has no password encryption choice")
}

func (e accounts) CreateUser(ctx context.Context, request requests.UserCreate) (responses.UserCreate, error) {
	if err := e.requirePrimary(ctx); err != nil {
		return responses.UserCreate{}, err
	}
	if err := rejectEncryption(request.Encryption); err != nil {
		return responses.UserCreate{}, err
	}
	if err := e.refuseReservedAccount(request.Name, databasesIn(request.Grants)); err != nil {
		return responses.UserCreate{}, err
	}
	if err := e.requireDatabases(ctx, databasesIn(request.Grants)); err != nil {
		return responses.UserCreate{}, err
	}
	created, err := e.flag(ctx, createUserScript, map[string]string{"name": request.Name, "password": request.Password})
	if err != nil {
		return responses.UserCreate{}, err
	}
	if err := e.applyGrants(ctx, request.Name, request.Grants); err != nil {
		return responses.UserCreate{}, err
	}

	return responses.UserCreate{Created: created}, nil
}

func (e accounts) SetPassword(ctx context.Context, request requests.UserSetPassword) error {
	if err := e.requirePrimary(ctx); err != nil {
		return err
	}
	if err := rejectEncryption(request.Encryption); err != nil {
		return err
	}
	if err := e.refuseReservedAccount(request.Name, nil); err != nil {
		return err
	}
	if err := e.requireRole(ctx, request.Name); err != nil {
		return err
	}
	if _, err := e.psql.Query(ctx, defaultDatabase, setPasswordScript, map[string]string{"name": request.Name, "password": request.Password}); err != nil {
		return err
	}

	return nil
}

func (e accounts) Grant(ctx context.Context, request requests.UserGrant) error {
	if err := e.requirePrimary(ctx); err != nil {
		return err
	}
	if err := e.refuseReservedAccount(request.Name, databasesIn(request.Grants)); err != nil {
		return err
	}
	if err := e.requireRole(ctx, request.Name); err != nil {
		return err
	}
	if err := e.requireDatabases(ctx, databasesIn(request.Grants)); err != nil {
		return err
	}
	if err := e.applyGrants(ctx, request.Name, request.Grants); err != nil {
		return err
	}
	if !request.Replication {
		return nil
	}
	if _, err := e.psql.Query(ctx, defaultDatabase, grantReplicationScript, map[string]string{"name": request.Name}); err != nil {
		return err
	}

	return nil
}

func (e accounts) DropUser(ctx context.Context, request requests.UserDrop) (responses.UserDrop, error) {
	if err := e.requirePrimary(ctx); err != nil {
		return responses.UserDrop{}, err
	}
	if err := e.refuseReservedAccount(request.Name, nil); err != nil {
		return responses.UserDrop{}, err
	}
	exists, err := e.roleExists(ctx, request.Name)
	if err != nil || !exists {
		return responses.UserDrop{}, err
	}
	if err := e.releaseOwnedObjects(ctx, request.Name); err != nil {
		return responses.UserDrop{}, err
	}
	if _, err := e.psql.Query(ctx, defaultDatabase, dropRoleScript, map[string]string{"name": request.Name}); err != nil {
		return responses.UserDrop{}, err
	}

	return responses.UserDrop{Dropped: true}, nil
}

func (e Engine) roleExists(ctx context.Context, name string) (bool, error) {
	return e.flag(ctx, roleExistsScript, map[string]string{"name": name})
}

func (e Engine) requireRole(ctx context.Context, name string) error {
	exists, err := e.roleExists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return command.NotFound(fmt.Sprintf("user %q does not exist", name))
	}

	return nil
}

func databasesIn(grants []dtos.Grant) []string {
	databases := make([]string, 0, len(grants))
	for _, grant := range grants {
		databases = append(databases, grant.Database)
	}

	return databases
}

func (e Engine) applyGrants(ctx context.Context, name string, grants []dtos.Grant) error {
	listing, err := e.psql.Query(ctx, defaultDatabase, listDatabasesScript, nil)
	if err != nil {
		return err
	}
	reserved := e.Capabilities().ReservedDatabases
	for _, database := range nonEmptyLines(listing) {
		if slices.Contains(reserved, database) {
			continue
		}
		if err := e.revokeOn(ctx, name, database); err != nil {
			return err
		}
	}
	for _, grant := range grants {
		if err := e.grantOn(ctx, name, grant); err != nil {
			return err
		}
	}

	return nil
}

func (e Engine) revokeOn(ctx context.Context, name string, database string) error {
	vars, err := e.grantVars(ctx, name, database)
	if err != nil {
		return err
	}
	if err := e.inDatabase(ctx, defaultDatabase, revokeDatabaseScript, vars); err != nil {
		return err
	}

	return e.inDatabase(ctx, database, revokeObjectsScript, vars)
}

func (e Engine) grantOn(ctx context.Context, name string, grant dtos.Grant) error {
	vars, err := e.grantVars(ctx, name, grant.Database)
	if err != nil {
		return err
	}
	databaseScript, objectsScript := grantReadDatabaseScript, grantReadObjectsScript
	if grant.Access == enums.AccessWrite {
		databaseScript, objectsScript = grantWriteDatabaseScript, grantWriteObjectsScript
	}
	if err := e.inDatabase(ctx, defaultDatabase, databaseScript, vars); err != nil {
		return err
	}

	return e.inDatabase(ctx, grant.Database, objectsScript, vars)
}

func (e Engine) grantVars(ctx context.Context, name string, database string) (map[string]string, error) {
	owner, err := e.psql.Query(ctx, defaultDatabase, ownerScript, map[string]string{"db": database})
	if err != nil {
		return nil, err
	}

	return map[string]string{"name": name, "db": database, "owner": strings.TrimSpace(owner)}, nil
}

func (e Engine) inDatabase(ctx context.Context, database string, script string, vars map[string]string) error {
	_, err := e.psql.Query(ctx, database, script, vars)
	var missing *MissingDatabaseError
	if errors.As(err, &missing) {
		return command.NotFound(missing.Message)
	}

	return err
}

func (e Engine) requireDatabases(ctx context.Context, databases []string) error {
	listing, err := e.psql.Query(ctx, defaultDatabase, listDatabasesScript, nil)
	if err != nil {
		return err
	}
	existing := nonEmptyLines(listing)
	for _, database := range databases {
		if !slices.Contains(existing, database) {
			return command.NotFound(fmt.Sprintf("database %q does not exist", database))
		}
	}

	return nil
}

func (e Engine) refuseReservedAccount(name string, databases []string) error {
	if strings.HasPrefix(name, reservedRolePrefix) {
		return reservedNameError(name)
	}
	reserved := e.Capabilities()
	if err := e.refuseReserved(reserved.ReservedUsers, name); err != nil {
		return err
	}

	return e.refuseReserved(reserved.ReservedDatabases, databases...)
}

func (e Engine) releaseOwnedObjects(ctx context.Context, name string) error {
	listing, err := e.psql.Query(ctx, defaultDatabase, listDatabasesScript, nil)
	if err != nil {
		return err
	}
	for _, database := range nonEmptyLines(listing) {
		if _, err := e.psql.Query(ctx, database, reassignScript, map[string]string{"name": name}); err != nil {
			return err
		}
	}

	return nil
}
