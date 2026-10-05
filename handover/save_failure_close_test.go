package handover

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// failedCloseExpectations 汇总结束失败尝试发生前已保存的业务事实，供 reopen
// 前后用同一组期望核对，以及成功结束后对照。
type failedCloseExpectations struct {
	aID, bID, hID           string
	idOpen, idNew, idClosed string
	aClosedAt               time.Time // 上一班成功结束的时间（其结束时记录不受影响）
	idClosedAt              time.Time // 上一班已关闭事项的关闭时间
	handoverCreatedAt       time.Time
	trackAt                 time.Time // 唯一交接事项成功接收（继续跟踪）的时间
	openUpdatedAt           time.Time // 接班后第一次修改未关闭事项的时间
	idNewClosedAt           time.Time // 本班新增事项的关闭时间
	failAt                  time.Time // 失败的结束尝试时刻（不应留下任何痕迹）
	reUpdateAt              time.Time // 保存恢复后再次修改未关闭事项的时间
	successAt               time.Time // 真正成功结束本班的时间
	origContent             string
	origConstraints         string
	trackNote               string
	trackFollow             string // 接收当时指定的后续负责人（交接记录保存）
	midContent              string
	midConstraints          string
	midFollow               string
	midSeverity             Severity
	finalContent            string
	finalConstraints        string
	finalFollow             string
	finalSeverity           Severity
}

