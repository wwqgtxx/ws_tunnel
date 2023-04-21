package udp

import (
	"github.com/wwqgtxx/wstunnel/config"
	"log"
	"net"
)

const MaxUdpAge = 5 * 60

type Tunnel interface {
	Handle()
}

var tunnels = make(map[string]Tunnel)

func BuildUdp(udpConfig config.UdpConfig) {
	_, port, err := net.SplitHostPort(udpConfig.BindAddress)
	if err != nil {
		log.Println(err)
		return
	}
	if udpConfig.MMsg {
		tunnels[port] = NewMmsgTunnel(udpConfig)
	} else {
		tunnels[port] = NewStdTunnel(udpConfig)
	}

}

func StartUdps() {
	for _, tunnel := range tunnels {
		go tunnel.Handle()
	}
}
