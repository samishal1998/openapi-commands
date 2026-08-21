// Package runtime turns a parsed OpenAPI spec into Cobra commands at
// runtime. Argument validation happens against the schema when the command
// runs; there is no compile-time type safety (use the gen package for that).
package runtime

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/d3servelabs/openapi-commands"
	"github.com/d3servelabs/openapi-commands/spec"
)

// Options configures the runtime command builder.
type Options struct {
	// Exec configures how commands execute their HTTP requests.
	Exec oascmd.ExecOptions
	// Hooks is the extension surface; see oascmd.Hooks.
	Hooks oascmd.Hooks
	// NameFunc overrides the command-path derivation;
	// oascmd.DeriveCommandPath when nil.
	NameFunc oascmd.NameFunc
}

// Build parses an OpenAPI 3.0/3.1 document (JSON or YAML) and returns the
// top-level commands of the derived tree.
func Build(data []byte, opts Options) ([]*cobra.Command, error) {
	ops, err := spec.Load(data)
	if err != nil {
		return nil, err
	}
	return BuildFromOperations(ops, opts)
}

// Attach parses a spec and adds the derived commands onto parent.
func Attach(parent *cobra.Command, data []byte, opts Options) error {
	cmds, err := Build(data, opts)
	if err != nil {
		return err
	}
	for _, c := range cmds {
		parent.AddCommand(c)
	}
	return nil
}

// BuildFromOperations builds the command tree from already-normalized
// operations.
func BuildFromOperations(ops []oascmd.Operation, opts Options) ([]*cobra.Command, error) {
	nameFunc := opts.NameFunc
	if nameFunc == nil {
		nameFunc = oascmd.DeriveCommandPath
	}

	root := &cobra.Command{}
	groups := map[string]*cobra.Command{"": root}
	groupCmd := func(path []string) *cobra.Command {
		key := ""
		cmd := root
		for _, word := range path {
			key = key + "/" + word
			child, ok := groups[key]
			if !ok {
				child = &cobra.Command{Use: word}
				cmd.AddCommand(child)
				groups[key] = child
			}
			cmd = child
		}
		return cmd
	}

	for i := range ops {
		op := ops[i]
		keep, err := opts.Hooks.ApplyRead(&op)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		if !keep {
			continue
		}
		if err := opts.Hooks.ApplyBody(&op); err != nil {
			return nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		cmd, err := buildCommand(op, nameFunc(op), opts)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		if cmd == nil {
			continue
		}
		groupCmd(nameFunc(op).Groups).AddCommand(cmd)
	}
	return root.Commands(), nil
}

func buildCommand(op oascmd.Operation, path oascmd.CommandPath, opts Options) (*cobra.Command, error) {
	short := op.Summary
	if short == "" {
		short = fmt.Sprintf("%s %s", op.Method, op.Path)
	}
	cmd := &cobra.Command{
		Use:    path.Name,
		Short:  short,
		Long:   op.Description,
		Hidden: op.Ext.Hidden,
	}
	if op.Deprecated {
		cmd.Deprecated = "this operation is deprecated"
	}

	if opts.Hooks.OnBeforeCreateCommand != nil {
		if err := opts.Hooks.OnBeforeCreateCommand(op, cmd); err != nil {
			if err == oascmd.SkipOperation {
				return nil, nil
			}
			return nil, err
		}
	}

	var binds []*flagBind
	for _, p := range op.Params {
		bind, err := registerFlag(cmd.Flags(), flagSpec{
			name:        oascmd.FlagName(p.Name, p.Ext),
			shorthand:   p.Ext.Shorthand,
			typ:         p.Type,
			required:    p.Required,
			enum:        p.Enum,
			def:         p.Default,
			description: p.Description,
		})
		if err != nil {
			return nil, err
		}
		bind.param = &p
		binds = append(binds, bind)
	}

	var rawData *string
	if op.Body != nil {
		rawData = cmd.Flags().String("data", "", "request body as raw JSON (wins over per-property flags)")
		if op.Body.Flat {
			for _, prop := range op.Body.Props {
				bind, err := registerFlag(cmd.Flags(), flagSpec{
					name:        oascmd.FlagName(prop.Name, prop.Ext),
					shorthand:   prop.Ext.Shorthand,
					typ:         prop.Type,
					enum:        prop.Enum,
					def:         prop.Default,
					description: prop.Description,
				})
				if err != nil {
					return nil, err
				}
				prop := prop
				bind.prop = &prop
				binds = append(binds, bind)
			}
		}
	}

	jsonRaw := cmd.Flags().Bool("json", false, "print the raw JSON response")
	var yes *bool
	if op.Ext.Confirm {
		yes = cmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateBinds(cmd.Flags(), binds); err != nil {
			return err
		}
		if op.Ext.Confirm && (yes == nil || !*yes) {
			ok, err := oascmd.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(),
				fmt.Sprintf("About to run %s %s. Continue?", op.Method, op.Path))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("aborted")
			}
		}

		req := oascmd.Request{
			Method:     op.Method,
			Path:       op.Path,
			PathParams: map[string]string{},
			Query:      url.Values{},
		}
		body := map[string]any{}
		for _, bind := range binds {
			bind.collect(cmd.Flags(), &req, body)
		}
		if op.Body != nil {
			if rawData != nil && *rawData != "" {
				req.RawBody = []byte(*rawData)
			} else if len(body) > 0 {
				req.Body = oascmd.NestBody(op.Body.WrapPath, body)
			} else if op.Body.Required {
				return fmt.Errorf("a request body is required: pass --data or the body flags")
			}
		}

		exec := opts.Exec
		exec.Raw = exec.Raw || *jsonRaw
		if exec.Out == nil {
			exec.Out = os.Stdout
		}
		return oascmd.Execute(cmd.Context(), exec, req)
	}

	if opts.Hooks.OnAfterCreateCommand != nil {
		if err := opts.Hooks.OnAfterCreateCommand(op, cmd); err != nil {
			return nil, err
		}
	}
	return cmd, nil
}

