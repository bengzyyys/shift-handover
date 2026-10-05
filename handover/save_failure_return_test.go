package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// failedFirstReturnExpectations 汇总首次退回失败尝试发生前已保存的事实，
// 供 reopen 前后用同一组期望核对。
type failedFirstReturnExpectations struct {
	hID                string
	fromShift, toShift string
	idTarget, idOther  string
	otherConfirmedAt   time.Time // 同交接中另一项已接收的处理时间
	failAt             time.Time // 失败尝试时刻（不应留下任何痕迹）
	failedReason       string    // 失败尝试填写的退回原因（不应留下任何痕迹）
}

// assertStillPendingAfterFailedFirstReturn 用当前打开的数据核对：首次退回保存
// 失败后事项仍是待处理、接班人尚未处理，不产生退回轮次；失败前已保存的事实
// （同交接已接收的另一项、交班班次结束时记录）原样保留，交接查询、事项处理
// 经过与展示层都与已保存事实一致。
func assertStillPendingAfterFailedFirstReturn(t *testing.T, f *fixture, w failedFirstReturnExpectations) {
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

	// 目标项仍为待处理：当前处理人与处理时间显示尚未处理，没有退回轮次，
	// 不出现失败尝试的原因、人名或时刻。
	e := findEntryOf(t, h, w.idTarget)
	if e.Status != EntryPending {
		t.Fatalf("首次退回失败后该项应仍为待处理，got %s", e.Status)
	}
	if e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("该项的当前处理人与处理时间应仍为尚未处理：%+v", e)
	}
	if len(e.Rounds) != 0 {
		t.Fatalf("失败的首次退回不应产生退回轮次：%+v", e.Rounds)
	}

	// 同一份交接中另一项已经接收的结果、处理人和处理时间不受影响。
	oe := findEntryOf(t, h, w.idOther)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(w.otherConfirmedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}

	// 事项继续留在交班班次，原文、严重程度、限制条件和后续负责人不变；
	// 不复制事项，事项历史不增加任何记录。
	it, err := f.svc.GetItem(w.idTarget)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != w.fromShift {
		t.Fatalf("事项应继续留在交班班次 %s，got %s", w.fromShift, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != w.fromShift {
		t.Fatalf("事项流经班次不应变化：%v", it.ShiftIDs)
	}
	if it.Content != "事项甲" || it.Severity != SeverityNormal ||
		it.Constraints != "" || it.FollowOwner != "李四" {
		t.Fatalf("原文、严重程度、限制条件和后续负责人应保持原值：%+v", it)
	}
	if n := countItems(f, w.idTarget); n != 1 {
		t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", w.idTarget, n)
	}
	for _, ev := range it.Events {
		if ev.Kind != "created" {
			t.Fatalf("失败的退回不应在事项历史中留下任何记录：%+v", ev)
		}
	}

	// 处理经过查询呈现失败前的事实：当前结果为待处理，时间线只有建立与
	// 发起交接，不混入失败尝试的原因、人名或时刻。
	j, err := f.svc.ItemJourney(w.idTarget)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init" {
		t.Fatalf("处理经过应只含建立与发起交接，got %s", got)
	}
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
		}
		if ev.Operator == "王五" || strings.Contains(ev.Reason, w.failedReason) ||
			strings.Contains(ev.Detail, w.failedReason) {
			t.Fatalf("失败尝试的人名或原因不应出现在处理经过：%+v", ev)
		}
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "待处理（接班人尚未处理）") {
		t.Fatalf("处理经过当前结果应显示待处理、接班人尚未处理：\n%s", jText)
	}
	if strings.Contains(jText, w.failedReason) || strings.Contains(jText, "第1次退回") {
		t.Fatalf("处理经过不应出现失败尝试的退回记录：\n%s", jText)
	}

	// 交接展示与保存事实一致：未完成、待处理项显示尚未处理，不出现失败
	// 尝试的原因与退回轮次。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if strings.Contains(text, w.failedReason) || strings.Contains(text, "第1次退回") ||
		strings.Contains(text, "[退回]") {
		t.Fatalf("失败尝试的原因与退回记录不应出现在交接查询中：\n%s", text)
	}

	// 交班班次结束时记录保留原来的内容与负责人。
	rep, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, w.idTarget)
	if snap == nil || snap.Content != "事项甲" || snap.FollowOwner != "李四" || snap.Closed {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}

	// 交接仍未完成，接班班次不能结束。
	if _, err := f.svc.CloseShift(w.toShift); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("交接未完成时接班班次不能结束，got %v", err)
	}
}

