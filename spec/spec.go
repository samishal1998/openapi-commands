// Package spec parses OpenAPI 3.0/3.1 documents (JSON or YAML) into the
// normalized oascmd operation model.
//
// It is built on github.com/pb33f/libopenapi, which models both OpenAPI 3.0
// and 3.1 natively (see the project README for the parser-selection
// rationale).
package spec

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"gopkg.in/yaml.v3"

	"github.com/samishal1998/openapi-commands"
)

// Load parses an OpenAPI 3.0/3.1 document (JSON or YAML) and returns its
// operations in the normalized model, ordered by path then method.
func Load(data []byte) ([]oascmd.Operation, error) {
	doc, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	model, errs := doc.BuildV3Model()
	if model == nil {
		return nil, fmt.Errorf("build spec model: %w", joinErrs(errs))
	}

	var ops []oascmd.Operation
	if model.Model.Paths == nil {
		return ops, nil
	}
	for pathPair := model.Model.Paths.PathItems.First(); pathPair != nil; pathPair = pathPair.Next() {
		path := pathPair.Key()
		item := pathPair.Value()
		for opPair := item.GetOperations().First(); opPair != nil; opPair = opPair.Next() {
			method := strings.ToUpper(opPair.Key())
			op, err := convertOperation(method, path, item, opPair.Value())
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			ops = append(ops, op)
		}
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops, nil
}

func convertOperation(method, path string, item *v3.PathItem, src *v3.Operation) (oascmd.Operation, error) {
	op := oascmd.Operation{
		ID:          src.OperationId,
		Method:      method,
		Path:        path,
		Tags:        src.Tags,
		Summary:     src.Summary,
		Description: src.Description,
	}
	if src.Deprecated != nil {
		op.Deprecated = *src.Deprecated
	}
	op.Ext = operationExtensions(src.Extensions)

	// Path-level parameters apply to all operations; operation-level
	// parameters with the same name+in win.
	seen := map[string]bool{}
	for _, p := range src.Parameters {
		param, ok, err := convertParam(p)
		if err != nil {
			return op, err
		}
		if ok {
			op.Params = append(op.Params, param)
			seen[p.In+":"+p.Name] = true
		}
	}
	for _, p := range item.Parameters {
		if seen[p.In+":"+p.Name] {
			continue
		}
		param, ok, err := convertParam(p)
		if err != nil {
			return op, err
		}
		if ok {
			op.Params = append(op.Params, param)
		}
	}

	body, err := convertBody(src.RequestBody)
	if err != nil {
		return op, err
	}
	op.Body = body
	return op, nil
}

func convertParam(p *v3.Parameter) (oascmd.Param, bool, error) {
	if p.In != "path" && p.In != "query" {
		// header/cookie parameters are not mapped to flags.
		return oascmd.Param{}, false, nil
	}
	param := oascmd.Param{
		Name:        p.Name,
		In:          p.In,
		Description: p.Description,
		Ext:         paramExtensions(p.Extensions),
	}
	// Checked before the schema is converted: the whole point of skipping a
	// parameter is usually that its schema has no flag form, so converting
	// it first would fail before the opt-out could take effect.
	if param.Ext.Skip {
		return oascmd.Param{}, false, nil
	}
	if p.Required != nil {
		param.Required = *p.Required
	}
	if p.In == "path" {
		param.Required = true
	}
	var schema *base.Schema
	if p.Schema != nil {
		schema = p.Schema.Schema()
	}
	typ, enum, def, err := convertSchema(schema)
	if err != nil {
		return param, false, fmt.Errorf("parameter %q: %w", p.Name, err)
	}
	param.Type = typ
	param.Enum = enum
	param.Default = def
	return param, true, nil
}

func convertBody(rb *v3.RequestBody) (*oascmd.Body, error) {
	if rb == nil || rb.Content == nil {
		return nil, nil
	}
	media, ok := rb.Content.Get("application/json")
	if !ok || media == nil || media.Schema == nil {
		return nil, nil
	}
	body := &oascmd.Body{Description: rb.Description}
	if rb.Required != nil {
		body.Required = *rb.Required
	}
	body.Ext = bodyExtensions(rb.Extensions)
	schema := media.Schema.Schema()
	if schema == nil {
		return body, nil
	}
	if body.Ext.Unwrap == "" && body.Ext.Wrap == "" {
		// The extensions may also sit on the schema itself, which is
		// where a $ref'd envelope can declare them.
		body.Ext = bodyExtensions(schema.Extensions)
	}
	if !hasType(schema, "object") || schema.Properties == nil {
		return body, nil
	}
	body.Props = convertBodyProps(schema)
	body.Flat = len(body.Props) > 0
	return body, nil
}

// convertBodyProps converts the properties of an object schema. A property
// that is itself an object keeps its children in BodyProp.Object so
// oascmd.ResolveBody can unwrap an envelope; a property representable as
// neither scalar nor object is dropped, which leaves the body non-flat.
func convertBodyProps(schema *base.Schema) []oascmd.BodyProp {
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	var props []oascmd.BodyProp
	for pair := schema.Properties.First(); pair != nil; pair = pair.Next() {
		propSchema := pair.Value().Schema()
		prop := oascmd.BodyProp{Name: pair.Key(), Required: required[pair.Key()]}
		if propSchema != nil {
			prop.Description = propSchema.Description
			prop.Ext = paramExtensions(propSchema.Extensions)
		}
		if propSchema != nil && hasType(propSchema, "object") && propSchema.Properties != nil {
			prop.Object = convertBodyProps(propSchema)
			if prop.Object == nil {
				// An object with no representable children
				// cannot back flags at all.
				return nil
			}
			props = append(props, prop)
			continue
		}
		typ, enum, def, err := convertSchema(propSchema)
		if err != nil {
			return nil
		}
		prop.Type = typ
		prop.Enum = enum
		prop.Default = def
		props = append(props, prop)
	}
	return props
}

// convertSchema maps a scalar or array-of-scalar schema to a CLI type. It
// returns an error for shapes flags cannot represent (nested objects, arrays
// of arrays, missing types), which callers treat as "fall back to --data".
func convertSchema(schema *base.Schema) (oascmd.Type, []string, string, error) {
	if schema == nil {
		return oascmd.Type{Kind: oascmd.KindString}, nil, "", nil
	}
	kind, array, err := schemaKind(schema)
	if err != nil {
		return oascmd.Type{}, nil, "", err
	}
	typ := oascmd.Type{Kind: kind, Array: array}

	var enum []string
	enumSource := schema.Enum
	if array && schema.Items != nil && schema.Items.IsA() {
		if itemSchema := schema.Items.A.Schema(); itemSchema != nil && len(itemSchema.Enum) > 0 {
			enumSource = itemSchema.Enum
		}
	}
	for _, node := range enumSource {
		value, err := scalarNode(node)
		if err != nil {
			return typ, nil, "", fmt.Errorf("enum: %w", err)
		}
		enum = append(enum, value)
	}

	var def string
	if schema.Default != nil && !array {
		value, err := scalarNode(schema.Default)
		if err != nil {
			return typ, enum, "", fmt.Errorf("default: %w", err)
		}
		def = value
	}
	return typ, enum, def, nil
}

func schemaKind(schema *base.Schema) (oascmd.Kind, bool, error) {
	primary := ""
	for _, t := range schema.Type {
		if t == "null" {
			continue // 3.1 nullable union
		}
		if primary != "" {
			return "", false, fmt.Errorf("union type %v not representable as a flag", schema.Type)
		}
		primary = t
	}
	switch primary {
	case "", "string":
		return oascmd.KindString, false, nil
	case "integer":
		return oascmd.KindInt, false, nil
	case "number":
		return oascmd.KindNumber, false, nil
	case "boolean":
		return oascmd.KindBool, false, nil
	case "array":
		if schema.Items == nil || !schema.Items.IsA() {
			return oascmd.KindString, true, nil
		}
		itemKind, itemArray, err := schemaKind(schema.Items.A.Schema())
		if err != nil {
			return "", false, err
		}
		if itemArray {
			return "", false, fmt.Errorf("nested arrays not representable as a flag")
		}
		return itemKind, true, nil
	default:
		return "", false, fmt.Errorf("type %q not representable as a flag", primary)
	}
}

func scalarNode(node *yaml.Node) (string, error) {
	if node == nil {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("non-scalar value")
	}
	return node.Value, nil
}

func operationExtensions(ext extMap) oascmd.Extensions {
	return oascmd.Extensions{
		Name:    extString(ext, "x-cli-name"),
		Group:   extString(ext, "x-cli-group"),
		Hidden:  extBool(ext, "x-cli-hidden"),
		Skip:    extBool(ext, "x-cli-skip"),
		Confirm: extBool(ext, "x-cli-confirm"),
	}
}

func bodyExtensions(ext extMap) oascmd.BodyExtensions {
	return oascmd.BodyExtensions{
		Unwrap: extScalar(ext, "x-cli-body-unwrap"),
		Wrap:   extString(ext, "x-cli-body-wrap"),
	}
}

// extScalar reads an extension whose value may be a string or a boolean
// (x-cli-body-unwrap accepts both: "payload", true, false).
func extScalar(ext extMap, key string) string {
	if ext == nil {
		return ""
	}
	node, ok := ext.Get(key)
	if !ok || node == nil {
		return ""
	}
	return node.Value
}

func paramExtensions(ext extMap) oascmd.ParamExtensions {
	return oascmd.ParamExtensions{
		FlagName:  extString(ext, "x-cli-flag-name"),
		Shorthand: extString(ext, "x-cli-shorthand"),
		Skip:      extBool(ext, "x-cli-skip"),
	}
}

// extMap abstracts the ordered extension map from libopenapi high-level
// types.
type extMap interface {
	Get(key string) (*yaml.Node, bool)
}

func extString(ext extMap, key string) string {
	if ext == nil {
		return ""
	}
	node, ok := ext.Get(key)
	if !ok || node == nil {
		return ""
	}
	var value string
	if err := node.Decode(&value); err != nil {
		return ""
	}
	return value
}

func extBool(ext extMap, key string) bool {
	if ext == nil {
		return false
	}
	node, ok := ext.Get(key)
	if !ok || node == nil {
		return false
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false
	}
	return value
}

func hasType(schema *base.Schema, want string) bool {
	if len(schema.Type) == 0 {
		return want == "object" && schema.Properties != nil
	}
	for _, t := range schema.Type {
		if t == want {
			return true
		}
	}
	return false
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return fmt.Errorf("unknown error")
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}
