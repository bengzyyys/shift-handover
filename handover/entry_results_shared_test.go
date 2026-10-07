package handover

import (
	"strings"
	"testing"
)

// 本文件锁定“各次交接当前结果”整理规则在 item-show 与 shift-show 两个入口
// 共用同一份实现（entry_results.go）后必须继续成立的事实：选取范围不同但
// 同一交接得到的事实与排列次序一致，每条结果都是独立副本。
//
// 场景为 S001 -> S002 -> S003 -> S004 的链式交接：
//   - I001 一直未关闭，参与 H001、H002、H003 三次交接；H001、H002 已确认
//     接收，H003 尚未处理；
//   - I002 在 H001 确认后于 S002 关闭，只参与 H001，不进入后续交接。

// entryResultsChain 建立链式交接场景，返回各编号便于断言。
func entryResultsChain(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	c := mustShift(t, f, "调度", "赵六", tsDay(2, 23, 0), tsDay(3, 7, 0), "")
	mustShift(t, f, "调度", "钱七", tsDay(3, 7, 0), tsDay(3, 15, 0), "")

	it, err := f.svc.AddItem(a.ID, "一路流转的事项", SeverityImportant, "", "李四")
	if err != nil {
		t.Fatalf("add I001: %v", err)
	}
	other, err := f.svc.AddItem(a.ID, "接班后关闭的事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add I002: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	h1, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("H001: %v", err)
	}
	for _, id := range []string{it.ID, other.ID} {
		if _, err := f.svc.ProcessEntry(h1.ID, id, ActionConfirm, "李四", "", "", ""); err != nil {
			t.Fatalf("confirm %s in H001: %v", id, err)
		}
	}
	// I002 在接班班次关闭，不再随班交接；I001 继续流转。
	if _, err := f.svc.CloseItem(other.ID, "李四"); err != nil {
		t.Fatalf("close I002: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}

	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("H002: %v", err)
	}
	if len(h2.Entries) != 1 || h2.Entries[0].ItemID != it.ID {
		t.Fatalf("H002 应只含未关闭的 I001：%+v", h2.Entries)
	}
	if _, err := f.svc.ProcessEntry(h2.ID, it.ID, ActionConfirm, "赵六", "", "", ""); err != nil {
		t.Fatalf("confirm I001 in H002: %v", err)
	}
	if _, err := f.svc.CloseShift(c.ID); err != nil {
		t.Fatalf("close c: %v", err)
	}
	if _, err := f.svc.CreateHandover(c.ID, "S004"); err != nil {
		t.Fatalf("H003: %v", err)
	}
	return f
}

// resultIDs 取出按事项查询结果中的交接编号次序。
func resultIDs(rs []EntryView) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.HandoverID
	}
	return out
}

// TestItemJourneyListsEveryHandover：按事项查询列出该事项参与的全部交接，
// 即使它已转到后面的班次，也不能只留最近一条；次序按交接编号排列，
// 每条结果沿用对应交接保存的值，不被事项最新信息覆盖。
func TestItemJourneyListsEveryHandover(t *testing.T) {
	f := entryResultsChain(t)

	j, err := f.svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if !j.HasHandovers {
		t.Fatalf("I001 参与过三次交接，应识别有交接记录")
	}
	if got := resultIDs(j.Results); strings.Join(got, ",") != "H001,H002,H003" {
		t.Fatalf("按事项查询应列出全部交接且按编号排列，got %v", got)
	}
	// 班次关系随各次交接原样带出。
	wantRel := []string{"S001->S002", "S002->S003", "S003->S004"}
	for i, want := range wantRel {
		got := j.Results[i].FromShift + "->" + j.Results[i].ToShift
		if got != want {
			t.Fatalf("第%d条结果班次关系 want %s got %s", i, want, got)
		}
	}
	// 各次交接保存的结果分别保留：前两次已确认、最近一次仍待处理。
	if j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[1].Entry.Status != EntryConfirmed ||
		j.Results[2].Entry.Status != EntryPending {
		t.Fatalf("各次交接当前结果应分别保留：%+v", j.Results)
	}
	if j.Results[0].Entry.Operator != "李四" || j.Results[1].Entry.Operator != "赵六" {
		t.Fatalf("处理人应沿用对应交接保存的值：%+v", j.Results)
	}
	if j.Results[2].Entry.Operator != "" || j.Results[2].Entry.ProcessedAt != nil {
		t.Fatalf("待处理项本次处理人与时间应显示尚未处理：%+v", j.Results[2].Entry)
	}

	// 事项查询的交接结果文本也按交接编号排列。
	text := FormatItemJourney(j)
	i1 := strings.Index(text, "交接 H001（S001 -> S002）")
	i2 := strings.Index(text, "交接 H002（S002 -> S003）")
	i3 := strings.Index(text, "交接 H003（S003 -> S004）")
	if i1 < 0 || i2 < 0 || i3 < 0 || !(i1 < i2 && i2 < i3) {
		t.Fatalf("事项查询交接结果应按交接编号排列：%d %d %d\n%s", i1, i2, i3, text)
	}
}

