# 点位 offset 实现与验证

日期：2026-11-02。工作区基线提交：`c450085`。本次未提交或发布新版本，未修改 go.mod/go.sum，也未修改网关仓库。

## 行为约定

- 可选 `offset` 为有限 JSON 数值，允许正、负、小数；缺省为 0。显式 null、字符串、布尔、数组、对象和非有限/溢出值报错，不做宽松类型转换。
- 不使用 Lua 设备驱动时，读取为 `raw * scale + offset`，之后执行既有 decimals 截断；写入为 `(engineering - offset) / scale`，再交给协议编码。保留 scale 缺省/为 0 时不缩放的既有含义。
- 非零偏移仅允许 int/float 数值点；String、布尔、线圈/离散输入和位提取点不接受非零偏移。显式 offset 为 0 时不收紧原有 scale、类型转换和取整语义。
- 非零偏移使用标准库 math/big 的有理数运算，避免整数输入先转 float64 而损失 >2^53 的精度，也避免简单十进制参数产生的浮点计算毛刺。offset/scale 配置系数本身按 float64 表示，浮点工程值仍受 float64 精度限制。
- valueType=int 的非零偏移点要求最终工程值为 int64 整数；写入整数 rawType 要求逆变换结果为整数并在目标类型范围内。拒绝小数、负数写无符号类型、越界或非有限结果，不四舍五入。offset 缺省/为 0 的旧点位保持原有取整行为。
- 采样出错只跳过对应点位，不导出原始值，不覆盖旧 Shadow；同批其他合法点位继续导出。变化过滤在换算后执行，合法零值可以覆盖旧值。
- 公共 WritePoint/WritePoints API 接收工程值，单点和批量写入统一逆变换。本次写入中的换算失败会在编码/发送前返回，不产生部分写请求。协议 Encode 接收的仍是协议侧原始值，仅作必要的整数/范围校验，不重复应用偏移。
- device.driverKey 启用时，Lua 设备驱动拥有 scale/offset 的换算职责；框架不自动叠加。字段类型和适用点位约束仍会校验。
- Modbus 初始化在创建连接、监听器和调度任务前预校验所有绑定设备的显式 offset，包括 W 点；失败拒绝所属连接，其他连接继续初始化。其他协议通过公共读写路径进行校验，不宣称所有插件已具备初始化错误传播。

## 修改文件

| 路径 | 作用 |
|------|------|
| pkg/config/point_offset.go | Offset 获取、严格类型/适用点位校验、安全的有效 scale 获取 |
| pkg/convutil/offset.go | 公共正逆变换、精确整数运算、目标范围校验 |
| driverbox/export.go | 采集接入，按点隔离偏移错误，保留既有零偏移路径 |
| internal/core/common.go | 单点/批量控制接入；复制值切片，保留调用方工程值供写后校验使用 |
| internal/cache/cache.go | 缓存配置检查识别新字段，避免非零偏移的非法 scale 先触发旧类型断言 panic |
| plugins/modbus/internal/connector.go、adapter.go | 配置及直接协议编码入口校验 |
| plugins/modbus/internal/string_config.go、plugin.go | 原连接级 String 预校验扩展为 String/offset 预校验 |
| internal/core/batch_send.go | 修复已有 fmt.Errorf 占位符数量错误，以便正常运行 core 的默认 vet 和测试 |
| pkg/config/point_offset_test.go、pkg/convutil/offset_test.go | 参数、运算、精度和边界测试 |
| driverbox/export_offset_test.go | 最终 ExportTo/Shadow、变化过滤、零值、错误隔离和恢复测试 |
| internal/core/offset_integration_test.go | 公共单点/批量写入、输入切片不变、Lua 不重复换算测试 |
| internal/cache/point_offset_test.go | 非法新配置不会触发旧 scale 类型断言 panic |
| plugins/modbus/internal/offset_test.go | 正向虚拟从站、反向 net.Pipe 实际报文、初始化及控制边界测试 |
| plugins/modbus/internal/string_config_test.go | 使用扩展后的连接预校验函数名 |
| pages/src/content/docs/guides/device-config.mdx、concepts/library.mdx、plugins/modbus.mdx | 字段、顺序、读写示例、Lua 和兼容性说明 |
| CHANGELOG.md | 未发布功能说明 |

