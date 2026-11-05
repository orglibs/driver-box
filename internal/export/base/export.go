package base

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/core"
	"github.com/orglibs/driver-box/v2/internal/export/base/restful"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/event"
)

var driverInstance *Export
var once = &sync.Once{}
var srv *http.Server

// 设备自动发现插件
type Export struct {
	discoverMu   sync.Mutex
	discoverConn net.PacketConn
	ready        bool
	httpListen   string
}

func (export *Export) Init() error {
	// 检查并生成唯一码文件
	const uniqueCodeFile = ".driverbox_serial_no"
	if _, err := os.Stat(uniqueCodeFile); err == nil {
		// 文件存在则读取内容
		content, err := os.ReadFile(uniqueCodeFile)
		if err != nil {
			return fmt.Errorf("failed to read unique code file: %v", err)
		}
		core.Metadata.SerialNo = string(content)
	} else if os.IsNotExist(err) {
		// 生成UUID作为唯一码
		uniqueCode := uuid.New().String()
		if err := os.WriteFile(uniqueCodeFile, []byte(uniqueCode), 0644); err != nil {
			return fmt.Errorf("failed to write unique code file: %v", err)
		}
		core.Metadata.SerialNo = uniqueCode
	}
	//http服务绑定host
	httpListen := os.Getenv(config.ENV_HTTP_LISTEN)
	if httpListen != "" {
		export.httpListen = httpListen
	} else {
		export.httpListen = "8081"
	}

	export.ready = true
	export.registerApi()
	// Publish the listener before Init returns so failure cleanup cannot race
	// with a discovery goroutine that has not bound its socket yet.
	export.udpDiscover()
	return nil
}

func (export *Export) Destroy() error {
	export.ready = false
	export.stopDiscover()
	if srv != nil {
		_ = srv.Shutdown(context.Background())
		srv = nil
		restful.HttpRouter = httprouter.New()
		http.DefaultServeMux = http.NewServeMux()
	}
	return nil
}
func Get() *Export {
	once.Do(func() {
		driverInstance = &Export{}
	})
	return driverInstance
}

// 点位变化触发场景联动
func (export *Export) ExportTo(deviceData plugin.DeviceData) {

}

// 继承Export OnEvent接口
func (export *Export) OnEvent(eventCode event.EventCode, key string, eventValue interface{}) error {
	return nil
}

func (export *Export) IsReady() bool {
	return export.ready
}
