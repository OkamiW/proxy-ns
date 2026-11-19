package main

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"os"
	"testing"
)

func TestSudoIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		euid     int
		uid, gid *string
		want     bool
		bad      bool
	}{
		{"absent", true, 0, nil, nil, false, false},
		{"disabled", false, 0, idString("bad"), nil, false, false},
		{"non-root", true, 1000, idString("bad"), nil, false, false},
		{"incomplete", true, 0, idString("1000"), nil, false, true},
		{"empty", true, 0, idString(""), idString("100"), false, true},
		{"invalid", true, 0, idString("1000"), idString("4294967295"), false, true},
		{"valid", true, 0, idString("1000"), idString("100"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range map[string]*string{"SUDO_UID": tc.uid, "SUDO_GID": tc.gid} {
				t.Setenv(key, "")
				if value == nil {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(key, *value)
				}
			}
			cred, err := sudoIdentity(tc.enabled, tc.euid)
			if (err != nil) != tc.bad || (cred != nil) != tc.want {
				t.Fatalf("credential = %v, error = %v", cred, err)
			}
			if cred != nil && (cred.Uid != 1000 || cred.Gid != 100 || len(cred.Groups) != 0) {
				t.Fatalf("unexpected credential: %+v", cred)
			}
		})
	}
}

func idString(value string) *string { return &value }

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
			sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(sockets[0])
			defer unix.Close(sockets[1])
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
