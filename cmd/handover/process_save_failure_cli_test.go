package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“接班人逐项处理交接事项
// （handover-process）在本地数据保存阶段失败”建立端到端回归保障。修复前，
// 服务在保存失败、数据已回滚的情况下仍把本次尚未保存的处理结果返回给命令行，
// 用户会看到“已处理”和带着新状态（甚至完成时间）的交接清单，误报成功。
//
// 场景：交接 H001 含两项待处理事项，I001 已成功确认接收；对最后一项 I002
// 依次尝试确认接收、继续跟踪、退回，每次本地写盘都失败。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已处理”，也不展示本次尝试的状态、操作人、跟踪说明、退回原因或
//     “已完成”的交接清单；
//   - 数据文件一个字节不变；handover-show、item-show、shift-show 都仍是
//     失败前的事实：I002 待处理、尚未处理，交接 [未完成]、没有完成时间，
//     事项留在 S001，交班班次结束时记录保持原样；
//   - 恢复保存后确认 I002 成功，才显示“已处理”、I002 确认接收与整份
//     交接已完成，接班班次随后可以结束。

// setupReadyForProcess 按用户真实步骤建立场景并返回数据文件路径：
// S001（已结束，留有未关闭事项 I001、I002）-> S002（进行中）；已发起 H001，
// I001 已确认接收，I002 仍待处理（是交接中的最后一项）。
func setupReadyForProcess(t *testing.T) string {
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
		"--severity", "important", "--follow", "李四"))
	must("新增未关闭事项二", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "巡检台账补录",
		"--severity", "normal", "--follow", "李四"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	return dataPath
}

// assertCLIProcessStillPending 核对失败尝试后各查询视图仍是失败前事实：
// I002 待处理、尚未处理，交接未完成且没有完成时间，事项仍留在 S001，
// 交班班次结束时记录不变；不出现本次尝试的跟踪说明、新负责人或退回原因。
func assertCLIProcessStillPending(t *testing.T, dataPath, failMarker string) {
	t.Helper()

	hShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	for _, want := range []string{"[未完成]", "I002  [待处理]", "处理人=尚未处理；处理时间=尚未处理"} {
		if !strings.Contains(hShow, want) {
			t.Fatalf("失败后交接应保持未完成、I002 待处理 %q，got:\n%s", want, hShow)
		}
	}
	for _, unwanted := range []string{"[已完成", "I002  [确认接收]", "I002  [继续跟踪]", "I002  [退回]", "第1次退回", failMarker} {
		if strings.Contains(hShow, unwanted) {
			t.Fatalf("失败后交接不应展示本次尝试的结果 %q，got:\n%s", unwanted, hShow)
		}
	}

	show := runCLI(t, dataPath, "item-show", "--id", "I002").ok(t, "失败后查询事项")
	for _, want := range []string{"当前班次=S001", "待处理（接班人尚未处理）"} {
		if !strings.Contains(show, want) {
			t.Fatalf("失败后事项应仍留在交班班次且交接结果为待处理 %q，got:\n%s", want, show)
		}
	}
	if strings.Contains(show, "确认接收") || strings.Contains(show, "继续跟踪") ||
		strings.Contains(show, "第1次退回") || strings.Contains(show, failMarker) {
		t.Fatalf("失败后事项处理经过不应出现本次尝试，got:\n%s", show)
	}

	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "失败后查看交班班次报告")
	if !strings.Contains(repA, "巡检台账补录") || !strings.Contains(repA, "[未完成]") {
		t.Fatalf("交班班次结束时记录与未完成状态应保持原样，got:\n%s", repA)
	}
}

