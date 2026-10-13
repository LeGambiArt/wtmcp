package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ProxyTransport implements http.RoundTripper by serializing HTTP
// requests through the core's stdin/stdout proxy protocol. Go plugins
// can pass &http.Client{Transport: NewProxyTransport(p)} to SDK
// clients (Google APIs, GitLab, etc.) to route all traffic through
// the core proxy.
//
// Authentication is handled by the core's auth provider (configured
// via services.auth in plugin.yaml). SDK-set Authorization headers
// are stripped by the proxy's dangerous-header filter — the core is
// the sole auth gateway.
//
// ProxyTransport is safe for sequential use only. All verified target
// SDKs (Google API client-go, GitLab client-go) make sequential HTTP
// calls. If a future SDK uses internal concurrency, the handler SDK
// would need a channel-based message router.
type ProxyTransport struct {
	plugin *Plugin
}

// NewProxyTransport creates a transport that routes HTTP through the
// core proxy. Pass the resulting *http.Client to SDK constructors.
func NewProxyTransport(p *Plugin) *ProxyTransport {
	return &ProxyTransport{plugin: p}
}

// RoundTrip implements http.RoundTripper.
func (t *ProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	msg, err := t.encodeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("proxy transport: encode request: %w", err)
	}

	t.plugin.send(msg)

	resp, err := t.plugin.waitFor(msg.ID, TypeHTTPResponse)
	if err != nil {
		return nil, fmt.Errorf("proxy transport: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("proxy transport: [%s] %s", resp.Error.Code, resp.Error.Message)
	}

	return t.decodeResponse(req, resp)
}

func (t *ProxyTransport) encodeRequest(req *http.Request) (Message, error) {
	msg := Message{
		ID:       t.plugin.nextMsgID("http"),
		Type:     TypeHTTPRequest,
		Method:   req.Method,
		URL:      req.URL.String(),
		ParentID: t.plugin.callID,
	}

	if len(req.Header) > 0 {
		headers := make(map[string]string, len(req.Header))
		for k, v := range req.Header {
			headers[k] = strings.Join(v, ", ")
		}
		msg.Headers = headers
	}

	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return Message{}, fmt.Errorf("read request body: %w", err)
		}
		req.Body.Close() //nolint:errcheck,gosec // best effort

		ct := req.Header.Get("Content-Type")
		if strings.Contains(ct, "application/json") && json.Valid(body) {
			msg.Body = json.RawMessage(body)
		} else {
			encoded := base64.StdEncoding.EncodeToString(body)
			b, _ := json.Marshal(encoded) //nolint:errcheck // json.Marshal on string cannot fail
			msg.Body = json.RawMessage(b)
			msg.BodyEncoding = "base64"
		}
	}

	return msg, nil
}

func (t *ProxyTransport) decodeResponse(req *http.Request, msg Message) (*http.Response, error) {
	body, err := decodeResponseBody(msg.Body, msg.BodyEncoding)
	if err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}

	resp := &http.Response{
		StatusCode:    msg.Status,
		Status:        fmt.Sprintf("%d %s", msg.Status, http.StatusText(msg.Status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}

	for k, v := range msg.Headers {
		resp.Header.Set(k, v)
	}

	return resp, nil
}

func decodeResponseBody(raw json.RawMessage, encoding string) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	if encoding == "base64" {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, fmt.Errorf("unmarshal base64 body: %w", err)
		}
		return base64.StdEncoding.DecodeString(encoded)
	}

	// Try to unquote as a JSON string (text/* responses).
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return []byte(s), nil
		}
	}

	// Raw JSON: return as-is.
	return []byte(raw), nil
}

// Client returns an *http.Client configured to use this transport.
// Convenience method for passing to SDK constructors.
func (t *ProxyTransport) Client() *http.Client {
	return &http.Client{
		Transport: t,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// WithNoAuth returns a RequestOption that disables auth injection
// for a specific request made via HTTP() (useful for public endpoints).
func WithNoAuth() RequestOption {
	return func(m *Message) { m.NoAuth = true }
}

// Compile-time interface check.
var _ http.RoundTripper = (*ProxyTransport)(nil)
