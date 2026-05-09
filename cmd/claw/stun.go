package main

import (
	"fmt"
	"net"

	"github.com/pion/stun"
)

// STUNInfo public address info obtained via STUN
type STUNInfo struct {
	IP   string
	Port int
}

// GetSTUNInfo get public IP:port via STUN server
func GetSTUNInfo(stunAddr string) (*STUNInfo, error) {
	if stunAddr == "" {
		stunAddr = "stun.qq.com:3478"
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("listen on UDP failed: %w", err)
	}
	defer conn.Close()

	stunClient, err := stun.Dial("udp", stunAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to STUN server failed: %w", err)
	}
	defer stunClient.Close()

	var xorAddr stun.XORMappedAddress
	if err := stunClient.Do(stun.MustBuild(stun.TransactionID, stun.BindingRequest), func(res stun.Event) {
		if res.Error != nil {
			return
		}
		xorAddr.GetFrom(res.Message)
	}); err != nil {
		return nil, fmt.Errorf("STUN request failed: %w", err)
	}

	return &STUNInfo{
		IP:   xorAddr.IP.String(),
		Port: xorAddr.Port,
	}, nil
}
