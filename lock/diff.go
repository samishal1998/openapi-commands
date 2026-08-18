package lock

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Severity is how deeply a change cuts into the CLI surface. The ladder is
// ordered: None < Cosmetic < Additive < Breaking.
type Severity string

const (
	// SeverityNone means nothing changed.
	SeverityNone Severity = "none"
	// SeverityCosmetic means only help text moved: summaries,
	// descriptions, flag usage strings. No command, flag or type
	// changed, so nothing a user types or compiles is affected.
	SeverityCosmetic Severity = "cosmetic"
	// SeverityAdditive means the surface grew but every existing
	// invocation still works: a new operation, a new optional flag, a new
	// enum value, a new optional body property, a flag becoming optional,
	// a deprecation marker.
	SeverityAdditive Severity = "additive"
	// SeverityBreaking means an existing invocation or compiling caller
	// stops working: an operation removed, a command path or constructor
	// renamed, a flag removed, a flag type changed, optional becoming
	// required, an enum value removed, method/path changed, or the body
	// shape changed.
	SeverityBreaking Severity = "breaking"
)

var severityRank = map[Severity]int{
	SeverityNone:     0,
	SeverityCosmetic: 1,
	SeverityAdditive: 2,
	SeverityBreaking: 3,
}

// Rank orders severities; higher is worse.
func (s Severity) Rank() int { return severityRank[s] }

// AtLeast reports whether s is at least as severe as other.
func (s Severity) AtLeast(other Severity) bool { return s.Rank() >= other.Rank() }

