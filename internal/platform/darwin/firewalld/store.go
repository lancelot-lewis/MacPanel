package firewalld

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileStore 把状态存到 JSON 文件。
//
// 为什么用文件而非数据库：回滚状态必须能被 root 独占读写，
// 且必须在数据库损坏时依然可用（数据库在一个非特权账户可写的目录里）。
// 文件放在 root 独占目录（0750 root:wheel）比数据库更可靠。
//
// 原子写入（临时文件 → fsync → rename）是必须的：
// 半截写入的状态文件会导致 Agent 重启后无法判断该不该回滚。
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore 创建文件状态存储。
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("firewalld: 状态文件路径不能为空")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("firewalld: 状态文件路径必须是绝对路径，实际 %q", path)
	}
	return &FileStore{path: path}, nil
}

// Load 读取状态。
func (s *FileStore) Load(_ context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrNoState
		}
		return State{}, fmt.Errorf("读取状态文件失败：%w", err)
	}
	if len(data) == 0 {
		return State{}, ErrNoState
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		// 文件损坏：不猜测内容，明确报错让上层决定。
		// 上层（Manager.Recover）会保守地把损坏状态当作「需要回滚」处理。
		return State{}, fmt.Errorf("状态文件损坏（%s）：%w", s.path, err)
	}
	return st, nil
}

// Save 原子写入状态。
func (s *FileStore) Save(_ context.Context, st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态失败：%w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建状态目录失败：%w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("创建临时状态文件失败：%w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// 成功 rename 后该路径已不存在，Remove 失败可忽略。
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置状态文件权限失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入状态失败：%w", err)
	}
	// fsync 是必须的：否则断电后可能得到空文件，
	// 而「空状态」会被误判为「从未应用过」。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步状态文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时状态文件失败：%w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换状态文件失败：%w", err)
	}
	return nil
}

// Clear 删除状态文件。
func (s *FileStore) Clear(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除状态文件失败：%w", err)
	}
	return nil
}

// MemoryStore 是测试用的内存实现。
type MemoryStore struct {
	mu    sync.Mutex
	state State
	has   bool
	// SaveErr 非空时 Save 返回该错误，用于测试失败路径。
	SaveErr error
}

// NewMemoryStore 创建内存状态存储。
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Load 读取状态。
func (m *MemoryStore) Load(context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.has {
		return State{}, ErrNoState
	}
	return m.state, nil
}

// Save 写入状态。
func (m *MemoryStore) Save(_ context.Context, s State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SaveErr != nil {
		return m.SaveErr
	}
	m.state = s
	m.has = true
	return nil
}

// Clear 清除状态。
func (m *MemoryStore) Clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.has = false
	m.state = State{}
	return nil
}
