package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// 本文件为“逐项退回”补充保存失败时的可重复回归保障：接班人填了有效操作人、
// 非空原因且事项当前允许退回（待处理），但本地数据在原子保存阶段失败。
// 通用的快照回滚（store.mutate）适用于退回，但之前没有针对退回的回归测试，
// 因此用两个用例分别覆盖首次退回与补充后再次退回这两种使用条件，并沿用既
// 有保存失败测试的同一套故障注入方式（在 .tmp 路径放目录使写入必然失败）。

// failedReturnExpectations 汇总失败尝试发生前已保存的业务事实，供 reopen
// 前后用同一组期望核对。
type failedReturnExpectations struct {
	hID                string
	fromShift, toShift string
	idA, idB           string
	otherConfirmedAt   time.Time // 同交接中另一项已接收的处理时间
	preRounds          int       // 失败尝试前已成功保存的退回轮次
	r1ReturnedAt       time.Time // 第一轮成功退回时间
	r1Reason           string
	r1ResubmitAt       time.Time // 第一轮成功重新提交时间
	r1Supplement       string
	failAt             time.Time // 失败尝试时刻（不应留下任何痕迹）
	failedReason       string    // 失败尝试填写的退回原因
	failedOperator     string    // 失败尝试填写的操作人
}

// assertPendingBeforeAnyReturn 用当前打开的数据核对“首次退回失败”后事实：
// 事项仍是待处理、接班人尚未处理，不产生退回轮次；失败尝试的原因、人名与
// 时刻都不是已发生的退回；同交接已确认的另一项与交班班次结束时记录不受影响。
func assertPendingBeforeAnyReturn(t *testing.T, f *fixture, w failedReturnExpectations) {
	t.Helper()

	// 交接：整份交接继续未完成且没有完成时间；不生成第二条交接。
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

	// 当前结果仍为待处理：接班人尚未处理，没有本次尝试的操作人、时间或原因。
	if e.Status != EntryPending {
		t.Fatalf("首次退回失败后该项应仍为待处理，got %s", e.Status)
	}
	if e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("待处理项的当前处理人与处理时间应仍为尚未处理：%+v", e)
	}
	if len(e.Rounds) != 0 {
		t.Fatalf("首次退回失败不应产生退回轮次：%+v", e.Rounds)
	}
	if e.TrackingNote != "" {
		t.Fatalf("失败尝试不应留下跟踪说明：%+v", e)
	}

	// 同一份交接中另一项已经确认接收的结果、处理人和处理时间不受影响。
	oe := findEntryOf(t, h, w.idB)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(w.otherConfirmedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}
	if len(oe.Rounds) != 0 {
		t.Fatalf("其他事项不应被失败尝试增加退回经过：%+v", oe.Rounds)
	}

	// 事项继续留在交班班次，原文、严重程度、限制条件与后续负责人保持原样，
	// 流经班次不增加接班班次，历史不增加这次退回/接收。
	assertItemStaysInFromShift(t, f, w, "事项甲", "李四")

	// 处理经过查询呈现失败前的事实：当前结果为待处理、接班人尚未处理；时间线
	// 只有发起交接，没有退回、重新提交或接收，也不混入失败尝试的原因/时刻。
	j, err := f.svc.ItemJourney(w.idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init" {
		t.Fatalf("处理经过应只保留失败前事实（建立、发起交接），got %s", got)
	}
	assertNoFailedReturnTraceInJourney(t, w, j)
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "待处理（接班人尚未处理）") {
		t.Fatalf("处理经过应显示待处理、接班人尚未处理：\n%s", jText)
	}
	if strings.Contains(jText, w.failedReason) {
		t.Fatalf("失败尝试的退回原因不应出现在处理经过：\n%s", jText)
	}
	if strings.Contains(jText, w.failedOperator) {
		t.Fatalf("失败尝试的操作人不应出现在处理经过：\n%s", jText)
	}

	// 交接展示与保存事实一致：未完成、待处理项显示尚未处理，不出现失败尝试
	// 的原因、人名或第1次退回记录。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if strings.Contains(text, "第1次退回") || strings.Contains(text, "[退回]") {
		t.Fatalf("首次退回失败不应留下退回状态或轮次展示：\n%s", text)
	}
	if strings.Contains(text, w.failedReason) || strings.Contains(text, w.failedOperator) {
		t.Fatalf("失败尝试的原因与操作人不应出现在交接查询中：\n%s", text)
	}
	if strings.Contains(text, "已重新提交") {
		t.Fatalf("首次退回失败不应出现重新提交记录：\n%s", text)
	}

	// 交班班次结束时记录保留原来的内容与负责人。
	assertFromShiftSnapshotUnchanged(t, f, w)

	// 待处理项使交接未完成，接班班次仍不能结束。
	if _, err := f.svc.CloseShift(w.toShift); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("待处理项未接收时接班班次不能结束，got %v", err)
	}
}

