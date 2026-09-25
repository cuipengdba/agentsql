package b5wal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

var (
	recordMagic     = [8]byte{'A', 'S', 'Q', 'L', 'B', '5', 'W', '4'}
	accountingMagic = [8]byte{'B', '5', 'A', 'C', 'C', 'T', '0', '1'}
	commitMarker    = [8]byte{0xa9, 0x31, 0x57, 0xcd, 0x04, 0xb5, 0x7e, 0x11}
	crc32cTable     = crc32.MakeTable(crc32.Castagnoli)
)

const (
	WALFormatVersion  = uint16(1)
	TrailerVersion    = uint16(1)
	AccountingVersion = uint16(1)
)

// Extension is a canonical binary header extension. Tags must be non-zero,
// strictly increasing, and unique. Each entry is encoded as uint16 tag,
// uint16 value length, then value bytes. The aggregate encoding is capped at
// MaxHeaderExtension bytes and unused header bytes are zero.
type Extension struct {
	Tag   uint16
	Value []byte
}

// Record identifies and carries one already-canonical event-v4 payload.
type Record struct {
	SegmentID          [16]byte
	RecordOrdinal      uint64
	ReservationID      [16]byte
	EventUUID          [16]byte
	EventSchemaID      string
	EventSchemaVersion uint16
	KeyID              string
	Nonce              [12]byte
	CanonicalEvent     []byte
	Extensions         []Extension
}

// DecodedRecord is returned only after length, commit marker, CRC32C, AEAD,
// event digest, zero padding, and accounting metadata have all verified.
type DecodedRecord struct {
	Record
	EventPayloadDigest [32]byte
	FrameLength        uint32
	ChargedBytes       uint32
}

var (
	ErrInvalidRecord     = errors.New("b5wal: invalid record")
	ErrAuthentication    = errors.New("b5wal: authentication failed")
	ErrEventDigest       = errors.New("b5wal: event digest mismatch")
	ErrNonCanonicalField = errors.New("b5wal: non-canonical header field")
)

