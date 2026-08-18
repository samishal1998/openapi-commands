package oascmd

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// bodyOp is a small constructor for an operation carrying a body.
func bodyOp(ext BodyExtensions, props ...BodyProp) *Operation {
	return &Operation{Method: "POST", Path: "/x", Body: &Body{Props: props, Ext: ext}}
}

func scalarProp(name string) BodyProp {
	return BodyProp{Name: name, Type: Type{Kind: KindString}}
}

func objProp(name string, children ...BodyProp) BodyProp {
	return BodyProp{Name: name, Object: children}
}

func TestResolveBody(t *testing.T) {
	tests := []struct {
		name     string
		op       *Operation
		wantErr  string
		wantWrap []string
		wantFlat bool
		wantFlag []string
	}{
		{
			name:     "already flat body is untouched",
			op:       bodyOp(BodyExtensions{}, scalarProp("name"), scalarProp("kind")),
			wantFlat: true,
			wantFlag: []string{"name", "kind"},
		},
		{
			name:     "single object property is auto-unwrapped",
			op:       bodyOp(BodyExtensions{}, objProp("json", scalarProp("arg1"), scalarProp("arg2"))),
			wantWrap: []string{"json"},
			wantFlat: true,
			wantFlag: []string{"arg1", "arg2"},
		},
		{
			name:     "auto-unwrap descends several levels",
			op:       bodyOp(BodyExtensions{}, objProp("data", objProp("attributes", scalarProp("addr")))),
			wantWrap: []string{"data", "attributes"},
			wantFlat: true,
			wantFlag: []string{"addr"},
		},
		{
			name:     "two properties are not an envelope",
			op:       bodyOp(BodyExtensions{}, objProp("json", scalarProp("a")), scalarProp("b")),
			wantFlat: false,
		},
		{
			name:     "explicit unwrap picks one of several properties",
			op:       bodyOp(BodyExtensions{Unwrap: "payload"}, objProp("payload", scalarProp("a")), scalarProp("meta")),
			wantWrap: []string{"payload"},
			wantFlat: true,
			wantFlag: []string{"a"},
		},
		{
			name:     "explicit dotted unwrap",
			op:       bodyOp(BodyExtensions{Unwrap: "data.attributes"}, objProp("data", objProp("attributes", scalarProp("addr"))), scalarProp("meta")),
			wantWrap: []string{"data", "attributes"},
			wantFlat: true,
			wantFlag: []string{"addr"},
		},
		{
			name:     "unwrap none disables automatic detection",
			op:       bodyOp(BodyExtensions{Unwrap: "none"}, objProp("json", scalarProp("a"))),
			wantFlat: false,
		},
		{
			name:     "unwrap false disables automatic detection",
			op:       bodyOp(BodyExtensions{Unwrap: "false"}, objProp("json", scalarProp("a"))),
			wantFlat: false,
		},
		{
			name:     "unwrap auto is the default behavior",
			op:       bodyOp(BodyExtensions{Unwrap: "auto"}, objProp("json", scalarProp("a"))),
			wantWrap: []string{"json"},
			wantFlat: true,
			wantFlag: []string{"a"},
		},
		{
			name:     "wrap nests a flat body the spec does not describe",
			op:       bodyOp(BodyExtensions{Wrap: "json"}, scalarProp("a")),
			wantWrap: []string{"json"},
			wantFlat: true,
			wantFlag: []string{"a"},
		},
		{
			name:     "wrap and unwrap compose outermost-first",
			op:       bodyOp(BodyExtensions{Wrap: "envelope"}, objProp("json", scalarProp("a"))),
			wantWrap: []string{"envelope", "json"},
			wantFlat: true,
			wantFlag: []string{"a"},
		},
		{
			name:    "explicit unwrap of a missing property errors",
			op:      bodyOp(BodyExtensions{Unwrap: "nope"}, objProp("json", scalarProp("a"))),
			wantErr: `no property "nope"`,
		},
		{
			name:    "explicit unwrap of a scalar errors",
			op:      bodyOp(BodyExtensions{Unwrap: "a"}, scalarProp("a")),
			wantErr: `"a" is not an object property`,
		},
		{
			name: "colliding flag names after unwrapping error",
			op: bodyOp(BodyExtensions{Unwrap: "json"},
				objProp("json",
					BodyProp{Name: "petId", Type: Type{Kind: KindString}},
					BodyProp{Name: "pet_id", Type: Type{Kind: KindString}})),
			wantErr: "both map to --pet-id",
		},
		{
			name:     "x-cli-flag-name resolves a collision",
			op:       bodyOp(BodyExtensions{}, objProp("json", BodyProp{Name: "petId", Type: Type{Kind: KindString}}, BodyProp{Name: "pet_id", Type: Type{Kind: KindString}, Ext: ParamExtensions{FlagName: "pet-id-alt"}})),
			wantWrap: []string{"json"},
			wantFlat: true,
			wantFlag: []string{"petId", "pet_id"},
		},
		{
			name:     "a leftover nested object leaves the body non-flat",
			op:       bodyOp(BodyExtensions{}, objProp("json", scalarProp("a"), objProp("nested", scalarProp("b")))),
			wantWrap: []string{"json"},
			wantFlat: false,
		},
		{
			name:     "no properties means --data only",
			op:       bodyOp(BodyExtensions{}),
			wantFlat: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ResolveBody(tc.op)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b := tc.op.Body
			if b.Flat != tc.wantFlat {
				t.Errorf("Flat = %v, want %v", b.Flat, tc.wantFlat)
			}
			if !reflect.DeepEqual(b.WrapPath, tc.wantWrap) {
				t.Errorf("WrapPath = %v, want %v", b.WrapPath, tc.wantWrap)
			}
			var got []string
			for _, p := range b.Props {
				got = append(got, p.Name)
			}
			if tc.wantFlat && !reflect.DeepEqual(got, tc.wantFlag) {
				t.Errorf("props = %v, want %v", got, tc.wantFlag)
			}
			if !tc.wantFlat && len(b.Props) != 0 {
				t.Errorf("non-flat body kept %d props", len(b.Props))
			}
		})
	}
}

