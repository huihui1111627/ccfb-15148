package mq

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// 持久化记录类型:半消息、提交、回滚、投递确认全部走 WAL,重启后重放恢复。
const (
	RecHalf      = "half"
	RecCommit    = "commit"
	RecRollback  = "rollback"
	RecDelivered = "delivered"
)

type Rec struct {
	Type     string `json:"type"`
	TxID     string `json:"txID,omitempty"`
	Group    string `json:"group,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Body     string `json:"body,omitempty"`
	Consumer string `json:"consumer,omitempty"`
	Reason   string `json:"reason,omitempty"` // 决议来源: producer / checkback / policy
	Ts       int64  `json:"ts"`
}

// Store 是 append-only 的 WAL,每条记录写盘后 fsync。
type Store struct {
	mu   sync.Mutex
	f    *os.File
	Path string
}

func OpenStore(path string) (*Store, []Rec, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	var recs []Rec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r Rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // 跳过崩溃时写了一半的尾部记录
		}
		recs = append(recs, r)
	}
	return &Store{f: f, Path: path}, recs, nil
}

func (s *Store) Append(r Rec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return errors.New("store closed")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.f.Sync()
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		s.f.Sync()
		s.f.Close()
		s.f = nil
	}
}
