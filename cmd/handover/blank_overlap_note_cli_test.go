package main

// 本文件从命令行端到端保障“发起交接时以实际保存的重叠说明正文为准”：
// 本地数据里即使保存了关联两班的重叠说明记录，只要正文缺失、为空或全是空白，
// handover-create 就必须明确失败（指出两班编号并提示先填写重叠说明），不能
// 输出已发起交接、不能留下新交接或改变事项归属；用户通过 note-add 追加非空
// 说明后应能重新发起，原来的空记录仍保留可查。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rewriteNotes 直接改写数据文件中重叠说明的正文，模拟旧数据里“记录存在、
// 正文为空白”的情况；其余数据原样保留。
func rewriteNotes(t *testing.T, dataPath string, fn func(note map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("读取数据文件：%v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析数据文件：%v", err)
	}
	notes, _ := doc["notes"].([]any)
	for _, item := range notes {
		n, _ := item.(map[string]any)
		fn(n)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("序列化数据文件：%v", err)
	}
	if err := os.WriteFile(dataPath, out, 0o644); err != nil {
		t.Fatalf("写回数据文件：%v", err)
	}
}

// blankAllNotes 把数据文件中全部重叠说明正文改为纯空白。
func blankAllNotes(t *testing.T, dataPath, blank string) {
	t.Helper()
	rewriteNotes(t, dataPath, func(n map[string]any) {
		n["note"] = blank
	})
}

// setupOverlappingShifts 通过真实命令建立一对实际重叠一小时的同岗位班次
// （S001 08:00-16:00、S002 15:00-22:00），建立 S002 时保存一条说明，
// 并在交班班次留下一个未关闭事项后结束交班班次。
func setupOverlappingShifts(t *testing.T, dataPath string, withItem bool) {
	t.Helper()
	runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00").
		ok(t, "建立交班班次")
	runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T15:00:00+08:00", "--end", "2026-10-02T22:00:00+08:00",
		"--note", "建立班次时填写的说明").
		ok(t, "建立接班班次（重叠，带说明）")
	if withItem {
		runCLI(t, dataPath,
			"item-add", "--shift", "S001", "--content", "一号泵压力异常待复核",
			"--severity", "important", "--constraints", "需停电窗口", "--follow", "李四").
			ok(t, "交班班次新增事项")
	}
	runCLI(t, dataPath, "shift-close", "--id", "S001").ok(t, "结束交班班次")
}

// TestCLIHandoverCreateBlankOverlapNoteRejectedThenNoteAdd：数据里只有正文
// 空白的重叠说明记录时，首次发起交接必须失败；note-add 追加非空说明后重新
// 发起成功，空记录保留，事项归属与数据文件在失败前后不变。
func TestCLIHandoverCreateBlankOverlapNoteRejectedThenNoteAdd(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	setupOverlappingShifts(t, dataPath, true)

	// 把已保存说明的正文改写成纯空白：记录仍关联 S001/S002，但没有任何有效内容。
	blankAllNotes(t, dataPath, "   \n\t ")

	// 失败前记录事项所在班次，供失败后对照。
	journeyBefore := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败前查看事项")
	if !strings.Contains(journeyBefore, "当前班次=S001") {
		t.Fatalf("事项失败前应在交班班次 S001，got:\n%s", journeyBefore)
	}
	before := hashFile(t, dataPath)

	// 实际重叠、交班已结束、接班未结束，但只有空白说明：必须明确失败。
	res := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	res.failed(t, "只有空白重叠说明时发起交接", "重叠说明")
	if !strings.Contains(res.stderr, "S001") || !strings.Contains(res.stderr, "S002") {
		t.Fatalf("错误应指出两班编号 S001 与 S002，got %q", res.stderr)
	}

	// 不得留下交接记录。
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "暂无交接记录") {
		t.Fatalf("失败后不应有交接记录，got:\n%s", list)
	}
	// 失败不得写盘改变任何数据，也不得改变事项归属。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("失败的发起不得改变数据文件：before=%s after=%s", before, after)
	}
	journeyAfter := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查看事项")
	if !strings.Contains(journeyAfter, "当前班次=S001") {
		t.Fatalf("失败后事项应仍属于交班班次 S001，got:\n%s", journeyAfter)
	}

	// 用户通过现有 note-add 为这两个班次追加非空说明；空记录不删除、不覆盖。
	runCLI(t, dataPath,
		"note-add", "--a", "S001", "--b", "S002", "--note", "抢修并行一小时").
		ok(t, "追加有效重叠说明")
	noteList := runCLI(t, dataPath, "note-list", "--id", "S001").ok(t, "查看说明清单")
	if !strings.Contains(noteList, "抢修并行一小时") {
		t.Fatalf("说明清单应包含新追加的有效说明，got:\n%s", noteList)
	}
	// 两条记录都还在：原空记录保留可查（仍显示该记录编号 N001）。
	if strings.Count(noteList, "说明：") != 2 {
		t.Fatalf("原空记录与新说明都应保留，共 2 条，got:\n%s", noteList)
	}
	if !strings.Contains(noteList, "N001") || !strings.Contains(noteList, "N002") {
		t.Fatalf("应同时看到原记录 N001 与新记录 N002，got:\n%s", noteList)
	}

	// 追加有效说明后重新发起交接：成功，且清单包含原未关闭事项。
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").ok(t, "补填说明后发起交接")
	if !strings.Contains(out, "已发起交接") || !strings.Contains(out, "H001") {
		t.Fatalf("应输出已发起交接 H001，got:\n%s", out)
	}
	if !strings.Contains(out, "S001 -> S002") || !strings.Contains(out, "I001") {
		t.Fatalf("交接应保留两班关系与原清单事项，got:\n%s", out)
	}
}

// TestCLIHandoverCreateBlankOverlapNoteEmptyList：即使交班班次没有未关闭事项，
// 重叠班次的首次空清单交接同样必须先有有效重叠说明。
func TestCLIHandoverCreateBlankOverlapNoteEmptyList(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	setupOverlappingShifts(t, dataPath, false)
	blankAllNotes(t, dataPath, " \t ")
	before := hashFile(t, dataPath)

	res := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	res.failed(t, "空清单交接只有空白说明", "重叠说明")
	if !strings.Contains(res.stderr, "S001") || !strings.Contains(res.stderr, "S002") {
		t.Fatalf("错误应指出两班编号，got %q", res.stderr)
	}
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "暂无交接记录") {
		t.Fatalf("空清单失败后也不应留下交接，got:\n%s", list)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("失败不得改变数据文件：before=%s after=%s", before, after)
	}

	// 补填说明后空清单交接在发起时即完成。
	runCLI(t, dataPath,
		"note-add", "--a", "S001", "--b", "S002", "--note", "两班实际重叠一小时").
		ok(t, "追加有效重叠说明")
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").ok(t, "空清单交接补填后发起")
	if !strings.Contains(out, "已发起交接") || !strings.Contains(out, "已完成") {
		t.Fatalf("空清单交接应在发起时即完成，got:\n%s", out)
	}
}
