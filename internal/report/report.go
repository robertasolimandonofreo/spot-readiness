// Package report renders analysis results as a table or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

const calculatorURL = "https://www.robertasoliman.com/ferramentas/calculadora-economia-kubernetes"

type Summary struct {
	Workloads    int   `json:"workloads"`
	Ready        int   `json:"ready"`
	Caveats      int   `json:"readyWithCaveats"`
	NotReady     int   `json:"notReady"`
	Skipped      int   `json:"skipped"`
	CPUMilli     int64 `json:"cpuRequestMillis"`
	PotentialCPU int64 `json:"spotPotentialCpuMillis"`
	// PotentialPercent is the share of requested CPU in workloads that are ready or ready with
	// caveats. It is an upper bound, not a promise: see the README.
	PotentialPercent int    `json:"spotPotentialPercent"`
	CalculatorURL    string `json:"calculatorUrl,omitempty"`
}

type Report struct {
	Summary Summary        `json:"summary"`
	Results []rules.Result `json:"results"`
}

func Build(results []rules.Result, skipped int) Report {
	s := Summary{Workloads: len(results), Skipped: skipped}
	for _, r := range results {
		s.CPUMilli += r.CPUMilli
		switch r.Verdict {
		case rules.Ready:
			s.Ready++
			s.PotentialCPU += r.CPUMilli
		case rules.Caveats:
			s.Caveats++
			s.PotentialCPU += r.CPUMilli
		case rules.NotReady:
			s.NotReady++
		}
	}
	if s.CPUMilli > 0 {
		s.PotentialPercent = int((s.PotentialCPU*100 + s.CPUMilli/2) / s.CPUMilli)
		s.CalculatorURL = fmt.Sprintf("%s?tolerant=%d", calculatorURL, s.PotentialPercent)
	}
	return Report{Summary: s, Results: results}
}

func JSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func Table(w io.Writer, r Report, verbose bool) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERDICT\tSCORE\tWORKLOAD\tREPLICAS\tCPU\tSPOT\tFINDINGS")
	for _, res := range r.Results {
		spot := "-"
		if res.OnSpot {
			spot = "yes"
		}
		ids := make([]string, 0, len(res.Findings))
		for _, f := range res.Findings {
			ids = append(ids, f.Rule)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s/%s/%s\t%d\t%dm\t%s\t%s\n", res.Verdict, res.Score,
			res.Namespace, strings.ToLower(res.Kind), res.Name, res.Replicas, res.CPUMilli, spot, strings.Join(ids, ","))
	}
	tw.Flush()

	if verbose {
		fmt.Fprintln(w)
		for _, res := range r.Results {
			if len(res.Findings) == 0 {
				continue
			}
			fmt.Fprintf(w, "%s/%s/%s\n", res.Namespace, strings.ToLower(res.Kind), res.Name)
			for _, f := range res.Findings {
				fmt.Fprintf(w, "  %s [%s] %s\n", f.Rule, f.Severity, f.Message)
			}
		}
	}

	s := r.Summary
	fmt.Fprintf(w, "\n%d workloads: %d ready, %d with caveats, %d not ready", s.Workloads, s.Ready, s.Caveats, s.NotReady)
	if s.Skipped > 0 {
		fmt.Fprintf(w, " (%d skipped)", s.Skipped)
	}
	fmt.Fprintln(w)
	if s.CPUMilli > 0 {
		fmt.Fprintf(w, "Spot potential: %d%% of requested CPU (workloads ready or with caveats).\n", s.PotentialPercent)
		fmt.Fprintf(w, "Estimate the savings: %s\n", s.CalculatorURL)
	}
	fmt.Fprintln(w, "Ready does not mean safe: this tool only sees configuration, not how your application behaves.")
}
