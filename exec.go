package oascmd

import (
	"bytes"
	"context"
	"encoding/base64"
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

// BearerAuth sets "Authorization: Bearer <token>" on every request.
func BearerAuth(token string) AuthFunc {
	return func(req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// APIKeyAuth sets an API key in the named header, e.g.
// APIKeyAuth("X-API-Key", key).
func APIKeyAuth(header, value string) AuthFunc {
	return func(req *http.Request) error {
		if header == "" {
			return fmt.Errorf("oascmd: APIKeyAuth needs a header name")
		}
		req.Header.Set(header, value)
		return nil
	}
}

// BasicAuth sets HTTP basic authentication credentials.
func BasicAuth(username, password string) AuthFunc {
	return func(req *http.Request) error {
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Authorization", "Basic "+encoded)
		return nil
	}
}

// ChainAuth applies several AuthFuncs in order, skipping nil entries. It is
// the escape hatch for APIs wanting more than one credential (e.g. a bearer
// token plus a tenant key).
func ChainAuth(funcs ...AuthFunc) AuthFunc {
	return func(req *http.Request) error {
		for _, f := range funcs {
			if f == nil {
				continue
			}
			if err := f(req); err != nil {
				return err
			}
		}
		return nil
	}
}

// ExecOptions configures the HTTP executor shared by runtime commands and
// generated commands.
type ExecOptions struct {
	// BaseURL is the API base URL the operation path is joined onto.
	// Required.
	BaseURL string
	// Client is the HTTP client; http.DefaultClient when nil. Supply your
	// own to control timeouts, proxies, TLS and transport.
	Client *http.Client
	// CookieJar, when non-nil, is installed on the client used for the
	// request (a shallow copy of Client, so the caller's client is not
	// mutated). Ignored when Client already has a jar.
	CookieJar http.CookieJar
	// Headers are default headers applied to every request before auth
	// and the before-execute hooks. They win over the executor's own
	// defaults (Accept, Content-Type) and can be overridden per request
	// by an OnBeforeExecute hook.
	Headers http.Header
	// Cookies are attached to every request, in addition to anything the
	// CookieJar contributes.
	Cookies []*http.Cookie
	// Auth, when non-nil, is called on every request before it is sent,
	// after Headers and Cookies are applied. See BearerAuth, APIKeyAuth,
	// BasicAuth and ChainAuth.
	Auth AuthFunc
	// Out is where the response body is printed; os.Stdout is used by the
	// command layer when nil here.
	Out io.Writer
	// Raw disables pretty-printing of JSON responses (the --json flag).
	Raw bool
	// OnBeforeExecute hooks run in order after the request is built,
	// headers, cookies and auth are applied, and before it is sent. They
	// may mutate the request (headers, URL, body: use SetRequestBody) or
	// abort by returning an error.
	OnBeforeExecute []func(ctx context.Context, req *http.Request) error
	// OnAfterExecute hooks run in order after a response is received,
	// before its body is read and printed. They may inspect and transform
	// the response (including replacing resp.Body) or abort by returning
	// an error. They run for every response, including non-2xx.
	OnAfterExecute []func(ctx context.Context, resp *http.Response) error
	// OnRequestError runs when the transport fails (no response: DNS,
	// connection, timeout). attempt counts from 1. Returning retry=true
	// re-sends the same request (the body is replayed); returning an
	// error aborts with that error, and returning (false, nil) aborts
	// with the transport error. Nil means "no retry".
	OnRequestError func(ctx context.Context, req *http.Request, attempt int, err error) (bool, error)
}

// maxRequestAttempts caps OnRequestError retries so a hook that always asks
// to retry cannot loop forever.
const maxRequestAttempts = 100

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

// SetRequestBody replaces the body of an in-flight request, keeping it
// replayable (ContentLength and GetBody are updated). It is the supported
// way for an OnBeforeExecute hook to rewrite the payload.
func SetRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	buf := append([]byte(nil), body...)
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
}

// Execute builds the HTTP request, applies headers, cookies, auth and hooks,
// sends it, and prints the response body to opts.Out (pretty-printed JSON
// unless opts.Raw). Non-2xx responses return a *StatusError.
//
// Order of operations:
//
//	build request -> ExecOptions.Headers -> ExecOptions.Cookies ->
//	Auth -> OnBeforeExecute (in order) -> send
//	  transport error -> OnRequestError (may retry from "send")
//	  response        -> OnAfterExecute (in order) -> status check -> print
func Execute(ctx context.Context, opts ExecOptions, r Request) error {
	if opts.BaseURL == "" {
		return fmt.Errorf("oascmd: base URL is required")
	}
	req, err := BuildHTTPRequest(ctx, opts.BaseURL, r)
	if err != nil {
		return err
	}
	for key, values := range opts.Headers {
		req.Header.Del(key)
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	for _, c := range opts.Cookies {
		if c != nil {
			req.AddCookie(c)
		}
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
	if opts.CookieJar != nil && client.Jar == nil {
		clone := *client
		clone.Jar = opts.CookieJar
		client = &clone
	}

	var resp *http.Response
	for attempt := 1; ; attempt++ {
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if opts.OnRequestError == nil || attempt >= maxRequestAttempts {
			return err
		}
		retry, hookErr := opts.OnRequestError(ctx, req, attempt, err)
		if hookErr != nil {
			return hookErr
		}
		if !retry {
			return err
		}
		replay, replayErr := replayRequest(req)
		if replayErr != nil {
			return replayErr
		}
		req = replay
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

// replayRequest returns a copy of req with a fresh body, so a retry can send
// the same payload again.
func replayRequest(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.GetBody == nil {
		return clone, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("oascmd: replay request body: %w", err)
	}
	clone.Body = body
	return clone, nil
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

	var payload []byte
	switch {
	case len(r.RawBody) > 0:
		if !json.Valid(r.RawBody) {
			return nil, fmt.Errorf("--data is not valid JSON")
		}
		payload = r.RawBody
	case r.Body != nil:
		encoded, err := json.Marshal(r.Body)
		if err != nil {
			return nil, err
		}
		payload = encoded
	}

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		SetRequestBody(req, payload)
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
