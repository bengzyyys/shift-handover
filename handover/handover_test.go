package handover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// 已关闭事项仍保留在结束时记录中。
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose {
		t.Fatalf("已结束班次应展示结束时记录")
	}
	if len(rep.CloseItems) != 2 {
		t.Fatalf("已关闭事项也应保留在结束时记录中，期望2项，got %d", len(rep.CloseItems))
	}
	foundClosed := false
	for _, x := range rep.CloseItems {
		if x.ItemID == it.ID && x.Closed {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Fatalf("结束时记录应展示关闭情况")
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

// findCloseItem 在结束时记录中查找指定事项的快照。
func findCloseItem(rep ShiftReport, itemID string) *CloseItemSnapshot {
	for i := range rep.CloseItems {
		if rep.CloseItems[i].ItemID == itemID {
			return &rep.CloseItems[i]
		}
	}
	return nil
}

// TestClosedShiftRecordFreezesItems：事项被后一班接收后，后续修改内容、调整
// 负责人或关闭，都不改变已结束班次报告里的结束时记录；结束前已关闭的事项
// 保留关闭人与关闭时间；item-show 仍展示最新状态。
func TestClosedShiftRecordFreezesItems(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	it, err := f.svc.AddItem(a.ID, "原始内容", SeverityImportant, "原限制", "原负责人")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	it2, err := f.svc.AddItem(a.ID, "第二项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseItem(it2.ID, "钱七"); err != nil {
		t.Fatalf("close before shift end: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close shift: %v", err)
	}

	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, it.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// 接班后修改内容、严重程度、限制条件、负责人，然后关闭。
	if _, err := f.svc.UpdateItem(it.ID, "接班改后内容", SeverityUrgent, "新限制", "新负责人"); err != nil {
		t.Fatalf("update after receive: %v", err)
	}
	if _, err := f.svc.CloseItem(it.ID, "李四"); err != nil {
		t.Fatalf("close after receive: %v", err)
	}

	ra, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	if !ra.ItemsAtClose {
		t.Fatalf("已结束班次应展示结束时记录")
	}
	if len(ra.CloseItems) != 2 {
		t.Fatalf("结束时清单应含2项（含结束前已关闭的），got %d", len(ra.CloseItems))
	}
	snap := findCloseItem(ra, it.ID)
	if snap == nil {
		t.Fatalf("结束时记录缺少事项 %s", it.ID)
	}
	if snap.Content != "原始内容" || snap.Severity != SeverityImportant ||
		snap.Constraints != "原限制" || snap.FollowOwner != "原负责人" {
		t.Fatalf("结束时记录不得被后班修改改变：%+v", snap)
	}
	if snap.Closed {
		t.Fatalf("结束时未关闭的事项不能因后班关闭而显示成当时已关闭")
	}
	snap2 := findCloseItem(ra, it2.ID)
	if snap2 == nil || !snap2.Closed || snap2.CloseOperator != "钱七" || snap2.ClosedAt == nil {
		t.Fatalf("结束前已关闭事项应保留关闭人与关闭时间：%+v", snap2)
	}
	latest := ra.LatestItems[it.ID]
	if latest.CurrentShiftID != b.ID || latest.FollowOwner != "新负责人" || !latest.Closed {
		t.Fatalf("最新状态应与结束时信息明确区分：%+v", latest)
	}

	// item-show 继续展示事项最新状态。
	cur, err := f.svc.GetItem(it.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if cur.Content != "接班改后内容" || cur.Severity != SeverityUrgent ||
		cur.Constraints != "新限制" || cur.FollowOwner != "新负责人" || !cur.Closed {
		t.Fatalf("item-show 应展示最新状态：%+v", cur)
	}

	// 渲染文本应标明“结束时记录”并区分“最新状态”。
	text := handoverReportText(t, f.svc, a.ID)
	if !strings.Contains(text, "结束时记录") {
		t.Fatalf("报告应标明结束时记录")
	}
	if !strings.Contains(text, "最新状态") {
		t.Fatalf("报告应展示最新状态对照")
	}
}

// handoverReportText 生成班次报告文本。
func handoverReportText(t *testing.T, svc *Service, shiftID string) string {
	t.Helper()
	rep, err := svc.ShiftReport(shiftID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	return FormatReport(rep)
}

// TestChainedShiftsEachKeepOwnRecord：同一事项连续经过几个班次，编号与原始
// 班次不变，各班分别保留自己的结束时记录。
func TestChainedShiftsEachKeepOwnRecord(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	it, err := f.svc.AddItem(a.ID, "事项", SeverityNormal, "甲留下的限制", "甲负责人")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h1, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("handover a->b: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, it.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// 接班修改限制，再结束。
	if _, err := f.svc.UpdateItem(it.ID, "事项", SeverityImportant, "乙修改的限制", "甲负责人"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}

	c := mustShift(t, f, "调度", "赵六", tsDay(2, 23, 0), tsDay(3, 7, 0), "")
	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("handover b->c: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h2.ID, it.ID, ActionConfirm, "赵六", "", "", ""); err != nil {
		t.Fatalf("confirm c: %v", err)
	}
	if _, err := f.svc.CloseItem(it.ID, "赵六"); err != nil {
		t.Fatalf("close in c: %v", err)
	}
	if _, err := f.svc.CloseShift(c.ID); err != nil {
		t.Fatalf("close c: %v", err)
	}

	ra, _ := f.svc.ShiftReport(a.ID)
	rb, _ := f.svc.ShiftReport(b.ID)
	rc, _ := f.svc.ShiftReport(c.ID)
	sa, sb, sc := findCloseItem(ra, it.ID), findCloseItem(rb, it.ID), findCloseItem(rc, it.ID)
	if sa == nil || sb == nil || sc == nil {
		t.Fatalf("三个班次都应保留结束时记录")
	}
	if sa.Constraints != "甲留下的限制" || sa.Severity != SeverityNormal || sa.Closed {
		t.Fatalf("甲班记录应为自己结束时的限制且未关闭：%+v", sa)
	}
	if sb.Constraints != "乙修改的限制" || sb.Severity != SeverityImportant || sb.Closed {
		t.Fatalf("乙班记录应为自己结束时的限制且未关闭：%+v", sb)
	}
	if !sc.Closed || sc.CloseOperator != "赵六" {
		t.Fatalf("丙班记录应为已关闭：%+v", sc)
	}

	// 编号与原始班次保持不变。
	cur, _ := f.svc.GetItem(it.ID)
	if cur.ID != it.ID || cur.OriginShiftID != a.ID {
		t.Fatalf("编号与原始班次应保持不变：%+v", cur)
	}

	// 进行中的班次仍展示当前信息。
	d := mustShift(t, f, "调度", "孙八", tsDay(3, 7, 0), tsDay(3, 15, 0), "")
	rep, err := f.svc.ShiftReport(d.ID)
	if err != nil {
		t.Fatalf("report open shift: %v", err)
	}
	if rep.ItemsAtClose || rep.HistoryIncomplete {
		t.Fatalf("进行中的班次不应有结束时记录标记")
	}
}

// TestEmptyCloseRecordMarked：空清单成功结束后明确显示当时没有事项，
// 与缺少历史记录区分开。
func TestEmptyCloseRecordMarked(t *testing.T) {
	f := newFixture(t)
	e := mustShift(t, f, "调度", "孙八", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	if _, err := f.svc.CloseShift(e.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	rep, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose {
		t.Fatalf("空清单结束也应留下结束时记录")
	}
	if len(rep.CloseItems) != 0 {
		t.Fatalf("结束时记录应为空清单，got %d", len(rep.CloseItems))
	}
	text := handoverReportText(t, f.svc, e.ID)
	if !strings.Contains(text, "结束时没有事项") {
		t.Fatalf("空清单结束应明确显示当时没有事项：\n%s", text)
	}
	if strings.Contains(text, "历史记录不完整") {
		t.Fatalf("空清单记录不应被标记为历史不完整")
	}
}

// TestLegacyDataWithoutCloseRecord：旧文件中缺少结束时记录的已结束班次，
// 事项区标明“历史记录不完整，以下为当前信息”；旧的进行中班次成功结束后
// 留下完整记录。
func TestLegacyDataWithoutCloseRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	closedAt := tsDay(2, 16, 0)
	createdAt := tsDay(2, 8, 0)
	data := Data{
		ShiftSeq: 2,
		ItemSeq:  1,
		Shifts: []Shift{
			{ID: "S001", Position: "调度", Owner: "张三", Start: tsDay(2, 8, 0), End: tsDay(2, 16, 0),
				CreatedAt: createdAt, Closed: true, ClosedAt: &closedAt},
			{ID: "S002", Position: "调度", Owner: "李四", Start: tsDay(2, 16, 0), End: tsDay(2, 23, 0),
				CreatedAt: closedAt},
		},
		Items: []Item{{
			ID: "I001", OriginShiftID: "S001", ShiftIDs: []string{"S001"}, CurrentShiftID: "S001",
			Content: "旧内容", Severity: SeverityNormal, FollowOwner: "王五", CreatedAt: createdAt,
		}},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := NewService(store)

	rep, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.HistoryIncomplete || rep.ItemsAtClose {
		t.Fatalf("旧数据应标记历史记录不完整，got %+v", rep)
	}
	if len(rep.Items) != 1 || rep.Items[0].Content != "旧内容" {
		t.Fatalf("旧数据应展示当前事项信息：%+v", rep.Items)
	}
	text := handoverReportText(t, svc, "S001")
	if !strings.Contains(text, "历史记录不完整") || !strings.Contains(text, "当前信息") {
		t.Fatalf("旧数据报告应标明历史记录不完整、以下为当前信息：\n%s", text)
	}

	// 旧的进行中班次成功结束后也要留下完整记录。
	if _, err := svc.CloseShift("S002"); err != nil {
		t.Fatalf("close legacy open shift: %v", err)
	}
	rep2, err := svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report after close: %v", err)
	}
	if !rep2.ItemsAtClose || rep2.HistoryIncomplete {
		t.Fatalf("旧进行中班次结束后应留下完整结束时记录")
	}
}

// TestFailedCloseLeavesNoRecord：结束班次因接班交接未完成而失败时，班次仍
// 保持进行中，不留下结束时记录；重试成功后以那次成功时的事项为准；
// 再次结束已结束班次报错，不能重写历史。
func TestFailedCloseLeavesNoRecord(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	if _, err := f.svc.AddItem(a.ID, "事项", SeverityNormal, "", "李四"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}

	// 接班交接未完成，结束失败。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("有未处理交接项时结束应报错，got %v", err)
	}
	rep, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.Shift.Closed || rep.ItemsAtClose {
		t.Fatalf("结束失败不应留下结束时记录")
	}

	// 处理交接；接收后修改内容，使重试成功时的事项与失败时不同。
	if _, err := f.svc.ProcessEntry(h.ID, "I001", ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.UpdateItem("I001", "重试时内容", SeverityUrgent, "限制", "王五"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	rep2, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report after retry: %v", err)
	}
	if !rep2.ItemsAtClose {
		t.Fatalf("重试成功后应有结束时记录")
	}
	snap := findCloseItem(rep2, "I001")
	if snap == nil || snap.Content != "重试时内容" || snap.Severity != SeverityUrgent {
		t.Fatalf("重试成功应以那次成功时的事项为准：%+v", snap)
	}

	// 再次结束已结束班次报错，不能重写历史。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("重复结束应报错，got %v", err)
	}
}

// TestTrackNewOwnerKeepsHandoverShiftOwner：继续跟踪指定新负责人时，交接
// 处理结果与事项最新信息反映新负责人，交班班次结束时留下的负责人仍可辨认。
func TestTrackNewOwnerKeepsHandoverShiftOwner(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	idA := items[0].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "继续盯到底", "新负责人"); err != nil {
		t.Fatalf("track: %v", err)
	}

	ra, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	snap := findCloseItem(ra, idA)
	if snap == nil {
		t.Fatalf("交班班次应有结束时记录")
	}
	if snap.FollowOwner != "李四" {
		t.Fatalf("交班班次结束时留下的负责人仍可辨认，got %s", snap.FollowOwner)
	}

	cur, err := f.svc.GetItem(idA)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if cur.FollowOwner != "新负责人" {
		t.Fatalf("事项最新信息应反映新负责人，got %s", cur.FollowOwner)
	}
	got, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	for _, e := range got.Entries {
		if e.ItemID == idA && e.FollowOwner != "新负责人" {
			t.Fatalf("交接处理结果应反映新负责人，got %s", e.FollowOwner)
		}
	}
}

// TestCloseRecordPersistsAcrossReopen：退出再打开后，结束时记录、最新事项
// 信息与交接进度都应保留。
func TestCloseRecordPersistsAcrossReopen(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	if _, err := f.svc.AddItem(a.ID, "事项", SeverityNormal, "限制", "李四"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, "I001", ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}

	f.reopen(t)

	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose || len(rep.CloseItems) != 1 {
		t.Fatalf("重开后结束时记录应保留")
	}
	snap := findCloseItem(rep, "I001")
	if snap == nil || snap.Constraints != "限制" || snap.FollowOwner != "李四" || snap.Closed {
		t.Fatalf("重开后结束时记录内容应保留：%+v", snap)
	}
	// 交接进度仍在：退回项未完成。
	got, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if got.Completed() {
		t.Fatalf("重开后交接进度应保留（退回项未完成）")
	}
}

// TestOpenShiftReportListsItemsOnce：事项清单按编号排列，每项只列一次。
func TestOpenShiftReportListsItemsOnce(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	for _, c := range []string{"甲", "乙", "丙"} {
		if _, err := f.svc.AddItem(a.ID, c, SeverityNormal, "", "李四"); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	seen := map[string]int{}
	for _, it := range rep.Items {
		seen[it.ID]++
	}
	for _, id := range []string{"I001", "I002", "I003"} {
		if seen[id] != 1 {
			t.Fatalf("事项 %s 应恰好出现一次，got %d", id, seen[id])
		}
	}
	if rep.Items[0].ID != "I001" || rep.Items[1].ID != "I002" || rep.Items[2].ID != "I003" {
		t.Fatalf("事项清单应按编号排列")
	}
}

// TestReturnedItemCannotBeProcessedBeforeResubmit：退回表示等待交班人补充，
// 尚未重新提交的退回项，接班人即使填了操作人、退回原因或跟踪说明，也不能
// 确认、继续跟踪或再次退回；失败后状态、轮次、处理人/时间、后续负责人、
// 事项所在班次与交接完成情况都不变。
func TestReturnedItemCannotBeProcessedBeforeResubmit(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID

	// 退回第一项。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "信息不全", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}

	// 退回后未重新提交前，确认、继续跟踪、再次退回都应报状态错误。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未重新提交不能确认，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "跟踪说明", "新负责人"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未重新提交不能继续跟踪，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "再次退回原因", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未重新提交不能再次退回，got %v", err)
	}

	// 失败后该项仍为退回，不增加轮次、不覆盖处理人和时间。
	got, _ := f.svc.GetHandover(h.ID)
	var ea *HandoverEntry
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if ea.Status != EntryReturned {
		t.Fatalf("失败后该项应仍为退回，got %s", ea.Status)
	}
	if len(ea.Rounds) != 1 {
		t.Fatalf("失败不应增加退回轮次，期望1轮，got %d", len(ea.Rounds))
	}
	if ea.Operator != "李四" || ea.ProcessedAt == nil {
		t.Fatalf("失败不应覆盖上次处理人和时间：%+v", ea)
	}
	if got.Completed() {
		t.Fatalf("退回项未处理，交接应仍未完成")
	}
	// 事项仍留在交班班次，后续负责人未被调整。
	itA, _ := f.svc.GetItem(idA)
	if itA.CurrentShiftID != a.ID {
		t.Fatalf("退回项不应移动到接班班次，got %s", itA.CurrentShiftID)
	}
	if itA.FollowOwner != "李四" {
		t.Fatalf("失败不应调整后续负责人，got %s", itA.FollowOwner)
	}
	// 接班班次仍不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未重新提交，接班班次不能结束，got %v", err)
	}
}

// TestResubmitClearsCurrentResultAndKeepsRound：重新提交只恢复该项为待处理，
// 当前接班处理人与处理时间清空（显示尚未处理），退回时的操作人与时间仍保留
// 在本轮退回历史中；事项不移动、原文/严重程度/限制条件不变。
func TestResubmitClearsCurrentResultAndKeepsRound(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID

	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "信息不全", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "图纸已补"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	got, _ := f.svc.GetHandover(h.ID)
	var ea *HandoverEntry
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if ea.Status != EntryPending {
		t.Fatalf("重新提交后应恢复待处理，got %s", ea.Status)
	}
	if ea.Operator != "" || ea.ProcessedAt != nil {
		t.Fatalf("当前接班处理人与处理时间应清空（尚未处理）：operator=%q processedAt=%v", ea.Operator, ea.ProcessedAt)
	}
	if got.Completed() {
		t.Fatalf("恢复待处理后交接应未完成")
	}
	r := ea.Rounds[len(ea.Rounds)-1]
	if r.Reason != "信息不全" || r.ReturnOperator != "李四" || r.ReturnedAt.IsZero() {
		t.Fatalf("退回轮次应保留原因、退回人与退回时间：%+v", r)
	}
	if r.Supplement != "图纸已补" || r.SupplementOperator != "张三" || r.SupplementAt == nil || r.ResubmittedAt == nil {
		t.Fatalf("应记录补充人、补充时间与重新提交时间：%+v", r)
	}
	// 重新提交不表示接收：事项仍留在交班班次，原文/严重程度/限制条件不变。
	itA, _ := f.svc.GetItem(idA)
	if itA.CurrentShiftID != a.ID || itA.Content != "事项甲" || itA.Severity != SeverityNormal {
		t.Fatalf("重新提交不表示接收，事项应留在交班班次且原文不变：%+v", itA)
	}
	// 事项历史应留痕重新提交。
	found := false
	for _, ev := range itA.Events {
		if ev.Kind == "resubmitted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("事项历史应记录重新提交：%+v", itA.Events)
	}
}

// TestResubmitOnlyOnReturned：只有退回项能补充说明并重新提交；待处理、已确认
// 或继续跟踪的事项重新提交报状态错误，且不写入补充说明。
func TestResubmitOnlyOnReturned(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	// 待处理项不能重新提交。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("待处理项不能重新提交，got %v", err)
	}
	// 确认后不能重新提交。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已确认项不能重新提交，got %v", err)
	}
	// 继续跟踪项不能重新提交。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "跟踪", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("继续跟踪项不能重新提交，got %v", err)
	}
	// 补充说明未写入。
	got, _ := f.svc.GetHandover(h.ID)
	for _, e := range got.Entries {
		if len(e.Rounds) != 0 {
			t.Fatalf("非退回项不应写入补充说明：%+v", e)
		}
	}
}

