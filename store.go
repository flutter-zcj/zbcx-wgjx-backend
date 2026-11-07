// store 负责《万国觉醒·统帅远征》后端的全部业务状态：
// 排行榜（按分数降序，保留 Top 20）、玩家最佳分、玩家存档。
// 状态在内存中维护，并按变更落盘到 JSON 文件，进程重启不丢数据。
//
// 与前端 wgjx-net.js 的离线本地逻辑保持一致：服务端是「权威版」本地排行榜。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Entry 排行榜单条记录。
type Entry struct {
	Nickname string `json:"nickname"`
	Score    int    `json:"score"`
	Ts       int64  `json:"ts"`
}

// Store 业务状态容器，所有方法并发安全（内部互斥锁）。
type Store struct {
	mu       sync.Mutex
	path     string
	board    []Entry
	progress map[string]json.RawMessage
	nextPID  int64
}

// NewStore 创建并加载持久化文件（不存在则初始化空状态）。
func NewStore(path string) *Store {
	s := &Store{
		path:     path,
		progress: make(map[string]json.RawMessage),
		nextPID:  1,
	}
	_ = s.loadFile()
	return s
}

// Login 返回（playerId, 历史最佳分）。playerId 由服务端统一分配，保证唯一。
func (s *Store) Login(playerID, nickname string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if playerID == "" {
		playerID = "S" + itoa(atomic.AddInt64(&s.nextPID, 1))
	}
	best := 0
	for _, e := range s.board {
		if e.Score > best {
			best = e.Score
		}
	}
	return playerID, best
}

// SubmitScore 记录一局成绩，返回（名次, Top 榜单）。名次 1 起，0 表示未上榜。
func (s *Store) SubmitScore(playerID, nickname string, score int, win bool) (int, []Entry) {
	_ = win // 胜负仅作统计预留，不影响排行（排行以分数为准）
	s.mu.Lock()
	defer s.mu.Unlock()
	if playerID == "" {
		playerID = "S" + itoa(atomic.AddInt64(&s.nextPID, 1))
	}
	s.board = append(s.board, Entry{
		Nickname: nickname,
		Score:    score,
		Ts:       time.Now().Unix(),
	})
	sort.SliceStable(s.board, func(i, j int) bool {
		if s.board[i].Score == s.board[j].Score {
			return s.board[i].Ts < s.board[j].Ts
		}
		return s.board[i].Score > s.board[j].Score
	})
	if len(s.board) > 20 {
		s.board = s.board[:20]
	}
	rank := 0
	for i, e := range s.board {
		if e.Nickname == nickname && e.Score == score {
			rank = i + 1
			break
		}
	}
	return rank, s.snapshotLocked()
}

// GetLeaderboard 返回 Top 榜单副本。
func (s *Store) GetLeaderboard() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// Save 持久化某玩家的存档（任意 JSON）。
func (s *Store) Save(playerID string, progress json.RawMessage) {
	if playerID == "" {
		return
	}
	s.mu.Lock()
	s.progress[playerID] = progress
	s.mu.Unlock()
}

// Load 读取某玩家的存档，空则返回 null。
func (s *Store) Load(playerID string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.progress[playerID]; ok {
		return p
	}
	return nil
}

// Persist 将内存状态写入磁盘（先写临时文件再原子替换）。
func (s *Store) Persist() error {
	s.mu.Lock()
	payload := struct {
		Board    []Entry                    `json:"board"`
		Progress map[string]json.RawMessage `json:"progress"`
	}{
		Board:    s.board,
		Progress: s.progress,
	}
	s.mu.Unlock()

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) snapshotLocked() []Entry {
	out := make([]Entry, len(s.board))
	copy(out, s.board)
	return out
}

func (s *Store) loadFile() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var payload struct {
		Board    []Entry                    `json:"board"`
		Progress map[string]json.RawMessage `json:"progress"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return err
	}
	s.board = payload.Board
	if payload.Progress != nil {
		s.progress = payload.Progress
	}
	return nil
}

// itoa 轻量整数转字符串，避免重复 import strconv。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
