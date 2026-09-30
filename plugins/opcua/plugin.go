package opcua

import (
	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/plugins/opcua/internal"
)

func EnablePlugin() {
	driverbox.EnablePlugin(internal.ProtocolName, new(internal.Plugin))
}
