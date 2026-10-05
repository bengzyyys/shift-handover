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
// 与接收方式完整标注；item-show 合并接收记录时要求这些归属信息全部明确记载且
// 与交接清单一致，不能仅凭操作人与时刻合并，也不能只靠部分信息吻合。模板为
// 固定格式，parseReceivedDetail 据其解析。
const (
	receivedDetailPrefix = "交接 "
	receivedDetailMid    = "（"
	receivedDetailArrow  = " -> "
	receivedDetailTail   = "）接班班次接收："
	// 旧版本写入的说明只明确记载接班班次与接收方式，没有交接编号与交班班次，
	// 例如“接班班次 S002 接收：确认接收”。缺少归属信息的旧说明不能认定为
	// 某次交接的接收，一律作为独立接收历史保留，不替它补交接编号或交班班次。
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
// 说明没有明确记载这一项；合并要求四项全部明确记载，任一缺失即不能认定为
// 同一次接收（解析仍如实提取已记载的部分，不凭空补全）。
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
// 接收。只有能够明确认定的两条记录才合并，依据只来自已记载的事实：
//   - 操作人相同、实际发生时刻相同（time.Time.Equal 按同一实际时刻比较，
//     不同时区写法表示同一时刻仍视为相同）；
//   - 接收说明必须是本工具写入的完整固定格式，明确记载交接编号、交班班次、
//     接班班次与接收方式，且四项都与凭据一致；自由文字或残缺说明无法确认
//     归属，一律不合并且不补字段；
//   - 旧格式说明（“接班班次 S002 接收：…”）没有明确记载交接编号与交班班次，
//     即使接班班次与接收方式恰好吻合，也只是部分信息相符，不能认定为同一
//     次接收：旧说明作为独立接收历史原样保留，不替它补交接编号或交班班次；
//   - 明确记载的交接编号、任一班次或接收方式不同即为矛盾，保留原记录。
//
// 不能只因操作人相同、部分信息吻合，或事项当前处于接班班次，就认定为同一次
// 接收。
func sameReceivedEvent(iev ItemEvent, rcpt receivedReceipt) bool {
	if iev.Kind != "received" || !iev.At.Equal(rcpt.at) || iev.Operator != rcpt.operator {
		return false
	}
	attr, ok := parseReceivedDetail(iev.Detail)
	if !ok {
		return false
	}
	// 四项归属信息都必须明确记载且与凭据一致。旧格式解析出的 handoverID 与
	// fromShift 为空，而凭据中它们恒非空，直接比较即自然排除旧格式说明，
	// 无须也不允许按“未记载不比较”放行。
	return attr.handoverID == rcpt.handoverID &&
		attr.fromShift == rcpt.fromShift &&
		attr.toShift == rcpt.toShift &&
		attr.kind == rcpt.kind
}

// receivedMerge 在遍历各次交接清单时记录事项自身 received 事件中哪些与某次
// 交接接收同属一次。匹配后该事项事件在时间线中只以信息更全的交接事件展示；
// 未匹配的 received 事件由调用方原样保留。
//
// 一条事项事件至多匹配一次，一次交接接收至多匹配一条事项事件。只有完整记载
// 且全部一致的记录才参与匹配：缺少归属信息的旧说明、指向其他交接/班次/方式
// 的说明都不会被认领，因此调换两条历史的保存顺序不会改变结果——当一条旧说明
// 与一条完整说明都与同一清单记录部分吻合时，被合并的始终是完整对应的那一条，
// 旧说明始终保留，与存放先后无关。
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
