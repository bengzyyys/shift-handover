package handover

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件的测试把“按事项编号查询返回的处理经过是查询当时的独立副本”这一约定
// 变成可重复执行的检查，与按班次查询报告的独立性（report_independence_test.go）
// 对齐：调用方可以暂存处理经过、改动其中的事项最新信息、时间线、退回原因、
// 补充内容或已记录的时间用于自己的展示，但这些改动只属于这一份结果，不能影响
// 系统保存的原记录，不能影响先后取得的其他结果，也不能让后来的补充、接收、
// 修改或关闭混入先前取得的结果；系统的正常处理始终依据真实状态进行。

// findJourneyEvent 按类型（及退回轮次）找出时间线中的一条经过。
func findJourneyEvent(t *testing.T, j ItemJourney, kind string, roundSeq int) *JourneyEvent {
	t.Helper()
	for i := range j.Events {
		if j.Events[i].Kind == kind && (roundSeq == 0 || j.Events[i].RoundSeq == roundSeq) {
			return &j.Events[i]
		}
	}
	t.Fatalf("处理经过中缺少 %s（轮次 %d）：%v", kind, roundSeq, journeyKinds(j))
	return nil
}

// prepareReturnedScenario 构建“事项已退回、尚未补充”的场景：a 已结束
// （I001、I002 未关闭，I003 结束前已关闭），h 为 a→b 的交接，I002 刚被
// 退回一轮，等待交班人补充。
func prepareReturnedScenario(t *testing.T, f *fixture) (Shift, Shift, []Item, Handover) {
	t.Helper()
	a, b, items := prepareHandover(t, f)
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	if _, err := f.svc.ProcessEntry(h.ID, items[1].ID, ActionReturn, "李四", "需要补充细节", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	return a, b, items, h
}

// TestItemJourneyMutationKeepsStoredFacts：调用方改动一份处理经过中的事项最新
// 信息（内容、负责人、归属、流经班次、关闭情况与关闭时间、历史事件）、时间线
// （退回原因、补充内容、补充时间）与各次交接当前结果（结果、处理人、处理时间、
// 退回原因、补充与重新提交时间）后，再次查询仍得到系统原来保存的事实；先取得
// 的另一份结果也保持原样；系统后续正常处理仍依据真实状态。
func TestItemJourneyMutationKeepsStoredFacts(t *testing.T) {
	f := newFixture(t)
	a, b, items, h := prepareReportScenario(t, f)
	idB := items[1].ID

	first, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	second, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey again: %v", err)
	}
	// 取值当时的 JSON 快照：之后改动结果即使经由共享内存写进存储，快照里仍是
	// 原值，能识别出任何方向的串改。
	firstSnapshot := mustJSON(t, first)
	storedHandoverSnapshot := mustJSON(t, mustGetHandover(t, f, h.ID))
	storedItem, err := f.svc.GetItem(idB)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	storedItemSnapshot := mustJSON(t, storedItem)
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 事项最新信息：内容、负责人、归属、流经班次、关闭情况与历史。
	second.Item.Content = "篡改最新内容"
	second.Item.FollowOwner = "篡改负责人"
	second.Item.CurrentShiftID = "S999"
	second.Item.ShiftIDs[0] = "S998"
	second.Item.ShiftIDs = append(second.Item.ShiftIDs, "S999")
	second.Item.Closed = true
	second.Item.ClosedAt = &forge
	second.Item.CloseOperator = "篡改人"
	second.Item.Events[0].Detail = "篡改事项历史"
	second.Item.Events = append(second.Item.Events, ItemEvent{At: forge, Kind: "closed", Operator: "篡改人"})

	// 时间线：退回原因、补充内容、补充人与补充时间。
	ret := findJourneyEvent(t, second, "return", 1)
	ret.Reason = "篡改时间线退回原因"
	ret.Operator = "篡改人"
	resub := findJourneyEvent(t, second, "resubmit", 1)
	resub.Supplement = "篡改时间线补充"
	resub.SupplementOperator = "篡改人"
	*resub.SupplementAt = forge
	second.Events = append(second.Events, JourneyEvent{At: forge, TimeKnown: true, Kind: "closed"})

	// 各次交接当前结果：结果、处理人、处理时间、退回原因、补充与重新提交时间。
	re := &second.Results[0].Entry
	if re.Status != EntryPending {
		t.Fatalf("前置：%s 补充重新提交后应为待处理，got %s", idB, re.Status)
	}
	re.Status = EntryTracking
	re.Operator = "篡改人"
	re.ProcessedAt = &forge
	re.TrackingNote = "篡改跟踪说明"
	re.FollowOwner = "篡改负责人"
	re.Rounds[0].Reason = "篡改结果退回原因"
	re.Rounds[0].Supplement = "篡改结果补充"
	re.Rounds[0].ReturnOperator = "篡改人"
	re.Rounds[0].SupplementOperator = "篡改人"
	*re.Rounds[0].SupplementAt = forge
	*re.Rounds[0].ResubmittedAt = forge
	second.Results[0].FromShift = "S998"

	// 再次查询仍应得到系统原来保存的事实，且与先取得的那份结果完全一致
	//（既验证存储未被改动，也验证先后取得的两份结果互不影响）。
	third, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey after mutation: %v", err)
	}
	if got := mustJSON(t, third); got != firstSnapshot {
		t.Fatalf("改动查询结果后再次查询仍应得到系统原来保存的事实\nwant %s\ngot  %s", firstSnapshot, got)
	}

	// 系统保存的交接记录与事项记录整体保持原值。
	if got := mustJSON(t, mustGetHandover(t, f, h.ID)); got != storedHandoverSnapshot {
		t.Fatalf("系统保存的交接记录不应被查询结果改动\nwant %s\ngot  %s", storedHandoverSnapshot, got)
	}
	gotItem, err := f.svc.GetItem(idB)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got := mustJSON(t, gotItem); got != storedItemSnapshot {
		t.Fatalf("系统保存的事项记录不应被查询结果改动\nwant %s\ngot  %s", storedItemSnapshot, got)
	}
	// 直接核对系统保存的关键字段：仍是待处理、无接班处理人，退回与补充是原值。
	stored := mustGetHandover(t, f, h.ID)
	se := findEntryOf(t, stored, idB)
	if se.Status != EntryPending || se.Operator != "" || se.ProcessedAt != nil {
		t.Fatalf("系统保存的当前结果不应被查询结果改动：%+v", se)
	}
	if se.Rounds[0].Reason != "需要补充细节" || se.Rounds[0].Supplement != "补充说明如下" ||
		se.Rounds[0].ReturnOperator != "李四" || se.Rounds[0].SupplementOperator != "张三" {
		t.Fatalf("系统保存的退回与补充记录不应被查询结果改动：%+v", se.Rounds[0])
	}
	if gotItem.CurrentShiftID != a.ID || gotItem.FollowOwner != "李四" || gotItem.Closed {
		t.Fatalf("系统保存的事项归属、负责人与关闭情况不应被查询结果改动：%+v", gotItem)
	}

	// 正常处理仍依据真实状态：该项在真实存储中是待处理，接班人可以继续跟踪。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "继续盯压力", "王五"); err != nil {
		t.Fatalf("篡改查询结果后正常处理仍应依据真实状态进行：%v", err)
	}
	if _, err := f.svc.GetItem(idB); err != nil {
		t.Fatalf("get item: %v", err)
	}
	after, _ := f.svc.GetItem(idB)
	if after.CurrentShiftID != b.ID || after.FollowOwner != "王五" {
		t.Fatalf("正常接收应移动事项并更新负责人：%+v", after)
	}
}

