package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// breakSaving 在数据文件的临时写入路径上放一个目录，使 commit 的临时文件
// 写入必然失败（EISDIR）。这是“真正进入保存过程后”的失败：业务校验已全部
// 通过、内存变更已经发生，失败点在原子写盘阶段；与是否以 root 运行无关，
// 因而测试可在任意机器上独立重复。
func breakSaving(t *testing.T, f *fixture) {
	t.Helper()
	tmp := f.store.Path() + ".tmp"
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("制造保存失败：%v", err)
	}
}

// restoreSaving 移除故障路径，使保存恢复正常。
func restoreSaving(t *testing.T, f *fixture) {
	t.Helper()
	if err := os.RemoveAll(f.store.Path() + ".tmp"); err != nil {
		t.Fatalf("恢复保存：%v", err)
	}
}

// assertLastItemStillPending 用当前打开的数据核对：失败的继续跟踪没有留下
// 任何半完成结果。
func assertLastItemStillPending(t *testing.T, f *fixture, hID, fromShift, toShift, lastID, otherID string, otherProcessedAt time.Time) {
	t.Helper()

	// 交接：最后一项仍为待处理，当前处理人与处理时间显示尚未处理，没有本次
	// 跟踪说明或新负责人，也没有退回轮次；整份交接未完成且无完成时间。
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
	if e.TrackingNote != "" || e.FollowOwner != "李四" {
		t.Fatalf("不应留下本次跟踪说明或新负责人：%+v", e)
	}
	if len(e.Rounds) != 0 {
		t.Fatalf("失败尝试不应产生退回/补充经过：%+v", e.Rounds)
	}
	// 之前已经接收的其他事项及其处理信息保持原样。
	oe := findEntryOf(t, h, otherID)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(otherProcessedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}

	// 事项：仍留在交班班次，流经班次不增加接班班次，负责人不变，
	// 事项历史不增加这次接收。
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
	if it.FollowOwner != "李四" {
		t.Fatalf("事项后续负责人应保持原值，got %s", it.FollowOwner)
	}
	for _, ev := range it.Events {
		if ev.Kind == "received" || strings.Contains(ev.Detail, "接班班次") {
			t.Fatalf("事项处理经过不应增加这次失败的接收：%+v", ev)
		}
	}

	// 处理经过查询同样不出现这次接收，交接当前结果仍为待处理。
	j, err := f.svc.ItemJourney(lastID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	for _, ev := range j.Events {
		if ev.Kind == "track" || ev.Kind == "received" {
			t.Fatalf("处理经过不应出现失败的继续跟踪接收：%+v", ev)
		}
		if ev.TrackingNote != "" {
			t.Fatalf("处理经过不应留下失败尝试的跟踪说明：%+v", ev)
		}
	}

	// 交班班次的结束时事项记录保留原来的内容与负责人。
	rep, err := f.svc.ShiftReport(fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, lastID)
	if snap == nil || snap.FollowOwner != "李四" || snap.Content != "事项乙" {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}

	// 展示层与保存事实一致：未完成、尚未处理、无本次跟踪说明。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if strings.Contains(text, "持续跟踪压力变化") || strings.Contains(text, "王五") {
		t.Fatalf("失败的跟踪说明与新负责人不应出现在交接查询中：\n%s", text)
	}
}

