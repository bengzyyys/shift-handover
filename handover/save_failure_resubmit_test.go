package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// failedResubmitExpectations 汇总失败尝试发生前已保存事实，供 reopen 前后
// 用同一组期望核对。
type failedResubmitExpectations struct {
	hID                        string
	fromShift, toShift         string
	idA, idB                   string
	otherConfirmedAt           time.Time // 同交接中另一项已接收的处理时间
	r1ReturnedAt               time.Time // 第一轮退回时间
	r1ResubmitAt               time.Time // 第一轮成功重新提交时间
	r2ReturnedAt               time.Time // 最近一轮（第二轮）退回时间
	failAt                     time.Time // 失败尝试时刻（不应留下任何痕迹）
	r1Reason, r1Supplement     string
	r2Reason, failedSupplement string
}

// assertStillReturnedAfterFailedResubmit 用当前打开的数据核对：保存失败后
// 事项仍是“退回、等待交班人补充”，失败前已保存的两轮退回与第一轮补充事实
// 原样保留，本次失败的补充不产生任何半完成结果；交接查询、事项处理经过与
// 展示层都与已保存事实一致。
func assertStillReturnedAfterFailedResubmit(t *testing.T, f *fixture, w failedResubmitExpectations) {
	t.Helper()

	// 交接：整份交接继续未完成且没有完成时间；没有生成第二条交接。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if h.Completed() || h.CompletedAt != nil {
		t.Fatalf("保存失败后整份交接应继续未完成、不能留下完成时间：%+v", h)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != w.hID {
		t.Fatalf("失败尝试不应生成另一条交接：%+v", hs)
	}

	e := findEntryOf(t, h, w.idA)

	// 当前结果仍为退回，继续等待交班人补充；原来保存的接班处理人（退回人）
	// 与处理时间保持原样，不能提前清空或显示成尚未处理。
	if e.Status != EntryReturned {
		t.Fatalf("失败后当前结果应仍为退回，got %s", e.Status)
	}
	if e.Operator != "李四" || e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.r2ReturnedAt) {
		t.Fatalf("最近一轮退回的处理人与处理时间应保持原样：op=%s at=%v want 李四 %s",
			e.Operator, e.ProcessedAt, w.r2ReturnedAt)
	}

	// 最近一轮退回原因、退回人、退回时间和轮次保留；不出现本次尝试填写的
	// 补充说明、补充人、补充时间或重新提交时间，也不多出一轮。
	if len(e.Rounds) != 2 {
		t.Fatalf("应仍为两轮退回记录，失败尝试不多出一轮，got %d：%+v", len(e.Rounds), e.Rounds)
	}
	r1, r2 := e.Rounds[0], e.Rounds[1]
	if r1.Seq != 1 || r1.Reason != w.r1Reason || r1.ReturnOperator != "李四" ||
		!r1.ReturnedAt.Equal(w.r1ReturnedAt) {
		t.Fatalf("第一轮退回信息应保留：%+v", r1)
	}
	// 此前已经成功保存的第一轮补充不能丢失或被覆盖。
	if r1.Supplement != w.r1Supplement || r1.SupplementOperator != "张三" ||
		r1.SupplementAt == nil || !r1.SupplementAt.Equal(w.r1ResubmitAt) ||
		r1.ResubmittedAt == nil || !r1.ResubmittedAt.Equal(w.r1ResubmitAt) {
		t.Fatalf("第一轮补充与重新提交信息应原样保留：%+v", r1)
	}
	if r2.Seq != 2 || r2.Reason != w.r2Reason || r2.ReturnOperator != "李四" ||
		!r2.ReturnedAt.Equal(w.r2ReturnedAt) {
		t.Fatalf("最近一轮退回原因、退回人、退回时间应保留：%+v", r2)
	}
	if r2.Supplement != "" || r2.SupplementOperator != "" ||
		r2.SupplementAt != nil || r2.ResubmittedAt != nil {
		t.Fatalf("失败尝试不得写入补充说明、补充人、补充时间或重新提交时间：%+v", r2)
	}

	// 同一份交接中另一项已经接收的结果、处理人和处理时间不受影响。
	oe := findEntryOf(t, h, w.idB)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(w.otherConfirmedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}

	// 事项继续留在交班班次，原文、严重程度、限制条件和后续负责人不因补充
	// 失败而变化；不复制事项。
	it, err := f.svc.GetItem(w.idA)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != w.fromShift {
		t.Fatalf("事项应继续留在交班班次 %s，got %s", w.fromShift, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != w.fromShift {
		t.Fatalf("事项流经班次不应变化：%v", it.ShiftIDs)
	}
	if it.Content != e.Content || it.Severity != e.Severity ||
		it.Constraints != e.Constraints || it.FollowOwner != e.FollowOwner {
		t.Fatalf("原文、严重程度、限制条件和后续负责人应与交接清单一致：item=%+v entry=%+v", it, e)
	}
	if it.Content != "事项甲" || it.FollowOwner != "李四" {
		t.Fatalf("事项原文与后续负责人应保持原值：%+v", it)
	}
	if n := countItems(f, w.idA); n != 1 {
		t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", w.idA, n)
	}

	// 处理经过查询呈现失败前的事实：当前结果为退回、等待补充；时间线只含
	// 两轮退回与第一轮成功重新提交，不混入失败尝试的说明或时刻。
	j, err := f.svc.ItemJourney(w.idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("处理经过中的交接当前结果应仍为退回：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit,return" {
		t.Fatalf("处理经过应只保留失败前的两轮退回与第一轮重新提交，got %s", got)
	}
	resubmits, returns := 0, 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
		}
		if strings.Contains(ev.Supplement, w.failedSupplement) ||
			strings.Contains(ev.Detail, w.failedSupplement) {
			t.Fatalf("失败尝试的补充说明不应出现在处理经过：%+v", ev)
		}
		switch ev.Kind {
		case "return":
			returns++
		case "resubmit":
			resubmits++
			if ev.RoundSeq != 1 || ev.Supplement != w.r1Supplement ||
				ev.SupplementOperator != "张三" || ev.SupplementAt == nil ||
				!ev.SupplementAt.Equal(w.r1ResubmitAt) {
				t.Fatalf("唯一的重新提交应是第一轮已成功保存的那次：%+v", ev)
			}
		case "confirm", "track", "received":
			t.Fatalf("失败尝试不应被当成提交成功或接收：%+v", ev)
		}
	}
	if returns != 2 || resubmits != 1 {
		t.Fatalf("应只有两次退回、一次（第一轮）重新提交，got returns=%d resubmits=%d",
			returns, resubmits)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "退回（等待交班人补充）") {
		t.Fatalf("处理经过应显示退回、等待交班人补充：\n%s", jText)
	}
	if strings.Contains(jText, "第2次重新提交") || strings.Contains(jText, w.failedSupplement) {
		t.Fatalf("处理经过不应出现第二轮重新提交或失败尝试的说明：\n%s", jText)
	}

	// 交接展示与保存事实一致：未完成、退回项保留退回人与退回时间、两轮记录
	// 中只有第一轮带补充，不出现失败尝试的补充与重新提交时刻。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "[退回]") {
		t.Fatalf("事项应显示退回状态：\n%s", text)
	}
	if !strings.Contains(text, "最后处理：处理人=李四；处理时间="+fmtTime(w.r2ReturnedAt)) {
		t.Fatalf("退回项应显示原来的退回人与退回时间，不能显示尚未处理：\n%s", text)
	}
	if !strings.Contains(text, "第1次退回") || !strings.Contains(text, w.r1Supplement) ||
		!strings.Contains(text, "第2次退回") || !strings.Contains(text, w.r2Reason) {
		t.Fatalf("两轮退回原因与第一轮补充都应保留：\n%s", text)
	}
	if strings.Contains(text, w.failedSupplement) ||
		strings.Contains(text, "已重新提交："+fmtTime(w.failAt)) {
		t.Fatalf("失败尝试的补充说明与重新提交时间不应出现在交接查询中：\n%s", text)
	}

	// 交班班次结束时记录保留原来的内容与负责人。
	rep, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, w.idA)
	if snap == nil || snap.Content != "事项甲" || snap.FollowOwner != "李四" {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}

	// 退回项仍等待补充，接班班次不能结束。
	if _, err := f.svc.CloseShift(w.toShift); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未补充完成时接班班次不能结束，got %v", err)
	}
}

