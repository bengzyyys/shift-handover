package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“退回后必须先补充并重新提交，接班人
// 才能再次处理”建立端到端回归保障：构建真实可执行程序，像用户一样逐命令
// 调用，检查退出状态、标准输出/错误输出以及数据文件的实际变化，而不是直接
// 调用领域层接口。
//
// 场景固定为同一份交接 H001（交班班次 S001 已结束，接班班次 S002 进行中）
// 中的一个退回事项 I001：接班人李四已填写原因退回，交班人张三尚未补充重新
// 提交；同一份交接里的另一项 I002 已确认接收，用来区分单项失败与整份交接
// 的状态。业务层已有该规则，本文件保证命令行的错误提示、退出结果与保存的
// 交接信息一致，且不改变现有功能和命令用法。

const (
	waitReturnReason = "信息不全，需补充图纸"
	waitSupplement   = "图纸编号已补到资料柜B层"
	waitTrackNote    = "继续跟踪的跟踪说明"
	waitNextFollow   = "赵六"
	waitNewOperator  = "接班人王五"
	waitNewReason    = "第二次尝试退回的原因"
)

// setupReturnedAwaitingSupplement 按用户真实步骤建立“退回后等待交班人补充”
// 的场景并返回数据文件路径：
// S001（张三，白班，已结束）-> S002（李四，夜班，进行中）；H001 共2项，
// I002 已由李四确认接收，I001 已由李四填写原因退回，尚未补充重新提交。
func setupReturnedAwaitingSupplement(t *testing.T) string {
	t.Helper()
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}

	// 建立相邻两班（端点相接，无需重叠说明）。
	must("建立交班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	must("建立接班班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T16:00:00+08:00", "--end", "2026-10-02T23:00:00+08:00"))

	// 交班班次在班期间留下两项未关闭事项。
	must("新增退回事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "一号泵压力异常待复核",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "李四"))
	must("新增另一项事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "巡检台账补录",
		"--severity", "normal", "--follow", "李四"))

	// 交班班次结束后发起交接。
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))

	// 同一份交接中另一项先确认接收，与退回项形成对照。
	must("确认接收另一项", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "confirm", "--operator", "李四"))
	// 接班人为本项填写原因并退回，交班人尚未补充重新提交。
	must("退回本项", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "return", "--operator", "李四", "--reason", waitReturnReason))

	return dataPath
}

// assertWaitingSupplementError 校验一次被状态规则拒绝的命令行调用：非零退出、
// 正常输出为空（不出现“已处理”“已完成”等成功信息），错误输出明确说明正在
// 等待交班人补充并提示在原交接 H001 上重新提交。
func assertWaitingSupplementError(t *testing.T, r cliResult, what string) {
	t.Helper()
	// failed 同时断言非零退出且 stdout 完全为空，从退出结果与正常输出两侧
	// 排除“处理成功/交接完成”被误报的可能。
	r.failed(t, what, "等待交班人补充")
	for _, want := range []string{"等待交班人补充", "原交接", "重新提交", "H001", "I001"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("%s：错误输出应明确说明等待交班人补充并提示在原交接上重新提交（含 %q），got %q",
				what, want, r.stderr)
		}
	}
	// 必须由状态规则拒绝，而不是缺必填信息或编号错误：本次调用各操作所需
	// 参数都给了有效值，错误输出不能落到这些输入类提示上。
	for _, unwanted := range []string{"不能为空", "不存在", "没有事项", "已处理", "已完成"} {
		if strings.Contains(r.stderr, unwanted) {
			t.Fatalf("%s：拒绝原因应是等待补充的状态规则，错误输出不应含 %q，got %q",
				what, unwanted, r.stderr)
		}
	}
}

