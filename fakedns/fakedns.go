package fakedns

import (
	"encoding/binary"
	"net"
	"sync"

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

		mapping:         make(map[string]uint32),
		reversedMapping: make(map[uint32]string),
	}
	ones, bits := fakeNetwork.Mask.Size()
	zeros := bits - ones
	size := uint32((1 << zeros) - 1)
	s.min = binary.BigEndian.Uint32(fakeNetwork.IP)
	s.max = s.min - 1 + size
	s.next = s.min - 1
	return s
}

// Fake A and AAAA records, forward other records
type Server struct {
	dialer         proxy.Dialer
	packetConn     net.PacketConn
	upstreamServer string
	fakeNetwork    *net.IPNet

	next uint32
	min  uint32
	max  uint32

	mutex           sync.Mutex
	mapping         map[string]uint32
	reversedMapping map[uint32]string
}

func (s *Server) Contains(ip net.IP) bool {
	return s.fakeNetwork.Contains(ip)
}

func (s *Server) NameFromIP(ip net.IP) (name string) {
	ipUint := binary.BigEndian.Uint32(ip)

	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.reversedMapping[ipUint]
}

func (s *Server) reset() {
	s.next = s.min - 1
	s.mapping = make(map[string]uint32)
	s.reversedMapping = make(map[uint32]string)
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
	case dns.TypeA:
		var (
			ip   net.IP
			next uint32
		)
		domain := question.Name
		if dns.IsFqdn(domain) {
			domain = domain[:len(domain)-1]
		}

		s.mutex.Lock()
		next, ok := s.mapping[domain]
		if !ok {
			s.next += 1
			if s.next > s.max {
				s.reset()
				s.next += 1
			}
			next = s.next
			s.mapping[domain] = next
			s.reversedMapping[next] = domain
		}
		s.mutex.Unlock()

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
	case dns.TypeAAAA:
		// empty response for AAAA questions
		break
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
