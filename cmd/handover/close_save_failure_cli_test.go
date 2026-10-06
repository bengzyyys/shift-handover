package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“接班人关闭已接收事项（item-close）在
// 本地数据保存阶段失败”建立端到端回归保障：上一班已结束并留下结束时记录，
// 事项已确认接收到仍在进行中的接班班次（接班后还被修改过），接班人填写操作
// 人关闭它时，数据文件的临时写入路径被占用（保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已关闭事项”的成功提示，也不展示尚未保存的关闭状态；
//   - 数据文件一个字节不变；item-show、shift-show（当前班与上一班结束时
//     记录）、handover-show 都仍是失败前的全部信息：事项仍未关闭、关闭
//     人和关闭时间未记录，原有的建立、修改、确认接收经过保留，不出现这次
//     失败产生的关闭经过，同班次其他事项不受影响；
//   - 恢复保存后对同一事项再次填写操作人关闭应成功，只追加这次成功关闭的
//     操作人和时间；上一班结束时记录仍显示当时未关闭，原交接仍保留确认
//     接收结果；重复关闭与空白操作人都被明确拒绝，不覆盖原关闭信息。

// 失败尝试与成功关闭分别使用独特的操作人姓名，便于在输出中精确核对失败的
// 操作人没有留下任何痕迹。
const (
	closeFailOperator = "关闭失败操作人甲"
	closeOKOperator   = "关闭成功操作人乙"
)

// setupReceivedItemForClose 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）-> S002（李四，夜班，进行中）；S001 有一项
// 当时未关闭事项 I001（经 H001 确认接收到 S002，接班后被修改）和一项结束前
// 已关闭事项 I002；S002 另有一项未关闭事项 I003。
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
	must("新增未关闭事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "上一班遗留的压力异常",
		"--severity", "normal", "--constraints", "原限制条件：需停电窗口", "--follow", "李四"))
	must("新增上一班已关闭事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "上一班已关闭的巡检事项",
		"--severity", "important", "--constraints", "", "--follow", "李四"))
	must("上一班关闭 I002", runCLI(t, dataPath,
		"item-close", "--id", "I002", "--operator", "张三"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	must("接班后修改 I001", runCLI(t, dataPath,
		"item-update", "--id", "I001",
		"--content", "接班后修订的压力异常", "--severity", "urgent",
		"--constraints", "接班后新增限制", "--follow", "赵六"))
	must("接班班次新增另一事项", runCLI(t, dataPath,
		"item-add", "--shift", "S002", "--content", "接班班次的其他事项",
		"--severity", "normal", "--constraints", "", "--follow", "孙九"))
	return dataPath
}

// assertCLICloseRolledBack 逐项核对：失败的关闭没有在任何查询视图中留下痕迹。
func assertCLICloseRolledBack(t *testing.T, dataPath string) {
	t.Helper()

	// 凭事项编号查询：仍未关闭、关闭人与关闭时间未记录；保留建立、确认接收
	// 与接班后的修改，不出现失败的关闭经过或失败尝试填写的操作人。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查询事项")
	for _, want := range []string{
		"[未关闭]",
		"接班后修订的压力异常", // 接班后修改的内容仍是当前信息
		"接班后新增限制",
		"赵六",
		"确认接收",
		"当前班次=S002",
		"原始班次=S001",
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后 item-show 应保留失败前信息 %q，got:\n%s", want, show)
		}
	}
	for _, unwanted := range []string{
		"[已关闭",
		"关闭 操作人=",
		closeFailOperator,
	} {
		if strings.Contains(show, unwanted) {
			t.Fatalf("失败后 item-show 不应出现关闭信息或失败操作人 %q，got:\n%s", unwanted, show)
		}
	}
	// 处理经过应保留确认接收与一次修改，不混入关闭记录。
	if !strings.Contains(show, "确认接收 操作人=李四") || !strings.Contains(show, "修改：") {
		t.Fatalf("失败后 item-show 应保留接收与修改经过，got:\n%s", show)
	}

	// 当前班次报告：I001 继续显示为未关闭，同班次 I003 也仍未关闭。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看当前班次报告")
	if !strings.Contains(repB, "接班后修订的压力异常") || !strings.Contains(repB, "[未关闭]") {
		t.Fatalf("失败后当前班次报告应把 I001 显示为未关闭，got:\n%s", repB)
	}
	if strings.Contains(repB, closeFailOperator) || strings.Contains(repB, "已关闭（"+closeFailOperator) {
		t.Fatalf("失败后当前班次报告不应出现失败关闭的操作人，got:\n%s", repB)
	}
	if !strings.Contains(repB, "接班班次的其他事项") {
		t.Fatalf("失败后同班次其他事项应保持原状，got:\n%s", repB)
	}

	// 上一班结束时记录：I001 当时未关闭、原文与原负责人保留；I002 仍为
	// 当时已关闭（张三）；失败尝试不改写任何冻结信息。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看上一班结束时记录")
	if !strings.Contains(repA, "结束时记录") ||
		!strings.Contains(repA, "上一班遗留的压力异常") ||
		!strings.Contains(repA, "结束时未关闭") ||
		!strings.Contains(repA, "结束时后续负责人：李四") {
		t.Fatalf("失败后上一班结束时记录应保持 I001 当时未关闭的原样，got:\n%s", repA)
	}
	if !strings.Contains(repA, "结束时已关闭（张三") {
		t.Fatalf("失败后上一班已关闭事项 I002 应保持关闭，got:\n%s", repA)
	}
	if strings.Contains(repA, closeFailOperator) ||
		strings.Contains(repA, "接班后修订的压力异常") {
		t.Fatalf("失败尝试不应改写上一班结束时记录，got:\n%s", repA)
	}

	// 交接记录：确认接收结果、接收人李四保留，交接仍完成；失败操作人不出现。
	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	if !strings.Contains(hShow, "确认接收") || !strings.Contains(hShow, "[已完成") {
		t.Fatalf("失败后原交接应仍为已完成的确认接收，got:\n%s", hShow)
	}
	if strings.Contains(hShow, closeFailOperator) {
		t.Fatalf("失败尝试不应在交接记录中留下操作人，got:\n%s", hShow)
	}

	// 同班次其他事项仍未关闭。
	other := runCLI(t, dataPath, "item-show", "--id", "I003").ok(t, "失败后查询同班次其他事项")
	if !strings.Contains(other, "[未关闭]") || !strings.Contains(other, "接班班次的其他事项") {
		t.Fatalf("失败后同班次其他事项应仍未关闭，got:\n%s", other)
	}
}

