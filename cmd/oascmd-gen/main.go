// Command oascmd-gen generates strongly-typed Cobra command source from an
// OpenAPI 3.0/3.1 spec file. Intended for go:generate:
//
//	//go:generate go run github.com/d3servelabs/namefi-astra/projects/oascmd/cmd/oascmd-gen -spec api.yaml -package apicli -out zz_generated_commands.go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/d3servelabs/namefi-astra/projects/oascmd/gen"
)

func main() {
	specPath := flag.String("spec", "", "path to the OpenAPI spec (JSON or YAML)")
	pkg := flag.String("package", "", "package name for the generated file")
	out := flag.String("out", "", "output file path (stdout when empty)")
	flag.Parse()

	if *specPath == "" || *pkg == "" {
		fmt.Fprintln(os.Stderr, "usage: oascmd-gen -spec <file> -package <name> [-out <file>]")
		os.Exit(2)
	}

	data, err := os.ReadFile(*specPath)
	if err != nil {
		fatal(err)
	}
	source, err := gen.Generate(data, gen.Options{PackageName: *pkg})
	if err != nil {
		fatal(err)
	}
	if *out == "" {
		if _, err := os.Stdout.Write(source); err != nil {
			fatal(err)
		}
		return
	}
	if err := os.WriteFile(*out, source, 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "oascmd-gen:", err)
	os.Exit(1)
}
