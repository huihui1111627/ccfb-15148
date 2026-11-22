package mq

import (
	"encoding/json"
	"errors"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Frame 是双向 RPC 的唯一传输单元。
// 请求帧: {seq, type, payload};响应帧: {seq, type:"resp", payload|error}。
type Frame struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

var (
	ErrClosed  = errors.New("session closed")
	ErrTimeout = errors.New("request timed out")
	ErrDropped = errors.New("simulated network fault: request dropped")
)

// Session 是一条双向连接,两端都可以发起 Call,对端通过 Handler 应答。
type Session struct {
	Name string

	conn   net.Conn
	enc    *json.Encoder
	dec    *json.Decoder
	wmu    sync.Mutex
	pmu    sync.Mutex
	pend   map[int64]chan *Frame
	seq    atomic.Int64
	closed atomic.Bool
	done   chan struct{}

	// Handler 处理对端发来的请求帧,返回值会作为响应 payload。
	Handler func(s *Session, f *Frame) (any, error)
	OnClose func(s *Session)

	// 网络故障注入(丢包 / 响应丢失 / 延迟)。
	DropProb     float64
	RespDropProb float64
	MaxLatency   time.Duration

	rng *rand.Rand
	rmu sync.Mutex
}

func NewSession(conn net.Conn) *Session {
	return &Session{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
		pend: map[int64]chan *Frame{},
		done: make(chan struct{}),
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *Session) rf() float64 {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.rng.Float64()
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) IsClosed() bool { return s.closed.Load() }

// Call 发起一次请求-响应。注入的丢包表现为超时,由上层重试(服务端幂等)。
func (s *Session) Call(typ string, payload any, timeout time.Duration) (json.RawMessage, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if s.rf() < s.DropProb {
		return nil, ErrDropped // 请求根本没发出去
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	seq := s.seq.Add(1)
	ch := make(chan *Frame, 1)
	s.pmu.Lock()
	s.pend[seq] = ch
	s.pmu.Unlock()
	defer func() {
		s.pmu.Lock()
		delete(s.pend, seq)
		s.pmu.Unlock()
	}()

	if s.MaxLatency > 0 {
		time.Sleep(time.Duration(s.rf() * float64(s.MaxLatency)))
	}
	s.wmu.Lock()
	err := s.enc.Encode(&Frame{Seq: seq, Type: typ, Payload: raw})
	s.wmu.Unlock()
	if err != nil {
		return nil, err
	}
	if s.rf() < s.RespDropProb {
		// 响应丢包:等服务端回了也假装没收到
		select {
		case <-ch:
		case <-time.After(timeout):
		case <-s.done:
		}
		return nil, ErrTimeout
	}
	select {
	case f := <-ch:
		if f == nil {
			return nil, ErrClosed
		}
		if f.Error != "" {
			return nil, errors.New(f.Error)
		}
		return f.Payload, nil
	case <-time.After(timeout):
		return nil, ErrTimeout
	case <-s.done:
		return nil, ErrClosed
	}
}

// Run 是读循环,连接断开后返回。
func (s *Session) Run() {
	for {
		var f Frame
		if err := s.dec.Decode(&f); err != nil {
			s.Close()
			return
		}
		if f.Type == "resp" {
			s.pmu.Lock()
			ch, ok := s.pend[f.Seq]
			s.pmu.Unlock()
			if ok {
				select {
				case ch <- &f:
				default:
				}
			}
			continue
		}
		if s.Handler != nil {
			go func(f Frame) {
				payload, err := s.Handler(s, &f)
				var perr string
				var raw json.RawMessage
				if err != nil {
					perr = err.Error()
				} else if payload != nil {
					raw, _ = json.Marshal(payload)
				}
				s.wmu.Lock()
				s.enc.Encode(&Frame{Seq: f.Seq, Type: "resp", Payload: raw, Error: perr})
				s.wmu.Unlock()
			}(f)
		}
	}
}

func (s *Session) Close() {
	if s.closed.CompareAndSwap(false, true) {
		s.conn.Close()
		close(s.done)
		if s.OnClose != nil {
			s.OnClose(s)
		}
	}
}
