// store 负责《万国觉醒·统帅远征》后端的全部业务状态：
//
//	· 赛季排行榜（按分数降序，保留 Top 20，按自然周分赛季）
//	· 玩家档案（文明 / 统帅 / 时代 / 宝石 / 场次 / 最佳分）
//	· 兼容旧的 save/load 原始 JSON 进度
//
// 状态在内存中维护，并按变更落盘到 JSON 文件，进程重启不丢数据。
// 赛季口径与前端展示一致：ISO 周，例如 S2026-W45。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Entry 排行榜单条记录。
type Entry struct {
	PlayerID     string `json:"playerId,omitempty"`
	Nickname     string `json:"nickname"`
	Score        int    `json:"score"`
	Civilization string `json:"civilization,omitempty"`
	Commander    string `json:"commander,omitempty"`
	Age          int    `json:"age,omitempty"`
	Season       string `json:"season,omitempty"`
	Ts           int64  `json:"ts"`
}

// Profile 玩家档案，字段与前端 wgjx-rules.js defaultProfile() 同构。
type Profile struct {
	Civilization string `json:"civilization"`
	Commander    string `json:"commander"`
	Age          int    `json:"age"`
	Gems         int    `json:"gems"`
	Matches      int    `json:"matches"`
	Wins         int    `json:"wins"`
	Best         int    `json:"best"`
}

// Store 业务状态容器，所有方法并发安全（内部互斥锁）。
type Store struct {
	mu       sync.Mutex
	path     string
	board    []Entry
	profiles map[string]*Profile
	progress map[string]json.RawMessage
	nextPID  int64
}

// NewStore 创建并加载持久化文件（不存在则初始化空状态）。
func NewStore(path string) *Store {
	s := &Store{
		path:     path,
		profiles: make(map[string]*Profile),
		progress: make(map[string]json.RawMessage),
		nextPID:  1,
	}
	_ = s.loadFile()
	return s
}

// CurrentSeason 返回当前赛季标识（ISO 周）。
func CurrentSeason() string {
	y, w := time.Now().ISOWeek()
	return fmt.Sprintf("S%d-W%02d", y, w)
}

// Login 登录并返回（playerId, 历史最佳, 档案, 赛季）。
func (s *Store) Login(playerID, nickname string) (string, int, Profile, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if playerID == "" {
		playerID = "S" + itoa(atomic.AddInt64(&s.nextPID, 1))
	}
	season := CurrentSeason()
	p := s.profileLocked(playerID)
	// 历史最佳以该玩家全部赛季的最高分为准
	best := p.Best
	for _, e := range s.board {
		if e.PlayerID == playerID && e.Score > best {
			best = e.Score
		}
	}
	if best > p.Best {
		p.Best = best
	}
	if p.Civilization == "" {
		p.Civilization = "china"
	}
	if p.Commander == "" {
		p.Commander = "sun"
	}
	if p.Age < 5 {
		p.Age = 5
	}
	return playerID, best, *p, season
}

// SubmitScore 记录一局成绩，返回（名次, 当前赛季 Top 榜, 赛季, 历史最佳）。
func (s *Store) SubmitScore(playerID, nickname, civilization, commander string, score, age int, win bool) (int, []Entry, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if playerID == "" {
		playerID = "S" + itoa(atomic.AddInt64(&s.nextPID, 1))
	}
	season := CurrentSeason()
	entry := Entry{
		PlayerID:     playerID,
		Nickname:     nickname,
		Score:        score,
		Civilization: civilization,
		Commander:    commander,
		Age:          age,
		Season:       season,
		Ts:           time.Now().UnixMilli(),
	}
	s.board = append(s.board, entry)

	p := s.profileLocked(playerID)
	p.Matches++
	if win {
		p.Wins++
	}
	if score > p.Best {
		p.Best = score
	}
	if civilization != "" {
		p.Civilization = civilization
	}
	if commander != "" {
		p.Commander = commander
	}
	if age > p.Age {
		p.Age = age
	}

	seasonBoard := s.seasonBoardLocked(season)
	rank := 0
	for i, e := range seasonBoard {
		if e.Ts == entry.Ts && e.PlayerID == entry.PlayerID {
			rank = i + 1
			break
		}
	}
	return rank, seasonBoard, season, p.Best
}

// GetLeaderboard 返回指定赛季 Top 榜（空则当前赛季）。
func (s *Store) GetLeaderboard(season string) ([]Entry, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if season == "" {
		season = CurrentSeason()
	}
	return s.seasonBoardLocked(season), season
}

// SaveProfile 保存玩家档案。
func (s *Store) SaveProfile(playerID string, p Profile) {
	if playerID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Age < 5 {
		p.Age = 5
	}
	if p.Civilization == "" {
		p.Civilization = "china"
	}
	if p.Commander == "" {
		p.Commander = "sun"
	}
	cur := s.profileLocked(playerID)
	if cur.Best > p.Best {
		p.Best = cur.Best
	}
	if cur.Matches > p.Matches {
		p.Matches = cur.Matches
	}
	s.profiles[playerID] = &p
}

// LoadProfile 读取玩家档案（不存在时返回默认档案）。
func (s *Store) LoadProfile(playerID string) Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.profileLocked(playerID)
}

// Save 持久化某玩家的原始存档（兼容旧协议）。
func (s *Store) Save(playerID string, progress json.RawMessage) {
	if playerID == "" {
		return
	}
	s.mu.Lock()
	s.progress[playerID] = progress
	s.mu.Unlock()
}

// Load 读取某玩家的原始存档，空则返回 null。
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
		Profiles map[string]*Profile        `json:"profiles"`
		Progress map[string]json.RawMessage `json:"progress"`
	}{
		Board:    s.board,
		Profiles: s.profiles,
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

// profileLocked 取档案（不存在则创建默认档案），调用方需持锁。
func (s *Store) profileLocked(playerID string) *Profile {
	if p, ok := s.profiles[playerID]; ok {
		if p.Age < 5 {
			p.Age = 5
		}
		return p
	}
	p := &Profile{Civilization: "china", Commander: "sun", Age: 5}
	s.profiles[playerID] = p
	return p
}

// seasonBoardLocked 生成某赛季 Top 20 榜单（调用方需持锁）。
func (s *Store) seasonBoardLocked(season string) []Entry {
	list := make([]Entry, 0, len(s.board))
	for _, e := range s.board {
		if e.Season == "" {
			// 历史数据（赛季字段缺失）归入当前赛季，避免老记录消失
			if season != CurrentSeason() {
				continue
			}
		} else if e.Season != season {
			continue
		}
		list = append(list, e)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Score == list[j].Score {
			return list[i].Ts < list[j].Ts
		}
		return list[i].Score > list[j].Score
	})
	if len(list) > 20 {
		list = list[:20]
	}
	return list
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
		Profiles map[string]*Profile        `json:"profiles"`
		Progress map[string]json.RawMessage `json:"progress"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return err
	}
	s.board = payload.Board
	if payload.Profiles != nil {
		s.profiles = payload.Profiles
	}
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
