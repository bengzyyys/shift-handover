package handover

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// notePairs 把一组重叠说明整理成无序班次对的集合，便于断言关联关系。
func notePairs(notes []OverlapNote) map[[2]string]OverlapNote {
	out := map[[2]string]OverlapNote{}
	for _, n := range notes {
		out[[2]string{n.ShiftA, n.ShiftB}] = n
	}
	return out
}

// TestCreateShiftSpanningTwoShiftsSavesNotePerOverlap：新班次 10:00-14:00 同时
// 跨过同岗位相邻两班（08:00-12:00 与 12:00-16:00），填写一段说明后成功建立；
// 说明分别关联新班次与每个实际重叠的已有班次，既有班次间只是端点相接，
// 不产生第三条说明；不同岗位的同时间段班次收不到这段说明。
func TestCreateShiftSpanningTwoShiftsSavesNotePerOverlap(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")
	// 同时间段但岗位不同：不应参与本次重叠说明。
	otherPos := mustShift(t, f, "巡检", "赵六", ts(10, 0), ts(14, 0), "")

	created, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修期间两班并行交接")
	if err != nil {
		t.Fatalf("跨两班填写说明后应成功建立：%v", err)
	}
	if created.ID != "S004" {
		t.Fatalf("新班次应获得稳定编号 S004，got %s", created.ID)
	}
	if _, err := f.svc.GetShift(created.ID); err != nil {
		t.Fatalf("建立后应能查到新班次：%v", err)
	}

	// 全部说明恰好两条：新班次分别与两个重叠班次各一条，不多不少。
	all := f.store.data.Notes
	if len(all) != 2 {
		t.Fatalf("应恰好保存两条说明，got %d：%+v", len(all), all)
	}
	pairs := notePairs(all)
	p1, ok1 := pairs[notePairKey(created.ID, first.ID)]
	p2, ok2 := pairs[notePairKey(created.ID, second.ID)]
	if !ok1 || !ok2 {
		t.Fatalf("说明应分别关联新班次与两个重叠班次，got %+v", all)
	}
	if _, ok := pairs[notePairKey(first.ID, second.ID)]; ok {
		t.Fatalf("既有班次间只是端点相接，不应出现第三条说明：%+v", all)
	}
	for _, n := range []OverlapNote{p1, p2} {
		if n.Note != "抢修期间两班并行交接" {
			t.Fatalf("说明内容应沿用用户输入，got %q", n.Note)
		}
		if n.Position != "调度" {
			t.Fatalf("说明应属于本岗位，got %q", n.Position)
		}
	}
	if p1.ID != "N001" || p2.ID != "N002" {
		t.Fatalf("说明应获得稳定编号 N001/N002，got %s %s", p1.ID, p2.ID)
	}

	// 从新班次查看：两条说明都在。
	if got := f.svc.OverlapNotes(created.ID); len(got) != 2 {
		t.Fatalf("从新班次应看到两条说明，got %+v", got)
	}
	// 从任一已有班次查看：只看到涉及自己的那条。
	for _, sh := range []Shift{first, second} {
		got := f.svc.OverlapNotes(sh.ID)
		if len(got) != 1 {
			t.Fatalf("从 %s 应只看到涉及自己的一条说明，got %+v", sh.ID, got)
		}
		if got[0].ShiftA != sh.ID && got[0].ShiftB != sh.ID {
			t.Fatalf("说明应涉及班次 %s，got %+v", sh.ID, got[0])
		}
		other := first.ID
		if sh.ID == first.ID {
			other = second.ID
		}
		if got[0].ShiftA == other || got[0].ShiftB == other {
			t.Fatalf("班次 %s 不应看到与另一已有班次 %s 的关系：%+v", sh.ID, other, got[0])
		}
	}
	// 不同岗位的班次收不到这段说明。
	if got := f.svc.OverlapNotes(otherPos.ID); len(got) != 0 {
		t.Fatalf("不同岗位班次不应收到说明，got %+v", got)
	}

	// 班次报告的查询面与之一致。
	rep, err := f.svc.ShiftReport(created.ID)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.OverlapNotes) != 2 {
		t.Fatalf("新班次报告应展示两条说明，got %+v", rep.OverlapNotes)
	}
}

func notePairKey(a, b string) [2]string {
	x, y := notePair(a, b)
	return [2]string{x, y}
}

