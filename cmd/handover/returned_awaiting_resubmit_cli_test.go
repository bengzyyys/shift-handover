package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“退回后必须先补充并重新提交，接班人才能
// 再次处理”建立端到端回归保障：构建真实可执行程序，像用户一样逐命令调用，检查
// 退出状态、标准输出/错误输出以及数据文件的实际变化，而不是直接调用领域层接口。
//
// 场景围绕同一份交接 H001（S001 已结束 -> S002 进行中）中的退回事项 I001 展开；
// 同一交接保留另一项已确认接收的 I002，用来区分单项失败与整份交接的状态：
//   - 接班人已退回 I001、交班人尚未补充重新提交时，再确认接收、继续跟踪或再次
//     退回都必须非零退出；错误输出说明正等待交班人补充并提示在原交接上重新提交，
//     正常输出不出现处理成功或交接完成信息。操作人、退回原因、跟踪说明、后续
//     负责人都填有效值，仍被状态规则拒绝（不能报成缺参数或编号错误）；
//   - 失败后 I001 仍是同一事项编号、同一份交接里的退回项，留在交班班次，原退回
//     原因、退回人、处理时间与退回轮次不变，失败尝试的说明、负责人与处理经过
//     不写入；I002 保持确认接收，整份交接继续未完成，数据文件不变；查询继续呈现
//     等待交班人补充，而不是尚未处理或已接收；
//   - 交班人提供非空补充说明并在原交接上重新提交后，仅 I001 恢复待处理（当前
//     接班处理人与时间显示尚未处理），原退回信息与本次补充仍可查看，事项仍留在
//     交班班次；随后接班人确认接收成功，I001 进入接班班次，其他项已接收时整份
//     交接才显示已完成；
//   - 补充说明只有空白时明确失败并继续保留退回状态，不能成为放行后续处理的依据。

// setupReturnedAwaitingSupplement 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，白班，已结束）-> S002（李四，夜班，进行中）；H001 含两项，
// I002 已被接班人确认接收，I001 已被接班人退回（原因“现场处置经过缺失，需交班人
// 补充”），交班人尚未补充重新提交，整份交接未完成。
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
	must("新增退回目标事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "二号阀内漏待处置",
		"--severity", "important", "--constraints", "需隔离上游", "--follow", "李四"))
	must("新增确认接收事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "交接班记录归档",
		"--severity", "normal", "--follow", "李四"))

	// 交班班次结束后发起交接；接班人确认接收 I002、退回 I001。
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I002", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "confirm", "--operator", "李四"))
	must("退回 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "return", "--operator", "李四", "--reason", "现场处置经过缺失，需交班人补充"))

	return dataPath
}

// TestCLIReturnedItemRejectsProcessingUntilResubmit 是核心保障：退回后、交班人
// 尚未在原交接上补充并重新提交前，接班人再确认接收、继续跟踪或再次退回同一项，
// 都必须以非零退出失败。各操作所需的操作人、退回原因、跟踪说明、后续负责人均填
// 有效值，仍应被状态规则拒绝——错误输出说明正等待交班人补充并提示在原交接记录上
// 重新提交，不能报成缺参数或编号错误；正常输出不出现处理成功信息，数据文件不变。
func TestCLIReturnedItemRejectsProcessingUntilResubmit(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			name: "确认接收",
			args: []string{"handover-process", "--id", "H001", "--item", "I001",
				"--action", "confirm", "--operator", "王五"},
		},
		{
			name: "继续跟踪",
			args: []string{"handover-process", "--id", "H001", "--item", "I001",
				"--action", "track", "--operator", "王五",
				"--note", "继续观察阀压", "--follow", "赵六"},
		},
		{
			name: "再次退回",
			args: []string{"handover-process", "--id", "H001", "--item", "I001",
				"--action", "return", "--operator", "王五", "--reason", "信息仍然不足"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataPath := setupReturnedAwaitingSupplement(t)
			before := hashFile(t, dataPath)

			r := runCLI(t, dataPath, tc.args...)
			r.failed(t, "退回后未补充重新提交前"+tc.name, "等待交班人补充")

			// 错误输出须提示在原交接记录上重新提交，并指出具体交接与事项。
			for _, want := range []string{"重新提交", "H001", "I001"} {
				if !strings.Contains(r.stderr, want) {
					t.Fatalf("%s：错误输出应提示在原交接上重新提交并包含 %q，got %q", tc.name, want, r.stderr)
				}
			}
			// 各必填项都填了有效值：拒绝只能来自状态规则，不能报成缺参数或编号错误。
			if strings.Contains(r.stderr, "不能为空") {
				t.Fatalf("%s：操作人与各项说明均已填写有效值，不应报缺参数错误，got %q", tc.name, r.stderr)
			}
			if strings.Contains(r.stderr, "不存在") {
				t.Fatalf("%s：交接与事项编号均真实存在，不应报编号错误，got %q", tc.name, r.stderr)
			}
			// 正常输出不能宣称处理成功或交接完成（failed 已保证 stdout 为空，这里
			// 显式说明该使用条件）。
			if strings.Contains(r.stdout, "已处理") || strings.Contains(r.stdout, "已完成") {
				t.Fatalf("%s：失败时正常输出不应出现处理成功或交接完成信息，got %q", tc.name, r.stdout)
			}
			if after := hashFile(t, dataPath); after != before {
				t.Fatalf("%s：被状态规则拒绝不得改动数据文件：before=%s after=%s", tc.name, before, after)
			}
		})
	}
}

