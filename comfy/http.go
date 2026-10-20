package comfy

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody is how much of an unexpected answer's body an HTTPError keeps.
const maxErrorBody = 4 << 10

// lenient decodes what the server sends: Python's json writes strings it read with
// surrogateescape as lone surrogates ("\udc80"), which become U+FFFD rather than an error.
var lenient = jsontext.AllowInvalidUTF8(true)

// maxBody is the largest JSON answer the client reads: a job record with many outputs is far
// smaller.
const maxBody = 64 << 20

// parseBaseURL checks the server's base URL (docs/spec.md §7.2): http or https, no credentials,
// no fragment. Its path prefix and query are kept on every request. The error doesn't quote the
// URL, which may hold a token.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, errors.New("comfy: the server URL doesn't parse")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("comfy: the server URL must be http or https")
	case u.Host == "":
		return nil, errors.New("comfy: the server URL has no host")
	case u.User != nil:
		return nil, errors.New("comfy: the server URL can't hold credentials; send them in Options.Header")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, errors.New("comfy: the server URL can't have a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// endpoint is a path under the base URL, with the base URL's query and the given one.
func endpoint(base *url.URL, path string, query url.Values) *url.URL {
	u := *base
	u.Path = base.Path + "/" + path
	q := base.Query()
	maps.Copy(q, query)
	u.RawQuery = q.Encode()
	return &u
}

// shown is a URL fit for messages: without its query, where servers expect tokens.
func shown(u *url.URL) string {
	c := *u
	c.RawQuery = ""
	c.ForceQuery = false
	return c.String()
}

// redacted is an error whose message has every URL with a query replaced by the URL without it.
// Unwrap keeps the original, so errors.Is and errors.As still work.
type redacted struct {
	msg string
	err error
}

func (e *redacted) Error() string { return e.msg }
func (e *redacted) Unwrap() error { return e.err }

// redact removes u's query from err's message.
func redact(err error, u *url.URL) error {
	if err == nil || u.RawQuery == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), u.String(), shown(u))
	msg = strings.ReplaceAll(msg, u.RawQuery, "…")
	return &redacted{msg: msg, err: err}
}

// do sends a request to path under the base URL, with the client's headers, and returns the
// response whatever its status. A transport error is wrapped with the request, its URL redacted.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := endpoint(c.base, path, query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("comfy: %s /%s: %w", method, path, redact(err, u))
	}
	maps.Copy(req.Header, c.header)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfy: %s /%s: %w", method, path, redact(err, u))
	}
	return resp, nil
}

// readJSON reads a response's body as one JSON value, raw, and closes it.
func readJSON(resp *http.Response, op string) (jsontext.Value, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("comfy: %s: reading the answer: %w", op, err)
	}
	v := jsontext.Value(bytes.TrimSpace(data))
	if !v.IsValid(lenient) {
		return nil, &ProtocolError{Op: op, Err: errors.New("the answer isn't JSON")}
	}
	return v, nil
}

// decodeJSON reads a response's body into v, leniently: unknown members are ignored and null is
// absent, as a Python server sends them.
func decodeJSON(resp *http.Response, op string, v any) error {
	data, err := readJSON(resp, op)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v, lenient); err != nil {
		return &ProtocolError{Op: op, Err: fmt.Errorf("the answer doesn't fit: %w", err)}
	}
	return nil
}

// unexpected is the HTTPError for an answer the protocol doesn't expect; it closes the body.
func unexpected(resp *http.Response, op string) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &HTTPError{Op: op, StatusCode: resp.StatusCode, Body: body}
}

// drain discards what is left of a body and closes it, so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
}
