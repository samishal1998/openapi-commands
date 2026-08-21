package runtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d3servelabs/openapi-commands"
	"github.com/d3servelabs/openapi-commands/lock"
	oasruntime "github.com/d3servelabs/openapi-commands/runtime"
)

func verifySpec(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestVerifyLockDetectsServedSpecDrift is the runtime-mode use case: a
// checked-in lock describes what the CLI expects, and the live spec is
// compared against it.
func TestVerifyLockDetectsServedSpecDrift(t *testing.T) {
	data := verifySpec(t)
	expected, err := oasruntime.ComputeLock(data, oasruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}

	report, err := oasruntime.VerifyLock(data, oasruntime.Options{}, expected)
	if err != nil {
		t.Fatal(err)
	}
	if report.Severity() != lock.SeverityNone {
		t.Fatalf("an unchanged spec must not drift:\n%s", report.Text())
	}

	// Simulate the served API dropping an operation.
	trimmed := expected
	trimmed.Operations = map[string]lock.Operation{}
	for k, v := range expected.Operations {
		trimmed.Operations[k] = v
	}
	extra := trimmed.Operations["listPets"]
	extra.OperationID = "removedFromServer"
	trimmed.Operations["removedFromServer"] = extra

	report, err = oasruntime.VerifyLock(data, oasruntime.Options{}, trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if report.Severity() != lock.SeverityBreaking {
		t.Fatalf("a missing operation must be breaking, got %q", report.Severity())
	}
}

// TestVerifyLockIgnoresConstructorNames lets a runtime check consume a lock
// written by the generator, which records constructor names runtime mode
// does not have.
func TestVerifyLockIgnoresConstructorNames(t *testing.T) {
	data := verifySpec(t)
	live, err := oasruntime.ComputeLock(data, oasruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	withNames := live
	withNames.Operations = map[string]lock.Operation{}
	for k, v := range live.Operations {
		v.FuncName = "NewSomethingCommand"
		withNames.Operations[k] = v
	}
	report, err := oasruntime.VerifyLock(data, oasruntime.Options{}, withNames)
	if err != nil {
		t.Fatal(err)
	}
	if report.Severity() != lock.SeverityNone {
		t.Fatalf("constructor names must not count as drift:\n%s", report.Text())
	}
}

func TestComputeLockHonorsHooks(t *testing.T) {
	data := verifySpec(t)
	l, err := oasruntime.ComputeLock(data, oasruntime.Options{
		Hooks: oascmd.Hooks{OnReadOperation: func(op *oascmd.Operation) error {
			if strings.HasPrefix(op.Path, "/pets") {
				return oascmd.SkipOperation
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range l.Operations {
		if strings.HasPrefix(op.Path, "/pets") {
			t.Fatalf("vetoed operation %s present in the lock", op.Path)
		}
	}
	if l.Operations["listPets"].Command != "" {
		t.Error("listPets should have been skipped")
	}
}