// assertPendingAfterOneRound 用当前打开的数据核对“补充后再次退回失败”后事实：
// 该项仍是待处理（第一轮已成功退回、补充并重新提交，所以恢复为待处理），先前
// 那一轮的原因、退回人、退回时间、补充说明、补充人和重新提交时间全部保留，
// 不增加下一轮，也不把以前的补充改成这次尝试的内容。
func assertPendingAfterOneRound(t *testing.T, f *fixture, w failedReturnExpectations) {
	t.Helper()

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

	// 当前结果仍为待处理：接班人尚未处理（第一轮重新提交后清空的当前处理
	// 信息保持清空），不因为失败尝试而变成退回。
	if e.Status != EntryPending {
		t.Fatalf("再次退回失败后该项应仍为待处理，got %s", e.Status)
	}
	if e.Operator != "" || e.ProcessedAt != nil {
		t.Fatalf("待处理项的当前处理人与处理时间应仍为尚未处理：%+v", e)
	}

	// 仍只有先前成功保存的那一轮；其原因、退回人、退回时间、补充说明、补充人
	// 与重新提交时间全部保留，不增加下一轮，也不把补充改成失败尝试的内容。
	if len(e.Rounds) != 1 {
		t.Fatalf("应仍为一轮退回记录，失败尝试不多出一轮，got %d：%+v", len(e.Rounds), e.Rounds)
	}
	r1 := e.Rounds[0]
	if r1.Seq != 1 || r1.Reason != w.r1Reason || r1.ReturnOperator != "李四" ||
		!r1.ReturnedAt.Equal(w.r1ReturnedAt) {
		t.Fatalf("第一轮退回原因、退回人、退回时间应保留：%+v", r1)
	}
	if r1.Supplement != w.r1Supplement || r1.SupplementOperator != "张三" ||
		r1.SupplementAt == nil || !r1.SupplementAt.Equal(w.r1ResubmitAt) ||
		r1.ResubmittedAt == nil || !r1.ResubmittedAt.Equal(w.r1ResubmitAt) {
		t.Fatalf("第一轮补充说明、补充人和重新提交时间应原样保留：%+v", r1)
	}
	if strings.Contains(r1.Supplement, w.failedReason) {
		t.Fatalf("不得把以前的补充改成失败尝试的内容：%+v", r1)
	}

	// 同交接已确认的另一项处理结果与处理经过不受影响。
	oe := findEntryOf(t, h, w.idB)
	if oe.Status != EntryConfirmed || oe.Operator != "李四" ||
		oe.ProcessedAt == nil || !oe.ProcessedAt.Equal(w.otherConfirmedAt) {
		t.Fatalf("已接收的其他事项处理信息应保持原样：%+v", oe)
	}
	if len(oe.Rounds) != 0 {
		t.Fatalf("其他事项不应被失败尝试增加退回经过：%+v", oe.Rounds)
	}

	// 事项仍留在交班班次，原文、严重程度、限制条件、后续负责人保持原样。
	assertItemStaysInFromShift(t, f, w, "事项甲", "李四")

	// 处理经过：当前结果待处理；时间线只含第一轮成功的退回与重新提交，不混入
	// 失败尝试的原因、人名或时刻，也不多出第2次退回。
	j, err := f.svc.ItemJourney(w.idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("处理经过中的交接当前结果应仍为待处理：%+v", j.Results)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit" {
		t.Fatalf("处理经过应只保留失败前的一轮退回与重新提交，got %s", got)
	}
	returns, resubmits := 0, 0
	for _, ev := range j.Events {
		switch ev.Kind {
		case "return":
			returns++
			if ev.RoundSeq != 1 || !ev.TimeKnown || !ev.At.Equal(w.r1ReturnedAt) ||
				ev.Operator != "李四" || ev.Reason != w.r1Reason {
				t.Fatalf("唯一退回应是第一轮已成功保存的那次：%+v", ev)
			}
		case "resubmit":
			resubmits++
			if ev.RoundSeq != 1 || !ev.TimeKnown || !ev.At.Equal(w.r1ResubmitAt) ||
				ev.Supplement != w.r1Supplement || ev.SupplementOperator != "张三" {
				t.Fatalf("唯一重新提交应是第一轮已成功保存的那次：%+v", ev)
			}
		case "confirm", "track", "received":
			t.Fatalf("失败尝试不应被当成提交成功或接收：%+v", ev)
		}
	}
	if returns != 1 || resubmits != 1 {
		t.Fatalf("应只有一次退回、一次重新提交（均为第一轮成功事实），got returns=%d resubmits=%d",
			returns, resubmits)
	}
	assertNoFailedReturnTraceInJourney(t, w, j)
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "待处理（接班人尚未处理）") {
		t.Fatalf("处理经过应显示待处理、接班人尚未处理：\n%s", jText)
	}
	if !strings.Contains(jText, "第1次退回") || !strings.Contains(jText, w.r1Supplement) {
		t.Fatalf("处理经过应保留第一轮退回原因与补充：\n%s", jText)
	}
	if strings.Contains(jText, "第2次退回") || strings.Contains(jText, w.failedReason) {
		t.Fatalf("处理经过不应出现第2次退回或失败尝试的原因：\n%s", jText)
	}
	if strings.Contains(jText, w.failedOperator) {
		t.Fatalf("失败尝试的操作人王五不应出现在处理经过（第一轮退回人为李四）：\n%s", jText)
	}

	// 交接展示：未完成、待处理项显示尚未处理；第一轮退回原因、补充与重新提交
	// 时间保留，不出现第2次退回或失败尝试的原因/人名。
	text := FormatHandover(h)
	if !strings.Contains(text, "[未完成]") {
		t.Fatalf("交接应显示未完成：\n%s", text)
	}
	if !strings.Contains(text, "最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("待处理项应显示尚未处理：\n%s", text)
	}
	if !strings.Contains(text, "第1次退回") || !strings.Contains(text, w.r1Reason) ||
		!strings.Contains(text, w.r1Supplement) ||
		!strings.Contains(text, "已重新提交："+fmtTime(w.r1ResubmitAt)) {
		t.Fatalf("第一轮退回原因、补充与重新提交时间都应保留：\n%s", text)
	}
	if strings.Contains(text, "第2次退回") || strings.Contains(text, "[退回]") {
		t.Fatalf("失败尝试不应产生第2轮或把当前结果显示成退回：\n%s", text)
	}
	if strings.Contains(text, w.failedReason) || strings.Contains(text, w.failedOperator) {
		t.Fatalf("失败尝试的原因与操作人不应出现在交接查询中：\n%s", text)
	}

	// 交班班次结束时记录不变。
	assertFromShiftSnapshotUnchanged(t, f, w)

	// 待处理项使交接未完成，接班班次仍不能结束。
	if _, err := f.svc.CloseShift(w.toShift); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("待处理项未接收时接班班次不能结束，got %v", err)
	}
}

