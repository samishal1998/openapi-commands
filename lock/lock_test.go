package lock_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d3servelabs/namefi-astra/projects/oascmd"
	"github.com/d3servelabs/namefi-astra/projects/oascmd/lock"
)

// baseModel is a small, complete operation used as the "before" side of the
// severity table.
func baseModel() lock.Model {
	return lock.Model{
		FuncName: "NewPetsListCommand",
		Path:     oascmd.CommandPath{Groups: []string{"pets"}, Name: "list"},
		Op: oascmd.Operation{
			ID:      "listPets",
			Method:  "GET",
			Path:    "/pets",
			Summary: "List pets",
			Params: []oascmd.Param{
				{Name: "limit", In: "query", Type: oascmd.Type{Kind: oascmd.KindInt}},
				{Name: "status", In: "query", Type: oascmd.Type{Kind: oascmd.KindString}, Enum: []string{"available", "sold"}},
			},
		},
	}
}

func lockOf(models ...lock.Model) lock.Lock { return lock.Compute(models) }

func TestSeverityRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m *lock.Model)
		want   lock.Severity
	}{
		{"identical", func(m *lock.Model) {}, lock.SeverityNone},
		{"summary changed", func(m *lock.Model) { m.Op.Summary = "List all pets" }, lock.SeverityCosmetic},
		{"description changed", func(m *lock.Model) { m.Op.Description = "more words" }, lock.SeverityCosmetic},
		{"flag description changed", func(m *lock.Model) { m.Op.Params[0].Description = "how many" }, lock.SeverityCosmetic},
		{"hidden toggled", func(m *lock.Model) { m.Op.Ext.Hidden = true }, lock.SeverityCosmetic},
		{"enum reordered", func(m *lock.Model) { m.Op.Params[1].Enum = []string{"sold", "available"} }, lock.SeverityCosmetic},

		{"new optional flag", func(m *lock.Model) {
			m.Op.Params = append(m.Op.Params, oascmd.Param{Name: "tag", In: "query", Type: oascmd.Type{Kind: oascmd.KindString}})
		}, lock.SeverityAdditive},
		{"new enum value", func(m *lock.Model) {
			m.Op.Params[1].Enum = append(m.Op.Params[1].Enum, "pending")
		}, lock.SeverityAdditive},
		{"required becomes optional", func(m *lock.Model) { m.Op.Params[0].Required = false }, lock.SeverityAdditive},
		{"optional becomes required", func(m *lock.Model) { m.Op.Params[0].Required = true }, lock.SeverityBreaking},
		{"deprecated added", func(m *lock.Model) { m.Op.Deprecated = true }, lock.SeverityAdditive},
		{"body added", func(m *lock.Model) { m.Op.Body = &oascmd.Body{Flat: false} }, lock.SeverityAdditive},
		{"shorthand added", func(m *lock.Model) { m.Op.Params[0].Ext.Shorthand = "l" }, lock.SeverityAdditive},
		{"confirm removed", func(m *lock.Model) { m.Op.Ext.Confirm = false }, lock.SeverityAdditive},

		{"flag removed", func(m *lock.Model) { m.Op.Params = m.Op.Params[:1] }, lock.SeverityBreaking},
		{"flag type changed", func(m *lock.Model) { m.Op.Params[0].Type = oascmd.Type{Kind: oascmd.KindString} }, lock.SeverityBreaking},
		{"flag became array", func(m *lock.Model) { m.Op.Params[0].Type.Array = true }, lock.SeverityBreaking},
		{"flag source changed", func(m *lock.Model) { m.Op.Params[0].In = "path"; m.Op.Params[0].Required = true }, lock.SeverityBreaking},
		{"enum value removed", func(m *lock.Model) { m.Op.Params[1].Enum = []string{"available"} }, lock.SeverityBreaking},
		{"default changed", func(m *lock.Model) { m.Op.Params[0].Default = "10" }, lock.SeverityBreaking},
		{"command renamed", func(m *lock.Model) { m.Path.Name = "ls" }, lock.SeverityBreaking},
		{"command moved group", func(m *lock.Model) { m.Path.Groups = []string{"animals"} }, lock.SeverityBreaking},
		{"constructor renamed", func(m *lock.Model) { m.FuncName = "NewPetsLsCommand" }, lock.SeverityBreaking},
		{"method changed", func(m *lock.Model) { m.Op.Method = "POST" }, lock.SeverityBreaking},
		{"path changed", func(m *lock.Model) { m.Op.Path = "/v2/pets" }, lock.SeverityBreaking},
		{"confirm added", func(m *lock.Model) { m.Op.Ext.Confirm = true }, lock.SeverityBreaking},
		{"new required flag", func(m *lock.Model) {
			m.Op.Params = append(m.Op.Params, oascmd.Param{Name: "owner", In: "query", Required: true, Type: oascmd.Type{Kind: oascmd.KindString}})
		}, lock.SeverityBreaking},
		{"shorthand changed", func(m *lock.Model) { m.Op.Params[0].Ext.Shorthand = "n" }, lock.SeverityBreaking},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := baseModel()
			after := baseModel()
			// A few cases describe a change *away* from a state, so
			// set that state on the "before" side.
			switch tc.name {
			case "required becomes optional":
				before.Op.Params[0].Required = true
			case "confirm removed":
				before.Op.Ext.Confirm = true
			case "shorthand changed":
				before.Op.Params[0].Ext.Shorthand = "l"
			}
			tc.mutate(&after)
			report := lock.Diff(lockOf(before), lockOf(after))
			if got := report.Severity(); got != tc.want {
				t.Fatalf("severity = %q, want %q\n%s", got, tc.want, report.Text())
			}
		})
	}
}

