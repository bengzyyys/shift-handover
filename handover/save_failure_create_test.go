package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// 本文件为“首次发起交接在本地数据保存阶段失败”建立可重复的回归保障：
// 交班班次已结束、接班班次符合首次发起的全部条件、且该交班班次此前没有
// 发起过交接，业务校验已全部通过、真正进入保存过程后本地写盘失败。
// 沿用既有保存失败测试的同一套故障注入方式（在 .tmp 路径放目录使写入必然
// 失败）。此时发起操作必须：
//   - 明确返回保存阶段的错误（保留原错误信息与失败步骤），不能改报成班次
//     不符合条件、交接已存在或发起成功；
//   - 同时返回零值交接结果：无编号、无岗位、无两班编号、发起时间为零值、
//     完成时间为空、清单没有事项，不能交出未保存的编号、发起时间、清单，
//     空清单时也不能连同完成时间一起交出，更不能用旧记录或部分清单充当；
//   - 内存与磁盘都回到失败前：交接列表与交班班次报告仍显示没有为该交班
//     班次建立交接，事项留在原班次，交班班次结束时记录与已保存的其他交接
//     不受影响，失败尝试不占用交接编号、不留下已指定接班对象的关系。
// 带未关闭事项与没有未关闭事项（空清单）的首次发起都遵守同一规则。

