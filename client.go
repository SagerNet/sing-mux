package mux

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/yamux"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

type Client struct {
	dialer         N.Dialer
	logger         logger.Logger
	protocol       byte
	maxConnections int
	minStreams     int
	maxStreams     int
	padding        bool
	access         sync.Mutex
	connections    list.List[*clientSession]
	brutal         BrutalOptions
	closeIdle      atomic.Bool
}

type clientSession struct {
	abstractSession
	element *list.Element[*clientSession]
	streams int
	ready   chan struct{}
	err     error
	cancel  context.CancelFunc
}

func (s *clientSession) IsClosed() bool {
	return s.abstractSession != nil && s.abstractSession.IsClosed()
}

func (s *clientSession) CanTakeNewRequest() bool {
	return s.abstractSession == nil || s.abstractSession.CanTakeNewRequest()
}

func (s *clientSession) Close() error {
	if s.abstractSession == nil {
		s.cancel()
		return nil
	}
	return s.abstractSession.Close()
}

type keepSessionKey struct{}

func ContextWithKeepSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, (*keepSessionKey)(nil), true)
}

func keepSessionFromContext(ctx context.Context) bool {
	keep, _ := ctx.Value((*keepSessionKey)(nil)).(bool)
	return keep
}

type Options struct {
	Dialer         N.Dialer
	Logger         logger.Logger
	Protocol       string
	MaxConnections int
	MinStreams     int
	MaxStreams     int
	Padding        bool
	Brutal         BrutalOptions
}

type BrutalOptions struct {
	Enabled    bool
	SendBPS    uint64
	ReceiveBPS uint64
}

func NewClient(options Options) (*Client, error) {
	client := &Client{
		dialer:         options.Dialer,
		logger:         options.Logger,
		maxConnections: options.MaxConnections,
		minStreams:     options.MinStreams,
		maxStreams:     options.MaxStreams,
		padding:        options.Padding,
		brutal:         options.Brutal,
	}
	if client.dialer == nil {
		client.dialer = N.SystemDialer
	}
	if client.maxStreams == 0 && client.maxConnections == 0 {
		client.minStreams = 8
	}
	switch options.Protocol {
	case "", "h2mux":
		client.protocol = ProtocolH2Mux
	case "smux":
		client.protocol = ProtocolSmux
	case "yamux":
		client.protocol = ProtocolYAMux
	default:
		return nil, E.New("unknown protocol: " + options.Protocol)
	}
	return client, nil
}

func (c *Client) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		stream, err := c.openStream(ctx)
		if err != nil {
			return nil, err
		}
		conn := &clientConn{Conn: stream, destination: destination}
		if c.protocol == ProtocolH2Mux || c.protocol == ProtocolYAMux {
			return &clientConnWithCloseWrite{clientConn: conn}, nil
		}
		return conn, nil
	case N.NetworkUDP:
		stream, err := c.openStream(ctx)
		if err != nil {
			return nil, err
		}
		extendedConn := bufio.NewExtendedConn(stream)
		return &clientPacketConn{AbstractConn: extendedConn, conn: extendedConn, destination: destination}, nil
	default:
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
}

func (c *Client) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	stream, err := c.openStream(ctx)
	if err != nil {
		return nil, err
	}
	extendedConn := bufio.NewExtendedConn(stream)
	return &clientPacketAddrConn{AbstractConn: extendedConn, conn: extendedConn, destination: destination}, nil
}