// flagSpec is what registerFlag needs to declare one flag.
type flagSpec struct {
	name        string
	shorthand   string
	typ         oascmd.Type
	required    bool
	enum        []string
	def         string
	description string
}

// flagBind connects a registered flag back to the parameter or body property
// it fills in.
type flagBind struct {
	spec  flagSpec
	param *oascmd.Param
	prop  *oascmd.BodyProp
}

func registerFlag(fs *pflag.FlagSet, s flagSpec) (*flagBind, error) {
	usage := s.description
	if len(s.enum) > 0 {
		usage = strings.TrimSpace(usage + " (one of: " + strings.Join(s.enum, ", ") + ")")
	}
	if s.typ.Array {
		switch s.typ.Kind {
		case oascmd.KindInt:
			fs.Int64SliceP(s.name, s.shorthand, nil, usage)
		case oascmd.KindNumber:
			fs.Float64SliceP(s.name, s.shorthand, nil, usage)
		case oascmd.KindBool:
			fs.BoolSliceP(s.name, s.shorthand, nil, usage)
		default:
			fs.StringSliceP(s.name, s.shorthand, nil, usage)
		}
	} else {
		switch s.typ.Kind {
		case oascmd.KindInt:
			def := int64(0)
			if s.def != "" {
				parsed, err := strconv.ParseInt(s.def, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("flag --%s: invalid integer default %q", s.name, s.def)
				}
				def = parsed
			}
			fs.Int64P(s.name, s.shorthand, def, usage)
		case oascmd.KindNumber:
			def := float64(0)
			if s.def != "" {
				parsed, err := strconv.ParseFloat(s.def, 64)
				if err != nil {
					return nil, fmt.Errorf("flag --%s: invalid number default %q", s.name, s.def)
				}
				def = parsed
			}
			fs.Float64P(s.name, s.shorthand, def, usage)
		case oascmd.KindBool:
			fs.BoolP(s.name, s.shorthand, s.def == "true", usage)
		default:
			fs.StringP(s.name, s.shorthand, s.def, usage)
		}
	}
	return &flagBind{spec: s}, nil
}

