package handover

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// twoIncomingExpectations 汇总“一个接班班次同时收到两份交接”场景中各业务事实
// 的期望，供拒绝结束阶段、成功结束后以及退出重开前后用同一组期望核对。
type twoIncomingExpectations struct {
	aID, bID, cID          string
	h1ID, h2ID             string
	idA1, idA2, idB1, idC1 string
	aClosedAt              time.Time // 前班A成功结束的时间
	bClosedAt              time.Time // 前班B成功结束的时间
	idA2ClosedAt           time.Time // 前班A在自己结束前关闭的事项的关闭时间
	confirmAt              time.Time // 第一份交接确认接收的时间
	returnAt               time.Time // 第二份交接退回的时间
	resubmitAt             time.Time // 退回项补充后重新提交的时间
	trackAt                time.Time // 第二份交接继续跟踪（接收）的时间
	failPendingAt          time.Time // 有待处理项时失败的结束尝试时刻
	failReturnedAt         time.Time // 有退回项时失败的结束尝试时刻
	idC1ClosedAt           time.Time // 本班新增事项的关闭时间
	successAt              time.Time // 真正成功结束本班的时间
}

// 本场景使用的事项内容与负责人取值。前班交来事项的原始取值与后班修改后的
// 最终取值不同，用于区分结束时记录保存的是哪一版事实。
const (
	a1OrigContent      = "泵压波动待观察"
	a1OrigConstraints  = "限速运行"
	a1OrigFollow       = "孙八"
	a1FinalContent     = "修订后的泵压波动"
	a1FinalConstraints = "需双人复核"
	a1FinalFollow      = "钱七"
	a2Content          = "班内已完成的巡检"
	b1Content          = "机组振动偏高"
	b1Constraints      = "禁止满载"
	b1OrigFollow       = "周九"
	c1Content          = "本班记录归档"
	trackNoteText      = "继续跟踪振动趋势"
	trackFollowName    = "赵六"
	returnReasonText   = "缺少现场照片"
	supplementText     = "已补充现场照片"
)

// prepareTwoIncomingShifts 建立三个同岗位依次衔接的班次：前班A（含一项将交出的
// 未关闭事项与一项结束前关闭的事项）、前班B（含一项将交出的未关闭事项）、
// 仍在进行中的后班C。返回三个班次与四个事项编号。
func prepareTwoIncomingShifts(t *testing.T, f *fixture, w *twoIncomingExpectations) (a, b, c Shift) {
	t.Helper()
	a = mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b = mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(3, 0, 0), "")
	c = mustShift(t, f, "调度", "王五", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	w.aID, w.bID, w.cID = a.ID, b.ID, c.ID

	// 前班A：一项未关闭（随后交给后班），一项在自己结束前关闭（不交出）。
	a1, err := f.svc.AddItem(a.ID, a1OrigContent, SeverityImportant, a1OrigConstraints, a1OrigFollow)
	if err != nil {
		t.Fatalf("add a1: %v", err)
	}
	a2, err := f.svc.AddItem(a.ID, a2Content, SeverityNormal, "", "张三")
	if err != nil {
		t.Fatalf("add a2: %v", err)
	}
	w.idA1, w.idA2 = a1.ID, a2.ID
	f.svc.nowAt(func() time.Time { return w.idA2ClosedAt })
	if _, err := f.svc.CloseItem(a2.ID, "张三"); err != nil {
		t.Fatalf("close a2 in prior shift: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.aClosedAt })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close shift a: %v", err)
	}

	// 前班B：一项未关闭（随后交给后班），随后结束。
	b1, err := f.svc.AddItem(b.ID, b1Content, SeverityUrgent, b1Constraints, b1OrigFollow)
	if err != nil {
		t.Fatalf("add b1: %v", err)
	}
	w.idB1 = b1.ID
	f.svc.nowAt(func() time.Time { return w.bClosedAt })
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close shift b: %v", err)
	}

	// 后班C：一项本班新增事项。
	c1, err := f.svc.AddItem(c.ID, c1Content, SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add c1: %v", err)
	}
	w.idC1 = c1.ID
	return a, b, c
}

