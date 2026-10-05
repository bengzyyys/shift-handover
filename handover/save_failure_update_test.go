package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“事项修改（item-update）在本地数据保存阶段失败”建立回归保障：
// 上一班已结束、事项已确认接收到仍在进行中的下一班，接班人通过现有修改功能
// 同时改动内容、严重程度、限制条件和后续负责人四项。只有本地数据保存成功才
// 算修改完成；写盘失败时操作明确报错，系统里不留下这次修改的任何一部分，
// 失败前已保存的数据（含上一班结束时记录与交接接收结果）原样保留。

// updateScenario 是一次修改回归场景的稳定事实：编号、原始四项与修改后四项。
type updateScenario struct {
	shiftFrom, shiftTo, handoverID, itemID string
	oldContent, oldConstraints, oldFollow  string
	oldSeverity                            Severity
	newContent, newConstraints, newFollow  string
	newSeverity                            Severity
}

// prepareReceivedItem 构建场景：上一班 S001（已结束）建立事项，经 H001 确认
// 接收到仍在进行中的下一班 S002；事项处理经过为建立与接收，四项信息齐全
// （限制条件非空）。返回场景事实与确认接收时刻。
func prepareReceivedItem(t *testing.T, f *fixture) updateScenario {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	it, err := f.svc.AddItem(a.ID, "原内容：一号泵压力异常", SeverityNormal, "原限制条件：需停电窗口", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close from shift: %v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	confirmAt := tsDay(2, 17, 0)
	f.svc.nowAt(func() time.Time { return confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, it.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm receive: %v", err)
	}
	return updateScenario{
		shiftFrom: a.ID, shiftTo: b.ID, handoverID: h.ID, itemID: it.ID,
		oldContent: "原内容：一号泵压力异常", oldSeverity: SeverityNormal,
		oldConstraints: "原限制条件：需停电窗口", oldFollow: "李四",
		newContent: "新内容：一号泵压力已复核", newSeverity: SeverityUrgent,
		newConstraints: "新限制条件：夜间禁动", newFollow: "王五",
	}
}

func itemEventKinds(it Item) []string {
	kinds := []string{}
	for _, ev := range it.Events {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

// assertUpdateRolledBack 用当前打开的数据核对：失败的修改没有留下任何一部分，
// 系统里仍是失败前已保存的全部信息。
func assertUpdateRolledBack(t *testing.T, f *fixture, sc updateScenario, confirmAt time.Time) {
	t.Helper()

	// 单项查询：四项信息仍是失败前的值——不能出现内容已变而负责人未变、
	// 或原限制条件被清空这类半完成状态；编号、原始班次、当前班次、流经班次
	// 与未关闭状态保持原样。
	it, err := f.svc.GetItem(sc.itemID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.ID != sc.itemID || it.OriginShiftID != sc.shiftFrom || it.CurrentShiftID != sc.shiftTo {
		t.Fatalf("事项编号与所在班次应保持原样：%+v", it)
	}
	if joinStrings(it.ShiftIDs) != sc.shiftFrom+","+sc.shiftTo {
		t.Fatalf("流经班次应保持原样：%v", it.ShiftIDs)
	}
	if it.Closed || it.ClosedAt != nil {
		t.Fatalf("事项应保持未关闭：%+v", it)
	}
	if it.Content != sc.oldContent || it.Severity != sc.oldSeverity ||
		it.Constraints != sc.oldConstraints || it.FollowOwner != sc.oldFollow {
		t.Fatalf("保存失败后四项信息应全部保持失败前的值：%+v", it)
	}
	// 处理经过保留此前的建立与接收，不新增这次失败的修改记录，也不丢失原有记录。
	if got := joinStrings(itemEventKinds(it)); got != "created,received" {
		t.Fatalf("事项处理经过应仍只有建立与接收，got %s", got)
	}

	// 处理经过查询与单项查询一致，交接当前结果仍是当时的确认接收。
	j, err := f.svc.ItemJourney(sc.itemID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if j.Item.Content != sc.oldContent || j.Item.Constraints != sc.oldConstraints ||
		j.Item.FollowOwner != sc.oldFollow || j.Item.Severity != sc.oldSeverity {
		t.Fatalf("处理经过中的最新状态应与失败前一致：%+v", j.Item)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm" {
		t.Fatalf("处理经过不应出现失败的修改记录，got %s", got)
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[0].Entry.Operator != "李四" ||
		j.Results[0].Entry.ProcessedAt == nil || !j.Results[0].Entry.ProcessedAt.Equal(confirmAt) {
		t.Fatalf("交接当前结果应仍为当时的确认接收：%+v", j.Results)
	}

	// 交接记录：确认接收结果、处理人和处理时间不受修改失败影响；
	// 清单里保存的事项原文不被改动。
	h, err := f.svc.GetHandover(sc.handoverID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	e := findEntryOf(t, h, sc.itemID)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(confirmAt) {
		t.Fatalf("交接中的确认接收结果应保持原样：%+v", e)
	}
	if e.Content != sc.oldContent || e.Constraints != sc.oldConstraints || e.FollowOwner != sc.oldFollow {
		t.Fatalf("交接清单保存的事项原文不应被改动：%+v", e)
	}

	// 当前班次报告中的事项信息与单项查询一致（仍是失败前的值）。
	repB, err := f.svc.ShiftReport(sc.shiftTo)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	cur := []Item{}
	for _, x := range repB.Items {
		if x.ID == sc.itemID {
			cur = append(cur, x)
		}
	}
	if len(cur) != 1 || cur[0].Content != sc.oldContent || cur[0].Severity != sc.oldSeverity ||
		cur[0].Constraints != sc.oldConstraints || cur[0].FollowOwner != sc.oldFollow {
		t.Fatalf("当前班次报告应与单项查询一致（失败前的值）：%+v", cur)
	}

	// 上一班的结束时记录继续保留当时的内容、严重程度、限制条件和负责人。
	repA, err := f.svc.ShiftReport(sc.shiftFrom)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	if !repA.ItemsAtClose {
		t.Fatalf("上一班已结束，应展示结束时记录")
	}
	snap := findCloseItem(repA, sc.itemID)
	if snap == nil || snap.Content != sc.oldContent || snap.Severity != sc.oldSeverity ||
		snap.Constraints != sc.oldConstraints || snap.FollowOwner != sc.oldFollow {
		t.Fatalf("上一班结束时记录应保持当时的四项信息：%+v", snap)
	}
}

// assertUpdateSucceeded 核对恢复保存后的那次成功修改：四项新信息生效、
// 新增一次修改经过且发生时间属于成功操作，历史记录各自保留原含义。
func assertUpdateSucceeded(t *testing.T, f *fixture, sc updateScenario, confirmAt, successAt time.Time) {
	t.Helper()

	it, err := f.svc.GetItem(sc.itemID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.Content != sc.newContent || it.Severity != sc.newSeverity ||
		it.Constraints != sc.newConstraints || it.FollowOwner != sc.newFollow {
		t.Fatalf("成功后查询应展示四项新信息：%+v", it)
	}
	if it.ID != sc.itemID || it.OriginShiftID != sc.shiftFrom || it.CurrentShiftID != sc.shiftTo ||
		joinStrings(it.ShiftIDs) != sc.shiftFrom+","+sc.shiftTo || it.Closed {
		t.Fatalf("编号、原始班次、当前班次、流经班次与未关闭状态不应被修改改变：%+v", it)
	}
	// 新增一次对应的修改经过，发生时间属于这次成功操作，不能沿用失败时刻。
	if got := joinStrings(itemEventKinds(it)); got != "created,received,updated" {
		t.Fatalf("成功后应只新增一次修改经过，got %s", got)
	}
	up := it.Events[len(it.Events)-1]
	if !up.At.Equal(successAt) {
		t.Fatalf("修改经过的发生时间应属于成功操作 %s，got %s", successAt, up.At)
	}

	j, err := f.svc.ItemJourney(sc.itemID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated" {
		t.Fatalf("处理经过应新增一次修改，got %s", got)
	}
	updated := 0
	for _, ev := range j.Events {
		if ev.Kind == "updated" {
			updated++
			if !ev.TimeKnown || !ev.At.Equal(successAt) {
				t.Fatalf("修改经过时间应为成功操作时刻：%+v", ev)
			}
		}
	}
	if updated != 1 {
		t.Fatalf("处理经过应只新增一次修改记录，got %d", updated)
	}

	// 最新负责人和接收当时的处理信息各自保留自己的含义：交接清单里的
	// 确认接收结果、处理人、处理时间与事项原文不被新值替换。
	h, err := f.svc.GetHandover(sc.handoverID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	e := findEntryOf(t, h, sc.itemID)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(confirmAt) {
		t.Fatalf("接收当时的处理信息应保持原样：%+v", e)
	}
	if e.Content != sc.oldContent || e.Constraints != sc.oldConstraints || e.FollowOwner != sc.oldFollow {
		t.Fatalf("交接中的事项原文不能被新值替换：%+v", e)
	}
	if h.CompletedAt == nil || !h.CompletedAt.Equal(confirmAt) {
		t.Fatalf("交接完成时间应保持接收齐的时刻：%+v", h.CompletedAt)
	}

	// 上一班结束时记录继续保留当时的四项信息。
	repA, err := f.svc.ShiftReport(sc.shiftFrom)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(repA, sc.itemID)
	if snap == nil || snap.Content != sc.oldContent || snap.Severity != sc.oldSeverity ||
		snap.Constraints != sc.oldConstraints || snap.FollowOwner != sc.oldFollow {
		t.Fatalf("上一班结束时记录不应被新值替换：%+v", snap)
	}

	// 当前班次报告展示修改后的新值，与单项查询一致。
	repB, err := f.svc.ShiftReport(sc.shiftTo)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	cur := []Item{}
	for _, x := range repB.Items {
		if x.ID == sc.itemID {
			cur = append(cur, x)
		}
	}
	if len(cur) != 1 || cur[0].Content != sc.newContent || cur[0].Severity != sc.newSeverity ||
		cur[0].Constraints != sc.newConstraints || cur[0].FollowOwner != sc.newFollow {
		t.Fatalf("当前班次报告应展示修改后的四项新信息：%+v", cur)
	}
}

// TestUpdateSaveFailureAtomicRollback：接班人在当前班次对已接收事项同时修改
// 内容、严重程度、限制条件和后续负责人，参数合法、状态允许、真正进入保存过程
// 后本地写盘失败——操作必须明确返回保存错误、不把未保存的新值当作修改结果；
// 凭编号查询仍是失败前的全部信息，处理经过、当前班次报告、上一班结束时记录
// 与交接接收结果都不变，数据文件一个字节不动，重新打开后一致。保存恢复后对
// 同一事项再次提交同样的修改应成功，且只留下成功这一次修改经过。
func TestUpdateSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	sc := prepareReceivedItem(t, f)
	confirmAt := tsDay(2, 17, 0)

	// 记录失败前已落盘的文件内容，并固定失败尝试的发生时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	// 使下一次写盘在原子保存阶段失败，然后同时修改四项信息。
	breakSaving(t, f)
	got, err := f.svc.UpdateItem(sc.itemID, sc.newContent, sc.newSeverity, sc.newConstraints, sc.newFollow)
	// 操作输出必须明确报错：这是保存错误而不是业务拒绝（不能用内容为空、
	// 班次已结束等来冒充），也不能把尚未保存的新值当作修改结果返回。
	if err == nil {
		t.Fatalf("保存失败时修改应返回错误")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrShiftClosed) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、状态允许时的保存失败不应被报告为业务校验错误，got %v", err)
	}
	if !reflect.DeepEqual(got, Item{}) {
		t.Fatalf("保存失败不得把尚未保存的新值当作修改结果返回：%+v", got)
	}

	// 当前打开的数据中不留这次修改的任何一部分。
	assertUpdateRolledBack(t, f, sc, confirmAt)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 在失败之后、成功之前退出并重新打开：仍应看到原来已保存的事项与历史。
	f.reopen(t)
	assertUpdateRolledBack(t, f, sc, confirmAt)

	// 恢复正常保存后，对同一事项再次提交同样的修改应能成功。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	updated, err := f.svc.UpdateItem(sc.itemID, sc.newContent, sc.newSeverity, sc.newConstraints, sc.newFollow)
	if err != nil {
		t.Fatalf("恢复后修改应成功：%v", err)
	}
	if updated.Content != sc.newContent || updated.Severity != sc.newSeverity ||
		updated.Constraints != sc.newConstraints || updated.FollowOwner != sc.newFollow {
		t.Fatalf("成功修改的返回结果应为四项新信息：%+v", updated)
	}
	assertUpdateSucceeded(t, f, sc, confirmAt, successAt)

	// 退出后重新打开同一份数据，看到的应与最后一次成功保存后的查询一致。
	f.reopen(t)
	assertUpdateSucceeded(t, f, sc, confirmAt, successAt)
}
