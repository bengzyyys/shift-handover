package handover

import (
	"errors"
	"testing"
	"time"
)

// 本文件的测试针对“班次成功结束后，结束时记录必须保留这次成功结束时的事实”：
// 交给调用方的班次结果与系统保存的记录相互独立，调用方为本地展示改写自己保留
// 的结果（实际结束时刻、事项内容、清单项、某项关闭时间等）只能影响手中的结果；
// 结束前关闭事项所得结果中的关闭时间也不能再串进结束时记录。系统随后的正常
// 保存不得把调用方的本地修改带进已结束班次的历史，后班的处理既不改写调用方
// 保留的结束结果，也不改写原班次的结束时事实。

// prepareCloseResultScenario 建立一个含未关闭事项与结束前已关闭事项的班次，
// 在班次结束前保留已关闭事项的关闭结果，并成功结束班次、保留结束结果。
// 返回结束结果、已关闭事项的关闭结果与固定的关闭时刻。
func prepareCloseResultScenario(t *testing.T, f *fixture) (Shift, Shift, Item, Item, time.Time) {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	open, err := f.svc.AddItem(a.ID, "压力异常待观察", SeverityImportant, "不得超压", "李四")
	if err != nil {
		t.Fatalf("add open item: %v", err)
	}
	closed, err := f.svc.AddItem(a.ID, "巡检记录归档", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add closed item: %v", err)
	}
	itemClosedAt := tsDay(2, 14, 0)
	f.svc.nowAt(func() time.Time { return itemClosedAt })
	closedResult, err := f.svc.CloseItem(closed.ID, "张三")
	if err != nil {
		t.Fatalf("close item before shift close: %v", err)
	}
	if closedResult.ClosedAt == nil || !closedResult.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("前置：关闭结果应记录关闭时刻 %s，got %+v", itemClosedAt, closedResult.ClosedAt)
	}
	shiftClosedAt := tsDay(2, 16, 0)
	f.svc.nowAt(func() time.Time { return shiftClosedAt })
	res, err := f.svc.CloseShift(a.ID)
	if err != nil {
		t.Fatalf("close shift: %v", err)
	}
	if !res.Closed || res.ClosedAt == nil || !res.ClosedAt.Equal(shiftClosedAt) || res.CloseRecord == nil {
		t.Fatalf("前置：结束结果应记录成功结束时刻与结束时记录：%+v", res)
	}
	return a, res, open, closedResult, itemClosedAt
}