// TestCLIProcessSaveFailureNoSuccessOutput 是核心保障：三种处理动作在保存
// 阶段失败时命令都明确报错、非零退出、正常输出为空（不显示“已处理”或失败
// 尝试的交接清单），系统里不留下本次处理的任何一部分；恢复保存后处理成功，
// 才展示处理结果与交接完成。
func TestCLIProcessSaveFailureNoSuccessOutput(t *testing.T) {
	dataPath := setupReadyForProcess(t)

	// 失败前交接确实未完成、I002 待处理。
	before := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败前查看交接")
	if !strings.Contains(before, "[未完成]") || !strings.Contains(before, "I002  [待处理]") {
		t.Fatalf("前置状态不正确：\n%s", before)
	}
	beforeHash := hashFile(t, dataPath)

	// 故障期间对最后一项依次尝试三种动作，每次参数都合法、状态都允许。
	breakCLISaving(t, dataPath)

	cases := []struct {
		name   string
		args   []string
		marker string // 本次尝试特有的未保存内容
	}{
		{
			name:   "最后一项确认接收保存失败",
			args:   []string{"handover-process", "--id", "H001", "--item", "I002", "--action", "confirm", "--operator", "李四"},
			marker: "I002  [确认接收]",
		},
		{
			name:   "最后一项继续跟踪保存失败",
			args:   []string{"handover-process", "--id", "H001", "--item", "I002", "--action", "track", "--operator", "李四", "--note", "持续跟踪压力变化", "--follow", "王五"},
			marker: "持续跟踪压力变化",
		},
		{
			name:   "最后一项退回保存失败",
			args:   []string{"handover-process", "--id", "H001", "--item", "I002", "--action", "return", "--operator", "接班人王五", "--reason", "失败尝试的退回原因"},
			marker: "失败尝试的退回原因",
		},
	}
	for _, tc := range cases {
		r := runCLI(t, dataPath, tc.args...)
		// r.failed 同时断言非零退出、stdout 为空（不显示“已处理”或失败
		// 尝试的交接清单）与 stderr 明确是保存失败。
		r.failed(t, tc.name, "写入数据文件失败")
		if strings.Contains(r.stderr, "已处理") || strings.Contains(r.stderr, tc.marker) {
			t.Fatalf("%s：错误输出只能说明保存失败，不得夹带成功提示或未保存内容：stderr=%q",
				tc.name, r.stderr)
		}
		// 每次失败后查询都仍是失败前事实。
		assertCLIProcessStillPending(t, dataPath, tc.marker)
	}

	// 三次失败后数据文件一个字节都不应改变。
	if after := hashFile(t, dataPath); after != beforeHash {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", beforeHash, after)
	}

	// 交接仍未完成，接班班次不能结束。
	closeR := runCLI(t, dataPath, "shift-close", "--id", "S002")
	closeR.failed(t, "最后一项未接收时接班班次不能结束", "当前交接状态不允许该操作")

	// 恢复保存后确认最后一项：成功，交接在本次处理后完成。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "confirm", "--operator", "李四").ok(t, "恢复保存后确认接收")
	for _, want := range []string{"已处理", "H001", "I002  [确认接收]", "[已完成"} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功处理应展示已处理、确认结果与交接完成 %q，got:\n%s", want, out)
		}
	}
	// 失败尝试的内容不混入成功结果。
	for _, unwanted := range []string{"失败尝试的退回原因", "持续跟踪压力变化", "接班人王五", "第1次退回"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("成功结果不应包含失败尝试的内容 %q，got:\n%s", unwanted, out)
		}
	}

	// 事项进入接班班次，处理经过只留下成功的这一次确认接收。
	show := runCLI(t, dataPath, "item-show", "--id", "I002").ok(t, "成功后查询事项")
	for _, want := range []string{"当前班次=S002", "确认接收"} {
		if !strings.Contains(show, want) {
			t.Fatalf("成功后事项应显示已进入接班班次、确认接收 %q，got:\n%s", want, show)
		}
	}
	if strings.Contains(show, "待处理（接班人尚未处理）") {
		t.Fatalf("成功后事项不应再显示待处理：\n%s", show)
	}

	// 全部接收后接班班次可以结束。
	runCLI(t, dataPath, "shift-close", "--id", "S002").ok(t, "全部接收后结束接班班次")
}