// assertItemStaysInFromShift 核对事项仍留在交班班次、原文/严重程度/限制条件/
// 后续负责人保持原样，且事项自身历史不增加这次失败的退回/接收。
func assertItemStaysInFromShift(t *testing.T, f *fixture, w failedReturnExpectations, wantContent, wantFollow string) {
	t.Helper()
	it, err := f.svc.GetItem(w.idA)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != w.fromShift {
		t.Fatalf("事项应仍留在交班班次 %s，got %s", w.fromShift, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != w.fromShift {
		t.Fatalf("事项流经班次不应变化：%v", it.ShiftIDs)
	}
	if it.Content != wantContent || it.FollowOwner != wantFollow {
		t.Fatalf("事项原文与后续负责人应保持原值：content=%s follow=%s", it.Content, it.FollowOwner)
	}
	if n := countItems(f, w.idA); n != 1 {
		t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", w.idA, n)
	}
	for _, ev := range it.Events {
		if ev.Kind == "received" {
			t.Fatalf("失败尝试不应在事项历史留下接收记录：%+v", ev)
		}
		if !ev.At.IsZero() && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应写入事项历史：%+v", w.failAt, ev)
		}
		if ev.Operator == w.failedOperator || strings.Contains(ev.Detail, w.failedReason) {
			t.Fatalf("失败尝试的操作人与原因不应写入事项历史：%+v", ev)
		}
	}
}

// assertNoFailedReturnTraceInJourney 核对处理经过时间线里不出现失败尝试的
// 时刻、原因或操作人。
func assertNoFailedReturnTraceInJourney(t *testing.T, w failedReturnExpectations, j ItemJourney) {
	t.Helper()
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
		}
		if strings.Contains(ev.Reason, w.failedReason) ||
			strings.Contains(ev.Detail, w.failedReason) ||
			strings.Contains(ev.Supplement, w.failedReason) {
			t.Fatalf("失败尝试的退回原因不应出现在处理经过：%+v", ev)
		}
		if ev.Operator == w.failedOperator {
			t.Fatalf("失败尝试的操作人 %s 不应出现在处理经过：%+v", w.failedOperator, ev)
		}
	}
}

