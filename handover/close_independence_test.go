package handover

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 本文件把“班次成功结束后，结束时记录必须保留这次成功结束时的事实”这一
// 约定变成可重复执行的检查：
//   - 交给调用方的班次结果与系统保存的结束时记录相互独立，调用方为本地展示
//     改动手中结果（实际结束时刻、结束时事项内容、严重程度、限制条件、后续
//     负责人、关闭情况、关闭时间，或移除清单中的一项）只能影响这份结果；
//   - 结束前关闭事项所得结果中的关闭时间与结束时记录各属各的副本，改写那份
//     关闭结果不能影响结束时保留的关闭人与关闭时间；
//   - 后班对结束时未关闭事项的接收、修改与关闭，以及随后的正常保存，都不改写
//     调用方保留的结束结果，也不把调用方对本地结果的篡改带进原班次历史；
//   - 没有事项的班次结束后仍留下明确的空记录（非 nil 的空清单），与缺少
//     结束时记录的旧数据相区别。

// prepareCloseIndependenceScenario 建立一个有在班事项的班次：一项未关闭
// （带限制条件、重要），一项在班次结束前关闭并返回关闭结果。返回班次、
// 两项编号与结束前关闭事项所得的关闭结果。
func prepareCloseIndependenceScenario(t *testing.T, f *fixture) (Shift, string, string, Item) {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	open, err := f.svc.AddItem(a.ID, "压力异常待跟踪", SeverityImportant, "不得超过上限", "李四")
	if err != nil {
		t.Fatalf("add open item: %v", err)
	}
	closed, err := f.svc.AddItem(a.ID, "巡检记录归档", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add closed item: %v", err)
	}
	closeResult, err := f.svc.CloseItem(closed.ID, "张三")
	if err != nil {
		t.Fatalf("close item before shift close: %v", err)
	}
	return a, open.ID, closed.ID, closeResult
}

