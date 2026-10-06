package handover

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“事项关闭（item-close）在本地数据保存阶段失败”建立回归保障：
// 事项编号存在、事项仍未关闭、当前所在班次仍在进行中、操作人也已填写，关闭
// 操作通过全部业务条件检查，却在真正保存数据时本地写盘失败。只有本地数据
// 保存成功才算关闭完成：失败时操作明确报保存错误，系统里不留下这次关闭的
// 任何一部分——关闭人和关闭时间不记录、处理经过不增加关闭记录，失败前已
// 保存的事项信息、接收事实、上一班结束时记录与同班次其他事项全部原样保留。

// failedItemCloseExpectations 汇总失败的关闭尝试发生前已保存的业务事实，
// 供 reopen 前后用同一组期望核对，以及成功关闭后对照。
type failedItemCloseExpectations struct {
	aID, bID, hID                string
	idOpen, idOther, idClosed    string
	aClosedAt                    time.Time // 上一班成功结束的时间（其结束时记录不受影响）
	idClosedAt                   time.Time // 上一班已关闭事项的关闭时间
	handoverCreatedAt            time.Time
	confirmAt                    time.Time // 未关闭事项被确认接收的时间
	openUpdatedAt                time.Time // 接班后修改未关闭事项的时间
	otherCreatedAt               time.Time // 接班班次中其他事项的建立时间
	failAt                       time.Time // 失败的关闭尝试时刻（不应留下任何痕迹）
	successAt                    time.Time // 保存恢复后真正成功关闭的时间
	origContent, origConstraints string
	midContent, midConstraints   string
	midFollow                    string
	midSeverity                  Severity
	failedOperator               string // 失败尝试填写的操作人（不能出现在任何记录里）
	successOperator              string // 成功关闭时填写的操作人
}

