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
	// 不能交给其他岗位：改用一个从未发起过交接的交班班次 c，首次交接仍按
	// 岗位不符拒绝（同岗位要求对首次交接保留）。
	other := mustShift(t, f, "巡检", "赵六", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	if _, err := f.svc.CreateHandover(c.ID, other.ID); !errors.Is(err, ErrPositionMismatch) {
		t.Fatalf("首次交接给其他岗位应报错，got %v", err)
	}
	// 已有交接记录时改指其他岗位的班次，按改换接班对象拒绝并指出原接班班次，
	// 不再产生第二条交接（同岗位校验只对首次交接保留）。
	if _, err := f.svc.CreateHandover(a.ID, other.ID); !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("已有交接时改指其他岗位应按改换对象拒绝，got %v", err)
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

// TestRecreateHandoverAfterToShiftClosed：某次交接全部处理完成、接班班次随后
// 结束（事项又在后续班次关闭或继续流转）后，用原交班/接班编号重复发起必须视为
// 成功返回已有记录：原交接编号、两班关系、发起与完成时间、各事项当时的确认/
// 跟踪结果、处理人、处理时间以及退回与补充经过全部沿用原值，不重新挑选清单、
// 不重新接收、不移动事项、不追加经过。改换接班对象（无论新对象是否结束）一律
// 拒绝并指出原接班班次，且不得生成第二条交接；不存在的编号仍然报错。
func TestRecreateHandoverAfterToShiftClosed(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h1, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	idA, idB := items[0].ID, items[1].ID

	// 乙先退回、交班人补充后再继续跟踪，保留一轮退回/补充经过；甲确认接收。
	if _, err := f.svc.ProcessEntry(h1.ID, idB, ActionReturn, "李四", "需要补充细节", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h1.ID, idB, "张三", "补充说明如下"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, idB, ActionTrack, "李四", "", "继续盯压力", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	h1, _ = f.svc.GetHandover(h1.ID)
	if !h1.Completed() || h1.CompletedAt == nil {
		t.Fatalf("全部处理后交接应已完成")
	}
	createdAt, completedAt := h1.CreatedAt, *h1.CompletedAt

	// 接班班次结束；事项继续流转到下一班 c，甲在 c 关闭。
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	c := mustShift(t, f, "调度", "赵六", tsDay(2, 23, 0), tsDay(3, 7, 0), "")
	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("chain handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h2.ID, idA, ActionConfirm, "赵六", "", "", ""); err != nil {
		t.Fatalf("confirm on c: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h2.ID, idB, ActionTrack, "赵六", "", "持续跟进", "孙七"); err != nil {
		t.Fatalf("track on c: %v", err)
	}
	if _, err := f.svc.CloseItem(idA, "赵六"); err != nil {
		t.Fatalf("close item on c: %v", err)
	}

	snapshot := func() string {
		raw, err := json.Marshal(f.store.data)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	before := snapshot()

	// 用原来的交班、接班编号重复发起：返回原记录（ErrHandoverExists），命令行
	// 据此视为成功；即使接班班次 b 已结束也不再报“接班班次已结束”。
	again, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) {
		t.Fatalf("接班班次结束后重复发起应返回 ErrHandoverExists，got %v", err)
	}
	stored, _ := f.svc.GetHandover(h1.ID)
	if again.ID != h1.ID || again.FromShiftID != a.ID || again.ToShiftID != b.ID {
		t.Fatalf("应返回原交接 %s 及原两班关系，got %+v", h1.ID, again)
	}
	if !again.CreatedAt.Equal(createdAt) || again.CompletedAt == nil || !again.CompletedAt.Equal(completedAt) {
		t.Fatalf("发起时间与完成时间应沿用原值：created %v->%v completed %v->%v",
			createdAt, again.CreatedAt, completedAt, again.CompletedAt)
	}
	if len(again.Entries) != 2 {
		t.Fatalf("仍应展示原交接的2项清单，而不是按最新班次重新挑选，got %d", len(again.Entries))
	}
	got := map[string]HandoverEntry{}
	for _, e := range again.Entries {
		got[e.ItemID] = e
	}
	ea, eb := got[idA], got[idB]
	if ea.Status != EntryConfirmed || ea.Operator != "李四" || ea.ProcessedAt == nil {
		t.Fatalf("甲的确认结果/处理人/处理时间应沿用原值：%+v", ea)
	}
	if eb.Status != EntryTracking || eb.Operator != "李四" || eb.ProcessedAt == nil ||
		eb.TrackingNote != "继续盯压力" || eb.FollowOwner != "王五" {
		t.Fatalf("乙的继续跟踪结果应沿用原值：%+v", eb)
	}
	if len(eb.Rounds) != 1 {
		t.Fatalf("退回与补充经过应保留，got %d 轮", len(eb.Rounds))
	}
	r := eb.Rounds[0]
	if r.Reason != "需要补充细节" || r.ReturnOperator != "李四" ||
		r.Supplement != "补充说明如下" || r.SupplementOperator != "张三" ||
		r.ResubmittedAt == nil {
		t.Fatalf("退回原因、补充说明、补充人与重新提交时间应原样保留：%+v", r)
	}
	// 返回的记录与存储中的原记录逐项一致。
	if fmt.Sprintf("%+v", again) != fmt.Sprintf("%+v", stored) {
		t.Fatalf("返回内容应为保存的完整原记录\nwant %+v\ngot  %+v", stored, again)
	}

	// 重复返回不改动任何数据：不生成新交接、不移动事项、不追加事件或经过。
	if after := snapshot(); after != before {
		t.Fatalf("重复发起不得写入或移动任何数据\nbefore %s\nafter  %s", before, after)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 2 {
		t.Fatalf("不应生成第二条交接，期望仍为2条，got %d", len(hs))
	}

	// 改换为一个确实存在、但已结束的班次：仍按改换对象拒绝并指出原接班班次，
	// 不能因为新对象已结束而报“接班班次已结束”，更不能产生第二条交接。
	d := mustShift(t, f, "调度", "钱七", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	if _, err := f.svc.CloseShift(d.ID); err != nil {
		t.Fatalf("close d: %v", err)
	}
	_, err = f.svc.CreateHandover(a.ID, d.ID)
	if !errors.Is(err, ErrHandoverTarget) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("改换为已结束班次应报 ErrHandoverTarget 并指出原接班班次 %s，got %v", b.ID, err)
	}

	// 改换为尚未结束的其他班次同样拒绝。
	open := mustShift(t, f, "调度", "孙八", tsDay(3, 16, 0), tsDay(4, 0, 0), "")
	if _, err := f.svc.CreateHandover(a.ID, open.ID); !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("改换为未结束班次也应报 ErrHandoverTarget，got %v", err)
	}
	// 改指其他岗位的现存班次同样按改换对象拒绝（同岗位等要求只对首次交接保留）。
	otherPos := mustShift(t, f, "巡检", "周九", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	if _, err := f.svc.CreateHandover(a.ID, otherPos.ID); !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("已有交接时改指其他岗位应按改换对象拒绝，got %v", err)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 2 {
		t.Fatalf("改换对象不得产生第二条交接，got %d 条", len(hs))
	}

	// 编号错误不能被已有记录掩盖。
	if _, err := f.svc.CreateHandover(a.ID, "S999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("接班编号不存在应报 ErrNotFound，got %v", err)
	}
	if _, err := f.svc.CreateHandover("S999", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("交班编号不存在应报 ErrNotFound，got %v", err)
	}
	if _, err := f.svc.CreateHandover("S999", "S998"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("编号均不存在应报 ErrNotFound，got %v", err)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 2 {
		t.Fatalf("报错后交接数量应不变，got %d", len(hs))
	}
}

// TestRecreateEmptyHandoverAfterToShiftClosed：发起时即完成的空清单交接，
// 在接班班次结束后重复发起也应返回原记录与原完成时间。
func TestRecreateEmptyHandoverAfterToShiftClosed(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(h.Entries) != 0 || !h.Completed() || h.CompletedAt == nil {
		t.Fatalf("空清单交接应发起即完成：%+v", h)
	}
	completedAt := *h.CompletedAt
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}

	again, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) {
		t.Fatalf("空清单交接在接班班次结束后重复发起应返回原记录，got %v", err)
	}
	if again.ID != h.ID || len(again.Entries) != 0 ||
		again.CompletedAt == nil || !again.CompletedAt.Equal(completedAt) {
		t.Fatalf("应返回原空清单交接与原完成时间：%+v", again)
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

// findEntryOf 从交接记录中取出指定事项的当前记录。
func findEntryOf(t *testing.T, h Handover, itemID string) *HandoverEntry {
	t.Helper()
	for i := range h.Entries {
		if h.Entries[i].ItemID == itemID {
			return &h.Entries[i]
		}
	}
	t.Fatalf("交接 %s 中缺少事项 %s", h.ID, itemID)
	return nil
}

// TestReturnedMustWaitForResubmit：退回表示等待交班人补充；未重新提交前，
// 确认、继续跟踪、再次退回应报状态错误，且不增加退回轮次、不覆盖上次处理人
// 与时间、不调整后续负责人、不移动事项班次，交接完成情况也不变。
func TestReturnedMustWaitForResubmit(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "信息不全，需补充图纸", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm idB: %v", err)
	}

	before, _ := f.svc.GetHandover(h.ID)
	eaBefore := findEntryOf(t, before, idA)
	returnedAt := eaBefore.ProcessedAt
	if eaBefore.Status != EntryReturned || len(eaBefore.Rounds) != 1 {
		t.Fatalf("前置状态应为退回且只有1轮：%+v", eaBefore)
	}
	if eaBefore.Operator != "李四" {
		t.Fatalf("前置处理人应为退回人李四")
	}

	// 即使填了操作人、退回原因、跟踪说明与后续负责人，也不能跳过重新提交。
	try := func(act EntryAction, reason, note, follow string) {
		t.Helper()
		if _, err := f.svc.ProcessEntry(h.ID, idA, act, "接班人王五", reason, note, follow); !errors.Is(err, ErrHandoverState) {
			t.Fatalf("退回未重新提交时 %s 应报 ErrHandoverState，got %v", act, err)
		}
	}
	try(ActionConfirm, "", "", "")
	try(ActionConfirm, "硬塞退回原因", "硬塞跟踪说明", "硬塞负责人")
	try(ActionTrack, "", "继续跟踪说明", "王五")
	try(ActionReturn, "第二次退回原因", "", "")

	got, _ := f.svc.GetHandover(h.ID)
	ea := findEntryOf(t, got, idA)
	if ea.Status != EntryReturned {
		t.Fatalf("失败后状态仍应为退回，got %s", ea.Status)
	}
	if len(ea.Rounds) != 1 {
		t.Fatalf("失败处理不应增加退回轮次，got %d", len(ea.Rounds))
	}
	r := ea.Rounds[0]
	if r.Reason != "信息不全，需补充图纸" || r.ReturnOperator != "李四" {
		t.Fatalf("上一轮退回原因与退回人不得覆盖：%+v", r)
	}
	if r.Supplement != "" || r.SupplementOperator != "" || r.ResubmittedAt != nil {
		t.Fatalf("失败处理不得写入补充或重新提交信息：%+v", r)
	}
	if ea.Operator != "李四" || ea.ProcessedAt == nil || !ea.ProcessedAt.Equal(*returnedAt) {
		t.Fatalf("上次处理人和时间不得被覆盖：op=%s at=%v want %s %s",
			ea.Operator, ea.ProcessedAt, "李四", returnedAt)
	}
	if ea.FollowOwner != "李四" || ea.TrackingNote != "" {
		t.Fatalf("失败处理不得调整后续负责人或留下跟踪说明：%+v", ea)
	}

	it, _ := f.svc.GetItem(idA)
	if it.CurrentShiftID != a.ID || it.FollowOwner != "李四" {
		t.Fatalf("失败处理不得移动事项班次或改负责人：%+v", it)
	}
	if got.Completed() || got.CompletedAt != nil {
		t.Fatalf("交接完成情况不得变化，应仍未完成")
	}
	eb := findEntryOf(t, got, idB)
	if eb.Status != EntryConfirmed {
		t.Fatalf("其他事项结果不应受影响：%s", eb.Status)
	}

	// 接班班次仍因退回项不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未处理完时接班班次不能结束，got %v", err)
	}

	// 退出重开后仍按保存的状态拦截。
	f.reopen(t)
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重开后退回项仍应拦截，got %v", err)
	}

	// 成功重新提交后仅该项恢复待处理，接班处理人与处理时间显示为尚未处理。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "图纸编号已补到资料柜B层"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	ea = findEntryOf(t, got, idA)
	if ea.Status != EntryPending || ea.Operator != "" || ea.ProcessedAt != nil {
		t.Fatalf("重新提交后应恢复待处理且接班处理信息清空（尚未处理）：%+v", ea)
	}
	r = ea.Rounds[0]
	if r.Reason != "信息不全，需补充图纸" || r.ReturnOperator != "李四" || !r.ReturnedAt.Equal(*returnedAt) {
		t.Fatalf("本轮退回原因、退回人、退回时间应保留：%+v", r)
	}
	if r.Supplement != "图纸编号已补到资料柜B层" || r.SupplementOperator != "张三" ||
		r.SupplementAt == nil || r.ResubmittedAt == nil {
		t.Fatalf("应记录补充人、补充时间与重新提交时间：%+v", r)
	}
	it, _ = f.svc.GetItem(idA)
	if it.CurrentShiftID != a.ID {
		t.Fatalf("重新提交本身不表示接收，事项仍应留在交班班次")
	}
	if it.Content != ea.Content || string(it.Severity) != string(ea.Severity) || it.Constraints != ea.Constraints {
		t.Fatalf("重新提交不得改变原文、严重程度和限制条件")
	}

	// 重新提交后接班人可以正常确认接收。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重新提交后确认应成功：%v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	if !got.Completed() || got.CompletedAt == nil {
		t.Fatalf("全部接收后交接才完成")
	}
}

// TestEachReturnRoundRequiresOwnResubmit：再次退回才产生下一轮并重新等待补充；
// 此前某一轮已有补充不能代替新一轮的重新提交。
func TestEachReturnRoundRequiresOwnResubmit(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID
	// 另一项直接确认，避免交接提前进入完成态的干扰。
	if _, err := f.svc.ProcessEntry(h.ID, items[1].ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}

	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第一轮原因", "", ""); err != nil {
		t.Fatalf("return 1: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第一轮补充"); err != nil {
		t.Fatalf("resubmit 1: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第二轮原因", "", ""); err != nil {
		t.Fatalf("return 2: %v", err)
	}

	got, _ := f.svc.GetHandover(h.ID)
	ea := findEntryOf(t, got, idA)
	if len(ea.Rounds) != 2 {
		t.Fatalf("再次退回应产生第2轮，got %d", len(ea.Rounds))
	}

	// 第一轮补充不能代替第二轮的重新提交。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("第二轮退回后必须重新提交，got %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "说明", "王五"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("第二轮退回后继续跟踪也必须拦截，got %v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	ea = findEntryOf(t, got, idA)
	r1, r2 := ea.Rounds[0], ea.Rounds[1]
	if r1.Reason != "第一轮原因" || r1.Supplement != "第一轮补充" || r1.SupplementOperator != "张三" {
		t.Fatalf("第一轮记录不得被覆盖：%+v", r1)
	}
	if r2.Reason != "第二轮原因" || r2.ReturnOperator != "李四" ||
		r2.Supplement != "" || r2.ResubmittedAt != nil {
		t.Fatalf("第二轮应保留新退回原因且尚无补充：%+v", r2)
	}
	if ea.Status != EntryReturned {
		t.Fatalf("拦截后仍应为退回")
	}

	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第二轮补充"); err != nil {
		t.Fatalf("resubmit 2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionTrack, "李四", "", "持续跟进", "王五"); err != nil {
		t.Fatalf("第二轮重新提交后应能继续跟踪：%v", err)
	}
	got, _ = f.svc.GetHandover(h.ID)
	ea = findEntryOf(t, got, idA)
	if ea.Status != EntryTracking || ea.Operator != "李四" || ea.ProcessedAt == nil {
		t.Fatalf("继续跟踪应只处理一次并记录处理人/时间：%+v", ea)
	}
	if len(ea.Rounds) != 2 {
		t.Fatalf("两轮历史都应保留")
	}
	if ea.Rounds[0].Supplement != "第一轮补充" {
		t.Fatalf("第一轮补充不得被第二轮覆盖：%+v", ea.Rounds[0])
	}
	it, _ := f.svc.GetItem(idA)
	if it.CurrentShiftID != b.ID || it.FollowOwner != "王五" {
		t.Fatalf("继续跟踪后事项应进入接班班次并更新后续负责人：%+v", it)
	}
	// 已接收项不能再退回，也不能重新提交。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "再退", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已接收项不能退回，got %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "不应写入"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("继续跟踪项不能重新提交，got %v", err)
	}
}

// TestResubmitStateAndInputRules：只有退回项可重新提交；待处理、已确认、
// 继续跟踪都报状态错误且不写入补充；操作人必填、纯空格补充视为空。
func TestResubmitStateAndInputRules(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID

	// 待处理项重新提交：状态错误，不写入。
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充内容"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("待处理项重新提交应报 ErrHandoverState，got %v", err)
	}
	got, _ := f.svc.GetHandover(h.ID)
	ea := findEntryOf(t, got, idA)
	if len(ea.Rounds) != 0 || ea.Status != EntryPending {
		t.Fatalf("状态错误时不应写入补充或退回记录：%+v", ea)
	}

	// 已确认项重新提交：状态错误。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充内容"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已确认项重新提交应报错，got %v", err)
	}

	// 继续跟踪项重新提交：状态错误。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "盯住", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充内容"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("继续跟踪项重新提交应报错，got %v", err)
	}

	// 退回后：操作人缺失、纯空格补充均为输入错误，且不写入。idA/idB 都已接收，
	// 另起一套数据制造退回项。
	f2 := newFixture(t)
	a2, b2, items2 := prepareHandover(t, f2)
	h3, _ := f2.svc.CreateHandover(a2.ID, b2.ID)
	id := items2[0].ID
	if _, err := f2.svc.ProcessEntry(h3.ID, id, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f2.svc.ResubmitReturned(h3.ID, id, "   ", "补充"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("操作人必填，got %v", err)
	}
	if _, err := f2.svc.ResubmitReturned(h3.ID, id, "张三", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("纯空格补充说明应视为空，got %v", err)
	}
	g, _ := f2.svc.GetHandover(h3.ID)
	e := findEntryOf(t, g, id)
	if e.Status != EntryReturned || len(e.Rounds) != 1 || e.Rounds[0].Supplement != "" ||
		e.Rounds[0].ResubmittedAt != nil || e.Operator != "李四" {
		t.Fatalf("输入校验失败不得写入任何内容：%+v", e)
	}
	if _, err := f2.svc.ResubmitReturned(h3.ID, "I999", "张三", "补充"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清单外事项应 ErrNotFound，got %v", err)
	}
	if _, err := f2.svc.ResubmitReturned("H999", id, "张三", "补充"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在交接应 ErrNotFound，got %v", err)
	}

	// 重复重新提交已恢复待处理的事项也要报状态错误（不能二次写入同一轮）。
	if _, err := f2.svc.ResubmitReturned(h3.ID, id, "张三", "第一次补充"); err != nil {
		t.Fatalf("first resubmit: %v", err)
	}
	if _, err := f2.svc.ResubmitReturned(h3.ID, id, "张三", "再次补充"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已重新提交、等待处理的事项不能再次补充提交，got %v", err)
	}
	g, _ = f2.svc.GetHandover(h3.ID)
	e = findEntryOf(t, g, id)
	if e.Rounds[0].Supplement != "第一次补充" {
		t.Fatalf("重复重新提交失败不得覆盖原补充：%+v", e.Rounds[0])
	}
}

