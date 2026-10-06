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
	writeItemHeader(&b, it)
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

// writeItemHeader 输出事项的最新内容、严重程度、限制条件、后续负责人、
// 当前所在班次、关闭情况和流经班次。
func writeItemHeader(b *strings.Builder, it Item) {
	state := "未关闭"
	if it.Closed {
		state = fmt.Sprintf("已关闭（%s 于 %s）", it.CloseOperator, fmtTimePtr(it.ClosedAt))
	}
	fmt.Fprintf(b, "%s  严重程度=%s  当前班次=%s  原始班次=%s  [%s]\n",
		it.ID, it.Severity.Label(), it.CurrentShiftID, it.OriginShiftID, state)
	fmt.Fprintf(b, "  内容：%s\n", it.Content)
	fmt.Fprintf(b, "  限制条件：%s\n", dashIfEmpty(it.Constraints))
	fmt.Fprintf(b, "  后续负责人：%s\n", it.FollowOwner)
	fmt.Fprintf(b, "  流经班次：%s\n", strings.Join(it.ShiftIDs, " -> "))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// whoIfRecorded 展示操作人，未记录时明确标为未记录，不以其他人代替。
func whoIfRecorded(s string) string {
	if s == "" {
		return "未记录"
	}
	return s
}

// fmtTimePtrIfRecorded 展示时间指针，缺失时明确标为未记录。
func fmtTimePtrIfRecorded(t *time.Time) string {
	if t == nil {
		return "未记录"
	}
	return fmtTimeIfRecorded(*t)
}

// fmtTimeIfRecorded 展示时间值，旧数据零值（0001-01-01T00:00:00Z）时
// 明确标为未记录，不显示公元元年日期。
func fmtTimeIfRecorded(t time.Time) string {
	if t.IsZero() {
		return "未记录"
	}
	return fmtTime(t)
}

// fmtProcessedAtIfRecorded 展示处理时间，缺失（nil）或旧数据零值
// （0001-01-01T00:00:00Z）时明确标为未记录，不推测补齐。
func fmtProcessedAtIfRecorded(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "未记录"
	}
	return fmtTime(*t)
}

// currentProcessing 返回交接单项当前处理结果的处理人与处理时间展示，是
// 交接清单（handover-show、shift-show 内嵌清单与末尾结果说明）和事项处理
// 经过查询（item-show）共用的同一条业务规则。
//
// 是否处理过以已保存的处理结果（状态）为准，与处理时间是否保存无关：确认
// 接收、继续跟踪与退回都属于已经发生的处理，缺少处理时间不能使它们变成待
// 处理；真正待处理（含退回后已补充重新提交、等待接班人再次处理）的事项，
// 处理人与处理时间都显示尚未处理，即使旧记录残留姓名或时间也不显示成已处理。
//
// 已处理时处理人与处理时间各自独立展示：人名有值展示原姓名、缺失明确标为
// 未记录；时间有真实值沿用带时区格式、缺失或为旧数据零值
// （0001-01-01T00:00:00Z）同样标为未记录。缺少其中一个不能隐藏另一个，
// 也不以班次负责人、交接发起时间或退回历史中的信息代替。结果为空或无法
// 识别的记录同样按各自已保存的姓名与时间展示，不据以推测接收结果。
func currentProcessing(e HandoverEntry) (operator, processedAt string) {
	if e.Status == EntryPending {
		return "尚未处理", "尚未处理"
	}
	return whoIfRecorded(e.Operator), fmtProcessedAtIfRecorded(e.ProcessedAt)
}

// returnRoundView 是一轮退回历史在展示层的统一解读。交接查询
// （handover-show，及 shift-show 报告内嵌的交接清单）与班次报告末尾
// “各项交接当前结果”两处共用同一份判断，只保留各自的措辞、缩进与
// 字段次序——统一维护的是共同规则，不是把两处改成同一种文本。
type returnRoundView struct {
	Seq            int
	ReturnedAt     string // 退回时间：真实值用带时区格式；缺失或零值显示未记录
	ReturnOperator string // 退回人：有值保留原姓名，缺失显示未记录
	Reason         string // 该轮保存的退回原因，原样展示

	HasSupplement      bool   // 该轮是否保存了非空补充说明；没有就不产生补充段落
	Supplement         string // 该轮保存的补充说明
	SupplementOperator string // 补充人：缺失显示未记录
	SupplementAt       string // 补充时间：缺失（nil）或零值显示未记录

	Resubmitted   bool   // 该轮是否保存了重新提交时间（旧数据零值也算已重新提交）
	ResubmittedAt string // 重新提交时间：零值显示未记录；未保存时不展示该行
}

