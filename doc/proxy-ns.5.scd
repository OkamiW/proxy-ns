proxy-ns(5)

# NAME

proxy-ns - configuration file for *proxy-ns*(1)

# DESCRIPTION

*proxy-ns* uses JSON format for configuration.

The configuration file is located at _$sysconfdir/proxy-ns/config.json_

The exact location can be found by running *proxy-ns --help*.

*proxy-ns* ships with sensible configuration by default.
In most cases, you only need to change *socks5_address*.

# CONFIGURATION
*tun_name* (required)
	Set TUN device name. (e.g. tun0)

*tun_ip* (required)
	Set TUN device IPv4 address. (e.g. 10.0.0.1/24)

*tun_ip6* (optional)
	Set TUN device IPv6 address. (e.g. fc00::1/7)

	*proxy-ns* only adds IPv6 route if this is specified.

*socks5_address* (required)
	Set SOCKS5 server address. (e.g. 127.0.0.1:1080)

*username* (optional)
	Set SOCKS5 server username.

*password* (optional)
	Set SOCKS5 server password.

*fake_dns* (required)
	Enable or disable fake DNS. (e.g. true)

*fake_network* (required)
	Set the network used for fake DNS. (e.g. 240.0.0.0/4)

*dns_server* (required)
	Set DNS server. (e.g. 9.9.9.9)

*udp_session_timeout* (optional)
	Set UDP session timeout. (e.g. 1m0s)

# SEE ALSO
*proxy-ns*(1)