// TestShiftReportOnlyDirectHandovers：按班次查询只汇总与所查班次有直接交班
// 或接班关系的交接；同一事项既交入又交出时两条结果分别保留，不混入该事项
// 在其他班次之间的交接。报告按事项编号排列，同一事项按交接编号排列。
func TestShiftReportOnlyDirectHandovers(t *testing.T) {
	f := entryResultsChain(t)

	// 中间班次 S002：I001 既交入（H001）又交出（H002），两条分别保留；
	// I002 只有交入一条；S003 -> S004 的 H003 与本班无关，不得混入。
	rb, err := f.svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	if got := resultIDs(rb.Results["I001"]); strings.Join(got, ",") != "H001,H002" {
		t.Fatalf("S002 对 I001 应分别保留交入与交出两条：%v", got)
	}
	if rb.Results["I001"][0].FromShift != "S001" || rb.Results["I001"][0].ToShift != "S002" ||
		rb.Results["I001"][1].FromShift != "S002" || rb.Results["I001"][1].ToShift != "S003" {
		t.Fatalf("两条结果的班次关系应分别指向交入与交出：%+v", rb.Results["I001"])
	}
	if got := resultIDs(rb.Results["I002"]); strings.Join(got, ",") != "H001" {
		t.Fatalf("I002 在 S002 只应有交入一条，不进入后续交接：%v", got)
	}

	// 首尾班次只看到与自己直接相关的那一条，其他班次之间的交接不混入。
	ra, err := f.svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report S001: %v", err)
	}
	if got := resultIDs(ra.Results["I001"]); strings.Join(got, ",") != "H001" {
		t.Fatalf("S001 只应汇总 H001：%v", got)
	}
	rc, err := f.svc.ShiftReport("S003")
	if err != nil {
		t.Fatalf("report S003: %v", err)
	}
	if got := resultIDs(rc.Results["I001"]); strings.Join(got, ",") != "H002,H003" {
		t.Fatalf("S003 应汇总 H002（交入）与 H003（交出）：%v", got)
	}
	rd, err := f.svc.ShiftReport("S004")
	if err != nil {
		t.Fatalf("report S004: %v", err)
	}
	if got := resultIDs(rd.Results["I001"]); strings.Join(got, ",") != "H003" {
		t.Fatalf("S004 只应汇总 H003：%v", got)
	}

	// 报告文本按事项编号排列，同一事项按交接编号排列。
	text := FormatReport(rb)
	l1 := strings.Index(text, "事项 I001 交接 H001")
	l2 := strings.Index(text, "事项 I001 交接 H002")
	l3 := strings.Index(text, "事项 I002 交接 H001")
	if l1 < 0 || l2 < 0 || l3 < 0 || !(l1 < l2 && l2 < l3) {
		t.Fatalf("报告应按事项编号、同事项按交接编号排列：%d %d %d\n%s", l1, l2, l3, text)
	}
	if strings.Contains(text, "H003") {
		t.Fatalf("S002 报告不得混入 S003 -> S004 的 H003：\n%s", text)
	}
}

// TestTwoEntriesReportSameFactsAsItemJourney：用 item-show 与 shift-show
// 核对同一次交接，得到的事实必须一致：结果、处理人、处理时间与退回/补充
// 记录是同样的值（各为独立副本），排列次序也一致。
func TestTwoEntriesReportSameFactsAsItemJourney(t *testing.T) {
	f := entryResultsChain(t)

	j, err := f.svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	rb, err := f.svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	rc, err := f.svc.ShiftReport("S003")
	if err != nil {
		t.Fatalf("report S003: %v", err)
	}

	// 三处对 H001/H002/H003 的单项事实逐一相同（JSON 全等），与取自哪个
	// 入口无关；待处理的 H003 也不例外。
	cross := []struct {
		fromJourney EntryView
		fromReport  EntryView
	}{
		{j.Results[0], rb.Results["I001"][0]}, // H001
		{j.Results[1], rb.Results["I001"][1]}, // H002（也是 rc 的交入）
		{j.Results[2], rc.Results["I001"][1]}, // H003
	}
	for i, x := range cross {
		if x.fromJourney.HandoverID != x.fromReport.HandoverID {
			t.Fatalf("第%d条交接编号不一致", i)
		}
		if got, want := mustJSON(t, x.fromReport.Entry), mustJSON(t, x.fromJourney.Entry); got != want {
			t.Fatalf("交接 %s 在两个入口的事实应一致\njourney=%s\nreport =%s",
				x.fromJourney.HandoverID, want, got)
		}
	}
	// rc 的交入条目与 rb 的交出条目（同为 H002）也必须一致。
	if got, want := mustJSON(t, rc.Results["I001"][0].Entry), mustJSON(t, rb.Results["I001"][1].Entry); got != want {
		t.Fatalf("H002 在交班、接班两个视角的事实应相同\nwant %s\ngot  %s", want, got)
	}
}

// TestEntryResultsAreIndependentCopies：两个入口的结果互不共享内存，也不与
// 存储及报告内嵌交接清单共享；为本地展示改动其中一份，不改变另一份与存储。
func TestEntryResultsAreIndependentCopies(t *testing.T) {
	f := entryResultsChain(t)
	forge := tsDay(9, 10, 11)

	j, err := f.svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	rb, err := f.svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	// 改事项查询结果中的 H001 处理时间，班次报告与存储不变。
	*j.Results[0].Entry.ProcessedAt = forge
	if rb.Results["I001"][0].Entry.ProcessedAt == nil ||
		rb.Results["I001"][0].Entry.ProcessedAt.Equal(forge) {
		t.Fatalf("改动事项查询结果不应连带改掉班次报告：%v", rb.Results["I001"][0].Entry.ProcessedAt)
	}
	stored, err := f.svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get H001: %v", err)
	}
	se, _ := findEntry(&stored, "I001")
	if se.ProcessedAt == nil || se.ProcessedAt.Equal(forge) {
		t.Fatalf("改动查询结果不应写入存储：%v", se.ProcessedAt)
	}

	// 改班次报告逐项结果，不连带改报告内嵌的交接清单。
	rb.Results["I001"][0].Entry.Status = EntryTracking
	ie, _ := findEntry(&rb.Incoming[0], "I001")
	if ie.Status != EntryConfirmed {
		t.Fatalf("改动逐项结果不应连带改掉报告内嵌交接清单：%s", ie.Status)
	}
}
