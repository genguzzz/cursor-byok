// Package codec implements the minimal protobuf wire format needed to read
// Cursor Tab requests and write Tab responses.
//
// Only the varint, 64-bit, length-delimited and 32-bit wire types are handled,
// and only the tags declared in this package's message structs are interpreted.
// Every other tag is skipped by length or wire size, so new upstream fields
// never break decoding.
package codec

import (
	"encoding/binary"
	"errors"
	"math"
)

// Protobuf wire types.
const (
	WireVarint = 0
	Wire64Bit  = 1
	WireBytes  = 2
	Wire32Bit  = 5
)

// ErrTruncated is returned when a field extends past the end of the buffer.
var ErrTruncated = errors.New("truncated protobuf field")

// ErrBadWireType is returned when a tag declares an unsupported wire type.
var ErrBadWireType = errors.New("unsupported protobuf wire type")

// Reader decodes protobuf fields sequentially.
type Reader struct {
	buf []byte
	pos int
}

// NewReader returns a Reader over buf.
func NewReader(buf []byte) *Reader {
	return &Reader{buf: buf}
}

// Next returns the next field. It returns false at end of input.
func (r *Reader) Next() (Field, bool, error) {
	if r.pos >= len(r.buf) {
		return Field{}, false, nil
	}
	key, n, err := readVarint(r.buf, r.pos)
	if err != nil {
		return Field{}, false, err
	}
	r.pos = n
	field := Field{
		Number:   int32(key >> 3),
		WireType: int(key & 0x07),
	}
	switch field.WireType {
	case WireVarint:
		value, n, err := readVarint(r.buf, r.pos)
		if err != nil {
			return Field{}, false, err
		}
		field.Varint = value
		r.pos = n
	case Wire64Bit:
		if r.pos+8 > len(r.buf) {
			return Field{}, false, ErrTruncated
		}
		field.Fixed64 = binary.LittleEndian.Uint64(r.buf[r.pos : r.pos+8])
		r.pos += 8
	case WireBytes:
		length, n, err := readVarint(r.buf, r.pos)
		if err != nil {
			return Field{}, false, err
		}
		if int(length) < 0 || n+int(length) > len(r.buf) {
			return Field{}, false, ErrTruncated
		}
		field.Bytes = r.buf[n : n+int(length)]
		r.pos = n + int(length)
	case Wire32Bit:
		if r.pos+4 > len(r.buf) {
			return Field{}, false, ErrTruncated
		}
		field.Fixed32 = binary.LittleEndian.Uint32(r.buf[r.pos : r.pos+4])
		r.pos += 4
	default:
		return Field{}, false, wrapWireType(field.WireType)
	}
	return field, true, nil
}

// Field is one decoded protobuf field.
type Field struct {
	Number   int32
	WireType int
	Varint   uint64
	Fixed64  uint64
	Fixed32  uint32
	Bytes    []byte
}

// Bool interprets a varint field as a bool.
func (f Field) Bool() bool {
	return f.Varint != 0
}

// Int32 interprets a varint field as a signed 32-bit integer.
func (f Field) Int32() int32 {
	return int32(uint32(f.Varint))
}

// Int64 interprets a varint field as a signed 64-bit integer.
func (f Field) Int64() int64 {
	return int64(f.Varint)
}

// Float32 interprets a 32-bit field as a float.
func (f Field) Float32() float32 {
	return math.Float32frombits(f.Fixed32)
}

// Float64 interprets a 64-bit field as a double.
func (f Field) Float64() float64 {
	return math.Float64frombits(f.Fixed64)
}

// String interprets a length-delimited field as a string.
func (f Field) String() string {
	return string(f.Bytes)
}

// Writer encodes protobuf fields.
type Writer struct {
	buf []byte
}

// NewWriter returns a Writer with capacity hint.
func NewWriter(size int) *Writer {
	return &Writer{buf: make([]byte, 0, size)}
}

// Bytes returns the encoded buffer.
func (w *Writer) Bytes() []byte {
	return w.buf
}

// Tag writes a field key.
func (w *Writer) Tag(number int32, wireType int) {
	w.Varint(uint64(uint32(number))<<3 | uint64(wireType))
}

// Varint writes a base-128 varint.
func (w *Writer) Varint(value uint64) {
	for value >= 0x80 {
		w.buf = append(w.buf, byte(value)|0x80)
		value >>= 7
	}
	w.buf = append(w.buf, byte(value))
}

// ZigZagVarint writes a signed integer using zig-zag encoding.
func (w *Writer) ZigZagVarint(value int64) {
	w.Varint(uint64(value<<1) ^ uint64(value>>63))
}

// String writes a length-delimited string field. An empty value is omitted,
// matching proto3 default-value elision.
func (w *Writer) String(number int32, value string) {
	if value == "" {
		return
	}
	w.Tag(number, WireBytes)
	w.Varint(uint64(len(value)))
	w.buf = append(w.buf, value...)
}

// Nested writes an already-encoded submessage as a length-delimited field.
// An empty submessage is omitted.
func (w *Writer) Nested(number int32, value []byte) {
	if len(value) == 0 {
		return
	}
	w.Tag(number, WireBytes)
	w.Varint(uint64(len(value)))
	w.buf = append(w.buf, value...)
}

// Bool writes a varint bool field. A false value is omitted.
func (w *Writer) Bool(number int32, value bool) {
	if !value {
		return
	}
	w.Tag(number, WireVarint)
	w.buf = append(w.buf, 1)
}

// Int32 writes a varint int32 field. A zero value is omitted.
func (w *Writer) Int32(number int32, value int32) {
	if value == 0 {
		return
	}
	w.Tag(number, WireVarint)
	w.Varint(uint64(uint32(value)))
}

// Int64 writes a varint int64 field. A zero value is omitted.
func (w *Writer) Int64(number int32, value int64) {
	if value == 0 {
		return
	}
	w.Tag(number, WireVarint)
	w.Varint(uint64(value))
}

// Float32 writes a 32-bit float field. A zero value is omitted.
func (w *Writer) Float32(number int32, value float32) {
	if value == 0 {
		return
	}
	w.Tag(number, Wire32Bit)
	var bits [4]byte
	binary.LittleEndian.PutUint32(bits[:], math.Float32bits(value))
	w.buf = append(w.buf, bits[:]...)
}

// Float64 writes a 64-bit double field. A zero value is omitted.
func (w *Writer) Float64(number int32, value float64) {
	if value == 0 {
		return
	}
	w.Tag(number, Wire64Bit)
	var bits [8]byte
	binary.LittleEndian.PutUint64(bits[:], math.Float64bits(value))
	w.buf = append(w.buf, bits[:]...)
}

func readVarint(buf []byte, pos int) (uint64, int, error) {
	var value uint64
	var shift uint
	for i := pos; i < len(buf); i++ {
		if shift >= 64 {
			return 0, 0, ErrTruncated
		}
		value |= uint64(buf[i]&0x7f) << shift
		if buf[i] < 0x80 {
			return value, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, ErrTruncated
}

func wrapWireType(wireType int) error {
	return errorf("%w: %d", ErrBadWireType, wireType)
}