// assertPriorShiftRecordsIntact 核对两个前班的结束时记录保持各自结束时的原样：
// 前班A 的记录含交出的未关闭事项（原始取值、当时未关闭）与自己关闭的事项
// （保留关闭人与关闭时间）；前班B 的记录含交出的未关闭事项（原始取值）。后班
// 后来的修改、接收与结束都不改变这些冻结记录。
func assertPriorShiftRecordsIntact(t *testing.T, f *fixture, w twoIncomingExpectations) {
	t.Helper()

	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	if !repA.ItemsAtClose || len(repA.CloseItems) != 2 {
		t.Fatalf("前班A结束时记录应保留2项，got %+v", repA.CloseItems)
	}
	snapA1 := findCloseItem(repA, w.idA1)
	if snapA1 == nil || snapA1.Content != a1OrigContent ||
		snapA1.Severity != SeverityImportant || snapA1.Constraints != a1OrigConstraints ||
		snapA1.FollowOwner != a1OrigFollow || snapA1.Closed {
		t.Fatalf("前班A记录中交出的事项应保持结束时原样：%+v", snapA1)
	}
	snapA2 := findCloseItem(repA, w.idA2)
	if snapA2 == nil || !snapA2.Closed || snapA2.CloseOperator != "张三" ||
		snapA2.ClosedAt == nil || !snapA2.ClosedAt.Equal(w.idA2ClosedAt) {
		t.Fatalf("前班A记录中已关闭事项应保留关闭人与关闭时间：%+v", snapA2)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, "以下为结束时记录") ||
		!strings.Contains(textA, a1OrigContent) ||
		!strings.Contains(textA, "结束时后续负责人："+a1OrigFollow) {
		t.Fatalf("前班A报告应展示自己的原始结束时记录：\n%s", textA)
	}
	if strings.Contains(textA, a1FinalContent) {
		t.Fatalf("后班对事项的修改不应进入前班A的报告：\n%s", textA)
	}

	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report b: %v", err)
	}
	if !repB.ItemsAtClose || len(repB.CloseItems) != 1 {
		t.Fatalf("前班B结束时记录应保留1项，got %+v", repB.CloseItems)
	}
	snapB1 := findCloseItem(repB, w.idB1)
	if snapB1 == nil || snapB1.Content != b1Content ||
		snapB1.Severity != SeverityUrgent || snapB1.Constraints != b1Constraints ||
		snapB1.FollowOwner != b1OrigFollow || snapB1.Closed {
		t.Fatalf("前班B记录中交出的事项应保持结束时原样：%+v", snapB1)
	}
	textB := FormatReport(repB)
	if !strings.Contains(textB, "结束时后续负责人："+b1OrigFollow) {
		t.Fatalf("前班B报告应保留接收前的原负责人：\n%s", textB)
	}
}

