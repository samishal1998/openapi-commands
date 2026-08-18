package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/d3servelabs/namefi-astra/projects/oascmd"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// find walks the tree by command words, e.g. find(cmds, "pets", "list").
func find(t *testing.T, cmds []*cobra.Command, words ...string) *cobra.Command {
	t.Helper()
	level := cmds
	var found *cobra.Command
	for _, word := range words {
		found = nil
		for _, c := range level {
			if c.Name() == word {
				found = c
				break
			}
		}
		if found == nil {
			t.Fatalf("command %q not found under %v", word, words)
		}
		level = found.Commands()
	}
	return found
}

func TestBuildTree(t *testing.T) {
	cmds, err := Build(fixture(t), Options{Exec: oascmd.ExecOptions{BaseURL: "http://x"}})
	if err != nil {
		t.Fatal(err)
	}

	// x-cli-skip drops the operation entirely.
	internal := find(t, cmds, "internal")
	for _, c := range internal.Commands() {
		if c.Name() == "skip-me" {
			t.Error("x-cli-skip operation was not dropped")
		}
	}

	// x-cli-group + x-cli-name relocate the command.
	find(t, cmds, "tools", "renamed", "thing")
	// tag + operationId dedupe.
	find(t, cmds, "dns", "records", "get")
	// no operationId falls back to method + path.
	find(t, cmds, "untagged", "anon", "put")

	if hidden := find(t, cmds, "internal", "dump", "debug"); !hidden.Hidden {
		t.Error("x-cli-hidden operation is not hidden")
	}
	if dep := find(t, cmds, "legacy", "ping"); dep.Deprecated == "" {
		t.Error("deprecated operation carries no Deprecated notice")
	}
}

func TestFlagMapping(t *testing.T) {
	cmds, err := Build(fixture(t), Options{Exec: oascmd.ExecOptions{BaseURL: "http://x"}})
	if err != nil {
		t.Fatal(err)
	}
	list := find(t, cmds, "pets", "list")

	tests := []struct {
		flag      string
		typ       string
		def       string
		shorthand string
	}{
		{flag: "limit", typ: "int64", def: "20"},
		{flag: "status", typ: "string", def: ""},
		{flag: "tags", typ: "stringSlice", def: "[]"},
		{flag: "long", typ: "bool", def: "false", shorthand: "l"},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			f := list.Flags().Lookup(tt.flag)
			if f == nil {
				t.Fatalf("flag --%s not registered", tt.flag)
			}
			if f.Value.Type() != tt.typ {
				t.Errorf("type = %q, want %q", f.Value.Type(), tt.typ)
			}
			if f.DefValue != tt.def {
				t.Errorf("default = %q, want %q", f.DefValue, tt.def)
			}
			if f.Shorthand != tt.shorthand {
				t.Errorf("shorthand = %q, want %q", f.Shorthand, tt.shorthand)
			}
		})
	}

	if usage := list.Flags().Lookup("status").Usage; !strings.Contains(usage, "available") {
		t.Errorf("enum values missing from usage: %q", usage)
	}

	// Body flags: --data plus one per flat top-level property.
	create := find(t, cmds, "pets", "create")
	for _, flag := range []string{"data", "name", "kind", "age", "weight", "vaccinated", "nicknames"} {
		if create.Flags().Lookup(flag) == nil {
			t.Errorf("body flag --%s not registered", flag)
		}
	}
	if create.Flags().Lookup("age").Value.Type() != "int64" {
		t.Error("age flag should be int64")
	}
	if create.Flags().Lookup("weight").Value.Type() != "float64" {
		t.Error("weight flag should be float64")
	}
	if create.Flags().Lookup("nicknames").Value.Type() != "stringSlice" {
		t.Error("nicknames flag should be stringSlice")
	}
}

// run executes a command from the tree with args, against a test server.
func run(t *testing.T, handler http.HandlerFunc, opts Options, args ...string) (string, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()

	opts.Exec.BaseURL = server.URL
	var out bytes.Buffer
	opts.Exec.Out = &out

	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "test", SilenceUsage: true, SilenceErrors: true}
	for _, c := range cmds {
		root.AddCommand(c)
	}
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestExecuteQueryAndAuth(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAuth string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	opts := Options{Exec: oascmd.ExecOptions{
		Auth: func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer tok")
			return nil
		},
	}}
	out, err := run(t, handler, opts,
		"pets", "list", "--limit", "5", "--status", "sold", "--tags", "a", "--tags", "b", "-l")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/pets" {
		t.Errorf("request = %s %s, want GET /pets", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header = %q", gotAuth)
	}
	for _, want := range []string{"limit=5", "status=sold", "tags=a", "tags=b", "verbose=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Errorf("output not pretty-printed: %q", out)
	}
}

