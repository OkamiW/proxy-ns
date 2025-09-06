proxy-ns(1)

# NAME

proxy-ns - Force any program to use your SOCKS5 proxy server

# SYNOPSIS

*proxy-ns* [_OPTIONS_] <_command_> [_COMMAND OPTIONS_]

# OPTIONS

## General options

*-c*
	Path to configuration file, see *proxy-ns*(5) for details.

*-q*
	Quiet mode. By default, proxy-ns prints errors to standard error, this option makes proxy-ns redirects standard error to /dev/null at startup.

*-h*, *--help*
	Show help message.

## Overriding options
These options override settings in the configuration file:

*--tun-name*=_TUN_NAME_
	Set TUN device name.

*--tun-ip*=_TUN_IP_
	Set TUN device IPv4 address.

*--tun-ip6*=_TUN_IP6_
	Set TUN device IPv6 address.

	Setting this option enables IPv6 default route in proxy-ns network namespace.
	Only set this option if your SOCKS5 server has IPv6 connectivity.

*--socks5-address*=_SOCKS5_ADDRESS_
	Set the SOCKS5 server to use.

*--username*=_USERNAME_
	Set the username of the specified SOCKS5 server.

*--password*=_PASSWORD_
	Set the password of the specified SOCKS5 server.

*--fake-dns*=_BOOL_
	Enable or disable fake DNS. See *NOTES ON FAKEDNS* for more details.

*--fake-network*=_FAKE_NETWORK_
	Set the network used for fake DNS.

	If *fake-dns* is enabled, and programs in proxy-ns network namespace makes a DNS request, an IP address from *fake-network* will be returned as response. The relationship between the domain name in request and the IP address returned will be saved, further accesses to the IP address will be recognized as accesses to the saved DNS name.

*--dns-server*=_DNS_SERVER_
	Set DNS server.

	DNS server used to resolve domain names. When *fake-dns* is enabled, it's only used for DNS requests *fake-dns* cannot handle: DNS message types other than A and AAAA.

*--udp-session-timeout*=_UDP_SESSION_TIMEOUT_
	Set UDP session timeout.

# NOTES ON CAPABILITIES
- *cap_sys_admin* is for *setns* system call.

- *cap_net_admin* is for creating TUN device.

- *cap_net_bind_service* is for the FakeDNS server listening on *127.0.0.1:53*.

- *cap_sys_chroot* is for *setns* into a new mount namespace.

- *cap_chown* is for *chown 0:0 /etc/resolv.conf*.

All capabilities are dropped permanently before the program's main loop.

# NOTES ON FAKEDNS
Advantages of FakeDNS:
- Avoid an extra roundtrip for DNS.

- Resolve DNS even when SOCKS5 server doesn't support UDP ASSOCIATE.

Disadvantages of FakeDNS:
- All domain names are resolved to *fake_network*, some programs may not
  work as expected(e.g. *dig*, *geoiplookup*).

- Many SOCKS5 servers doesn't support *AtypDomainName* for UDP ASSOCIATE.
  If you enable FakeDNS, all UDP packets to domain names will be dropped.
  (e.g. DNS may still work, but QUIC probably won't.)

Caveats:
- Some programs may not use your system DNS resolver. FakeDNS won't
  work for them.

# NOTES ON FORKING PROGRAMS
proxy-ns daemon program exits as the command exits, so if you start a forking program in proxy-ns, it will lose network connection.

If you start the forking program in shell, you can work around this problem by executing the shell in proxy-ns beforehand:

	*exec proxy-ns $SHELL*

# SEE ALSO

*proxy-ns(5)*
