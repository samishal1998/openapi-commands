package oascmd

import (
	"reflect"
	"testing"
)

func TestDeriveCommandPath(t *testing.T) {
	tests := []struct {
		name string
		op   Operation
		want CommandPath
	}{
		{
			name: "tag plus operationId with repeated resource",
			op:   Operation{ID: "getDnsRecords", Tags: []string{"dns"}},
			want: CommandPath{Groups: []string{"dns", "records"}, Name: "get"},
		},
		{
			name: "tag plus simple operationId",
			op:   Operation{ID: "listPets", Tags: []string{"pets"}},
			want: CommandPath{Groups: []string{"pets"}, Name: "list"},
		},
		{
			name: "singular resource deduped against plural tag",
			op:   Operation{ID: "createPet", Tags: []string{"pets"}},
			want: CommandPath{Groups: []string{"pets"}, Name: "create"},
		},
		{
			name: "operationId leading word repeating the tag is stripped",
			op:   Operation{ID: "legacyPing", Tags: []string{"legacy"}},
			want: CommandPath{Groups: []string{"legacy"}, Name: "ping"},
		},
		{
			name: "no tags no group",
			op:   Operation{ID: "doThing"},
			want: CommandPath{Groups: []string{"thing"}, Name: "do"},
		},
		{
			name: "camelCase tag becomes kebab group",
			op:   Operation{ID: "listItems", Tags: []string{"myStuff"}},
			want: CommandPath{Groups: []string{"my-stuff", "items"}, Name: "list"},
		},
		{
			name: "no operationId falls back to method and path",
			op:   Operation{Method: "PUT", Path: "/untagged/anon"},
			want: CommandPath{Groups: []string{"untagged", "anon"}, Name: "put"},
		},
		{
			name: "no operationId with path params skipped",
			op:   Operation{Method: "GET", Path: "/pets/{petId}", Tags: []string{"pets"}},
			want: CommandPath{Groups: []string{"pets"}, Name: "get"},
		},
		{
			name: "x-cli-group overrides tag",
			op:   Operation{ID: "listItems", Tags: []string{"stuff"}, Ext: Extensions{Group: "tools"}},
			want: CommandPath{Groups: []string{"tools", "items"}, Name: "list"},
		},
		{
			name: "x-cli-name single word",
			op:   Operation{ID: "someWeirdName", Tags: []string{"misc"}, Ext: Extensions{Name: "go"}},
			want: CommandPath{Groups: []string{"misc"}, Name: "go"},
		},
		{
			name: "x-cli-name multi word extends groups",
			op:   Operation{ID: "x", Tags: []string{"misc"}, Ext: Extensions{Group: "tools", Name: "renamed thing"}},
			want: CommandPath{Groups: []string{"tools", "renamed"}, Name: "thing"},
		},
		{
			name: "acronym operationId",
			op:   Operation{ID: "getHTTPStatus", Tags: []string{"net"}},
			want: CommandPath{Groups: []string{"net", "http", "status"}, Name: "get"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveCommandPath(tt.op)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DeriveCommandPath() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFlagName(t *testing.T) {
	tests := []struct {
		name  string
		param string
		ext   ParamExtensions
		want  string
	}{
		{"camelCase kebabbed", "pageSize", ParamExtensions{}, "page-size"},
		{"snake_case kebabbed", "page_size", ParamExtensions{}, "page-size"},
		{"already simple", "limit", ParamExtensions{}, "limit"},
		{"x-cli-flag-name wins", "verbose", ParamExtensions{FlagName: "long"}, "long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FlagName(tt.param, tt.ext); got != tt.want {
				t.Errorf("FlagName(%q) = %q, want %q", tt.param, got, tt.want)
			}
		})
	}
}

func TestValidateEnum(t *testing.T) {
	if err := ValidateEnum("status", []string{"a", "b"}, "a"); err != nil {
		t.Errorf("valid value rejected: %v", err)
	}
	if err := ValidateEnum("status", []string{"a", "b"}, "c"); err == nil {
		t.Error("invalid value accepted")
	}
	if err := ValidateEnum("status", []string{"a", "b"}, "a", "c"); err == nil {
		t.Error("invalid value in list accepted")
	}
}
