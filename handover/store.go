package handover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Data 是持久化到本地文件的全部数据。
type Data struct {
	Shifts         []*Shift    `json:"shifts"`
	Items          []*Item     `json:"items"`
	Handovers      []*Handover `json:"handovers"`
	NextShiftID    int         `json:"nextShiftId"`
	NextItemID     int         `json:"nextItemId"`
	NextHandoverID int         `json:"nextHandoverId"`
}

// Store 是本地数据存储，所有修改通过 JSON 文件持久化。
type Store struct {
	path string
	data Data
}

// DefaultPath 返回默认数据文件路径：环境变量 SHIFTHANDOVER_DATA 优先，
// 否则使用用户主目录下的 .shift-handover/data.json。
func DefaultPath() string {
	if p := os.Getenv("SHIFTHANDOVER_DATA"); strings.TrimSpace(p) != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".shift-handover-data.json"
	}
	return filepath.Join(home, ".shift-handover", "data.json")
}

// Open 打开（必要时创建）存储文件。文件不存在时返回空存储。
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: Data{NextShiftID: 1, NextItemID: 1, NextHandoverID: 1},
	}
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
		return nil, fmt.Errorf("解析数据文件失败: %w", err)
	}
	if s.data.NextShiftID < 1 {
		s.data.NextShiftID = 1
	}
	if s.data.NextItemID < 1 {
		s.data.NextItemID = 1
	}
	if s.data.NextHandoverID < 1 {
		s.data.NextHandoverID = 1
	}
	return s, nil
}

// save 以原子方式写入数据文件（先写临时文件再重命名），
// 失败时已有数据保持不变。
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("编码数据失败: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("写入数据文件失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("保存数据文件失败: %w", err)
	}
	return nil
}

// Path 返回数据文件路径。
func (s *Store) Path() string { return s.path }

// ---------- 内部辅助 ----------

func trim(v string) string { return strings.TrimSpace(v) }

func nonEmpty(v string) bool { return trim(v) != "" }

func now() time.Time { return time.Now() }

// overlaps 判断两个班次按实际时刻比较是否重叠。
// 前班结束恰好等于后班开始属于区间首尾相接，不算重叠。
func overlaps(a, b *Shift) bool {
	return a.Start.Before(b.End) && b.Start.Before(a.End)
}

// hasOverlapNote 判断两个班次之间是否已有重叠说明（任一方记录了另一方）。
func (s *Store) hasOverlapNote(a, b *Shift) bool {
	for _, id := range a.OverlapIDs {
		if id == b.ID && nonEmpty(a.OverlapNote) {
			return true
		}
	}
	for _, id := range b.OverlapIDs {
		if id == a.ID && nonEmpty(b.OverlapNote) {
			return true
		}
	}
	return false
}

// ---------- 查询 ----------

// ListShifts 返回全部班次（按编号顺序）。
func (s *Store) ListShifts() []*Shift { return s.data.Shifts }

// ListItems 返回全部事项（按编号顺序）。
func (s *Store) ListItems() []*Item { return s.data.Items }

// ListHandovers 返回全部交接记录（按编号顺序）。
func (s *Store) ListHandovers() []*Handover { return s.data.Handovers }

// GetShift 按编号查询班次，不存在时返回 NotFoundError。
func (s *Store) GetShift(id int) (*Shift, error) {
	for _, sh := range s.data.Shifts {
		if sh.ID == id {
			return sh, nil
		}
	}
	return nil, &NotFoundError{What: "班次", ID: id}
}

// GetItem 按编号查询事项，不存在时返回 NotFoundError。
func (s *Store) GetItem(id int) (*Item, error) {
	for _, it := range s.data.Items {
		if it.ID == id {
			return it, nil
		}
	}
	return nil, &NotFoundError{What: "事项", ID: id}
}

// GetHandover 按编号查询交接记录，不存在时返回 NotFoundError。
func (s *Store) GetHandover(id int) (*Handover, error) {
	for _, h := range s.data.Handovers {
		if h.ID == id {
			return h, nil
		}
	}
	return nil, &NotFoundError{What: "交接记录", ID: id}
}

// HandoverByFromShift 查询某交班班次发起的交接记录，没有则返回 nil。
func (s *Store) HandoverByFromShift(fromShiftID int) *Handover {
	for _, h := range s.data.Handovers {
		if h.FromShiftID == fromShiftID {
			return h
		}
	}
	return nil
}

// OpenHandoversToShift 查询以某班次为接班班次、且尚未完成的交接记录。
func (s *Store) OpenHandoversToShift(toShiftID int) []*Handover {
	var out []*Handover
	for _, h := range s.data.Handovers {
		if h.ToShiftID == toShiftID && !h.Complete {
			out = append(out, h)
		}
	}
	return out
}

// ShiftItems 返回当前属于某班次的全部事项（按编号顺序）。
func (s *Store) ShiftItems(shiftID int) []*Item {
	var out []*Item
	for _, it := range s.data.Items {
		if it.CurrentShiftID == shiftID {
			out = append(out, it)
		}
	}
	return out
}
