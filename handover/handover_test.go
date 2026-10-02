package handover

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type fixture struct {
	svc   *Service
	store *Store
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := NewService(store)
	f := &fixture{
		svc:   svc,
		store: store,
		clock: time.Date(2026, 10, 2, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)),
	}
	svc.nowAt(func() time.Time {
		f.clock = f.clock.Add(time.Minute)
		return f.clock
	})
	return f
}

// reopen 模拟退出后重新打开，数据与处理进度必须仍在。
func (f *fixture) reopen(t *testing.T) {
	t.Helper()
	store, err := Open(f.store.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	svc := NewService(store)
	svc.nowAt(f.svc.now)
	f.store = store
	f.svc = svc
}

func tsDay(day, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, time.FixedZone("CST", 8*3600))
}

func ts(hour, min int) time.Time { return tsDay(2, hour, min) }

func mustShift(t *testing.T, f *fixture, position, owner string, s, e time.Time, note string) Shift {
	t.Helper()
	sh, err := f.svc.CreateShift(position, owner, s, e, note)
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	return sh
}

func TestCreateShiftValidation(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateShift("   ", "张三", ts(8, 0), ts(16, 0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白岗位应报错， got %v", err)
	}
	if _, err := f.svc.CreateShift("调度", "  ", ts(8, 0), ts(16, 0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白负责人应报错，got %v", err)
	}
	if _, err := f.svc.CreateShift("调度", "张三", ts(16, 0), ts(8, 0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("结束早于开始应报错，got %v", err)
	}
	if _, err := f.svc.CreateShift("调度", "张三", ts(8, 0), ts(8, 0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("结束等于开始应报错，got %v", err)
	}

	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	b := mustShift(t, f, "调度", "李四", ts(16, 0), ts(23, 0), "")
	if a.ID != "S001" || b.ID != "S002" {
		t.Fatalf("稳定编号错误：%s %s", a.ID, b.ID)
	}
}

func TestOverlapEndpointAndPositions(t *testing.T) {
	f := newFixture(t)
	mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")

	// 重叠但没有说明：报错。
	if _, err := f.svc.CreateShift("调度", "王五", ts(15, 0), ts(22, 0), "   "); !errors.Is(err, ErrOverlap) {
		t.Fatalf("重叠无说明应报 ErrOverlap，got %v", err)
	}

	// 重叠且提供说明：成功并保存说明。
	overlap, err := f.svc.CreateShift("调度", "王五", ts(15, 0), ts(22, 0), "突发抢修，两班并行一小时")
	if err != nil {
		t.Fatalf("重叠带说明应成功，got %v", err)
	}
	notes := f.svc.OverlapNotes(overlap.ID)
	if len(notes) != 1 || notes[0].Note != "突发抢修，两班并行一小时" {
		t.Fatalf("应能查看说明与涉及班次，got %+v", notes)
	}
	if !((notes[0].ShiftA == "S001" && notes[0].ShiftB == overlap.ID) ||
		(notes[0].ShiftA == overlap.ID && notes[0].ShiftB == "S001")) {
		t.Fatalf("说明应涉及两个重叠班次，got %+v", notes[0])
	}

	// 不同岗位同时间段互不影响。
	if _, err := f.svc.CreateShift("巡检", "赵六", ts(8, 30), ts(15, 30), ""); err != nil {
		t.Fatalf("不同岗位不应判定重叠：%v", err)
	}
}

func TestAddOverlapNoteValidation(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	b := mustShift(t, f, "调度", "李四", ts(16, 0), ts(23, 0), "")
	other := mustShift(t, f, "巡检", "赵六", ts(8, 0), ts(16, 0), "")

	if _, err := f.svc.AddOverlapNote(a.ID, b.ID, "相邻"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("不重叠班次（端点相接）应拒绝说明，got %v", err)
	}
	if _, err := f.svc.AddOverlapNote(a.ID, other.ID, "跨岗位"); !errors.Is(err, ErrPositionMismatch) {
		t.Fatalf("不同岗位应拒绝，got %v", err)
	}
	if _, err := f.svc.AddOverlapNote(a.ID, "S999", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在编号应 ErrNotFound，got %v", err)
	}
	if _, err := f.svc.AddOverlapNote(a.ID, a.ID, "自身"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("同一班次应拒绝，got %v", err)
	}
}

func TestItemLifecycleAndClosedShiftImmutability(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")

	if _, err := f.svc.AddItem(a.ID, "  ", SeverityNormal, "", "李四"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白内容应报错，got %v", err)
	}
	if _, err := f.svc.AddItem(a.ID, "跟进1号机", Severity("bad"), "", "李四"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("非法严重程度应报错，got %v", err)
	}
	if _, err := f.svc.AddItem(a.ID, "跟进1号机", SeverityNormal, "", "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白后续负责人应报错，got %v", err)
	}

	it, err := f.svc.AddItem(a.ID, "  跟进1号机检修 ", SeverityImportant, "  需停电窗口 ", " 李四 ")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	if it.ID != "I001" {
		t.Fatalf("事项稳定编号应为 I001，got %s", it.ID)
	}
	if it.Content != "跟进1号机检修" || it.Constraints != "需停电窗口" || it.FollowOwner != "李四" {
		t.Fatalf("字段应去掉首尾空白：%+v", it)
	}

	// 限制条件可以为空。
	if _, err := f.svc.AddItem(a.ID, "巡检记录归档", SeverityNormal, "", "王五"); err != nil {
		t.Fatalf("限制条件应可为空：%v", err)
	}

	if _, err := f.svc.UpdateItem(it.ID, "新内容", SeverityUrgent, "", "赵六"); err != nil {
		t.Fatalf("结束前应可修改：%v", err)
	}
	if _, err := f.svc.CloseItem(it.ID, "张三"); err != nil {
		t.Fatalf("结束前应可关闭：%v", err)
	}
	if _, err := f.svc.CloseItem(it.ID, "张三"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重复关闭应报错，got %v", err)
	}
	if _, err := f.svc.CloseItem("I999", "张三"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在事项应 ErrNotFound，got %v", err)
	}

	// 结束班次。
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	// 已结束班次不能新增、修改、关闭事项。
	if _, err := f.svc.AddItem(a.ID, "x", SeverityNormal, "", "y"); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("已结束班次新增应报错，got %v", err)
	}
	if _, err := f.svc.UpdateItem(it.ID, "篡改", SeverityNormal, "", "y"); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("已结束班次事项内容不能修改，got %v", err)
	}
	if _, err := f.svc.CloseItem("I002", "张三"); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("已结束班次事项关闭状态不能修改，got %v", err)
	}

	// 已关闭事项仍保留在查询结果中。
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.Items) != 2 {
		t.Fatalf("已关闭事项也应保留，期望2项，got %d", len(rep.Items))
	}
	foundClosed := false
	for _, x := range rep.Items {
		if x.ID == it.ID && x.Closed {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Fatalf("查询应展示关闭情况")
	}
}

