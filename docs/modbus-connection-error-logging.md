# Modbus 连接复用与错误日志

状态：已应用到当前源码。补丁基线为 driver-box v2.2.2；本次未调整网关依赖或运行程序。

## 连接策略

普通 tcp、rtuovertcp 和 rtu 连接在请求成功后保持打开。收到完整且已识别的 Modbus 异常响应时，
本次请求仍返回原错误，保留连接供后续地址段或请求使用，不再因某段地址非法关闭整个连接。
支持的异常为非法功能、非法地址、非法值、设备故障、确认、设备忙、存储奇偶校验错误、
网关路径不可用及网关目标无响应；客户端本地参数校验错误 `ErrUnexpectedParameters` 同样保留连接。
分类使用 `errors.Is`，支持包装错误。

EOF、网络读写错误、超时、CRC/短帧、站号/事务号错误、协议错误及其他未识别错误均关闭连接，
下次请求重新建立连接。即使超时后底层连接可能仍存活，也保守关闭，避免迟到响应影响后续请求。
对端主动关闭连接时，本次请求可能报错，后续请求才重新连接；本补丁不增加透明重发，保持既有重试策略。

写请求通过延迟闭包读取最终错误，修复原 `defer` 提前捕获空错误而遗漏写失败的情况。
读写使用同一分类策略，不改变错误类型或将异常响应当作写成功。
停机标志使用原子访问；Close 等待当前请求释放连接锁后关闭资源，重复关闭不再重复释放，关闭后拒绝重新打开。
审查补充了挂载真实采集任务的并发关闭测试，复现调度器 `Future.Disable` 启停标志的数据竞争；已改为原子读取及交换，保证并发停用操作仅执行一次。不调整任务周期或请求分类策略。
不修改 reverse_tcp 连接器，不增加配置或依赖。

## 日志字段

打开失败日志 `open modbus client error` 和请求失败日志均补充连接标识，打开失败不再输出整个配置对象。

| 字段 | 含义 |
| --- | --- |
| `connectionKey` | 配置中的连接标识 |
| `mode` | 连接模式，如 tcp、rtu、rtuovertcp |
| `connectionAddress` | 配置中的网络端点或串口路径 |
| `error` | 原错误文本 |
| `connectionAction` | 请求失败后的动作：keep 或 close；打开失败不带此字段 |

请求失败保留连接时输出 ERROR `modbus request failed, connection kept`；
需要关闭时输出 ERROR `modbus client error, will close it`。成功路径不增加日志。
已打开连接的读写请求失败日志还包含 `operation`（read/write）、`unitID`、`registerType`、`address`、`quantity`。
`address` 为实际传给客户端的零基地址，不是设备手册的 4xxxx 编号；
`quantity` 按寄存器表类型表示寄存器数或线圈/离散输入位数。
打开失败没有已执行请求，不补造站号或请求地址；批量请求不推断某一个设备或点位。

示例字段（连接、站号及地址均为示意）：

```json
{
  "connectionKey": "pcs-link",
  "mode": "tcp",
  "connectionAddress": "192.0.2.10:502",
  "error": "illegal data address",
  "connectionAction": "keep",
  "operation": "read",
  "unitID": 7,
  "registerType": "HOLDING_REGISTER",
  "address": 123,
  "quantity": 2
}
```

## 当前仓库复验

2026-11-18 应用补丁后，在 `GOWORK=off GOPROXY=off GOCACHE=/tmp/driver-box-go-cache` 环境执行并通过：

```bash
go test -mod=readonly -race -count=1 -timeout=120s \
  -skip '^(TestReverseConnector_HandshakeAndCollect|TestReverseConnector_KickOffStaleConnection|TestReverseTLSIntegration|TestModbusStringIntegration)$' \
  ./plugins/modbus/... ./pkg/crontab
go vet -mod=readonly ./plugins/modbus/... ./driverbox/... ./pkg/crontab
go build -mod=readonly ./plugins/modbus/... ./driverbox/... ./pkg/crontab
```

已单独执行上述 4 个集成测试，当前环境因 `listen tcp 127.0.0.1:0: socket: operation not permitted` 失败；String 初始化子用例也会因合法反向连接无法监听而失败。排除后的 race 回归不能代替完整网络集成回归或真实设备验证。

## 补丁随附验证记录

以下为补丁提供方在独立源码副本中的验证记录，网关构建与测试结果未经本次操作复验。

在模块根目录执行：

```bash
GOWORK=off GOPROXY=off GOCACHE=/tmp/zggatewayv2-go-cache go test -mod=readonly -race -count=1 -timeout 120s ./plugins/modbus/internal -run '^(TestConnector.*|TestCollectDiagnostics.*|TestModbusReadWriteEnums|TestModbusInvalidReadWrite|TestModbusEnumReadGrouping|TestModbusMaxReadGap|TestModbusReadGroupsAvoidUnreadableGaps|TestModbusWriteOnlyReadTasks|TestModbusSkipWriteOnlyInBatchRead)$'
```

上述范围通过。回退为旧关闭判断及旧写入 defer 后，新增复用与写错误回归复现失败。
覆盖合法异常后继续读取另一段、成功请求复用、包装错误分类、传输故障后下次请求重连、
单/多线圈与寄存器写最终错误、打开失败再试、重复/并发关闭及关闭等待当前请求和禁止重开。
只替换外部客户端 I/O，使用真实连接器的读写、分类、锁和生命周期，不替换被验证的判断。
另通过临时 modfile 将网关指向此补丁源码副本，网关全工程只读构建及 config/driverbox-modbus/control 三个包的 race 测试通过。
未修改网关正式依赖，未执行上游全模块测试，也未连接真实 Modbus 服务或硬件。
