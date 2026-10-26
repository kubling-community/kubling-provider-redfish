package redfish

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/stmcginnis/gofish"
)

type gofishAdapter struct {
	client     *gofish.APIClient
	sessionURI string
}

func connectGofish(
	ctx context.Context,
	configuredDevice device.Device,
	origin *url.URL,
	httpClient *http.Client,
	tracker *requestTracker,
) (*gofishAdapter, error) {
	switch configuredDevice.AuthMode() {
	case device.AuthModeBasic:
		return connectGofishMode(ctx, configuredDevice, origin, httpClient, tracker, true)
	case device.AuthModeSession:
		adapter, err := connectGofishMode(ctx, configuredDevice, origin, httpClient, tracker, false)
		if err != nil && sessionAuthenticationUnsupported(err, tracker.snapshot()) {
			return nil, errSessionUnsupported
		}
		return adapter, err
	case device.AuthModeAuto:
		adapter, err := connectGofishMode(ctx, configuredDevice, origin, httpClient, tracker, false)
		if err == nil {
			return adapter, nil
		}
		if !sessionAuthenticationUnsupported(err, tracker.snapshot()) {
			return nil, err
		}
		return connectGofishMode(ctx, configuredDevice, origin, httpClient, tracker, true)
	default:
		return nil, errInvalidAuthMode
	}
}

func connectGofishMode(
	ctx context.Context,
	configuredDevice device.Device,
	origin *url.URL,
	httpClient *http.Client,
	tracker *requestTracker,
	basic bool,
) (*gofishAdapter, error) {
	credential := configuredDevice.Credential()
	client, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:              origin.String(),
		Username:              credential.Username().Reveal(),
		Password:              credential.Password().Reveal(),
		HTTPClient:            httpClient,
		BasicAuth:             basic,
		MaxConcurrentRequests: 1,
		ReuseConnections:      true,
		NoModifyTransport:     true,
		AutoExpand:            false,
	})
	if err != nil {
		return nil, err
	}
	adapter := &gofishAdapter{client: client}
	if basic {
		return adapter, nil
	}
	snapshot := tracker.snapshot()
	if !snapshot.sessionAttempted || snapshot.sessionLocation == "" {
		client.Logout()
		return nil, errInvalidSessionLocation
	}
	sessionURI, err := resolveURI(origin, ServiceRootURI, snapshot.sessionLocation)
	if err != nil {
		client.Logout()
		return nil, errInvalidSessionLocation
	}
	adapter.sessionURI = sessionURI
	return adapter, nil
}

func (a *gofishAdapter) get(ctx context.Context, uri string) (*http.Response, error) {
	return a.client.WithContext(ctx).Get(uri)
}

func (a *gofishAdapter) close(ctx context.Context) error {
	if a == nil || a.client == nil || a.sessionURI == "" {
		return nil
	}
	_, err := a.client.WithContext(ctx).Delete(a.sessionURI)
	if err != nil {
		return err
	}
	a.sessionURI = ""
	return nil
}

type requestTracker struct {
	mu                   sync.Mutex
	serviceRootSucceeded bool
	sessionAttempted     bool
	sessionLocation      string
	next                 http.RoundTripper
}

type requestSnapshot struct {
	serviceRootSucceeded bool
	sessionAttempted     bool
	sessionLocation      string
}

func (t *requestTracker) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		t.mu.Lock()
		t.sessionAttempted = true
		t.mu.Unlock()
	}
	response, err := t.next.RoundTrip(request)
	if err == nil && request.Method == http.MethodPost && response.StatusCode >= http.StatusOK &&
		response.StatusCode < http.StatusMultipleChoices {
		t.mu.Lock()
		t.sessionLocation = response.Header.Get("Location")
		t.mu.Unlock()
	}
	if err == nil && request.Method == http.MethodGet && request.URL.Path == ServiceRootURI &&
		response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		t.mu.Lock()
		t.serviceRootSucceeded = true
		t.mu.Unlock()
	}
	return response, err
}

func (t *requestTracker) snapshot() requestSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return requestSnapshot{
		serviceRootSucceeded: t.serviceRootSucceeded,
		sessionAttempted:     t.sessionAttempted,
		sessionLocation:      t.sessionLocation,
	}
}

func sessionAuthenticationUnsupported(err error, snapshot requestSnapshot) bool {
	statusCode := errorStatusCode(err)
	if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented {
		return snapshot.sessionAttempted
	}
	return snapshot.serviceRootSucceeded && !snapshot.sessionAttempted
}
