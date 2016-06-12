package orc

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"code.simon-critchley.co.uk/orc/proto"
)

var (
	unsupportedFormat = fmt.Errorf("unsupported format")
)

// TreeReader is an interface that provides methods for reading an individual stream.
type TreeReader interface {
	HasNext() bool
	IsPresent() bool
	Next() interface{}
	Err() error
}

// BaseTreeReader wraps a *BooleanReader and is used for reading the Present stream
// in all TreeReader implementations.
type BaseTreeReader struct {
	*BooleanReader
}

// NewBaseTreeReader return a new BaseTreeReader from the provided io.Reader.
func NewBaseTreeReader(r io.Reader) BaseTreeReader {
	if r == nil {
		return BaseTreeReader{}
	}
	return BaseTreeReader{NewBooleanReader(bufio.NewReader(r))}
}

// IsPresent returns true if a value is available and is present in the stream.
func (b BaseTreeReader) HasNext() bool {
	if b.BooleanReader != nil {
		return b.BooleanReader.HasNext()
	}
	return true
}

// IsPresent returns true if a value is available and is present in the stream.
func (b BaseTreeReader) IsPresent() bool {
	if b.BooleanReader != nil {
		return b.BooleanReader.NextBool()
	}
	return true
}

// Err returns the last error to occur.
func (b BaseTreeReader) Err() error {
	if b.BooleanReader != nil {
		return b.BooleanReader.Err()
	}
	return nil
}

// IntegerReader is an interface that provides methods for reading an integer stream.
type IntegerReader interface {
	TreeReader
	NextInt() int64
}

// IntegerTreeReader is a TreeReader that can read Integer type streams.
type IntegerTreeReader struct {
	BaseTreeReader
	IntegerReader
}

func (i *IntegerTreeReader) IsPresent() bool {
	return i.BaseTreeReader.IsPresent()
}

func (i *IntegerTreeReader) HasNext() bool {
	return i.BaseTreeReader.HasNext() && i.IntegerReader.HasNext()
}

func (i *IntegerTreeReader) Err() error {
	if err := i.IntegerReader.Err(); err != nil {
		return err
	}
	return i.BaseTreeReader.Err()
}

// NewIntegerTreeReader returns a new IntegerReader or an error if one occurs.
func NewIntegerTreeReader(present, data io.Reader, encoding *proto.ColumnEncoding) (*IntegerTreeReader, error) {
	ireader, err := createIntegerReader(encoding.GetKind(), data, true, false)
	if err != nil {
		return nil, err
	}
	return &IntegerTreeReader{
		NewBaseTreeReader(present),
		ireader,
	}, nil
}

func createIntegerReader(kind proto.ColumnEncoding_Kind, in io.Reader, signed, skipCorrupt bool) (IntegerReader, error) {
	switch kind {
	case proto.ColumnEncoding_DIRECT_V2, proto.ColumnEncoding_DICTIONARY_V2:
		return NewRunLengthIntegerReaderV2(bufio.NewReader(in), signed, skipCorrupt), nil
	case proto.ColumnEncoding_DIRECT, proto.ColumnEncoding_DICTIONARY:
		return NewRunLengthIntegerReader(bufio.NewReader(in), signed), nil
	default:
		return nil, fmt.Errorf("unknown encoding: %s", kind)
	}
}

// IntegerReader is an interface that provides methods for reading a string stream.
type StringTreeReader interface {
	TreeReader
	NextString() string
}

func NewStringTreeReader(present, data, length, dictionary io.Reader, encoding *proto.ColumnEncoding) (StringTreeReader, error) {
	switch kind := encoding.GetKind(); kind {
	case proto.ColumnEncoding_DIRECT, proto.ColumnEncoding_DIRECT_V2:
		return NewStringDirectTreeReader(present, data, length, kind)
	case proto.ColumnEncoding_DICTIONARY, proto.ColumnEncoding_DICTIONARY_V2:
		return NewStringDictionaryTreeReader(present, data, length, dictionary, encoding)
	}
	return nil, fmt.Errorf("unsupported column encoding: %s", encoding.GetKind())
}

type StringDirectTreeReader struct {
	BaseTreeReader
	lengths IntegerReader
	data    io.Reader
	err     error
}

func NewStringDirectTreeReader(present, data, length io.Reader, kind proto.ColumnEncoding_Kind) (*StringDirectTreeReader, error) {
	ireader, err := createIntegerReader(kind, length, false, false)
	if err != nil {
		return nil, err
	}
	return &StringDirectTreeReader{
		BaseTreeReader: NewBaseTreeReader(present),
		lengths:        ireader,
		data:           data,
	}, nil
}

func (s *StringDirectTreeReader) HasNext() bool {
	return s.lengths.HasNext() && s.err == nil
}

func (s *StringDirectTreeReader) NextString() string {
	l := int(s.lengths.NextInt())
	byt := make([]byte, l, l)
	n, err := s.data.Read(byt)
	if err != nil {
		s.err = err
		return ""
	}
	if n != l {
		s.err = fmt.Errorf("read unexpected number of bytes: %v expected: %v", n, l)
		return ""
	}
	return string(byt)
}

