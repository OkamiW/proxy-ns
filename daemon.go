package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"proxy-ns/config"
	"proxy-ns/fakedns"
	"proxy-ns/proxy"

	"golang.org/x/sys/unix"
)

func namespaceAttributes(userNS bool) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{
		Pdeathsig:  syscall.SIGKILL,
		Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS,
	}
	if userNS {
		attr.Cloneflags |= unix.CLONE_NEWUSER
		uid, gid := os.Geteuid(), os.Getegid()
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
		attr.AmbientCaps = []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_NET_ADMIN, unix.CAP_NET_BIND_SERVICE}
	}
	return attr
}

func namespacePermission(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}

func commandStatus(cmd *exec.Cmd, err error) (int, error) {
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

func forwardSignals(cmd *exec.Cmd, signals <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case sig := <-signals:
			cmd.Process.Signal(sig)
		case <-done:
			return
		}
	}
}

func runMain(cfg *config.Config, args []string, quiet bool, restoreUID, restoreGID int) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dir, err := os.MkdirTemp("", "proxy-ns-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "control")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socketPath, Net: "unixpacket"})
	if err != nil {
		return 1, err
	}
	defer listener.Close()
	childArgs := []string{
		"--namespace-child", "--control-socket", socketPath,
		"--restore-uid", strconv.Itoa(restoreUID),
		"--restore-gid", strconv.Itoa(restoreGID),
		"--tun-name", cfg.TunName,
		"--tun-ip", (&net.IPNet{IP: cfg.TunIP, Mask: cfg.TunMask}).String(),
		"--fake-dns=" + strconv.FormatBool(cfg.FakeDNS),
		"--dns-server", cfg.DNSServer,
		"--userns=" + strconv.FormatBool(cfg.UserNS),
	}
	if len(cfg.TunIP6) != 0 {
		childArgs = append(childArgs, "--tun-ip6", (&net.IPNet{IP: cfg.TunIP6, Mask: cfg.TunMask6}).String())
	}
	if quiet {
		childArgs = append(childArgs, "-q")
	}
	childArgs = append(childArgs, "--")
	childArgs = append(childArgs, args...)
	cmd := exec.Command(executable, childArgs...)
	cmd.SysProcAttr = namespaceAttributes(cfg.UserNS)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		if !namespacePermission(err) {
			return 1, fmt.Errorf("start namespace child: %w", err)
		}
		fmt.Fprintln(os.Stderr, "proxy-ns:", err)
		fmt.Fprintln(os.Stderr, "proxy-ns: namespace creation requires permission; using sudo -P for temporary elevation and retrying.")
		sudo, lookupErr := exec.LookPath("sudo")
		if lookupErr != nil {
			return 1, lookupErr
		}
		cfgPath, pathErr := filepath.Abs(flag.Lookup("c").Value.String())
		if pathErr != nil {
			return 1, pathErr
		}
		if restoreUID < 0 {
			restoreUID, restoreGID = os.Geteuid(), os.Getegid()
		}
		sudoArgs := sudoRetryArgs(flag.CommandLine, executable, cfgPath, restoreUID, restoreGID)
		// Exec replaces Main, so close its listener and remove the socket first.
		listener.Close()
		if err := os.RemoveAll(dir); err != nil {
			return 1, err
		}
		signal.Stop(signals)
		return 1, unix.Exec(sudo, sudoArgs, os.Environ())
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	done := make(chan struct{})
	defer close(done)
	go forwardSignals(cmd, signals, done)
	waited := false
	defer func() {
		if !waited {
			// Stop the namespace child if initialization fails.
			cmd.Process.Signal(syscall.SIGTERM)
			<-exited
		}
	}()
	control, err, childExited := acceptChild(listener, exited)
	if err != nil || childExited {
		if childExited {
			waited = true
			status, waitErr := commandStatus(cmd, err)
			if status == 0 && waitErr == nil {
				return 1, fmt.Errorf("child exited before connecting")
			}
			return status, waitErr
		}
		return 1, err
	}
	defer control.Close()
	listener.Close()
	mtu, received, err := receiveProxyFiles(int(control.Fd()), cfg.FakeDNS)
	if err != nil {
		return 1, err
	}
	defer unix.Close(received[0])
	// Own every received descriptor even if dropping capabilities fails.
	var dnsFile *os.File
	if cfg.FakeDNS {
		dnsFile = os.NewFile(uintptr(received[1]), "FakeDNS listener")
		defer dnsFile.Close()
	}
	os.Stdin.Close()
	os.Stdout.Close()
	if err := dropPrivileges(); err != nil {
		return 1, err
	}
	config.UDPSessionTimeout = cfg.UDPSessionTimeout
	client := proxy.SOCKS5("tcp", cfg.Socks5Address, cfg.Username, cfg.Password)
	var dns *fakedns.Server
	if cfg.FakeDNS {
		file := dnsFile
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

	err = <-exited
	waited = true
	return commandStatus(cmd, err)
}

func sudoRetryArgs(flags *flag.FlagSet, executable, configPath string, uid, gid int) []string {
	args := []string{"sudo", "-P", "--", executable}
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "c", "restore-uid", "restore-gid":
			return
		}
		args = append(args, "--"+option.Name+"="+option.Value.String())
	})
	args = append(args, "-c", configPath,
		"--restore-uid="+strconv.Itoa(uid),
		"--restore-gid="+strconv.Itoa(gid), "--")
	return append(args, flags.Args()...)
}

func receiveProxyFiles(socket int, fakeDNS bool) (uint32, []int, error) {
	expectedFDs := 1
	if fakeDNS {
		expectedFDs = 2
	}
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
		if err == nil && n == len(mtu) &&
			flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) == 0 && len(files) == expectedFDs {
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

func acceptChild(listener *net.UnixListener, exited <-chan error) (*os.File, error, bool) {
	type result struct {
		file *os.File
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				accepted <- result{err: err}
				return
			}
			file, err := conn.File()
			conn.Close()
			if err != nil {
				accepted <- result{err: err}
				return
			}
			peer, err := unix.GetsockoptUcred(int(file.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
			if err != nil {
				file.Close()
				accepted <- result{err: err}
				return
			}
			if peer.Uid != uint32(os.Geteuid()) && peer.Uid != 0 {
				file.Close()
				continue
			}
			accepted <- result{file: file}
			return
		}
	}()
	select {
	case value := <-accepted:
		return value.file, value.err, false
	case err := <-exited:
		listener.Close()
		value := <-accepted
		if value.file != nil {
			value.file.Close()
		}
		return nil, err, true
	}
}
