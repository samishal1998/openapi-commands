package oascmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AuthFunc mutates an outgoing request to attach credentials, e.g. set an
// Authorization header. Returning an error aborts the request.
type AuthFunc func(req *http.Request) error

// ExecOptions configures the HTTP executor shared by runtime commands and
// generated commands.
type ExecOptions struct {
	// BaseURL is the API base URL the operation path is joined onto.
	// Required.
	BaseURL string
	// Client is the HTTP client; http.DefaultClient when nil.
	Client *http.Client
	// Auth, when non-nil, is called on every request before it is sent.
	Auth AuthFunc
	// Out is where the response body is printed; os.Stdout is used by the
	// command layer when nil here.
	Out io.Writer
	// Raw disables pretty-printing of JSON responses (the --json flag).
	Raw bool
	// OnBeforeExecute hooks run in order after the request is built and
	// authenticated, before it is sent. Returning an error aborts.
	OnBeforeExecute []func(ctx context.Context, req *http.Request) error
	// OnAfterExecute hooks run in order after a response is received,
	// before it is printed. Returning an error aborts.
	OnAfterExecute []func(ctx context.Context, resp *http.Response) error
}

// Request is a fully resolved operation invocation, ready to execute.
type Request struct {
	Method string
	// Path is the OpenAPI path template.
	Path string
	// PathParams substitute {name} segments in Path. Values are
	// URL-escaped.
	PathParams map[string]string
	// Query values are appended to the URL. Multiple values per key are
	// sent as repeated parameters.
	Query url.Values
	// Body, when non-nil, is sent as application/json.
	Body any
	// RawBody, when non-empty, is sent verbatim as application/json and
	// wins over Body. This backs the --data flag.
	RawBody []byte
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	StatusCode int
	Status     string
	// Snippet is the leading part of the response body (capped).
	Snippet string
}

func (e *StatusError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("request failed: %s", e.Status)
	}
	return fmt.Sprintf("request failed: %s: %s", e.Status, e.Snippet)
}

const snippetLimit = 512

// Execute builds the HTTP request, applies auth and hooks, sends it, and
// prints the response body to opts.Out (pretty-printed JSON unless opts.Raw).
// Non-2xx responses return a *StatusError.
func Execute(ctx context.Context, opts ExecOptions, r Request) error {
	if opts.BaseURL == "" {
		return fmt.Errorf("oascmd: base URL is required")
	}
	req, err := BuildHTTPRequest(ctx, opts.BaseURL, r)
	if err != nil {
		return err
	}
	if opts.Auth != nil {
		if err := opts.Auth(req); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	for _, hook := range opts.OnBeforeExecute {
		if err := hook(ctx, req); err != nil {
			return err
		}
	}

	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	for _, hook := range opts.OnAfterExecute {
		if err := hook(ctx, resp); err != nil {
			return err
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > snippetLimit {
			snippet = snippet[:snippetLimit] + "..."
		}
		return &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, Snippet: snippet}
	}

	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	return printBody(out, body, opts.Raw)
}

// BuildHTTPRequest resolves the path template, query, and body of r against
// baseURL into an *http.Request.
func BuildHTTPRequest(ctx context.Context, baseURL string, r Request) (*http.Request, error) {
	path := r.Path
	for name, value := range r.PathParams {
		placeholder := "{" + name + "}"
		if !strings.Contains(path, placeholder) {
			return nil, fmt.Errorf("path %q has no parameter %q", r.Path, name)
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
	}
	if i := strings.Index(path, "{"); i >= 0 {
		return nil, fmt.Errorf("path %q has unresolved parameters", path)
	}

	full := strings.TrimSuffix(baseURL, "/") + path
	u, err := url.Parse(full)
	if err != nil {
		return nil, err
	}
	if len(r.Query) > 0 {
		q := u.Query()
		for k, vs := range r.Query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}

	var body io.Reader
	hasBody := false
	switch {
	case len(r.RawBody) > 0:
		if !json.Valid(r.RawBody) {
			return nil, fmt.Errorf("--data is not valid JSON")
		}
		body = bytes.NewReader(r.RawBody)
		hasBody = true
	case r.Body != nil:
		encoded, err := json.Marshal(r.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
		hasBody = true
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func printBody(out io.Writer, body []byte, raw bool) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !raw {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			pretty.WriteByte('\n')
			_, err = out.Write(pretty.Bytes())
			return err
		}
	}
	if _, err := out.Write(body); err != nil {
		return err
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		_, _ = out.Write([]byte("\n"))
	}
	return nil
}
