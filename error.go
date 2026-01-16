package mux

import (
	"io"
	"net"

	"github.com/hashicorp/yamux"
	N "github.com/sagernet/sing/common/network"
)

type wrapStream struct {
	net.Conn
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

func (w *wrapStream) CloseWrite() error {
	return N.CloseWrite(w.Conn)
}

func (w *wrapStream) Upstream() any {
	return w.Conn
}

func wrapError(err error) error {
	switch err {
	case yamux.ErrStreamClosed:
		return io.EOF
	default:
		return err
	}
}
