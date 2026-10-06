package handover

import (
	"errors"
	"strings"
	"testing"
)

// dualIncomingFixture 搭建“两个同岗位已结束交班班次指向同一进行中接班班次”
// 的场景：
//   - S001（张三）08:00-16:00：I001 未关闭（将交后班）、I002 结束前已关闭（不交）；
//   - S002（李四）16:00-23:00：I003 未关闭（将交后班）；
//   - S003（王五）23:00-次日07:00：I004 本班自行新增；
//   - H001 = S001 -> S003，H002 = S002 -> S003，发起后均尚未逐项处理。
//
// 三个班次区间端点相接、互不重叠，无需重叠说明。
type dualIncomingFixture struct {
	f              *fixture
	s1, s2, s3     Shift
	i1, i2, i3, i4 Item
	h1, h2         Handover
}

func setupDualIncoming(t *testing.T) *dualIncomingFixture {
	t.Helper()
	f := newFixture(t)

	s1 := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	i1, err := f.svc.AddItem(s1.ID, "泵房压力异常偏高", SeverityImportant, "夜间禁动", "郑七")
	if err != nil {
		t.Fatalf("add i1: %v", err)
	}
	// I002 在 S001 自己结束前已关闭：只能留在 S001 的记录里，不进入交接清单，
	// 也不能混入后班 S003 的结束时记录。
	i2, err := f.svc.AddItem(s1.ID, "前班自留已办事项", SeverityNormal, "", "张三")
	if err != nil {
		t.Fatalf("add i2: %v", err)
	}
	if _, err := f.svc.CloseItem(i2.ID, "张三"); err != nil {
		t.Fatalf("close i2: %v", err)
	}
	if _, err := f.svc.CloseShift(s1.ID); err != nil {
		t.Fatalf("close s1: %v", err)
	}

	s2 := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	i3, err := f.svc.AddItem(s2.ID, "阀门井积水", SeverityImportant, "下井须通风", "周九")
	if err != nil {
		t.Fatalf("add i3: %v", err)
	}
	if _, err := f.svc.CloseShift(s2.ID); err != nil {
		t.Fatalf("close s2: %v", err)
	}

	s3 := mustShift(t, f, "调度", "王五", tsDay(2, 23, 0), tsDay(3, 7, 0), "")
	i4, err := f.svc.AddItem(s3.ID, "本班新增巡检项", SeverityNormal, "", "王五")
	if err != nil {
		t.Fatalf("add i4: %v", err)
	}

	h1, err := f.svc.CreateHandover(s1.ID, s3.ID)
	if err != nil {
		t.Fatalf("handover s1->s3: %v", err)
	}
	h2, err := f.svc.CreateHandover(s2.ID, s3.ID)
	if err != nil {
		t.Fatalf("handover s2->s3: %v", err)
	}
	return &dualIncomingFixture{
		f: f, s1: s1, s2: s2, s3: s3,
		i1: i1, i2: i2, i3: i3, i4: i4, h1: h1, h2: h2,
	}
}

// closeRecordSection 截取班次报告中“结束时记录”事项区段（到“交班对象”前），
// 使断言只针对结束时事实，不被报告后半部嵌入的交接清单原文干扰。
func closeRecordSection(t *testing.T, svc *Service, shiftID string) string {
	t.Helper()
	text := handoverReportText(t, svc, shiftID)
	start := strings.Index(text, "事项（以下为结束时记录")
	if start < 0 {
		t.Fatalf("报告缺少结束时记录区段：\n%s", text)
	}
	rest := text[start:]
	end := strings.Index(rest, "\n交班对象：")
	if end < 0 {
		t.Fatalf("报告结构异常，找不到交班对象区段：\n%s", text)
	}
	return rest[:end]
}

