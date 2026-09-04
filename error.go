package mux

import (
	"io"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
)

type wrapStream struct {
	net.Conn
	closeOnce sync.Once
	onClose   func()
}

func (w *wrapStream) Close() error {
	err := w.Conn.Close()
	if w.onClose != nil {
		w.closeOnce.Do(w.onClose)
	}
	return err
}

func (w *wrapStream) Read(p []byte) (n int, err error) {
	n, err = w.Conn.Read(p)
	err = wrapError(err)
	return
}

func (w *wrapStream) Write(p []byte) (n int, err error) {
	n, err = w.Conn.Write(p)
	err = wrapError(err)
	return
}

func (w *wrapStream) Upstream() any {
	return w.Conn
}

type wrapStreamCloseWrite struct {
	*wrapStream
}

func newWrapStreamCloseWrite(conn net.Conn) *wrapStreamCloseWrite {
	return &wrapStreamCloseWrite{wrapStream: &wrapStream{Conn: conn}}
}

func (w *wrapStreamCloseWrite) CloseWrite() error {
	if c, ok := w.Conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return nil
}

func wrapError(err error) error {
	switch err {
	case yamux.ErrStreamClosed:
		return io.EOF
	default:
		return err
	}
}