// TestFirstReturnSaveFailureAtomicRollback：接班人对首次处理的待处理事项选择
// 退回，操作人有效、退回原因非空、事项允许处理，业务校验全部通过、真正进入
// 保存过程后本地写盘失败——操作必须明确返回保存错误，不能返回成功退回的结果；
// 内存与磁盘都回到失败前：该项仍是待处理、接班人尚未处理，不产生退回轮次，
// 交接查询与处理经过只反映失败前已保存的事实，失败尝试的原因、人名与时刻
// 都不被当作已经发生的退回。退回原因为空或纯空白时仍按现有必填校验拒绝，
// 不留下退回事实。保存恢复后对同一事项重新执行退回，只新增一轮成功记录。
func TestFirstReturnSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idTarget, idOther := items[0].ID, items[1].ID // 事项甲、事项乙（事项丙已关闭）

	// 同交接另一项先成功接收，后续核对它不受失败尝试影响。
	otherConfirmedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return otherConfirmedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idOther, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}
	if got, _ := f.svc.GetHandover(h.ID); got.Completed() {
		t.Fatalf("前置：仍有一项待处理，交接不应完成")
	}

	want := failedFirstReturnExpectations{
		hID: h.ID, fromShift: a.ID, toShift: b.ID,
		idTarget: idTarget, idOther: idOther,
		otherConfirmedAt: otherConfirmedAt,
		failAt:           tsDay(2, 18, 0),
		failedReason:     "信息不全-失败尝试",
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的处理时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 退回原因为空或只有空白时，沿用现有必填校验明确拒绝，不留下退回事实
	// （保存故障不影响这条业务校验）。
	breakSaving(t, f)
	if _, err := f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "王五", "   ", "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白退回原因应按必填校验拒绝，got %v", err)
	}

	// 操作人有效、退回原因非空、事项允许处理；使下一次写盘在原子保存阶段失败。
	_, err = f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "王五", want.failedReason, "", "")
	if err == nil {
		t.Fatalf("保存失败时退回应明确返回错误，不能返回成功退回的结果")
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
	// 文件没变、查询却显示已经退回。
	assertStillPendingAfterFailedFirstReturn(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：待处理状态、已接收的另一项与失败前一致，失败尝试
	// 不产生退回经过。
	f.reopen(t)
	assertStillPendingAfterFailedFirstReturn(t, f, want)

	// 保存恢复正常后，对同一事项重新执行退回：只新增一轮成功记录。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "王五", "信息不全，需补充图纸", "", ""); err != nil {
		t.Fatalf("恢复后退回应成功：%v", err)
	}

	// 交接：该项只有这一次成功退回，记录本次成功操作的原因、操作人与实际
	// 处理时间；失败尝试不占用轮次。当前结果明确表示等待交班人补充。
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("存在退回项时交接应继续未完成、无完成时间：%+v", h2)
	}
	le := findEntryOf(t, h2, idTarget)
	if le.Status != EntryReturned || le.Operator != "王五" ||
		le.ProcessedAt == nil || !le.ProcessedAt.Equal(successAt) {
		t.Fatalf("该项应为本次成功的退回，记录操作人与实际处理时间：%+v", le)
	}
	if len(le.Rounds) != 1 {
		t.Fatalf("失败尝试不应占用轮次，应只有一轮成功记录，got %d：%+v", len(le.Rounds), le.Rounds)
	}
	r := le.Rounds[0]
	if r.Seq != 1 || r.Reason != "信息不全，需补充图纸" || r.ReturnOperator != "王五" ||
		!r.ReturnedAt.Equal(successAt) {
		t.Fatalf("退回轮次应记录本次成功操作的原因、操作人与实际处理时间：%+v", r)
	}
	if r.Supplement != "" || r.SupplementOperator != "" || r.SupplementAt != nil || r.ResubmittedAt != nil {
		t.Fatalf("新退回的一轮不应带有补充或重新提交信息：%+v", r)
	}

	// 事项仍留在交班班次，不因这次退回而被接收。
	it, err := f.svc.GetItem(idTarget)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != a.ID {
		t.Fatalf("退回不等于接收，事项应仍留在交班班次：%+v", it)
	}

	// 处理经过只呈现成功的这一次退回，失败尝试不混入经过。
	j, err := f.svc.ItemJourney(idTarget)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return" {
		t.Fatalf("处理经过应只留下成功的一次退回，got %s", got)
	}
	returns := 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(want.failAt) {
			t.Fatalf("失败尝试时刻不应出现在成功后的处理经过：%+v", ev)
		}
		if strings.Contains(ev.Reason, want.failedReason) {
			t.Fatalf("失败尝试的原因不应混入成功后的处理经过：%+v", ev)
		}
		if ev.Kind == "return" {
			returns++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "王五" ||
				ev.RoundSeq != 1 || ev.Reason != "信息不全，需补充图纸" {
				t.Fatalf("成功退回事件内容不正确：%+v", ev)
			}
		}
	}
	if returns != 1 {
		t.Fatalf("应只有一次（成功的）退回记录，got %d", returns)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("交接当前结果应为退回、等待交班人补充：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "退回（等待交班人补充）") {
		t.Fatalf("成功退回的当前结果应明确表示等待交班人补充：\n%s", jText)
	}

	// 退回项仍等待补充，接班班次不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未补充完成时接班班次不能结束，got %v", err)
	}

	// 退出重开后，成功的退回记录完整保留，失败尝试仍无痕迹。
	f.reopen(t)
	h3, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	re := findEntryOf(t, h3, idTarget)
	if re.Status != EntryReturned || len(re.Rounds) != 1 ||
		re.Rounds[0].Reason != "信息不全，需补充图纸" || !re.Rounds[0].ReturnedAt.Equal(successAt) {
		t.Fatalf("重开后成功的退回记录应保留、失败尝试无痕迹：%+v", re)
	}
	j3, err := f.svc.ItemJourney(idTarget)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j3)); got != "created,handover-init,return" {
		t.Fatalf("重开后处理经过应只保留成功的一次退回，got %s", got)
	}
}