// TestCLIReturnedItemBlocksProcessAwaitingSupplement 是核心拦截保障：
// 退回后、交班人尚未在原交接上补充重新提交前，再尝试确认接收、继续跟踪或
// 再次退回都必须非零退出并给出等待补充的状态错误；即使操作人、退回原因、
// 跟踪说明与后续负责人都填了有效值也一样被拒，且数据文件不发生任何变化。
func TestCLIReturnedItemBlocksProcessAwaitingSupplement(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)

	attempts := []struct {
		name string
		args []string
	}{
		{"等待补充时确认接收", []string{
			"handover-process", "--id", "H001", "--item", "I001",
			"--action", "confirm", "--operator", waitNewOperator,
		}},
		{"等待补充时继续跟踪（跟踪说明与后续负责人均有效）", []string{
			"handover-process", "--id", "H001", "--item", "I001",
			"--action", "track", "--operator", waitNewOperator,
			"--note", waitTrackNote, "--follow", waitNextFollow,
		}},
		{"等待补充时再次退回（退回原因有效）", []string{
			"handover-process", "--id", "H001", "--item", "I001",
			"--action", "return", "--operator", waitNewOperator,
			"--reason", waitNewReason,
		}},
	}
	for _, a := range attempts {
		before := hashFile(t, dataPath)
		r := runCLI(t, dataPath, a.args...)
		assertWaitingSupplementError(t, r, a.name)
		// 每次失败后立即比对整个数据文件：不得新增退回轮次、处理经过或任何
		// 其他保存变化。
		if after := hashFile(t, dataPath); after != before {
			t.Fatalf("%s：失败调用不得改动数据文件：before=%s after=%s", a.name, before, after)
		}
	}
}

// TestCLIReturnedItemQueriesUnchangedAfterFailedAttempts 保障失败尝试之后
// 查看原交接与该事项，仍是同一个事项编号、同一份交接：本项留在交班班次、
// 当前结果为退回，原退回原因、退回人、处理时间与退回轮次保持原样，不混入
// 本次尝试的说明、负责人或新的处理经过；同一交接已确认的另一项结果不变，
// 整份交接继续未完成。查询呈现的是“等待交班人补充”，而不是尚未处理或已
// 接收。
func TestCLIReturnedItemQueriesUnchangedAfterFailedAttempts(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)

	// 失败尝试前先用只读命令取得保存内容作为基准；只读命令本身不写盘。
	baseHandover := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "基线查看交接")
	baseItem := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "基线查看事项")
	before := hashFile(t, dataPath)

	// 三类被禁操作各试一次（参数均有效）。
	runCLI(t, dataPath, "handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", waitNewOperator)
	runCLI(t, dataPath, "handover-process", "--id", "H001", "--item", "I001",
		"--action", "track", "--operator", waitNewOperator,
		"--note", waitTrackNote, "--follow", waitNextFollow)
	runCLI(t, dataPath, "handover-process", "--id", "H001", "--item", "I001",
		"--action", "return", "--operator", waitNewOperator, "--reason", waitNewReason)

	gotHandover := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看原交接")
	gotItem := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查看该事项")

	// 仍是同一份交接、同一项与另一项，整份交接继续未完成。
	for _, want := range []string{
		"H001  岗位=调度  S001 -> S002", "[未完成]", "共2项",
		"I001  [退回]", "I002  [确认接收]",
		// 原退回原因、退回人、处理时间（最后处理）与唯一一轮退回保持原样。
		"最后处理：处理人=李四", waitReturnReason,
		"第1次退回：", "操作人=李四 原因=" + waitReturnReason,
	} {
		if !strings.Contains(gotHandover, want) {
			t.Fatalf("失败后原交接应保留 %q，got:\n%s", want, gotHandover)
		}
	}

	// 不出现这次失败尝试留下的任何痕迹。
	for _, unwanted := range []string{
		waitNewOperator, waitNewReason, waitTrackNote, waitNextFollow,
		"第2次退回", "补充说明：", "已重新提交",
		"I001  [待处理]", "I001  [确认接收]", "I001  [继续跟踪]",
		"[已完成]",
	} {
		if strings.Contains(gotHandover, unwanted) {
			t.Fatalf("失败尝试不得在原交接中留下 %q：\n%s", unwanted, gotHandover)
		}
	}

	// 该事项仍留在交班班次，查询明确呈现等待交班人补充，而不是尚未处理或
	// 已接收；处理人仍是原退回人李四，处理经过只有成功发生过的第1次退回。
	for _, want := range []string{
		"当前班次=S001", "退回（等待交班人补充）", "处理人=李四",
		"第1次退回 操作人=李四 原因=" + waitReturnReason,
	} {
		if !strings.Contains(gotItem, want) {
			t.Fatalf("失败后事项查询应保留 %q，got:\n%s", want, gotItem)
		}
	}
	for _, unwanted := range []string{
		"待处理（接班人尚未处理）", "确认接收", "继续跟踪",
		waitNewOperator, waitNewReason, waitTrackNote, waitNextFollow,
		"第2次退回", "重新提交",
	} {
		if strings.Contains(gotItem, unwanted) {
			t.Fatalf("失败尝试不得在事项查询中留下 %q：\n%s", unwanted, gotItem)
		}
	}

	// 查询输出与失败前完全一致：同一编号、同一份交接、时间与轮次都没变。
	if gotHandover != baseHandover {
		t.Fatalf("失败尝试后原交接展示应与尝试前一致：\n--- before ---\n%s\n--- after ---\n%s",
			baseHandover, gotHandover)
	}
	if gotItem != baseItem {
		t.Fatalf("失败尝试后事项查询应与尝试前一致：\n--- before ---\n%s\n--- after ---\n%s",
			baseItem, gotItem)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("失败尝试与只读查询后数据文件不应变化：before=%s after=%s", before, after)
	}
}

