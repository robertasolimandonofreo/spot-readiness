// Command shellcheck-action runs shellcheck on every bash script in the steps of action.yml.
//
// actionlint checks workflow files but not the steps of a composite action, and action.yml holds
// a lot of bash (the base commit scan, checksums, outputs). Expressions like ${{ inputs.x }} are
// replaced by a placeholder so shellcheck can read the script. Run it from the repository root.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

var expression = regexp.MustCompile(`\$\{\{.*?\}\}`)

func main() {
	data, err := os.ReadFile("action.yml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				Name  string `json:"name"`
				ID    string `json:"id"`
				Shell string `json:"shell"`
				Run   string `json:"run"`
			} `json:"steps"`
		} `json:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	failed := false
	for _, step := range action.Runs.Steps {
		if step.Run == "" || step.Shell != "bash" {
			continue
		}
		name := step.Name
		if name == "" {
			name = step.ID
		}
		script := "#!/usr/bin/env bash\n" + expression.ReplaceAllString(step.Run, "GHA_EXPRESSION")
		cmd := exec.Command("shellcheck", "-s", "bash", "-S", "style", "-")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			failed = true
			fmt.Printf("--- %s\n%s", name, strings.ReplaceAll(string(out), "In - line", "In script line"))
			continue
		}
		fmt.Printf("ok   %s\n", name)
	}
	if failed {
		os.Exit(1)
	}
}
