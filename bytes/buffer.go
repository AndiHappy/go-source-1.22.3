// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bytes

// Simple byte buffer for marshaling data.

import (
	"errors"
	"io"
	"unicode/utf8"
)

// smallBufferSize is an initial allocation minimal capacity.
const smallBufferSize = 64

// A Buffer is a variable-sized buffer of bytes with [Buffer.Read] and [Buffer.Write] methods.
// The zero value for Buffer is an empty buffer ready to use.
type Buffer struct {
	buf []byte // contents are the bytes buf[off : len(buf)],
	// 缓存数据buf[off : len(buf)]，类似Scan中的buf使用方法
	off int // read at &buf[off], write at &buf[len(buf)],
	// 缓存的读写位置：&buf[off],缓存写的位置：&buf[len(buf)]
	lastRead readOp // last read operation, so that Unread* can work correctly.
	// 上一次读的操作
}

// The readOp constants describe the last action performed on
// the buffer, so that UnreadRune and UnreadByte can check for
// invalid usage. opReadRuneX constants are chosen such that
// converted to int they correspond to the rune size that was read.
type readOp int8

// Don't use iota for these, as the values need to correspond with the
// names and comments, which is easier to see when being explicit.
const (
	opRead      readOp = -1 // Any other read operation.
	opInvalid   readOp = 0  // Non-read operation.
	opReadRune1 readOp = 1  // Read rune of size 1.
	opReadRune2 readOp = 2  // Read rune of size 2.
	opReadRune3 readOp = 3  // Read rune of size 3.
	opReadRune4 readOp = 4  // Read rune of size 4.
)

// ErrTooLarge is passed to panic if memory cannot be allocated to store data in a buffer.
var ErrTooLarge = errors.New("bytes.Buffer: too large")
var errNegativeRead = errors.New("bytes.Buffer: reader returned negative count from Read")

const maxInt = int(^uint(0) >> 1) //9223372036854775807

// Bytes returns a slice of length b.Len() holding the unread portion of the buffer.
// The slice is valid for use only until the next buffer modification (that is,
// only until the next call to a method like [Buffer.Read], [Buffer.Write], [Buffer.Reset], or [Buffer.Truncate]).
// The slice aliases the buffer content at least until the next buffer modification,
// so immediate changes to the slice will affect the result of future reads.
//   - 返回的是buffer中的没有读的缓存数据
func (b *Buffer) Bytes() []byte { return b.buf[b.off:] }

// AvailableBuffer returns an empty buffer with b.Available() capacity.
// This buffer is intended to be appended to and
// passed to an immediately succeeding [Buffer.Write] call.
// The buffer is only valid until the next write operation on b.
//   - 返回的是一个empty buffer，容量是b.buf
func (b *Buffer) AvailableBuffer() []byte { return b.buf[len(b.buf):] }

// String returns the contents of the unread portion of the buffer
// as a string. If the [Buffer] is a nil pointer, it returns "<nil>".
//
// To build strings more efficiently, see the strings.Builder type.
//
//	返回buffer中的数据，以String的样式
func (b *Buffer) String() string {
	if b == nil {
		// Special case, useful in debugging.
		return "<nil>"
	}
	return string(b.buf[b.off:])
}

// empty reports whether the unread portion of the buffer is empty.
//
//	确定buffer中是否存在未读的数据
func (b *Buffer) empty() bool { return len(b.buf) <= b.off }

// Len returns the number of bytes of the unread portion of the buffer;
// b.Len() == len(b.Bytes()).
// buffer未读的数据的大小
func (b *Buffer) Len() int { return len(b.buf) - b.off }

// Cap returns the capacity of the buffer's underlying byte slice, that is, the
// total space allocated for the buffer's data.
// buffer的缓存数据的cap
func (b *Buffer) Cap() int { return cap(b.buf) }

// Available returns how many bytes are unused in the buffer.
func (b *Buffer) Available() int { return cap(b.buf) - len(b.buf) }

