package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sourceClient interface {
	ServiceRoot() redfish.Document
	Resolve(baseURI string, link string) (string, error)
	GetFrom(context.Context, string, string) (redfish.Document, error)
	Close() error
}

type resourceWalker struct {
	client        sourceClient
	table         catalog.Table
	directURI     *string
	maxPages      int
	maxRows       int
	initialized   bool
	done          bool
	page          *pageCursor
	pageDocument  redfish.Document
	nextPageBase  string
	nextPageLink  string
	pageCount     int
	rowCount      int
	seenPages     map[string]struct{}
	seenResources map[string]struct{}
	graph         *graphWalker
}

func newResourceWalker(
	client sourceClient,
	table catalog.Table,
	directURI *string,
	maxPages int,
	maxRows int,
) *resourceWalker {
	walker := &resourceWalker{
		client:        client,
		table:         table,
		directURI:     directURI,
		maxPages:      maxPages,
		maxRows:       maxRows,
		seenPages:     make(map[string]struct{}),
		seenResources: make(map[string]struct{}),
	}
	if table.Discovery.Kind == catalog.DiscoveryGraph {
		walker.graph = newGraphWalker(client, table, directURI, maxPages, maxRows)
	}
	return walker
}

func (w *resourceWalker) next(ctx context.Context) (resourceRow, error) {
	if err := contextStatus(ctx); err != nil {
		return resourceRow{}, err
	}
	if w.graph != nil {
		return w.graph.next(ctx)
	}
	if w.done {
		return resourceRow{}, io.EOF
	}
	if !w.initialized {
		w.initialized = true
		switch w.table.Discovery.Kind {
		case catalog.DiscoverySingleton:
			return w.singleton(ctx)
		case catalog.DiscoveryCollection:
			if w.directURI != nil {
				return w.direct(ctx)
			}
			if err := w.discoverCollection(); err != nil {
				return resourceRow{}, err
			}
		default:
			return resourceRow{}, status.Error(codes.Internal, "unsupported generated Redfish discovery kind")
		}
	}
	if w.table.Discovery.Kind == catalog.DiscoverySingleton || w.directURI != nil {
		w.done = true
		return resourceRow{}, io.EOF
	}
	return w.nextCollectionResource(ctx)
}

func (w *resourceWalker) singleton(ctx context.Context) (resourceRow, error) {
	w.done = true
	document := w.client.ServiceRoot()
	if w.table.Discovery.URI != redfish.ServiceRootURI {
		var err error
		document, err = w.client.GetFrom(ctx, redfish.ServiceRootURI, w.table.Discovery.URI)
		if err != nil {
			return resourceRow{}, sourceError("read Redfish singleton", err)
		}
	}
	row, err := w.rowFromDocument(document, w.table.Discovery.URI)
	if err != nil {
		return resourceRow{}, err
	}
	if w.directURI != nil {
		requested, err := w.canonicalResourceURI(redfish.ServiceRootURI, *w.directURI)
		if err != nil {
			return resourceRow{}, status.Error(codes.InvalidArgument, "resource_uri is not a canonical Redfish resource link")
		}
		if row.uri != requested {
			return resourceRow{}, io.EOF
		}
	}
	return w.accept(row)
}

func (w *resourceWalker) direct(ctx context.Context) (resourceRow, error) {
	w.done = true
	requested, err := w.canonicalResourceURI(redfish.ServiceRootURI, *w.directURI)
	if err != nil {
		return resourceRow{}, status.Error(codes.InvalidArgument, "resource_uri is not a canonical Redfish resource link")
	}
	document, err := w.client.GetFrom(ctx, redfish.ServiceRootURI, requested)
	if err != nil {
		return resourceRow{}, sourceError("read Redfish resource", err)
	}
	row, err := w.rowFromDocument(document, requested)
	if err != nil {
		return resourceRow{}, err
	}
	if row.uri != requested {
		return resourceRow{}, io.EOF
	}
	return w.accept(row)
}

func (w *resourceWalker) discoverCollection() error {
	root := w.client.ServiceRoot()
	payload, err := decodeJSONObject(root.Raw())
	if err != nil {
		return invalidResource("Redfish ServiceRoot is not a JSON object")
	}
	raw, exists, err := lookupJSONPointer(payload, w.table.Discovery.JSONPointer)
	if err != nil {
		return invalidResource("Redfish ServiceRoot collection link is malformed")
	}
	if !exists {
		w.done = true
		return nil
	}
	link, ok := raw.(string)
	if !ok || link == "" {
		return invalidResource("Redfish ServiceRoot collection link is not a string")
	}
	w.nextPageBase = root.URI()
	w.nextPageLink = link
	return nil
}

func (w *resourceWalker) nextCollectionResource(ctx context.Context) (resourceRow, error) {
	for {
		if w.done {
			return resourceRow{}, io.EOF
		}
		if w.page == nil {
			if w.nextPageLink == "" {
				w.done = true
				return resourceRow{}, io.EOF
			}
			if err := w.loadPage(ctx); err != nil {
				return resourceRow{}, err
			}
		}
		member, exists, err := w.page.nextMember()
		if err != nil {
			return resourceRow{}, invalidResource("Redfish collection page is malformed")
		}
		if exists {
			row, err := w.rowFromMember(ctx, member)
			if err != nil {
				return resourceRow{}, err
			}
			row, err = w.accept(row)
			if errors.Is(err, errDuplicateResource) {
				continue
			}
			return row, err
		}
		continuation, err := w.page.continuation()
		if err != nil {
			return resourceRow{}, invalidResource("Redfish collection continuation is malformed")
		}
		w.nextPageBase = w.pageDocument.URI()
		w.nextPageLink = continuation
		w.page = nil
		if continuation == "" {
			w.done = true
			return resourceRow{}, io.EOF
		}
	}
}

