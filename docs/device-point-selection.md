# 设备级点位选择

状态：已发布（v2.2.0）。基于 v2.2.0 发行版，应用代码并重新构建后生效。

文档站入口：[设备配置](../pages/src/content/docs/guides/device-config.mdx)、[Modbus](../pages/src/content/docs/plugins/modbus.mdx)、[核心缓存 API](../pages/src/content/docs/concepts/corecache.mdx)。

模型 `devicePoints` 保持完整共享定义，每个 `devices[]` 可增加 `pointSelection`：

```json
{
  "id": "charger-01",
  "description": "充电桩",
  "connectionKey": "charger-bus",
  "properties": {"unitID": "1"},
  "pointSelection": {
    "mode": "selected",
    "names": ["common.totalPower", "gun1.voltage"]
  }
}
```

以上是 driver-box 设备字段片段，`names` 对应所属模型稳定的 `devicePoints[].name`，不是显示名称、寄存器地址或数据库 ID。

| 配置 | 行为 |
| --- | --- |
| 新配置省略 `pointSelection` | 使用模型全部点位，保留原有点位范围和顺序 |
| `{"mode":"all"}` | 使用模型全部点位；模型新增点位后跟随启用 |
| `{"mode":"selected","names":["common.totalPower"]}` | 仅启用保存的清单，模型新增点不会自动启用 |
| `{"mode":"selected","names":[]}` | 零点位，保留设备身份，任何模型点均不可通过主动读写 API 访问；不回退为全模型 |

选中只是访问的必要条件，仍须符合点位 `readWrite`：R 只读，W 只写，RW 可读写。
批量请求含未启用点时整批在发送前拒绝，不能部分发送；原请求的启用范围在 Lua 编码前校验，编码后的点名也校验。
拒绝的原始请求不修改期望写值 Shadow，也不获取物理连接器。

`selected` 必须提供 `names` 数组，省略/null 拒绝；显式空数组会在序列化和重新加载中保留。
空/未知模式、未知/重复/空白点名、歧义点名、错误字段类型、显式 null 的 `pointSelection` 均拒绝。
`all` 的 names 可省略或为空，不接受非空清单。点名精确匹配且区分大小写，不改名、不重排模型、不自动追加点位。

对已有 `Device` 执行 JSON 局部更新时，未提供的字段（包括 `pointSelection`）保留原值，设备协议属性按原有 JSON map 规则合并。
显式提供 `pointSelection` 时替换整个选择对象，`selected` 仍必须提供 `names`；恢复全部点位应使用 `{"mode":"all"}`。

## API 与实现边界

- `config.ResolvePointSelection(points, selection)`：公开的校验/解析函数，应用可复用做预检。
- `config.Model.PointsFor(device)`：模型点表结合设备选择，插件无需依赖已初始化的全局缓存即可构建采集任务。
- `config.DeviceConfig.ValidatePointSelections()`：校验整份协议配置，启动解析时先执行，再建立该配置的缓存。
- `CoreCache.GetPoints(modelName)`：仍返回模型完整定义，兼容原接口。
- 新增 `CoreCache.GetDevicePoints(deviceID)`：返回设备有效点表，空数组且 true 表示合法的零点位设备；false 表示设备不存在。
- `CoreCache.GetPointByDevice(deviceID, name)`：未启用点与不存在点均返回 false，单点/批量读写共用此边界。

设备只缓存指向共享点定义的有效点列表及名称集合，不复制模型或更改原模型标识。
`AddOrUpdateDevice` 校验后才替换选择；`AddModel` 先验证所有绑定，再一起更新模型及有效点表。
失败不改变已保存的缓存状态，配置持久化保存完整模型和每台设备的选择，清单切片在缓存边界复制。
新增 CoreCache 方法后，自定义接口实现/测试替身也需要提供该方法。

Modbus（含反向）、DL/T645、IEC104、BACnet、OPC UA 和 S7 建立轮询/监视点时使用有效点表。
TCP Server 等推送解码结果通过公共导出流程过滤；未启用值不进入新的 Shadow 点值、ExportTo 或 Exporting 点值事件。
`selected` 设备的 Lua 驱动加载或解码失败时，停止本批点值导出，不会继续发送未经选择过滤的原始值；未配置选择或使用 `all` 时保留原有异常路径。
此失败批次不写入新的 Shadow 点值、不触发 `Exporting` 点值事件，也不调用 `ExportTo`；已有 Shadow 值保留。
DoExport 等原始批次诊断事件仍保留原始输入；任意直接 Shadow 缓存读写接口不作为物理读写的权限接口，本补丁不删除历史缓存。
设备列表接口展示有效点表，模型查询接口展示完整模型。

