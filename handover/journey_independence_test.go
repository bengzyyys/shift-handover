package handover

import (
	"testing"
	"time"
)

// 本文件的测试把“凭事项编号查询得到的处理经过是查询当时的独立副本”这一约定
// 变成可重复执行的检查，与班次报告的独立性约定（report_independence_test.go）
// 一致：调用方可以暂存结果、改动结果用于自己的展示，但改动不得影响系统保存的
// 记录、不得影响同一事项先后取得的其他结果；系统随后的正常处理（补充后重新
// 提交、接收、修改、关闭）也不得改写已取得的结果。

// findJourneyEvent 取处理经过中第一条指定类型的事件，不存在返回 nil。
func findJourneyEvent(j ItemJourney, kind string) *JourneyEvent {
	for i := range j.Events {
		if j.Events[i].Kind == kind {
			return &j.Events[i]
		}
	}
	return nil
}

// mustItem 按编号取出系统当前保存的事项记录。
func mustItem(t *testing.T, f *fixture, id string) Item {
	t.Helper()
	it, err := f.svc.GetItem(id)
	if err != nil {
		t.Fatalf("get item %s: %v", id, err)
	}
	return it
}

// TestItemJourneyMutationKeepsStoredFacts：调用方改动结果中的事项最新信息
// （内容、负责人、归属与流经班次、关闭信息、历史事件）、处理经过（退回原因、
// 补充说明与补充时间）和各次交接当前结果（结果、处理人、处理时间、退回轮次）
// 后，再次查询仍得到系统原来保存的事实；先取得的另一份结果也保持原样。
func TestItemJourneyMutationKeepsStoredFacts(t *testing.T) {
	f := newFixture(t)
	_, _, items, h := prepareReportScenario(t, f)
	idA, idB := items[0].ID, items[1].ID

	first, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	second, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey again: %v", err)
	}
	// 取值当时的 JSON 快照：之后改动结果即使经由共享内存写进存储，
	// 快照里仍是原值，能识别出任何方向的串改。
	firstSnapshot := mustJSON(t, first)
	storedHandover := mustJSON(t, mustGetHandover(t, f, h.ID))
	storedItem := mustJSON(t, mustItem(t, f, idB))
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 事项最新信息：内容、负责人、归属与流经班次、关闭信息、历史事件。
	second.Item.Content = "篡改内容"
	second.Item.FollowOwner = "篡改负责人"
	second.Item.CurrentShiftID = "S999"
	second.Item.ShiftIDs = append(second.Item.ShiftIDs, "S999")
	second.Item.Closed = true
	second.Item.ClosedAt = &forge
	second.Item.CloseOperator = "篡改人"
	second.Item.Events = append(second.Item.Events, ItemEvent{At: forge, Kind: "closed", Operator: "篡改人"})

	// 处理经过：退回原因、补充说明、补充时间与重新提交时间。
	ret := findJourneyEvent(second, "return")
	if ret == nil {
		t.Fatalf("前置：处理经过应包含退回记录")
	}
	ret.Reason = "篡改退回原因"
	res := findJourneyEvent(second, "resubmit")
	if res == nil || res.SupplementAt == nil {
		t.Fatalf("前置：处理经过应包含带补充时间的重新提交记录")
	}
	res.Supplement = "篡改补充说明"
	*res.SupplementAt = forge

	// 交接当前结果：结果、处理人、处理时间与退回轮次。
	if len(second.Results) != 1 {
		t.Fatalf("前置：%s 应恰有一次交接当前结果，got %d", idB, len(second.Results))
	}
	entry := &second.Results[0].Entry
	entry.Status = EntryConfirmed
	entry.Operator = "篡改人"
	entry.ProcessedAt = &forge
	entry.Rounds[0].Reason = "篡改结果里的原因"
	entry.Rounds[0].Supplement = "篡改结果里的补充"
	*entry.Rounds[0].SupplementAt = forge
	*entry.Rounds[0].ResubmittedAt = forge

	// 已确认事项的处理时间指针同样与存储脱离。
	ja, err := f.svc.ItemJourney(idA)
	if err != nil {
		t.Fatalf("journey %s: %v", idA, err)
	}
	ea := &ja.Results[0].Entry
	if ea.ProcessedAt == nil {
		t.Fatalf("前置：%s 应已确认并记录处理时间", idA)
	}
	*ea.ProcessedAt = forge

	// 再次查询仍应得到系统原来保存的事实，且与先取得的那份结果完全一致。
	third, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey after mutation: %v", err)
	}
	if got := mustJSON(t, third); got != firstSnapshot {
		t.Fatalf("改动结果后再次查询仍应得到系统原来保存的事实\nwant %s\ngot  %s", firstSnapshot, got)
	}
	// 系统保存的交接记录与事项记录整体保持原值。
	if got := mustJSON(t, mustGetHandover(t, f, h.ID)); got != storedHandover {
		t.Fatalf("系统保存的交接记录不应被查询结果改动\nwant %s\ngot  %s", storedHandover, got)
	}
	if got := mustJSON(t, mustItem(t, f, idB)); got != storedItem {
		t.Fatalf("系统保存的事项记录不应被查询结果改动\nwant %s\ngot  %s", storedItem, got)
	}
	stored := mustGetHandover(t, f, h.ID)
	se := findEntryOf(t, stored, idB)
	if se.Rounds[0].Reason != "需要补充细节" || se.Rounds[0].Supplement != "补充说明如下" {
		t.Fatalf("系统保存的退回与补充记录不应被查询结果改动：%+v", se.Rounds[0])
	}
}

