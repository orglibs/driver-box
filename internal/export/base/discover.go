package base

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/orglibs/driver-box/v2/internal/core"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
	"go.uber.org/zap"
)

func (export *Export) udpDiscover() {
	// 监听UDP端口
	port := 9090
	httpListen := os.Getenv(config.ENV_UDP_DISCOVER_LISTEN)
	if httpListen != "" {
		p, e := convutil.Int64(httpListen)
		if e != nil {
			logger.Logger.Error("udp discover listen port error", zap.String(config.ENV_UDP_DISCOVER_LISTEN, httpListen), zap.Error(e))
		} else {
			port = int(p)
		}
	}
	addr := net.UDPAddr{
		Port: port,
		IP:   net.IPv4(0, 0, 0, 0),
	}
	if err := export.startDiscover(func() (net.PacketConn, error) {
		return net.ListenUDP("udp", &addr)
	}); err != nil {
		logger.Logger.Error("UDP监听失败", zap.Error(err))
		return
	}

	logger.Logger.Info("UDP服务已启动.", zap.Int("port", port))
}

// startDiscover serializes binding with stopDiscover. Only the packet reader
// runs asynchronously; a completed stop cannot leave a listener starting later.
func (export *Export) startDiscover(listen func() (net.PacketConn, error)) error {
	export.discoverMu.Lock()
	defer export.discoverMu.Unlock()
	if export.discoverConn != nil {
		return nil
	}
	conn, err := listen()
	if err != nil {
		return err
	}
	export.discoverConn = conn
	go export.serveDiscover(conn, export.httpListen)
	return nil
}

func (export *Export) serveDiscover(conn net.PacketConn, httpListen string) {
	defer func() {
		export.discoverMu.Lock()
		if export.discoverConn == conn {
			export.discoverConn = nil
		}
		_ = conn.Close()
		export.discoverMu.Unlock()
	}()
	buffer := make([]byte, 1024)
	for {
		n, remoteAddr, err := conn.ReadFrom(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logger.Logger.Error("读取UDP数据失败", zap.Error(err))
			}
			return
		}

		data := string(buffer[:n])
		logger.Logger.Info("收到UDP数据", zap.String("data", data), zap.String("remoteAddr", remoteAddr.String()))

		// 基础验证
		if !validateRequest(data) {
			logger.Logger.Error("验证失败", zap.String("data", data))
			continue
		}

		type Resp struct {
			config.Metadata
			Port string `json:"port"`
		}
		resp := Resp{
			Metadata: core.Metadata,
			Port:     httpListen,
		}
		// 获取网关Metadata信息
		response, err := json.Marshal(resp)
		if err != nil {
			logger.Logger.Error("JSON编码失败", zap.Error(err))
			continue
		}

		// 返回响应
		_, err = conn.WriteTo(response, remoteAddr)
		if err != nil {
			logger.Logger.Error("发送响应失败", zap.Error(err))
		}
	}
}

func (export *Export) stopDiscover() {
	export.discoverMu.Lock()
	conn := export.discoverConn
	export.discoverConn = nil
	if conn == nil {
		export.discoverMu.Unlock()
		return
	}
	_ = conn.Close()
	export.discoverMu.Unlock()
	logger.Logger.Info("UDP服务已停止.")
}

// 验证请求数据
func validateRequest(data string) bool {
	// 简单验证示例：检查数据是否包含特定令牌
	return strings.Contains(data, "driver-box")
}