// Truncate discards all but the first n unread bytes from the buffer
// but continues to use the same allocated storage.
// It panics if n is negative or greater than the length of the buffer.
//
//	截断会丢弃缓冲区中除前 n 个未读取字节之外的所有字节，但继续使用相同的分配存储空间。
//	英语表述很有意思： discard all but the first n unread bytes from the buffer
func (b *Buffer) Truncate(n int) {
	if n == 0 {
		b.Reset()
		return
	}
	b.lastRead = opInvalid
	if n < 0 || n > b.Len() {
		panic("bytes.Buffer: truncation out of range")
	}
	b.buf = b.buf[:b.off+n] //只保留了b.off+n 其余的全部丢弃了
}

// Reset resets the buffer to be empty,
// but it retains the underlying storage for use by future writes.
// Reset is the same as [Buffer.Truncate](0).
func (b *Buffer) Reset() { //成员变量全部进行了设置
	b.buf = b.buf[:0]
	b.off = 0
	b.lastRead = opInvalid
}

// tryGrowByReslice is an inlineable version of grow for the fast-case where the
// internal buffer only needs to be resliced.
// It returns the index where bytes should be written and whether it succeeded.
func (b *Buffer) tryGrowByReslice(n int) (int, bool) {
	if l := len(b.buf); n <= cap(b.buf)-l { //在cap(b.buf)的范围之下的
		b.buf = b.buf[:l+n]
		return l, true
	}
	return 0, false
}

// grow grows the buffer to guarantee space for n more bytes.
// It returns the index where bytes should be written.
// If the buffer can't grow it will panic with ErrTooLarge.
// 增长n个byte的空间
func (b *Buffer) grow(n int) int {
	m := b.Len()
	// If buffer is empty, reset to recover space.
	// buffer为empty的情况下，重新设置
	if m == 0 && b.off != 0 {
		b.Reset()
	}

	// Try to grow by means of a reslice.
	if i, ok := b.tryGrowByReslice(n); ok {
		//n <= cap(b.buf)-len(b.buf)的情况下，才能够直接扩展
		return i
	}

	if b.buf == nil && n <= smallBufferSize {
		//如果buf为nil的情况下，申请的n较小的情况下，直接分配
		b.buf = make([]byte, n, smallBufferSize)
		return 0
	}

	c := cap(b.buf)
	if n <= c/2-m { //m=len(b.buf)
		// We can slide things down instead of allocating a new
		// slice. We only need m+n <= c to slide, but
		// we instead let capacity get twice as large so we
		// don't spend all our time copying.
		// 我们可以将内容向下滑动，而不是分配新的切片。我们只需要 m+n <= c 即可滑动，
		// 也可以让容量增加一倍，但是这样就会把所有时间都花在复制上。
		copy(b.buf, b.buf[b.off:])
	} else if c > maxInt-c-n {
		//直接扩容的判断 2*cap(b.buf)+n > maxInt
		panic(ErrTooLarge)
	} else {
		// Add b.off to account for b.buf[:b.off] being sliced off the front.
		b.buf = growSlice(b.buf[b.off:], b.off+n)
	}

	// Restore b.off and len(b.buf).
	b.off = 0
	b.buf = b.buf[:m+n]
	return m
}

// Grow grows the buffer's capacity, if necessary, to guarantee space for
// another n bytes. After Grow(n), at least n bytes can be written to the
// buffer without another allocation.
// If n is negative, Grow will panic.
// If the buffer can't grow it will panic with [ErrTooLarge].
func (b *Buffer) Grow(n int) {
	if n < 0 {
		panic("bytes.Buffer.Grow: negative count")
	}
	m := b.grow(n)
	b.buf = b.buf[:m]
}

