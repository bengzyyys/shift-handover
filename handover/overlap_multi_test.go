package handover

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件为“一次建立的新班次同时与同岗位多个已有班次重叠”补充回归保障：
// 重叠说明必须分别关联新班次与每一个实际重叠的已有班次，不能只记最早找到
// 的一班，也不能把仅端点相接或不同岗位的无关班次带进来；重叠一律按实际时刻
// 判断；缺少说明时整笔拒绝、不留部分数据且不跳号。

// tsZone 在指定 UTC 偏移的时区构造 2026-10-某日的时刻。
func tsZone(day, hour, min, offsetSec int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, time.FixedZone("Z", offsetSec))
}

// otherShiftID 返回重叠说明中相对 self 的对端班次编号；self 不参与该说明时返回空串。
func otherShiftID(n OverlapNote, self string) string {
	switch self {
	case n.ShiftA:
		return n.ShiftB
	case n.ShiftB:
		return n.ShiftA
	}
	return ""
}

// pairKey 返回规范化后的班次对键，便于精确比较说明关联了哪两班。
func pairKey(a, b string) string {
	x, y := notePair(a, b)
	return x + "|" + y
}

// notePeers 收集涉及 self 的全部说明，以对端班次编号为索引。
func notePeers(f *fixture, self string) map[string]OverlapNote {
	out := map[string]OverlapNote{}
	for _, n := range f.svc.OverlapNotes(self) {
		out[otherShiftID(n, self)] = n
	}
	return out
}

// TestCreateShiftSpanningTwoShiftsNotesEachOverlap：同一岗位相邻两班
// 08:00-12:00、12:00-16:00，新班次 10:00-14:00 同时与两班重叠。填写有效说明
// 后新班次成功建立并获得稳定编号；两条说明各自关联新班次与一个实际重叠班次，
// 内容沿用用户输入；既有两班仅端点相接，不应出现第三条关系。
func TestCreateShiftSpanningTwoShiftsNotesEachOverlap(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")

	const noteText = "交接班高峰，跨两班并行"
	span, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), noteText)
	if err != nil {
		t.Fatalf("填写有效说明后新班次应成功建立：%v", err)
	}
	if span.ID != "S003" {
		t.Fatalf("新班次应获得稳定编号 S003，got %s", span.ID)
	}

	// 全部说明恰好两条，每条都沿用用户输入、归属同岗位，并取得稳定编号。
	all := append([]OverlapNote(nil), f.store.data.Notes...)
	if len(all) != 2 {
		t.Fatalf("应为每个实际重叠班次各保存一条说明，期望2条，got %d：%+v", len(all), all)
	}
	wantPairs := map[string]bool{
		pairKey(first.ID, span.ID):  true,
		pairKey(second.ID, span.ID): true,
	}
	gotIDs := map[string]bool{}
	for _, n := range all {
		if n.Note != noteText {
			t.Fatalf("说明内容应沿用用户输入，want %q got %q", noteText, n.Note)
		}
		if n.Position != "调度" {
			t.Fatalf("说明应归属同岗位：%+v", n)
		}
		gotIDs[n.ID] = true
		if !wantPairs[n.ShiftA+"|"+n.ShiftB] {
			t.Fatalf("说明关联了不该有的班次对 %s|%s", n.ShiftA, n.ShiftB)
		}
	}
	if !gotIDs["N001"] || !gotIDs["N002"] {
		t.Fatalf("两条说明应获得稳定编号 N001/N002，got %v", gotIDs)
	}

	// 既有两班只是端点相接（12:00=12:00），不能因新班次跨过它们而出现第三条关系。
	if findNote(&f.store.data, first.ID, second.ID) != nil {
		t.Fatalf("端点相接的两班不应产生重叠说明")
	}

	// 从新班次查看：两条说明都在，对端分别是两班。
	repSpan, err := f.svc.ShiftReport(span.ID)
	if err != nil {
		t.Fatalf("report span: %v", err)
	}
	if len(repSpan.OverlapNotes) != 2 {
		t.Fatalf("从新班次查看应看到两条说明，got %d", len(repSpan.OverlapNotes))
	}
	spanPeers := notePeers(f, span.ID)
	if !spanPeers[first.ID].NoteSet() || spanPeers[first.ID].Note != noteText {
		t.Fatalf("新班次应能看到与 %s 的说明：%+v", first.ID, spanPeers)
	}
	if !spanPeers[second.ID].NoteSet() || spanPeers[second.ID].Note != noteText {
		t.Fatalf("新班次应能看到与 %s 的说明：%+v", second.ID, spanPeers)
	}

	// 从任一已有班次查看：只能看到涉及自己的那一条。
	repFirst, _ := f.svc.ShiftReport(first.ID)
	if len(repFirst.OverlapNotes) != 1 ||
		otherShiftID(repFirst.OverlapNotes[0], first.ID) != span.ID ||
		repFirst.OverlapNotes[0].Note != noteText {
		t.Fatalf("第一班只应看到涉及自己的那一条说明：%+v", repFirst.OverlapNotes)
	}
	repSecond, _ := f.svc.ShiftReport(second.ID)
	if len(repSecond.OverlapNotes) != 1 ||
		otherShiftID(repSecond.OverlapNotes[0], second.ID) != span.ID ||
		repSecond.OverlapNotes[0].Note != noteText {
		t.Fatalf("第二班只应看到涉及自己的那一条说明：%+v", repSecond.OverlapNotes)
	}

	// 退出重开后两条关系与视角仍完整保留。
	f.reopen(t)
	if peers := notePeers(f, span.ID); len(peers) != 2 || !peers[first.ID].NoteSet() || !peers[second.ID].NoteSet() {
		t.Fatalf("重开后新班次仍应看到两条说明：%+v", peers)
	}
	if peers := notePeers(f, first.ID); len(peers) != 1 || peers[span.ID].Note != noteText {
		t.Fatalf("重开后第一班仍只看到自己的一条：%+v", peers)
	}
	if findNote(&f.store.data, first.ID, second.ID) != nil {
		t.Fatalf("重开后仍不应出现端点相接两班的关系")
	}
}

