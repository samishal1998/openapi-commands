package lock

import "fmt"

// Policy decides what a generator run does when the spec has drifted away
// from the lock file.
type Policy string

const (
	// PolicyAuto regenerates when the drift is at most additive, and
	// refuses on breaking drift with a report of exactly what breaks.
	// This is the default: safe growth is silent, breakage is a decision.
	PolicyAuto Policy = "auto"
	// PolicyAll always regenerates, breaking changes included. The escape
	// hatch for when the break is intended.
	PolicyAll Policy = "all"
	// PolicyAdditiveOnly regenerates the safe parts and keeps every
	// breaking operation exactly as it was last generated, reporting what
	// it left alone.
	PolicyAdditiveOnly Policy = "additive-only"
	// PolicyCheck never writes anything; it only reports. CI mode.
	PolicyCheck Policy = "check"
)

// Policies lists every accepted policy, for help text and validation.
var Policies = []Policy{PolicyAuto, PolicyAll, PolicyAdditiveOnly, PolicyCheck}

// ParsePolicy validates a policy name.
func ParsePolicy(s string) (Policy, error) {
	for _, p := range Policies {
		if Policy(s) == p {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown drift policy %q (want one of: auto, all, additive-only, check)", s)
}

// Exit codes, modelled on `terraform plan -detailed-exitcode` and `gofmt -l`:
// success is 0, a real failure is 1, and "there is drift" gets its own code
// so CI can branch on it.
const (
	// ExitOK means the run succeeded: either nothing drifted, or the
	// drift was applied.
	ExitOK = 0
	// ExitError means a usage or internal error (bad flags, unreadable
	// spec, unparsable lock).
	ExitError = 1
	// ExitDrift means drift was detected in check mode and nothing was
	// written.
	ExitDrift = 2
	// ExitBreaking means breaking drift was refused: nothing was written
	// and the report explains what breaks.
	ExitBreaking = 3
)

// Decision is what a policy concluded for one run.
type Decision struct {
	Policy Policy
	Report Report
	// Write reports whether the generated file and lock should be
	// written.
	Write bool
	// KeptKeys lists operations whose previously generated form was kept
	// instead of the new one (additive-only). Sorted by key.
	KeptKeys []string
	// ExitCode is the process exit code this decision implies.
	ExitCode int
	// Summary is a one-line plain-language statement of the outcome.
	Summary string
}

// Decide applies a policy to a drift report. It does not touch the file
// system; the caller writes (or does not) according to the Decision.
func Decide(p Policy, r Report) Decision {
	sev := r.Severity()
	d := Decision{Policy: p, Report: r, ExitCode: ExitOK}
	switch p {
	case PolicyCheck:
		if sev == SeverityNone {
			d.Summary = "Up to date: the generated CLI matches the lock file."
			return d
		}
		d.ExitCode = ExitDrift
		d.Summary = fmt.Sprintf("Drift detected (%s). Nothing was written because this is check mode.", sev)
		return d
	case PolicyAll:
		d.Write = true
		if sev == SeverityNone {
			d.Summary = "Up to date; regenerated anyway."
			return d
		}
		d.Summary = fmt.Sprintf("Regenerated, accepting %s changes.", sev)
		return d
	case PolicyAdditiveOnly:
		d.Write = true
		for _, op := range r.BreakingChanges() {
			d.KeptKeys = append(d.KeptKeys, op.Key)
		}
		if len(d.KeptKeys) == 0 {
			d.Summary = fmt.Sprintf("Regenerated; all changes were safe (%s).", sev)
			return d
		}
		d.Summary = fmt.Sprintf("Regenerated the safe changes; left %d breaking operation(s) as they were.", len(d.KeptKeys))
		return d
	default: // PolicyAuto
		if sev.AtLeast(SeverityBreaking) {
			d.ExitCode = ExitBreaking
			d.Summary = "Refusing to regenerate: this would break existing commands. " +
				"Re-run with --on-drift=all to accept the breakage, or --on-drift=additive-only to apply just the safe changes."
			return d
		}
		d.Write = true
		if sev == SeverityNone {
			d.Summary = "Up to date."
			return d
		}
		d.Summary = fmt.Sprintf("Regenerated; the changes were %s.", sev)
		return d
	}
}
