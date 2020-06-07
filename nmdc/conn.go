package nmdc

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"

	"github.com/RoLex/go-dc/keyprint"
	"github.com/RoLex/go-dc/keyprint/tlskp"
	"github.com/RoLex/go-dc/nmdc"
)

var (
	Debug bool

	DefaultFallbackEncoding encoding.Encoding
)

const writeBuffer = 0

var dialer = net.Dialer{}

// Dial connects to a specified address.
func Dial(addr string) (*Conn, error) {
	return DialContext(context.Background(), addr)
}

// DialContext connects to a specified address.
func DialContext(ctx context.Context, addr string) (*Conn, error) {
	u, err := nmdc.ParseAddr(addr)
	if err != nil {
		return nil, err
	}

	secure := false
	switch u.Scheme {
	case nmdc.SchemeNMDC:
		// continue
	case nmdc.SchemeNMDCS:
		secure = true
	default:
		return nil, fmt.Errorf("unsupported protocol: %q", u.Scheme)
	}

	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		var err2 error
		host, port, err2 = net.SplitHostPort(u.Host + ":" + strconv.Itoa(nmdc.DefaultPort))
		if err2 != nil {
			return nil, err
		}
	}
	u.Host = net.JoinHostPort(host, port)

	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	var kps []string
	if secure {
		sconn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
		})
		if err = sconn.Handshake(); err != nil {
			_ = sconn.Close()
			return nil, fmt.Errorf("TLS handshake failed: %v", err)
		}
		conn = sconn
		// verify keyprint if it's set in the URL
		if exp := keyprint.FromURL(u); exp != "" {
			if kps, err = tlskp.VerifyKeyPrint(sconn, exp); err != nil {
				_ = sconn.Close()
				return nil, err
			}
		} else {
			kps = tlskp.GetKeyPrints(sconn)
		}
	}
	c, err := NewConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.kps = kps
	return c, nil
}

// NewConn runs an NMDC protocol over a specified connection.
func NewConn(conn net.Conn) (*Conn, error) {
	c := &Conn{
		conn: conn,
	}
	c.w = nmdc.NewWriterSize(conn, writeBuffer)
	c.r = nmdc.NewReader(conn)
	c.r.OnUnknownEncoding = c.onUnknownEncoding
	if DefaultFallbackEncoding != nil {
		c.SetFallbackEncoding(DefaultFallbackEncoding)
	}
	c.r.OnRawMessage(func(cmd, args []byte) (bool, error) {
		if bytes.Equal(cmd, []byte("ZOn")) {
			err := c.r.EnableZlib()
			return false, err
		}
		return true, nil
	})
	if Debug {
		c.w.OnLine(func(line []byte) (bool, error) {
			log.Printf("-> %q", string(line))
			return true, nil
		})
		c.r.OnLine(func(line []byte) (bool, error) {
			log.Printf("<- %q", string(line))
			return true, nil
		})
	}
	return c, nil
}

// Conn is a NMDC protocol connection.
type Conn struct {
	kps []string // keyprints, set by TLS

	fallback encoding.Encoding

	conn net.Conn

	wmu    sync.Mutex
	w      *nmdc.Writer
	closed bool

	rmu sync.Mutex
	r   *nmdc.Reader
}

// GetKeyPrints returns keyprints set by TLS, if any.
func (c *Conn) GetKeyPrints() []string {
	return c.kps
}

func (c *Conn) OnUnmarshalError(fnc func(line []byte, err error) (bool, error)) {
	c.r.OnUnmarshalError = fnc
}

func (c *Conn) OnLineR(fnc func(line []byte) (bool, error)) {
	c.r.OnLine(fnc)
}

func (c *Conn) OnLineW(fnc func(line []byte) (bool, error)) {
	c.w.OnLine(fnc)
}

func (c *Conn) OnRawMessageR(fnc func(cmd, data []byte) (bool, error)) {
	c.r.OnRawMessage(fnc)
}

func (c *Conn) OnMessageR(fnc func(m nmdc.Message) (bool, error)) {
	c.r.OnMessage(fnc)
}

func (c *Conn) OnMessageW(fnc func(m nmdc.Message) (bool, error)) {
	c.w.OnMessage(fnc)
}

func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

func (c *Conn) SetWriteTimeout(dt time.Duration) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if dt <= 0 {
		c.w.Timeout = nil
		return
	}
	c.w.Timeout = func(enable bool) error {
		if enable {
			return c.conn.SetWriteDeadline(time.Now().Add(dt))
		}
		return c.conn.SetWriteDeadline(time.Time{})
	}
}

func (c *Conn) FallbackEncoding() encoding.Encoding {
	return c.fallback
}

func (c *Conn) TextEncoder() *encoding.Encoder {
	return c.w.Encoder()
}

func (c *Conn) TextDecoder() *encoding.Decoder {
	return c.r.Decoder()
}

func (c *Conn) setEncoding(enc encoding.Encoding, event bool) {
	if enc != nil {
		e := enc.NewEncoder()
		e = encoding.HTMLEscapeUnsupported(e)
		c.w.SetEncoder(e)
		if !event {
			c.r.SetDecoder(enc.NewDecoder())
		}
	} else {
		c.w.SetEncoder(nil)
		if !event {
			c.r.SetDecoder(nil)
		}
	}
}

func (c *Conn) SetEncoding(enc encoding.Encoding) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.setEncoding(enc, false)
}

func (c *Conn) SetFallbackEncoding(enc encoding.Encoding) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.fallback = enc
}

func (c *Conn) ZOn(lvl int) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.w.ZOnLevel(lvl)
}

// Close closes the connection.
func (c *Conn) Close() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	// should not hold any other mutex
	var last error
	// first close the writer so it flushes all buffers
	if err := c.w.Close(); err != nil {
		last = err
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	// then close the connection so it unblocks the reader
	_ = c.conn.Close()
	// finally close the reader
	if err := c.r.Close(); err != nil {
		last = err
	}
	return last
}

func (c *Conn) WriteMsg(m ...nmdc.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.w.WriteMsg(m...)
}

func (c *Conn) WriteLine(data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.w.WriteLine(data)
}

func (c *Conn) Flush() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.w.Flush()
}

func (c *Conn) WriteOneMsg(m nmdc.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.w.WriteMsg(m); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Conn) WriteOneLine(data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.w.WriteLine(data); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Conn) onUnknownEncoding(text []byte) (*encoding.Decoder, error) {
	fallback := c.FallbackEncoding()
	if fallback == nil {
		return nil, nil
	}
	// try fallback encoding
	dec := fallback.NewDecoder()
	str, err := dec.String(string(text))
	if err != nil || !utf8.ValidString(str) {
		return nil, nil // use current decoder
	}
	// fallback is valid - switch encoding
	if Debug {
		log.Println(c.RemoteAddr(), "switched to a fallback encoding")
	}
	c.setEncoding(fallback, true)
	return dec, nil
}

func (c *Conn) ReadMsgTo(deadline time.Time, m nmdc.Message) error {
	if m == nil {
		panic("nil message to decode")
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if !deadline.IsZero() {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	return c.r.ReadMsgTo(m)
}

func (c *Conn) ReadMsgToAny(deadline time.Time, m ...nmdc.Message) (nmdc.Message, error) {
	if len(m) == 0 {
		panic("no messages to decode")
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if !deadline.IsZero() {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	return c.r.ReadMsgToAny(m...)
}

func (c *Conn) ReadMsg(deadline time.Time) (nmdc.Message, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if !deadline.IsZero() {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	return c.r.ReadMsg()
}