// Write appends the contents of p to the buffer, growing the buffer as
// needed. The return value n is the length of p; err is always nil. If the
// buffer becomes too large, Write will panic with [ErrTooLarge].
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.lastRead = opInvalid
	m, ok := b.tryGrowByReslice(len(p))
	if !ok {
		m = b.grow(len(p))
	}
	return copy(b.buf[m:], p), nil
}

// WriteString appends the contents of s to the buffer, growing the buffer as
// needed. The return value n is the length of s; err is always nil. If the
// buffer becomes too large, WriteString will panic with [ErrTooLarge].
func (b *Buffer) WriteString(s string) (n int, err error) {
	b.lastRead = opInvalid
	m, ok := b.tryGrowByReslice(len(s))
	if !ok {
		m = b.grow(len(s))
	}
	return copy(b.buf[m:], s), nil
}

// MinRead is the minimum slice size passed to a Read call by
// [Buffer.ReadFrom]. As long as the [Buffer] has at least MinRead bytes beyond
// what is required to hold the contents of r, ReadFrom will not grow the
// underlying buffer.
const MinRead = 512

// ReadFrom reads data from r until EOF and appends it to the buffer, growing
// the buffer as needed. The return value n is the number of bytes read. Any
// error except io.EOF encountered during the read is also returned. If the
// buffer becomes too large, ReadFrom will panic with [ErrTooLarge].
func (b *Buffer) ReadFrom(r io.Reader) (n int64, err error) {
	b.lastRead = opInvalid
	for {
		i := b.grow(MinRead)
		b.buf = b.buf[:i]
		m, e := r.Read(b.buf[i:cap(b.buf)])
		if m < 0 {
			panic(errNegativeRead)
		}

		b.buf = b.buf[:i+m]
		n += int64(m)
		if e == io.EOF {
			return n, nil // e is EOF, so return nil explicitly
		}
		if e != nil {
			return n, e
		}
	}
}

// growSlice grows b by n, preserving the original content of b.
// If the allocation fails, it panics with ErrTooLarge.
// Slice扩容的内容
func growSlice(b []byte, n int) []byte {
	defer func() {
		if recover() != nil {
			panic(ErrTooLarge)
		}
	}()
	// TODO(http://golang.org/issue/51462): We should rely on the append-make
	// pattern so that the compiler can call runtime.growslice. For example:
	//	return append(b, make([]byte, n)...)
	// This avoids unnecessary zero-ing of the first len(b) bytes of the
	// allocated slice, but this pattern causes b to escape onto the heap.
	//
	// Instead use the append-make pattern with a nil slice to ensure that
	// we allocate buffers rounded up to the closest size class.
	c := len(b) + n // ensure enough space for n elements
	if c < 2*cap(b) {
		// The growth rate has historically always been 2x. In the future,
		// we could rely purely on append to determine the growth rate.
		c = 2 * cap(b)
	}
	//这个nil Slice使用是为了保证： rounded up to the closest size class
	b2 := append([]byte(nil), make([]byte, c)...)
	copy(b2, b)
	return b2[:len(b)]

	// 为什么不实用这个方式，直接返回，原因是:this pattern causes b to escape onto the heap.
	// 那么什么情况下，会escape onto the heap.
	// return append(b, make([]byte, n)...)
}

// WriteTo writes data to w until the buffer is drained or an error occurs.
// The return value n is the number of bytes written; it always fits into an
// int, but it is int64 to match the io.WriterTo interface. Any error
// encountered during the write is also returned.
//   - 把缓存的数据全部的写入io.Writer，然后reset
func (b *Buffer) WriteTo(w io.Writer) (n int64, err error) {
	b.lastRead = opInvalid
	if nBytes := b.Len(); nBytes > 0 { //b.Len(),不是len(b.buf),而是len(b.buf) - b.off
		m, e := w.Write(b.buf[b.off:])
		if m > nBytes {
			panic("bytes.Buffer.WriteTo: invalid Write count")
		}
		b.off += m
		n = int64(m)
		if e != nil {
			return n, e
		}
		// all bytes should have been written, by definition of
		// Write method in io.Writer
		if m != nBytes {
			return n, io.ErrShortWrite
		}
	}
	// Buffer is now empty; reset.
	b.Reset()
	return n, nil
}