// validateBinds enforces required flags and enum membership at run time.
func validateBinds(fs *pflag.FlagSet, binds []*flagBind) error {
	for _, bind := range binds {
		s := bind.spec
		changed := fs.Changed(s.name)
		if s.required && !changed && s.def == "" {
			return fmt.Errorf("required flag --%s not set", s.name)
		}
		if len(s.enum) > 0 && (changed || s.def != "") {
			for _, v := range flagValues(fs, s) {
				if !slices.Contains(s.enum, v) {
					return fmt.Errorf("invalid value %q for --%s (one of: %s)",
						v, s.name, strings.Join(s.enum, ", "))
				}
			}
		}
	}
	return nil
}

// collect writes the flag's value into the request (path/query) or body map.
// Optional flags left unset and lacking a default are omitted.
func (b *flagBind) collect(fs *pflag.FlagSet, req *oascmd.Request, body map[string]any) {
	s := b.spec
	changed := fs.Changed(s.name)
	if !changed && s.def == "" && !s.required {
		return
	}
	switch {
	case b.param != nil && b.param.In == "path":
		req.PathParams[b.param.Name] = flagValues(fs, s)[0]
	case b.param != nil:
		for _, v := range flagValues(fs, s) {
			req.Query.Add(b.param.Name, v)
		}
	case b.prop != nil:
		if !changed && s.def == "" {
			return
		}
		body[b.prop.Name] = flagAny(fs, s)
	}
}

// flagValues returns the flag's value(s) as strings, for path/query use and
// enum validation.
func flagValues(fs *pflag.FlagSet, s flagSpec) []string {
	if s.typ.Array {
		switch s.typ.Kind {
		case oascmd.KindInt:
			vs, _ := fs.GetInt64Slice(s.name)
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = strconv.FormatInt(v, 10)
			}
			return out
		case oascmd.KindNumber:
			vs, _ := fs.GetFloat64Slice(s.name)
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = strconv.FormatFloat(v, 'f', -1, 64)
			}
			return out
		case oascmd.KindBool:
			vs, _ := fs.GetBoolSlice(s.name)
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = strconv.FormatBool(v)
			}
			return out
		default:
			vs, _ := fs.GetStringSlice(s.name)
			return vs
		}
	}
	switch s.typ.Kind {
	case oascmd.KindInt:
		v, _ := fs.GetInt64(s.name)
		return []string{strconv.FormatInt(v, 10)}
	case oascmd.KindNumber:
		v, _ := fs.GetFloat64(s.name)
		return []string{strconv.FormatFloat(v, 'f', -1, 64)}
	case oascmd.KindBool:
		v, _ := fs.GetBool(s.name)
		return []string{strconv.FormatBool(v)}
	default:
		v, _ := fs.GetString(s.name)
		return []string{v}
	}
}

// flagAny returns the flag's value with its native Go type, for the JSON
// body.
func flagAny(fs *pflag.FlagSet, s flagSpec) any {
	if s.typ.Array {
		switch s.typ.Kind {
		case oascmd.KindInt:
			v, _ := fs.GetInt64Slice(s.name)
			return v
		case oascmd.KindNumber:
			v, _ := fs.GetFloat64Slice(s.name)
			return v
		case oascmd.KindBool:
			v, _ := fs.GetBoolSlice(s.name)
			return v
		default:
			v, _ := fs.GetStringSlice(s.name)
			return v
		}
	}
	switch s.typ.Kind {
	case oascmd.KindInt:
		v, _ := fs.GetInt64(s.name)
		return v
	case oascmd.KindNumber:
		v, _ := fs.GetFloat64(s.name)
		return v
	case oascmd.KindBool:
		v, _ := fs.GetBool(s.name)
		return v
	default:
		v, _ := fs.GetString(s.name)
		return v
	}
}