func TestExecuteDefaultsApplied(t *testing.T) {
	var gotQuery string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}
	// --limit not passed: the schema default (20) is still sent.
	if _, err := run(t, handler, Options{}, "pets", "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "limit=20") {
		t.Errorf("query = %q, want limit=20 from the schema default", gotQuery)
	}
	if strings.Contains(gotQuery, "status=") {
		t.Errorf("query = %q, unset optional flag should be omitted", gotQuery)
	}
}

func TestExecutePathParam(t *testing.T) {
	var gotPath string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}
	if _, err := run(t, handler, Options{}, "pets", "get", "--pet-id", "p-42"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/pets/p-42" {
		t.Errorf("path = %q, want /pets/p-42", gotPath)
	}
}

func TestExecuteBodyFromFlags(t *testing.T) {
	var gotBody map[string]any
	var gotContentType string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}
	_, err := run(t, handler, Options{}, "pets", "create",
		"--name", "Rex", "--kind", "dog", "--age", "3", "--weight", "12.5",
		"--vaccinated", "--nicknames", "rexy", "--nicknames", "r")
	if err != nil {
		t.Fatal(err)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if gotBody["name"] != "Rex" || gotBody["kind"] != "dog" {
		t.Errorf("body = %+v", gotBody)
	}
	if gotBody["age"] != float64(3) || gotBody["weight"] != 12.5 {
		t.Errorf("numeric body values wrong: %+v", gotBody)
	}
	if gotBody["vaccinated"] != true {
		t.Errorf("vaccinated = %v", gotBody["vaccinated"])
	}
	names, ok := gotBody["nicknames"].([]any)
	if !ok || len(names) != 2 {
		t.Errorf("nicknames = %v", gotBody["nicknames"])
	}
}

func TestExecuteBodyFromDataFlag(t *testing.T) {
	var raw []byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}
	// --data wins over the per-property flags.
	_, err := run(t, handler, Options{}, "pets", "create",
		"--name", "ignored", "--data", `{"name":"FromData"}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"name":"FromData"}` {
		t.Errorf("body = %q, want the --data payload", raw)
	}
}

func TestExecuteInvalidDataJSON(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") }
	_, err := run(t, handler, Options{}, "pets", "create", "--data", "{oops")
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("err = %v, want an invalid-JSON error", err)
	}
}

func TestRequiredFlagEnforced(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") }
	_, err := run(t, handler, Options{}, "dns", "records", "get")
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Errorf("err = %v, want a required-flag error mentioning domain", err)
	}
}

func TestEnumValidation(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") }
	_, err := run(t, handler, Options{}, "pets", "list", "--status", "nope")
	if err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Errorf("err = %v, want an enum validation error", err)
	}
}

func TestNon2xxError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such pet"}`))
	}
	_, err := run(t, handler, Options{}, "pets", "get", "--pet-id", "x")
	var statusErr *oascmd.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v (%T), want *oascmd.StatusError", err, err)
	}
	if statusErr.StatusCode != 404 {
		t.Errorf("status = %d, want 404", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Snippet, "no such pet") {
		t.Errorf("snippet = %q, want the response body", statusErr.Snippet)
	}
}

func TestRawJSONFlag(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	}
	out, err := run(t, handler, Options{}, "pets", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n  ") {
		t.Errorf("--json output should not be indented: %q", out)
	}
}

func TestHookOnReadOperationFilters(t *testing.T) {
	opts := Options{
		Exec: oascmd.ExecOptions{BaseURL: "http://x"},
		Hooks: oascmd.Hooks{
			OnReadOperation: func(op *oascmd.Operation) error {
				if op.Deprecated {
					return oascmd.SkipOperation
				}
				return nil
			},
		},
	}
	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range cmds {
		if group.Name() != "legacy" {
			continue
		}
		for _, c := range group.Commands() {
			if c.Name() == "ping" {
				t.Error("deprecated operation was not filtered by OnReadOperation")
			}
		}
	}
}

func TestHookOnReadOperationMutates(t *testing.T) {
	opts := Options{
		Exec: oascmd.ExecOptions{BaseURL: "http://x"},
		Hooks: oascmd.Hooks{
			OnReadOperation: func(op *oascmd.Operation) error {
				if op.ID == "listPets" {
					op.Ext.Name = "ls"
				}
				return nil
			},
		},
	}
	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	find(t, cmds, "pets", "ls")
}

func TestHookOnReadOperationError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Build(fixture(t), Options{
		Exec:  oascmd.ExecOptions{BaseURL: "http://x"},
		Hooks: oascmd.Hooks{OnReadOperation: func(op *oascmd.Operation) error { return boom }},
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the hook error to abort the build", err)
	}
}

