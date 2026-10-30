# Modbus 反向 TLS 实现与验证记录

验证日期：2026-10-30。本次为工作区代码改动，未创建发布版本或提交；不修改 go.mod/go.sum。

## 实现范围

- `plugins/modbus/internal/model.go`：增加连接参数 `serverTls`、`clientTls`，均为可选 PEM 字符串。
- `plugins/modbus/internal/reverse_tls.go`：严格解析字段；加载合并的服务端证书链和未加密私钥；加载可信客户端 CA；构造 TLS 1.2/1.3 配置；在双向模式使用 `RequireAndVerifyClientCert`；提供独立 5 秒握手超时和不输出 PEM 的连接日志。
- `plugins/modbus/internal/reverse_connector.go`：创建监听器前校验证书，先握手再识别并绑定设备；沿用原有 pending 连接追踪、会话替换、采集、写入和关闭逻辑。
- `plugins/modbus/internal/connector.go`：拒绝在非反向模式下配置上述 TLS 字段，避免配置被静默忽略。
- `plugins/modbus/internal/plugin.go`：初始化日志使用 connectionKey，避免直接打印含私钥的原始连接 Map，包括字段解析失败的路径。
- `plugins/modbus/internal/reverse_tls_test.go`、`reverse_tls_integration_test.go`：新增自动测试。
- `pages/src/content/docs/plugins/modbus.mdx`、`CHANGELOG.md`：补充字段、示例、模式判定和边界说明。

两个字段都省略时仍使用普通 TCP。仅 serverTls 时为单向 TLS，两者都提供时为强制双向 TLS；clientTls 单独提供、显式 null/空字符串/错误类型、无效 PEM 或证书与私钥不匹配均拒绝连接初始化。不回退到明文或较弱认证模式。其他连接按原有策略继续初始化，Initialize 不返回全局启动错误。

## 自动测试覆盖

- TLS 1.2 和 TLS 1.3 的单向、双向握手；客户端验证服务端 CA 和域名。
- 拒绝缺失、不可信、过期或用途错误的设备客户端证书。
- 配置缺省及类型/空值校验；在创建监听器前拒绝无效 TLS；非反向连接拒绝 TLS 字段。
- 连接结构体日志和原始配置转换失败日志不包含 PEM；无效配置不会进入连接池。
- 静默客户端握手超时；source_ip、single_device 不能绕过握手；连接器关闭能中断待握手连接。
- 三种身份识别方式在单向/双向 TLS 下读取寄存器，并实际发送 FC06/FC16 写入帧。
- 缺失/不可信/过期客户端证书及纯 TCP 客户端不能绑定或替换既有会话。
- 真实本机 TCP 监听器上的单向/双向 TLS：寄存器身份识别，String、重叠 Uint32 和位提取点通过 ExportTo/Shadow 导出，TLS 重连替换旧会话后继续采集。
- 完整原有 Modbus 回归，包括普通 TCP、反向 TCP、RTU over TCP、模拟 PTY RTU、String、数值端序、位域读改写和会话隔离。

涉及缓存、Shadow 和 Exporter 的 TLS 集成测试沿用子进程隔离。测试证书及私钥由标准库在运行时生成，不保存生产证书或示例私钥。

## 已运行的检查

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -race -count=1 -timeout 90s ./plugins/modbus/...
GOCACHE=/tmp/driverbox-string-build-cache go vet -mod=readonly ./plugins/modbus/...
GOCACHE=/tmp/driverbox-string-build-cache go build -mod=readonly -o /tmp/driverbox-reverse-tls .
git diff --check
```

以上全部通过。完整 Modbus race 测试中 internal 包耗时约 13.3 秒，真实 TCP listener 成功创建，没有跳过网络集成测试。

另外，使用用户提供的 `hw_server.crt`、`hw_server.key`、`hw_ca.crt` 在仓库外运行标准库检查：合并 PEM 可加载，服务端证书与私钥匹配，证书链可由所给 CA 验证；未打印私钥内容或将其复制到仓库。该服务端证书没有 DNS/IP SAN，现代客户端启用严格主机名校验时需要更换带匹配 SAN 的证书，未通过关闭客户端校验规避此问题。

本次未运行整个文档站构建；此前文档站构建受现有 Sharp 安装/系统 libvips 缺失影响，详见 `docs/modbus-string-validation.md`。本次未修改文档站依赖。

## 兼容性与未包含内容

未配置 TLS 的连接沿用原传输和业务流程。TLS 字段仅适用于 reverse_tcp；同一监听器不混用明文和 TLS。新增功能不改变点位编解码、数值端序、采集分组或控制规则。

证书在连接器初始化时加载，不支持证书热更新。本次双向认证只验证可信客户端证书，设备选择继续使用现有 identify；不提供证书与 ESN/deviceId 的绑定、证书签发/吊销管理或完整 Modbus Security 角色授权。

自动模拟测试通过，厂家设备 TLS 联调未执行。网关端若对 connect 参数有字段白名单或固定结构，仍需单独确认两个字段的透传；本次未修改网关仓库或升级其依赖。
