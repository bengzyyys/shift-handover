package handover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Data 是落盘的完整数据。
type Data struct {
	ShiftSeq    int           `json:"shift_seq"`
	ItemSeq     int           `json:"item_seq"`
	HandoverSeq int           `json:"handover_seq"`
	NoteSeq     int           `json:"note_seq"`
	Shifts      []Shift       `json:"shifts"`
	Items       []Item        `json:"items"`
	Handovers   []Handover    `json:"handovers"`
	Notes       []OverlapNote `json:"notes"`
}

// Store 是本地 JSON 文件存储。所有业务操作先改内存再原子写盘，
// 写盘失败时内存变更一并回滚，保证“失败前已保存的数据保持不变”。
type Store struct {
	path string
	data Data
}

// Open 打开数据文件；文件不存在时创建空数据。
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("读取数据文件失败: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("数据文件已损坏: %w", err)
	}
	return s, nil
}

// Path 返回数据文件路径。
func (s *Store) Path() string { return s.path }

// commit 先把当前数据写进临时文件再改名落盘，失败返回错误且不影响既有文件。
func (s *Store) commit() (err error) {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return fmt.Errorf("创建数据目录失败: %w", mkErr)
		}
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("写入数据文件失败: %w", err)
	}
	if err = os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存数据文件失败: %w", err)
	}
	return nil
}

// mutate 在数据快照上执行业务变更；业务失败或写盘失败都回滚到快照。
// 快照采用深拷贝：写盘失败时内存中的数据也必须恢复到变更前，
// 避免出现“文件未写成、内存已改半套”的情况。
func (s *Store) mutate(fn func(*Data) error) error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	var snapshot Data
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if err := fn(&s.data); err != nil {
		s.data = snapshot
		return err
	}
	if err := s.commit(); err != nil {
		s.data = snapshot
		return err
	}
	return nil
}