// TestCloseShiftResultIndependentFromStoredRecord：调用方改动成功结束操作
// 返回的班次结果（实际结束时刻、结束时事项的内容/严重程度/限制条件/后续
// 负责人/关闭情况/关闭时间/关闭人，并移除清单中的一项）后，再查询原班次
// 仍读到成功结束当时的完整事实；先取得的班次报告也不被连带改写。
func TestCloseShiftResultIndependentFromStoredRecord(t *testing.T) {
	f := newFixture(t)
	a, idOpen, idClosed, _ := prepareCloseIndependenceScenario(t, f)

	result, err := f.svc.CloseShift(a.ID)
	if err != nil {
		t.Fatalf("close shift: %v", err)
	}
	if !result.Closed || result.ClosedAt == nil || result.CloseRecord == nil {
		t.Fatalf("结束成功应返回已结束班次与结束时记录：%+v", result)
	}
	closedAtOrig := *result.ClosedAt
	if len(result.CloseRecord.Items) != 2 {
		t.Fatalf("前置：结束时记录应含2项（含结束前已关闭者），got %+v", result.CloseRecord.Items)
	}

	// 结束后先取得一份班次报告，作为“已取得的另一份报告”的原值基准。
	reportBefore, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report before: %v", err)
	}
	reportSnapshot := mustJSON(t, reportBefore)

	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 改动调用方手中的结束结果：实际结束时刻。
	*result.ClosedAt = forge
	// 改动结束时事项：内容、严重程度、限制条件、后续负责人、关闭情况、
	// 关闭时间与关闭人；并把未关闭项从清单中移除。
	var kept []CloseItemSnapshot
	for i := range result.CloseRecord.Items {
		s := &result.CloseRecord.Items[i]
		if s.ItemID == idOpen {
			s.Content = "篡改结束时内容"
			s.Severity = SeverityUrgent
			s.Constraints = "篡改限制"
			s.FollowOwner = "篡改负责人"
			s.Closed = true
			s.ClosedAt = &forge
			s.CloseOperator = "篡改人"
			continue // 从本地结果的清单中移除这一项
		}
		if s.ItemID == idClosed {
			s.Content = "篡改已关闭事项内容"
			*s.ClosedAt = forge
			s.CloseOperator = "篡改人"
		}
		kept = append(kept, *s)
	}
	result.CloseRecord.Items = kept

	// 再查原班次：结束时事项仍完整保留原内容、严重程度、限制条件、后续
	// 负责人和关闭情况，清单顺序与编号不变。
	stored, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after mutation: %v", err)
	}
	if !stored.Closed || stored.ClosedAt == nil || !stored.ClosedAt.Equal(closedAtOrig) {
		t.Fatalf("实际结束时刻不应被本地结果改写：%+v", stored)
	}
	if stored.CloseRecord == nil || len(stored.CloseRecord.Items) != 2 {
		t.Fatalf("结束时清单不应被本地结果移除条目，应仍含2项，got %+v", stored.CloseRecord)
	}
	if stored.CloseRecord.Items[0].ItemID != idOpen ||
		stored.CloseRecord.Items[1].ItemID != idClosed {
		t.Fatalf("清单顺序和编号应不变：%+v", stored.CloseRecord.Items)
	}
	sOpen := stored.CloseRecord.Items[0]
	if sOpen.Content != "压力异常待跟踪" || sOpen.Severity != SeverityImportant ||
		sOpen.Constraints != "不得超过上限" || sOpen.FollowOwner != "李四" ||
		sOpen.Closed || sOpen.ClosedAt != nil || sOpen.CloseOperator != "" {
		t.Fatalf("结束时未关闭事项应保留原内容、严重程度、限制条件、负责人与未关闭情况：%+v", sOpen)
	}
	sClosed := stored.CloseRecord.Items[1]
	if !sClosed.Closed || sClosed.CloseOperator != "张三" ||
		sClosed.ClosedAt == nil || sClosed.ClosedAt.Equal(forge) ||
		sClosed.Content != "巡检记录归档" {
		t.Fatalf("结束前已关闭事项应保留原关闭人与关闭时间：%+v", sClosed)
	}

	// 班次报告同样读到系统保存的原事实，且与先取得的那份报告完全一致。
	reportAfter, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report after mutation: %v", err)
	}
	if got := mustJSON(t, reportAfter); got != reportSnapshot {
		t.Fatalf("改动结束结果不应改写已取得的班次报告与系统记录\nwant %s\ngot  %s",
			reportSnapshot, got)
	}
}

// TestMutatingEarlierCloseItemResultKeepsCloseRecord：调用方在班次结束后
// 改写“结束前关闭事项”操作所得结果中的关闭时刻（及关闭人），结束时记录中
// 该事项的关闭人和关闭时间仍保留当时的值，事项本身保存的关闭信息也不变。
func TestMutatingEarlierCloseItemResultKeepsCloseRecord(t *testing.T) {
	f := newFixture(t)
	a, _, idClosed, closeResult := prepareCloseIndependenceScenario(t, f)
	if closeResult.ClosedAt == nil {
		t.Fatalf("前置：关闭结果应带关闭时间")
	}
	closedAtOrig := *closeResult.ClosedAt

	result, err := f.svc.CloseShift(a.ID)
	if err != nil {
		t.Fatalf("close shift: %v", err)
	}

	forge := time.Date(2032, 1, 2, 3, 4, 5, 0, time.UTC)
	*closeResult.ClosedAt = forge
	closeResult.CloseOperator = "篡改人"
	closeResult.Content = "篡改关闭结果内容"

	// 结束时记录保留当时的关闭人与关闭时间。
	var snap *CloseItemSnapshot
	for i := range result.CloseRecord.Items {
		if result.CloseRecord.Items[i].ItemID == idClosed {
			snap = &result.CloseRecord.Items[i]
		}
	}
	if snap == nil {
		t.Fatalf("结束前已关闭事项应进入结束时记录")
	}
	// 注意：这里核对的是系统保存的记录，而不是被本地改动后可能受影响的
	// result 本身——result 是结束操作的独立副本，再查一次系统为准。
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	storedSnap := findCloseItem(rep, idClosed)
	if storedSnap == nil || !storedSnap.Closed || storedSnap.CloseOperator != "张三" ||
		storedSnap.ClosedAt == nil || !storedSnap.ClosedAt.Equal(closedAtOrig) {
		t.Fatalf("结束时记录应保留当时的关闭人与关闭时间，不受关闭结果改写影响：%+v", storedSnap)
	}
	// 事项本身保存的关闭信息同样不变。
	storedItem, err := f.svc.GetItem(idClosed)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if !storedItem.Closed || storedItem.CloseOperator != "张三" ||
		storedItem.ClosedAt == nil || !storedItem.ClosedAt.Equal(closedAtOrig) {
		t.Fatalf("事项保存的关闭人与关闭时间不应被关闭结果改写：%+v", storedItem)
	}
	// 手中那份关闭结果确实只保留本地改动，不反向影响系统（上面已证），也不
	// 影响结束操作结果中独立保存的同一项。
	if snap.ClosedAt == nil || !snap.ClosedAt.Equal(closedAtOrig) ||
		snap.CloseOperator != "张三" {
		t.Fatalf("结束操作结果中的关闭时间应是独立副本，不与关闭结果共享：%+v", snap)
	}
}

