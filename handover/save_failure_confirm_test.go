package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“接班人逐项处理（handover-process 的服务操作 ProcessEntry）在本地
// 数据保存阶段失败时返回什么”补齐可重复回归保障：既有保存失败测试已证明内存
// 与磁盘都会随快照回滚到失败前，但此前没有约束返回给调用方的交接结果——修复
// 前它仍带着本次尚未保存的处理状态、操作人、处理时间甚至交接完成时间，调用方
// 据此展示会误报处理成功。这里专项覆盖“最后一项确认接收原本会让整份交接完成”
// 的场景：保存失败必须返回原保存错误与零值交接结果，不能借返回结果宣称完成；
// 保存恢复后确认成功，才返回包含本次处理事实与完成时间的原交接记录。

// assertLastConfirmStillPending 用当前打开的数据核对：最后一项失败的确认接收
// 没有留下任何半完成结果（事项仍待处理、交接未完成、事项未移动）。
func assertLastConfirmStillPending(t *testing.T, f *fixture, hID, fromShift, toShift, lastID, otherID string, otherProcessedAt time.Time) {
	t.Helper()

	// 交接：最后一项仍为待处理，当前处理人与处理时间显示尚未处理；整份交接
	// 未完成且无完成时间，失败尝试没有把完成时间写进记录。
	h, err := f.svc.GetHandover(hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if h.Completed() || h.CompletedAt != nil {
		t.Fatalf("保存失败后整份交接应继续未完成、不能留下完成时间：%+v", h)
	}
	e := findEntryOf(t, h, lastID)
	if e.Status != EntryPending {
		t.Fatalf("最后一项应仍为待处理，got %s", e.Status)
	}
	if e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("最后一项的当前处理人与处理时间应仍为尚未处理：%+v", e)
	}
	if len(e.Rounds) != 0 || e.TrackingNote != "" {
		t.Fatalf("失败的确认不应产生退回经过或跟踪说明：%+v", e)
	}
	// 之前已经接收的其他事项及其处理信息保持原样。
	oe := findEntryOf(t, h, otherID)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(otherProcessedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}

	// 事项：仍留在交班班次，流经班次不增加接班班次，历史不增加这次接收。
	it, err := f.svc.GetItem(lastID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != fromShift {
		t.Fatalf("事项应仍留在交班班次 %s，got %s", fromShift, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != fromShift {
		t.Fatalf("流经班次不应增加接班班次：%v", it.ShiftIDs)
	}
	for _, ev := range it.Events {
		if ev.Kind == "received" {
			t.Fatalf("事项处理经过不应增加这次失败的接收：%+v", ev)
		}
	}

	// 处理经过查询同样不出现这次确认接收，交接当前结果仍为待处理。
	j, err := f.svc.ItemJourney(lastID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init" {
		t.Fatalf("处理经过应只保留失败前事实（建立、发起交接），got %s", got)
	}

	// 交班班次的结束时事项记录保留原来的内容与负责人。
	rep, err := f.svc.ShiftReport(fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, lastID)
	if snap == nil || snap.FollowOwner != "李四" || snap.Content != "事项乙" || snap.Closed {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}

	// 展示层与保存事实一致：未完成、尚未处理。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if strings.Contains(text, "已完成") {
		t.Fatalf("失败的最后一项不能让交接展示成已完成：\n%s", text)
	}
}

// TestConfirmLastItemSaveFailureReturnsZeroResult：另一项已确认接收、本次确认
// 的是交接中最后一项（原本会让整份交接完成），输入合法、状态允许且真正进入
// 保存过程后本地写盘失败——操作必须返回原保存错误与零值交接结果：没有交接
// 编号、岗位或两班编号，发起时间为零值，完成时间为空，清单没有事项；不能返回
// 本次尝试形成的确认结果与完成时间，也不能返回失败前的整份交接充当处理结果。
// 内存与磁盘都回到失败前：最后一项仍待处理、接班人尚未处理，事项留在交班班次，
// 交班班次结束时记录不变，已接收的另一项及其历史保留原样。保存恢复后再次确认
// 成功，才以那次成功处理时间记录交接完成，返回内容与随后查询一致。
func TestConfirmLastItemSaveFailureReturnsZeroResult(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idFirst, idLast := items[0].ID, items[1].ID // 事项甲、事项乙（事项丙已关闭）

	// 其中一项已经成功接收；最后一项仍待处理且属于交班班次。
	confirmAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, idFirst, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm first: %v", err)
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的处理时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	// 最后一项确认接收会让整份交接首次全部接收；使下一次写盘在保存阶段失败。
	breakSaving(t, f)
	failed, err := f.svc.ProcessEntry(h.ID, idLast, ActionConfirm, "李四", "", "", "")
	if err == nil {
		t.Fatalf("保存失败时操作应返回错误")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	// 不能把本次尚未保存的确认结果、完成时间或失败前的整份交接当作处理结果。
	assertZeroHandoverResult(t, failed)

	// 当前打开的数据不留半完成结果。
	assertLastConfirmStillPending(t, f, h.ID, a.ID, b.ID, idLast, idFirst, confirmAt)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：交接进度、事项所在班次与失败前一致，仍查不到完成时间。
	f.reopen(t)
	assertLastConfirmStillPending(t, f, h.ID, a.ID, b.ID, idLast, idFirst, confirmAt)

	// 保存条件恢复后，对最后一项再次确认：按现有功能成功接收并完成整份交接。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	result, err := f.svc.ProcessEntry(h.ID, idLast, ActionConfirm, "李四", "", "", "")
	if err != nil {
		t.Fatalf("恢复后确认接收应成功：%v", err)
	}

	// 成功时返回包含本次处理事实的原交接记录，完成时间以本次成功处理为准。
	if result.ID != h.ID || result.Position != "调度" ||
		result.FromShiftID != a.ID || result.ToShiftID != b.ID {
		t.Fatalf("成功结果应是原交接记录：%+v", result)
	}
	if !result.Completed() || result.CompletedAt == nil || !result.CompletedAt.Equal(successAt) {
		t.Fatalf("最后一项成功接收才记录完成时间，应以 19:00 为准：%+v", result)
	}
	le := findEntryOf(t, result, idLast)
	if le.Status != EntryConfirmed || le.Operator != "李四" || le.ProcessedAt == nil ||
		!le.ProcessedAt.Equal(successAt) {
		t.Fatalf("最后一项应为本次成功的确认接收：%+v", le)
	}
	fe := findEntryOf(t, result, idFirst)
	if fe.Status != EntryConfirmed || fe.Operator != "李四" ||
		fe.ProcessedAt == nil || !fe.ProcessedAt.Equal(confirmAt) {
		t.Fatalf("已接收的其他事项及其历史应保留原样：%+v", fe)
	}

	// 返回内容与再次查询到的保存记录一致。
	queried, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !reflect.DeepEqual(result, queried) {
		t.Fatalf("成功返回内容应与随后查询一致：\n返回 %+v\n查询 %+v", result, queried)
	}

	// 事项保留编号进入接班班次，处理经过只留下成功的这一次确认接收。
	it, err := f.svc.GetItem(idLast)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != b.ID || len(it.ShiftIDs) != 2 {
		t.Fatalf("应保留事项编号并进入接班班次：%+v", it)
	}
	j, err := f.svc.ItemJourney(idLast)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm" {
		t.Fatalf("处理经过应只留下成功的一次确认接收，got %s", got)
	}

	// 重新打开后成功结果完整保留。
	f.reopen(t)
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	if !h2.Completed() || h2.CompletedAt == nil || !h2.CompletedAt.Equal(successAt) {
		t.Fatalf("重开后交接完成状态与完成时间应保留：%+v", h2)
	}
}

// TestProcessEntryRejectionsReturnZeroResult：保存之前的业务拒绝（输入不合法、
// 编号不存在、状态不允许、必填缺失）同样不返回任何交接结果。既有规则与错误
// 标识保持不变，这里只补充“返回值为零值交接”的承诺，避免调用方把拒绝前查到
// 的交接当作处理结果展示。
func TestProcessEntryRejectionsReturnZeroResult(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	// 先成功退回一项并由另一人确认另一项，制造“已退回待补充”和“已接收”两种状态。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	try := func(name string, fn func() (Handover, error), want error) {
		t.Helper()
		got, err := fn()
		if !errors.Is(err, want) {
			t.Fatalf("%s：应返回 %v，got %v", name, want, err)
		}
		assertZeroHandoverResult(t, got)
	}

	try("未知操作", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, EntryAction("bogus"), "李四", "", "", "")
	}, ErrInvalidInput)
	try("操作人为空", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "  ", "", "", "")
	}, ErrInvalidInput)
	try("交接不存在", func() (Handover, error) {
		return f.svc.ProcessEntry("H999", idA, ActionConfirm, "李四", "", "", "")
	}, ErrNotFound)
	try("清单外事项", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, "I999", ActionConfirm, "李四", "", "", "")
	}, ErrNotFound)
	try("已接收项不能再处理", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", "")
	}, ErrHandoverState)
	try("退回项未重新提交不能处理", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", "")
	}, ErrHandoverState)

	// 重新提交后再制造待处理项，覆盖退回原因、跟踪说明与后续负责人的必填拒绝。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充内容"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	try("退回原因为空", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "   ", "", "")
	}, ErrInvalidInput)
	try("跟踪说明为空", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "   ", "王五")
	}, ErrInvalidInput)
	try("跟踪后续负责人为空", func() (Handover, error) {
		return f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "跟踪说明", "  ")
	}, ErrInvalidInput)

	// 全部拒绝都不改变保存事实：idA 仍待处理、idB 仍确认，交接仍未完成。
	got, _ := f.svc.GetHandover(h.ID)
	if got.Completed() || got.CompletedAt != nil {
		t.Fatalf("业务拒绝后交接应仍未完成：%+v", got)
	}
	ea := findEntryOf(t, got, idA)
	eb := findEntryOf(t, got, idB)
	if ea.Status != EntryPending || ea.Operator != "" || ea.ProcessedAt != nil {
		t.Fatalf("被拒绝处理的事项应仍为待处理、尚未处理：%+v", ea)
	}
	if eb.Status != EntryConfirmed {
		t.Fatalf("已接收事项不应受拒绝影响：%+v", eb)
	}
}
