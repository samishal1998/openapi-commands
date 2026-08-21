// Package lock records what the generator produced, so a later run can tell
// what changed about the CLI surface and how badly.
//
// The lock file is a JSON snapshot of the generated command surface: one
// entry per operation with its command path, constructor name, flags, body
// shape and x-cli-* values, plus a digest of the spec it came from. It is
// written next to the generated file (oascmd.lock.json by default) and is
// meant to be committed, so a pull request diff shows exactly how the CLI
// changed.
//
// The snapshot is computed from the POST-hook model — after OnReadOperation
// and OnEmitOperation have filtered, renamed or mutated operations — because
// that is what was actually generated.
//
// Compute/Load/Write/Diff are the whole API; see Diff for the severity
// ladder.
package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/d3servelabs/openapi-commands"
)

// Version is the lock schema version. It is bumped when the on-disk shape
// changes incompatibly; a lock written by a newer schema is refused rather
// than misread.
const Version = 1

// GeneratorVersion identifies the emitter. It is recorded in the lock so a
// change to the code generator is itself detectable drift, even when the
// spec did not move.
const GeneratorVersion = "oascmd-gen/1"

// DefaultFileName is the lock file name written alongside the generated
// output.
const DefaultFileName = "oascmd.lock.json"

// Model is the post-hook view of one generated command, i.e. exactly what
// gen emitted. gen.CommandModel converts to this.
type Model struct {
	// FuncName is the emitted constructor name (empty when the caller has
	// no constructors, e.g. runtime verification).
	FuncName string
	// Path is the command path in the tree.
	Path oascmd.CommandPath
	// Op is the normalized operation, post-hook.
	Op oascmd.Operation
}

// Lock is the on-disk snapshot.
//
// Deliberately absent: a generatedAt timestamp. A timestamp would change on
// every regeneration, so every run would produce a diff even when the CLI
// surface is identical — exactly the noise this file exists to remove. The
// file records *what* was generated, not *when*; git already knows when.
type Lock struct {
	// LockVersion is the schema version (see Version).
	LockVersion int `json:"lockVersion"`
	// GeneratorVersion identifies the emitter that wrote this lock.
	GeneratorVersion string `json:"generatorVersion"`
	// SpecDigest is sha256 of the normalized operation model read from
	// the spec (not of the raw bytes, so reformatting the spec is not
	// drift).
	SpecDigest string `json:"specDigest"`
	// Operations is keyed by operation identity: the operationId when the
	// spec has one, else "METHOD path".
	Operations map[string]Operation `json:"operations"`
}

// Operation is the recorded surface of one generated command.
type Operation struct {
	// OperationID is the spec operationId, empty when absent.
	OperationID string `json:"operationId,omitempty"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	// Command is the space-joined command path, e.g. "pets list".
	Command string `json:"command"`
	// FuncName is the emitted constructor name.
	FuncName string `json:"funcName,omitempty"`
	// Summary and Description are help text only; changes to them are
	// cosmetic.
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`
	// Flags are sorted by name.
	Flags []Flag `json:"flags,omitempty"`
	Body  *Body  `json:"body,omitempty"`
	Ext   Ext    `json:"ext"`
	// Digest is a sha256 prefix over everything above, so a diff can say
	// "this operation changed" without comparing field by field.
	Digest string `json:"digest"`
}