// EncodeRecord encrypts a record with AES-256-GCM. GCM's 16-byte tag is split
// from the ciphertext and placed immediately after it. The complete 512-byte
// header is AAD. The returned slice is one full physical extent.
func EncodeRecord(key []byte, record Record) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: AES-256 key is %d bytes", ErrInvalidRecord, len(key))
	}
	if len(record.CanonicalEvent) > MaxCanonicalEventSize {
		return nil, fmt.Errorf("%w: canonical event is %d bytes", ErrInvalidRecord, len(record.CanonicalEvent))
	}
	if err := validateASCII("event_schema_id", record.EventSchemaID, MaxEventSchemaIDSize); err != nil {
		return nil, err
	}
	if err := validateASCII("key_id", record.KeyID, MaxKeyIDSize); err != nil {
		return nil, err
	}
	extensions, err := encodeExtensions(record.Extensions)
	if err != nil {
		return nil, err
	}

	frameLength := RecordHeaderSize + len(record.CanonicalEvent) + AEADTagSize + RecordTrailerSize
	charge := EncodedRecordCharge()
	if frameLength > charge-AccountingSize {
		return nil, fmt.Errorf("%w: frame overlaps accounting block", ErrInvalidRecord)
	}

	digest := sha256.Sum256(record.CanonicalEvent)
	header := make([]byte, RecordHeaderSize)
	copy(header[0:8], recordMagic[:])
	binary.BigEndian.PutUint16(header[8:10], WALFormatVersion)
	binary.BigEndian.PutUint16(header[10:12], 0) // flags: no bits defined in v1
	binary.BigEndian.PutUint16(header[12:14], RecordHeaderSize)
	binary.BigEndian.PutUint16(header[14:16], uint16(len(extensions)))
	binary.BigEndian.PutUint32(header[16:20], uint32(frameLength))
	binary.BigEndian.PutUint32(header[20:24], uint32(charge))
	binary.BigEndian.PutUint32(header[24:28], uint32(len(record.CanonicalEvent)))
	binary.BigEndian.PutUint16(header[28:30], record.EventSchemaVersion)
	binary.BigEndian.PutUint16(header[30:32], uint16(len(record.EventSchemaID)))
	binary.BigEndian.PutUint16(header[32:34], uint16(len(record.KeyID)))
	binary.BigEndian.PutUint16(header[34:36], AEADTagSize)
	binary.BigEndian.PutUint16(header[36:38], RecordTrailerSize)
	binary.BigEndian.PutUint16(header[38:40], 0)
	copy(header[40:56], record.SegmentID[:])
	binary.BigEndian.PutUint64(header[56:64], record.RecordOrdinal)
	copy(header[64:80], record.ReservationID[:])
	copy(header[80:96], record.EventUUID[:])
	copy(header[96:128], digest[:])
	copy(header[128:140], record.Nonce[:])
	copy(header[140:204], record.EventSchemaID)
	copy(header[204:300], record.KeyID)
	copy(header[300:512], extensions)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, record.Nonce[:], record.CanonicalEvent, header)
	ciphertext := sealed[:len(record.CanonicalEvent)]
	tag := sealed[len(record.CanonicalEvent):]

	extent := make([]byte, charge)
	copy(extent[:RecordHeaderSize], header)
	copy(extent[RecordHeaderSize:], ciphertext)
	tagOffset := RecordHeaderSize + len(ciphertext)
	copy(extent[tagOffset:tagOffset+AEADTagSize], tag)

	trailerOffset := tagOffset + AEADTagSize
	trailer := extent[trailerOffset : trailerOffset+RecordTrailerSize]
	crc := crc32.New(crc32cTable)
	_, _ = crc.Write(header)
	_, _ = crc.Write(ciphertext)
	_, _ = crc.Write(tag)
	binary.BigEndian.PutUint32(trailer[0:4], crc.Sum32())
	binary.BigEndian.PutUint32(trailer[4:8], uint32(frameLength))
	binary.BigEndian.PutUint64(trailer[8:16], record.RecordOrdinal)
	binary.BigEndian.PutUint16(trailer[16:18], TrailerVersion)
	binary.BigEndian.PutUint16(trailer[18:20], 0)
	binary.BigEndian.PutUint32(trailer[20:24], 0)
	copy(trailer[24:32], commitMarker[:])

	accountingOffset := charge - AccountingSize
	accounting := extent[accountingOffset:]
	copy(accounting[0:8], accountingMagic[:])
	binary.BigEndian.PutUint16(accounting[8:10], AccountingVersion)
	binary.BigEndian.PutUint16(accounting[10:12], AccountingSize)
	binary.BigEndian.PutUint32(accounting[12:16], uint32(frameLength))
	binary.BigEndian.PutUint32(accounting[16:20], uint32(charge))
	copy(accounting[24:40], record.SegmentID[:])
	binary.BigEndian.PutUint64(accounting[40:48], record.RecordOrdinal)
	copy(accounting[48:64], record.EventUUID[:])
	copy(accounting[64:96], digest[:])
	recordDigest := sha256.Sum256(extent[:frameLength])
	copy(accounting[96:128], recordDigest[:])
	binary.BigEndian.PutUint32(accounting[20:24], accountingCRC(accounting))
	return extent, nil
}