// assertFromShiftSnapshotUnchanged 核对交班班次结束时记录保留原来的内容与
// 负责人（失败的退回不改变冻结记录）。
func assertFromShiftSnapshotUnchanged(t *testing.T, f *fixture, w failedReturnExpectations) {
	t.Helper()
	rep, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, w.idA)
	if snap == nil || snap.Content != "事项甲" || snap.FollowOwner != "李四" {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}
}

// TestReturnSaveFailureFirstAtomicRollback：首次退回时，接班人填写了有效操作人、
// 非空原因，事项也正处于允许退回的待处理状态，业务条件全部成立、真正进入保存
// 过程后本地写盘失败——退回操作必须明确返回保存错误，不能返回成功退回的结果；
// 内存与磁盘都回到失败前：该项仍是待处理、接班人尚未处理，不产生退回轮次，
// 失败尝试的原因、人名与时刻都不是已发生的退回，交接未完成、无完成时间，接班
// 班次不能结束，同交接已确认接收的另一项与交班班次结束时记录不受影响，原数据
// 文件一个字节不变。保存恢复后对同一事项重新执行退回，只新增一轮成功记录，
// 记录本次成功操作的原因、操作人与实际处理时间；失败尝试不占用轮次、不混入
// 经过。成功退回的当前结果明确表示等待交班人补充，事项仍留在交班班次，不因
// 这次退回而被接收。
func TestReturnSaveFailureFirstAtomicRollback(t *testing.T) {
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

	want := failedReturnExpectations{
		hID: h.ID, fromShift: a.ID, toShift: b.ID, idA: idA, idB: idB,
		otherConfirmedAt: otherConfirmedAt,
		preRounds:        0,
		failAt:           tsDay(2, 17, 0),
		failedReason:     "第一次退回原因-失败尝试",
		failedOperator:   "接班人王五",
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的处理时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 操作人有效、原因非空、事项待处理允许退回；使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	failed, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, want.failedOperator, want.failedReason, "", "")
	if err == nil {
		t.Fatalf("保存失败时退回应明确返回错误，不能返回成功退回的结果")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	// 业务条件全部成立，不能用缺少原因、操作人为空、编号不存在或状态不允许的
	// 拒绝来冒充这个保存失败场景。
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	// 返回给调用方的只能是零值交接结果：不能带本次尚未保存的退回状态、操作人、
	// 处理时间或新增的第1轮退回，也不能把失败前的整份交接当作退回结果。
	assertZeroHandoverResult(t, failed)

	// 当前打开的数据上查看交接与处理经过，应看到失败前的事实，而不是只保证
	// 文件没变、查询却显示已经退回。
	assertPendingBeforeAnyReturn(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的退回不能成为其中的业务事实")
	}

	// 退出后重新打开：仍是待处理、无退回轮次，失败尝试不产生任何退回事实。
	f.reopen(t)
	assertPendingBeforeAnyReturn(t, f, want)

	// 保存恢复正常后，对同一事项重新执行退回：按现有功能成功，只新增一轮。
	restoreSaving(t, f)
	successAt := tsDay(2, 18, 0)
	successReason := "第一次退回原因-成功"
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", successReason, "", ""); err != nil {
		t.Fatalf("恢复后退回应成功：%v", err)
	}

	// 交接：只成功退回这一轮，记录本次成功操作的原因、操作人与实际处理时间；
	// 失败尝试不占用轮次（成功记录是第1轮而不是第2轮）。
	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("退回不接收事项，整份交接应继续未完成、无完成时间：%+v", h2)
	}
	le := findEntryOf(t, h2, idA)
	if le.Status != EntryReturned || le.Operator != "李四" ||
		le.ProcessedAt == nil || !le.ProcessedAt.Equal(successAt) {
		t.Fatalf("当前结果应为本次成功退回，记录成功操作人与实际处理时间：%+v", le)
	}
	if len(le.Rounds) != 1 {
		t.Fatalf("失败尝试不占用轮次，成功后应只有一轮，got %d：%+v", len(le.Rounds), le.Rounds)
	}
	r := le.Rounds[0]
	if r.Seq != 1 || r.Reason != successReason || r.ReturnOperator != "李四" ||
		!r.ReturnedAt.Equal(successAt) || r.Supplement != "" || r.ResubmittedAt != nil {
		t.Fatalf("第1轮应只记录本次成功退回：%+v", r)
	}

	// 事项仍留在交班班次，没有被这次退回接收，原文、负责人不变。
	it, err := f.svc.GetItem(idA)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != a.ID {
		t.Fatalf("成功退回不应接收事项，应仍留在交班班次：%+v", it)
	}
	if it.Content != "事项甲" || it.FollowOwner != "李四" {
		t.Fatalf("事项原文与后续负责人应不变：%+v", it)
	}

	// 处理经过只呈现成功的这一次退回，明确等待交班人补充，不混入失败尝试。
	j, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return" {
		t.Fatalf("处理经过应只留下成功的一次退回，got %s", got)
	}
	returns := 0
	for _, ev := range j.Events {
		if ev.Kind == "return" {
			returns++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "李四" ||
				ev.RoundSeq != 1 || ev.Reason != successReason {
				t.Fatalf("成功退回事件内容不正确：%+v", ev)
			}
		}
		if ev.TimeKnown && ev.At.Equal(want.failAt) {
			t.Fatalf("失败尝试时刻不应出现在处理经过：%+v", ev)
		}
		if ev.Operator == want.failedOperator || strings.Contains(ev.Reason, want.failedReason) {
			t.Fatalf("失败尝试的人名与原因不应混入处理经过：%+v", ev)
		}
	}
	if returns != 1 {
		t.Fatalf("应只有一次退回记录，got %d", returns)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("交接当前结果应为退回：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "退回（等待交班人补充）") {
		t.Fatalf("成功退回的当前结果应明确表示等待交班人补充：\n%s", jText)
	}
	if strings.Contains(jText, want.failedReason) || strings.Contains(jText, want.failedOperator) {
		t.Fatalf("失败尝试的原因与人名不能混入成功后的处理经过：\n%s", jText)
	}
	text := FormatHandover(h2)
	if !strings.Contains(text, "[退回]") || !strings.Contains(text, "第1次退回") ||
		!strings.Contains(text, successReason) {
		t.Fatalf("交接查询应展示成功退回与第1轮原因：\n%s", text)
	}
	if strings.Contains(text, want.failedReason) || strings.Contains(text, want.failedOperator) ||
		strings.Contains(text, fmtTime(want.failAt)) {
		t.Fatalf("失败尝试的原因、人名与时刻不应出现在交接查询中：\n%s", text)
	}

	// 退回等待补充期间接班班次仍不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项等待补充时接班班次不能结束，got %v", err)
	}

	// 成功落盘的文件只包含成功事实，不包含失败尝试的原因、人名与时刻。
	rawSuccess, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after success: %v", err)
	}
	if !strings.Contains(string(rawSuccess), successAt.Format("2006-01-02T15:04:05-07:00")) ||
		!strings.Contains(string(rawSuccess), successReason) {
		t.Fatalf("成功退回的时间与原因应已写入数据文件")
	}
	if strings.Contains(string(rawSuccess), want.failAt.Format("2006-01-02T15:04:05-07:00")) ||
		strings.Contains(string(rawSuccess), want.failedReason) ||
		strings.Contains(string(rawSuccess), want.failedOperator) {
		t.Fatalf("失败尝试的时刻、原因与人名不应留在数据文件中")
	}

	// 退出重开后，成功退回的事实完整保留，失败尝试仍无痕迹。
	f.reopen(t)
	h3, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("reopen get handover: %v", err)
	}
	if h3.Completed() || h3.CompletedAt != nil {
		t.Fatalf("重开后交接仍应未完成：%+v", h3)
	}
	re := findEntryOf(t, h3, idA)
	if re.Status != EntryReturned || len(re.Rounds) != 1 ||
		re.Rounds[0].Reason != successReason || re.Rounds[0].ReturnOperator != "李四" {
		t.Fatalf("重开后成功退回的一轮应完整保留：%+v", re)
	}
	j3, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j3)); got != "created,handover-init,return" {
		t.Fatalf("重开后处理经过应只保留成功的一次退回，got %s", got)
	}
}