// TestItemJourneyTimelineAndResultsDoNotShareStorage：同一份处理经过里，时间线
// 与交接当前结果可能展示同一次补充；它们各是一份副本，仅调整其中一处的补充
// 时间、退回原因或补充内容，另一处应保留原值。
func TestItemJourneyTimelineAndResultsDoNotShareStorage(t *testing.T) {
	f := newFixture(t)
	_, _, items, _ := prepareReportScenario(t, f)
	idB := items[1].ID
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	j, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	round := j.Results[0].Entry.Rounds[0]
	origSupplementAt := *round.SupplementAt
	origResubmittedAt := *round.ResubmittedAt

	// 改时间线中的退回原因、补充内容与补充时间，交接当前结果保留原值。
	ret := findJourneyEvent(t, j, "return", 1)
	ret.Reason = "篡改时间线原因"
	resub := findJourneyEvent(t, j, "resubmit", 1)
	*resub.SupplementAt = forge
	resub.Supplement = "篡改时间线补充"
	re := &j.Results[0].Entry
	if re.Rounds[0].Reason != "需要补充细节" ||
		re.Rounds[0].Supplement != "补充说明如下" ||
		!re.Rounds[0].SupplementAt.Equal(origSupplementAt) ||
		!re.Rounds[0].ResubmittedAt.Equal(origResubmittedAt) {
		t.Fatalf("改动时间线不应连带改掉交接当前结果：%+v", re.Rounds[0])
	}

	// 反向：另取一份新结果，改交接当前结果里的退回原因、补充与补充时间，
	// 同一份结果的时间线保留原值。
	j2, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey j2: %v", err)
	}
	re2 := &j2.Results[0].Entry
	re2.Rounds[0].Reason = "篡改结果原因"
	re2.Rounds[0].Supplement = "篡改结果补充"
	*re2.Rounds[0].SupplementAt = forge
	ret2 := findJourneyEvent(t, j2, "return", 1)
	if ret2.Reason != "需要补充细节" {
		t.Fatalf("改动交接当前结果不应连带改掉时间线退回原因：%+v", ret2)
	}
	resub2 := findJourneyEvent(t, j2, "resubmit", 1)
	if resub2.Supplement != "补充说明如下" || !resub2.SupplementAt.Equal(origSupplementAt) {
		t.Fatalf("改动交接当前结果不应连带改掉时间线补充记录：%+v", resub2)
	}

	// 改交接当前结果的处理时间指针，不影响时间线中的其他记录与再次查询。
	re2.Status = EntryConfirmed
	re2.ProcessedAt = &forge
	again, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("journey again: %v", err)
	}
	ae := &again.Results[0].Entry
	if ae.Status != EntryPending || ae.ProcessedAt != nil {
		t.Fatalf("改动一份结果不应影响再次查询的当前结果：%+v", ae)
	}
}