// TestRetainedCloseResultSurvivesLaterShiftAndSaves：结束时未关闭的事项后来
// 由下一班接收、修改并关闭。调用方保留的结束操作结果不随后班处理变化；
// 原班次历史仍显示该事项结束时未关闭，最新状态对照才反映现状。即便调用方
// 先篡改了手中的结束结果，后续正常保存也不把篡改带进原班次历史。
func TestRetainedCloseResultSurvivesLaterShiftAndSaves(t *testing.T) {
	f := newFixture(t)
	a, idOpen, idClosed, _ := prepareCloseIndependenceScenario(t, f)

	result, err := f.svc.CloseShift(a.ID)
	if err != nil {
		t.Fatalf("close shift a: %v", err)
	}
	closeAt := *result.ClosedAt
	// 结束操作刚返回时取一份原班次事实快照，作为后续正常保存后的对照基准。
	storedBefore, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift before: %v", err)
	}
	storedSnapshot := mustJSON(t, storedBefore)

	// 调用方为本地展示篡改手中的结束结果。
	forge := time.Date(2033, 9, 8, 7, 6, 5, 0, time.UTC)
	*result.ClosedAt = forge
	for i := range result.CloseRecord.Items {
		s := &result.CloseRecord.Items[i]
		if s.ItemID == idOpen {
			s.Content = "本地展示内容"
			s.Closed = true
			s.ClosedAt = &forge
		}
	}

	// 下一班：交接、确认接收、修改并关闭该事项。
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idOpen, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.UpdateItem(idOpen, "后班修订内容", SeverityUrgent, "后班限制", "赵六"); err != nil {
		t.Fatalf("update in next shift: %v", err)
	}
	if _, err := f.svc.CloseItem(idOpen, "李四"); err != nil {
		t.Fatalf("close in next shift: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close shift b: %v", err)
	}

	// 调用方保留的结束结果不随后班处理变化。
	if result.ClosedAt == nil || !result.ClosedAt.Equal(forge) {
		t.Fatalf("保留的结束结果应只受本地改动影响，不随后班处理变化：%v", result.ClosedAt)
	}
	var retainedOpen *CloseItemSnapshot
	for i := range result.CloseRecord.Items {
		if result.CloseRecord.Items[i].ItemID == idOpen {
			retainedOpen = &result.CloseRecord.Items[i]
		}
	}
	if retainedOpen == nil || retainedOpen.Content != "本地展示内容" || !retainedOpen.Closed {
		t.Fatalf("保留的结束结果应保持本地改动后的样子：%+v", retainedOpen)
	}

	// 原班次历史不随后班处理变化，也不被本地结果的篡改污染（含正常保存后）。
	storedAfter, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after: %v", err)
	}
	if got := mustJSON(t, storedAfter); got != storedSnapshot {
		t.Fatalf("后续正常保存不得改写原班次历史或带入本地结果篡改\nwant %s\ngot  %s",
			storedSnapshot, got)
	}
	repA, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	snap := findCloseItem(repA, idOpen)
	if snap == nil || snap.Closed || snap.Content != "压力异常待跟踪" ||
		snap.Severity != SeverityImportant || snap.Constraints != "不得超过上限" ||
		snap.FollowOwner != "李四" {
		t.Fatalf("原班次结束时记录应仍显示该事项当时未关闭：%+v", snap)
	}
	if repA.Shift.ClosedAt == nil || !repA.Shift.ClosedAt.Equal(closeAt) {
		t.Fatalf("原班次实际结束时刻应保持：%v", repA.Shift.ClosedAt)
	}
	// 最新状态对照继续反映正常业务操作后的现状。
	latest := repA.LatestItems[idOpen]
	if !latest.Closed || latest.CurrentShiftID != b.ID || latest.FollowOwner != "赵六" ||
		latest.Content != "后班修订内容" || latest.CloseOperator != "李四" {
		t.Fatalf("最新状态对照应反映后班接收、修改与关闭后的现状：%+v", latest)
	}
	// 结束前已关闭事项在原班次记录中仍保留当时的关闭信息。
	snapClosed := findCloseItem(repA, idClosed)
	if snapClosed == nil || !snapClosed.Closed || snapClosed.CloseOperator != "张三" {
		t.Fatalf("结束前已关闭事项应仍保留在原班次记录中：%+v", snapClosed)
	}

	// 落盘文件不得包含本地篡改的任何痕迹。
	raw, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	if strings.Contains(string(raw), "本地展示内容") ||
		strings.Contains(string(raw), forge.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("本地结果篡改不得在后续正常保存时写入数据文件")
	}
}