// TestReturnSaveFailureSecondAfterResubmitAtomicRollback：事项已经成功退回过
// 一轮、交班人补充并重新提交（恢复待处理）后，接班人再次退回时填写了有效
// 操作人、非空原因且状态允许，但本地写盘失败——操作必须明确失败，不能当成
// 退回成功；内存与磁盘都回到失败前：该项仍是待处理，先前那一轮的原因、退回人、
// 退回时间、补充说明、补充人和重新提交时间全部保留，不增加下一轮，也不把以前
// 的补充改成这次尝试的内容；同交接已接收的另一项不受影响。保存恢复后对同一
// 事项再次退回，只新增一轮（第2轮）成功记录；随后交班人补充重新提交、接班人
// 确认接收的既有规则仍成立，整份交接在确认时刻才完成。
func TestReturnSaveFailureSecondAfterResubmitAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idA, idB := items[0].ID, items[1].ID

	// 同交接另一项先成功接收。
	otherConfirmedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return otherConfirmedAt })
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}

	// 第一轮：成功退回 -> 交班人补充并重新提交，事项恢复待处理。
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
	pre, _ := f.svc.GetHandover(h.ID)
	preEntry := findEntryOf(t, pre, idA)
	if preEntry.Status != EntryPending || len(preEntry.Rounds) != 1 ||
		preEntry.Rounds[0].ResubmittedAt == nil {
		t.Fatalf("前置：事项应已补充重新提交、恢复待处理且保留一轮：%+v", preEntry)
	}

	want := failedReturnExpectations{
		hID: h.ID, fromShift: a.ID, toShift: b.ID, idA: idA, idB: idB,
		otherConfirmedAt: otherConfirmedAt,
		preRounds:        1,
		r1ReturnedAt:     r1ReturnedAt, r1Reason: "第一轮原因",
		r1ResubmitAt: r1ResubmitAt, r1Supplement: "第一轮补充内容",
		failAt:         tsDay(2, 18, 0),
		failedReason:   "第二轮退回原因-失败尝试",
		failedOperator: "接班人王五",
	}

	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 再次退回：操作人有效、原因非空、事项待处理允许退回；写盘在保存阶段失败。
	breakSaving(t, f)
	failed, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, want.failedOperator, want.failedReason, "", "")
	if err == nil {
		t.Fatalf("保存失败时再次退回应明确返回错误，不能返回成功退回的结果")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	// 返回零值交接结果：不能带本次尚未保存的第2轮退回，也不能把失败前保留着
	// 第一轮记录的整份交接当作再次退回的结果。
	assertZeroHandoverResult(t, failed)

	assertPendingAfterOneRound(t, f, want)

	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的退回不能成为其中的业务事实")
	}

	f.reopen(t)
	assertPendingAfterOneRound(t, f, want)

	// 保存恢复正常后再次退回：只新增第2轮成功记录，失败尝试不占用轮次。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	successReason := "第二轮退回原因-成功"
	f.svc.nowAt(func() time.Time { return successAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", successReason, "", ""); err != nil {
		t.Fatalf("恢复后再次退回应成功：%v", err)
	}

	h2, err := f.svc.GetHandover(h.ID)
	if err != nil {
		t.Fatalf("get handover after retry: %v", err)
	}
	if h2.Completed() || h2.CompletedAt != nil {
		t.Fatalf("再次退回不接收事项，交接应继续未完成：%+v", h2)
	}
	le := findEntryOf(t, h2, idA)
	if le.Status != EntryReturned || le.Operator != "李四" ||
		le.ProcessedAt == nil || !le.ProcessedAt.Equal(successAt) {
		t.Fatalf("当前结果应为本次成功退回：%+v", le)
	}
	if len(le.Rounds) != 2 {
		t.Fatalf("失败尝试不占用轮次，成功后应只有两轮，got %d：%+v", len(le.Rounds), le.Rounds)
	}
	s1, s2 := le.Rounds[0], le.Rounds[1]
	// 先前那一轮完整保留。
	if s1.Seq != 1 || s1.Reason != want.r1Reason || s1.ReturnOperator != "李四" ||
		!s1.ReturnedAt.Equal(want.r1ReturnedAt) ||
		s1.Supplement != want.r1Supplement || s1.SupplementOperator != "张三" ||
		s1.SupplementAt == nil || !s1.SupplementAt.Equal(want.r1ResubmitAt) ||
		s1.ResubmittedAt == nil || !s1.ResubmittedAt.Equal(want.r1ResubmitAt) {
		t.Fatalf("第一轮退回与补充应继续完整保留：%+v", s1)
	}
	// 新一轮记录本次成功操作，尚无补充。
	if s2.Seq != 2 || s2.Reason != successReason || s2.ReturnOperator != "李四" ||
		!s2.ReturnedAt.Equal(successAt) || s2.Supplement != "" ||
		s2.SupplementOperator != "" || s2.ResubmittedAt != nil {
		t.Fatalf("第2轮应只记录本次成功退回、重新等待补充：%+v", s2)
	}

	// 事项仍留在交班班次，原文、负责人不变。
	it, err := f.svc.GetItem(idA)
	if err != nil {
		t.Fatalf("get item after retry: %v", err)
	}
	if it.CurrentShiftID != a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != a.ID {
		t.Fatalf("再次退回成功不应接收事项，应仍留在交班班次：%+v", it)
	}
	if it.Content != "事项甲" || it.FollowOwner != "李四" {
		t.Fatalf("事项原文与后续负责人应不变：%+v", it)
	}

	// 处理经过：两轮退回（第2轮只反映成功操作）与第一轮重新提交，不混入失败
	// 尝试的原因、人名或时刻。
	j, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey after retry: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,return,resubmit,return" {
		t.Fatalf("处理经过应呈现两轮成功退回与第一轮重新提交，got %s", got)
	}
	secondReturns := 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(want.failAt) {
			t.Fatalf("失败尝试时刻不应出现在处理经过：%+v", ev)
		}
		if ev.Operator == want.failedOperator || strings.Contains(ev.Reason, want.failedReason) ||
			strings.Contains(ev.Supplement, want.failedReason) {
			t.Fatalf("失败尝试的人名与原因不能混入处理经过：%+v", ev)
		}
		if ev.Kind == "return" && ev.RoundSeq == 2 {
			secondReturns++
			if !ev.TimeKnown || !ev.At.Equal(successAt) || ev.Operator != "李四" ||
				ev.Reason != successReason {
				t.Fatalf("第2轮退回应只反映成功操作：%+v", ev)
			}
		}
	}
	if secondReturns != 1 {
		t.Fatalf("第2轮应只有一次（成功的）退回，got %d", secondReturns)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "退回（等待交班人补充）") {
		t.Fatalf("成功退回的当前结果应明确表示等待交班人补充：\n%s", jText)
	}
	if strings.Contains(jText, want.failedReason) || strings.Contains(jText, want.failedOperator) {
		t.Fatalf("失败尝试的原因与人名不能混入成功后的处理经过：\n%s", jText)
	}

	// 退回等待补充期间接班班次不能结束。
	if _, err := f.svc.CloseShift(b.ID); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("退回项等待补充时接班班次不能结束，got %v", err)
	}

	// 保留既有规则：交班人就第2轮补充重新提交，接班人确认接收后事项才进入
	// 接班班次，整份交接在确认时刻才完成。
	r2ResubmitAt := tsDay(2, 19, 30)
	f.svc.nowAt(func() time.Time { return r2ResubmitAt })
	if _, err := f.svc.ResubmitReturned(h.ID, idA, "张三", "第二轮补充-成功提交"); err != nil {
		t.Fatalf("resubmit round 2: %v", err)
	}
	confirmAt := tsDay(2, 20, 0)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重新提交后确认接收应成功：%v", err)
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

	// 退出重开后，两轮成功退回、补充与最终接收完整保留，失败尝试仍无痕迹。
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
		re.Rounds[0].Supplement != want.r1Supplement ||
		re.Rounds[1].Reason != successReason ||
		re.Rounds[1].Supplement != "第二轮补充-成功提交" {
		t.Fatalf("重开后两轮退回与补充应完整保留：%+v", re.Rounds)
	}
	j4, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("reopen journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j4)); got != "created,handover-init,return,resubmit,return,resubmit,confirm" {
		t.Fatalf("重开后处理经过应只保留成功事实，got %s", got)
	}
	for _, ev := range j4.Events {
		if ev.TimeKnown && ev.At.Equal(want.failAt) ||
			ev.Operator == want.failedOperator ||
			strings.Contains(ev.Reason, want.failedReason) {
			t.Fatalf("重开后失败尝试的时刻、人名与原因仍不应出现：%+v", ev)
		}
	}

	// 全部接收后接班班次才能结束。
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("全部接收后接班班次应能结束：%v", err)
	}
}

