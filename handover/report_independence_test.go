package handover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件的测试把“按班次查询返回的报告是查询当时的独立副本”这一约定变成
// 可重复执行的检查：调用方可以暂存报告、改动报告内容用于自己的展示，但改动
// 不得影响系统保存的记录、不得影响同一班次先后取得的其他报告；系统随后的
// 正常处理（接收、修改、关闭事项）也不得改写已取得的报告。

// prepareReportScenario 构建含结束时记录与退回/补充历史的交接场景：
// a 已结束（I001、I002 结束时未关闭，I003 结束前已关闭），h 为 a→b 的交接；
// I002 被退回一轮后由交班人补充并重新提交（恢复待处理），I001 已确认接收，
// 整份交接仍未完成。
func prepareReportScenario(t *testing.T, f *fixture) (Shift, Shift, []Item, Handover) {
	t.Helper()
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, items[1].ID, ActionReturn, "李四", "需要补充细节", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, items[1].ID, "张三", "补充说明如下"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, items[0].ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return a, b, items, h
}

// mustJSON 把报告或记录序列化为 JSON 快照：序列化在取值当时展开全部指针，
// 之后即使报告与存储共享内存，快照里保存的仍是取值当时的原值。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestShiftReportMutationKeepsStoredFacts：调用方改动报告中的班次摘要、结束时
// 记录（含关闭时间）、最新状态对照（内容、负责人、关闭信息、流经班次、处理
// 经过）、交接清单与逐项结果中的结果、处理人、处理时间、退回原因与补充说明后，
// 再次查询仍得到系统原来保存的事实；先取得的另一份报告也保持原样。
func TestShiftReportMutationKeepsStoredFacts(t *testing.T) {
	f := newFixture(t)
	a, _, items, h := prepareReportScenario(t, f)
	idA, idB, idC := items[0].ID, items[1].ID, items[2].ID

	first, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	second, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report again: %v", err)
	}
	// 取值当时的 JSON 快照：之后改动报告即使经由共享内存写进存储，
	// 快照里仍是原值，能识别出任何方向的串改。
	firstSnapshot := mustJSON(t, first)
	storedSnapshot := mustJSON(t, mustGetHandover(t, f, h.ID))
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 班次摘要与班次上携带的结束时记录。
	second.Shift.Owner = "篡改负责人"
	*second.Shift.ClosedAt = forge
	second.Shift.CloseRecord.Items[0].Content = "篡改班次上的结束时内容"

	// 报告的结束时记录：内容、负责人、关闭信息与关闭时间。
	snap := findCloseItem(second, idC)
	if snap == nil || !snap.Closed || snap.ClosedAt == nil {
		t.Fatalf("前置：%s 应在结束时记录中且已关闭", idC)
	}
	snap.Content = "篡改关闭事项内容"
	snap.FollowOwner = "篡改负责人"
	snap.Closed = false
	*snap.ClosedAt = forge
	snap.CloseOperator = "篡改人"

	// 最新状态对照：内容、负责人、归属与流经班次、关闭信息、处理经过。
	latest := second.LatestItems[idA]
	latest.Content = "篡改最新内容"
	latest.FollowOwner = "篡改负责人"
	latest.CurrentShiftID = "S999"
	latest.ShiftIDs = append(latest.ShiftIDs, "S999")
	latest.Closed = true
	latest.ClosedAt = &forge
	latest.CloseOperator = "篡改人"
	latest.Events = append(latest.Events, ItemEvent{At: forge, Kind: "closed", Operator: "篡改人"})
	second.LatestItems[idA] = latest

	// 交接清单中的结果、处理人、处理时间、退回原因与补充说明、完成时间。
	ea := findEntryOf(t, *second.Outgoing, idA)
	if ea.ProcessedAt == nil {
		t.Fatalf("前置：%s 应已确认并记录处理时间", idA)
	}
	ea.Status = EntryReturned
	ea.Operator = "篡改人"
	*ea.ProcessedAt = forge
	eb := findEntryOf(t, *second.Outgoing, idB)
	eb.Rounds[0].Reason = "篡改退回原因"
	eb.Rounds[0].Supplement = "篡改补充说明"
	eb.Rounds[0].ReturnOperator = "篡改人"
	*eb.Rounds[0].SupplementAt = forge
	*eb.Rounds[0].ResubmittedAt = forge
	second.Outgoing.CompletedAt = &forge

	// 逐项结果中的同一交接条目。
	second.Results[idA][0].Entry.Status = EntryTracking
	second.Results[idA][0].Entry.Operator = "篡改人"
	second.Results[idB][0].Entry.Rounds[0].Reason = "篡改结果里的原因"
	second.Results[idB][0].Entry.Rounds[0].Supplement = "篡改结果里的补充"

	// 再次查询仍应得到系统原来保存的事实，且与先取得的那份报告完全一致
	//（既验证存储未被改动，也验证先后取得的两份报告互不影响）。
	third, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report after mutation: %v", err)
	}
	if got := mustJSON(t, third); got != firstSnapshot {
		t.Fatalf("改动报告后再次查询仍应得到系统原来保存的事实\nwant %s\ngot  %s", firstSnapshot, got)
	}

	// 系统保存的交接记录整体保持原值。
	if got := mustJSON(t, mustGetHandover(t, f, h.ID)); got != storedSnapshot {
		t.Fatalf("系统保存的交接记录不应被报告改动\nwant %s\ngot  %s", storedSnapshot, got)
	}
	// 直接核对系统保存的交接记录：退回原因与补充说明仍是原值。
	stored := mustGetHandover(t, f, h.ID)
	se := findEntryOf(t, stored, idB)
	if se.Rounds[0].Reason != "需要补充细节" || se.Rounds[0].Supplement != "补充说明如下" ||
		se.Rounds[0].ReturnOperator != "李四" || se.Rounds[0].SupplementOperator != "张三" {
		t.Fatalf("系统保存的退回与补充记录不应被报告改动：%+v", se.Rounds[0])
	}
	sae := findEntryOf(t, stored, idA)
	if sae.Status != EntryConfirmed || sae.Operator != "李四" {
		t.Fatalf("系统保存的处理结果不应被报告改动：%+v", sae)
	}
	if stored.CompletedAt != nil {
		t.Fatalf("交接尚未完成，报告改动不得写入完成时间")
	}
}