// WriteByte appends the byte c to the buffer, growing the buffer as needed.
// The returned error is always nil, but is included to match [bufio.Writer]'s
// WriteByte. If the buffer becomes too large, WriteByte will panic with
// [ErrTooLarge].
//   - 向b.buf中写入byte
func (b *Buffer) WriteByte(c byte) error {
	b.lastRead = opInvalid
	//有意思，先扩容
	m, ok := b.tryGrowByReslice(1)
	if !ok {
		m = b.grow(1)
	}
	b.buf[m] = c
	return nil
}

// WriteRune appends the UTF-8 encoding of Unicode code point r to the
// buffer, returning its length and an error, which is always nil but is
// included to match [bufio.Writer]'s WriteRune. The buffer is grown as needed;
// if it becomes too large, WriteRune will panic with [ErrTooLarge].
//   - WriteRune: 先判断容量，再去写入b.buf；其中有针对rune的处理逻辑
func (b *Buffer) WriteRune(r rune) (n int, err error) {
	// Compare as uint32 to correctly handle negative runes.
	if uint32(r) < utf8.RuneSelf {
		b.WriteByte(byte(r))
		return 1, nil
	}
	b.lastRead = opInvalid
	m, ok := b.tryGrowByReslice(utf8.UTFMax)
	if !ok {
		m = b.grow(utf8.UTFMax)
	}
	b.buf = utf8.AppendRune(b.buf[:m], r)
	return len(b.buf) - m, nil
}

// Read reads the next len(p) bytes from the buffer or until the buffer
// is drained. The return value n is the number of bytes read. If the
// buffer has no data to return, err is io.EOF (unless len(p) is zero);
// otherwise it is nil.
//   - buffer向p写入数据
func (b *Buffer) Read(p []byte) (n int, err error) {
	b.lastRead = opInvalid
	if b.empty() { //buf为空
		// Buffer is empty, reset to recover space.
		b.Reset()
		if len(p) == 0 {
			return 0, nil
		}
		return 0, io.EOF
	}
	n = copy(p, b.buf[b.off:])
	b.off += n
	if n > 0 {
		b.lastRead = opRead
	}
	return n, nil
}

// Next returns a slice containing the next n bytes from the buffer,
// advancing the buffer as if the bytes had been returned by [Buffer.Read].
// If there are fewer than n bytes in the buffer, Next returns the entire buffer.
// The slice is only valid until the next call to a read or write method.
// 返回buf中的包含接下来读的n个byte
func (b *Buffer) Next(n int) []byte {
	b.lastRead = opInvalid
	m := b.Len()
	if n > m {
		n = m
	}
	data := b.buf[b.off : b.off+n]
	b.off += n
	if n > 0 {
		b.lastRead = opRead
	}
	return data
}

// ReadByte reads and returns the next byte from the buffer.
// If no byte is available, it returns error io.EOF.
//
//	返回buf中下一个byte
func (b *Buffer) ReadByte() (byte, error) {
	if b.empty() {
		// Buffer is empty, reset to recover space.
		b.Reset()
		return 0, io.EOF
	}
	c := b.buf[b.off]
	b.off++
	b.lastRead = opRead
	return c, nil
}

// ReadRune reads and returns the next UTF-8-encoded
// Unicode code point from the buffer.
// If no bytes are available, the error returned is io.EOF.
// If the bytes are an erroneous UTF-8 encoding, it
// consumes one byte and returns U+FFFD, 1.
// 返回下一个字符
func (b *Buffer) ReadRune() (r rune, size int, err error) {
	if b.empty() {
		// Buffer is empty, reset to recover space.
		b.Reset()
		return 0, 0, io.EOF
	}
	c := b.buf[b.off]
	if c < utf8.RuneSelf {
		b.off++
		b.lastRead = opReadRune1
		return rune(c), 1, nil
	}
	r, n := utf8.DecodeRune(b.buf[b.off:])
	b.off += n
	b.lastRead = readOp(n)
	return r, n, nil
}

