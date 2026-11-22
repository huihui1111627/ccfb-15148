# txmq — 支持事务消息的消息队列(仿真)

解决"先写库再发消息会丢消息、先发消息再写库会多出消息"的双写一致性问题,
用**半消息 + 本地事务 + 事务回查**保证订单库与下游消费者最终完全一致。

## 架构

```
下单服务(生产者组 order-service,多实例,会崩溃)
   │  1) sendHalf 半消息(持久化,对消费者不可见)
   │  2) 本地事务:订单库提交/回滚
   │  3) commit / rollback
   ▼
MQ 服务端 ── 回查 ──▶ 生产者组内任意存活实例(查本地订单库回答 commit/rollback/unknown)
   │  半消息超时未决议 → 回查;超过上限 → 按策略处理并告警
   │  commit 后消息可见,at-least-once 推送 + 消费者幂等去重
   ▼
下游消费者:库存 inventory / 积分 points / 物流 logistics
```

## 关键设计

- **半消息**:`sendHalf` 先落 WAL(fsync)再应答,对消费者不可见;`commit` 后可见;`rollback` 丢弃。
- **持久化**:半消息、提交、回滚、投递确认全部 append-only WAL + fsync(`mq/store.go`)。
  服务端重启后重放 WAL 恢复每条事务的状态:已提交未投递的继续投,已回滚的永不投递。
- **事务回查**:半消息超过 `CheckAfter` 未决议,服务端向**同一生产者组内任意存活实例**
  发起回查(原实例下线也没关系);生产者按本地订单库记录回答
  commit / rollback / unknown;unknown 稍后再查;超过 `MaxChecks` 按 `-policy` 处理并告警。
- **幂等**:所有请求按 TxID 幂等(丢包超时后安全重试);消费者按 TxID 去重(至少一次投递 + 幂等 = 效果上一次)。
- **故障注入**:网络丢包/响应丢失/延迟(`Session.DropProb/RespDropProb/MaxLatency`)、
  生产者在任意步骤崩溃(发半消息前/后、本地事务后)、订单库提交失败、MQ 服务端崩溃重启。

## 运行

```bash
go run . -orders 300            # 完整仿真 + 监控页面 http://localhost:8080
go run . -orders 120 -once      # 只跑验收,不一致时退出码非零(CI 用)
```

参数:`-crash` 生产者崩溃概率、`-drop` 网络丢包率、`-dbfail` 订单库提交失败率、
`-restarts` MQ 重启次数、`-maxchecks` 最大回查次数、`-policy` 超限策略(rollback|commit)、`-seed`。

## 验收

仿真结束后自动比对:**订单库已提交订单集合 == 每个下游消费者收到的消息集合**
(不多不少),并交叉核对 MQ 侧状态(已回滚的绝不被投递、订单库有的必然已提交)。
同时输出回查开销:回查总次数、commit/rollback/unknown 分布、失败次数、策略处理次数。

监控页面:状态分布、每条事务从发送到提交/回滚的完整时间线、回查记录、告警、验收结果。

## 代码结构

| 文件 | 说明 |
|---|---|
| `mq/proto.go` | 双向 RPC 会话(请求/响应帧 + 丢包/延迟注入) |
| `mq/store.go` | append-only WAL,重启重放恢复 |
| `mq/server.go` | MQ 服务端:半消息、决议、回查调度、投递、统计 |
| `mq/client.go` | 生产者/消费者客户端(重连、幂等重试、崩溃模拟) |
| `sim/sim.go` | 订单库、下单服务(崩溃注入)、消费者仿真 |
| `sim/verify.go` | 一致性验收与回查开销统计 |
| `main.go` | 仿真编排(流量、服务端重启注入、验收) |
| `web.go` | 监控页面 |