// TestShiftReportListAndResultsDoNotShareStorage：同一交接在报告的交接清单
// （Outgoing/Incoming）和逐项结果（Results）中各是一份副本，改动其中一处
// 不能连带改掉另一处，避免用于核对的两处信息一起被无意修改。
func TestShiftReportListAndResultsDoNotShareStorage(t *testing.T) {
	f := newFixture(t)
	a, b, items, h := prepareReportScenario(t, f)
	idA, idB := items[0].ID, items[1].ID
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 交班班次报告：改 Outgoing 清单，逐项结果不受影响。
	ra, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	ea := findEntryOf(t, *ra.Outgoing, idA)
	origProcessedAt := *ea.ProcessedAt
	ea.Status = EntryReturned
	ea.Operator = "篡改人"
	*ea.ProcessedAt = forge
	got := ra.Results[idA][0].Entry
	if got.Status != EntryConfirmed || got.Operator != "李四" ||
		got.ProcessedAt == nil || !got.ProcessedAt.Equal(origProcessedAt) {
		t.Fatalf("改动交接清单不应连带改掉逐项结果：%+v", got)
	}

	// 反向：改逐项结果里的退回历史（含补充时间指针），Outgoing 清单中的
	// 同一交接不受影响。
	rb0 := findEntryOf(t, *ra.Outgoing, idB)
	origSupplementAt := *rb0.Rounds[0].SupplementAt
	ra.Results[idB][0].Entry.Rounds[0].Reason = "篡改原因"
	ra.Results[idB][0].Entry.Rounds[0].Supplement = "篡改补充"
	*ra.Results[idB][0].Entry.Rounds[0].SupplementAt = forge
	eb := findEntryOf(t, *ra.Outgoing, idB)
	if eb.Rounds[0].Reason != "需要补充细节" || eb.Rounds[0].Supplement != "补充说明如下" ||
		!eb.Rounds[0].SupplementAt.Equal(origSupplementAt) {
		t.Fatalf("改动逐项结果不应连带改掉交接清单：%+v", eb.Rounds[0])
	}

	// 接班班次报告：Incoming 清单与逐项结果同样是各自独立的副本。
	rb, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report b: %v", err)
	}
	if len(rb.Incoming) != 1 || rb.Incoming[0].ID != h.ID {
		t.Fatalf("前置：接班班次应看到交接 %s，got %+v", h.ID, rb.Incoming)
	}
	ie := findEntryOf(t, rb.Incoming[0], idA)
	origIncomingProcessedAt := *ie.ProcessedAt
	ie.Status = EntryReturned
	ie.Operator = "篡改人"
	*ie.ProcessedAt = forge
	gotIn := rb.Results[idA][0].Entry
	if gotIn.Status != EntryConfirmed || gotIn.Operator != "李四" ||
		gotIn.ProcessedAt == nil || !gotIn.ProcessedAt.Equal(origIncomingProcessedAt) {
		t.Fatalf("改动接班清单不应连带改掉逐项结果：%+v", gotIn)
	}
	rb.Results[idB][0].Entry.Rounds[0].Reason = "篡改原因"
	ib := findEntryOf(t, rb.Incoming[0], idB)
	if ib.Rounds[0].Reason != "需要补充细节" {
		t.Fatalf("改动逐项结果不应连带改掉接班清单：%+v", ib.Rounds[0])
	}
}

