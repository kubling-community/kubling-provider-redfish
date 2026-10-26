package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type pageCursor struct {
	decoder      *json.Decoder
	membersOpen  bool
	membersDone  bool
	nextLink     string
	nextLinkSeen bool
}

func newPageCursor(raw []byte) (*pageCursor, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("collection page must be a JSON object")
	}
	cursor := &pageCursor{decoder: decoder}
	if err := cursor.scanUntilMembers(); err != nil {
		return nil, err
	}
	return cursor, nil
}

func (c *pageCursor) scanUntilMembers() error {
	for c.decoder.More() {
		key, err := c.objectKey()
		if err != nil {
			return err
		}
		if key == "Members" {
			token, err := c.decoder.Token()
			if err != nil {
				return err
			}
			if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
				return errors.New("collection Members must be a JSON array")
			}
			c.membersOpen = true
			return nil
		}
		if err := c.readProperty(key); err != nil {
			return err
		}
	}
	return errors.New("collection page has no Members array")
}

func (c *pageCursor) nextMember() (json.RawMessage, bool, error) {
	if !c.membersOpen || c.membersDone {
		return nil, false, nil
	}
	if c.decoder.More() {
		var member json.RawMessage
		if err := c.decoder.Decode(&member); err != nil {
			return nil, false, err
		}
		return member, true, nil
	}
	token, err := c.decoder.Token()
	if err != nil {
		return nil, false, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return nil, false, errors.New("collection Members array is not terminated")
	}
	c.membersDone = true
	if err := c.scanRemainder(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func (c *pageCursor) scanRemainder() error {
	for c.decoder.More() {
		key, err := c.objectKey()
		if err != nil {
			return err
		}
		if key == "Members" {
			return errors.New("collection page repeats Members")
		}
		if err := c.readProperty(key); err != nil {
			return err
		}
	}
	token, err := c.decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return errors.New("collection page object is not terminated")
	}
	var trailing any
	if err := c.decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("collection response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func (c *pageCursor) objectKey() (string, error) {
	token, err := c.decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", errors.New("collection property name is not a string")
	}
	return key, nil
}

func (c *pageCursor) readProperty(key string) error {
	if key == "Members@odata.nextLink" {
		if c.nextLinkSeen {
			return errors.New("collection page repeats Members@odata.nextLink")
		}
		c.nextLinkSeen = true
		if err := c.decoder.Decode(&c.nextLink); err != nil {
			return fmt.Errorf("decode Members@odata.nextLink: %w", err)
		}
		if c.nextLink == "" {
			return errors.New("Members@odata.nextLink must not be empty")
		}
		return nil
	}
	var ignored json.RawMessage
	return c.decoder.Decode(&ignored)
}

func (c *pageCursor) continuation() (string, error) {
	if !c.membersDone {
		return "", errors.New("collection Members have not been consumed")
	}
	return c.nextLink, nil
}
