package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“修改事项时本地数据保存失败”建立端到端
// 回归保障：上一班已结束、事项已确认接收到仍在进行中的下一班后，用户通过
// item-update 同时改动内容、严重程度、限制条件和后续负责人四项信息，而保存
// 阶段失败（数据文件的临时写入路径被目录占用，写盘必然失败）。
//
// 覆盖的用户场景：
//   - 失败的 item-update 必须以非零退出码结束，错误输出明确报保存错误，正常
//     输出为空——不能显示“已修改事项”的成功提示，也不能把尚未保存的新值
//     当作修改结果展示；
//   - 数据文件一个字节不变；item-show 仍显示失败前的全部信息（四项原值完整，
//     不出现内容已变而负责人未变、或原限制条件被清空的半套修改），处理经过
//     仍只有建立与接收，不新增这次失败的修改记录；
//   - 当前班次报告与单项查询一致；上一班结束时记录与原交接清单中的事项原文、
//     确认接收结果不受影响；
//   - 保存恢复正常后，对同一事项再次提交同样的修改应成功并显示“已修改事项”，
//     item-show 展示四项新信息且只新增一次修改经过；上一班结束时记录与原
//     交接清单原文不被新值替换。

// setupReceivedItemForUpdate 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）-> S002（李四，夜班，进行中）；I001 原本有明确的
// 内容、严重程度、非空限制条件和后续负责人，经 H001 确认接收到 S002。
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
		"item-add", "--shift", "S001", "--content", "一号泵压力异常",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "王五"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	return dataPath
}

// updateArgs 是一次同时改动内容、严重程度、限制条件和后续负责人的修改参数。
func updateArgs() []string {
	return []string{"item-update", "--id", "I001",
		"--content", "一号泵压力异常（已复核）",
		"--severity", "urgent", "--constraints", "白班可申请停电", "--follow", "赵六"}
}

// breakCLISaving 在数据文件的临时写入路径上放一个目录，使保存必然失败。
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

// TestCLIUpdateSaveFailureRollsBackCompletely 是核心保障：合法修改在保存阶段
// 失败时，命令明确报错、不显示成功提示，数据文件不变，各查询仍显示失败前的
// 全部信息；保存恢复后同样的修改成功，且只新增一次修改经过。
func TestCLIUpdateSaveFailureRollsBackCompletely(t *testing.T) {
	dataPath := setupReceivedItemForUpdate(t)
	before := hashFile(t, dataPath)

	// 保存阶段失败：非零退出、错误输出明确、正常输出为空（不显示“已修改事项”）。
	breakCLISaving(t, dataPath)
	res := runCLI(t, dataPath, updateArgs()...)
	res.failed(t, "保存失败的修改", "写入数据文件失败")
	if strings.Contains(res.stdout, "已修改事项") {
		t.Fatalf("保存失败不能显示“已修改事项”的成功提示，got %q", res.stdout)
	}
	if strings.Contains(res.stderr, "不能为空") || strings.Contains(res.stderr, "已结束") ||
		strings.Contains(res.stderr, "不存在") {
		t.Fatalf("参数合法、班次进行中的保存失败不应被报告为业务校验错误，got %q", res.stderr)
	}

	// 数据文件一个字节都不应改变。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 凭事项编号查询仍显示失败前的全部信息：四项原值完整，处理经过只有建立
	// 与接收，不新增这次失败的修改记录。
	show := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查询事项")
	for _, want := range []string{
		"内容：一号泵压力异常\n",
		"严重程度=重要",
		"限制条件：需停电窗口",
		"后续负责人：王五",
		"当前班次=S002", "原始班次=S001", "[未关闭]",
		"流经班次：S001 -> S002",
		"事项建立", "确认接收",
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后 item-show 应显示失败前信息 %q，got:\n%s", want, show)
		}
	}
	for _, unwanted := range []string{"已复核", "紧急", "白班可申请停电", "赵六", "修改："} {
		if strings.Contains(show, unwanted) {
			t.Fatalf("失败后 item-show 不应出现尚未保存的新值或修改记录 %q，got:\n%s", unwanted, show)
		}
	}

	// 当前班次报告与单项查询一致，仍显示失败前信息。
	rep := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "失败后查看当前班次")
	if !strings.Contains(rep, "内容：一号泵压力异常\n") || strings.Contains(rep, "已复核") {
		t.Fatalf("当前班次报告应显示失败前内容：\n%s", rep)
	}

	// 上一班结束时记录继续保留当时的四项信息。
	repFrom := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看上一班")
	for _, want := range []string{"内容：一号泵压力异常", "限制条件：需停电窗口", "结束时后续负责人：王五"} {
		if !strings.Contains(repFrom, want) {
			t.Fatalf("上一班结束时记录应保留当时信息 %q，got:\n%s", want, repFrom)
		}
	}

	// 原交接中的事项原文与确认接收结果不受影响。
	hv := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	if !strings.Contains(hv, "I001  [确认接收]") || !strings.Contains(hv, "原文：一号泵压力异常") ||
		strings.Contains(hv, "已复核") {
		t.Fatalf("交接清单应保持接收当时的原文与结果：\n%s", hv)
	}

	// 保存恢复正常后，对同一事项再次提交同样的修改应能成功。
	restoreCLISaving(t, dataPath)
	okOut := runCLI(t, dataPath, updateArgs()...).ok(t, "恢复后再次提交同样的修改")
	if !strings.Contains(okOut, "已修改事项") {
		t.Fatalf("成功的修改应显示“已修改事项”，got:\n%s", okOut)
	}

	// 查询展示四项新信息，且只新增一次修改经过。
	show2 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "成功后查询事项")
	for _, want := range []string{
		"内容：一号泵压力异常（已复核）",
		"严重程度=紧急",
		"限制条件：白班可申请停电",
		"后续负责人：赵六",
		"事项建立", "确认接收",
	} {
		if !strings.Contains(show2, want) {
			t.Fatalf("成功后 item-show 应展示四项新信息 %q，got:\n%s", want, show2)
		}
	}
	if n := strings.Count(show2, "修改："); n != 1 {
		t.Fatalf("处理经过应只新增一次修改记录，got %d 次：\n%s", n, show2)
	}

	// 上一班结束时记录与原交接清单原文不被新值替换。
	repFrom2 := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "成功后查看上一班")
	if !strings.Contains(repFrom2, "内容：一号泵压力异常\n") ||
		!strings.Contains(repFrom2, "结束时后续负责人：王五") ||
		strings.Contains(repFrom2, "已复核") {
		t.Fatalf("上一班结束时记录不能被新值替换：\n%s", repFrom2)
	}
	hv2 := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "成功后查看交接")
	if !strings.Contains(hv2, "原文：一号泵压力异常") || strings.Contains(hv2, "已复核") {
		t.Fatalf("原交接中的事项原文不能被新值替换：\n%s", hv2)
	}
}
