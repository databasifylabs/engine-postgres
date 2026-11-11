package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/responses"
)

func (e accounts) List(ctx context.Context) (responses.AccountList, error) {
	reserved := e.Capabilities()
	users, err := e.namesFrom(ctx, "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_'", reserved.ReservedUsers)
	if err != nil {
		return responses.AccountList{}, err
	}
	databases, err := e.namesFrom(ctx, "SELECT datname FROM pg_database WHERE NOT datistemplate", reserved.ReservedDatabases)
	if err != nil {
		return responses.AccountList{}, err
	}

	grants, err := e.grantsOn(ctx, databases)
	if err != nil {
		return responses.AccountList{}, err
	}

	return responses.AccountList{Users: users, Databases: databases, Grants: grants}, nil
}

const grantsScript = `SELECT r.rolname, bool_or(a.privilege_type = 'INSERT') FROM pg_default_acl d CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles r ON r.oid = a.grantee WHERE d.defaclobjtype = 'r' GROUP BY r.rolname ORDER BY r.rolname`

func (e Engine) grantsOn(ctx context.Context, databases []string) (map[string][]dtos.Grant, error) {
	grants := map[string][]dtos.Grant{}
	for _, database := range databases {
		rows, err := e.psql.Query(ctx, database, grantsScript, nil)
		if err != nil {
			return nil, err
		}
		for _, row := range nonEmptyLines(rows) {
			user, writes, _ := strings.Cut(row, "|")
			access := enums.AccessRead
			if writes == "t" {
				access = enums.AccessWrite
			}
			grants[user] = append(grants[user], dtos.Grant{Database: database, Access: access})
		}
	}

	return grants, nil
}

func (e Engine) namesFrom(ctx context.Context, query string, reserved []string) ([]string, error) {
	reply, err := e.psql.Query(ctx, defaultDatabase, query, nil)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range nonEmptyLines(reply) {
		if !slices.Contains(reserved, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	return names, nil
}

func nonEmptyLines(reply string) []string {
	var lines []string
	for _, line := range strings.Split(reply, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
