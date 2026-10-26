# Modbus 多寄存器 String 实现与验证记录

验证日期：2026-10-23。实施仓库为 `github.com/orglibs/driver-box/v2`，基线标签 `v2.1.5`、提交 `57ae9c8`。本次是源码、测试和文档改动，尚未创建发布版本或提交；版本号由维护者确定。未修改模块缓存、`go.mod`、`go.sum`，未升级网关依赖。

## 修改文件与行为

| 文件（仓库相对路径） | 变化 |
|---|---|
| `plugins/modbus/internal/string_codec.go` | 新增仅依赖标准库的文本规范化和寄存器编解码；严格 UTF-8、清理首尾 NUL/ASCII 空格、安全处理相邻寄存器交换及奇数长度、完整容量补零 |
| `plugins/modbus/internal/string_config.go` | 独立内部配置辅助函数：严格解析 quantity、校验 String 类型/权限/数值属性/物理地址、计算有效读取上限、逐连接预检并记录上下文 |
| `plugins/modbus/internal/read_window.go` | 正反向共用的宽整数窗口边界检查；返回只读窗口 |
| `plugins/modbus/internal/model.go` | 保留 `Quantity uint16` 的 `json:"-"`，说明 String 显式解析和数值固定宽度职责 |
| `plugins/modbus/internal/connector.go` | String 使用配置长度并在数值反转前分流；数值反转使用私有副本；分组使用 uint32，寄存器合并上限不超过 125 |
| `plugins/modbus/internal/reverse_connector.go` | 同步读取、分组和窗口隔离；ASCII 探针和配置身份统一规范化；拒绝空值、非法 UTF-8 和重复身份；保护 FC16 长度及地址范围 |
| `plugins/modbus/internal/adapter.go` | String 写入复用公共 codec，取消整体反转；正向 FC16 合并限制为 123 个寄存器 |
| `plugins/modbus/internal/plugin.go` | 在创建客户端、反向监听器和任务前检查绑定的 R/W/RW String 点位；非法连接不进入连接池，其他连接继续初始化 |
| `plugins/modbus/internal/string_codec_test.go` | 固定顺序向量、填充/空白/UTF-8、容量、输入不变性、各交换组合往返测试 |
| `plugins/modbus/internal/string_config_test.go` | 缺省/非法数量、类型和权限、数值属性、地址末端、数值兼容、批量上限及正反向分组测试 |
| `plugins/modbus/internal/string_identify_test.go` | ASCII 身份清理、唯一性、大小写、非法编码、空值、hex 原规则、失败后关闭待识别连接 |
| `plugins/modbus/internal/string_integration_test.go` | 子进程隔离的 TCP、RTU over TCP、反向 TCP 实际帧/Shadow/ExportTo/控制/重连测试 |
| `plugins/modbus/internal/string_rtu_linux_test.go` | Linux 伪终端上的实际 RTU 串口读取路由、CRC、16 寄存器中文 ESN 和最终导出测试 |
| `pages/src/content/docs/plugins/modbus.mdx` | 字段、16/24 寄存器示例、长度、字节顺序、编码、读写合并、错误及迁移说明 |
| `CHANGELOG.md` | 未发布功能记录与兼容性变化 |
| `docs/modbus-string-validation.md` | 本验证记录 |

新增 `string_config.go` 和 `read_window.go` 用于隔离内部辅助逻辑，正反向复用；未调整公开 API 或全局数值加工算法。

## 已覆盖的关键行为

