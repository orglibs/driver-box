package plugins

import (
	"github.com/orglibs/driver-box/v2/plugins/bacnet"
	"github.com/orglibs/driver-box/v2/plugins/dlt645"
	"github.com/orglibs/driver-box/v2/plugins/httpclient"
	"github.com/orglibs/driver-box/v2/plugins/httpserver"
	"github.com/orglibs/driver-box/v2/plugins/iec104"
	"github.com/orglibs/driver-box/v2/plugins/modbus"
	"github.com/orglibs/driver-box/v2/plugins/mqtt"
	"github.com/orglibs/driver-box/v2/plugins/opcua"
	"github.com/orglibs/driver-box/v2/plugins/s7"
	"github.com/orglibs/driver-box/v2/plugins/tcpserver"
	"github.com/orglibs/driver-box/v2/plugins/websocket"
)

func EnableAll() {
	iec104.EnablePlugin()
	modbus.EnablePlugin()
	bacnet.EnablePlugin()
	httpserver.EnablePlugin()
	httpclient.EnablePlugin()
	websocket.EnablePlugin()
	tcpserver.EnablePlugin()
	mqtt.EnablePlugin()
	dlt645.EnablePlugin()
	opcua.EnablePlugin()
	s7.EnablePlugin()
}
