package handover

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const displayTime = "2006-01-02 15:04:05 -07:00"

func fmtTime(t time.Time) string { return t.Format(displayTime) }

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return fmtTime(*t)
}

// FormatShift 格式化班次摘要。
func FormatShift(sh Shift) string {
	state := "进行中"
	if sh.Closed {
		state = "已结束 " + fmtTimePtr(sh.ClosedAt)
	}
	return fmt.Sprintf("%s  岗位=%s  负责人=%s  %s ~ %s  [%s]",
		sh.ID, sh.Position, sh.Owner, fmtTime(sh.Start), fmtTime(sh.End), state)
}

// FormatItem 格式化事项及其关闭情况与历史。
func FormatItem(it Item) string {
	var b strings.Builder
	state := "未关闭"
	if it.Closed {
		state = fmt.Sprintf("已关闭（%s 于 %s）", it.CloseOperator, fmtTimePtr(it.ClosedAt))
	}
	fmt.Fprintf(&b, "%s  严重程度=%s  当前班次=%s  原始班次=%s  [%s]\n",
		it.ID, it.Severity.Label(), it.CurrentShiftID, it.OriginShiftID, state)
	fmt.Fprintf(&b, "  内容：%s\n", it.Content)
	fmt.Fprintf(&b, "  限制条件：%s\n", dashIfEmpty(it.Constraints))
	fmt.Fprintf(&b, "  后续负责人：%s\n", it.FollowOwner)
	fmt.Fprintf(&b, "  流经班次：%s\n", strings.Join(it.ShiftIDs, " -> "))
	if len(it.Events) > 0 {
		b.WriteString("  历史：\n")
		for _, ev := range it.Events {
			who := ""
			if ev.Operator != "" {
				who = " 操作人=" + ev.Operator
			}
			detail := ""
			if ev.Detail != "" {
				detail = " " + ev.Detail
			}
			fmt.Fprintf(&b, "    - %s %s%s%s\n", fmtTime(ev.At), ev.Kind, who, detail)
		}
	}
	return b.String()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// recOrMissing 展示历史中的人名：缺失时明确标为未记录，不推测补齐。
func recOrMissing(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未记录"
	}
	return s
}

// fmtTimeMissing 展示历史时间：缺失时明确标为未记录。
func fmtTimeMissing(t *time.Time) string {
	if t == nil {
		return "未记录"
	}
	return fmtTime(*t)
}

// itemHistEvent 是事项历史时间线上的一条经过。时间一律用实际时刻比较，
// 带不同时区的时间按同一实际时刻排序；时间相同时按 group、seq 排序：
// group=0 为事项自带事件，group 越大交接编号越靠前；同一交接内按
// 发起、该轮退回、该轮重新提交、后续处理的先后关系排列。
type itemHistEvent struct {
	at    time.Time
	group int
	seq   int
	line  string
}

