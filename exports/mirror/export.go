package mirror

import (
	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/exports/mirror/internal"
	"github.com/orglibs/driver-box/v2/exports/mirror/internal/plugin"
)

// LoadMirrorExport 加载镜像设备Export插件
// 功能:
//
//	创建并加载mirror.NewExport()实例
func EnableExport() {
	plugin.EnablePlugin()
	driverbox.EnableExport(internal.NewExport())
}