// DecodeRecord verifies and decrypts one complete physical extent.
func DecodeRecord(key, extent []byte) (DecodedRecord, error) {
	var out DecodedRecord
	if len(key) != 32 || len(extent) != EncodedRecordCharge() {
		return out, fmt.Errorf("%w: key=%d extent=%d", ErrInvalidRecord, len(key), len(extent))
	}
	header := extent[:RecordHeaderSize]
	if !bytes.Equal(header[0:8], recordMagic[:]) || binary.BigEndian.Uint16(header[8:10]) != WALFormatVersion {
		return out, fmt.Errorf("%w: magic/version", ErrInvalidRecord)
	}
	if binary.BigEndian.Uint16(header[10:12]) != 0 || binary.BigEndian.Uint16(header[12:14]) != RecordHeaderSize || binary.BigEndian.Uint16(header[38:40]) != 0 {
		return out, fmt.Errorf("%w: header constants", ErrInvalidRecord)
	}
	frameLength := binary.BigEndian.Uint32(header[16:20])
	charged := binary.BigEndian.Uint32(header[20:24])
	ciphertextLength := binary.BigEndian.Uint32(header[24:28])
	if charged != uint32(EncodedRecordCharge()) || ciphertextLength > MaxCanonicalEventSize || frameLength != RecordHeaderSize+ciphertextLength+AEADTagSize+RecordTrailerSize {
		return out, fmt.Errorf("%w: lengths", ErrInvalidRecord)
	}
	if binary.BigEndian.Uint16(header[34:36]) != AEADTagSize || binary.BigEndian.Uint16(header[36:38]) != RecordTrailerSize {
		return out, fmt.Errorf("%w: tag/trailer lengths", ErrInvalidRecord)
	}
	schemaLength := int(binary.BigEndian.Uint16(header[30:32]))
	keyIDLength := int(binary.BigEndian.Uint16(header[32:34]))
	extensionLength := int(binary.BigEndian.Uint16(header[14:16]))
	if schemaLength < 1 || schemaLength > MaxEventSchemaIDSize || keyIDLength < 1 || keyIDLength > MaxKeyIDSize || extensionLength > MaxHeaderExtension {
		return out, fmt.Errorf("%w: variable field lengths", ErrInvalidRecord)
	}
	if !allZero(header[140+schemaLength:204]) || !allZero(header[204+keyIDLength:300]) || !allZero(header[300+extensionLength:512]) {
		return out, fmt.Errorf("%w: non-zero header padding", ErrNonCanonicalField)
	}
	schemaID := string(header[140 : 140+schemaLength])
	keyID := string(header[204 : 204+keyIDLength])
	if err := validateASCII("event_schema_id", schemaID, MaxEventSchemaIDSize); err != nil {
		return out, err
	}
	if err := validateASCII("key_id", keyID, MaxKeyIDSize); err != nil {
		return out, err
	}
	extensions, err := decodeExtensions(header[300 : 300+extensionLength])
	if err != nil {
		return out, err
	}

	tagOffset := RecordHeaderSize + int(ciphertextLength)
	trailerOffset := tagOffset + AEADTagSize
	trailer := extent[trailerOffset : trailerOffset+RecordTrailerSize]
	ordinal := binary.BigEndian.Uint64(header[56:64])
	if binary.BigEndian.Uint32(trailer[4:8]) != frameLength || binary.BigEndian.Uint64(trailer[8:16]) != ordinal || binary.BigEndian.Uint16(trailer[16:18]) != TrailerVersion || binary.BigEndian.Uint16(trailer[18:20]) != 0 || binary.BigEndian.Uint32(trailer[20:24]) != 0 || !bytes.Equal(trailer[24:32], commitMarker[:]) {
		return out, fmt.Errorf("%w: trailer", ErrInvalidRecord)
	}
	crc := crc32.New(crc32cTable)
	_, _ = crc.Write(header)
	_, _ = crc.Write(extent[RecordHeaderSize:tagOffset])
	_, _ = crc.Write(extent[tagOffset:trailerOffset])
	if binary.BigEndian.Uint32(trailer[0:4]) != crc.Sum32() {
		return out, fmt.Errorf("%w: crc32c", ErrInvalidRecord)
	}

	accountingOffset := len(extent) - AccountingSize
	if int(frameLength) > accountingOffset || !allZero(extent[frameLength:accountingOffset]) {
		return out, fmt.Errorf("%w: extent padding", ErrNonCanonicalField)
	}
	accounting := extent[accountingOffset:]
	if err := verifyAccounting(accounting, extent[:frameLength], header, frameLength, charged); err != nil {
		return out, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return out, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return out, err
	}
	sealed := make([]byte, 0, int(ciphertextLength)+AEADTagSize)
	sealed = append(sealed, extent[RecordHeaderSize:tagOffset]...)
	sealed = append(sealed, extent[tagOffset:trailerOffset]...)
	plaintext, err := gcm.Open(nil, header[128:140], sealed, header)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	digest := sha256.Sum256(plaintext)
	if !bytes.Equal(digest[:], header[96:128]) {
		return out, ErrEventDigest
	}

	copy(out.SegmentID[:], header[40:56])
	out.RecordOrdinal = ordinal
	copy(out.ReservationID[:], header[64:80])
	copy(out.EventUUID[:], header[80:96])
	out.EventSchemaID = schemaID
	out.EventSchemaVersion = binary.BigEndian.Uint16(header[28:30])
	out.KeyID = keyID
	copy(out.Nonce[:], header[128:140])
	out.CanonicalEvent = plaintext
	out.Extensions = extensions
	copy(out.EventPayloadDigest[:], header[96:128])
	out.FrameLength = frameLength
	out.ChargedBytes = charged
	return out, nil
}