// assertZeroHandover 核对发起操作在失败时交出的是零值交接：编号、岗位、
// 两班编号为空，发起时间为零值，完成时间为空，清单没有事项。
func assertZeroHandover(t *testing.T, got Handover) {
	t.Helper()
	if got.ID != "" || got.Position != "" || got.FromShiftID != "" || got.ToShiftID != "" {
		t.Fatalf("保存失败不得返回未保存交接的编号、岗位或两班编号：%+v", got)
	}
	if !got.CreatedAt.IsZero() {
		t.Fatalf("保存失败返回的发起时间应为零值，got %v", got.CreatedAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("保存失败不得返回完成时间（空清单也一样），got %v", got.CompletedAt)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("保存失败返回的清单应没有事项，got %+v", got.Entries)
	}
}

// assertNoHandoverForFromShift 核对当前数据中没有为交班班次建立交接：
// 列表里没有任何来自该班的交接，班次报告的交班对象为空，接班班次也没有
// 收到接班交接；期望数量只含与本交班班次无关的其他已保存交接。
func assertNoHandoverForFromShift(t *testing.T, f *fixture, fromID, toID string, wantCount int) {
	t.Helper()
	hs := f.svc.ListHandovers()
	if len(hs) != wantCount {
		t.Fatalf("失败尝试不应新增交接，期望仍为 %d 条，got %d：%+v", wantCount, len(hs), hs)
	}
	for _, h := range hs {
		if h.FromShiftID == fromID {
			t.Fatalf("不应为交班班次 %s 留下交接：%+v", fromID, h)
		}
		if h.ToShiftID == toID && h.FromShiftID == fromID {
			t.Fatalf("不应留下已指定接班对象 %s 的关系：%+v", toID, h)
		}
	}
	repFrom, err := f.svc.ShiftReport(fromID)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	if repFrom.Outgoing != nil {
		t.Fatalf("交班班次报告不应出现交班交接：%+v", repFrom.Outgoing)
	}
	repTo, err := f.svc.ShiftReport(toID)
	if err != nil {
		t.Fatalf("report to shift: %v", err)
	}
	for _, h := range repTo.Incoming {
		if h.FromShiftID == fromID {
			t.Fatalf("接班班次报告不应出现来自 %s 的接班交接：%+v", fromID, h)
		}
	}
}

// assertItemsStillInClosedFromShift 核对交班班次的未关闭事项在失败尝试后
// 仍留在原班次：当前班次仍是交班班次、流经班次不增加接班班次、事项历史不
// 增加接收记录；交班班次结束时记录中原事项内容与负责人保持不变。
func assertItemsStillInClosedFromShift(t *testing.T, f *fixture, fromID string, items []Item, failAt time.Time) {
	t.Helper()
	rep, err := f.svc.ShiftReport(fromID)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	for _, want := range items {
		it, err := f.svc.GetItem(want.ID)
		if err != nil {
			t.Fatalf("get item %s: %v", want.ID, err)
		}
		if it.CurrentShiftID != fromID {
			t.Fatalf("事项 %s 应仍留在交班班次 %s，got %s", want.ID, fromID, it.CurrentShiftID)
		}
		if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != fromID {
			t.Fatalf("事项 %s 流经班次不应增加接班班次：%v", want.ID, it.ShiftIDs)
		}
		if it.Closed {
			t.Fatalf("未关闭事项 %s 不能因失败尝试变成已关闭", want.ID)
		}
		for _, ev := range it.Events {
			if ev.Kind == "received" {
				t.Fatalf("失败尝试不应在事项 %s 历史留下接收记录：%+v", want.ID, ev)
			}
			if !ev.At.IsZero() && ev.At.Equal(failAt) {
				t.Fatalf("失败尝试时刻 %s 不应写入事项 %s 历史", failAt, want.ID)
			}
		}
		// 事项处理经过不应出现任何交接。
		j, err := f.svc.ItemJourney(want.ID)
		if err != nil {
			t.Fatalf("journey %s: %v", want.ID, err)
		}
		if j.HasHandovers || len(j.Results) != 0 {
			t.Fatalf("事项 %s 不应参与任何交接：%+v", want.ID, j.Results)
		}
		for _, ev := range j.Events {
			if strings.Contains(ev.Kind, "handover") || ev.Kind == "confirm" ||
				ev.Kind == "track" || ev.Kind == "return" {
				t.Fatalf("事项 %s 处理经过不应出现交接相关事件：%+v", want.ID, ev)
			}
		}
		// 交班班次结束时记录中原事项保持发起失败前的内容与负责人。
		snap := findCloseItem(rep, want.ID)
		if snap == nil || snap.Content != want.Content || snap.FollowOwner != want.FollowOwner {
			t.Fatalf("交班班次结束时记录中的事项 %s 应保持原样：%+v", want.ID, snap)
		}
	}
}

// TestCreateHandoverSaveFailureWithPendingItemsRollback：交班班次已结束并
// 留有未关闭事项、接班班次符合全部首次发起条件且此前没有交接，真正进入
// 保存过程后本地写盘失败——发起操作必须返回保存错误与零值交接，不能把
// 未保存的编号、发起时间与待处理清单当作结果；内存与磁盘都回到失败前：
// 没有为交班班次建立交接，事项留在原班次，结束时记录不变，失败尝试不占用
// 交接编号。保存恢复后再次发起按现有功能成功，返回完整已保存交接且与随后
// 的查询一致，失败尝试不占用编号（成功记录仍是 H001）。
func TestCreateHandoverSaveFailureWithPendingItemsRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	pending := items[:2] // 事项甲、事项乙未关闭；事项丙结束前已关闭

	failAt := tsDay(2, 17, 0)
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return failAt })

	// 业务条件全部成立，使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	got, err := f.svc.CreateHandover(a.ID, b.ID)
	if err == nil {
		t.Fatalf("保存失败时发起交接必须明确返回错误，不能返回成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误并保留原错误信息，got %v", err)
	}
	// 业务条件全部成立，不能改报成班次不符合条件、交接已存在或其他业务拒绝。
	for _, target := range []error{
		ErrInvalidInput, ErrShiftClosed, ErrShiftNotClosed, ErrOverlap,
		ErrPositionMismatch, ErrHandoverExists, ErrHandoverTarget,
		ErrSameShift, ErrNotFound,
	} {
		if errors.Is(err, target) {
			t.Fatalf("合法首次发起的保存失败不应被报告为业务错误 %v，got %v", target, err)
		}
	}
	assertZeroHandover(t, got)

	// 当前打开的数据里没有建立交接，事项留在原班次，结束时记录不变。
	assertNoHandoverForFromShift(t, f, a.ID, b.ID, 0)
	assertItemsStillInClosedFromShift(t, f, a.ID, pending, failAt)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的发起不能成为其中的业务事实")
	}

	// 退出后重新打开：仍没有交接、事项仍在原班次，失败尝试无任何痕迹。
	f.reopen(t)
	assertNoHandoverForFromShift(t, f, a.ID, b.ID, 0)
	assertItemsStillInClosedFromShift(t, f, a.ID, pending, failAt)

	// 保存恢复正常后再次发起：按现有功能成功，失败尝试不占用编号。
	restoreSaving(t, f)
	successAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("恢复后发起交接应成功：%v", err)
	}
	if h.ID != "H001" {
		t.Fatalf("失败尝试不应占用交接编号，成功记录应为 H001，got %s", h.ID)
	}
	if h.Position != a.Position || h.FromShiftID != a.ID || h.ToShiftID != b.ID {
		t.Fatalf("成功交接的岗位与两班编号应正确：%+v", h)
	}
	if !h.CreatedAt.Equal(successAt) {
		t.Fatalf("发起时间应为成功操作时刻 %s，got %s", successAt, h.CreatedAt)
	}
	if h.CompletedAt != nil || h.Completed() {
		t.Fatalf("有待处理事项的交接发起时不应完成：%+v", h)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("清单应包含2项未关闭事项，got %d", len(h.Entries))
	}
	for _, e := range h.Entries {
		if e.Status != EntryPending {
			t.Fatalf("清单事项初始应为待处理，%s got %s", e.ItemID, e.Status)
		}
	}
	// 返回内容与随后的查询一致。
	stored, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if FormatHandover(h) != FormatHandover(stored) {
		t.Fatalf("返回内容应与随后查询一致\n返回 %+v\n查询 %+v", h, stored)
	}
	// 已关闭事项不进入清单。
	for _, e := range h.Entries {
		if e.ItemID == items[2].ID {
			t.Fatalf("结束前已关闭的事项不应进入清单")
		}
	}
}

