package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReceiveProxyFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fakeDNS bool
		files   int
		bad     bool
	}{
		{"tun", false, 1, false},
		{"fake DNS", true, 2, false},
		{"missing DNS", true, 1, true},
		{"extra DNS", false, 2, true},
		{"truncated payload", false, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control")
			listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(client)
			if err := unix.Connect(client, &unix.SockaddrUnix{Name: path}); err != nil {
				t.Fatal(err)
			}
			control, err, exited := acceptChild(listener, make(chan error))
			if err != nil || exited {
				t.Fatalf("accept: %v, exited=%v", err, exited)
			}
			defer control.Close()
			sockets := []int{client, int(control.Fd())}
			file, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			fds := []int{int(file.Fd())}
			if tc.files == 2 {
				fds = append(fds, int(file.Fd()))
			}
			payload := make([]byte, 4)
			binary.BigEndian.PutUint32(payload, 1500)
			if tc.name == "truncated payload" {
				payload = append(payload, 0)
			}
			if tc.name == "truncated payload" {
				err = unix.Sendmsg(sockets[0], payload, unix.UnixRights(fds...), nil, unix.MSG_NOSIGNAL)
			} else {
				dnsFD := -1
				if tc.files == 2 {
					dnsFD = int(file.Fd())
				}
				err = sendProxyFiles(sockets[0], 1500, int(file.Fd()), dnsFD)
			}
			if err != nil {
				t.Fatal(err)
			}
			mtu, received, err := receiveProxyFiles(sockets[1], tc.fakeDNS)
			for _, fd := range received {
				defer unix.Close(fd)
				flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
				if flagErr != nil || flags&unix.FD_CLOEXEC == 0 {
					t.Errorf("missing close-on-exec: flags=%d, error=%v", flags, flagErr)
				}
			}
			if (err != nil) != tc.bad {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := sendReady(sockets[1]); err != nil {
				t.Fatal(err)
			}
			if err := waitReady(sockets[0]); err != nil {
				t.Fatal(err)
			}
			if !tc.bad && (mtu != 1500 || len(received) != tc.files) {
				t.Fatalf("MTU=%d, received %d descriptors", mtu, len(received))
			}
		})
	}
}

func TestAcceptChildExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	exited := make(chan error, 1)
	want := errors.New("sudo cancelled")
	exited <- want
	file, err, stopped := acceptChild(listener, exited)
	if file != nil || !errors.Is(err, want) || !stopped {
		t.Fatalf("file=%v, error=%v, stopped=%v", file, err, stopped)
	}
}

func TestNamespacePermission(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("clone: %w", unix.EPERM), true},
		{fmt.Errorf("clone: %w", unix.EACCES), true},
		{unix.ENOENT, false},
		{nil, false},
	} {
		if namespacePermission(tc.err) != tc.want {
			t.Errorf("error=%v", tc.err)
		}
	}
}

func TestSudoRetryArgs(t *testing.T) {
	flags := flag.NewFlagSet("proxy-ns", flag.ContinueOnError)
	flags.String("c", "", "")
	flags.Int("restore-uid", -1, "")
	flags.Int("restore-gid", -1, "")
	flags.String("username", "", "")
	if err := flags.Parse([]string{"-c", "./proxy.json", "--restore-uid=1000",
		"--restore-gid=1000", "--username", "--", "curl", "--version", "--"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"sudo", "-P", "--", "/usr/local/bin/proxy-ns", "--username=--",
		"-c", "/work/proxy.json", "--restore-uid=1000", "--restore-gid=1000",
		"--", "curl", "--version", "--"}
	if got := sudoRetryArgs(flags, "/usr/local/bin/proxy-ns", "/work/proxy.json", 1000, 1000); !slices.Equal(got, want) {
		t.Fatalf("sudo argv = %q, want %q", got, want)
	}
}
