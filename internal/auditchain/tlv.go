// Package auditchain implements the byte-level primitives used by the audit
// hash chain. It contains no persistence or chain-state behavior.
package auditchain

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// Tag identifies the payload type of a TLV item.
type Tag byte

const (
	TagNil   Tag = 0x00
	TagText  Tag = 0x01
	TagInt   Tag = 0x02
	TagBytes Tag = 0x03
)

const tlvHeaderLen = 5

// Item is one decoded TLV item. Payload is always a copy of the encoded input.
type Item struct {
	Tag     Tag
	Payload []byte
}

// EncodeTLV encodes items as TAG || uint32-big-endian-LEN || PAYLOAD.
// It rejects values that do not have the canonical representation for their
// tag, including invalid UTF-8 text and non-canonical integers.
func EncodeTLV(items ...Item) ([]byte, error) {
	var encoded []byte
	for i, item := range items {
		if err := validateItem(item.Tag, item.Payload); err != nil {
			return nil, fmt.Errorf("TLV item %d: %w", i, err)
		}
		if uint64(len(item.Payload)) > uint64(^uint32(0)) {
			return nil, fmt.Errorf("TLV item %d: payload length exceeds uint32", i)
		}

		start := len(encoded)
		encoded = append(encoded, make([]byte, tlvHeaderLen)...)
		encoded[start] = byte(item.Tag)
		binary.BigEndian.PutUint32(encoded[start+1:start+tlvHeaderLen], uint32(len(item.Payload)))
		encoded = append(encoded, item.Payload...)
	}
	return encoded, nil
}

// DecodeTLV strictly decodes a complete sequence of TLV items. Partial
// headers, truncated payloads, unknown tags, and non-canonical values fail.
func DecodeTLV(encoded []byte) ([]Item, error) {
	items := make([]Item, 0)
	for offset := 0; offset < len(encoded); {
		remaining := len(encoded) - offset
		if remaining < tlvHeaderLen {
			return nil, fmt.Errorf("trailing %d byte(s) after complete TLV items", remaining)
		}

		tag := Tag(encoded[offset])
		declared := binary.BigEndian.Uint32(encoded[offset+1 : offset+tlvHeaderLen])
		payloadStart := offset + tlvHeaderLen
		available := len(encoded) - payloadStart
		// Compare without converting declared to int. This remains safe on
		// 32-bit platforms and prevents offset arithmetic from overflowing.
		if uint64(declared) > uint64(available) {
			return nil, fmt.Errorf("TLV item at byte %d declares length %d with only %d byte(s) available", offset, declared, available)
		}

		payloadLen := int(declared)
		payloadEnd := payloadStart + payloadLen
		payload := encoded[payloadStart:payloadEnd]
		if err := validateItem(tag, payload); err != nil {
			return nil, fmt.Errorf("TLV item at byte %d: %w", offset, err)
		}

		payloadCopy := append([]byte(nil), payload...)
		items = append(items, Item{Tag: tag, Payload: payloadCopy})
		offset = payloadEnd
	}
	return items, nil
}

func validateItem(tag Tag, payload []byte) error {
	switch tag {
	case TagNil:
		if len(payload) != 0 {
			return fmt.Errorf("NIL has non-zero length %d", len(payload))
		}
	case TagText:
		if !utf8.Valid(payload) {
			return fmt.Errorf("TEXT payload is not valid UTF-8")
		}
	case TagInt:
		if !validCanonicalInt(payload) {
			return fmt.Errorf("invalid canonical INT %q", payload)
		}
	case TagBytes:
		// Every byte sequence is valid.
	default:
		return fmt.Errorf("unknown tag 0x%02x", byte(tag))
	}
	return nil
}

func validCanonicalInt(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	start := 0
	if payload[0] == '-' {
		if len(payload) == 1 || payload[1] == '0' {
			return false
		}
		start = 1
	} else if payload[0] == '0' {
		return len(payload) == 1
	}

	if payload[start] < '1' || payload[start] > '9' {
		return false
	}
	for _, b := range payload[start+1:] {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}