func (c *Client) openStream(ctx context.Context) (net.Conn, error) {
	var (
		session *clientSession
		stream  net.Conn
		err     error
	)
	for attempts := 0; attempts < 2; attempts++ {
		err = ctx.Err()
		if err != nil {
			return nil, err
		}
		session, err = c.offer(ctx)
		if err != nil {
			continue
		}
		stream, err = session.Open()
		if err != nil {
			c.releaseStream(session, false)
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	keepSession := keepSessionFromContext(ctx)
	onClose := func() {
		c.releaseStream(session, keepSession)
	}
	if yamuxStream, ok := stream.(*yamux.Stream); ok {
		return &yamuxWrapStream{Stream: yamuxStream, onClose: onClose}, nil
	}
	if c.protocol == ProtocolH2Mux {
		wrapped := newWrapStreamCloseWrite(stream)
		wrapped.onClose = onClose
		return wrapped, nil
	}
	return &wrapStream{Conn: stream, onClose: onClose}, nil
}

func (c *Client) offer(ctx context.Context) (*clientSession, error) {
	c.access.Lock()
	var sessions []*clientSession
	for element := c.connections.Front(); element != nil; {
		if element.Value.IsClosed() {
			element.Value.Close()
			element.Value.element = nil
			nextElement := element.Next()
			c.connections.Remove(element)
			element = nextElement
			continue
		}
		sessions = append(sessions, element.Value)
		element = element.Next()
	}
	session := c.selectSession(sessions)
	if session == nil {
		session = c.offerNew(ctx)
	}
	session.streams++
	c.access.Unlock()
	select {
	case <-session.ready:
	case <-ctx.Done():
		c.releaseStream(session, keepSessionFromContext(ctx))
		return nil, ctx.Err()
	}
	if session.err != nil {
		return nil, session.err
	}
	return session, nil
}

func (c *Client) selectSession(sessions []*clientSession) *clientSession {
	if c.brutal.Enabled {
		if len(sessions) > 0 {
			return sessions[0]
		}
		return nil
	}
	session := common.MinBy(common.Filter(sessions, func(it *clientSession) bool {
		return it.CanTakeNewRequest()
	}), func(it *clientSession) int {
		return it.streams
	})
	if session == nil {
		return nil
	}
	if session.streams == 0 {
		return session
	}
	if c.maxConnections > 0 {
		if len(sessions) >= c.maxConnections || session.streams < c.minStreams {
			return session
		}
	} else {
		if c.maxStreams > 0 && session.streams < c.maxStreams {
			return session
		}
	}
	return nil
}

func (c *Client) releaseStream(session *clientSession, keepSession bool) {
	c.access.Lock()
	session.streams--
	if session.streams > 0 || keepSession || !c.closeIdle.Load() {
		c.access.Unlock()
		return
	}
	if session.element != nil {
		c.connections.Remove(session.element)
		session.element = nil
	}
	c.access.Unlock()
	session.Close()
}

func (c *Client) offerNew(ctx context.Context) *clientSession {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TCPTimeout)
	session := &clientSession{ready: make(chan struct{}), cancel: cancel}
	session.element = c.connections.PushBack(session)
	go func() {
		muxSession, err := c.dialSession(ctx)
		cancel()
		c.access.Lock()
		if session.element == nil {
			if err == nil {
				muxSession.Close()
			}
			session.err = net.ErrClosed
		} else if err != nil {
			c.connections.Remove(session.element)
			session.element = nil
			session.err = err
		} else {
			session.abstractSession = muxSession
			session.cancel = nil
		}
		close(session.ready)
		c.access.Unlock()
	}()
	return session
}

func (c *Client) dialSession(ctx context.Context) (abstractSession, error) {
	conn, err := c.dialer.DialContext(ctx, N.NetworkTCP, Destination)
	if err != nil {
		return nil, err
	}
	var version byte
	if c.padding {
		version = Version1
	} else {
		version = Version0
	}
	conn = newProtocolConn(conn, Request{
		Version:  version,
		Protocol: c.protocol,
		Padding:  c.padding,
	})
	if c.padding {
		conn = newPaddingConn(conn)
	}
	session, err := newClientSession(conn, c.protocol)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if c.brutal.Enabled {
		stopClose := context.AfterFunc(ctx, func() {
			conn.Close()
		})
		err = c.brutalExchange(conn, session)
		if !stopClose() {
			err = ctx.Err()
		}
		if err != nil {
			conn.Close()
			session.Close()
			return nil, E.Cause(err, "brutal exchange")
		}
	}
	return session, nil
}

func (c *Client) brutalExchange(sessionConn net.Conn, session abstractSession) error {
	stream, err := session.Open()
	if err != nil {
		return err
	}
	conn := &clientConn{Conn: &wrapStream{Conn: stream}, destination: M.Socksaddr{Fqdn: BrutalExchangeDomain}}
	err = WriteBrutalRequest(conn, c.brutal.ReceiveBPS)
	if err != nil {
		return err
	}
	serverReceiveBPS, err := ReadBrutalResponse(conn)
	if err != nil {
		return err
	}
	conn.Close()
	sendBPS := c.brutal.SendBPS
	if serverReceiveBPS < sendBPS {
		sendBPS = serverReceiveBPS
	}
	clientBrutalErr := SetBrutalOptions(sessionConn, sendBPS)
	if clientBrutalErr != nil {
		c.logger.Debug(E.Cause(clientBrutalErr, "failed to enable TCP Brutal at client"))
	}
	return nil
}

func (c *Client) Reset() {
	c.access.Lock()
	defer c.access.Unlock()
	for _, session := range c.connections.Array() {
		session.element = nil
		session.Close()
	}
	c.connections.Init()
}

func (c *Client) SetKeepIdleConnections(keep bool) {
	c.closeIdle.Store(!keep)
	if !keep {
		c.CloseIdleConnections()
	}
}

func (c *Client) CloseIdleConnections() {
	c.access.Lock()
	var closing []*clientSession
	for element := c.connections.Front(); element != nil; {
		nextElement := element.Next()
		session := element.Value
		if session.streams == 0 {
			c.connections.Remove(element)
			session.element = nil
			closing = append(closing, session)
		}
		element = nextElement
	}
	c.access.Unlock()
	for _, session := range closing {
		session.Close()
	}
}

func (c *Client) Close() error {
	c.Reset()
	return nil
}
