package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// closeFailureFacts 汇总失败尝试发生前已保存的业务事实，供 reopen 前后
// 用同一组期望核对。
type closeFailureFacts struct {
	hID          string
	fromShift    string // 上一班（已成功结束并留下自己的结束时记录）
	toShift      string // 本班（结束尝试失败、应保持进行中）
	receivedID   string // 上一班交来、仍未关闭的事项
	ownID        string // 本班新增、结束前已关闭的事项
	confirmAt    time.Time
	closeItemAt  time.Time
	receivedEdit struct {
		content, constraints, follow string
		severity                     Severity
	}
}

// assertCloseFailureRolledBack 用当前打开的数据核对：班次本来允许结束、却在
// 本地保存过程中失败时，不留下任何半完成结果——班次仍进行中、没有结束时间与
// 结束时记录，事项、交接与上一班的结束时记录都保持失败前的原样。
func assertCloseFailureRolledBack(t *testing.T, f *fixture, w closeFailureFacts) {
	t.Helper()

	// 班次：仍显示进行中，没有结束时间，也没有本次生成的结束时事项记录。
	sh, err := f.svc.GetShift(w.toShift)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if sh.Closed || sh.ClosedAt != nil {
		t.Fatalf("保存失败后班次应仍为进行中、无结束时间：%+v", sh)
	}
	if sh.CloseRecord != nil {
		t.Fatalf("失败的结束尝试不应留下结束时记录：%+v", sh.CloseRecord)
	}

	// 班次报告：事项区仍展示当前信息，不标为结束时记录。
	rep, err := f.svc.ShiftReport(w.toShift)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("进行中的班次不应出现结束时记录：%+v", rep)
	}
	if len(rep.Items) != 2 {
		t.Fatalf("事项区应仍展示当前2项事项，got %d", len(rep.Items))
	}
	text := FormatReport(rep)
	if strings.Contains(text, "结束时记录") {
		t.Fatalf("未成功结束的班次不应把事项区标为结束时记录：\n%s", text)
	}

	// 接收来的事项：留在本班，保持接班后修改的内容、严重程度、限制条件与
	// 后续负责人，不被退回上一班，也未被关闭。
	rcv, err := f.svc.GetItem(w.receivedID)
	if err != nil {
		t.Fatalf("get received item: %v", err)
	}
	if rcv.CurrentShiftID != w.toShift {
		t.Fatalf("接收事项不应被退回上一班，所在班次 got %s want %s", rcv.CurrentShiftID, w.toShift)
	}
	if rcv.Content != w.receivedEdit.content || rcv.Severity != w.receivedEdit.severity ||
		rcv.Constraints != w.receivedEdit.constraints || rcv.FollowOwner != w.receivedEdit.follow {
		t.Fatalf("接收事项应保持修改后的当前信息：%+v", rcv)
	}
	if rcv.Closed || rcv.ClosedAt != nil || rcv.CloseOperator != "" {
		t.Fatalf("未关闭事项不应被失败的结束尝试关闭：%+v", rcv)
	}

	// 本班新增且已关闭的事项：保持原样，不被重新打开，关闭人与关闭时间不变。
	own, err := f.svc.GetItem(w.ownID)
	if err != nil {
		t.Fatalf("get own item: %v", err)
	}
	if own.CurrentShiftID != w.toShift {
		t.Fatalf("本班事项所在班次不应改变，got %s", own.CurrentShiftID)
	}
	if !own.Closed || own.CloseOperator != "李四" ||
		own.ClosedAt == nil || !own.ClosedAt.Equal(w.closeItemAt) {
		t.Fatalf("已关闭事项不得被重新打开，关闭人与关闭时间应保持原样：%+v", own)
	}

	// 既有交接：接收结果、处理人、处理时间与完成时间原样保留。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("交接完成状态与完成时间不应受失败的结束尝试影响：%+v", h)
	}
	e := findEntryOf(t, h, w.receivedID)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("交接接收结果、处理人与处理时间应保留：%+v", e)
	}

	// 上一班的结束时记录不受影响，仍展示自己结束时的原始信息。
	repFrom, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	if !repFrom.ItemsAtClose {
		t.Fatalf("上一班的结束时记录应保留")
	}
	snap := findCloseItem(repFrom, w.receivedID)
	if snap == nil || snap.Content != "事项甲" || snap.Severity != SeverityNormal ||
		snap.Constraints != "原限制" || snap.FollowOwner != "李四" || snap.Closed {
		t.Fatalf("上一班结束时记录应保持原始内容与未关闭状态：%+v", snap)
	}
}