// assertStillOpenAfterRefusedClose 核对结束被拒绝后：后班保持进行中，没有结束
// 时间也没有结束时记录；已接收事项的归属与此前处理经过保持原样，未接收事项
// 仍留在交班班次；两份交接各自的进度不变；两个前班的结束时记录不受影响。
// h2Status 是第二份交接当前应处的状态（待处理或退回）。
func assertStillOpenAfterRefusedClose(t *testing.T, f *fixture, w twoIncomingExpectations, h2Status EntryStatus) {
	t.Helper()

	// 失败尝试不新增班次、交接，也不复制事项。
	if hs := f.svc.ListShifts(); len(hs) != 3 {
		t.Fatalf("失败尝试不应新增班次，got %d 个", len(hs))
	}
	if hs := f.svc.ListHandovers(); len(hs) != 2 || hs[0].ID != w.h1ID || hs[1].ID != w.h2ID {
		t.Fatalf("失败尝试不应新增交接：%+v", hs)
	}
	for _, id := range []string{w.idA1, w.idA2, w.idB1, w.idC1} {
		if n := countItems(f, id); n != 1 {
			t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", id, n)
		}
	}

	// 后班仍在进行中：没有结束时间，也没有结束时记录。
	c, err := f.svc.GetShift(w.cID)
	if err != nil {
		t.Fatalf("get shift c: %v", err)
	}
	if c.Closed || c.ClosedAt != nil || c.CloseRecord != nil {
		t.Fatalf("结束被拒绝后班次应仍在进行中、无结束时间与结束时记录：%+v", c)
	}
	repC, err := f.svc.ShiftReport(w.cID)
	if err != nil {
		t.Fatalf("report c: %v", err)
	}
	if repC.Shift.Closed || repC.ItemsAtClose || repC.HistoryIncomplete || len(repC.CloseItems) != 0 {
		t.Fatalf("进行中班次不应出现结束时记录或历史不完整标记：%+v", repC.Shift)
	}
	// 当前事项只含已接收的前班A事项与本班新增事项；前班B事项尚未接收，不在其中。
	if len(repC.Items) != 2 || repC.Items[0].ID != w.idA1 || repC.Items[1].ID != w.idC1 {
		t.Fatalf("后班当前事项应为已接收项与本班新增项各一次：%+v", repC.Items)
	}

	// 已接收事项：归属已转到后班，保持接收时的内容，此前处理经过不变。
	a1, err := f.svc.GetItem(w.idA1)
	if err != nil {
		t.Fatalf("get a1: %v", err)
	}
	if a1.OriginShiftID != w.aID || a1.CurrentShiftID != w.cID ||
		len(a1.ShiftIDs) != 2 || a1.ShiftIDs[0] != w.aID || a1.ShiftIDs[1] != w.cID {
		t.Fatalf("已接收事项的归属与流经班次应保持：%+v", a1)
	}
	if a1.Content != a1OrigContent || a1.FollowOwner != a1OrigFollow || a1.Closed {
		t.Fatalf("已接收事项应保持接收时的内容与未关闭状态：%+v", a1)
	}
	// 未接收事项仍留在前班B，不因失败尝试移动或改变。
	b1, err := f.svc.GetItem(w.idB1)
	if err != nil {
		t.Fatalf("get b1: %v", err)
	}
	if b1.CurrentShiftID != w.bID || b1.OriginShiftID != w.bID ||
		len(b1.ShiftIDs) != 1 || b1.FollowOwner != b1OrigFollow || b1.Closed {
		t.Fatalf("未接收事项应仍留在交班班次：%+v", b1)
	}
	// 前班A已关闭、没有交来的事项仍在原班、保持关闭。
	a2, err := f.svc.GetItem(w.idA2)
	if err != nil {
		t.Fatalf("get a2: %v", err)
	}
	if !a2.Closed || a2.CurrentShiftID != w.aID || a2.CloseOperator != "张三" {
		t.Fatalf("前班已关闭事项应保持原样：%+v", a2)
	}

	// 第一份交接已完成，接收结果、处理人、处理时间与完成时间保留。
	h1, err := f.svc.GetHandover(w.h1ID)
	if err != nil {
		t.Fatalf("get h1: %v", err)
	}
	if !h1.Completed() || h1.CompletedAt == nil || !h1.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("已完成交接的完成时间应保留：%+v", h1)
	}
	e1 := findEntryOf(t, h1, w.idA1)
	if e1.Status != EntryConfirmed || e1.Operator != "王五" ||
		e1.ProcessedAt == nil || !e1.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("第一份交接的确认接收记录应保留：%+v", e1)
	}

	// 第二份交接保持未完成，进度与失败前一致。
	h2, err := f.svc.GetHandover(w.h2ID)
	if err != nil {
		t.Fatalf("get h2: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("第二份交接应保持未完成且无完成时间：%+v", h2)
	}
	e2 := findEntryOf(t, h2, w.idB1)
	if e2.Status != h2Status {
		t.Fatalf("第二份交接事项应保持 %s，got %+v", h2Status, e2)
	}
	if h2Status == EntryReturned {
		if len(e2.Rounds) != 1 || e2.Rounds[0].Reason != returnReasonText ||
			e2.Rounds[0].ReturnOperator != "王五" || !e2.Rounds[0].ReturnedAt.Equal(w.returnAt) {
			t.Fatalf("退回轮次记录应保持原样：%+v", e2.Rounds)
		}
	}

	// 两个前班的结束时记录不受失败尝试影响。
	assertPriorShiftRecordsIntact(t, f, w)
}

