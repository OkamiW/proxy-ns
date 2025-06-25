package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"proxy-ns/fakedns"
	"proxy-ns/proxy"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	// defaultWndSize if set to zero, the default
	// receive window buffer size is used instead.
	defaultWndSize = 0

	// maxConnAttempts specifies the maximum number
	// of in-flight tcp connection attempts.
	maxConnAttempts = 2 << 10

	udpSessionTimeout = time.Minute
	maxPacketSize     = (1 << 16) - 1
)

func manageTun(mtu uint32, fd int, dialer proxy.Dialer, fakeDNSServer *fakedns.Server) (err error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	linkEP, err := fdbased.New(&fdbased.Options{
		FDs: []int{fd},
		MTU: mtu,
	})
	if err != nil {
		return
	}

	nicID := s.NextNICID()
	if err := s.CreateNIC(nicID, linkEP); err != nil {
		return errors.New(err.String())
	}

	e := s.SetPromiscuousMode(nicID, true)
	if e != nil {
		return errors.New(e.String())
	}

	e = s.SetSpoofing(nicID, true)
	if e != nil {
		return errors.New(e.String())
	}

	s.SetRouteTable([]tcpip.Route{
		{
			Destination: header.IPv4EmptySubnet,
			NIC:         nicID,
		},
		{
			Destination: header.IPv6EmptySubnet,
			NIC:         nicID,
		},
	})

	var addrFromID func(stack.TransportEndpointID) string
	if fakeDNSServer != nil {
		addrFromID = func(id stack.TransportEndpointID) (result string) {
			remoteAddr := id.LocalAddress
			remotePort := id.LocalPort
			if fakeDNSServer.Contains(remoteAddr.AsSlice()) {
				name := fakeDNSServer.NameFromIP(remoteAddr.AsSlice())
				if name == "" {
					return ""
				}
				result = net.JoinHostPort(name, strconv.Itoa(int(remotePort)))
			} else {
				result = net.JoinHostPort(remoteAddr.String(), strconv.Itoa(int(remotePort)))
			}
			return result
		}
	} else {
		addrFromID = func(id stack.TransportEndpointID) (result string) {
			remoteAddr := id.LocalAddress
			remotePort := id.LocalPort
			return net.JoinHostPort(remoteAddr.String(), strconv.Itoa(int(remotePort)))
		}
	}

	tcpForwarder := tcp.NewForwarder(s, defaultWndSize, maxConnAttempts, func(r *tcp.ForwarderRequest) {
		remoteAddrStr := addrFromID(r.ID())
		if remoteAddrStr == "" {
			r.Complete(true)
			return
		}

		remoteConn, err := dialer.Dial("tcp", remoteAddrStr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			r.Complete(true)
			return
		}

		var wq waiter.Queue
		ep, e := r.CreateEndpoint(&wq)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			r.Complete(true)
			return
		}

		originConn := gonet.NewTCPConn(&wq, ep)

		r.Complete(false)
		go forwardConn(originConn, remoteConn, io.Copy)
	})
	udpForwarder := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		var wq waiter.Queue
		ep, e := r.CreateEndpoint(&wq)
		if e != nil {
			return
		}

		originConn := gonet.NewUDPConn(&wq, ep)

		remoteAddrStr := addrFromID(r.ID())
		if remoteAddrStr == "" {
			return
		}

		go func() {
			remoteConn, err := dialer.Dial("udp", remoteAddrStr)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			forwardConn(originConn, remoteConn, copyPacketData)
		}()
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	return nil
}

// TODO: handle tcp closeWrite/closeRead
func forwardConn(c1, c2 net.Conn, copyFn func(io.Writer, io.Reader) (int64, error)) error {
	var (
		wg     sync.WaitGroup
		e1, e2 error
	)

	wg.Add(2)
	go func() {
		_, e1 = copyFn(c1, c2)
		c1.Close()
		c2.Close()
		wg.Done()
	}()
	go func() {
		_, e2 = copyFn(c2, c1)
		c1.Close()
		c2.Close()
		wg.Done()
	}()
	wg.Wait()

	if e1 != nil || e2 != nil {
		return fmt.Errorf("%w, %w", e1, e2)
	}
	return nil
}

var errInvalidWrite = errors.New("invalid write result")

func copyPacketData(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := make([]byte, maxPacketSize)
	for {
		src.(net.Conn).SetReadDeadline(time.Now().Add(udpSessionTimeout))
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
		dst.(net.Conn).SetReadDeadline(time.Now().Add(udpSessionTimeout))
	}
	return written, err
}
