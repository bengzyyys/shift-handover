package handover

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// resubmitFailFixture 汇集“退回项补充说明并重新提交”保存失败场景的前置数据
// 与各次操作的固定时刻/文本，供失败回滚、业务拒绝区分与恢复后成功等用例复用。
//
// 事项甲（idA）在同一交接中已经历两轮完整的“退回-补充-重新提交”，第三次又被
// 退回、当前正等待交班人补充；事项乙（idB）已被接班人确认接收；事项丙在交班
// 班次结束前已关闭，不进入交接清单。
type resubmitFailFixture struct {
	f                                 *fixture
	a, b                              Shift
	h                                 Handover
	idA, idB                          string
	confirmBAt                        time.Time
	returnAt                          [3]time.Time
	resubAt                           [3]time.Time // 第三轮在成功重新提交前没有该时间
	reasons, supplements              [3]string    // 第三轮 supplements[2] 初始为空
	failAt, successAt                 time.Time
	finalConfirmAt                    time.Time
	failSupplement, successSupplement string
}

// setupResubmitFailFixture 构造上述前置状态，并固定后续失败/成功尝试的时间。
func setupResubmitFailFixture(t *testing.T) *resubmitFailFixture {
	t.Helper()
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	sc := &resubmitFailFixture{
		f:          f,
		a:          a,
		b:          b,
		h:          h,
		idA:        items[0].ID,
		idB:        items[1].ID,
		confirmBAt: tsDay(2, 16, 30),
		returnAt:   [3]time.Time{tsDay(2, 17, 0), tsDay(2, 17, 20), tsDay(2, 17, 40)},
		// 只有前两轮已成功重新提交；第三轮成功前该时间保持零值。
		resubAt:           [3]time.Time{0: tsDay(2, 17, 10), 1: tsDay(2, 17, 30)},
		reasons:           [3]string{"第一轮退回原因", "第二轮退回原因", "第三轮退回原因"},
		supplements:       [3]string{"第一轮补充说明", "第二轮补充说明", ""},
		failAt:            tsDay(2, 18, 0),
		successAt:         tsDay(2, 19, 0),
		finalConfirmAt:    tsDay(2, 19, 30),
		failSupplement:    "失败尝试填写的第三轮补充说明",
		successSupplement: "第三轮补充说明（保存恢复后提交）",
	}

	// 事项乙先确认接收；事项甲走完两轮退回与补充，第三次被退回后等待补充。
	f.svc.nowAt(func() time.Time { return sc.confirmBAt })
	if _, err := f.svc.ProcessEntry(h.ID, sc.idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm idB: %v", err)
	}
	f.svc.nowAt(func() time.Time { return sc.returnAt[0] })
	if _, err := f.svc.ProcessEntry(h.ID, sc.idA, ActionReturn, "李四", sc.reasons[0], "", ""); err != nil {
		t.Fatalf("return 1: %v", err)
	}
	f.svc.nowAt(func() time.Time { return sc.resubAt[0] })
	if _, err := f.svc.ResubmitReturned(h.ID, sc.idA, "张三", sc.supplements[0]); err != nil {
		t.Fatalf("resubmit 1: %v", err)
	}
	f.svc.nowAt(func() time.Time { return sc.returnAt[1] })
	if _, err := f.svc.ProcessEntry(h.ID, sc.idA, ActionReturn, "李四", sc.reasons[1], "", ""); err != nil {
		t.Fatalf("return 2: %v", err)
	}
	f.svc.nowAt(func() time.Time { return sc.resubAt[1] })
	if _, err := f.svc.ResubmitReturned(h.ID, sc.idA, "张三", sc.supplements[1]); err != nil {
		t.Fatalf("resubmit 2: %v", err)
	}
	f.svc.nowAt(func() time.Time { return sc.returnAt[2] })
	if _, err := f.svc.ProcessEntry(h.ID, sc.idA, ActionReturn, "李四", sc.reasons[2], "", ""); err != nil {
		t.Fatalf("return 3: %v", err)
	}

	got, _ := f.svc.GetHandover(h.ID)
	ea := findEntryOf(t, got, sc.idA)
	if ea.Status != EntryReturned || len(ea.Rounds) != 3 {
		t.Fatalf("前置：事项甲应为第三次退回、共3轮，got %s %d 轮", ea.Status, len(ea.Rounds))
	}
	if got.Completed() || got.CompletedAt != nil {
		t.Fatalf("前置：仍有退回项，交接应未完成且无完成时间")
	}
	return sc
}

