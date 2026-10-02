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

// FormatCloseRecord 格式化班次结束成功时为单个事项留下的不可变记录。
// 其中的关闭情况只反映结束那一刻：结束后由后续班次关闭的事项仍显示“结束时未关闭”。
func FormatCloseRecord(r ShiftItemRecord) string {
	var b strings.Builder
	if r.Closed {
		fmt.Fprintf(&b, "%s  严重程度=%s  原始班次=%s  [结束时已关闭（%s 于 %s）]\n",
			r.ID, r.Severity.Label(), r.OriginShiftID,
			dashIfEmpty(r.CloseOperator), fmtTimePtr(r.ClosedAt))
	} else {
		b.WriteString(r.ID + "  严重程度=" + r.Severity.Label() +
			"  原始班次=" + r.OriginShiftID + "  [结束时未关闭]\n")
	}
	fmt.Fprintf(&b, "  内容：%s\n", r.Content)
	fmt.Fprintf(&b, "  限制条件：%s\n", dashIfEmpty(r.Constraints))
	fmt.Fprintf(&b, "  后续负责人：%s\n", r.FollowOwner)
	return b.String()
}

// FormatItemLatest 格式化事项的最新信息，用于已结束班次报告中与结束时记录并列对照。
func FormatItemLatest(it Item) string {
	state := "未关闭"
	if it.Closed {
		state = fmt.Sprintf("已关闭（%s 于 %s）", dashIfEmpty(it.CloseOperator), fmtTimePtr(it.ClosedAt))
	}
	return fmt.Sprintf("%s  当前班次=%s  最新后续负责人=%s  最新关闭情况：%s  内容：%s",
		it.ID, it.CurrentShiftID, it.FollowOwner, state, it.Content)
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
	fmt.Fprintf(&b, "  最后处理：操作人=%s 时间=%s\n", dashIfEmpty(e.Operator), fmtTimePtr(e.ProcessedAt))
	if e.Status == EntryTracking || e.TrackingNote != "" {
		fmt.Fprintf(&b, "  跟踪说明：%s\n", dashIfEmpty(e.TrackingNote))
		fmt.Fprintf(&b, "  跟踪后续负责人：%s\n", dashIfEmpty(e.FollowOwner))
	}
	for _, r := range e.Rounds {
		fmt.Fprintf(&b, "  第%d次退回：%s 操作人=%s 原因=%s\n",
			r.Seq, fmtTime(r.ReturnedAt), r.ReturnOperator, r.Reason)
		if r.Supplement != "" {
			fmt.Fprintf(&b, "    补充说明：%s 操作人=%s 时间=%s\n",
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

	switch {
	case rep.Shift.Closed && rep.Shift.CloseSnapshot != nil:
		fmt.Fprintf(&b, "\n事项（结束时记录，记录于 %s）：\n", fmtTime(rep.Shift.CloseSnapshot.ClosedAt))
		if len(rep.CloseItems) == 0 {
			b.WriteString("  （本班结束成功时没有事项）\n")
		}
		for _, r := range rep.CloseItems {
			b.WriteString(indentLines(FormatCloseRecord(r), "  "))
		}
		// 最新信息随后续办理变化，与上面的结束时事实明确区分。
		b.WriteString("\n最新信息（非结束时记录，随后续办理变化）：\n")
		if len(rep.LatestItems) == 0 {
			b.WriteString("  （无）\n")
		}
		for _, it := range rep.LatestItems {
			b.WriteString(indentLines(FormatItemLatest(it), "  "))
		}
	case rep.Shift.Closed:
		b.WriteString("\n事项：\n")
		b.WriteString("  历史记录不完整，以下为当前信息（非结束时记录，不能视为本班结束时事实）：\n")
		if len(rep.LegacyItems) == 0 {
			b.WriteString("    （无）\n")
		}
		for _, it := range rep.LegacyItems {
			b.WriteString(indentLines(FormatItem(it), "    "))
		}
	default:
		b.WriteString("\n事项（进行中，当前信息）：\n")
		if len(rep.OpenItems) == 0 {
			b.WriteString("  （无）\n")
		}
		for _, it := range rep.OpenItems {
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
			fmt.Fprintf(&b, "  事项 %s 交接 %s（%s -> %s）当前结果：%s；处理人=%s；处理时间=%s\n",
				id, v.HandoverID, v.FromShift, v.ToShift,
				v.Entry.Status.Label(), dashIfEmpty(v.Entry.Operator), fmtTimePtr(v.Entry.ProcessedAt))
			for _, r := range v.Entry.Rounds {
				fmt.Fprintf(&b, "    第%d次退回：操作人=%s 时间=%s 原因=%s\n",
					r.Seq, r.ReturnOperator, fmtTime(r.ReturnedAt), r.Reason)
				if r.Supplement != "" {
					fmt.Fprintf(&b, "      补充：%s（%s，%s）\n",
						r.Supplement, dashIfEmpty(r.SupplementOperator), fmtTimePtr(r.SupplementAt))
				}
			}
		}
	}
	return b.String()
}
