package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	createStatementsExtension = `CREATE EXTENSION IF NOT EXISTS pg_stat_statements`

	topStatementsScript = `SELECT calls, round(total_exec_time)::bigint, round(mean_exec_time)::bigint, regexp_replace(query, '\s+', ' ', 'g') FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 50`

	notPreloaded = "must be loaded via"

	restartForStatistics = "restart the node to turn on query statistics"
)

func (e nodes) Insights(ctx context.Context) (responses.QueryInsights, error) {
	rows, err := e.psql.Query(ctx, defaultDatabase, topStatementsScript, nil)
	if err != nil && (strings.Contains(err.Error(), notPreloaded) || strings.Contains(err.Error(), "does not exist")) {
		reason := restartForStatistics

		return responses.QueryInsights{Statements: []dtos.Statement{}, Reason: &reason}, nil
	}
	if err != nil {
		return responses.QueryInsights{}, err
	}

	return responses.QueryInsights{Statements: statementsIn(rows)}, nil
}

func statementsIn(rows string) []dtos.Statement {
	statements := []dtos.Statement{}
	for _, row := range nonEmptyLines(rows) {
		fields := strings.SplitN(row, "|", 4)
		if len(fields) != 4 {
			continue
		}
		calls, _ := strconv.ParseInt(fields[0], 10, 64)
		total, _ := strconv.ParseInt(fields[1], 10, 64)
		mean, _ := strconv.ParseInt(fields[2], 10, 64)
		statements = append(statements, dtos.Statement{Text: fields[3], Calls: calls, TotalMs: total, MeanMs: mean})
	}

	return statements
}
