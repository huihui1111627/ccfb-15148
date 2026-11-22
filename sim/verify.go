package sim

import (
	"fmt"
	"sync/atomic"

	"txmq/mq"
)

// Report 验收报告:订单库 vs 下游消费者,两边必须完全一致。
type Report struct {
	CommittedOrders int `json:"committedOrders"`

	PerConsumerReceived map[string]int `json:"perConsumerReceived"`

	// 不一致明细(都应为空)
	Missing             map[string][]string `json:"missing"`             // 消费者 -> 订单库有但没收到的
	Extra               map[string][]string `json:"extra"`               // 消费者 -> 收到但订单库没有的
	RolledBackDelivered []string            `json:"rolledBackDelivered"` // 已回滚却被投递的
	CommittedNotInDB    []string            `json:"committedNotInDB"`    // MQ 已提交但订单库没有的
	DBNotMessaged       []string            `json:"dbNotMessaged"`       // 订单库有但 MQ 未提交的

	Consistent bool `json:"consistent"`

	// 回查开销统计
	CheckRequests int64 `json:"checkRequests"`
	CheckCommit   int64 `json:"checkCommit"`
	CheckRollback int64 `json:"checkRollback"`
	CheckUnknown  int64 `json:"checkUnknown"`
	CheckErrors   int64 `json:"checkErrors"`
	PolicyApplied int64 `json:"policyApplied"`
	ProducerCrash int64 `json:"producerCrashes"`
	ServerRestart int64 `json:"serverRestarts"`
	Duplicates    int64 `json:"duplicateDeliveries"`
}

func Verify(db *OrderDB, srv *mq.Server, consumers []*Consumer, rec *mq.Recorder, crashes int64) *Report {
	rep := &Report{
		Missing:             map[string][]string{},
		Extra:               map[string][]string{},
		PerConsumerReceived: map[string]int{},
	}
	committed := db.CommittedIDs()
	rep.CommittedOrders = len(committed)

	// 1) 每个消费者收到的集合必须 == 订单库已提交集合
	for _, c := range consumers {
		got := c.Received()
		rep.PerConsumerReceived[c.Name] = len(got)
		for id := range committed {
			if !got[id] {
				rep.Missing[c.Name] = append(rep.Missing[c.Name], id)
			}
		}
		for id := range got {
			if !committed[id] {
				rep.Extra[c.Name] = append(rep.Extra[c.Name], id)
			}
		}
		rep.Duplicates += c.Dups
	}

	// 2) MQ 侧状态与订单库交叉核对
	gotAny := map[string]bool{}
	for _, c := range consumers {
		for id := range c.Received() {
			gotAny[id] = true
		}
	}
	for _, tx := range srv.Txs() {
		switch tx.State {
		case mq.StateCommitted:
			if !committed[tx.ID] {
				rep.CommittedNotInDB = append(rep.CommittedNotInDB, tx.ID)
			}
		case mq.StateRolledBack:
			if gotAny[tx.ID] {
				rep.RolledBackDelivered = append(rep.RolledBackDelivered, tx.ID)
			}
		}
	}
	for id := range committed {
		if srv.StateOf(id) != mq.StateCommitted {
			rep.DBNotMessaged = append(rep.DBNotMessaged, id)
		}
	}

	rep.Consistent = len(rep.Missing) == 0 && len(rep.Extra) == 0 &&
		len(rep.RolledBackDelivered) == 0 && len(rep.CommittedNotInDB) == 0 &&
		len(rep.DBNotMessaged) == 0

	rep.CheckRequests = atomic.LoadInt64(&rec.CheckRequests)
	rep.CheckCommit = atomic.LoadInt64(&rec.CheckCommit)
	rep.CheckRollback = atomic.LoadInt64(&rec.CheckRollback)
	rep.CheckUnknown = atomic.LoadInt64(&rec.CheckUnknown)
	rep.CheckErrors = atomic.LoadInt64(&rec.CheckErrors)
	rep.PolicyApplied = atomic.LoadInt64(&rec.PolicyApplied)
	rep.ProducerCrash = crashes
	rep.ServerRestart = atomic.LoadInt64(&rec.Restarts)
	return rep
}

func (r *Report) String() string {
	s := fmt.Sprintf(`==================== 验收报告 ====================
订单库已提交订单:        %d
`, r.CommittedOrders)
	for name, n := range r.PerConsumerReceived {
		s += fmt.Sprintf("消费者 %-12s 收到: %d\n", name, n)
	}
	s += fmt.Sprintf("重复投递(已被幂等去重): %d\n", r.Duplicates)
	if r.Consistent {
		s += "一致性: ✅ 订单库与下游完全一致,没有多出也没有缺少\n"
	} else {
		s += "一致性: ❌ 发现不一致!\n"
		for c, ids := range r.Missing {
			s += fmt.Sprintf("  消费者 %s 缺少 %d 条: %v\n", c, len(ids), ids)
		}
		for c, ids := range r.Extra {
			s += fmt.Sprintf("  消费者 %s 多出 %d 条: %v\n", c, len(ids), ids)
		}
		if len(r.RolledBackDelivered) > 0 {
			s += fmt.Sprintf("  已回滚却被投递: %v\n", r.RolledBackDelivered)
		}
		if len(r.CommittedNotInDB) > 0 {
			s += fmt.Sprintf("  MQ 已提交但订单库没有: %v\n", r.CommittedNotInDB)
		}
		if len(r.DBNotMessaged) > 0 {
			s += fmt.Sprintf("  订单库有但 MQ 未提交: %v\n", r.DBNotMessaged)
		}
	}
	s += fmt.Sprintf(`-------------------- 回查开销 --------------------
生产者实例崩溃:          %d 次
服务端重启:              %d 次
回查请求总数:            %d 次
  结果=提交:             %d
  结果=回滚:             %d
  结果=未知(稍后再查):   %d
  失败/无存活实例:       %d
超过上限按策略处理+告警: %d 次
=================================================
`, r.ProducerCrash, r.ServerRestart, r.CheckRequests,
		r.CheckCommit, r.CheckRollback, r.CheckUnknown, r.CheckErrors, r.PolicyApplied)
	return s
}
