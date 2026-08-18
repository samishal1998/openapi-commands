package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/d3servelabs/namefi-astra/projects/oascmd/gen"
	"github.com/d3servelabs/namefi-astra/projects/oascmd/lock"
)

const usage = `usage: oascmd-gen -spec <file> -package <name> [-out <file>] [options]

Options:
  -spec <file>        OpenAPI 3.0/3.1 document (JSON or YAML). Required.
  -package <name>     Package name for the generated file. Required.
  -out <file>         Output file path. Writes to stdout when empty.
  -lock <file>        Lock file path. Defaults to oascmd.lock.json next to -out.
  -no-lock            Do not read or write a lock file.
  -on-drift <policy>  auto (default) | all | additive-only | check.
  -diff               Print the drift report and exit without writing anything.
  -json               Render the drift report as JSON (with -diff or -on-drift=check).

Exit codes:
  0  up to date, or the changes were applied
  1  usage or internal error
  2  drift detected and nothing written (check or diff mode)
  3  breaking drift refused (policy auto)
`

// run is main's testable body: it returns the process exit code instead of
// calling os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oascmd-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }

	specPath := fs.String("spec", "", "path to the OpenAPI spec (JSON or YAML)")
	pkg := fs.String("package", "", "package name for the generated file")
	out := fs.String("out", "", "output file path (stdout when empty)")
	lockPath := fs.String("lock", "", "lock file path (defaults to oascmd.lock.json next to -out)")
	noLock := fs.Bool("no-lock", false, "do not read or write a lock file")
	onDrift := fs.String("on-drift", string(lock.PolicyAuto), "what to do when the spec drifted: auto|all|additive-only|check")
	diff := fs.Bool("diff", false, "print the drift report and exit without writing")
	asJSON := fs.Bool("json", false, "render the drift report as JSON")

	if err := fs.Parse(args); err != nil {
		return lock.ExitError
	}
	if *specPath == "" || *pkg == "" {
		fs.Usage()
		return lock.ExitError
	}
	policy, err := lock.ParsePolicy(*onDrift)
	if err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}
	if *diff {
		policy = lock.PolicyCheck
	}

	specData, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}
	opts := gen.Options{PackageName: *pkg}
	source, models, err := gen.GenerateWithModels(specData, opts)
	if err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}
	next := lock.Compute(gen.LockModels(models))

	// Without a lock there is nothing to compare against, so drift
	// handling is skipped entirely and the file is simply written.
	if *noLock {
		if *diff {
			fmt.Fprintln(stderr, "oascmd-gen: -diff needs a lock file to compare against; drop -no-lock")
			return lock.ExitError
		}
		if err := writeSource(*out, source, stdout); err != nil {
			fmt.Fprintln(stderr, "oascmd-gen:", err)
			return lock.ExitError
		}
		return lock.ExitOK
	}

	resolvedLock := resolveLockPath(*lockPath, *out)
	previous, existed, err := lock.Load(resolvedLock)
	if err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}

	// First run: no baseline, so nothing can have drifted.
	if !existed {
		if policy == lock.PolicyCheck {
			fmt.Fprintf(stderr, "oascmd-gen: no lock file at %s yet; run the generator once to create it.\n", resolvedLock)
			return lock.ExitDrift
		}
		if err := writeAll(*out, resolvedLock, source, next, stdout); err != nil {
			fmt.Fprintln(stderr, "oascmd-gen:", err)
			return lock.ExitError
		}
		return lock.ExitOK
	}

	report := lock.Diff(previous, next)
	decision := lock.Decide(policy, report)

	if err := printReport(stdout, stderr, report, decision, *asJSON, policy); err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}
	if !decision.Write {
		return decision.ExitCode
	}

	// additive-only: replace every breaking operation with the form
	// recorded in the lock, so the generated file grows without moving
	// anything that already worked.
	if policy == lock.PolicyAdditiveOnly && len(decision.KeptKeys) > 0 {
		source, next, err = applyAdditiveOnly(specData, opts, models, previous, decision.KeptKeys)
		if err != nil {
			fmt.Fprintln(stderr, "oascmd-gen:", err)
			return lock.ExitError
		}
	}
	if err := writeAll(*out, resolvedLock, source, next, stdout); err != nil {
		fmt.Fprintln(stderr, "oascmd-gen:", err)
		return lock.ExitError
	}
	return decision.ExitCode
}

// applyAdditiveOnly rebuilds the emitted models, substituting the recorded
// form for each operation whose new form would break.
func applyAdditiveOnly(specData []byte, opts gen.Options, models []gen.CommandModel, previous lock.Lock, keptKeys []string) ([]byte, lock.Lock, error) {
	kept := map[string]bool{}
	for _, k := range keptKeys {
		kept[k] = true
	}
	var final []gen.CommandModel
	seen := map[string]bool{}
	for _, m := range models {
		key := lock.Key(m.Op)
		if kept[key] {
			entry, ok := previous.Operations[key]
			if !ok {
				continue
			}
			restored, err := lock.ToModel(entry)
			if err != nil {
				return nil, lock.Lock{}, err
			}
			final = append(final, gen.ModelFromLock(restored))
			seen[key] = true
			continue
		}
		final = append(final, m)
		seen[key] = true
	}
	// Removed operations are breaking too: keep emitting them.
	var missing []string
	for _, k := range keptKeys {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		restored, err := lock.ToModel(previous.Operations[k])
		if err != nil {
			return nil, lock.Lock{}, err
		}
		final = append(final, gen.ModelFromLock(restored))
	}

	source, err := gen.GenerateFromModels(specData, opts, final)
	if err != nil {
		return nil, lock.Lock{}, err
	}
	return source, lock.Compute(gen.LockModels(final)), nil
}

func printReport(stdout, stderr io.Writer, report lock.Report, decision lock.Decision, asJSON bool, policy lock.Policy) error {
	if asJSON {
		data, err := report.JSON()
		if err != nil {
			return err
		}
		if _, err := stdout.Write(data); err != nil {
			return err
		}
		return nil
	}
	if report.Severity() != lock.SeverityNone {
		fmt.Fprint(stderr, report.Text())
	}
	fmt.Fprintln(stderr, "oascmd-gen:", decision.Summary)
	for _, key := range decision.KeptKeys {
		fmt.Fprintf(stderr, "  kept as previously generated: %s\n", key)
	}
	return nil
}

// resolveLockPath returns the explicit lock path, or oascmd.lock.json beside
// the generated file (in the working directory when writing to stdout).
func resolveLockPath(explicit, out string) string {
	if explicit != "" {
		return explicit
	}
	if out == "" {
		return lock.DefaultFileName
	}
	return filepath.Join(filepath.Dir(out), lock.DefaultFileName)
}

func writeAll(out, lockPath string, source []byte, l lock.Lock, stdout io.Writer) error {
	if err := writeSource(out, source, stdout); err != nil {
		return err
	}
	return lock.Write(lockPath, l)
}

func writeSource(out string, source []byte, stdout io.Writer) error {
	if out == "" {
		_, err := stdout.Write(source)
		return err
	}
	return os.WriteFile(out, source, 0o644)
}
