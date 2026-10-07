package handover

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“事项关闭信息的查询展示”补回归保障。README“查询”一节已经约定：
// 保存状态为已关闭即显示已关闭，关闭人、关闭时间各自缺失时分别标为未记录，
// 缺一项不隐藏另一项；结束时记录与最新状态各用各的关闭信息；保存状态为
// 未关闭时残留的关闭人或时间不算一次关闭；查询只读不补写。
//
// 这些用例全部通过用户实际使用的查询入口核对可见文本：item-show 对应
// FormatItemJourney，shift-show 对应 FormatReport（当前事项、结束时记录、
// 最新状态对照）。旧数据缺项直接按旧数据的样子构造（字段缺失、空串、null、
// 0001-01-01T00:00:00Z），不靠正常关闭流程制造，保证锁住的是“解读旧数据”
// 这条路径。

// closeInfoFixture 中各类旧数据事项的编号与固定时间，供多个用例复用。
var (
	legacyRealClosedAt = tsDay(2, 14, 30)
	legacyOtherZoneAt  = time.Date(2026, 10, 2, 15, 30, 0, 0, time.FixedZone("JST", 9*3600))
)

// ptrTime 返回时间的指针，便于直接构造旧数据里的关闭时间。
func ptrTime(t time.Time) *time.Time { return &t }

// tamperStoredItem 直接按旧数据的样子改写已保存事项（不经过业务操作），
// 模拟历史版本留下的缺失、空值或残留字段。
func tamperStoredItem(t *testing.T, f *fixture, itemID string, fn func(*Item)) {
	t.Helper()
	for i := range f.store.data.Items {
		if f.store.data.Items[i].ID == itemID {
			fn(&f.store.data.Items[i])
			return
		}
	}
	t.Fatalf("存储中找不到事项 %s", itemID)
}

// tamperCloseSnapshot 直接改写班次结束时记录中某事项的冻结快照，
// 模拟旧版本冻结出的缺项或残留字段。
func tamperCloseSnapshot(t *testing.T, f *fixture, shiftID, itemID string, fn func(*CloseItemSnapshot)) {
	t.Helper()
	for i := range f.store.data.Shifts {
		sh := &f.store.data.Shifts[i]
		if sh.ID != shiftID || sh.CloseRecord == nil {
			continue
		}
		for j := range sh.CloseRecord.Items {
			if sh.CloseRecord.Items[j].ItemID == itemID {
				fn(&sh.CloseRecord.Items[j])
				return
			}
		}
		t.Fatalf("班次 %s 的结束时记录中找不到事项 %s", shiftID, itemID)
	}
	t.Fatalf("找不到班次 %s 或其结束时记录", shiftID)
}

// commitLegacyData 把按旧数据样子做的内存改动真正落盘，模拟“退出后重开
// 仍是这份旧数据”，也让后续可以核对查询不产生任何写盘。
func commitLegacyData(t *testing.T, f *fixture) {
	t.Helper()
	snapshot := f.store.data
	if err := f.store.mutate(func(d *Data) error {
		*d = snapshot
		return nil
	}); err != nil {
		t.Fatalf("落盘旧数据：%v", err)
	}
}

// assertStateBracket 核对事项标题行 [..] 中的关闭状态文本整段一致，
// 防止缺项时夹带其他人名、横线或公元元年日期。只看带“严重程度=”的事项
// 标题行，避免匹配上班次标题行上的 [进行中]/[已结束] 括号。
func assertStateBracket(t *testing.T, text, wantState string) {
	t.Helper()
	want := "[" + wantState + "]"
	found := false
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "严重程度=") {
			continue
		}
		i := strings.Index(line, "[")
		if i < 0 {
			continue
		}
		j := strings.Index(line[i:], "]")
		if j < 0 {
			continue
		}
		found = true
		if got := line[i : i+j+1]; got == want {
			return
		}
	}
	if found {
		t.Fatalf("事项状态括号中找不到 %q\n完整输出：\n%s", want, text)
	}
	t.Fatalf("输出中找不到事项状态括号 %q：\n%s", want, text)
}

