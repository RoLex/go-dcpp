package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"time"

	adcp "github.com/direct-connect/go-dc/adc"
	"github.com/direct-connect/go-dcpp/adc"
	"github.com/direct-connect/go-dcpp/version"
)

// DialHub connects to a hub and runs a handshake.
func DialHub(addr string, info *Config) (*Conn, error) {
	conn, err := adc.Dial(addr)
	if err != nil {
		return nil, err
	}
	return HubHandshake(conn, info)
}

type Config struct {
	PID        adc.PID
	Name       string
	Extensions adcp.ExtFeatures
}

func (c *Config) validate() error {
	if c.PID.IsZero() {
		return errors.New("PID should not be empty")
	}
	if c.Name == "" {
		return errors.New("name should be set")
	}
	return nil
}

// HubHandshake begins a Client-Hub handshake on a connection.
func HubHandshake(conn *adc.Conn, conf *Config) (*Conn, error) {
	if err := conf.validate(); err != nil {
		return nil, err
	}
	sid, mutual, err := protocolToHub(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c := &Conn{
		conn: conn,
		sid:  sid, pid: conf.PID,
		fea:     mutual,
		closing: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	c.user.Pid = &conf.PID
	c.user.Name = conf.Name
	c.user.Features = conf.Extensions
	c.user.Slots = 1

	if err := identifyToHub(conn, sid, &c.user); err != nil {
		conn.Close()
		return nil, err
	}
	c.ext = make(map[adcp.Feature]struct{})
	for _, ext := range c.user.Features {
		c.ext[ext] = struct{}{}
	}
	if err := c.acceptUsersList(); err != nil {
		conn.Close()
		return nil, err
	}
	//c.conn.KeepAlive(time.Minute / 2)
	go c.readLoop()
	return c, nil
}

func protocolToHub(conn *adc.Conn) (adc.SID, adcp.ModFeatures, error) {
	ourFeatures := adcp.ModFeatures{
		// should always be set for ADC
		adcp.FeaBASE: true,
		adcp.FeaBAS0: true,
		adcp.FeaTIGR: true,
		// extensions

		// TODO: some hubs will stop the handshake after sending the hub info
		//       if this extension is specified
		//adc.FeaPING: true,

		adcp.FeaBZIP: true,
		// TODO: ZLIG
	}

	// Send supported features (SUP), initiating the PROTOCOL state.
	// We expect SUP followed by SID to transition to IDENTIFY.
	//
	// https://adc.sourceforge.io/ADC.html#_protocol
	err := conn.WriteHubMsg(adcp.Supported{
		Features: ourFeatures,
	})
	if err != nil {
		return adc.SID{}, nil, err
	}
	if err := conn.Flush(); err != nil {
		return adc.SID{}, nil, err
	}
	// shouldn't take longer than this
	deadline := time.Now().Add(time.Second * 5)

	// first, we expect a SUP from the hub with a list of supported features
	msg, err := conn.ReadInfoMsg(deadline)
	if err != nil {
		return adc.SID{}, nil, err
	}
	sup, ok := msg.(adcp.Supported)
	if !ok {
		return adc.SID{}, nil, fmt.Errorf("expected SUP command, got: %#v", msg)
	}
	hubFeatures := sup.Features

	// check mutual features
	mutual := ourFeatures.Intersect(hubFeatures)
	if !mutual.IsSet(adcp.FeaBASE) && !mutual.IsSet(adcp.FeaBAS0) {
		return adc.SID{}, nil, fmt.Errorf("hub does not support BASE")
	} else if !mutual.IsSet(adcp.FeaTIGR) {
		return adc.SID{}, nil, fmt.Errorf("hub does not support TIGR")
	}

	// next, we expect a SID that will assign a Session ID
	msg, err = conn.ReadInfoMsg(deadline)
	if err != nil {
		return adc.SID{}, nil, err
	}
	sid, ok := msg.(adcp.SIDAssign)
	if !ok {
		return adc.SID{}, nil, fmt.Errorf("expected SID command, got: %#v", msg)
	}
	return sid.SID, mutual, nil
}

func identifyToHub(conn *adc.Conn, sid adc.SID, user *adcp.UserInfo) error {
	// Hub may send INF, but it's not required.
	// The client should broadcast INF with PD/ID and other required fields.
	//
	// https://adc.sourceforge.io/ADC.html#_identify

	if user.Id.IsZero() {
		user.Id = user.Pid.Hash()
	}
	if user.Application == "" {
		user.Application = version.Name
		user.Version = version.Vers
	}
	for _, f := range []adcp.Feature{adcp.FeaSEGA, adcp.FeaTCP4} {
		if !user.Features.Has(f) {
			user.Features = append(user.Features, f)
		}
	}
	err := conn.WriteBroadcast(sid, user)
	if err != nil {
		return err
	}
	if err := conn.Flush(); err != nil {
		return err
	}
	// TODO: registered user
	return nil
}

// Conn represents a Client-to-Hub connection.
type Conn struct {
	conn *adc.Conn
	fea  adcp.ModFeatures

	closing chan struct{}
	closed  chan struct{}

	pid  adc.PID
	sid  adc.SID
	user adcp.UserInfo
	ext  map[adcp.Feature]struct{}

	hub adcp.HubInfo

	peers struct {
		sync.RWMutex
		// keeps both online and offline users
		byCID map[adc.CID]*Peer
		// only keeps online users
		bySID map[adc.SID]*Peer
	}

	revConn struct {
		sync.Mutex
		tokens map[string]revConnToken
	}
}

type revConnToken struct {
	cid    adc.CID
	cancel <-chan struct{}
	addr   chan string
	errc   chan error
}

// PID returns Private ID associated with this connection.
func (c *Conn) PID() adc.PID { return c.pid }

// CID returns Client ID associated with this connection.
func (c *Conn) CID() adc.CID { return c.user.Id }

// SID returns Session ID associated with this connection.
// Only valid after a Client-Hub handshake.
func (c *Conn) SID() adc.SID { return c.sid }

// Hub returns hub information.
func (c *Conn) Hub() adcp.HubInfo { return c.hub }

// Features returns a set of negotiated features.
func (c *Conn) Features() adcp.ModFeatures { return c.fea.Clone() }

func (c *Conn) Close() error {
	select {
	case <-c.closing:
		<-c.closed
		return nil
	default:
	}
	close(c.closing)
	err := c.conn.Close()
	<-c.closed
	return err
}

func (c *Conn) writeDirect(to adc.SID, msg adcp.Message) error {
	if err := c.conn.WriteDirect(c.SID(), to, msg); err != nil {
		return err
	}
	return c.conn.Flush()
}

func (c *Conn) revConnToken(ctx context.Context, cid adc.CID) (token string, addr <-chan string, _ <-chan error) {
	ch := make(chan string, 1)
	errc := make(chan error, 1)
	for {
		tok := strconv.Itoa(rand.Int())
		c.revConn.Lock()
		_, ok := c.revConn.tokens[tok]
		if !ok {
			if c.revConn.tokens == nil {
				c.revConn.tokens = make(map[string]revConnToken)
			}
			c.revConn.tokens[tok] = revConnToken{cancel: ctx.Done(), cid: cid, addr: ch, errc: errc}
			c.revConn.Unlock()
			return tok, ch, errc
		}
		c.revConn.Unlock()
		// collision, pick another token
	}
}

func (c *Conn) readLoop() {
	defer close(c.closed)
	for {
		cmd, err := c.conn.ReadPacketRaw(time.Time{})
		if err != nil {
			log.Println(err)
			return
		}
		switch cmd := cmd.(type) {
		case *adcp.BroadcastPacket:
			if err := c.handleBroadcast(cmd); err != nil {
				log.Println(err)
				return
			}
		case *adcp.InfoPacket:
			if err := c.handleInfo(cmd); err != nil {
				log.Println(err)
				return
			}
		case *adcp.FeaturePacket:
			if err := c.handleFeature(cmd); err != nil {
				log.Println(err)
				return
			}
		case *adcp.DirectPacket:
			// TODO: ADC flaw: why ever send the client his own SID? hub should append it instead
			//		 same for the sending party
			if cmd.To != c.SID() {
				log.Println("direct command to a wrong destination:", cmd.To)
				return
			}
			if err := c.handleDirect(cmd); err != nil {
				log.Println(err)
				return
			}
		default:
			log.Printf("unhandled command: %T", cmd)
		}
	}
}

func (c *Conn) handleBroadcast(p *adcp.BroadcastPacket) error {
	// we could decode the message and type-switch, but for cases
	// below it's better to decode later
	switch p.Msg.Cmd() {
	case (adcp.UserInfo{}).Cmd():
		// easier to merge while decoding
		return c.peerUpdate(p.ID, p)
	case (adcp.SearchRequest{}).Cmd():
		peer := c.peerBySID(p.ID)
		c.handleSearch(peer, p)
		return nil
	// TODO: MSG
	default:
		log.Printf("unhandled broadcast command: %v", p.Msg.Cmd())
		return nil
	}
}

func (c *Conn) handleInfo(p *adcp.InfoPacket) error {
	err := p.DecodeMessage()
	if err != nil {
		return err
	}
	switch msg := p.Msg.(type) {
	case adcp.ChatMessage:
		// TODO: ADC: maybe hub should take a AAAA SID for itself
		//       and this will become B-MSG AAAA, instead of I-MSG
		fmt.Printf("%s\n", msg.Text)
		return nil
	case adcp.Disconnect:
		// TODO: ADC flaw: this should be B-QUI, not I-QUI
		//  	 it always includes a SID and is, in fact, a broadcast
		return c.peerQuit(msg.ID)
	default:
		log.Printf("unhandled info command: %v", p.Msg.Cmd())
		return nil
	}
}

func (c *Conn) handleFeature(cmd *adcp.FeaturePacket) error {
	// TODO: ADC protocol: this is another B-XXX command, but with a feature selector
	//		 might be a good idea to extend selector with some kind of tags
	//		 it may work for extensions, geo regions, chat channels, etc

	// TODO: ADC flaw: shouldn't the hub convert F-XXX to B-XXX if the current client
	//		 supports all listed extensions? does the client care about the selector?

	for _, f := range cmd.Sel {
		if _, enabled := c.ext[f.Fea]; enabled != f.Sel {
			return nil
		}
	}

	// FIXME: this allows F-MSG that we should probably avoid
	return c.handleBroadcast(&adcp.BroadcastPacket{
		ID: cmd.ID, Msg: cmd.Msg,
	})
}

func (c *Conn) handleDirect(cmd *adcp.DirectPacket) error {
	err := cmd.DecodeMessage()
	if err != nil {
		return err
	}
	switch msg := cmd.Msg.(type) {
	case adcp.ConnectRequest:
		c.revConn.Lock()
		tok, ok := c.revConn.tokens[msg.Token]
		delete(c.revConn.tokens, msg.Token)
		c.revConn.Unlock()
		if !ok {
			// TODO: handle a direct connection request from peers
			log.Printf("ignoring connection attempt from %v", cmd.ID)
			return nil
		}
		p := c.peerBySID(cmd.ID)
		go c.handleConnReq(p, tok, msg)
		return nil
	default:
		log.Printf("unhandled direct command: %v", cmd.Msg.Cmd())
		return nil
	}
}

func (c *Conn) handleConnReq(p *Peer, tok revConnToken, s adcp.ConnectRequest) {
	if p == nil {
		tok.errc <- ErrPeerOffline
		return
	}
	if s.Proto != adcp.ProtoADC {
		tok.errc <- fmt.Errorf("unsupported protocol: %q", s.Proto)
		return
	}
	if s.Port == 0 {
		tok.errc <- errors.New("no port to connect to")
		return
	}
	addr := p.Info().Ip4
	if addr == "" {
		tok.errc <- errors.New("no address to connect to")
		return
	}
	tok.addr <- addr + ":" + strconv.Itoa(s.Port)
}

func (c *Conn) handleSearch(p *Peer, pck adcp.Packet) {
	var sch adcp.SearchRequest
	if err := pck.DecodeMessageTo(&sch); err != nil {
		log.Println("failed to decode search:", err)
		return
	}
	log.Printf("search: %+v", sch)
}

func (c *Conn) OnlinePeers() []*Peer {
	c.peers.RLock()
	defer c.peers.RUnlock()
	arr := make([]*Peer, 0, len(c.peers.bySID))
	for _, p := range c.peers.bySID {
		arr = append(arr, p)
	}
	return arr
}
func (c *Conn) peerBySID(sid adc.SID) *Peer {
	c.peers.RLock()
	p := c.peers.bySID[sid]
	c.peers.RUnlock()
	return p
}

func (c *Conn) peerJoins(sid adc.SID, u adcp.UserInfo) *Peer {
	c.peers.Lock()
	defer c.peers.Unlock()
	if c.peers.byCID == nil {
		c.peers.bySID = make(map[adc.SID]*Peer)
		c.peers.byCID = make(map[adc.CID]*Peer)
	}
	p, ok := c.peers.byCID[u.Id]
	if ok {
		c.peers.bySID[sid] = p
		p.online(sid)
		p.update(u)
		return p
	}
	p = &Peer{hub: c, user: &u}
	c.peers.bySID[sid] = p
	c.peers.byCID[u.Id] = p
	p.online(sid)
	return p
}

func (c *Conn) peerQuit(sid adc.SID) error {
	c.peers.Lock()
	defer c.peers.Unlock()
	p := c.peers.bySID[sid]
	if p == nil {
		return fmt.Errorf("unknown user quits: %v", sid)
	}
	p.offline()
	delete(c.peers.bySID, sid)
	return nil
}

func (c *Conn) peerUpdate(sid adc.SID, pck adcp.Packet) error {
	c.peers.Lock()
	p, ok := c.peers.bySID[sid]
	if ok {
		c.peers.Unlock()

		p.mu.Lock()
		defer p.mu.Unlock()
		return pck.DecodeMessageTo(p.user)
	}
	defer c.peers.Unlock()

	var u adcp.UserInfo
	if err := pck.DecodeMessageTo(&u); err != nil {
		return err
	}
	if c.peers.byCID == nil {
		c.peers.bySID = make(map[adc.SID]*Peer)
		c.peers.byCID = make(map[adc.CID]*Peer)
	}
	p = &Peer{hub: c, user: &u}
	c.peers.bySID[sid] = p
	c.peers.byCID[u.Id] = p
	p.online(sid)
	return nil
}

func (c *Conn) acceptUsersList() error {
	// https://adc.sourceforge.io/ADC.html#_identify

	deadline := time.Now().Add(time.Minute)
	// Accept commands in the following order:
	// 1) Hub info (I-INF)
	// 2) Status (I-STA, optional)
	// 3) User info (B-INF, xN)
	// 3.1) Our own info (B-INF)
	const (
		hubInfo = iota
		optStatus
		userList
	)
	stage := hubInfo
	for {
		cmd, err := c.conn.ReadPacketRaw(deadline)
		if err != nil {
			return err
		}
		switch cmd := cmd.(type) {
		case *adcp.InfoPacket:
			typ := cmd.Msg.Cmd()
			switch stage {
			case hubInfo:
				// waiting for hub info
				if typ != (adcp.UserInfo{}).Cmd() {
					return fmt.Errorf("expected hub info, received: %#v", cmd)
				}
				if err := cmd.DecodeMessageTo(&c.hub); err != nil {
					return err
				}
				stage = optStatus
			case optStatus:
				// optionally wait for status command
				if typ != (adcp.Status{}).Cmd() {
					return fmt.Errorf("expected status, received: %#v", cmd)
				}
				var st adcp.Status
				if err := cmd.DecodeMessageTo(&st); err != nil {
					return err
				} else if !st.Ok() {
					return st.Err()
				}
				stage = userList
			default:
				return fmt.Errorf("unexpected command in stage %d: %#v", stage, cmd)
			}
		case *adcp.BroadcastPacket:
			switch stage {
			case optStatus:
				stage = userList
				fallthrough
			case userList:
				if cmd.ID == c.sid {
					// make sure to wipe PID, so we don't send it later occasionally
					c.user.Pid = nil
					if err := cmd.DecodeMessageTo(&c.user); err != nil {
						return err
					}
					// done, should switch to NORMAL
					return nil
				}
				// other users
				var u adcp.UserInfo
				if err := cmd.DecodeMessageTo(&u); err != nil {
					return err
				}
				_ = c.peerJoins(cmd.ID, u)
				// continue until we see ourselves in the list
			default:
				return fmt.Errorf("unexpected command in stage %d: %#v", stage, cmd)
			}
		default:
			return fmt.Errorf("unexpected command: %#v", cmd)
		}
	}
}

type Peer struct {
	hub *Conn

	mu   sync.RWMutex
	sid  *adc.SID // may change if user disconnects
	user *adcp.UserInfo
}

func (p *Peer) online(sid adc.SID) {
	p.mu.Lock()
	p.sid = &sid
	p.mu.Unlock()
}

func (p *Peer) offline() {
	p.mu.Lock()
	p.sid = nil
	p.mu.Unlock()
}

func (p *Peer) getSID() *adc.SID {
	p.mu.RLock()
	sid := p.sid
	p.mu.RUnlock()
	return sid
}

func (p *Peer) Online() bool {
	return p.getSID() != nil
}

func (p *Peer) Info() adcp.UserInfo {
	p.mu.RLock()
	user := *p.user
	p.mu.RUnlock()
	return user
}

func (p *Peer) update(u adcp.UserInfo) {
	p.mu.Lock()
	if p.user.Id != u.Id {
		p.mu.Unlock()
		panic("wrong cid")
	}
	*p.user = u
	p.mu.Unlock()
}
