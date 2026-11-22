package mq

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type TxState string

const (
	StateHalf       TxState = "HALF"        // 半消息:已持久化,对消费者不可见
	StateCommitted  TxState = "COMMITTED"   // 已提交:对消费者可见,至少投递一次
	StateRolledBack TxState = "ROLLED_BACK" // 已回滚:丢弃,永不投递
)

type Tx struct {
	ID         string
	Group      string
	Topic      string
	Body       string
	State      TxState
	HalfTs     time.Time
	EndTs      time.Time
	Checks     int
	ResolvedBy string // producer / checkback / policy
	Delivered  map[string]bool
}

type Event struct {
	Ts     time.Time `json:"ts"`
	TxID   string    `json:"txID"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

type CheckRecord struct {
	Ts       time.Time `json:"ts"`
	TxID     string    `json:"txID"`
	Group    string    `json:"group"`
	Instance string    `json:"instance"`
	Attempt  int       `json:"attempt"`
	Result   string    `json:"result"` // commit / rollback / unknown / ""
	Err      string    `json:"err,omitempty"`
}

type AlertRecord struct {
	Ts   time.Time `json:"ts"`
	TxID string    `json:"txID"`
	Msg  string    `json:"msg"`
}

// Recorder 跨服务端重启存活,保存事件流、回查记录、告警和统计。
type Recorder struct {
	mu     sync.Mutex
	events map[string][]Event
	checks []CheckRecord
	alerts []AlertRecord

	HalfTotal     int64
	Committed     int64
	RolledBack    int64
	DwellTotalMs  int64 // 半消息停留总时长(毫秒)
	CheckRequests int64
	CheckCommit   int64
	CheckRollback int64
	CheckUnknown  int64
	CheckErrors   int64
	DeliveredMsgs int64
	PolicyApplied int64
	Restarts      int64
}

func NewRecorder() *Recorder { return &Recorder{events: map[string][]Event{}} }

func (r *Recorder) AddEvent(txID, kind, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[txID] = append(r.events[txID], Event{Ts: time.Now(), TxID: txID, Kind: kind, Detail: detail})
}

func (r *Recorder) AddCheck(c CheckRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.Ts = time.Now()
	r.checks = append(r.checks, c)
}

func (r *Recorder) AddAlert(txID, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, AlertRecord{Ts: time.Now(), TxID: txID, Msg: msg})
}

func (r *Recorder) EventsFor(txID string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]Event(nil), r.events[txID]...)
	return out
}

func (r *Recorder) Checks() []CheckRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CheckRecord(nil), r.checks...)
}

func (r *Recorder) Alerts() []AlertRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AlertRecord(nil), r.alerts...)
}

type Config struct {
	Addr              string
	DataFile          string
	CheckAfter        time.Duration // 半消息超过该时长未决议则触发回查
	CheckInterval     time.Duration
	CheckCallTimeout  time.Duration
	MaxChecks         int    // 回查次数上限
	Policy            string // 超过上限后的处理策略: "rollback" | "commit"
	ExpectedConsumers []string
	DeliverTimeout    time.Duration
	FaultProb         float64 // 服务端侧连接的网络故障注入
}

type Server struct {
	cfg Config
	Rec *Recorder

	mu        sync.Mutex
	txs       map[string]*Tx
	producers map[string]map[string]*Session // 生产者组 -> 实例 -> 连接
	sessMeta  map[*Session][2]string
	consumers map[string]*Session
	consMeta  map[*Session]string

	ln     net.Listener
	store  *Store
	stopCh chan struct{}
	wg     sync.WaitGroup

	allSess  map[*Session]struct{}
	stopping atomic.Bool
}

func NewServer(cfg Config, rec *Recorder) *Server {
	return &Server{
		cfg:       cfg,
		Rec:       rec,
		txs:       map[string]*Tx{},
		producers: map[string]map[string]*Session{},
		sessMeta:  map[*Session][2]string{},
		consumers: map[string]*Session{},
		consMeta:  map[*Session]string{},
		allSess:   map[*Session]struct{}{},
	}
}

// Start 重放 WAL 恢复全部事务状态,然后监听并启动回查/投递调度。
func (s *Server) Start() error {
	store, recs, err := OpenStore(s.cfg.DataFile)
	if err != nil {
		return err
	}
	s.store = store
	for _, r := range recs {
		s.applyRec(r)
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.stopCh = make(chan struct{})
	s.wg.Add(3)
	go s.acceptLoop()
	go s.checkLoop()
	go s.deliverLoop()
	return nil
}

// Stop 模拟服务端崩溃/重启:断开所有连接,内存状态全部丢弃(状态在 WAL 里)。
func (s *Server) Stop() {
	s.stopping.Store(true)
	close(s.stopCh)
	s.ln.Close()
	s.mu.Lock()
	sessList := make([]*Session, 0, len(s.allSess))
	for ss := range s.allSess {
		sessList = append(sessList, ss)
	}
	s.mu.Unlock()
	for _, ss := range sessList {
		ss.Close()
	}
	s.wg.Wait()
	s.store.Close()
}

func (s *Server) applyRec(r Rec) {
	switch r.Type {
	case RecHalf:
		if _, ok := s.txs[r.TxID]; ok {
			return // 生产者重试导致的重复半消息,幂等
		}
		s.txs[r.TxID] = &Tx{
			ID: r.TxID, Group: r.Group, Topic: r.Topic, Body: r.Body,
			State: StateHalf, HalfTs: time.UnixMilli(r.Ts), Delivered: map[string]bool{},
		}
	case RecCommit:
		if tx, ok := s.txs[r.TxID]; ok && tx.State == StateHalf {
			tx.State = StateCommitted
			tx.EndTs = time.UnixMilli(r.Ts)
			tx.ResolvedBy = r.Reason
		}
	case RecRollback:
		if tx, ok := s.txs[r.TxID]; ok && tx.State == StateHalf {
			tx.State = StateRolledBack
			tx.EndTs = time.UnixMilli(r.Ts)
			tx.ResolvedBy = r.Reason
		}
	case RecDelivered:
		if tx, ok := s.txs[r.TxID]; ok {
			tx.Delivered[r.Consumer] = true
		}
	}
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		sess := NewSession(conn)
		sess.DropProb = s.cfg.FaultProb
		sess.RespDropProb = s.cfg.FaultProb
		sess.Handler = s.handle
		sess.OnClose = s.onSessClose
		s.mu.Lock()
		s.allSess[sess] = struct{}{}
		s.mu.Unlock()
		go sess.Run()
	}
}

func (s *Server) onSessClose(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.allSess, sess)
	if meta, ok := s.sessMeta[sess]; ok {
		group, inst := meta[0], meta[1]
		if s.producers[group][inst] == sess {
			delete(s.producers[group], inst)
		}
		delete(s.sessMeta, sess)
	}
	if name, ok := s.consMeta[sess]; ok {
		if s.consumers[name] == sess {
			delete(s.consumers, name)
		}
		delete(s.consMeta, sess)
	}
}

func (s *Server) handle(sess *Session, f *Frame) (any, error) {
	if s.stopping.Load() {
		return nil, fmt.Errorf("server is stopped")
	}
	switch f.Type {
	case "sendHalf":
		var p struct {
			TxID  string `json:"txID"`
			Group string `json:"group"`
			Topic string `json:"topic"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if _, ok := s.txs[p.TxID]; ok {
			s.mu.Unlock()
			return map[string]any{"status": "ok", "dup": true}, nil // 幂等去重
		}
		now := time.Now()
		if err := s.store.Append(Rec{Type: RecHalf, TxID: p.TxID, Group: p.Group, Topic: p.Topic, Body: p.Body, Ts: now.UnixMilli()}); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.txs[p.TxID] = &Tx{ID: p.TxID, Group: p.Group, Topic: p.Topic, Body: p.Body, State: StateHalf, HalfTs: now, Delivered: map[string]bool{}}
		s.mu.Unlock()
		atomic.AddInt64(&s.Rec.HalfTotal, 1)
		s.Rec.AddEvent(p.TxID, "half", fmt.Sprintf("半消息已持久化(组=%s 主题=%s),对消费者不可见", p.Group, p.Topic))
		return map[string]any{"status": "ok"}, nil

	case "commit":
		var p struct {
			TxID string `json:"txID"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, s.resolve(p.TxID, StateCommitted, "producer", "")

	case "rollback":
		var p struct {
			TxID   string `json:"txID"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, s.resolve(p.TxID, StateRolledBack, "producer", p.Reason)

	case "registerProducer":
		var p struct {
			Group    string `json:"group"`
			Instance string `json:"instance"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.producers[p.Group] == nil {
			s.producers[p.Group] = map[string]*Session{}
		}
		s.producers[p.Group][p.Instance] = sess
		s.sessMeta[sess] = [2]string{p.Group, p.Instance}
		s.mu.Unlock()
		return map[string]string{"status": "ok"}, nil

	case "subscribe":
		var p struct {
			Consumer string `json:"consumer"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.consumers[p.Consumer] = sess
		s.consMeta[sess] = p.Consumer
		s.mu.Unlock()
		return map[string]string{"status": "ok"}, nil
	}
	return nil, fmt.Errorf("unknown frame type %q", f.Type)
}

// resolve 把半消息决议为最终状态,先写 WAL 再改内存,幂等。
func (s *Server) resolve(txID string, st TxState, by, reason string) error {
	s.mu.Lock()
	tx, ok := s.txs[txID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown tx %s", txID)
	}
	if tx.State != StateHalf {
		prev := tx.State
		s.mu.Unlock()
		if prev == st {
			return nil // 重复决议,幂等
		}
		s.Rec.AddEvent(txID, "conflict", fmt.Sprintf("忽略冲突决议 %s(已是 %s)", st, prev))
		return fmt.Errorf("tx %s already %s", txID, prev)
	}
	recType := RecCommit
	if st == StateRolledBack {
		recType = RecRollback
	}
	if err := s.store.Append(Rec{Type: recType, TxID: txID, Reason: by, Ts: time.Now().UnixMilli()}); err != nil {
		s.mu.Unlock()
		return err
	}
	tx.State = st
	tx.EndTs = time.Now()
	tx.ResolvedBy = by
	dwell := tx.EndTs.Sub(tx.HalfTs)
	s.mu.Unlock()

	if st == StateCommitted {
		atomic.AddInt64(&s.Rec.Committed, 1)
		s.Rec.AddEvent(txID, "commit", fmt.Sprintf("事务提交(来源=%s),消息对消费者可见", by))
	} else {
		atomic.AddInt64(&s.Rec.RolledBack, 1)
		detail := fmt.Sprintf("事务回滚(来源=%s),消息丢弃", by)
		if reason != "" {
			detail += ":" + reason
		}
		s.Rec.AddEvent(txID, "rollback", detail)
	}
	atomic.AddInt64(&s.Rec.DwellTotalMs, dwell.Milliseconds())
	return nil
}

// checkLoop 定期扫描超时未决议的半消息,向生产者组内任意存活实例回查。
func (s *Server) checkLoop() {
	defer s.wg.Done()
	tick := time.NewTicker(s.cfg.CheckInterval)
	defer tick.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-tick.C:
		}
		type job struct {
			txID, group, instance string
			sess                  *Session
			attempt               int
		}
		var jobs []job
		var policyIDs []string
		s.mu.Lock()
		now := time.Now()
		for _, tx := range s.txs {
			if tx.State != StateHalf || now.Sub(tx.HalfTs) < s.cfg.CheckAfter {
				continue
			}
			if tx.Checks >= s.cfg.MaxChecks {
				policyIDs = append(policyIDs, tx.ID)
				continue
			}
			tx.Checks++
			var sess *Session
			inst := ""
			for id, ss := range s.producers[tx.Group] {
				sess, inst = ss, id
				break // 组内任意一个活着的实例即可
			}
			jobs = append(jobs, job{tx.ID, tx.Group, inst, sess, tx.Checks})
		}
		s.mu.Unlock()

		for _, id := range policyIDs {
			s.applyPolicy(id)
		}
		for _, j := range jobs {
			s.doCheck(j.txID, j.group, j.instance, j.sess, j.attempt)
		}
	}
}