// TestOverlapJudgedByInstantAcrossZones：重叠按实际时刻判断。新班次用不同于
// 已有班次的时区偏移表示同一段时刻（10:00-14:00 +08:00 即 02:00-06:00 UTC），
// 得到的重叠关系与说明不变；起点按实际时刻恰好等于已有班次终点时不算重叠，
// 可以不填说明，也不产生该对班次的说明。
func TestOverlapJudgedByInstantAcrossZones(t *testing.T) {
	utc := time.UTC
	tsUTC := func(hour, min int) time.Time {
		return time.Date(2026, 10, 2, hour, min, 0, 0, utc)
	}

	// 同一段时刻换时区表示：02:00-06:00 UTC == 10:00-14:00 +08:00，
	// 仍跨过同岗位两班，说明关系与钟面写法无关。
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")
	created, err := f.svc.CreateShift("调度", "王五", tsUTC(2, 0), tsUTC(6, 0), "跨时区填写的同一段时刻")
	if err != nil {
		t.Fatalf("换算后同一段时刻应得到相同重叠关系：%v", err)
	}
	pairs := notePairs(f.store.data.Notes)
	if len(pairs) != 2 {
		t.Fatalf("应仍判定与两班重叠并各存一条说明，got %+v", f.store.data.Notes)
	}
	if _, ok := pairs[notePairKey(created.ID, first.ID)]; !ok {
		t.Fatalf("应关联第一班 %s：%+v", first.ID, f.store.data.Notes)
	}
	if _, ok := pairs[notePairKey(created.ID, second.ID)]; !ok {
		t.Fatalf("应关联第二班 %s：%+v", second.ID, f.store.data.Notes)
	}

	// 起点按实际时刻恰好等于已有班次终点：04:00 UTC == 12:00 +08:00，
	// 与第一班端点相接不算重叠，也不与其他同岗位班次重叠，可不填说明。
	f2 := newFixture(t)
	early := mustShift(t, f2, "调度", "张三", ts(8, 0), ts(12, 0), "")
	touching, err := f2.svc.CreateShift("调度", "李四", tsUTC(4, 0), tsUTC(8, 0), "")
	if err != nil {
		t.Fatalf("端点按实际时刻相接应允许不填说明：%v", err)
	}
	if got := f2.svc.OverlapNotes(touching.ID); len(got) != 0 {
		t.Fatalf("端点相接不应产生说明，got %+v", got)
	}
	if got := f2.svc.OverlapNotes(early.ID); len(got) != 0 {
		t.Fatalf("已有班次一侧也不应留下说明，got %+v", got)
	}
}

// TestCreateShiftSpanningTwoShiftsRequiresNote：跨两班的新班次不填说明或只填
// 空白时明确拒绝，错误指出两个冲突班次；失败后查不到新班次、不留部分说明、
// 原有班次与说明不变；随后用有效说明重建时编号从原来的下一个继续，不跳号。
func TestCreateShiftSpanningTwoShiftsRequiresNote(t *testing.T) {
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

	// 不填说明与只填空白都应拒绝，错误指出两个冲突班次。
	for _, note := range []string{"", "   "} {
		_, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), note)
		if !errors.Is(err, ErrOverlap) {
			t.Fatalf("说明为 %q 时应报 ErrOverlap，got %v", note, err)
		}
		if !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), second.ID) {
			t.Fatalf("错误应指出两个冲突班次 %s 与 %s，got %v", first.ID, second.ID, err)
		}
	}

	// 失败后查不到新班次，编号未被占用。
	if _, err := f.svc.GetShift("S003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败后不应查到这个新班次，got %v", err)
	}
	if shifts := f.svc.ListShifts(); len(shifts) != 2 {
		t.Fatalf("失败后不应留下新班次，got %d 个班次", len(shifts))
	}
	// 不留下部分说明，原有班次与说明不变。
	if after := snapshot(); after != before {
		t.Fatalf("失败不得改变任何已保存数据\nbefore %s\nafter  %s", before, after)
	}

	// 随后提供有效说明再次建立：班次与说明编号都从原来的下一个继续，不跳号。
	created, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修并行")
	if err != nil {
		t.Fatalf("重试应成功：%v", err)
	}
	if created.ID != "S003" {
		t.Fatalf("失败不应消耗编号，重试应为 S003，got %s", created.ID)
	}
	notes := f.svc.OverlapNotes(created.ID)
	if len(notes) != 2 || notes[0].ID != "N001" || notes[1].ID != "N002" {
		t.Fatalf("说明编号应从 N001 继续，got %+v", notes)
	}
}

