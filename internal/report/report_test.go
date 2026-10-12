package report

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func TestCSV(t *testing.T) {
	rep := Build([]rules.Result{
		{Kind: "Deployment", Namespace: "shop", Name: "web, v2", Replicas: 3, CPUMilli: 1500,
			Score: 85, Verdict: rules.Caveats, Findings: []rules.Finding{{Rule: "SR006"}, {Rule: "SR009"}}},
		{Kind: "StatefulSet", Namespace: "shop", Name: "db", Replicas: 1, CPUMilli: 2000,
			OnSpot: true, Score: 15, Verdict: rules.NotReady},
	}, 0)
	var buf bytes.Buffer
	if err := CSV(&buf, rep); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(rows) != 3 || len(rows[0]) != 9 {
		t.Fatalf("want header plus 2 rows of 9 columns, got %d rows", len(rows))
	}
	got := strings.Join(rows[1], "|")
	want := "ready-with-caveats|85|shop|Deployment|web, v2|3|1500|false|SR006;SR009"
	if got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
	if rows[2][7] != "true" || rows[2][8] != "" {
		t.Fatalf("second row = %v", rows[2])
	}
}
