package oascmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBuildHTTPRequestPathAndQuery(t *testing.T) {
	req, err := BuildHTTPRequest(context.Background(), "https://api.example.com/", Request{
		Method:     "GET",
		Path:       "/pets/{petId}",
		PathParams: map[string]string{"petId": "a b/c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The value is path-escaped, so the slash does not create a segment.
	if got := req.URL.EscapedPath(); got != "/pets/a%20b%2Fc" {
		t.Errorf("path = %q, want the escaped parameter", got)
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Error("Accept header not set")
	}
}

func TestBuildHTTPRequestUnresolvedPathParam(t *testing.T) {
	_, err := BuildHTTPRequest(context.Background(), "http://x", Request{
		Method: "GET", Path: "/pets/{petId}",
	})
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("err = %v, want an unresolved-parameter error", err)
	}
}

func TestBuildHTTPRequestUnknownPathParam(t *testing.T) {
	_, err := BuildHTTPRequest(context.Background(), "http://x", Request{
		Method: "GET", Path: "/pets", PathParams: map[string]string{"nope": "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "no parameter") {
		t.Errorf("err = %v, want an unknown-parameter error", err)
	}
}

func TestBuildHTTPRequestBody(t *testing.T) {
	req, err := BuildHTTPRequest(context.Background(), "http://x", Request{
		Method: "POST", Path: "/pets", Body: map[string]any{"name": "Rex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Error("Content-Type not set for a body request")
	}
	body, _ := io.ReadAll(req.Body)
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["name"] != "Rex" {
		t.Errorf("body = %s", body)
	}
}

func TestBuildHTTPRequestRawBodyWins(t *testing.T) {
	req, err := BuildHTTPRequest(context.Background(), "http://x", Request{
		Method:  "POST",
		Path:    "/pets",
		Body:    map[string]any{"name": "ignored"},
		RawBody: []byte(`{"name":"raw"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"name":"raw"}` {
		t.Errorf("body = %s, want RawBody to win", body)
	}
}

func TestExecuteRequiresBaseURL(t *testing.T) {
	err := Execute(context.Background(), ExecOptions{}, Request{Method: "GET", Path: "/"})
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Errorf("err = %v, want a base-URL error", err)
	}
}

func TestExecuteAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not be sent")
	}))
	defer server.Close()

	err := Execute(context.Background(), ExecOptions{
		BaseURL: server.URL,
		Auth:    func(req *http.Request) error { return io.ErrUnexpectedEOF },
	}, Request{Method: "GET", Path: "/"})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Errorf("err = %v, want an auth error", err)
	}
}

func TestExecutePrettyAndRaw(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":1,"b":[2]}`))
	}))
	defer server.Close()

	var pretty bytes.Buffer
	if err := Execute(context.Background(), ExecOptions{BaseURL: server.URL, Out: &pretty},
		Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty.String(), "\n  \"a\": 1") {
		t.Errorf("pretty output = %q", pretty.String())
	}

	var raw bytes.Buffer
	if err := Execute(context.Background(), ExecOptions{BaseURL: server.URL, Out: &raw, Raw: true},
		Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(raw.String()) != `{"a":1,"b":[2]}` {
		t.Errorf("raw output = %q", raw.String())
	}
}

func TestExecuteNonJSONBodyPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain text"))
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := Execute(context.Background(), ExecOptions{BaseURL: server.URL, Out: &out},
		Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "plain text" {
		t.Errorf("output = %q", out.String())
	}
}

func TestExecuteEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := Execute(context.Background(), ExecOptions{BaseURL: server.URL, Out: &out},
		Request{Method: "DELETE", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing for an empty body", out.String())
	}
}

func TestStatusErrorSnippetTruncated(t *testing.T) {
	long := strings.Repeat("x", snippetLimit*2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(long))
	}))
	defer server.Close()

	err := Execute(context.Background(), ExecOptions{BaseURL: server.URL, Out: io.Discard},
		Request{Method: "GET", Path: "/"})
	statusErr, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}
	if statusErr.StatusCode != 500 {
		t.Errorf("status = %d", statusErr.StatusCode)
	}
	if len(statusErr.Snippet) != snippetLimit+3 {
		t.Errorf("snippet length = %d, want the body truncated to %d + ellipsis",
			len(statusErr.Snippet), snippetLimit)
	}
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"\n", false},
		{"whatever\n", false},
	}
	for _, tt := range tests {
		got, err := Confirm(strings.NewReader(tt.input), io.Discard, "Continue?")
		if err != nil {
			t.Fatalf("input %q: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("Confirm(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// captured records what an httptest server saw.
type captured struct {
	header  http.Header
	cookies []*http.Cookie
	body    []byte
}

// echoServer records the first request it receives and replies with 200 {}.
func echoServer(t *testing.T, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		got.cookies = r.Cookies()
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestExecuteClientConfiguration covers Q1: headers, cookies, auth helpers
// and a caller-supplied *http.Client, asserted against a real server.
func TestExecuteClientConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(o *ExecOptions)
		check  func(t *testing.T, got captured)
	}{
		{
			name:   "bearer auth",
			mutate: func(o *ExecOptions) { o.Auth = BearerAuth("t0ken") },
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("Authorization"); v != "Bearer t0ken" {
					t.Errorf("Authorization = %q", v)
				}
			},
		},
		{
			name:   "api key auth in a named header",
			mutate: func(o *ExecOptions) { o.Auth = APIKeyAuth("X-API-Key", "abc") },
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("X-API-Key"); v != "abc" {
					t.Errorf("X-API-Key = %q", v)
				}
			},
		},
		{
			name:   "basic auth",
			mutate: func(o *ExecOptions) { o.Auth = BasicAuth("alice", "s3cret") },
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("Authorization"); v != "Basic YWxpY2U6czNjcmV0" {
					t.Errorf("Authorization = %q", v)
				}
			},
		},
		{
			name: "chained auth applies both",
			mutate: func(o *ExecOptions) {
				o.Auth = ChainAuth(BearerAuth("t"), nil, APIKeyAuth("X-Tenant", "acme"))
			},
			check: func(t *testing.T, got captured) {
				if got.header.Get("Authorization") != "Bearer t" || got.header.Get("X-Tenant") != "acme" {
					t.Errorf("headers = %v", got.header)
				}
			},
		},
		{
			name: "custom auth callback",
			mutate: func(o *ExecOptions) {
				o.Auth = func(req *http.Request) error {
					req.Header.Set("X-Signature", "sig("+req.URL.Path+")")
					return nil
				}
			},
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("X-Signature"); v != "sig(/pets)" {
					t.Errorf("X-Signature = %q", v)
				}
			},
		},
		{
			name: "static default headers",
			mutate: func(o *ExecOptions) {
				o.Headers = http.Header{"X-Client": {"petstore/1"}, "Accept": {"application/vnd.api+json"}}
			},
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("X-Client"); v != "petstore/1" {
					t.Errorf("X-Client = %q", v)
				}
				if v := got.header.Get("Accept"); v != "application/vnd.api+json" {
					t.Errorf("Headers must win over the default Accept, got %q", v)
				}
			},
		},
		{
			name: "explicit cookies",
			mutate: func(o *ExecOptions) {
				o.Cookies = []*http.Cookie{{Name: "session", Value: "xyz"}, nil}
			},
			check: func(t *testing.T, got captured) {
				if len(got.cookies) != 1 || got.cookies[0].Name != "session" || got.cookies[0].Value != "xyz" {
					t.Errorf("cookies = %v", got.cookies)
				}
			},
		},
		{
			name: "per-request header mutation via a hook",
			mutate: func(o *ExecOptions) {
				o.Headers = http.Header{"X-Trace": {"static"}}
				o.OnBeforeExecute = append(o.OnBeforeExecute, func(ctx context.Context, req *http.Request) error {
					req.Header.Set("X-Trace", "per-request")
					return nil
				})
			},
			check: func(t *testing.T, got captured) {
				if v := got.header.Get("X-Trace"); v != "per-request" {
					t.Errorf("X-Trace = %q, want the hook to win", v)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got captured
			srv := echoServer(t, &got)
			opts := ExecOptions{BaseURL: srv.URL, Out: io.Discard}
			tc.mutate(&opts)
			if err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"}); err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
		})
	}
}

