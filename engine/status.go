package postgres

import (
	"context"
	"strings"

	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/responses"
)

func (e nodes) Status(ctx context.Context) (responses.NodeStatus, error) {
	role, err := e.role(ctx)
	if err != nil {
		return responses.NodeStatus{}, err
	}
	if role == enums.NodeRoleOffline {
		return offlineStatus(), nil
	}
	version, err := e.serverVersion(ctx)
	if isUnavailable(err) {
		return offlineStatus(), nil
	}
	if err != nil {
		return responses.NodeStatus{}, err
	}

	return responses.NodeStatus{Role: role, Version: &version}, nil
}

func offlineStatus() responses.NodeStatus {
	return responses.NodeStatus{Role: enums.NodeRoleOffline}
}

func (e Engine) serverVersion(ctx context.Context) (string, error) {
	reply, err := e.psql.Query(ctx, defaultDatabase, "SHOW server_version", nil)
	if err != nil {
		return "", err
	}
	version, _, _ := strings.Cut(strings.TrimSpace(reply), " ")

	return version, nil
}