// TestCloseShiftSaveFailureAtomicRollback：班次编号正确、仍在进行中、接班交接
// 全部明确接收，本来允许结束，却在本地保存过程中失败——结束操作必须明确返回
// 保存错误，班次保持进行中且不留下结束时记录，事项、交接与上一班的结束时记录
// 原样保留，既有数据文件一个字节不变。保存恢复后仍能在同一会话中修改本班
// 未关闭事项并正常结束，结束时记录以这次成功时的事项信息为准。
func TestCloseShiftSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 上一班新增事项甲（未关闭），随后成功结束并留下自己的结束时记录。
	received, err := f.svc.AddItem(a.ID, "事项甲", SeverityNormal, "原限制", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	f.svc.nowAt(func() time.Time { return tsDay(2, 16, 0) })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	// 发起交接并确认接收，交接完成。
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	confirmAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, received.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got, _ := f.svc.GetHandover(h.ID); !got.Completed() {
		t.Fatalf("前置：全部接收后交接应已完成")
	}

	// 接班后修改未关闭事项的内容、严重程度、限制条件和后续负责人。
	f.svc.nowAt(func() time.Time { return tsDay(2, 18, 0) })
	if _, err := f.svc.UpdateItem(received.ID, "接班后内容", SeverityImportant, "新限制", "王五"); err != nil {
		t.Fatalf("update received: %v", err)
	}
	// 本班新增事项并在结束前关闭，关闭人与关闭时间已保存。
	own, err := f.svc.AddItem(b.ID, "本班新事项", SeverityUrgent, "本班限制", "赵六")
	if err != nil {
		t.Fatalf("add own item: %v", err)
	}
	closeItemAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return closeItemAt })
	if _, err := f.svc.CloseItem(own.ID, "李四"); err != nil {
		t.Fatalf("close own item: %v", err)
	}

	w := closeFailureFacts{
		hID: h.ID, fromShift: a.ID, toShift: b.ID,
		receivedID: received.ID, ownID: own.ID,
		confirmAt: confirmAt, closeItemAt: closeItemAt,
	}
	w.receivedEdit.content, w.receivedEdit.severity = "接班后内容", SeverityImportant
	w.receivedEdit.constraints, w.receivedEdit.follow = "新限制", "王五"

	// 记录失败前已落盘的文件内容，并固定失败尝试的时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 20, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	// 使下一次写盘在原子保存阶段失败，然后尝试结束本班。
	breakSaving(t, f)
	_, err = f.svc.CloseShift(b.ID)
	// 必须明确返回保存错误，而不是业务拒绝（不能用交接未完成、班次已结束
	// 或编号不存在等来冒充）。
	if err == nil {
		t.Fatalf("保存失败时结束操作应返回错误")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrHandoverState) || errors.Is(err, ErrShiftClosed) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("本来允许结束时的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据中不留半完成结果。
	assertCloseFailureRolledBack(t, f, w)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生），
	// 失败的结束尝试不能成为其中的业务事实。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：班次状态、事项、交接与上一班记录与失败前一致。
	f.reopen(t)
	assertCloseFailureRolledBack(t, f, w)

	// 保存条件恢复后，同一会话中仍可修改本班未关闭事项。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return tsDay(2, 21, 0) })
	if _, err := f.svc.UpdateItem(received.ID, "再次修改内容", SeverityUrgent, "再改限制", "钱七"); err != nil {
		t.Fatalf("恢复后应能继续修改本班未关闭事项：%v", err)
	}

	// 再正常结束本班：成功，结束时间属于这次成功操作。
	successAt := tsDay(2, 22, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	closed, err := f.svc.CloseShift(b.ID)
	if err != nil {
		t.Fatalf("恢复后结束班次应成功：%v", err)
	}
	if !closed.Closed || closed.ClosedAt == nil || !closed.ClosedAt.Equal(successAt) {
		t.Fatalf("结束时间应以成功操作时刻 22:00 为准：%+v", closed)
	}

	assertCloseSuccess := func() {
		t.Helper()

		// 结束时记录按成功时的事项信息保存：本班新增且已关闭的事项与
		// 已接收但未关闭的事项各出现一次。
		rep, err := f.svc.ShiftReport(b.ID)
		if err != nil {
			t.Fatalf("report after success: %v", err)
		}
		if !rep.ItemsAtClose {
			t.Fatalf("成功结束后事项区应标为结束时记录")
		}
		if len(rep.CloseItems) != 2 {
			t.Fatalf("结束时记录应含2项，got %d", len(rep.CloseItems))
		}
		seen := map[string]int{}
		for _, s := range rep.CloseItems {
			seen[s.ItemID]++
		}
		if seen[received.ID] != 1 || seen[own.ID] != 1 {
			t.Fatalf("每项事项在结束时记录中应只出现一次：%v", seen)
		}
		// 未关闭项保留成功时修改后的内容、严重程度、限制条件和负责人。
		snapR := findCloseItem(rep, received.ID)
		if snapR == nil || snapR.Content != "再次修改内容" || snapR.Severity != SeverityUrgent ||
			snapR.Constraints != "再改限制" || snapR.FollowOwner != "钱七" || snapR.Closed {
			t.Fatalf("未关闭项应以成功时的修改后信息冻结：%+v", snapR)
		}
		// 已关闭项保留原关闭人和关闭时间。
		snapO := findCloseItem(rep, own.ID)
		if snapO == nil || !snapO.Closed || snapO.CloseOperator != "李四" ||
			snapO.ClosedAt == nil || !snapO.ClosedAt.Equal(closeItemAt) {
			t.Fatalf("已关闭项应保留原关闭人与关闭时间：%+v", snapO)
		}
		text := FormatReport(rep)
		if !strings.Contains(text, "结束时记录") {
			t.Fatalf("成功结束后报告应把事项区标为结束时记录：\n%s", text)
		}

		// 上一班仍展示自己的原始记录，不受本班结束影响。
		repFrom, err := f.svc.ShiftReport(a.ID)
		if err != nil {
			t.Fatalf("report from shift after success: %v", err)
		}
		snap := findCloseItem(repFrom, received.ID)
		if snap == nil || snap.Content != "事项甲" || snap.Severity != SeverityNormal ||
			snap.Constraints != "原限制" || snap.FollowOwner != "李四" || snap.Closed {
			t.Fatalf("上一班结束时记录应保持原始信息：%+v", snap)
		}

		// 既有交接的接收结果与完成时间仍原样保留。
		h2, err := f.svc.GetHandover(h.ID)
		if err != nil {
			t.Fatalf("get handover after success: %v", err)
		}
		if !h2.Completed() || h2.CompletedAt == nil || !h2.CompletedAt.Equal(confirmAt) {
			t.Fatalf("交接完成时间应保持原值：%+v", h2)
		}
	}
	assertCloseSuccess()

	// 重新打开后成功结果同样完整保留。
	f.reopen(t)
	assertCloseSuccess()
}
