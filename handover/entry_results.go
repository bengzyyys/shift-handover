package handover

import "sort"

// 本文件只承载“各次交接当前结果”的整理规则，item-show（按事项查询）与
// shift-show（按班次查询）共用同一份实现，不再在两处分别维护同一条规则。
// 两处的选取范围不同，但每条结果怎么组装、按什么次序排列、是否独立副本，
// 全部由这里的同一套函数决定：
//
//   - 按事项查询（entryViewsForItem）：列出该事项参与的全部交接——即使它已经
//     转到后面的班次或后来关闭，也不能只留最近一条；结果按交接编号排列。
//   - 按班次查询（entryViewsForShift）：只汇总与所查班次有直接交班或接班
//     关系的交接；同一事项在该班既交入又交出时，两条结果分别保留，绝不混入
//     该事项在其他班次之间的交接；同一事项下按交接编号排列（对应报告按事项
//     编号排列、同一事项按交接编号排列的展示次序）。
//
// 每条结果中的后续负责人、跟踪说明、处理人、处理时间和各轮退回补充内容，
// 一律使用对应交接清单上保存的值（深拷贝），不用事项最新信息覆盖。返回的
// 视图彼此独立、也与存储及同一份报告内嵌的交接清单脱钩：调用方暂存结果或
// 为本地展示改动其中的处理时间、退回原因、补充说明和补充时间，不改变保存
// 的数据、先前取得的另一份结果或内嵌交接清单的对应内容；之后正常重新提交
// 或接收，已取得的结果保持原样，再次查询才反映新内容。

// entryViewFor 按统一规则把一份交接清单中的单项整理为当前结果视图：
// 带上交接编号、交班与接班班次，单项内容（含处理时间与历次退回/补充记录）
// 深拷贝为独立副本。结果中的后续负责人、跟踪说明、处理人、处理时间与各轮
// 退回补充全部沿用该交接保存的值，不从事项最新状态取值。
func entryViewFor(h *Handover, e *HandoverEntry) EntryView {
	return EntryView{
		HandoverID: h.ID,
		FromShift:  h.FromShiftID,
		ToShift:    h.ToShiftID,
		Entry:      cloneEntry(*e),
	}
}

// entryViewsForItem 整理一个事项在给定交接中的当前结果。传入的交接由调用方
// 按交接编号排好序（见 itemHandovers），且只包含清单中确有该事项的交接；
// 这里逐条复制同一组装规则，事项后来转到后面的班次或已关闭都不影响它参与
// 过的交接全部列出。
func entryViewsForItem(hs []*Handover, itemID string) []EntryView {
	out := []EntryView{}
	for _, h := range hs {
		if e, _ := findEntry(h, itemID); e != nil {
			out = append(out, entryViewFor(h, e))
		}
	}
	return out
}

// entryViewsForShift 汇总与所查班次有直接交班或接班关系的交接中全部单项的
// 当前结果，按事项编号归入返回的映射，同一事项下的结果按交接编号排列。
// 与所查班次既不交班也不接班的交接一律不进入汇总：同一事项在该班既交入又
// 交出时得到各自独立的两条结果，该事项在其他班次之间的交接不混入。
func entryViewsForShift(d *Data, shiftID string) map[string][]EntryView {
	out := map[string][]EntryView{}
	for i := range d.Handovers {
		h := &d.Handovers[i]
		if h.FromShiftID != shiftID && h.ToShiftID != shiftID {
			continue
		}
		for j := range h.Entries {
			e := &h.Entries[j]
			out[e.ItemID] = append(out[e.ItemID], entryViewFor(h, e))
		}
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool {
			return out[k][i].HandoverID < out[k][j].HandoverID
		})
	}
	return out
}