// TestTwoIncomingHandoversCloseRecordKeepsAllHeldItems：一个接班班次同时收到
// 两份交接时，结束时记录必须完整、准确：本班新增与两份交接中已接收的事项全部
// 在列（每项只出现一次、按原编号顺序），保存的是结束前最后生效的信息而不是
// 交接清单原文或接收当时信息；前班结束前已关闭、没有交来的事项不得混入；
// 前班各自冻结的结束时记录不随后班接收、跟踪改派与修改而变化。
func TestTwoIncomingHandoversCloseRecordKeepsAllHeldItems(t *testing.T) {
	d := setupDualIncoming(t)
	f := d.f

	// 第一份交接：确认接收 I001。
	if _, err := f.svc.ProcessEntry(d.h1.ID, d.i1.ID, ActionConfirm, "吴甲", "", "", ""); err != nil {
		t.Fatalf("confirm i1: %v", err)
	}
	// 第二份交接：继续跟踪 I003，指定新的后续负责人陈十。
	if _, err := f.svc.ProcessEntry(d.h2.ID, d.i3.ID, ActionTrack, "吴甲", "",
		"持续盯守积水消退", "陈十"); err != nil {
		t.Fatalf("track i3: %v", err)
	}
	h1, h2 := mustGetHandover(t, f, d.h1.ID), mustGetHandover(t, f, d.h2.ID)
	if !h1.Completed() || !h2.Completed() {
		t.Fatalf("两份交接逐项接收齐后均应完成")
	}

	// 接收后在本班再修改一个接收项的全部可改字段；并关闭本班新增项。
	if _, err := f.svc.UpdateItem(d.i1.ID, "泵房压力表已完成校验", SeverityUrgent,
		"白班复核后操作", "褚八"); err != nil {
		t.Fatalf("update i1 after receive: %v", err)
	}
	if _, err := f.svc.CloseItem(d.i4.ID, "王五"); err != nil {
		t.Fatalf("close i4: %v", err)
	}

	// 后班正常结束。
	if _, err := f.svc.CloseShift(d.s3.ID); err != nil {
		t.Fatalf("两份交接均接收齐后应能结束后班：%v", err)
	}

	rep, err := f.svc.ShiftReport(d.s3.ID)
	if err != nil {
		t.Fatalf("report s3: %v", err)
	}
	if !rep.ItemsAtClose || rep.HistoryIncomplete {
		t.Fatalf("后班结束成功后应明确留下结束时记录")
	}

	// 清单完整保留三项，每个原事项编号只出现一次，并沿用现有编号顺序。
	if len(rep.CloseItems) != 3 {
		t.Fatalf("结束时应保留本班当时实际持有的全部3项（2项接收+1项新增），got %d",
			len(rep.CloseItems))
	}
	gotIDs := make([]string, len(rep.CloseItems))
	seen := map[string]int{}
	for i, s := range rep.CloseItems {
		gotIDs[i] = s.ItemID
		seen[s.ItemID]++
	}
	wantIDs := []string{d.i1.ID, d.i3.ID, d.i4.ID}
	for i, want := range wantIDs {
		if gotIDs[i] != want {
			t.Fatalf("结束时清单应按事项编号顺序排列，got %v want %v", gotIDs, wantIDs)
		}
		if seen[want] != 1 {
			t.Fatalf("事项 %s 应只出现一次，got %d 次", want, seen[want])
		}
	}
	// 前班结束前已关闭、没有交来的 I002 绝不能混入后班清单。
	if findCloseItem(rep, d.i2.ID) != nil {
		t.Fatalf("前班已关闭、未交来的事项 %s 不能混入后班结束时记录", d.i2.ID)
	}

	// I001：保存结束前最后生效的信息（后班修改值），结束时仍未关闭。
	s1 := findCloseItem(rep, d.i1.ID)
	if s1.Content != "泵房压力表已完成校验" || s1.Severity != SeverityUrgent ||
		s1.Constraints != "白班复核后操作" || s1.FollowOwner != "褚八" {
		t.Fatalf("接收项结束时记录应为结束前最后生效的信息：%+v", s1)
	}
	if s1.Closed || s1.ClosedAt != nil || s1.CloseOperator != "" {
		t.Fatalf("I001 结束时未关闭，不能带关闭信息：%+v", s1)
	}

	// I003：继续跟踪指定的新负责人成为该事项在后班的负责人，结束时仍未关闭；
	// 内容、严重程度、限制条件沿用原文（本班未再修改）。
	s3 := findCloseItem(rep, d.i3.ID)
	if s3.Content != "阀门井积水" || s3.Severity != SeverityImportant ||
		s3.Constraints != "下井须通风" || s3.FollowOwner != "陈十" {
		t.Fatalf("继续跟踪项应以新负责人进入后班结束时记录，其余字段沿用原值：%+v", s3)
	}
	if s3.Closed || s3.ClosedAt != nil || s3.CloseOperator != "" {
		t.Fatalf("I003 结束时仍应显示未关闭：%+v", s3)
	}

	// I004：本班新增且结束前已关闭，保留关闭人和关闭时间。
	s4 := findCloseItem(rep, d.i4.ID)
	if !s4.Closed || s4.CloseOperator != "王五" || s4.ClosedAt == nil {
		t.Fatalf("已关闭的本班新增项应保留关闭人与关闭时间：%+v", s4)
	}

	// 班次报告文本：结束时记录区段展示结束时事实。
	section := closeRecordSection(t, f.svc, d.s3.ID)
	for _, want := range []string{
		"结束时记录",
		d.i1.ID, "泵房压力表已完成校验", "严重程度=紧急", "白班复核后操作",
		"结束时后续负责人：褚八", "结束时未关闭",
		d.i3.ID, "阀门井积水", "结束时后续负责人：陈十",
		d.i4.ID, "结束时已关闭（王五 于",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("结束时记录区段应包含 %q：\n%s", want, section)
		}
	}
	for _, banned := range []string{
		d.i2.ID, "前班自留已办事项", // 前班已关闭、未交来的事项
		"泵房压力异常偏高", "夜间禁动", "郑七", // I001 的交接原文/接收当时信息
	} {
		if strings.Contains(section, banned) {
			t.Fatalf("结束时记录区段不应再出现接收当时/前班自留信息 %q：\n%s", banned, section)
		}
	}

	// 交接清单保存的原文与接收当时信息仍原样保留，不能被结束时信息覆盖。
	e1 := findEntryOf(t, h1, d.i1.ID)
	if e1.Status != EntryConfirmed || e1.Content != "泵房压力异常偏高" ||
		e1.Severity != SeverityImportant || e1.Constraints != "夜间禁动" ||
		e1.FollowOwner != "郑七" || e1.Operator != "吴甲" || e1.ProcessedAt == nil {
		t.Fatalf("H001 清单应保存原文与接收当时信息：%+v", e1)
	}
	e3 := findEntryOf(t, h2, d.i3.ID)
	if e3.Status != EntryTracking || e3.Content != "阀门井积水" ||
		e3.Severity != SeverityImportant || e3.Constraints != "下井须通风" ||
		e3.FollowOwner != "陈十" || e3.TrackingNote != "持续盯守积水消退" {
		t.Fatalf("H002 清单应保存原文与继续跟踪当时信息：%+v", e3)
	}

	// 事项原始班次不因接收而改变，编号保持不变；接收后所在班次为后班。
	cur1, _ := f.svc.GetItem(d.i1.ID)
	if cur1.ID != d.i1.ID || cur1.OriginShiftID != d.s1.ID || cur1.CurrentShiftID != d.s3.ID {
		t.Fatalf("I001 编号与原始班次不变、当前应在后班：%+v", cur1)
	}
	cur3, _ := f.svc.GetItem(d.i3.ID)
	if cur3.OriginShiftID != d.s2.ID || cur3.CurrentShiftID != d.s3.ID || cur3.FollowOwner != "陈十" {
		t.Fatalf("I003 原始班次不变、当前应在后班且负责人为跟踪指定人：%+v", cur3)
	}

	// 前班 S001 的冻结记录：只含 I001、I002，且为 S001 结束当时的事实，
	// 不随后班修改/关闭变化；最新状态对照另行展示后班现值。
	r1, err := f.svc.ShiftReport(d.s1.ID)
	if err != nil {
		t.Fatalf("report s1: %v", err)
	}
	if len(r1.CloseItems) != 2 {
		t.Fatalf("S001 结束时记录应只含本班2项，got %d", len(r1.CloseItems))
	}
	fr1 := findCloseItem(r1, d.i1.ID)
	if fr1 == nil || fr1.Content != "泵房压力异常偏高" || fr1.Severity != SeverityImportant ||
		fr1.Constraints != "夜间禁动" || fr1.FollowOwner != "郑七" || fr1.Closed {
		t.Fatalf("S001 冻结记录不得被后班修改改变：%+v", fr1)
	}
	fr2 := findCloseItem(r1, d.i2.ID)
	if fr2 == nil || !fr2.Closed || fr2.CloseOperator != "张三" || fr2.ClosedAt == nil {
		t.Fatalf("S001 自己结束前已关闭的 I002 应只留在 S001 记录中：%+v", fr2)
	}
	if findCloseItem(r1, d.i3.ID) != nil || findCloseItem(r1, d.i4.ID) != nil {
		t.Fatalf("后班事项不能混入 S001 的结束时记录")
	}
	latest1 := r1.LatestItems[d.i1.ID]
	if latest1.Content != "泵房压力表已完成校验" || latest1.CurrentShiftID != d.s3.ID ||
		latest1.FollowOwner != "褚八" {
		t.Fatalf("S001 报告应对照展示 I001 的后班最新状态：%+v", latest1)
	}

	// 前班 S002 的冻结记录：只含 I003，负责人仍是 S002 结束当时的周九，
	// 不被随后继续跟踪改派成陈十。
	r2, err := f.svc.ShiftReport(d.s2.ID)
	if err != nil {
		t.Fatalf("report s2: %v", err)
	}
	if len(r2.CloseItems) != 1 {
		t.Fatalf("S002 结束时记录应只含 I003 一项，got %d", len(r2.CloseItems))
	}
	fr3 := findCloseItem(r2, d.i3.ID)
	if fr3 == nil || fr3.Content != "阀门井积水" || fr3.FollowOwner != "周九" || fr3.Closed {
		t.Fatalf("S002 冻结记录应为自己结束时的负责人与未关闭状态：%+v", fr3)
	}
	if findCloseItem(r2, d.i1.ID) != nil || findCloseItem(r2, d.i4.ID) != nil {
		t.Fatalf("其他班次事项不能混入 S002 的结束时记录")
	}
}

