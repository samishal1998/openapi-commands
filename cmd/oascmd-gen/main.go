// Command oascmd-gen generates strongly-typed Cobra command source from an
// OpenAPI 3.0/3.1 spec file. Intended for go:generate:
//
//	//go:generate go run github.com/d3servelabs/openapi-commands/cmd/oascmd-gen -spec api.yaml -package apicli -out zz_generated_commands.go
//
// It also maintains a lock file recording the generated CLI surface, so a
// later run can report exactly what changed and refuse to silently break
// existing commands. See the project README for the severity ladder, the
// drift policies and the exit codes.
package main

import (
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