// failedSecondReturnExpectations 汇总“已成功退回并补充重新提交后，再次退回
// 失败”尝试发生前已保存的事实，供 reopen 前后用同一组期望核对。
type failedSecondReturnExpectations struct {
	hID                        string
	fromShift, toShift         string
	idTarget, idOther          string
	otherConfirmedAt           time.Time // 同交接中另一项已接收的处理时间
	r1ReturnedAt               time.Time // 第一轮（已成功）退回时间
	r1ResubmitAt               time.Time // 第一轮成功重新提交时间
	failAt                     time.Time // 失败尝试时刻（不应留下任何痕迹）
	r1Reason, r1Supplement     string
	failedReason               string // 失败尝试填写的第二轮原因（不应留下任何痕迹）
}

// assertStillPendingAfterFailedSecondReturn 用当前打开的数据核对：已经成功
// 退回并补充重新提交的事项，再次退回保存失败后仍是待处理；先前那一轮的原因、
// 退回人、退回时间、补充说明、补充人和重新提交时间全部保留，不增加下一轮，
// 也不把以前的补充改成这次尝试的内容。
func assertStillPendingAfterFailedSecondReturn(t *testing.T, f *fixture, w failedSecondReturnExpectations) {
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

	// 目标项仍是待处理（重新提交后的状态）：当前处理人与处理时间显示尚未处理。
	e := findEntryOf(t, h, w.idTarget)
	if e.Status != EntryPending {
		t.Fatalf("再次退回失败后该项应仍为待处理，got %s", e.Status)
	}
	if e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("该项的当前处理人与处理时间应仍为尚未处理：%+v", e)
	}

	// 不增加下一轮：仍只有第一轮，且第一轮的原因、退回人、退回时间、补充
	// 说明、补充人和重新提交时间全部保留，不被这次尝试改写。
	if len(e.Rounds) != 1 {
		t.Fatalf("失败尝试不应增加退回轮次，应仍只有第一轮，got %d：%+v", len(e.Rounds), e.Rounds)
	}
	r1 := e.Rounds[0]
	if r1.Seq != 1 || r1.Reason != w.r1Reason || r1.ReturnOperator != "李四" ||
		!r1.ReturnedAt.Equal(w.r1ReturnedAt) {
		t.Fatalf("第一轮退回原因、退回人、退回时间应保留：%+v", r1)
	}
	if r1.Supplement != w.r1Supplement || r1.SupplementOperator != "张三" ||
		r1.SupplementAt == nil || !r1.SupplementAt.Equal(w.r1ResubmitAt) ||
		r1.ResubmittedAt == nil || !r1.ResubmittedAt.Equal(w.r1ResubmitAt) {
		t.Fatalf("第一轮的补充说明、补充人和重新提交时间应原样保留：%+v", r1)
	}

	// 同一份交接中另一项已经接收的结果、处理人和处理时间不受影响。
	oe := findEntryOf(t, h, w.idOther)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(w.otherConfirmedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}

	// 事项继续留在交班班次，原文、严重程度、限制条件和后续负责人不因失败
	// 尝试而变化；不复制事项。
	it, err := f.svc.GetItem(w.idTarget)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != w.fromShift {
		t.Fatalf("事项应继续留在交班班次 %s，got %s", w.fromShift, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != w.fromShift {
		t.Fatalf("事项流经班次不应变化：%v", it.ShiftIDs)
	}
	if it.Content != "事项甲" || it.Severity != SeverityNormal ||
		it.Constraints != "" || it.FollowOwner != "李四" {
		t.Fatalf("原文、严重程度、限制条件和后续负责人应保持原值：%+v", it)
	}
	if n := countItems(f, w.idTarget); n != 1 {
		t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", w.idTarget, n)
	}

	// 处理经过查询呈现失败前的事实：当前结果为待处理；时间线只含第一轮
	// 退回与成功重新提交，不混入失败尝试的原因、人名或时刻。
	j, err := f.svc.ItemJourney(w.idTarget)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit" {
		t.Fatalf("处理经过应只保留失败前的第一轮退回与重新提交，got %s", got)
	}
	returns, resubmits := 0, 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
		}
		if strings.Contains(ev.Reason, w.failedReason) || strings.Contains(ev.Detail, w.failedReason) {
			t.Fatalf("失败尝试的退回原因不应出现在处理经过：%+v", ev)
		}
		switch ev.Kind {
		case "return":
			returns++
			if ev.RoundSeq != 1 || ev.Reason != w.r1Reason || ev.Operator != "李四" {
				t.Fatalf("唯一的退回应是第一轮已成功保存的那次：%+v", ev)
			}
		case "resubmit":
			resubmits++
			if ev.RoundSeq != 1 || ev.Supplement != w.r1Supplement ||
				ev.SupplementOperator != "张三" {
				t.Fatalf("唯一的重新提交应是第一轮已成功保存的那次：%+v", ev)
			}
		}
	}
	if returns != 1 || resubmits != 1 {
		t.Fatalf("应只有一次退回、一次重新提交，got returns=%d resubmits=%d", returns, resubmits)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "待处理（接班人尚未处理）") {
		t.Fatalf("处理经过当前结果应显示待处理、接班人尚未处理：\n%s", jText)
	}
	if strings.Contains(jText, "第2次退回") || strings.Contains(jText, w.failedReason) {
		t.Fatalf("处理经过不应出现第二轮退回或失败尝试的原因：\n%s", jText)
	}

	// 交接展示与保存事实一致：未完成、待处理项显示尚未处理、只保留第一轮
	// 退回与补充，不出现失败尝试的原因与第二轮。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if !strings.Contains(text, "第1次退回") || !strings.Contains(text, w.r1Reason) ||
		!strings.Contains(text, w.r1Supplement) {
		t.Fatalf("第一轮退回原因与补充说明都应保留：\n%s", text)
	}
	if strings.Contains(text, "第2次退回") || strings.Contains(text, w.failedReason) {
		t.Fatalf("失败尝试的原因与第二轮退回不应出现在交接查询中：\n%s", text)
	}

	// 交班班次结束时记录保留原来的内容与负责人。
	rep, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, w.idTarget)
	if snap == nil || snap.Content != "事项甲" || snap.FollowOwner != "李四" || snap.Closed {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}

	// 交接仍未完成，接班班次不能结束。
	if _, err := f.svc.CloseShift(w.toShift); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("交接未完成时接班班次不能结束，got %v", err)
	}
}