func TestAddedAndRemovedOperations(t *testing.T) {
	other := baseModel()
	other.Op.ID = "getPet"
	other.Op.Path = "/pets/{petId}"
	other.FuncName = "NewPetsGetCommand"
	other.Path.Name = "get"

	added := lock.Diff(lockOf(baseModel()), lockOf(baseModel(), other))
	if added.Severity() != lock.SeverityAdditive {
		t.Fatalf("adding an operation = %q, want additive", added.Severity())
	}
	if len(added.Operations) != 1 || added.Operations[0].Kind != lock.ChangeAdded {
		t.Fatalf("unexpected report: %+v", added.Operations)
	}

	removed := lock.Diff(lockOf(baseModel(), other), lockOf(baseModel()))
	if removed.Severity() != lock.SeverityBreaking {
		t.Fatalf("removing an operation = %q, want breaking", removed.Severity())
	}
	if removed.Operations[0].Kind != lock.ChangeRemoved {
		t.Fatalf("unexpected kind %q", removed.Operations[0].Kind)
	}
}

func TestGeneratorVersionChangeIsAdditive(t *testing.T) {
	old := lockOf(baseModel())
	old.GeneratorVersion = "oascmd-gen/0"
	report := lock.Diff(old, lockOf(baseModel()))
	if !report.GeneratorChanged {
		t.Fatal("expected generatorChanged")
	}
	if report.Severity() != lock.SeverityAdditive {
		t.Fatalf("severity = %q, want additive", report.Severity())
	}
}