// TestJourneyEventsAndResultsDoNotShareStorage：同一份结果里，处理经过与交接
// 当前结果可能展示同一次补充；两者各是一份副本，仅调整其中一处的补充时间，
// 另一处保留原值。
func TestJourneyEventsAndResultsDoNotShareStorage(t *testing.T) {
	f := newFixture(t)
	_, _, items, _ := prepareReportScenario(t, f)
	idB := items[1].ID
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	forge2 := time.Date(2032, 6, 7, 8, 9, 10, 0, time.UTC)

	j, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	res := findJourneyEvent(j, "resubmit")
	if res == nil || res.SupplementAt == nil {
		t.Fatalf("前置：处理经过应包含带补充时间的重新提交记录")
	}
	round := &j.Results[0].Entry.Rounds[0]
	if round.SupplementAt == nil || round.ResubmittedAt == nil {
		t.Fatalf("前置：当前结果的退回轮次应记录补充时间与重新提交时间")
	}
	origEvent, origResult := *res.SupplementAt, *round.SupplementAt
	if !origEvent.Equal(origResult) {
		t.Fatalf("前置：两处展示同一次补充，时间应一致：%v vs %v", origEvent, origResult)
	}

	// 改当前结果里的补充时间，处理经过中的同一补充保留原值。
	*round.SupplementAt = forge
	if !res.SupplementAt.Equal(origEvent) {
		t.Fatalf("改动当前结果的补充时间不应连带改掉处理经过：%v", res.SupplementAt)
	}
	// 反向：改处理经过里的补充时间与重新提交时间，当前结果保留刚改的值。
	*res.SupplementAt = forge2
	res.At = forge2
	if !round.SupplementAt.Equal(forge) {
		t.Fatalf("改动处理经过的补充时间不应连带改掉当前结果：%v", round.SupplementAt)
	}
	if !round.ResubmittedAt.Equal(origResult) {
		t.Fatalf("改动处理经过不应连带改掉当前结果的重新提交时间：%v", round.ResubmittedAt)
	}
}

