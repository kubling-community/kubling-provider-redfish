package redfish

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
)

func buildTLSConfig(settings device.TLSConfig) (*tls.Config, error) {
	configuration := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         settings.ServerName(),
		InsecureSkipVerify: settings.InsecureSkipVerify(),
	}
	if settings.CAFile() == "" {
		return configuration, nil
	}
	contents, err := os.ReadFile(settings.CAFile())
	if err != nil {
		return nil, fmt.Errorf("read Redfish CA file %q: %w", settings.CAFile(), err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(contents) {
		return nil, errors.New("Redfish CA file does not contain a valid PEM certificate")
	}
	configuration.RootCAs = pool
	return configuration, nil
}

func buildHTTPClient(
	originURL *url.URL,
	tlsConfig *tls.Config,
	options Options,
	dependencies clientDependencies,
) (*http.Client, *http.Transport, *requestTracker) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig.Clone()
	transport.TLSHandshakeTimeout = options.RequestTimeout
	transport.ForceAttemptHTTP2 = true

	sleep := dependencies.sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	now := dependencies.now
	if now == nil {
		now = timeNow
	}
	retrier := &retryTransport{
		next:   transport,
		policy: options.Retry,
		sleep:  sleep,
		now:    now,
	}
	tracker := &requestTracker{next: retrier}
	limited := &limitedTransport{next: tracker, maxBytes: options.MaxResponseBytes}
	client := &http.Client{
		Transport:     limited,
		Timeout:       options.RequestTimeout,
		CheckRedirect: checkRedirect(originURL),
	}
	return client, transport, tracker
}
