package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"proxy-ns/network"
	"proxy-ns/proxy/transport/socks5"
)

type SOCKS5Client struct {
	network string
	address string
	auth    *socks5.Auth
}

func SOCKS5(network, address, username, password string) *SOCKS5Client {
	if username != "" && password != "" {
		return &SOCKS5Client{
			network: network,
			address: address,
			auth: &socks5.Auth{
				Username: username,
				Password: password,
			},
		}
	}
	return &SOCKS5Client{
		network: network,
		address: address,
		auth:    nil,
	}
}

func (d *SOCKS5Client) Dial(network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return d.Connect(address)
	case "udp", "udp4", "udp6":
		relay, err := d.UDPAssociate()
		if err != nil {
			return nil, fmt.Errorf("failed to request udp associate: %w", err)
		}
		return relay.Dial(address)
	default:
		return nil, fmt.Errorf("network not implemented: %s", network)
	}
}

func (d *SOCKS5Client) Connect(address string) (net.Conn, error) {
	addr, err := serializeAddr(address)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize address: %w", err)
	}
	conn, err := net.Dial(d.network, d.address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", d.address, err)
	}

	_, err = socks5.ClientHandshake(conn, addr, socks5.CmdConnect, d.auth)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to perform client handshake: %w", err)
	}
	return conn, nil
}

func (d *SOCKS5Client) UDPAssociate() (*SOCKS5UDPRelayClient, error) {
	conn, err := net.Dial(d.network, d.address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", d.address, err)
	}

	// The UDP ASSOCIATE request is used to establish an association within
	// the UDP relay process to handle UDP datagrams.  The DST.ADDR and
	// DST.PORT fields contain the address and port that the client expects
	// to use to send UDP datagrams on for the association.  The server MAY
	// use this information to limit access to the association.  If the
	// client is not in possession of the information at the time of the UDP
	// ASSOCIATE, the client MUST use a port number and address of all
	// zeros. RFC1928
	var targetAddr socks5.Addr = []byte{socks5.AtypIPv4, 0, 0, 0, 0, 0, 0}

	addr, err := socks5.ClientHandshake(conn, targetAddr, socks5.CmdUDPAssociate, d.auth)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to perform client handshake: %w", err)
	}

	relayAddr := addr.UDPAddr()
	if relayAddr == nil {
		return nil, fmt.Errorf("invalid UDP binding address: %#v", addr)
	}

	if relayAddr.IP.IsUnspecified() { /* e.g. "0.0.0.0" or "::" */
		udpAddr, err := net.ResolveUDPAddr("udp", d.address)
		if err != nil {
			return nil, fmt.Errorf("resolve udp address %s: %w", d.address, err)
		}
		relayAddr.IP = udpAddr.IP
	}
	return NewSOCKS5UDPRelayClient(conn, relayAddr)
}

func serializeAddr(address string) (socks5.Addr, error) {
	host, port, err := splitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return socks5.SerializeAddr(host, nil, port), nil
	}
	return socks5.SerializeAddr("", ip, port), nil
}

func splitHostPort(address string) (string, uint16, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	portnum, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, err
	}
	if 1 > portnum || portnum > 0xffff {
		return "", 0, errors.New("port number out of range " + port)
	}
	return host, uint16(portnum), nil
}

type SOCKS5UDPRelayClient struct {
	tcpConn   net.Conn
	relayAddr net.Addr
	pc        *muxedPacketConn
	count     atomic.Int64
	finalizer func()
}

func NewSOCKS5UDPRelayClient(tcpConn net.Conn, relayAddr net.Addr) (*SOCKS5UDPRelayClient, error) {
	pc, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	return &SOCKS5UDPRelayClient{
		tcpConn:   tcpConn,
		relayAddr: relayAddr,
		pc:        newMuxedPacketConn(pc),
	}, nil
}

func (r *SOCKS5UDPRelayClient) Dial(address string) (net.Conn, error) {
	addr, err := serializeAddr(address)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize address: %w", err)
	}
	r.Add(1)
	return &socks5UDPConn{
		muxedPacketConn: r.pc,
		relayClient:     r,
		relayAddr:       r.relayAddr,
		targetAddr:      addr,
	}, nil
}

func (r *SOCKS5UDPRelayClient) Add(delta int64) {
	if r.count.Add(delta) == 0 {
		r.Close()
	}
}

func (r *SOCKS5UDPRelayClient) SetFinalizer(f func()) {
	r.finalizer = f
}

func (r *SOCKS5UDPRelayClient) Close() error {
	if r.finalizer != nil {
		r.finalizer()
	}

	r.tcpConn.Close()
	return r.pc.Close()
}

func newMuxedPacketConn(pc net.PacketConn) *muxedPacketConn {
	return &muxedPacketConn{PacketConn: pc}
}

type muxedPacketConn struct {
	net.PacketConn
	once    sync.Once
	packets sync.Map // map[endpoint]chan []byte
}

type endpoint struct {
	relayAddr  string
	targetAddr string
}

func (c *muxedPacketConn) poll() {
	buf := make([]byte, network.MaxPacketSize)
	for {
		n, addr, err := c.PacketConn.ReadFrom(buf)
		if err != nil {
			return
		}
		target, payload, err := socks5.DecodeUDPPacket(buf[:n])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		ep := endpoint{
			relayAddr:  addr.String(),
			targetAddr: target.String(),
		}
		value, ok := c.packets.Load(ep)
		if ok {
			value.(chan []byte) <- slices.Clone(payload)
		} else {
			fmt.Fprintln(os.Stderr, "Dropped unrelated packet from:", addr, target)
		}
	}
}

func (c *muxedPacketConn) ReadFrom(p []byte, relayAddr net.Addr, target socks5.Addr) (n int, err error) {
	ep := endpoint{
		relayAddr:  relayAddr.String(),
		targetAddr: target.String(),
	}
	actual, _ := c.packets.LoadOrStore(ep, make(chan []byte))
	go c.once.Do(c.poll)
	select {
	case buf := <-actual.(chan []byte):
		return copy(p, buf), nil
	case <-time.After(network.UDPSessionTimeout):
		return 0, context.DeadlineExceeded
	}
}

func (c *muxedPacketConn) WriteTo(p []byte, relayAddr net.Addr, target socks5.Addr) (n int, err error) {
	data, err := socks5.EncodeUDPPacket(target, p)
	if err != nil {
		return 0, err
	}
	n, err = c.PacketConn.WriteTo(data, relayAddr)
	if err != nil {
		return 0, err
	}
	if n < len(data) {
		return 0, fmt.Errorf("short write: expected %d, not %d", len(data), n)
	}
	return len(p), nil
}

type socks5UDPConn struct {
	*muxedPacketConn
	relayClient *SOCKS5UDPRelayClient
	relayAddr   net.Addr
	targetAddr  socks5.Addr
}

func (c *socks5UDPConn) Read(p []byte) (n int, err error) {
	return c.muxedPacketConn.ReadFrom(p, c.relayAddr, c.targetAddr)
}

func (c *socks5UDPConn) Write(p []byte) (n int, err error) {
	return c.muxedPacketConn.WriteTo(p, c.relayAddr, c.targetAddr)
}

func (c *socks5UDPConn) RemoteAddr() net.Addr {
	return c.targetAddr.UDPAddr()
}

func (c *socks5UDPConn) Close() error {
	c.relayClient.Add(-1)
	return nil
}