// assertItemStillOpenAfterFailedClose 用当前打开的数据核对：保存失败后事项
// 仍未关闭，关闭人与关闭时间没有记录，处理经过只保留失败前的建立、接收与
// 修改；接收事实、上一班结束时记录、同班次其他事项全部原样保留。
func assertItemStillOpenAfterFailedClose(t *testing.T, f *fixture, w failedItemCloseExpectations) {
	t.Helper()

	// 失败尝试不新增班次或交接，也不复制事项。
	if hs := f.svc.ListShifts(); len(hs) != 2 {
		t.Fatalf("失败尝试不应新增班次，got %d 个：%+v", len(hs), hs)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != w.hID {
		t.Fatalf("失败尝试不应新增交接：%+v", hs)
	}
	for _, id := range []string{w.idOpen, w.idOther, w.idClosed} {
		if n := countItems(f, id); n != 1 {
			t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", id, n)
		}
	}

	// 接班班次仍在进行中。
	b, err := f.svc.GetShift(w.bID)
	if err != nil {
		t.Fatalf("get current shift: %v", err)
	}
	if b.Closed || b.ClosedAt != nil || b.CloseRecord != nil {
		t.Fatalf("关闭事项失败不应结束接班班次：%+v", b)
	}

	// 事项本身：仍未关闭、没有关闭人和关闭时间；内容、严重程度、限制条件与
	// 后续负责人沿用失败前保存的值；仍属于接班班次，编号与流经班次不变。
	it, err := f.svc.GetItem(w.idOpen)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.Closed || it.ClosedAt != nil || it.CloseOperator != "" {
		t.Fatalf("保存失败后事项应仍未关闭、关闭人与关闭时间未记录：%+v", it)
	}
	if it.Content != w.midContent || it.Severity != w.midSeverity ||
		it.Constraints != w.midConstraints || it.FollowOwner != w.midFollow {
		t.Fatalf("事项内容、严重程度、限制条件与后续负责人应沿用失败前的值：%+v", it)
	}
	if it.OriginShiftID != w.aID || it.CurrentShiftID != w.bID ||
		len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.aID || it.ShiftIDs[1] != w.bID {
		t.Fatalf("事项应仍属于接班班次，原始班次与流经班次保持：%+v", it)
	}
	// 事项自身历史只保留建立、接收与接班后的修改，不出现这次失败的关闭经过、
	// 失败时刻或失败尝试填写的操作人。
	if got := joinStrings(itemEventKinds(it)); got != "created,received,updated" {
		t.Fatalf("事项历史应仍只有建立、接收与修改，got %s", got)
	}
	for _, ev := range it.Events {
		if ev.Kind == "closed" {
			t.Fatalf("失败尝试不应产生关闭经过：%+v", ev)
		}
		if !ev.At.IsZero() && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在事项历史：%+v", w.failAt, ev)
		}
		if ev.Operator == w.failedOperator {
			t.Fatalf("失败尝试填写的操作人不应写入事项历史：%+v", ev)
		}
	}

	// 处理经过查询：保留建立、交接发起、确认接收与修改，不出现关闭记录；
	// 交接当前结果仍是当时的确认接收。
	j, err := f.svc.ItemJourney(w.idOpen)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated" {
		t.Fatalf("处理经过应只保留失败前的建立、接收与修改，got %s", got)
	}
	for _, ev := range j.Events {
		if ev.Kind == "closed" {
			t.Fatalf("处理经过不应出现失败的关闭记录：%+v", ev)
		}
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应出现在处理经过：%+v", ev)
		}
		if ev.Operator == w.failedOperator {
			t.Fatalf("失败尝试的操作人不应出现在处理经过：%+v", ev)
		}
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[0].Entry.Operator != "李四" ||
		j.Results[0].Entry.ProcessedAt == nil || !j.Results[0].Entry.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("交接当前结果应仍为当时的确认接收：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, "[未关闭]") || !strings.Contains(jText, "确认接收") {
		t.Fatalf("事项查询应显示未关闭并保留接收经过：\n%s", jText)
	}
	if strings.Contains(jText, "关闭 操作人") || strings.Contains(jText, w.failedOperator) ||
		strings.Contains(jText, fmtTime(w.failAt)) {
		t.Fatalf("事项查询不应出现失败的关闭信息：\n%s", jText)
	}

	// 已确认接收的事实不能因关闭失败而被撤销：交接仍完成，完成时间、该项的
	// 确认结果、接收人和接收时间保持原样；清单保存的事项原文也不变。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("原交接的完成状态与完成时间应保留：%+v", h)
	}
	e := findEntryOf(t, h, w.idOpen)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("确认结果、接收人和接收时间应保持原样：%+v", e)
	}
	if e.Content != w.origContent || e.Constraints != w.origConstraints || e.FollowOwner != "李四" {
		t.Fatalf("交接清单保存的事项原文不应被改动：%+v", e)
	}

	// 上一班保持成功结束，其结束时记录仍是它自己结束时的内容：当时未关闭的
	// 事项继续显示未关闭、原内容与原负责人；已关闭事项保留原关闭人与时间。
	sa, err := f.svc.GetShift(w.aID)
	if err != nil {
		t.Fatalf("get prior shift: %v", err)
	}
	if !sa.Closed || sa.ClosedAt == nil || !sa.ClosedAt.Equal(w.aClosedAt) || sa.CloseRecord == nil {
		t.Fatalf("上一班的结束状态、结束时间与结束时记录应保留：%+v", sa)
	}
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report prior shift: %v", err)
	}
	if !repA.ItemsAtClose || len(repA.CloseItems) != 2 {
		t.Fatalf("上一班结束时记录应保留2项，got %+v", repA.CloseItems)
	}
	snapOpen := findCloseItem(repA, w.idOpen)
	if snapOpen == nil || snapOpen.Content != w.origContent ||
		snapOpen.Severity != SeverityNormal || snapOpen.Constraints != w.origConstraints ||
		snapOpen.FollowOwner != "李四" || snapOpen.Closed ||
		snapOpen.CloseOperator != "" || snapOpen.ClosedAt != nil {
		t.Fatalf("上一班记录中的未关闭事项应保持结束时原样，不能被失败关闭改写：%+v", snapOpen)
	}
	snapClosed := findCloseItem(repA, w.idClosed)
	if snapClosed == nil || !snapClosed.Closed || snapClosed.CloseOperator != "张三" ||
		snapClosed.ClosedAt == nil || !snapClosed.ClosedAt.Equal(w.idClosedAt) {
		t.Fatalf("上一班记录中的已关闭事项应保持关闭人与关闭时间：%+v", snapClosed)
	}

	// 当前班次报告把该事项继续显示为未关闭，信息为接班后修改的当前值；
	// 同班次其他事项保持原状，上一班已关闭事项不被拉进本班清单。
	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	if repB.Shift.Closed || repB.ItemsAtClose || repB.HistoryIncomplete {
		t.Fatalf("进行中班次不应出现结束时记录或历史不完整标记：%+v", repB.Shift)
	}
	if len(repB.Items) != 2 || repB.Items[0].ID != w.idOpen || repB.Items[1].ID != w.idOther {
		t.Fatalf("接班班次当前事项应为2项且按编号排列，got %+v", repB.Items)
	}
	cur := repB.Items[0]
	if cur.Closed || cur.CloseOperator != "" || cur.ClosedAt != nil {
		t.Fatalf("当前班次报告应继续把该事项显示为未关闭：%+v", cur)
	}
	if cur.Content != w.midContent || cur.Severity != w.midSeverity ||
		cur.Constraints != w.midConstraints || cur.FollowOwner != w.midFollow {
		t.Fatalf("当前班次报告中的事项信息应与失败前一致：%+v", cur)
	}
	other := repB.Items[1]
	if other.Closed || other.Content != "接班班次的其他事项" || other.FollowOwner != "孙九" {
		t.Fatalf("同一接班班次的其他事项应保持原状：%+v", other)
	}
	textB := FormatReport(repB)
	if !strings.Contains(textB, "\n事项：\n") || !strings.Contains(textB, w.midContent) ||
		!strings.Contains(textB, "[未关闭]") {
		t.Fatalf("当前班次报告应展示未关闭事项的当前信息：\n%s", textB)
	}
	if strings.Contains(textB, "结束时记录") || strings.Contains(textB, fmtTime(w.failAt)) ||
		strings.Contains(textB, w.failedOperator) {
		t.Fatalf("当前班次报告不应出现失败关闭的任何痕迹：\n%s", textB)
	}

	// 上一班已关闭事项仍在上一班、保持关闭，失败尝试不重开它。
	old, err := f.svc.GetItem(w.idClosed)
	if err != nil {
		t.Fatalf("get closed item: %v", err)
	}
	if !old.Closed || old.CloseOperator != "张三" ||
		old.ClosedAt == nil || !old.ClosedAt.Equal(w.idClosedAt) ||
		old.CurrentShiftID != w.aID {
		t.Fatalf("上一班已关闭事项应保持关闭且不移动：%+v", old)
	}
	jClosed, err := f.svc.ItemJourney(w.idClosed)
	if err != nil {
		t.Fatalf("journey closed item: %v", err)
	}
	if got := joinStrings(journeyKinds(jClosed)); got != "created,closed" {
		t.Fatalf("上一班已关闭事项的处理经过应保持创建与关闭，got %s", got)
	}

	// 同班次其他事项仍未关闭，只有建立经过。
	ot, err := f.svc.GetItem(w.idOther)
	if err != nil {
		t.Fatalf("get other item: %v", err)
	}
	if ot.Closed || ot.CurrentShiftID != w.bID ||
		joinStrings(itemEventKinds(ot)) != "created" {
		t.Fatalf("同班次其他事项应保持未关闭原状：%+v", ot)
	}
}

