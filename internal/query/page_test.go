package query

import (
	"testing"
)

func TestPageCursorReadsMembersIncrementallyAndPreservesOpaqueContinuation(t *testing.T) {
	cursor, err := newPageCursor([]byte(`{
		"Members@odata.nextLink":"/redfish/v1/Systems?$skiptoken=opaque%2Bcursor%26part",
		"Members":[{"@odata.id":"/redfish/v1/Systems/1"},{"@odata.id":"/redfish/v1/Systems/2"}],
		"Members@odata.count":2
	}`))
	if err != nil {
		t.Fatalf("newPageCursor() error = %v", err)
	}
	for index := 0; index < 2; index++ {
		member, exists, err := cursor.nextMember()
		if err != nil || !exists || len(member) == 0 {
			t.Fatalf("nextMember(%d) = (%s, %v, %v)", index, member, exists, err)
		}
	}
	if member, exists, err := cursor.nextMember(); err != nil || exists || member != nil {
		t.Fatalf("nextMember(end) = (%s, %v, %v)", member, exists, err)
	}
	continuation, err := cursor.continuation()
	if err != nil || continuation != "/redfish/v1/Systems?$skiptoken=opaque%2Bcursor%26part" {
		t.Fatalf("continuation() = (%q, %v)", continuation, err)
	}
}

func TestPageCursorRejectsMalformedCollections(t *testing.T) {
	for name, raw := range map[string]string{
		"not object":        `[]`,
		"missing members":   `{"Name":"Systems"}`,
		"members not array": `{"Members":{}}`,
		"invalid next link": `{"Members@odata.nextLink":42,"Members":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newPageCursor([]byte(raw)); err == nil {
				t.Fatal("newPageCursor() error = nil")
			}
		})
	}
}
