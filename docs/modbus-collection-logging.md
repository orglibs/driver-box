# Modbus 采集日志降频

状态：已应用到当前源码，基线为 driver-box v2.2.0，尚未发布版本。

## 范围与兼容性

仅修改普通连接器（tcp、rtu、rtuovertcp）的采集诊断。reverse_tcp 使用另一连接器，其他协议、协议报文日志、写失败的返回与上层记录不变。
不修改网关或设备 JSON，不增加依赖、后台线程或公共接口。不调整 scanInterval、minInterval、分组、读写优先级、超时与重试、离线通知或读写返回语义。
包括原有写入避让返回 nil 的语义；只有真正执行读取并成功返回，才结束读失败告警状态。

## 日志规则

| 事件 | 行为 |
| --- | --- |
| 扫描处于超时退避的分组 | 不记录重复退避日志，原退避计算保持不变 |
| 周期采集实际读失败 | 每个连接/分组首次立即 ERROR；错误文本变化立即 ERROR；相同故障每 30 秒汇总 |
| 故障仍未恢复 | 扫描回调结束时检查汇总窗口；即使当前在退避中，也每 30 秒保留一条故障摘要 |
| 读取成功 | 同一分组有故障记录时输出一次 INFO，并移除该故障状态 |
| 写入避让 | 累计跳过的分组检查次数；短于 30 秒不输出日志 |
| 持续写入避让 | 任一分组从首次避让到现在达到 30 秒，按连接 WARN；此后最多每 30 秒汇总一次 |
| 写入避让结束 | 所有已记录分组都开始真实读尝试后，若此前告警，输出一次 INFO；该日志不代表读成功 |

时间窗口为内部常量 `collectLogInterval`，当前没有新增 JSON/环境变量参数。恢复日志需要 LOG_LEVEL 为 info 或 debug。
“每 30 秒”由现有扫描回调驱动，精度受扫描周期、同步 I/O 和日志输出耗时影响；不是独立定时器或严格告警时限。
故障频繁变化或大量不同分组首次失败仍会立即记录，以免采样隐藏新问题。

ERROR 摘要仅包含 connectionKey、unitID、registerType、address、quantity、pointCount、baseInterval、error、totalFailures、newFailures、failureDuration、lastFailureAge。
完整 group/Points 仅在 debug 级别输出真实读失败的详细信息，不再打印完整 connection 配置。

`totalFailures` 表示该故障期间真正失败的周期读请求总数；`newFailures` 表示上次摘要后新增的失败数。
退避期间的摘要可能出现 newFailures=0，不能把这条摘要当作新发生的超时。
恢复日志中的 unreportedFailures 保留尚未汇总的重复失败数。

`skippedGroupChecks` 表示本次写入避让期间被跳过的分组检查次数，同一分组跨扫描会重复计数，不代表实际发送失败或丢失的数据量。
deferredGroups 为尚未重新尝试读取的分组数，longestDeferral 为其中最长等待时间；unitID/address 等字段标识最长等待分组的样本。
部分分组开始读取不会清除其他分组的等待告警。

诊断状态归属于连接器，以已经配置的分组指针为键，恢复时移除；互斥锁保护显式读取与周期读取共享的诊断状态。
不引入全局采样，不抑制其他连接、协议或模块的日志。该补丁不解决设备真实超时或持续写入导致的采集拥塞。

## 回归验证

```bash
GOWORK=off go test -mod=readonly -race -count=1 -timeout 120s -run '^TestCollectDiagnostics' ./plugins/modbus/internal
GOWORK=off go test -mod=readonly -race -count=1 -timeout 120s ./plugins/modbus/...
GOWORK=off go vet -mod=readonly ./plugins/modbus/... ./driverbox/...
GOWORK=off go build -mod=readonly ./plugins/modbus/... ./driverbox/...
```

新增用例覆盖首次失败、重复汇总、退避期间摘要、错误变化、新分组/连接独立告警、恢复计数、短暂/持续写入避让、部分分组恢复与并发计数。
集成用例经过真实 collectOnce/Send/sendReadCommand 和原生虚拟 Lua I/O；验证 1000 次短暂避让不产生告警、下层避让分支保持 nil 返回、错误继续传播、真实读取成功才报告恢复。
该模拟验证不等于 TCP/串口连接或厂家硬件验证。

### 受限环境运行

当前开发环境禁止创建 TCP socket，以下 4 个顶层测试会因 `listen tcp 127.0.0.1:0: socket: operation not permitted` 失败：

- `TestReverseConnector_HandshakeAndCollect`
- `TestReverseConnector_KickOffStaleConnection`
- `TestReverseTLSIntegration`
- `TestModbusStringIntegration`（其中 initialization 子用例也会因合法反向连接无法监听而失败）

已单独执行并确认这些环境错误。排除这 4 项后，可用下列命令执行其余回归及竞态检查；该结果不能代替完整 Modbus 回归或真实设备联调：

```bash
GOWORK=off go test -mod=readonly -race -count=1 -timeout 120s \
  -skip '^(TestReverseConnector_HandshakeAndCollect|TestReverseConnector_KickOffStaleConnection|TestReverseTLSIntegration|TestModbusStringIntegration)$' \
  ./plugins/modbus/...
```
