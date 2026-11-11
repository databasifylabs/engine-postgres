package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInsightsParsesTopStatements(t *testing.T) {
	psql := scriptPsql{replies: map[string]string{"pg_stat_statements": "12|340|28|SELECT * FROM items WHERE id = $1\n3|9|3|UPDATE items SET name = $1 | x\n"}}

	insights, err := Engine{psql: psql}.Nodes().Insights(context.Background())

	if err != nil || len(insights.Statements) != 2 || insights.Statements[0].Calls != 12 || insights.Statements[0].TotalMs != 340 || insights.Statements[1].Text != "UPDATE items SET name = $1 | x" || insights.Reason != nil {
		t.Fatalf("insights = %+v, err = %v", insights, err)
	}
}

func TestInsightsWithoutTheExtensionGivesAReason(t *testing.T) {
	psql := scriptPsql{err: errors.New(`ERROR: pg_stat_statements must be loaded via "shared_preload_libraries"`)}

	insights, err := Engine{psql: psql}.Nodes().Insights(context.Background())

	if err != nil || len(insights.Statements) != 0 || insights.Reason == nil || *insights.Reason != restartForStatistics {
		t.Fatalf("insights = %+v, err = %v", insights, err)
	}
}

func TestConfigurePreloadsStatStatements(t *testing.T) {
	standalone := renderedFiles(t, configureRequest(false, false))["postgresql.conf"]
	clustered := renderedFiles(t, configureRequest(true, false))["patroni.yml"]

	if !strings.Contains(standalone, "shared_preload_libraries = 'pg_stat_statements'") {
		t.Fatalf("postgresql.conf lacks pg_stat_statements:\n%s", standalone)
	}
	if !strings.Contains(clustered, "        shared_preload_libraries: 'pg_stat_statements'\n") {
		t.Fatalf("patroni.yml lacks pg_stat_statements in the cluster-wide parameters:\n%s", clustered)
	}
}
