package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
)

type resourceIterator interface {
	next(context.Context) (resourceRow, error)
}

type graphWalker struct {
	table         catalog.Table
	directURI     *string
	root          resourceRow
	initErr       error
	client        sourceClient
	maxPages      int
	maxRows       int
	routeIndex    int
	route         resourceIterator
	rowCount      int
	seenResources map[string]struct{}
}

func newGraphWalker(
	client sourceClient,
	table catalog.Table,
	directURI *string,
	maxPages int,
	maxRows int,
) *graphWalker {
	document := client.ServiceRoot()
	payload, err := decodeJSONObject(document.Raw())
	return &graphWalker{
		table:         table,
		directURI:     directURI,
		root:          resourceRow{uri: document.URI(), payload: payload, observedAt: document.ObservedAt()},
		initErr:       err,
		client:        client,
		maxPages:      maxPages,
		maxRows:       maxRows,
		seenResources: make(map[string]struct{}),
	}
}

func (w *graphWalker) next(ctx context.Context) (resourceRow, error) {
	if w.initErr != nil {
		w.initErr = nil
		return resourceRow{}, invalidResource("Redfish ServiceRoot is not a JSON object")
	}
	for {
		if w.route == nil {
			if w.routeIndex >= len(w.table.Discovery.Routes) {
				return resourceRow{}, io.EOF
			}
			w.route = newRouteIterator(
				w.client,
				w.root,
				w.table.Discovery.Routes[w.routeIndex],
				w.maxPages,
			)
			w.routeIndex++
		}
		row, err := w.route.next(ctx)
		if errors.Is(err, io.EOF) {
			w.route = nil
			continue
		}
		if err != nil {
			return resourceRow{}, err
		}
		if w.directURI != nil && row.uri != *w.directURI {
			continue
		}
		identity, err := graphRowIdentity(w.table, row)
		if err != nil {
			return resourceRow{}, invalidResource("generated Redfish row identity is incomplete")
		}
		if _, exists := w.seenResources[identity]; exists {
			continue
		}
		if w.rowCount >= w.maxRows {
			return resourceRow{}, resultLimit("Redfish query row limit exceeded")
		}
		w.seenResources[identity] = struct{}{}
		w.rowCount++
		return row, nil
	}
}

type routeIterator struct {
	client      sourceClient
	route       catalog.TraversalRoute
	maxPages    int
	anchors     resourceIterator
	anchor      resourceRow
	branch      resourceIterator
	branchIndex int
	branchRows  bool
}

func newRouteIterator(
	client sourceClient,
	root resourceRow,
	route catalog.TraversalRoute,
	maxPages int,
) *routeIterator {
	anchors := resourceIterator(&singleIterator{row: root})
	anchors = chainTraversalSteps(client, anchors, route.Prefix, maxPages)
	return &routeIterator{
		client:      client,
		route:       route,
		maxPages:    maxPages,
		anchors:     anchors,
		branchIndex: len(route.Branches),
	}
}

func (i *routeIterator) next(ctx context.Context) (resourceRow, error) {
	for {
		if i.branch != nil {
			row, err := i.branch.next(ctx)
			if err == nil {
				i.branchRows = true
				return row, nil
			}
			if !errors.Is(err, io.EOF) {
				return resourceRow{}, err
			}
			i.branch = nil
			if i.route.Mode == catalog.TraversalFallback && i.branchRows {
				i.branchIndex = len(i.route.Branches)
			}
		}
		if i.branchIndex < len(i.route.Branches) {
			branch := i.route.Branches[i.branchIndex]
			i.branchIndex++
			i.branchRows = false
			iterator := resourceIterator(&singleIterator{row: i.anchor})
			iterator = chainTraversalSteps(i.client, iterator, branch.Steps, i.maxPages)
			i.branch = &branchIterator{upstream: iterator, id: branch.ID, variant: branch.Variant}
			continue
		}
		anchor, err := i.anchors.next(ctx)
		if err != nil {
			return resourceRow{}, err
		}
		i.anchor = anchor
		i.branchIndex = 0
		i.branchRows = false
	}
}

