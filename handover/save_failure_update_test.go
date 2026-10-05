package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// updateScenario 是修改保存失败场景的事实基线：失败尝试发生前已保存的
// 班次、交接与事项信息，以及失败/成功两次尝试的时刻与拟写入的新值。
type updateScenario struct {
	itemID, hID        string
	fromShift, toShift string
	oldContent         string
	oldSeverity        Severity
	oldConstraints     string // 原本非空的限制条件
	oldFollow          string
	confirmAt          time.Time // 接班确认接收的处理时间
	failAt             time.Time // 失败尝试时刻（不应留下任何痕迹）
	successAt          time.Time // 保存恢复后的成功修改时刻
	newContent         string
	newSeverity        Severity
	newConstraints     string
	newFollow          string
}

// prepareReceivedItem 建立场景：上一班（fromShift）已结束，事项原本有明确的
// 内容、严重程度、非空限制条件和后续负责人，处理经过保留建立与接收；事项已
// 确认接收到仍在进行中的下一班（toShift），原始班次不能妨碍接班人在当前班次
// 的正常修改。
func prepareReceivedItem(t *testing.T, f *fixture) updateScenario {
	t.Helper()
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	it, err := f.svc.AddItem(a.ID, "一号泵压力异常", SeverityImportant, "需停电窗口", "王五")
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
	w := updateScenario{
		itemID: it.ID, hID: h.ID, fromShift: a.ID, toShift: b.ID,
		oldContent: "一号泵压力异常", oldSeverity: SeverityImportant,
		oldConstraints: "需停电窗口", oldFollow: "王五",
		confirmAt:  tsDay(2, 16, 30),
		failAt:     tsDay(2, 18, 0),
		successAt:  tsDay(2, 19, 0),
		newContent: "一号泵压力异常（已复核）", newSeverity: SeverityUrgent,
		newConstraints: "白班可申请停电", newFollow: "赵六",
	}
	f.svc.nowAt(func() time.Time { return w.confirmAt })
	if _, err := f.svc.ProcessEntry(h.ID, it.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm receive: %v", err)
	}
	// 前置：事项已进入当前班次，处理经过只有建立与接收。
	cur, err := f.svc.GetItem(it.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if cur.CurrentShiftID != b.ID || cur.Closed {
		t.Fatalf("前置：事项应已确认接收到进行中的下一班且未关闭：%+v", cur)
	}
	if got := joinStrings(eventKinds(cur)); got != "created,received" {
		t.Fatalf("前置：处理经过应保留建立与接收，got %s", got)
	}
	return w
}

// eventKinds 提取事项自身历史的事件类型序列。
func eventKinds(it Item) []string {
	kinds := make([]string, len(it.Events))
	for i, ev := range it.Events {
		kinds[i] = ev.Kind
	}
	return kinds
}

// assertItemIntactAfterFailedUpdate 用当前打开的数据核对：修改保存失败后，
// 凭事项编号查询仍显示失败前的全部信息（四项原值完整，不出现内容已变而负责人
// 未变、或原限制条件被清空这类半套修改）；编号、原始班次、当前班次、流经班次
// 与未关闭状态保持原样；处理经过只保留此前的建立与接收；当前班次报告与单项
// 查询一致；上一班结束时记录与交接中的确认接收结果不受影响。
func assertItemIntactAfterFailedUpdate(t *testing.T, f *fixture, w updateScenario) {
	t.Helper()

	// 单项查询：四项信息全部为失败前的原值，不是尚未保存的新值。
	it, err := f.svc.GetItem(w.itemID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.Content != w.oldContent || it.Severity != w.oldSeverity ||
		it.Constraints != w.oldConstraints || it.FollowOwner != w.oldFollow {
		t.Fatalf("失败后查询应显示失败前的全部信息（内容/严重程度/限制条件/负责人）：%+v", it)
	}
	if it.Constraints == "" {
		t.Fatalf("原限制条件不应被清空：%+v", it)
	}
	// 编号、原始班次、当前班次、流经班次与未关闭状态保持原样。
	if it.ID != w.itemID || it.OriginShiftID != w.fromShift ||
		it.CurrentShiftID != w.toShift {
		t.Fatalf("编号、原始班次与当前班次应保持原样：%+v", it)
	}
	if len(it.ShiftIDs) != 2 || it.ShiftIDs[0] != w.fromShift || it.ShiftIDs[1] != w.toShift {
		t.Fatalf("流经班次应保持原样：%v", it.ShiftIDs)
	}
	if it.Closed || it.ClosedAt != nil || it.CloseOperator != "" {
		t.Fatalf("未关闭状态应保持原样：%+v", it)
	}
	if n := countItems(f, w.itemID); n != 1 {
		t.Fatalf("失败尝试不应复制事项，编号 %s 出现 %d 次", w.itemID, n)
	}
	// 处理经过保留此前的建立与接收，不新增这次失败的修改记录，也不丢失原有记录。
	if got := joinStrings(eventKinds(it)); got != "created,received" {
		t.Fatalf("失败后处理经过应仍只有建立与接收，got %s", got)
	}
	for _, ev := range it.Events {
		if ev.Kind == "updated" || ev.At.Equal(w.failAt) ||
			strings.Contains(ev.Detail, w.newContent) {
			t.Fatalf("不应留下失败修改的记录：%+v", ev)
		}
	}

	// 处理经过查询与单项查询一致：时间线只有建立、发起交接、确认接收。
	j, err := f.svc.ItemJourney(w.itemID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm" {
		t.Fatalf("处理经过不应新增失败的修改记录，got %s", got)
	}
	for _, ev := range j.Events {
		if ev.Kind == "updated" || (ev.TimeKnown && ev.At.Equal(w.failAt)) {
			t.Fatalf("处理经过不应出现失败修改或其时刻：%+v", ev)
		}
	}
	if len(j.Results) != 1 || j.Results[0].Entry.Status != EntryConfirmed ||
		j.Results[0].Entry.Operator != "李四" ||
		j.Results[0].Entry.ProcessedAt == nil ||
		!j.Results[0].Entry.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("交接的确认接收结果、处理人和时间不应受修改失败影响：%+v", j.Results)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, w.oldContent) || strings.Contains(jText, w.newContent) ||
		strings.Contains(jText, "修改：") {
		t.Fatalf("处理经过展示应仍为失败前信息，不出现修改记录：\n%s", jText)
	}

	// 当前班次报告中的事项信息与单项查询一致。
	rep, err := f.svc.ShiftReport(w.toShift)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	if rep.ItemsAtClose || rep.Shift.Closed {
		t.Fatalf("当前班次应仍在进行中：%+v", rep.Shift)
	}
	var inReport *Item
	for i := range rep.Items {
		if rep.Items[i].ID == w.itemID {
			inReport = &rep.Items[i]
		}
	}
	if inReport == nil {
		t.Fatalf("当前班次报告应包含该事项：%+v", rep.Items)
	}
	if !reflect.DeepEqual(*inReport, it) {
		t.Fatalf("当前班次报告中的事项信息应与单项查询一致：\nreport %+v\nitem   %+v", *inReport, it)
	}
	repText := FormatReport(rep)
	if !strings.Contains(repText, w.oldContent) || strings.Contains(repText, w.newContent) {
		t.Fatalf("当前班次报告应显示失败前内容：\n%s", repText)
	}

	// 上一班的结束时记录继续保留当时的内容、严重程度、限制条件和负责人。
	repFrom, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(repFrom, w.itemID)
	if snap == nil || snap.Content != w.oldContent || snap.Severity != w.oldSeverity ||
		snap.Constraints != w.oldConstraints || snap.FollowOwner != w.oldFollow || snap.Closed {
		t.Fatalf("上一班结束时记录应保持当时的四项信息：%+v", snap)
	}

	// 已有交接的确认接收结果、处理人和时间不受修改失败影响；清单原文保持原值。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	if !h.Completed() || h.CompletedAt == nil || !h.CompletedAt.Equal(w.confirmAt) {
		t.Fatalf("交接完成状态与完成时间应保持原样：%+v", h)
	}
	e := findEntryOf(t, h, w.itemID)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("确认接收结果、处理人和时间应保持原样：%+v", e)
	}
	if e.Content != w.oldContent || e.Severity != w.oldSeverity ||
		e.Constraints != w.oldConstraints || e.FollowOwner != w.oldFollow {
		t.Fatalf("交接清单中的事项原文应保持原值：%+v", e)
	}
}

