// Command petstore is a runnable example of both oascmd modes.
//
// Runtime mode (the default): the spec is embedded, parsed at startup, and
// turned into commands dynamically.
//
//	go run ./examples/petstore --help
//	go run ./examples/petstore pets list --limit 5
//
// The "orders create" and "shipments create" commands demonstrate body
// envelope unwrapping: the spec wraps their bodies in {"json": …} and
// {"data": {"attributes": …}}, but the flags are the inner properties
// (--pet-id, --address) and the CLI re-wraps them on submit.
//
//	go run ./examples/petstore orders create --help
//
// Buildtime mode: the go:generate line below emits typed constructors into
// the petstoregen package, which the "typed" subtree uses.
//
//	go generate ./examples/petstore
//	go run ./examples/petstore typed pets list --limit 5
//
// The generator also maintains petstoregen/oascmd.lock.json, the record of
// the generated CLI surface. Re-running go:generate after a spec change
// regenerates only when the change is safe; a breaking change is refused
// with a report (see the README for the severity ladder and policies).
//
//go:generate go run github.com/d3servelabs/openapi-commands/cmd/oascmd-gen -spec ../../testdata/petstore.yaml -package petstoregen -out petstoregen/generated.go -on-drift auto
package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/d3servelabs/openapi-commands"
	"github.com/d3servelabs/openapi-commands/examples/petstore/petstoregen"
	oasruntime "github.com/d3servelabs/openapi-commands/runtime"
)

//go:embed petstore.yaml
var specData []byte

func main() {
	var baseURL string
	root := &cobra.Command{
		Use:           "petstore",
		Short:         "Example CLI generated from an OpenAPI spec",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&baseURL, "base-url", "https://petstore.example.com", "API base URL")

	execOpts := oascmd.ExecOptions{
		// BaseURL is read from the flag at execute time via the
		// pointer-free closure below; for brevity this example
		// resolves it once at wiring time.
		BaseURL: baseURLFromArgs(os.Args, "https://petstore.example.com"),
		Auth: func(req *http.Request) error {
			if token := os.Getenv("PETSTORE_TOKEN"); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			return nil
		},
	}

	// --- Runtime mode: commands built from the spec at startup. ---
	err := oasruntime.Attach(root, specData, oasruntime.Options{
		Exec: execOpts,
		Hooks: oascmd.Hooks{
			// Drop deprecated operations from the CLI entirely.
			OnReadOperation: func(op *oascmd.Operation) error {
				if op.Deprecated {
					return oascmd.SkipOperation
				}
				return nil
			},
			// Tag every command's help with its HTTP route.
			OnAfterCreateCommand: func(op oascmd.Operation, cmd *cobra.Command) error {
				cmd.Annotations = map[string]string{"route": op.Method + " " + op.Path}
				return nil
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "petstore:", err)
		os.Exit(1)
	}

	// --- Buildtime mode: typed constructors from the generated package. ---
	typed := &cobra.Command{Use: "typed", Short: "The same API via generated, compile-time-checked commands"}
	for _, c := range petstoregen.NewCommandTree(execOpts) {
		typed.AddCommand(c)
	}
	root.AddCommand(typed)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "petstore:", err)
		os.Exit(1)
	}
}

// baseURLFromArgs is a small shim so the example can resolve --base-url
// before the command tree is built. A real CLI would read it from config in
// a PersistentPreRun and store it in a shared struct the ExecOptions
// closures read.
func baseURLFromArgs(args []string, fallback string) string {
	for i, a := range args {
		if a == "--base-url" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return fallback
}
