package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“首次发起交接在本地数据保存阶段失败”的可重复回归保障：交班班次
// 已经结束、接班班次符合首次发起的现有条件、交班班次此前没有发起交接，业务
// 校验全部通过、真正进入保存过程后本地写盘失败。此时发起操作必须返回原保存
// 错误与零值交接结果，不能把尚未保存的编号、发起时间、清单（空清单交接还
// 包括完成时间）当作发起结果交给调用方；系统内不留下这次交接的任何一部分，
// 失败尝试不占用交接编号，也不留下已指定接班对象的关系。沿用既有保存失败
// 测试的同一套故障注入方式（在 .tmp 路径放目录使写入必然失败）。

// failedCreateExpectations 汇总失败尝试发生前已保存的业务事实，供 reopen
// 前后用同一组期望核对。
type failedCreateExpectations struct {
	fromShift, toShift string
	idA, idB           string // 交班班次结束时的未关闭事项（应进入清单但未保存）
	closedID           string // 交班班次结束前已关闭的事项
	failAt             time.Time
}

// assertZeroHandoverResult 核对发起操作返回的是零值交接结果：编号、岗位和
// 两班编号为空，发起时间为零值，完成时间为空，清单没有事项。
func assertZeroHandoverResult(t *testing.T, got Handover) {
	t.Helper()
	if got.ID != "" || got.Position != "" || got.FromShiftID != "" || got.ToShiftID != "" {
		t.Fatalf("保存失败不得返回未保存的编号、岗位或两班编号：%+v", got)
	}
	if !got.CreatedAt.IsZero() {
		t.Fatalf("保存失败不得返回未保存的发起时间：%+v", got.CreatedAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("保存失败不得返回完成时间（空清单交接同样如此）：%+v", got.CompletedAt)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("保存失败不得返回未保存的清单：%+v", got.Entries)
	}
	if !reflect.DeepEqual(got, Handover{}) {
		t.Fatalf("保存失败应返回零值交接结果：%+v", got)
	}
}

// assertNoHandoverAfterFailedCreate 用当前打开的数据核对：失败的发起没有
// 留下任何交接事实——交接列表与班次报告中都没有为这个交班班次建立交接，
// 事项留在原班次，交班班次的结束时记录保持原样。
func assertNoHandoverAfterFailedCreate(t *testing.T, f *fixture, w failedCreateExpectations) {
	t.Helper()

	// 交接列表中没有为这个交班班次建立的交接；失败尝试不占用的编号查不到记录。
	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("保存失败后不应存在任何交接记录：%+v", hs)
	}
	if _, err := f.svc.GetHandover("H001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败尝试不占用的交接编号应查不到记录，got %v", err)
	}

	// 交班班次报告：没有交班对象，没有各项交接当前结果；结束时记录保持原样
	// （未关闭事项当时未关闭、已关闭事项保留关闭人与关闭时间）。
	repFrom, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	if repFrom.Outgoing != nil {
		t.Fatalf("交班班次报告不应出现交班对象：%+v", repFrom.Outgoing)
	}
	if len(repFrom.Incoming) != 0 || len(repFrom.Results) != 0 {
		t.Fatalf("交班班次报告不应出现任何交接结果：incoming=%+v results=%+v",
			repFrom.Incoming, repFrom.Results)
	}
	if !repFrom.ItemsAtClose {
		t.Fatalf("交班班次原有结束时记录不应受失败尝试影响")
	}
	snapA := findCloseItem(repFrom, w.idA)
	if snapA == nil || snapA.Closed || snapA.Content != "事项甲" || snapA.FollowOwner != "李四" {
		t.Fatalf("交班班次结束时记录应保持原样：%+v", snapA)
	}
	snapClosed := findCloseItem(repFrom, w.closedID)
	if snapClosed == nil || !snapClosed.Closed || snapClosed.CloseOperator != "张三" {
		t.Fatalf("已关闭事项的结束时记录应保持原样：%+v", snapClosed)
	}
	if text := FormatReport(repFrom); !strings.Contains(text, "（尚未发起交接）") {
		t.Fatalf("交班班次报告应显示尚未发起交接：\n%s", text)
	}

	// 接班班次报告：没有接班交接。
	repTo, err := f.svc.ShiftReport(w.toShift)
	if err != nil {
		t.Fatalf("report to shift: %v", err)
	}
	if len(repTo.Incoming) != 0 || repTo.Outgoing != nil || len(repTo.Results) != 0 {
		t.Fatalf("接班班次报告不应出现任何交接：%+v", repTo)
	}

	// 事项留在原班次，不增加流经班次，历史中没有发起交接。
	for _, id := range []string{w.idA, w.idB} {
		it, err := f.svc.GetItem(id)
		if err != nil {
			t.Fatalf("get item %s: %v", id, err)
		}
		if it.CurrentShiftID != w.fromShift || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != w.fromShift {
			t.Fatalf("事项 %s 应留在原班次 %s：%+v", id, w.fromShift, it)
		}
		j, err := f.svc.ItemJourney(id)
		if err != nil {
			t.Fatalf("journey %s: %v", id, err)
		}
		if j.HasHandovers || len(j.Results) != 0 {
			t.Fatalf("事项 %s 不应有任何交接记录：%+v", id, j.Results)
		}
		if got := joinStrings(journeyKinds(j)); got != "created" {
			t.Fatalf("事项 %s 的处理经过应只有建立，没有发起交接，got %s", id, got)
		}
		for _, ev := range j.Events {
			if ev.TimeKnown && ev.At.Equal(w.failAt) {
				t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
			}
		}
		if text := FormatItemJourney(j); !strings.Contains(text, "暂无交接记录") {
			t.Fatalf("事项 %s 的处理经过应显示暂无交接记录：\n%s", id, text)
		}
	}
}