// assertItemShowsSuccessfulUpdate 核对保存恢复后的成功修改：查询展示四项新
// 信息，处理经过新增一次对应的修改经过，发生时间属于这次成功操作；上一班结束
// 时记录与原交接中的事项原文不被新值替换。
func assertItemShowsSuccessfulUpdate(t *testing.T, f *fixture, w updateScenario) {
	t.Helper()

	it, err := f.svc.GetItem(w.itemID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.Content != w.newContent || it.Severity != w.newSeverity ||
		it.Constraints != w.newConstraints || it.FollowOwner != w.newFollow {
		t.Fatalf("成功修改后查询应展示四项新信息：%+v", it)
	}
	// 归属与状态不因修改改变。
	if it.OriginShiftID != w.fromShift || it.CurrentShiftID != w.toShift ||
		len(it.ShiftIDs) != 2 || it.Closed {
		t.Fatalf("修改不应改变编号归属与未关闭状态：%+v", it)
	}
	// 新增一次对应的修改经过，发生时间属于这次成功操作，不能沿用失败时刻。
	if got := joinStrings(eventKinds(it)); got != "created,received,updated" {
		t.Fatalf("处理经过应新增一次修改记录，got %s", got)
	}
	updates := 0
	for _, ev := range it.Events {
		if ev.At.Equal(w.failAt) {
			t.Fatalf("修改经过不能沿用失败时刻 %s：%+v", w.failAt, ev)
		}
		if ev.Kind == "updated" {
			updates++
			if !ev.At.Equal(w.successAt) {
				t.Fatalf("修改经过的发生时间应属于这次成功操作 %s：%+v", w.successAt, ev)
			}
			if ev.Detail != "修改字段：内容、严重程度、限制条件、后续负责人" {
				t.Fatalf("修改经过应记录四项字段的变更：%+v", ev)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("应只新增一次修改经过，got %d", updates)
	}

	// 处理经过查询同样只多这一次成功修改。
	j, err := f.svc.ItemJourney(w.itemID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if got := joinStrings(journeyKinds(j)); got != "created,handover-init,confirm,updated" {
		t.Fatalf("处理经过应只新增成功的这次修改，got %s", got)
	}
	jUpdates := 0
	for _, ev := range j.Events {
		if ev.TimeKnown && ev.At.Equal(w.failAt) {
			t.Fatalf("处理经过不能沿用失败时刻：%+v", ev)
		}
		if ev.Kind == "updated" {
			jUpdates++
			if !ev.TimeKnown || !ev.At.Equal(w.successAt) {
				t.Fatalf("修改经过时间应为成功操作时刻：%+v", ev)
			}
		}
	}
	if jUpdates != 1 {
		t.Fatalf("处理经过应只有一次修改记录，got %d", jUpdates)
	}
	jText := FormatItemJourney(j)
	if !strings.Contains(jText, w.newContent) || !strings.Contains(jText, "修改：修改字段：内容、严重程度、限制条件、后续负责人") {
		t.Fatalf("处理经过应展示四项新信息与本次修改：\n%s", jText)
	}

	// 当前班次报告与单项查询一致，展示四项新信息。
	rep, err := f.svc.ShiftReport(w.toShift)
	if err != nil {
		t.Fatalf("report current shift: %v", err)
	}
	var inReport *Item
	for i := range rep.Items {
		if rep.Items[i].ID == w.itemID {
			inReport = &rep.Items[i]
		}
	}
	if inReport == nil || !reflect.DeepEqual(*inReport, it) {
		t.Fatalf("当前班次报告中的事项信息应与单项查询一致：\nreport %+v\nitem   %+v", inReport, it)
	}

	// 最新负责人和接收当时的处理信息各自保留自己的含义：事项最新负责人是新值，
	// 交接清单仍保留接收当时的处理信息与事项原文，不被新值替换。
	h, err := f.svc.GetHandover(w.hID)
	if err != nil {
		t.Fatalf("get handover: %v", err)
	}
	e := findEntryOf(t, h, w.itemID)
	if e.Status != EntryConfirmed || e.Operator != "李四" ||
		e.ProcessedAt == nil || !e.ProcessedAt.Equal(w.confirmAt) {
		t.Fatalf("接收当时的处理信息应保持原样：%+v", e)
	}
	if e.Content != w.oldContent || e.Severity != w.oldSeverity ||
		e.Constraints != w.oldConstraints || e.FollowOwner != w.oldFollow {
		t.Fatalf("原交接中的事项原文不能被新值替换：%+v", e)
	}

	// 上一班结束时记录继续保留当时的四项信息。
	repFrom, err := f.svc.ShiftReport(w.fromShift)
	if err != nil {
		t.Fatalf("report from shift: %v", err)
	}
	snap := findCloseItem(repFrom, w.itemID)
	if snap == nil || snap.Content != w.oldContent || snap.Severity != w.oldSeverity ||
		snap.Constraints != w.oldConstraints || snap.FollowOwner != w.oldFollow {
		t.Fatalf("上一班结束时记录不能被新值替换：%+v", snap)
	}
}

// TestUpdateItemSaveFailureAtomicRollback：上一班已结束、事项已确认接收到仍在
// 进行中的下一班后，接班人通过现有修改功能同时改动内容、严重程度、限制条件和
// 后续负责人四项信息；参数合法、班次未结束、真正进入保存过程后本地写盘失败——
// 操作必须明确返回保存错误，不能把尚未保存的新值当作修改结果；内存与磁盘都
// 回到失败前：四项原值完整、归属与未关闭状态不变、处理经过仍只有建立与接收、
// 当前班次报告与单项查询一致、上一班结束时记录与交接确认结果不受影响。保存
// 恢复后对同一事项再次提交同样的修改应成功，查询展示四项新信息并新增一次
// 属于成功时刻的修改经过；退出重开后与最后一次成功保存一致。
func TestUpdateItemSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	w := prepareReceivedItem(t, f)

	// 记录失败前已落盘的文件内容，并固定失败尝试的时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	f.svc.nowAt(func() time.Time { return w.failAt })

	// 四项新值都合法、事项所在当前班次未结束；使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)
	_, err = f.svc.UpdateItem(w.itemID, w.newContent, w.newSeverity, w.newConstraints, w.newFollow)
	if err == nil {
		t.Fatalf("保存失败时修改应明确返回错误，不能当成修改成功")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误，got %v", err)
	}
	// 业务条件全部成立，不能用内容为空、班次已结束或编号不存在的拒绝来冒充
	// 这个保存失败场景。
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrShiftClosed) || errors.Is(err, ErrNotFound) {
		t.Fatalf("参数合法、班次进行中的保存失败不应被报告为业务校验错误，got %v", err)
	}

	// 当前打开的数据上查询，应看到失败前的全部信息，而不是只保证文件没变、
	// 查询却显示已经修改。
	assertItemIntactAfterFailedUpdate(t, f, w)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件")
	}

	// 在失败之后、成功之前退出重开：仍应看到原来已保存的事项与历史。
	f.reopen(t)
	assertItemIntactAfterFailedUpdate(t, f, w)

	// 恢复正常保存后，对同一事项再次提交同样的修改应能成功。
	restoreSaving(t, f)
	f.svc.nowAt(func() time.Time { return w.successAt })
	updated, err := f.svc.UpdateItem(w.itemID, w.newContent, w.newSeverity, w.newConstraints, w.newFollow)
	if err != nil {
		t.Fatalf("恢复后同样的修改应成功：%v", err)
	}
	if updated.Content != w.newContent || updated.Severity != w.newSeverity ||
		updated.Constraints != w.newConstraints || updated.FollowOwner != w.newFollow {
		t.Fatalf("成功的修改应返回四项新信息：%+v", updated)
	}
	assertItemShowsSuccessfulUpdate(t, f, w)

	// 退出后重新打开同一份数据，看到的应与最后一次成功保存后的查询一致。
	f.reopen(t)
	assertItemShowsSuccessfulUpdate(t, f, w)
}
