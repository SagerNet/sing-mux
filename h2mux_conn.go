package mux

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/baderror"
	M "github.com/sagernet/sing/common/metadata"
)

type httpConn struct {
	reader io.Reader
	writer io.Writer
	create chan struct{}
	err    error
	cancel context.CancelFunc
	access sync.Mutex
	closed bool
}

func newHTTPConn(reader io.Reader, writer io.Writer) *httpConn {
	return &httpConn{
		reader: reader,
		writer: writer,
	}
}

func newLateHTTPConn(writer io.Writer, cancel context.CancelFunc) *httpConn {
	return &httpConn{
		create: make(chan struct{}),
		writer: writer,
		cancel: cancel,
	}
}

func (c *httpConn) setup(reader io.Reader, err error) {
	c.access.Lock()
	closed := c.closed
	if closed {
		c.err = net.ErrClosed
	} else {
		c.reader = reader
		c.err = err
	}
	close(c.create)
	c.access.Unlock()
	if closed {
		common.Close(reader)
	}
}

func (c *httpConn) Read(b []byte) (n int, err error) {
	if c.create != nil {
		<-c.create
		if c.err != nil {
			return 0, baderror.WrapH2(c.err)
		}
	}
	n, err = c.reader.Read(b)
	return n, baderror.WrapH2(err)
}

func (c *httpConn) Write(b []byte) (n int, err error) {
	n, err = c.writer.Write(b)
	return n, baderror.WrapH2(err)
}

func (c *httpConn) Close() error {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return net.ErrClosed
	}
	c.closed = true
	reader := c.reader
	c.access.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	return common.Close(reader, c.writer)
}

func (c *httpConn) CloseWrite() error {
	return common.Close(c.writer)
}

func (c *httpConn) LocalAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *httpConn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *httpConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *httpConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *httpConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *httpConn) NeedAdditionalReadDeadline() bool {
	return true
}
