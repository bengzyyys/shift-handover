package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“接班人关闭已接收事项（item-close）在
// 本地数据保存阶段失败”建立端到端回归保障：上一班已结束并留下结束时记录，
// 事项经已完成的交接确认接收到仍在进行中的接班班次（接班后还修改过一次），
// 接班人填写操作人关闭时数据文件的临时写入路径被占用（保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已关闭事项”的成功提示；
//   - 数据文件一个字节不变；item-show 仍显示该事项未关闭、无关闭人和关闭
//     时间，处理经过保留建立、接收与修改而不出现这次失败的关闭；shift-show
//     接班班次继续把它显示为未关闭，上一班结束时记录仍是当时未关闭的原样，
//     handover-show 仍是已完成的确认接收；
//   - 保存恢复后再次关闭成功，只追加这一次关闭；上一班结束时记录仍显示当时
//     未关闭；对已关闭事项重复关闭、对未关闭事项填写空白操作人都明确拒绝且
//     不改变原状态。

// setupReceivedItemForClose 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）留有三项——I001、I002 结束时未关闭，I003 结束前
// 已关闭；S001 结束后经 H001 把 I001、I002 交给仍在进行中的 S002（李四），
// 两项均确认接收、交接完成；接班后李四又修改过 I001 的四项信息。
func setupReceivedItemForClose(t *testing.T) string {
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
	must("新增待交接事项 I001", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "二号泵异响待复核",
		"--severity", "normal", "--constraints", "原限制：需停电窗口", "--follow", "李四"))
	must("新增待交接事项 I002", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "交接的另一项未关闭事项",
		"--severity", "normal", "--follow", "李四"))
	must("新增上一班已关闭事项 I003", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "上一班已关闭的巡检事项",
		"--severity", "important", "--constraints", "需复查记录", "--follow", "李四"))
	must("上一班结束前关闭 I003", runCLI(t, dataPath,
		"item-close", "--id", "I003", "--operator", "张三"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	must("确认接收 I002", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "confirm", "--operator", "李四"))
	must("接班后修改 I001 四项", runCLI(t, dataPath,
		"item-update", "--id", "I001", "--content", "接班后修订的二号泵异响",
		"--severity", "urgent", "--constraints", "接班后新增限制", "--follow", "赵六"))
	return dataPath
}