// TestCloseRejectedUntilBothIncomingHandoversReceived：两份接班交接中一份已
// 完成、另一份仍有待处理或退回事项时，后班不能结束，错误必须指出阻止结束的
// 那份交接；失败后班次保持进行中，不留下结束时间或结束时记录，已接收事项的
// 归属与此前处理经过保持原样。剩余事项按现有规则完成接收后才允许结束，
// 结束时记录以那次成功结束时的内容为准。
func TestCloseRejectedUntilBothIncomingHandoversReceived(t *testing.T) {
	d := setupDualIncoming(t)
	f := d.f

	// H001 完成接收 I001；H002 的 I003 保持待处理。先关闭本班新增项 I004，
	// 用于核对结束失败不影响此前已保存的事项状态。
	if _, err := f.svc.ProcessEntry(d.h1.ID, d.i1.ID, ActionConfirm, "吴甲", "", "", ""); err != nil {
		t.Fatalf("confirm i1: %v", err)
	}
	if _, err := f.svc.CloseItem(d.i4.ID, "王五"); err != nil {
		t.Fatalf("close i4: %v", err)
	}

	// 待处理项阻止结束：错误指出 H002，而不是已完成的 H001。
	if _, err := f.svc.CloseShift(d.s3.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("仍有待处理接班事项时应报 ErrHandoverState，got %v", err)
	} else {
		if !strings.Contains(err.Error(), d.h2.ID) {
			t.Fatalf("错误应指出阻止结束的交接 %s，got %v", d.h2.ID, err)
		}
		if strings.Contains(err.Error(), d.h1.ID) {
			t.Fatalf("错误不应指向已完成的交接 %s，got %v", d.h1.ID, err)
		}
	}

	// 失败后班次保持进行中：无结束时间、无结束时记录。
	sh, err := f.svc.GetShift(d.s3.ID)
	if err != nil {
		t.Fatalf("get s3: %v", err)
	}
	if sh.Closed || sh.ClosedAt != nil || sh.CloseRecord != nil {
		t.Fatalf("结束失败后班次应保持进行中且不留下记录：%+v", sh)
	}
	rep, err := f.svc.ShiftReport(d.s3.ID)
	if err != nil {
		t.Fatalf("report s3: %v", err)
	}
	if rep.Shift.Closed || rep.ItemsAtClose || rep.HistoryIncomplete {
		t.Fatalf("结束失败不应产生结束时记录标记")
	}

	// 已接收事项的归属与处理经过保持原样；未接收项仍留在交班班次。
	i1, _ := f.svc.GetItem(d.i1.ID)
	if i1.CurrentShiftID != d.s3.ID {
		t.Fatalf("已接收的 I001 应仍归属后班：%+v", i1)
	}
	i3, _ := f.svc.GetItem(d.i3.ID)
	if i3.CurrentShiftID != d.s2.ID {
		t.Fatalf("待处理的 I003 不应移动到后班：%+v", i3)
	}
	gh1 := mustGetHandover(t, f, d.h1.ID)
	if !gh1.Completed() || gh1.CompletedAt == nil {
		t.Fatalf("已完成的 H001 状态与完成时间不应改变")
	}
	if findEntryOf(t, gh1, d.i1.ID).Status != EntryConfirmed {
		t.Fatalf("H001 中 I001 的确认接收结果不应改变")
	}
	gh2 := mustGetHandover(t, f, d.h2.ID)
	if gh2.Completed() || gh2.CompletedAt != nil {
		t.Fatalf("未完成的 H002 不应因结束失败而被写成完成")
	}
	if findEntryOf(t, gh2, d.i3.ID).Status != EntryPending {
		t.Fatalf("I003 应仍为待处理")
	}
	// 本班新增项此前的关闭保持有效。
	i4, _ := f.svc.GetItem(d.i4.ID)
	if !i4.Closed || i4.CloseOperator != "王五" {
		t.Fatalf("结束失败前已保存的 I004 关闭状态应保持原样：%+v", i4)
	}

	// 把 I003 退回：退回项同样阻止结束，错误仍指出 H002。
	if _, err := f.svc.ProcessEntry(d.h2.ID, d.i3.ID, ActionReturn, "吴甲",
		"缺少现场积水照片", "", ""); err != nil {
		t.Fatalf("return i3: %v", err)
	}
	if _, err := f.svc.CloseShift(d.s3.ID); !errors.Is(err, ErrHandoverState) ||
		err == nil || !strings.Contains(err.Error(), d.h2.ID) {
		t.Fatalf("存在退回事项时结束应被拒绝并指出 %s，got %v", d.h2.ID, err)
	}

	// 第二次失败后一切仍保持原样：班次进行中、退回轮次不增加、事项不移动。
	sh, _ = f.svc.GetShift(d.s3.ID)
	if sh.Closed || sh.ClosedAt != nil || sh.CloseRecord != nil {
		t.Fatalf("第二次结束失败后仍不应留下任何结束痕迹：%+v", sh)
	}
	gh2 = mustGetHandover(t, f, d.h2.ID)
	e3 := findEntryOf(t, gh2, d.i3.ID)
	if e3.Status != EntryReturned || len(e3.Rounds) != 1 ||
		e3.Rounds[0].Reason != "缺少现场积水照片" || e3.Operator != "吴甲" {
		t.Fatalf("失败的结束尝试不应改变退回状态与处理经过：%+v", e3)
	}
	i3, _ = f.svc.GetItem(d.i3.ID)
	if i3.CurrentShiftID != d.s2.ID {
		t.Fatalf("退回项应仍留在交班班次 S002：%+v", i3)
	}
	i1, _ = f.svc.GetItem(d.i1.ID)
	if i1.CurrentShiftID != d.s3.ID {
		t.Fatalf("另一份已完成交接中已接收的 I001 归属不应受影响：%+v", i1)
	}

	// 剩余事项按现有规则完成接收：交班人补充重新提交，接班人继续跟踪。
	if _, err := f.svc.ResubmitReturned(d.h2.ID, d.i3.ID, "李四", "积水照片已补传"); err != nil {
		t.Fatalf("resubmit i3: %v", err)
	}
	if _, err := f.svc.ProcessEntry(d.h2.ID, d.i3.ID, ActionTrack, "吴甲", "",
		"继续跟踪积水消退", "陈十"); err != nil {
		t.Fatalf("track i3 after resubmit: %v", err)
	}
	gh2 = mustGetHandover(t, f, d.h2.ID)
	if !gh2.Completed() || gh2.CompletedAt == nil {
		t.Fatalf("最后一项接收后 H002 才完成并记录完成时间")
	}
	e3 = findEntryOf(t, gh2, d.i3.ID)
	if e3.Status != EntryTracking || e3.FollowOwner != "陈十" || len(e3.Rounds) != 1 ||
		e3.Rounds[0].Supplement != "积水照片已补传" {
		t.Fatalf("此前退回/补充经过应保留，最终结果为继续跟踪：%+v", e3)
	}

	// 全部接收齐后结束成功，记录以这次成功结束时的内容为准。
	if _, err := f.svc.CloseShift(d.s3.ID); err != nil {
		t.Fatalf("两份交接均接收齐后应允许结束：%v", err)
	}
	sh, _ = f.svc.GetShift(d.s3.ID)
	if !sh.Closed || sh.ClosedAt == nil || sh.CloseRecord == nil {
		t.Fatalf("成功结束应留下结束时间与结束时记录：%+v", sh)
	}
	rep, err = f.svc.ShiftReport(d.s3.ID)
	if err != nil {
		t.Fatalf("report s3 after success: %v", err)
	}
	if !rep.ItemsAtClose || len(rep.CloseItems) != 3 {
		t.Fatalf("成功结束应以当时持有的3项留下完整记录，got %+v", rep.CloseItems)
	}
	ids := make([]string, len(rep.CloseItems))
	for i, s := range rep.CloseItems {
		ids[i] = s.ItemID
	}
	if ids[0] != d.i1.ID || ids[1] != d.i3.ID || ids[2] != d.i4.ID {
		t.Fatalf("成功结束记录应按编号列出3项，got %v", ids)
	}
	final1 := findCloseItem(rep, d.i1.ID)
	if final1.Content != "泵房压力异常偏高" || final1.FollowOwner != "郑七" || final1.Closed {
		t.Fatalf("I001 应以成功结束时的持有信息入册（未再修改，沿用原值）：%+v", final1)
	}
	final3 := findCloseItem(rep, d.i3.ID)
	if final3.FollowOwner != "陈十" || final3.Content != "阀门井积水" || final3.Closed {
		t.Fatalf("I003 应以补齐接收时的跟踪负责人入册且结束时未关闭：%+v", final3)
	}
	final4 := findCloseItem(rep, d.i4.ID)
	if !final4.Closed || final4.CloseOperator != "王五" || final4.ClosedAt == nil {
		t.Fatalf("结束失败前已关闭的 I004 应在成功记录中保留关闭信息：%+v", final4)
	}

	// 已结束班次不能重复结束。
	if _, err := f.svc.CloseShift(d.s3.ID); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("重复结束已结束班次应报 ErrShiftClosed，got %v", err)
	}
}
