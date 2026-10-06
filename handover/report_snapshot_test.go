package handover

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// tamperReport 模拟调用方为展示需要对报告做的各种本地改动：替换事项
// 文字、调整清单顺序、修改保存的关闭/处理时间、改动交接结果与退回记录。
func tamperReport(rep *ShiftReport, fake time.Time) {
	rep.Shift.Owner = "篡改人"
	if rep.Shift.ClosedAt != nil {
		*rep.Shift.ClosedAt = fake
	}
	if rep.Shift.CloseRecord != nil && len(rep.Shift.CloseRecord.Items) > 0 {
		rep.Shift.CloseRecord.Items[0].Content = "篡改内容"
		rep.Shift.CloseRecord.Items[0].Closed = true
		rep.Shift.CloseRecord.Items[0].ClosedAt = &fake
	}
	for i := range rep.CloseItems {
		rep.CloseItems[i].Content = "篡改内容"
		rep.CloseItems[i].Closed = true
		rep.CloseItems[i].ClosedAt = &fake
	}
	// 调整清单顺序。
	for i, j := 0, len(rep.CloseItems)-1; i < j; i, j = i+1, j-1 {
		rep.CloseItems[i], rep.CloseItems[j] = rep.CloseItems[j], rep.CloseItems[i]
	}
	for id, it := range rep.LatestItems {
		it.Content = "篡改内容"
		it.FollowOwner = "篡改人"
		if it.ClosedAt != nil {
			*it.ClosedAt = fake
		}
		it.Events = append(it.Events, ItemEvent{At: fake, Kind: "closed", Operator: "篡改人"})
		rep.LatestItems[id] = it
	}
	for i := range rep.Items {
		rep.Items[i].Content = "篡改内容"
		rep.Items[i].Events = append(rep.Items[i].Events, ItemEvent{At: fake, Kind: "updated", Detail: "篡改"})
	}
	if rep.Outgoing != nil {
		rep.Outgoing.CompletedAt = &fake
		for i := range rep.Outgoing.Entries {
			e := &rep.Outgoing.Entries[i]
			e.Status = EntryConfirmed
			e.Content = "篡改内容"
			e.Operator = "篡改人"
			e.ProcessedAt = &fake
			e.Rounds = append(e.Rounds, ReturnRound{Seq: 99, ReturnedAt: fake, ReturnOperator: "篡改人", Reason: "篡改原因"})
		}
	}
	for i := range rep.Incoming {
		rep.Incoming[i].CompletedAt = &fake
		for j := range rep.Incoming[i].Entries {
			rep.Incoming[i].Entries[j].Status = EntryConfirmed
			rep.Incoming[i].Entries[j].ProcessedAt = &fake
		}
	}
	for _, views := range rep.Results {
		for i := range views {
			views[i].Entry.Status = EntryConfirmed
			views[i].Entry.Operator = "篡改人"
			views[i].Entry.ProcessedAt = &fake
			views[i].Entry.Rounds = append(views[i].Entry.Rounds, ReturnRound{Seq: 98, Reason: "篡改原因"})
		}
	}
}

// setupReportScenario 建立两班一交接的场景：A 班有未关闭事项 I1 与结束前
// 已关闭事项 I2，A 结束后向 B 班发起交接（I1 待处理）。返回班次与事项编号。
func setupReportScenario(t *testing.T, f *fixture) (aID, bID, i1ID, i2ID string) {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	i1, err := f.svc.AddItem(a.ID, "跟进1号机检修", SeverityImportant, "需停电窗口", "李四")
	if err != nil {
		t.Fatalf("add item1: %v", err)
	}
	i2, err := f.svc.AddItem(a.ID, "巡检记录归档", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add item2: %v", err)
	}
	if _, err := f.svc.CloseItem(i2.ID, "张三"); err != nil {
		t.Fatalf("close item2: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close shift a: %v", err)
	}
	b := mustShift(t, f, "调度", "李四", ts(16, 0), ts(23, 0), "")
	if _, err := f.svc.CreateHandover(a.ID, b.ID); err != nil {
		t.Fatalf("create handover: %v", err)
	}
	return a.ID, b.ID, i1.ID, i2.ID
}