// FormatItemReport 格式化事项完整查询：开头展示事项最新内容、严重程度、
// 限制条件、后续负责人、当前所在班次、关闭情况与流经班次；历史部分保留
// 已有的创建、修改、关闭信息，并加入该事项参与的交接发起、逐轮退回、
// 补充后重新提交、确认接收或继续跟踪；最后明确列出每次交接的当前处理结果。
func FormatItemReport(rep ItemReport) string {
	var b strings.Builder
	it := rep.Item
	state := "未关闭"
	if it.Closed {
		state = fmt.Sprintf("已关闭（%s 于 %s）", it.CloseOperator, fmtTimePtr(it.ClosedAt))
	}
	fmt.Fprintf(&b, "%s  严重程度=%s  当前班次=%s  原始班次=%s  [%s]\n",
		it.ID, it.Severity.Label(), it.CurrentShiftID, it.OriginShiftID, state)
	fmt.Fprintf(&b, "  内容：%s\n", it.Content)
	fmt.Fprintf(&b, "  限制条件：%s\n", dashIfEmpty(it.Constraints))
	fmt.Fprintf(&b, "  后续负责人：%s\n", it.FollowOwner)
	fmt.Fprintf(&b, "  流经班次：%s\n", strings.Join(it.ShiftIDs, " -> "))

	// 标记与交接接收重复的事项自带 received 事件：同一次接收只展示一次，
	// 改由交接经过展示交接编号等完整信息；创建、修改、关闭记录不受影响。
	matchedReceived := map[int]bool{}
	for _, ih := range rep.Handovers {
		h, e := ih.Handover, ih.Entry
		if !e.Status.Received() || e.ProcessedAt == nil {
			continue
		}
		for i, ev := range it.Events {
			if ev.Kind != "received" || matchedReceived[i] {
				continue
			}
			if ev.At.Equal(*e.ProcessedAt) && ev.Operator == e.Operator &&
				strings.Contains(ev.Detail, h.ToShiftID) {
				matchedReceived[i] = true
			}
		}
	}

	events := []itemHistEvent{}
	for i, ev := range it.Events {
		if matchedReceived[i] {
			continue
		}
		line := fmt.Sprintf("%s %s", fmtTime(ev.At), ev.Kind)
		if ev.Operator != "" {
			line += " 操作人=" + ev.Operator
		}
		if ev.Detail != "" {
			line += " " + ev.Detail
		}
		events = append(events, itemHistEvent{at: ev.At, group: 0, seq: i, line: line})
	}
	for hi, ih := range rep.Handovers {
		h, e := ih.Handover, ih.Entry
		group := hi + 1
		// 交接发起：数据中没有操作人记录，明确显示未记录，不用班次负责人代替。
		events = append(events, itemHistEvent{
			at: h.CreatedAt, group: group, seq: 1,
			line: fmt.Sprintf("%s 交接发起 交接=%s（%s -> %s） 操作人=%s",
				fmtTime(h.CreatedAt), h.ID, h.FromShiftID, h.ToShiftID, "未记录"),
		})
		for _, r := range e.Rounds {
			events = append(events, itemHistEvent{
				at: r.ReturnedAt, group: group, seq: 2*r.Seq,
				line: fmt.Sprintf("%s 退回 交接=%s（%s -> %s） 第%d轮 操作人=%s 原因=%s",
					fmtTime(r.ReturnedAt), h.ID, h.FromShiftID, h.ToShiftID,
					r.Seq, recOrMissing(r.ReturnOperator), r.Reason),
			})
			if r.ResubmittedAt != nil {
				events = append(events, itemHistEvent{
					at: *r.ResubmittedAt, group: group, seq: 2*r.Seq + 1,
					line: fmt.Sprintf("%s 重新提交 交接=%s（%s -> %s） 第%d轮 补充说明=%s 补充人=%s 补充时间=%s 重新提交时间=%s",
						fmtTime(*r.ResubmittedAt), h.ID, h.FromShiftID, h.ToShiftID,
						r.Seq, r.Supplement, recOrMissing(r.SupplementOperator),
						fmtTimeMissing(r.SupplementAt), fmtTime(*r.ResubmittedAt)),
				})
			}
		}
		if e.Status.Received() {
			kind := "确认接收"
			if e.Status == EntryTracking {
				kind = "继续跟踪"
			}
			line := fmt.Sprintf("%s %s 交接=%s（%s -> %s） 操作人=%s",
				fmtTimePtr(e.ProcessedAt), kind, h.ID, h.FromShiftID, h.ToShiftID,
				recOrMissing(e.Operator))
			if e.Status == EntryTracking {
				// 当次跟踪说明与指定的后续负责人以交接记录为准，事后修改事项
				// 负责人不改变这里的历史值。
				line += fmt.Sprintf(" 跟踪说明=%s 后续负责人=%s",
					dashIfEmpty(e.TrackingNote), recOrMissing(e.FollowOwner))
			}
			events = append(events, itemHistEvent{
				at: *e.ProcessedAt, group: group, seq: 1000, line: line,
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		if events[i].group != events[j].group {
			return events[i].group < events[j].group
		}
		return events[i].seq < events[j].seq
	})

	b.WriteString("  历史：\n")
	for _, ev := range events {
		fmt.Fprintf(&b, "    - %s\n", ev.line)
	}

	// 该事项在每次交接中的当前处理结果。
	b.WriteString("  交接当前结果：\n")
	if len(rep.Handovers) == 0 {
		b.WriteString("    暂无交接记录\n")
	}
	for _, ih := range rep.Handovers {
		h, e := ih.Handover, ih.Entry
		fmt.Fprintf(&b, "    交接=%s（%s -> %s）：%s\n",
			h.ID, h.FromShiftID, h.ToShiftID, entryCurrentResult(e))
	}
	return b.String()
}

// entryCurrentResult 描述事项在一次交接中的当前处理结果：退回尚未重新提交
// 时等待交班人补充；重新提交后接班人尚未处理；已接收则确认或继续跟踪。
func entryCurrentResult(e HandoverEntry) string {
	switch e.Status {
	case EntryReturned:
		return "退回，等待交班人补充"
	case EntryPending:
		if len(e.Rounds) > 0 {
			return "待处理，接班人尚未处理（上一轮退回已重新提交，退回人保留在历史中）"
		}
		return "待处理"
	case EntryConfirmed:
		return "已确认接收"
	case EntryTracking:
		return "已继续跟踪"
	}
	return e.Status.Label()
}

// FormatCloseItem 格式化班次结束时冻结的事项记录，并与最新状态对照。
func FormatCloseItem(snap CloseItemSnapshot, latest *Item) string {
	var b strings.Builder
	state := "结束时未关闭"
	if snap.Closed {
		state = fmt.Sprintf("结束时已关闭（%s 于 %s）", snap.CloseOperator, fmtTimePtr(snap.ClosedAt))
	}
	fmt.Fprintf(&b, "%s  严重程度=%s  %s\n", snap.ItemID, snap.Severity.Label(), state)
	fmt.Fprintf(&b, "  内容：%s\n", snap.Content)
	fmt.Fprintf(&b, "  限制条件：%s\n", dashIfEmpty(snap.Constraints))
	fmt.Fprintf(&b, "  结束时后续负责人：%s\n", snap.FollowOwner)
	if latest == nil {
		b.WriteString("  最新状态：事项记录已不存在\n")
		return b.String()
	}
	closeState := "未关闭"
	if latest.Closed {
		closeState = fmt.Sprintf("已关闭（%s 于 %s）", latest.CloseOperator, fmtTimePtr(latest.ClosedAt))
	}
	fmt.Fprintf(&b, "  最新状态：当前所在班次=%s  最新负责人=%s  最新关闭情况=%s\n",
		latest.CurrentShiftID, latest.FollowOwner, closeState)
	return b.String()
}

// FormatOverlapNote 格式化重叠说明。
func FormatOverlapNote(n OverlapNote) string {
	return fmt.Sprintf("%s  岗位=%s  班次 %s <-> %s  %s\n  说明：%s",
		n.ID, n.Position, n.ShiftA, n.ShiftB, fmtTime(n.CreatedAt), n.Note)
}

// FormatEntry 格式化交接单项当前结果与历次退回、补充说明。
func FormatEntry(e HandoverEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  [%s]  严重程度=%s  后续负责人=%s\n",
		e.ItemID, e.Status.Label(), e.Severity.Label(), e.FollowOwner)
	fmt.Fprintf(&b, "  原文：%s\n", e.Content)
	fmt.Fprintf(&b, "  限制条件：%s\n", dashIfEmpty(e.Constraints))
	if e.ProcessedAt == nil {
		b.WriteString("  最后处理：尚未处理（等待接班人处理）\n")
	} else {
		fmt.Fprintf(&b, "  最后处理：操作人=%s 时间=%s\n", dashIfEmpty(e.Operator), fmtTimePtr(e.ProcessedAt))
	}
	if e.Status == EntryTracking || e.TrackingNote != "" {
		fmt.Fprintf(&b, "  跟踪说明：%s\n", dashIfEmpty(e.TrackingNote))
		fmt.Fprintf(&b, "  跟踪后续负责人：%s\n", dashIfEmpty(e.FollowOwner))
	}
	for _, r := range e.Rounds {
		fmt.Fprintf(&b, "  第%d次退回：%s 操作人=%s 原因=%s\n",
			r.Seq, fmtTime(r.ReturnedAt), r.ReturnOperator, r.Reason)
		if r.Supplement != "" {
			fmt.Fprintf(&b, "    补充说明：%s 补充人=%s 补充时间=%s\n",
				r.Supplement, dashIfEmpty(r.SupplementOperator), fmtTimePtr(r.SupplementAt))
		}
		if r.ResubmittedAt != nil {
			fmt.Fprintf(&b, "    已重新提交：%s\n", fmtTime(*r.ResubmittedAt))
		}
	}
	return b.String()
}

// FormatHandover 格式化完整交接记录。
func FormatHandover(h Handover) string {
	var b strings.Builder
	state := "未完成"
	if h.Completed() {
		state = "已完成 " + fmtTimePtr(h.CompletedAt)
	}
	fmt.Fprintf(&b, "%s  岗位=%s  %s -> %s  发起于 %s  [%s]  共%d项\n",
		h.ID, h.Position, h.FromShiftID, h.ToShiftID, fmtTime(h.CreatedAt), state, len(h.Entries))
	if len(h.Entries) == 0 {
		b.WriteString("  （空清单，直接完成）\n")
	}
	for i := range h.Entries {
		b.WriteString(indentLines(FormatEntry(h.Entries[i]), "  "))
	}
	return b.String()
}

func indentLines(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix) + "\n"
}

