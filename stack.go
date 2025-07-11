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

	"proxy-ns/config"
	"proxy-ns/fakedns"
	"proxy-ns/network"
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

func manageTun(mtu uint32, fd int, socks5Client *proxy.SOCKS5Client, fakeDNSServer *fakedns.Server) (err error) {
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

	tcpForwarder := tcp.NewForwarder(s, 0, 2<<10, func(r *tcp.ForwarderRequest) {
		remoteAddrStr := addrFromID(r.ID())
		if remoteAddrStr == "" {
			r.Complete(true)
			return
		}

		remoteConn, err := socks5Client.Connect(remoteAddrStr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			r.Complete(true)
			return
		}

		var wq waiter.Queue
		ep, e := r.CreateEndpoint(&wq)
		if e != nil {
			remoteConn.Close()
			r.Complete(true)
			return
		}

		originConn := gonet.NewTCPConn(&wq, ep)

		r.Complete(false)
		go forwardConn(originConn, remoteConn, io.Copy)
	})
	type endpoint struct {
		address tcpip.Address
		port    uint16
	}
	var relays sync.Map
	relayFromID := func(id stack.TransportEndpointID) *proxy.SOCKS5UDPRelayClient {
		ep := endpoint{
			address: id.RemoteAddress,
			port:    id.RemotePort,
		}
		onceValue := sync.OnceValue(func() *proxy.SOCKS5UDPRelayClient {
			relay, err := socks5Client.UDPAssociate()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return nil
			}
			relay.SetFinalizer(func() {
				relays.Delete(ep)
			})
			return relay
		})
		actual, _ := relays.LoadOrStore(ep, onceValue)
		relay := actual.(func() *proxy.SOCKS5UDPRelayClient)()
		if relay == nil {
			relays.Delete(ep)
			return nil
		}
		return relay
	}
	udpForwarder := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		var wq waiter.Queue
		ep, e := r.CreateEndpoint(&wq)
		if e != nil {
			return
		}

		originConn := gonet.NewUDPConn(&wq, ep)

		id := r.ID()
		remoteAddrStr := addrFromID(id)
		if remoteAddrStr == "" {
			return
		}
		go func() {
			relay := relayFromID(id)
			if relay == nil {
				return
			}
			remoteConn, err := relay.Dial(remoteAddrStr)
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

type copyFunc func(io.Writer, io.Reader) (int64, error)

// TODO: handle tcp closeWrite/closeRead
func forwardConn(c1, c2 net.Conn, copyFn copyFunc) error {
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

	if e1 == nil && e2 == nil {
		return nil
	}
	if e1 != nil && e2 == nil {
		return e1
	}
	if e1 == nil && e2 != nil {
		return e2
	}
	if e1 != nil && e2 != nil {
		return fmt.Errorf("%w, %w", e1, e2)
	}
	return nil
}

var errInvalidWrite = errors.New("invalid write result")

func copyPacketData(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := make([]byte, network.MaxPacketSize)
	for {
		src.(net.Conn).SetReadDeadline(time.Now().Add(config.UDPSessionTimeout))
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
		dst.(net.Conn).SetReadDeadline(time.Now().Add(config.UDPSessionTimeout))
	}
	return written, err
}
