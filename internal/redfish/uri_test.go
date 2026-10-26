package redfish

import (
	"strings"
	"testing"
)

func TestResolveURIHandlesRelativeAndSameOriginAbsoluteLinks(t *testing.T) {
	origin, err := parseEndpoint("https://bmc.example.invalid")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	tests := map[string]struct {
		base string
		link string
		want string
	}{
		"service-root relative": {
			base: ServiceRootURI,
			link: "Systems/1",
			want: "/redfish/v1/Systems/1",
		},
		"resource relative": {
			base: "/redfish/v1/Systems/1/",
			link: "Memory/1?opaque=token",
			want: "/redfish/v1/Systems/1/Memory/1?opaque=token",
		},
		"absolute path": {
			base: ServiceRootURI,
			link: "/redfish/v1/Chassis/1",
			want: "/redfish/v1/Chassis/1",
		},
		"absolute URL": {
			base: ServiceRootURI,
			link: "https://bmc.example.invalid:443/redfish/v1/Managers/1?opaque=a%2Fb",
			want: "/redfish/v1/Managers/1?opaque=a%2Fb",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolveURI(origin, test.base, test.link)
			if err != nil || got != test.want {
				t.Fatalf("resolveURI() = (%q, %v), want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveURIRejectsUnsafeLinksWithoutEchoingThem(t *testing.T) {
	origin, err := parseEndpoint("https://bmc.example.invalid")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	const canary = "URI-SECRET-CANARY"
	links := []string{
		"https://other.example.invalid/redfish/v1/Systems?token=" + canary,
		"http://bmc.example.invalid/redfish/v1/Systems?token=" + canary,
		"//other.example.invalid/redfish/v1/Systems?token=" + canary,
		"https://user:" + canary + "@bmc.example.invalid/redfish/v1/Systems",
		"/redfish/v1/Systems#" + canary,
		"https://bmc.example.invalid/%zz" + canary,
	}
	for _, link := range links {
		_, err := resolveURI(origin, ServiceRootURI, link)
		if err == nil {
			t.Fatalf("resolveURI(%q) error = nil", link)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("resolveURI() error exposes canary: %v", err)
		}
	}
}

func TestDocumentFormattingRedactsOpaqueQuery(t *testing.T) {
	const canary = "QUERY-CANARY"
	document := newDocument("/redfish/v1/Systems?$skiptoken="+canary, []byte(`{"Id":"1"}`))
	formatted := document.String()
	if strings.Contains(formatted, canary) || !strings.Contains(formatted, "redacted") {
		t.Fatalf("Document.String() = %q", formatted)
	}
}
