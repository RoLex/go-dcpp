package all

import (
	_ "github.com/RoLex/go-dcpp/hub/plugins/hubstats"
	_ "github.com/RoLex/go-dcpp/hub/plugins/myip"
	_ "github.com/RoLex/go-dcpp/hub/plugins/updates"

	// LUA is loaded the last
	_ "github.com/RoLex/go-dcpp/hub/plugins/lua"
	_ "github.com/RoLex/go-dcpp/hub/plugins/lua/px"
)
