package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“修改事项（item-update）在本地数据
// 保存阶段失败”建立端到端回归保障：上一班已结束、事项已确认接收到仍在进行
// 中的下一班，用户通过现有修改功能同时改动内容、严重程度、限制条件和后续
// 负责人四项，而数据文件的临时写入路径被占用（保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已修改事项”的成功提示，也不展示尚未保存的新值；
//   - 数据文件一个字节不变；item-show、shift-show（当前班与上一班结束时
//     记录）、handover-show 都仍是失败前的全部信息，不出现内容已变而
//     负责人未变、原限制条件被清空这类半完成状态；
//   - 恢复保存后对同一事项再次提交同样的修改应成功，查询展示四项新信息，
//     而上一班结束时记录与交接清单中的事项原文不被新值替换。

// setupReceivedItemForUpdate 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）-> S002（李四，夜班，进行中）；I001 在 S001 建立
// （内容、严重程度、非空限制条件、后续负责人齐全），经 H001 确认接收到 S002。
func setupReceivedItemForUpdate(t *testing.T) string {
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
	must("新增事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "原内容：一号泵压力异常",
		"--severity", "normal", "--constraints", "原限制条件：需停电窗口", "--follow", "李四"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	return dataPath
}

// breakCLISaving 在数据文件的临时写入路径上放一个目录，使原子保存必然失败；
// 与是否以 root 运行无关，测试可在任意机器上独立重复。
func breakCLISaving(t *testing.T, dataPath string) {
	t.Helper()
	if err := os.Mkdir(dataPath+".tmp", 0o755); err != nil {
		t.Fatalf("制造保存失败：%v", err)
	}
}

// restoreCLISaving 移除故障路径，使保存恢复正常。
func restoreCLISaving(t *testing.T, dataPath string) {
	t.Helper()
	if err := os.RemoveAll(dataPath + ".tmp"); err != nil {
		t.Fatalf("恢复保存：%v", err)
	}
}

// updateArgs 是同时修改四项信息的完整参数。
func updateArgs() []string {
	return []string{
		"item-update", "--id", "I001",
		"--content", "新内容：一号泵压力已复核",
		"--severity", "urgent",
		"--constraints", "新限制条件：夜间禁动",
		"--follow", "王五",
	}
}

// assertCLIUpdateRolledBack 逐项核对：失败的修改没有在任何查询视图中留下痕迹。
func assertCLIUpdateRolledBack(t *testing.T, dataPath string) {
	t.Helper()

	// 凭事项编号查询：仍是失败前的全部信息，没有这次修改的记录。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查询事项")
	for _, want := range []string{"原内容：一号泵压力异常", "原限制条件：需停电窗口", "李四", "确认接收"} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后 item-show 应仍显示失败前信息 %q，got:\n%s", want, show)
		}
	}
	for _, unwanted := range []string{"已复核", "夜间禁动", "王五", "修改："} {
		if strings.Contains(show, unwanted) {
			t.Fatalf("失败后 item-show 不应出现未保存的新值或修改记录 %q，got:\n%s", unwanted, show)
		}
	}

	// 当前班次报告与单项查询一致（仍是失败前的值）。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看当前班次报告")
	if !strings.Contains(repB, "原内容：一号泵压力异常") || strings.Contains(repB, "已复核") {
		t.Fatalf("失败后当前班次报告应仍是失败前信息，got:\n%s", repB)
	}

	// 上一班的结束时记录继续保留当时的内容、限制条件和负责人。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看上一班结束时记录")
	if !strings.Contains(repA, "原内容：一号泵压力异常") || !strings.Contains(repA, "原限制条件：需停电窗口") ||
		strings.Contains(repA, "已复核") || strings.Contains(repA, "夜间禁动") {
		t.Fatalf("失败后上一班结束时记录应保持当时内容，got:\n%s", repA)
	}

	// 交接清单中的事项原文与确认接收结果不受修改失败影响。
	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	if !strings.Contains(hShow, "原内容：一号泵压力异常") || !strings.Contains(hShow, "确认接收") ||
		strings.Contains(hShow, "已复核") {
		t.Fatalf("失败后交接记录应保持接收时原文与结果，got:\n%s", hShow)
	}
}

// TestCLIItemUpdateSaveFailureAtomicRollback 是核心保障：保存失败时命令明确
// 报错、不宣称成功、不把未保存的新值当作修改结果，系统里不留下修改的任何
// 一部分；恢复保存后同样的修改成功，历史记录各自保留原含义。
func TestCLIItemUpdateSaveFailureAtomicRollback(t *testing.T) {
	dataPath := setupReceivedItemForUpdate(t)

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 同时修改四项信息，本地数据保存失败。
	r := runCLI(t, dataPath, updateArgs()...)
	r.failed(t, "保存失败时修改事项", "写入数据文件失败")
	// 不显示“已修改事项”的成功提示，也不把尚未保存的新值当作修改结果展示。
	if strings.Contains(r.stdout, "已修改事项") || strings.Contains(r.stderr, "已修改事项") {
		t.Fatalf("保存失败不得显示成功提示：stdout=%q stderr=%q", r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, "已复核") || strings.Contains(r.stdout, "王五") {
		t.Fatalf("保存失败不得展示尚未保存的新值：stdout=%q", r.stdout)
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 各查询视图都仍是失败前已保存的信息。
	assertCLIUpdateRolledBack(t, dataPath)

	// 恢复正常保存后，对同一事项再次提交同样的修改应能成功。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, updateArgs()...).ok(t, "恢复保存后修改事项")
	if !strings.Contains(out, "已修改事项") || !strings.Contains(out, "新内容：一号泵压力已复核") {
		t.Fatalf("成功修改应显示成功提示与新值，got:\n%s", out)
	}

	// 查询展示四项新信息，并新增一次修改经过。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "成功后查询事项")
	for _, want := range []string{"新内容：一号泵压力已复核", "新限制条件：夜间禁动", "王五", "修改："} {
		if !strings.Contains(show, want) {
			t.Fatalf("成功后 item-show 应展示新信息与修改经过 %q，got:\n%s", want, show)
		}
	}

	// 上一班结束时记录以及交接清单中的事项原文不能被新值替换。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "成功后查看上一班结束时记录")
	if !strings.Contains(repA, "原内容：一号泵压力异常") || !strings.Contains(repA, "原限制条件：需停电窗口") {
		t.Fatalf("上一班结束时记录不应被新值替换，got:\n%s", repA)
	}
	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "成功后查看交接")
	if !strings.Contains(hShow, "原内容：一号泵压力异常") || strings.Contains(hShow, "已复核") {
		t.Fatalf("交接清单中的事项原文不应被新值替换，got:\n%s", hShow)
	}
}
