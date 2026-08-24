package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/openapi-commands/lock"
)

// minimalSpec is a small hand-written spec so drift can be introduced
// surgically. Each scenario mutates it and re-runs the generator.
const minimalSpec = `openapi: 3.0.3
info: {title: Pets, version: 1.0.0}
paths:
  /pets:
    get:
      operationId: listPets
      tags: [pets]
      summary: List pets
      parameters:
        - name: limit
          in: query
          schema: {type: integer}
      responses: {"200": {description: ok}}
  /pets/{petId}:
    get:
      operationId: getPet
      tags: [pets]
      parameters:
        - name: petId
          in: path
          required: true
          schema: {type: string}
      responses: {"200": {description: ok}}
`

type env struct {
	t        *testing.T
	dir      string
	specPath string
	outPath  string
	lockPath string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{
		t:        t,
		dir:      dir,
		specPath: filepath.Join(dir, "api.yaml"),
		outPath:  filepath.Join(dir, "generated.go"),
		lockPath: filepath.Join(dir, lock.DefaultFileName),
	}
	e.writeSpec(minimalSpec)
	return e
}

func (e *env) writeSpec(s string) {
	e.t.Helper()
	if err := os.WriteFile(e.specPath, []byte(s), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run(extra ...string) (int, string, string) {
	e.t.Helper()
	args := append([]string{"-spec", e.specPath, "-package", "apicli", "-out", e.outPath}, extra...)
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (e *env) lock() lock.Lock {
	e.t.Helper()
	l, existed, err := lock.Load(e.lockPath)
	if err != nil || !existed {
		e.t.Fatalf("load lock: existed=%v err=%v", existed, err)
	}
	return l
}

func (e *env) generated() string {
	e.t.Helper()
	data, err := os.ReadFile(e.outPath)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func TestFirstRunWritesLock(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run()
	if code != lock.ExitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	l := e.lock()
	if len(l.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(l.Operations))
	}
	if l.Operations["listPets"].Command != "pets list" {
		t.Errorf("unexpected command %q", l.Operations["listPets"].Command)
	}
	if l.Operations["listPets"].FuncName == "" {
		t.Error("constructor name not recorded")
	}
}

func TestLockIsByteIdenticalAcrossRuns(t *testing.T) {
	e := newEnv(t)
	e.run()
	first, err := os.ReadFile(e.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	e.run()
	second, err := os.ReadFile(e.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("regeneration changed the lock file:\n%s\n---\n%s", first, second)
	}
}

func TestNoLockOptOut(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("-no-lock")
	if code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, err := os.Stat(e.lockPath); !os.IsNotExist(err) {
		t.Fatal("--no-lock still wrote a lock file")
	}
	if e.generated() == "" {
		t.Fatal("source was not generated")
	}
}

func TestCustomLockPath(t *testing.T) {
	e := newEnv(t)
	custom := filepath.Join(e.dir, "sub", "custom.lock.json")
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("-lock", custom); code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("custom lock not written: %v", err)
	}
}

// specWithNewOperation adds an operation (additive drift).
func specWithNewOperation() string {
	return minimalSpec + `  /owners:
    get:
      operationId: listOwners
      tags: [owners]
      responses: {"200": {description: ok}}
`
}

// specWithChangedFlagType turns --limit from int to string (breaking).
func specWithChangedFlagType() string {
	return strings.Replace(minimalSpec, "schema: {type: integer}", "schema: {type: string}", 1)
}

// specWithRemovedOperation drops GET /pets/{petId} (breaking).
func specWithRemovedOperation() string {
	return strings.Split(minimalSpec, "  /pets/{petId}:")[0]
}

func TestDriftScenarios(t *testing.T) {
	cases := []struct {
		name     string
		spec     string
		severity lock.Severity
	}{
		{"added operation", specWithNewOperation(), lock.SeverityAdditive},
		{"changed flag type", specWithChangedFlagType(), lock.SeverityBreaking},
		{"removed operation", specWithRemovedOperation(), lock.SeverityBreaking},
		{"cosmetic", strings.Replace(minimalSpec, "summary: List pets", "summary: List all the pets", 1), lock.SeverityCosmetic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.run()
			e.writeSpec(tc.spec)

			code, stdout, _ := e.run("-diff", "-json")
			var payload struct {
				Severity lock.Severity `json:"severity"`
			}
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatalf("bad JSON report: %v\n%s", err, stdout)
			}
			if payload.Severity != tc.severity {
				t.Fatalf("severity = %q, want %q\n%s", payload.Severity, tc.severity, stdout)
			}
			if code != lock.ExitDrift {
				t.Fatalf("diff exit = %d, want %d", code, lock.ExitDrift)
			}
		})
	}
}

func TestPolicyAutoAppliesAdditiveDrift(t *testing.T) {
	e := newEnv(t)
	e.run()
	e.writeSpec(specWithNewOperation())

	code, _, stderr := e.run()
	if code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, ok := e.lock().Operations["listOwners"]; !ok {
		t.Error("the new operation was not written to the lock")
	}
	if !strings.Contains(e.generated(), "NewOwnersListCommand") {
		t.Error("the new command was not generated")
	}
}

func TestPolicyAutoRefusesBreakingDrift(t *testing.T) {
	e := newEnv(t)
	e.run()
	before := e.generated()
	beforeLock, _ := os.ReadFile(e.lockPath)
	e.writeSpec(specWithRemovedOperation())

	code, _, stderr := e.run()
	if code != lock.ExitBreaking {
		t.Fatalf("exit = %d, want %d: %s", code, lock.ExitBreaking, stderr)
	}
	if e.generated() != before {
		t.Error("the generated file was modified despite the refusal")
	}
	afterLock, _ := os.ReadFile(e.lockPath)
	if !bytes.Equal(beforeLock, afterLock) {
		t.Error("the lock was modified despite the refusal")
	}
	for _, want := range []string{"Breaking changes", "pets get", "--on-drift=all"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, stderr)
		}
	}
}

