// Command spot-readiness tells which Kubernetes workloads can safely run on Spot capacity.
package main

import (
	"os"

	"github.com/robertasolimandonofreo/spot-readiness/internal/app"
)

var version = "dev"

func main() {
	os.Exit(app.Run(os.Args[1:], app.Env{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}))
}
