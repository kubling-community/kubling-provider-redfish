// Package redfish provides the provider's bounded, authenticated Redfish
// transport. External Redfish client-library types are kept behind this API.
package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
)

const (
	// ServiceRootURI is the standard Redfish service-root path.
	ServiceRootURI = "/redfish/v1/"

	defaultRequestTimeout   = 15 * time.Second
	defaultMaxResponseBytes = 8 * 1024 * 1024
)

// Options bounds requests made by a Client.
type Options struct {
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	Retry            RetryPolicy
}

// DefaultOptions returns conservative standalone client limits. Provider code
// should replace them with the already validated execution configuration.
func DefaultOptions() Options {
	return Options{
		RequestTimeout:   defaultRequestTimeout,
		MaxResponseBytes: defaultMaxResponseBytes,
		Retry:            DefaultRetryPolicy(),
	}
}

func normalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaults.RequestTimeout
	}
	if options.RequestTimeout < 0 {
		return Options{}, errors.New("Redfish request timeout must be positive")
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if options.MaxResponseBytes < 0 {
		return Options{}, errors.New("Redfish maximum response bytes must be positive")
	}
	retry, err := normalizeRetryPolicy(options.Retry)
	if err != nil {
		return Options{}, err
	}
	options.Retry = retry
	return options, nil
}

// Document is an immutable raw Redfish JSON resource.
type Document struct {
	uri        string
	raw        json.RawMessage
	observedAt time.Time
}

func newDocument(uri string, raw []byte) Document {
	return newDocumentAt(uri, raw, timeNow().UTC())
}

func newDocumentAt(uri string, raw []byte, observedAt time.Time) Document {
	return Document{
		uri:        uri,
		raw:        append(json.RawMessage(nil), raw...),
		observedAt: observedAt,
	}
}

func (d Document) clone() Document {
	return newDocumentAt(d.uri, d.raw, d.observedAt)
}

// URI returns the canonical path and opaque query used to retrieve the resource.
func (d Document) URI() string {
	return d.uri
}

// Raw returns a detached copy of the resource JSON.
func (d Document) Raw() json.RawMessage {
	return append(json.RawMessage(nil), d.raw...)
}

// ObservedAt returns when the complete response document was received.
func (d Document) ObservedAt() time.Time {
	return d.observedAt
}

// Decode unmarshals the document without exposing its body in errors.
func (d Document) Decode(destination any) error {
	if destination == nil {
		return newError("decode", ErrorKindInvalidResponse, 0, false, errors.New("destination is nil"))
	}
	if err := json.Unmarshal(d.raw, destination); err != nil {
		return newError("decode", ErrorKindInvalidResponse, 0, false, err)
	}
	return nil
}

// String formats only a redacted URI and body size.
func (d Document) String() string {
	return fmt.Sprintf("Redfish document %q (%d bytes)", safeURI(d.uri), len(d.raw))
}

// GoString formats only a redacted URI and body size.
func (d Document) GoString() string {
	return d.String()
}

// Client owns one authenticated connection to one Redfish service.
type Client struct {
	mu            sync.RWMutex
	closed        bool
	origin        *url.URL
	adapter       *gofishAdapter
	transport     *http.Transport
	serviceRoot   Document
	responseLimit int64
	closeTimeout  time.Duration
	closeErr      error
}

type clientDependencies struct {
	sleep sleepFunc
	now   func() time.Time
}

// NewClient connects to one configured device and captures its ServiceRoot.
func NewClient(ctx context.Context, configuredDevice device.Device, options Options) (*Client, error) {
	return newClient(ctx, configuredDevice, options, clientDependencies{})
}

func newClient(
	ctx context.Context,
	configuredDevice device.Device,
	options Options,
	dependencies clientDependencies,
) (*Client, error) {
	if ctx == nil {
		return nil, newError("connect", ErrorKindConfiguration, 0, false, errors.New("context is nil"))
	}
	normalizedOptions, err := normalizeOptions(options)
	if err != nil {
		return nil, newError("connect", ErrorKindConfiguration, 0, false, err)
	}
	origin, err := parseEndpoint(configuredDevice.Endpoint())
	if err != nil {
		return nil, newError("connect", ErrorKindConfiguration, 0, false, err)
	}
	credential := configuredDevice.Credential()
	if credential.Username().Reveal() == "" || credential.Password().Reveal() == "" {
		return nil, newError("connect", ErrorKindConfiguration, 0, false, errors.New("credential is empty"))
	}
	tlsConfig, err := buildTLSConfig(configuredDevice.TLS())
	if err != nil {
		return nil, newError("connect", ErrorKindTLS, 0, false, err)
	}

	httpClient, transport, tracker := buildHTTPClient(
		origin,
		tlsConfig,
		normalizedOptions,
		dependencies,
	)
	adapter, err := connectGofish(ctx, configuredDevice, origin, httpClient, tracker)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, normalizeError("connect", err)
	}

	client := &Client{
		origin:        origin,
		adapter:       adapter,
		transport:     transport,
		responseLimit: normalizedOptions.MaxResponseBytes,
		closeTimeout:  normalizedOptions.RequestTimeout,
	}
	serviceRoot, err := client.Get(ctx, ServiceRootURI)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	client.serviceRoot = serviceRoot
	return client, nil
}