func (w *resourceWalker) loadPage(ctx context.Context) error {
	if w.pageCount >= w.maxPages {
		return resultLimit("Redfish collection page limit exceeded")
	}
	resolved, err := w.client.Resolve(w.nextPageBase, w.nextPageLink)
	if err != nil {
		return sourceError("resolve Redfish collection page", err)
	}
	if _, exists := w.seenPages[resolved]; exists {
		return paginationLoop()
	}
	document, err := w.client.GetFrom(ctx, w.nextPageBase, w.nextPageLink)
	if err != nil {
		return sourceError("read Redfish collection page", err)
	}
	if _, exists := w.seenPages[document.URI()]; exists && document.URI() != resolved {
		return paginationLoop()
	}
	w.seenPages[resolved] = struct{}{}
	w.seenPages[document.URI()] = struct{}{}
	w.pageCount++
	cursor, err := newPageCursor(document.Raw())
	if err != nil {
		return invalidResource("Redfish collection page is malformed")
	}
	w.page = cursor
	w.pageDocument = document
	return nil
}

func (w *resourceWalker) rowFromMember(ctx context.Context, raw json.RawMessage) (resourceRow, error) {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish collection member is not a JSON object")
	}
	link, ok := payload["@odata.id"].(string)
	if !ok || link == "" {
		return resourceRow{}, invalidResource("Redfish collection member has no @odata.id")
	}
	inline := false
	for property := range payload {
		if !strings.HasPrefix(property, "@odata.") {
			inline = true
			break
		}
	}
	if inline {
		uri, err := w.canonicalResourceURI(w.pageDocument.URI(), link)
		if err != nil {
			return resourceRow{}, invalidResource("Redfish collection member has an unsafe @odata.id")
		}
		return resourceRow{uri: uri, payload: payload, observedAt: w.pageDocument.ObservedAt()}, nil
	}
	document, err := w.client.GetFrom(ctx, w.pageDocument.URI(), link)
	if err != nil {
		return resourceRow{}, sourceError("read Redfish collection member", err)
	}
	return w.rowFromDocument(document, link)
}

func (w *resourceWalker) rowFromDocument(document redfish.Document, fallback string) (resourceRow, error) {
	payload, err := decodeJSONObject(document.Raw())
	if err != nil {
		return resourceRow{}, invalidResource("Redfish resource is not a JSON object")
	}
	link := document.URI()
	if link == "" {
		link = fallback
	}
	if raw, exists := payload["@odata.id"]; exists {
		var ok bool
		link, ok = raw.(string)
		if !ok || link == "" {
			return resourceRow{}, invalidResource("Redfish resource @odata.id is not a string")
		}
	}
	uri, err := w.canonicalResourceURI(document.URI(), link)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish resource has an unsafe @odata.id")
	}
	return resourceRow{uri: uri, payload: payload, observedAt: document.ObservedAt()}, nil
}

var errDuplicateResource = errors.New("duplicate Redfish resource")

func (w *resourceWalker) accept(row resourceRow) (resourceRow, error) {
	if _, exists := w.seenResources[row.uri]; exists {
		return resourceRow{}, errDuplicateResource
	}
	if w.rowCount >= w.maxRows {
		return resourceRow{}, resultLimit("Redfish query row limit exceeded")
	}
	w.seenResources[row.uri] = struct{}{}
	w.rowCount++
	return row, nil
}

func invalidResource(message string) error {
	return diagnosticStatus(
		codes.DataLoss,
		"REDFISH_RESOURCE_INVALID_RESPONSE",
		message,
		false,
	)
}

func paginationLoop() error {
	return diagnosticStatus(
		codes.DataLoss,
		"REDFISH_PAGINATION_LOOP",
		"Redfish collection pagination loop detected",
		false,
	)
}

func resultLimit(message string) error {
	return diagnosticStatus(
		codes.ResourceExhausted,
		"REDFISH_RESULT_LIMIT_EXCEEDED",
		message,
		false,
	)
}

func (w *resourceWalker) canonicalResourceURI(baseURI string, link string) (string, error) {
	return canonicalResourceURI(w.client, baseURI, link)
}

func canonicalResourceURI(client sourceClient, baseURI string, link string) (string, error) {
	resolved, err := client.Resolve(baseURI, link)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(resolved)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path == "" {
		return "", errors.New("resource URI must be a path without query or fragment")
	}
	return parsed.EscapedPath(), nil
}

func canonicalEmbeddedURI(client sourceClient, baseURI string, link string) (string, error) {
	reference, err := url.Parse(link)
	if err != nil || reference.User != nil || reference.Fragment == "" ||
		!strings.HasPrefix(reference.Fragment, "/") {
		if err == nil && reference.Fragment == "" {
			return canonicalResourceURI(client, baseURI, link)
		}
		return "", errors.New("embedded resource URI has an invalid fragment")
	}
	fragment := reference.EscapedFragment()
	reference.Fragment = ""
	reference.RawFragment = ""
	resolved, err := client.Resolve(baseURI, reference.String())
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(resolved)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path == "" {
		return "", errors.New("embedded resource URI must be a path without query")
	}
	return parsed.EscapedPath() + "#" + fragment, nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("expected JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected trailing JSON value")
	}
	return result, nil
}
