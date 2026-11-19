package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"proxy-ns/buildconfig"
	"proxy-ns/config"

	"golang.org/x/sys/unix"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] [command [argument ...]]
Force any program to use your socks5 proxy server.

Options:
  -q                         Quiet mode
  -c config                  Specify config file to use (Default: %s)

These options override settings in config file:
  --userns=<BOOL>                              Use a user namespace (default: true)
  --sudo-drop=<BOOL>                            Restore the original sudo user (default: true)
  --tun-name=<TUN_NAME>                        Set tun device name
  --tun-ip=<TUN_IP>                            Set tun device IPv4 address
  --tun-ip6=<TUN_IP6>                          Set tun device IPv6 address (optional)
  --socks5-address=<SOCKS5_ADDRESS>            Use the specified proxy
  --username=<SOCKS5_USER>                     Username of the specified proxy (optional)
  --password=<SOCKS5_PASS>                     Password of the specified proxy (optional)
  --fake-dns=<BOOL>                            Enable/Disable fake DNS
  --fake-network=<NETWORK>                     Set network used for fake DNS
  --dns-server=<DNS_SERVER>                    Set DNS server(only available when fake DNS is disabled)
  --udp-session-timeout=<UDP_SESSION_TIMEOUT>  Set UDP session timeout (optional) (Default: %s)
`, os.Args[0], buildconfig.ConfigPath, config.UDPSessionTimeout)
}

func isFlagPresent(name string) (present bool) {
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			present = true
		}
	})
	return
}

func main() {
	quietMode := flag.Bool("q", false, "")
	cfgPath := flag.String("c", buildconfig.ConfigPath, "")
	tunName := flag.String("tun-name", "", "")
	tunIp := flag.String("tun-ip", "", "")
	tunIp6 := flag.String("tun-ip6", "", "")
	socks5Address := flag.String("socks5-address", "", "")
	username := flag.String("username", "", "")
	password := flag.String("password", "", "")
	// See https://pkg.go.dev/flag
	// Boolean flags are not permitted to be written in the form
	// like "--fake-dns false"
	fakeDns := flag.String("fake-dns", "true", "")
	fakeNetwork := flag.String("fake-network", "", "")
	dnsServer := flag.String("dns-server", "", "")
	udpSessionTimeout := flag.Duration("udp-session-timeout", config.UDPSessionTimeout, "")
	userNS := flag.String("userns", "true", "")
	sudoDrop := flag.String("sudo-drop", "true", "")
	namespaceChild := flag.Bool("namespace-child", false, "")
	flag.CommandLine.Usage = usage
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("proxy-ns: ")

	if *quietMode {
		devNull, err := os.Open("/dev/null")
		if err != nil {
			os.Exit(1)
		}
		log.SetOutput(devNull)
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	if *namespaceChild {
		setup, err := parseNetSetup(*tunName, *tunIp, *tunIp6, *fakeDns, *dnsServer, *userNS, *sudoDrop)
		if err == nil {
			err = runNamespaceChild(setup, args)
		}
		if err != nil {
			log.Println(err)
			os.Exit(1)
		}
		return
	}

	var (
		cfg *config.Config
		err error
	)
	cfg, err = config.FromFile(*cfgPath)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	var data config.Data
	if isFlagPresent("tun-name") {
		data.TunName = tunName
	}
	if isFlagPresent("tun-ip") {
		data.TunIP = tunIp
	}
	if isFlagPresent("tun-ip6") {
		data.TunIP6 = tunIp6
	}
	if isFlagPresent("socks5-address") {
		data.Socks5Address = socks5Address
	}
	if isFlagPresent("username") {
		data.Username = username
	}
	if isFlagPresent("password") {
		data.Password = password
	}

	if isFlagPresent("fake-network") {
		data.FakeNetwork = fakeNetwork
	}
	if isFlagPresent("dns-server") {
		data.DNSServer = dnsServer
	}
	if isFlagPresent("udp-session-timeout") {
		s := udpSessionTimeout.String()
		data.UDPSessionTimeout = &s
	}

	if isFlagPresent("fake-dns") {
		fakeDnsBool, err := strconv.ParseBool(*fakeDns)
		if err != nil {
			usage()
			os.Exit(1)
		}
		data.FakeDNS = &fakeDnsBool
	}
	if isFlagPresent("userns") {
		userNSBool, err := strconv.ParseBool(*userNS)
		if err != nil {
			usage()
			os.Exit(1)
		}
		data.UserNS = &userNSBool
	}
	if isFlagPresent("sudo-drop") {
		sudoDropBool, err := strconv.ParseBool(*sudoDrop)
		if err != nil {
			usage()
			os.Exit(1)
		}
		data.SudoDrop = &sudoDropBool
	}

	err = cfg.Update(data)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	status, err := runMain(cfg, args, *quietMode)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	os.Exit(status)
}

func dropCapabilities() error {
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := &[2]unix.CapUserData{}
	if _, _, e1 := syscall.AllThreadsSyscall6(unix.SYS_CAPSET, uintptr(unsafe.Pointer(hdr)), uintptr(unsafe.Pointer(data)), 0, 0, 0, 0); e1 != 0 {
		return fmt.Errorf("Failed to capset: %s", e1)
	}

	return nil
}

func dropPrivileges() error {
	err := dropCapabilities()
	if err != nil {
		return fmt.Errorf("Failed to drop capabilities: %w", err)
	}
	if _, _, e1 := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); e1 != 0 {
		return fmt.Errorf("Failed to prctl PR_SET_NO_NEW_PRIVS: %s", e1)
	}
	return nil
}