// TestExecuteCookieJar checks that a jar is used without mutating the
// caller's client, and that the server's Set-Cookie is replayed.
func TestExecuteCookieJar(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err == nil {
			seen = append(seen, c.Value)
		} else {
			seen = append(seen, "")
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "from-server", Path: "/"})
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{}
	opts := ExecOptions{BaseURL: srv.URL, Out: io.Discard, Client: client, CookieJar: jar}
	for i := 0; i < 2; i++ {
		if err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != "from-server" {
		t.Errorf("cookies seen by the server = %v, want the jar to replay the second time", seen)
	}
	if client.Jar != nil {
		t.Error("the caller's client was mutated; the jar must be installed on a copy")
	}
}

// TestExecuteCustomClient proves a caller-supplied client (transport,
// timeouts, TLS) is the one actually used.
func TestExecuteCustomClient(t *testing.T) {
	used := false
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		used = true
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Body:   io.NopCloser(strings.NewReader(`{}`)),
			Header: http.Header{},
		}, nil
	})}
	opts := ExecOptions{BaseURL: "http://example.invalid", Out: io.Discard, Client: client}
	if err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"}); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("the supplied client's transport was not used")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestExecuteHookOrderAndMutation covers Q3: hooks fire in declaration
// order, may mutate the request (headers, URL, body), and may veto.
func TestExecuteHookOrder(t *testing.T) {
	var got captured
	srv := echoServer(t, &got)
	var order []string
	opts := ExecOptions{
		BaseURL: srv.URL,
		Out:     io.Discard,
		Headers: http.Header{"X-Stage": {"headers"}},
		Auth: func(req *http.Request) error {
			order = append(order, "auth:"+req.Header.Get("X-Stage"))
			return nil
		},
		OnBeforeExecute: []func(context.Context, *http.Request) error{
			func(ctx context.Context, req *http.Request) error {
				order = append(order, "before1")
				req.URL.Path = "/pets/rewritten"
				SetRequestBody(req, []byte(`{"rewritten":true}`))
				return nil
			},
			func(ctx context.Context, req *http.Request) error {
				order = append(order, "before2")
				return nil
			},
		},
		OnAfterExecute: []func(context.Context, *http.Response) error{
			func(ctx context.Context, resp *http.Response) error {
				order = append(order, "after1")
				resp.Body = io.NopCloser(strings.NewReader(`{"transformed":true}`))
				return nil
			},
			func(ctx context.Context, resp *http.Response) error {
				order = append(order, "after2:"+resp.Status)
				return nil
			},
		},
	}
	var out bytes.Buffer
	opts.Out = &out
	err := Execute(context.Background(), opts, Request{
		Method: "POST", Path: "/pets", Body: map[string]any{"original": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"auth:headers", "before1", "before2", "after1", "after2:200 OK"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("hook order = %v, want %v", order, want)
	}
	if string(got.body) != `{"rewritten":true}` {
		t.Errorf("server saw body %q, want the hook's rewrite", got.body)
	}
	if !strings.Contains(out.String(), "transformed") {
		t.Errorf("output = %q, want the after hook's replacement body", out.String())
	}
}

func TestExecuteHookVeto(t *testing.T) {
	var got captured
	srv := echoServer(t, &got)
	sentinel := errors.New("vetoed")
	tests := []struct {
		name string
		opts func(o *ExecOptions)
		sent bool
	}{
		{
			name: "auth veto",
			opts: func(o *ExecOptions) { o.Auth = func(*http.Request) error { return sentinel } },
		},
		{
			name: "before-execute veto",
			opts: func(o *ExecOptions) {
				o.OnBeforeExecute = []func(context.Context, *http.Request) error{
					func(context.Context, *http.Request) error { return sentinel },
				}
			},
		},
		{
			name: "after-execute veto",
			opts: func(o *ExecOptions) {
				o.OnAfterExecute = []func(context.Context, *http.Response) error{
					func(context.Context, *http.Response) error { return sentinel },
				}
			},
			sent: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got = captured{}
			opts := ExecOptions{BaseURL: srv.URL, Out: io.Discard}
			tc.opts(&opts)
			err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"})
			if !errors.Is(err, sentinel) {
				t.Fatalf("err = %v, want the hook's error", err)
			}
			if sent := got.header != nil; sent != tc.sent {
				t.Errorf("request reached the server = %v, want %v", sent, tc.sent)
			}
		})
	}
}

// TestExecuteOnRequestError covers the transport-failure hook: it sees each
// attempt, can retry (replaying the body), and can abort.
func TestExecuteOnRequestError(t *testing.T) {
	var got captured
	srv := echoServer(t, &got)

	t.Run("retry succeeds against a good URL", func(t *testing.T) {
		attempts := 0
		opts := ExecOptions{
			BaseURL: "http://127.0.0.1:1", // refuses connections
			Out:     io.Discard,
			OnRequestError: func(ctx context.Context, req *http.Request, attempt int, err error) (bool, error) {
				attempts = attempt
				if attempt > 1 {
					return false, nil
				}
				u, _ := url.Parse(srv.URL)
				req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
				return true, nil
			},
		}
		err := Execute(context.Background(), opts, Request{
			Method: "POST", Path: "/pets", Body: map[string]any{"name": "Rex"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want the retry to succeed after one failure", attempts)
		}
		if string(got.body) != `{"name":"Rex"}` {
			t.Errorf("replayed body = %q, want the original payload", got.body)
		}
	})

	t.Run("hook error wins", func(t *testing.T) {
		sentinel := errors.New("give up")
		opts := ExecOptions{
			BaseURL: "http://127.0.0.1:1",
			Out:     io.Discard,
			OnRequestError: func(context.Context, *http.Request, int, error) (bool, error) {
				return false, sentinel
			},
		}
		err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"})
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want the hook's error", err)
		}
	})

	t.Run("no hook means the transport error surfaces", func(t *testing.T) {
		opts := ExecOptions{BaseURL: "http://127.0.0.1:1", Out: io.Discard}
		if err := Execute(context.Background(), opts, Request{Method: "GET", Path: "/pets"}); err == nil {
			t.Error("want the transport error")
		}
	})
}
