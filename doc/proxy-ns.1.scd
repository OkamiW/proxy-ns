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
	Quiet mode.

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

*--socks5-address*=_SOCKS5_ADDRESS_
	Set the SOCKS5 server to use.

*--username*=_USERNAME_
	Set the username of the specified SOCKS5 server.

*--password*=_PASSWORD_
	Set the password of the specified SOCKS5 server.

*--fake-dns*=_BOOL_
	Enable or disable fake DNS.

*--fake-network*=_FAKE_NETWORK_
	Set the network used for fake DNS.

*--dns-server*=_DNS_SERVER_
	Set DNS server.

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
FakeDNS can be used to avoid a separate DNS roundtrip, thus improving
latency. But it has its own limitations:

- All domains are resolved to *fake_network*, some programs may not
  work as expected(e.g. *dig*, *geoiplookup*).

- Some programs may not use your system DNS resolver. FakeDNS won't
  work for them.

- Many SOCKS5 servers doesn't support *AtypDomainName* for UDP ASSOCIATE.
  If you enable FakeDNS, all UDP packets to domains will be dropped.
  (e.g. DNS may still work, but QUIC probably won't.)

# NOTES ON FORKING PROGRAMS
proxy-ns daemon program exits as the command exits, so if you start a forking program in proxy-ns, it will lose network connection.

If you start the forking program in shell, you can work around this problem by executing the shell in proxy-ns beforehand:

	*exec proxy-ns $SHELL*

# SEE ALSO

*proxy-ns(5)*