// assertClosedWithThreeItems 核对后班成功结束：结束时间属于这次成功操作，
// 结束时清单完整保留三项（两份交接各接收一项 + 本班新增一项），每个原事项
// 编号只出现一次并沿用编号顺序；清单保存结束前最后生效的信息，已关闭项
// 保留关闭人和关闭时间，未关闭项显示结束时未关闭；交接清单保存的原文与
// 接收当时的信息不替代结束时事实，前班冻结记录也不随后班修改变化。
func assertClosedWithThreeItems(t *testing.T, f *fixture, w twoIncomingExpectations) {
	t.Helper()

	c, err := f.svc.GetShift(w.cID)
	if err != nil {
		t.Fatalf("get shift c after success: %v", err)
	}
	if !c.Closed || c.ClosedAt == nil || !c.ClosedAt.Equal(w.successAt) || c.CloseRecord == nil {
		t.Fatalf("后班应以成功操作时刻结束并留下结束时记录：%+v", c)
	}

	repC, err := f.svc.ShiftReport(w.cID)
	if err != nil {
		t.Fatalf("report c after success: %v", err)
	}
	if !repC.ItemsAtClose || repC.HistoryIncomplete {
		t.Fatalf("成功结束后事项区应标为结束时记录：%+v", repC)
	}
	// 结束时清单完整保留三项，每个原事项编号只出现一次，并沿用现有编号顺序。
	if len(repC.CloseItems) != 3 {
		t.Fatalf("结束时记录应含3项，got %d：%+v", len(repC.CloseItems), repC.CloseItems)
	}
	wantOrder := []string{w.idA1, w.idB1, w.idC1}
	seen := map[string]int{}
	for i, snap := range repC.CloseItems {
		if snap.ItemID != wantOrder[i] {
			t.Fatalf("结束时记录应按编号顺序排列：第%d项 got %s，want %s", i, snap.ItemID, wantOrder[i])
		}
		seen[snap.ItemID]++
	}
	for _, id := range wantOrder {
		if seen[id] != 1 {
			t.Fatalf("事项 %s 在结束时记录中应只出现一次，got %d", id, seen[id])
		}
	}
	// 前班A已关闭、没有交来的事项仍只留在前班记录中，不能混入后班清单。
	if findCloseItem(repC, w.idA2) != nil {
		t.Fatalf("前班已关闭且未交来的事项不应出现在后班结束时记录中")
	}

	// 接收自前班A的事项：保存结束前最后生效的修改（内容、严重程度、限制条件、
	// 负责人），而不是交接清单里的原文或接收当时的信息；结束时未关闭。
	snapA1 := findCloseItem(repC, w.idA1)
	if snapA1 == nil || snapA1.Content != a1FinalContent || snapA1.Severity != SeverityUrgent ||
		snapA1.Constraints != a1FinalConstraints || snapA1.FollowOwner != a1FinalFollow ||
		snapA1.Closed || snapA1.CloseOperator != "" || snapA1.ClosedAt != nil {
		t.Fatalf("接收项应冻结结束前最后生效的信息且仍为未关闭：%+v", snapA1)
	}
	// 接收自前班B的事项：继续跟踪指定的新负责人成为该事项在后班的负责人；
	// 事项的原始班次不同，不影响它被接收后进入本班结束时记录；结束时未关闭。
	snapB1 := findCloseItem(repC, w.idB1)
	if snapB1 == nil || snapB1.Content != b1Content || snapB1.Severity != SeverityUrgent ||
		snapB1.Constraints != b1Constraints || snapB1.FollowOwner != trackFollowName ||
		snapB1.Closed || snapB1.CloseOperator != "" || snapB1.ClosedAt != nil {
		t.Fatalf("继续跟踪接收项应保留新负责人且仍为未关闭：%+v", snapB1)
	}
	// 本班新增且结束前已关闭的事项：保留关闭人和关闭时间。
	snapC1 := findCloseItem(repC, w.idC1)
	if snapC1 == nil || !snapC1.Closed || snapC1.CloseOperator != "王五" ||
		snapC1.ClosedAt == nil || !snapC1.ClosedAt.Equal(w.idC1ClosedAt) {
		t.Fatalf("本班新增已关闭项应保留关闭人与关闭时间：%+v", snapC1)
	}

	// 最新状态对照：两项接收项当前仍在后班、未关闭，负责人为结束时生效值。
	latestA1 := repC.LatestItems[w.idA1]
	if latestA1.CurrentShiftID != w.cID || latestA1.FollowOwner != a1FinalFollow || latestA1.Closed {
		t.Fatalf("接收项最新状态应对照成功时信息：%+v", latestA1)
	}
	latestB1 := repC.LatestItems[w.idB1]
	if latestB1.CurrentShiftID != w.cID || latestB1.FollowOwner != trackFollowName || latestB1.Closed {
		t.Fatalf("继续跟踪接收项最新状态应对照成功时信息：%+v", latestB1)
	}
	// 报告应同时列出两份接班交接。
	if len(repC.Incoming) != 2 || repC.Incoming[0].ID != w.h1ID || repC.Incoming[1].ID != w.h2ID {
		t.Fatalf("后班报告应列出两份接班交接：%+v", repC.Incoming)
	}

	// 班次报告明确展示结束时记录：保存的是结束前最后生效的信息；已关闭项
	// 保留关闭人和关闭时间，另两项显示结束时未关闭；失败尝试时刻不出现。
	textC := FormatReport(repC)
	if !strings.Contains(textC, "以下为结束时记录") ||
		!strings.Contains(textC, a1FinalContent) ||
		!strings.Contains(textC, "结束时后续负责人："+a1FinalFollow) ||
		!strings.Contains(textC, "结束时后续负责人："+trackFollowName) {
		t.Fatalf("后班报告应按结束前最后生效的信息展示结束时记录：\n%s", textC)
	}
	if !strings.Contains(textC, "结束时已关闭（王五 于 "+fmtTime(w.idC1ClosedAt)+"）") {
		t.Fatalf("结束时记录应保留已关闭项的关闭人与关闭时间：\n%s", textC)
	}
	if n := strings.Count(textC, "结束时未关闭"); n != 2 {
		t.Fatalf("两个未关闭接收项应都显示结束时未关闭，got %d 处：\n%s", n, textC)
	}
	if strings.Contains(textC, fmtTime(w.failPendingAt)) || strings.Contains(textC, fmtTime(w.failReturnedAt)) {
		t.Fatalf("失败的结束尝试时刻不应出现在成功后的报告中：\n%s", textC)
	}

	// 交接清单保存的原文与接收当时的信息不替代结束时事实：第一份交接仍保存
	// 事项原文与确认接收记录；第二份交接保存原文、退回与补充经过、继续跟踪
	// 当时的跟踪说明与负责人。
	h1, err := f.svc.GetHandover(w.h1ID)
	if err != nil {
		t.Fatalf("get h1 after success: %v", err)
	}
	e1 := findEntryOf(t, h1, w.idA1)
	if e1.Content != a1OrigContent || e1.Constraints != a1OrigConstraints ||
		e1.FollowOwner != a1OrigFollow {
		t.Fatalf("交接清单应保存接收当时的原文与信息，不被后班修改改写：%+v", e1)
	}
	if e1.Status != EntryConfirmed || e1.Operator != "王五" ||
		e1.ProcessedAt == nil || !e1.ProcessedAt.Equal(w.confirmAt) ||
		h1.CompletedAt == nil || !h1.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("第一份交接的接收结果与完成时间应保留：%+v", e1)
	}
	h2, err := f.svc.GetHandover(w.h2ID)
	if err != nil {
		t.Fatalf("get h2 after success: %v", err)
	}
	if !h2.Completed() || h2.CompletedAt == nil || !h2.CompletedAt.Equal(w.trackAt) {
		t.Fatalf("第二份交接应以最后一项接收时刻完成：%+v", h2)
	}
	e2 := findEntryOf(t, h2, w.idB1)
	if e2.Content != b1Content || e2.Status != EntryTracking || e2.Operator != "王五" ||
		e2.ProcessedAt == nil || !e2.ProcessedAt.Equal(w.trackAt) ||
		e2.TrackingNote != trackNoteText || e2.FollowOwner != trackFollowName {
		t.Fatalf("第二份交接应保存原文与继续跟踪当时的信息：%+v", e2)
	}
	if len(e2.Rounds) != 1 || e2.Rounds[0].Reason != returnReasonText ||
		e2.Rounds[0].Supplement != supplementText ||
		e2.Rounds[0].ResubmittedAt == nil || !e2.Rounds[0].ResubmittedAt.Equal(w.resubmitAt) {
		t.Fatalf("退回与补充经过应完整保留：%+v", e2.Rounds)
	}

	// 事项本身：接收项保留原编号与流经班次，处理经过只含成功事实，没有失败
	// 的结束尝试时刻；继续跟踪指定的新负责人已成为事项在后班的负责人。
	a1, err := f.svc.GetItem(w.idA1)
	if err != nil {
		t.Fatalf("get a1 after success: %v", err)
	}
	if a1.Closed || a1.Content != a1FinalContent || a1.FollowOwner != a1FinalFollow ||
		a1.OriginShiftID != w.aID || a1.CurrentShiftID != w.cID {
		t.Fatalf("接收项应保持成功时的当前信息：%+v", a1)
	}
	b1, err := f.svc.GetItem(w.idB1)
	if err != nil {
		t.Fatalf("get b1 after success: %v", err)
	}
	if b1.Closed || b1.FollowOwner != trackFollowName ||
		b1.OriginShiftID != w.bID || b1.CurrentShiftID != w.cID ||
		len(b1.ShiftIDs) != 2 || b1.ShiftIDs[0] != w.bID || b1.ShiftIDs[1] != w.cID {
		t.Fatalf("继续跟踪接收项应保留原编号、流经班次与新负责人：%+v", b1)
	}
	jA1, err := f.svc.ItemJourney(w.idA1)
	if err != nil {
		t.Fatalf("journey a1: %v", err)
	}
	if got := joinStrings(journeyKinds(jA1)); got != "created,handover-init,confirm,updated" {
		t.Fatalf("确认接收项的处理经过应呈现一次接收与一次修改，got %s", got)
	}
	jB1, err := f.svc.ItemJourney(w.idB1)
	if err != nil {
		t.Fatalf("journey b1: %v", err)
	}
	if got := joinStrings(journeyKinds(jB1)); got != "created,handover-init,return,resubmit,track" {
		t.Fatalf("继续跟踪接收项的处理经过应呈现退回、重新提交与接收，got %s", got)
	}
	for _, j := range []ItemJourney{jA1, jB1} {
		for _, ev := range j.Events {
			if ev.TimeKnown && (ev.At.Equal(w.failPendingAt) || ev.At.Equal(w.failReturnedAt)) {
				t.Fatalf("失败的结束尝试时刻不应出现在处理经过：%+v", ev)
			}
		}
	}

	// 两个前班的冻结记录不随后班的修改、关闭与结束而变化。
	assertPriorShiftRecordsIntact(t, f, w)

	// 已结束班次不能重复结束。
	if _, err := f.svc.CloseShift(w.cID); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("成功结束后重复结束应报错，got %v", err)
	}
}