// TestCreateHandoverSaveFailureEmptyListRollback：没有未关闭事项（空清单）
// 的首次发起在保存阶段失败时，同样返回保存错误与零值交接，尤其不能把空
// 清单的完成时间连同未保存编号一起交出；恢复后再次发起才是发起即完成的
// 空清单交接。
func TestCreateHandoverSaveFailureEmptyListRollback(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	// 交班班次没有任何事项，结束后发起的将是空清单交接。
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	failAt := tsDay(2, 17, 0)
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return failAt })

	breakSaving(t, f)
	got, err := f.svc.CreateHandover(a.ID, b.ID)
	if err == nil {
		t.Fatalf("保存失败时空清单发起也必须明确返回错误")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrHandoverExists) || errors.Is(err, ErrShiftClosed) ||
		errors.Is(err, ErrShiftNotClosed) {
		t.Fatalf("合法首次发起的保存失败不应改报成业务错误，got %v", err)
	}
	assertZeroHandover(t, got)

	assertNoHandoverForFromShift(t, f, a.ID, b.ID, 0)

	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	f.reopen(t)
	assertNoHandoverForFromShift(t, f, a.ID, b.ID, 0)

	// 恢复后再次发起：空清单交接发起即完成，编号仍是 H001。
	restoreSaving(t, f)
	successAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("恢复后发起空清单交接应成功：%v", err)
	}
	if h.ID != "H001" || len(h.Entries) != 0 {
		t.Fatalf("应为编号 H001 的空清单交接，got %+v", h)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(successAt) {
		t.Fatalf("空清单交接应在成功发起时完成、完成时间为成功时刻：%+v", h)
	}
	stored, _ := f.svc.GetHandover(h.ID)
	if FormatHandover(h) != FormatHandover(stored) {
		t.Fatalf("返回内容应与随后查询一致\n返回 %+v\n查询 %+v", h, stored)
	}
}

