package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

func TestDatabaseCreateReportsCreated(t *testing.T) {
	for reply, want := range map[string]bool{"t\n": true, "f\n": false} {
		engine, psql := primaryWith(map[string]string{"pg_database": reply})

		got, err := engine.Accounts().CreateDatabase(context.Background(), request[requests.DatabaseCreate](t, []byte(`{"name":"shop"}`)))

		if err != nil || got != (responses.DatabaseCreate{Created: want}) {
			t.Fatalf("reply %q: got %#v err %v", reply, got, err)
		}
		write := psql.writes()[0]
		if write.database != "postgres" || write.vars["name"] != "shop" || len(write.vars) != 1 {
			t.Fatalf("call = %#v", write)
		}
		for _, want := range []string{`:'name'`, `CREATE DATABASE :"name"`, `\gset`, `\if :created`} {
			if !strings.Contains(write.script, want) {
				t.Fatalf("script lacks %q: %s", want, write.script)
			}
		}
	}
}

func TestDatabaseDropReportsDropped(t *testing.T) {
	for reply, want := range map[string]bool{"t\n": true, "f\n": false} {
		engine, psql := primaryWith(map[string]string{"pg_database": reply})

		got, err := engine.Accounts().DropDatabase(context.Background(), request[requests.DatabaseDrop](t, []byte(`{"name":"shop"}`)))

		if err != nil || got != (responses.DatabaseDrop{Dropped: want}) {
			t.Fatalf("reply %q: got %#v err %v", reply, got, err)
		}
		write := psql.writes()[0]
		if !strings.Contains(write.script, `DROP DATABASE IF EXISTS :"name" WITH (FORCE)`) || write.vars["name"] != "shop" {
			t.Fatalf("call = %#v", write)
		}
	}
}

func TestDatabaseCommandsRefuseOnReplica(t *testing.T) {
	handlers := map[string]handler{
		"create": databaseCreate,
		"drop":   databaseDrop,
	}
	for name, handler := range handlers {
		engine, psql := replica()

		_, err := handler(engine, context.Background(), []byte(`{"name":"shop"}`))

		if failureCode(t, err) != enums.ErrorCodeNotPrimary || len(psql.calls) != 1 {
			t.Fatalf("%s: err=%v calls=%d", name, err, len(psql.calls))
		}
	}
}