// ServiceRoot returns a detached copy of the ServiceRoot captured at connect time.
func (c *Client) ServiceRoot() Document {
	if c == nil {
		return Document{}
	}
	return c.serviceRoot.clone()
}

// Resolve resolves a Redfish link against a resource URI and rejects links to
// another origin, userinfo, fragments and HTTPS downgrades.
func (c *Client) Resolve(baseURI string, link string) (string, error) {
	if c == nil {
		return "", newError("resolve URI", ErrorKindConfiguration, 0, false, errors.New("client is nil"))
	}
	resolved, err := resolveURI(c.origin, baseURI, link)
	if err != nil {
		return "", newError("resolve URI", ErrorKindUnsafeURI, 0, false, err)
	}
	return resolved, nil
}

// Get retrieves raw JSON, resolving the link against the ServiceRoot.
func (c *Client) Get(ctx context.Context, link string) (Document, error) {
	return c.GetFrom(ctx, ServiceRootURI, link)
}

// GetFrom retrieves raw JSON after resolving link against baseURI.
func (c *Client) GetFrom(ctx context.Context, baseURI string, link string) (Document, error) {
	if c == nil {
		return Document{}, newError("GET", ErrorKindConfiguration, 0, false, errors.New("client is nil"))
	}
	if ctx == nil {
		return Document{}, newError("GET", ErrorKindConfiguration, 0, false, errors.New("context is nil"))
	}
	resolved, err := c.Resolve(baseURI, link)
	if err != nil {
		return Document{}, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return Document{}, newError("GET", ErrorKindClosed, 0, false, errClientClosed)
	}
	response, err := c.adapter.get(ctx, resolved)
	if err != nil {
		return Document{}, normalizeError("GET", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return Document{}, normalizeError("read response", err)
	}
	if int64(len(body)) > c.responseLimit {
		return Document{}, newError("read response", ErrorKindResponseLimit, 0, false, errResponseTooLarge)
	}
	if !json.Valid(body) {
		return Document{}, newError("decode response", ErrorKindInvalidResponse, 0, false, errInvalidJSON)
	}
	documentURI := resolved
	if response.Request != nil && response.Request.URL != nil {
		documentURI = response.Request.URL.RequestURI()
	}
	return newDocument(documentURI, body), nil
}

// Close logs out an active Redfish session and releases idle connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	closeContext, cancel := context.WithTimeout(context.Background(), c.closeTimeout)
	c.closeErr = normalizeError("logout", c.adapter.close(closeContext))
	cancel()
	c.transport.CloseIdleConnections()
	return c.closeErr
}

// String formats only the non-secret Redfish origin.
func (c *Client) String() string {
	if c == nil || c.origin == nil {
		return "Redfish client (nil)"
	}
	return fmt.Sprintf("Redfish client (%s://%s)", c.origin.Scheme, c.origin.Host)
}

// GoString formats only the non-secret Redfish origin.
func (c *Client) GoString() string {
	return c.String()
}

type limitedTransport struct {
	next     http.RoundTripper
	maxBytes int64
}

func (t *limitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > t.maxBytes {
		response.Body.Close()
		return nil, errResponseTooLarge
	}
	response.Body = &limitedReadCloser{
		body:      response.Body,
		remaining: t.maxBytes,
	}
	return response, nil
}

type limitedReadCloser struct {
	body      io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(buffer []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:r.remaining]
		}
		read, err := r.body.Read(buffer)
		r.remaining -= int64(read)
		return read, err
	}
	var probe [1]byte
	read, err := r.body.Read(probe[:])
	if read != 0 {
		return 0, errResponseTooLarge
	}
	return 0, err
}

func (r *limitedReadCloser) Close() error {
	return r.body.Close()
}