type branchIterator struct {
	upstream resourceIterator
	id       string
	variant  string
}

func (i *branchIterator) next(ctx context.Context) (resourceRow, error) {
	row, err := i.upstream.next(ctx)
	if err != nil {
		return resourceRow{}, err
	}
	row.branchID = i.id
	row.variant = i.variant
	return row, nil
}

func chainTraversalSteps(
	client sourceClient,
	upstream resourceIterator,
	steps []catalog.TraversalStep,
	maxPages int,
) resourceIterator {
	result := upstream
	for _, step := range steps {
		result = &stepIterator{client: client, upstream: result, step: step, maxPages: maxPages}
	}
	return result
}

type stepIterator struct {
	client   sourceClient
	upstream resourceIterator
	step     catalog.TraversalStep
	maxPages int
	children resourceIterator
}

func (i *stepIterator) next(ctx context.Context) (resourceRow, error) {
	for {
		if i.children != nil {
			child, err := i.children.next(ctx)
			if err == nil {
				matches, predicateErr := matchesPredicate(child.payload, i.step.Predicate)
				if predicateErr != nil {
					return resourceRow{}, invalidResource("Redfish traversal predicate has an invalid value")
				}
				if !matches {
					continue
				}
				if i.step.Capture != "" {
					child.context = cloneContext(child.context)
					child.context[i.step.Capture] = child.uri
				}
				return child, nil
			}
			if !errors.Is(err, io.EOF) {
				return resourceRow{}, err
			}
			i.children = nil
		}
		parent, err := i.upstream.next(ctx)
		if err != nil {
			return resourceRow{}, err
		}
		children, err := traversalChildren(i.client, parent, i.step, i.maxPages)
		if err != nil {
			return resourceRow{}, err
		}
		i.children = children
	}
}

func traversalChildren(
	client sourceClient,
	parent resourceRow,
	step catalog.TraversalStep,
	maxPages int,
) (resourceIterator, error) {
	raw, exists, err := lookupJSONPointer(parent.payload, step.JSONPointer)
	if err != nil {
		return nil, invalidResource("Redfish traversal link is malformed")
	}
	if !exists || raw == nil {
		return emptyIterator{}, nil
	}
	switch step.Kind {
	case catalog.TraversalSingleton:
		link, ok := raw.(string)
		if !ok || link == "" {
			return nil, invalidResource("Redfish singleton link is not a string")
		}
		return &singletonLinkIterator{client: client, parent: parent, link: link}, nil
	case catalog.TraversalCollection, catalog.TraversalCollectionLinks:
		link, ok := raw.(string)
		if !ok || link == "" {
			return nil, invalidResource("Redfish collection link is not a string")
		}
		return &graphCollectionIterator{
			client:    client,
			parent:    parent,
			base:      parent.uri,
			link:      link,
			maxPages:  maxPages,
			linksOnly: step.Kind == catalog.TraversalCollectionLinks,
			seenPages: make(map[string]struct{}),
		}, nil
	case catalog.TraversalLinked, catalog.TraversalLinks, catalog.TraversalEmbedded:
		items, ok := raw.([]any)
		if !ok {
			return nil, invalidResource("Redfish traversal array is malformed")
		}
		return &arrayIterator{
			client: client,
			parent: parent,
			items:  items,
			kind:   step.Kind,
		}, nil
	default:
		return nil, invalidResource("unsupported generated Redfish traversal step")
	}
}

type singleIterator struct {
	row  resourceRow
	done bool
}

func (i *singleIterator) next(context.Context) (resourceRow, error) {
	if i.done {
		return resourceRow{}, io.EOF
	}
	i.done = true
	return i.row, nil
}

type emptyIterator struct{}

func (emptyIterator) next(context.Context) (resourceRow, error) {
	return resourceRow{}, io.EOF
}