// viewReturnRound 按共同规则解读一轮已保存的退回历史。它只做展示判断，
// 不补齐、不推测：
//   - 姓名有值保留原姓名、缺失显示未记录；时间缺失或为旧数据零值
//     （0001-01-01T00:00:00Z）显示未记录，真实时间沿用带时区格式，
//     姓名与时间各自独立判断，缺一个不隐藏另一个；
//   - 不用班次负责人、交接发起时间、当前处理人或其他轮次的资料补齐
//     本轮记录；
//   - 该轮没有非空补充说明时不产生补充段落；没有重新提交时间时不显示
//     已重新提交；保存了零值重新提交时间仍按已重新提交展示、时间显示
//     未记录，不悄悄改成尚未提交。
func viewReturnRound(r ReturnRound) returnRoundView {
	v := returnRoundView{
		Seq:            r.Seq,
		ReturnedAt:     fmtTimeIfRecorded(r.ReturnedAt),
		ReturnOperator: whoIfRecorded(r.ReturnOperator),
		Reason:         r.Reason,
		Resubmitted:    r.ResubmittedAt != nil,
		ResubmittedAt:  fmtTimePtrIfRecorded(r.ResubmittedAt),
	}
	if r.Supplement != "" {
		v.HasSupplement = true
		v.Supplement = r.Supplement
		v.SupplementOperator = whoIfRecorded(r.SupplementOperator)
		v.SupplementAt = fmtTimePtrIfRecorded(r.SupplementAt)
	}
	return v
}

// viewReturnRounds 按保存次序解读全部退回轮次，不增删、不重排、不补造：
// 连续发生多轮退回时每轮各用各的退回原因与补充说明，前一轮的原因和
// 补充不会被后一轮替换；没有退回轮次时返回空切片，调用方不补造轮次。
func viewReturnRounds(rounds []ReturnRound) []returnRoundView {
	vs := make([]returnRoundView, len(rounds))
	for i, r := range rounds {
		vs[i] = viewReturnRound(r)
	}
	return vs
}

// entryCurrentResultLabel 是交接当前结果在事项处理经过查询中的展示名。
// 缺失或空结果显示“处理结果未记录”，无法识别的结果显示“处理结果无法识别”
// 并带出保存的原值，与交接清单、班次报告表达同一事实，不推测成已接收。
func entryCurrentResultLabel(s EntryStatus) string {
	switch s {
	case EntryPending:
		return "待处理（接班人尚未处理）"
	case EntryReturned:
		return "退回（等待交班人补充）"
	case EntryConfirmed:
		return "确认接收"
	case EntryTracking:
		return "继续跟踪（已接收）"
	case "":
		return "处理结果未记录"
	}
	return "处理结果无法识别（原值：" + string(s) + "）"
}

// FormatJourneyEvent 格式化处理经过中的单条记录。交接相关记录带有交接
// 编号与交班、接班班次；缺少的人名或时间明确标为未记录，不推测补齐。
func FormatJourneyEvent(ev JourneyEvent) string {
	when := "未记录"
	if ev.TimeKnown {
		when = fmtTime(ev.At)
	}
	who := whoIfRecorded(ev.Operator)
	where := ""
	if ev.HandoverID != "" {
		where = fmt.Sprintf("交接 %s（%s -> %s）", ev.HandoverID, ev.FromShift, ev.ToShift)
	}
	switch ev.Kind {
	case "created":
		return when + " 事项建立"
	case "updated":
		return fmt.Sprintf("%s 修改：%s", when, ev.Detail)
	case "closed":
		return fmt.Sprintf("%s 关闭 操作人=%s", when, who)
	case "received":
		return fmt.Sprintf("%s 接收 操作人=%s %s", when, who, ev.Detail)
	case "handover-init":
		// 交接发起不记录操作人，始终显示未记录，不以班次负责人代替。
		return fmt.Sprintf("%s %s发起交接 操作人=%s", when, where, who)
	case "return":
		return fmt.Sprintf("%s %s第%d次退回 操作人=%s 原因=%s",
			when, where, ev.RoundSeq, who, ev.Reason)
	case "return-incomplete":
		// 当前结果是退回却没有退回轮次记录：退回确实发生过，但轮次序号、
		// 退回原因、补充与重新提交经过都没有可靠记录。处理人与时间各自使用
		// 当前保存的信息（缺失分别标为未记录），并明确提示退回历史不完整、
		// 原因未记录，不编造第几次退回，也不拿事项内容代替原因。
		return fmt.Sprintf("%s %s退回 操作人=%s（退回历史不完整：缺少退回轮次记录，退回原因未记录）",
			when, where, who)
	case "resubmit":
		return fmt.Sprintf("%s %s第%d次重新提交 补充说明=%s 补充人=%s 补充时间=%s",
			when, where, ev.RoundSeq, dashIfEmpty(ev.Supplement),
			whoIfRecorded(ev.SupplementOperator), fmtTimePtrIfRecorded(ev.SupplementAt))
	case "confirm":
		return fmt.Sprintf("%s %s确认接收 操作人=%s", when, where, who)
	case "track":
		return fmt.Sprintf("%s %s继续跟踪 操作人=%s 跟踪说明=%s 后续负责人=%s",
			when, where, who, dashIfEmpty(ev.TrackingNote), dashIfEmpty(ev.FollowOwner))
	}
	return when + " " + ev.Kind
}

