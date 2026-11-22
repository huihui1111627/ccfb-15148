package sim

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"txmq/mq"
)

// Order 订单。
type Order struct {
	ID     string `json:"id"`
	Item   string `json:"item"`
	Amount int    `json:"amount"`
}

// OrderDB 仿真的订单库:append-only 文件 + fsync,提交可能失败(注入)。
type OrderDB struct {
	mu         sync.Mutex
	f          *os.File
	Committed  map[string]Order
	RolledBack map[string]bool
	FailProb   float64
	rng        *rand.Rand
}

func OpenOrderDB(path string, failProb float64, seed int64) (*OrderDB, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	db := &OrderDB{
		f: f, FailProb: failProb,
		Committed:  map[string]Order{},
		RolledBack: map[string]bool{},
		rng:        rand.New(rand.NewSource(seed)),
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "O\t") {
			var o Order
			if json.Unmarshal([]byte(line[2:]), &o) == nil {
				db.Committed[o.ID] = o
			}
		} else if strings.HasPrefix(line, "R\t") {
			db.RolledBack[strings.TrimPrefix(line, "R\t")] = true
		}
	}
	return db, nil
}

// CommitOrder 模拟本地事务提交,可能整体失败(订单库提交失败注入)。
func (db *OrderDB) CommitOrder(o Order) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.rng.Float64() < db.FailProb {
		return errors.New("simulated order DB commit failure")
	}
	b, _ := json.Marshal(o)
	if _, err := db.f.Write(append([]byte("O\t"), append(b, '\n')...)); err != nil {
		return err
	}
	if err := db.f.Sync(); err != nil {
		return err
	}
	db.Committed[o.ID] = o
	return nil
}

// MarkRolledBack 记录本地事务已回滚,供回查时回答 rollback。
func (db *OrderDB) MarkRolledBack(id string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.f.Write([]byte("R\t" + id + "\n"))
	db.f.Sync()
	db.RolledBack[id] = true
}

// StateOf 回查依据:committed / rolledback / none(未知)。
func (db *OrderDB) StateOf(id string) string {
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, ok := db.Committed[id]; ok {
		return "committed"
	}
	if db.RolledBack[id] {
		return "rolledback"
	}
	return "none"
}

func (db *OrderDB) CommittedIDs() map[string]bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make(map[string]bool, len(db.Committed))
	for id := range db.Committed {
		out[id] = true
	}
	return out
}

func (db *OrderDB) Close() { db.f.Sync(); db.f.Close() }

// OrderService 仿真的下单服务:一个生产者组,实例会崩溃并被替换。
type OrderService struct {
	DB    *OrderDB
	Group string
	Addr  string

	DropProb     float64
	RespDropProb float64
	CrashProb    float64

	mu       sync.Mutex
	instance *mq.ProducerClient
	seq      int
	rng      *rand.Rand
	rmu      sync.Mutex

	Crashes int64
}

func NewOrderService(db *OrderDB, group, addr string, dropProb, respDropProb, crashProb float64, seed int64) *OrderService {
	return &OrderService{
		DB: db, Group: group, Addr: addr,
		DropProb: dropProb, RespDropProb: respDropProb, CrashProb: crashProb,
		rng: rand.New(rand.NewSource(seed)),
	}
}

func (s *OrderService) ensure() *mq.ProducerClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instance != nil && s.instance.Alive() {
		return s.instance
	}
	s.seq++
	inst := fmt.Sprintf("%s-%d", s.Group, s.seq)
	db := s.DB
	p := mq.NewProducerClient(s.Group, inst, s.Addr, func(txID string) string {
		switch db.StateOf(txID) {
		case "committed":
			return "commit"
		case "rolledback":
			return "rollback"
		default:
			return "unknown"
		}
	})
	p.DropProb = s.DropProb
	p.RespDropProb = s.RespDropProb
	p.MaxLatency = 30 * time.Millisecond
	p.Start()
	s.instance = p
	return p
}

func (s *OrderService) randFloat() float64 {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.rng.Float64()
}

func (s *OrderService) randIntn(n int) int {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.rng.Intn(n)
}

func (s *OrderService) crash(p *mq.ProducerClient) {
	p.Crash()
	atomic.AddInt64(&s.Crashes, 1)
	// 集群会在稍后拉起替代实例(回查要求组内有任意存活实例即可)
	go func() {
		time.Sleep(time.Duration(300+s.randIntn(500)) * time.Millisecond)
		s.ensure()
	}()
}

// PlaceOrder 执行一次下单的事务消息流程,在任意步骤都可能崩溃。
// 返回本次的结局描述(用于仿真日志)。
func (s *OrderService) PlaceOrder(o Order) string {
	p := s.ensure()
	stage := 0
	if s.randFloat() < s.CrashProb {
		stage = 1 + s.randIntn(3) // 1=发半消息前 2=半消息后/本地事务前 3=本地事务后/通知前
	}
	if stage == 1 {
		s.crash(p)
		return "crash-before-half"
	}
	body, _ := json.Marshal(o)
	if err := p.SendHalf(o.ID, "order-created", string(body)); err != nil {
		return "dead-during-half"
	}
	if stage == 2 {
		s.crash(p)
		return "crash-after-half"
	}
	lerr := s.DB.CommitOrder(o)
	if lerr != nil {
		s.DB.MarkRolledBack(o.ID)
	}
	if stage == 3 {
		s.crash(p)
		if lerr == nil {
			return "crash-after-local-commit"
		}
		return "crash-after-local-rollback"
	}
	if lerr == nil {
		p.Commit(o.ID)
		return "committed"
	}
	p.Rollback(o.ID, "order DB commit failed: "+lerr.Error())
	return "rolledback"
}

func (s *OrderService) EnsureAlive() { s.ensure() }

// Consumer 仿真的下游消费者,收到消息落盘后确认,按 txID 幂等去重。
type Consumer struct {
	Name string

	mu       sync.Mutex
	received map[string]bool
	order    []string
	f        *os.File
	Dups     int64
}

func NewConsumer(name, path string) (*Consumer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	c := &Consumer{Name: name, received: map[string]bool{}, f: f}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id != "" && !c.received[id] {
			c.received[id] = true
			c.order = append(c.order, id)
		}
	}
	return c, nil
}

// Handle 幂等消费:先落盘再确认,重复投递直接确认。
func (c *Consumer) Handle(txID, topic, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.received[txID] {
		c.Dups++
		return nil
	}
	if _, err := c.f.Write([]byte(txID + "\n")); err != nil {
		return err
	}
	if err := c.f.Sync(); err != nil {
		return err
	}
	c.received[txID] = true
	c.order = append(c.order, txID)
	return nil
}

func (c *Consumer) Received() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.received))
	for id := range c.received {
		out[id] = true
	}
	return out
}

func (c *Consumer) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.received)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