func TestEmptyListCanCloseAndHandoverCompletes(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 空清单也能结束班次。
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("空清单应能结束班次：%v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("空清单交接：%v", err)
	}
	if !h.Completed() {
		t.Fatalf("空清单交接应直接完成")
	}
}

// 构建一个交班班次（含若干未关闭事项）并结束，返回接班班次。
func prepareHandover(t *testing.T, f *fixture) (Shift, Shift, []Item) {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	var items []Item
	for _, c := range []string{"事项甲", "事项乙", "事项丙"} {
		it, err := f.svc.AddItem(a.ID, c, SeverityNormal, "", "李四")
		if err != nil {
			t.Fatalf("add item: %v", err)
		}
		items = append(items, it)
	}
	// 关闭一项，验证只有未关闭事项进入清单。
	if _, err := f.svc.CloseItem(items[2].ID, "张三"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	return a, b, items
}

func TestHandoverCreationRules(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)

	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	if h.ID != "H001" {
		t.Fatalf("交接稳定编号应为 H001，got %s", h.ID)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("只有未关闭事项进入待交接清单，期望2项，got %d", len(h.Entries))
	}
	for _, e := range h.Entries {
		if e.ItemID == items[2].ID {
			t.Fatalf("已关闭事项不应进入清单")
		}
		if e.Status != EntryPending {
			t.Fatalf("初始应为待处理")
		}
	}

	// 重复发起同一对象：返回已有记录。
	h2, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || h2.ID != h.ID {
		t.Fatalf("重复发起应返回已有记录 ErrHandoverExists，got %v id=%s", err, h2.ID)
	}

	// 改换对象：报错。
	c := mustShift(t, f, "调度", "王五", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	if _, err := f.svc.CreateHandover(a.ID, c.ID); !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("改换接班对象应报错，got %v", err)
	}

	// 不能交给自身。
	if _, err := f.svc.CreateHandover(b.ID, b.ID); !errors.Is(err, ErrSameShift) {
		// b 未结束，先命中自身校验。
		t.Fatalf("交给自身应报错，got %v", err)
	}
	// 不能交给其他岗位。
	other := mustShift(t, f, "巡检", "赵六", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	if _, err := f.svc.CreateHandover(a.ID, other.ID); !errors.Is(err, ErrPositionMismatch) {
		t.Fatalf("其他岗位应报错，got %v", err)
	}
	// 接班班次必须尚未结束：用一个没有接班交接的班次 d，先结束再作为接班对象。
	d := mustShift(t, f, "调度", "钱七", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	if _, err := f.svc.CloseShift(d.ID); err != nil {
		t.Fatalf("close d: %v", err)
	}
	if _, err := f.svc.CreateHandover(c.ID, d.ID); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("已结束的接班班次应报错，got %v", err)
	}
	// 交班班次未结束不能发起。
	if _, err := f.svc.CreateHandover(c.ID, "S999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在接班班次应 ErrNotFound，got %v", err)
	}
	// 交班班次未结束时即使接班班次合法也不能发起。
	open := mustShift(t, f, "调度", "孙八", tsDay(3, 16, 0), tsDay(4, 0, 0), "")
	if _, err := f.svc.CreateHandover(c.ID, open.ID); !errors.Is(err, ErrShiftNotClosed) {
		t.Fatalf("交班班次未结束应报 ErrShiftNotClosed，got %v", err)
	}
}

