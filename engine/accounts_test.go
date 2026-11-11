package postgres

import (
	"context"
	"slices"
	"testing"

	"github.com/databasifylabs/engine-sdk/responses"
)

func accountsOf(t *testing.T, engine Engine) responses.AccountList {
	t.Helper()
	reply, err := engine.Accounts().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestAccountListExcludesReservedAndPgRoles(t *testing.T) {
	psql := scriptPsql{replies: map[string]string{
		"pg_roles":    "postgres\nreplicator\nalice\npgbouncer\npatroni\nadmin\n",
		"pg_database": "postgres\ntemplate0\nshop\n",
	}}

	accounts := accountsOf(t, Engine{psql: psql})

	if !slices.Equal(accounts.Users, []string{"alice"}) {
		t.Fatalf("users = %v", accounts.Users)
	}
	if !slices.Equal(accounts.Databases, []string{"shop"}) {
		t.Fatalf("databases = %v", accounts.Databases)
	}
}

func TestAccountListIsSorted(t *testing.T) {
	psql := scriptPsql{replies: map[string]string{
		"pg_roles":    "zed\nalice\nmike\n",
		"pg_database": "shop\naudit\n",
	}}

	accounts := accountsOf(t, Engine{psql: psql})

	if !slices.Equal(accounts.Users, []string{"alice", "mike", "zed"}) {
		t.Fatalf("users = %v", accounts.Users)
	}
	if !slices.Equal(accounts.Databases, []string{"audit", "shop"}) {
		t.Fatalf("databases = %v", accounts.Databases)
	}
}

func TestAccountListIsEmptyNotNull(t *testing.T) {
	psql := scriptPsql{replies: map[string]string{"pg_roles": "", "pg_database": ""}}

	accounts := accountsOf(t, Engine{psql: psql})

	if accounts.Users == nil || accounts.Databases == nil {
		t.Fatalf("accounts = %#v", accounts)
	}
}
