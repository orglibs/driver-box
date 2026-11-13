# 写后回读确认：正负零与成功日志

状态：补丁已应用到当前源码，补丁基线为 driver-box v2.2.1；尚未发布或应用到实际运行程序。

## 行为

原 `tryReadNewValues` 用 `fmt.Sprint` 比较目标与回读数值。数值 0 与 -0 相等，字符串 `"0"` 与 `"-0"` 不相等，
导致负系数换算出的零值一直触发额外回读。原日志还在未确认相等时输出 `point write success, read new value`。

此次修改：

- 两边都是 Go 整数/无符号整数/浮点数（含这些底层类型的命名类型）时，使用标准库 math/big 的有理数精确比较。
- 0 与 -0 相等，不再为了符号位差异反复回读；大于 2^53 的不同整数不会因 float64 舍入而误判相等。
- 不引入浮点容差；真实数值不一致仍沿用最多 10 轮、递增等待的原回读重试。
- NaN/Inf 不作为有效确认值。其他非数值或混合类型保留原有 fmt.Sprint 比较行为，包括已有数字字符串兼容。
- 不匹配时仅输出 DEBUG `point write readback not confirmed`，再进入已有回读路径；只有匹配才输出成功确认 INFO。
- 保留后续新写入覆盖旧期望值的判断、原等待周期、写入及读取返回语义，不修改原始值、scale、Shadow 或设备数据。

没有新增第三方依赖或配置项。本补丁作用于公共写后确认链路，不局限于 Modbus；不改变 PMS 脚本的提交/保活周期。

连续写入相同值仍会更新 Shadow 的 `WriteAt`，因此仍可能触发 `point write success, but expect point value maybe expired` WARN。
该告警按写入时间判断旧期望值是否失效，与正负零的数值比较独立，本次修改保留其行为。

## 当前仓库复验

应用补丁后，以下命令均已通过，环境为 `GOWORK=off GOPROXY=off GOCACHE=/tmp/driver-box-go-cache`：

```bash
go test -mod=readonly -race -count=1 -timeout=90s ./internal/core
go vet -mod=readonly ./internal/core
go build -mod=readonly ./driverbox/... ./plugins/modbus/...
```

本次未执行网关构建或真实设备联调。

## 补丁随附的 2026-11-13 验证记录

以下为补丁随附的原始验证记录，其中网关构建使用的临时路径不属于当前仓库的复验环境。

在 v2.2.1 独立临时副本中验证，未修改模块缓存或实际 driver-box 仓库。以下检查通过，环境为
`GOWORK=off GOPROXY=off GOCACHE=/tmp/zggatewayv2-go-build`：

```bash
go test -mod=readonly -race -count=1 -timeout 90s ./internal/core
go vet -mod=readonly ./internal/core
go build -mod=readonly -buildvcs=false ./driverbox/... ./plugins/modbus/...
```

单元测试覆盖正负零、数值类型转换、命名类型、大整数精度、数值不等、无隐式容差、原有字符串/布尔/nil 比较和非有限值。
集成测试使用真实公开 WritePoint、tryReadNewValues、SendBatchRead 与 Shadow 链路，仅模拟外部 I/O，
验证一次写入、一次回读为 -0 后结束，不再发出多余回读，也不为初次不匹配输出成功确认日志。

把同一集成测试运行在原版 batch_send.go 上，实际复现 `reads=2 writes=1; want one write and one read`；
修复版本 core 包完整 race 回归通过。

网关通过 `/tmp` 临时 modfile 引用修复副本的全工程构建通过，未修改网关 go.mod/go.sum：

```bash
GOWORK=off GOPROXY=off GOCACHE=/tmp/zggatewayv2-go-build go build -mod=readonly -modfile=/tmp/driverbox-write-readback.7fkqnJ/gateway.mod ./...
```

修改的 Go 文件已运行 gofmt。没有执行 driver-box 上游全工程测试或构建，没有进行实际设备或厂家联调。
补丁正向应用/反向检查及应用后源码一致性结果见随附验证说明。
