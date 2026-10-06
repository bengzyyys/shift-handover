package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“首次发起交接（handover-create）在
// 本地数据保存阶段失败”建立端到端回归保障：交班班次已结束、接班班次符合
// 首次发起的现有条件、此前没有发起交接，而数据文件的临时写入路径被占用
// （保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已发起交接”的成功提示，也不展示尚未保存的清单或完成信息；
//   - 数据文件一个字节不变；handover-list、shift-show（交班与接班班次）、
//     item-show 都仍是失败前的全部信息：没有为这个交班班次建立交接，事项
//     留在原班次，交班班次的结束时记录不受影响；
//   - 恢复保存后重新发起应成功并取得 H001（失败尝试不占用交接编号），
//     输出完整交接内容；之后的重复发起仍按既有规则作为成功展示已有记录。
// 空清单交接同样覆盖：失败时不输出完成信息，恢复后发起时才完成。

// setupReadyForHandoverCreate 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束，留有未关闭事项 I001、I002 与已关闭事项 I003）
// -> S002（李四，夜班，进行中）；尚未发起交接。
func setupReadyForHandoverCreate(t *testing.T) string {
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
	must("新增未关闭事项一", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "一号泵压力异常待复核",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "李四"))
	must("新增未关闭事项二", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "巡检台账补录",
		"--severity", "normal", "--follow", "李四"))
	must("新增并关闭事项三", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "交接前已处理完的事项",
		"--severity", "normal", "--follow", "李四"))
	must("交班前关闭 I003", runCLI(t, dataPath, "item-close", "--id", "I003", "--operator", "张三"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	return dataPath
}

// assertCLICreateRolledBack 逐项核对：失败的发起没有在任何查询视图中留下痕迹。
func assertCLICreateRolledBack(t *testing.T, dataPath string) {
	t.Helper()

	// 交接列表：没有为这个交班班次建立的交接。
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "暂无交接记录") || strings.Contains(list, "H001") {
		t.Fatalf("失败后交接列表应显示暂无交接记录，got:\n%s", list)
	}

	// 交班班次报告：尚未发起交接；结束时记录保持原样（I001/I002 当时未关闭、
	// I003 当时已关闭）。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看交班班次报告")
	for _, want := range []string{
		"（尚未发起交接）",
		"结束时记录",
		"一号泵压力异常待复核", "结束时未关闭",
		"结束时已关闭（张三",
	} {
		if !strings.Contains(repA, want) {
			t.Fatalf("失败后交班班次报告应保留失败前信息 %q，got:\n%s", want, repA)
		}
	}
	if strings.Contains(repA, "H001") {
		t.Fatalf("失败后交班班次报告不应出现任何交接编号，got:\n%s", repA)
	}

	// 接班班次报告：没有接班交接。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看接班班次报告")
	if strings.Contains(repB, "H001") {
		t.Fatalf("失败后接班班次报告不应出现任何交接，got:\n%s", repB)
	}

	// 事项留在原班次，处理经过中没有发起交接。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查询事项")
	for _, want := range []string{"[未关闭]", "当前班次=S001", "原始班次=S001", "暂无交接记录"} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后 item-show 应保留失败前信息 %q，got:\n%s", want, show)
		}
	}
	if strings.Contains(show, "发起交接") || strings.Contains(show, "H001") {
		t.Fatalf("失败后 item-show 不应出现发起交接经过或交接编号，got:\n%s", show)
	}
}

// TestCLICreateHandoverSaveFailureAtomicRollback 是核心保障：保存失败时命令
// 明确报错、不宣称成功、不展示尚未保存的新交接，系统里不留下这份交接的任何
// 一部分；恢复保存后重新发起成功并取得 H001，重复发起仍作为成功展示已有记录。
func TestCLICreateHandoverSaveFailureAtomicRollback(t *testing.T) {
	dataPath := setupReadyForHandoverCreate(t)

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 交班班次已结束、接班班次符合条件、此前没有发起交接；本地数据保存失败。
	r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	r.failed(t, "保存失败时发起交接", "写入数据文件失败")
	// 不显示“已发起交接”的成功提示，正常输出与错误输出都不能展示尚未保存的
	// 编号、清单或完成信息。
	for _, unwanted := range []string{"已发起交接", "H001", "待处理", "已完成"} {
		if strings.Contains(r.stdout, unwanted) || strings.Contains(r.stderr, unwanted) {
			t.Fatalf("保存失败不得显示成功提示或未保存内容 %q：stdout=%q stderr=%q",
				unwanted, r.stdout, r.stderr)
		}
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 每次查询都是独立进程重新打开同一数据文件，仍应看到失败前的全部信息。
	assertCLICreateRolledBack(t, dataPath)

	// 恢复正常保存后重新发起：按现有功能成功，失败尝试不占用交接编号。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").
		ok(t, "恢复保存后发起交接")
	for _, want := range []string{
		"已发起交接", "H001", "S001 -> S002", "共2项",
		"I001  [待处理]", "I002  [待处理]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功发起应展示完整交接内容 %q，got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "I003") || strings.Contains(out, "已完成") {
		t.Fatalf("已关闭事项不应进入清单，有事项的清单不应在发起时完成，got:\n%s", out)
	}

	// 交接列表与班次报告都显示这份成功保存的交接。
	list := runCLI(t, dataPath, "handover-list").ok(t, "成功后列出交接")
	if strings.Count(list, "H001") != 1 {
		t.Fatalf("成功后应恰有一份 H001 交接，got:\n%s", list)
	}
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "成功后查看交班班次报告")
	if !strings.Contains(repA, "H001") || strings.Contains(repA, "（尚未发起交接）") {
		t.Fatalf("成功后交班班次报告应显示已发起的交接，got:\n%s", repA)
	}

	// 保留已有的重复发起行为：命令行继续把它作为成功展示已有记录。
	rep := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	repOut := rep.ok(t, "重复发起")
	if !strings.Contains(repOut, "已存在交接记录") || !strings.Contains(repOut, "H001") {
		t.Fatalf("重复发起应作为成功展示已有记录，got:\n%s", repOut)
	}
}

// TestCLICreateHandoverSaveFailureEmptyList 覆盖空清单交接：保存失败时命令
// 明确报错、不输出新交接的成功提示或完成信息；恢复保存后发起成功，空清单在
// 发起时完成。
func TestCLICreateHandoverSaveFailureEmptyList(t *testing.T) {
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
	r.failed(t, "保存失败时空清单发起交接", "写入数据文件失败")
	// 空清单交接也不能输出完成信息。
	for _, unwanted := range []string{"已发起交接", "H001", "已完成", "空清单"} {
		if strings.Contains(r.stdout, unwanted) || strings.Contains(r.stderr, unwanted) {
			t.Fatalf("保存失败不得显示成功提示或完成信息 %q：stdout=%q stderr=%q",
				unwanted, r.stdout, r.stderr)
		}
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if !strings.Contains(list, "暂无交接记录") {
		t.Fatalf("失败后交接列表应显示暂无交接记录，got:\n%s", list)
	}

	// 恢复保存后发起成功：空清单在发起时完成。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").
		ok(t, "恢复保存后空清单发起交接")
	for _, want := range []string{"已发起交接", "H001", "共0项", "（空清单，直接完成）", "已完成"} {
		if !strings.Contains(out, want) {
			t.Fatalf("空清单发起成功应展示完成信息 %q，got:\n%s", want, out)
		}
	}
}