// TestShiftReportIsSnapshotOfQueryTime：报告是查询当时的独立快照。调用方
// 对报告的任何本地改动（替换文字、调整顺序、修改关闭/处理时间、改动交接
// 结果与退回记录）都不影响系统记录，也不影响另一份已取得的报告；随后的
// 成功保存也不会把这些改动带进数据文件。
func TestShiftReportIsSnapshotOfQueryTime(t *testing.T) {
	f := newFixture(t)
	aID, bID, i1ID, i2ID := setupReportScenario(t, f)

	rep1, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	rep2, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("report again: %v", err)
	}
	if !rep1.ItemsAtClose || len(rep1.CloseItems) != 2 {
		t.Fatalf("已结束班次应有结束时记录两项：%+v", rep1.CloseItems)
	}
	origClosedAt := *rep1.Shift.ClosedAt
	origI2ClosedAt := *rep1.LatestItems[i2ID].ClosedAt

	// 篡改第一份报告。
	fake := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tamperReport(&rep1, fake)

	// 第二份报告不受第一份改动影响。
	if rep2.Shift.Owner != "张三" || !rep2.Shift.ClosedAt.Equal(origClosedAt) {
		t.Fatalf("另一份报告的班次信息不应被改写：%+v", rep2.Shift)
	}
	for _, s := range rep2.CloseItems {
		if s.Content == "篡改内容" {
			t.Fatalf("另一份报告的结束时记录不应被改写：%+v", rep2.CloseItems)
		}
	}
	if rep2.CloseItems[0].ItemID != i1ID || rep2.CloseItems[1].ItemID != i2ID {
		t.Fatalf("另一份报告的清单顺序不应被改写：%+v", rep2.CloseItems)
	}
	if rep2.Outgoing == nil || rep2.Outgoing.CompletedAt != nil ||
		rep2.Outgoing.Entries[0].Status != EntryPending || rep2.Outgoing.Entries[0].ProcessedAt != nil {
		t.Fatalf("另一份报告的交接结果不应被改写：%+v", rep2.Outgoing)
	}

	// 重新查询仍看到系统实际保存的内容。
	rep3, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("re-report: %v", err)
	}
	if rep3.Shift.Owner != "张三" || !rep3.Shift.ClosedAt.Equal(origClosedAt) {
		t.Fatalf("重新查询的班次信息应是系统保存的原值：%+v", rep3.Shift)
	}
	byID := map[string]CloseItemSnapshot{}
	for _, s := range rep3.CloseItems {
		byID[s.ItemID] = s
	}
	if byID[i1ID].Content != "跟进1号机检修" || byID[i1ID].Closed || byID[i1ID].ClosedAt != nil {
		t.Fatalf("结束时记录应保持冻结事实：%+v", byID[i1ID])
	}
	if byID[i2ID].Content != "巡检记录归档" || !byID[i2ID].Closed || !byID[i2ID].ClosedAt.Equal(origI2ClosedAt) {
		t.Fatalf("已关闭事项的结束时记录应保持原样：%+v", byID[i2ID])
	}
	if rep3.LatestItems[i1ID].Content != "跟进1号机检修" || rep3.LatestItems[i2ID].CloseOperator != "张三" {
		t.Fatalf("最新状态对照应是系统保存的内容：%+v", rep3.LatestItems)
	}
	if len(rep3.LatestItems[i1ID].Events) != 1 {
		t.Fatalf("事项历史不应混入篡改事件：%+v", rep3.LatestItems[i1ID].Events)
	}
	if rep3.Outgoing == nil || rep3.Outgoing.CompletedAt != nil {
		t.Fatalf("交接不应被本地改动标成完成：%+v", rep3.Outgoing)
	}
	e := rep3.Outgoing.Entries[0]
	if e.Status != EntryPending || e.Operator != "" || e.ProcessedAt != nil || len(e.Rounds) != 0 {
		t.Fatalf("交接事项应仍待处理、无处理人/处理时间/退回记录：%+v", e)
	}
	if v := rep3.Results[i1ID]; len(v) != 1 || v[0].Entry.Status != EntryPending || len(v[0].Entry.Rounds) != 0 {
		t.Fatalf("各项交接当前结果应仍待处理：%+v", v)
	}

	// 随后通过现有功能成功保存其他业务操作，先前的本地改动不能混入数据文件。
	if _, err := f.svc.AddItem(bID, "新班次事项", SeverityNormal, "", "赵六"); err != nil {
		t.Fatalf("add item to b: %v", err)
	}
	f.reopen(t)
	rep4, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("report after reopen: %v", err)
	}
	if rep4.Shift.Owner != "张三" || rep4.Outgoing.Entries[0].Status != EntryPending {
		t.Fatalf("落盘数据不应包含报告里的本地改动：%+v", rep4.Shift)
	}
	for _, s := range rep4.CloseItems {
		if strings.Contains(s.Content, "篡改") {
			t.Fatalf("数据文件不应混入篡改内容：%+v", s)
		}
	}
}

