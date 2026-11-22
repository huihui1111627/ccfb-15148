package mq

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ProducerClient 是事务消息生产者客户端,属于某个生产者组的一个实例。
// 崩溃(Crash)后不再重连;同组的新实例会接替它应答回查。
type ProducerClient struct {
	Group    string
	Instance string
	Addr     string

	// Checker 由本地事务存储驱动,回答 commit / rollback / unknown。
	Checker func(txID string) string

	DropProb     float64
	RespDropProb float64
	MaxLatency   time.Duration

	mu     sync.Mutex
	sess   *Session
	alive  atomic.Bool
	stopCh chan struct{}
}

func NewProducerClient(group, instance, addr string, checker func(txID string) string) *ProducerClient {
	return &ProducerClient{
		Group: group, Instance: instance, Addr: addr, Checker: checker,
		stopCh: make(chan struct{}),
	}
}

func (p *ProducerClient) Start() {
	p.alive.Store(true)
	go p.loop()
}

func (p *ProducerClient) Alive() bool { return p.alive.Load() }

func (p *ProducerClient) loop() {
	for p.alive.Load() {
		conn, err := net.DialTimeout("tcp", p.Addr, time.Second)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		sess := NewSession(conn)
		sess.Name = "producer:" + p.Instance
		sess.DropProb = p.DropProb
		sess.RespDropProb = p.RespDropProb
		sess.MaxLatency = p.MaxLatency
		sess.Handler = func(s *Session, f *Frame) (any, error) {
			if f.Type == "check" {
				var req struct {
					TxID string `json:"txID"`
				}
				if err := json.Unmarshal(f.Payload, &req); err != nil {
					return nil, err
				}
				return map[string]string{"result": p.Checker(req.TxID)}, nil
			}
			return nil, fmt.Errorf("unknown frame %q", f.Type)
		}
		go sess.Run()
		if _, err := sess.Call("registerProducer", map[string]string{
			"group": p.Group, "instance": p.Instance,
		}, 2*time.Second); err != nil {
			sess.Close() // 注册失败的连接不可用,重连
			time.Sleep(150 * time.Millisecond)
			continue
		}
		p.mu.Lock()
		p.sess = sess
		p.mu.Unlock()
		<-sess.Done()
		p.mu.Lock()
		if p.sess == sess {
			p.sess = nil
		}
		p.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
}

// call 带重试的 RPC:丢包/超时/连接断开都会重试,服务端按 TxID 幂等去重。
// 重试有上限(30s),超过后报错,由上层决定放弃或崩溃。
func (p *ProducerClient) call(typ string, payload any) error {
	deadline := time.Now().Add(30 * time.Second)
	for p.alive.Load() {
		if time.Now().After(deadline) {
			return errors.New("retry budget exhausted")
		}
		p.mu.Lock()
		sess := p.sess
		p.mu.Unlock()
		if sess == nil {
			time.Sleep(80 * time.Millisecond)
			continue
		}
		_, err := sess.Call(typ, payload, 800*time.Millisecond)
		if err == nil {
			return nil
		}
		time.Sleep(60 * time.Millisecond)
	}
	return errors.New("producer instance is dead")
}

func (p *ProducerClient) SendHalf(txID, topic, body string) error {
	return p.call("sendHalf", map[string]string{
		"txID": txID, "group": p.Group, "topic": topic, "body": body,
	})
}

func (p *ProducerClient) Commit(txID string) error {
	return p.call("commit", map[string]string{"txID": txID})
}

func (p *ProducerClient) Rollback(txID, reason string) error {
	return p.call("rollback", map[string]string{"txID": txID, "reason": reason})
}

// Crash 模拟实例崩溃:立即断开连接,不再重连,内存状态丢失。
func (p *ProducerClient) Crash() {
	if p.alive.CompareAndSwap(true, false) {
		p.mu.Lock()
		sess := p.sess
		p.mu.Unlock()
		if sess != nil {
			sess.Close()
		}
		close(p.stopCh)
	}
}

// ConsumerClient 是下游消费者(库存/积分/物流),at-least-once 接收 + 幂等去重。
type ConsumerClient struct {
	Name string
	Addr string

	// OnMessage 必须幂等:同一 txID 可能重复投递。
	OnMessage func(txID, topic, body string) error

	DropProb     float64
	RespDropProb float64
	MaxLatency   time.Duration

	stopCh chan struct{}
}

func NewConsumerClient(name, addr string, onMessage func(txID, topic, body string) error) *ConsumerClient {
	return &ConsumerClient{
		Name: name, Addr: addr, OnMessage: onMessage,
		stopCh: make(chan struct{}),
	}
}

func (c *ConsumerClient) Start() {
	go c.loop()
}

func (c *ConsumerClient) Stop() { close(c.stopCh) }

func (c *ConsumerClient) loop() {
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}
		conn, err := net.DialTimeout("tcp", c.Addr, time.Second)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		sess := NewSession(conn)
		sess.Name = "consumer:" + c.Name
		sess.DropProb = c.DropProb
		sess.RespDropProb = c.RespDropProb
		sess.MaxLatency = c.MaxLatency
		sess.Handler = func(s *Session, f *Frame) (any, error) {
			if f.Type == "deliver" {
				var req struct {
					TxID  string `json:"txID"`
					Topic string `json:"topic"`
					Body  string `json:"body"`
				}
				if err := json.Unmarshal(f.Payload, &req); err != nil {
					return nil, err
				}
				if err := c.OnMessage(req.TxID, req.Topic, req.Body); err != nil {
					return nil, err // 不确认,服务端会重投
				}
				return map[string]string{"status": "ok"}, nil
			}
			return nil, fmt.Errorf("unknown frame %q", f.Type)
		}
		go sess.Run()
		if _, err := sess.Call("subscribe", map[string]string{"consumer": c.Name}, 2*time.Second); err != nil {
			sess.Close()
			time.Sleep(150 * time.Millisecond)
			continue
		}
		<-sess.Done()
		time.Sleep(150 * time.Millisecond)
	}
}
