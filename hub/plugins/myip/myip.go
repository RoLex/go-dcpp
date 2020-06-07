package myip

import (
	"net"

	"github.com/RoLex/go-dcpp/hub"
)

func init() {
	hub.RegisterPlugin(&myIP{})
}

type myIP struct {
	h *hub.Hub
}

func (*myIP) Name() string {
	return "MyIP"
}

func (*myIP) Version() hub.Version {
	return hub.Version{Major: 2, Minor: 0}
}

func (p *myIP) Init(h *hub.Hub, path string) error {
	p.h = h
	h.RegisterCommand(hub.Command{
		Menu: []string{"My IP"},
		Name: "myip", Aliases: []string{"ip"},
		Short: "shows your current ip address",
		Func:  p.cmdIP,
	})
	return nil
}

func (p *myIP) cmdIP(peer hub.Peer, args string) error {
	host, _, _ := net.SplitHostPort(peer.RemoteAddr().String())
	_ = peer.ChatMsg(nil, peer, hub.Message{Me: true, Text: "- " + host})
	return nil
}

func (p *myIP) Close() error {
	return nil
}