// TestReturnedQueriesShowPendingAsUnprocessed：交接查询与按班次查询一致展示
// 当前结果与逐轮退回、补充记录；重新提交后接班处理人和处理时间显示尚未处理。
func TestReturnedQueriesShowPendingAsUnprocessed(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID

	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充图纸", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "图纸在B层"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}

	htext := FormatHandover(mustGetHandover(t, f, h.ID))
	if !strings.Contains(htext, "尚未处理") {
		t.Fatalf("交接查询应把恢复待处理项显示为尚未处理：\n%s", htext)
	}
	if !strings.Contains(htext, "第1次退回") || !strings.Contains(htext, "需补充图纸") ||
		!strings.Contains(htext, "图纸在B层") || !strings.Contains(htext, "已重新提交") {
		t.Fatalf("交接查询应逐轮展示退回原因、补充与重新提交：\n%s", htext)
	}

	rep, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	rtext := FormatReport(rep)
	if !strings.Contains(rtext, "处理人=尚未处理") || !strings.Contains(rtext, "处理时间=尚未处理") {
		t.Fatalf("按班次查询当前结果应显示尚未处理：\n%s", rtext)
	}
	if !strings.Contains(rtext, "第1次退回") || !strings.Contains(rtext, "图纸在B层") ||
		!strings.Contains(rtext, "已重新提交") {
		t.Fatalf("按班次查询应逐轮展示退回与补充、重新提交记录：\n%s", rtext)
	}
}