// TestCloseShiftResultIsIndependentOfStorage：调用方改写成功结束所得结果中的
// 实际结束时刻、事项内容/严重程度/限制条件/后续负责人、删除清单中的一项、
// 已关闭事项的关闭情况与关闭时间，都只能改动手上的结果：再次查询原班次仍读到
// 成功结束时保存的事实，先取得的班次报告也不被连带改写；随后的正常保存同样
// 不把这些本地修改落进存储。
func TestCloseShiftResultIsIndependentOfStorage(t *testing.T) {
	f := newFixture(t)
	a, res, open, closedResult, itemClosedAt := prepareCloseResultScenario(t, f)
	shiftClosedAt := *res.ClosedAt
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 结束后、动手改写前先取得一份班次报告与系统班次快照，作为“另一份已取得
	// 结果”与存储原值的凭据。
	reportBefore, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report before: %v", err)
	}
	reportSnapshot := mustJSON(t, reportBefore)
	storedSnapshot := mustJSON(t, func() Shift {
		sh, _ := f.svc.GetShift(a.ID)
		return sh
	}())
	closeResultSnapshot := mustJSON(t, res)

	// 改写调用方保留的结束结果：实际结束时刻。
	*res.ClosedAt = forge
	res.Owner = "篡改负责人"
	if len(res.CloseRecord.Items) != 2 {
		t.Fatalf("前置：结束时记录应含2项，got %+v", res.CloseRecord.Items)
	}
	// 改写未关闭事项的内容、严重程度、限制条件、后续负责人。
	s0 := &res.CloseRecord.Items[0]
	if s0.ItemID != open.ID {
		t.Fatalf("前置：清单应按编号排列，首项为 %s，got %s", open.ID, s0.ItemID)
	}
	s0.Content = "篡改后的内容"
	s0.Severity = SeverityUrgent
	s0.Constraints = "篡改限制"
	s0.FollowOwner = "篡改负责人"
	// 改写结束前已关闭事项的关闭情况、关闭人与关闭时间。
	s1 := &res.CloseRecord.Items[1]
	s1.Closed = false
	s1.CloseOperator = "篡改人"
	*s1.ClosedAt = forge
	// 从本地清单中移除一项。
	res.CloseRecord.Items = res.CloseRecord.Items[:1]

	// 改写结束前关闭事项所得结果中的关闭时刻：这份结果同样不能影响结束时记录。
	closedResultMut := closedResult
	*closedResultMut.ClosedAt = forge
	closedResultMut.CloseOperator = "篡改人"
	closedResultMut.Closed = false

	// 再次查询原班次：结束时事项仍完整保留原内容、严重程度、限制条件、后续负责人
	// 与关闭情况，清单顺序和编号不变。
	stored, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after mutation: %v", err)
	}
	if got := mustJSON(t, stored); got != storedSnapshot {
		t.Fatalf("改写结束结果不得影响系统保存的班次记录\nwant %s\ngot  %s", storedSnapshot, got)
	}
	if !stored.Closed || stored.ClosedAt == nil || !stored.ClosedAt.Equal(shiftClosedAt) {
		t.Fatalf("实际结束时刻应保留成功结束时的值：%+v", stored.ClosedAt)
	}
	if stored.Owner != "张三" || len(stored.CloseRecord.Items) != 2 {
		t.Fatalf("负责人与清单项不应被结束结果改写：%+v", stored)
	}
	got0 := stored.CloseRecord.Items[0]
	if got0.ItemID != open.ID || got0.Content != "压力异常待观察" || got0.Severity != SeverityImportant ||
		got0.Constraints != "不得超压" || got0.FollowOwner != "李四" || got0.Closed ||
		got0.ClosedAt != nil || got0.CloseOperator != "" {
		t.Fatalf("未关闭事项应保留结束时原样：%+v", got0)
	}
	got1 := stored.CloseRecord.Items[1]
	if !got1.Closed || got1.CloseOperator != "张三" ||
		got1.ClosedAt == nil || !got1.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("结束前已关闭事项的关闭人与关闭时间应保留当时的值：%+v", got1)
	}

	// 班次报告仍是结束时事实，且与改写前先取得的那份报告一致。
	reportAfter, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report after mutation: %v", err)
	}
	if got := mustJSON(t, reportAfter); got != reportSnapshot {
		t.Fatalf("改写结束结果不得影响班次报告\nwant %s\ngot  %s", reportSnapshot, got)
	}
	if !reportAfter.ItemsAtClose || reportAfter.HistoryIncomplete || len(reportAfter.CloseItems) != 2 {
		t.Fatalf("报告应仍展示完整的结束时记录：%+v", reportAfter)
	}
	if cs := findCloseItem(reportAfter, closedResult.ID); cs == nil || !cs.Closed ||
		cs.CloseOperator != "张三" || cs.ClosedAt == nil || !cs.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("报告中已关闭事项的关闭时间不应被关闭结果改写：%+v", cs)
	}

	// 调用方手上的结束结果确实已是改动后的样子（独立副本允许本地展示修改）。
	if !res.ClosedAt.Equal(forge) || len(res.CloseRecord.Items) != 1 {
		t.Fatalf("调用方保留的结果应保留本地修改：%+v", res)
	}

	// 随后进行一次完全正常的保存（新建不重叠班次）：原班次历史不得带入调用方
	// 对本地结果所做的修改。
	mustShift(t, f, "调度", "赵六", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	storedAgain, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after later save: %v", err)
	}
	if got := mustJSON(t, storedAgain); got != closeResultSnapshot {
		t.Fatalf("后续正常保存不得把本地修改带进原班次历史\nwant %s\ngot  %s", closeResultSnapshot, got)
	}
}

// TestCloseResultAndPreCloseResultDoNotShareTime：结束结果中的关闭时间、结束前
// 关闭事项操作结果中的关闭时间与系统保存的记录三者互不共享指针；改任一处的
// 关闭时刻，另外两处保留当时的值。
func TestCloseResultAndPreCloseResultDoNotShareTime(t *testing.T) {
	f := newFixture(t)
	a, res, _, closedResult, itemClosedAt := prepareCloseResultScenario(t, f)
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 改结束结果里已关闭事项的关闭时间，关闭事项结果与存储都不变。
	*res.CloseRecord.Items[1].ClosedAt = forge
	if closedResult.ClosedAt == nil || !closedResult.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("改写结束结果不应连带改掉关闭事项结果中的关闭时刻：%+v", closedResult.ClosedAt)
	}
	stored, err := f.svc.GetItem(closedResult.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if stored.ClosedAt == nil || !stored.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("改写结束结果不应改写事项保存的关闭时刻：%+v", stored.ClosedAt)
	}

	// 反向：再取一份结束结果并改其关闭时间，先前那份结束结果与存储不变。
	res2, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	*res2.CloseRecord.Items[1].ClosedAt = forge
	if !res.CloseRecord.Items[1].ClosedAt.Equal(forge) {
		t.Fatalf("本结果内的修改应保留：%v", res.CloseRecord.Items[1].ClosedAt)
	}
	fresh, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	cs := findCloseItem(fresh, closedResult.ID)
	if cs == nil || cs.ClosedAt == nil || !cs.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("改写另一份结果不应影响系统保存的结束时记录：%+v", cs)
	}
}

