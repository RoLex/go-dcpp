package dc

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	dc "github.com/RoLex/go-dc"
	adcp "github.com/RoLex/go-dc/adc"
	nmdcp "github.com/RoLex/go-dc/nmdc"
	"github.com/RoLex/go-dcpp/adc"
	"github.com/RoLex/go-dcpp/nmdc"
)

type PingConfig = adc.PingConfig

// Ping fetches the information about the specified hub.
func Ping(ctx context.Context, addr string, conf *PingConfig) (*HubInfo, error) {
	if conf == nil {
		conf = &PingConfig{}
	}
	if conf.Name == "" {
		num := int64(time.Now().Nanosecond())
		conf.Name = "pinger_" + strconv.FormatInt(num, 16)
	}
	if conf.Hubs == 0 {
		conf.Hubs = 1 + rand.Intn(10)
	}
	if conf.Slots == 0 {
		conf.Slots = 5
	}
	if conf.ShareFiles == 0 {
		conf.ShareFiles = 100 + rand.Intn(1000)
	}
	if conf.ShareSize == 0 {
		conf.ShareSize = uint64(100+rand.Intn(200)) * 1023 * 1023 * 1023
	}

	// probe first, if protocol is not specified
	i := strings.Index(addr, "://")
	if i < 0 {
		u, err := Probe(ctx, addr)
		if err != nil {
			return nil, err
		}
		addr = u.String()
		i = strings.Index(addr, "://")
	}

	switch addr[:i] {
	case nmdcSchema, nmdcsSchema:
		hub, err := nmdc.Ping(ctx, addr, nmdc.PingConfig{
			Name: conf.Name, Share: conf.ShareSize,
			Slots: conf.Slots, Hubs: conf.Hubs,
		})
		if err == nmdc.ErrRegisteredOnly {
			// TODO: should support error code in NMDC
			err = adcp.Error{Status: adcp.Status{
				Sev: adcp.Fatal, Code: 26,
				Msg: err.Error(),
			}}
		} else if e, ok := err.(*nmdc.ErrBanned); ok {
			// TODO: distinguish temporary and permanent bans
			err = adcp.Error{Status: adcp.Status{
				Sev: adcp.Fatal, Code: 30,
				Msg: e.Reason,
			}}
		}
		if err != nil && hub == nil {
			return nil, err
		}
		info := &HubInfo{
			Name:      hub.Name,
			Desc:      hub.Desc,
			Addr:      []string{addr},
			KeyPrints: hub.KeyPrints,
			Enc:       hub.Encoding,
			Server: &Software{
				Name:    hub.Server.Name,
				Version: hub.Server.Version,
				Ext:     hub.Ext,
			},
			Users:    len(hub.Users),
			UserList: make([]HubUser, 0, len(hub.Users)),
			Redirect: hub.Redirect,
		}
		if info.Desc == "" {
			info.Desc = hub.Topic
		}
		if hub.Redirect != "" {
			if uri, err := dc.NormalizeAddr(hub.Redirect); err == nil && uri != addr {
				info.Addr = append([]string{uri}, info.Addr...)
			}
		}
		if hub.Addr != "" {
			if uri, err := nmdcp.NormalizeAddr(hub.Addr); err == nil && uri != addr {
				info.Addr = append(info.Addr, uri)
			}
		}
		for _, a := range hub.Failover {
			uri, err := nmdcp.NormalizeAddr(a)
			if err == nil {
				info.Addr = append(info.Addr, uri)
			}
		}

		for _, u := range hub.Users {
			info.Share += uint64(u.ShareSize)
			user := HubUser{
				Name:  string(u.Name),
				Share: u.ShareSize,
				Email: u.Email,
				Client: &Software{
					Name:    u.Client.Name,
					Version: u.Client.Version,
				},
			}
			if u.Flag&nmdcp.FlagTLS != 0 {
				user.Client.Ext = append(user.Client.Ext, "TLS")
			}
			if u.Flag&nmdcp.FlagIPv4 != 0 {
				user.Client.Ext = append(user.Client.Ext, adcp.FeaTCP4.String())
			}
			if u.Flag&nmdcp.FlagIPv6 != 0 {
				user.Client.Ext = append(user.Client.Ext, adcp.FeaTCP6.String())
			}
			info.UserList = append(info.UserList, user)
		}
		return info, err
	case adcSchema, adcsSchema:
		hub, err := adc.Ping(ctx, addr, *conf)
		if err != nil && hub == nil {
			return nil, err
		}
		info := &HubInfo{
			Name:      hub.Name,
			Desc:      hub.Desc,
			Addr:      []string{addr},
			KeyPrints: hub.KeyPrints,
			Enc:       "utf-8",
			Website:   hub.Website,
			Network:   hub.Network,
			Owner:     hub.Owner,
			Uptime:    uint64(hub.Uptime),
			UsersLimit: int(hub.UsersLimit),
			MinSlots:   int(hub.MinSlots),
			MinShare:   uint64(hub.MinShare),
			MaxHubsUser: int(hub.MaxHubsUser),
			MaxHubsReg:  int(hub.MaxHubsReg),
			MaxHubsOp:  int(hub.MaxHubsOp),
			Server: &Software{
				Name:    hub.Application,
				Version: hub.Version,
				Ext:     hub.Ext,
			},
			Users:    len(hub.Users),
			UserList: make([]HubUser, 0, len(hub.Users)),
		}
		for _, u := range hub.Users {
			u.Normalize()
			info.Files += uint64(u.ShareFiles)
			info.Share += uint64(u.ShareSize)
			user := HubUser{
				Name:  string(u.Name),
				Ip4:  string(u.Ip4),
				Files: u.ShareFiles,
				Slots: u.Slots,
				Upload: string(u.MaxUpload),
				HubsUse: u.HubsNormal,
				HubsReg: u.HubsRegistered,
				HubsOp: u.HubsOperator,
				Share: uint64(u.ShareSize),
				Type: int(u.Type),
				Desc: u.Desc,
				Email: u.Email,
				Client: &Software{
					Name:    u.Application,
					Version: u.Version,
				},
			}
			for _, f := range u.Features {
				user.Client.Ext = append(user.Client.Ext, f.String())
			}
			info.UserList = append(info.UserList, user)
		}
		return info, err
	default:
		return nil, fmt.Errorf("unsupported protocol: %q", addr)
	}
}

