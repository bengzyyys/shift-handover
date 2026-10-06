package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“事项关闭（item-close）在本地数据保存阶段失败”建立回归保障：
// 上一班已结束并留下结束时记录，事项经已完成的交接由接班人确认接收到仍在
// 进行中的接班班次（接班后还修改过一次）。接班人填写操作人关闭该事项时，
// 事项编号存在、事项仍未关闭、当前班次仍在进行中，业务条件全部成立，却在
// 真正写盘时失败。只有本地数据保存成功才算关闭完成：必须明确返回保存错误，
// 不能把尚未保存的关闭当作结果返回，系统里不留下关闭人、关闭时间或关闭经过，
// 失败前已保存的数据（事项内容、接收事实、交接完成状态、上一班结束时记录、
// 同班次其他事项）原样保留。保存恢复后再次关闭按现有功能成功，只追加这一次
// 关闭；之后重复关闭或操作人为空白都明确拒绝且不覆盖原关闭事实。

// failedItemCloseExpectations 汇总关闭失败尝试前后必须保持的稳定事实，
// 供 reopen 前后用同一组期望核对。
type failedItemCloseExpectations struct {
	aID, bID, hID                 string
	idTarget, idOther, idPriorClz string
	priorShiftClosedAt            time.Time // 上一班成功结束的时间（结束时记录不受影响）
	priorItemClosedAt             time.Time // 上一班已关闭事项的关闭时间
	handoverCreatedAt             time.Time
	confirmTargetAt               time.Time // 目标事项确认接收时间
	confirmOtherAt                time.Time // 另一事项确认接收时间（即交接完成时间）
	updatedAt                     time.Time // 接班后修改目标事项的时间
	failAt                        time.Time // 失败的关闭尝试时刻（不应留下任何痕迹）
	successAt                     time.Time // 恢复保存后成功关闭的时间
	origContent                   string
	origConstraints               string
	midContent                    string
	midConstraints                string
	midFollow                     string
	midSeverity                   Severity
	otherContent                  string
	priorClosedContent            string
}

// prepareReceivedItemToClose 构建场景：S001（张三，已结束）留有三项——目标
// 事项 I001 与另一事项 I002 结束时未关闭，I003 结束前已由张三关闭；S001
// 结束后经 H001 把两个未关闭事项交给仍在进行中的 S002（李四），两项均确认
// 接收，交接完成。接班后李四又修改过目标事项的内容、严重程度、限制条件与
// 后续负责人。返回固定的场景事实。
func prepareReceivedItemToClose(t *testing.T, f *fixture) failedItemCloseExpectations {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 上一班三项：两个未关闭（随后交接），一个结束前已关闭。
	target, err := f.svc.AddItem(a.ID, "二号泵异响待复核", SeverityNormal, "原限制：需停电窗口", "李四")
	if err != nil {
		t.Fatalf("add target item: %v", err)
	}
	other, err := f.svc.AddItem(a.ID, "交接的另一项未关闭事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add other item: %v", err)
	}
	priorClosed, err := f.svc.AddItem(a.ID, "上一班已关闭的巡检事项", SeverityImportant, "需复查记录", "李四")
	if err != nil {
		t.Fatalf("add prior closed item: %v", err)
	}
	priorItemClosedAt := tsDay(2, 14, 0)
	f.svc.nowAt(func() time.Time { return priorItemClosedAt })
	if _, err := f.svc.CloseItem(priorClosed.ID, "张三"); err != nil {
		t.Fatalf("close prior item in prior shift: %v", err)
	}
	priorShiftClosedAt := tsDay(2, 15, 0)
	f.svc.nowAt(func() time.Time { return priorShiftClosedAt })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close prior shift: %v", err)
	}

	handoverCreatedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return handoverCreatedAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	confirmTargetAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return confirmTargetAt })
	if _, err := f.svc.ProcessEntry(h.ID, target.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm target: %v", err)
	}
	confirmOtherAt := tsDay(2, 17, 15)
	f.svc.nowAt(func() time.Time { return confirmOtherAt })
	if _, err := f.svc.ProcessEntry(h.ID, other.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm other: %v", err)
	}

	// 接班后修改目标事项四项，使接班班次当前信息与上一班结束时记录不同。
	updatedAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return updatedAt })
	if _, err := f.svc.UpdateItem(target.ID, "接班后修订的二号泵异响", SeverityUrgent,
		"接班后新增限制", "赵六"); err != nil {
		t.Fatalf("update target after receive: %v", err)
	}

	return failedItemCloseExpectations{
		aID: a.ID, bID: b.ID, hID: h.ID,
		idTarget: target.ID, idOther: other.ID, idPriorClz: priorClosed.ID,
		priorShiftClosedAt: priorShiftClosedAt,
		priorItemClosedAt:  priorItemClosedAt,
		handoverCreatedAt:  handoverCreatedAt,
		confirmTargetAt:    confirmTargetAt,
		confirmOtherAt:     confirmOtherAt,
		updatedAt:          updatedAt,
		failAt:             tsDay(2, 20, 0),
		successAt:          tsDay(2, 21, 0),
		origContent:        "二号泵异响待复核",
		origConstraints:    "原限制：需停电窗口",
		midContent:         "接班后修订的二号泵异响",
		midConstraints:     "接班后新增限制",
		midFollow:          "赵六",
		midSeverity:        SeverityUrgent,
		otherContent:       "交接的另一项未关闭事项",
		priorClosedContent: "上一班已关闭的巡检事项",
	}
}