// TestLaterShiftProcessingKeepsCloseResultAndHistory：结束时未关闭的事项后来由
// 下一班接收、修改并关闭，原班次的结束时记录仍显示当时未关闭，最新状态对照
// 显示正常业务操作后的现状；调用方保留的结束结果不随后班处理变化，调用方对
// 本地结果的修改也不会在后续保存时进入原班次历史。
func TestLaterShiftProcessingKeepsCloseResultAndHistory(t *testing.T) {
	f := newFixture(t)
	a, closeResult, open, _, itemClosedAt := prepareCloseResultScenario(t, f)
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 下一班完成交接、接收未关闭事项，随后修改并关闭它，再结束下一班。
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	if len(h.Entries) != 1 || h.Entries[0].ItemID != open.ID {
		t.Fatalf("交接清单应只含未关闭事项：%+v", h.Entries)
	}
	if _, err := f.svc.ProcessEntry(h.ID, open.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.UpdateItem(open.ID, "接班后修订内容", SeverityUrgent, "新限制", "钱七"); err != nil {
		t.Fatalf("update in next shift: %v", err)
	}
	laterClosedAt := tsDay(2, 20, 0)
	f.svc.nowAt(func() time.Time { return laterClosedAt })
	if _, err := f.svc.CloseItem(open.ID, "李四"); err != nil {
		t.Fatalf("close in next shift: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close next shift: %v", err)
	}

	// 调用方保留的结束结果不随后班处理变化：未关闭事项当时仍未关闭、内容照旧。
	s0 := closeResult.CloseRecord.Items[0]
	if s0.ItemID != open.ID || s0.Content != "压力异常待观察" || s0.Severity != SeverityImportant ||
		s0.Constraints != "不得超压" || s0.FollowOwner != "李四" || s0.Closed ||
		s0.ClosedAt != nil || s0.CloseOperator != "" {
		t.Fatalf("保留的结束结果应仍是当时未关闭的事实：%+v", s0)
	}
	if s1 := closeResult.CloseRecord.Items[1]; !s1.Closed ||
		s1.ClosedAt == nil || !s1.ClosedAt.Equal(itemClosedAt) {
		t.Fatalf("保留的结束结果中已关闭事项应保持当时的关闭时间：%+v", s1)
	}

	// 原班次报告：结束时记录显示该事项当时未关闭；最新状态对照显示后班关闭的现状。
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report original shift: %v", err)
	}
	cs := findCloseItem(rep, open.ID)
	if cs == nil || cs.Closed || cs.Content != "压力异常待观察" || cs.FollowOwner != "李四" {
		t.Fatalf("原班次结束时记录应仍显示当时未关闭：%+v", cs)
	}
	latest := rep.LatestItems[open.ID]
	if !latest.Closed || latest.CurrentShiftID != b.ID || latest.FollowOwner != "钱七" ||
		latest.ClosedAt == nil || !latest.ClosedAt.Equal(laterClosedAt) {
		t.Fatalf("最新状态对照应显示后班处理后的现状：%+v", latest)
	}

	// 调用方改写手上的结束结果，再触发一次正常保存：原班次历史不得被带入修改。
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	wantSnapshot := mustJSON(t, closeResult)
	*closeResult.ClosedAt = forge
	closeResult.CloseRecord.Items[0].Content = "篡改内容"
	*closeResult.CloseRecord.Items[1].ClosedAt = forge
	mustShift(t, f, "巡检", "孙八", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	stored, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after later save: %v", err)
	}
	if got := mustJSON(t, stored); got != wantSnapshot {
		t.Fatalf("后续正常保存不得把本地修改带进原班次历史\nwant %s\ngot  %s", wantSnapshot, got)
	}
}