func (s *Server) doCheck(txID, group, instance string, sess *Session, attempt int) {
	atomic.AddInt64(&s.Rec.CheckRequests, 1)
	if sess == nil {
		atomic.AddInt64(&s.Rec.CheckErrors, 1)
		s.Rec.AddCheck(CheckRecord{TxID: txID, Group: group, Attempt: attempt, Err: "生产者组内无存活实例"})
		s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查:组 %s 内无存活实例,稍后再查", attempt, group))
		return
	}
	s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查:询问实例 %s", attempt, instance))
	resp, err := sess.Call("check", map[string]string{"txID": txID}, s.cfg.CheckCallTimeout)
	if err != nil {
		atomic.AddInt64(&s.Rec.CheckErrors, 1)
		s.Rec.AddCheck(CheckRecord{TxID: txID, Group: group, Instance: instance, Attempt: attempt, Err: err.Error()})
		s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查失败:%v", attempt, err))
		return
	}
	var r struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return
	}
	s.Rec.AddCheck(CheckRecord{TxID: txID, Group: group, Instance: instance, Attempt: attempt, Result: r.Result})
	switch r.Result {
	case "commit":
		atomic.AddInt64(&s.Rec.CheckCommit, 1)
		s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查结果:本地事务已提交 -> 提交消息", attempt))
		s.resolve(txID, StateCommitted, "checkback", "")
	case "rollback":
		atomic.AddInt64(&s.Rec.CheckRollback, 1)
		s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查结果:本地事务已回滚 -> 丢弃消息", attempt))
		s.resolve(txID, StateRolledBack, "checkback", "")
	default: // unknown
		atomic.AddInt64(&s.Rec.CheckUnknown, 1)
		s.Rec.AddEvent(txID, "check", fmt.Sprintf("第%d次回查结果:未知,稍后再查", attempt))
	}
}

