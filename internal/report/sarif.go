package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// The types below are the part of SARIF 2.1.0 that GitHub Code Scanning reads.

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifRule struct {
	ID                   string          `json:"id"`
	HelpURI              string          `json:"helpUri,omitempty"`
	ShortDescription     sarifText       `json:"shortDescription"`
	DefaultConfiguration sarifRuleConfig `json:"defaultConfiguration"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
}

type sarifLocation struct {
	PhysicalLocation *sarifPhysical `json:"physicalLocation,omitempty"`
	LogicalLocations []sarifLogical `json:"logicalLocations,omitempty"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           sarifRegion   `json:"region"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifLogical struct {
	FullyQualifiedName string `json:"fullyQualifiedName"`
	Kind               string `json:"kind"`
}

// sarifLevel maps the severities to SARIF levels: blockers are errors, caveats warnings and infos notes.
func sarifLevel(s rules.Severity) string {
	switch s {
	case rules.Blocker:
		return "error"
	case rules.Caveat:
		return "warning"
	}
	return "note"
}

// SARIF writes the findings as a SARIF 2.1.0 log, for GitHub Code Scanning and other viewers.
// Findings that came from a file point to it and to the line of the field; findings from a cluster
// have no file and carry the workload as a logical location. Only the workload rules (SR) are
// included.
func SARIF(w io.Writer, r Report, version string) error {
	driver := sarifDriver{Name: "spot-readiness", Version: version,
		InformationURI: "https://github.com/robertasolimandonofreo/spot-readiness", Rules: []sarifRule{}}
	index := map[string]int{}
	for _, d := range rules.Descriptions {
		index[d.ID] = len(driver.Rules)
		driver.Rules = append(driver.Rules, sarifRule{ID: d.ID, HelpURI: rules.DocsURL(d.ID), ShortDescription: sarifText{d.Text},
			DefaultConfiguration: sarifRuleConfig{Level: sarifLevel(rules.Severity(d.Severity))}})
	}

	results := []sarifResult{}
	for _, res := range r.Results {
		name := workloadKey(res)
		for _, f := range res.Findings {
			loc := sarifLocation{LogicalLocations: []sarifLogical{{FullyQualifiedName: name, Kind: strings.ToLower(res.Kind)}}}
			if f.File != "" {
				line := f.Line
				if line < 1 {
					line = 1
				}
				loc.PhysicalLocation = &sarifPhysical{
					ArtifactLocation: sarifArtifact{URI: f.File, URIBaseID: "%SRCROOT%"},
					Region:           sarifRegion{StartLine: line},
				}
			}
			// The fingerprint ignores the line, so an alert survives edits that move the manifest.
			sum := sha256.Sum256([]byte(f.File + "|" + name + "|" + f.Rule))
			results = append(results, sarifResult{RuleID: f.Rule, RuleIndex: index[f.Rule], Level: sarifLevel(f.Severity),
				Message:             sarifText{f.Message},
				Locations:           []sarifLocation{loc},
				PartialFingerprints: map[string]string{"spotReadiness/v1": hex.EncodeToString(sum[:16])}})
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(sarifLog{Schema: "https://json.schemastore.org/sarif-2.1.0.json", Version: "2.1.0",
		Runs: []sarifRun{{Tool: sarifTool{Driver: driver}, Results: results}}})
}