// assertItemCloseRolledBack 用当前打开的数据核对：失败的关闭没有留下任何痕迹，
// 失败前已保存的全部事实原样保留，当前班次报告仍把目标事项显示为未关闭。
func assertItemCloseRolledBack(t *testing.T, f *fixture, w failedItemCloseExpectations) {
	t.Helper()

	// 失败尝试不新增班次、交接，也不复制事项。
	if hs := f.svc.ListShifts(); len(hs) != 2 {
		t.Fatalf("失败尝试不应新增班次，got %d 个", len(hs))
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != w.hID {
		t.Fatalf("失败尝试不应新增交接：%+v", hs)
	}
	for _, id := range []string{w.idTarget, w.idOther, w.idPriorClz} {
		if n := countItems(f, id); n != 1 {
			t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", id, n)
		}
	}

	// 目标事项：仍未关闭、无关闭人与关闭时间；内容、严重程度、限制条件、后续
	// 负责人、原始班次与当前班次沿用失败前已保存的信息；仍属于接班班次。
	it, err := f.svc.GetItem(w.idTarget)
	if err != nil {
		t.Fatalf("get target item: %v", err)
	}
	if it.Closed || it.ClosedAt != nil || it.CloseOperator != "" {
		t.Fatalf("保存失败后事项应仍未关闭、无关闭人与关闭时间：%+v", it)
	}
	if it.Content != w.midContent || it.Severity != w.midSeverity ||
		it.Constraints != w.midConstraints || it.FollowOwner != w.midFollow {
		t.Fatalf("事项四项信息应沿用失败前保存的值：%+v", it)
	}
	if it.OriginShiftID != w.aID || it.CurrentShiftID != w.bID ||
		len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.aID || it.ShiftIDs[1] != w.bID {
		t.Fatalf("事项仍应属于接班班次、编号与流经班次保持：%+v", it)
	}
	// 事项自身历史只保留建立、接收与此前的修改，不出现这次失败的关闭经过，
	// 失败时刻也不落在任何一条历史上。
	if got := joinStrings(itemEventKinds(it)); got != "created,received,updated" {
		t.Fatalf("事项历史应仍只有建立、接收与修改，got %s", got)
	}
	for _, ev := range it.Events {
		if ev.Kind == "closed" || (!ev.At.IsZero() && ev.At.Equal(w.failAt)) {
			t.Fatalf("失败的关闭不应进入事项历史：%+v", ev)
		}
	}

	// 处理经过查询与单项查询一致：完整保留建立、交接发起、确认接收与修改，
	// 不出现失败的关闭经过或失败时刻；交接当前结果仍是当时的确认接收。
	j, err := f.svc.ItemJourney(w.idTarget)
	if err != nil {
		t.Fatalf("journey target: %v", err)
	}
	if j.Item.Closed || j.Item.Content != w.midContent || j.Item.FollowOwner != w.midFollow {
		t.Fatalf("处理经过开头的最新状态应与失败前一致：%+v", j.Item)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated" {
		t.Fatalf("处理经过应保留建立、交接、接收与修改且不含失败关闭，got %s", got)
	}
	for _, ev := range j.Events {
		if ev.Kind == "closed" || (ev.TimeKnown && ev.At.Equal(w.failAt)) {
			t.Fatalf("失败的关闭不应出现在处理经过：%+v", ev)
		}
	}
	if len(j.Results) != 1 {
		t.Fatalf("目标事项应只参与一次交接，got %+v", j.Results)
	}
	rv := j.Results[0]
	if rv.HandoverID != w.hID || rv.Entry.Status != EntryConfirmed ||
		rv.Entry.Operator != "李四" || rv.Entry.ProcessedAt == nil ||
		!rv.Entry.ProcessedAt.Equal(w.confirmTargetAt) {
		t.Fatalf("已确认接收的事实不能因关闭失败被撤销：%+v", rv)
	}

	// 交接：原交接仍已完成，完成时间是最后一项接收的时刻；两项的确认结果、
	// 接收人和接收时间保持原样，清单快照仍是发起时的事项原文。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmOtherAt) {
		t.Fatalf("已完成的交接不能因关闭失败重新变成未完成：%+v", h)
	}
	if !h.CreatedAt.Equal(w.handoverCreatedAt) || h.FromShiftID != w.aID || h.ToShiftID != w.bID {
		t.Fatalf("交接编号、两班关系与发起时间应保留：%+v", h)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("交接清单不应变化，got %d 项", len(h.Entries))
	}
	et := findEntryOf(t, h, w.idTarget)
	if et.Status != EntryConfirmed || et.Operator != "李四" ||
		et.ProcessedAt == nil || !et.ProcessedAt.Equal(w.confirmTargetAt) {
		t.Fatalf("目标事项的确认接收结果、接收人与接收时间应保持原样：%+v", et)
	}
	if et.Content != w.origContent || et.Constraints != w.origConstraints || et.FollowOwner != "李四" {
		t.Fatalf("交接清单保存的事项原文不应被改动：%+v", et)
	}
	eo := findEntryOf(t, h, w.idOther)
	if eo.Status != EntryConfirmed || eo.Operator != "李四" ||
		eo.ProcessedAt == nil || !eo.ProcessedAt.Equal(w.confirmOtherAt) {
		t.Fatalf("同一交接中其他事项的接收结果应保持原样：%+v", eo)
	}

	// 接班班次仍在进行中，当前班次报告继续把目标事项（及其另一项）显示为
	// 未关闭，不被标成结束时记录，也不出现失败时刻。
	b, err := f.svc.GetShift(w.bID)
	if err != nil {
		t.Fatalf("get current shift: %v", err)
	}
	if b.Closed || b.ClosedAt != nil || b.CloseRecord != nil {
		t.Fatalf("接班班次应仍在进行中：%+v", b)
	}
	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	if repB.ItemsAtClose || repB.HistoryIncomplete || len(repB.CloseItems) != 0 {
		t.Fatalf("进行中班次不应出现结束时记录：%+v", repB)
	}
	if len(repB.Items) != 2 || repB.Items[0].ID != w.idTarget || repB.Items[1].ID != w.idOther {
		t.Fatalf("接班班次当前事项应仍是接收到的两项：%+v", repB.Items)
	}
	curTarget := repB.Items[0]
	if curTarget.Closed || curTarget.CloseOperator != "" || curTarget.ClosedAt != nil {
		t.Fatalf("当前班次报告应继续把目标事项显示为未关闭：%+v", curTarget)
	}
	if curTarget.Content != w.midContent || curTarget.FollowOwner != w.midFollow {
		t.Fatalf("当前班次报告中的事项信息应与失败前一致：%+v", curTarget)
	}
	if repB.Items[1].Closed {
		t.Fatalf("同一接班班次的其他事项应保持原状（未关闭）：%+v", repB.Items[1])
	}
	textB := FormatReport(repB)
	if strings.Contains(textB, "结束时记录") || strings.Contains(textB, fmtTime(w.failAt)) ||
		strings.Contains(textB, "已关闭（李四 于 "+fmtTime(w.failAt)+"）") {
		t.Fatalf("接班班次报告不应出现结束时记录或失败的关闭：\n%s", textB)
	}
	if !strings.Contains(textB, w.midContent) || !strings.Contains(textB, "[未关闭]") {
		t.Fatalf("接班班次报告应继续展示未关闭目标事项的当前信息：\n%s", textB)
	}
	if !strings.Contains(FormatShift(b), "[进行中]") {
		t.Fatalf("班次摘要应显示进行中：%s", FormatShift(b))
	}

	// 同一接班班次里的其他事项保持原状：仍未关闭、仍在接班班次，处理经过不变。
	oth, err := f.svc.GetItem(w.idOther)
	if err != nil {
		t.Fatalf("get other item: %v", err)
	}
	if oth.Closed || oth.CurrentShiftID != w.bID || oth.FollowOwner != "李四" ||
		joinStrings(itemEventKinds(oth)) != "created,received" {
		t.Fatalf("同一接班班次的其他事项应保持原状：%+v", oth)
	}
	jOther, err := f.svc.ItemJourney(w.idOther)
	if err != nil {
		t.Fatalf("journey other: %v", err)
	}
	if got := joinStrings(journeyKinds(jOther)); got != "created,handover-init,confirm" {
		t.Fatalf("其他事项处理经过应保持原状，got %s", got)
	}

	// 上一班保持成功结束，结束时记录保存的仍是当时尚未关闭的目标事项（原文、
	// 原负责人、当时未关闭），以及当时已关闭事项的关闭人与时间；失败的关闭
	// 既不能改写这份记录，也不能把当时未关闭显示成当时已关闭。
	sa, err := f.svc.GetShift(w.aID)
	if err != nil {
		t.Fatalf("get prior shift: %v", err)
	}
	if !sa.Closed || sa.ClosedAt == nil || !sa.ClosedAt.Equal(w.priorShiftClosedAt) || sa.CloseRecord == nil {
		t.Fatalf("上一班的结束状态、结束时间与结束时记录应保留：%+v", sa)
	}
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report prior shift: %v", err)
	}
	if !repA.ItemsAtClose || len(repA.CloseItems) != 3 {
		t.Fatalf("上一班结束时记录应保留3项，got %+v", repA.CloseItems)
	}
	snapTarget := findCloseItem(repA, w.idTarget)
	if snapTarget == nil || snapTarget.Content != w.origContent ||
		snapTarget.Severity != SeverityNormal || snapTarget.Constraints != w.origConstraints ||
		snapTarget.FollowOwner != "李四" || snapTarget.Closed ||
		snapTarget.CloseOperator != "" || snapTarget.ClosedAt != nil {
		t.Fatalf("上一班记录中的目标事项应仍是当时未关闭的原样：%+v", snapTarget)
	}
	snapOther := findCloseItem(repA, w.idOther)
	if snapOther == nil || snapOther.Closed || snapOther.Content != w.otherContent {
		t.Fatalf("上一班记录中的另一事项应仍是当时未关闭：%+v", snapOther)
	}
	snapPrior := findCloseItem(repA, w.idPriorClz)
	if snapPrior == nil || !snapPrior.Closed || snapPrior.CloseOperator != "张三" ||
		snapPrior.ClosedAt == nil || !snapPrior.ClosedAt.Equal(w.priorItemClosedAt) {
		t.Fatalf("上一班记录中的已关闭事项应保留关闭人与关闭时间：%+v", snapPrior)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, "结束时记录") || !strings.Contains(textA, w.origContent) ||
		strings.Contains(textA, w.midContent) || strings.Contains(textA, fmtTime(w.failAt)) {
		t.Fatalf("上一班报告应仍是自己的原始结束时记录：\n%s", textA)
	}
	if !strings.Contains(textA, "结束时已关闭（张三 于 "+fmtTime(w.priorItemClosedAt)+"）") {
		t.Fatalf("上一班报告应保留当时已关闭事项：\n%s", textA)
	}

	// 处理经过的展示文本与保存事实一致：开头未关闭，时间线没有失败的关闭。
	textJ := FormatItemJourney(j)
	if !strings.Contains(textJ, "[未关闭]") || strings.Contains(textJ, "关闭 操作人=") ||
		strings.Contains(textJ, fmtTime(w.failAt)) {
		t.Fatalf("事项处理经过展示应仍为未关闭且不含失败的关闭：\n%s", textJ)
	}
}

