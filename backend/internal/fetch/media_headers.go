package fetch

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
)

var ErrMediaHeaders = errors.New("invalid or repeated critical media response headers")

// mediaHeaderConn preserves net/http for HTTP parsing/body framing while
// refusing evidence it would otherwise normalize away. It buffers only bounded
// response headers, including interim responses; body bytes stay in the reader.
// Connections in this mode are not reused for a second request.
type mediaHeaderConn struct {
	net.Conn
	reader      *bufio.Reader
	limit       int64
	pending     []byte
	checked     bool
	headerBytes int64
	seen        map[string]bool
	chunked     bool
	chunkStage  int
	remaining   uint64
}

func newMediaHeaderConn(conn net.Conn, limit int64) *mediaHeaderConn {
	return &mediaHeaderConn{Conn: conn, reader: bufio.NewReaderSize(conn, 4096), limit: limit}
}

func (c *mediaHeaderConn) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	if !c.checked {
		if err := c.check(); err != nil {
			return 0, err
		}
		c.checked = true
	}
	if len(c.pending) > 0 {
		n := copy(output, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.chunked {
		return c.readChunked(output)
	}
	return c.reader.Read(output)
}

func (c *mediaHeaderConn) line() ([]byte, error) {
	start := len(c.pending)
	for {
		fragment, err := c.reader.ReadSlice('\n')
		if c.headerBytes+int64(len(fragment)) > c.limit {
			return nil, ErrMediaHeaders
		}
		c.headerBytes += int64(len(fragment))
		c.pending = append(c.pending, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		return c.pending[start:], nil
	}
}

func (c *mediaHeaderConn) check() error {
	for {
		statusLine, err := c.line()
		if err != nil {
			return err
		}
		parts := bytes.Fields(statusLine)
		if len(parts) < 2 || (!bytes.Equal(parts[0], []byte("HTTP/1.0")) && !bytes.Equal(parts[0], []byte("HTTP/1.1"))) {
			return ErrMediaHeaders
		}
		status, err := strconv.Atoi(string(parts[1]))
		if err != nil || status < 100 || status > 999 {
			return ErrMediaHeaders
		}
		seen := map[string]bool{}
		mimeStart := len(c.pending)
		for {
			line, err := c.line()
			if err != nil {
				return err
			}
			// A whitespace-only continuation is not the terminating empty line.
			// Stopping there would let later duplicate fields reach net/http.
			if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
				break
			}
			colon := bytes.IndexByte(line, ':')
			if colon < 0 {
				continue
			} // net/http validates non-field syntax.
			key := strings.ToLower(strings.TrimSpace(string(line[:colon])))
			switch key {
			case "location", "content-range", "content-length", "content-type", "content-encoding":
				if seen[key] {
					return ErrMediaHeaders
				}
				seen[key] = true
			}
		}
		if status >= 200 || status == 101 {
			headers, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(c.pending[mimeStart:]))).ReadMIMEHeader()
			if err != nil {
				return err
			}
			encodings := headers.Values("Transfer-Encoding")
			c.chunked = len(encodings) == 1 && strings.EqualFold(encodings[0], "chunked")
			c.seen = seen
			return nil
		}
		// The same field may occur once in a later response. The cumulative
		// header byte limit still includes every interim response.
	}
}

// Observe chunk boundaries without decoding or buffering chunk data. This
// allows trailer bytes to share the original header budget and duplicate set;
// net/http still performs the actual HTTP body framing and decoding.
func (c *mediaHeaderConn) readChunked(output []byte) (int, error) {
	for len(c.pending) == 0 {
		switch c.chunkStage {
		case 0: // Chunk size and optional extension.
			line, err := c.reader.ReadSlice('\n')
			if err != nil || len(line) >= 4096 {
				if err == nil {
					err = ErrMediaHeaders
				}
				return 0, err
			}
			value := strings.TrimRight(string(line), " \t\r\n")
			value = strings.SplitN(value, ";", 2)[0]
			if value == "" || len(value) > 16 {
				return 0, ErrMediaHeaders
			}
			for _, ch := range value {
				if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
					return 0, ErrMediaHeaders
				}
			}
			size, err := strconv.ParseUint(value, 16, 64)
			if err != nil {
				return 0, err
			}
			c.pending = append(c.pending, line...)
			c.remaining = size
			c.chunkStage = 1
			if size == 0 {
				c.chunkStage = 3
			}
		case 1: // Forward only bytes belonging to this chunk.
			length := len(output)
			if uint64(length) > c.remaining {
				length = int(c.remaining)
			}
			n, err := c.reader.Read(output[:length])
			c.remaining -= uint64(n)
			if c.remaining == 0 {
				c.chunkStage = 2
			}
			return n, err
		case 2:
			ending := make([]byte, 2)
			if _, err := io.ReadFull(c.reader, ending); err != nil {
				return 0, err
			}
			if !bytes.Equal(ending, []byte("\r\n")) {
				return 0, ErrMediaHeaders
			}
			c.pending = ending
			c.chunkStage = 0
		case 3: // Trailers have the same critical-field and raw-byte boundary.
			for {
				line, err := c.line()
				if err != nil {
					return 0, err
				}
				if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
					break
				}
				colon := bytes.IndexByte(line, ':')
				if colon < 0 {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(string(line[:colon])))
				switch key {
				case "location", "content-range", "content-length", "content-type", "content-encoding":
					if c.seen[key] {
						return 0, ErrMediaHeaders
					}
					c.seen[key] = true
				}
			}
			c.chunkStage = 4
		case 4:
			return 0, io.EOF
		}
	}
	n := copy(output, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