// assertStillOpenAfterFailedClose 用当前打开的数据核对：保存失败后本班仍在
// 进行中，没有结束时间，也没有结束时事项记录；失败前已保存的事项、交接与
// 上一班结束时记录全部原样保留，本次失败不成为任何业务事实。
func assertStillOpenAfterFailedClose(t *testing.T, f *fixture, w failedCloseExpectations) {
	t.Helper()

	// 失败尝试不新增班次或交接，也不复制事项。
	if hs := f.svc.ListShifts(); len(hs) != 2 {
		t.Fatalf("失败尝试不应新增班次，got %d 个：%+v", len(hs), hs)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != w.hID {
		t.Fatalf("失败尝试不应新增交接：%+v", hs)
	}
	for _, id := range []string{w.idOpen, w.idNew, w.idClosed} {
		if n := countItems(f, id); n != 1 {
			t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", id, n)
		}
	}

	// 本班仍在进行中：没有结束时间，也没有结束时记录。
	b, err := f.svc.GetShift(w.bID)
	if err != nil {
		t.Fatalf("get shift: %v", err)
	}
	if b.Closed || b.ClosedAt != nil || b.CloseRecord != nil {
		t.Fatalf("保存失败后班次应仍在进行中、无结束时间与结束时记录：%+v", b)
	}

	// 查看本班：事项区仍展示当前信息，不被标成结束时记录或历史不完整；
	// 本班只含两项（本班新增已关闭项、上一班交来仍未关闭项），各一次并按
	// 编号排列；上一班已关闭的事项不属于本班，不能被拉进本班清单。
	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report b: %v", err)
	}
	if repB.Shift.Closed || repB.ItemsAtClose || repB.HistoryIncomplete {
		t.Fatalf("进行中班次不应出现结束时记录或历史不完整标记：%+v", repB.Shift)
	}
	if len(repB.CloseItems) != 0 {
		t.Fatalf("失败尝试不应生成结束时事项记录：%+v", repB.CloseItems)
	}
	if len(repB.Items) != 2 {
		t.Fatalf("本班当前事项应为2项，got %d：%+v", len(repB.Items), repB.Items)
	}
	if repB.Items[0].ID != w.idOpen || repB.Items[1].ID != w.idNew {
		t.Fatalf("事项应按编号排列且各出现一次：%s %s", repB.Items[0].ID, repB.Items[1].ID)
	}
	cur := map[string]Item{}
	for _, it := range repB.Items {
		cur[it.ID] = it
	}
	// 接收到的未关闭事项：保留接班后修改过的内容、严重程度、限制条件与负责人，
	// 仍在本班、仍未关闭，不能退回上一班，也不能多记一条关闭。
	op := cur[w.idOpen]
	if op.Content != w.midContent || op.Severity != w.midSeverity ||
		op.Constraints != w.midConstraints || op.FollowOwner != w.midFollow {
		t.Fatalf("未关闭事项应保持接班后修改的当前信息：%+v", op)
	}
	if op.Closed || op.ClosedAt != nil || op.CloseOperator != "" {
		t.Fatalf("未关闭事项不能因失败尝试变成已关闭：%+v", op)
	}
	if op.OriginShiftID != w.aID || op.CurrentShiftID != w.bID ||
		len(op.ShiftIDs) != 2 || op.ShiftIDs[0] != w.aID || op.ShiftIDs[1] != w.bID {
		t.Fatalf("接收到的事项不能退回上一班，编号与流经班次应保持：%+v", op)
	}
	// 本班新增且已关闭的事项：关闭人与关闭时间保持原样，不能被重新打开。
	nw := cur[w.idNew]
	if !nw.Closed || nw.CloseOperator != "李四" ||
		nw.ClosedAt == nil || !nw.ClosedAt.Equal(w.idNewClosedAt) {
		t.Fatalf("本班已关闭事项的关闭人与关闭时间应保持原样：%+v", nw)
	}
	if nw.OriginShiftID != w.bID || nw.CurrentShiftID != w.bID ||
		len(nw.ShiftIDs) != 1 || nw.ShiftIDs[0] != w.bID {
		t.Fatalf("本班新增事项所在班次应保持：%+v", nw)
	}
	// 上一班已关闭事项仍在上一班、保持关闭。
	old, err := f.svc.GetItem(w.idClosed)
	if err != nil {
		t.Fatalf("get closed item: %v", err)
	}
	if !old.Closed || old.CloseOperator != "张三" ||
		old.ClosedAt == nil || !old.ClosedAt.Equal(w.idClosedAt) {
		t.Fatalf("上一班已关闭事项不能被重新打开：%+v", old)
	}
	if old.OriginShiftID != w.aID || old.CurrentShiftID != w.aID ||
		len(old.ShiftIDs) != 1 || old.ShiftIDs[0] != w.aID {
		t.Fatalf("上一班已关闭事项所在班次应保持：%+v", old)
	}

	// 交接：接收结果、处理人、处理时间与完成时间保留；交接记录里继续跟踪
	// 当时的跟踪说明与后续负责人是接收时刻保存的值，不被接班后的修改改写。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.trackAt) {
		t.Fatalf("既有交接的接收结果与完成时间应保留：%+v", h)
	}
	if !h.CreatedAt.Equal(w.handoverCreatedAt) || h.FromShiftID != w.aID || h.ToShiftID != w.bID {
		t.Fatalf("交接编号、两班关系与发起时间应保留：%+v", h)
	}
	if len(h.Entries) != 1 {
		t.Fatalf("交接清单不应变化，got %d 项", len(h.Entries))
	}
	e := findEntryOf(t, h, w.idOpen)
	if e.Status != EntryTracking || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.trackAt) ||
		e.TrackingNote != w.trackNote || e.FollowOwner != w.trackFollow {
		t.Fatalf("接收方式、处理人、处理时间、跟踪说明与当时负责人应保留：%+v", e)
	}
	if len(e.Rounds) != 0 {
		t.Fatalf("失败尝试不应产生退回/补充经过：%+v", e.Rounds)
	}

	// 上一班保持成功结束，其结束时记录仍是上一班自己结束时的内容：
	// 未关闭事项保留原内容、原负责人且当时未关闭；已关闭事项保留关闭人与时间。
	sa, err := f.svc.GetShift(w.aID)
	if err != nil {
		t.Fatalf("get shift a: %v", err)
	}
	if !sa.Closed || sa.ClosedAt == nil || !sa.ClosedAt.Equal(w.aClosedAt) || sa.CloseRecord == nil {
		t.Fatalf("上一班的结束状态、结束时间与结束时记录应保留：%+v", sa)
	}
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	if !repA.ItemsAtClose || len(repA.CloseItems) != 2 {
		t.Fatalf("上一班结束时记录应保留2项，got %+v", repA.CloseItems)
	}
	snapOpen := findCloseItem(repA, w.idOpen)
	if snapOpen == nil || snapOpen.Content != w.origContent ||
		snapOpen.Severity != SeverityNormal || snapOpen.Constraints != w.origConstraints ||
		snapOpen.FollowOwner != "李四" || snapOpen.Closed {
		t.Fatalf("上一班记录中的未关闭事项应保持结束时原样：%+v", snapOpen)
	}
	snapClosed := findCloseItem(repA, w.idClosed)
	if snapClosed == nil || !snapClosed.Closed || snapClosed.CloseOperator != "张三" ||
		snapClosed.ClosedAt == nil || !snapClosed.ClosedAt.Equal(w.idClosedAt) {
		t.Fatalf("上一班记录中的已关闭事项应保持关闭人与关闭时间：%+v", snapClosed)
	}

	// 展示层与保存事实一致：本班事项区是当前信息而非结束时记录，不出现失败
	// 尝试的结束时间；上一班仍展示自己的结束时记录。
	textB := FormatReport(repB)
	if strings.Contains(textB, "结束时记录") || strings.Contains(textB, fmtTime(w.failAt)) {
		t.Fatalf("本班报告不应出现结束时记录或失败尝试时刻：\n%s", textB)
	}
	if !strings.Contains(textB, "\n事项：\n") || !strings.Contains(textB, w.midContent) ||
		!strings.Contains(textB, "后续负责人："+w.midFollow) {
		t.Fatalf("本班报告应展示未关闭事项的当前信息：\n%s", textB)
	}
	if !strings.Contains(textB, fmt.Sprintf("已关闭（李四 于 %s）", fmtTime(w.idNewClosedAt))) {
		t.Fatalf("本班报告应保留已关闭事项的关闭人与关闭时间：\n%s", textB)
	}
	if !strings.Contains(FormatShift(b), "[进行中]") {
		t.Fatalf("班次摘要应显示进行中：%s", FormatShift(b))
	}
	if !strings.Contains(textB, "已完成 "+fmtTime(w.trackAt)) ||
		!strings.Contains(textB, w.trackFollow) {
		t.Fatalf("本班报告中的接班交接应保留完成时间与接收当时的负责人：\n%s", textB)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, "结束时记录") || !strings.Contains(textA, w.origContent) ||
		!strings.Contains(textA, "结束时后续负责人：李四") {
		t.Fatalf("上一班报告应仍展示自己的原始结束时记录：\n%s", textA)
	}

	// 事项处理经过只含失败前已保存的事实，不混入失败尝试时刻；接收到的事项
	// 保留接收经过，两个已关闭事项没有新的关闭/重开经过。
	jOpen, err := f.svc.ItemJourney(w.idOpen)
	if err != nil {
		t.Fatalf("journey open: %v", err)
	}
	if got := joinStrings(journeyKinds(jOpen)); got != "created,handover-init,track,updated" {
		t.Fatalf("未关闭事项处理经过应只含失败前事实，got %s", got)
	}
	updates := 0
	for _, ev := range jOpen.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在处理经过：%+v", w.failAt, ev)
		}
		if ev.Kind == "updated" {
			updates++
			if !ev.TimeKnown || !ev.At.Equal(w.openUpdatedAt) {
				t.Fatalf("唯一的修改记录应为失败前已保存的那次：%+v", ev)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("失败前应只有一次成功修改记录，got %d", updates)
	}
	if len(jOpen.Results) != 1 || jOpen.Results[0].Entry.Status != EntryTracking ||
		jOpen.Results[0].Entry.FollowOwner != w.trackFollow {
		t.Fatalf("交接当前结果应保留接收时的继续跟踪记录：%+v", jOpen.Results)
	}
	for _, id := range []string{w.idNew, w.idClosed} {
		j, err := f.svc.ItemJourney(id)
		if err != nil {
			t.Fatalf("journey %s: %v", id, err)
		}
		if got := joinStrings(journeyKinds(j)); got != "created,closed" {
			t.Fatalf("已关闭事项 %s 的处理经过应保持创建与关闭，got %s", id, got)
		}
		for _, ev := range j.Events {
			if ev.TimeKnown && ev.At.Equal(w.failAt) {
				t.Fatalf("失败尝试时刻不应出现在事项 %s 的处理经过：%+v", id, ev)
			}
		}
	}
}