// TestCLIFailedProcessingLeavesReturnedStateUntouched 保证三种失败尝试之后，保存的
// 交接与事项原样不变：同一事项编号、同一份交接，事项留在交班班次，当前结果为退回；
// 原退回原因、退回人、处理时间与退回轮次保持原样，失败尝试的说明、负责人与处理经过
// 不写入；已确认的 I002 保持原结果，整份交接继续未完成；查询继续呈现等待交班人补充。
func TestCLIFailedProcessingLeavesReturnedStateUntouched(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)

	showBefore := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败前查看交接")
	itemBefore := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败前查看事项")
	listBefore := runCLI(t, dataPath, "handover-list").ok(t, "失败前列出交接")
	before := hashFile(t, dataPath)

	// 三种处理各尝试一次，操作人、原因、跟踪说明、后续负责人均填有效值，
	// 且刻意使用此前从未出现过的取值，便于发现失败尝试被写入数据。
	attempts := [][]string{
		{"handover-process", "--id", "H001", "--item", "I001",
			"--action", "confirm", "--operator", "王五"},
		{"handover-process", "--id", "H001", "--item", "I001",
			"--action", "track", "--operator", "王五",
			"--note", "继续观察阀压", "--follow", "赵六"},
		{"handover-process", "--id", "H001", "--item", "I001",
			"--action", "return", "--operator", "王五", "--reason", "信息仍然不足"},
	}
	for i, args := range attempts {
		runCLI(t, dataPath, args...).failed(t, "第"+string(rune('1'+i))+"次失败尝试", "等待交班人补充")
	}

	showAfter := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "失败后查看交接")
	itemAfter := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "失败后查看事项")
	listAfter := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")

	// 查询输出与失败前完全一致：同一事项编号、同一份交接，没有任何新内容。
	if showBefore != showAfter {
		t.Fatalf("失败尝试不得改变交接内容：\nbefore:\n%s\nafter:\n%s", showBefore, showAfter)
	}
	if itemBefore != itemAfter {
		t.Fatalf("失败尝试不得改变事项处理经过：\nbefore:\n%s\nafter:\n%s", itemBefore, itemAfter)
	}
	if listBefore != listAfter {
		t.Fatalf("失败尝试不得改变交接清单：\nbefore:\n%s\nafter:\n%s", listBefore, listAfter)
	}

	// 内容级断言：I001 仍是退回项，原退回信息保持原样，退回轮次不增加。
	for _, want := range []string{
		"H001", "I001  [退回]",
		"第1次退回", "操作人=李四", "现场处置经过缺失，需交班人补充",
		// 同一交接已确认的另一项保持原处理结果，整份交接继续未完成。
		"I002  [确认接收]", "[未完成]",
	} {
		if !strings.Contains(showAfter, want) {
			t.Fatalf("失败后交接应保留 %q，got:\n%s", want, showAfter)
		}
	}
	// 失败尝试的说明、负责人与新的处理经过一律不得出现。
	for _, unwanted := range []string{
		"第2次退回", "王五", "赵六", "继续观察阀压", "信息仍然不足", "[已完成",
	} {
		if strings.Contains(showAfter, unwanted) {
			t.Fatalf("失败后交接不应出现失败尝试写入的 %q，got:\n%s", unwanted, showAfter)
		}
	}

	// 事项仍留在交班班次；查询继续呈现等待交班人补充，而不是尚未处理或已接收。
	if !strings.Contains(itemAfter, "当前班次=S001") {
		t.Fatalf("I001 应仍留在交班班次 S001，got:\n%s", itemAfter)
	}
	if !strings.Contains(itemAfter, "退回（等待交班人补充）") {
		t.Fatalf("查询应继续呈现等待交班人补充，got:\n%s", itemAfter)
	}
	for _, unwanted := range []string{"待处理（接班人尚未处理）", "确认接收", "继续跟踪（已接收）"} {
		if strings.Contains(itemAfter, unwanted) {
			t.Fatalf("退回未补充前不应把 I001 呈现为 %q，got:\n%s", unwanted, itemAfter)
		}
	}

	// 保存的数据没有变化（兜底，捕获任何未单独比对到的字段改动）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("失败尝试不得改动数据文件：before=%s after=%s", before, after)
	}
}