// applyPolicy 回查超过上限后按配置策略处理并告警。
func (s *Server) applyPolicy(txID string) {
	st := StateRolledBack
	if s.cfg.Policy == "commit" {
		st = StateCommitted
	}
	before := s.StateOf(txID)
	if before != StateHalf {
		return
	}
	if err := s.resolve(txID, st, "policy", fmt.Sprintf("超过最大回查次数%d", s.cfg.MaxChecks)); err != nil {
		return
	}
	atomic.AddInt64(&s.Rec.PolicyApplied, 1)
	msg := fmt.Sprintf("事务 %s 超过最大回查次数(%d),按策略强制%s", txID, s.cfg.MaxChecks, map[TxState]string{StateCommitted: "提交", StateRolledBack: "回滚"}[st])
	s.Rec.AddAlert(txID, msg)
	s.Rec.AddEvent(txID, "alert", msg)
}

// deliverLoop 把已提交的消息至少一次地推给所有订阅消费者(响应即 ACK)。
func (s *Server) deliverLoop() {
	defer s.wg.Done()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-tick.C:
		}
		type djob struct {
			txID, body, topic, consumer string
			sess                        *Session
		}
		var jobs []djob
		s.mu.Lock()
		for _, tx := range s.txs {
			if tx.State != StateCommitted {
				continue // 半消息与已回滚消息对消费者不可见
			}
			for _, c := range s.cfg.ExpectedConsumers {
				if tx.Delivered[c] {
					continue
				}
				jobs = append(jobs, djob{tx.ID, tx.Body, tx.Topic, c, s.consumers[c]})
			}
		}
		s.mu.Unlock()
		for _, j := range jobs {
			if j.sess == nil {
				continue
			}
			_, err := j.sess.Call("deliver", map[string]string{
				"txID": j.txID, "topic": j.topic, "body": j.body,
			}, s.cfg.DeliverTimeout)
			if err != nil {
				continue // 下次 tick 重投
			}
			s.mu.Lock()
			if tx, ok := s.txs[j.txID]; ok && !tx.Delivered[j.consumer] {
				s.store.Append(Rec{Type: RecDelivered, TxID: j.txID, Consumer: j.consumer, Ts: time.Now().UnixMilli()})
				tx.Delivered[j.consumer] = true
			}
			s.mu.Unlock()
			atomic.AddInt64(&s.Rec.DeliveredMsgs, 1)
			s.Rec.AddEvent(j.txID, "delivered", "已投递给消费者 "+j.consumer+" 并收到确认")
		}
	}
}