// TestResubmitWhitespaceSupplementRejected：补充说明必须非空，纯空格视为空；
// 操作人同样必填。
func TestResubmitWhitespaceSupplementRejected(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("纯空格补充说明应视为空，got %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "  ", "补充"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("操作人必填，got %v", err)
	}
}

// TestReReturnCreatesNextRoundAndWaits：重新提交后接班人再次退回才产生下一轮
// 记录并重新等待补充；此前某一轮已有补充不能代替新一轮的重新提交；全部接收后
// 交接才完成。
func TestReReturnCreatesNextRoundAndWaits(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	// 第一轮退回 + 补充 + 重新提交。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第一轮退回", "", ""); err != nil {
		t.Fatalf("return1: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第一轮补充"); err != nil {
		t.Fatalf("resubmit1: %v", err)
	}
	// 接班人再次退回，产生第二轮。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第二轮退回", "", ""); err != nil {
		t.Fatalf("return2: %v", err)
	}
	got, _ := f.svc.GetHandover(h.ID)
	var ea *HandoverEntry
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if len(ea.Rounds) != 2 {
		t.Fatalf("再次退回应产生下一轮记录，期望2轮，got %d", len(ea.Rounds))
	}
	if ea.Rounds[0].Reason != "第一轮退回" || ea.Rounds[0].Supplement != "第一轮补充" {
		t.Fatalf("第一轮说明不得被覆盖：%+v", ea.Rounds[0])
	}
	if ea.Rounds[1].Reason != "第二轮退回" || ea.Rounds[1].Supplement != "" {
		t.Fatalf("第二轮应为新的等待补充状态：%+v", ea.Rounds[1])
	}
	if ea.Status != EntryReturned {
		t.Fatalf("再次退回后应为退回状态，got %s", ea.Status)
	}
	// 第二轮未重新提交前不能处理；第一轮已有补充不能代替第二轮。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("第二轮未重新提交不能确认，got %v", err)
	}
	if ea.Rounds[1].ResubmittedAt != nil {
		t.Fatalf("第二轮不应已有重新提交时间")
	}
	// 补充第二轮并重新提交后才能处理。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第二轮补充"); err != nil {
		t.Fatalf("resubmit2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm after resubmit2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm idB: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() {
		t.Fatalf("全部接收后交接应完成")
	}
}