func (s *StringDirectTreeReader) Next() interface{} {
	return s.NextString()
}

func (s *StringDirectTreeReader) Err() error {
	err := s.lengths.Err()
	if err != nil {
		return err
	}
	return s.err
}

type StringDictionaryTreeReader struct {
	BaseTreeReader
	dictionaryOffsets []int
	dictionaryLengths []int
	reader            IntegerReader
	dictionaryBytes   []byte
	err               error
}

func NewStringDictionaryTreeReader(present, data, length, dictionary io.Reader, encoding *proto.ColumnEncoding) (*StringDictionaryTreeReader, error) {
	ireader, err := createIntegerReader(encoding.GetKind(), data, false, false)
	if err != nil {
		return nil, err
	}
	r := &StringDictionaryTreeReader{
		BaseTreeReader: NewBaseTreeReader(present),
		reader:         ireader,
	}
	if dictionary != nil && encoding != nil {
		err := r.readDictionaryStream(dictionary)
		if err != nil {
			return nil, err
		}
		if length != nil {
			err = r.readDictionaryLengths(length, encoding)
			if err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (s *StringDictionaryTreeReader) readDictionaryStream(dictionary io.Reader) error {
	var buf bytes.Buffer
	_, err := io.Copy(&buf, dictionary)
	if err != nil {
		return err
	}
	s.dictionaryBytes = buf.Bytes()
	return nil
}

func (s *StringDictionaryTreeReader) readDictionaryLengths(length io.Reader, encoding *proto.ColumnEncoding) error {
	lreader, err := createIntegerReader(encoding.GetKind(), length, false, false)
	if err != nil {
		return err
	}
	var offset int
	for lreader.HasNext() {
		l := int(lreader.NextInt())
		s.dictionaryLengths = append(s.dictionaryLengths, l)
		s.dictionaryOffsets = append(s.dictionaryOffsets, offset)
		offset += l
	}
	if err := lreader.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (s *StringDictionaryTreeReader) IsPresent() bool {
	return s.BaseTreeReader.IsPresent()
}

func (s *StringDictionaryTreeReader) HasNext() bool {
	return s.BaseTreeReader.HasNext() && s.reader.HasNext()
}

func (s *StringDictionaryTreeReader) getIndexLength(i int) (int, int) {
	if i >= len(s.dictionaryLengths) || i < 0 {
		s.err = fmt.Errorf("invalid integer value: %v expecting values between 0...%v", i, len(s.dictionaryLengths))
		return 0, 0
	}
	if i >= len(s.dictionaryOffsets) || i < 0 {
		s.err = fmt.Errorf("invalid integer value: %v expecting values between 0...%v", i, len(s.dictionaryOffsets))
		return 0, 0
	}
	return s.dictionaryOffsets[i], s.dictionaryLengths[i]
}

func (s *StringDictionaryTreeReader) NextString() string {
	i := int(s.reader.NextInt())
	offset, length := s.getIndexLength(i)
	return string(s.dictionaryBytes[offset : offset+length])
}

func (s *StringDictionaryTreeReader) Next() interface{} {
	return s.NextString()
}

func (s *StringDictionaryTreeReader) Err() error {
	return nil
}

type BooleanTreeReader struct {
	BaseTreeReader
	*BooleanReader
}

func (b *BooleanTreeReader) IsPresent() bool {
	return b.BaseTreeReader.IsPresent()
}

func (b *BooleanTreeReader) HasNext() bool {
	return b.BaseTreeReader.HasNext() && b.BooleanReader.HasNext()
}

func (b *BooleanTreeReader) NextBool() bool {
	return b.BooleanReader.NextBool()
}

func (b *BooleanTreeReader) Next() interface{} {
	return b.NextBool()
}

func (b *BooleanTreeReader) Err() error {
	if err := b.BooleanReader.Err(); err != nil {
		return err
	}
	return b.BaseTreeReader.Err()
}

func NewBooleanTreeReader(present, data io.Reader, encoding *proto.ColumnEncoding) (*BooleanTreeReader, error) {
	return &BooleanTreeReader{
		NewBaseTreeReader(present),
		NewBooleanReader(bufio.NewReader(data)),
	}, nil
}

type ByteTreeReader struct {
	BaseTreeReader
	*RunLengthByteReader
}

func (b *ByteTreeReader) IsPresent() bool {
	return b.BaseTreeReader.IsPresent()
}

func (b *ByteTreeReader) HasNext() bool {
	return b.BaseTreeReader.HasNext() && b.RunLengthByteReader.HasNext()
}

func (b *ByteTreeReader) NextByte() byte {
	return b.RunLengthByteReader.NextByte()
}

func (b *ByteTreeReader) Next() interface{} {
	return b.NextByte()
}

func (b *ByteTreeReader) Err() error {
	if err := b.RunLengthByteReader.Err(); err != nil {
		return err
	}
	return b.BaseTreeReader.Err()
}

func NewByteTreeReader(present, data io.Reader, encoding *proto.ColumnEncoding) (*ByteTreeReader, error) {
	return &ByteTreeReader{
		NewBaseTreeReader(present),
		NewRunLengthByteReader(bufio.NewReader(data)),
	}, nil
}
