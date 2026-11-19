package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"proxy-ns/config"
	"proxy-ns/fakedns"
	"proxy-ns/proxy"

	"golang.org/x/sys/unix"
)

func sudoIdentity(enabled bool, euid int) (*syscall.Credential, error) {
	if !enabled || euid != 0 {
		return nil, nil
	}
	uidText, hasUID := os.LookupEnv("SUDO_UID")
	gidText, hasGID := os.LookupEnv("SUDO_GID")
	if !hasUID && !hasGID {
		return nil, nil
	}
	uid, uidErr := strconv.ParseUint(uidText, 10, 32)
	gid, gidErr := strconv.ParseUint(gidText, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 1<<32-1 || gid == 1<<32-1 ||
		int(uid) < 0 || int(gid) < 0 {
		return nil, fmt.Errorf("SUDO_UID and SUDO_GID must both contain valid numeric IDs")
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

func runMain(cfg *config.Config, args []string, quiet bool) (int, error) {
	cred, err := sudoIdentity(cfg.SudoDrop, os.Geteuid())
	if err != nil {
		return 1, err
	}
	attr := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if cfg.UserNS {
		uid, gid := os.Geteuid(), os.Getegid()
		if cred != nil {
			uid, gid = int(cred.Uid), int(cred.Gid)
			attr.Credential = cred
			attr.GidMappingsEnableSetgroups = true
		}
		attr.Cloneflags = unix.CLONE_NEWUSER
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
		attr.AmbientCaps = []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_NET_ADMIN, unix.CAP_NET_BIND_SERVICE}
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 1, fmt.Errorf("create proxy control socket: %w", err)
	}
	control := os.NewFile(uintptr(fds[0]), "proxy control")
	defer control.Close()
	childSocket := os.NewFile(uintptr(fds[1]), "child control")

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer signal.Stop(signals)

	// Pdeathsig follows the OS thread that starts the child.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	childArgs := []string{
		"--namespace-child",
		"--tun-name", cfg.TunName,
		"--tun-ip", (&net.IPNet{IP: cfg.TunIP, Mask: cfg.TunMask}).String(),
		"--fake-dns=" + strconv.FormatBool(cfg.FakeDNS),
		"--dns-server", cfg.DNSServer,
		"--userns=" + strconv.FormatBool(cfg.UserNS),
		"--sudo-drop=" + strconv.FormatBool(cfg.SudoDrop),
	}
	if len(cfg.TunIP6) != 0 {
		childArgs = append(childArgs, "--tun-ip6", (&net.IPNet{IP: cfg.TunIP6, Mask: cfg.TunMask6}).String())
	}
	if quiet {
		childArgs = append(childArgs, "-q")
	}
	childArgs = append(childArgs, "--")
	cmd := exec.Command("/proc/self/exe", append(childArgs, args...)...)
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{childSocket}
	cmd.SysProcAttr = attr
	err = cmd.Start()
	childSocket.Close()
	if err != nil {
		return 1, fmt.Errorf("start namespace child (userns=%t): %w", cfg.UserNS, err)
	}
	os.Stdin.Close()
	os.Stdout.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-signals:
				cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	waited := false
	defer func() {
		if !waited {
			control.Close()
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	mtu, received, err := receiveProxyFiles(int(control.Fd()), cfg.FakeDNS)
	if err != nil {
		// A child that failed setup logs the original error before closing the socket.
		return 1, err
	}
	defer unix.Close(received[0])

	// Match the target UID so the supervisor can forward signals after dropping capabilities.
	if cred != nil {
		if err := setSudoIdentity(cred); err != nil {
			return 1, err
		}
	}
	if err := dropPrivileges(); err != nil {
		return 1, err
	}

	config.UDPSessionTimeout = cfg.UDPSessionTimeout
	client := proxy.SOCKS5("tcp", cfg.Socks5Address, cfg.Username, cfg.Password)
	var dns *fakedns.Server
	if cfg.FakeDNS {
		file := os.NewFile(uintptr(received[1]), "FakeDNS listener")
		conn, err := net.FilePacketConn(file)
		file.Close()
		if err != nil {
			return 1, fmt.Errorf("open FakeDNS listener: %w", err)
		}
		defer conn.Close()
		dns = fakedns.NewServer(conn, client, net.JoinHostPort(cfg.DNSServer, "53"), cfg.FakeNetwork)
		go func() {
			if err := dns.Run(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("FakeDNS: %v", err)
			}
		}()
	}
	if err := manageTun(mtu, received[0], client, dns); err != nil {
		return 1, fmt.Errorf("manage TUN: %w", err)
	}
	if err := sendReady(int(control.Fd())); err != nil {
		return 1, fmt.Errorf("notify namespace child: %w", err)
	}
	control.Close()

	err = cmd.Wait()
	waited = true
	if cmd.ProcessState != nil {
		if status := cmd.ProcessState.Sys().(syscall.WaitStatus); status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
	}
	if _, ok := err.(*exec.ExitError); ok {
		return cmd.ProcessState.ExitCode(), nil
	}
	return 0, err
}

func receiveProxyFiles(socket int, fakeDNS bool) (uint32, []int, error) {
	var mtu [4]byte
	control := make([]byte, unix.CmsgSpace(8))
	for {
		n, oobn, flags, _, err := unix.Recvmsg(socket, mtu[:], control, unix.MSG_CMSG_CLOEXEC)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, nil, fmt.Errorf("receive proxy descriptors: %w", err)
		}
		messages, err := unix.ParseSocketControlMessage(control[:oobn])
		var files []int
		for _, message := range messages {
			rights, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				err = parseErr
			}
			files = append(files, rights...)
		}
		if err == nil && n == len(mtu) && flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) == 0 && ((!fakeDNS && len(files) == 1) || (fakeDNS && len(files) == 2)) {
			return binary.BigEndian.Uint32(mtu[:]), files, nil
		}
		for _, fd := range files {
			unix.Close(fd)
		}
		return 0, nil, fmt.Errorf("namespace child did not send valid proxy descriptors")
	}
}

// sendReady releases the helper only after the proxy has initialized.
func sendReady(socket int) error {
	return unix.Sendmsg(socket, []byte{1}, nil, nil, unix.MSG_NOSIGNAL)
}
