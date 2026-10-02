package handover

import (
	"strings"
	"time"
)

// Severity 表示事项的严重程度。
type Severity string

const (
	SeverityNormal    Severity = "normal"    // 普通
	SeverityImportant Severity = "important" // 重要
	SeverityUrgent    Severity = "urgent"   // 紧急
)

// ParseSeverity 解析严重程度，接受中文（普通/重要/紧急）或英文（normal/important/urgent）。
// 空字符串按普通处理。
func ParseSeverity(s string) (Severity, error) {
	switch strings.TrimSpace(s) {
	case "", "普通", "normal":
		return SeverityNormal, nil
	case "重要", "important":
		return SeverityImportant, nil
	case "紧急", "urgent":
		return SeverityUrgent, nil
	}
	return "", &ValidationError{Msg: "无效的严重程度: " + s + "（可选 普通/重要/紧急 或 normal/important/urgent）"}
}

// String 返回严重程度的中文显示。
func (s Severity) String() string {
	switch s {
	case SeverityImportant:
		return "重要"
	case SeverityUrgent:
		return "紧急"
	default:
		return "普通"
	}
}

// Result 表示交接事项的处理结果。
type Result string

const (
	ResultPending  Result = "pending"  // 待处理
	ResultReceived Result = "received" // 已接收
	ResultReturned Result = "returned" // 已退回
	ResultTracking Result = "tracking" // 继续跟踪
)

// String 返回处理结果的中文显示。
func (r Result) String() string {
	switch r {
	case ResultReceived:
		return "已接收"
	case ResultReturned:
		return "已退回"
	case ResultTracking:
		return "继续跟踪"
	default:
		return "待处理"
	}
}

// Shift 表示一个班次。
type Shift struct {
	ID      int       `json:"id"`
	Post    string    `json:"post"`    // 岗位
	Owner   string    `json:"owner"`   // 负责人
	Start   time.Time `json:"start"`   // 开始时间（带时区）
	End     time.Time `json:"end"`     // 结束时间（带时区）
	Ended   bool      `json:"ended"`   // 是否已结束
	OverlapNote string `json:"overlapNote,omitempty"` // 重叠说明（与同岗位已有班次重叠时必填）
	OverlapIDs  []int  `json:"overlapIds,omitempty"`  // 重叠说明涉及的班次编号
	CreatedAt time.Time `json:"createdAt"`
}

// Event 是事项的一条历史记录。
type Event struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`               // 建立/修改/关闭/接收/退回/继续跟踪/补充说明
	Detail   string    `json:"detail,omitempty"`   // 详情
	Operator string    `json:"operator,omitempty"` // 操作人（如适用）
}

// Item 表示一条未关闭事项。
type Item struct {
	ID             int       `json:"id"`
	Content        string    `json:"content"`                  // 内容（必填）
	Severity       Severity  `json:"severity"`                 // 严重程度
	Constraints    string    `json:"constraints,omitempty"`    // 限制条件（可为空）
	FollowOwner    string    `json:"followOwner"`              // 后续负责人（必填）
	OriginShiftID  int       `json:"originShiftId"`            // 来源班次
	CurrentShiftID int       `json:"currentShiftId"`           // 当前所在班次
	Closed         bool      `json:"closed"`                   // 是否已关闭
	CreatedAt      time.Time `json:"createdAt"`
	History        []Event   `json:"history"`
}

// Note 是退回后交班人追加的补充说明。
type Note struct {
	Time     time.Time `json:"time"`
	Operator string    `json:"operator"` // 操作人
	Text     string    `json:"text"`     // 说明内容（必填）
}

// HandoverItem 是交接记录中一项事项的处理状态。
type HandoverItem struct {
	ItemID       int       `json:"itemId"`
	Result       Result    `json:"result"`
	Operator     string    `json:"operator,omitempty"`     // 处理操作人
	HandledAt    time.Time `json:"handledAt,omitempty"`    // 处理时间
	TrackNote    string    `json:"trackNote,omitempty"`    // 继续跟踪说明
	TrackOwner   string    `json:"trackOwner,omitempty"`   // 继续跟踪后续负责人
	ReturnReason string    `json:"returnReason,omitempty"` // 退回原因（原文保留，不覆盖）
	Notes        []Note    `json:"notes,omitempty"`        // 历次补充说明
}

// Handover 表示一个班次交接记录。
type Handover struct {
	ID          int            `json:"id"`
	FromShiftID int            `json:"fromShiftId"` // 交班班次
	ToShiftID   int            `json:"toShiftId"`   // 接班班次
	Items       []*HandoverItem `json:"items"`
	Complete    bool           `json:"complete"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// IsComplete 判断交接是否完成：所有事项均已接收或继续跟踪；空清单直接完成。
func (h *Handover) IsComplete() bool {
	for _, hi := range h.Items {
		if hi.Result != ResultReceived && hi.Result != ResultTracking {
			return false
		}
	}
	return true
}

// recompute 根据当前各项结果重新计算完成状态。
func (h *Handover) recompute() {
	h.Complete = h.IsComplete()
}

// FindItem 在交接记录中查找指定事项的交接状态，找不到返回 nil。
func (h *Handover) FindItem(itemID int) *HandoverItem {
	for _, hi := range h.Items {
		if hi.ItemID == itemID {
			return hi
		}
	}
	return nil
}