// TestCLIWhitespaceSupplementDoesNotUnlock 保障补充说明只有空白时明确失败，
// 退回状态原样保留，不能成为放行后续处理的依据。
func TestCLIWhitespaceSupplementDoesNotUnlock(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)
	before := hashFile(t, dataPath)

	// 空格、制表符与换行混合的纯空白补充：按空处理，明确报输入错误。
	r := runCLI(t, dataPath, "handover-resubmit",
		"--id", "H001", "--item", "I001",
		"--operator", "张三", "--supplement", "  \t \n ")
	r.failed(t, "纯空白补充说明", "补充说明不能为空")

	// 状态仍是退回、继续等待补充，查询不得出现待处理或补充/重新提交痕迹。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "空白补充后查看交接")
	if !strings.Contains(show, "I001  [退回]") || strings.Contains(show, "I001  [待处理]") {
		t.Fatalf("纯空白补充后本项应仍是退回，got:\n%s", show)
	}
	if strings.Contains(show, "补充说明：") || strings.Contains(show, "已重新提交") {
		t.Fatalf("纯空白补充不得写入补充说明或重新提交记录：\n%s", show)
	}
	item := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "空白补充后查看事项")
	if !strings.Contains(item, "退回（等待交班人补充）") {
		t.Fatalf("纯空白补充后应继续呈现等待交班人补充，got:\n%s", item)
	}

	// 空白补充不能放行：随后确认接收仍必须被等待补充的状态规则拦截。
	blocked := runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I001", "--action", "confirm", "--operator", waitNewOperator)
	assertWaitingSupplementError(t, blocked, "纯空白补充后再确认接收")

	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("纯空白补充及后续失败尝试不得改动数据文件：before=%s after=%s", before, after)
	}
}