// TestItemShowClosedWithIncompleteCloseInfo：item-show 查看最新状态时，
// 已关闭事项即使关闭人或关闭时间缺失，结论仍固定为已关闭，并分别指出
// 缺项；姓名与时间各自独立保留，二者都有时沿用“姓名 于 时间”。
func TestItemShowClosedWithIncompleteCloseInfo(t *testing.T) {
	f := newFixture(t)
	// 班次负责人与后续负责人使用固定的独特姓名，任何一项缺失时都不得拿
	// 他们补齐关闭人。
	a := mustShift(t, f, "调度", "班次负责人甲", tsDay(2, 8, 0), tsDay(2, 16, 0), "")

	cases := []struct {
		name      string
		content   string
		operator  string
		closedAt  *time.Time
		wantState string
	}{
		{
			name:      "关闭人与关闭时间都缺失",
			content:   "关闭信息全无",
			operator:  "",
			closedAt:  nil,
			wantState: "已关闭（关闭人未记录，关闭时间未记录）",
		},
		{
			name:      "只有关闭人没有关闭时间",
			content:   "只有关闭人",
			operator:  "旧关闭人张三",
			closedAt:  nil,
			wantState: "已关闭（关闭人 旧关闭人张三，关闭时间未记录）",
		},
		{
			name:      "有姓名但关闭时间是零值",
			content:   "零值关闭时间",
			operator:  "旧关闭人李四",
			closedAt:  ptrTime(time.Time{}),
			wantState: "已关闭（关闭人 旧关闭人李四，关闭时间未记录）",
		},
		{
			name:      "只有真实关闭时间没有关闭人",
			content:   "只有关闭时间",
			operator:  "",
			closedAt:  &legacyRealClosedAt,
			wantState: "已关闭（关闭人未记录，关闭时间 " + fmtTime(legacyRealClosedAt) + "）",
		},
		{
			name:      "关闭人与关闭时间都完整（含另一时区）",
			content:   "关闭信息完整",
			operator:  "旧关闭人王五",
			closedAt:  &legacyOtherZoneAt,
			wantState: "已关闭（旧关闭人王五 于 " + fmtTime(legacyOtherZoneAt) + "）",
		},
	}
	for _, tc := range cases {
		it, err := f.svc.AddItem(a.ID, tc.content, SeverityNormal, "", "后续负责人乙")
		if err != nil {
			t.Fatalf("%s：新增事项失败：%v", tc.name, err)
		}
		tamperStoredItem(t, f, it.ID, func(p *Item) {
			p.Closed = true
			p.CloseOperator = tc.operator
			p.ClosedAt = tc.closedAt
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 用内容定位事项编号。
			var id string
			for _, it := range f.store.data.Items {
				if it.Content == tc.content {
					id = it.ID
				}
			}
			j, err := f.svc.ItemJourney(id)
			if err != nil {
				t.Fatalf("item-show：%v", err)
			}
			text := FormatItemJourney(j)
			assertStateBracket(t, text, tc.wantState)
			if strings.Contains(text, "0001-01-01") {
				t.Fatalf("缺时间时不得出现公元元年日期：\n%s", text)
			}
			// 班次负责人用了独特姓名：关闭人缺失时任何位置都不得出现该姓名，
			// 即不得拿班次负责人补齐（事项视图本身也不展示班次负责人）。
			if strings.Contains(text, "班次负责人甲") {
				t.Fatalf("关闭人缺失时不得拿班次负责人补齐：\n%s", text)
			}
		})
	}
}