// TestCreateHandoverSaveFailureAtomicRollback：首次发起交接（清单含未关闭
// 事项）时输入与班次状态均合法，实际进入保存阶段后写盘失败——发起操作必须
// 返回原保存错误与零值交接结果；内存与磁盘都回到失败前：没有为这个交班班次
// 建立交接，事项留在原班次，结束时记录与其他数据不受影响，失败尝试不占用
// 交接编号、不留下已指定接班对象的关系。保存恢复后重新发起成功，取得 H001
// 与待处理清单，返回内容与随后的查询一致；重复发起、接班班次结束后重复发起
// 与改换接班对象的既有规则保持不变。
func TestCreateHandoverSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	idA, idB, idC := items[0].ID, items[1].ID, items[2].ID // 事项甲、事项乙（事项丙已关闭）

	// 另一个确实存在的同岗位接班候选，用于验证失败尝试不留下接班对象关系。
	c := mustShift(t, f, "调度", "王五", tsDay(3, 0, 0), tsDay(3, 8, 0), "")

	want := failedCreateExpectations{
		fromShift: a.ID, toShift: b.ID,
		idA: idA, idB: idB, closedID: idC,
		failAt: tsDay(2, 17, 0),
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的发起时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 交班班次已结束、接班班次符合条件、此前没有发起交接；使下一次写盘在
	// 原子保存阶段失败。
	breakSaving(t, f)
	got, err := f.svc.CreateHandover(a.ID, b.ID)
	if err == nil {
		t.Fatalf("保存失败时发起交接应明确返回错误，不能返回新交接记录")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误并保留原错误信息，got %v", err)
	}
	// 输入与班次状态均合法，不能改报成班次不符合条件、已经存在交接或其他
	// 业务拒绝。
	for _, sentinel := range []error{
		ErrInvalidInput, ErrNotFound, ErrShiftClosed, ErrShiftNotClosed,
		ErrHandoverExists, ErrHandoverTarget, ErrHandoverState,
		ErrPositionMismatch, ErrSameShift, ErrOverlap,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("保存失败不应被报告为业务校验错误 %v，got %v", sentinel, err)
		}
	}
	// 调用方不应取得看似已经建立的记录：编号、岗位、两班编号、发起时间、
	// 完成时间与清单都必须是零值。
	assertZeroHandoverResult(t, got)

	// 当前打开的数据上查看，应看到失败前的事实，而不是只保证文件没变、
	// 查询却显示已经建立交接。
	assertNoHandoverAfterFailedCreate(t, f, want)

	// 失败尝试不留下已指定接班对象的关系：保存仍失败时改向另一个确实存在
	// 的接班班次发起，得到的仍是保存失败，而不是“不能改换接班对象”或
	// “已经存在交接”。
	if _, err := f.svc.CreateHandover(a.ID, c.ID); err == nil ||
		errors.Is(err, ErrHandoverTarget) || errors.Is(err, ErrHandoverExists) ||
		!strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("失败尝试不应留下接班对象关系，改向其他班次发起应仍是保存失败，got %v", err)
	}

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的发起不能成为其中的业务事实")
	}

	// 退出后重新打开：仍没有为这个交班班次建立交接。
	f.reopen(t)
	assertNoHandoverAfterFailedCreate(t, f, want)

	// 保存恢复正常后重新发起：按现有功能成功，失败尝试不占用交接编号。
	restoreSaving(t, f)
	successAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("恢复后发起交接应成功：%v", err)
	}
	if h.ID != "H001" {
		t.Fatalf("失败尝试不应占用交接编号，成功发起应取得 H001，got %s", h.ID)
	}
	if h.Position != "调度" || h.FromShiftID != a.ID || h.ToShiftID != b.ID {
		t.Fatalf("成功发起的岗位与两班关系不正确：%+v", h)
	}
	if !h.CreatedAt.Equal(successAt) {
		t.Fatalf("发起时间应以本次成功发起为准：%+v", h.CreatedAt)
	}
	if h.Completed() || h.CompletedAt != nil {
		t.Fatalf("有事项的清单初始为待处理，交接不应完成：%+v", h)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("清单应包含两项未关闭事项，got %d：%+v", len(h.Entries), h.Entries)
	}
	for _, e := range h.Entries {
		if e.ItemID == idC {
			t.Fatalf("已关闭事项不应进入清单：%+v", e)
		}
		if e.Status != EntryPending || e.Operator != "" || e.ProcessedAt != nil {
			t.Fatalf("清单项初始应为待处理、尚未处理：%+v", e)
		}
	}

	// 返回内容应与随后的查询一致。
	queried, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !reflect.DeepEqual(h, queried) {
		t.Fatalf("发起返回内容应与随后查询一致：\n返回 %+v\n查询 %+v", h, queried)
	}

	// 保留已有的重复发起行为：返回原记录及交接已存在错误标识。
	dup, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || dup.ID != h.ID {
		t.Fatalf("重复发起应返回原记录及 ErrHandoverExists，got %v id=%s", err, dup.ID)
	}
	if !reflect.DeepEqual(dup, queried) {
		t.Fatalf("重复发起应返回保存的原记录：\n返回 %+v\n原记录 %+v", dup, queried)
	}

	// 改换接班对象仍按现有规则明确拒绝。
	if _, err := f.svc.CreateHandover(a.ID, c.ID); !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("改换接班对象应报 ErrHandoverTarget，got %v", err)
	}

	// 接班班次后来已经结束也一样：逐项接收后结束接班班次，重复发起仍返回
	// 原记录，不能把原记录清空或重新建立交接。
	f.svc.nowAt(func() time.Time { return tsDay(2, 19, 0) })
	for _, id := range []string{idA, idB} {
		if _, err := f.svc.ProcessEntry(h.ID, id, ActionConfirm, "李四", "", "", ""); err != nil {
			t.Fatalf("confirm %s: %v", id, err)
		}
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("全部接收后接班班次应能结束：%v", err)
	}
	saved, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after receiver closed: %v", err)
	}
	dup2, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || dup2.ID != h.ID {
		t.Fatalf("接班班次结束后重复发起应返回原记录及 ErrHandoverExists，got %v id=%s", err, dup2.ID)
	}
	if !reflect.DeepEqual(dup2, saved) {
		t.Fatalf("接班班次结束后重复发起不应清空或重建原记录：\n返回 %+v\n原记录 %+v", dup2, saved)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != h.ID {
		t.Fatalf("应仍只有成功保存的那一份交接：%+v", hs)
	}
}