func (s *Server) StateOf(txID string) TxState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tx, ok := s.txs[txID]; ok {
		return tx.State
	}
	return ""
}

// Quiescent 用于验收:没有未决议的半消息,且所有已提交消息都投给了全部消费者。
func (s *Server) Quiescent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tx := range s.txs {
		if tx.State == StateHalf {
			return false
		}
		if tx.State == StateCommitted {
			for _, c := range s.cfg.ExpectedConsumers {
				if !tx.Delivered[c] {
					return false
				}
			}
		}
	}
	return true
}

type TxInfo struct {
	ID         string   `json:"id"`
	Group      string   `json:"group"`
	Topic      string   `json:"topic"`
	State      TxState  `json:"state"`
	Checks     int      `json:"checks"`
	ResolvedBy string   `json:"resolvedBy"`
	HalfTs     int64    `json:"halfTs"`
	EndTs      int64    `json:"endTs"`
	DwellMs    int64    `json:"dwellMs"`
	Delivered  []string `json:"delivered"`
}

func (s *Server) Txs() []TxInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TxInfo, 0, len(s.txs))
	for _, tx := range s.txs {
		info := TxInfo{
			ID: tx.ID, Group: tx.Group, Topic: tx.Topic, State: tx.State,
			Checks: tx.Checks, ResolvedBy: tx.ResolvedBy,
			HalfTs: tx.HalfTs.UnixMilli(),
		}
		if !tx.EndTs.IsZero() {
			info.EndTs = tx.EndTs.UnixMilli()
			info.DwellMs = tx.EndTs.Sub(tx.HalfTs).Milliseconds()
		}
		for c := range tx.Delivered {
			info.Delivered = append(info.Delivered, c)
		}
		sort.Strings(info.Delivered)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HalfTs > out[j].HalfTs })
	return out
}