func mustGetHandover(t *testing.T, f *fixture, id string) Handover {
	t.Helper()
	h, err := f.svc.GetHandover(id)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	return h
}

// TestReturnedStatePersistsAcrossReopen：退回后退出重开，未重新提交仍不能处理；
// 重新提交与后续处理、完成状态在重开后均保留。
func TestReturnedStatePersistsAcrossReopen(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA, idB := items[0].ID, items[1].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "需补充", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	f.reopen(t)
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "再退一轮", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重开后未重新提交仍不能再次退回，got %v", err)
	}
	g, _ := f.svc.GetHandover(h.ID)
	e := findEntryOf(t, g, idA)
	if e.Status != EntryReturned || len(e.Rounds) != 1 || e.Operator != "李四" {
		t.Fatalf("重开后退回状态与轮次应原样保留：%+v", e)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "补充材料"); err != nil {
		t.Fatalf("重开后应能补充重新提交：%v", err)
	}

	f.reopen(t)
	g, _ = f.svc.GetHandover(h.ID)
	e = findEntryOf(t, g, idA)
	if e.Status != EntryPending || e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("重开后应保持待处理且尚未处理：%+v", e)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重开后应能确认接收：%v", err)
	}
	g, _ = f.svc.GetHandover(h.ID)
	if !g.Completed() {
		t.Fatalf("重开后续办完成应保留")
	}
}

// journeyKinds 提取处理经过的事件类型序列，便于断言顺序。
func journeyKinds(j ItemJourney) []string {
	kinds := make([]string, len(j.Events))
	for i, ev := range j.Events {
		kinds[i] = ev.Kind
	}
	return kinds
}