// TestCloseShiftWithTwoIncomingHandovers：两个同岗位的已结束班次分别向同一个
// 仍在进行中的后班交来一项未关闭事项，后班另有一项本班新增事项。两份交接
// 分别通过确认接收与继续跟踪完成接收后，后班正常结束：结束时清单完整保留
// 这三项，每个原事项编号只出现一次并沿用编号顺序；前班在自己结束前已关闭、
// 没有交来的事项仍只留在前班记录中。结束时记录保存结束前最后生效的信息
// （含后班对接收项的修改与本班新增项的关闭），交接清单保存的原文与接收当时
// 的信息不替代结束时事实，前班冻结记录也不随后班修改变化。一份交接已完成、
// 另一份仍有待处理或退回事项时结束被拒绝并指出阻止结束的交接，班次保持
// 进行中、不留下结束时间或结束时记录；补齐接收后结束成功，记录以那次成功
// 结束时的内容为准。
func TestCloseShiftWithTwoIncomingHandovers(t *testing.T) {
	f := newFixture(t)
	w := twoIncomingExpectations{
		aClosedAt:      tsDay(2, 15, 30),
		bClosedAt:      tsDay(2, 23, 30),
		idA2ClosedAt:   tsDay(2, 14, 0),
		confirmAt:      tsDay(3, 1, 30),
		returnAt:       tsDay(3, 2, 30),
		resubmitAt:     tsDay(3, 3, 30),
		trackAt:        tsDay(3, 4, 0),
		failPendingAt:  tsDay(3, 2, 0),
		failReturnedAt: tsDay(3, 3, 0),
		idC1ClosedAt:   tsDay(3, 6, 0),
		successAt:      tsDay(3, 7, 0),
	}
	_, _, c := prepareTwoIncomingShifts(t, f, &w)

	// 两个已结束的前班分别向后班发起交接，各交来一项未关闭事项。
	h1, err := f.svc.CreateHandover(w.aID, c.ID)
	if err != nil {
		t.Fatalf("create handover h1: %v", err)
	}
	h2, err := f.svc.CreateHandover(w.bID, c.ID)
	if err != nil {
		t.Fatalf("create handover h2: %v", err)
	}
	w.h1ID, w.h2ID = h1.ID, h2.ID
	if len(h1.Entries) != 1 || h1.Entries[0].ItemID != w.idA1 {
		t.Fatalf("第一份交接清单应只含前班A的未关闭事项：%+v", h1.Entries)
	}
	if len(h2.Entries) != 1 || h2.Entries[0].ItemID != w.idB1 {
		t.Fatalf("第二份交接清单应只含前班B的未关闭事项：%+v", h2.Entries)
	}

	// 第一份交接通过确认接收完成。
	f.svc.nowAt(func() time.Time { return w.confirmAt })
	if _, err := f.svc.ProcessEntry(h1.ID, w.idA1, ActionConfirm, "王五", "", "", ""); err != nil {
		t.Fatalf("confirm h1 entry: %v", err)
	}

	// 一份交接已完成、另一份仍有待处理事项：后班不能结束，错误指出阻止结束
	// 的交接；失败后班次保持进行中，不留下结束时间或结束时记录。
	f.svc.nowAt(func() time.Time { return w.failPendingAt })
	_, err = f.svc.CloseShift(c.ID)
	if !errors.Is(err, ErrHandoverState) {
		t.Fatalf("有待处理事项时结束应被拒绝，got %v", err)
	}
	if !strings.Contains(err.Error(), h2.ID) || strings.Contains(err.Error(), h1.ID) {
		t.Fatalf("错误应指出阻止结束的交接 %s 而非已完成的 %s，got %v", h2.ID, h1.ID, err)
	}
	assertStillOpenAfterRefusedClose(t, f, w, EntryPending)

	// 第二份交接的事项被退回后同样不能结束。
	f.svc.nowAt(func() time.Time { return w.returnAt })
	if _, err := f.svc.ProcessEntry(h2.ID, w.idB1, ActionReturn, "王五", returnReasonText, "", ""); err != nil {
		t.Fatalf("return h2 entry: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.failReturnedAt })
	_, err = f.svc.CloseShift(c.ID)
	if !errors.Is(err, ErrHandoverState) {
		t.Fatalf("有退回事项时结束应被拒绝，got %v", err)
	}
	if !strings.Contains(err.Error(), h2.ID) {
		t.Fatalf("错误应指出阻止结束的交接 %s，got %v", h2.ID, err)
	}
	assertStillOpenAfterRefusedClose(t, f, w, EntryReturned)

	// 交班人补充说明并重新提交后，接班人按继续跟踪接收，指定新的后续负责人。
	f.svc.nowAt(func() time.Time { return w.resubmitAt })
	if _, err := f.svc.ResubmitReturned(h2.ID, w.idB1, "李四", supplementText); err != nil {
		t.Fatalf("resubmit h2 entry: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.trackAt })
	if _, err := f.svc.ProcessEntry(h2.ID, w.idB1, ActionTrack, "王五", "",
		trackNoteText, trackFollowName); err != nil {
		t.Fatalf("track h2 entry: %v", err)
	}
	// 继续跟踪指定的新负责人成为该事项在后班的负责人。
	b1, err := f.svc.GetItem(w.idB1)
	if err != nil {
		t.Fatalf("get b1 after track: %v", err)
	}
	if b1.FollowOwner != trackFollowName || b1.CurrentShiftID != c.ID {
		t.Fatalf("继续跟踪指定的新负责人应成为该事项在后班的负责人：%+v", b1)
	}

	// 后班结束前再修改一个接收项的内容、严重程度、限制条件和负责人，
	// 并关闭本班新增项。
	f.svc.nowAt(func() time.Time { return tsDay(3, 5, 0) })
	if _, err := f.svc.UpdateItem(w.idA1, a1FinalContent, SeverityUrgent,
		a1FinalConstraints, a1FinalFollow); err != nil {
		t.Fatalf("update received item: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.idC1ClosedAt })
	if _, err := f.svc.CloseItem(w.idC1, "王五"); err != nil {
		t.Fatalf("close own item: %v", err)
	}

	// 两份交接都已接收齐，后班正常结束；记录以这次成功结束时的内容为准。
	f.svc.nowAt(func() time.Time { return w.successAt })
	if _, err := f.svc.CloseShift(c.ID); err != nil {
		t.Fatalf("两份交接接收齐后结束应成功：%v", err)
	}
	assertClosedWithThreeItems(t, f, w)

	// 退出后重新打开：成功结束的事实、三项结束时记录与前班冻结记录都完整保留。
	f.reopen(t)
	assertClosedWithThreeItems(t, f, w)
}