// assertClosedWithSuccessRecord 用当前打开的数据核对：保存恢复后本班成功
// 结束，结束时间与结束时记录都只属于这次成功操作；失败尝试无痕迹。
func assertClosedWithSuccessRecord(t *testing.T, f *fixture, w failedCloseExpectations) {
	t.Helper()

	b, err := f.svc.GetShift(w.bID)
	if err != nil {
		t.Fatalf("get shift after success: %v", err)
	}
	if !b.Closed || b.ClosedAt == nil || !b.ClosedAt.Equal(w.successAt) || b.CloseRecord == nil {
		t.Fatalf("本班应以成功操作时刻结束并留下结束时记录：%+v", b)
	}

	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report b after success: %v", err)
	}
	if !repB.ItemsAtClose || repB.HistoryIncomplete {
		t.Fatalf("成功结束后事项区应标为结束时记录：%+v", repB)
	}
	// 结束时记录含本班新增且已关闭的事项与已经接收但未关闭的事项，每项一次。
	if len(repB.CloseItems) != 2 {
		t.Fatalf("结束时记录应含2项，got %d", len(repB.CloseItems))
	}
	if repB.CloseItems[0].ItemID != w.idOpen || repB.CloseItems[1].ItemID != w.idNew {
		t.Fatalf("结束时记录应按编号排列、每项一次：%s %s",
			repB.CloseItems[0].ItemID, repB.CloseItems[1].ItemID)
	}
	// 未关闭事项按成功时的信息冻结，保留再次修改后的内容、严重程度、限制条件
	// 与负责人，结束时仍未关闭。
	so := findCloseItem(repB, w.idOpen)
	if so == nil || so.Content != w.finalContent || so.Severity != w.finalSeverity ||
		so.Constraints != w.finalConstraints || so.FollowOwner != w.finalFollow || so.Closed ||
		so.CloseOperator != "" || so.ClosedAt != nil {
		t.Fatalf("未关闭事项应以成功时的信息冻结且仍为未关闭：%+v", so)
	}
	// 已关闭事项保留原关闭人与关闭时间，不被重新打开。
	sn := findCloseItem(repB, w.idNew)
	if sn == nil || !sn.Closed || sn.CloseOperator != "李四" ||
		sn.ClosedAt == nil || !sn.ClosedAt.Equal(w.idNewClosedAt) {
		t.Fatalf("已关闭事项应保留原关闭人与关闭时间：%+v", sn)
	}
	// 上一班已关闭事项不属于本班结束时记录。
	if findCloseItem(repB, w.idClosed) != nil {
		t.Fatalf("上一班已关闭事项不应出现在本班结束时记录中")
	}
	latest := repB.LatestItems[w.idOpen]
	if latest.CurrentShiftID != w.bID || latest.FollowOwner != w.finalFollow || latest.Closed {
		t.Fatalf("最新状态对照应反映成功时的未关闭事项：%+v", latest)
	}

	textB := FormatReport(repB)
	if !strings.Contains(textB, "以下为结束时记录") ||
		!strings.Contains(textB, w.finalContent) ||
		!strings.Contains(textB, "结束时后续负责人："+w.finalFollow) {
		t.Fatalf("本班报告应按成功时信息展示结束时记录：\n%s", textB)
	}
	if !strings.Contains(textB, fmt.Sprintf("结束时已关闭（李四 于 %s）", fmtTime(w.idNewClosedAt))) {
		t.Fatalf("本班结束时记录应保留已关闭项的关闭人与关闭时间：\n%s", textB)
	}
	if strings.Contains(textB, fmtTime(w.failAt)) || strings.Contains(textB, w.midContent) {
		t.Fatalf("失败尝试时刻与失败前的中间值不应进入成功后的结束时记录：\n%s", textB)
	}

	// 上一班仍展示它自己的原始结束时记录，不随后班修改或成功结束改变。
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report a after success: %v", err)
	}
	sa := findCloseItem(repA, w.idOpen)
	if sa == nil || sa.Content != w.origContent || sa.FollowOwner != "李四" || sa.Closed {
		t.Fatalf("上一班应仍展示自己的原始记录：%+v", sa)
	}
	sc := findCloseItem(repA, w.idClosed)
	if sc == nil || !sc.Closed || sc.CloseOperator != "张三" {
		t.Fatalf("上一班已关闭事项记录应保留：%+v", sc)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, "以下为结束时记录") || !strings.Contains(textA, w.origContent) {
		t.Fatalf("上一班报告应仍展示自己的原始结束时记录：\n%s", textA)
	}

	// 既有交接仍是原来的接收结果；交接记录中继续跟踪当时的负责人不被后来的
	// 修改覆盖。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.trackAt) {
		t.Fatalf("交接完成时间应仍是最后一项接收的时刻：%+v", h)
	}
	e := findEntryOf(t, h, w.idOpen)
	if e.Status != EntryTracking || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.trackAt) ||
		e.TrackingNote != w.trackNote || e.FollowOwner != w.trackFollow {
		t.Fatalf("交接接收记录应保留接收当时保存的信息：%+v", e)
	}

	// 未关闭事项本身：仍未关闭、保留编号与流经班次，处理经过只留下成功的
	// 业务事实（一次接收、两次修改），没有失败尝试时刻。
	it, err := f.svc.GetItem(w.idOpen)
	if err != nil {
		t.Fatalf("get item after success: %v", err)
	}
	if it.Closed || it.Content != w.finalContent || it.Severity != w.finalSeverity ||
		it.Constraints != w.finalConstraints || it.FollowOwner != w.finalFollow {
		t.Fatalf("未关闭事项应保持成功时的当前信息：%+v", it)
	}
	if it.OriginShiftID != w.aID || it.CurrentShiftID != w.bID ||
		len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.aID || it.ShiftIDs[1] != w.bID {
		t.Fatalf("事项编号与流经班次应保持：%+v", it)
	}
	// 事项自身历史：只发生过一次接收、两次成功修改；没有失败尝试的任何痕迹。
	kinds := make([]string, 0, len(it.Events))
	received := 0
	for _, ev := range it.Events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == "received" {
			received++
		}
		if !ev.At.IsZero() && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应出现在事项历史：%+v", ev)
		}
	}
	if received != 1 || joinStrings(kinds) != "created,received,updated,updated" {
		t.Fatalf("事项历史应只留下一次接收与两次成功修改，got %v", kinds)
	}
	j, err := f.svc.ItemJourney(w.idOpen)
	if err != nil {
		t.Fatalf("journey after success: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,track,updated,updated" {
		t.Fatalf("处理经过应呈现一次接收与两次成功修改，got %s", got)
	}
	updatedAt := []time.Time{}
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应出现在成功后的处理经过：%+v", ev)
		}
		if ev.Kind == "updated" {
			updatedAt = append(updatedAt, ev.At)
		}
	}
	if len(updatedAt) != 2 ||
		!updatedAt[0].Equal(w.openUpdatedAt) || !updatedAt[1].Equal(w.reUpdateAt) {
		t.Fatalf("两次修改应分别为接班后与保存恢复后的成功修改：%v", updatedAt)
	}

	// 已结束班次不能重复结束。
	if _, err := f.svc.CloseShift(w.bID); !errors.Is(err, ErrShiftClosed) {
		t.Fatalf("成功结束后重复结束应报错，got %v", err)
	}
}

