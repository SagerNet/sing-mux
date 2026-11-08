package mux

import (
	"net"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

type protocolConn struct {
	net.Conn
	request        Request
	requestWritten bool
}

func newProtocolConn(conn net.Conn, request Request) net.Conn {
	writer, isVectorised := bufio.CreateVectorisedWriter(conn)
	if isVectorised {
		return &vectorisedProtocolConn{
			protocolConn{
				Conn:    conn,
				request: request,
			},
			writer,
		}
	} else {
		return &protocolConn{
			Conn:    conn,
			request: request,
		}
	}
}

func (c *protocolConn) NeedHandshake() bool {
	return !c.requestWritten
}

func (c *protocolConn) Write(p []byte) (n int, err error) {
	if c.requestWritten {
		return c.Conn.Write(p)
	}
	buffer := EncodeRequest(c.request, p)
	_, err = c.Conn.Write(buffer.Bytes())
	buffer.Release()
	c.requestWritten = true
	if err != nil {
		return
	}
	return len(p), nil
}

func (c *protocolConn) Upstream() any {
	return c.Conn
}

type vectorisedProtocolConn struct {
	protocolConn
	writer N.VectorisedWriter
}

func (c *vectorisedProtocolConn) WriteVectorised(buffers []*buf.Buffer) error {
	if c.requestWritten {
		return c.writer.WriteVectorised(buffers)
	}
	c.requestWritten = true
	buffer := EncodeRequest(c.request, nil)
	return c.writer.WriteVectorised(append([]*buf.Buffer{buffer}, buffers...))
}