func TestHookBeforeCreateCommandVetoAndMutate(t *testing.T) {
	opts := Options{
		Exec: oascmd.ExecOptions{BaseURL: "http://x"},
		Hooks: oascmd.Hooks{
			OnBeforeCreateCommand: func(op oascmd.Operation, cmd *cobra.Command) error {
				if op.ID == "deletePet" {
					return oascmd.SkipOperation
				}
				cmd.Short = "mutated: " + cmd.Short
				return nil
			},
		},
	}
	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	pets := find(t, cmds, "pets")
	for _, c := range pets.Commands() {
		if c.Name() == "delete" {
			t.Error("vetoed command was still added")
		}
	}
	if short := find(t, cmds, "pets", "list").Short; !strings.HasPrefix(short, "mutated: ") {
		t.Errorf("Short = %q, want the hook mutation", short)
	}
}

func TestHookAfterCreateCommand(t *testing.T) {
	var seen []string
	opts := Options{
		Exec: oascmd.ExecOptions{BaseURL: "http://x"},
		Hooks: oascmd.Hooks{
			OnAfterCreateCommand: func(op oascmd.Operation, cmd *cobra.Command) error {
				seen = append(seen, op.ID)
				cmd.Aliases = append(cmd.Aliases, "alias-"+cmd.Name())
				return nil
			},
		},
	}
	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("OnAfterCreateCommand never ran")
	}
	list := find(t, cmds, "pets", "list")
	if len(list.Aliases) != 1 || list.Aliases[0] != "alias-list" {
		t.Errorf("aliases = %v, want the hook mutation to stick", list.Aliases)
	}
}

func TestExecuteHooks(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Trace"); got != "yes" {
			t.Errorf("X-Trace = %q, OnBeforeExecute did not run", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}
	afterRan := false
	opts := Options{Exec: oascmd.ExecOptions{
		OnBeforeExecute: []func(context.Context, *http.Request) error{
			func(ctx context.Context, req *http.Request) error {
				req.Header.Set("X-Trace", "yes")
				return nil
			},
		},
		OnAfterExecute: []func(context.Context, *http.Response) error{
			func(ctx context.Context, resp *http.Response) error {
				afterRan = true
				return nil
			},
		},
	}}
	if _, err := run(t, handler, opts, "pets", "list"); err != nil {
		t.Fatal(err)
	}
	if !afterRan {
		t.Error("OnAfterExecute did not run")
	}
}

func TestExecuteHookAborts(t *testing.T) {
	boom := errors.New("nope")
	handler := func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") }
	opts := Options{Exec: oascmd.ExecOptions{
		OnBeforeExecute: []func(context.Context, *http.Request) error{
			func(ctx context.Context, req *http.Request) error { return boom },
		},
	}}
	if _, err := run(t, handler, opts, "pets", "list"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the hook error", err)
	}
}

func TestConfirmPrompt(t *testing.T) {
	called := false
	handler := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	newRoot := func() *cobra.Command {
		cmds, err := Build(fixture(t), Options{Exec: oascmd.ExecOptions{
			BaseURL: server.URL,
			Out:     io.Discard,
		}})
		if err != nil {
			t.Fatal(err)
		}
		root := &cobra.Command{Use: "test", SilenceUsage: true, SilenceErrors: true}
		for _, c := range cmds {
			root.AddCommand(c)
		}
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		return root
	}

	// Declining aborts without sending the request.
	root := newRoot()
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"pets", "delete", "--pet-id", "x"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Error("declining the prompt should abort")
	}
	if called {
		t.Error("request was sent despite declining")
	}

	// Accepting proceeds.
	root = newRoot()
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"pets", "delete", "--pet-id", "x"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("request was not sent after accepting")
	}

	// --yes skips the prompt entirely (no stdin available).
	called = false
	root = newRoot()
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"pets", "delete", "--pet-id", "x", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("--yes did not skip the prompt")
	}
}

func TestAttach(t *testing.T) {
	root := &cobra.Command{Use: "app"}
	if err := Attach(root, fixture(t), Options{Exec: oascmd.ExecOptions{BaseURL: "http://x"}}); err != nil {
		t.Fatal(err)
	}
	if len(root.Commands()) == 0 {
		t.Fatal("Attach added no commands")
	}
	find(t, root.Commands(), "pets", "list")
}

