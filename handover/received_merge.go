package handover

import (
	"strings"
	"time"
)

// 本文件只承载 item-show 查询中的“接收记录合并”：判定事项自身历史里的
// received 事件与某次交接清单中的确认接收/继续跟踪是否同一次接收。
// 接班写盘（ProcessEntry）只用 receivedDetailText 生成说明；合并判定本身
// 是只读逻辑，不改变任何已保存数据，也不改变接班操作与数据格式。

// 事项历史中的接收说明模板。新写入的 received 事件用交接编号、交班、接班班次
// 与接收方式完整标注；item-show 合并接收记录时依据这些明确记载判定同一次接收，
// 不能仅凭操作人与时刻合并。模板为固定格式，parseReceivedDetail 据其解析。
const (
	receivedDetailPrefix = "交接 "
	receivedDetailMid    = "（"
	receivedDetailArrow  = " -> "
	receivedDetailTail   = "）接班班次接收："
	// 旧版本写入的说明只明确记载接班班次与接收方式，没有交接编号与交班班次，
	// 例如“接班班次 S002 接收：确认接收”。其中明确记载的事实仍可用于比对，
	// 未记载的字段不比较，更不凭空补齐。
	legacyReceivedPrefix = "接班班次 "
	legacyReceivedTail   = " 接收："
)

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

// receivedAttribution 是从接收说明中明确解析出的归属信息。空字符串表示该
// 说明没有明确记载这一项，比对时不得据此判断一致或矛盾。
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

// parseReceivedDetail 只解析本工具写入的固定格式说明（含旧版本格式），提取
// 其中明确记载的交接编号、交班、接班班次与接收方式。其他文字（外部写入或
// 残缺的说明）一律视为没有明确记载，返回 ok=false，绝不靠猜测补全。
func parseReceivedDetail(detail string) (receivedAttribution, bool) {
	switch {
	case strings.HasPrefix(detail, receivedDetailPrefix):
		rest := detail[len(receivedDetailPrefix):]
		pi := strings.Index(rest, receivedDetailMid)
		ai := strings.Index(rest, receivedDetailArrow)
		ti := strings.Index(rest, receivedDetailTail)
		if pi <= 0 || ai < 0 || ti < 0 || !(pi < ai && ai+len(receivedDetailArrow) < ti) {
			return receivedAttribution{}, false
		}
		hid := rest[:pi]
		from := rest[pi+len(receivedDetailMid) : ai]
		to := rest[ai+len(receivedDetailArrow) : ti]
		kind := receivedKindFromLabel(rest[ti+len(receivedDetailTail):])
		if hid == "" || from == "" || to == "" || kind == "" {
			return receivedAttribution{}, false
		}
		return receivedAttribution{handoverID: hid, fromShift: from, toShift: to, kind: kind}, true
	case strings.HasPrefix(detail, legacyReceivedPrefix):
		rest := detail[len(legacyReceivedPrefix):]
		ti := strings.Index(rest, legacyReceivedTail)
		if ti <= 0 {
			return receivedAttribution{}, false
		}
		to := rest[:ti]
		kind := receivedKindFromLabel(rest[ti+len(legacyReceivedTail):])
		if to == "" || kind == "" {
			return receivedAttribution{}, false
		}
		// 旧格式只明确记载接班班次与接收方式；交接编号、交班班次未记载。
		return receivedAttribution{toShift: to, kind: kind}, true
	}
	return receivedAttribution{}, false
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

// sameReceivedEvent 判定一条事项自身 received 事件与一次交接接收是否同一次
// 接收。依据只来自已记载的事实：
//   - 操作人相同、实际发生时刻相同（time.Time.Equal 按同一实际时刻比较，
//     不同时区写法表示同一时刻仍视为相同）；
//   - 接收说明必须是本工具写入的固定格式（新格式或旧格式），自由文字或残缺
//     说明无法确认归属，一律不合并且不补字段；
//   - 说明中明确记载的交接编号、交班、接班班次与接收方式，逐项与凭据一致或
//     未记载（旧格式没有交接编号与交班班次，不要求补齐）；明确指向其他交接、
//     其他班次或另一种接收方式即为矛盾，保留原记录。
//
// 不能只因操作人相同，或事项当前处于接班班次，就认定为同一次接收。
func sameReceivedEvent(iev ItemEvent, rcpt receivedReceipt) bool {
	if iev.Kind != "received" || !iev.At.Equal(rcpt.at) || iev.Operator != rcpt.operator {
		return false
	}
	attr, ok := parseReceivedDetail(iev.Detail)
	if !ok {
		return false
	}
	return (attr.handoverID == "" || attr.handoverID == rcpt.handoverID) &&
		(attr.fromShift == "" || attr.fromShift == rcpt.fromShift) &&
		(attr.toShift == "" || attr.toShift == rcpt.toShift) &&
		(attr.kind == "" || attr.kind == rcpt.kind)
}

// receivedMerge 在遍历各次交接清单时记录事项自身 received 事件中哪些与某次
// 交接接收同属一次。匹配后该事项事件在时间线中只以信息更全的交接事件展示；
// 未匹配的 received 事件由调用方原样保留。
//
// 一条事项事件至多匹配一次，一次交接接收至多匹配一条事项事件。逐条按事项
// 事件的保存顺序取第一条全部事实相符的记录，因此调换两条历史的保存顺序
// 不会改变结果：指向其他交接/班次/方式的记录会被跳过，继续寻找真正同一次
// 的记录，而不是隐藏它。
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