// TestCLIWhitespaceSupplementRejected 保证补充说明只有空白时明确失败：非零退出、
// 错误输出指出补充说明不能为空，退回状态继续保留，且不能成为放行后续处理的依据。
func TestCLIWhitespaceSupplementRejected(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)
	before := hashFile(t, dataPath)

	r := runCLI(t, dataPath,
		"handover-resubmit", "--id", "H001", "--item", "I001",
		"--operator", "张三", "--supplement", "   ")
	r.failed(t, "纯空白补充说明", "补充说明")
	if !strings.Contains(r.stderr, "不能为空") {
		t.Fatalf("纯空白补充说明应报补充说明不能为空，got %q", r.stderr)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("空白补充失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 退回状态继续保留：仍只有第1次退回，没有写入补充说明或重新提交。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "空白补充失败后查看交接")
	if !strings.Contains(show, "I001  [退回]") || !strings.Contains(show, "[未完成]") {
		t.Fatalf("空白补充失败后 I001 应继续保留退回状态、交接未完成，got:\n%s", show)
	}
	if strings.Contains(show, "补充说明") || strings.Contains(show, "已重新提交") {
		t.Fatalf("空白补充不得写入补充说明或重新提交记录，got:\n%s", show)
	}

	// 空白补充不能成为放行依据：接班人确认接收仍被状态规则拒绝。
	runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四").failed(t, "空白补充后确认接收", "等待交班人补充")
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("空白补充后的处理尝试不得改动数据文件：before=%s after=%s", before, after)
	}
}

// TestCLIResubmitThenConfirmCompletesHandover 覆盖允许继续操作的条件：交班人提供
// 非空补充说明并在原交接上重新提交后，仅 I001 恢复待处理（当前接班处理人与时间显示
// 尚未处理），原退回信息与本次补充仍可查看，事项仍留在交班班次；随后接班人确认接收
// 成功，I001 进入接班班次，其他项已接收时整份交接才显示已完成。
func TestCLIResubmitThenConfirmCompletesHandover(t *testing.T) {
	dataPath := setupReturnedAwaitingSupplement(t)

	// 交班人在原交接上补充说明并重新提交。
	out := runCLI(t, dataPath,
		"handover-resubmit", "--id", "H001", "--item", "I001",
		"--operator", "张三", "--supplement", "已补充现场处置经过").ok(t, "补充并重新提交")
	if !strings.Contains(out, "已补充说明并重新提交") {
		t.Fatalf("重新提交成功输出应说明已补充说明并重新提交，got:\n%s", out)
	}

	// 仅 I001 恢复待处理：当前接班处理人与处理时间显示尚未处理；原退回信息
	// 与本次补充仍可查看；I002 保持确认接收；重新提交本身不表示接收，交接未完成。
	show := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "重新提交后查看交接")
	for _, want := range []string{
		"I001  [待处理]",
		"最后处理：尚未处理（等待接班人处理）；处理人=尚未处理；处理时间=尚未处理",
		"第1次退回", "操作人=李四", "现场处置经过缺失，需交班人补充",
		"补充说明：已补充现场处置经过 补充人=张三",
		"已重新提交",
		"I002  [确认接收]", "[未完成]",
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("重新提交后交接应包含 %q，got:\n%s", want, show)
		}
	}

	// 事项仍留在交班班次，查询呈现待处理、接班人尚未处理。
	journey := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重新提交后查看事项")
	for _, want := range []string{"当前班次=S001", "待处理（接班人尚未处理）", "第1次重新提交", "已补充现场处置经过"} {
		if !strings.Contains(journey, want) {
			t.Fatalf("重新提交后事项查询应包含 %q，got:\n%s", want, journey)
		}
	}

	// 接班人确认接收才应成功：I001 进入接班班次；其他项已接收，整份交接完成。
	done := runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四").ok(t, "重新提交后确认接收")
	if !strings.Contains(done, "已处理") {
		t.Fatalf("确认接收成功输出应说明已处理，got:\n%s", done)
	}
	finalShow := runCLI(t, dataPath, "handover-show", "--id", "H001").ok(t, "确认接收后查看交接")
	for _, want := range []string{"I001  [确认接收]", "I002  [确认接收]", "[已完成"} {
		if !strings.Contains(finalShow, want) {
			t.Fatalf("全部接收后交接应包含 %q，got:\n%s", want, finalShow)
		}
	}
	finalJourney := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "确认接收后查看事项")
	if !strings.Contains(finalJourney, "当前班次=S002") || !strings.Contains(finalJourney, "S001 -> S002") {
		t.Fatalf("确认接收后 I001 应进入接班班次 S002 并保留流转历史，got:\n%s", finalJourney)
	}
}