// TestEmptyShiftCloseLeavesExplicitEmptyRecord：没有事项的班次结束后留下
// 明确的空记录（非 nil 的空清单），与缺少结束时记录的旧数据相区别；调用方
// 向手中结果塞入假事项、篡改结束时刻，不影响系统保存的空记录。
func TestEmptyShiftCloseLeavesExplicitEmptyRecord(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")

	result, err := f.svc.CloseShift(a.ID)
	if err != nil {
		t.Fatalf("close empty shift: %v", err)
	}
	if result.CloseRecord == nil {
		t.Fatalf("没有事项的班次结束后也应留下非 nil 的结束时记录，以区别于旧数据缺记录")
	}
	if len(result.CloseRecord.Items) != 0 {
		t.Fatalf("空班次结束时记录应为空清单，got %+v", result.CloseRecord.Items)
	}
	closedAtOrig := *result.ClosedAt

	forge := time.Date(2034, 3, 4, 5, 6, 7, 0, time.UTC)
	*result.ClosedAt = forge
	result.CloseRecord.Items = append(result.CloseRecord.Items,
		CloseItemSnapshot{ItemID: "I999", Content: "篡改事项"})

	stored, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if stored.CloseRecord == nil || len(stored.CloseRecord.Items) != 0 {
		t.Fatalf("系统应保存明确的空记录，不受手中结果塞入假事项影响：%+v", stored.CloseRecord)
	}
	if stored.ClosedAt == nil || !stored.ClosedAt.Equal(closedAtOrig) {
		t.Fatalf("实际结束时刻不应被手中结果改写：%v", stored.ClosedAt)
	}
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("空记录应被识别为结束时事实而非历史不完整：%+v", rep)
	}

	// 退出重开后仍是明确的空记录（落盘后非 nil 空清单可被识别）。
	f.reopen(t)
	reopened, err := f.svc.GetShift(a.ID)
	if err != nil {
		t.Fatalf("get shift after reopen: %v", err)
	}
	if reopened.CloseRecord == nil || len(reopened.CloseRecord.Items) != 0 {
		t.Fatalf("重开后应仍是明确的空记录：%+v", reopened.CloseRecord)
	}
}
