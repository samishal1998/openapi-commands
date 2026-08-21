package spec

import (
	"fmt"
	"sort"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"

	"github.com/d3servelabs/openapi-commands"
)

// ComponentField is one property of a component object schema, in a shape
// the buildtime generator can turn into a Go struct field.
type ComponentField struct {
	// Name is the JSON property name.
	Name string
	// Type is set for scalar and array-of-scalar properties.
	Type oascmd.Type
	// Ref is the referenced component name when the property is a $ref
	// (or an array of $ref, with Type.Array true).
	Ref string
	// Raw is true when the property shape is not representable as a
	// typed field (nested inline object, union, ...); the generator
	// emits json.RawMessage for it.
	Raw         bool
	Required    bool
	Description string
}

// ComponentSchema is a named object schema under components.schemas.
type ComponentSchema struct {
	Name        string
	Description string
	Fields      []ComponentField
}

// LoadWithSchemas is Load plus the object schemas under components.schemas
// (non-object components are skipped), sorted by name.
func LoadWithSchemas(data []byte) ([]oascmd.Operation, []ComponentSchema, error) {
	ops, err := Load(data)
	if err != nil {
		return nil, nil, err
	}

	doc, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse spec: %w", err)
	}
	model, errs := doc.BuildV3Model()
	if model == nil {
		return nil, nil, fmt.Errorf("build spec model: %w", joinErrs(errs))
	}

	var schemas []ComponentSchema
	if model.Model.Components == nil || model.Model.Components.Schemas == nil {
		return ops, nil, nil
	}
	for pair := model.Model.Components.Schemas.First(); pair != nil; pair = pair.Next() {
		schema := pair.Value().Schema()
		if schema == nil || !hasType(schema, "object") || schema.Properties == nil {
			continue
		}
		comp := ComponentSchema{Name: pair.Key(), Description: schema.Description}
		required := map[string]bool{}
		for _, name := range schema.Required {
			required[name] = true
		}
		for propPair := schema.Properties.First(); propPair != nil; propPair = propPair.Next() {
			comp.Fields = append(comp.Fields,
				componentField(propPair.Key(), propPair.Value(), required[propPair.Key()]))
		}
		schemas = append(schemas, comp)
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Name < schemas[j].Name })
	return ops, schemas, nil
}

func componentField(name string, proxy *base.SchemaProxy, required bool) ComponentField {
	field := ComponentField{Name: name, Required: required}
	if ref := refName(proxy); ref != "" {
		field.Ref = ref
		return field
	}
	schema := proxy.Schema()
	if schema == nil {
		field.Raw = true
		return field
	}
	field.Description = schema.Description
	if hasType(schema, "array") && schema.Items != nil && schema.Items.IsA() {
		if ref := refName(schema.Items.A); ref != "" {
			field.Ref = ref
			field.Type.Array = true
			return field
		}
	}
	typ, _, _, err := convertSchema(schema)
	if err != nil {
		field.Raw = true
		return field
	}
	field.Type = typ
	return field
}

func refName(proxy *base.SchemaProxy) string {
	if proxy == nil || !proxy.IsReference() {
		return ""
	}
	ref := proxy.GetReference()
	const prefix = "#/components/schemas/"
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):]
	}
	return ""
}