## 已执行验证

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -race -count=1 -timeout 90s -skip '^(TestReverseConnector_(HandshakeAndCollect|KickOffStaleConnection)|TestModbusStringIntegration|TestReverseTLSIntegration)$' ./plugins/modbus/... ./driverbox/... ./internal/core/... ./internal/cache/... ./pkg/config/... ./pkg/convutil/...
GOCACHE=/tmp/driverbox-string-build-cache go vet -mod=readonly ./plugins/modbus/... ./driverbox/... ./internal/core/... ./internal/cache/... ./pkg/config/... ./pkg/convutil/...
GOCACHE=/tmp/driverbox-string-build-cache go build -mod=readonly -o /tmp/driverbox-offset .
git diff --check
```

以上全部通过。最终 race 回归耗时约：Modbus internal 10.5 秒、driverbox 3.1 秒、core 3.0 秒，其余包约 1 秒。涉及全局缓存、Shadow、Exporter、Lua 的集成用例沿用子进程隔离。

测试覆盖温度 raw=400 → 0℃、写入 25℃ → raw=650；只偏移、负偏移、小数偏移、负 scale、合法空/零工程值、默认/零 scale；>2^53 和 int64/uint64 边界；小数/范围错误不发请求；批量中后一个点失败也不发送前一个点；旧点位默认/零 offset 保持整数精度；Lua 拥有读写换算；正反向最终导出一致，反向实际 FC06/FC16 帧与零值写后采集一致。

曾尝试不跳过 TCP 用例的完整 Modbus race 测试，但本轮环境的 `listen tcp 127.0.0.1:0` 返回 `socket: operation not permitted`。上方 skip 精确排除了既有需要 TCP listener 的顶层用例；这部分是权限限制，不能记为通过。新增 offset 的虚拟从站和 net.Pipe 集成、原有内存 TLS 握手与模拟 PTY RTU 回归均实际运行通过。

本次未运行文档站构建，当前 pages/node_modules 未安装。没有厂家设备，厂家联调未执行。

## 兼容性边界

未配置 offset 或配置为数值 0 的点位沿用原有转换，不改变 Modbus 字节序、分组、连接、并发锁或重连策略；写入值切片复制用于保留正确的工程值，不改变已计算的原始写入值。

过去作为无关 Map 字段被忽略的 offset 现在会生效并接受校验；已有点表如果自行使用同名字段，应检查含义。新增的严格整数写入规则只针对非零 offset，未统一收紧旧点位的取整策略。身份探针和 String codec 不应用数值偏移。

网关若使用固定 PointConfig 结构，需要另行增加 offset 字段、透传及校验，并升级锁定 driver-box 版本；本次工作不代表网关已完成适配。

## Review 后续调整

针对本次 review 的五项意见：

1. 采用大小写不敏感的 COIL/DISCRETE_INPUT 偏移约束，并新增混合/小写用例。这是公共 offset 校验的防御性修复，不代表 Modbus 插件新增对小写寄存器区名称的支持。
2. 将 big.Rat 的链式原地运算改为逐步 Sub/Quo、Mul/Add。原算法没有求值顺序错误，拆分后更易阅读，计算顺序和结果不变。
3. 为 float64 和各标准整数类型新增直接解析快路径，保留 NaN/Inf 拒绝规则；其他类型沿用 JSON 兜底。使用独立的兜底解码变量，避免其地址逃逸使快路径也发生堆分配。新增与旧 JSON 解析结果对照测试及 Point.Offset 基准测试，不引入配置缓存或失效同步问题。
4. 删除 offsetNumber 的不可达 float32 switch 分支，保留入口处 float32 → float64 的转换，并增加注释与精度回归。原始 float32 测量值转换为 float64 不会增加测量精度，但会保留它实际代表的二进制值；直接以 float32 的最短字符串计算会改变数值，例如 float32(1e20) 实际约为 1.0000000200408773e20。配置参数的 float32 JSON 解析语义仍保持原样。
5. 保留已完成的 fmt.Errorf 占位符修复，没有进一步修改。

基准命令：

```bash
GOCACHE=/tmp/driverbox-string-build-cache go test -mod=readonly -run '^$' -bench '^BenchmarkPointOffset$' -benchmem -benchtime=200ms ./pkg/config
```

同机单次对照结果如下，属于局部参数读取基准，不代表整条采集链路的提速比例：

| Point.Offset 配置 | 修改前 ns/op | 修改后 ns/op | 修改前 B/op、allocs/op | 修改后 B/op、allocs/op |
|-------------------|-------------|-------------|-----------------------|-----------------------|
| offset=0，float64 | 317.3 | 6.23 | 160、3 | 0、0 |
| offset/scale 为 float64 | 749.7 | 62.74 | 320、6 | 0、0 |
| offset/scale 为 int | 622.2 | 61.27 | 320、6 | 0、0 |

Review 修改后重新执行上方同范围 race 回归、go vet、主程序构建（输出 `/tmp/driverbox-offset-review`）和 diff 检查。TCP listener 用例仍按上述环境限制排除。
