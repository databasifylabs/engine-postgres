package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
	"github.com/databasifylabs/engine-sdk/settings"
)

const (
	requestState      = confDir + "/request.json"
	tunedState        = confDir + "/tuned.json"
	liveSettingsQuery = `SELECT lower(s.name), CASE WHEN s.source = 'client' THEN COALESCE((SELECT f.setting FROM pg_file_settings f WHERE lower(f.name) = lower(s.name) AND f.applied ORDER BY f.seqno DESC LIMIT 1), s.boot_val) ELSE current_setting(s.name) END, s.pending_restart FROM pg_settings s WHERE lower(s.name) IN (%s) OR s.pending_restart ORDER BY 1`
)

type config struct{ Engine }

func (e Engine) Config() contract.Config { return config{e} }

func (e Engine) catalog() settings.Catalog {
	return catalogFor(os.Getenv("DATABASIFY_LINE"))
}

func (e Engine) savedRequest() (requests.NodeConfigure, error) {
	var request requests.NodeConfigure
	data, err := os.ReadFile(e.path(requestState))
	if err != nil {
		return request, command.Failure{Code: enums.ErrorCodeBusy, Message: "run node configure first"}
	}

	return request, json.Unmarshal(data, &request)
}

func (e Engine) withTunedSettings(request requests.NodeConfigure) (requests.NodeConfigure, error) {
	catalog := e.catalog()
	if err := catalog.Check(request.Settings); err != nil {
		return request, err
	}
	if err := e.ensureDirectory(e.path(confDir)); err != nil {
		return request, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return request, err
	}
	if _, err := e.write(nodeFile{path: requestState, content: string(encoded), mode: 0o600}); err != nil {
		return request, err
	}
	tuned, err := catalog.Tuned(e.path(tunedState), request.Self())
	if err != nil {
		return request, err
	}
	request.Settings = settings.Effective(tuned, request.Settings)

	return request, nil
}

func (e config) Fields(context.Context) (responses.ConfigFields, error) {
	request, err := e.savedRequest()
	if err != nil {
		return responses.ConfigFields{Fields: e.catalog().Fields}, nil
	}
	tuned, err := e.catalog().Tuned(e.path(tunedState), request.Self())

	return responses.ConfigFields{Fields: e.catalog().FieldsWith(tuned)}, err
}

func (e config) Show(ctx context.Context) (responses.ConfigShow, error) {
	request, err := e.savedRequest()
	if err != nil {
		return responses.ConfigShow{}, err
	}
	tuned, err := e.catalog().Tuned(e.path(tunedState), request.Self())
	if err != nil {
		return responses.ConfigShow{}, err
	}
	keys := make([]string, 0, len(e.catalog().Fields))
	for _, field := range e.catalog().Fields {
		keys = append(keys, "'"+field.Key+"'")
	}
	rows, err := e.psql.Query(ctx, defaultDatabase, strings.Replace(liveSettingsQuery, "%s", strings.Join(keys, ","), 1), nil)
	if err != nil {
		return responses.ConfigShow{}, err
	}
	values := []dtos.ConfigValue{}
	for _, row := range nonEmptyLines(rows) {
		fields := strings.SplitN(row, "|", 3)
		if len(fields) != 3 {
			continue
		}
		values = append(values, dtos.ConfigValue{Key: fields[0], Value: fields[1], Source: e.catalog().SourceOf(fields[0], tuned, request.Settings), PendingRestart: fields[2] == "t"})
	}

	return responses.ConfigShow{Values: values}, nil
}

func (e config) Tune(ctx context.Context) (responses.ConfigTune, error) {
	request, err := e.savedRequest()
	if err != nil {
		return responses.ConfigTune{}, err
	}
	previous, err := e.catalog().Tuned(e.path(tunedState), request.Self())
	if err != nil {
		return responses.ConfigTune{}, err
	}
	tuned, err := e.catalog().Retune(e.path(tunedState), request.Self())
	if err != nil {
		return responses.ConfigTune{}, err
	}
	if _, err := e.Nodes().Configure(ctx, request); err != nil {
		return responses.ConfigTune{}, err
	}
	if _, err := e.Nodes().SyncConfig(ctx); err != nil {
		return responses.ConfigTune{}, err
	}
	if err := e.Nodes().ApplyConfig(ctx); err != nil {
		return responses.ConfigTune{}, err
	}

	return responses.ConfigTune{Tuned: tuned, Restart: e.catalog().RestartChanged(previous, tuned, request.Settings)}, nil
}