// UnreadRune unreads the last rune returned by [Buffer.ReadRune].
// If the most recent read or write operation on the buffer was
// not a successful [Buffer.ReadRune], UnreadRune returns an error.  (In this regard
// it is stricter than [Buffer.UnreadByte], which will unread the last byte
// from any read operation.)
func (b *Buffer) UnreadRune() error {
	if b.lastRead <= opInvalid {
		return errors.New("bytes.Buffer: UnreadRune: previous operation was not a successful ReadRune")
	}
	if b.off >= int(b.lastRead) {
		b.off -= int(b.lastRead)
	}
	b.lastRead = opInvalid
	return nil
}

var errUnreadByte = errors.New("bytes.Buffer: UnreadByte: previous operation was not a successful read")

// UnreadByte unreads the last byte returned by the most recent successful
// read operation that read at least one byte. If a write has happened since
// the last read, if the last read returned an error, or if the read read zero
// bytes, UnreadByte returns an error.
func (b *Buffer) UnreadByte() error {
	if b.lastRead == opInvalid {
		return errUnreadByte
	}
	b.lastRead = opInvalid
	if b.off > 0 {
		b.off--
	}
	return nil
}

// ReadBytes reads until the first occurrence of delim in the input,
// returning a slice containing the data up to and including the delimiter.
// If ReadBytes encounters an error before finding a delimiter,
// it returns the data read before the error and the error itself (often io.EOF).
// ReadBytes returns err != nil if and only if the returned data does not end in
// delim.
// 类似bufio中的readSlice
func (b *Buffer) ReadBytes(delim byte) (line []byte, err error) {
	slice, err := b.readSlice(delim)
	// return a copy of slice. The buffer's backing array may
	// be overwritten by later calls.
	line = append(line, slice...)
	return line, err
}

// readSlice is like ReadBytes but returns a reference to internal buffer data.
//   - 如果没有delim，返回的是整个buf的内容，并且error为io.EOF
func (b *Buffer) readSlice(delim byte) (line []byte, err error) {
	i := IndexByte(b.buf[b.off:], delim)
	end := b.off + i + 1
	if i < 0 {
		end = len(b.buf)
		err = io.EOF
	}
	line = b.buf[b.off:end]
	b.off = end
	b.lastRead = opRead
	return line, err
}

// ReadString reads until the first occurrence of delim in the input,
// returning a string containing the data up to and including the delimiter.
// If ReadString encounters an error before finding a delimiter,
// it returns the data read before the error and the error itself (often io.EOF).
// ReadString returns err != nil if and only if the returned data does not end
// in delim.
// 同样使用readSlice
func (b *Buffer) ReadString(delim byte) (line string, err error) {
	slice, err := b.readSlice(delim)
	return string(slice), err
}

// NewBuffer creates and initializes a new [Buffer] using buf as its
// initial contents. The new [Buffer] takes ownership of buf, and the
// caller should not use buf after this call. NewBuffer is intended to
// prepare a [Buffer] to read existing data. It can also be used to set
// the initial size of the internal buffer for writing. To do that,
// buf should have the desired capacity but a length of zero.
//
// In most cases, new([Buffer]) (or just declaring a [Buffer] variable) is
// sufficient to initialize a [Buffer].
func NewBuffer(buf []byte) *Buffer { return &Buffer{buf: buf} }

// NewBufferString creates and initializes a new [Buffer] using string s as its
// initial contents. It is intended to prepare a buffer to read an existing
// string.
//
// In most cases, new([Buffer]) (or just declaring a [Buffer] variable) is
// sufficient to initialize a [Buffer].
func NewBufferString(s string) *Buffer {
	return &Buffer{buf: []byte(s)}
}
