package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"proxy-ns/proxy/transport/socks5"
	"strconv"
)

type socks5Dialer struct {
	network string
	address string
	auth    *socks5.Auth
}

func SOCKS5(network, address string, auth *socks5.Auth) Dialer {
	return &socks5Dialer{
		network: network,
		address: address,
		auth:    auth,
	}
}

func (d *socks5Dialer) Dial(network, address string) (net.Conn, error) {
	addr, err := d.serializeAddr(address)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize address: %s", address)
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
		return d.dialTCP(addr)
	case "udp", "udp4", "udp6":
		return d.dialUDP(addr)
	default:
		return nil, fmt.Errorf("network not implemented: %s", network)
	}
}

func (d *socks5Dialer) dialTCP(target socks5.Addr) (net.Conn, error) {
	conn, err := net.Dial(d.network, d.address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", d.address, err)
	}

	_, err = socks5.ClientHandshake(conn, target, socks5.CmdConnect, d.auth)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to perform client handshake: %w", err)
	}
	return conn, nil
}

func (d *socks5Dialer) dialUDP(target socks5.Addr) (net.Conn, error) {
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
	udpConn, err := net.Dial("udp", addr.String())
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to dial bound address: %s: %w", addr, err)
	}
	go func() {
		io.Copy(io.Discard, conn)
		conn.Close()
		// A UDP association terminates when the TCP connection that the UDP
		// ASSOCIATE request arrived on terminates. RFC1928
		udpConn.Close()
	}()

	boundAddr := addr.UDPAddr()
	if boundAddr == nil {
		return nil, fmt.Errorf("invalid UDP binding address: %#v", addr)
	}

	if boundAddr.IP.IsUnspecified() { /* e.g. "0.0.0.0" or "::" */
		udpAddr, err := net.ResolveUDPAddr("udp", d.address)
		if err != nil {
			return nil, fmt.Errorf("resolve udp address %s: %w", d.address, err)
		}
		boundAddr.IP = udpAddr.IP
	}
	return &socks5UDPConn{Conn: udpConn, tcpConn: conn, targetAddr: target}, nil
}

func (d *socks5Dialer) serializeAddr(address string) (socks5.Addr, error) {
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

type socks5UDPConn struct {
	net.Conn
	tcpConn    net.Conn
	targetAddr socks5.Addr
}

func (c *socks5UDPConn) Read(b []byte) (n int, err error) {
	var buf = make([]byte, 65535)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return
	}
	addr, payload, err := socks5.DecodeUDPPacket(buf[:n])
	if err != nil {
		return
	}

	udpAddr := addr.UDPAddr()
	if udpAddr == nil {
		return 0, fmt.Errorf("convert %s to UDPAddr is nil", addr)
	}
	if udpAddr.String() != c.targetAddr.String() {
		return 0, fmt.Errorf("expected remote address: %s, not %s", c.targetAddr, udpAddr)
	}
	copy(b, payload)
	return n - len(addr) - 3, nil
}

func (c *socks5UDPConn) Write(b []byte) (n int, err error) {
	packet, err := socks5.EncodeUDPPacket(c.targetAddr, b)
	if err != nil {
		return
	}
	n, err = c.Conn.Write(packet)
	if err != nil {
		return
	}
	if n < len(packet) {
		return 0, fmt.Errorf("short write: expected %d, not %d", len(packet), n)
	}
	return len(b), nil
}

func (c *socks5UDPConn) Close() error {
	c.tcpConn.Close()
	return c.Conn.Close()
}
