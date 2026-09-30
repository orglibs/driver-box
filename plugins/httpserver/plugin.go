package httpserver

import (
	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/plugins/httpserver/internal"
)

func EnablePlugin() {
	driverbox.EnablePlugin(internal.ProtocolName, new(internal.Plugin))
}