// assertItemClosedAfterRecovery 用当前打开的数据核对：保存恢复后的关闭成功，
// 只追加这次成功关闭的操作人和时间，编号、事项信息与原有历史保留；失败尝试
// 无痕迹，上一班结束时记录仍显示当时未关闭，原交接仍保留确认接收结果。
func assertItemClosedAfterRecovery(t *testing.T, f *fixture, w failedItemCloseExpectations) {
	t.Helper()

	it, err := f.svc.GetItem(w.idOpen)
	if err != nil {
		t.Fatalf("get item after success: %v", err)
	}
	if !it.Closed || it.CloseOperator != w.successOperator ||
		it.ClosedAt == nil || !it.ClosedAt.Equal(w.successAt) {
		t.Fatalf("事项应以成功操作的操作人与时间关闭：%+v", it)
	}
	if it.Content != w.midContent || it.Severity != w.midSeverity ||
		it.Constraints != w.midConstraints || it.FollowOwner != w.midFollow {
		t.Fatalf("关闭不应改动事项内容、严重程度、限制条件与后续负责人：%+v", it)
	}
	if it.ID != w.idOpen || it.OriginShiftID != w.aID || it.CurrentShiftID != w.bID ||
		len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.aID || it.ShiftIDs[1] != w.bID {
		t.Fatalf("事项编号与流经班次应保持：%+v", it)
	}
	// 历史只追加一次成功关闭：建立、接收、修改、关闭各一次；没有失败尝试。
	if got := joinStrings(itemEventKinds(it)); got != "created,received,updated,closed" {
		t.Fatalf("事项历史应只追加一次成功关闭，got %s", got)
	}
	closedCount := 0
	for _, ev := range it.Events {
		if ev.Kind == "closed" {
			closedCount++
			if !ev.At.Equal(w.successAt) || ev.Operator != w.successOperator {
				t.Fatalf("关闭经过应属于成功操作：%+v", ev)
			}
		}
		if !ev.At.IsZero() && ev.At.Equal(w.failAt) {
			t.Fatalf("失败尝试时刻不应留在事项历史：%+v", ev)
		}
		if ev.Operator == w.failedOperator {
			t.Fatalf("失败尝试的操作人不应留在事项历史：%+v", ev)
		}
	}
	if closedCount != 1 {
		t.Fatalf("应只记录一次关闭，got %d", closedCount)
	}

	// 处理经过只追加一次成功关闭，接收与修改历史原样保留。
	j, err := f.svc.ItemJourney(w.idOpen)
	if err != nil {
		t.Fatalf("journey after success: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated,closed" {
		t.Fatalf("处理经过应追加一次成功关闭，got %s", got)
	}
	journeyClosed := 0
	for _, ev := range j.Events {
		if ev.Kind == "closed" {
			journeyClosed++
			if !ev.TimeKnown || !ev.At.Equal(w.successAt) || ev.Operator != w.successOperator {
				t.Fatalf("处理经过中的关闭应属于成功操作：%+v", ev)
			}
		}
		if ev.TimeKnown && ev.At.Equal(w.failAt) || ev.Operator == w.failedOperator {
			t.Fatalf("失败尝试不应出现在成功后的处理经过：%+v", ev)
		}
	}
	if journeyClosed != 1 {
		t.Fatalf("处理经过应只有一次关闭记录，got %d", journeyClosed)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[0].Entry.Operator != "李四" ||
		j.Results[0].Entry.ProcessedAt == nil || !j.Results[0].Entry.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("原交接的确认接收结果仍应保留：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	wantClose := fmt.Sprintf("已关闭（%s 于 %s）", w.successOperator, fmtTime(w.successAt))
	if !strings.Contains(jText, wantClose) ||
		!strings.Contains(jText, "关闭 操作人="+w.successOperator) {
		t.Fatalf("事项查询应显示成功关闭的操作人与时间：\n%s", jText)
	}
	if strings.Contains(jText, w.failedOperator) || strings.Contains(jText, fmtTime(w.failAt)) {
		t.Fatalf("事项查询不应留下失败尝试的痕迹：\n%s", jText)
	}

	// 原交接仍保留当时的确认接收结果、接收人和接收时间。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover after success: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("交接完成时间应仍是接收齐的时刻：%+v", h)
	}
	e := findEntryOf(t, h, w.idOpen)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("原交接的确认接收结果不应改变：%+v", e)
	}

	// 上一班结束时记录仍显示该事项当时未关闭、原内容与原负责人；最新状态
	// 对照才反映它已在接班班次关闭。
	repA, err := f.svc.ShiftReport(w.aID)
	if err != nil {
		t.Fatalf("report prior shift after success: %v", err)
	}
	snap := findCloseItem(repA, w.idOpen)
	if snap == nil || snap.Closed || snap.Content != w.origContent || snap.FollowOwner != "李四" {
		t.Fatalf("上一班结束时记录应仍显示当时未关闭：%+v", snap)
	}
	latest := repA.LatestItems[w.idOpen]
	if !latest.Closed || latest.CurrentShiftID != w.bID ||
		latest.CloseOperator != w.successOperator ||
		latest.ClosedAt == nil || !latest.ClosedAt.Equal(w.successAt) {
		t.Fatalf("最新状态对照应反映接班班次的成功关闭：%+v", latest)
	}
	textA := FormatReport(repA)
	if !strings.Contains(textA, "结束时未关闭") || !strings.Contains(textA, w.origContent) {
		t.Fatalf("上一班报告应仍显示结束时未关闭的原记录：\n%s", textA)
	}
	if strings.Contains(textA, w.failedOperator) {
		t.Fatalf("上一班报告不应出现失败尝试的操作人：\n%s", textA)
	}

	// 当前班次报告显示该事项已关闭，关闭人与关闭时间属于成功操作；同班次其他
	// 事项仍未关闭；接班班次本身仍在进行中。
	repB, err := f.svc.ShiftReport(w.bID)
	if err != nil {
		t.Fatalf("report current shift after success: %v", err)
	}
	if repB.Shift.Closed || repB.ItemsAtClose {
		t.Fatalf("接班班次应仍在进行中：%+v", repB.Shift)
	}
	if len(repB.Items) != 2 || repB.Items[0].ID != w.idOpen {
		t.Fatalf("接班班次当前事项应仍为2项，got %+v", repB.Items)
	}
	cur := repB.Items[0]
	if !cur.Closed || cur.CloseOperator != w.successOperator ||
		cur.ClosedAt == nil || !cur.ClosedAt.Equal(w.successAt) {
		t.Fatalf("当前班次报告应显示成功关闭的操作人与时间：%+v", cur)
	}
	if repB.Items[1].Closed {
		t.Fatalf("同班次其他事项不应被连带关闭：%+v", repB.Items[1])
	}
	textB := FormatReport(repB)
	if !strings.Contains(textB, wantClose) {
		t.Fatalf("当前班次报告应显示成功关闭：\n%s", textB)
	}
	if strings.Contains(textB, fmtTime(w.failAt)) || strings.Contains(textB, w.failedOperator) {
		t.Fatalf("当前班次报告不应留下失败尝试的痕迹：\n%s", textB)
	}
}

// TestCloseItemSaveFailureAtomicRollback：接班人关闭已确认接收、接班后又修改
// 过的未关闭事项时，编号正确、事项未关闭、接班班次进行中、操作人已填写，
// 业务条件全部成立、真正进入保存过程后本地写盘失败——关闭操作必须明确返回
// 保存错误，不能当成关闭成功；内存与磁盘都回到失败前：事项仍未关闭、关闭
// 人与关闭时间未记录，处理经过保留建立、接收与修改而不出现关闭记录，接收
// 事实不撤销，上一班结束时记录（当时未关闭）与已关闭事项不受影响，同班次
// 其他事项保持原状，原数据文件一个字节不变，重新打开后一致。保存恢复后再
// 对同一事项填写操作人关闭，按现有功能成功，只追加这次成功关闭的操作人和
// 时间；成功后重复关闭或提交空白操作人都明确拒绝，不覆盖原关闭人或时间、
// 不多记关闭经过。
func TestCloseItemSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	// 上一班：一项未关闭（随后交给接班班次），一项结束前已关闭。
	idOpen, err := f.svc.AddItem(a.ID, "上一班遗留的压力异常", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add open item: %v", err)
	}
	idClosed, err := f.svc.AddItem(a.ID, "上一班已关闭的巡检事项", SeverityImportant, "需复查记录", "李四")
	if err != nil {
		t.Fatalf("add closed item: %v", err)
	}
	idClosedAt := tsDay(2, 14, 0)
	f.svc.nowAt(func() time.Time { return idClosedAt })
	if _, err := f.svc.CloseItem(idClosed.ID, "张三"); err != nil {
		t.Fatalf("close item in prior shift: %v", err)
	}
	aClosedAt := tsDay(2, 15, 0)
	f.svc.nowAt(func() time.Time { return aClosedAt })
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close prior shift: %v", err)
	}

	// 交接完成：未关闭事项由接班人确认接收。
	handoverCreatedAt := tsDay(2, 16, 30)
	f.svc.nowAt(func() time.Time { return handoverCreatedAt })
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	confirmAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, idOpen.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm receive: %v", err)
	}

	// 接班后修改过该事项，使失败前后需要保留的“当前信息”明确可辨。
	openUpdatedAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return openUpdatedAt })
	if _, err := f.svc.UpdateItem(idOpen.ID, "接班后修订的压力异常", SeverityUrgent,
		"接班后新增限制", "赵六"); err != nil {
		t.Fatalf("update received item: %v", err)
	}

	// 同一接班班次另有一项未关闭事项，用于核对它不被连带影响。
	otherCreatedAt := tsDay(2, 18, 30)
	f.svc.nowAt(func() time.Time { return otherCreatedAt })
	idOther, err := f.svc.AddItem(b.ID, "接班班次的其他事项", SeverityNormal, "", "孙九")
	if err != nil {
		t.Fatalf("add other item in current shift: %v", err)
	}

	// 前置：事项存在且未关闭、接班班次进行中、交接已完成。
	pre, _ := f.svc.GetItem(idOpen.ID)
	if pre.Closed || pre.CurrentShiftID != b.ID {
		t.Fatalf("前置：事项应未关闭且属于接班班次：%+v", pre)
	}
	if preShift, _ := f.svc.GetShift(b.ID); preShift.Closed {
		t.Fatalf("前置：接班班次应仍在进行中")
	}
	if preH, _ := f.svc.GetHandover(h.ID); !preH.Completed() {
		t.Fatalf("前置：交接应已确认接收完成")
	}

	want := failedItemCloseExpectations{
		aID: a.ID, bID: b.ID, hID: h.ID,
		idOpen: idOpen.ID, idOther: idOther.ID, idClosed: idClosed.ID,
		aClosedAt:         aClosedAt,
		idClosedAt:        idClosedAt,
		handoverCreatedAt: handoverCreatedAt,
		confirmAt:         confirmAt,
		openUpdatedAt:     openUpdatedAt,
		otherCreatedAt:    otherCreatedAt,
		failAt:            tsDay(2, 19, 0),
		successAt:         tsDay(2, 20, 0),
		origContent:       "上一班遗留的压力异常",
		origConstraints:   "",
		midContent:        "接班后修订的压力异常",
		midConstraints:    "接班后新增限制",
		midFollow:         "赵六",
		midSeverity:       SeverityUrgent,
		failedOperator:    "接班人王五",
		successOperator:   "接班人郑七",
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的关闭时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return want.failAt })

	// 使下一次写盘在原子保存阶段失败，然后由接班人填写操作人关闭该事项。
	breakSaving(t, f)
	got, err := f.svc.CloseItem(idOpen.ID, want.failedOperator)
	// 操作输出必须明确报错：这是保存错误而不是业务拒绝（不能用编号不存在、
	// 事项已关闭、班次已结束或操作人为空来冒充），也不能把回滚掉的关闭状态
	// 当作关闭结果返回。
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
		t.Fatalf("保存失败不得把回滚掉的关闭状态当作关闭结果返回：%+v", got)
	}

	// 当前打开的数据上查看事项、交接与报告，应看到失败前的事实，而不是只
	// 保证文件没变、查询却显示已经关闭。
	assertItemStillOpenAfterFailedClose(t, f, want)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的关闭不能成为其中的业务事实")
	}

	// 退出后重新打开同一数据文件：事项仍未关闭，原有的接收与修改经过都在。
	f.reopen(t)
	assertItemStillOpenAfterFailedClose(t, f, want)

	// 保存恢复后，接班人再对同一事项填写操作人并关闭：按现有功能成功。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return want.successAt })
	closed, err := f.svc.CloseItem(idOpen.ID, want.successOperator)
	if err != nil {
		t.Fatalf("保存恢复后关闭事项应成功：%v", err)
	}
	if !closed.Closed || closed.CloseOperator != want.successOperator ||
		closed.ClosedAt == nil || !closed.ClosedAt.Equal(want.successAt) {
		t.Fatalf("成功关闭的返回结果应为本次操作人与时间：%+v", closed)
	}
	assertItemClosedAfterRecovery(t, f, want)

	// 成功落盘的文件只包含成功事实，不包含失败尝试时刻或失败尝试的操作人。
	rawSuccess, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after success: %v", err)
	}
	if !strings.Contains(string(rawSuccess), want.successAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("成功关闭时间应已写入数据文件")
	}
	if strings.Contains(string(rawSuccess), want.failAt.Format("2006-01-02T15:04:05-07:00")) {
		t.Fatalf("失败的关闭尝试时刻不应留在数据文件中")
	}
	if strings.Contains(string(rawSuccess), want.failedOperator) {
		t.Fatalf("失败尝试填写的操作人不应写入数据文件")
	}

	// 已成功关闭的事项再次关闭：明确拒绝，不覆盖原关闭人或时间，也不多记
	// 一条关闭经过。
	if _, err := f.svc.CloseItem(idOpen.ID, "赵六"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重复关闭已关闭事项应报 ErrHandoverState，got %v", err)
	} else if !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("重复关闭的错误信息应指出事项已关闭，got %v", err)
	}
	retry, _ := f.svc.GetItem(idOpen.ID)
	if !retry.Closed || retry.CloseOperator != want.successOperator ||
		retry.ClosedAt == nil || !retry.ClosedAt.Equal(want.successAt) ||
		joinStrings(itemEventKinds(retry)) != "created,received,updated,closed" {
		t.Fatalf("重复关闭被拒绝后原关闭人、关闭时间与处理经过应保持：%+v", retry)
	}

	// 操作人为空或只有空白：同样拒绝并保留原状态。
	if _, err := f.svc.CloseItem(idOpen.ID, "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白操作人关闭应报 ErrInvalidInput，got %v", err)
	}
	blank, _ := f.svc.GetItem(idOpen.ID)
	if !blank.Closed || blank.CloseOperator != want.successOperator ||
		blank.ClosedAt == nil || !blank.ClosedAt.Equal(want.successAt) ||
		joinStrings(itemEventKinds(blank)) != "created,received,updated,closed" {
		t.Fatalf("空白操作人被拒绝后原状态应保持：%+v", blank)
	}

	// 退出重开后，成功关闭的事实、原有历史与上一班原始记录都完整保留，
	// 失败尝试仍无痕迹；重复关闭依旧被拒绝。
	f.reopen(t)
	assertItemClosedAfterRecovery(t, f, want)
	if _, err := f.svc.CloseItem(idOpen.ID, "赵六"); !errors.Is(err, ErrHandoverState) {
		t.Fatalf("重开后重复关闭仍应拒绝，got %v", err)
	}
}