// TestEarlierJourneySurvivesLaterSupplementAndProcessing：事项被退回后先取得
// 处理经过，交班人随后在原交接中补充说明并重新提交，接班人再接收、修改并关闭：
// 先前那份结果继续保留当时的退回状态、退回原因和未补充情况，时间线也不混入
// 后来的补充与接收；再次查询才显示本轮补充、待处理以及之后的接收与关闭。
func TestEarlierJourneySurvivesLaterSupplementAndProcessing(t *testing.T) {
	f := newFixture(t)
	a, b, items, h := prepareReturnedScenario(t, f)
	idB := items[1].ID

	old, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("old journey: %v", err)
	}
	// 旧结果取得时：仍是退回状态，没有补充，时间线止于退回。
	oe := &old.Results[0].Entry
	if oe.Status != EntryReturned || oe.Operator != "李四" || oe.ProcessedAt == nil {
		t.Fatalf("旧结果应保留当时的退回状态与处理信息：%+v", oe)
	}
	if len(oe.Rounds) != 1 || oe.Rounds[0].Reason != "需要补充细节" || oe.Rounds[0].Supplement != "" ||
		oe.Rounds[0].SupplementAt != nil || oe.Rounds[0].ResubmittedAt != nil {
		t.Fatalf("旧结果应保留未补充情况：%+v", oe.Rounds)
	}
	if got := journeyKinds(old); joinStrings(got) != "created,handover-init,return" {
		t.Fatalf("旧结果时间线应止于退回：%v", got)
	}
	if old.Item.CurrentShiftID != a.ID || old.Item.FollowOwner != "李四" ||
		old.Item.Closed || old.Item.ClosedAt != nil || len(old.Item.ShiftIDs) != 1 {
		t.Fatalf("旧结果应保留当时的事项归属、负责人与关闭情况：%+v", old.Item)
	}

	// 交班人补充并重新提交：新查询显示本轮补充与待处理。
	if _, err := f.svc.ResubmitReturned(h.ID, idB, "张三", "补充说明如下"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	mid, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("mid journey: %v", err)
	}
	me := &mid.Results[0].Entry
	if me.Status != EntryPending || me.Operator != "" || me.ProcessedAt != nil {
		t.Fatalf("重新提交后新查询应显示待处理、接班人尚未处理：%+v", me)
	}
	if me.Rounds[0].Supplement != "补充说明如下" || me.Rounds[0].ResubmittedAt == nil {
		t.Fatalf("新查询应显示本轮补充与重新提交：%+v", me.Rounds[0])
	}
	if got := journeyKinds(mid); joinStrings(got) != "created,handover-init,return,resubmit" {
		t.Fatalf("新查询时间线应包含重新提交：%v", got)
	}
	// 先前那份结果原样保留退回状态，不混入本轮补充。
	if old.Results[0].Entry.Status != EntryReturned ||
		old.Results[0].Entry.Rounds[0].Supplement != "" {
		t.Fatalf("旧结果不应混入后来的补充：%+v", old.Results[0].Entry)
	}
	if got := journeyKinds(old); joinStrings(got) != "created,handover-init,return" {
		t.Fatalf("旧结果时间线不应混入后来的经过：%v", got)
	}

	// 接班人正常接收，随后修改并关闭事项。
	if _, err := f.svc.ProcessEntry(h.ID, idB, ActionTrack, "李四", "", "继续盯压力", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.UpdateItem(idB, "接班后修改的内容", SeverityUrgent, "新限制", "新负责人"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.svc.CloseItem(idB, "李四"); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 新查询反映接收、归属变化、修改与关闭。
	fresh, err := f.svc.ItemJourney(idB)
	if err != nil {
		t.Fatalf("fresh journey: %v", err)
	}
	fe := &fresh.Results[0].Entry
	if fe.Status != EntryTracking || fe.Operator != "李四" || fe.ProcessedAt == nil ||
		fe.TrackingNote != "继续盯压力" || fe.FollowOwner != "王五" {
		t.Fatalf("新查询应反映继续跟踪结果：%+v", fe)
	}
	if !fresh.Item.Closed || fresh.Item.ClosedAt == nil || fresh.Item.CloseOperator != "李四" ||
		fresh.Item.CurrentShiftID != b.ID || fresh.Item.FollowOwner != "新负责人" ||
		len(fresh.Item.ShiftIDs) != 2 {
		t.Fatalf("新查询应反映事项归属、负责人与关闭情况：%+v", fresh.Item)
	}
	if got := journeyKinds(fresh); joinStrings(got) != "created,handover-init,return,resubmit,track,updated,closed" {
		t.Fatalf("新查询时间线应包含接收、修改与关闭：%v", got)
	}

	// 旧结果仍是取得时的事实：退回中、未补充、事项在交班班次、未关闭。
	oldEntry := old.Results[0].Entry
	if oldEntry.Status != EntryReturned || oldEntry.Rounds[0].Supplement != "" ||
		oldEntry.Rounds[0].ResubmittedAt != nil {
		t.Fatalf("旧结果应始终保留取得时的退回与未补充事实：%+v", oldEntry)
	}
	if old.Item.CurrentShiftID != a.ID || old.Item.FollowOwner != "李四" ||
		old.Item.Closed || old.Item.ClosedAt != nil {
		t.Fatalf("旧结果应始终保留取得时的事项归属与关闭情况：%+v", old.Item)
	}
	if got := journeyKinds(old); joinStrings(got) != "created,handover-init,return" {
		t.Fatalf("旧结果时间线不应拼接不同时间的事实：%v", got)
	}
}

// TestItemJourneyEmptyLegacyAndNotFoundAlsoIndependent：未参与交接的事项、缺少
// 退回轮次的旧数据与不存在的事项编号都遵守独立性约定；旧数据缺姓名、缺时间、
// 缺退回轮次时继续如实标明缺失，改动结果不为存储补造记录。
func TestItemJourneyEmptyLegacyAndNotFoundAlsoIndependent(t *testing.T) {
	f := newFixture(t)
	forge := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	// 未参与交接的事项显示暂无交接记录；改动结果不影响再次查询。
	a := mustShift(t, f, "调度", "张三", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	it, err := f.svc.AddItem(a.ID, "独立事项", SeverityNormal, "", "李四")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	j, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if j.HasHandovers || len(j.Results) != 0 {
		t.Fatalf("未参与交接应显示暂无交接记录：%+v", j)
	}
	if !strings.Contains(FormatItemJourney(j), "暂无交接记录") {
		t.Fatalf("展示应包含暂无交接记录：\n%s", FormatItemJourney(j))
	}
	j.Item.Content = "篡改内容"
	j.Item.ShiftIDs = append(j.Item.ShiftIDs, "S999")
	j.Events = append(j.Events, JourneyEvent{At: forge, TimeKnown: true, Kind: "return"})
	j.Results = append(j.Results, EntryView{HandoverID: "H999"})
	j.HasHandovers = true
	again, err := f.svc.ItemJourney(it.ID)
	if err != nil {
		t.Fatalf("journey again: %v", err)
	}
	if again.HasHandovers || len(again.Results) != 0 ||
		again.Item.Content != "独立事项" || len(again.Item.ShiftIDs) != 1 ||
		len(again.Events) != 1 {
		t.Fatalf("改动未交接事项的结果不应影响再次查询：%+v", again)
	}

	// 旧数据：当前结果是退回却没有退回轮次记录。改动这份结果不影响再次查询，
	// 再次查询仍如实标明退回历史不完整、原因未记录。
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	raw := `{
  "shift_seq": 2, "item_seq": 1, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"旧事项","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00",
     "entries":[
       {"item_id":"I001","content":"旧事项","severity":"normal","follow_owner":"李四",
        "status":"returned","operator":"李四","processed_at":"2026-10-02T11:00:00+08:00"}
     ]}
  ],
  "notes": []
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	svc := NewService(store)
	legacy, err := svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("legacy journey: %v", err)
	}
	inc := findJourneyEvent(t, legacy, "return-incomplete", 0)
	if !inc.HistoryIncomplete || inc.Operator != "李四" {
		t.Fatalf("旧数据应呈现已发生的退回并标明历史不完整：%+v", inc)
	}
	if legacy.Results[0].Entry.Status != EntryReturned || len(legacy.Results[0].Entry.Rounds) != 0 {
		t.Fatalf("旧数据当前结果为退回且无退回轮次：%+v", legacy.Results[0].Entry)
	}
	if !strings.Contains(FormatItemJourney(legacy), "退回历史不完整") {
		t.Fatalf("展示应标明退回历史不完整：\n%s", FormatItemJourney(legacy))
	}
	inc.HistoryIncomplete = false
	inc.Operator = "篡改人"
	legacy.Results[0].Entry.Status = EntryConfirmed
	legacy.Results[0].Entry.Rounds = []ReturnRound{{Seq: 1, Reason: "编造的退回原因"}}
	legacy.Item.Content = "篡改旧内容"
	legacyAgain, err := svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("legacy journey again: %v", err)
	}
	incAgain := findJourneyEvent(t, legacyAgain, "return-incomplete", 0)
	if !incAgain.HistoryIncomplete || incAgain.Operator != "李四" {
		t.Fatalf("改动结果不得替旧记录补造轮次或修改处理人：%+v", incAgain)
	}
	if legacyAgain.Results[0].Entry.Status != EntryReturned ||
		len(legacyAgain.Results[0].Entry.Rounds) != 0 ||
		legacyAgain.Item.Content != "旧事项" {
		t.Fatalf("改动旧数据结果不应影响再次查询：%+v", legacyAgain)
	}
	if !strings.Contains(FormatItemJourney(legacyAgain), "退回历史不完整") {
		t.Fatalf("再次查询仍应标明退回历史不完整：\n%s", FormatItemJourney(legacyAgain))
	}

	// 不存在的事项编号仍明确报错，不受任何结果改动影响。
	if _, err := svc.ItemJourney("I999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的事项编号应明确报错，got %v", err)
	}
}