启用 Modbus `skipWriteOnlyInBatchRead` 时，只写地址屏障仍从完整模型获得，不因取消启用 W 点而丢失。
屏障仅依赖寄存器类型、地址和长度，不受未启用点的 `bitLen`、`offset`、字节序等写入参数影响；必要的范围信息非法时，明确拒绝所属连接的初始化，不静默忽略屏障。
未启用只写避让时不额外校验未启用点的屏障；批量跨度仍受 `maxReadGap`、`batchReadLen` 和协议限制。
选择限制的是逻辑点位，不承诺物理请求跨度绝不经过未选点的地址，也不限制反向连接的身份探测寄存器。

例如，模型中 100、102 可读，101 只写，设备仅选择两侧的可读点：

| 连接配置 | 批量读取行为（其余分组条件相同且长度允许） |
| --- | --- |
| 省略 `skipWriteOnlyInBatchRead` 和 `maxReadGap` | 仍可合并读取 100～102；101 不作为逻辑采集点解码或导出 |
| `skipWriteOnlyInBatchRead: true` | 分别读取 100、102；未选择的 101 仍是只写屏障 |
| `maxReadGap: 0` | 不跨越中间空洞，分别读取 100、102 |

屏障长度按协议定义推导：线圈为 1，数值寄存器按 `rawType` 固定宽度，String 按 `quantity`。
未选择的 W 点即使存在非法 `bitLen`、`offset` 或字节序参数，也不会丢失已知的物理范围；若地址、类型或必要长度无法解析，开启避让的连接会被拒绝初始化，其他合法连接继续初始化。

更改选择后需要按现有流程重建插件连接/采集任务或重启。缓存 API 的更新不提供在途任务取消、已启动轮询任务立即重分组或历史数据清理；
本补丁不增加在线热切换接口。网关、Redis、Lua 虚拟点等上层独立业务不在本补丁实现范围内。

## 默认兼容性

省略 `pointSelection` 时，解析器直接使用完整模型点表，不增加选择模式的读写拦截或导出过滤。设备级点表查询的结果也包含全部模型点位。
这表示默认点位范围和正常采集、合法读写流程兼容，不表示公共接口和所有异常路径完全没有变化：

- `CoreCache` 接口新增 `GetDevicePoints`，外部实现和测试替身需要补齐方法。
- 单点操作现在先检查操作模式，非法模式不会再预先更新期望写值 Shadow。
- JSON 局部更新已恢复保留未提供字段及 map 合并的行为；显式选择对象按前述规则整体替换。

## 回归与验证

新增测试覆盖配置缺省/all/selected/空清单、JSON 往返、启动预检、缓存查询和并发更新、模型新增点与失败回滚、
真实公共 API 读写拦截、批量失败不发送且不改期望 Shadow、Lua 前后的点位校验入口、导出过滤，
JSON 局部更新兼容性、驱动加载/解码失败的 selected 与旧模式差异，
正反向 Modbus 分组、未启用 W 点写入参数异常时的屏障保留、非法屏障范围拒绝及默认合并行为，
DL/T645 分组和 IEC104 路由。外部 I/O 用记录连接器替代，未做真实硬件联调。

应用本补丁后建议执行：

```bash
GOWORK=off go test -mod=readonly -race -count=1 -timeout 120s ./pkg/config ./internal/cache ./internal/core ./driverbox ./plugins/modbus/internal ./plugins/dlt645/internal ./plugins/iec104/internal ./plugins/tcpserver/internal -run '^TestPointSelection'
GOWORK=off go build -mod=readonly ./driverbox/... ./internal/cache ./internal/core ./internal/export/base ./plugins/modbus/... ./plugins/dlt645/... ./plugins/iec104/... ./plugins/tcpserver/... ./plugins/bacnet/... ./plugins/opcua/... ./plugins/s7/...
```

本次上述点位选择 race 测试和相关模块构建已通过。扩大检查仍有原版本已有的 BACnet/DL/T645 测试或静态检查问题；
受限环境禁止本地 TCP 监听时，依赖该能力的集成测试也无法验证。这些结果不代表真实设备联调或全仓库检查通过。