// TestCLIItemCloseSaveFailureAtomicRollback 是核心保障：保存失败时命令明确
// 报错、不宣称成功、不把未保存的关闭状态当作结果，系统里不留下关闭的任何
// 一部分；恢复保存后的关闭成功并只追加一次成功关闭，重复关闭与空白操作人
// 都被拒绝。
func TestCLIItemCloseSaveFailureAtomicRollback(t *testing.T) {
	dataPath := setupReceivedItemForClose(t)

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 接班人填写操作人关闭已接收事项，本地数据保存失败。
	r := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", closeFailOperator)
	r.failed(t, "保存失败时关闭事项", "写入数据文件失败")
	// 不显示“已关闭事项”的成功提示，正常输出与错误输出都不能宣称成功或
	// 展示尚未保存的关闭状态/失败操作人。
	if strings.Contains(r.stdout, "已关闭事项") || strings.Contains(r.stderr, "已关闭事项") {
		t.Fatalf("保存失败不得显示成功提示：stdout=%q stderr=%q", r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, closeFailOperator) {
		t.Fatalf("保存失败不得展示尚未保存的关闭状态：stdout=%q", r.stdout)
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 每次查询都是独立进程重新打开同一数据文件，仍应看到失败前的全部信息。
	assertCLICloseRolledBack(t, dataPath)

	// 恢复正常保存后，接班人再对同一事项填写操作人关闭：按现有功能成功。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", closeOKOperator).
		ok(t, "恢复保存后关闭事项")
	if !strings.Contains(out, "已关闭事项") || !strings.Contains(out, closeOKOperator) {
		t.Fatalf("成功关闭应显示成功提示与本次操作人，got:\n%s", out)
	}
	if strings.Contains(out, closeFailOperator) {
		t.Fatalf("成功关闭的输出不应出现失败尝试的操作人，got:\n%s", out)
	}

	// 事项查询显示已关闭，关闭人与关闭时间属于成功操作，处理经过只追加这
	// 一次关闭；失败尝试的操作人与时刻无痕迹，原接收与修改经过保留。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "成功后查询事项")
	if !strings.Contains(show, "已关闭（"+closeOKOperator) ||
		!strings.Contains(show, "关闭 操作人="+closeOKOperator) {
		t.Fatalf("成功后 item-show 应显示成功关闭的操作人与时间，got:\n%s", show)
	}
	if !strings.Contains(show, "确认接收 操作人=李四") || !strings.Contains(show, "修改：") {
		t.Fatalf("成功后 item-show 应保留原接收与修改经过，got:\n%s", show)
	}
	if strings.Contains(show, closeFailOperator) {
		t.Fatalf("成功后 item-show 不应出现失败尝试的操作人，got:\n%s", show)
	}
	if n := strings.Count(show, "关闭 操作人="); n != 1 {
		t.Fatalf("处理经过应只记录一次关闭，got %d 次：\n%s", n, show)
	}

	// 当前班次报告显示 I001 已关闭，同班次 I003 仍未关闭。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "成功后查看当前班次报告")
	if !strings.Contains(repB, "已关闭（"+closeOKOperator) {
		t.Fatalf("成功后当前班次报告应显示 I001 已关闭，got:\n%s", repB)
	}
	if !strings.Contains(repB, "接班班次的其他事项") {
		t.Fatalf("成功后同班次其他事项应仍在，got:\n%s", repB)
	}

	// 上一班结束时记录仍显示 I001 当时未关闭；最新状态对照才反映已关闭。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "成功后查看上一班结束时记录")
	if !strings.Contains(repA, "结束时未关闭") ||
		!strings.Contains(repA, "上一班遗留的压力异常") {
		t.Fatalf("上一班结束时记录应仍显示 I001 当时未关闭，got:\n%s", repA)
	}
	if !strings.Contains(repA, "最新关闭情况=已关闭（"+closeOKOperator) {
		t.Fatalf("上一班报告的最新状态对照应反映成功关闭，got:\n%s", repA)
	}

	// 原交接仍保留当时的确认接收结果与接收人。
	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "成功后查看交接")
	if !strings.Contains(hShow, "确认接收") || !strings.Contains(hShow, "处理人=李四") {
		t.Fatalf("原交接应保留确认接收结果与接收人，got:\n%s", hShow)
	}

	// 已成功关闭的事项再次关闭：非零退出、正常输出为空、明确指出已关闭，
	// 不覆盖原关闭人或时间，也不多记一条关闭经过。使用与事项负责人不同的
	// 独特姓名，便于确认它没有写进任何字段。
	repeat := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "重复关闭操作者")
	repeat.failed(t, "重复关闭已关闭事项", "已关闭，不能重复关闭")
	afterRepeat := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重复关闭后查询事项")
	if !strings.Contains(afterRepeat, "已关闭（"+closeOKOperator) ||
		strings.Contains(afterRepeat, "重复关闭操作者") {
		t.Fatalf("重复关闭不得覆盖原关闭人，got:\n%s", afterRepeat)
	}
	if n := strings.Count(afterRepeat, "关闭 操作人="); n != 1 {
		t.Fatalf("重复关闭不得多记关闭经过，got %d 次：\n%s", n, afterRepeat)
	}

	// 操作人只有空白：同样非零退出、正常输出为空、明确报操作人不能为空，
	// 原关闭状态保持。
	blank := runCLI(t, dataPath, "item-close", "--id", "I001", "--operator", "   ")
	blank.failed(t, "空白操作人关闭", "操作人不能为空")
	afterBlank := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "空白操作人后查询事项")
	if !strings.Contains(afterBlank, "已关闭（"+closeOKOperator) {
		t.Fatalf("空白操作人被拒绝后原关闭状态应保持，got:\n%s", afterBlank)
	}
	if n := strings.Count(afterBlank, "关闭 操作人="); n != 1 {
		t.Fatalf("空白操作人不得多记关闭经过，got %d 次：\n%s", n, afterBlank)
	}
}
