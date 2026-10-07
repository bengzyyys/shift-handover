package handover

import "sort"

// 本文件只承载“各次交接当前结果”（EntryView）的汇总，是 item-show 与
// shift-show 两个查询入口共用的同一条规则，避免同一份汇总逻辑在事项查询
// （journey.go）与班次报告（service.go）中分别维护：
//
//   - 每条结果整理什么：交接编号、交班与接班班次，以及该交接清单中单项当前
//     保存的处理结果——状态、后续负责人、跟踪说明、处理人、处理时间和各轮
//     退回补充记录；一律使用该交接保存的快照值，不用事项最新信息覆盖；
//   - 副本独立性：每条结果都是独立深拷贝（含处理时间与历次退回/补充记录），
//     调用方为本地展示改动结果不影响存储、先前取得的另一份结果，或同一班次
//     报告内嵌交接清单的对应内容；
//   - 排列次序：同一事项的结果按交接编号排列。
//
// 两个入口的选取范围不同，各自在调用前确定，本文件不改变选取行为：
// item-show 列出该事项参与的全部交接（即使已转到后续班次或已关闭），
// shift-show 只取与所查班次有直接交班或接班关系的交接，同一事项既交入又
// 交出时两条结果分别保留。两个入口对同一（事项，交接）得到的事实与排列
// 次序由这里的同一实现保证一致。本文件只读存储记录，不改变事项流转、
// 交接完成判断、班次结束时记录或本地数据格式。

// entryViewFor 把交接清单中单项当前保存的值整理为一条“交接当前结果”。
// 交接编号、交班与接班班次取自该交接记录；单项整体深拷贝，后续负责人、
// 跟踪说明、处理人、处理时间与各轮退回补充内容都沿用这次交接保存的值。
func entryViewFor(h *Handover, e *HandoverEntry) EntryView {
	return EntryView{
		HandoverID: h.ID,
		FromShift:  h.FromShiftID,
		ToShift:    h.ToShiftID,
		Entry:      cloneEntry(*e),
	}
}

// sortEntryViews 把交接当前结果按交接编号排列。事项查询与班次报告共用
// 这同一条次序规则。
func sortEntryViews(vs []EntryView) {
	sort.Slice(vs, func(i, j int) bool { return vs[i].HandoverID < vs[j].HandoverID })
}

// entryViewsForItem 汇总某事项在给定交接中的当前结果，只收集清单中确有
// 该事项的交接，结果按交接编号排列。item-show 用它列出该事项参与的全部
// 交接：事项已转到后续班次或后来关闭不影响收集，仍列出全部交接，不只留
// 最近一条。
func entryViewsForItem(hs []*Handover, itemID string) []EntryView {
	out := make([]EntryView, 0, len(hs))
	for _, h := range hs {
		if e, _ := findEntry(h, itemID); e != nil {
			out = append(out, entryViewFor(h, e))
		}
	}
	sortEntryViews(out)
	return out
}

// entryViewsByItem 汇总给定交接中的全部单项当前结果，按事项编号分组；
// 同一事项的多条结果（例如该事项对同一班次既交入又交出，或班次报告同时
// 纳入交入与交出交接时）分别保留并按交接编号排列，不混入未给定的其他
// 交接。shift-show 只传入与所查班次有直接交班或接班关系的交接。
// 返回的 map 与其中每条结果都是独立副本。
func entryViewsByItem(hs []*Handover) map[string][]EntryView {
	out := map[string][]EntryView{}
	for _, h := range hs {
		for i := range h.Entries {
			out[h.Entries[i].ItemID] = append(out[h.Entries[i].ItemID], entryViewFor(h, &h.Entries[i]))
		}
	}
	for itemID := range out {
		sortEntryViews(out[itemID])
	}
	return out
}
