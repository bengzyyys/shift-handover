package handover

import (
	"strings"
	"time"
)

// 本文件只承载 item-show 查询中的“接收记录合并”：判定事项自身历史里的
// received 事件与某次交接清单中的确认接收/继续跟踪是否同一次接收。
// 接班写盘（ProcessEntry）只用 receivedDetailText 生成说明；合并判定本身
// 是只读逻辑，不改变任何已保存数据，也不改变接班操作与数据格式。

// 事项历史中的接收说明模板。当前版本写入的 received 事件用交接编号、交班、
// 接班班次与接收方式完整标注；item-show 只有读到这种归属信息完整、且逐项
// 与交接清单一致的说明，才把它与清单接收认定为同一次接收。模板为固定格式，
// parseReceivedDetail 据其解析。
const (
	receivedDetailPrefix = "交接 "
	receivedDetailMid    = "（"
	receivedDetailArrow  = " -> "
	receivedDetailTail   = "）接班班次接收："
)

// 旧版本写入的接收说明形如“接班班次 S002 接收：继续跟踪…”：只明确记载接班
// 班次与接收方式，没有交接编号与交班班次。这种说明归属信息不完整，无法明确
// 认定是哪一次接收：它可能对应某次交接清单，也可能是清单之外的另一次接收
// （同一时刻、同一操作人、同一接班班次在业务上完全可能发生两次）。因此旧
// 格式一律不参与合并，只作为独立接收历史原样保留。

// receivedDetailText 生成接收事件的标准说明，明确记载交接编号、交班、接班班次
// 与接收方式；继续跟踪还保留当时的跟踪说明与后续负责人。
func receivedDetailText(h *Handover, action EntryAction, statusLabel, trackingNote, nextFollowOwner string) string {
	s := receivedDetailPrefix + h.ID + receivedDetailMid + h.FromShiftID +
		receivedDetailArrow + h.ToShiftID + receivedDetailTail + statusLabel
	if action == ActionTrack {
		s += "；跟踪说明：" + trackingNote + "；后续负责人：" + nextFollowOwner
	}
	return s
}

// receivedAttribution 是从接收说明中明确解析出的归属信息。只有四项全部有值
// 才可能用于合并判定；任何一项缺失都说明该说明归属不完整。
type receivedAttribution struct {
	handoverID string
	fromShift  string
	toShift    string
	kind       string // confirm/track，无法确定时为 ""
}

// receivedKindFromLabel 只识别两种已保存的接收方式中文名；track 的说明后还
// 带有“；跟踪说明：…”后缀。无法识别返回 ""，不据其他信息猜测。
func receivedKindFromLabel(label string) string {
	switch {
	case label == EntryConfirmed.Label():
		return "confirm"
	case label == EntryTracking.Label() || strings.HasPrefix(label, EntryTracking.Label()+"；"):
		return "track"
	}
	return ""
}

// complete 报告归属信息是否完整：交接编号、交班、接班班次与接收方式四项都
// 明确记载才算完整。缺任何一项（旧格式没有交接编号与交班班次）都无法明确
// 认定为某一次接收，不能据此合并。
func (a receivedAttribution) complete() bool {
	return a.handoverID != "" && a.fromShift != "" && a.toShift != "" && a.kind != ""
}

// parseReceivedDetail 解析本工具写入的固定格式说明（当前格式与旧版本格式），
// 提取其中明确记载的交接编号、交班、接班班次与接收方式。
//
// 只有当前版本的完整格式会返回 ok=true：四项归属信息都明确记载。旧版本格式
// 与残缺说明、自由文字一样返回 ok=false——旧格式不是解析失败，而是归属信息
// 不完整，仍按原文独立保留，绝不靠猜测补齐交接编号或交班班次。
func parseReceivedDetail(detail string) (receivedAttribution, bool) {
	if !strings.HasPrefix(detail, receivedDetailPrefix) {
		// 旧版本格式（“接班班次 S002 接收：…”）以及任何自由文字都没有
		// 明确记载完整归属，不参与合并。
		return receivedAttribution{}, false
	}
	rest := detail[len(receivedDetailPrefix):]
	pi := strings.Index(rest, receivedDetailMid)
	ai := strings.Index(rest, receivedDetailArrow)
	ti := strings.Index(rest, receivedDetailTail)
	if pi <= 0 || ai < 0 || ti < 0 || !(pi < ai && ai+len(receivedDetailArrow) < ti) {
		return receivedAttribution{}, false
	}
	attr := receivedAttribution{
		handoverID: rest[:pi],
		fromShift:  rest[pi+len(receivedDetailMid) : ai],
		toShift:    rest[ai+len(receivedDetailArrow) : ti],
		kind:       receivedKindFromLabel(rest[ti+len(receivedDetailTail):]),
	}
	if !attr.complete() {
		return receivedAttribution{}, false
	}
	return attr, true
}

