package runtime

import (
	"fmt"

	"github.com/d3servelabs/namefi-astra/projects/oascmd"
	"github.com/d3servelabs/namefi-astra/projects/oascmd/lock"
	"github.com/d3servelabs/namefi-astra/projects/oascmd/spec"
)

// ComputeLock builds a lock snapshot of the command surface this spec would
// produce at runtime, applying the same hooks and naming rules Build uses.
//
// Constructor names are absent: runtime mode has no generated constructors,
// so the snapshot describes commands and flags only.
func ComputeLock(data []byte, opts Options) (lock.Lock, error) {
	ops, err := spec.Load(data)
	if err != nil {
		return lock.Lock{}, err
	}
	nameFunc := opts.NameFunc
	if nameFunc == nil {
		nameFunc = oascmd.DeriveCommandPath
	}
	var models []lock.Model
	for i := range ops {
		op := ops[i]
		keep, err := opts.Hooks.ApplyRead(&op)
		if err != nil {
			return lock.Lock{}, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		if !keep {
			continue
		}
		if err := opts.Hooks.ApplyBody(&op); err != nil {
			return lock.Lock{}, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		models = append(models, lock.Model{Path: nameFunc(op), Op: op})
	}
	return lock.Compute(models), nil
}

// VerifyLock compares a live spec against a checked-in lock file and reports
// how the served API has drifted from what the CLI was built for.
//
// Constructor names are cleared on both sides before comparing, because
// runtime mode does not have them; comparing a runtime snapshot against a
// generator-written lock would otherwise report every operation as renamed.
func VerifyLock(data []byte, opts Options, expected lock.Lock) (lock.Report, error) {
	live, err := ComputeLock(data, opts)
	if err != nil {
		return lock.Report{}, err
	}
	return lock.Diff(withoutFuncNames(expected), withoutFuncNames(live)), nil
}

func withoutFuncNames(l lock.Lock) lock.Lock {
	out := l
	out.Operations = make(map[string]lock.Operation, len(l.Operations))
	for k, op := range l.Operations {
		op.FuncName = ""
		out.Operations[k] = op
	}
	return out
}