type HubInfo struct {
	Name      string    `json:"name" xml:"Name,attr"`
	Desc      string    `json:"desc,omitempty" xml:"Description,attr,omitempty"`
	Addr      []string  `json:"addr,omitempty" xml:"Address,attr,omitempty"`
	KeyPrints []string  `json:"kp,omitempty" xml:"KP,attr,omitempty"`
	Icon      string    `json:"icon,omitempty" xml:"Icon,attr,omitempty"`
	Owner     string    `json:"owner,omitempty" xml:"Owner,attr,omitempty"`
	Website   string    `json:"website,omitempty" xml:"Website,attr,omitempty"`
	Network   string    `json:"network,omitempty" xml:"Network,attr,omitempty"`
	Email     string    `json:"email,omitempty" xml:"Email,attr,omitempty"`
	Enc       string    `json:"encoding,omitempty" xml:"Encoding,attr,omitempty"`
	Server    *Software `json:"soft,omitempty" xml:"Software,omitempty"`
	Uptime    uint64    `json:"uptime,omitempty" xml:"Uptime,attr,omitempty"`
	Users     int       `json:"users" xml:"Users,attr"`
	Files     uint64    `json:"files,omitempty" xml:"Files,attr,omitempty"`
	Share     uint64    `json:"share,omitempty" xml:"Shared,attr,omitempty"`
	Redirect  string    `json:"redirect,omitempty" xml:"Redirect,attr,omitempty"`
	UsersLimit int      `json:"userlimit,omitempty" xml:"UserLimit,attr,omitempty"`
	MinSlots   int      `json:"minslots,omitempty" xml:"MinSlots,attr,omitempty"`
	MinShare  uint64    `json:"minshare,omitempty" xml:"MinShare,attr,omitempty"`
	MaxHubsUser int     `json:"maxhubsuse,omitempty" xml:"MaxHubsUse,attr,omitempty"`
	MaxHubsReg  int     `json:"maxhubsreg,omitempty" xml:"MaxHubsReg,attr,omitempty"`
	MaxHubsOp   int     `json:"maxhubsop,omitempty" xml:"MaxHubsOp,attr,omitempty"`
	UserList  []HubUser `json:"userlist,omitempty" xml:"User,attr,omitempty"`
}

type HubUser struct {
	Name   string    `json:"name" xml:"Name,attr"`
	Client *Software `json:"soft,omitempty" xml:"Software,omitempty"`
	Ip4    string    `json:"ip4,omitempty" xml:"IP4,attr,omitempty"`
	Files  int       `json:"files,omitempty" xml:"Files,attr,omitempty"`
	Slots  int       `json:"slots,omitempty" xml:"Slots,attr,omitempty"`
	Upload string    `json:"upload,omitempty" xml:"Upload,attr,omitempty"`
	HubsUse int      `json:"hubsuse,omitempty" xml:"HubsUse,attr,omitempty"`
	HubsReg int      `json:"hubsreg,omitempty" xml:"HubsReg,attr,omitempty"`
	HubsOp int       `json:"hubsop,omitempty" xml:"HubsOp,attr,omitempty"`
	Share  uint64    `json:"share,omitempty" xml:"Shared,attr,omitempty"`
	Type   int       `json:"type,omitempty" xml:"Type,attr,omitempty"`
	Desc   string    `json:"desc,omitempty" xml:"Description,attr,omitempty"`
	Email  string    `json:"email,omitempty" xml:"Email,attr,omitempty"`
}

// Software version.
type Software struct {
	Name    string   `json:"name" xml:"Name,attr"`
	Version string   `json:"vers,omitempty" xml:"Version,attr,omitempty"`
	Ext     []string `json:"ext,omitempty" xml:"Ext,attr,omitempty"`
}
