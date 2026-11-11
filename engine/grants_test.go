package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
)

func grantEngine() (Engine, *recordingPsql) {
	return primaryWith(map[string]string{
		"SELECT EXISTS (SELECT FROM pg_roles": "t\n",
		"SELECT datname FROM pg_database":     "postgres\nshop\naudit\n",
		"pg_get_userbyid":                     "owner_role\n",
	})
}

func grantFor(t *testing.T, engine Engine, access enums.Access) {
	t.Helper()
	request := requests.UserGrant{Name: "alice", Grants: []dtos.Grant{{Database: "shop", Access: access}}}
	if err := engine.Accounts().Grant(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func callsIn(psql *recordingPsql, database string, fragment string) []int {
	var found []int
	for index, c := range psql.calls {
		if c.database == database && strings.Contains(c.script, fragment) {
			found = append(found, index)
		}
	}

	return found
}

func TestGrantReadIssuesSelectAndDefaultPrivileges(t *testing.T) {
	engine, psql := grantEngine()

	grantFor(t, engine, enums.AccessRead)

	grants := callsIn(psql, "shop", "GRANT SELECT ON ALL TABLES IN SCHEMA public")
	if len(grants) != 1 || !strings.Contains(psql.calls[grants[0]].script, `ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT ON TABLES`) {
		t.Fatalf("scripts = %q", psql.scripts())
	}
	if psql.calls[grants[0]].vars["owner"] != "owner_role" || len(callsIn(psql, "shop", "GRANT ALL ON ALL TABLES")) != 0 {
		t.Fatalf("call = %#v", psql.calls[grants[0]])
	}
}

func TestGrantRevokesBeforeGranting(t *testing.T) {
	engine, psql := grantEngine()

	grantFor(t, engine, enums.AccessWrite)

	revokes, grants := callsIn(psql, "shop", "REVOKE ALL ON ALL TABLES"), callsIn(psql, "shop", "GRANT ALL ON ALL TABLES")
	if len(revokes) != 1 || len(grants) != 1 || revokes[0] > grants[0] {
		t.Fatalf("revokes = %v, grants = %v", revokes, grants)
	}
}

func TestGrantRevokesUnlistedDatabases(t *testing.T) {
	engine, psql := grantEngine()

	grantFor(t, engine, enums.AccessRead)

	if len(callsIn(psql, "audit", "REVOKE ALL ON ALL TABLES")) != 1 || len(callsIn(psql, "audit", "GRANT")) != 0 || len(callsIn(psql, "postgres", "REVOKE ALL ON ALL TABLES")) != 0 {
		t.Fatalf("scripts = %q", psql.scripts())
	}
}

func TestAccountListReportsGrants(t *testing.T) {
	psql := &recordingPsql{replies: map[string]string{
		"pg_default_acl":                  "alice|f\nbob|t\n",
		"SELECT rolname FROM pg_roles":    "alice\nbob\n",
		"SELECT datname FROM pg_database": "shop\n",
	}}

	accounts, err := Engine{psql: psql}.Accounts().List(context.Background())

	want := []dtos.Grant{{Database: "shop", Access: enums.AccessRead}}
	if err != nil || !slices.Equal(accounts.Grants["alice"], want) || accounts.Grants["bob"][0].Access != enums.AccessWrite {
		t.Fatalf("grants = %+v, err = %v", accounts.Grants, err)
	}
}
