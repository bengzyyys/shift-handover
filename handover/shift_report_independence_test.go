package handover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 本文件把“按班次查询（shift-show / ShiftReport）返回的报告与系统保存的记录
// 相互独立”这一约定固化为可重复执行的回归检查：
//   - 调用方改动已取得的报告（事项、结束时记录、最新状态对照、交接清单、
//     逐项结果、退回与补充经过），不影响系统保存的数据，也不影响另一份
//     已经取得的报告；再次查询仍得到系统原来保存的事实。
//   - 保留一份报告后，系统随后的正常处理（接收、修改、关闭）只反映在新
//     查询里，不改写先前报告。
//   - 没有事项或关联交接的班次、缺少结束时记录的旧数据班次同样遵守该约定，
//     空清单与缺少历史记录保持区别。

// buildReportScenario 构造带完整交接经过的场景：
// S001 已结束并留下结束时记录（I002 在结束前已关闭，I001、I003 结束时未关闭）；
// H001 由 S001 交给进行中的 S002，其中 I001 经历退回→补充重新提交→确认接收，
// I003 以继续跟踪接收并指定新的后续负责人。
func buildReportScenario(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	s1 := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	mustAddItem(t, f, s1.ID, "检查泵房压力", SeverityUrgent, "需双人确认", "李四")
	mustAddItem(t, f, s1.ID, "记录仪表读数", SeverityNormal, "", "王五")
	mustAddItem(t, f, s1.ID, "更换阀门密封", SeverityImportant, "", "赵六")
	if _, err := f.svc.CloseItem("I002", "张三"); err != nil {
		t.Fatalf("close item: %v", err)
	}
	if _, err := f.svc.CloseShift(s1.ID); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	s2 := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(3, 0, 0), "")
	if _, err := f.svc.CreateHandover(s1.ID, s2.ID); err != nil {
		t.Fatalf("create handover: %v", err)
	}
	mustProcessEntry(t, f, "H001", "I001", ActionReturn, "李四", "缺少现场照片", "", "")
	if _, err := f.svc.ResubmitReturned("H001", "I001", "张三", "已补现场照片"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	mustProcessEntry(t, f, "H001", "I001", ActionConfirm, "李四", "", "", "")
	mustProcessEntry(t, f, "H001", "I003", ActionTrack, "李四", "", "夜班继续观察", "孙七")
	return f
}

func mustAddItem(t *testing.T, f *fixture, shiftID, content string, sev Severity, constraints, follow string) Item {
	t.Helper()
	it, err := f.svc.AddItem(shiftID, content, sev, constraints, follow)
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	return it
}

func mustProcessEntry(t *testing.T, f *fixture, handoverID, itemID string, action EntryAction, operator, reason, note, follow string) {
	t.Helper()
	if _, err := f.svc.ProcessEntry(handoverID, itemID, action, operator, reason, note, follow); err != nil {
		t.Fatalf("process %s %s: %v", handoverID, itemID, err)
	}
}

func mustReport(t *testing.T, svc *Service, shiftID string) ShiftReport {
	t.Helper()
	rep, err := svc.ShiftReport(shiftID)
	if err != nil {
		t.Fatalf("report %s: %v", shiftID, err)
	}
	return rep
}