// NoteSet 便于在 map 取值后区分“没有这条说明”。
func (n OverlapNote) NoteSet() bool { return n.ID != "" }

// TestCreateShiftSpanningDoesNotNoteOtherPosition：同一时间段但岗位不同的班次
// 与新班次不构成同岗位重叠，不能收到这段说明。
func TestCreateShiftSpanningDoesNotNoteOtherPosition(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")
	// 与新班次完全同时间段、但岗位不同的班次。
	otherPos := mustShift(t, f, "巡检", "赵六", ts(10, 0), ts(14, 0), "")

	span, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "跨两班说明")
	if err != nil {
		t.Fatalf("建立跨班班次：%v", err)
	}
	if span.ID == otherPos.ID {
		t.Fatalf("不同岗位班次不应占用新班次编号")
	}

	for _, n := range f.store.data.Notes {
		if n.ShiftA == otherPos.ID || n.ShiftB == otherPos.ID {
			t.Fatalf("不同岗位班次 %s 不应收到重叠说明：%+v", otherPos.ID, n)
		}
	}
	if peers := notePeers(f, otherPos.ID); len(peers) != 0 {
		t.Fatalf("不同岗位班次一条说明都不应看到，got %+v", peers)
	}
	// 同岗位两班的关系不受影响。
	if peers := notePeers(f, span.ID); len(peers) != 2 ||
		!peers[first.ID].NoteSet() || !peers[second.ID].NoteSet() {
		t.Fatalf("同岗位两班仍应各有一条说明：%+v", peers)
	}
}

// TestOverlapComparedByInstantAcrossZones：重叠必须按实际时刻判断，而不是比较
// 钟面或时间字符串。新班次的起止采用不同时区偏移，只要换算后是同一段实际时刻，
// 就应得到相同的两条重叠关系。
func TestOverlapComparedByInstantAcrossZones(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", tsDay(5, 8, 0), tsDay(5, 12, 0), "")
	second := mustShift(t, f, "调度", "李四", tsDay(5, 12, 0), tsDay(5, 16, 0), "")

	// 10:00-14:00 +08:00 对应 02:00-06:00 UTC。
	// 起点用 +09:00 的 11:00、终点用 +07:00 的 13:00 表达同一段实际时刻，
	// 两端时区互不相同、且都不同于已有班次的 +08:00。
	cst := 8 * 3600
	start := tsZone(5, 11, 0, 9*3600) // 02:00 UTC = 10:00 +08
	end := tsZone(5, 13, 0, 7*3600)   // 06:00 UTC = 14:00 +08
	wantStart := tsDay(5, 10, 0)
	wantEnd := tsDay(5, 14, 0)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("测试前置时刻换算错误：%v=%v %v=%v", start, wantStart, end, wantEnd)
	}

	span, err := f.svc.CreateShift("调度", "王五", start, end, "跨时区同一时段")
	if err != nil {
		t.Fatalf("换算后与两班重叠、已填说明，应成功：%v", err)
	}
	all := append([]OverlapNote(nil), f.store.data.Notes...)
	if len(all) != 2 {
		t.Fatalf("不同偏移换算后同一时段应得到相同的两条重叠关系，got %d：%+v", len(all), all)
	}
	peers := notePeers(f, span.ID)
	if !peers[first.ID].NoteSet() || !peers[second.ID].NoteSet() {
		t.Fatalf("两条关系应分别关联 %s、%s：%+v", first.ID, second.ID, peers)
	}
	if peers[first.ID].Note != "跨时区同一时段" || peers[second.ID].Note != "跨时区同一时段" {
		t.Fatalf("两条说明都应沿用用户输入：%+v", peers)
	}
	if findNote(&f.store.data, first.ID, second.ID) != nil {
		t.Fatalf("端点相接的两班仍不应产生第三条关系")
	}

	// 钟面相同但实际时刻不同：已有班次为 12:00-16:00 +08（04:00-08:00 UTC），
	// 新班次写成 +09 的钟面 12:00-16:00（03:00-07:00 UTC），实际提前一小时，
	// 与已有班次重叠而不是端点相接；按钟面比较会误判为不重叠。
	f2 := newFixture(t)
	existing := mustShift(t, f2, "调度", "张三",
		tsZone(6, 12, 0, cst), tsZone(6, 16, 0, cst), "")
	_, err = f2.svc.CreateShift("调度", "李四",
		tsZone(6, 12, 0, 9*3600), tsZone(6, 16, 0, 9*3600), "")
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("钟面相同但实际时刻重叠时应按实际时刻判为重叠（ErrOverlap），got %v", err)
	}
	// 错误应指出与之重叠的已有班次，而不是把它当成端点相接放行。
	if !strings.Contains(err.Error(), existing.ID) {
		t.Fatalf("重叠错误应指出冲突班次 %s：%v", existing.ID, err)
	}
}

