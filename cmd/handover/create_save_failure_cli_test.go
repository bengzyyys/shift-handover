package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“首次发起交接（handover-create）在
// 本地数据保存阶段失败”建立端到端回归保障：交班班次已结束、接班班次符合
// 首次发起的全部条件，且该交班班次此前没有发起过交接，而数据文件的临时
// 写入路径被占用（保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不输出
//     “已发起交接”的成功提示、未保存的新交接编号、清单或完成信息
//     （空清单交接的完成信息同样不能出现）；
//   - 数据文件一个字节不变；handover-list、handover-show、shift-show 都
//     显示没有为这个交班班次建立交接，事项留在原班次、处理经过里没有发起
//     交接，原先结束时记录与已保存的其他交接不受影响；失败尝试不占用编号；
//   - 恢复保存后再次发起按现有功能成功，输出完整的已保存交接且与
//     handover-show 一致；成功后重复发起仍作为成功展示原记录。
// 带未关闭事项与没有未关闭事项（空清单）的首次发起各覆盖一个用例。

// setupClosedFromShiftForCreate 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）留有两项未关闭事项 I001、I002（一项结束前已
// 关闭的 I003 不进交接清单）；S002（李四，夜班，进行中）符合接班条件。
func setupClosedFromShiftForCreate(t *testing.T) string {
	t.Helper()
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}
	must("建立交班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	must("建立接班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T16:00:00+08:00", "--end", "2026-10-02T23:00:00+08:00"))
	must("新增未关闭事项甲", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "一号泵压力异常待复核",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "李四"))
	must("新增未关闭事项乙", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "巡检台账补录",
		"--severity", "normal", "--follow", "李四"))
	must("新增并关闭事项丙", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "结束前已处理事项",
		"--severity", "normal", "--follow", "李四"))
	must("结束前关闭事项丙", runCLI(t, dataPath,
		"item-close", "--id", "I003", "--operator", "张三"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	return dataPath
}

// assertCLICreateRolledBack 逐项核对：失败的发起没有在任何查询视图中留下
// 交接痕迹，事项仍留在交班班次。
func assertCLICreateRolledBack(t *testing.T, dataPath string) {
	t.Helper()

	// 交接列表显示没有任何交接，不出现失败尝试的编号。
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "（暂无交接记录）") {
		t.Fatalf("失败后交接列表应显示暂无交接记录，got:\n%s", list)
	}
	if strings.Contains(list, "H001") {
		t.Fatalf("失败尝试不应占用或显示交接编号 H001，got:\n%s", list)
	}

	// 直接查询失败尝试使用的编号：明确不存在，而不是返回一份未保存记录。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001")
	if show.exitCode == 0 {
		t.Fatalf("失败后 H001 不应能查到，stdout=%q", show.stdout)
	}
	if !strings.Contains(show.stderr, "不存在") || show.stdout != "" {
		t.Fatalf("查询 H001 应报记录不存在且正常输出为空，stderr=%q stdout=%q",
			show.stderr, show.stdout)
	}

	// 交班班次报告：交班对象显示尚未发起交接；事项仍在结束时记录中且当时
	// 未关闭；接班班次报告里没有接班交接。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看交班班次报告")
	if !strings.Contains(repA, "（尚未发起交接）") {
		t.Fatalf("失败后交班班次报告应显示尚未发起交接，got:\n%s", repA)
	}
	if strings.Contains(repA, "H001") || strings.Contains(repA, "S001 -> S002") {
		t.Fatalf("失败后交班班次报告不应出现交接编号或接班关系，got:\n%s", repA)
	}
	if !strings.Contains(repA, "结束时未关闭") ||
		!strings.Contains(repA, "一号泵压力异常待复核") ||
		!strings.Contains(repA, "巡检台账补录") {
		t.Fatalf("交班班次结束时记录应保留未关闭事项原样，got:\n%s", repA)
	}
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看接班班次报告")
	if !strings.Contains(repB, "接班交接：") || !strings.Contains(repB, "（无）") {
		t.Fatalf("失败后接班班次不应有接班交接，got:\n%s", repB)
	}
	if strings.Contains(repB, "H001") {
		t.Fatalf("失败后接班班次报告不应出现交接编号，got:\n%s", repB)
	}

	// 事项仍留在交班班次，处理经过里没有发起交接，交接当前结果为暂无。
	for _, id := range []string{"I001", "I002"} {
		it := runCLI(t, dataPath, "item-show", "--id", id).ok(t, "失败后查询事项 "+id)
		if !strings.Contains(it, "当前班次=S001") {
			t.Fatalf("事项 %s 应仍留在交班班次 S001，got:\n%s", id, it)
		}
		if strings.Contains(it, "当前班次=S002") || strings.Contains(it, "发起交接") ||
			strings.Contains(it, "H001") || !strings.Contains(it, "暂无交接记录") {
			t.Fatalf("事项 %s 的处理经过不应出现失败的发起交接：\n%s", id, it)
		}
	}
}