func joinStrings(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

// TestItemJourneyFullFlow：凭一个事项编号即可查看它从建立到当前、跨多个班次
// 交接的完整处理经过，无须先知道交接编号；各次交接的当前结果一并列出。
func TestItemJourneyFullFlow(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	c := mustShift(t, f, "调度", "赵六", tsDay(2, 23, 0), tsDay(3, 7, 0), "")
	it, err := f.svc.AddItem(a.ID, "泵房压力异常", SeverityImportant, "夜间禁动", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	// 同一交接里的另一事项，不应出现在本事项经过中。
	other, err := f.svc.AddItem(a.ID, "无关事项勿混入", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add other: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	h1, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create h1: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, it.ID, ActionReturn, "李四", "缺少现场照片", "", ""); err != nil {
		t.Fatalf("return1: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h1.ID, it.ID, "张三", "照片已上传"); err != nil {
		t.Fatalf("resubmit1: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, it.ID, ActionReturn, "李四", "照片不清晰", "", ""); err != nil {
		t.Fatalf("return2: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h1.ID, it.ID, "张三", "已重新拍摄"); err != nil {
		t.Fatalf("resubmit2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, it.ID, ActionTrack, "李四", "", "每两小时记录压力", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h1.ID, other.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("create h2: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h2.ID, it.ID, ActionConfirm, "赵六", "", "", ""); err != nil {
		t.Fatalf("confirm h2: %v", err)
	}
	// 接收后再修改事项负责人，历史中的跟踪负责人不能跟着变。
	if _, err := f.svc.UpdateItem(it.ID, "泵房压力异常", SeverityImportant, "夜间禁动", "钱七"); err != nil {
		t.Fatalf("update: %v", err)
	}

	j, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if !j.HasHandovers {
		t.Fatalf("应识别出事项参与过交接")
	}
	want := []string{
		"created",
		"handover-init", "return", "resubmit", "return", "resubmit", "track",
		"handover-init", "confirm",
		"updated",
	}
	got := journeyKinds(j)
	if joinStrings(got) != joinStrings(want) {
		t.Fatalf("经过顺序不正确：want %v, got %v", want, got)
	}
	// 同一次接收只展示一次：事项历史里的 received 事件不应重复出现。
	for _, ev := range j.Events {
		if ev.Kind == "received" {
			t.Fatalf("接收事件应与交接中的接收处理合并，只展示一次：%+v", ev)
		}
	}
	// 时间从早到晚。
	for i := 1; i < len(j.Events); i++ {
		if j.Events[i].TimeKnown && j.Events[i-1].TimeKnown && j.Events[i].At.Before(j.Events[i-1].At) {
			t.Fatalf("经过应按实际发生时刻排序：%v 早于 %v", j.Events[i].At, j.Events[i-1].At)
		}
	}
	// 发起交接不记录操作人，明确为空（展示为未记录），不以班次负责人代替。
	if j.Events[1].Operator != "" || j.Events[1].HandoverID != h1.ID {
		t.Fatalf("发起交接不应有操作人：%+v", j.Events[1])
	}
	// 退回保留轮次与完整原因；重新提交保留补充说明与补充人。
	if j.Events[2].RoundSeq != 1 || j.Events[2].Reason != "缺少现场照片" || j.Events[2].Operator != "李四" {
		t.Fatalf("第1次退回记录不正确：%+v", j.Events[2])
	}
	if j.Events[3].Supplement != "照片已上传" || j.Events[3].SupplementOperator != "张三" {
		t.Fatalf("第1次重新提交记录不正确：%+v", j.Events[3])
	}
	if j.Events[4].Reason != "照片不清晰" || j.Events[5].Supplement != "已重新拍摄" {
		t.Fatalf("第2轮退回与补充不应覆盖第1轮：%+v %+v", j.Events[4], j.Events[5])
	}
	// 继续跟踪保留跟踪说明与当时的后续负责人，不随后续修改改变。
	tr := j.Events[6]
	if tr.TrackingNote != "每两小时记录压力" || tr.FollowOwner != "王五" {
		t.Fatalf("跟踪说明与当时的跟踪负责人应保留：%+v", tr)
	}
	if j.Item.FollowOwner != "钱七" {
		t.Fatalf("事项最新负责人应为修改后的值：%s", j.Item.FollowOwner)
	}
	// 每次交接的当前处理结果。
	if len(j.Results) != 2 || j.Results[0].HandoverID != h1.ID || j.Results[1].HandoverID != h2.ID {
		t.Fatalf("应列出两次交接的当前结果：%+v", j.Results)
	}
	if j.Results[0].Entry.Status != EntryTracking || j.Results[1].Entry.Status != EntryConfirmed {
		t.Fatalf("各次交接当前结果不正确：%+v", j.Results)
	}

	text := FormatItemJourney(j)
	for _, want := range []string{
		"泵房压力异常", "重要", "夜间禁动", "钱七", "当前班次=" + c.ID,
		"发起交接", "操作人=未记录",
		"第1次退回", "缺少现场照片", "第1次重新提交", "照片已上传", "补充人=张三",
		"第2次退回", "照片不清晰", "已重新拍摄",
		"继续跟踪", "每两小时记录压力", "后续负责人=王五",
		"确认接收", "交接当前结果", h1.ID, h2.ID, a.ID + " -> " + b.ID, b.ID + " -> " + c.ID,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("展示缺少 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, other.ID) || strings.Contains(text, "无关事项勿混入") {
		t.Fatalf("同一交接中的其他事项不应出现在本事项经过中：\n%s", text)
	}

	// 退出再打开后，已保存的经过仍能查到。
	f.reopen(t)
	j2, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("重开后 journey: %v", err)
	}
	if joinStrings(journeyKinds(j2)) != joinStrings(want) || len(j2.Results) != 2 {
		t.Fatalf("重开后经过应完整保留：%v", journeyKinds(j2))
	}
}

// TestItemJourneyReturnedAndResubmittedResult：退回未重新提交时当前结果显示
// 等待交班人补充；重新提交后显示待处理、接班人尚未处理，且不显示成已接收、
// 不改变事项当前班次。
func TestItemJourneyReturnedAndResubmittedResult(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "信息不全", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}

	j, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("当前结果应为退回：%+v", j.Results)
	}
	text := FormatItemJourney(j)
	if !strings.Contains(text, "等待交班人补充") {
		t.Fatalf("退回未补充时应显示等待交班人补充：\n%s", text)
	}

	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "已补齐"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	j, err = f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey after resubmit: %v", err)
	}
	if j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("重新提交后应恢复待处理：%+v", j.Results[0].Entry.Status)
	}
	text = FormatItemJourney(j)
	if !strings.Contains(text, "待处理（接班人尚未处理）") {
		t.Fatalf("重新提交后应显示待处理、接班人尚未处理：\n%s", text)
	}
	if strings.Contains(text, "当前结果：确认接收") || strings.Contains(text, "当前结果：继续跟踪") {
		t.Fatalf("重新提交本身不能显示成已接收：\n%s", text)
	}
	// 上一轮退回人保留在历史里。
	if !strings.Contains(text, "第1次退回 操作人=李四") {
		t.Fatalf("上一轮退回人应保留在历史里：\n%s", text)
	}
	// 重新提交不改变事项当前班次。
	if j.Item.CurrentShiftID != a.ID {
		t.Fatalf("重新提交不应改变事项当前班次：%s", j.Item.CurrentShiftID)
	}
}

// TestItemJourneyNoHandover：尚未参与交接的事项显示暂无交接记录，
// 建立、修改、关闭记录仍然保留。
func TestItemJourneyNoHandover(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	it, err := f.svc.AddItem(a.ID, "本班事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.UpdateItem(it.ID, "本班事项（已核实）", SeverityImportant, "", "李四"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem(it.ID, "张三"); err != nil {
		t.Fatalf("close: %v", err)
	}

	j, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if j.HasHandovers || len(j.Results) != 0 {
		t.Fatalf("未参与交接不应有交接结果：%+v", j.Results)
	}
	got := journeyKinds(j)
	if joinStrings(got) != "created,updated,closed" {
		t.Fatalf("建立、修改、关闭记录都应保留：%v", got)
	}
	text := FormatItemJourney(j)
	if !strings.Contains(text, "暂无交接记录") {
		t.Fatalf("未参与交接应显示暂无交接记录：\n%s", text)
	}
	if !strings.Contains(text, "事项建立") || !strings.Contains(text, "关闭 操作人=张三") {
		t.Fatalf("建立与关闭信息不应丢失：\n%s", text)
	}
}

// TestItemJourneyNotFound：查询不存在的事项编号继续报明确错误。
func TestItemJourneyNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ItemJourney("I999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的事项应报 ErrNotFound，got %v", err)
	}
}

