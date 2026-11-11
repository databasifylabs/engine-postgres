package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
	"github.com/databasifylabs/engine-sdk/support"
)

type call struct {
	method   string
	database string
	script   string
	vars     map[string]string
}

type recordingPsql struct {
	replies map[string]string
	err     error
	calls   []call
}

func (r *recordingPsql) Query(_ context.Context, database string, script string, vars map[string]string) (string, error) {
	return r.answer(call{method: "Query", database: database, script: script, vars: vars})
}

func (r *recordingPsql) answer(c call) (string, error) {
	r.calls = append(r.calls, c)
	if r.err != nil {
		return "", r.err
	}
	for fragment, reply := range r.replies {
		if strings.Contains(c.script, fragment) {
			return reply, nil
		}
	}

	return "", nil
}

func (r *recordingPsql) scripts() []string {
	var scripts []string
	for _, c := range r.calls {
		scripts = append(scripts, c.script)
	}

	return scripts
}

func (r *recordingPsql) writes() []call {
	var writes []call
	for _, c := range r.calls {
		if !strings.Contains(c.script, "pg_is_in_recovery") {
			writes = append(writes, c)
		}
	}

	return writes
}

func primaryWith(replies map[string]string) (Engine, *recordingPsql) {
	merged := map[string]string{"pg_is_in_recovery": "f\n"}
	for fragment, reply := range replies {
		merged[fragment] = reply
	}
	psql := &recordingPsql{replies: merged}

	return Engine{psql: psql, units: fakeUnits{}}, psql
}

func replica() (Engine, *recordingPsql) {
	psql := &recordingPsql{replies: map[string]string{"pg_is_in_recovery": "t\n"}}

	return Engine{psql: psql, units: fakeUnits{}}, psql
}

func failureCode(t *testing.T, err error) enums.ErrorCode {
	t.Helper()
	var failure command.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a command.Failure", err)
	}

	return failure.Code
}

func request[T any](t *testing.T, input []byte) T {
	t.Helper()
	value, err := support.DecodeStrict[T](bytes.NewReader(input))
	if err != nil {
		t.Fatalf("bad test request %s: %v", input, err)
	}

	return value
}

type handler func(Engine, context.Context, []byte) (any, error)

func returning[Req, Rep any](method func(Engine) func(context.Context, Req) (Rep, error)) handler {
	return func(engine Engine, ctx context.Context, input []byte) (any, error) {
		var request Req
		if err := json.Unmarshal(input, &request); err != nil {
			return nil, err
		}

		return method(engine)(ctx, request)
	}
}

func acting[Req any](method func(Engine) func(context.Context, Req) error) handler {
	return returning(func(engine Engine) func(context.Context, Req) (dtos.Empty, error) {
		return func(ctx context.Context, request Req) (dtos.Empty, error) {
			return dtos.Empty{}, method(engine)(ctx, request)
		}
	})
}

var (
	databaseCreate = returning(func(e Engine) func(context.Context, requests.DatabaseCreate) (responses.DatabaseCreate, error) {
		return e.Accounts().CreateDatabase
	})
	databaseDrop = returning(func(e Engine) func(context.Context, requests.DatabaseDrop) (responses.DatabaseDrop, error) {
		return e.Accounts().DropDatabase
	})
	userCreate = returning(func(e Engine) func(context.Context, requests.UserCreate) (responses.UserCreate, error) {
		return e.Accounts().CreateUser
	})
	userDrop = returning(func(e Engine) func(context.Context, requests.UserDrop) (responses.UserDrop, error) {
		return e.Accounts().DropUser
	})
	userSetPassword = acting(func(e Engine) func(context.Context, requests.UserSetPassword) error {
		return e.Accounts().SetPassword
	})
	userGrant = acting(func(e Engine) func(context.Context, requests.UserGrant) error { return e.Accounts().Grant })
)