// TestResubmitPersistsAcrossReopen：退出后重新打开，仍按保存的当前状态决定
// 能否处理；补充与重新提交记录保留，接班人可继续办理。
func TestResubmitPersistsAcrossReopen(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充材料"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}

	f.reopen(t)

	got, _ := f.svc.GetHandover(h.ID)
	var ea *HandoverEntry
	for i := range got.Entries {
		if got.Entries[i].ItemID == idA {
			ea = &got.Entries[i]
		}
	}
	if ea.Status != EntryPending || ea.Operator != "" || ea.ProcessedAt != nil {
		t.Fatalf("重开后当前结果应保持待处理且处理人/时间为空：%+v", ea)
	}
	r := ea.Rounds[len(ea.Rounds)-1]
	if r.Supplement != "补充材料" || r.ResubmittedAt == nil {
		t.Fatalf("重开后补充与重新提交记录应保留：%+v", r)
	}
	// 重开后接班人可以继续处理。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重开后应能继续处理：%v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重开后应能继续处理 idB：%v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() {
		t.Fatalf("重开后续办应能完成")
	}
}

// TestHandoverAndShiftReportShowRoundsConsistently：交接查询与按班次查询应
// 一致展示当前结果与逐轮退回、补充记录。
func TestHandoverAndShiftReportShowRoundsConsistently(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充材料"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}

	hh, _ := f.svc.GetHandover(h.ID)
	handoverText := FormatHandover(hh)
	shiftText := handoverReportText(t, f.svc, b.ID)
	for _, text := range []string{handoverText, shiftText} {
		if !strings.Contains(text, "待处理") {
			t.Fatalf("应展示当前结果为待处理")
		}
		if !strings.Contains(text, "需补充") || !strings.Contains(text, "补充材料") {
			t.Fatalf("应逐轮展示退回原因与补充说明")
		}
		if !strings.Contains(text, "已重新提交") {
			t.Fatalf("应展示重新提交时间")
		}
	}
}