// TestReturnBlankReasonRejectedWithoutSaving：退回原因为空或只有空白时沿用
// 现有必填校验，明确拒绝操作，不留下任何退回事实（不产生轮次、不改当前结果、
// 交接仍未完成），也不发生保存。
func TestReturnBlankReasonRejectedWithoutSaving(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, _ := f.svc.CreateHandover(a.ID, b.ID)
	idA := items[0].ID

	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	for _, reason := range []string{"", "   ", "\t\n "} {
		if _, err := f.svc.ProcessEntry(h.ID, idA, ActionReturn, "李四", reason, "", ""); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("空白退回原因 %q 应按必填校验拒绝，got %v", reason, err)
		}
	}

	got, _ := f.svc.GetHandover(h.ID)
	e := findEntryOf(t, got, idA)
	if e.Status != EntryPending || e.Operator != "" || e.ProcessedAt != nil || len(e.Rounds) != 0 {
		t.Fatalf("校验拒绝不应留下任何退回事实：%+v", e)
	}
	if got.Completed() || got.CompletedAt != nil {
		t.Fatalf("校验拒绝后交接应仍未完成：%+v", got)
	}
	it, _ := f.svc.GetItem(idA)
	if it.CurrentShiftID != a.ID {
		t.Fatalf("校验拒绝不应移动事项：%+v", it)
	}
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after rejection: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("校验拒绝不应发生写入、不应改动数据文件")
	}
}