type singletonLinkIterator struct {
	client sourceClient
	parent resourceRow
	link   string
	done   bool
}

func (i *singletonLinkIterator) next(ctx context.Context) (resourceRow, error) {
	if i.done {
		return resourceRow{}, io.EOF
	}
	i.done = true
	document, err := i.client.GetFrom(ctx, i.parent.uri, i.link)
	if err != nil {
		return resourceRow{}, sourceError("read Redfish traversal resource", err)
	}
	return graphRowFromDocument(i.client, document, i.link, i.parent.context)
}

type arrayIterator struct {
	client sourceClient
	parent resourceRow
	items  []any
	kind   catalog.TraversalKind
	index  int
}

func (i *arrayIterator) next(ctx context.Context) (resourceRow, error) {
	if i.index >= len(i.items) {
		return resourceRow{}, io.EOF
	}
	item := i.items[i.index]
	i.index++
	payload, ok := item.(map[string]any)
	if !ok || payload == nil {
		return resourceRow{}, invalidResource("Redfish traversal array member is not an object")
	}
	link, ok := payload["@odata.id"].(string)
	if !ok || link == "" {
		return resourceRow{}, invalidResource("Redfish traversal array member has no @odata.id")
	}
	if i.kind == catalog.TraversalLinked && !hasInlineProperties(payload) {
		document, err := i.client.GetFrom(ctx, i.parent.uri, link)
		if err != nil {
			return resourceRow{}, sourceError("read linked Redfish resource", err)
		}
		return graphRowFromDocument(i.client, document, link, i.parent.context)
	}
	canonicalize := canonicalEmbeddedURI
	if i.kind == catalog.TraversalLinked {
		canonicalize = canonicalResourceURI
	}
	uri, err := canonicalize(i.client, i.parent.uri, link)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish traversal array member has an unsafe @odata.id")
	}
	return resourceRow{
		uri:        uri,
		payload:    payload,
		observedAt: i.parent.observedAt,
		context:    i.parent.context,
	}, nil
}

type graphCollectionIterator struct {
	client       sourceClient
	parent       resourceRow
	base         string
	link         string
	maxPages     int
	linksOnly    bool
	page         *pageCursor
	pageDocument redfish.Document
	pageCount    int
	seenPages    map[string]struct{}
}

func (i *graphCollectionIterator) next(ctx context.Context) (resourceRow, error) {
	for {
		if i.page == nil {
			if i.link == "" {
				return resourceRow{}, io.EOF
			}
			if err := i.loadPage(ctx); err != nil {
				return resourceRow{}, err
			}
		}
		member, exists, err := i.page.nextMember()
		if err != nil {
			return resourceRow{}, invalidResource("Redfish collection page is malformed")
		}
		if exists {
			return graphRowFromMember(ctx, i.client, i.pageDocument, member, i.parent.context, i.linksOnly)
		}
		continuation, err := i.page.continuation()
		if err != nil {
			return resourceRow{}, invalidResource("Redfish collection continuation is malformed")
		}
		i.base = i.pageDocument.URI()
		i.link = continuation
		i.page = nil
		if continuation == "" {
			return resourceRow{}, io.EOF
		}
	}
}

func (i *graphCollectionIterator) loadPage(ctx context.Context) error {
	if i.pageCount >= i.maxPages {
		return resultLimit("Redfish collection page limit exceeded")
	}
	resolved, err := i.client.Resolve(i.base, i.link)
	if err != nil {
		return sourceError("resolve Redfish collection page", err)
	}
	if _, exists := i.seenPages[resolved]; exists {
		return paginationLoop()
	}
	document, err := i.client.GetFrom(ctx, i.base, i.link)
	if err != nil {
		return sourceError("read Redfish collection page", err)
	}
	if _, exists := i.seenPages[document.URI()]; exists && document.URI() != resolved {
		return paginationLoop()
	}
	i.seenPages[resolved] = struct{}{}
	i.seenPages[document.URI()] = struct{}{}
	i.pageCount++
	cursor, err := newPageCursor(document.Raw())
	if err != nil {
		return invalidResource("Redfish collection page is malformed")
	}
	i.page = cursor
	i.pageDocument = document
	return nil
}