- String 缺省长度为 1，显式数量严格解析；只读 1–125、可写 1–123。拒绝 0/null、负数、小数、字符串、布尔、数组和溢出值。输入寄存器只读，线圈/离散输入禁止 String；`valueType` 必须为 string，数值属性只允许缺省或数值 0。
- 测试完整覆盖设计中的七组顺序向量，另含 UTF-8 跨寄存器边界、非法/不完整 UTF-8、中间 NUL、首尾交替填充、其他空白、全空和输入不变性。1/3/16/24/123 寄存器的四种交换组合满足往返恒等式。
- TCP、RTU over TCP 和反向 TCP 使用可控从站检查实际请求地址/数量，以及最终 Shadow/ExportTo；读取软件版本 24 寄存器、ESN 16 寄存器，覆盖四种交换组合及文本规范化样例。Linux RTU 通过伪终端验证真实串口读取路由和帧 CRC。
- 同一组中的 String、重叠 Uint32、位提取和重叠 String 不受点表顺序影响；旧 Uint32 语义仍得到 `0x43444142`。非法 UTF-8 和窗口错误不丢弃其他点、不覆盖旧 Shadow、不关闭有效反向会话；相同文本按 change 过滤，非空到空值会导出，后续合法采样可恢复。
- 反向设备主动连入并完成 ESN 身份识别；两台设备使用相同 unitID，文本和会话保持隔离；重连只替换对应旧会话。身份探针长度独立于普通点位批量上限，配置身份不原地修改。保留现有 source_ip、single_device 测试并增加 hex 匹配回归。
- String 控制经过实际公共写入口和真实 FC06/FC16 帧；验证四种交换组合、奇数字节补零、短文本清除残留、空字符串、MultiWrite 强制 FC16、非法/超长文本不发送请求、单点 123 寄存器及合并上限。小于单点长度的 batchWriteLen 不拆分 String。
- 正反向分组验证配置上限、寄存器 125 上限、末端 65536 和线圈不受 125 上限影响；实际从站请求覆盖地址 65411/长度 125 和地址 65535/长度 1。只写 String 不受 batchReadLen 限制；初始化拒绝错误 String 连接，合法连接继续启动。
- 现有 HoldingRegisterEncoding、HoldingRegisterBitLogic、反向会话读改写、控制避让、并发锁和重连测试包含在完整 Modbus 回归中。

## 实际命令及结果

在模块根目录执行：

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -race -count=1 -timeout 90s ./plugins/modbus/...
```

**通过。** 最后一轮生产代码变更后的完整结果为 Modbus internal 包 `ok`（约 6 秒）；随后仅将 quantity 测试的预期值改为独立的显式常量，并再次执行以下测试，通过：

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -race -run '^TestStringPointQuantity$' -count=1 ./plugins/modbus/internal
```

TCP listener 在沙箱内首次被 `socket: operation not permitted` 拒绝；完整通过结果来自授权允许本机监听的环境。权限拒绝没有计为协议失败或测试通过。

```bash
GOCACHE=/tmp/driverbox-string-build-cache go vet -mod=readonly ./plugins/modbus/...
GOCACHE=/tmp/driverbox-string-build-cache go build -mod=readonly -o /tmp/driverbox-string-driver-box .
git diff --check
```

**通过。** 编译产物放在临时目录，未写入仓库。

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -race -count=1 -timeout 90s ./driverbox/... ./internal/core/... ./internal/shadow/... ./pkg/convutil/...
```

**整体失败，存在两项基线阻塞。** `driverbox`、`driverbox/plugin` 和 `pkg/convutil` 测试通过；以下失败在从 HEAD 导出的独立临时副本上再次执行 `go test -mod=readonly -race -count=1 -timeout 90s ./internal/core/... ./internal/shadow/...` 后复现，均未修改：

- `internal/shadow/shadow_test.go:113`：测试调用 `DeviceShadow.SetOnlineChangeCallback`，当前接口没有该方法，测试无法编译。
- `internal/core/batch_send.go:161`：`fmt.Errorf` 包含两个 `%s`，仅传入一个参数，触发 go test 默认 vet 检查失败。

```bash
npm --prefix pages run build
```

**文档站构建未完成。** 仓库未安装 Astro，首次构建报 `astro: not found`。为避免改动仓库依赖和锁文件，将 pages 复制到临时目录，执行：

```bash
npm install --package-lock=false --no-audit --no-fund --fetch-retries=0 --fetch-timeout=15000
```

沙箱网络拒绝后，在授权环境重试；安装在现有 `sharp` 依赖处失败：预编译包下载发生 TLS 连接失败，回退源码构建又缺少 `vips/vips8` 头文件。这是文档构建环境/第三方原生依赖问题，不计作功能测试通过，也未升级 Sharp 或修改 `pages/package.json`。

## 兼容性与剩余事项

显式 String quantity 从原先被忽略变为生效；缺省仍为单寄存器。String 改用地址递增顺序，依赖旧整体反转行为的配置需迁移；正反向与 ASCII 身份统一首尾清理并拒绝非法 UTF-8，规范化后重复身份需清理。其他数值类型固定宽度、端序参数及数值加工语义保持原有定义。

自动模拟测试通过，厂家联调未执行；伪终端测试不是实际串口厂家联调。完整框架回归仍需先修复上述两个基线问题，文档站构建仍需修复现有 Sharp 构建环境。未创建发布版本，未升级 zggatewayv2；网关数量透传、字段存在性记录、预检和北向链路测试属于后续工作。