// Flag is one CLI flag: a path/query parameter or a flat body property.
type Flag struct {
	// Name is the flag name as typed on the command line, without "--".
	Name string `json:"name"`
	// APIName is the underlying OpenAPI parameter or property name, when
	// it differs from the flag name (x-cli-flag-name). Recorded so the
	// entry can be replayed exactly.
	APIName string `json:"apiName,omitempty"`
	// Source is "path", "query" or "body".
	Source string `json:"source"`
	// Type is the CLI type, e.g. "string", "[]int".
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Shorthand   string   `json:"shorthand,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Body records the request body shape.
type Body struct {
	// Flat is true when the body is exploded into one flag per property
	// (in addition to --data); false means --data only.
	Flat     bool `json:"flat"`
	Required bool `json:"required,omitempty"`
	// Wrap is the dotted envelope path the flags are nested under before
	// the request is sent ("json", "data.attributes"). Empty means the
	// body root. It is recorded because it changes what is sent while the
	// flag names stay the same.
	Wrap string `json:"wrap,omitempty"`
}

// Ext records the operation-level x-cli-* values that survived the hooks.
type Ext struct {
	Name    string `json:"name,omitempty"`
	Group   string `json:"group,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

// Key returns the stable identity of an operation: its operationId when the
// spec provides one, else "METHOD path".
func Key(op oascmd.Operation) string {
	if op.ID != "" {
		return op.ID
	}
	return op.Method + " " + op.Path
}

// Compute builds a Lock from the post-hook models.
func Compute(models []Model) Lock {
	l := Lock{
		LockVersion:      Version,
		GeneratorVersion: GeneratorVersion,
		Operations:       map[string]Operation{},
	}
	for _, m := range models {
		entry := computeOperation(m)
		key := Key(m.Op)
		// Defensive: identical keys would silently collapse. Suffix
		// duplicates so the lock still describes every command.
		if _, clash := l.Operations[key]; clash {
			for n := 2; ; n++ {
				alt := fmt.Sprintf("%s#%d", key, n)
				if _, clash := l.Operations[alt]; !clash {
					key = alt
					break
				}
			}
		}
		l.Operations[key] = entry
	}
	l.SpecDigest = specDigest(l.Operations)
	return l
}

func computeOperation(m Model) Operation {
	entry := Operation{
		OperationID: m.Op.ID,
		Method:      m.Op.Method,
		Path:        m.Op.Path,
		Command:     strings.TrimSpace(strings.Join(append(append([]string{}, m.Path.Groups...), m.Path.Name), " ")),
		FuncName:    m.FuncName,
		Summary:     m.Op.Summary,
		Description: m.Op.Description,
		Deprecated:  m.Op.Deprecated,
		Ext: Ext{
			Name:    m.Op.Ext.Name,
			Group:   m.Op.Ext.Group,
			Hidden:  m.Op.Ext.Hidden,
			Confirm: m.Op.Ext.Confirm,
		},
	}
	for _, p := range m.Op.Params {
		entry.Flags = append(entry.Flags, Flag{
			Name:        oascmd.FlagName(p.Name, p.Ext),
			APIName:     apiName(p.Name, oascmd.FlagName(p.Name, p.Ext)),
			Source:      p.In,
			Type:        typeString(p.Type),
			Required:    p.Required,
			Default:     p.Default,
			Shorthand:   p.Ext.Shorthand,
			Enum:        append([]string{}, p.Enum...),
			Description: p.Description,
		})
	}
	if m.Op.Body != nil {
		entry.Body = &Body{
			Flat:     m.Op.Body.Flat,
			Required: m.Op.Body.Required,
			Wrap:     strings.Join(m.Op.Body.WrapPath, "."),
		}
		if m.Op.Body.Flat {
			for _, prop := range m.Op.Body.Props {
				entry.Flags = append(entry.Flags, Flag{
					Name:        oascmd.FlagName(prop.Name, prop.Ext),
					APIName:     apiName(prop.Name, oascmd.FlagName(prop.Name, prop.Ext)),
					Source:      "body",
					Type:        typeString(prop.Type),
					Required:    prop.Required,
					Default:     prop.Default,
					Shorthand:   prop.Ext.Shorthand,
					Enum:        append([]string{}, prop.Enum...),
					Description: prop.Description,
				})
			}
		}
	}
	sort.SliceStable(entry.Flags, func(i, j int) bool { return entry.Flags[i].Name < entry.Flags[j].Name })
	for i := range entry.Flags {
		if len(entry.Flags[i].Enum) == 0 {
			entry.Flags[i].Enum = nil
		}
	}
	entry.Digest = digestOf(entry)
	return entry
}

// apiName records the underlying spec name only when it differs from the
// flag name, so the common case stays out of the JSON diff.
func apiName(specName, flag string) string {
	if specName == flag {
		return ""
	}
	return specName
}

func typeString(t oascmd.Type) string {
	if t.Array {
		return "[]" + string(t.Kind)
	}
	return string(t.Kind)
}

// digestOf hashes an operation entry (with its own digest field cleared).
func digestOf(entry Operation) string {
	entry.Digest = ""
	data, err := json.Marshal(entry)
	if err != nil {
		// Operation contains only marshalable types.
		panic("lock: marshal operation: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// specDigest hashes the whole normalized surface, keyed and sorted so it is
// independent of spec formatting and operation order.
func specDigest(ops map[string]Operation) string {
	keys := make([]string, 0, len(ops))
	for k := range ops {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, ops[k].Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Marshal renders the lock as deterministic JSON: sorted keys (encoding/json
// sorts map keys), 2-space indent, trailing newline.
func Marshal(l Lock) ([]byte, error) {
	if l.Operations == nil {
		l.Operations = map[string]Operation{}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("lock: marshal: %w", err)
	}
	return append(data, '\n'), nil
}

// Unmarshal parses a lock file, rejecting a schema version this build does
// not understand.
func Unmarshal(data []byte) (Lock, error) {
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return Lock{}, fmt.Errorf("lock: parse: %w", err)
	}
	if l.LockVersion == 0 {
		return Lock{}, fmt.Errorf("lock: missing lockVersion (not an oascmd lock file?)")
	}
	if l.LockVersion > Version {
		return Lock{}, fmt.Errorf("lock: file uses lockVersion %d but this build understands up to %d; upgrade oascmd-gen", l.LockVersion, Version)
	}
	if l.LockVersion < Version {
		return Lock{}, fmt.Errorf("lock: file uses lockVersion %d, which this build no longer reads; delete it and regenerate", l.LockVersion)
	}
	if l.Operations == nil {
		l.Operations = map[string]Operation{}
	}
	return l, nil
}

// Load reads a lock file. The bool reports whether the file exists; a
// missing file is not an error (the first run has no lock).
func Load(path string) (Lock, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Lock{}, false, nil
	}
	if err != nil {
		return Lock{}, false, fmt.Errorf("lock: read %s: %w", path, err)
	}
	l, err := Unmarshal(data)
	if err != nil {
		return Lock{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return l, true, nil
}

// Write writes a lock file deterministically.
func Write(path string, l Lock) error {
	data, err := Marshal(l)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("lock: write %s: %w", path, err)
	}
	return nil
}