// countItems 统计数据中某事项编号出现次数，用于发现失败尝试是否复制了事项。
func countItems(f *fixture, itemID string) int {
	n := 0
	for _, it := range f.store.data.Items {
		if it.ID == itemID {
			n++
		}
	}
	return n
}

// TestResubmitSaveFailureAtomicRollback：交班人对已经历一轮退回与补充、后来
// 又被再次退回的事项，在原交接记录上填写操作人与非空补充说明并重新提交，
// 业务条件全部成立、真正进入保存过程后本地写盘失败——操作必须明确返回保存
// 错误，不能当成提交成功；内存与磁盘都回到失败前：当前结果仍为退回并保留
// 最近一轮退回人/时间，两轮退回与第一轮补充完整，不出现本次补充、补充人、
// 补充时间、重新提交时间，也不多出一轮；同交接已接收的另一项与整份交接的
// 未完成状态不变。缺少必填内容、编号不存在或状态不允许的业务拒绝都不能代替
// 这个场景。保存恢复后在原交接记录上对同一退回项补充并重新提交成功，处理
// 经过只留下成功的这次，随后接班人按原规则接收，交接才完成。
func TestResubmitSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idA, idB := items[0].ID, items[1].ID // 事项甲、事项乙（事项丙已关闭）

	// 同交接另一项先成功接收，后续核对它不受失败尝试影响。
	otherConfirmedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return otherConfirmedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}

	// 第一轮：退回 -> 交班人补充并重新提交。
	r1ReturnedAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return r1ReturnedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第一轮原因", "", ""); err != nil {
		t.Fatalf("return round 1: %v", err)
	}
	r1ResubmitAt := tsDay(2, 17, 30)
	f.svc.nowAt(func() time.Time { return r1ResubmitAt })
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第一轮补充内容"); err != nil {
		t.Fatalf("resubmit round 1: %v", err)
	}

	// 后来又被再次退回：产生第二轮并重新等待补充。
	r2ReturnedAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return r2ReturnedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", "第二轮原因", "", ""); err != nil {
		t.Fatalf("return round 2: %v", err)
	}
	pre, _ := f.svc.GetHandover(h.ID)
	preEntry := findEntryOf(t, pre, idA)
	if preEntry.Status != EntryReturned || len(preEntry.Rounds) != 2 {
		t.Fatalf("前置：事项应处于第二轮退回状态，got %+v", preEntry)
	}

	want := failedResubmitExpectations{
		hID: h.ID, fromShift: a.ID, toShift: b.ID, idA: idA, idB: idB,
		otherConfirmedAt: otherConfirmedAt,
		r1ReturnedAt:     r1ReturnedAt, r1ResubmitAt: r1ResubmitAt,
		r2ReturnedAt: r2ReturnedAt,
		failAt:       tsDay(2, 18, 30),
		r1Reason:     "第一轮原因", r1Supplement: "第一轮补充内容",
		r2Reason: "第二轮原因", failedSupplement: "第二轮补充-失败尝试",
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 操作人与非空补充都已填写、编号存在、事项正处于允许重新提交的退回
	// 状态；使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	_, err = f.svc.ResubmitReturned(h.ID, idA, "张三", want.failedSupplement)
	if err == nil {
		t.Fatalf("保存失败时重新提交应明确返回错误，不能当成提交成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	// 业务条件全部成立，不能用缺少必填内容、编号不存在或状态不允许的拒绝
	// 来冒充这个保存失败场景。
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据上查看交接与处理经过，应看到失败前的事实，而不是只保证
	// 文件没变、查询却显示已经提交。
	assertStillReturnedAfterFailedResubmit(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：退回状态、两轮记录与已接收的另一项全部与失败前一致，
	// 失败尝试不产生重新提交经过。
	f.reopen(t)
	assertStillReturnedAfterFailedResubmit(t, f, want)

	// 保存恢复正常后，交班人仍能在原交接记录上对同一退回项补充并重新提交。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第二轮补充-成功提交"); err != nil {
		t.Fatalf("恢复后重新提交应成功：%v", err)
	}

	// 这次成功仅让该项恢复待处理：当前处理人和处理时间显示尚未处理。
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("重新提交不等于接收，交接应继续未完成：%+v", h2)
	}
	le := findEntryOf(t, h2, idA)
	if le.Status != EntryPending || le.Operator != "" || le.ProcessedAt != nil {
		t.Fatalf("成功重新提交后应恢复待处理、当前处理人与处理时间为尚未处理：%+v", le)
	}
	if len(le.Rounds) != 2 {
		t.Fatalf("轮次不应增加，got %d", len(le.Rounds))
	}
	// 新的补充写在最近一轮（第二轮），记录成功提交时的补充人、补充时间与
	// 重新提交时间；原退回信息及第一轮内容继续保留。
	s1, s2 := le.Rounds[0], le.Rounds[1]
	if s1.Reason != want.r1Reason || s1.Supplement != want.r1Supplement ||
		s1.SupplementOperator != "张三" || s1.SupplementAt == nil ||
		!s1.SupplementAt.Equal(want.r1ResubmitAt) {
		t.Fatalf("第一轮退回与补充应继续保留：%+v", s1)
	}
	if s2.Reason != want.r2Reason || s2.ReturnOperator != "李四" ||
		!s2.ReturnedAt.Equal(want.r2ReturnedAt) {
		t.Fatalf("第二轮原退回信息应保留：%+v", s2)
	}
	if s2.Supplement != "第二轮补充-成功提交" || s2.SupplementOperator != "张三" ||
		s2.SupplementAt == nil || !s2.SupplementAt.Equal(successAt) ||
		s2.ResubmittedAt == nil || !s2.ResubmittedAt.Equal(successAt) {
		t.Fatalf("第二轮应记录成功提交的补充人、补充时间与重新提交时间：%+v", s2)
	}

	// 事项仍留在交班班次，原文、严重程度、限制条件和后续负责人不变。
	it, err := f.svc.GetItem(idA)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != a.ID {
		t.Fatalf("重新提交不移动事项：%+v", it)
	}
	if it.Content != "事项甲" || it.FollowOwner != "李四" {
		t.Fatalf("事项原文与后续负责人应不变：%+v", it)
	}

	// 处理经过只呈现成功的这次第二轮重新提交，不混入失败尝试的说明或时刻；
	// 第一轮经过仍在。
	j, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit,return,resubmit" {
		t.Fatalf("处理经过应呈现两轮退回与两次成功重新提交，got %s", got)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("交接当前结果应显示待处理：%+v", j.Results)
	}
	secondResubmits := 0
	for _, ev := range j.Events {
		if strings.Contains(ev.Supplement, want.failedSupplement) ||
			(ev.TimeKnown && ev.At.Equal(want.failAt)) {
			t.Fatalf("处理经过不能混入失败尝试的说明或时刻：%+v", ev)
		}
		if ev.Kind == "resubmit" && ev.RoundSeq == 2 {
			secondResubmits++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "张三" ||
				ev.Supplement != "第二轮补充-成功提交" || ev.SupplementAt == nil ||
				!ev.SupplementAt.Equal(successAt) {
				t.Fatalf("第二轮重新提交应只反映成功操作：%+v", ev)
			}
		}
	}
	if secondResubmits != 1 {
		t.Fatalf("第二轮应只有一次（成功的）重新提交，got %d", secondResubmits)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "待处理（接班人尚未处理）") {
		t.Fatalf("处理经过当前结果应显示待处理、接班人尚未处理：\n%s", jText)
	}
	if strings.Contains(jText, want.failedSupplement) {
		t.Fatalf("失败尝试的说明不能混入成功后的处理经过：\n%s", jText)
	}

	// 保留现有接班人之后逐项处理的规则：确认接收后事项才进入接班班次，
	// 整份交接在这一刻才完成，完成时间以本次成功处理为准。
	confirmAt := tsDay(2, 19, 30)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重新提交后接班人确认应成功：%v", err)
	}
	h3, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after confirm: %v", err)
	}
	if !h3.Completed() || h3.CompletedAt == nil || !h3.CompletedAt.Equal(confirmAt) {
		t.Fatalf("全部接收后交接才完成，完成时间应以确认时刻为准：%+v", h3)
	}
	ce := findEntryOf(t, h3, idA)
	if ce.Status != EntryConfirmed || ce.Operator != "李四" ||
		ce.ProcessedAt == nil || !ce.ProcessedAt.Equal(confirmAt) {
		t.Fatalf("事项应记录本次确认接收：%+v", ce)
	}
	if len(ce.Rounds) != 2 || ce.Rounds[1].Supplement != "第二轮补充-成功提交" {
		t.Fatalf("两轮退回与成功补充应作为历史保留：%+v", ce.Rounds)
	}
	it2, err := f.svc.GetItem(idA)
	if err != nil {
		t.Fatalf("get item after confirm: %v", err)
	}
	if it2.CurrentShiftID != b.ID || len(it2.ShiftIDs) != 2 {
		t.Fatalf("确认接收后事项才进入接班班次：%+v", it2)
	}

	// 退出重开后，成功的重新提交与接收结果完整保留，失败尝试仍无痕迹。
	f.reopen(t)
	h4, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	if !h4.Completed() || h4.CompletedAt == nil || !h4.CompletedAt.Equal(confirmAt) {
		t.Fatalf("重开后交接完成状态与完成时间应保留：%+v", h4)
	}
	re := findEntryOf(t, h4, idA)
	if re.Status != EntryConfirmed || len(re.Rounds) != 2 ||
		re.Rounds[1].Supplement != "第二轮补充-成功提交" {
		t.Fatalf("重开后第二轮成功补充应保留：%+v", re)
	}
	j4, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j4)); got != "created,handover-init,return,resubmit,return,resubmit,confirm" {
		t.Fatalf("重开后处理经过应只保留成功的两次重新提交与确认接收，got %s", got)
	}
	for _, ev := range j4.Events {
		if strings.Contains(ev.Supplement, want.failedSupplement) {
			t.Fatalf("重开后失败尝试的说明仍不应出现：%+v", ev)
		}
	}
}