func maxSeverity(a, b Severity) Severity {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// ChangeKind is how an operation entry moved between two locks.
type ChangeKind string

const (
	// ChangeAdded is an operation present only in the new surface.
	ChangeAdded ChangeKind = "added"
	// ChangeRemoved is an operation present only in the old lock.
	ChangeRemoved ChangeKind = "removed"
	// ChangeModified is an operation present in both but different.
	ChangeModified ChangeKind = "modified"
)

// FieldChange is one precise difference inside an operation.
type FieldChange struct {
	// Field names what moved, e.g. "command", "funcName",
	// "flag --limit type", "flag --tag".
	Field string `json:"field"`
	// Old and New are the human-readable before/after values ("" when the
	// side does not exist).
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
	// Severity of this single field change.
	Severity Severity `json:"severity"`
	// Detail is a plain-language sentence explaining the consequence.
	Detail string `json:"detail"`
}

// OperationChange groups every field change for one operation.
type OperationChange struct {
	// Key is the operation identity (operationId, else "METHOD path").
	Key      string        `json:"key"`
	Kind     ChangeKind    `json:"kind"`
	Command  string        `json:"command"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Severity Severity      `json:"severity"`
	Changes  []FieldChange `json:"changes,omitempty"`
}

// Report is the result of comparing two locks.
type Report struct {
	// GeneratorChanged is true when the lock was written by a different
	// generator version than this build. The emitted code may differ even
	// if the spec did not, so it counts as additive drift.
	GeneratorChanged bool   `json:"generatorChanged"`
	OldGenerator     string `json:"oldGenerator,omitempty"`
	NewGenerator     string `json:"newGenerator,omitempty"`
	// Operations lists every changed operation, sorted by key. Unchanged
	// operations are omitted.
	Operations []OperationChange `json:"operations"`
}

// Severity aggregates the report to a single worst-case severity.
func (r Report) Severity() Severity {
	sev := SeverityNone
	if r.GeneratorChanged {
		sev = SeverityAdditive
	}
	for _, op := range r.Operations {
		sev = maxSeverity(sev, op.Severity)
	}
	return sev
}

// BreakingChanges returns only the operations that break.
func (r Report) BreakingChanges() []OperationChange {
	var out []OperationChange
	for _, op := range r.Operations {
		if op.Severity == SeverityBreaking {
			out = append(out, op)
		}
	}
	return out
}

// Diff compares an old lock against a newly computed one and classifies
// every difference. See Severity for the ladder.
func Diff(old, next Lock) Report {
	r := Report{
		OldGenerator: old.GeneratorVersion,
		NewGenerator: next.GeneratorVersion,
	}
	if old.GeneratorVersion != "" && next.GeneratorVersion != "" && old.GeneratorVersion != next.GeneratorVersion {
		r.GeneratorChanged = true
	}

	keys := map[string]bool{}
	for k := range old.Operations {
		keys[k] = true
	}
	for k := range next.Operations {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, key := range sorted {
		o, hadOld := old.Operations[key]
		n, hasNew := next.Operations[key]
		switch {
		case !hadOld:
			r.Operations = append(r.Operations, OperationChange{
				Key: key, Kind: ChangeAdded, Command: n.Command,
				Method: n.Method, Path: n.Path, Severity: SeverityAdditive,
				Changes: []FieldChange{{
					Field: "operation", New: n.Command, Severity: SeverityAdditive,
					Detail: "new command; nothing that works today stops working",
				}},
			})
		case !hasNew:
			r.Operations = append(r.Operations, OperationChange{
				Key: key, Kind: ChangeRemoved, Command: o.Command,
				Method: o.Method, Path: o.Path, Severity: SeverityBreaking,
				Changes: []FieldChange{{
					Field: "operation", Old: o.Command, Severity: SeverityBreaking,
					Detail: "command is gone; anyone running it, or calling its constructor, breaks",
				}},
			})
		default:
			changes := diffOperation(o, n)
			if len(changes) == 0 {
				continue
			}
			sev := SeverityNone
			for _, c := range changes {
				sev = maxSeverity(sev, c.Severity)
			}
			r.Operations = append(r.Operations, OperationChange{
				Key: key, Kind: ChangeModified, Command: n.Command,
				Method: n.Method, Path: n.Path, Severity: sev, Changes: changes,
			})
		}
	}
	return r
}

func diffOperation(o, n Operation) []FieldChange {
	var out []FieldChange
	if o.Digest == n.Digest && o.Digest != "" {
		return nil
	}

	add := func(field, oldV, newV string, sev Severity, detail string) {
		out = append(out, FieldChange{Field: field, Old: oldV, New: newV, Severity: sev, Detail: detail})
	}

	if o.Command != n.Command {
		add("command", o.Command, n.Command, SeverityBreaking,
			"the command moved; the old words no longer resolve")
	}
	if o.FuncName != n.FuncName {
		add("funcName", o.FuncName, n.FuncName, SeverityBreaking,
			"the generated constructor was renamed; code calling the old name stops compiling")
	}
	if o.Method != n.Method {
		add("method", o.Method, n.Method, SeverityBreaking,
			"the request method changed; the command now calls the API differently")
	}
	if o.Path != n.Path {
		add("path", o.Path, n.Path, SeverityBreaking,
			"the request path changed; the command now calls a different endpoint")
	}
	if o.Summary != n.Summary {
		add("summary", o.Summary, n.Summary, SeverityCosmetic, "help text only")
	}
	if o.Description != n.Description {
		add("description", o.Description, n.Description, SeverityCosmetic, "help text only")
	}
	if o.Deprecated != n.Deprecated {
		if n.Deprecated {
			add("deprecated", "false", "true", SeverityAdditive,
				"the command still runs but now prints a deprecation notice")
		} else {
			add("deprecated", "true", "false", SeverityCosmetic,
				"the deprecation notice was dropped")
		}
	}
	if o.Ext.Hidden != n.Ext.Hidden {
		add("hidden", boolStr(o.Ext.Hidden), boolStr(n.Ext.Hidden), SeverityCosmetic,
			"only whether the command shows up in help")
	}
	if o.Ext.Confirm != n.Ext.Confirm {
		if n.Ext.Confirm {
			add("confirm", "false", "true", SeverityBreaking,
				"the command now prompts before running, so unattended scripts hang unless they pass --yes")
		} else {
			add("confirm", "true", "false", SeverityAdditive,
				"the confirmation prompt was removed")
		}
	}

	out = append(out, diffBody(o.Body, n.Body)...)
	out = append(out, diffFlags(o.Flags, n.Flags)...)
	return out
}

func diffBody(o, n *Body) []FieldChange {
	switch {
	case o == nil && n == nil:
		return nil
	case o == nil:
		return []FieldChange{{Field: "body", New: bodyStr(n), Severity: SeverityAdditive,
			Detail: "the command gained a request body; existing invocations are unaffected"}}
	case n == nil:
		return []FieldChange{{Field: "body", Old: bodyStr(o), Severity: SeverityBreaking,
			Detail: "the request body is gone; --data and the body flags no longer exist"}}
	}
	var out []FieldChange
	if o.Flat != n.Flat {
		out = append(out, FieldChange{Field: "body shape", Old: bodyStr(o), New: bodyStr(n), Severity: SeverityBreaking,
			Detail: "the body is passed differently now; per-property flags appear or disappear"})
	}
	if o.Wrap != n.Wrap {
		out = append(out, FieldChange{Field: "body wrap", Old: wrapStr(o.Wrap), New: wrapStr(n.Wrap), Severity: SeverityBreaking,
			Detail: "the same flags are now sent at a different place in the JSON body; the API sees a different payload"})
	}
	if o.Required != n.Required && n.Required {
		out = append(out, FieldChange{Field: "body required", Old: "false", New: "true", Severity: SeverityBreaking,
			Detail: "the body is now mandatory; calls that omitted it fail"})
	} else if o.Required != n.Required {
		out = append(out, FieldChange{Field: "body required", Old: "true", New: "false", Severity: SeverityAdditive,
			Detail: "the body is now optional"})
	}
	return out
}

func wrapStr(wrap string) string {
	if wrap == "" {
		return "(body root)"
	}
	return wrap
}

func bodyStr(b *Body) string {
	if b == nil {
		return "none"
	}
	if b.Flat {
		return "flags + --data"
	}
	return "--data only"
}

func diffFlags(oldFlags, newFlags []Flag) []FieldChange {
	oldByName := map[string]Flag{}
	for _, f := range oldFlags {
		oldByName[f.Name] = f
	}
	newByName := map[string]Flag{}
	for _, f := range newFlags {
		newByName[f.Name] = f
	}
	names := map[string]bool{}
	for k := range oldByName {
		names[k] = true
	}
	for k := range newByName {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var out []FieldChange
	for _, name := range sorted {
		o, hadOld := oldByName[name]
		n, hasNew := newByName[name]
		label := "flag --" + name
		switch {
		case !hadOld:
			sev := SeverityAdditive
			detail := "a new optional flag; existing invocations keep working"
			if n.Required && n.Default == "" {
				sev = SeverityBreaking
				detail = "a new required flag; every existing invocation must now pass it"
			}
			out = append(out, FieldChange{Field: label, New: flagStr(n), Severity: sev, Detail: detail})
		case !hasNew:
			out = append(out, FieldChange{Field: label, Old: flagStr(o), Severity: SeverityBreaking,
				Detail: "the flag is gone; invocations passing it now fail"})
		default:
			out = append(out, diffFlag(label, o, n)...)
		}
	}
	return out
}

func diffFlag(label string, o, n Flag) []FieldChange {
	var out []FieldChange
	if o.Type != n.Type {
		out = append(out, FieldChange{Field: label + " type", Old: o.Type, New: n.Type, Severity: SeverityBreaking,
			Detail: "the value is parsed differently now; values that used to be accepted may be rejected"})
	}
	if o.Source != n.Source {
		out = append(out, FieldChange{Field: label + " source", Old: o.Source, New: n.Source, Severity: SeverityBreaking,
			Detail: "the value is sent in a different place in the request"})
	}
	if o.Required != n.Required {
		if n.Required {
			out = append(out, FieldChange{Field: label + " required", Old: "optional", New: "required", Severity: SeverityBreaking,
				Detail: "invocations that omitted this flag now fail"})
		} else {
			out = append(out, FieldChange{Field: label + " required", Old: "required", New: "optional", Severity: SeverityAdditive,
				Detail: "the flag may now be omitted"})
		}
	}
	if o.Default != n.Default {
		out = append(out, FieldChange{Field: label + " default", Old: o.Default, New: n.Default, Severity: SeverityBreaking,
			Detail: "leaving the flag off now sends a different value"})
	}
	if o.Shorthand != n.Shorthand {
		sev := SeverityAdditive
		detail := "the flag gained a one-letter shorthand"
		if o.Shorthand != "" {
			sev = SeverityBreaking
			detail = "the one-letter shorthand changed or was removed; invocations using it fail"
		}
		out = append(out, FieldChange{Field: label + " shorthand", Old: o.Shorthand, New: n.Shorthand, Severity: sev, Detail: detail})
	}
	out = append(out, diffEnum(label, o.Enum, n.Enum)...)
	if o.Description != n.Description {
		out = append(out, FieldChange{Field: label + " description", Old: o.Description, New: n.Description,
			Severity: SeverityCosmetic, Detail: "help text only"})
	}
	return out
}

func diffEnum(label string, o, n []string) []FieldChange {
	if strings.Join(o, ",") == strings.Join(n, ",") {
		return nil
	}
	oldSet := map[string]bool{}
	for _, v := range o {
		oldSet[v] = true
	}
	newSet := map[string]bool{}
	for _, v := range n {
		newSet[v] = true
	}
	var added, removed []string
	for _, v := range n {
		if !oldSet[v] {
			added = append(added, v)
		}
	}
	for _, v := range o {
		if !newSet[v] {
			removed = append(removed, v)
		}
	}
	var out []FieldChange
	if len(removed) > 0 {
		out = append(out, FieldChange{Field: label + " allowed values", Old: strings.Join(o, ", "), New: strings.Join(n, ", "),
			Severity: SeverityBreaking,
			Detail:   "these values are no longer accepted: " + strings.Join(removed, ", ")})
	}
	if len(added) > 0 {
		out = append(out, FieldChange{Field: label + " allowed values", Old: strings.Join(o, ", "), New: strings.Join(n, ", "),
			Severity: SeverityAdditive,
			Detail:   "these values are now accepted as well: " + strings.Join(added, ", ")})
	}
	if len(out) == 0 {
		// Same set, different order: nothing a user can observe.
		out = append(out, FieldChange{Field: label + " allowed values", Old: strings.Join(o, ", "), New: strings.Join(n, ", "),
			Severity: SeverityCosmetic, Detail: "the same values, listed in a different order"})
	}
	return out
}

func flagStr(f Flag) string {
	s := f.Type
	if f.Required {
		s += ", required"
	}
	if f.Default != "" {
		s += ", default " + f.Default
	}
	return s
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Text renders the report for a human: grouped by severity, worst first,
// with "+ added", "~ changed" and "- removed" markers.
func (r Report) Text() string {
	var b strings.Builder
	if r.Severity() == SeverityNone {
		return "No changes: the generated CLI matches the lock file.\n"
	}
	fmt.Fprintf(&b, "Drift detected (overall severity: %s)\n", r.Severity())
	if r.GeneratorChanged {
		fmt.Fprintf(&b, "\nThe generator itself changed (%s -> %s), so the emitted code may differ even where the spec did not.\n",
			r.OldGenerator, r.NewGenerator)
	}
	for _, sev := range []Severity{SeverityBreaking, SeverityAdditive, SeverityCosmetic} {
		var ops []OperationChange
		for _, op := range r.Operations {
			if op.Severity == sev {
				ops = append(ops, op)
			}
		}
		if len(ops) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d):\n", severityHeading(sev), len(ops))
		for _, op := range ops {
			fmt.Fprintf(&b, "  %s %s  (%s %s)\n", marker(op.Kind), displayName(op), op.Method, op.Path)
			for _, c := range op.Changes {
				fmt.Fprintf(&b, "      %s\n", fieldLine(c))
			}
		}
	}
	return b.String()
}

func displayName(op OperationChange) string {
	if op.Command != "" {
		return op.Command
	}
	return op.Key
}

func severityHeading(s Severity) string {
	switch s {
	case SeverityBreaking:
		return "Breaking changes"
	case SeverityAdditive:
		return "Safe additions"
	default:
		return "Help-text only"
	}
}

func marker(k ChangeKind) string {
	switch k {
	case ChangeAdded:
		return "+"
	case ChangeRemoved:
		return "-"
	default:
		return "~"
	}
}

func fieldLine(c FieldChange) string {
	switch {
	case c.Old == "" && c.New != "":
		return fmt.Sprintf("%s: added %s (%s) - %s", c.Field, c.New, c.Severity, c.Detail)
	case c.Old != "" && c.New == "":
		return fmt.Sprintf("%s: removed %s (%s) - %s", c.Field, c.Old, c.Severity, c.Detail)
	default:
		return fmt.Sprintf("%s: %s -> %s (%s) - %s", c.Field, c.Old, c.New, c.Severity, c.Detail)
	}
}

// JSON renders the report as stable JSON for CI, with the aggregate
// severity included.
func (r Report) JSON() ([]byte, error) {
	type payload struct {
		Severity         Severity          `json:"severity"`
		GeneratorChanged bool              `json:"generatorChanged"`
		OldGenerator     string            `json:"oldGenerator,omitempty"`
		NewGenerator     string            `json:"newGenerator,omitempty"`
		Operations       []OperationChange `json:"operations"`
	}
	p := payload{
		Severity:         r.Severity(),
		GeneratorChanged: r.GeneratorChanged,
		OldGenerator:     r.OldGenerator,
		NewGenerator:     r.NewGenerator,
		Operations:       r.Operations,
	}
	if p.Operations == nil {
		p.Operations = []OperationChange{}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("lock: marshal report: %w", err)
	}
	return append(data, '\n'), nil
}
