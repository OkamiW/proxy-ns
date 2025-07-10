package network

import "time"

const (
	MaxPacketSize     = (1 << 16) - 1
	UDPSessionTimeout = time.Minute
)