// TestTrackSaveFailureAtomicRollback：接班人对最后一项待处理事项选择继续跟踪，
// 参数合法、状态允许、真正进入保存过程后本地写盘失败——操作必须明确返回保存
// 错误，且交接、事项、班次与处理经过都不留下半完成结果；失败前已保存的数据
// （含交班班次结束时记录）原样保留，重新打开后一致。保存恢复后对同一项再次
// 提交，按现有功能成功接收，处理经过只留下成功的这一次。
func TestTrackSaveFailureAtomicRollback(t *testing.T) {
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
	got, _ := f.svc.GetHandover(h.ID)
	if got.Completed() {
		t.Fatalf("前置：仍有一项待处理，交接不应完成")
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的处理时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	// 使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	failed, err := f.svc.ProcessEntry(h.ID, idLast, ActionTrack, "李四", "", "持续跟踪压力变化", "王五")
	// 操作必须明确返回错误，且这是保存错误而不是业务拒绝
	// （不能用缺少跟踪说明、负责人为空或事项已接收等来冒充）。
	if err == nil {
		t.Fatalf("保存失败时操作应返回错误")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	// 调用方拿不到本次尝试形成的交接结果：没有编号、岗位、两班编号，发起
	// 时间为零值，完成时间为空，清单没有事项——既不能借这份结果宣称最后一项
	// 已接收或整份交接完成，也不能拿失败前的整份交接充当处理结果。
	assertZeroHandoverResult(t, failed)

	// 当前打开的数据中不留半完成结果。
	assertLastItemStillPending(t, f, h.ID, a.ID, b.ID, idLast, idFirst, confirmAt)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：交接进度、事项所在班次和后续负责人与失败前一致。
	f.reopen(t)
	assertLastItemStillPending(t, f, h.ID, a.ID, b.ID, idLast, idFirst, confirmAt)

	// 保存条件恢复后，对同一项再次提交继续跟踪：按现有功能成功接收。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ProcessEntry(h.ID, idLast, ActionTrack, "李四", "", "持续跟踪压力变化", "王五"); err != nil {
		t.Fatalf("恢复后继续跟踪应成功：%v", err)
	}

	// 交接：最后一项只成功接收这一次，处理人与处理时间反映成功操作，
	// 整份交接的完成时间以这次成功处理为准。
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if !h2.Completed() || h2.CompletedAt == nil || !h2.CompletedAt.Equal(successAt) {
		t.Fatalf("完成时间应以成功处理时刻 19:00 为准：%+v", h2)
	}
	le := findEntryOf(t, h2, idLast)
	if le.Status != EntryTracking || le.Operator != "李四" || le.ProcessedAt == nil ||
		!le.ProcessedAt.Equal(successAt) || le.TrackingNote != "持续跟踪压力变化" ||
		le.FollowOwner != "王五" {
		t.Fatalf("最后一项应为本次成功的继续跟踪：%+v", le)
	}
	if len(le.Rounds) != 0 {
		t.Fatalf("失败尝试不应变成历史事实（无退回轮次）：%+v", le.Rounds)
	}

	// 事项：保留编号进入接班班次，流经班次只含交班与接班两班，负责人与历史
	// 只反映成功的这一次接收。
	it, err := f.svc.GetItem(idLast)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.ID != idLast || it.CurrentShiftID != b.ID {
		t.Fatalf("应保留事项编号并进入接班班次：%+v", it)
	}
	if len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != a.ID || it.ShiftIDs[1] != b.ID {
		t.Fatalf("流经班次应只增加一次接班班次：%v", it.ShiftIDs)
	}
	if it.FollowOwner != "王五" {
		t.Fatalf("事项后续负责人应为本次填写的新负责人，got %s", it.FollowOwner)
	}
	kinds := []string{}
	received := 0
	for _, ev := range it.Events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == "received" {
			received++
			if !strings.Contains(ev.Detail, "持续跟踪压力变化") || ev.Operator != "李四" {
				t.Fatalf("接收记录应反映成功操作：%+v", ev)
			}
		}
	}
	if received != 1 || joinStrings(kinds) != "created,received" {
		t.Fatalf("事项历史只应留下成功的这一次接收，got %v", kinds)
	}

	// 处理经过：只展示成功的这一次继续跟踪，失败尝试不在任何事件中。
	j, err := f.svc.ItemJourney(idLast)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,track" {
		t.Fatalf("处理经过应只留下成功的一次接收，got %s", got)
	}
	tracks := 0
	for _, ev := range j.Events {
		if ev.Kind == "track" {
			tracks++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "李四" ||
				ev.TrackingNote != "持续跟踪压力变化" || ev.FollowOwner != "王五" {
				t.Fatalf("成功接收事件内容不正确：%+v", ev)
			}
		}
	}
	if tracks != 1 {
		t.Fatalf("应只有一次继续跟踪记录，got %d", tracks)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryTracking {
		t.Fatalf("交接当前结果应为继续跟踪：%+v", j.Results)
	}

	// 重新打开后成功结果同样完整保留。
	f.reopen(t)
	h3, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	if !h3.Completed() || h3.CompletedAt == nil || !h3.CompletedAt.Equal(successAt) {
		t.Fatalf("重开后交接完成状态与完成时间应保留：%+v", h3)
	}
	it3, err := f.svc.GetItem(idLast)
	if err != nil {
		t.Fatalf("reopen get item: %v", err)
	}
	if it3.CurrentShiftID != b.ID || it3.FollowOwner != "王五" {
		t.Fatalf("重开后事项所在班次与新负责人应保留：%+v", it3)
	}
	j3, err := f.svc.ItemJourney(idLast)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j3)); got != "created,handover-init,track" {
		t.Fatalf("重开后处理经过应只保留成功的一次，got %s", got)
	}
}
