package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

var DefaultConfig = Config{
	TunName:       "tun0",
	TunIP:         net.IP{10, 0, 0, 1},
	TunMask:       net.IPMask{255, 255, 255, 255},
	Socks5Address: "127.0.0.1:1080",
	Username:      "",
	Password:      "",
	FakeDNS:       true,
	FakeNetwork: &net.IPNet{
		IP:   net.IP{240, 0, 0, 0},
		Mask: net.IPMask{240, 0, 0, 0},
	},
	DNSServer: "9.9.9.9",
}

type Data struct {
	TunName       *string `json:"tun_name,omitempty"`
	TunIP         *string `json:"tun_ip,omitempty"`
	Socks5Address *string `json:"socks5_address,omitempty"`
	Username      *string `json:"username,omitempty"`
	Password      *string `json:"password,omitempty"`
	FakeDNS       *bool   `json:"fake_dns,omitempty"`
	FakeNetwork   *string `json:"fake_network,omitempty"`
	DNSServer     *string `json:"dns_server,omitempty"`
}

type Config struct {
	TunName       string
	TunIP         net.IP
	TunMask       net.IPMask
	Socks5Address string
	Username      string
	Password      string
	FakeDNS       bool
	FakeNetwork   *net.IPNet
	DNSServer     string
}

func (cfg *Config) Update(data Data) error {
	if data.TunName != nil {
		if *data.TunName == "" {
			return errors.New("Empty tun name")
		}
		cfg.TunName = *data.TunName
	}
	if data.TunIP != nil {
		ip, ipNet, err := net.ParseCIDR(*data.TunIP)
		if err != nil {
			return fmt.Errorf("Invalid tun ip: %s: %w", *data.TunIP, err)
		}
		cfg.TunIP = ip
		cfg.TunMask = ipNet.Mask
	}
	if data.Socks5Address != nil {
		if *data.Socks5Address == "" {
			return errors.New("Empty socks5 address")
		}
		var (
			addr net.Addr
			err  error
		)
		if addr, err = net.ResolveTCPAddr("tcp", *data.Socks5Address); err != nil {
			return fmt.Errorf("Invalid socks5 address: %s: %w", *data.Socks5Address, err)
		}
		cfg.Socks5Address = addr.String()
	}
	if data.Username != nil {
		cfg.Username = *data.Username
	}
	if data.Password != nil {
		cfg.Password = *data.Password
	}
	if data.FakeDNS != nil {
		cfg.FakeDNS = *data.FakeDNS
	}
	if data.FakeNetwork != nil {
		_, ipNet, err := net.ParseCIDR(*data.FakeNetwork)
		if err != nil {
			return fmt.Errorf("Invalid fake network: %s: %w", *data.FakeNetwork, err)
		}
		cfg.FakeNetwork = ipNet
	}
	if data.DNSServer != nil {
		if ip := net.ParseIP(*data.DNSServer); ip == nil {
			return fmt.Errorf("Invalid dns server: %s", *data.DNSServer)
		}
		cfg.DNSServer = *data.DNSServer
	}
	return nil
}

func FromFile(path string) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("Config file not found")
	}

	var cfg Config = DefaultConfig
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to open config: %w", err)
	}
	defer f.Close()

	var data Data
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&data)
	if err != nil {
		return nil, fmt.Errorf("Failed to decode %s: %w", path, err)
	}

	err = cfg.Update(data)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}