// TestShiftShowOngoingCurrentItemsIncompleteCloseInfo：进行中班次的当前事项
// 同样适用关闭信息规则——不能只保障已结束班次的报告。同时覆盖保存状态为
// 未关闭却残留关闭人/关闭时间的旧事项：必须按未关闭展示，残留字段不是
// 一次关闭。
func TestShiftShowOngoingCurrentItemsIncompleteCloseInfo(t *testing.T) {
	f := newFixture(t)
	a := mustShift(t, f, "调度", "班次负责人丙", tsDay(3, 8, 0), tsDay(3, 16, 0), "")

	closedNoInfo, err := f.svc.AddItem(a.ID, "进行中班里已关闭但全无信息", SeverityImportant, "", "后续负责人丁")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	tamperStoredItem(t, f, closedNoInfo.ID, func(p *Item) {
		p.Closed = true
		p.CloseOperator = ""
		p.ClosedAt = nil
	})

	closedWithTime, err := f.svc.AddItem(a.ID, "进行中班里只有关闭时间", SeverityNormal, "", "后续负责人丁")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	tamperStoredItem(t, f, closedWithTime.ID, func(p *Item) {
		p.Closed = true
		p.CloseOperator = ""
		p.ClosedAt = &legacyOtherZoneAt
	})

	openResidual, err := f.svc.AddItem(a.ID, "未关闭却残留关闭字段", SeverityNormal, "", "后续负责人丁")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	tamperStoredItem(t, f, openResidual.ID, func(p *Item) {
		p.Closed = false
		p.CloseOperator = "残留关闭人赵"
		p.ClosedAt = &legacyRealClosedAt
	})

	rep, err := f.svc.ShiftReport(a.ID)
	if err != nil {
		t.Fatalf("shift-show: %v", err)
	}
	if rep.ItemsAtClose || rep.HistoryIncomplete {
		t.Fatalf("进行中班次应展示当前事项，而不是结束时记录：%+v", rep.Shift)
	}
	text := FormatReport(rep)
	if strings.Contains(text, "结束时记录") || strings.Contains(text, "历史记录不完整") {
		t.Fatalf("进行中班次不应出现结束时记录标题：\n%s", text)
	}
	assertStateBracket(t, text, "已关闭（关闭人未记录，关闭时间未记录）")
	assertStateBracket(t, text, "已关闭（关闭人未记录，关闭时间 "+fmtTime(legacyOtherZoneAt)+"）")

	// 残留关闭字段的事项整段展示必须是未关闭，且状态括号内不出现残留人名/时间。
	residualBlock := itemBlock(text, openResidual.ID)
	assertStateBracket(t, residualBlock, "未关闭")
	if strings.Contains(residualBlock, "残留关闭人赵") || strings.Contains(residualBlock, fmtTime(legacyRealClosedAt)) {
		t.Fatalf("未关闭事项的残留关闭人/时间不得被解释成关闭：\n%s", residualBlock)
	}
	if strings.Contains(text, "0001-01-01") {
		t.Fatalf("报告中不得出现公元元年日期：\n%s", text)
	}

	// item-show 对残留字段同样按未关闭展示。
	j, err := f.svc.ItemJourney(openResidual.ID)
	if err != nil {
		t.Fatalf("item-show residual: %v", err)
	}
	jtext := FormatItemJourney(j)
	assertStateBracket(t, jtext, "未关闭")
	if strings.Contains(strings.SplitN(jtext, "\n", 2)[0], "残留关闭人赵") {
		t.Fatalf("未关闭事项标题不得带出残留关闭人：\n%s", jtext)
	}
}

