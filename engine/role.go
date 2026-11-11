package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/ctl"
	"github.com/databasifylabs/engine-sdk/enums"
)

const (
	patroniUnit    = "databasify-patroni"
	patroniTimeout = 5 * time.Second
)

type unitProbe interface {
	IsActive(unit string) (bool, error)
}

type supervisorUnits struct {
	socket string
}

func (u supervisorUnits) IsActive(unit string) (bool, error) {
	response, err := ctl.Send(u.socket, ctl.Request{Verb: "is-active", Quiet: true, Units: []string{unit}})
	if err != nil {
		return false, err
	}

	return response.Exit == 0, nil
}

func (e Engine) requirePrimary(ctx context.Context) error {
	inRecovery, err := e.inRecovery(ctx)
	if err != nil {
		return err
	}
	if inRecovery {
		return command.NotPrimary("this node is a replica; run the command on the primary")
	}

	return nil
}

func (e Engine) role(ctx context.Context) (enums.NodeRole, error) {
	if active, _ := e.units.IsActive(patroniUnit); active {
		return e.patroniRole(ctx), nil
	}

	return e.standaloneRole(ctx)
}

func (e Engine) standaloneRole(ctx context.Context) (enums.NodeRole, error) {
	inRecovery, err := e.inRecovery(ctx)
	if isUnavailable(err) {
		return enums.NodeRoleOffline, nil
	}
	if err != nil {
		return "", err
	}
	if inRecovery {
		return enums.NodeRoleReplica, nil
	}

	return enums.NodeRolePrimary, nil
}

func (e Engine) inRecovery(ctx context.Context) (bool, error) {
	reply, err := e.psql.Query(ctx, defaultDatabase, "SELECT pg_is_in_recovery()", nil)
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(reply) == "t", nil
}

func isUnavailable(err error) bool {
	var failure command.Failure

	return errors.As(err, &failure) && failure.Code == enums.ErrorCodeUnavailable
}

type patroniStatus struct {
	Role           string `json:"role"`
	State          string `json:"state"`
	PendingRestart bool   `json:"pending_restart"`
}

func (e Engine) patroniRole(ctx context.Context) enums.NodeRole {
	state := e.patroniState(ctx)
	if state.Role == "" {
		return enums.NodeRoleOffline
	}

	return roleNamed(state.Role)
}

func settled(state patroniStatus) bool {
	running := state.State == "running" || state.State == "streaming"
	role := roleNamed(state.Role)

	return running && (role == enums.NodeRolePrimary || role == enums.NodeRoleReplica)
}

func (e Engine) leading(ctx context.Context) bool {
	state := e.patroniState(ctx)

	return roleNamed(state.Role) == enums.NodeRolePrimary && state.State == "running"
}

func (e Engine) patroniState(ctx context.Context) patroniStatus {
	var state patroniStatus
	_ = e.patroniGet(ctx, "/patroni", &state)

	return state
}

func (e Engine) patroniGet(ctx context.Context, path string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, patroniTimeout)
	defer cancel()
	base := e.settings.PatroniURL
	if base == "" {
		base = "http://127.0.0.1:" + patroniPort
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	return json.NewDecoder(response.Body).Decode(into)
}

func roleNamed(name string) enums.NodeRole {
	switch name {
	case "master", "primary":
		return enums.NodeRolePrimary
	case "replica", "standby_leader":
		return enums.NodeRoleReplica
	}

	return enums.NodeRoleUnknown
}