// TestCreateHandoverSaveFailureEmptyListAtomicRollback：空清单的首次交接
// 同样遵守保存失败规则——保存失败后返回原保存错误与零值交接结果（完成时间
// 也为空，不能把未保存的“发起时完成”当作结果）；系统内不留下交接。保存
// 恢复后重新发起成功，空清单在发起时完成，返回内容与随后查询一致。
func TestCreateHandoverSaveFailureEmptyListAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("空清单应能结束班次：%v", err)
	}

	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	breakSaving(t, f)
	got, err := f.svc.CreateHandover(a.ID, b.ID)
	if err == nil {
		t.Fatalf("保存失败时空清单发起应明确返回错误，不能返回新交接记录")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误并保留原错误信息，got %v", err)
	}
	for _, sentinel := range []error{
		ErrInvalidInput, ErrNotFound, ErrShiftClosed, ErrShiftNotClosed,
		ErrHandoverExists, ErrHandoverTarget, ErrHandoverState,
		ErrPositionMismatch, ErrSameShift, ErrOverlap,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("保存失败不应被报告为业务校验错误 %v，got %v", sentinel, err)
		}
	}
	// 空清单交接也不能返回未保存的完成时间。
	assertZeroHandoverResult(t, got)

	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("保存失败后不应存在任何交接记录：%+v", hs)
	}
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	if rep.Outgoing != nil {
		t.Fatalf("交班班次报告不应出现交班对象：%+v", rep.Outgoing)
	}
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：仍没有交接。
	f.reopen(t)
	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("重开后仍不应存在交接记录：%+v", hs)
	}

	// 保存恢复后重新发起：空清单在发起时完成，失败尝试不占用编号。
	restoreSaving(t, f)
	successAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("恢复后空清单发起应成功：%v", err)
	}
	if h.ID != "H001" {
		t.Fatalf("失败尝试不应占用交接编号，成功发起应取得 H001，got %s", h.ID)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(successAt) {
		t.Fatalf("空清单应在发起时完成，完成时间以成功发起时刻为准：%+v", h)
	}
	if len(h.Entries) != 0 || !h.CreatedAt.Equal(successAt) {
		t.Fatalf("空清单交接应没有事项，发起时间以成功发起为准：%+v", h)
	}
	queried, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !reflect.DeepEqual(h, queried) {
		t.Fatalf("发起返回内容应与随后查询一致：\n返回 %+v\n查询 %+v", h, queried)
	}

	// 空清单的重复发起同样返回原记录及交接已存在错误标识。
	dup, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || dup.ID != h.ID {
		t.Fatalf("空清单重复发起应返回原记录及 ErrHandoverExists，got %v id=%s", err, dup.ID)
	}
	if !reflect.DeepEqual(dup, queried) {
		t.Fatalf("空清单重复发起应返回保存的原记录：\n返回 %+v\n原记录 %+v", dup, queried)
	}
}