// FormatReport 格式化按班次查询的完整报告。
func FormatReport(rep ShiftReport) string {
	var b strings.Builder
	b.WriteString("班次 " + FormatShift(rep.Shift) + "\n")

	b.WriteString("\n重叠说明：\n")
	if len(rep.OverlapNotes) == 0 {
		b.WriteString("  （无）\n")
	}
	for _, n := range rep.OverlapNotes {
		b.WriteString(indentLines(FormatOverlapNote(n), "  "))
	}

	if rep.ItemsAtClose {
		b.WriteString("\n事项（以下为结束时记录，冻结于该班结束成功时刻；后班的修改、关闭与交接不改变本记录）：\n")
		if len(rep.CloseItems) == 0 {
			b.WriteString("  （结束时没有事项）\n")
		}
		for _, s := range rep.CloseItems {
			var latest *Item
			if v, ok := rep.LatestItems[s.ItemID]; ok {
				latest = &v
			}
			b.WriteString(indentLines(FormatCloseItem(s, latest), "  "))
		}
	} else {
		if rep.HistoryIncomplete {
			b.WriteString("\n事项（历史记录不完整，该班结束时未留下记录，以下为当前信息，不能视为结束时事实）：\n")
		} else {
			b.WriteString("\n事项：\n")
		}
		if len(rep.Items) == 0 {
			b.WriteString("  （无）\n")
		}
		for _, it := range rep.Items {
			b.WriteString(indentLines(FormatItem(it), "  "))
		}
	}

	b.WriteString("\n交班对象：\n")
	if rep.Outgoing == nil {
		b.WriteString("  （尚未发起交接）\n")
	} else {
		b.WriteString(indentLines(FormatHandover(*rep.Outgoing), "  "))
	}

	b.WriteString("\n接班交接：\n")
	if len(rep.Incoming) == 0 {
		b.WriteString("  （无）\n")
	}
	for _, h := range rep.Incoming {
		b.WriteString(indentLines(FormatHandover(h), "  "))
	}

	b.WriteString("\n各项交接当前结果：\n")
	if len(rep.Results) == 0 {
		b.WriteString("  （无）\n")
	}
	ids := make([]string, 0, len(rep.Results))
	for id := range rep.Results {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		views := rep.Results[id]
		for _, v := range views {
			operator, processedAt := dashIfEmpty(v.Entry.Operator), fmtTimePtr(v.Entry.ProcessedAt)
			if v.Entry.ProcessedAt == nil {
				operator, processedAt = "尚未处理", "尚未处理"
			}
			fmt.Fprintf(&b, "  事项 %s 交接 %s（%s -> %s）当前结果：%s；处理人=%s；处理时间=%s\n",
				id, v.HandoverID, v.FromShift, v.ToShift,
				v.Entry.Status.Label(), operator, processedAt)
			for _, r := range v.Entry.Rounds {
				fmt.Fprintf(&b, "    第%d次退回：操作人=%s 时间=%s 原因=%s\n",
					r.Seq, r.ReturnOperator, fmtTime(r.ReturnedAt), r.Reason)
				if r.Supplement != "" {
					fmt.Fprintf(&b, "      补充：%s（补充人=%s，补充时间=%s）\n",
						r.Supplement, dashIfEmpty(r.SupplementOperator), fmtTimePtr(r.SupplementAt))
				}
				if r.ResubmittedAt != nil {
					fmt.Fprintf(&b, "      已重新提交：%s\n", fmtTime(*r.ResubmittedAt))
				}
			}
		}
	}
	return b.String()
}