// TestCreateHandoverBlankOverlapNoteDoesNotCount：两班时间确实重叠时，发起交接
// 以实际保存的说明正文为准。本地数据中只存在正文缺失、为空或全是空白（空格、
// 制表符、换行）的说明记录时不能放行：handover-create 明确失败并指出两班编号、
// 提示先填写重叠说明，不留下新交接、不指定接班关系、不改变事项归属；只关联其中
// 一个班次、实际属于另一对班次的有效说明也不能借用。通过 note-add 追加非空说明
// 后可重新发起，较早的空白记录不能挡住它，且空白记录仍保留可查。
func TestCreateHandoverBlankOverlapNoteDoesNotCount(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	if _, err := f.svc.AddItem(a.ID, "未关闭事项", SeverityImportant, "", "李四"); err != nil {
		t.Fatalf("add item: %v", err)
	}
	b := mustShift(t, f, "调度", "李四", ts(15, 0), ts(23, 0), "抢修并行")
	// 另一对班次 (b,c) 的有效说明：只关联 b，不能借给 (a,b) 使用。
	c := mustShift(t, f, "调度", "王五", ts(22, 0), tsDay(3, 6, 0), "夜间并班")
	if got := f.svc.OverlapNotes(c.ID); len(got) != 1 {
		t.Fatalf("(b,c) 应保存一条有效说明，got %+v", got)
	}

	// 模拟本地数据中 (a,b) 的说明记录正文缺失、为空或全是空白：
	// 记录存在，但没有任何有效正文。
	x, y := notePair(a.ID, b.ID)
	blanks := []string{"", "   ", "\t \n"}
	notes := []OverlapNote{}
	for _, n := range f.store.data.Notes {
		if n.ShiftA == x && n.ShiftB == y {
			continue // 去掉建立时保存的有效说明，换成空白记录
		}
		notes = append(notes, n)
	}
	for i, body := range blanks {
		notes = append(notes, OverlapNote{
			ID:        fmt.Sprintf("N9%02d", i+1),
			Position:  "调度",
			ShiftA:    x,
			ShiftB:    y,
			Note:      body,
			CreatedAt: f.clock,
		})
	}
	f.store.data.Notes = notes

	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}

	// 只有空白说明记录：不允许发起交接，错误指出两班编号并提示先填说明。
	_, err := f.svc.CreateHandover(a.ID, b.ID)
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("只有空白说明记录时应报 ErrOverlap，got %v", err)
	}
	if !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("错误应指出两班编号 %s 与 %s，got %v", a.ID, b.ID, err)
	}
	if !strings.Contains(err.Error(), "重叠说明") {
		t.Fatalf("错误应提示先填写重叠说明，got %v", err)
	}

	// 不留下新交接、不指定接班关系、不改变事项归属。
	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("失败后不应留下交接，got %+v", hs)
	}
	it, err := f.svc.GetItem("I001")
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != a.ID {
		t.Fatalf("事项归属不应改变，got %s", it.CurrentShiftID)
	}

	// 通过 note-add 追加非空说明后可重新发起；较早的空白记录不能挡住它。
	if _, err := f.svc.AddOverlapNote(a.ID, b.ID, "抢修期间两班并行，已口头交接"); err != nil {
		t.Fatalf("note-add: %v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("追加有效说明后应能发起交接：%v", err)
	}
	if h.FromShiftID != a.ID || h.ToShiftID != b.ID {
		t.Fatalf("交接关系错误：%+v", h)
	}

	// 空白记录与有效说明都保留可查，不需要删除或覆盖历史记录。
	pairNotes := []OverlapNote{}
	for _, n := range f.svc.OverlapNotes(a.ID) {
		if n.ShiftA == x && n.ShiftB == y {
			pairNotes = append(pairNotes, n)
		}
	}
	if len(pairNotes) != len(blanks)+1 {
		t.Fatalf("空白记录与有效说明都应保留，got %+v", pairNotes)
	}
}

// TestCreateHandoverLaterBlankNoteDoesNotInvalidate：同一对班次先保存了有效
// 说明，之后又出现更晚存放的空白记录时，已有说明不失效，仍可发起交接。
func TestCreateHandoverLaterBlankNoteDoesNotInvalidate(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	b := mustShift(t, f, "调度", "李四", ts(15, 0), ts(23, 0), "抢修并行")
	x, y := notePair(a.ID, b.ID)
	// 较晚存放的空白记录不能使已有有效说明失效。
	f.store.data.Notes = append(f.store.data.Notes, OverlapNote{
		ID: "N900", Position: "调度", ShiftA: x, ShiftB: y, Note: "  \n\t ",
		CreatedAt: f.clock,
	})
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}
	if _, err := f.svc.CreateHandover(a.ID, b.ID); err != nil {
		t.Fatalf("已有有效说明时不应被较晚的空白记录挡住：%v", err)
	}
}

// TestCreateHandoverEmptyListRequiresEffectiveNote：重叠班次即使没有未关闭
// 事项，首次发起空清单交接也必须满足同一说明要求；追加有效说明后空清单
// 交接可以发起并直接完成。
func TestCreateHandoverEmptyListRequiresEffectiveNote(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", ts(8, 0), ts(16, 0), "")
	b := mustShift(t, f, "调度", "李四", ts(15, 0), ts(23, 0), "并行")
	// 把 (a,b) 的说明正文改成空白，模拟只保存了空记录的本地数据。
	x, y := notePair(a.ID, b.ID)
	for i := range f.store.data.Notes {
		if f.store.data.Notes[i].ShiftA == x && f.store.data.Notes[i].ShiftB == y {
			f.store.data.Notes[i].Note = " \t\n "
		}
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close a: %v", err)
	}
	if _, err := f.svc.CreateHandover(a.ID, b.ID); !errors.Is(err, ErrOverlap) {
		t.Fatalf("空清单交接只有空白说明时也应报 ErrOverlap，got %v", err)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("失败后不应留下交接，got %+v", hs)
	}
	// 追加有效说明后空清单交接可以发起并直接完成。
	if _, err := f.svc.AddOverlapNote(a.ID, b.ID, "抢修并行半小时"); err != nil {
		t.Fatalf("note-add: %v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("追加有效说明后应能发起空清单交接：%v", err)
	}
	if h.CompletedAt == nil {
		t.Fatalf("空清单交接应在发起时完成：%+v", h)
	}
}