// TestCloseShiftSaveFailureAtomicRollback：编号正确、班次仍在进行中、接班
// 交接已全部明确接收、本班又存在已关闭与未关闭事项时结束班次，业务条件全部
// 成立、真正进入保存过程后本地写盘失败——结束操作必须明确返回保存错误，
// 不能当成结束成功；内存与磁盘都回到失败前：班次仍进行中、无结束时间、无
// 本次生成的结束时事项记录，事项区仍展示当前信息；未关闭事项保持接班后
// 修改的内容/严重程度/限制条件/负责人且不退回上一班，已关闭事项不被重新
// 打开；既有交接的接收结果、处理人、处理时间与完成时间保留，上一班的
// 结束时记录不受影响，原数据文件内容一个字节不变。保存恢复后仍能在同一
// 会话中修改本班未关闭事项并正常结束：结束时间属于这次成功操作，结束时
// 记录按成功时信息保存（含本班新增已关闭项与已接收未关闭项，每项一次；
// 已关闭项保留原关闭人与关闭时间），班次报告此时才标为结束时记录，上一班
// 仍展示自己的原始记录。
func TestCloseShiftSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 上一班：一项未关闭（随后交给本班），一项结束前已关闭。
	idOpen, err := f.svc.AddItem(a.ID, "上一班遗留的压力异常", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add open item: %v", err)
	}
	idClosed, err := f.svc.AddItem(a.ID, "上一班已关闭的巡检事项", SeverityImportant, "需复查记录", "李四")
	if err != nil {
		t.Fatalf("add closed item: %v", err)
	}
	itemClosedAt := tsDay(2, 14, 0)
	f.svc.nowAt(func() time.Time { return itemClosedAt })
	if _, err := f.svc.CloseItem(idClosed.ID, "张三"); err != nil {
		t.Fatalf("close item in prior shift: %v", err)
	}
	aClosedAt := tsDay(2, 15, 0)
	f.svc.nowAt(func() time.Time { return aClosedAt })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close prior shift: %v", err)
	}

	// 交接完成：唯一事项由接班人继续跟踪，明确接收并指定跟踪说明与新负责人。
	handoverCreatedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return handoverCreatedAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	trackAt := tsDay(2, 17, 30)
	f.svc.nowAt(func() time.Time { return trackAt })
	if _, err := f.svc.ProcessEntry(h.ID, idOpen.ID, ActionTrack, "李四", "",
		"持续跟踪压力变化", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// 接班后修改过未关闭事项的内容、严重程度、限制条件和后续负责人，因此
	// 两班保存的信息有所不同。
	openUpdatedAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return openUpdatedAt })
	if _, err := f.svc.UpdateItem(idOpen.ID, "接班后修订的压力异常", SeverityUrgent,
		"接班后新增限制", "赵六"); err != nil {
		t.Fatalf("update received item: %v", err)
	}

	// 本班新增一项，并在结束尝试前关闭，关闭人与关闭时间已保存。
	f.svc.nowAt(func() time.Time { return tsDay(2, 18, 30) })
	idNew, err := f.svc.AddItem(b.ID, "本班新增的记录归档", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add item in current shift: %v", err)
	}
	idNewClosedAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return idNewClosedAt })
	if _, err := f.svc.CloseItem(idNew.ID, "李四"); err != nil {
		t.Fatalf("close new item: %v", err)
	}

	// 前置：交接已全部明确接收，编号正确、班次进行中——结束的业务条件成立。
	got, _ := f.svc.GetHandover(h.ID)
	if !got.Completed() {
		t.Fatalf("前置：交接应已全部明确接收")
	}
	if pre, _ := f.svc.GetShift(b.ID); pre.Closed {
		t.Fatalf("前置：本班应仍在进行中")
	}

	want := failedCloseExpectations{
		aID: a.ID, bID: b.ID, hID: h.ID,
		idOpen: idOpen.ID, idNew: idNew.ID, idClosed: idClosed.ID,
		aClosedAt:         aClosedAt,
		idClosedAt:        itemClosedAt,
		handoverCreatedAt: handoverCreatedAt,
		trackAt:           trackAt,
		openUpdatedAt:     openUpdatedAt,
		idNewClosedAt:     idNewClosedAt,
		failAt:            tsDay(2, 20, 0),
		reUpdateAt:        tsDay(2, 21, 0),
		successAt:         tsDay(2, 22, 0),
		origContent:       "上一班遗留的压力异常",
		origConstraints:   "",
		trackNote:         "持续跟踪压力变化",
		trackFollow:       "王五",
		midContent:        "接班后修订的压力异常",
		midConstraints:    "接班后新增限制",
		midFollow:         "赵六",
		midSeverity:       SeverityUrgent,
		finalContent:      "再次修订的压力异常",
		finalConstraints:  "最终限制条件",
		finalFollow:       "钱七",
		finalSeverity:     SeverityImportant,
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的结束时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	_, err = f.svc.CloseShift(b.ID)
	if err == nil {
		t.Fatalf("保存失败时结束班次应明确返回错误，不能当成结束成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	// 业务条件全部成立，不能用编号不存在、班次已结束或接班交接未完成的拒绝
	// 来冒充这个保存失败场景。
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrHandoverState) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrShiftClosed) {
		t.Fatalf("编号正确、进行中且交接已接收时的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据上查看班次、事项、交接与报告，应看到失败前的事实，而不是
	// 只保证文件没变、查询却显示已经结束。
	assertStillOpenAfterFailedClose(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的结束尝试不能成为其中的业务事实")
	}

	// 退出后重新打开：班次进行中、事项与交接事实与失败前一致，仍无结束时记录。
	f.reopen(t)
	assertStillOpenAfterFailedClose(t, f, want)

	// 保存条件恢复后，用户仍能在同一应用中修改本班未关闭事项，再正常结束本班。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return want.reUpdateAt })
	if _, err := f.svc.UpdateItem(idOpen.ID, want.finalContent, want.finalSeverity,
		want.finalConstraints, want.finalFollow); err != nil {
		t.Fatalf("恢复后应仍能修改本班未关闭事项：%v", err)
	}
	retry, _ := f.svc.GetItem(idOpen.ID)
	if retry.Content != want.finalContent || retry.Severity != want.finalSeverity ||
		retry.Constraints != want.finalConstraints || retry.FollowOwner != want.finalFollow ||
		retry.Closed || retry.CurrentShiftID != b.ID {
		t.Fatalf("恢复后修改应已生效且事项仍未关闭、仍在本班：%+v", retry)
	}
	f.svc.nowAt(func() time.Time { return want.successAt })
	if _, err := f.svc.CloseShift(b.ID); err != nil {
		t.Fatalf("保存恢复后结束班次应成功：%v", err)
	}

	// 成功后的结束时间属于这次成功操作，结束时记录按成功时的事项信息保存。
	assertClosedWithSuccessRecord(t, f, want)

	// 成功落盘的文件只包含成功事实，不包含失败尝试时刻。
	rawSuccess, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after success: %v", err)
	}
	if !strings.Contains(string(rawSuccess), want.successAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("成功结束时间应已写入数据文件")
	}
	if strings.Contains(string(rawSuccess), want.failAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("失败的结束尝试时刻不应留在数据文件中")
	}

	// 退出重开后，成功结束的事实与上一班原始记录都完整保留，失败尝试仍无痕迹。
	f.reopen(t)
	assertClosedWithSuccessRecord(t, f, want)
}