// receivedReceipt 是一次交接清单接收处理（确认接收或继续跟踪）在合并判定中
// 使用的“接收凭据”：交接编号、交班与接班班次、接收方式，以及操作人与实际
// 发生时刻。处理时间缺失或为零值时不产生凭据（timeKnown=false）：这种记录
// 不参与合并，更不能拿交接发起时间代替。
type receivedReceipt struct {
	handoverID string
	fromShift  string
	toShift    string
	kind       string // confirm/track
	operator   string
	at         time.Time
	timeKnown  bool
}

// receiptFromEntry 从交接清单单项当前保存的接收结果构造凭据。只有结果明确为
// 确认接收或继续跟踪、且处理时间真实记录时才可用于合并；其余情况返回
// timeKnown=false。
func receiptFromEntry(h *Handover, e *HandoverEntry) receivedReceipt {
	rcpt := receivedReceipt{
		handoverID: h.ID,
		fromShift:  h.FromShiftID,
		toShift:    h.ToShiftID,
		operator:   e.Operator,
	}
	switch {
	case e.Status == EntryConfirmed:
		rcpt.kind = "confirm"
	case e.Status == EntryTracking:
		rcpt.kind = "track"
	default:
		return rcpt
	}
	if validProcessedAt(e.ProcessedAt) {
		rcpt.at, rcpt.timeKnown = *e.ProcessedAt, true
	}
	return rcpt
}

// sameReceivedEvent 判定一条事项自身 received 事件与一次交接接收是否明确为
// 同一次接收。所有条件都必须满足：
//   - 操作人相同、实际发生时刻相同（time.Time.Equal 按同一实际时刻比较，
//     不同时区写法表示同一时刻仍视为相同）；
//   - 接收说明必须是当前版本写入的完整固定格式，明确记载交接编号、交班、
//     接班班次与接收方式；旧版本格式（缺交接编号、交班班次）、自由文字或
//     残缺说明归属不完整，一律不合并且不补字段；
//   - 说明中明确记载的交接编号、交班、接班班次与接收方式逐项与清单凭据一致；
//     任一项指向其他交接、其他班次或另一种接收方式即为矛盾，保留原记录。
//
// 不能只因部分信息吻合（如同操作人、同时刻、同接班班次）就认定为同一次
// 接收，也不能因事项当前处于接班班次而合并。
func sameReceivedEvent(iev ItemEvent, rcpt receivedReceipt) bool {
	if iev.Kind != "received" || !iev.At.Equal(rcpt.at) || iev.Operator != rcpt.operator {
		return false
	}
	attr, ok := parseReceivedDetail(iev.Detail)
	if !ok {
		return false
	}
	return attr.handoverID == rcpt.handoverID &&
		attr.fromShift == rcpt.fromShift &&
		attr.toShift == rcpt.toShift &&
		attr.kind == rcpt.kind
}

// receivedMerge 在遍历各次交接清单时记录事项自身 received 事件中哪些与某次
// 交接接收同属一次。匹配后该事项事件在时间线中只以信息更全的交接事件展示；
// 未匹配的 received 事件由调用方原样保留。
//
// 一条事项事件至多匹配一次，一次交接接收至多匹配一条事项事件。逐条按事项
// 事件的保存顺序取第一条全部事实相符的记录，因此调换两条历史的保存顺序
// 不会改变结果：归属不完整（旧说明）或指向其他交接/班次/方式的记录会被
// 跳过，继续寻找真正同一次的完整记录，而不是隐藏它；只有部分信息吻合的
// 旧说明永远不会被标记，始终独立保留。
type receivedMerge struct {
	matched []bool
	events  []ItemEvent
}

func newReceivedMerge(events []ItemEvent) *receivedMerge {
	return &receivedMerge{matched: make([]bool, len(events)), events: events}
}

// consider 为一次交接接收标记同一次接收的事项事件；处理时间未记录的接收不
// 参与合并。调用顺序（各交接按编号排列）不影响最终匹配集合。
func (m *receivedMerge) consider(rcpt receivedReceipt) {
	if !rcpt.timeKnown {
		return
	}
	for i := range m.events {
		if m.matched[i] {
			continue
		}
		if sameReceivedEvent(m.events[i], rcpt) {
			m.matched[i] = true
			return
		}
	}
}

// isUsed 报告该事项事件是否已被某次交接接收合并。
func (m *receivedMerge) isUsed(i int) bool { return m.matched[i] }
