package oascmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
