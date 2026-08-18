// Package oascmd maps OpenAPI operations onto Cobra command trees.
//
// It contains the normalized operation model shared by both consumption
// modes, the command-name derivation rules, the HTTP executor, and the hook
// surface. The two modes live in subpackages:
//
//   - runtime:  parse a spec at runtime and get []*cobra.Command back.
//   - gen:      emit strongly-typed Go source at build time (cmd/oascmd-gen).
//
// See the README for the full documentation.
package oascmd

// Kind is the scalar kind of a parameter or body property.
type Kind string

const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindNumber Kind = "number"
	KindBool   Kind = "bool"
)

// Type describes the CLI-facing type of a parameter or body property.
// Array wraps the scalar Kind (e.g. {KindString, true} is a string array).
type Type struct {
	Kind  Kind
	Array bool
}

// Extensions carries the operation-level x-cli-* OpenAPI extension values.
type Extensions struct {
	// Name (x-cli-name) overrides the derived command words for the
	// operation. Space-separated words become nested command levels.
	Name string
	// Group (x-cli-group) overrides the tag-derived command group.
	// Space-separated words become nested command levels.
	Group string
	// Hidden (x-cli-hidden) hides the command from help output.
	Hidden bool
	// Skip (x-cli-skip) drops the operation entirely.
	Skip bool
	// Confirm (x-cli-confirm) prompts for confirmation before executing
	// unless --yes is passed.
	Confirm bool
}

// ParamExtensions carries the parameter-level x-cli-* extension values.
type ParamExtensions struct {
	// FlagName (x-cli-flag-name) overrides the derived flag name.
	FlagName string
	// Shorthand (x-cli-shorthand) is a one-letter flag shorthand.
	Shorthand string
}

// Param is a path or query parameter mapped to a flag.
type Param struct {
	// Name is the OpenAPI parameter name (used verbatim in the request).
	Name string
	// In is "path" or "query".
	In          string
	Type        Type
	Required    bool
	Description string
	// Enum, when non-empty, restricts accepted values (validated before
	// the request is sent).
	Enum []string
	// Default is the schema default rendered as a string, or "" when the
	// schema declares none. It becomes the flag default.
	Default string
	Ext     ParamExtensions
}

// BodyProp is a top-level property of a flat JSON request body, mapped to a
// flag.
type BodyProp struct {
	Name        string
	Type        Type
	Required    bool
	Description string
	Enum        []string
	Default     string
	Ext         ParamExtensions
}

// Body describes the application/json request body of an operation.
type Body struct {
	Required bool
	// Flat is true when the schema is an object whose top-level properties
	// are all scalars or arrays of scalars. Flat bodies get one flag per
	// property in addition to --data; non-flat bodies get only --data.
	Flat        bool
	Props       []BodyProp
	Description string
}

// Operation is the normalized view of one OpenAPI operation. Both the
// runtime builder and the buildtime generator consume this model.
type Operation struct {
	// ID is the operationId. When empty, naming falls back to the HTTP
	// method plus the static path segments.
	ID string
	// Method is the upper-case HTTP method.
	Method string
	// Path is the OpenAPI path template, e.g. "/pets/{petId}".
	Path        string
	Tags        []string
	Summary     string
	Description string
	Deprecated  bool
	Params      []Param
	Body        *Body
	Ext         Extensions
}
