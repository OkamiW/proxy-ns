package fakedns

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"

	"proxy-ns/proxy"

	"github.com/miekg/dns"
)

const maxTtl = 10

func NewServer(packetConn net.PacketConn, dialer proxy.Dialer, upstreamServer string, fakeNetwork *net.IPNet) *Server {
	s := &Server{
		packetConn:     packetConn,
		dialer:         dialer,
		upstreamServer: upstreamServer,
		fakeNetwork:    fakeNetwork,
	}
	ones, bits := fakeNetwork.Mask.Size()
	zeros := bits - ones
	size := uint32((1 << zeros) - 1)
	s.min = binary.BigEndian.Uint32(fakeNetwork.IP)
	s.max = s.min - 1 + size
	s.next.Store(s.min - 1)
	return s
}

// Fake A and AAAA records, forward other records
type Server struct {
	dialer         proxy.Dialer
	packetConn     net.PacketConn
	upstreamServer string
	fakeNetwork    *net.IPNet

	next atomic.Uint32
	min  uint32
	max  uint32

	mapping         sync.Map
	reversedMapping sync.Map
}

func (s *Server) Contains(ip net.IP) bool {
	return s.fakeNetwork.Contains(ip)
}

func (s *Server) NameFromIP(ip net.IP) (name string) {
	ipUint := binary.BigEndian.Uint32(ip)
	value, ok := s.reversedMapping.Load(ipUint)
	if !ok {
		return ""
	}
	return value.(string)
}

func (s *Server) reset() {
	s.next.Store(s.min - 1)
	s.mapping = sync.Map{}
}

func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg).SetReply(r)

	if len(r.Question) != 1 {
		w.WriteMsg(m)
		return
	}

	question := r.Question[0]

	if question.Qclass != dns.ClassINET {
		w.WriteMsg(m)
		return
	}

	switch question.Qtype {
	case dns.TypeAAAA:
	case dns.TypeA:
		var (
			ip   net.IP
			next uint32
		)
		domain := question.Name
		if dns.IsFqdn(domain) {
			domain = domain[:len(domain)-1]
		}
		actual, loaded := s.mapping.LoadOrStore(domain, uint32(0))
		if !loaded {
			for {
				next = s.next.Add(1)
				if next > s.max {
					s.reset()
					continue
				}
				break
			}
			s.mapping.Store(domain, next)
			s.reversedMapping.Store(next, domain)
		} else {
			next = actual.(uint32)
		}

		m.Answer = []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    maxTtl,
				},
				A: binary.BigEndian.AppendUint32(ip, next),
			},
		}
	default:
		conn, err := s.dialer.Dial("udp", s.upstreamServer)
		if err != nil {
			w.WriteMsg(m)
			return
		}
		defer conn.Close()
		em, err := ExchangeConn(conn, r)
		if err != nil {
			w.WriteMsg(m)
			return
		}
		m = em
	}
	w.WriteMsg(m)
}

func (s *Server) Run() error {
	server := dns.Server{
		PacketConn: s.packetConn,
		Handler:    s,
	}
	return server.ActivateAndServe()
}

func ExchangeConn(c net.Conn, m *dns.Msg) (r *dns.Msg, err error) {
	co := new(dns.Conn)
	co.Conn = c
	if err = co.WriteMsg(m); err != nil {
		return nil, err
	}
	r, err = co.ReadMsg()
	if err == nil && r.Id != m.Id {
		err = dns.ErrId
	}
	return r, err
}