// TestEarlierReportSurvivesLaterProcessing：保留报告后，接班人通过已有功能正常
// 接收事项、修改并关闭它，系统的新查询反映这些成功操作，但先前报告仍保留
// 查询时的事项归属、负责人、关闭情况与交接进度；退回与补充历史、处理时间与
// 交接完成时间各属各自报告取得时的事实，旧报告不混入后来新增的经过。
func TestEarlierReportSurvivesLaterProcessing(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idA, idB := items[0].ID, items[1].ID
	// 先制造一轮退回/补充，作为旧报告取得时已有的历史。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionReturn, "李四", "需要补充细节", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充说明如下"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}

	oldA, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("report a: %v", err)
	}
	oldB, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report b: %v", err)
	}

	// 接班人正常接收两项，随后修改并关闭其中一项。
	if _, err := f.svc.ProcessEntry(h.ID, idA, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.UpdateItem(idA, "接班后修改的内容", SeverityUrgent, "新限制", "新负责人"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem(idA, "李四"); err != nil {
		t.Fatalf("close item: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "继续盯压力", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// 新查询反映这些成功操作：交接完成、事项归属与关闭情况都是最新值。
	newB, err := f.svc.ShiftReport(b.ID)
	if err != nil {
		t.Fatalf("report b after processing: %v", err)
	}
	if len(newB.Incoming) != 1 || !newB.Incoming[0].Completed() || newB.Incoming[0].CompletedAt == nil {
		t.Fatalf("新查询应反映交接已完成：%+v", newB.Incoming)
	}
	ne := findEntryOf(t, newB.Incoming[0], idA)
	if ne.Status != EntryConfirmed || ne.Operator != "李四" || ne.ProcessedAt == nil {
		t.Fatalf("新查询应反映确认结果、处理人与处理时间：%+v", ne)
	}
	te := findEntryOf(t, newB.Incoming[0], idB)
	if te.Status != EntryTracking || te.TrackingNote != "继续盯压力" || te.FollowOwner != "王五" {
		t.Fatalf("新查询应反映继续跟踪结果：%+v", te)
	}
	var newItem *Item
	for i := range newB.Items {
		if newB.Items[i].ID == idA {
			newItem = &newB.Items[i]
		}
	}
	if newItem == nil || !newItem.Closed || newItem.FollowOwner != "新负责人" || newItem.CurrentShiftID != b.ID {
		t.Fatalf("新查询应反映事项已归属接班班次并关闭：%+v", newItem)
	}

	// 先前报告仍保留查询时的交接进度：待处理、无处理人与处理时间、无完成时间。
	oa := findEntryOf(t, *oldA.Outgoing, idA)
	if oa.Status != EntryPending || oa.Operator != "" || oa.ProcessedAt != nil {
		t.Fatalf("旧报告的交接进度不应混入后来的接收：%+v", oa)
	}
	if oldA.Outgoing.CompletedAt != nil {
		t.Fatalf("旧报告的交接完成时间应仍为未记录，got %v", oldA.Outgoing.CompletedAt)
	}
	// 先前报告仍保留查询时的事项归属、负责人与关闭情况。
	latest := oldA.LatestItems[idA]
	if latest.CurrentShiftID != a.ID || latest.FollowOwner != "李四" || latest.Closed || latest.ClosedAt != nil {
		t.Fatalf("旧报告应保留查询时的事项归属、负责人与关闭情况：%+v", latest)
	}
	// 先前报告保留查询时的退回与补充历史，不混入后来新增的继续跟踪经过。
	ob := findEntryOf(t, oldB.Incoming[0], idB)
	if ob.Status != EntryPending || ob.ProcessedAt != nil || ob.TrackingNote != "" {
		t.Fatalf("旧报告不应混入后来新增的处理经过：%+v", ob)
	}
	if len(ob.Rounds) != 1 || ob.Rounds[0].Reason != "需要补充细节" ||
		ob.Rounds[0].Supplement != "补充说明如下" || ob.Rounds[0].ResubmittedAt == nil {
		t.Fatalf("旧报告应原样保留查询时的退回与补充历史：%+v", ob.Rounds)
	}
	// 旧报告取得时尚未接收任何事项，清单应保持查询时的样子。
	if len(oldB.Items) != 0 {
		t.Fatalf("旧报告的事项清单应保持查询时为空，got %d 项", len(oldB.Items))
	}
}

// TestEmptyAndLegacyReportsAlsoIndependent：没有事项或关联交接的班次、空清单
// 结束的班次与缺少结束时记录的旧数据班次都能正常取得报告，同样遵守独立性
// 约定；空清单与缺少历史记录保持区别，旧数据的当前值不能宣称为结束时事实。
func TestEmptyAndLegacyReportsAlsoIndependent(t *testing.T) {
	f := newFixture(t)
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 没有事项、没有关联交接的进行中班次也能正常取得报告。
	e := mustShift(t, f, "调度", "孙八", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	rep, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("空班次也应能取得报告：%v", err)
	}
	if len(rep.Items) != 0 || rep.Outgoing != nil || len(rep.Incoming) != 0 || len(rep.Results) != 0 {
		t.Fatalf("空班次报告应为空清单：%+v", rep)
	}
	// 向报告中塞入假事项、假结果、假交接并改班次摘要，不影响再次查询。
	rep.Items = append(rep.Items, Item{ID: "I999", Content: "篡改事项"})
	rep.Results["I999"] = []EntryView{{HandoverID: "H999"}}
	rep.Outgoing = &Handover{ID: "H999"}
	rep.Shift.Owner = "篡改负责人"
	again, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("report again: %v", err)
	}
	if len(again.Items) != 0 || again.Outgoing != nil || len(again.Incoming) != 0 ||
		len(again.Results) != 0 || again.Shift.Owner != "孙八" {
		t.Fatalf("改动空班次的报告不应影响再次查询：%+v", again)
	}

	// 空清单结束：留下空的结束时记录，与缺少历史记录相区别。
	if _, err := f.svc.CloseShift(e.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("report closed: %v", err)
	}
	if !closed.ItemsAtClose || closed.HistoryIncomplete || len(closed.CloseItems) != 0 {
		t.Fatalf("空清单结束应留下空的结束时记录，且不是历史不完整：%+v", closed)
	}
	closed.CloseItems = append(closed.CloseItems, CloseItemSnapshot{ItemID: "I999", Content: "篡改事项"})
	closed.Shift.CloseRecord.Items = append(closed.Shift.CloseRecord.Items, CloseItemSnapshot{ItemID: "I999"})
	closedAgain, err := f.svc.ShiftReport(e.ID)
	if err != nil {
		t.Fatalf("report closed again: %v", err)
	}
	if !closedAgain.ItemsAtClose || closedAgain.HistoryIncomplete || len(closedAgain.CloseItems) != 0 {
		t.Fatalf("改动报告不应影响空清单结束时记录：%+v", closedAgain)
	}

	// 旧数据：已结束但缺少结束时记录的班次。
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	closedAt := tsDay(2, 16, 0)
	createdAt := tsDay(2, 8, 0)
	data := Data{
		ShiftSeq: 1,
		ItemSeq:  1,
		Shifts: []Shift{
			{ID: "S001", Position: "调度", Owner: "张三", Start: tsDay(2, 8, 0), End: tsDay(2, 16, 0),
				CreatedAt: createdAt, Closed: true, ClosedAt: &closedAt},
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

	legacy, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("legacy report: %v", err)
	}
	if !legacy.HistoryIncomplete || legacy.ItemsAtClose || len(legacy.CloseItems) != 0 {
		t.Fatalf("旧数据应明确历史记录不完整，当前值不能宣称为结束时事实：%+v", legacy)
	}
	if len(legacy.Items) != 1 || legacy.Items[0].Content != "旧内容" {
		t.Fatalf("旧数据应展示当前事项信息：%+v", legacy.Items)
	}
	// 改动旧数据报告中的当前事项（内容、关闭信息、流经班次），再次查询仍得
	// 原来的当前信息，且仍标明历史不完整、不变成结束时事实。
	legacy.Items[0].Content = "篡改旧内容"
	legacy.Items[0].Closed = true
	legacy.Items[0].ClosedAt = &forge
	legacy.Items[0].CloseOperator = "篡改人"
	legacy.Items[0].ShiftIDs = append(legacy.Items[0].ShiftIDs, "S999")
	legacyAgain, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("legacy report again: %v", err)
	}
	if !legacyAgain.HistoryIncomplete || legacyAgain.ItemsAtClose || len(legacyAgain.CloseItems) != 0 {
		t.Fatalf("改动报告不得把当前值变成结束时事实：%+v", legacyAgain)
	}
	if len(legacyAgain.Items) != 1 || legacyAgain.Items[0].Content != "旧内容" ||
		legacyAgain.Items[0].Closed || len(legacyAgain.Items[0].ShiftIDs) != 1 {
		t.Fatalf("改动旧数据报告不应影响再次查询的当前信息：%+v", legacyAgain.Items)
	}
}