// TestShiftReportMutationKeepsSystemAndOtherReports：改动已取得的报告中的
// 班次关闭信息、结束时记录、最新状态对照、交接清单与逐项结果（含处理人、
// 处理时间、退回原因与补充说明）后，再次查询仍得到系统原来保存的事实，
// 另一份已经取得的报告也保持原样；重新打开数据文件后事实依旧。
func TestShiftReportMutationKeepsSystemAndOtherReports(t *testing.T) {
	f := buildReportScenario(t)

	keep := mustReport(t, f.svc, "S001")
	mut := mustReport(t, f.svc, "S001")
	textBefore := FormatReport(keep)

	fakeTime := tsDay(9, 9, 0)

	// 班次与结束时记录：已结束的交班班次，其结束时留下的事项与关闭时间
	// 不能因报告被改动而变化。
	mut.Shift.Closed = false
	*mut.Shift.ClosedAt = fakeTime
	mut.Shift.CloseRecord.Items[0].Content = "篡改内容"
	mut.Shift.CloseRecord.Items[1].Closed = false
	// 同一份报告内，班次内嵌的结束时记录与 CloseItems 也是各自独立的副本。
	if mut.CloseItems[0].Content == "篡改内容" {
		t.Fatalf("改动班次内嵌结束时记录不应连带改掉 CloseItems")
	}

	// 结束时事项快照：内容、负责人、关闭信息与关闭时间。
	mut.CloseItems[0].Content = "篡改内容"
	mut.CloseItems[0].FollowOwner = "篡改人"
	mut.CloseItems[0].Closed = true
	mut.CloseItems[0].ClosedAt = &fakeTime
	mut.CloseItems[0].CloseOperator = "篡改人"
	*mut.CloseItems[1].ClosedAt = fakeTime

	// 最新状态对照：事项内容、负责人、关闭信息、流经班次与处理经过。
	latest := mut.LatestItems["I001"]
	latest.Content = "篡改内容"
	latest.FollowOwner = "篡改人"
	latest.CurrentShiftID = "S999"
	latest.ShiftIDs = append(latest.ShiftIDs, "S999")
	latest.Events = append(latest.Events, ItemEvent{At: fakeTime, Kind: "closed", Operator: "篡改人"})
	latest.Closed = true
	latest.ClosedAt = &fakeTime
	latest.CloseOperator = "篡改人"
	mut.LatestItems["I001"] = latest

	// 交接清单：处理结果、处理人、处理时间、退回原因与补充说明、完成时间。
	e := &mut.Outgoing.Entries[0] // I001
	e.Status = EntryPending
	e.Operator = "篡改人"
	*e.ProcessedAt = fakeTime
	e.Rounds[0].Reason = "篡改原因"
	e.Rounds[0].Supplement = "篡改补充"
	*e.Rounds[0].SupplementAt = fakeTime
	*e.Rounds[0].ResubmittedAt = fakeTime
	mut.Outgoing.Entries[1].TrackingNote = "篡改说明"
	mut.Outgoing.Entries[1].FollowOwner = "篡改人"
	*mut.Outgoing.CompletedAt = fakeTime

	// 逐项结果：与交接清单表达同一事实的另一处展示。
	re := &mut.Results["I001"][0].Entry
	re.Status = EntryReturned
	re.Operator = "篡改人"
	*re.ProcessedAt = fakeTime
	re.Rounds[0].Reason = "篡改原因"
	mut.Results["I003"][0].Entry.Status = EntryPending

	// 再次查询仍得到系统原来保存的事实，且与未被动过的另一份报告一致。
	fresh := mustReport(t, f.svc, "S001")
	if !reflect.DeepEqual(keep, fresh) {
		t.Fatalf("改动报告后再次查询应得到系统原保存的事实\n旧：%+v\n新：%+v", keep, fresh)
	}
	if text := FormatReport(fresh); text != textBefore {
		t.Fatalf("改动报告后中文输出不应变化\n之前：\n%s\n之后：\n%s", textBefore, text)
	}

	// 退出后重新打开，保存的事实也不受影响（JSON 形式比较，落盘会抹平时区名）。
	f.reopen(t)
	reopened := mustReport(t, f.svc, "S001")
	keepJSON, err := json.Marshal(keep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reopenedJSON, err := json.Marshal(reopened)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(keepJSON) != string(reopenedJSON) {
		t.Fatalf("重新打开后查询应得到系统原保存的事实")
	}
}

// TestShiftReportHandoverListAndResultsAreSeparateCopies：同一交接在报告的
// 交接清单和逐项结果中出现时，改动其中一处不能连带改掉另一处，避免原本
// 用于核对的两处信息一起被无意修改。
func TestShiftReportHandoverListAndResultsAreSeparateCopies(t *testing.T) {
	f := buildReportScenario(t)

	// 交班视角：改动交接清单，逐项结果保持原样。
	rep := mustReport(t, f.svc, "S001")
	rep.Outgoing.Entries[0].Status = EntryReturned
	rep.Outgoing.Entries[0].Operator = "篡改人"
	*rep.Outgoing.Entries[0].ProcessedAt = tsDay(9, 9, 0)
	rep.Outgoing.Entries[0].Rounds[0].Reason = "篡改原因"
	got := rep.Results["I001"][0].Entry
	if got.Status != EntryConfirmed || got.Operator != "李四" || got.Rounds[0].Reason != "缺少现场照片" {
		t.Fatalf("改动交接清单不应连带改掉逐项结果：%+v", got)
	}

	// 改动逐项结果，交接清单保持原样。
	rep.Results["I003"][0].Entry.Status = EntryPending
	rep.Results["I003"][0].Entry.TrackingNote = "篡改说明"
	rep.Results["I003"][0].Entry.FollowOwner = "篡改人"
	got = rep.Outgoing.Entries[1]
	if got.Status != EntryTracking || got.TrackingNote != "夜班继续观察" || got.FollowOwner != "孙七" {
		t.Fatalf("改动逐项结果不应连带改掉交接清单：%+v", got)
	}

	// 接班视角的接班交接清单与逐项结果同样相互独立。
	rep2 := mustReport(t, f.svc, "S002")
	rep2.Incoming[0].Entries[0].Status = EntryPending
	*rep2.Incoming[0].Entries[0].ProcessedAt = tsDay(9, 9, 0)
	if got := rep2.Results["I001"][0].Entry; got.Status != EntryConfirmed {
		t.Fatalf("接班视角改动交接清单不应连带改掉逐项结果：%+v", got)
	}
	rep2.Results["I003"][0].Entry.Status = EntryReturned
	if got := rep2.Incoming[0].Entries[1]; got.Status != EntryTracking {
		t.Fatalf("接班视角改动逐项结果不应连带改掉交接清单：%+v", got)
	}
}

// TestShiftReportSurvivesLaterOperations：保留一份报告后，接班人通过已有功能
// 修改并关闭接收到的事项，系统的新查询反映这些成功操作，但先前报告仍保留
// 查询时的事项归属、负责人、关闭情况和交接进度（含退回与补充历史、已记录的
// 处理时间和交接完成时间）。进行中班次的当前事项被改动同样不影响系统数据。
func TestShiftReportSurvivesLaterOperations(t *testing.T) {
	f := buildReportScenario(t)

	keep := mustReport(t, f.svc, "S002")
	snapshot, err := json.Marshal(keep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// 改动进行中班次报告里的当前事项与接班清单，不影响系统保存的数据。
	mut := mustReport(t, f.svc, "S002")
	fakeTime := tsDay(9, 9, 0)
	mut.Items[0].Content = "篡改内容"
	mut.Items[0].FollowOwner = "篡改人"
	mut.Items[0].CurrentShiftID = "S999"
	mut.Items[0].ShiftIDs = append(mut.Items[0].ShiftIDs, "S999")
	mut.Items[0].Events = append(mut.Items[0].Events, ItemEvent{At: fakeTime, Kind: "closed", Operator: "篡改人"})
	mut.Items[0].Closed = true
	mut.Items[0].ClosedAt = &fakeTime
	mut.Incoming[0].Entries[0].Status = EntryPending
	if again := mustReport(t, f.svc, "S002"); !reflect.DeepEqual(keep, again) {
		t.Fatalf("改动进行中班次的报告不应影响系统保存的数据")
	}

	// 接班人正常处理：修改接收到的事项并关闭它。
	if _, err := f.svc.UpdateItem("I001", "复查泵房压力并记录", SeverityImportant, "夜班限双人操作", "周九"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem("I001", "李四"); err != nil {
		t.Fatalf("close item: %v", err)
	}

	// 新查询反映这些成功操作。
	fresh := mustReport(t, f.svc, "S002")
	var freshItem *Item
	for i := range fresh.Items {
		if fresh.Items[i].ID == "I001" {
			freshItem = &fresh.Items[i]
		}
	}
	if freshItem == nil || freshItem.Content != "复查泵房压力并记录" ||
		freshItem.FollowOwner != "周九" || !freshItem.Closed ||
		freshItem.ClosedAt == nil || freshItem.CloseOperator != "李四" {
		t.Fatalf("新查询应反映修改与关闭：%+v", freshItem)
	}

	// 先前报告仍保留查询时的事实，不混入后来新增的经过。
	after, err := json.Marshal(keep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(after) != string(snapshot) {
		t.Fatalf("系统随后的处理不应改写先前已取得的报告")
	}
	var keepItem *Item
	for i := range keep.Items {
		if keep.Items[i].ID == "I001" {
			keepItem = &keep.Items[i]
		}
	}
	if keepItem == nil || keepItem.Content != "检查泵房压力" || keepItem.FollowOwner != "李四" ||
		keepItem.CurrentShiftID != "S002" || keepItem.Closed || keepItem.ClosedAt != nil {
		t.Fatalf("先前报告应保留查询时的事项归属、负责人与关闭情况：%+v", keepItem)
	}
	keptEntry := keep.Incoming[0].Entries[0]
	if keptEntry.Status != EntryConfirmed || keptEntry.Operator != "李四" || keptEntry.ProcessedAt == nil {
		t.Fatalf("先前报告应保留查询时的交接进度与处理时间：%+v", keptEntry)
	}
	if len(keptEntry.Rounds) != 1 || keptEntry.Rounds[0].Reason != "缺少现场照片" ||
		keptEntry.Rounds[0].Supplement != "已补现场照片" {
		t.Fatalf("先前报告应保留查询时的退回与补充历史：%+v", keptEntry.Rounds)
	}
	if keep.Incoming[0].CompletedAt == nil ||
		!keep.Incoming[0].CompletedAt.Equal(*fresh.Incoming[0].CompletedAt) {
		t.Fatalf("交接完成时间应属于各自报告取得时的事实")
	}
	// 已记录的处理时间也属于各自报告取得时的事实。
	if !keptEntry.ProcessedAt.Equal(*fresh.Incoming[0].Entries[0].ProcessedAt) {
		t.Fatalf("先前报告的处理时间不应被后续操作改写")
	}
}

// TestShiftReportEmptyShiftIndependence：没有事项或关联交接的班次仍能正常
// 取得报告；空清单结束后留下空的结束时记录，与缺少历史记录相区别；改动
// 这样的报告同样不影响系统数据。
func TestShiftReportEmptyShiftIndependence(t *testing.T) {
	f := newFixture(t)
	sh := mustShift(t, f, "巡检", "赵六", tsDay(2, 8, 0), tsDay(2, 16, 0), "")

	rep := mustReport(t, f.svc, sh.ID)
	if len(rep.Items) != 0 || rep.Outgoing != nil || len(rep.Incoming) != 0 || len(rep.Results) != 0 {
		t.Fatalf("空班次应能正常取得空报告：%+v", rep)
	}

	if _, err := f.svc.CloseShift(sh.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	rep = mustReport(t, f.svc, sh.ID)
	if !rep.ItemsAtClose || rep.HistoryIncomplete || len(rep.CloseItems) != 0 {
		t.Fatalf("空清单结束应留下空的结束时记录：%+v", rep)
	}
	textBefore := FormatReport(rep)
	if !strings.Contains(textBefore, "结束时没有事项") {
		t.Fatalf("空清单结束应明确显示当时没有事项：\n%s", textBefore)
	}

	// 改动报告中的空结束时记录与关闭信息，再次查询仍为空记录。
	rep.Shift.CloseRecord.Items = append(rep.Shift.CloseRecord.Items, CloseItemSnapshot{ItemID: "I999", Content: "篡改内容"})
	rep.CloseItems = append(rep.CloseItems, CloseItemSnapshot{ItemID: "I999", Content: "篡改内容"})
	*rep.Shift.ClosedAt = tsDay(9, 9, 0)
	fresh := mustReport(t, f.svc, sh.ID)
	if !fresh.ItemsAtClose || len(fresh.CloseItems) != 0 || len(fresh.Shift.CloseRecord.Items) != 0 {
		t.Fatalf("改动报告不应影响空结束时记录：%+v", fresh)
	}
	if text := FormatReport(fresh); text != textBefore {
		t.Fatalf("改动报告后空班次输出不应变化")
	}
}

// TestShiftReportLegacyWithoutCloseRecordIndependence：旧数据中缺少结束时
// 记录的已结束班次，继续明确说明历史记录不完整并展示当前信息，不把当前值
// 宣称为结束时事实；改动这样的报告不影响系统数据，空清单与缺少历史记录
// 保持区别。
func TestShiftReportLegacyWithoutCloseRecordIndependence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	closedAt := tsDay(2, 16, 0)
	createdAt := tsDay(2, 8, 0)
	data := Data{
		ShiftSeq: 2,
		ItemSeq:  1,
		Shifts: []Shift{
			{ID: "S001", Position: "调度", Owner: "张三", Start: tsDay(2, 8, 0), End: tsDay(2, 16, 0),
				CreatedAt: createdAt, Closed: true, ClosedAt: &closedAt},
			{ID: "S002", Position: "调度", Owner: "李四", Start: tsDay(2, 16, 0), End: tsDay(2, 23, 0),
				CreatedAt: closedAt},
		},
		Items: []Item{{
			ID: "I001", OriginShiftID: "S001", ShiftIDs: []string{"S001"}, CurrentShiftID: "S001",
			Content: "旧内容", Severity: SeverityNormal, FollowOwner: "王五", CreatedAt: createdAt,
		}},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := NewService(store)

	rep := mustReport(t, svc, "S001")
	if !rep.HistoryIncomplete || rep.ItemsAtClose || len(rep.Items) != 1 {
		t.Fatalf("旧数据应标记历史记录不完整并展示当前信息：%+v", rep)
	}
	textBefore := FormatReport(rep)
	if !strings.Contains(textBefore, "历史记录不完整") || !strings.Contains(textBefore, "当前信息") {
		t.Fatalf("旧数据报告应标明历史记录不完整、以下为当前信息：\n%s", textBefore)
	}
	if strings.Contains(textBefore, "以下为结束时记录") {
		t.Fatalf("缺少结束时记录的旧数据不能把当前值宣称为结束时事实：\n%s", textBefore)
	}

	// 改动报告中的当前事项与历史标记，再次查询仍展示系统保存的当前信息，
	// 并继续明确说明历史记录不完整。
	rep.Items[0].Content = "篡改内容"
	rep.Items[0].FollowOwner = "篡改人"
	rep.Items[0].ShiftIDs = append(rep.Items[0].ShiftIDs, "S999")
	rep.Items[0].Events = append(rep.Items[0].Events, ItemEvent{At: tsDay(9, 9, 0), Kind: "closed", Operator: "篡改人"})
	rep.Items[0].Closed = true
	rep.HistoryIncomplete = false
	rep.ItemsAtClose = true

	fresh := mustReport(t, svc, "S001")
	if !fresh.HistoryIncomplete || fresh.ItemsAtClose {
		t.Fatalf("改动报告不应改变历史记录不完整的事实：%+v", fresh)
	}
	if len(fresh.Items) != 1 || fresh.Items[0].Content != "旧内容" ||
		fresh.Items[0].FollowOwner != "王五" || fresh.Items[0].Closed ||
		len(fresh.Items[0].ShiftIDs) != 1 || len(fresh.Items[0].Events) != 0 {
		t.Fatalf("再次查询仍应得到系统保存的当前信息：%+v", fresh.Items)
	}
	if text := FormatReport(fresh); text != textBefore {
		t.Fatalf("改动报告后旧数据班次输出不应变化")
	}

	// 空清单与缺少历史记录保持区别：空清单结束有结束时记录，
	// 旧数据班次没有，两者不能混为一谈。
	if _, err := svc.CloseShift("S002"); err != nil {
		t.Fatalf("close empty shift: %v", err)
	}
	empty := mustReport(t, svc, "S002")
	if !empty.ItemsAtClose || empty.HistoryIncomplete || len(empty.CloseItems) != 0 {
		t.Fatalf("空清单结束应留下空的结束时记录：%+v", empty)
	}
	legacy := mustReport(t, svc, "S001")
	if legacy.ItemsAtClose || !legacy.HistoryIncomplete {
		t.Fatalf("缺少历史记录与空清单应保持区别：%+v", legacy)
	}
}