// TestEmptyShiftCloseResultLeavesExplicitEmptyRecord：没有事项的班次结束后仍留下
// 明确的空记录（非 nil、空清单），与旧数据缺少结束时记录相区别；调用方向手上
// 的空结果塞事项或改结束时刻不影响系统保存的空记录。
func TestEmptyShiftCloseResultLeavesExplicitEmptyRecord(t *testing.T) {
	f := newFixture(t)
	e := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	closedAt := tsDay(2, 16, 0)
	f.svc.nowAt(func() time.Time { return closedAt })
	res, err := f.svc.CloseShift(e.ID)
	if err != nil {
		t.Fatalf("close empty shift: %v", err)
	}
	if !res.Closed || res.CloseRecord == nil || len(res.CloseRecord.Items) != 0 {
		t.Fatalf("空清单结束应留下明确的空记录：%+v", res)
	}
	snapshot := mustJSON(t, res)

	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	*res.ClosedAt = forge
	res.CloseRecord.Items = append(res.CloseRecord.Items, CloseItemSnapshot{ItemID: "I999", Content: "篡改事项"})

	stored, err := f.svc.GetShift(e.ID)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if got := mustJSON(t, stored); got != snapshot {
		t.Fatalf("改写空班次的结束结果不应影响系统保存的空记录\nwant %s\ngot  %s", snapshot, got)
	}
	if stored.CloseRecord == nil || len(stored.CloseRecord.Items) != 0 ||
		stored.ClosedAt == nil || !stored.ClosedAt.Equal(closedAt) {
		t.Fatalf("系统中应仍是明确的空记录与原结束时刻：%+v", stored)
	}
	rep, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("空记录结束后应标为结束时记录且清单为空，不是历史不完整：%+v", rep)
	}
}

// TestCloseShiftFailureLeavesNoRecord：接班交接尚未接收齐全时结束班次属于校验
// 失败，必须返回零值结果与明确错误，不留下结束时记录、不写结束时刻，系统中的
// 班次仍保持进行中；交接补齐后重试成功，以那次成功时为准留下记录。
func TestCloseShiftFailureLeavesNoRecord(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	it, err := f.svc.AddItem(a.ID, "遗留事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	failAt := tsDay(2, 16, 0)
	f.svc.nowAt(func() time.Time { return failAt })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("交班班次本身没有接班交接，应能结束：%v", err)
	}
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	own, err := f.svc.AddItem(b.ID, "本班新增事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add own item: %v", err)
	}

	// 接班交接仍有待处理项：结束接班班次必须失败。
	f.svc.nowAt(func() time.Time { return tsDay(2, 22, 0) })
	res, err := f.svc.CloseShift(b.ID)
	if !errors.Is(err, ErrHandoverState) {
		t.Fatalf("接班交接未完成时结束应报 ErrHandoverState，got %v", err)
	}
	if res.ID != "" || res.Closed || res.ClosedAt != nil || res.CloseRecord != nil {
		t.Fatalf("失败时应返回零值结果，不能留下结束时记录：%+v", res)
	}
	stored, err := f.svc.GetShift(b.ID)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if stored.Closed || stored.ClosedAt != nil || stored.CloseRecord != nil {
		t.Fatalf("校验失败后班次应仍在进行中、无结束时记录：%+v", stored)
	}
	rep, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("失败后报告不应出现结束时记录：%+v", rep)
	}
	if len(rep.Items) != 1 || rep.Items[0].ID != own.ID {
		t.Fatalf("进行中班次应仍展示当前事项：%+v", rep.Items)
	}

	// 补齐交接后重试成功，以成功时刻留下完整记录：本班新增事项与已接收事项都在。
	if _, err := f.svc.ProcessEntry(h.ID, it.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	successAt := tsDay(2, 22, 30)
	f.svc.nowAt(func() time.Time { return successAt })
	ok, err := f.svc.CloseShift(b.ID)
	if err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if !ok.Closed || ok.ClosedAt == nil || !ok.ClosedAt.Equal(successAt) ||
		ok.CloseRecord == nil || len(ok.CloseRecord.Items) != 2 {
		t.Fatalf("重试成功应以成功时刻留下含两项的结束时记录：%+v", ok)
	}
	if ok.CloseRecord.Items[0].ItemID != it.ID || ok.CloseRecord.Items[1].ItemID != own.ID {
		t.Fatalf("结束时记录应按编号保留本班新增与已接收事项：%+v", ok.CloseRecord.Items)
	}
}
