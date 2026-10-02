package handover

import (
	"errors"
	"strings"
	"time"
)

// Severity 是事项的严重程度。
type Severity string

const (
	SeverityNormal    Severity = "normal"    // 普通
	SeverityImportant Severity = "important" // 重要
	SeverityUrgent    Severity = "urgent"    // 紧急
)

// ParseSeverity 解析严重程度，接受英文标识与中文名称；空串非法。
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "normal", "普通":
		return SeverityNormal, nil
	case "important", "重要":
		return SeverityImportant, nil
	case "urgent", "紧急":
		return SeverityUrgent, nil
	}
	return "", errors.New("严重程度无效：必须是 normal(普通)、important(重要)、urgent(紧急) 之一")
}

func (v Severity) valid() bool {
	return v == SeverityNormal || v == SeverityImportant || v == SeverityUrgent
}

// Label 返回严重程度的中文展示名。
func (v Severity) Label() string {
	switch v {
	case SeverityNormal:
		return "普通"
	case SeverityImportant:
		return "重要"
	case SeverityUrgent:
		return "紧急"
	}
	return string(v)
}

// EntryStatus 是交接事项的当前处理结果。
type EntryStatus string

const (
	EntryPending   EntryStatus = "pending"   // 待处理
	EntryConfirmed EntryStatus = "confirmed" // 确认接收
	EntryTracking  EntryStatus = "tracking"  // 继续跟踪（已接收）
	EntryReturned  EntryStatus = "returned"  // 退回
)

// Label 返回交接结果的中文展示名。
func (s EntryStatus) Label() string {
	switch s {
	case EntryPending:
		return "待处理"
	case EntryConfirmed:
		return "确认接收"
	case EntryTracking:
		return "继续跟踪"
	case EntryReturned:
		return "退回"
	}
	return string(s)
}

// Received 报告该结果是否表示接班班次已经接收。
func (s EntryStatus) Received() bool {
	return s == EntryConfirmed || s == EntryTracking
}

// EntryAction 是接班人对单项交接事项可执行的操作。
type EntryAction string

const (
	ActionConfirm EntryAction = "confirm" // 确认接收
	ActionReturn  EntryAction = "return"  // 退回
	ActionTrack   EntryAction = "track"   // 继续跟踪
)

// ParseAction 解析交接操作，接受英文标识与中文名称。
func ParseAction(s string) (EntryAction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "confirm", "确认", "确认接收":
		return ActionConfirm, nil
	case "return", "退回":
		return ActionReturn, nil
	case "track", "tracking", "继续跟踪", "跟踪":
		return ActionTrack, nil
	}
	return "", errors.New("操作无效：必须是 confirm(确认接收)、return(退回)、track(继续跟踪) 之一")
}

// Shift 是一个岗位班次，编号全局稳定。
type Shift struct {
	ID        string     `json:"id"`
	Position  string     `json:"position"`
	Owner     string     `json:"owner"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	CreatedAt time.Time  `json:"created_at"`
	Closed    bool       `json:"closed"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	// CloseRecord 是班次成功结束时留下的事项记录，一经写定不再随事项的后续
	// 修改、关闭或流转而改变。nil 表示该班次结束时未留下记录（旧数据）。
	CloseRecord *ShiftCloseRecord `json:"close_record,omitempty"`
}