// itemBlock 从报告文本中取出以“<id>  ”开头到下一个事项块之间的段落，
// 用于只检查某一项的展示，不被其他事项干扰。缩进按报告层级容忍。
func itemBlock(text, id string) string {
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if start < 0 && strings.HasPrefix(trimmed, id+"  ") {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(trimmed, "I0") && strings.Contains(trimmed, "  严重程度=") {
			end = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// setupCloseInfoHandover 建立 A（张三，已结束）-> B（李四，进行中）的交接，
// 并把两个事项确认接收到 B：I001 用来模拟“结束时记录已关闭但缺项”，
// I002 用来模拟“结束时未关闭、接班后才关闭（当前信息也缺项）”。
func setupCloseInfoHandover(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	a := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	b := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")

	for _, content := range []string{"结束时已关闭但缺关闭信息", "接班后才关闭的事项"} {
		if _, err := f.svc.AddItem(a.ID, content, SeverityNormal, "原限制", "李四"); err != nil {
			t.Fatalf("add item %s: %v", content, err)
		}
	}
	if _, err := f.svc.CloseShift(a.ID); err != nil {
		t.Fatalf("close A: %v", err)
	}
	h, err := f.svc.CreateHandover(a.ID, b.ID)
	if err != nil {
		t.Fatalf("create handover: %v", err)
	}
	for _, id := range []string{"I001", "I002"} {
		if _, err := f.svc.ProcessEntry(h.ID, id, ActionConfirm, "李四", "", "", ""); err != nil {
			t.Fatalf("confirm %s: %v", id, err)
		}
	}
	return f
}

// TestCloseRecordAndLatestUseOwnCloseInfo：结束时记录与最新状态对照必须各自
// 使用自己的关闭信息，互不补齐：
//   - I001：原班结束时记录为已关闭但关闭人/时间缺失，事项最新信息完整；
//     原班结束时部分仍保留缺项提示，最新状态对照展示完整信息；
//   - I002：原班结束时未关闭（且残留关闭字段），接班后才关闭且当前关闭人
//     缺失；原班仍显示结束时未关闭，最新状态对照与进行中班次展示当前缺项。
func TestCloseRecordAndLatestUseOwnCloseInfo(t *testing.T) {
	f := setupCloseInfoHandover(t)

	latestClosedAt := tsDay(2, 20, 0)

	// I001：旧版本在结束时记录里冻结了“已关闭”，却没有关闭人与关闭时间；
	// 接班后事项最新信息补全为完整关闭（业务事实只在最新值上）。
	tamperCloseSnapshot(t, f, "S001", "I001", func(s *CloseItemSnapshot) {
		s.Closed = true
		s.CloseOperator = ""
		s.ClosedAt = nil
	})
	tamperStoredItem(t, f, "I001", func(p *Item) {
		p.Closed = true
		p.CloseOperator = "李四"
		p.ClosedAt = &latestClosedAt
	})

	// I002：结束时未关闭，但旧数据残留了关闭人与时间（不得当成当时已关闭）；
	// 接班后才关闭，当前只有真实时间、没有关闭人。
	tamperCloseSnapshot(t, f, "S001", "I002", func(s *CloseItemSnapshot) {
		s.Closed = false
		s.CloseOperator = "残留张三"
		s.ClosedAt = &legacyRealClosedAt
	})
	tamperStoredItem(t, f, "I002", func(p *Item) {
		p.Closed = true
		p.CloseOperator = ""
		p.ClosedAt = &legacyOtherZoneAt
	})

	repA, err := f.svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report A: %v", err)
	}
	if !repA.ItemsAtClose {
		t.Fatalf("S001 应展示结束时记录")
	}
	textA := FormatReport(repA)

	block1 := closeItemBlock(textA, "I001")
	if !strings.Contains(block1, "结束时已关闭（关闭人未记录，关闭时间未记录）") {
		t.Fatalf("I001 结束时部分应保留缺项提示，不被最新值补齐：\n%s", block1)
	}
	if !strings.Contains(block1, "最新关闭情况=已关闭（李四 于 "+fmtTime(latestClosedAt)+"）") {
		t.Fatalf("I001 最新状态对照应展示完整的最新关闭信息：\n%s", block1)
	}

	block2 := closeItemBlock(textA, "I002")
	if !strings.Contains(block2, "结束时未关闭") {
		t.Fatalf("I002 原班结束时应显示当时未关闭：\n%s", block2)
	}
	if strings.Contains(block2, "残留张三") || strings.Contains(block2, fmtTime(legacyRealClosedAt)) {
		t.Fatalf("结束时未关闭事项的残留关闭字段不得显示为当时关闭：\n%s", block2)
	}
	if !strings.Contains(block2, "最新关闭情况=已关闭（关闭人未记录，关闭时间 "+fmtTime(legacyOtherZoneAt)+"）") {
		t.Fatalf("I002 最新状态对照应展示接班后缺关闭人的关闭信息：\n%s", block2)
	}

	// 进行中的接班班次：当前事项展示当前关闭信息，缺项同样标出。
	repB, err := f.svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report B: %v", err)
	}
	if repB.ItemsAtClose || repB.HistoryIncomplete {
		t.Fatalf("S002 仍应是进行中班次的当前事项视图")
	}
	textB := FormatReport(repB)
	cur1 := itemBlock(textB, "I001")
	assertStateBracket(t, cur1, "已关闭（李四 于 "+fmtTime(latestClosedAt)+"）")
	cur2 := itemBlock(textB, "I002")
	assertStateBracket(t, cur2, "已关闭（关闭人未记录，关闭时间 "+fmtTime(legacyOtherZoneAt)+"）")

	// item-show 展示事项最新状态：I001 完整、I002 缺关闭人。
	j1, _ := f.svc.ItemJourney("I001")
	assertStateBracket(t, FormatItemJourney(j1), "已关闭（李四 于 "+fmtTime(latestClosedAt)+"）")
	j2, _ := f.svc.ItemJourney("I002")
	assertStateBracket(t, FormatItemJourney(j2), "已关闭（关闭人未记录，关闭时间 "+fmtTime(legacyOtherZoneAt)+"）")
}

// closeItemBlock 从结束时报告中取出某事项的“结束时记录 + 最新状态对照”整块。
func closeItemBlock(text, id string) string {
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	for i, ln := range lines {
		if start < 0 && strings.HasPrefix(ln, "  "+id+"  ") {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(ln, "  I0") && strings.Contains(ln, "  严重程度=") {
			end = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// TestCloseInfoQueriesDoNotRewriteData：查询只是呈现已有事实。对含缺项已关闭、
// 残留字段未关闭、缺项结束时记录的旧数据连续执行 item-show 与两个班次的
// shift-show 后，内存数据与落盘文件都必须保持原样：关闭状态、人名、时间、
// 结束时快照一个字节都不补写。
func TestCloseInfoQueriesDoNotRewriteData(t *testing.T) {
	f := setupCloseInfoHandover(t)
	tamperCloseSnapshot(t, f, "S001", "I001", func(s *CloseItemSnapshot) {
		s.Closed = true
		s.CloseOperator = ""
		s.ClosedAt = nil
	})
	tamperStoredItem(t, f, "I001", func(p *Item) {
		p.Closed = true
		p.CloseOperator = "李四"
		p.ClosedAt = &legacyRealClosedAt
	})
	tamperStoredItem(t, f, "I002", func(p *Item) {
		p.Closed = false
		p.CloseOperator = "残留关闭人"
		p.ClosedAt = ptrTime(time.Time{})
	})
	commitLegacyData(t, f)

	beforeData := mustJSON(t, f.store.data)
	beforeFile := hashStoreFile(t, f)

	// 重新打开后查询，模拟用户退出重开只做查询；所有查询入口都走一遍。
	f.reopen(t)
	for _, id := range []string{"I001", "I002"} {
		j, err := f.svc.ItemJourney(id)
		if err != nil {
			t.Fatalf("item-show %s: %v", id, err)
		}
		_ = FormatItemJourney(j)
	}
	for _, id := range []string{"S001", "S002"} {
		rep, err := f.svc.ShiftReport(id)
		if err != nil {
			t.Fatalf("shift-show %s: %v", id, err)
		}
		_ = FormatReport(rep)
	}
	// 再查一轮，确认重复查询也不写入。
	for _, id := range []string{"I001", "I002", "S001", "S002"} {
		if strings.HasPrefix(id, "I") {
			j, err := f.svc.ItemJourney(id)
			if err != nil {
				t.Fatalf("repeat item-show: %v", err)
			}
			_ = FormatItemJourney(j)
			continue
		}
		rep, err := f.svc.ShiftReport(id)
		if err != nil {
			t.Fatalf("repeat shift-show: %v", err)
		}
		_ = FormatReport(rep)
	}

	if got := mustJSON(t, f.store.data); got != beforeData {
		t.Fatalf("查询不得补写任何关闭信息\nwant %s\ngot  %s", beforeData, got)
	}
	if got := hashStoreFile(t, f); got != beforeFile {
		t.Fatalf("查询不得触发写盘：before=%s after=%s", beforeFile, got)
	}

	// 关闭状态、人名与时间逐项保持原样。
	var i1, i2 Item
	for _, it := range f.store.data.Items {
		switch it.ID {
		case "I001":
			i1 = it
		case "I002":
			i2 = it
		}
	}
	if !i1.Closed || i1.CloseOperator != "李四" || i1.ClosedAt == nil || !i1.ClosedAt.Equal(legacyRealClosedAt) {
		t.Fatalf("I001 最新关闭信息应保持原样：%+v", i1)
	}
	if i2.Closed || i2.CloseOperator != "残留关闭人" || i2.ClosedAt == nil || !i2.ClosedAt.IsZero() {
		t.Fatalf("I002 未关闭状态与残留字段应保持原样，不被清理也不被解释成关闭：%+v", i2)
	}
	for _, sh := range f.store.data.Shifts {
		if sh.ID != "S001" {
			continue
		}
		for _, s := range sh.CloseRecord.Items {
			if s.ItemID == "I001" {
				if !s.Closed || s.CloseOperator != "" || s.ClosedAt != nil {
					t.Fatalf("I001 结束时记录缺项应保持原样：%+v", s)
				}
			}
			if s.ItemID == "I002" && s.Closed {
				t.Fatalf("I002 结束时记录应保持未关闭：%+v", s)
			}
		}
	}
}

func hashStoreFile(t *testing.T, f *fixture) string {
	t.Helper()
	b, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("读取数据文件：%v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// TestLegacyCloseInfoFromRawJSON：直接按旧版本可能写出的 JSON 落盘，锁住
// 反序列化与展示的配合：关闭人字段缺失或为 null、关闭时间字段缺失、为
// null 或 0001-01-01T00:00:00Z，全部显示未记录，不出现公元元年日期；
// 未关闭事项残留字段按未关闭展示；结束时记录中的同类缺项同样成立。
func TestLegacyCloseInfoFromRawJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	raw := `{
  "shift_seq": 2, "item_seq": 5, "handover_seq": 0, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"班次负责人",
     "start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00",
     "created_at":"2026-10-02T08:00:00+08:00","closed":true,
     "closed_at":"2026-10-02T16:00:00+08:00",
     "close_record":{"items":[
       {"item_id":"I001","content":"字段全缺","severity":"normal","follow_owner":"李四","closed":true},
       {"item_id":"I002","content":"字段为 null","severity":"normal","follow_owner":"李四","closed":true,
        "close_operator":null,"closed_at":null},
       {"item_id":"I003","content":"有姓名零值时间","severity":"normal","follow_owner":"李四","closed":true,
        "close_operator":"旧关闭人","closed_at":"0001-01-01T00:00:00Z"},
       {"item_id":"I004","content":"未关闭残留字段","severity":"normal","follow_owner":"李四","closed":false,
        "close_operator":"残留人","closed_at":"2026-10-02T14:00:00+08:00"},
       {"item_id":"I005","content":"有真实时间缺姓名","severity":"normal","follow_owner":"李四","closed":false}
     ]}},
    {"id":"S002","position":"调度","owner":"李四",
     "start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00",
     "created_at":"2026-10-02T16:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"字段全缺","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00","closed":true},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"字段为 null","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00","closed":true,
     "close_operator":null,"closed_at":null},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"有姓名零值时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00","closed":true,
     "close_operator":"旧关闭人","closed_at":"0001-01-01T00:00:00Z"},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"未关闭残留字段","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:30:00+08:00","closed":false,
     "close_operator":"残留人","closed_at":"2026-10-02T14:00:00+08:00"},
    {"id":"I005","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"有真实时间缺姓名","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T15:00:00+08:00","closed":true,
     "closed_at":"2026-10-02T15:30:00+09:00"}
  ],
  "handovers": [],
  "notes": []
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open raw legacy: %v", err)
	}
	svc := NewService(store)

	// 反序列化层面：缺字段与 null 都是 nil 指针/空串，零值字符串是零值时间。
	var byID = map[string]Item{}
	for _, it := range store.data.Items {
		byID[it.ID] = it
	}
	if byID["I001"].CloseOperator != "" || byID["I001"].ClosedAt != nil {
		t.Fatalf("缺字段应解析为空串与 nil：%+v", byID["I001"])
	}
	if byID["I002"].CloseOperator != "" || byID["I002"].ClosedAt != nil {
		t.Fatalf("null 应解析为空串与 nil：%+v", byID["I002"])
	}
	if byID["I003"].ClosedAt == nil || !byID["I003"].ClosedAt.IsZero() {
		t.Fatalf("零值时间应解析为 IsZero 的时间：%+v", byID["I003"].ClosedAt)
	}

	// item-show：四种缺项/残留形态各自的最新状态文本。
	for _, tc := range []struct {
		id        string
		wantState string
	}{
		{"I001", "已关闭（关闭人未记录，关闭时间未记录）"},
		{"I002", "已关闭（关闭人未记录，关闭时间未记录）"},
		{"I003", "已关闭（关闭人 旧关闭人，关闭时间未记录）"},
		{"I005", "已关闭（关闭人未记录，关闭时间 " + fmtTime(legacyOtherZoneAt) + "）"},
	} {
		j, err := svc.ItemJourney(tc.id)
		if err != nil {
			t.Fatalf("journey %s: %v", tc.id, err)
		}
		text := FormatItemJourney(j)
		assertStateBracket(t, text, tc.wantState)
		if strings.Contains(text, "0001-01-01") {
			t.Fatalf("%s 输出不得出现公元元年日期：\n%s", tc.id, text)
		}
	}
	j4, err := svc.ItemJourney("I004")
	if err != nil {
		t.Fatalf("journey I004: %v", err)
	}
	text4 := FormatItemJourney(j4)
	assertStateBracket(t, text4, "未关闭")
	if strings.Contains(strings.SplitN(text4, "\n", 2)[0], "残留人") {
		t.Fatalf("未关闭残留事项不得在状态中带出残留关闭人：\n%s", text4)
	}

	// 已结束班次：结束时记录中的缺项与最新状态对照各自独立展示。
	rep1, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report S001: %v", err)
	}
	rtext := FormatReport(rep1)
	for _, want := range []string{
		"结束时已关闭（关闭人未记录，关闭时间未记录）", // I001、I002 同形
		"结束时已关闭（关闭人 旧关闭人，关闭时间未记录）",
		"结束时未关闭", // I004
		"最新关闭情况=已关闭（关闭人未记录，关闭时间 " + fmtTime(legacyOtherZoneAt) + "）", // I005
	} {
		if !strings.Contains(rtext, want) {
			t.Fatalf("结束时记录/最新状态对照缺少 %q：\n%s", want, rtext)
		}
	}
	if strings.Count(rtext, "结束时已关闭（关闭人未记录，关闭时间未记录）") != 2 {
		t.Fatalf("I001 与 I002 两条结束时缺项记录都应保留，got:\n%s", rtext)
	}
	if strings.Contains(rtext, "0001-01-01") {
		t.Fatalf("结束时记录不得出现公元元年日期：\n%s", rtext)
	}

	// 进行中班次当前事项同样成立。
	rep2, err := svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	if rep2.ItemsAtClose || rep2.HistoryIncomplete {
		t.Fatalf("S002 应为进行中班次当前事项视图")
	}
	btext := FormatReport(rep2)
	assertStateBracket(t, itemBlock(btext, "I005"),
		"已关闭（关闭人未记录，关闭时间 "+fmtTime(legacyOtherZoneAt)+"）")
	assertStateBracket(t, itemBlock(btext, "I004"), "未关闭")
}