// TestOverlapByInstantNotClockFace：构造一对“按实际时刻重叠、按钟面却不重叠”
// 的区间，锁死重叠只能按实际时刻判断。已有班次 08:00-12:00 +08（即 UTC
// 00:00-04:00），新班次用 +00:00 表达为 03:00-05:00（即北京时间 11:00-13:00），
// 实际与已有班次重叠于 UTC 03:00-04:00；但只比钟面（03:00-05:00 对
// 08:00-12:00）会误判为不重叠。此时不填说明必须被拒，杜绝按钟面放行。
func TestOverlapByInstantNotClockFace(t *testing.T) {
	f := newFixture(t)
	existing := mustShift(t, f, "调度", "张三", tsDay(8, 8, 0), tsDay(8, 12, 0), "")

	// 新班次：UTC 03:00-05:00，实际与已有班次（UTC 00:00-04:00）重叠。
	start := tsZone(8, 3, 0, 0)
	end := tsZone(8, 5, 0, 0)
	if start.Format("15:04") != "03:00" || end.Format("15:04") != "05:00" {
		t.Fatalf("前置：新班次应以 +00:00 呈现 03:00-05:00 的钟面")
	}

	// 不填说明：按实际时刻重叠，必须拒绝。
	_, err := f.svc.CreateShift("调度", "李四", start, end, "")
	if !errors.Is(err, ErrOverlap) || !strings.Contains(err.Error(), existing.ID) {
		t.Fatalf("实际重叠而钟面不重叠时必须按实际时刻拒绝（ErrOverlap 并指出 %s），got %v",
			existing.ID, err)
	}
	if len(f.store.data.Notes) != 0 {
		t.Fatalf("被拒后不应留下说明：%+v", f.store.data.Notes)
	}

	// 填入有效说明后成功，且只产生与该实际重叠班次的一条关系。
	span, err := f.svc.CreateShift("调度", "李四", start, end, "钟面不同但实际并行")
	if err != nil {
		t.Fatalf("实际重叠已填说明应成功：%v", err)
	}
	peers := notePeers(f, span.ID)
	if len(peers) != 1 || peers[existing.ID].Note != "钟面不同但实际并行" {
		t.Fatalf("应只关联实际重叠的班次 %s：%+v", existing.ID, peers)
	}
}

