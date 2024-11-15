package mux

import (
	"encoding/binary"
	"io"
	"net"
	"sync"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ N.NetPacketConn    = (*clientPacketConn)(nil)
	_ N.PacketReadWaiter = (*clientPacketConn)(nil)
)

type clientPacketConn struct {
	N.AbstractConn
	conn            N.ExtendedConn
	access          sync.Mutex
	destination     M.Socksaddr
	readWaitOptions N.ReadWaitOptions
}

func (c *clientPacketConn) Read(b []byte) (n int, err error) {
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	if cap(b) < int(length) {
		return 0, io.ErrShortBuffer
	}
	return io.ReadFull(c.conn, b[:length])
}

func (c *clientPacketConn) Write(b []byte) (n int, err error) {
	err = binary.Write(c.conn, binary.BigEndian, uint16(len(b)))
	if err != nil {
		return
	}
	return c.conn.Write(b)
}

func (c *clientPacketConn) ReadBuffer(buffer *buf.Buffer) (err error) {
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	_, err = buffer.ReadFullFrom(c.conn, int(length))
	return
}

func (c *clientPacketConn) WriteBuffer(buffer *buf.Buffer) error {
	bLen := buffer.Len()
	binary.BigEndian.PutUint16(buffer.ExtendHeader(2), uint16(bLen))
	return c.conn.WriteBuffer(buffer)
}

func (c *clientPacketConn) FrontHeadroom() int {
	return 2
}

func (c *clientPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	if cap(p) < int(length) {
		return 0, nil, io.ErrShortBuffer
	}
	n, err = io.ReadFull(c.conn, p[:length])
	return
}

func (c *clientPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	err = binary.Write(c.conn, binary.BigEndian, uint16(len(p)))
	if err != nil {
		return
	}
	return c.conn.Write(p)
}

func (c *clientPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	err = c.ReadBuffer(buffer)
	return
}

func (c *clientPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	return c.WriteBuffer(buffer)
}

func (c *clientPacketConn) InitializeReadWaiter(options N.ReadWaitOptions) (needCopy bool) {
	c.readWaitOptions = options
	return false
}

func (c *clientPacketConn) WaitReadPacket() (buffer *buf.Buffer, destination M.Socksaddr, err error) {
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	buffer = c.readWaitOptions.NewPacketBuffer()
	_, err = buffer.ReadFullFrom(c.conn, int(length))
	if err != nil {
		buffer.Release()
		return nil, M.Socksaddr{}, err
	}
	c.readWaitOptions.PostReturn(buffer)
	return
}

func (c *clientPacketConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *clientPacketConn) RemoteAddr() net.Addr {
	return c.destination.UDPAddr()
}

func (c *clientPacketConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *clientPacketConn) Upstream() any {
	return c.conn
}

var (
	_ N.NetPacketConn    = (*clientPacketAddrConn)(nil)
	_ N.PacketReadWaiter = (*clientPacketAddrConn)(nil)
)

type clientPacketAddrConn struct {
	N.AbstractConn
	conn            N.ExtendedConn
	access          sync.Mutex
	destination     M.Socksaddr
	readWaitOptions N.ReadWaitOptions
}

func (c *clientPacketAddrConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	destination, err := M.SocksaddrSerializer.ReadAddrPort(c.conn)
	if err != nil {
		return
	}
	if destination.IsFqdn() {
		addr = destination
	} else {
		addr = destination.UDPAddr()
	}
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	if cap(p) < int(length) {
		return 0, nil, io.ErrShortBuffer
	}
	n, err = io.ReadFull(c.conn, p[:length])
	return
}

func (c *clientPacketAddrConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	err = M.SocksaddrSerializer.WriteAddrPort(c.conn, M.SocksaddrFromNet(addr))
	if err != nil {
		return
	}
	err = binary.Write(c.conn, binary.BigEndian, uint16(len(p)))
	if err != nil {
		return
	}
	return c.conn.Write(p)
}

func (c *clientPacketAddrConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	destination, err = M.SocksaddrSerializer.ReadAddrPort(c.conn)
	if err != nil {
		return
	}
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	_, err = buffer.ReadFullFrom(c.conn, int(length))
	return
}

func (c *clientPacketAddrConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	bLen := buffer.Len()
	header := buf.With(buffer.ExtendHeader(M.SocksaddrSerializer.AddrPortLen(destination) + 2))
	err := M.SocksaddrSerializer.WriteAddrPort(header, destination)
	if err != nil {
		return err
	}
	common.Must(binary.Write(header, binary.BigEndian, uint16(bLen)))
	return c.conn.WriteBuffer(buffer)
}

func (c *clientPacketAddrConn) InitializeReadWaiter(options N.ReadWaitOptions) (needCopy bool) {
	c.readWaitOptions = options
	return false
}

func (c *clientPacketAddrConn) WaitReadPacket() (buffer *buf.Buffer, destination M.Socksaddr, err error) {
	destination, err = M.SocksaddrSerializer.ReadAddrPort(c.conn)
	if err != nil {
		return
	}
	var length uint16
	err = binary.Read(c.conn, binary.BigEndian, &length)
	if err != nil {
		return
	}
	buffer = c.readWaitOptions.NewPacketBuffer()
	_, err = buffer.ReadFullFrom(c.conn, int(length))
	if err != nil {
		buffer.Release()
		return nil, M.Socksaddr{}, err
	}
	c.readWaitOptions.PostReturn(buffer)
	return
}

func (c *clientPacketAddrConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *clientPacketAddrConn) FrontHeadroom() int {
	return 2 + M.MaxSocksaddrLength
}

func (c *clientPacketAddrConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *clientPacketAddrConn) Upstream() any {
	return c.conn
}