// TestEarlierJourneySurvivesLaterProcessing：事项被退回后先取得处理经过，交班人
// 随后在原交接中补充说明并重新提交、接班人再接收，之后事项被修改并关闭：新查询
// 反映这些成功操作，先前那份结果仍保留取得时的退回状态、退回原因、未补充情况、
// 负责人、所在班次与流经班次，不混入后来的经过。
func TestEarlierJourneySurvivesLaterProcessing(t *testing.T) {
	f := newFixture(t)
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	idB := items[1].ID
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionReturn, "李四", "需要补充细节", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}

	old, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if old.Results[0].Entry.Status != EntryReturned || len(old.Results[0].Entry.Rounds) != 1 {
		t.Fatalf("前置：取得时应为退回且只有1轮：%+v", old.Results[0].Entry)
	}

	// 交班人补充并重新提交，接班人确认接收，随后修改并关闭事项。
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充说明如下"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.UpdateItem(idB, "接班后修改的内容", SeverityUrgent, "新限制", "新负责人"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem(idB, "李四"); err != nil {
		t.Fatalf("close item: %v", err)
	}

	// 新查询反映这些成功操作。
	fresh, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey after processing: %v", err)
	}
	if fresh.Results[0].Entry.Status != EntryConfirmed || !fresh.Item.Closed ||
		fresh.Item.FollowOwner != "新负责人" || findJourneyEvent(fresh, "resubmit") == nil {
		t.Fatalf("新查询应反映补充、接收、修改与关闭：%+v", fresh.Results[0].Entry)
	}

	// 先前那份结果仍保留取得时的退回状态、退回原因与未补充情况。
	oe := old.Results[0].Entry
	if oe.Status != EntryReturned || oe.Operator != "李四" || oe.ProcessedAt == nil {
		t.Fatalf("旧结果应保留取得时的退回状态与处理人：%+v", oe)
	}
	if oe.Rounds[0].Reason != "需要补充细节" || oe.Rounds[0].Supplement != "" ||
		oe.Rounds[0].SupplementAt != nil || oe.Rounds[0].ResubmittedAt != nil {
		t.Fatalf("旧结果应保留取得时的退回原因与未补充情况：%+v", oe.Rounds[0])
	}
	// 旧结果中的事项最新信息保持取得时的内容：未关闭、负责人与所在班次不变。
	if old.Item.Closed || old.Item.ClosedAt != nil || old.Item.FollowOwner != "李四" ||
		old.Item.CurrentShiftID != a.ID || len(old.Item.ShiftIDs) != 1 {
		t.Fatalf("旧结果应保留取得时的事项归属、负责人与关闭情况：%+v", old.Item)
	}
	// 后来的补充、接收、修改与关闭经过不混入旧结果。
	for _, kind := range []string{"resubmit", "confirm", "received", "closed"} {
		if ev := findJourneyEvent(old, kind); ev != nil {
			t.Fatalf("旧结果不应混入后来的 %s 经过：%+v", kind, ev)
		}
	}
	if ev := findJourneyEvent(old, "updated"); ev != nil {
		t.Fatalf("旧结果不应混入后来的修改经过：%+v", ev)
	}
}

// TestJourneyWithoutHandoverAlsoIndependent：未参与交接的事项仍显示暂无交接
// 记录；改动其结果中的事项信息不影响再次查询，也不为独立性补造交接记录。
func TestJourneyWithoutHandoverAlsoIndependent(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	it, err := f.svc.AddItem(a.ID, "未交接事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	j, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if j.HasHandovers || len(j.Results) != 0 {
		t.Fatalf("未参与交接应显示暂无交接记录：%+v", j.Results)
	}
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	j.Item.Content = "篡改内容"
	j.Item.Events = append(j.Item.Events, ItemEvent{At: forge, Kind: "closed", Operator: "篡改人"})
	j.Item.ShiftIDs = append(j.Item.ShiftIDs, "S999")
	again, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey again: %v", err)
	}
	if again.HasHandovers || again.Item.Content != "未交接事项" ||
		len(again.Item.Events) != 1 || len(again.Item.ShiftIDs) != 1 {
		t.Fatalf("改动结果不应影响再次查询：%+v", again.Item)
	}
}