// assertItemClosedAfterSuccess 用当前打开的数据核对：保存恢复后的关闭成功，
// 只追加这一次关闭的操作人与时间；事项编号、四项信息与此前历史全部保留，
// 交接与上一班结束时记录不受影响。
func assertItemClosedAfterSuccess(t *testing.T, f *fixture, w failedItemCloseExpectations) {
	t.Helper()

	it, err := f.svc.GetItem(w.idTarget)
	if err != nil {
		t.Fatalf("get target after success: %v", err)
	}
	if !it.Closed || it.CloseOperator != "李四" || it.ClosedAt == nil ||
		!it.ClosedAt.Equal(w.successAt) {
		t.Fatalf("事项应以成功操作时刻关闭并记录操作人：%+v", it)
	}
	if it.Content != w.midContent || it.Severity != w.midSeverity ||
		it.Constraints != w.midConstraints || it.FollowOwner != w.midFollow {
		t.Fatalf("关闭不应改变事项四项信息：%+v", it)
	}
	if it.OriginShiftID != w.aID || it.CurrentShiftID != w.bID ||
		len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.aID || it.ShiftIDs[1] != w.bID {
		t.Fatalf("事项编号与流经班次应保持：%+v", it)
	}
	// 历史只追加一次成功关闭；接收仍只有一次，失败尝试时刻无痕迹。
	closedCnt, receivedCnt := 0, 0
	for _, ev := range it.Events {
		switch ev.Kind {
		case "closed":
			closedCnt++
			if ev.Operator != "李四" || !ev.At.Equal(w.successAt) {
				t.Fatalf("关闭经过应属于成功操作：%+v", ev)
			}
		case "received":
			receivedCnt++
		}
		if !ev.At.IsZero() && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应出现在事项历史：%+v", ev)
		}
	}
	if closedCnt != 1 || receivedCnt != 1 ||
		joinStrings(itemEventKinds(it)) != "created,received,updated,closed" {
		t.Fatalf("事项历史应只追加一次成功关闭，got %v", itemEventKinds(it))
	}

	j, err := f.svc.ItemJourney(w.idTarget)
	if err != nil {
		t.Fatalf("journey after success: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated,closed" {
		t.Fatalf("处理经过应只追加一次成功关闭，got %s", got)
	}
	closedInJourney := 0
	for _, ev := range j.Events {
		if ev.Kind == "closed" {
			closedInJourney++
			if !ev.TimeKnown || !ev.At.Equal(w.successAt) || ev.Operator != "李四" {
				t.Fatalf("处理经过中的关闭应为成功操作：%+v", ev)
			}
		}
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应出现在处理经过：%+v", ev)
		}
	}
	if closedInJourney != 1 {
		t.Fatalf("处理经过中应只有一次关闭，got %d", closedInJourney)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[0].Entry.Operator != "李四" ||
		j.Results[0].Entry.ProcessedAt == nil ||
		!j.Results[0].Entry.ProcessedAt.Equal(w.confirmTargetAt) {
		t.Fatalf("交接当前结果应仍是当时的确认接收，不因关闭改变：%+v", j.Results)
	}

	// 接班班次仍在进行中，当前事项区把目标事项显示为已关闭（李四 于成功时刻），
	// 另一事项仍未关闭。
	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report current shift after success: %v", err)
	}
	if repB.Shift.Closed || repB.ItemsAtClose {
		t.Fatalf("接班班次应仍在进行中、不生成结束时记录：%+v", repB.Shift)
	}
	if len(repB.Items) != 2 {
		t.Fatalf("接班班次当前事项应仍为2项，got %+v", repB.Items)
	}
	cur := map[string]Item{}
	for _, x := range repB.Items {
		cur[x.ID] = x
	}
	ct := cur[w.idTarget]
	if !ct.Closed || ct.CloseOperator != "李四" || ct.ClosedAt == nil ||
		!ct.ClosedAt.Equal(w.successAt) || ct.Content != w.midContent {
		t.Fatalf("当前班次报告应显示目标事项已成功关闭：%+v", ct)
	}
	if cur[w.idOther].Closed {
		t.Fatalf("另一事项应仍未关闭：%+v", cur[w.idOther])
	}
	textB := FormatReport(repB)
	if !strings.Contains(textB, "已关闭（李四 于 "+fmtTime(w.successAt)+"）") ||
		!strings.Contains(textB, w.midContent) {
		t.Fatalf("接班班次报告应展示成功关闭：\n%s", textB)
	}
	if strings.Contains(textB, fmtTime(w.failAt)) {
		t.Fatalf("失败尝试时刻不应出现在接班班次报告：\n%s", textB)
	}

	// 上一班结束时记录仍是当时未关闭的原样，不随后班关闭改变。
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report prior shift after success: %v", err)
	}
	snap := findCloseItem(repA, w.idTarget)
	if snap == nil || snap.Closed || snap.Content != w.origContent ||
		snap.FollowOwner != "李四" || snap.CloseOperator != "" {
		t.Fatalf("上一班结束时记录应仍显示当时未关闭：%+v", snap)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, w.origContent) || strings.Contains(textA, w.midContent) {
		t.Fatalf("上一班结束时记录不应被后班关闭改写：\n%s", textA)
	}

	// 交接仍保留当时的确认接收结果与完成时间。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmOtherAt) {
		t.Fatalf("交接完成时间应保持接收齐的时刻：%+v", h)
	}
	et := findEntryOf(t, h, w.idTarget)
	if et.Status != EntryConfirmed || et.Operator != "李四" ||
		et.ProcessedAt == nil || !et.ProcessedAt.Equal(w.confirmTargetAt) {
		t.Fatalf("交接中的确认接收应保持原样：%+v", et)
	}
}