// CloseItemSnapshot 冻结班次结束时刻某一事项的内容、严重程度、限制条件、
// 后续负责人与关闭情况。结束前已关闭的事项保留关闭人与关闭时间；
// 结束时未关闭的事项，之后无论由哪一班关闭，都不改变这里的未关闭记录。
type CloseItemSnapshot struct {
	ItemID        string     `json:"item_id"`
	Content       string     `json:"content"`
	Severity      Severity   `json:"severity"`
	Constraints   string     `json:"constraints,omitempty"`
	FollowOwner   string     `json:"follow_owner"`
	Closed        bool       `json:"closed"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	CloseOperator string     `json:"close_operator,omitempty"`
}

// ShiftCloseRecord 是班次成功结束时在班全部事项的冻结清单。
// Items 包含该班新增与已经接收的事项，也保留结束前已关闭的事项；
// 尚未确认或被退回的交接事项不在其中（仍留在交接记录里）。
// 空切片表示结束时没有事项，与缺少历史记录（CloseRecord 为 nil）相区别。
type ShiftCloseRecord struct {
	Items []CloseItemSnapshot `json:"items"`
}

// ItemEvent 是事项的追加式历史事件。
type ItemEvent struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Operator string    `json:"operator,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// Item 是一项未关闭事项。编号全局稳定，跨班次交接保持不变。
type Item struct {
	ID             string      `json:"id"`
	OriginShiftID  string      `json:"origin_shift_id"`
	ShiftIDs       []string    `json:"shift_ids"`
	CurrentShiftID string      `json:"current_shift_id"`
	Content        string      `json:"content"`
	Severity       Severity    `json:"severity"`
	Constraints    string      `json:"constraints,omitempty"`
	FollowOwner    string      `json:"follow_owner"`
	CreatedAt      time.Time   `json:"created_at"`
	Closed         bool        `json:"closed"`
	ClosedAt       *time.Time  `json:"closed_at,omitempty"`
	CloseOperator  string      `json:"close_operator,omitempty"`
	Events         []ItemEvent `json:"events"`
}

// ReturnRound 记录一次退回以及交班人随后的补充说明与重新提交。
// 原文与退回原因一经写入不再覆盖。
type ReturnRound struct {
	Seq                int        `json:"seq"`
	ReturnedAt         time.Time  `json:"returned_at"`
	ReturnOperator     string     `json:"return_operator"`
	Reason             string     `json:"reason"`
	Supplement         string     `json:"supplement,omitempty"`
	SupplementOperator string     `json:"supplement_operator,omitempty"`
	SupplementAt       *time.Time `json:"supplement_at,omitempty"`
	ResubmittedAt      *time.Time `json:"resubmitted_at,omitempty"`
}

// HandoverEntry 是交接清单中的单项处理记录。
type HandoverEntry struct {
	ItemID       string        `json:"item_id"`
	Content      string        `json:"content"`
	Severity     Severity      `json:"severity"`
	Constraints  string        `json:"constraints,omitempty"`
	FollowOwner  string        `json:"follow_owner"`
	Status       EntryStatus   `json:"status"`
	Operator     string        `json:"operator,omitempty"`
	ProcessedAt  *time.Time    `json:"processed_at,omitempty"`
	TrackingNote string        `json:"tracking_note,omitempty"`
	Rounds       []ReturnRound `json:"rounds,omitempty"`
}

// Handover 是一个交班班次对接班班次发起的一次交接。
type Handover struct {
	ID          string          `json:"id"`
	Position    string          `json:"position"`
	FromShiftID string          `json:"from_shift_id"`
	ToShiftID   string          `json:"to_shift_id"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Entries     []HandoverEntry `json:"entries"`
}

// Completed 报告交接是否已完成。
func (h *Handover) Completed() bool {
	for i := range h.Entries {
		st := h.Entries[i].Status
		if st == EntryPending || st == EntryReturned {
			return false
		}
	}
	return true
}

// OverlapNote 记录同一岗位两个重叠班次的重叠说明。
type OverlapNote struct {
	ID        string    `json:"id"`
	Position  string    `json:"position"`
	ShiftA    string    `json:"shift_a"`
	ShiftB    string    `json:"shift_b"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// JourneyEvent 是事项处理经过中的一条记录：可能来自事项自身历史
// （建立/修改/关闭/接收），也可能来自它参与的交接（发起交接、逐轮退回、
// 补充后重新提交、确认接收、继续跟踪）。交接相关事件带有交接编号与交班、
// 接班班次；Operator 为空表示该操作人未记录（旧数据可能缺人名）。
type JourneyEvent struct {
	At                 time.Time  // 实际发生时刻；TimeKnown 为 false 表示未记录
	TimeKnown          bool       // At 是否有效（旧数据可能缺时间）
	Kind               string     // created/updated/closed/received/handover-init/return/resubmit/confirm/track
	Operator           string     // 实际操作人；空表示未记录
	Detail             string     // 事项自身事件附带的说明
	HandoverID         string     // 交接相关事件所属的交接编号
	FromShift          string     // 交班班次
	ToShift            string     // 接班班次
	RoundSeq           int        // 退回/重新提交的轮次
	Reason             string     // 该轮完整退回原因
	Supplement         string     // 该轮补充说明
	SupplementOperator string     // 补充人
	SupplementAt       *time.Time // 补充时间
	TrackingNote       string     // 继续跟踪的跟踪说明
	FollowOwner        string     // 继续跟踪当时指定的后续负责人（不随后续修改改变）
}

// ItemJourney 是凭事项编号查询得到的完整处理经过：开头为事项最新状态，
// Events 是合并事项自身历史与各次交接经过后的时间线，Results 是该事项在
// 每次交接中的当前处理结果。
type ItemJourney struct {
	Item         Item
	Events       []JourneyEvent // 按实际发生时刻排序（不同时区按同一实际时刻比较）
	Results      []EntryView    // 按交接编号排列
	HasHandovers bool           // false 表示尚未参与交接
}

// EntryView 把交接单项与其所属交接编号关联，用于按班次查询。
type EntryView struct {
	HandoverID string
	FromShift  string
	ToShift    string
	Entry      HandoverEntry
}

// ShiftReport 是按班次查询得到的完整视图。
type ShiftReport struct {
	Shift        Shift
	OverlapNotes []OverlapNote
	// Items 是进行中班次的当前事项，或旧数据班次（无结束时记录）的当前事项。
	Items []Item
	// CloseItems 是已结束班次结束时刻的事项快照（已冻结），按事项编号排列。
	CloseItems []CloseItemSnapshot
	// ItemsAtClose 为 true 表示本次查到了结束时记录，CloseItems 即结束时事实。
	ItemsAtClose bool
	// HistoryIncomplete 为 true 表示该已结束班次是旧数据、缺少结束时记录，
	// Items 只是当前信息，不能视为结束时事实。
	HistoryIncomplete bool
	// LatestItems 按事项编号索引结束时事项的最新状态，与结束时信息对照。
	LatestItems map[string]Item
	Outgoing    *Handover
	Incoming    []Handover
	// Results 按事项编号汇总它在历次交接中的当前结果与退回历史。
	Results map[string][]EntryView
}