// TestSecondReturnSaveFailureAtomicRollback：已经成功退回并补充重新提交的
// 事项，接班人再次退回时操作人有效、退回原因非空、事项允许处理，业务校验
// 全部通过、真正进入保存过程后本地写盘失败——操作必须明确返回保存错误；
// 内存与磁盘都回到失败前：该项仍是待处理，先前那一轮的原因、退回人、退回
// 时间、补充说明、补充人和重新提交时间全部保留，不增加下一轮，也不把以前
// 的补充改成这次尝试的内容；同交接已接收的另一项不受影响。保存恢复后对
// 同一事项重新执行退回，只新增一轮成功记录，失败尝试不占用轮次、不混入经过。
func TestSecondReturnSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idTarget, idOther := items[0].ID, items[1].ID // 事项甲、事项乙（事项丙已关闭）

	// 同交接另一项先成功接收，后续核对它不受失败尝试影响。
	otherConfirmedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return otherConfirmedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idOther, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}

	// 第一轮：退回 -> 交班人补充并重新提交，该项恢复待处理。
	r1ReturnedAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return r1ReturnedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "李四", "第一轮原因", "", ""); err != nil {
		t.Fatalf("return round 1: %v", err)
	}
	r1ResubmitAt := tsDay(2, 17, 30)
	f.svc.nowAt(func() time.Time { return r1ResubmitAt })
	if _, err := f.svc.ResubmitReturned(h.ID, idTarget, "张三", "第一轮补充内容"); err != nil {
		t.Fatalf("resubmit round 1: %v", err)
	}
	pre, _ := f.svc.GetHandover(h.ID)
	preEntry := findEntryOf(t, pre, idTarget)
	if preEntry.Status != EntryPending || len(preEntry.Rounds) != 1 {
		t.Fatalf("前置：事项应已重新提交并恢复待处理，got %+v", preEntry)
	}

	want := failedSecondReturnExpectations{
		hID: h.ID, fromShift: a.ID, toShift: b.ID,
		idTarget: idTarget, idOther: idOther,
		otherConfirmedAt: otherConfirmedAt,
		r1ReturnedAt:     r1ReturnedAt, r1ResubmitAt: r1ResubmitAt,
		failAt:       tsDay(2, 18, 0),
		r1Reason:     "第一轮原因", r1Supplement: "第一轮补充内容",
		failedReason: "第二轮原因-失败尝试",
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的处理时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 操作人有效、退回原因非空、事项（重新提交后）允许再次退回；使下一次
	// 写盘在原子保存阶段失败。
	breakSaving(t, f)
	_, err = f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "李四", want.failedReason, "", "")
	if err == nil {
		t.Fatalf("保存失败时再次退回应明确返回错误，不能返回成功退回的结果")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据上查看交接与处理经过，应看到失败前的事实。
	assertStillPendingAfterFailedSecondReturn(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：待处理状态、第一轮退回与补充、已接收的另一项全部
	// 与失败前一致，失败尝试不产生第二轮。
	f.reopen(t)
	assertStillPendingAfterFailedSecondReturn(t, f, want)

	// 保存恢复正常后，对同一事项重新执行退回：只新增一轮成功记录。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ProcessEntry(h.ID, idTarget, ActionReturn, "李四", "第二轮原因", "", ""); err != nil {
		t.Fatalf("恢复后再次退回应成功：%v", err)
	}

	// 交接：该项新增第二轮成功记录，记录本次成功操作的原因、操作人与实际
	// 处理时间；第一轮内容完整保留，失败尝试不占用轮次、不混入经过。
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("存在退回项时交接应继续未完成、无完成时间：%+v", h2)
	}
	le := findEntryOf(t, h2, idTarget)
	if le.Status != EntryReturned || le.Operator != "李四" ||
		le.ProcessedAt == nil || !le.ProcessedAt.Equal(successAt) {
		t.Fatalf("该项应为本次成功的退回，记录操作人与实际处理时间：%+v", le)
	}
	if len(le.Rounds) != 2 {
		t.Fatalf("应只有两轮（失败尝试不占轮次），got %d：%+v", len(le.Rounds), le.Rounds)
	}
	s1, s2 := le.Rounds[0], le.Rounds[1]
	if s1.Reason != want.r1Reason || s1.ReturnOperator != "李四" ||
		!s1.ReturnedAt.Equal(want.r1ReturnedAt) ||
		s1.Supplement != want.r1Supplement || s1.SupplementOperator != "张三" ||
		s1.SupplementAt == nil || !s1.SupplementAt.Equal(want.r1ResubmitAt) ||
		s1.ResubmittedAt == nil || !s1.ResubmittedAt.Equal(want.r1ResubmitAt) {
		t.Fatalf("第一轮退回与补充应完整保留、不被改写：%+v", s1)
	}
	if s2.Seq != 2 || s2.Reason != "第二轮原因" || s2.ReturnOperator != "李四" ||
		!s2.ReturnedAt.Equal(successAt) {
		t.Fatalf("第二轮应记录本次成功操作的原因、操作人与实际处理时间：%+v", s2)
	}
	if s2.Supplement != "" || s2.SupplementOperator != "" ||
		s2.SupplementAt != nil || s2.ResubmittedAt != nil {
		t.Fatalf("新退回的第二轮不应带有补充或重新提交信息：%+v", s2)
	}

	// 事项仍留在交班班次，不因这次退回而被接收；原文与负责人不变。
	it, err := f.svc.GetItem(idTarget)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != a.ID {
		t.Fatalf("退回不等于接收，事项应仍留在交班班次：%+v", it)
	}
	if it.Content != "事项甲" || it.FollowOwner != "李四" {
		t.Fatalf("事项原文与后续负责人应不变：%+v", it)
	}

	// 处理经过只呈现成功的两轮退回与第一轮重新提交，失败尝试不混入经过。
	j, err := f.svc.ItemJourney(idTarget)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit,return" {
		t.Fatalf("处理经过应呈现两轮成功退回与第一轮重新提交，got %s", got)
	}
	secondReturns := 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(want.failAt) {
			t.Fatalf("失败尝试时刻不应出现在成功后的处理经过：%+v", ev)
		}
		if strings.Contains(ev.Reason, want.failedReason) {
			t.Fatalf("失败尝试的原因不应混入成功后的处理经过：%+v", ev)
		}
		if ev.Kind == "return" && ev.RoundSeq == 2 {
			secondReturns++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "李四" ||
				ev.Reason != "第二轮原因" {
				t.Fatalf("第二轮退回应只反映成功操作：%+v", ev)
			}
		}
	}
	if secondReturns != 1 {
		t.Fatalf("第二轮应只有一次（成功的）退回记录，got %d", secondReturns)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("交接当前结果应为退回、等待交班人补充：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "退回（等待交班人补充）") {
		t.Fatalf("成功退回的当前结果应明确表示等待交班人补充：\n%s", jText)
	}

	// 退回项仍等待补充，接班班次不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项未补充完成时接班班次不能结束，got %v", err)
	}

	// 退出重开后，两轮成功记录完整保留，失败尝试仍无痕迹。
	f.reopen(t)
	h3, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	re := findEntryOf(t, h3, idTarget)
	if re.Status != EntryReturned || len(re.Rounds) != 2 ||
		re.Rounds[0].Supplement != want.r1Supplement ||
		re.Rounds[1].Reason != "第二轮原因" || !re.Rounds[1].ReturnedAt.Equal(successAt) {
		t.Fatalf("重开后两轮成功记录应保留、失败尝试无痕迹：%+v", re)
	}
	j3, err := f.svc.ItemJourney(idTarget)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j3)); got != "created,handover-init,return,resubmit,return" {
		t.Fatalf("重开后处理经过应只保留成功的两轮退回，got %s", got)
	}
	for _, ev := range j3.Events {
		if strings.Contains(ev.Reason, want.failedReason) {
			t.Fatalf("重开后失败尝试的原因仍不应出现：%+v", ev)
		}
	}
}