// TestCreateHandoverSaveFailureDoesNotDisturbOtherHandover：失败尝试不能
// 影响此前已保存的其他交接，也不能占用编号导致后续交接编号跳号。
func TestCreateHandoverSaveFailureDoesNotDisturbOtherHandover(t *testing.T) {
	f := newFixture(t)
	// 先建立另一条已保存的交接：S001 -> S002（非空）。
	first := newCreateHandoverChain(t, f)

	// 再为第二个交班班次 S003 准备首次发起，但保存失败。
	c := mustShift(t, f, "巡检", "钱七", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	d := mustShift(t, f, "巡检", "孙八", tsDay(3, 16, 0), tsDay(3, 23, 0), "")
	if _, err := f.svc.AddItem(c.ID, "另一岗位事项", SeverityNormal, "", "孙八"); err != nil {
		t.Fatalf("add item: %v", err)
	}
	if _, err := f.svc.CloseShift(c.ID); err != nil {
		t.Fatalf("close c: %v", err)
	}
	failAt := tsDay(3, 17, 0)
	f.svc.nowAt(func() time.Time { return failAt })
	breakSaving(t, f)
	got, err := f.svc.CreateHandover(c.ID, d.ID)
	if err == nil || !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应返回保存阶段错误，got %v", err)
	}
	assertZeroHandover(t, got)

	// 仍只有第一条交接，内容原样保留。
	assertNoHandoverForFromShift(t, f, c.ID, d.ID, 1)
	existing, err := f.svc.GetHandover(first.h.ID)
	if err != nil {
		t.Fatalf("get existing handover: %v", err)
	}
	if existing.ID != first.h.ID || len(existing.Entries) != len(first.h.Entries) ||
		existing.FromShiftID != first.a.ID || existing.ToShiftID != first.b.ID {
		t.Fatalf("已保存的其他交接不应受失败尝试影响：\nwant %+v\ngot  %+v", first.h, existing)
	}

	// 恢复后为 S003 发起：失败尝试不占用编号，新交接是 H002 而不是 H003。
	restoreSaving(t, f)
	successAt := tsDay(3, 18, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	h2, err := f.svc.CreateHandover(c.ID, d.ID)
	if err != nil {
		t.Fatalf("恢复后发起应成功：%v", err)
	}
	if h2.ID != "H002" {
		t.Fatalf("失败尝试不应占用交接编号，新交接应为 H002，got %s", h2.ID)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 2 {
		t.Fatalf("应保存两条交接，got %d：%+v", len(hs), hs)
	}
}

// createHandoverChain 收集一条已建立并成功保存的交接链路。
type createHandoverChain struct {
	a, b Shift
	h    Handover
}

// newCreateHandoverChain 建立一个已结束交班班次（含未关闭事项）向进行中
// 接班班次的非空交接并成功保存。
func newCreateHandoverChain(t *testing.T, f *fixture) createHandoverChain {
	t.Helper()
	a, b, _ := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	return createHandoverChain{a: a, b: b, h: h}
}

// TestCreateHandoverSaveFailureThenRepeatBehavesAsBefore：保存失败没有建立
// 交接，因此恢复后向原接班班次发起是全新首次发起而不是“重复发起”；成功
// 后再次发起则保留既有重复发起行为：返回原记录与 ErrHandoverExists。
func TestCreateHandoverSaveFailureThenRepeatBehavesAsBefore(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)

	f.svc.nowAt(func() time.Time { return tsDay(2, 17, 0) })
	breakSaving(t, f)
	got, err := f.svc.CreateHandover(a.ID, b.ID)
	if err == nil || !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应返回保存阶段错误，got %v", err)
	}
	assertZeroHandover(t, got)
	restoreSaving(t, f)

	// 恢复后首次成功发起，不被当作“已存在”。
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("保存失败未建立记录，恢复后发起应是全新首次发起，got %v", err)
	}
	if h.ID != "H001" || len(h.Entries) != 2 {
		t.Fatalf("应成功建立 H001 且清单含2项，got %+v", h)
	}

	// 保留既有重复发起行为：返回原记录与 ErrHandoverExists。
	again, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || again.ID != h.ID {
		t.Fatalf("成功后重复发起应返回原记录 ErrHandoverExists，got %v id=%s", err, again.ID)
	}
	// 接班班次后来结束后重复发起仍返回原记录。
	for _, e := range h.Entries {
		if _, err := f.svc.ProcessEntry(h.ID, e.ItemID, ActionConfirm, "李四", "", "", ""); err != nil {
			t.Fatalf("confirm %s: %v", e.ItemID, err)
		}
	}
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	again2, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrHandoverExists) || again2.ID != h.ID {
		t.Fatalf("接班班次结束后重复发起仍应返回原记录，got %v", err)
	}

	// 改换接班对象与不存在的班次编号仍按既有规则明确拒绝。
	c := mustShift(t, f, "调度", "王五", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	if r, err := f.svc.CreateHandover(a.ID, c.ID); !errors.Is(err, ErrHandoverTarget) || r.ID != "" {
		t.Fatalf("改换接班对象应报 ErrHandoverTarget 且不返回交接记录，got %v %+v", err, r)
	}
	if r, err := f.svc.CreateHandover(a.ID, "S999"); !errors.Is(err, ErrNotFound) || r.ID != "" {
		t.Fatalf("接班编号不存在应报 ErrNotFound 且不返回交接记录，got %v %+v", err, r)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != h.ID {
		t.Fatalf("拒绝后不应产生新交接：%+v", hs)
	}
	// 逐项接收的原有规则保持不变：事项已进入接班班次。
	for _, it := range items[:2] {
		got, err := f.svc.GetItem(it.ID)
		if err != nil {
			t.Fatalf("get item: %v", err)
		}
		if got.CurrentShiftID != b.ID {
			t.Fatalf("确认接收后事项应在接班班次，got %s", got.CurrentShiftID)
		}
	}
}