func TestHandoverStartConstraintAndOverlapNote(t *testing.T) {
	f := newFixture(t)
	// 交班班次 16:00-23:00；接班班次开始时间早于交班开始、但不重叠：10:00-16:00（端点相接）。
	a := mustShift(t, f, "调度", "张三", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	early := mustShift(t, f, "调度", "李四", tsDay(2, 10, 0), tsDay(2, 16, 0), "")
	if _, err := f.svc.AddItem(a.ID, "x", SeverityNormal, "", "y"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.svc.CreateHandover(a.ID, early.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("接班开始早于交班开始应报错，got %v", err)
	}

	// 重叠但有说明时允许交接。
	f2 := newFixture(t)
	x := mustShift(t, f2, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	y := mustShift(t, f2, "调度", "李四", tsDay(2, 9, 0), tsDay(2, 17, 0), "抢修并行")
	if _, err := f2.svc.AddItem(x.ID, "x", SeverityNormal, "", "y"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f2.svc.CloseShift(x.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f2.svc.CreateHandover(x.ID, y.ID); err != nil {
		t.Fatalf("双方重叠但已有说明时应允许交接：%v", err)
	}
}

func TestProcessEntries(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	idA, idB := items[0].ID, items[1].ID

	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "  ", "", "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("操作人必填，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "  ", "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("退回原因必填，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "  ", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("跟踪说明必填，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "继续跟", " "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("跟踪后续负责人必填，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, "I999", ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清单外事项应 ErrNotFound，got %v", err)
	}
	if _, err := f.svc.ProcessEntry("H999", idA, ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在交接应 ErrNotFound，got %v", err)
	}

	// 确认接收第一项。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, " 李四 ", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got, _ := f.svc.GetHandover(h.ID)
	var ea *HandoverEntry
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if ea.Status != EntryConfirmed || ea.Operator != "李四" || ea.ProcessedAt == nil {
		t.Fatalf("确认结果/操作人/处理时间不正确：%+v", ea)
	}
	// 已接收项不能再次退回。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "反悔", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已接收项不能退回，got %v", err)
	}
	// 未处理项使交接保持未完成。
	if got.Completed() {
		t.Fatalf("仍有待处理项时不应完成")
	}

	// 第二项继续跟踪。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "需要持续观察压力", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() || got.CompletedAt == nil {
		t.Fatalf("全部确认或继续跟踪后应显示完成")
	}

	// 两项都进入接班班次的未关闭清单，保留原编号与历史。
	rep, _ := f.svc.ShiftReport(b.ID)
	ids := map[string]bool{}
	for _, it := range rep.Items {
		ids[it.ID] = true
		if it.Closed {
			t.Fatalf("接收后应为未关闭：%s", it.ID)
		}
	}
	if !ids[idA] || !ids[idB] {
		t.Fatalf("确认与继续跟踪事项都应进入接班班次未关闭清单，got %v", ids)
	}
	itA, _ := f.svc.GetItem(idA)
	if itA.CurrentShiftID != b.ID || itA.OriginShiftID != a.ID {
		t.Fatalf("应保留原编号与原始班次，流转到接班班次：%+v", itA)
	}
	itB, _ := f.svc.GetItem(idB)
	if itB.FollowOwner != "王五" {
		t.Fatalf("继续跟踪应更新后续负责人")
	}

	// 全部完成后接班班次可以结束。
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("交接完成后应能结束接班班次：%v", err)
	}
}

func TestReturnAndResubmit(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	// 退回第一项，交接保持未完成；退回项不进入接班清单。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "信息不全，需补充图纸", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	got, _ := f.svc.GetHandover(h.ID)
	if got.Completed() {
		t.Fatalf("存在退回项时交接不应完成")
	}
	itA, _ := f.svc.GetItem(idA)
	if itA.CurrentShiftID != a.ID {
		t.Fatalf("退回项不应流转到接班班次")
	}

	// 接班班次存在退回项时不能结束。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("仍有退回交接项时接班班次不能结束，got %v", err)
	}

	// 交班人补充说明不能为空。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("补充说明必须非空，got %v", err)
	}
	// 非退回项不能重新提交。
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已确认项不能重新提交，got %v", err)
	}
	// 补充并重新提交：只有该项恢复待处理，既有接收结果不变。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, " 张三 ", "图纸编号已补到资料柜B层"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	var ea, eb *HandoverEntry
	for i := range got.Entries {
		switch got.Entries[i].ItemID {
		case idA:
			ea = &got.Entries[i]
		case idB:
			eb = &got.Entries[i]
		}
	}
	if ea.Status != EntryPending {
		t.Fatalf("重新提交后该项应恢复待处理，got %s", ea.Status)
	}
	if eb.Status != EntryConfirmed {
		t.Fatalf("既有接收结果不应改变，got %s", eb.Status)
	}
	if got.Completed() {
		t.Fatalf("恢复待处理后交接应再次未完成")
	}
	r := ea.Rounds[len(ea.Rounds)-1]
	if r.Reason != "信息不全，需补充图纸" || r.Supplement != "图纸编号已补到资料柜B层" {
		t.Fatalf("原文退回原因与补充说明都应保留：%+v", r)
	}
	if r.SupplementOperator != "张三" || r.ResubmittedAt == nil {
		t.Fatalf("应记录补充操作人与重新提交时间：%+v", r)
	}

	// 重新提交后再次退回，产生第 2 轮历史；再确认接收。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "仍缺签字", "", ""); err != nil {
		t.Fatalf("second return: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if len(ea.Rounds) != 2 {
		t.Fatalf("应保留历次退回记录，期望2轮，got %d", len(ea.Rounds))
	}
	if ea.Rounds[0].Reason != "信息不全，需补充图纸" {
		t.Fatalf("第一轮退回原因不得覆盖：%+v", ea.Rounds[0])
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "签字已补齐"); err != nil {
		t.Fatalf("resubmit2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("final confirm: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() {
		t.Fatalf("最终应完成")
	}
}

func TestChainedHandoverKeepsIDAndHistory(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h1, _ := f.svc.CreateHandover(a.ID, b.ID)
	if _, err := f.svc.ProcessEntry(h1.ID, items[0].ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, items[1].ID, ActionTrack, "李四", "", "继续盯", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	c := mustShift(t, f, "调度", "赵六", tsDay(2, 23, 0), tsDay(3, 7, 0), "")

	// 接收项未在 b 关闭，随 b 再次交接，保留原编号和历史。
	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("chain handover: %v", err)
	}
	if len(h2.Entries) != 2 {
		t.Fatalf("未关闭接收项应随班再次交接，got %d 项", len(h2.Entries))
	}
	for _, e := range h2.Entries {
		if e.ItemID != items[0].ID && e.ItemID != items[1].ID {
			t.Fatalf("必须保留原事项编号：%s", e.ItemID)
		}
	}
	rep, err := f.svc.ShiftReport(c.ID)
	if err != nil {
		t.Fatalf("report c: %v", err)
	}
	if len(rep.Results[items[0].ID]) != 1 {
		t.Fatalf("接班查询应展示该项当前结果")
	}

	// 报告中交班班次应能看到两次交接历史。
	ra, _ := f.svc.ShiftReport(a.ID)
	if ra.Outgoing == nil || ra.Outgoing.ID != h1.ID {
		t.Fatalf("应展示接班对象")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	if _, err := f.svc.ProcessEntry(h.ID, items[0].ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, items[1].ID, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}

	f.reopen(t)

	sh, err := f.svc.GetShift(a.ID)
	if err != nil || !sh.Closed {
		t.Fatalf("重开后班次状态应保留：%v", err)
	}
	got, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("重开后交接记录应保留：%v", err)
	}
	if got.Completed() {
		t.Fatalf("重开后处理进度应保留（仍有退回项，未完成）")
	}
	status := map[string]EntryStatus{}
	for _, e := range got.Entries {
		status[e.ItemID] = e.Status
	}
	if status[items[0].ID] != EntryConfirmed || status[items[1].ID] != EntryReturned {
		t.Fatalf("重开后每项结果应保留：%v", status)
	}
	// 重开后继续办理。
	if _, err := f.svc.ResubmitReturned(h.ID, items[1].ID, "张三", "补充材料"); err != nil {
		t.Fatalf("重开后应能继续办理：%v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, items[1].ID, ActionTrack, "李四", "", "跟进到底", "王五"); err != nil {
		t.Fatalf("重开后继续处理：%v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() {
		t.Fatalf("重开后续办应能完成")
	}
}

func TestFailedValidationDoesNotMutate(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	it, err := f.svc.AddItem(a.ID, "原始内容", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// 非法修改（空白内容）必须失败，且原内容不变。
	if _, err := f.svc.UpdateItem(it.ID, "   ", SeverityUrgent, "", ""); err == nil {
		t.Fatalf("非法修改应失败")
	}
	got, _ := f.svc.GetItem(it.ID)
	if got.Content != "原始内容" || got.Severity != SeverityNormal || got.FollowOwner != "李四" {
		t.Fatalf("失败前已保存的数据应保持不变：%+v", got)
	}

	// 建班次失败（结束早于开始）不得占用编号。
	if _, err := f.svc.CreateShift("调度", "x", ts(16, 0), ts(8, 0), ""); err == nil {
		t.Fatalf("应失败")
	}
	next := mustShift(t, f, "调度", "李四", ts(16, 0), ts(23, 0), "")
	if next.ID != "S002" {
		t.Fatalf("失败操作不应消耗稳定编号，got %s", next.ID)
	}
}

func TestParseHelpers(t *testing.T) {
	if v, err := ParseSeverity("紧急"); err != nil || v != SeverityUrgent {
		t.Fatalf("中文严重程度解析：%v %v", v, err)
	}
	if _, err := ParseSeverity("xxx"); err == nil {
		t.Fatalf("非法严重程度应报错")
	}
	if a, err := ParseAction("退回"); err != nil || a != ActionReturn {
		t.Fatalf("中文操作解析：%v %v", a, err)
	}
	if _, err := ParseAction("xxx"); err == nil {
		t.Fatalf("非法操作应报错")
	}
	if !EntryTracking.Received() || EntryReturned.Received() {
		t.Fatalf("Received 判定错误")
	}
	if fmt.Sprint(SeverityUrgent.Label()) != "紧急" {
		t.Fatalf("label")
	}
}