// TestCLIResubmitRestoresOnlyItemAndConfirmCompletes 覆盖允许继续操作的条件：
// 交班人提供非空补充说明并在原交接成功重新提交后，仅该退回项恢复待处理、
// 当前接班处理人和时间显示尚未处理，原退回信息与本次补充仍可查看，事项仍
// 留在交班班次，整份交接仍未完成；随后接班人确认接收才成功，事项进入接班
// 班次，且在另一项已接收的前提下整份交接此时才显示已完成。
func TestCLIResubmitRestoresOnlyItemAndConfirmCompletes(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)

	// 非空补充说明并在原交接 H001 上重新提交：成功退出、错误输出为空。
	res := runCLI(t, dataPath, "handover-resubmit",
		"--id", "H001", "--item", "I001",
		"--operator", "张三", "--supplement", waitSupplement)
	out := res.ok(t, "非空补充并重新提交")
	if !strings.Contains(out, "已补充说明并重新提交") {
		t.Fatalf("重新提交成功应明确提示已补充说明并重新提交，got:\n%s", out)
	}
	// 重新提交本身不表示接收、不完成交接。
	if !strings.Contains(out, "[未完成]") || strings.Contains(out, "[已完成]") {
		t.Fatalf("重新提交后整份交接应仍未完成，got:\n%s", out)
	}

	// 原交接：仅本项恢复待处理，接班处理人与处理时间显示尚未处理；原退回
	// 原因、退回人和本轮补充信息都仍可查看；另一项保持已接收。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "重新提交后查看原交接")
	for _, want := range []string{
		"H001  岗位=调度  S001 -> S002", "[未完成]", "共2项",
		"I001  [待处理]",
		"最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理",
		"第1次退回：", "操作人=李四 原因=" + waitReturnReason,
		"补充说明：" + waitSupplement + " 补充人=张三",
		"已重新提交：",
		"I002  [确认接收]",
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("重新提交后原交接应包含 %q，got:\n%s", want, show)
		}
	}

	// 该事项：仍留在交班班次；查询呈现待处理、接班人尚未处理；处理经过保留
	// 原退回与本次重新提交，尚未出现接收。
	item := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重新提交后查看该事项")
	for _, want := range []string{
		"当前班次=S001", "流经班次：S001",
		"待处理（接班人尚未处理）；处理人=尚未处理；处理时间=尚未处理",
		"第1次退回 操作人=李四 原因=" + waitReturnReason,
		"第1次重新提交 补充说明=" + waitSupplement + " 补充人=张三",
	} {
		if !strings.Contains(item, want) {
			t.Fatalf("重新提交后事项查询应包含 %q，got:\n%s", want, item)
		}
	}
	for _, unwanted := range []string{"当前班次=S002", "确认接收 操作人=", "继续跟踪"} {
		if strings.Contains(item, unwanted) {
			t.Fatalf("重新提交本身不表示接收，事项查询不应含 %q：\n%s", unwanted, item)
		}
	}

	// 交班人补充重提后，接班人确认接收才成功。
	conf := runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I001", "--action", "confirm", "--operator", "李四")
	cout := conf.ok(t, "重新提交后确认接收")
	if !strings.Contains(cout, "已处理") || !strings.Contains(cout, "I001  [确认接收]") {
		t.Fatalf("确认接收成功应报告已处理且本项为确认接收，got:\n%s", cout)
	}

	// 本项进入接班班次；另一项已接收，整份交接此时才显示已完成。
	item2 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "接收后查看该事项")
	if !strings.Contains(item2, "当前班次=S002") || !strings.Contains(item2, "原始班次=S001") {
		t.Fatalf("确认接收后事项应进入接班班次 S002，got:\n%s", item2)
	}
	show2 := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "接收后查看原交接")
	if !strings.HasPrefix(show2, "H001") || !strings.Contains(show2, "[已完成") {
		t.Fatalf("两项均接收后整份交接应显示已完成，got:\n%s", show2)
	}
	if !strings.Contains(show2, "I001  [确认接收]") || !strings.Contains(show2, "I002  [确认接收]") {
		t.Fatalf("接收后两项都应为确认接收，got:\n%s", show2)
	}
}