// TestCLICreateHandoverSaveFailureWithPendingItemsAtomicRollback 是非空清单
// 首次发起在保存阶段失败的核心保障：命令明确报错、不宣称成功、不展示未保存
// 的编号/清单；系统里不留下这次发起的任何一部分；恢复后发起成功并返回完整
// 已保存交接，重复发起仍作为成功展示。
func TestCLICreateHandoverSaveFailureWithPendingItemsAtomicRollback(t *testing.T) {
	dataPath := setupClosedFromShiftForCreate(t)
	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 首次发起交接，本地数据保存失败。
	r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	r.failed(t, "保存失败时发起交接", "写入数据文件失败")
	// 正常输出必须为空：不能有成功提示、未保存编号、清单或完成信息。
	if r.stdout != "" {
		t.Fatalf("保存失败时正常输出应为空，got %q", r.stdout)
	}
	if strings.Contains(r.stderr, "已发起交接") || strings.Contains(r.stderr, "H001") {
		t.Fatalf("错误输出不应宣称成功或展示未保存交接：%q", r.stderr)
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 每次查询都是独立进程重新打开同一数据文件，仍应看到失败前的全部信息。
	assertCLICreateRolledBack(t, dataPath)

	// 恢复正常保存后再次发起：按现有功能成功，失败尝试不占用编号。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").
		ok(t, "恢复保存后发起交接")
	for _, want := range []string{
		"已发起交接",
		"H001  岗位=调度  S001 -> S002",
		"[未完成]",
		"I001  [待处理]",
		"I002  [待处理]",
		"等待接班人处理",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功发起输出应包含 %q，got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "I003") {
		t.Fatalf("结束前已关闭事项不应进入交接清单，got:\n%s", out)
	}
	if strings.Contains(out, "已完成") {
		t.Fatalf("有待处理事项的交接发起时不应显示完成，got:\n%s", out)
	}

	// 返回内容与随后的查询一致。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "成功后查询交接")
	if strings.TrimRight(out[strings.Index(out, "\n")+1:], "\n") != strings.TrimRight(show, "\n") {
		t.Fatalf("发起输出的交接内容应与 handover-show 一致\n--- create ---\n%s\n--- show ---\n%s", out, show)
	}
	list := runCLI(t, dataPath, "handover-list").ok(t, "成功后列出交接")
	if strings.Count(list, "H001") != 1 || strings.Contains(list, "H002") {
		t.Fatalf("应只有 H001 一条交接，got:\n%s", list)
	}

	// 保留既有重复发起行为：再次向原接班班次发起仍作为成功展示原记录。
	repeat := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").
		ok(t, "成功后重复发起")
	if !strings.Contains(repeat, "已存在交接记录") || !strings.Contains(repeat, "H001") {
		t.Fatalf("重复发起应作为成功展示原记录，got:\n%s", repeat)
	}
}

// TestCLICreateHandoverSaveFailureEmptyListAtomicRollback 是空清单首次发起
// 在保存阶段失败的保障：失败时不能输出成功提示或空清单交接的完成信息；
// 恢复后发起才是发起即完成的空清单交接。
func TestCLICreateHandoverSaveFailureEmptyListAtomicRollback(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}
	must("建立交班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	must("建立接班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T16:00:00+08:00", "--end", "2026-10-02T23:00:00+08:00"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	r.failed(t, "保存失败时发起空清单交接", "写入数据文件失败")
	if r.stdout != "" {
		t.Fatalf("保存失败时正常输出应为空，got %q", r.stdout)
	}
	// 空清单交接的成功提示与完成信息都不能出现。
	if strings.Contains(r.stderr, "已发起交接") || strings.Contains(r.stderr, "已完成") ||
		strings.Contains(r.stderr, "空清单") {
		t.Fatalf("错误输出不应展示未保存的空清单交接及其完成信息：%q", r.stderr)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 列表与班次报告仍显示没有建立交接。
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "（暂无交接记录）") || strings.Contains(list, "H001") {
		t.Fatalf("失败后不应有任何交接，got:\n%s", list)
	}
	rep := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看交班班次报告")
	if !strings.Contains(rep, "（尚未发起交接）") || strings.Contains(rep, "H001") {
		t.Fatalf("失败后交班班次报告应显示尚未发起交接，got:\n%s", rep)
	}

	// 恢复后发起：空清单交接发起即完成，编号仍是 H001。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").
		ok(t, "恢复保存后发起空清单交接")
	for _, want := range []string{"已发起交接", "H001", "S001 -> S002", "[已完成", "（空清单，直接完成）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功发起空清单交接输出应包含 %q，got:\n%s", want, out)
		}
	}
}
