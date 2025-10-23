package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"unsafe"

	"proxy-ns/buildconfig"
	"proxy-ns/config"
	"proxy-ns/fakedns"
	"proxy-ns/proxy"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/rawfile"
	"gvisor.dev/gvisor/pkg/tcpip/link/tun"
)

type Data struct {
	TunMTU uint32
	Config *config.Config
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] [command [argument ...]]
Force any program to use your socks5 proxy server.

Options:
  -q                         Quiet mode
  -c config                  Specify config file to use (Default: %s)

These options override settings in config file:
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
	daemon := flag.Bool("daemon", false, "")
	flag.CommandLine.Usage = usage
	flag.Parse()

	if *quietMode {
		devNull, err := os.Open("/dev/null")
		if err != nil {
			os.Exit(1)
		}
		err = unix.Dup2(int(devNull.Fd()), 2)
		if err != nil {
			os.Exit(1)
		}
	}

	if *daemon {
		if err := runDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	var (
		cfg *config.Config
		err error
	)
	cfg, err = config.FromFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	if isFlagPresent("fake-dns") {
		fakeDnsBool, err := strconv.ParseBool(*fakeDns)
		if err != nil {
			usage()
			os.Exit(1)
		}
		data.FakeDNS = &fakeDnsBool
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
	err = cfg.Update(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := runMain(cfg, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func dropCapabilities() error {
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := &unix.CapUserData{}
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

func runDaemon() error {
	err := dropPrivileges()
	if err != nil {
		return fmt.Errorf("Failed to drop privileges: %w", err)
	}

	pipeFd, tunFd, pidFd, packetConnFd := 3, 4, 5, 6

	pipeFile := os.NewFile(uintptr(pipeFd), "")

	var data Data
	err = gob.NewDecoder(pipeFile).Decode(&data)
	if err != nil {
		return fmt.Errorf("Failed to communicate with parent process: %w", err)
	}
	pipeFile.Close()
	tunMTU := data.TunMTU
	cfg := data.Config

	config.UDPSessionTimeout = cfg.UDPSessionTimeout

	var fakeDNSServer *fakedns.Server

	socks5Client := proxy.SOCKS5("tcp", cfg.Socks5Address, cfg.Username, cfg.Password)

	if cfg.FakeDNS {
		packetConn, err := net.FilePacketConn(os.NewFile(uintptr(packetConnFd), ""))
		if err != nil {
			return fmt.Errorf("Failed to get PacketConn: %w", err)
		}
		fakeDNSServer = fakedns.NewServer(packetConn, socks5Client, net.JoinHostPort(cfg.DNSServer, "53"), cfg.FakeNetwork)
		go func() {
			err := fakeDNSServer.Run()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start FakeDNS server: %s\n", err)
			}
		}()
	}

	err = manageTun(tunMTU, tunFd, socks5Client, fakeDNSServer)
	if err != nil {
		return fmt.Errorf("Failed to manage TUN: %w", err)
	}

	for {
		_, err = unix.Poll([]unix.PollFd{
			{
				Fd:     int32(pidFd),
				Events: unix.POLLIN,
			},
		}, -1)
		switch err {
		case unix.EINTR:
			continue
		case nil:
			return nil
		}
	}
}

func getNs(nstype string) (int, error) {
	return unix.Open(fmt.Sprintf("/proc/%d/task/%d/ns/%s", os.Getpid(), unix.Gettid(), nstype), unix.O_RDONLY|unix.O_CLOEXEC, 0)
}

func runMain(cfg *config.Config, args []string) error {
	var (
		daemonArgs []string

		originMntNs, originNetNs int
		newMntNs, newNetNs       int

		loLink, tunLink netlink.Link

		dnsServer string

		packetConn     net.PacketConn
		packetConnFile *os.File

		tempFile, nullFile *os.File

		wd string

		tunFd, pidFd int
		tunMTU       uint32

		r, w *os.File

		execName, progName string

		err error
	)
	runtime.LockOSThread()
	originMntNs, err = getNs("mnt")
	if err != nil {
		return fmt.Errorf("Failed to get origin mount namespace: %w", err)
	}
	originNetNs, err = getNs("net")
	if err != nil {
		return fmt.Errorf("Failed to get origin network namespace: %w", err)
	}

	err = unix.Unshare(unix.CLONE_NEWNS)
	if err != nil {
		return fmt.Errorf("Failed to unshare mount namespace: %w", err)
	}
	newMntNs, err = getNs("mnt")
	if err != nil {
		return fmt.Errorf("Failed to get new mount namespace: %w", err)
	}
	err = unix.Mount("none", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
	if err != nil {
		return fmt.Errorf("Failed to mount root as private: %w", err)
	}

	err = unix.Mount("tmpfs", os.TempDir(), "tmpfs", 0, "")
	if err != nil {
		return fmt.Errorf("Failed to mount tmpfs: %w", err)
	}

	tempFile, err = os.CreateTemp("", "resolv.conf.*")
	if err != nil {
		return fmt.Errorf("Failed to create resolv.conf: %w", err)
	}

	dnsServer = cfg.DNSServer
	if cfg.FakeDNS {
		dnsServer = "127.0.0.1"
	}
	_, err = fmt.Fprintf(tempFile, "nameserver %s\n", dnsServer)
	if err != nil {
		return fmt.Errorf("Failed to write to resolv.conf: %w", err)
	}
	err = tempFile.Close()
	if err != nil {
		return fmt.Errorf("Failed to close resolv.conf: %w", err)
	}
	err = os.Chmod(tempFile.Name(), 0o644)
	if err != nil {
		return fmt.Errorf("Failed to chmod resolv.conf: %w", err)
	}
	err = os.Chown(tempFile.Name(), 0, 0)
	if err != nil {
		return fmt.Errorf("Failed to chown resolv.conf: %w", err)
	}

	err = unix.Mount(tempFile.Name(), "/etc/resolv.conf", "", unix.MS_BIND, "")
	if err != nil {
		return fmt.Errorf("Failed to mount bind resolv.conf: %w", err)
	}

	err = unix.Unmount(os.TempDir(), 0)
	if err != nil {
		return fmt.Errorf("Failed to unmount tmpfs: %w", err)
	}

	err = unix.Unshare(unix.CLONE_NEWNET)
	if err != nil {
		return fmt.Errorf("Failed to unshare network namespace: %w", err)
	}
	newNetNs, err = getNs("net")
	if err != nil {
		return fmt.Errorf("Failed to get new network namespace: %w", err)
	}
	loLink, err = netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("Failed to get loopback link: %w", err)
	}
	err = netlink.LinkSetUp(loLink)
	if err != nil {
		return fmt.Errorf("Failed to bring up loopback link: %w", err)
	}

	if cfg.FakeDNS {
		packetConn, err = net.ListenPacket("udp", net.JoinHostPort(dnsServer, "53"))
		if err != nil {
			return fmt.Errorf("DNS server failed to listen: %w", err)
		}
		packetConnFile, err = packetConn.(*net.UDPConn).File()
		if err != nil {
			return fmt.Errorf("Failed to get DNS server listener fd: %w", err)
		}
	}

	err = netlink.LinkAdd(&netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name: cfg.TunName,
		},
		Mode: netlink.TUNTAP_MODE_TUN,
	})
	if err != nil {
		return fmt.Errorf("Failed to create TUN link: %w", err)
	}
	tunLink, err = netlink.LinkByName(cfg.TunName)
	if err != nil {
		return fmt.Errorf("Failed to get TUN link: %w", err)
	}
	err = netlink.LinkSetUp(tunLink)
	if err != nil {
		return fmt.Errorf("Failed to bring up TUN link: %w", err)
	}
	err = netlink.AddrAdd(tunLink, &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   cfg.TunIP,
			Mask: cfg.TunMask,
		},
	})
	if err != nil {
		return fmt.Errorf("Failed to add IPv4 address for TUN link: %w", err)
	}
	if len(cfg.TunIP6) != 0 && len(cfg.TunMask6) != 0 {
		err = netlink.AddrAdd(tunLink, &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   cfg.TunIP6,
				Mask: cfg.TunMask6,
			},
		})
		if err != nil {
			return fmt.Errorf("Failed to add IPv6 address for TUN link: %w", err)
		}
	}
	err = netlink.RouteAdd(&netlink.Route{
		Dst: &net.IPNet{
			IP:   net.IPv4zero,
			Mask: net.CIDRMask(0, 32),
		},
		LinkIndex: tunLink.Attrs().Index,
	})
	if err != nil {
		return fmt.Errorf("Failed to add IPv4 default route to TUN link: %w", err)
	}
	if len(cfg.TunIP6) != 0 && len(cfg.TunMask6) != 0 {
		err = netlink.RouteAdd(&netlink.Route{
			Dst: &net.IPNet{
				IP:   net.IPv6zero,
				Mask: net.CIDRMask(0, 128),
			},
			LinkIndex: tunLink.Attrs().Index,
		})
		if err != nil {
			return fmt.Errorf("Failed to add IPv6 default route to TUN link: %w", err)
		}
	}

	tunMTU, err = rawfile.GetMTU(cfg.TunName)
	if err != nil {
		return fmt.Errorf("Failed to get TUN link MTU: %w", err)
	}

	tunFd, err = tun.Open(cfg.TunName)
	if err != nil {
		return fmt.Errorf("Failed to open TUN link: %w", err)
	}
	unix.CloseOnExec(tunFd)

	pidFd, err = unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return fmt.Errorf("Failed to get pidfd: %w", err)
	}

	wd, err = os.Getwd()
	if err != nil {
		return fmt.Errorf("Failed to get current working directory: %w", err)
	}

	err = unix.Setns(originNetNs, unix.CLONE_NEWNET)
	if err != nil {
		return fmt.Errorf("Failed to enter origin network namespace: %w", err)
	}
	err = unix.Setns(originMntNs, unix.CLONE_NEWNS)
	if err != nil {
		return fmt.Errorf("Failed to enter origin mount namespace: %w", err)
	}

	execName, err = os.Executable()
	if err != nil {
		return fmt.Errorf("Failed to get executable path: %w", err)
	}
	nullFile, err = os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("Failed to open /dev/null: %w", err)
	}
	if !cfg.FakeDNS {
		packetConnFile = nullFile
	}
	r, w, err = os.Pipe()
	if err != nil {
		return fmt.Errorf("Failed to open pipe: %w", err)
	}
	daemonArgs = slices.Insert(slices.Clone(os.Args), 1, "--daemon")
	_, err = os.StartProcess(execName, daemonArgs, &os.ProcAttr{
		Dir: "/",
		Files: []*os.File{
			nullFile, nullFile, os.Stderr, r,
			os.NewFile(uintptr(tunFd), ""),
			os.NewFile(uintptr(pidFd), ""),
			packetConnFile,
		},
		Sys: &syscall.SysProcAttr{
			Setsid: true,
		},
	})
	if err != nil {
		return fmt.Errorf("Failed to start daemon process: %w", err)
	}
	err = gob.NewEncoder(w).Encode(&Data{
		TunMTU: tunMTU,
		Config: cfg,
	})
	if err != nil {
		return fmt.Errorf("Failed to communicate with daemon process: %w", err)
	}
	err = w.Close()
	if err != nil {
		return fmt.Errorf("Failed to close write end of the pipe: %w", err)
	}

	err = unix.Setns(newNetNs, unix.CLONE_NEWNET)
	if err != nil {
		return fmt.Errorf("Failed to enter new network namespace: %w", err)
	}
	err = unix.Setns(newMntNs, unix.CLONE_NEWNS)
	if err != nil {
		return fmt.Errorf("Failed to enter new mount namespace: %w", err)
	}

	// Switching mount namespace using setns(2) would change working directory to /
	err = os.Chdir(wd)
	if err != nil {
		return fmt.Errorf("Failed to chdir to origin working directory: %w", err)
	}

	progName, err = exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("Failed to search executable: %w", err)
	}

	err = dropCapabilities()
	if err != nil {
		return fmt.Errorf("Failed to drop capabilities: %w", err)
	}

	return unix.Exec(progName, args, os.Environ())
}
