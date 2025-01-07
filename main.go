package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"proxy-ns/config"
	"proxy-ns/fakedns"
	"proxy-ns/proxy"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/rawfile"
	"gvisor.dev/gvisor/pkg/tcpip/link/tun"
)

var (
	SysConfDir = "/etc"
	ConfigPath = filepath.Join(SysConfDir, "proxy-ns/config.json")
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
  --tun-name=<TUN_NAME>              Set tun device name
  --tun-ip=<TUN_IP>                  Set tun device ip
  --socks5-address=<SOCKS5_ADDRESS>  Use the specified proxy
  --fake-dns=<BOOL>                  Enable/Disable fake DNS
  --fake-network=<NETWORK>           Set network used for fake DNS
  --dns-server=<DNS_SERVER>          Set DNS server(only available when fake DNS is disabled)
`, os.Args[0], ConfigPath)
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
	cfgPath := flag.String("c", ConfigPath, "")
	tunName := flag.String("tun-name", "", "")
	tunIp := flag.String("tun-ip", "", "")
	socks5Address := flag.String("socks5-address", "", "")
	fakeDns := flag.Bool("fake-dns", true, "")
	fakeNetwork := flag.String("fake-network", "", "")
	dnsServer := flag.String("dns-server", "", "")
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
	if isFlagPresent("socks5-address") {
		data.Socks5Address = socks5Address
	}
	if isFlagPresent("fake-dns") {
		data.FakeDNS = fakeDns
	}
	if isFlagPresent("fake-network") {
		data.FakeNetwork = fakeNetwork
	}
	if isFlagPresent("dns-server") {
		data.DNSServer = dnsServer
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

func dropPrivilege() error {
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := &unix.CapUserData{}
	if _, _, e1 := syscall.AllThreadsSyscall6(unix.SYS_CAPSET, uintptr(unsafe.Pointer(hdr)), uintptr(unsafe.Pointer(data)), 0, 0, 0, 0); e1 != 0 {
		return fmt.Errorf("Failed to capset: %s", e1)
	}

	if _, _, e1 := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); e1 != 0 {
		return fmt.Errorf("Failed to prctl PR_SET_NO_NEW_PRIVS: %s", e1)
	}
	return nil
}

func runDaemon() error {
	err := dropPrivilege()
	if err != nil {
		return err
	}

	pipeFd, tunFd, pidFd, packetConnFd := 3, 4, 5, 6

	pipeFile := os.NewFile(uintptr(pipeFd), "")

	var data Data
	err = gob.NewDecoder(pipeFile).Decode(&data)
	if err != nil {
		return err
	}
	pipeFile.Close()
	tunMTU := data.TunMTU
	cfg := data.Config

	var fakeDNSServer *fakedns.Server

	if cfg.FakeDNS {
		packetConn, err := net.FilePacketConn(os.NewFile(uintptr(packetConnFd), ""))
		if err != nil {
			return err
		}
		fakeDNSServer = fakedns.NewServer(packetConn, net.JoinHostPort(cfg.DNSServer, "53"), cfg.FakeNetwork)
		go func() {
			err := fakeDNSServer.Run()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}

	err = manageTun(tunMTU, tunFd, proxy.SOCKS5("tcp", cfg.Socks5Address, nil), fakeDNSServer)
	if err != nil {
		return err
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
		return err
	}
	originNetNs, err = getNs("net")
	if err != nil {
		return err
	}

	err = unix.Unshare(unix.CLONE_NEWNS)
	if err != nil {
		return err
	}
	newMntNs, err = getNs("mnt")
	if err != nil {
		return err
	}
	err = unix.Mount("none", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
	if err != nil {
		return err
	}

	err = unix.Mount("tmpfs", os.TempDir(), "tmpfs", 0, "")
	if err != nil {
		return err
	}

	tempFile, err = os.CreateTemp("", "resolv.conf.*")
	if err != nil {
		return err
	}

	dnsServer = cfg.DNSServer
	if cfg.FakeDNS {
		dnsServer = "127.0.0.1"
	}
	_, err = tempFile.WriteString(fmt.Sprintf("nameserver %s\n", dnsServer))
	if err != nil {
		return err
	}
	err = tempFile.Close()
	if err != nil {
		return err
	}
	err = os.Chmod(tempFile.Name(), 0o644)
	if err != nil {
		return err
	}
	err = os.Chown(tempFile.Name(), 0, 0)
	if err != nil {
		return err
	}

	err = unix.Mount(tempFile.Name(), "/etc/resolv.conf", "", unix.MS_BIND, "")
	if err != nil {
		return err
	}

	err = unix.Unmount(os.TempDir(), 0)
	if err != nil {
		return err
	}

	err = unix.Unshare(unix.CLONE_NEWNET)
	if err != nil {
		return err
	}
	newNetNs, err = getNs("net")
	if err != nil {
		return err
	}
	loLink, err = netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	err = netlink.LinkSetUp(loLink)
	if err != nil {
		return err
	}

	if cfg.FakeDNS {
		packetConn, err = net.ListenPacket("udp", net.JoinHostPort(dnsServer, "53"))
		if err != nil {
			return err
		}
		packetConnFile, err = packetConn.(*net.UDPConn).File()
		if err != nil {
			return err
		}
	}

	err = netlink.LinkAdd(&netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name: cfg.TunName,
		},
		Mode: netlink.TUNTAP_MODE_TUN,
	})
	if err != nil {
		return err
	}
	tunLink, err = netlink.LinkByName(cfg.TunName)
	if err != nil {
		return err
	}
	err = netlink.LinkSetUp(tunLink)
	if err != nil {
		return err
	}
	err = netlink.AddrAdd(tunLink, &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   cfg.TunIP,
			Mask: cfg.TunMask,
		},
	})
	if err != nil {
		return err
	}
	err = netlink.RouteAdd(&netlink.Route{
		Dst:       &net.IPNet{},
		Gw:        cfg.TunIP,
		LinkIndex: tunLink.Attrs().Index,
	})
	if err != nil {
		return err
	}

	tunMTU, err = rawfile.GetMTU(cfg.TunName)
	if err != nil {
		return err
	}

	tunFd, err = tun.Open(cfg.TunName)
	if err != nil {
		return err
	}
	unix.CloseOnExec(tunFd)

	pidFd, err = unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return err
	}

	wd, err = os.Getwd()
	if err != nil {
		return err
	}

	err = unix.Setns(originNetNs, unix.CLONE_NEWNET)
	if err != nil {
		return err
	}
	err = unix.Setns(originMntNs, unix.CLONE_NEWNS)
	if err != nil {
		return err
	}

	execName, err = os.Executable()
	if err != nil {
		return err
	}
	nullFile, err = os.Open(os.DevNull)
	if err != nil {
		return err
	}
	if !cfg.FakeDNS {
		packetConnFile = nullFile
	}
	r, w, err = os.Pipe()
	if err != nil {
		return err
	}
	os.Args = slices.Insert(os.Args, 1, "--daemon")
	_, err = os.StartProcess(execName, os.Args, &os.ProcAttr{
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
		return err
	}
	err = gob.NewEncoder(w).Encode(&Data{
		TunMTU: tunMTU,
		Config: cfg,
	})
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}

	err = unix.Setns(newNetNs, unix.CLONE_NEWNET)
	if err != nil {
		return err
	}
	err = unix.Setns(newMntNs, unix.CLONE_NEWNS)
	if err != nil {
		return err
	}

	// Switching mount namespace using setns(2) would change working directory to /
	err = os.Chdir(wd)
	if err != nil {
		return err
	}

	progName, err = exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return unix.Exec(progName, args, os.Environ())
}
