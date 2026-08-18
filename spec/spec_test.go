package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/d3servelabs/namefi-astra/projects/oascmd"
)

func loadFixture(t *testing.T) []oascmd.Operation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func findOp(t *testing.T, ops []oascmd.Operation, id string) oascmd.Operation {
	t.Helper()
	for _, op := range ops {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("operation %q not found", id)
	return oascmd.Operation{}
}

func findParam(t *testing.T, op oascmd.Operation, name string) oascmd.Param {
	t.Helper()
	for _, p := range op.Params {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("param %q not found on %s", name, op.ID)
	return oascmd.Param{}
}

func TestLoadYAML(t *testing.T) {
	ops := loadFixture(t)
	if len(ops) != 12 {
		t.Fatalf("got %d operations, want 12", len(ops))
	}

	list := findOp(t, ops, "listPets")
	if list.Method != "GET" || list.Path != "/pets" {
		t.Errorf("listPets = %s %s", list.Method, list.Path)
	}

	limit := findParam(t, list, "limit")
	if limit.Type.Kind != oascmd.KindInt || limit.Type.Array {
		t.Errorf("limit type = %+v, want int scalar", limit.Type)
	}
	if limit.Default != "20" {
		t.Errorf("limit default = %q, want 20", limit.Default)
	}
	if limit.Required {
		t.Error("limit should not be required")
	}

	status := findParam(t, list, "status")
	wantEnum := []string{"available", "pending", "sold"}
	if len(status.Enum) != 3 {
		t.Fatalf("status enum = %v, want %v", status.Enum, wantEnum)
	}
	for i, e := range wantEnum {
		if status.Enum[i] != e {
			t.Errorf("status enum[%d] = %q, want %q", i, status.Enum[i], e)
		}
	}

	tags := findParam(t, list, "tags")
	if tags.Type.Kind != oascmd.KindString || !tags.Type.Array {
		t.Errorf("tags type = %+v, want string array", tags.Type)
	}

	verbose := findParam(t, list, "verbose")
	if verbose.Type.Kind != oascmd.KindBool {
		t.Errorf("verbose type = %+v, want bool", verbose.Type)
	}
	if verbose.Ext.FlagName != "long" || verbose.Ext.Shorthand != "l" {
		t.Errorf("verbose ext = %+v, want FlagName=long Shorthand=l", verbose.Ext)
	}
}

func TestLoadPathLevelParams(t *testing.T) {
	ops := loadFixture(t)
	get := findOp(t, ops, "getPet")
	petID := findParam(t, get, "petId")
	if petID.In != "path" || !petID.Required {
		t.Errorf("petId = %+v, want required path param", petID)
	}
}

func TestLoadBody(t *testing.T) {
	ops := loadFixture(t)
	create := findOp(t, ops, "createPet")
	body := create.Body
	if body == nil {
		t.Fatal("createPet has no body")
	}
	if !body.Required || !body.Flat {
		t.Errorf("body = %+v, want required flat", body)
	}
	if len(body.Props) != 6 {
		t.Fatalf("got %d body props, want 6", len(body.Props))
	}
	byName := map[string]oascmd.BodyProp{}
	for _, p := range body.Props {
		byName[p.Name] = p
	}
	if !byName["name"].Required {
		t.Error("name should be required")
	}
	if byName["kind"].Type.Kind != oascmd.KindString || len(byName["kind"].Enum) != 3 {
		t.Errorf("kind = %+v, want string with 3 enum values", byName["kind"])
	}
	if byName["age"].Type.Kind != oascmd.KindInt {
		t.Errorf("age kind = %v, want int", byName["age"].Type.Kind)
	}
	if byName["weight"].Type.Kind != oascmd.KindNumber {
		t.Errorf("weight kind = %v, want number", byName["weight"].Type.Kind)
	}
	if byName["vaccinated"].Type.Kind != oascmd.KindBool {
		t.Errorf("vaccinated kind = %v, want bool", byName["vaccinated"].Type.Kind)
	}
	if !byName["nicknames"].Type.Array {
		t.Errorf("nicknames = %+v, want array", byName["nicknames"])
	}
}

func TestLoadExtensions(t *testing.T) {
	ops := loadFixture(t)
	if op := findOp(t, ops, "deletePet"); !op.Ext.Confirm {
		t.Error("deletePet should have Confirm")
	}
	if op := findOp(t, ops, "debugDump"); !op.Ext.Hidden {
		t.Error("debugDump should be Hidden")
	}
	if op := findOp(t, ops, "skippedOp"); !op.Ext.Skip {
		t.Error("skippedOp should be Skip")
	}
	if op := findOp(t, ops, "legacyPing"); !op.Deprecated {
		t.Error("legacyPing should be Deprecated")
	}
	renamed := findOp(t, ops, "someWeirdInternalName")
	if renamed.Ext.Group != "tools" || renamed.Ext.Name != "renamed thing" {
		t.Errorf("renamed ext = %+v", renamed.Ext)
	}
}

func TestLoadJSON30(t *testing.T) {
	// A minimal OpenAPI 3.0 JSON doc: both format and version differ from
	// the YAML 3.1 fixture.
	doc := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "t", "version": "1"},
		"paths": map[string]any{
			"/things": map[string]any{
				"get": map[string]any{
					"operationId": "listThings",
					"tags":        []string{"things"},
					"parameters": []any{
						map[string]any{
							"name": "q", "in": "query",
							"schema": map[string]any{"type": "string"},
						},
					},
					"responses": map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].ID != "listThings" {
		t.Fatalf("ops = %+v", ops)
	}
	if q := findParam(t, ops[0], "q"); q.Type.Kind != oascmd.KindString {
		t.Errorf("q = %+v", q)
	}
}

func TestLoadWithSchemas(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, schemas, err := LoadWithSchemas(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 2 {
		t.Fatalf("got %d schemas, want 2 (Owner, Pet)", len(schemas))
	}
	if schemas[0].Name != "Owner" || schemas[1].Name != "Pet" {
		t.Fatalf("schema order = %s, %s", schemas[0].Name, schemas[1].Name)
	}

	fields := map[string]ComponentField{}
	for _, f := range schemas[1].Fields {
		fields[f.Name] = f
	}
	if f := fields["owner"]; f.Ref != "Owner" || f.Type.Array {
		t.Errorf("owner field = %+v, want scalar ref to Owner", f)
	}
	if f := fields["metadata"]; !f.Raw {
		t.Errorf("metadata field = %+v, want Raw (inline object)", f)
	}
	if f := fields["id"]; !f.Required {
		t.Errorf("id field = %+v, want required", f)
	}

	ownerFields := map[string]ComponentField{}
	for _, f := range schemas[0].Fields {
		ownerFields[f.Name] = f
	}
	if f := ownerFields["pets"]; f.Ref != "Pet" || !f.Type.Array {
		t.Errorf("pets field = %+v, want array ref to Pet", f)
	}
}

// TestBodyEnvelopeParsing checks what the parser hands ResolveBody: nested
// object properties survive as BodyProp.Object, and the body-level
// extensions are read.
func TestBodyEnvelopeParsing(t *testing.T) {
	ops := loadFixture(t)

	order := findOp(t, ops, "createOrder")
	if order.Body == nil || len(order.Body.Props) != 1 {
		t.Fatalf("createOrder body = %+v, want the single envelope property", order.Body)
	}
	env := order.Body.Props[0]
	if env.Name != "json" || len(env.Object) != 3 {
		t.Fatalf("envelope = %+v, want json with 3 inner properties", env)
	}

	ship := findOp(t, ops, "createShipment")
	if ship.Body == nil || ship.Body.Ext.Unwrap != "data.attributes" {
		t.Fatalf("createShipment x-cli-body-unwrap = %q", ship.Body.Ext.Unwrap)
	}

	// The parser leaves resolution to oascmd.ResolveBody.
	if err := oascmd.ResolveBody(&order); err != nil {
		t.Fatal(err)
	}
	if len(order.Body.WrapPath) != 1 || order.Body.WrapPath[0] != "json" {
		t.Errorf("WrapPath = %v", order.Body.WrapPath)
	}
}

func TestBodyExtensionsFromSpec(t *testing.T) {
	tests := []struct {
		name       string
		ext        string
		wantUnwrap string
		wantWrap   string
	}{
		{name: "string unwrap", ext: "x-cli-body-unwrap: payload", wantUnwrap: "payload"},
		{name: "boolean false unwrap", ext: "x-cli-body-unwrap: false", wantUnwrap: "false"},
		{name: "wrap", ext: "x-cli-body-wrap: envelope", wantWrap: "envelope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
openapi: 3.1.0
info: { title: t, version: "1" }
paths:
  /x:
    post:
      operationId: createX
      requestBody:
        ` + tc.ext + `
        content:
          application/json:
            schema:
              type: object
              properties:
                payload:
                  type: object
                  properties:
                    a: { type: string }
      responses: { "200": { description: OK } }
`
			ops, err := Load([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			body := ops[0].Body
			if body.Ext.Unwrap != tc.wantUnwrap || body.Ext.Wrap != tc.wantWrap {
				t.Errorf("ext = %+v, want unwrap %q wrap %q", body.Ext, tc.wantUnwrap, tc.wantWrap)
			}
		})
	}
}
