package lock

import (
	"fmt"
	"strings"

	"github.com/d3servelabs/namefi-astra/projects/oascmd"
)

// ToModel rebuilds the post-hook model of a recorded operation. It is the
// inverse of Compute for one entry, and it is what the additive-only policy
// uses to keep an operation exactly as it was previously generated while the
// rest of the file moves forward.
//
// The reconstruction is faithful for everything that affects the emitted
// command: method, path, command words, constructor name, flags (name,
// source, type, required, default, shorthand, enum, description), body
// shape, and the x-cli-* values.
func ToModel(entry Operation) (Model, error) {
	op := oascmd.Operation{
		ID:          entry.OperationID,
		Method:      entry.Method,
		Path:        entry.Path,
		Summary:     entry.Summary,
		Description: entry.Description,
		Deprecated:  entry.Deprecated,
		Ext: oascmd.Extensions{
			Name:    entry.Ext.Name,
			Group:   entry.Ext.Group,
			Hidden:  entry.Ext.Hidden,
			Confirm: entry.Ext.Confirm,
		},
	}
	if entry.Body != nil {
		op.Body = &oascmd.Body{
			Flat:     entry.Body.Flat,
			Required: entry.Body.Required,
			WrapPath: oascmd.SplitBodyPath(entry.Body.Wrap),
		}
	}
	for _, f := range entry.Flags {
		typ, err := parseType(f.Type)
		if err != nil {
			return Model{}, fmt.Errorf("lock: operation %s: flag --%s: %w", entry.Command, f.Name, err)
		}
		specName := f.APIName
		if specName == "" {
			specName = f.Name
		}
		ext := oascmd.ParamExtensions{Shorthand: f.Shorthand}
		if oascmd.FlagName(specName, oascmd.ParamExtensions{}) != f.Name {
			ext.FlagName = f.Name
		}
		switch f.Source {
		case "path", "query":
			op.Params = append(op.Params, oascmd.Param{
				Name: specName, In: f.Source, Type: typ, Required: f.Required,
				Description: f.Description, Enum: f.Enum, Default: f.Default, Ext: ext,
			})
		case "body":
			if op.Body == nil {
				op.Body = &oascmd.Body{Flat: true}
			}
			op.Body.Props = append(op.Body.Props, oascmd.BodyProp{
				Name: specName, Type: typ, Required: f.Required,
				Description: f.Description, Enum: f.Enum, Default: f.Default, Ext: ext,
			})
		default:
			return Model{}, fmt.Errorf("lock: operation %s: flag --%s has unknown source %q", entry.Command, f.Name, f.Source)
		}
	}

	words := strings.Fields(entry.Command)
	if len(words) == 0 {
		return Model{}, fmt.Errorf("lock: operation %s has no command path", entry.Method+" "+entry.Path)
	}
	return Model{
		FuncName: entry.FuncName,
		Path:     oascmd.CommandPath{Groups: words[:len(words)-1], Name: words[len(words)-1]},
		Op:       op,
	}, nil
}

func parseType(s string) (oascmd.Type, error) {
	array := strings.HasPrefix(s, "[]")
	kind := oascmd.Kind(strings.TrimPrefix(s, "[]"))
	switch kind {
	case oascmd.KindString, oascmd.KindInt, oascmd.KindNumber, oascmd.KindBool:
		return oascmd.Type{Kind: kind, Array: array}, nil
	default:
		return oascmd.Type{}, fmt.Errorf("unknown type %q", s)
	}
}