func TestDeterminism(t *testing.T) {
	a, err := lock.Marshal(lockOf(baseModel()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := lock.Marshal(lockOf(baseModel()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two computations produced different bytes")
	}
	if !bytes.HasSuffix(a, []byte("\n")) {
		t.Error("lock file must end with a newline")
	}
	if !bytes.Contains(a, []byte("\n  \"lockVersion\"")) {
		t.Errorf("expected 2-space indent, got:\n%s", a)
	}
	if bytes.Contains(a, []byte("generatedAt")) {
		t.Error("lock must not record a timestamp (it would churn every run)")
	}
	// Operation keys come from a Go map, so they must be emitted sorted
	// for the file to be diff-friendly.
	two := baseModel()
	two.Op.ID = "aaaFirst"
	three := baseModel()
	three.Op.ID = "zzzLast"
	multi, err := lock.Marshal(lockOf(three, baseModel(), two))
	if err != nil {
		t.Fatal(err)
	}
	body := string(multi)
	if strings.Index(body, "\"aaaFirst\"") > strings.Index(body, "\"zzzLast\"") {
		t.Errorf("operation keys are not sorted:\n%s", body)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(multi, &parsed); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lock.DefaultFileName)

	if _, existed, err := lock.Load(path); err != nil || existed {
		t.Fatalf("missing file: existed=%v err=%v", existed, err)
	}

	want := lockOf(baseModel())
	if err := lock.Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, existed, err := lock.Load(path)
	if err != nil || !existed {
		t.Fatalf("load: existed=%v err=%v", existed, err)
	}
	if lock.Diff(want, got).Severity() != lock.SeverityNone {
		t.Fatal("round trip changed the surface")
	}
	if got.SpecDigest != want.SpecDigest {
		t.Errorf("specDigest %q != %q", got.SpecDigest, want.SpecDigest)
	}
}

func TestUnknownLockVersion(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"newer", `{"lockVersion": 99, "operations": {}}`, "upgrade oascmd-gen"},
		{"older", `{"lockVersion": -1, "operations": {}}`, "no longer reads"},
		{"missing", `{"operations": {}}`, "missing lockVersion"},
		{"garbage", `not json`, "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lock.Unmarshal([]byte(tc.data))
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadReportsBadFileWithPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"lockVersion": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, existed, err := lock.Load(path)
	if !existed || err == nil {
		t.Fatalf("existed=%v err=%v", existed, err)
	}
	if !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("error should name the file: %v", err)
	}
}

func TestReportTextIsPlainLanguage(t *testing.T) {
	before := baseModel()
	after := baseModel()
	after.Op.Params[0].Type = oascmd.Type{Kind: oascmd.KindString}
	text := lock.Diff(lockOf(before), lockOf(after)).Text()
	for _, want := range []string{"Breaking changes", "pets list", "flag --limit type: int -> string"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
}

func TestNoChangesText(t *testing.T) {
	text := lock.Diff(lockOf(baseModel()), lockOf(baseModel())).Text()
	if !strings.Contains(text, "No changes") {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestReportJSONGolden(t *testing.T) {
	before := baseModel()
	after := baseModel()
	after.Op.Params[0].Type = oascmd.Type{Kind: oascmd.KindString}
	after.Op.Params = append(after.Op.Params, oascmd.Param{Name: "tag", In: "query", Type: oascmd.Type{Kind: oascmd.KindString}})

	data, err := lock.Diff(lockOf(before), lockOf(after)).JSON()
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.json")
	want, err := os.ReadFile(golden)
	if err != nil || os.Getenv("UPDATE_GOLDEN") != "" {
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(golden, data, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Log("golden updated")
			return
		}
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("report JSON differs from %s (re-run with UPDATE_GOLDEN=1)\ngot:\n%s", golden, data)
	}
}

func TestPolicyDecisions(t *testing.T) {
	safe := lock.Diff(lockOf(baseModel()), lockOf(baseModel(), addedOp()))
	breakingReport := lock.Diff(lockOf(baseModel(), addedOp()), lockOf(baseModel()))
	none := lock.Diff(lockOf(baseModel()), lockOf(baseModel()))

	cases := []struct {
		name     string
		policy   lock.Policy
		report   lock.Report
		write    bool
		exitCode int
		kept     int
	}{
		{"auto none", lock.PolicyAuto, none, true, lock.ExitOK, 0},
		{"auto additive", lock.PolicyAuto, safe, true, lock.ExitOK, 0},
		{"auto breaking", lock.PolicyAuto, breakingReport, false, lock.ExitBreaking, 0},
		{"all breaking", lock.PolicyAll, breakingReport, true, lock.ExitOK, 0},
		{"additive-only breaking", lock.PolicyAdditiveOnly, breakingReport, true, lock.ExitOK, 1},
		{"additive-only safe", lock.PolicyAdditiveOnly, safe, true, lock.ExitOK, 0},
		{"check none", lock.PolicyCheck, none, false, lock.ExitOK, 0},
		{"check additive", lock.PolicyCheck, safe, false, lock.ExitDrift, 0},
		{"check breaking", lock.PolicyCheck, breakingReport, false, lock.ExitDrift, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := lock.Decide(tc.policy, tc.report)
			if d.Write != tc.write {
				t.Errorf("write = %v, want %v", d.Write, tc.write)
			}
			if d.ExitCode != tc.exitCode {
				t.Errorf("exit = %d, want %d", d.ExitCode, tc.exitCode)
			}
			if len(d.KeptKeys) != tc.kept {
				t.Errorf("kept = %v, want %d", d.KeptKeys, tc.kept)
			}
			if d.Summary == "" {
				t.Error("summary must not be empty")
			}
		})
	}
}

func TestParsePolicy(t *testing.T) {
	for _, p := range lock.Policies {
		if got, err := lock.ParsePolicy(string(p)); err != nil || got != p {
			t.Errorf("ParsePolicy(%q) = %v, %v", p, got, err)
		}
	}
	if _, err := lock.ParsePolicy("yolo"); err == nil {
		t.Error("expected an error for an unknown policy")
	}
}

func TestToModelRoundTrip(t *testing.T) {
	original := baseModel()
	original.Op.Params[0].Ext.Shorthand = "l"
	original.Op.Params[0].Default = "10"
	original.Op.Params = append(original.Op.Params, oascmd.Param{
		Name: "pet_id", In: "path", Required: true, Type: oascmd.Type{Kind: oascmd.KindString},
		Ext: oascmd.ParamExtensions{FlagName: "petId"},
	})
	original.Op.Body = &oascmd.Body{Flat: true, Required: true, Props: []oascmd.BodyProp{
		{Name: "name", Type: oascmd.Type{Kind: oascmd.KindString}, Required: true},
		{Name: "tags", Type: oascmd.Type{Kind: oascmd.KindString, Array: true}},
	}}

	l := lockOf(original)
	entry := l.Operations[lock.Key(original.Op)]
	restored, err := lock.ToModel(entry)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Diff(l, lockOf(restored)).Severity() != lock.SeverityNone {
		t.Fatalf("reconstruction drifted:\n%s", lock.Diff(l, lockOf(restored)).Text())
	}
}

func addedOp() lock.Model {
	m := baseModel()
	m.Op.ID = "createPet"
	m.Op.Method = "POST"
	m.FuncName = "NewPetsCreateCommand"
	m.Path.Name = "create"
	return m
}

// TestDiffBodyWrap: changing where the flags land in the JSON body is a
// breaking change even though the flag names are identical.
func TestDiffBodyWrap(t *testing.T) {
	mk := func(wrap string) lock.Lock {
		op := oascmd.Operation{
			ID: "createOrder", Method: "POST", Path: "/orders",
			Body: &oascmd.Body{
				Flat:     true,
				WrapPath: oascmd.SplitBodyPath(wrap),
				Props:    []oascmd.BodyProp{{Name: "petId", Type: oascmd.Type{Kind: oascmd.KindString}}},
			},
		}
		return lock.Compute([]lock.Model{{FuncName: "NewOrdersCreateCommand",
			Path: oascmd.CommandPath{Groups: []string{"orders"}, Name: "create"}, Op: op}})
	}
	tests := []struct {
		name     string
		old, new string
		want     lock.Severity
		field    string
	}{
		{name: "unchanged", old: "json", new: "json", want: lock.SeverityNone},
		{name: "envelope added", old: "", new: "json", want: lock.SeverityBreaking, field: "body wrap"},
		{name: "envelope removed", old: "json", new: "", want: lock.SeverityBreaking, field: "body wrap"},
		{name: "envelope moved", old: "json", new: "data.attributes", want: lock.SeverityBreaking, field: "body wrap"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := lock.Diff(mk(tc.old), mk(tc.new))
			if got := report.Severity(); got != tc.want {
				t.Fatalf("severity = %s, want %s (%+v)", got, tc.want, report.Operations)
			}
			if tc.field == "" {
				return
			}
			found := false
			for _, op := range report.Operations {
				for _, c := range op.Changes {
					if c.Field == tc.field {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("no %q change reported: %+v", tc.field, report.Operations)
			}
		})
	}
}

// TestToModelRoundTripsWrap: replaying a locked operation keeps its
// envelope, so additive-only re-emits the same request shape.
func TestToModelRoundTripsWrap(t *testing.T) {
	entry := lock.Operation{
		OperationID: "createOrder", Method: "POST", Path: "/orders", Command: "orders create",
		Body:  &lock.Body{Flat: true, Required: true, Wrap: "data.attributes"},
		Flags: []lock.Flag{{Name: "pet-id", APIName: "petId", Source: "body", Type: "string"}},
	}
	m, err := lock.ToModel(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Op.Body.WrapPath, "."); got != "data.attributes" {
		t.Errorf("WrapPath = %q", got)
	}
	round := lock.Compute([]lock.Model{m})
	if got := round.Operations["createOrder"].Body.Wrap; got != "data.attributes" {
		t.Errorf("wrap after round trip = %q", got)
	}
}