func TestCustomNameFunc(t *testing.T) {
	opts := Options{
		Exec: oascmd.ExecOptions{BaseURL: "http://x"},
		NameFunc: func(op oascmd.Operation) oascmd.CommandPath {
			return oascmd.CommandPath{Groups: []string{"api"}, Name: strings.ToLower(op.Method) + "-" + op.ID}
		},
	}
	cmds, err := Build(fixture(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	find(t, cmds, "api", "get-listPets")
}

// TestRuntimeBodyUnwrap covers Q2 in runtime mode end to end: the flags come
// from the inner object and the request re-wraps them on submit.
func TestRuntimeBodyUnwrap(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		args     []string
		wantFlag string
		wantBody string
	}{
		{
			name:     "auto-detected single-property envelope",
			command:  []string{"orders", "create"},
			args:     []string{"--pet-id", "p1", "--quantity", "2"},
			wantFlag: "pet-id",
			wantBody: `{"json":{"petId":"p1","quantity":2}}`,
		},
		{
			name:     "declared multi-level envelope",
			command:  []string{"shipments", "create"},
			args:     []string{"--address", "1 Main St", "--carrier", "ups"},
			wantFlag: "address",
			wantBody: `{"data":{"attributes":{"address":"1 Main St","carrier":"ups"}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			cmds, err := Build(fixture(t), Options{Exec: oascmd.ExecOptions{BaseURL: srv.URL, Out: io.Discard}})
			if err != nil {
				t.Fatal(err)
			}
			cmd := find(t, cmds, tc.command...)
			if cmd.Flags().Lookup(tc.wantFlag) == nil {
				t.Fatalf("--%s not registered; flags are still wrapped", tc.wantFlag)
			}
			// The envelope itself never becomes a flag. ("--data" and
			// "--json" are the built-ins, so the check is that the
			// envelope property did not add typed flags of its own:
			// --data stays a string and --json stays a bool.)
			if f := cmd.Flags().Lookup("data"); f == nil || f.Value.Type() != "string" {
				t.Error("--data must stay the raw-JSON escape hatch")
			}
			if f := cmd.Flags().Lookup("json"); f == nil || f.Value.Type() != "bool" {
				t.Error("--json must stay the output flag, not an envelope flag")
			}
			runSubcommand(t, cmds, tc.command, tc.args)
			if got := normalizeJSON(t, body); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
		})
	}
}

// runSubcommand executes one command of a built tree, under a fresh root so
// cobra dispatches to it rather than to the group.
func runSubcommand(t *testing.T, cmds []*cobra.Command, words, args []string) {
	t.Helper()
	root := &cobra.Command{Use: "test", SilenceUsage: true, SilenceErrors: true}
	for _, c := range cmds {
		root.AddCommand(c)
	}
	root.SetArgs(append(append([]string{}, words...), args...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// normalizeJSON re-marshals so key order is deterministic.
func normalizeJSON(t *testing.T, data []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("body %q is not JSON: %v", data, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestRuntimeBodyUnwrapHook is the programmatic route: the spec declares no
// envelope, the consumer declares it with OnResolveBody.
func TestRuntimeBodyUnwrapHook(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cmds, err := Build(fixture(t), Options{
		Exec: oascmd.ExecOptions{BaseURL: srv.URL, Out: io.Discard},
		Hooks: oascmd.Hooks{OnResolveBody: func(op *oascmd.Operation) error {
			if op.ID == "createPet" {
				op.Body.Ext.Wrap = "payload"
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runSubcommand(t, cmds, []string{"pets", "create"}, []string{"--name", "Rex"})
	if got := normalizeJSON(t, body); got != `{"payload":{"name":"Rex"}}` {
		t.Errorf("body = %s, want the hook-declared envelope", got)
	}
}

// TestRuntimeBodyUnwrapCollision asserts the collision rule is an error with
// a clear message rather than a silently dropped flag.
func TestRuntimeBodyUnwrapCollision(t *testing.T) {
	spec := []byte(`
openapi: 3.1.0
info: { title: t, version: "1" }
paths:
  /x:
    post:
      operationId: createX
      tags: [x]
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                json:
                  type: object
                  properties:
                    petId: { type: string }
                    pet_id: { type: string }
      responses: { "200": { description: OK } }
`)
	_, err := Build(spec, Options{Exec: oascmd.ExecOptions{BaseURL: "http://x"}})
	if err == nil || !strings.Contains(err.Error(), "both map to --pet-id") {
		t.Fatalf("err = %v, want a collision error", err)
	}
}

// TestRuntimeLockRecordsUnwrappedSurface pins that the lock reflects the
// post-unwrap flags and the envelope path, so drift stays truthful.
func TestRuntimeLockRecordsUnwrappedSurface(t *testing.T) {
	l, err := ComputeLock(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := l.Operations["createOrder"]
	if !ok {
		t.Fatal("createOrder missing from the lock")
	}
	if entry.Body == nil || entry.Body.Wrap != "json" || !entry.Body.Flat {
		t.Errorf("body = %+v, want flat with wrap json", entry.Body)
	}
	var names []string
	for _, f := range entry.Flags {
		names = append(names, f.Name)
		if f.Source != "body" && f.Source != "path" && f.Source != "query" {
			t.Errorf("unexpected source %q", f.Source)
		}
	}
	want := "express,pet-id,quantity"
	if strings.Join(names, ",") != want {
		t.Errorf("flags = %v, want %s", names, want)
	}
}