// TestShiftReportNotRefreshedByLaterBusiness：系统后续的正常业务处理不改写
// 调用方已经拿到的报告：接班人查询时事项仍待处理，确认接收后旧报告仍保留
// 待处理结果；后班修改、关闭事项后，旧报告的最新状态对照保持原样；交班
// 班次的结束时记录始终保留结束当时的事实。重新查询才看到新状态。
func TestShiftReportNotRefreshedByLaterBusiness(t *testing.T) {
	f := newFixture(t)
	aID, bID, i1ID, _ := setupReportScenario(t, f)

	// 接班人查询：事项仍待处理。
	before, err := f.svc.ShiftReport(bID)
	if err != nil {
		t.Fatalf("report b: %v", err)
	}
	if len(before.Incoming) != 1 || before.Incoming[0].Entries[0].Status != EntryPending {
		t.Fatalf("查询时应仍待处理：%+v", before.Incoming)
	}
	aBefore, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	latestBefore := aBefore.LatestItems[i1ID]

	// 成功确认接收。
	h, err := f.svc.CreateHandover(aID, bID)
	if !errors.Is(err, ErrHandoverExists) {
		t.Fatalf("重复发起应返回已有交接，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, i1ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// 旧报告仍保留查询时的待处理结果。
	if before.Incoming[0].Entries[0].Status != EntryPending ||
		before.Incoming[0].Entries[0].Operator != "" ||
		before.Incoming[0].Entries[0].ProcessedAt != nil ||
		before.Incoming[0].CompletedAt != nil {
		t.Fatalf("旧报告不应随后续处理改变：%+v", before.Incoming[0].Entries[0])
	}
	if before.Results[i1ID][0].Entry.Status != EntryPending {
		t.Fatalf("旧报告的各项结果不应随后续处理改变：%+v", before.Results[i1ID])
	}

	// 重新查询显示已接收及实际处理人和时间。
	after, err := f.svc.ShiftReport(bID)
	if err != nil {
		t.Fatalf("re-report b: %v", err)
	}
	e := after.Incoming[0].Entries[0]
	if e.Status != EntryConfirmed || e.Operator != "李四" || e.ProcessedAt == nil {
		t.Fatalf("新报告应显示已接收及处理人、时间：%+v", e)
	}
	if after.Incoming[0].CompletedAt == nil || !after.Incoming[0].CompletedAt.Equal(*e.ProcessedAt) {
		t.Fatalf("完成时间应为本次成功处理时间：%+v", after.Incoming[0].CompletedAt)
	}

	// 事项在后班修改并关闭：旧报告中的最新状态对照保持原样。
	if _, err := f.svc.UpdateItem(i1ID, "跟进1号机检修（已停电）", SeverityUrgent, "", "王五"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem(i1ID, "李四"); err != nil {
		t.Fatalf("close item: %v", err)
	}
	got := aBefore.LatestItems[i1ID]
	if got.Content != latestBefore.Content || got.FollowOwner != latestBefore.FollowOwner ||
		got.Closed || got.ClosedAt != nil || got.CurrentShiftID != latestBefore.CurrentShiftID {
		t.Fatalf("旧报告的最新状态对照不应随后班处理改变：%+v", got)
	}

	// 新报告展示更新后的状态；交班班次的结束时记录仍是结束当时的事实。
	aAfter, err := f.svc.ShiftReport(aID)
	if err != nil {
		t.Fatalf("re-report a: %v", err)
	}
	latest := aAfter.LatestItems[i1ID]
	if latest.Content != "跟进1号机检修（已停电）" || !latest.Closed || latest.CloseOperator != "李四" {
		t.Fatalf("新报告应展示后班更新后的状态：%+v", latest)
	}
	for _, s := range aAfter.CloseItems {
		if s.ItemID == i1ID && (s.Content != "跟进1号机检修" || s.Closed) {
			t.Fatalf("结束时记录应保留结束当时的事实：%+v", s)
		}
	}
}

// TestShiftReportEmptyVsMissingCloseRecord：结束时没有事项的空记录与旧数据
// 根本没有结束时记录保持区别；查询不存在的班次编号返回明确错误。
func TestShiftReportEmptyVsMissingCloseRecord(t *testing.T) {
	f := newFixture(t)

	// 空记录：班次结束时没有事项。
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close empty shift: %v", err)
	}
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("空结束时记录应与缺失记录区分：%+v", rep)
	}

	// 旧数据：已结束但根本没有结束时记录。
	f.store.data.Shifts = append(f.store.data.Shifts, Shift{
		ID: "S900", Position: "调度", Owner: "旧人",
		Start: ts(0, 0), End: ts(8, 0), Closed: true,
	})
	legacy, err := f.svc.ShiftReport("S900")
	if err != nil {
		t.Fatalf("report legacy: %v", err)
	}
	if !legacy.HistoryIncomplete || legacy.ItemsAtClose {
		t.Fatalf("缺少结束时记录的旧数据应提示历史记录不完整：%+v", legacy)
	}

	if _, err := f.svc.ShiftReport("S999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的班次编号应返回明确错误，got %v", err)
	}
}
