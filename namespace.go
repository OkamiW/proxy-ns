package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"proxy-ns/config"
	"strconv"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/rawfile"
	"gvisor.dev/gvisor/pkg/tcpip/link/tun"
)

type NetSetup struct {
	TunName   string
	TunIP     net.IP
	TunMask   net.IPMask
	TunIP6    net.IP
	TunMask6  net.IPMask
	FakeDNS   bool
	DNSServer string
	UserNS    bool
	SudoDrop  bool
}

func parseNetSetup(name, ip, ip6, fakeDNS, dns, userNS, sudoDrop string) (*NetSetup, error) {
	fake, err := strconv.ParseBool(fakeDNS)
	if err != nil {
		return nil, err
	}
	user, err := strconv.ParseBool(userNS)
	if err != nil {
		return nil, err
	}
	drop, err := strconv.ParseBool(sudoDrop)
	if err != nil {
		return nil, err
	}
	// Reuse the same network validation as the public configuration.
	var cfg config.Config
	if err := cfg.Update(config.Data{TunName: &name, TunIP: &ip, TunIP6: &ip6, DNSServer: &dns}); err != nil {
		return nil, err
	}
	return &NetSetup{
		TunName: cfg.TunName, TunIP: cfg.TunIP, TunMask: cfg.TunMask,
		TunIP6: cfg.TunIP6, TunMask6: cfg.TunMask6,
		FakeDNS: fake, DNSServer: cfg.DNSServer, UserNS: user, SudoDrop: drop,
	}, nil
}

func runNamespaceChild(cfg *NetSetup, args []string) error {
	runtime.LockOSThread()
	// The helper can also be invoked directly. Always isolate its mounts and network.
	if err := unix.Unshare(unix.CLONE_NEWNS | unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("create mount and network namespaces: %w", err)
	}
	socket := os.NewFile(3, "proxy control")
	unix.CloseOnExec(3)
	defer socket.Close()
	parentPID := os.Getppid()
	cred, err := sudoIdentity(cfg.SudoDrop, os.Geteuid())
	if err != nil {
		return err
	}
	var (
		loLink, tunLink          netlink.Link
		dnsServer                string
		packetConn               net.PacketConn
		packetConnFile, tempFile *os.File
		tunFd                    int
		tunMTU                   uint32
	)
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
	if !cfg.UserNS {
		if err := os.Chown(tempFile.Name(), 0, 0); err != nil {
			return fmt.Errorf("Failed to chown resolv.conf: %w", err)
		}
	}

	err = unix.Mount(tempFile.Name(), "/etc/resolv.conf", "", unix.MS_BIND, "")
	if err != nil {
		return fmt.Errorf("Failed to mount bind resolv.conf: %w", err)
	}

	err = unix.Unmount(os.TempDir(), 0)
	if err != nil {
		return fmt.Errorf("Failed to unmount tmpfs: %w", err)
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
		defer packetConn.Close()
		packetConnFile, err = packetConn.(*net.UDPConn).File()
		if err != nil {
			return fmt.Errorf("Failed to get DNS server listener fd: %w", err)
		}
		defer packetConnFile.Close()
	}

	tuntap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name: cfg.TunName,
		},
		Mode: netlink.TUNTAP_MODE_TUN,
	}
	if cfg.UserNS {
		tuntap.Owner = uint32(os.Geteuid())
		tuntap.Group = uint32(os.Getegid())
	}
	err = netlink.LinkAdd(tuntap)
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

	defer func() {
		if tunFd >= 0 {
			unix.Close(tunFd)
		}
	}()
	if cred != nil && !cfg.UserNS {
		if err := setSudoIdentity(cred); err != nil {
			return err
		}
		// Changing credentials can clear the parent-death signal.
		if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
			return fmt.Errorf("restore parent-death signal: %w", err)
		}
		if os.Getppid() != parentPID {
			return fmt.Errorf("proxy process exited")
		}
	}
	progName, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("find target command: %w", err)
	}

	dnsFD := -1
	if packetConnFile != nil {
		dnsFD = int(packetConnFile.Fd())
	}
	if err := sendProxyFiles(int(socket.Fd()), tunMTU, tunFd, dnsFD); err != nil {
		return fmt.Errorf("send proxy descriptors: %w", err)
	}
	if err := waitReady(int(socket.Fd())); err != nil {
		return err
	}
	if err := socket.Close(); err != nil {
		return err
	}
	unix.Close(tunFd)
	tunFd = -1
	if packetConnFile != nil {
		packetConnFile.Close()
		packetConn.Close()
	}

	if cfg.UserNS && os.Geteuid() == 0 {
		err = dropPrivileges()
	} else {
		err = dropCapabilities()
	}
	if err != nil {
		return err
	}

	return unix.Exec(progName, args, os.Environ())
}

func setSudoIdentity(cred *syscall.Credential) error {
	if err := unix.Setgroups(nil); err != nil {
		return fmt.Errorf("clear sudo supplementary groups: %w", err)
	}
	gid, uid := int(cred.Gid), int(cred.Uid)
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("restore sudo GID: %w", err)
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("restore sudo UID: %w", err)
	}
	return nil
}

func sendProxyFiles(socket int, mtu uint32, tunFD, dnsFD int) error {
	fds := []int{tunFD}
	if dnsFD >= 0 {
		fds = append(fds, dnsFD)
	}
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], mtu)
	return unix.Sendmsg(socket, payload[:], unix.UnixRights(fds...), nil, unix.MSG_NOSIGNAL)
}

func waitReady(socket int) error {
	var ready [1]byte
	for {
		n, _, flags, _, err := unix.Recvmsg(socket, ready[:], nil, unix.MSG_CMSG_CLOEXEC)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for proxy: %w", err)
		}
		if n != 1 || ready[0] != 1 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			return fmt.Errorf("invalid proxy ready packet or proxy closed socket")
		}
		return nil
	}
}