func validateASCII(name, value string, max int) error {
	if len(value) == 0 || len(value) > max {
		return fmt.Errorf("%w: %s length %d", ErrNonCanonicalField, name, len(value))
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return fmt.Errorf("%w: %s must be visible ASCII", ErrNonCanonicalField, name)
		}
	}
	return nil
}

func encodeExtensions(extensions []Extension) ([]byte, error) {
	var encoded []byte
	var previous uint16
	for _, extension := range extensions {
		if extension.Tag == 0 || extension.Tag <= previous || len(extension.Value) > MaxHeaderExtension-4 {
			return nil, fmt.Errorf("%w: extension tag/order/length", ErrNonCanonicalField)
		}
		if len(encoded)+4+len(extension.Value) > MaxHeaderExtension {
			return nil, fmt.Errorf("%w: extensions exceed %d bytes", ErrNonCanonicalField, MaxHeaderExtension)
		}
		var prefix [4]byte
		binary.BigEndian.PutUint16(prefix[0:2], extension.Tag)
		binary.BigEndian.PutUint16(prefix[2:4], uint16(len(extension.Value)))
		encoded = append(encoded, prefix[:]...)
		encoded = append(encoded, extension.Value...)
		previous = extension.Tag
	}
	return encoded, nil
}

func decodeExtensions(encoded []byte) ([]Extension, error) {
	var extensions []Extension
	var previous uint16
	for len(encoded) > 0 {
		if len(encoded) < 4 {
			return nil, fmt.Errorf("%w: truncated extension", ErrInvalidRecord)
		}
		tag := binary.BigEndian.Uint16(encoded[0:2])
		length := int(binary.BigEndian.Uint16(encoded[2:4]))
		if tag == 0 || tag <= previous || length > len(encoded)-4 {
			return nil, fmt.Errorf("%w: extension tag/order/length", ErrInvalidRecord)
		}
		value := append([]byte(nil), encoded[4:4+length]...)
		extensions = append(extensions, Extension{Tag: tag, Value: value})
		encoded = encoded[4+length:]
		previous = tag
	}
	return extensions, nil
}

func accountingCRC(accounting []byte) uint32 {
	copyOfAccounting := append([]byte(nil), accounting...)
	clear(copyOfAccounting[20:24])
	return crc32.Checksum(copyOfAccounting, crc32cTable)
}

func verifyAccounting(accounting, frame, header []byte, frameLength, charged uint32) error {
	if !bytes.Equal(accounting[0:8], accountingMagic[:]) || binary.BigEndian.Uint16(accounting[8:10]) != AccountingVersion || binary.BigEndian.Uint16(accounting[10:12]) != AccountingSize || binary.BigEndian.Uint32(accounting[12:16]) != frameLength || binary.BigEndian.Uint32(accounting[16:20]) != charged {
		return fmt.Errorf("%w: accounting constants", ErrInvalidRecord)
	}
	if binary.BigEndian.Uint32(accounting[20:24]) != accountingCRC(accounting) || !allZero(accounting[128:]) {
		return fmt.Errorf("%w: accounting crc/padding", ErrInvalidRecord)
	}
	if !bytes.Equal(accounting[24:40], header[40:56]) || !bytes.Equal(accounting[40:48], header[56:64]) || !bytes.Equal(accounting[48:64], header[80:96]) || !bytes.Equal(accounting[64:96], header[96:128]) {
		return fmt.Errorf("%w: accounting identity", ErrInvalidRecord)
	}
	recordDigest := sha256.Sum256(frame)
	if !bytes.Equal(accounting[96:128], recordDigest[:]) {
		return fmt.Errorf("%w: accounting record digest", ErrInvalidRecord)
	}
	return nil
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
