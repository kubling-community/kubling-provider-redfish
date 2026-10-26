package redfish

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func parseEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errUnsafeURI
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return nil, errUnsafeURI
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errUnsafeURI
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errUnsafeURI
	}
	parsed.Path = ""
	return parsed, nil
}

func resolveURI(origin *url.URL, baseURI string, link string) (string, error) {
	if origin == nil {
		return "", errUnsafeURI
	}
	baseReference, err := url.Parse(baseURI)
	if err != nil || baseReference.IsAbs() || baseReference.Host != "" || baseReference.User != nil || baseReference.Fragment != "" {
		return "", errUnsafeURI
	}
	if baseReference.Path == "" {
		baseReference.Path = ServiceRootURI
	}
	base := origin.ResolveReference(baseReference)
	reference, err := url.Parse(link)
	if err != nil || reference.User != nil || reference.Fragment != "" {
		return "", errUnsafeURI
	}
	target := base.ResolveReference(reference)
	if target.Scheme != "https" || !sameOrigin(origin, target) || target.User != nil || target.Fragment != "" {
		return "", errUnsafeURI
	}
	if target.Path == "" {
		target.Path = ServiceRootURI
	}
	return target.RequestURI(), nil
}

func sameOrigin(left *url.URL, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

func checkRedirect(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errTooManyRedirects
		}
		if request.URL.Scheme != "https" || !sameOrigin(origin, request.URL) || request.URL.User != nil {
			return errUnsafeRedirect
		}
		return nil
	}
}

func safeURI(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid URI>"
	}
	parsed.User = nil
	parsed.Fragment = ""
	if parsed.RawQuery != "" || parsed.ForceQuery {
		parsed.RawQuery = "redacted"
		parsed.ForceQuery = false
	}
	if parsed.IsAbs() {
		return parsed.Scheme + "://" + parsed.Host + parsed.RequestURI()
	}
	return parsed.RequestURI()
}

var (
	errUnsafeURI        = errors.New("unsafe Redfish URI")
	errUnsafeRedirect   = errors.New("unsafe Redfish redirect")
	errTooManyRedirects = errors.New("too many Redfish redirects")
)