// FormatItemJourney 格式化凭事项编号查询得到的完整处理经过：开头为事项
// 最新状态，随后是合并事项自身历史与各次交接经过的时间线，最后列出该事项
// 在每次交接中的当前处理结果；尚未参与交接的显示暂无交接记录。
func FormatItemJourney(j ItemJourney) string {
	var b strings.Builder
	writeItemHeader(&b, j.Item)

	b.WriteString("  处理经过：\n")
	if len(j.Events) == 0 {
		b.WriteString("    （暂无记录）\n")
	}
	for _, ev := range j.Events {
		b.WriteString("    - " + FormatJourneyEvent(ev) + "\n")
	}

	b.WriteString("  交接当前结果：\n")
	if !j.HasHandovers {
		b.WriteString("    暂无交接记录\n")
	}
	for _, r := range j.Results {
		// 处理人与处理时间沿用共用的当前处理展示规则（见 currentProcessing）。
		operator, processedAt := currentProcessing(r.Entry)
		fmt.Fprintf(&b, "    交接 %s（%s -> %s）当前结果：%s；处理人=%s；处理时间=%s\n",
			r.HandoverID, r.FromShift, r.ToShift,
			entryCurrentResultLabel(r.Entry.Status), operator, processedAt)
	}
	return b.String()
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
	// 处理人与处理时间沿用共用的当前处理展示规则（见 currentProcessing）；
	// 待处理项在此额外说明等待接班人处理。
	operator, processedAt := currentProcessing(e)
	if e.Status == EntryPending {
		fmt.Fprintf(&b, "  最后处理：尚未处理（等待接班人处理）；处理人=%s；处理时间=%s\n",
			operator, processedAt)
	} else {
		fmt.Fprintf(&b, "  最后处理：处理人=%s；处理时间=%s\n", operator, processedAt)
	}
	if e.Status == EntryTracking || e.TrackingNote != "" {
		fmt.Fprintf(&b, "  跟踪说明：%s\n", dashIfEmpty(e.TrackingNote))
		fmt.Fprintf(&b, "  跟踪后续负责人：%s\n", dashIfEmpty(e.FollowOwner))
	}
	// 逐轮退回、补充与重新提交沿用共用的退回历史解读规则
	// （见 viewReturnRound），保留本处原有措辞、缩进与字段次序。
	for _, r := range viewReturnRounds(e.Rounds) {
		fmt.Fprintf(&b, "  第%d次退回：%s 操作人=%s 原因=%s\n",
			r.Seq, r.ReturnedAt, r.ReturnOperator, r.Reason)
		if r.HasSupplement {
			fmt.Fprintf(&b, "    补充说明：%s 补充人=%s 补充时间=%s\n",
				r.Supplement, r.SupplementOperator, r.SupplementAt)
		}
		if r.Resubmitted {
			fmt.Fprintf(&b, "    已重新提交：%s\n", r.ResubmittedAt)
		}
	}
	return b.String()
}

// FormatHandover 格式化完整交接记录。
func FormatHandover(h Handover) string {
	var b strings.Builder
	state := "未完成"
	if h.Completed() {
		state = "已完成 " + fmtTimePtrIfRecorded(h.CompletedAt)
	}
	fmt.Fprintf(&b, "%s  岗位=%s  %s -> %s  发起于 %s  [%s]  共%d项\n",
		h.ID, h.Position, h.FromShiftID, h.ToShiftID,
		fmtTimeIfRecorded(h.CreatedAt), state, len(h.Entries))
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
			// 处理人与处理时间沿用共用的当前处理展示规则（见
			// currentProcessing），与内嵌交接清单表达同一事实。
			operator, processedAt := currentProcessing(v.Entry)
			fmt.Fprintf(&b, "  事项 %s 交接 %s（%s -> %s）当前结果：%s；处理人=%s；处理时间=%s\n",
				id, v.HandoverID, v.FromShift, v.ToShift,
				v.Entry.Status.Label(), operator, processedAt)
			// 退回历史与内嵌交接清单共用同一解读规则（见
			// viewReturnRound），这里保留结果区原有的措辞、缩进与字段次序。
			for _, r := range viewReturnRounds(v.Entry.Rounds) {
				fmt.Fprintf(&b, "    第%d次退回：操作人=%s 时间=%s 原因=%s\n",
					r.Seq, r.ReturnOperator, r.ReturnedAt, r.Reason)
				if r.HasSupplement {
					fmt.Fprintf(&b, "      补充：%s（补充人=%s，补充时间=%s）\n",
						r.Supplement, r.SupplementOperator, r.SupplementAt)
				}
				if r.Resubmitted {
					fmt.Fprintf(&b, "      已重新提交：%s\n", r.ResubmittedAt)
				}
			}
		}
	}
	return b.String()
}