func TestPolicyAllAcceptsBreakingDrift(t *testing.T) {
	e := newEnv(t)
	e.run()
	e.writeSpec(specWithRemovedOperation())

	code, _, stderr := e.run("-on-drift", "all")
	if code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, ok := e.lock().Operations["getPet"]; ok {
		t.Error("the removed operation is still in the lock")
	}
	if strings.Contains(e.generated(), "NewPetsGetCommand") {
		t.Error("the removed command is still generated")
	}
}

func TestPolicyAdditiveOnlyKeepsBreakingOperations(t *testing.T) {
	e := newEnv(t)
	e.run()
	// Remove one operation (breaking) and add another (safe) at once.
	e.writeSpec(specWithRemovedOperation() + `  /owners:
    get:
      operationId: listOwners
      tags: [owners]
      responses: {"200": {description: ok}}
`)

	code, _, stderr := e.run("-on-drift", "additive-only")
	if code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "kept as previously generated: getPet") {
		t.Errorf("the skipped operation should be reported:\n%s", stderr)
	}
	src := e.generated()
	if !strings.Contains(src, "NewOwnersListCommand") {
		t.Error("the safe addition was not applied")
	}
	if !strings.Contains(src, "NewPetsGetCommand") {
		t.Error("the breaking removal was applied instead of being kept")
	}
	l := e.lock()
	if _, ok := l.Operations["getPet"]; !ok {
		t.Error("the kept operation is missing from the lock")
	}
	if _, ok := l.Operations["listOwners"]; !ok {
		t.Error("the new operation is missing from the lock")
	}
}

func TestPolicyAdditiveOnlyKeepsChangedFlagType(t *testing.T) {
	e := newEnv(t)
	e.run()
	e.writeSpec(specWithChangedFlagType())

	if code, _, stderr := e.run("-on-drift", "additive-only"); code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if got := e.lock().Operations["listPets"].Flags[0].Type; got != "int" {
		t.Errorf("--limit type = %q, want the previously generated int", got)
	}
	if !strings.Contains(e.generated(), "flagLimit int64") {
		t.Error("the generated flag should still be the previously generated int64")
	}
}

func TestPolicyCheckNeverWrites(t *testing.T) {
	e := newEnv(t)
	e.run()
	before := e.generated()
	e.writeSpec(specWithNewOperation())

	code, _, stderr := e.run("-on-drift", "check")
	if code != lock.ExitDrift {
		t.Fatalf("exit = %d, want %d: %s", code, lock.ExitDrift, stderr)
	}
	if e.generated() != before {
		t.Error("check mode wrote the generated file")
	}
	if _, ok := e.lock().Operations["listOwners"]; ok {
		t.Error("check mode wrote the lock")
	}
}

func TestPolicyCheckPassesWhenUpToDate(t *testing.T) {
	e := newEnv(t)
	e.run()
	code, _, stderr := e.run("-on-drift", "check")
	if code != lock.ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Up to date") {
		t.Errorf("unexpected output: %s", stderr)
	}
}

func TestCheckWithoutLockIsDrift(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("-on-drift", "check")
	if code != lock.ExitDrift {
		t.Fatalf("exit = %d, want %d", code, lock.ExitDrift)
	}
	if !strings.Contains(stderr, "no lock file") {
		t.Errorf("unexpected output: %s", stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{"-package", "x"},                          // no -spec
		{"-spec", "missing.yaml"},                  // no -package
		{"-spec", "missing.yaml", "-package", "x"}, // unreadable spec
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != lock.ExitError {
			t.Errorf("run(%v) = %d, want %d", args, code, lock.ExitError)
		}
	}
}

func TestUnknownPolicyIsUsageError(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("-on-drift", "yolo")
	if code != lock.ExitError {
		t.Fatalf("exit = %d, want %d", code, lock.ExitError)
	}
	if !strings.Contains(stderr, "unknown drift policy") {
		t.Errorf("unexpected output: %s", stderr)
	}
}

func TestDiffWithNoLockIsUsageError(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.run("-diff", "-no-lock"); code != lock.ExitError {
		t.Fatalf("exit = %d, want %d", code, lock.ExitError)
	}
}

func TestCorruptLockIsAnErrorNotAPanic(t *testing.T) {
	e := newEnv(t)
	e.run()
	if err := os.WriteFile(e.lockPath, []byte(`{"lockVersion": 999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.run()
	if code != lock.ExitError {
		t.Fatalf("exit = %d, want %d", code, lock.ExitError)
	}
	if !strings.Contains(stderr, "upgrade oascmd-gen") {
		t.Errorf("unexpected output: %s", stderr)
	}
}