// TestCloseItemSaveFailureAtomicRollback：接班人关闭已接收且仍未关闭的事项，
// 事项编号存在、事项未关闭、接班班次仍在进行中、操作人已填写，关闭业务条件
// 全部成立、真正进入保存过程后本地写盘失败——关闭操作必须明确返回保存错误，
// 不能当成关闭成功，也不能把尚未保存的关闭当作结果返回；内存与磁盘都回到
// 失败前：事项仍未关闭、无关闭人与关闭时间，完整处理经过保留此前的建立、
// 修改与接收，不出现这次失败的关闭经过；事项四项信息、原始班次与当前班次
// 沿用失败前的值，仍属于接班班次，当前班次报告继续显示未关闭；已确认接收的
// 事实不被撤销，上一班结束时记录与同班次其他事项不变，数据文件一个字节不变。
// 重新打开同一数据文件后事实一致。保存恢复后再次关闭按现有功能成功，只追加
// 这次成功关闭的操作人与时间；随后重复关闭明确拒绝且不覆盖原关闭人/时间、
// 不多记关闭经过，空白操作人同样被拒绝并保留原状态。
func TestCloseItemSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	w := prepareReceivedItemToClose(t, f)

	// 前置：编号存在、事项未关闭、接班班次进行中、交接已完成。
	pre, _ := f.svc.GetItem(w.idTarget)
	if pre.Closed || pre.CurrentShiftID != w.bID {
		t.Fatalf("前置：目标事项应未关闭且在接班班次：%+v", pre)
	}
	if ps, _ := f.svc.GetShift(w.bID); ps.Closed {
		t.Fatalf("前置：接班班次应仍在进行中")
	}
	if ph, _ := f.svc.GetHandover(w.hID); !ph.Completed() {
		t.Fatalf("前置：交接应已全部确认接收")
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的关闭时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.failAt })

	// 使下一次写盘在原子保存阶段失败，接班人填写操作人关闭目标事项。
	breakSaving(t, f)
	got, err := f.svc.CloseItem(w.idTarget, "李四")
	// 必须明确报错：这是保存错误而不是业务拒绝（不能用编号不存在、事项已关闭、
	// 班次已结束或操作人为空来冒充），也不能把尚未保存的关闭当作结果返回。
	if err == nil {
		t.Fatalf("保存失败时关闭事项应返回错误，不能当成关闭成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrShiftClosed) {
		t.Fatalf("编号正确、未关闭、班次进行中且操作人已填写时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	if !reflect.DeepEqual(got, Item{}) {
		t.Fatalf("保存失败不得把尚未保存的关闭当作结果返回：%+v", got)
	}

	// 当前打开的数据中不留这次关闭的任何一部分。
	assertItemCloseRolledBack(t, f, w)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的关闭不能成为其中的业务事实")
	}

	// 退出后重新打开同一数据文件：事项仍未关闭，原有的接收经过完整保留。
	f.reopen(t)
	assertItemCloseRolledBack(t, f, w)

	// 保存恢复后，接班人对同一事项再次填写操作人关闭，应按现有功能成功。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return w.successAt })
	closed, err := f.svc.CloseItem(w.idTarget, "李四")
	if err != nil {
		t.Fatalf("保存恢复后关闭事项应成功：%v", err)
	}
	if !closed.Closed || closed.CloseOperator != "李四" ||
		closed.ClosedAt == nil || !closed.ClosedAt.Equal(w.successAt) {
		t.Fatalf("成功关闭的返回结果应为本次关闭事实：%+v", closed)
	}
	assertItemClosedAfterSuccess(t, f, w)

	// 成功落盘的文件只包含成功事实，不包含失败尝试时刻。
	rawSuccess, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after success: %v", err)
	}
	if !strings.Contains(string(rawSuccess), w.successAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("成功关闭时间应已写入数据文件")
	}
	if strings.Contains(string(rawSuccess), w.failAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("失败的关闭尝试时刻不应留在数据文件中")
	}

	// 退出重开后，成功关闭的事实与上一班原始记录都完整保留，失败尝试无痕迹。
	f.reopen(t)
	assertItemClosedAfterSuccess(t, f, w)

	// 已成功关闭的事项再次关闭：明确拒绝，不覆盖原关闭人或关闭时间，也不多记
	// 一条关闭经过。
	if _, err := f.svc.CloseItem(w.idTarget, "王五"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重复关闭应报状态错误，got %v", err)
	}
	reGot, err := f.svc.GetItem(w.idTarget)
	if err != nil {
		t.Fatalf("get item after duplicate close: %v", err)
	}
	if !reGot.Closed || reGot.CloseOperator != "李四" ||
		reGot.ClosedAt == nil || !reGot.ClosedAt.Equal(w.successAt) {
		t.Fatalf("重复关闭被拒绝后原关闭人与关闭时间应保持：%+v", reGot)
	}
	if n := countClosedEvents(reGot); n != 1 {
		t.Fatalf("重复关闭不得多记关闭经过，got %d 条", n)
	}

	// 操作人为空或只有空白：即使对象是已关闭事项也先按输入不合法拒绝，原状态
	// 保持不变。
	for _, op := range []string{"", "   ", "\t "} {
		if _, err := f.svc.CloseItem(w.idTarget, op); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("空白操作人 %q 应报输入不合法，got %v", op, err)
		}
	}
	afterBlank, _ := f.svc.GetItem(w.idTarget)
	if afterBlank.CloseOperator != "李四" || afterBlank.ClosedAt == nil ||
		!afterBlank.ClosedAt.Equal(w.successAt) || countClosedEvents(afterBlank) != 1 {
		t.Fatalf("空白操作人被拒绝后已关闭事项应保持原关闭事实：%+v", afterBlank)
	}

	// 对仍未关闭的另一事项填写空白操作人：同样拒绝，事项保持未关闭且不留下
	// 任何关闭经过。
	if _, err := f.svc.CloseItem(w.idOther, "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("对未关闭事项填写空白操作人应报输入不合法，got %v", err)
	}
	other, _ := f.svc.GetItem(w.idOther)
	if other.Closed || other.CloseOperator != "" || other.ClosedAt != nil ||
		countClosedEvents(other) != 0 {
		t.Fatalf("空白操作人被拒绝后未关闭事项应保持原状态：%+v", other)
	}
}

// countClosedEvents 统计事项历史中的关闭经过条数。
func countClosedEvents(it Item) int {
	n := 0
	for _, ev := range it.Events {
		if ev.Kind == "closed" {
			n++
		}
	}
	return n
}
