package handover

import (
	"strings"
	"time"
)

// 本文件只负责 item-show（ItemJourney）中“接收记录合并”这一件事：
// 接收说明记载了哪些事实、如何从新旧两种固定格式中读出这些事实，以及交接
// 清单中的接收与事项自身保存的接收历史在什么条件下才算同一次接收。
// 交接处理的写入侧（ProcessEntry）与这里共用同一套说明格式，保证写入与
// 判读一致；本文件不改变保存的数据格式，也不参与任何写入判定。

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

// agreesWith 报告说明中明确记载的各项事实是否与交接清单中的一次接收一致。
// 空字段表示旧格式或残缺说明没有记载该项，既不视为一致也不视为矛盾，不参与
// 比较；只有明确记载且不同才算冲突。接收方式无法识别（kind 为空）时同样
// 不据其他信息猜测。
func (a receivedAttribution) agreesWith(rec receivedReception) bool {
	return (a.handoverID == "" || a.handoverID == rec.handoverID) &&
		(a.fromShift == "" || a.fromShift == rec.fromShift) &&
		(a.toShift == "" || a.toShift == rec.toShift) &&
		(a.kind == "" || a.kind == rec.kind)
}

// receivedReception 是交接清单一侧的一次已保存接收（确认接收或继续跟踪），
// 带有判定同一次接收所需的全部清单事实。只有处理时间真实记录（非 nil、非
// 零值）的接收才会被收集：缺失或零值时间不能参与合并，更不用发起时间等
// 其他时刻代替。
type receivedReception struct {
	handoverID string
	fromShift  string
	toShift    string
	kind       string // confirm/track
	operator   string
	at         time.Time // 实际发生时刻，不同时区写法按同一时刻比较
}

// mergeItemReceivedEvents 在事项自身历史中找出与交接清单接收属于同一次接收
// 的 received 事件，返回与 events 等长的标记：true 表示该条已被交接一侧的
// 接收覆盖，处理经过中不再单列，改以信息更全的交接事件展示。
//
// 是否同一次接收只依据已记载的事实，条件全部满足才合并：
//   - 操作人相同，且实际发生时刻相同（time.Time.Equal 按同一实际时刻比较，
//     11:00+08:00 与 10:00+07:00 视为同一时刻）；
//   - 接收说明中明确记载的交接编号、交班、接班班次与接收方式，与清单中的
//     这次接收均不冲突（见 agreesWith）；旧格式缺少的编号不要求补齐。
//
// 以下情况一律各自保留、原样展示：说明明确指向其他交接、其他交班/接班班次
// 或另一种接收方式；自由文字或残缺说明无法确认归属（不凭空补交接编号）。
// 不能只因为操作人相同，或事项当前正处在接班班次，就认定是同一次接收。
//
// receptions 按交接编号顺序传入；每条清单接收独立扫描全部尚未被认领的历史
// 记录，因此两条历史的存放先后不影响哪一条被合并；每条历史记录最多被认领
// 一次。本函数只读数据，不修改 events 的任何内容。
func mergeItemReceivedEvents(events []ItemEvent, receptions []receivedReception) []bool {
	used := make([]bool, len(events))
	for _, rec := range receptions {
		for i := range events {
			if used[i] {
				continue
			}
			iev := &events[i]
			if iev.Kind != "received" {
				continue
			}
			if iev.Operator != rec.operator || !iev.At.Equal(rec.at) {
				continue
			}
			// 操作人与实际时刻相同只是前提；说明无法解析（自由文字、残缺）
			// 或明确记载与清单冲突时保留原记录，继续查找真正同一次的记录。
			attr, ok := parseReceivedDetail(iev.Detail)
			if !ok || !attr.agreesWith(rec) {
				continue
			}
			used[i] = true
			break
		}
	}
	return used
}
