proxy-ns(5)

# NAME

proxy-ns - configuration file for *proxy-ns*(1)

# DESCRIPTION

*proxy-ns* uses JSON format for configuration. You can find the location of configuration file by running *proxy-ns --help*.

*proxy-ns* ships with sensible configuration by default. In most cases, you only need to change *socks5_address*.

# CONFIGURATION
*tun_name* (required)
	Set TUN device name. (e.g. tun0)

*tun_ip* (required)
	Set TUN device IPv4 address. (e.g. 10.0.0.1/24)

*tun_ip6* (optional)
	Set TUN device IPv6 address. (e.g. fc00::1/7)

	Setting this option enables IPv6 default route in proxy-ns network namespace. Only set this option if your SOCKS5 server has IPv6 connectivity.

*socks5_address* (required)
	Set SOCKS5 server address. (e.g. 127.0.0.1:1080)

*username* (optional)
	Set the username of the specified SOCKS5 server.

*password* (optional)
	Set the password of the specified SOCKS5 server.

*fake_dns* (required)
	Enable or disable fake DNS. See *NOTES ON FAKEDNS* for more details.

*fake_network* (required)
	Set the network used for fake DNS. (e.g. 240.0.0.0/4)

	If *fake-dns* is enabled, and programs in proxy-ns network namespace makes a DNS request, an IP address from *fake-network* will be returned as response. The relationship between the domain name in request and the IP address returned will be saved, further accesses to the IP address will be recognized as accesses to the saved DNS name.

*dns_server* (required)
	Set DNS server. (e.g. 9.9.9.9)

	DNS server used to resolve domain names. When *fake-dns* is enabled, it's only used for DNS requests *fake-dns* cannot handle: DNS message types other than A and AAAA.

*udp_session_timeout* (optional)
	Set UDP session timeout. (e.g. 1m0s)

# NOTES ON FAKEDNS
Advantages of FakeDNS:
- Avoid an extra roundtrip for DNS.

- Resolve DNS even when SOCKS5 server doesn't support UDP ASSOCIATE.

Disadvantages of FakeDNS:
- All domain names are resolved to *fake_network*, some programs may not work as expected(e.g. *dig*, *geoiplookup*).

- Many SOCKS5 servers doesn't support *AtypDomainName* for UDP ASSOCIATE. If you enable FakeDNS, all UDP packets to domain names will be dropped. (e.g. DNS may still work, but QUIC probably won't.)

Caveats:
- Some programs may not use your system DNS resolver. FakeDNS won't work for them.

# SEE ALSO
*proxy-ns*(1)