// TestItemJourneyLegacyData：此前已经存在的交接记录同样纳入查询；
// 缺少的人名或时间明确标为未记录，不推测补齐；同一时刻下同一交接内保持
// 发起、退回、重新提交、后续处理的先后，不同时区按同一实际时刻比较。
func TestItemJourneyLegacyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	raw := `{
  "shift_seq": 2, "item_seq": 1, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"旧事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T10:30:00+09:00",
     "events":[{"at":"2026-10-02T10:30:00+09:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00",
     "entries":[
       {"item_id":"I001","content":"旧事项","severity":"normal","follow_owner":"李四",
        "status":"confirmed","operator":"","processed_at":"2026-10-02T10:00:00+08:00",
        "rounds":[{"seq":1,"returned_at":"2026-10-02T10:00:00+08:00","return_operator":"","reason":"旧退回原因",
                   "supplement":"旧补充","supplement_operator":"",
                   "supplement_at":"2026-10-02T10:00:00+08:00","resubmitted_at":"2026-10-02T10:00:00+08:00"}]}
     ]}
  ],
  "notes": []
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	svc := NewService(store)

	j, err := svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("legacy journey: %v", err)
	}
	// 事项建立于 10:30+09:00（即 09:30+08:00），早于交接发起 10:00+08:00；
	// 同一交接内同一时刻保持发起、退回、重新提交、确认接收的先后。
	want := []string{"created", "handover-init", "return", "resubmit", "confirm"}
	if got := journeyKinds(j); joinStrings(got) != joinStrings(want) {
		t.Fatalf("旧数据经过顺序不正确：want %v, got %v", want, got)
	}
	text := FormatItemJourney(j)
	for _, want := range []string{"旧退回原因", "旧补充", "操作人=未记录", "补充人=未记录", "确认接收"} {
		if !strings.Contains(text, want) {
			t.Fatalf("旧数据展示缺少 %q：\n%s", want, text)
		}
	}

	// 缺少时间的旧记录明确标为未记录，且排在已记录时间之后。
	raw2 := `{
  "shift_seq": 2, "item_seq": 1, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"旧事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"0001-01-01T00:00:00Z",
     "entries":[
       {"item_id":"I001","content":"旧事项","severity":"normal","follow_owner":"李四",
        "status":"returned",
        "rounds":[{"seq":1,"returned_at":"0001-01-01T00:00:00Z","return_operator":"","reason":"无时间退回"}]}
     ]}
  ],
  "notes": []
}`
	if err := os.WriteFile(path, []byte(raw2), 0o644); err != nil {
		t.Fatalf("write legacy2: %v", err)
	}
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy2: %v", err)
	}
	j2, err := NewService(store2).ItemJourney("I001")
	if err != nil {
		t.Fatalf("legacy2 journey: %v", err)
	}
	if got := journeyKinds(j2); joinStrings(got) != "created,handover-init,return" {
		t.Fatalf("缺时间的记录应排在已记录时间之后：%v", got)
	}
	if j2.Events[1].TimeKnown || j2.Events[2].TimeKnown {
		t.Fatalf("缺时间的旧记录不应推测补齐：%+v", j2.Events)
	}
	text2 := FormatItemJourney(j2)
	if !strings.Contains(text2, "未记录 交接 H001") || !strings.Contains(text2, "无时间退回") {
		t.Fatalf("缺少时间应明确标为未记录：\n%s", text2)
	}
	if !strings.Contains(text2, "等待交班人补充") {
		t.Fatalf("旧退回记录同样纳入当前结果：\n%s", text2)
	}
}

// TestItemJourneyProcessedMissingTime：旧数据中已确认接收、继续跟踪或退回、
// 但未记录处理时间（缺失或零值）的交接事项，当前结果仍显示已保存的处理结果，
// 处理人有记录显示原姓名、缺失显示未记录，处理时间一律显示未记录，不能显示成
// 尚未处理；处理经过保留对应事件、交接编号与两班关系，时间标为未记录并排在
// 有真实时间的事件之后，不出现公元元年的日期。真正待处理的事项仍显示尚未处理。
func TestItemJourneyProcessedMissingTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	raw := `{
  "shift_seq": 2, "item_seq": 4, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"已确认缺时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"跟踪零值时间","severity":"important","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"退回缺时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00",
     "events":[{"at":"2026-10-02T09:20:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"真正待处理","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:30:00+08:00",
     "events":[{"at":"2026-10-02T09:30:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00",
     "entries":[
       {"item_id":"I001","content":"已确认缺时间","severity":"normal","follow_owner":"李四",
        "status":"confirmed","operator":"王五"},
       {"item_id":"I002","content":"跟踪零值时间","severity":"important","follow_owner":"李四",
        "status":"tracking","operator":"","processed_at":"0001-01-01T00:00:00Z",
        "tracking_note":"继续观察","follow_owner":"钱七"},
       {"item_id":"I003","content":"退回缺时间","severity":"normal","follow_owner":"李四",
        "status":"returned","operator":"赵六","processed_at":"0001-01-01T00:00:00Z",
        "rounds":[{"seq":1,"returned_at":"2026-10-02T11:00:00+08:00","return_operator":"赵六","reason":"信息不全"}]},
       {"item_id":"I004","content":"真正待处理","severity":"normal","follow_owner":"李四",
        "status":"pending"}
     ]}
  ],
  "notes": []
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	svc := NewService(store)

	// 已确认但缺处理时间：结果仍是确认接收，处理人照录，处理时间未记录。
	j1, err := svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("journey I001: %v", err)
	}
	if got := journeyKinds(j1); joinStrings(got) != "created,handover-init,confirm" {
		t.Fatalf("缺时间的确认事件应保留并排在有真实时间的事件之后：%v", got)
	}
	if j1.Events[2].TimeKnown {
		t.Fatalf("缺处理时间的确认事件不应推测时间：%+v", j1.Events[2])
	}
	text1 := FormatItemJourney(j1)
	if !strings.Contains(text1, "当前结果：确认接收；处理人=王五；处理时间=未记录") {
		t.Fatalf("已确认缺时间应显示确认接收、原处理人与未记录时间：\n%s", text1)
	}
	if strings.Contains(text1, "尚未处理") || strings.Contains(text1, "0001-01-01") {
		t.Fatalf("已处理事项不能显示成尚未处理或公元元年：\n%s", text1)
	}

	// 继续跟踪但处理人为空、处理时间为零值：两者各自标为未记录，
	// 跟踪说明与当时指定的负责人仍按原记录展示。
	j2, err := svc.ItemJourney("I002")
	if err != nil {
		t.Fatalf("journey I002: %v", err)
	}
	if got := journeyKinds(j2); joinStrings(got) != "created,handover-init,track" {
		t.Fatalf("零值时间的跟踪事件应保留并排在有真实时间的事件之后：%v", got)
	}
	if j2.Events[2].TimeKnown {
		t.Fatalf("零值处理时间应视为未记录：%+v", j2.Events[2])
	}
	text2 := FormatItemJourney(j2)
	if !strings.Contains(text2, "当前结果：继续跟踪（已接收）；处理人=未记录；处理时间=未记录") {
		t.Fatalf("零值时间的跟踪结果应显示未记录：\n%s", text2)
	}
	if !strings.Contains(text2, "跟踪说明=继续观察") || !strings.Contains(text2, "后续负责人=钱七") {
		t.Fatalf("跟踪说明与当时指定的负责人应按原记录展示：\n%s", text2)
	}
	if strings.Contains(text2, "0001-01-01") {
		t.Fatalf("零值时间不能显示成公元元年：\n%s", text2)
	}

	// 已退回但处理时间为零值：仍显示等待交班人补充，处理人照录、时间未记录。
	j3, err := svc.ItemJourney("I003")
	if err != nil {
		t.Fatalf("journey I003: %v", err)
	}
	text3 := FormatItemJourney(j3)
	if !strings.Contains(text3, "当前结果：退回（等待交班人补充）；处理人=赵六；处理时间=未记录") {
		t.Fatalf("零值时间的退回结果应显示未记录：\n%s", text3)
	}
	if !strings.Contains(text3, "信息不全") {
		t.Fatalf("退回经过应照常保留：\n%s", text3)
	}

	// 真正待处理的事项仍显示接班人尚未处理。
	j4, err := svc.ItemJourney("I004")
	if err != nil {
		t.Fatalf("journey I004: %v", err)
	}
	text4 := FormatItemJourney(j4)
	if !strings.Contains(text4, "当前结果：待处理（接班人尚未处理）；处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("真正待处理的事项仍应显示尚未处理：\n%s", text4)
	}
}

// legacyProcessedMissingTimeRaw 构造一份旧数据：同一交接中包含已确认缺处理时间、
// 继续跟踪（处理人缺失且时间零值）、退回（时间零值但退回轮次时间真实）以及真正
// 待处理四类事项。
const legacyProcessedMissingTimeRaw = `{
  "shift_seq": 2, "item_seq": 4, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"已确认缺时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"跟踪零值时间","severity":"important","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"退回缺时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00",
     "events":[{"at":"2026-10-02T09:20:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"真正待处理","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:30:00+08:00",
     "events":[{"at":"2026-10-02T09:30:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00",
     "entries":[
       {"item_id":"I001","content":"已确认缺时间","severity":"normal","follow_owner":"李四",
        "status":"confirmed","operator":"王五"},
       {"item_id":"I002","content":"跟踪零值时间","severity":"important","follow_owner":"李四",
        "status":"tracking","operator":"","processed_at":"0001-01-01T00:00:00Z",
        "tracking_note":"继续观察","follow_owner":"钱七"},
       {"item_id":"I003","content":"退回缺时间","severity":"normal","follow_owner":"李四",
        "status":"returned","operator":"赵六","processed_at":"0001-01-01T00:00:00Z",
        "rounds":[{"seq":1,"returned_at":"2026-10-02T11:00:00+08:00","return_operator":"赵六","reason":"信息不全"}]},
       {"item_id":"I004","content":"真正待处理","severity":"normal","follow_owner":"李四",
        "status":"pending"}
     ]}
  ],
  "notes": []
}`

// openLegacyService 把原始 JSON 写入临时文件并打开为服务。
func openLegacyService(t *testing.T, raw string) *Service {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	return NewService(store)
}

// TestLegacyProcessedMissingTimeInHandoverAndShiftQueries：handover-show 与
// shift-show（交班班次、接班班次两个视角）对旧数据中已处理但缺处理人或缺/零值
// 处理时间的事项，必须按已保存的处理结果展示，处理人与处理时间分别独立标为
// 未记录，不能显示成尚未处理，也不出现公元元年日期；继续跟踪保留原跟踪说明与
// 当时指定的后续负责人；退回保留原因与退回历史；真正待处理项仍显示尚未处理。
// 班次报告嵌入的交接清单与末尾结果说明必须一致。
func TestLegacyProcessedMissingTimeInHandoverAndShiftQueries(t *testing.T) {
	svc := openLegacyService(t, legacyProcessedMissingTimeRaw)

	wantFacts := []string{
		// 已确认接收但无处理时间：保留确认结果与原处理人王五，时间未记录。
		"[确认接收]",
		"最后处理：处理人=王五；处理时间=未记录",
		"当前结果：确认接收；处理人=王五；处理时间=未记录",
		// 继续跟踪：处理人、时间均未记录，跟踪说明与后续负责人沿用原值。
		"[继续跟踪]",
		"最后处理：处理人=未记录；处理时间=未记录",
		"当前结果：继续跟踪；处理人=未记录；处理时间=未记录",
		"跟踪说明：继续观察",
		"跟踪后续负责人：钱七",
		// 退回：仍是退回结果、保留原退回人，当前处理时间未记录；退回历史不丢。
		"[退回]",
		"最后处理：处理人=赵六；处理时间=未记录",
		"当前结果：退回；处理人=赵六；处理时间=未记录",
		"第1次退回", "信息不全", "赵六",
		// 真正待处理：仍显示尚未处理。
		"[待处理]",
		"最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理",
		"当前结果：待处理；处理人=尚未处理；处理时间=尚未处理",
	}
	mustNotContain := []string{"0001-01-01", "尚未处理（等待接班人处理）；处理人=王五"}

	h, err := svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	htext := FormatHandover(h)
	for _, want := range wantFacts {
		// [待处理]/当前结果 等部分事实只属于报告文本，交接文本里没有“当前结果”行；
		// 两类文本各自断言共有事实即可。
		if strings.HasPrefix(want, "当前结果：") {
			continue
		}
		if !strings.Contains(htext, want) {
			t.Fatalf("handover-show 缺少 %q：\n%s", want, htext)
		}
	}
	for _, bad := range mustNotContain {
		if strings.Contains(htext, bad) {
			t.Fatalf("handover-show 不应出现 %q：\n%s", bad, htext)
		}
	}

	// 交班班次与接班班次两个视角的报告都应与交接查询得到一致事实。
	for _, shiftID := range []string{"S001", "S002"} {
		rep, err := svc.ShiftReport(shiftID)
		if err != nil {
			t.Fatalf("report %s: %v", shiftID, err)
		}
		rtext := FormatReport(rep)
		for _, want := range wantFacts {
			if !strings.Contains(rtext, want) {
				t.Fatalf("shift-show %s 缺少 %q：\n%s", shiftID, want, rtext)
			}
		}
		for _, bad := range mustNotContain {
			if strings.Contains(rtext, bad) {
				t.Fatalf("shift-show %s 不应出现 %q：\n%s", shiftID, bad, rtext)
			}
		}
	}
}

// TestLegacyReturnedRoundsMissingPeopleAndTime：旧退回历史中缺少退回人/退回时间、
// 补充人/补充时间或重新提交时间（零值）时，交接查询与班次查询都标为未记录，
// 不显示公元元年、不以他人代替；已补充重新提交、当前恢复待处理的事项，当前
// 处理信息显示尚未处理，上一轮退回人与时间仍留在退回历史中（缺失就标未记录）。
func TestLegacyReturnedRoundsMissingPeopleAndTime(t *testing.T) {
	raw := `{
  "shift_seq": 2, "item_seq": 1, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"旧退回事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"0001-01-01T00:00:00Z",
     "entries":[
       {"item_id":"I001","content":"旧退回事项","severity":"normal","follow_owner":"李四",
        "status":"pending",
        "rounds":[{"seq":1,"returned_at":"0001-01-01T00:00:00Z","return_operator":"","reason":"旧退回无时间",
                   "supplement":"旧补充","supplement_operator":"",
                   "supplement_at":"0001-01-01T00:00:00Z","resubmitted_at":"0001-01-01T00:00:00Z"}]}
     ]}
  ],
  "notes": []
}`
	svc := openLegacyService(t, raw)
	wantFacts := []string{
		"[待处理]",
		"最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理",
		"原因=旧退回无时间",
		"操作人=未记录",
		"补充说明：旧补充 补充人=未记录 补充时间=未记录",
		"已重新提交：未记录",
		// 报告末尾结果区的退回历史格式。
		"第1次退回：操作人=未记录 时间=未记录 原因=旧退回无时间",
		"补充：旧补充（补充人=未记录，补充时间=未记录）",
		"当前结果：待处理；处理人=尚未处理；处理时间=尚未处理",
	}

	h, err := svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	htext := FormatHandover(h)
	if !strings.Contains(htext, "第1次退回：未记录 操作人=未记录 原因=旧退回无时间") {
		t.Fatalf("交接查询退回历史缺时间/缺人应标未记录：\n%s", htext)
	}
	if strings.Contains(htext, "0001-01-01") {
		t.Fatalf("交接查询不应出现公元元年日期：\n%s", htext)
	}

	rep, err := svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	rtext := FormatReport(rep)
	for _, want := range wantFacts {
		if !strings.Contains(rtext, want) {
			t.Fatalf("班次查询缺少 %q：\n%s", want, rtext)
		}
	}
	if strings.Contains(rtext, "0001-01-01") {
		t.Fatalf("班次查询不应出现公元元年日期：\n%s", rtext)
	}
}

// legacyUncertainResultRaw 构造一份旧数据：同一交接中 I001 已确认接收（缺处理人
// 与处理时间，仍算接收）、I002 处理结果缺失（空）、I003 处理结果无法识别；
// 交接记录里甚至已写入完成时间，也不能掩盖清单中的不确定结果。
const legacyUncertainResultRaw = `{
  "shift_seq": 2, "item_seq": 3, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true,"closed_at":"2026-10-02T16:00:00+08:00"},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"已确认旧事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"结果缺失事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"结果无法识别事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00",
     "events":[{"at":"2026-10-02T09:20:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00","completed_at":"2026-10-02T15:00:00+08:00",
     "entries":[
       {"item_id":"I001","content":"已确认旧事项","severity":"normal","follow_owner":"李四",
        "status":"confirmed"},
       {"item_id":"I002","content":"结果缺失事项","severity":"normal","follow_owner":"李四",
        "status":""},
       {"item_id":"I003","content":"结果无法识别事项","severity":"normal","follow_owner":"李四",
        "status":"archived"}
     ]}
  ],
  "notes": []
}`

// TestUncertainEntryResultKeepsHandoverIncomplete：清单中存在结果缺失或无法识别
// 的事项时，即使其他项已接收、记录里已有完成时间，交接也显示未完成；
// 缺失结果显示“处理结果未记录”，无法识别的结果显示“处理结果无法识别”并带出原值。
func TestUncertainEntryResultKeepsHandoverIncomplete(t *testing.T) {
	svc := openLegacyService(t, legacyUncertainResultRaw)

	h, err := svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if h.Completed() {
		t.Fatalf("存在结果缺失/无法识别事项时交接不应完成")
	}
	htext := FormatHandover(h)
	if !strings.Contains(htext, "未完成") || strings.Contains(htext, "已完成") {
		t.Fatalf("已有完成时间也不能显示成已完成：\n%s", htext)
	}
	if !strings.Contains(htext, "[确认接收]") {
		t.Fatalf("已确认项仍应显示确认接收：\n%s", htext)
	}
	if !strings.Contains(htext, "[处理结果未记录]") {
		t.Fatalf("缺失结果应显示处理结果未记录：\n%s", htext)
	}
	if !strings.Contains(htext, "处理结果无法识别（原值：archived）") {
		t.Fatalf("无法识别结果应显示处理结果无法识别并带出原值：\n%s", htext)
	}
}

// TestUncertainEntryResultBlocksShiftClose：接班班次收到的交接中存在结果缺失或
// 无法识别的事项时，结束班次明确失败，班次保持进行中，不写结束时间与结束时记录，
// 原交接结果、事项归属与处理经过不变；逐项处理修复后才能结束。
func TestUncertainEntryResultBlocksShiftClose(t *testing.T) {
	svc := openLegacyService(t, legacyUncertainResultRaw)
	svc.nowAt(func() time.Time { return tsDay(2, 20, 0) })

	snapshot := func() string {
		rep, err := svc.ShiftReport("S002")
		if err != nil {
			t.Fatalf("report: %v", err)
		}
		raw, err := json.Marshal(rep)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	before := snapshot()

	if _, err := svc.CloseShift("S002"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("存在未确认接收事项时结束应报 ErrHandoverState，got %v", err)
	}
	sh, err := svc.GetShift("S002")
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if sh.Closed || sh.ClosedAt != nil || sh.CloseRecord != nil {
		t.Fatalf("结束失败应保持进行中，不写结束时间与结束时记录：%+v", sh)
	}
	if after := snapshot(); after != before {
		t.Fatalf("拒绝结束不得改变交接结果、事项归属与处理经过\nbefore %s\nafter  %s", before, after)
	}

	// 缺失结果与无法识别结果都可以按现有逐项处理功能明确接收。
	if _, err := svc.ProcessEntry("H001", "I002", ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("处理结果缺失项应能确认接收：%v", err)
	}
	if _, err := svc.ProcessEntry("H001", "I003", ActionTrack, "李四", "", "继续跟进", "王五"); err != nil {
		t.Fatalf("无法识别结果项应能继续跟踪：%v", err)
	}
	h, err := svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	// I001 是已明确接收的旧记录，缺少处理人与处理时间也仍算接收，不需要重新处理。
	if !h.Completed() || h.CompletedAt == nil {
		t.Fatalf("全部明确接收后交接应完成：%+v", h)
	}
	if _, err := svc.CloseShift("S002"); err != nil {
		t.Fatalf("全部明确接收后应能结束班次：%v", err)
	}
}

// TestUncertainResultConsistentAcrossQueries：交接查询、交班与接班两侧的班次报告、
// 事项处理经过对结果缺失/无法识别的事项表达同一事实，不展示成已确认，也不从事项
// 所在班次、处理人、处理时间或退回历史推测接收结果。
func TestUncertainResultConsistentAcrossQueries(t *testing.T) {
	svc := openLegacyService(t, legacyUncertainResultRaw)

	for _, shiftID := range []string{"S001", "S002"} {
		rep, err := svc.ShiftReport(shiftID)
		if err != nil {
			t.Fatalf("report %s: %v", shiftID, err)
		}
		rtext := FormatReport(rep)
		if !strings.Contains(rtext, "事项 I002 交接 H001（S001 -> S002）当前结果：处理结果未记录") {
			t.Fatalf("shift-show %s 应把缺失结果显示为处理结果未记录：\n%s", shiftID, rtext)
		}
		if !strings.Contains(rtext, "事项 I003 交接 H001（S001 -> S002）当前结果：处理结果无法识别（原值：archived）") {
			t.Fatalf("shift-show %s 应把无法识别结果显示原值：\n%s", shiftID, rtext)
		}
		if !strings.Contains(rtext, "[处理结果未记录]") || !strings.Contains(rtext, "处理结果无法识别（原值：archived）") {
			t.Fatalf("shift-show %s 嵌入的交接清单应与结果说明一致：\n%s", shiftID, rtext)
		}
		if strings.Contains(rtext, "I002 交接 H001（S001 -> S002）当前结果：确认接收") ||
			strings.Contains(rtext, "I003 交接 H001（S001 -> S002）当前结果：确认接收") {
			t.Fatalf("shift-show %s 不得把不确定结果展示成已确认：\n%s", shiftID, rtext)
		}
	}

	// 事项处理经过：不确定结果不产生接收事件，当前结果如实展示。
	j2, err := svc.ItemJourney("I002")
	if err != nil {
		t.Fatalf("journey I002: %v", err)
	}
	if got := journeyKinds(j2); joinStrings(got) != "created,handover-init" {
		t.Fatalf("结果缺失不应推测出接收事件：%v", got)
	}
	text2 := FormatItemJourney(j2)
	if !strings.Contains(text2, "当前结果：处理结果未记录") {
		t.Fatalf("item-show 应显示处理结果未记录：\n%s", text2)
	}
	if strings.Contains(text2, "确认接收") || strings.Contains(text2, "继续跟踪") {
		t.Fatalf("item-show 不得把缺失结果展示成已接收：\n%s", text2)
	}

	j3, err := svc.ItemJourney("I003")
	if err != nil {
		t.Fatalf("journey I003: %v", err)
	}
	if got := journeyKinds(j3); joinStrings(got) != "created,handover-init" {
		t.Fatalf("无法识别结果不应推测出接收事件：%v", got)
	}
	text3 := FormatItemJourney(j3)
	if !strings.Contains(text3, "当前结果：处理结果无法识别（原值：archived）") {
		t.Fatalf("item-show 应显示处理结果无法识别并带出原值：\n%s", text3)
	}
}

// TestUncertainResultInOneHandoverBlocksShiftClose：同一班次收到多条交接时，
// 其他交接全部完成也不能放行存在不确定结果的交接。
func TestUncertainResultInOneHandoverBlocksShiftClose(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 12, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 12, 0), tsDay(2, 16, 0), "")
	c := mustShift(t, f, "调度", "王五", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	itA, err := f.svc.AddItem(a.ID, "甲班事项", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add a: %v", err)
	}
	itB, err := f.svc.AddItem(b.ID, "乙班事项", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add b: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	h1, err := f.svc.CreateHandover(a.ID, c.ID)
	if err != nil {
		t.Fatalf("handover a->c: %v", err)
	}
	h2, err := f.svc.CreateHandover(b.ID, c.ID)
	if err != nil {
		t.Fatalf("handover b->c: %v", err)
	}
	// h1 正常确认完成；h2 的清单被旧数据写成无法识别的结果。
	if _, err := f.svc.ProcessEntry(h1.ID, itA.ID, ActionConfirm, "王五", "", "", ""); err != nil {
		t.Fatalf("confirm h1: %v", err)
	}
	for i := range f.store.data.Handovers {
		if f.store.data.Handovers[i].ID == h2.ID {
			f.store.data.Handovers[i].Entries[0].Status = EntryStatus("archived")
		}
	}
	if got := mustGetHandover(t, f, h2.ID); got.Completed() {
		t.Fatalf("无法识别结果应使交接未完成")
	}

	if _, err := f.svc.CloseShift(c.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("其他交接已完成也不能放行不确定结果，got %v", err)
	}
	sh, _ := f.svc.GetShift(c.ID)
	if sh.Closed {
		t.Fatalf("班次应保持进行中")
	}

	// 明确接收后两条交接都完成，班次可以结束。
	if _, err := f.svc.ProcessEntry(h2.ID, itB.ID, ActionConfirm, "王五", "", "", ""); err != nil {
		t.Fatalf("修复不确定结果：%v", err)
	}
	if _, err := f.svc.CloseShift(c.ID); err != nil {
		t.Fatalf("全部交接完成后应能结束：%v", err)
	}
}

// reopenService 在同一路径上重新打开服务，模拟退出后重开。
func reopenService(t *testing.T, svc *Service) *Service {
	t.Helper()
	store, err := Open(svc.store.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return NewService(store)
}

// TestCompletingAfterUncertainEntriesOverwritesStaleCompletedAt：旧记录里已有
// 15:00 的完成时间，但清单中仍有结果缺失与无法识别事项。接班人 18:00 补齐其中
// 一项时交接仍未完成（另一项保留原异常提示，旧时间不改写单项结果）；19:00 成功
// 接收最后一项后整份交接才完成，完成时间必须保存为 19:00 这次成功处理的时刻，
// 不再沿用旧的 15:00；已明确接收但缺处理人/处理时间的旧事项不要求重新处理，
// 其结果与历史不被改写。交班、接班两侧班次报告与嵌入清单展示同一结果与时间。
func TestCompletingAfterUncertainEntriesOverwritesStaleCompletedAt(t *testing.T) {
	svc := openLegacyService(t, legacyUncertainResultRaw)

	stale := tsDay(2, 15, 0)
	at18 := tsDay(2, 18, 0)
	at19 := tsDay(2, 19, 0)
	svc.nowAt(func() time.Time { return at18 })

	// 18:00 补齐“处理结果未记录”的 I002：交接仍未完成，另一项保留异常提示。
	if _, err := svc.ProcessEntry("H001", "I002", ActionConfirm, "接班人王五", "", "", ""); err != nil {
		t.Fatalf("18:00 确认 I002：%v", err)
	}
	h, _ := svc.GetHandover("H001")
	if h.Completed() {
		t.Fatalf("仍有无法识别事项时交接应保持未完成")
	}
	if h.CompletedAt == nil || !h.CompletedAt.Equal(stale) {
		t.Fatalf("尚未完成时不应改动旧记录：完成时间=%v，仍应保存旧值 %s", h.CompletedAt, stale)
	}
	e3 := findEntryOf(t, h, "I003")
	if e3.Status.Label() != "处理结果无法识别（原值：archived）" {
		t.Fatalf("未处理的异常项应保留原提示：%s", e3.Status.Label())
	}
	// I001 是已明确接收的旧记录（缺处理人/处理时间），不要求重新处理且原样保留。
	e1 := findEntryOf(t, h, "I001")
	if !e1.Status.Received() || e1.Operator != "" || e1.ProcessedAt != nil {
		t.Fatalf("已接收旧事项不应被改写：%+v", e1)
	}

	// 19:00 以“继续跟踪”接收最后一项，整份交接完成，完成时间取本次时刻。
	svc.nowAt(func() time.Time { return at19 })
	if _, err := svc.ProcessEntry("H001", "I003", ActionTrack, "接班人王五", "", "继续跟踪到闭环", "赵六"); err != nil {
		t.Fatalf("19:00 继续跟踪 I003：%v", err)
	}
	h, _ = svc.GetHandover("H001")
	if !h.Completed() {
		t.Fatalf("全部事项明确接收后应完成")
	}
	if h.CompletedAt == nil || !h.CompletedAt.Equal(at19) {
		t.Fatalf("完成时间应为本次成功处理时刻 19:00，got %v", h.CompletedAt)
	}
	if text := FormatHandover(h); strings.Contains(text, "15:00:00") ||
		!strings.Contains(text, "已完成 2026-10-02 19:00:00 +08:00") {
		t.Fatalf("交接查询应显示 19:00 完成且不得再出现旧时间：\n%s", text)
	}

	// 单项结果与处理经过不得因修正整份交接的完成时间而被改写。
	e1 = findEntryOf(t, h, "I001")
	if !e1.Status.Received() || e1.Operator != "" || e1.ProcessedAt != nil || len(e1.Rounds) != 0 {
		t.Fatalf("I001 旧接收结果与历史不得被改写：%+v", e1)
	}
	e2 := findEntryOf(t, h, "I002")
	if e2.Status != EntryConfirmed || e2.Operator != "接班人王五" || e2.ProcessedAt == nil ||
		!e2.ProcessedAt.Equal(at18) {
		t.Fatalf("I002 应保留 18:00 的确认经过：%+v", e2)
	}
	e3 = findEntryOf(t, h, "I003")
	if e3.Status != EntryTracking || e3.TrackingNote != "继续跟踪到闭环" || e3.FollowOwner != "赵六" ||
		e3.ProcessedAt == nil || !e3.ProcessedAt.Equal(at19) {
		t.Fatalf("I003 应保留 19:00 的继续跟踪经过：%+v", e3)
	}

	// 退出重开后完成时间与全部单项经过仍一致。
	svc = reopenService(t, svc)
	h, _ = svc.GetHandover("H001")
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(at19) {
		t.Fatalf("重开后完成时间应仍为 19:00：%v", h.CompletedAt)
	}

	// 交班、接班两侧班次报告及嵌入的交接清单展示同一完成结果与时间。
	for _, shiftID := range []string{"S001", "S002"} {
		rep, err := svc.ShiftReport(shiftID)
		if err != nil {
			t.Fatalf("report %s: %v", shiftID, err)
		}
		text := FormatReport(rep)
		if strings.Contains(text, "15:00:00") {
			t.Fatalf("班次报告 %s 不得再出现旧完成时间：\n%s", shiftID, text)
		}
		if !strings.Contains(text, "已完成 2026-10-02 19:00:00 +08:00") {
			t.Fatalf("班次报告 %s 应显示 19:00 完成（含嵌入交接清单）：\n%s", shiftID, text)
		}
	}

	// 已接收项不能重复处理，重复处理不改写完成时间。
	if _, err := svc.ProcessEntry("H001", "I003", ActionConfirm, "接班人王五", "", "", ""); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已接收项重复处理应报 ErrHandoverState，got %v", err)
	}
	h, _ = svc.GetHandover("H001")
	if h.CompletedAt == nil || !h.CompletedAt.Equal(at19) {
		t.Fatalf("重复处理失败后完成时间应保持 19:00：%v", h.CompletedAt)
	}
}

// TestCompletingWithZeroOrMissingStaleCompletedAt：旧完成时间为零值时，补齐不
// 确定事项后按这次实际完成的操作记录完成时间（带时区展示），不保留公元元年值。
func TestCompletingWithZeroOrMissingStaleCompletedAt(t *testing.T) {
	raw := strings.Replace(legacyUncertainResultRaw,
		`"completed_at":"2026-10-02T15:00:00+08:00"`,
		`"completed_at":"0001-01-01T00:00:00Z"`, 1)
	svc := openLegacyService(t, raw)
	at19 := tsDay(2, 19, 0)
	svc.nowAt(func() time.Time { return tsDay(2, 18, 0) })
	if _, err := svc.ProcessEntry("H001", "I002", ActionConfirm, "接班人王五", "", "", ""); err != nil {
		t.Fatalf("confirm I002: %v", err)
	}
	svc.nowAt(func() time.Time { return at19 })
	if _, err := svc.ProcessEntry("H001", "I003", ActionConfirm, "接班人王五", "", "", ""); err != nil {
		t.Fatalf("confirm I003: %v", err)
	}
	h, _ := svc.GetHandover("H001")
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(at19) {
		t.Fatalf("旧完成时间为零值时应记录本次完成时刻 19:00：%v", h.CompletedAt)
	}
	if text := FormatHandover(h); !strings.Contains(text, "已完成 2026-10-02 19:00:00 +08:00") {
		t.Fatalf("应按带时区方式展示新完成时间：\n%s", text)
	}
}

// TestAllReceivedWithoutCompletedAtIsNotFabricated：旧记录中各项都已明确接收但
// 缺少完成时间时，任何查询都只显示已完成、时间未记录，不凭空编造时刻，也不能
// 通过逐项处理补写（已接收项一律拒绝处理）。
func TestAllReceivedWithoutCompletedAtIsNotFabricated(t *testing.T) {
	raw := legacyUncertainResultRaw
	raw = strings.Replace(raw,
		`,"completed_at":"2026-10-02T15:00:00+08:00"`, "", 1)
	raw = strings.Replace(raw, `"status":""`, `"status":"confirmed"`, 1)
	raw = strings.Replace(raw, `"status":"archived"`,
		`"status":"tracking","operator":"李四","processed_at":"2026-10-02T14:00:00+08:00","tracking_note":"继续观察","follow_owner":"王五"`, 1)
	svc := openLegacyService(t, raw)

	h, err := svc.GetHandover("H001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !h.Completed() || h.CompletedAt != nil {
		t.Fatalf("全部已接收缺完成时间：应判定完成且保持无时间，got %v", h.CompletedAt)
	}
	for _, id := range []string{"I001", "I002", "I003"} {
		if _, err := svc.ProcessEntry("H001", id, ActionConfirm, "王五", "", "", ""); !errors.Is(err, ErrHandoverState) {
			t.Fatalf("已接收项 %s 不能重新处理，got %v", id, err)
		}
	}
	h, _ = svc.GetHandover("H001")
	if h.CompletedAt != nil {
		t.Fatalf("处理被拒绝后仍不得编造完成时间：%v", h.CompletedAt)
	}
	if text := FormatHandover(h); !strings.Contains(text, "已完成 未记录") {
		t.Fatalf("缺少完成时间应显示未记录：\n%s", text)
	}
	for _, shiftID := range []string{"S001", "S002"} {
		rep, _ := svc.ShiftReport(shiftID)
		if text := FormatReport(rep); !strings.Contains(text, "已完成 未记录") {
			t.Fatalf("班次报告 %s 同样不得编造完成时间：\n%s", shiftID, text)
		}
	}
	j, err := svc.ItemJourney("I002")
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	for _, ev := range j.Events {
		if ev.Kind == "confirm" && ev.TimeKnown {
			t.Fatalf("缺少完成时间不得在处理经过中编造时刻：%+v", ev)
		}
	}
}

// TestFailedFinalProcessingDoesNotCompleteOrRewriteTime：最后一项因缺少必填信息
// 处理失败时，该项保持原异常结果、交接不能提前完成，旧完成时间也不被改动；
// 随后成功处理才以成功时刻完成。
func TestFailedFinalProcessingDoesNotCompleteOrRewriteTime(t *testing.T) {
	svc := openLegacyService(t, legacyUncertainResultRaw)
	stale := tsDay(2, 15, 0)
	at19 := tsDay(2, 19, 0)
	svc.nowAt(func() time.Time { return at19 })

	// 先只补齐 I002，I003 仍无法识别。
	if _, err := svc.ProcessEntry("H001", "I002", ActionConfirm, "接班人王五", "", "", ""); err != nil {
		t.Fatalf("confirm I002: %v", err)
	}
	// 对 I003 继续跟踪但缺少跟踪说明与后续负责人：失败，任何状态不变。
	if _, err := svc.ProcessEntry("H001", "I003", ActionTrack, "接班人王五", "", "   ", "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺少必填信息应报 ErrInvalidInput，got %v", err)
	}
	h, _ := svc.GetHandover("H001")
	if h.Completed() {
		t.Fatalf("最后一项处理失败时交接不能提前完成")
	}
	if h.CompletedAt == nil || !h.CompletedAt.Equal(stale) {
		t.Fatalf("失败处理不得改动旧完成时间：%v", h.CompletedAt)
	}
	e3 := findEntryOf(t, h, "I003")
	if string(e3.Status) != "archived" || e3.Operator != "" || e3.ProcessedAt != nil {
		t.Fatalf("失败后该项应保持原异常结果：%+v", e3)
	}

	// 重新带齐必填信息成功处理，交接以 19:00 完成。
	if _, err := svc.ProcessEntry("H001", "I003", ActionTrack, "接班人王五", "", "补充跟踪说明", "赵六"); err != nil {
		t.Fatalf("track I003: %v", err)
	}
	h, _ = svc.GetHandover("H001")
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(at19) {
		t.Fatalf("成功处理后应以 19:00 完成：%v", h.CompletedAt)
	}
}