// assertStillAwaitingSupplement 在当前打开的数据上核对失败尝试之后（或退出重开
// 之后）的事实：事项当前仍是退回、继续等待交班人补充；最近一轮退回原因、退回人、
// 退回时间与轮次保留，不出现本次失败尝试填写的补充说明、补充人、补充时间或重新
// 提交时间；此前两轮已成功保存的说明不丢失、不被覆盖、也不多出一轮；原来保存的
// 接班处理人和处理时间保持原样。同一交接中已接收的另一项、事项归属、交班班次
// 结束时记录与交接数量都不变，各查询与已保存事实一致。
func assertStillAwaitingSupplement(t *testing.T, sc *resubmitFailFixture) {
	t.Helper()
	f := sc.f
	failTimeText := sc.failAt.Format(displayTime)

	// 交接：整份继续未完成、无完成时间。
	h, err := f.svc.GetHandover(sc.h.ID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if h.Completed() || h.CompletedAt != nil {
		t.Fatalf("保存失败后整份交接应继续未完成、不能留下完成时间：%+v", h)
	}

	// 事项甲：当前结果仍为退回，当前处理人与处理时间沿用第三次退回的事实，
	// 不能提前清空或显示尚未处理。
	ea := findEntryOf(t, h, sc.idA)
	if ea.Status != EntryReturned {
		t.Fatalf("失败后当前结果仍应为退回，got %s", ea.Status)
	}
	if ea.Operator != "李四" || ea.ProcessedAt == nil || !ea.ProcessedAt.Equal(sc.returnAt[2]) {
		t.Fatalf("第三次退回的处理人与处理时间应保持原样：%+v want 李四 %v", ea, sc.returnAt[2])
	}
	if len(ea.Rounds) != 3 {
		t.Fatalf("失败尝试不得增加退回轮次，应仍为3轮，got %d", len(ea.Rounds))
	}
	for i := 0; i < 3; i++ {
		r := ea.Rounds[i]
		if r.Seq != i+1 || r.Reason != sc.reasons[i] || r.ReturnOperator != "李四" ||
			!r.ReturnedAt.Equal(sc.returnAt[i]) {
			t.Fatalf("第%d轮退回原因/退回人/退回时间应保持原样：%+v", i+1, r)
		}
		if i < 2 {
			// 此前已经成功保存的各轮说明不能丢失或被覆盖。
			if r.Supplement != sc.supplements[i] || r.SupplementOperator != "张三" ||
				r.SupplementAt == nil || !r.SupplementAt.Equal(sc.resubAt[i]) ||
				r.ResubmittedAt == nil || !r.ResubmittedAt.Equal(sc.resubAt[i]) {
				t.Fatalf("第%d轮已保存的补充与重新提交经过应完整保留：%+v", i+1, r)
			}
		} else {
			// 最近一轮不允许出现失败尝试的任何痕迹。
			if r.Supplement != "" || r.SupplementOperator != "" ||
				r.SupplementAt != nil || r.ResubmittedAt != nil {
				t.Fatalf("第三轮不得写入失败尝试的补充/补充人/补充时间/重新提交时间：%+v", r)
			}
		}
	}

	// 同一交接中另一项已经接收的结果、处理人和处理时间保持不变。
	eb := findEntryOf(t, h, sc.idB)
	if eb.Status != EntryConfirmed || eb.Operator != "李四" ||
		eb.ProcessedAt == nil || !eb.ProcessedAt.Equal(sc.confirmBAt) {
		t.Fatalf("已接收的其他事项应保持原接收结果：%+v", eb)
	}

	// 事项继续留在交班班次，原文、严重程度、限制条件与后续负责人不因补充
	// 失败而变化，也不生成另一条交接或复制事项。
	it, err := f.svc.GetItem(sc.idA)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != sc.a.ID {
		t.Fatalf("事项应继续留在交班班次 %s，got %s", sc.a.ID, it.CurrentShiftID)
	}
	if len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != sc.a.ID {
		t.Fatalf("流经班次不应变化：%v", it.ShiftIDs)
	}
	if it.Content != "事项甲" || it.Severity != SeverityNormal ||
		it.Constraints != "" || it.FollowOwner != "李四" {
		t.Fatalf("原文、严重程度、限制条件与后续负责人不应变化：%+v", it)
	}
	for _, ev := range it.Events {
		if strings.Contains(ev.Detail, sc.failSupplement) {
			t.Fatalf("事项历史不应出现失败尝试的补充说明：%+v", ev)
		}
	}
	if len(f.store.data.Items) != 3 {
		t.Fatalf("失败尝试不得复制事项，应仍为3项，got %d", len(f.store.data.Items))
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 {
		t.Fatalf("失败尝试不得生成另一条交接，应仍为1条，got %d", len(hs))
	}

	// 处理经过只呈现失败前的事实：没有第三轮重新提交，也没有失败时刻或
	// 失败说明；当前结果仍为退回、等待交班人补充。
	j, err := f.svc.ItemJourney(sc.idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	wantKinds := "created,handover-init,return,resubmit,return,resubmit,return"
	if got := joinStrings(journeyKinds(j)); got != wantKinds {
		t.Fatalf("处理经过应只保留失败前的两轮退回/补充与第三次退回，got %s", got)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryReturned {
		t.Fatalf("交接当前结果应仍为退回：%+v", j.Results)
	}
	for _, ev := range j.Events {
		if ev.Kind == "resubmit" && ev.RoundSeq == 3 {
			t.Fatalf("失败尝试不应产生第三轮重新提交经过：%+v", ev)
		}
		if ev.TimeKnown && ev.At.Equal(sc.failAt) {
			t.Fatalf("处理经过不应出现失败尝试时刻 %v：%+v", sc.failAt, ev)
		}
		if strings.Contains(ev.Supplement, sc.failSupplement) ||
			strings.Contains(ev.Detail, sc.failSupplement) {
			t.Fatalf("处理经过不应出现失败尝试的说明：%+v", ev)
		}
	}
	jtext := FormatItemJourney(j)
	if !strings.Contains(jtext, "等待交班人补充") {
		t.Fatalf("当前结果应显示等待交班人补充：\n%s", jtext)
	}
	if strings.Contains(jtext, sc.failSupplement) || strings.Contains(jtext, failTimeText) {
		t.Fatalf("处理经过查询不能显示成已提交：\n%s", jtext)
	}

	// 交接查询与已保存事实一致：未完成、退回、保留第三次退回原因与退回人，
	// 不出现失败尝试；另一项仍为确认接收。
	htext := FormatHandover(h)
	if !strings.Contains(htext, "[未完成]") || !strings.Contains(htext, "[退回]") {
		t.Fatalf("交接查询应显示未完成且事项为退回：\n%s", htext)
	}
	if !strings.Contains(htext, "[确认接收]") {
		t.Fatalf("另一项的确认接收结果应保留：\n%s", htext)
	}
	if !strings.Contains(htext, sc.reasons[2]) || !strings.Contains(htext, "处理人=李四") {
		t.Fatalf("第三次退回原因与退回人应保留在查询中：\n%s", htext)
	}
	if strings.Contains(htext, sc.failSupplement) || strings.Contains(htext, failTimeText) {
		t.Fatalf("交接查询不应出现失败尝试的说明或时刻：\n%s", htext)
	}

	// 交班班次的结束时事项记录保持原内容与负责人。
	rep, err := f.svc.ShiftReport(sc.a.ID)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(rep, sc.idA)
	if snap == nil || snap.Content != "事项甲" || snap.FollowOwner != "李四" {
		t.Fatalf("交班班次结束时记录应保持原内容与负责人：%+v", snap)
	}
}

// assertResubmittedSuccess 核对保存恢复后成功的重新提交：仅该事项恢复待处理、
// 当前处理人和处理时间显示尚未处理；新补充写在最近一轮并记录成功提交时的补充人、
// 补充时间与重新提交时间，原退回信息及前两轮内容继续保留；处理经过只呈现成功的
// 这次第三轮重新提交，不混入失败尝试；重新提交不等于接收，事项不移动、交接不
// 提前完成。
func assertResubmittedSuccess(t *testing.T, sc *resubmitFailFixture) {
	t.Helper()
	f := sc.f
	failTimeText := sc.failAt.Format(displayTime)
	successTimeText := sc.successAt.Format(displayTime)

	h, err := f.svc.GetHandover(sc.h.ID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if h.Completed() || h.CompletedAt != nil {
		t.Fatalf("重新提交不等于接收，交接应继续未完成：%+v", h)
	}
	ea := findEntryOf(t, h, sc.idA)
	if ea.Status != EntryPending {
		t.Fatalf("成功重新提交后该项应恢复待处理，got %s", ea.Status)
	}
	if ea.Operator != "" || ea.ProcessedAt != nil {
		t.Fatalf("当前接班处理人和处理时间应显示尚未处理：%+v", ea)
	}
	if len(ea.Rounds) != 3 {
		t.Fatalf("轮次应仍为3轮，不能新增一轮，got %d", len(ea.Rounds))
	}
	r3 := ea.Rounds[2]
	if r3.Reason != sc.reasons[2] || r3.ReturnOperator != "李四" ||
		!r3.ReturnedAt.Equal(sc.returnAt[2]) {
		t.Fatalf("第三轮原退回原因、退回人、退回时间应继续保留：%+v", r3)
	}
	if r3.Supplement != sc.successSupplement || r3.SupplementOperator != "张三" ||
		r3.SupplementAt == nil || !r3.SupplementAt.Equal(sc.successAt) ||
		r3.ResubmittedAt == nil || !r3.ResubmittedAt.Equal(sc.successAt) {
		t.Fatalf("最近一轮应记录成功提交的补充、补充人、补充时间与重新提交时间：%+v", r3)
	}
	for i := 0; i < 2; i++ {
		r := ea.Rounds[i]
		if r.Supplement != sc.supplements[i] || !r.ResubmittedAt.Equal(sc.resubAt[i]) {
			t.Fatalf("第%d轮更早的内容应继续保留：%+v", i+1, r)
		}
	}
	eb := findEntryOf(t, h, sc.idB)
	if eb.Status != EntryConfirmed || eb.Operator != "李四" ||
		eb.ProcessedAt == nil || !eb.ProcessedAt.Equal(sc.confirmBAt) {
		t.Fatalf("另一项的接收结果不应受影响：%+v", eb)
	}

	it, _ := f.svc.GetItem(sc.idA)
	if it.CurrentShiftID != sc.a.ID || len(it.ShiftIDs) != 1 || it.ShiftIDs[0] != sc.a.ID {
		t.Fatalf("重新提交不移动事项，应仍留在交班班次：%+v", it)
	}
	if it.FollowOwner != "李四" {
		t.Fatalf("后续负责人不应因重新提交变化，got %s", it.FollowOwner)
	}

	// 处理经过：第三次重新提交恰好一次，内容与时刻都是成功这次；
	// 不混入失败尝试的说明或 18:00 时刻。
	j, err := f.svc.ItemJourney(sc.idA)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	wantKinds := "created,handover-init,return,resubmit,return,resubmit,return,resubmit"
	if got := joinStrings(journeyKinds(j)); got != wantKinds {
		t.Fatalf("处理经过应只多出成功的第三轮重新提交，got %s", got)
	}
	resubmits := 0
	for _, ev := range j.Events {
		if ev.Kind == "resubmit" {
			if ev.RoundSeq == 3 {
				resubmits++
				if !ev.TimeKnown || !ev.At.Equal(sc.successAt) || ev.Operator != "张三" ||
					ev.Supplement != sc.successSupplement {
					t.Fatalf("第三轮重新提交应记录成功这次的事实：%+v", ev)
				}
			}
			if ev.TimeKnown && ev.At.Equal(sc.failAt) {
				t.Fatalf("处理经过不得混入失败尝试时刻：%+v", ev)
			}
			if strings.Contains(ev.Supplement, sc.failSupplement) {
				t.Fatalf("处理经过不得混入失败尝试的说明：%+v", ev)
			}
		}
	}
	if resubmits != 1 {
		t.Fatalf("第三轮重新提交应只呈现成功的一次，got %d", resubmits)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryPending {
		t.Fatalf("交接当前结果应为待处理：%+v", j.Results)
	}

	htext := FormatHandover(h)
	if !strings.Contains(htext, "处理人=尚未处理；处理时间=尚未处理") {
		t.Fatalf("交接查询应把恢复待处理项显示为尚未处理：\n%s", htext)
	}
	if !strings.Contains(htext, sc.successSupplement) || !strings.Contains(htext, successTimeText) {
		t.Fatalf("交接查询应呈现成功这次的补充与重新提交时间：\n%s", htext)
	}
	if strings.Contains(htext, sc.failSupplement) || strings.Contains(htext, failTimeText) {
		t.Fatalf("交接查询不得混入失败尝试：\n%s", htext)
	}
}

// TestResubmitReturnedSaveFailureAtomicRollback：交班人对“业务条件全部成立”的
// 第三次退回项填写了操作人与非空补充说明，保存本地数据时失败——必须明确返回
// 保存错误，不能当成提交成功；失败后当前打开的数据、退出重开后的数据与各类
// 查询都只呈现失败前的事实。保存恢复后在原交接记录上对同一退回项补充并重新
// 提交成功，仅该项恢复待处理、处理经过只留下成功这次；重新提交不等于接收，
// 接班人随后逐项确认，事项才进入接班班次、交接才完成。
func TestResubmitReturnedSaveFailureAtomicRollback(t *testing.T) {
	sc := setupResubmitFailFixture(t)
	f := sc.f

	// 记录失败前已落盘的文件内容。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}

	// 业务条件全部成立（操作人、非空补充、编号存在、状态为退回），仅在原子
	// 写盘阶段失败。
	breakSaving(t, f)
	f.svc.nowAt(func() time.Time { return sc.failAt })
	_, err = f.svc.ResubmitReturned(sc.h.ID, sc.idA, "张三", sc.failSupplement)
	if err == nil {
		t.Fatalf("保存失败时补充重新提交应明确返回错误，不能当成成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrHandoverState) {
		t.Fatalf("业务条件全部成立时的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据上查看交接与处理经过，应看到失败前的事实，而不是只保证
	// 文件没变、查询却显示已提交。
	assertStillAwaitingSupplement(t, sc)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 退出后重新打开：退回与补充记录、交接进度、事项归属与失败前一致，
	// 失败尝试不产生重新提交经过。
	f.reopen(t)
	assertStillAwaitingSupplement(t, sc)

	// 保存恢复正常后，交班人仍能在原交接记录上对同一退回项补充并重新提交。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return sc.successAt })
	if _, err := f.svc.ResubmitReturned(sc.h.ID, sc.idA, "张三", sc.successSupplement); err != nil {
		t.Fatalf("保存恢复后补充重新提交应成功：%v", err)
	}
	assertResubmittedSuccess(t, sc)

	// 重开后成功结果同样完整保留，失败尝试仍不在任何经过中。
	f.reopen(t)
	assertResubmittedSuccess(t, sc)

	// 重新提交仍不等于接收：接班人之后逐项确认，事项才移动、交接才完成。
	f.svc.nowAt(func() time.Time { return sc.finalConfirmAt })
	if _, err := f.svc.ProcessEntry(sc.h.ID, sc.idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("重新提交后接班人应能按原规则确认接收：%v", err)
	}
	h, _ := f.svc.GetHandover(sc.h.ID)
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(sc.finalConfirmAt) {
		t.Fatalf("接班人逐项确认后交接才完成，完成时间应为 %v：%+v", sc.finalConfirmAt, h)
	}
	ea := findEntryOf(t, h, sc.idA)
	if ea.Status != EntryConfirmed || ea.Operator != "李四" ||
		ea.ProcessedAt == nil || !ea.ProcessedAt.Equal(sc.finalConfirmAt) {
		t.Fatalf("事项应保留本次确认接收的处理人与时间：%+v", ea)
	}
	if len(ea.Rounds) != 3 || ea.Rounds[2].Supplement != sc.successSupplement {
		t.Fatalf("三轮退回/补充经过应保留，且第三轮是成功这次的补充：%+v", ea.Rounds)
	}
	it, _ := f.svc.GetItem(sc.idA)
	if it.CurrentShiftID != sc.b.ID || len(it.ShiftIDs) != 2 ||
		it.ShiftIDs[0] != sc.a.ID || it.ShiftIDs[1] != sc.b.ID {
		t.Fatalf("确认接收后事项才进入接班班次并保留流转历史：%+v", it)
	}
	j, err := f.svc.ItemJourney(sc.idA)
	if err != nil {
		t.Fatalf("journey after confirm: %v", err)
	}
	wantKinds := "created,handover-init,return,resubmit,return,resubmit,return,resubmit,confirm"
	if got := joinStrings(journeyKinds(j)); got != wantKinds {
		t.Fatalf("完整处理经过应只含成功的各次记录，got %s", got)
	}
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(sc.failAt) {
			t.Fatalf("最终处理经过仍不得混入失败尝试时刻：%+v", ev)
		}
		if strings.Contains(ev.Supplement, sc.failSupplement) {
			t.Fatalf("最终处理经过仍不得混入失败尝试说明：%+v", ev)
		}
	}

	// 重开后接收结果、事项归属与完成时间保留。
	f.reopen(t)
	h2, err := f.svc.GetHandover(sc.h.ID)
	if err != nil {
		t.Fatalf("reopen handover: %v", err)
	}
	if !h2.Completed() || h2.CompletedAt == nil || !h2.CompletedAt.Equal(sc.finalConfirmAt) {
		t.Fatalf("重开后交接完成状态与完成时间应保留：%+v", h2)
	}
	it2, _ := f.svc.GetItem(sc.idA)
	if it2.CurrentShiftID != sc.b.ID {
		t.Fatalf("重开后事项应仍在接班班次：%+v", it2)
	}
}

// TestResubmitBusinessRejectionsNotMaskedByBrokenSaving：即使保存路径已被破坏，
// 缺少必填内容、编号不存在、事项状态不允许所造成的拒绝仍按各自业务错误返回，
// 不会被冒充成保存失败，也不改动数据；只有业务条件全部成立时才进入保存阶段并
// 明确返回保存错误。业务拒绝不能代替真正的保存失败场景。
func TestResubmitBusinessRejectionsNotMaskedByBrokenSaving(t *testing.T) {
	sc := setupResubmitFailFixture(t)
	f := sc.f

	breakSaving(t, f)
	defer restoreSaving(t, f)

	// 缺少操作人、补充说明为空（纯空格）：输入校验在保存之前，仍为输入错误。
	if _, err := f.svc.ResubmitReturned(sc.h.ID, sc.idA, "   ", sc.failSupplement); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺少操作人应报 ErrInvalidInput，got %v", err)
	}
	if _, err := f.svc.ResubmitReturned(sc.h.ID, sc.idA, "张三", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("纯空格补充说明应报 ErrInvalidInput，got %v", err)
	}
	// 交接或事项编号不存在：仍是 not found，而不是写入失败。
	if _, err := f.svc.ResubmitReturned("H999", sc.idA, "张三", sc.failSupplement); !errors.Is(err, ErrNotFound) {
		t.Fatalf("交接编号不存在应报 ErrNotFound，got %v", err)
	}
	if _, err := f.svc.ResubmitReturned(sc.h.ID, "I999", "张三", sc.failSupplement); !errors.Is(err, ErrNotFound) {
		t.Fatalf("事项编号不存在应报 ErrNotFound，got %v", err)
	}
	// 对已确认接收的另一项重新提交：状态不允许，而不是写入失败。
	if _, err := f.svc.ResubmitReturned(sc.h.ID, sc.idB, "张三", sc.failSupplement); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("已接收项重新提交应报 ErrHandoverState，got %v", err)
	}

	// 业务条件全部成立时，才真正进入保存过程并返回保存错误。
	f.svc.nowAt(func() time.Time { return sc.failAt })
	_, err := f.svc.ResubmitReturned(sc.h.ID, sc.idA, "张三", sc.failSupplement)
	if err == nil || !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("业务条件成立时应明确返回保存错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrHandoverState) {
		t.Fatalf("保存失败不应归类为业务校验错误，got %v", err)
	}

	// 业务拒绝与这次真保存失败之后，状态仍是第三次退回、等待补充。
	assertStillAwaitingSupplement(t, sc)
}