func TestResolveBodyNilBody(t *testing.T) {
	op := &Operation{Method: "GET", Path: "/x"}
	if err := ResolveBody(op); err != nil {
		t.Fatal(err)
	}
}

func TestNestBody(t *testing.T) {
	tests := []struct {
		name string
		path []string
		want string
	}{
		{name: "no path is the body root", want: `map[a:1]`},
		{name: "one level", path: []string{"json"}, want: `map[json:map[a:1]]`},
		{name: "two levels", path: []string{"data", "attributes"}, want: `map[data:map[attributes:map[a:1]]]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NestBody(tc.path, map[string]any{"a": 1})
			if s := strings.ReplaceAll(fmt.Sprint(got), " ", ""); s != tc.want {
				t.Errorf("NestBody = %s, want %s", s, tc.want)
			}
		})
	}
}

func TestSplitBodyPath(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "  ", want: nil},
		{in: "json", want: []string{"json"}},
		{in: "data.attributes", want: []string{"data", "attributes"}},
		{in: "a. b .c", want: []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := SplitBodyPath(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitBodyPath(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyBodyHook covers the programmatic route for consumers who cannot
// edit the spec: OnResolveBody declares the envelope, ResolveBody applies it.
func TestApplyBodyHook(t *testing.T) {
	op := bodyOp(BodyExtensions{}, objProp("payload", scalarProp("a")), scalarProp("meta"))
	hooks := Hooks{OnResolveBody: func(op *Operation) error {
		op.Body.Ext.Unwrap = "payload"
		return nil
	}}
	if err := hooks.ApplyBody(op); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op.Body.WrapPath, []string{"payload"}) {
		t.Errorf("WrapPath = %v", op.Body.WrapPath)
	}
	if len(op.Body.Props) != 1 || op.Body.Props[0].Name != "a" {
		t.Errorf("props = %+v, want just a", op.Body.Props)
	}
}