// TestCLIItemCloseSaveFailureAtomicRollback 是核心保障：保存失败时命令明确
// 报错、不宣称成功，系统里不留下关闭的任何一部分，失败前已保存的数据原样
// 保留；恢复保存后关闭成功并只追加一次关闭，重复关闭与空白操作人均被拒绝。
func TestCLIItemCloseSaveFailureAtomicRollback(t *testing.T) {
	dataPath := setupReceivedItemForClose(t)

	// 关闭前 I001 确为未关闭，且位于接班班次。
	beforeShow := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "关闭前查询 I001")
	if !strings.Contains(beforeShow, "[未关闭]") ||
		!strings.Contains(beforeShow, "接班后修订的二号泵异响") {
		t.Fatalf("前置：I001 应未关闭且为接班后修改的内容，got:\n%s", beforeShow)
	}

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 接班人填写操作人关闭 I001，本地数据保存失败。
	r := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "李四")
	r.failed(t, "保存失败时关闭事项", "写入数据文件失败")
	// 正常输出与错误输出都不能出现“已关闭事项”的成功提示。
	if strings.Contains(r.stdout, "已关闭事项") || strings.Contains(r.stderr, "已关闭事项") {
		t.Fatalf("保存失败不得显示成功提示：stdout=%q stderr=%q", r.stdout, r.stderr)
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// item-show：仍是未关闭、无关闭人和关闭时间，保留建立、接收与修改经过，
	// 不出现这次失败的关闭。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查询 I001")
	for _, want := range []string{
		"[未关闭]",
		"接班后修订的二号泵异响",
		"接班后新增限制",
		"赵六",
		"当前班次=S002",
		"原始班次=S001",
		"事项建立",
		"确认接收",
		"修改：",
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后 item-show 应保留失败前信息与经过 %q，got:\n%s", want, show)
		}
	}
	for _, unwanted := range []string{"关闭 操作人=", "已关闭（", "关闭人"} {
		if strings.Contains(show, unwanted) {
			t.Fatalf("失败后 item-show 不应出现失败的关闭记录 %q，got:\n%s", unwanted, show)
		}
	}

	// 接班班次报告继续把 I001 显示为未关闭，班次仍进行中，I002 也保持未关闭。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看接班班次报告")
	if !strings.Contains(repB, "[进行中]") ||
		!strings.Contains(repB, "接班后修订的二号泵异响") {
		t.Fatalf("失败后接班班次报告应仍进行中并展示 I001 当前信息，got:\n%s", repB)
	}
	if strings.Contains(repB, "结束时记录") || strings.Contains(repB, "已关闭（李四 于") {
		t.Fatalf("失败后接班班次不应出现结束时记录或失败的关闭，got:\n%s", repB)
	}

	// 上一班结束时记录仍是当时的原样：I001 当时未关闭、原文原负责人，
	// I003 当时已关闭；不出现接班后修改的内容。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看上一班结束时记录")
	for _, want := range []string{
		"结束时记录",
		"二号泵异响待复核",
		"结束时未关闭",
		"结束时已关闭（张三 于",
		"上一班已关闭的巡检事项",
	} {
		if !strings.Contains(repA, want) {
			t.Fatalf("失败后上一班结束时记录应保留 %q，got:\n%s", want, repA)
		}
	}
	if strings.Contains(repA, "接班后修订的二号泵异响") {
		t.Fatalf("失败后上一班结束时记录的冻结内容不应被接班后的修改改写，got:\n%s", repA)
	}

	// 原交接仍已完成，I001 的确认接收结果保持原样。
	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	if !strings.Contains(hShow, "[已完成") ||
		!strings.Contains(hShow, "二号泵异响待复核") ||
		!strings.Contains(hShow, "确认接收") {
		t.Fatalf("失败后原交接应仍已完成且保留确认接收，got:\n%s", hShow)
	}

	// 保存恢复后，接班人再次关闭 I001 应成功，输出成功提示与本次关闭事实。
	restoreCLISaving(t, dataPath)
	closed := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "李四")
	closed.ok(t, "恢复保存后关闭 I001")
	if !strings.Contains(closed.stdout, "已关闭事项") ||
		!strings.Contains(closed.stdout, "已关闭（李四 于") {
		t.Fatalf("成功关闭应显示成功提示与关闭人/时间，got:\n%s", closed.stdout)
	}

	// item-show 与接班班次报告显示已关闭，只新增一次关闭经过，原编号、四项
	// 信息与接收/修改历史保留。
	show2 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "成功后查询 I001")
	for _, want := range []string{
		"已关闭（李四 于",
		"接班后修订的二号泵异响",
		"事项建立",
		"确认接收",
		"修改：",
		"关闭 操作人=李四",
	} {
		if !strings.Contains(show2, want) {
			t.Fatalf("成功后 item-show 应展示已关闭与完整历史 %q，got:\n%s", want, show2)
		}
	}
	if strings.Count(show2, "关闭 操作人=") != 1 {
		t.Fatalf("应只追加一次关闭经过，got:\n%s", show2)
	}
	repB2 := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "成功后查看接班班次报告")
	if !strings.Contains(repB2, "已关闭（李四 于") ||
		!strings.Contains(repB2, "接班后修订的二号泵异响") {
		t.Fatalf("接班班次报告应显示 I001 已关闭，got:\n%s", repB2)
	}

	// 上一班结束时记录仍显示 I001 当时未关闭，不随后班关闭改变。
	repA2 := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "成功后查看上一班结束时记录")
	if !strings.Contains(repA2, "二号泵异响待复核") ||
		!strings.Contains(repA2, "结束时未关闭") ||
		strings.Contains(repA2, "接班后修订的二号泵异响") {
		t.Fatalf("上一班结束时记录应仍显示 I001 当时未关闭，got:\n%s", repA2)
	}
	// 原交接仍保留当时的确认接收结果。
	hShow2 := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "成功后查看交接")
	if !strings.Contains(hShow2, "[已完成") || !strings.Contains(hShow2, "确认接收") {
		t.Fatalf("原交接应保持已完成的确认接收，got:\n%s", hShow2)
	}

	// 对已成功关闭的 I001 再次关闭：明确拒绝，不覆盖原关闭人或时间，也不多记
	// 一条关闭经过。
	dup := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "王五")
	dup.failed(t, "重复关闭已关闭事项", "不能重复关闭")
	if strings.Contains(dup.stderr, "写入数据文件失败") {
		t.Fatalf("重复关闭应是业务状态拒绝，不应冒充保存失败：%q", dup.stderr)
	}
	show3 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重复关闭被拒后查询 I001")
	if strings.Count(show3, "关闭 操作人=") != 1 ||
		!strings.Contains(show3, "关闭 操作人=李四") ||
		strings.Contains(show3, "关闭 操作人=王五") {
		t.Fatalf("重复关闭不得覆盖原关闭人或多记关闭经过，got:\n%s", show3)
	}

	// 操作人为空或只有空白：明确拒绝并保留原状态。对仍未关闭的 I002 用纯
	// 空白操作人关闭，应被拒绝，I002 仍未关闭且不产生关闭经过。
	blank := runCLI(t, dataPath, "item-close", "--id", "I002", "--operator", "   ")
	blank.failed(t, "空白操作人关闭未关闭事项", "操作人不能为空")
	showOther := runCLI(t, dataPath, "item-show", "--id", "I002").ok(t, "空白操作人被拒后查询 I002")
	if !strings.Contains(showOther, "[未关闭]") ||
		strings.Contains(showOther, "关闭 操作人=") {
		t.Fatalf("空白操作人被拒绝后 I002 应保持未关闭且无关闭经过，got:\n%s", showOther)
	}
	// 对已关闭的 I001 用空白操作人也应先按输入不合法拒绝，原关闭事实不变。
	blankClosed := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "  ")
	blankClosed.failed(t, "空白操作人关闭已关闭事项", "操作人不能为空")
	show4 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "空白操作人被拒后查询 I001")
	if strings.Count(show4, "关闭 操作人=") != 1 ||
		!strings.Contains(show4, "关闭 操作人=李四") {
		t.Fatalf("空白操作人被拒绝后 I001 原关闭事实应保持，got:\n%s", show4)
	}
}