func graphRowFromMember(
	ctx context.Context,
	client sourceClient,
	document redfish.Document,
	raw json.RawMessage,
	rowContext map[string]string,
	linksOnly bool,
) (resourceRow, error) {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish collection member is not a JSON object")
	}
	link, ok := payload["@odata.id"].(string)
	if !ok || link == "" {
		return resourceRow{}, invalidResource("Redfish collection member has no @odata.id")
	}
	if !linksOnly && !hasInlineProperties(payload) {
		member, err := client.GetFrom(ctx, document.URI(), link)
		if err != nil {
			return resourceRow{}, sourceError("read Redfish collection member", err)
		}
		return graphRowFromDocument(client, member, link, rowContext)
	}
	uri, err := canonicalResourceURI(client, document.URI(), link)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish collection member has an unsafe @odata.id")
	}
	return resourceRow{uri: uri, payload: payload, observedAt: document.ObservedAt(), context: rowContext}, nil
}

func graphRowFromDocument(
	client sourceClient,
	document redfish.Document,
	fallback string,
	rowContext map[string]string,
) (resourceRow, error) {
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
	uri, err := canonicalResourceURI(client, document.URI(), link)
	if err != nil {
		return resourceRow{}, invalidResource("Redfish resource has an unsafe @odata.id")
	}
	return resourceRow{uri: uri, payload: payload, observedAt: document.ObservedAt(), context: rowContext}, nil
}

func hasInlineProperties(payload map[string]any) bool {
	for property := range payload {
		if !strings.HasPrefix(property, "@odata.") {
			return true
		}
	}
	return false
}

func cloneContext(input map[string]string) map[string]string {
	result := make(map[string]string, len(input)+1)
	for key, value := range input {
		result[key] = value
	}
	return result
}

func graphRowIdentity(table catalog.Table, row resourceRow) (string, error) {
	var result strings.Builder
	for _, name := range table.StableKeyColumns {
		if name == "namespace" || name == "device_id" {
			continue
		}
		column, exists := graphIdentityColumn(table, name)
		if !exists {
			return "", errors.New("stable-key column is missing")
		}
		value, err := graphIdentityValue(column, row)
		if err != nil || value == "" {
			return "", errors.New("stable-key value is missing")
		}
		fmt.Fprintf(&result, "%d:%s|", len(value), value)
	}
	if result.Len() == 0 {
		return "", errors.New("stable key has no row-local component")
	}
	return result.String(), nil
}

func graphIdentityColumn(table catalog.Table, name string) (catalog.Column, bool) {
	for _, column := range table.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return catalog.Column{}, false
}

func graphIdentityValue(column catalog.Column, row resourceRow) (string, error) {
	switch column.Source {
	case catalog.ColumnSourceResourceURI:
		return row.uri, nil
	case catalog.ColumnSourceContext:
		return row.context[column.ContextKey], nil
	case catalog.ColumnSourceVariant:
		return row.variant, nil
	case catalog.ColumnSourcePayload:
		pointer := column.JSONPointer
		if mapping, ok := columnMapping(column, row.branchID); ok {
			if mapping.Null || mapping.Predicate != nil {
				return "", errors.New("conditional payload cannot form a stable key")
			}
			pointer = mapping.JSONPointer
		}
		raw, exists, err := lookupJSONPointer(row.payload, pointer)
		if err != nil || !exists {
			return "", errors.New("payload stable-key value is missing")
		}
		value, ok := raw.(string)
		if !ok {
			return "", errors.New("payload stable-key value is not a string")
		}
		return value, nil
	default:
		return "", errors.New("unsupported stable-key column source")
	}
}