// TestOverlapInstantEndpointTouchNeedsNoNote：新班次起点按实际时刻恰好等于
// 已有班次终点（钟面不同），且未与其他同岗位班次重叠时，允许不填说明建立，
// 且不产生该对班次的说明。
func TestOverlapInstantEndpointTouchNeedsNoNote(t *testing.T) {
	f := newFixture(t)
	// 已有班次 08:00-12:00 +08（00:00-04:00 UTC）。
	first := mustShift(t, f, "调度", "张三", tsDay(7, 8, 0), tsDay(7, 12, 0), "")

	// 新班次起点 13:00 +09（04:00 UTC）按实际时刻恰好等于已有终点 12:00 +08。
	touchStart := tsZone(7, 13, 0, 9*3600)
	touchEnd := tsZone(7, 17, 0, 9*3600) // 08:00 UTC
	if !touchStart.Equal(first.End) {
		t.Fatalf("前置：新班次起点实际时刻应等于已有班次终点 %v，got %v", first.End, touchStart)
	}
	if touchStart.Format("15:04") == first.End.Format("15:04") {
		t.Fatalf("前置：两时刻钟面应不同，以证明按实际时刻而非钟面判断")
	}

	next, err := f.svc.CreateShift("调度", "李四", touchStart, touchEnd, "")
	if err != nil {
		t.Fatalf("起点实际时刻恰好等于已有终点、且无其他重叠时，应允许不填说明：%v", err)
	}
	if len(f.store.data.Notes) != 0 {
		t.Fatalf("端点相接不应产生任何说明，got %+v", f.store.data.Notes)
	}
	if peers := notePeers(f, next.ID); len(peers) != 0 {
		t.Fatalf("新班次不应看到与端点相接班次的说明：%+v", peers)
	}
	if peers := notePeers(f, first.ID); len(peers) != 0 {
		t.Fatalf("已有班次也不应看到该说明：%+v", peers)
	}
}

// TestCreateShiftSpanningWithoutNoteRejectedNoSideEffects：跨两班的新班次缺少
// 说明或只有空白时必须明确拒绝，错误要同时指出两个冲突班次；失败后查不到新
// 班次、不留下部分说明、不改变原有班次与说明；随后用有效说明再次建立时编号
// 从原来的下一个继续，不因失败跳号。
func TestCreateShiftSpanningWithoutNoteRejectedNoSideEffects(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")

	snapshot := func() string {
		raw, err := json.Marshal(f.store.data)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	before := snapshot()

	tryReject := func(label, note string) {
		t.Helper()
		_, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), note)
		if !errors.Is(err, ErrOverlap) {
			t.Fatalf("%s：应报 ErrOverlap，got %v", label, err)
		}
		// 错误必须同时指出两个冲突班次，不能只报最早找到的一班。
		if !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), second.ID) {
			t.Fatalf("%s：错误应同时指出两个冲突班次 %s、%s：%v", label, first.ID, second.ID, err)
		}
	}
	tryReject("未填说明", "")
	tryReject("只有空白", "   ")
	tryReject("制表符等空白", "\t\n ")

	// 失败后查不到新班次，编号 S003 尚未被占用。
	if _, err := f.svc.GetShift("S003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败后不应能查到新班次，got %v", err)
	}
	if shifts := f.svc.ListShifts(); len(shifts) != 2 {
		t.Fatalf("失败后班次数量应不变，期望2，got %d", len(shifts))
	}
	// 不留下任何（部分）说明，编号序列也不前进。
	if len(f.store.data.Notes) != 0 || f.store.data.NoteSeq != 0 {
		t.Fatalf("失败后不应留下部分说明：notes=%+v seq=%d", f.store.data.Notes, f.store.data.NoteSeq)
	}
	// 原有班次与说明完全不变。
	if after := snapshot(); after != before {
		t.Fatalf("失败前后数据应完全一致\nbefore %s\nafter  %s", before, after)
	}

	// 退出重开确认失败未在文件上留下半条数据。
	f.reopen(t)
	if _, err := f.svc.GetShift("S003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重开后仍不应查到失败的新班次，got %v", err)
	}
	if len(f.store.data.Notes) != 0 {
		t.Fatalf("重开后不应有残留说明：%+v", f.store.data.Notes)
	}

	// 提供有效说明再次建立：班次编号从原来的下一个（S003）继续，不因失败跳号。
	span, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "补齐有效说明")
	if err != nil {
		t.Fatalf("补齐有效说明后应成功建立：%v", err)
	}
	if span.ID != "S003" {
		t.Fatalf("失败不应消耗班次编号，再次建立应得到 S003，got %s", span.ID)
	}
	all := append([]OverlapNote(nil), f.store.data.Notes...)
	if len(all) != 2 {
		t.Fatalf("成功后应一次保存两条说明，got %d：%+v", len(all), all)
	}
	noteIDs := map[string]bool{}
	for _, n := range all {
		noteIDs[n.ID] = true
		if n.Note != "补齐有效说明" {
			t.Fatalf("说明应沿用重试时输入：%+v", n)
		}
	}
	if !noteIDs["N001"] || !noteIDs["N002"] {
		t.Fatalf("说明编号也应从 N001/N002 连续开始，got %v", noteIDs)
	}
	peers := notePeers(f, span.ID)
	if !peers[first.ID].NoteSet() || !peers[second.ID].NoteSet() {
		t.Fatalf("成功后两条关系应分别关联两班：%+v", peers)
	}
}
