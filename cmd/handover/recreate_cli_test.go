package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“接班班次结束后重复发起交接”建立
// 端到端回归保障：构建真实可执行程序，像用户一样逐命令调用，检查退出状态、
// 标准输出/错误输出以及数据文件的实际变化，而不是直接调用领域层接口。
//
// 覆盖的用户场景：用户建立并完成了一份非空交接（清单中既有确认接收的事项，
// 也有继续跟踪的事项），原接班班次后来也结束了；此后再用原来的交班、接班
// 编号重复发起 handover-create：
//   - 必须以退出状态 0 结束，错误输出为空，不能把“记录已存在”或“接班班次
//     已结束”报成失败；
//   - 正常输出说明已有交接记录，并完整展示原编号、交班与接班关系、发起/完成
//     时间与完整事项清单（确认接收 + 继续跟踪，含处理人、处理时间、跟踪说明
//     与当时的后续负责人），不能只给一行成功提示；
//   - 返回内容仍代表当时那次交接：各字段沿用保存的信息，事项后来改负责人、
//     被关闭都只影响最新状态，不改变重复发起展示的原清单；
//   - 操作前后保存的交接数量、事项所在班次、处理经过以及班次结束时记录全部
//     不变——即使输出正确，只要悄悄改动了业务记录（如多写一次接收）也会失败。
//
// 另外两种失败路径：把接班对象改成另一个确实存在的班次，以及填写不存在的
// 班次编号，均须非零退出、错误输出明确（改换要指出原接班班次）、正常输出不
// 宣称成功，且不产生新的交接记录。

var cliBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "handover-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建临时目录失败:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	cliBinary = filepath.Join(dir, "handover")
	// 构建真实命令行程序；后续每个用例都以子进程方式运行它，与用户终端用法一致。
	build := exec.Command("go", "build", "-o", cliBinary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "构建命令行程序失败: %v\n%s", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// cliResult 收集一次命令行调用的全部可观察结果。
type cliResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// runCLI 以独立进程运行真实程序，显式指定数据文件，返回退出码与两路输出。
func runCLI(t *testing.T, dataPath string, args ...string) cliResult {
	t.Helper()
	full := append([]string{"--data", dataPath}, args...)
	cmd := exec.Command(cliBinary, full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("运行命令 %v 失败：%v", args, err)
	}
	return cliResult{exitCode: code, stdout: out.String(), stderr: errb.String()}
}

// ok 断言调用成功（退出码 0、错误输出为空），返回标准输出继续校验。
func (r cliResult) ok(t *testing.T, what string) string {
	t.Helper()
	if r.exitCode != 0 {
		t.Fatalf("%s：期望退出码 0，got %d；stderr=%q", what, r.exitCode, r.stderr)
	}
	if strings.TrimSpace(r.stderr) != "" {
		t.Fatalf("%s：成功时错误输出应为空，got %q", what, r.stderr)
	}
	return r.stdout
}

// failed 断言调用失败（退出码非零、stdout 不宣称成功、stderr 含指定信息）。
func (r cliResult) failed(t *testing.T, what, wantErrSubstring string) {
	t.Helper()
	if r.exitCode == 0 {
		t.Fatalf("%s：期望非零退出码，got 0；stdout=%q", what, r.stdout)
	}
	if r.stdout != "" {
		t.Fatalf("%s：失败时正常输出不应输出内容以免宣称成功，got %q", what, r.stdout)
	}
	if wantErrSubstring != "" && !strings.Contains(r.stderr, wantErrSubstring) {
		t.Fatalf("%s：错误输出应说明 %q，got %q", what, wantErrSubstring, r.stderr)
	}
}

// hashFile 计算数据文件的 SHA-256，用于证明重复发起/失败调用不写盘改变数据。
func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取数据文件 %s：%v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// setupCompletedHandover 按用户真实步骤建立场景并返回数据文件路径：
// S001（张三，10-02 白班，已结束）-> S002（李四，夜班，随后结束）；
// H001 是非空且已完成的交接，I001 确认接收、I002 继续跟踪（带跟踪说明与
// 当时的后续负责人王五）。接收完成后、接班班次结束前，事项发生只影响“最新
// 状态”的变化：I001 后续负责人被改成赵六，I002 被李四关闭，随后接班班次结束。
func setupCompletedHandover(t *testing.T) string {
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

	// 交班班次在班期间留下两项未关闭事项（一项重要、一项普通）。
	must("新增重要事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "一号泵压力异常待复核",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "李四"))
	must("新增跟踪事项", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "巡检台账补录",
		"--severity", "normal", "--follow", "李四"))

	// 原交班班次结束后才能发起首次交接。
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("首次发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))

	// 接班人逐项处理：I001 确认接收；I002 先退回、补充后再继续跟踪，覆盖确认
	// 与跟踪两种已接收结果，并保留一轮退回/补充历史。
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	must("退回 I002", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "return", "--operator", "李四", "--reason", "需要补充台账口径"))
	must("补充并重新提交 I002", runCLI(t, dataPath,
		"handover-resubmit", "--id", "H001", "--item", "I002",
		"--operator", "张三", "--supplement", "台账口径已对齐"))
	must("继续跟踪 I002", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "track", "--operator", "李四",
		"--note", "持续盯压并复测", "--follow", "王五"))

	// 接收完成之后、接班班次结束之前，事项发生只影响“最新状态”的变化：
	// I001 后续负责人被改为赵六；I002 被关闭。这些都不能改写已保存的交接清单。
	must("接收后修改 I001 后续负责人", runCLI(t, dataPath,
		"item-update", "--id", "I001", "--content", "一号泵压力异常待复核",
		"--severity", "important", "--constraints", "需停电窗口", "--follow", "赵六"))
	must("接班班次结束前关闭 I002", runCLI(t, dataPath, "item-close", "--id", "I002", "--operator", "李四"))
	must("结束接班班次", runCLI(t, dataPath, "shift-close", "--id", "S002"))

	return dataPath
}

// TestCLIRepeatHandoverAfterReceiverClosedReturnsSavedRecord 是核心保障：
// 接班班次结束后用原编号重复发起，必须成功且原样返回保存的交接记录，
// 且数据文件不发生任何变化。
func TestCLIRepeatHandoverAfterReceiverClosedReturnsSavedRecord(t *testing.T) {
	dataPath := setupCompletedHandover(t)

	// 重复发起前，用只读命令 handover-show 取得保存的原交接完整输出作为基准。
	baseShow := runCLI(t, dataPath, "handover-show", "--id", "H001")
	wantShow := baseShow.ok(t, "查询原交接 H001")
	if !strings.Contains(wantShow, "已完成") {
		t.Fatalf("前置交接应已完成，handover-show 输出：\n%s", wantShow)
	}
	before := hashFile(t, dataPath)

	// 接班班次结束后用原来的交班、接班编号再次发起。
	res := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
	repeatOut := res.ok(t, "接班班次结束后重复发起")

	// 正常输出首先说明这是已有记录的重复发起（而不是把它当成一次新交接）。
	if !strings.Contains(repeatOut, "已存在交接记录") {
		t.Fatalf("输出应说明已有交接记录（重复发起返回已有记录），got:\n%s", repeatOut)
	}
	// 不能只给一行成功提示：去掉首行提示后，必须包含与 handover-show 完全
	// 一致的完整交接内容（原编号、两班关系、时间、完整事项清单与处理结果）。
	body := repeatOut
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	}
	body = strings.TrimRight(body, "\n")
	wantBody := strings.TrimRight(wantShow, "\n")
	if body != wantBody {
		var dfr bytes.Buffer
		dfr.WriteString("重复发起展示的交接内容必须与保存的原记录一致：\n")
		if len(body) == 0 {
			dfr.WriteString("重复发起只输出了一行提示，省略了交接内容。\n")
		}
		dfr.WriteString(fmt.Sprintf("--- handover-show 保存的原记录 ---\n%s\n", wantBody))
		dfr.WriteString(fmt.Sprintf("--- 重复发起实际输出 ---\n%s\n", body))
		t.Fatal(dfr.String())
	}

	// 关键防回归点：输出正确不代表数据没被改动。比对整个数据文件的哈希，
	// 任何重新生成清单、重新接收（多一条 received 事件）、移动事项所在班次、
	// 改写完成时间或结束时记录都会在这里暴露。
	after := hashFile(t, dataPath)
	if after != before {
		t.Fatalf("重复发起不得改动数据文件：before=%s after=%s", before, after)
	}
}

// TestCLIRepeatHandoverShowsHistoricalValues 保证重复发起返回的是当时那次
// 交接的内容：原编号、两班关系、确认与跟踪的处理人、跟踪说明与当时的后续
// 负责人，都不因事项后来的变化而改变。
func TestCLIRepeatHandoverShowsHistoricalValues(t *testing.T) {
	dataPath := setupCompletedHandover(t)

	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").ok(t, "重复发起")

	for _, want := range []string{
		"H001", "S001 -> S002",
		// 两项事项与两种接收结果都要展示，不能按交班班次当前剩余事项重新生成。
		"I001  [确认接收]",
		"I002  [继续跟踪]",
		// 处理人沿用保存信息；跟踪说明与当时的后续负责人保留当时值。
		"处理人=李四", "持续盯压并复测", "跟踪后续负责人：王五",
		// 退回/补充历史仍属于当时那次交接。
		"第1次退回", "台账口径已对齐",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("重复发起输出应保留当时交接信息 %q，got:\n%s", want, out)
		}
	}
	// I001 接收后负责人已被改为赵六，但原清单保存的仍是发起交接时的李四。
	i001Block := out[strings.Index(out, "I001"):strings.Index(out, "I002")]
	if !strings.Contains(i001Block, "后续负责人=李四") || strings.Contains(i001Block, "赵六") {
		t.Fatalf("I001 应保留接收时清单里的原后续负责人李四，不能被最新负责人赵六替换：\n%s", i001Block)
	}
	// 交班班次当前已无未关闭事项（早已全部接收），不能按它当前还留下多少
	// 事项重新生成清单——输出里必须仍是原来的 2 项。
	if strings.Count(out, "\n  I00") != 2 {
		t.Fatalf("应完整展示原清单的2项，而不是按交班班次当前状态重新生成，got:\n%s", out)
	}
}

// TestCLIRepeatHandoverLatestItemChangesDoNotAffectRecord 验证“最新状态 vs
// 历史清单”对照：事项后来的负责人修改/关闭只影响最新状态，重复发起仍保留
// 接收时的原清单与历史信息。
func TestCLIRepeatHandoverLatestItemChangesDoNotAffectRecord(t *testing.T) {
	dataPath := setupCompletedHandover(t)

	// item-show 反映事项最新状态（I002 已关闭、当前班次 S002、负责人为王五）；
	// 重复发起的交接清单里 I002 仍是当时的“继续跟踪”，后续负责人仍为王五。
	journey := runCLI(t, dataPath, "item-show", "--id", "I002").ok(t, "查询 I002 最新状态")
	if !strings.Contains(journey, "已关闭") || !strings.Contains(journey, "当前班次=S002") {
		t.Fatalf("I002 最新状态应为 S002 班次上已关闭，got:\n%s", journey)
	}
	out := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").ok(t, "重复发起")
	if !strings.Contains(out, "I002  [继续跟踪]") {
		t.Fatalf("重复发起应保留 I002 接收时的继续跟踪结果，got:\n%s", out)
	}
	i002Block := out[strings.Index(out, "I002"):]
	if !strings.Contains(i002Block, "跟踪后续负责人：王五") || !strings.Contains(i002Block, "跟踪说明：持续盯压并复测") {
		t.Fatalf("I002 应保留接收时的后续负责人王五与跟踪说明，got:\n%s", i002Block)
	}
}

// TestCLIHandoverCountAndCloseRecordUnchanged 保障重复发起前后，保存的交接
// 数量、班次结束时记录与事项处理经过保持一致，事项历史不能多出一次接收。
func TestCLIHandoverCountAndCloseRecordUnchanged(t *testing.T) {
	dataPath := setupCompletedHandover(t)

	listBefore := runCLI(t, dataPath, "handover-list").ok(t, "重复前列出交接")
	showS002Before := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "重复前查看接班班次结束时记录")
	journeyBefore := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重复前 I001 处理经过")
	beforeHash := hashFile(t, dataPath)

	runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002").ok(t, "重复发起")

	listAfter := runCLI(t, dataPath, "handover-list").ok(t, "重复后列出交接")
	showS002After := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "重复后查看接班班次结束时记录")
	journeyAfter := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "重复后 I001 处理经过")

	// 仍只有原 H001 一条交接，数量不增加。
	if strings.Count(listAfter, "H001") != 1 || strings.Contains(listAfter, "H002") {
		t.Fatalf("应仍只有原 H001 一条交接，got:\n%s", listAfter)
	}
	if listBefore != listAfter {
		t.Fatalf("重复发起后交接清单不应变化：\nbefore:\n%s\nafter:\n%s", listBefore, listAfter)
	}
	// 班次结束时记录不变化（文本一致）。
	if showS002Before != showS002After {
		t.Fatalf("重复发起不得改变班次 S002 的结束时记录：\nbefore:\n%s\nafter:\n%s", showS002Before, showS002After)
	}
	// 事项处理经过不变化：接收只发生过一次，重复查询得到的时间线完全一致。
	if journeyBefore != journeyAfter {
		t.Fatalf("重复发起不得追加处理经过（不能多一次接收）：\nbefore:\n%s\nafter:\n%s", journeyBefore, journeyAfter)
	}
	// 整体数据文件也不能变（兜底，捕获任何未单独比对到的字段改动）。
	if afterHash := hashFile(t, dataPath); afterHash != beforeHash {
		t.Fatalf("重复发起后数据文件发生了变化：before=%s after=%s", beforeHash, afterHash)
	}
}

// TestCLIExistingHandoverRejectsDifferentExistingShift 已有交接只允许原接班
// 对象：改成另一个确实存在的班次（分别覆盖已结束与未结束）必须失败，错误
// 输出指出原接班班次，stdout 不宣称成功，且不产生新交接。
func TestCLIExistingHandoverRejectsDifferentExistingShift(t *testing.T) {
	dataPath := setupCompletedHandover(t)

	// 另一个确实存在且已结束的同岗位班次。
	runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "钱七",
		"--start", "2026-10-03T08:00:00+08:00", "--end", "2026-10-03T16:00:00+08:00").ok(t, "建立第三个班次")
	runCLI(t, dataPath, "shift-close", "--id", "S003").ok(t, "结束第三个班次")
	// 基线哈希取在额外班次建立之后：失败的 handover-create 本身不应写任何数据。
	before := hashFile(t, dataPath)

	// 已结束的新接班对象。失败后立即比对，文件不应有任何改动。
	r1 := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S003")
	r1.failed(t, "改换为已结束的现存班次", "不能改换接班对象")
	if !strings.Contains(r1.stderr, "S002") {
		t.Fatalf("错误输出应指出原接班班次 S002，got %q", r1.stderr)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("第一次改换对象失败不得改变数据文件：before=%s after=%s", before, after)
	}

	// 另一个确实存在且尚未结束的同岗位班次。
	runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "孙八",
		"--start", "2026-10-03T16:00:00+08:00", "--end", "2026-10-04T00:00:00+08:00").ok(t, "建立第四个班次")
	beforeOpen := hashFile(t, dataPath)
	r2 := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S004")
	r2.failed(t, "改换为未结束的现存班次", "不能改换接班对象")
	if !strings.Contains(r2.stderr, "S002") {
		t.Fatalf("错误输出应指出原接班班次 S002，got %q", r2.stderr)
	}
	if after := hashFile(t, dataPath); after != beforeOpen {
		t.Fatalf("第二次改换对象失败不得改变数据文件：before=%s after=%s", beforeOpen, after)
	}

	// 两次失败都不得产生新交接。
	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if strings.Count(list, "H001") != 1 || strings.Contains(list, "H002") {
		t.Fatalf("改换对象失败后不应产生新交接，got:\n%s", list)
	}
}

// TestCLIExistingHandoverRejectsNonexistentShift 填写不存在的班次编号必须
// 明确报不存在，非零退出、stdout 不宣称成功，且不产生新交接。
func TestCLIExistingHandoverRejectsNonexistentShift(t *testing.T) {
	dataPath := setupCompletedHandover(t)
	before := hashFile(t, dataPath)

	r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S999")
	r.failed(t, "接班编号不存在", "不存在")
	if !strings.Contains(r.stderr, "S999") {
		t.Fatalf("错误输出应指出不存在的编号 S999，got %q", r.stderr)
	}

	// 交班编号不存在同样明确报错。
	r2 := runCLI(t, dataPath, "handover-create", "--from", "S998", "--to", "S002")
	r2.failed(t, "交班编号不存在", "不存在")
	if !strings.Contains(r2.stderr, "S998") {
		t.Fatalf("错误输出应指出不存在的编号 S998，got %q", r2.stderr)
	}

	list := runCLI(t, dataPath, "handover-list").ok(t, "失败后列出交接")
	if strings.Count(list, "H001") != 1 || strings.Contains(list, "H002") {
		t.Fatalf("编号不存在失败后不应产生新交接，got:\n%s", list)
	}
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("编号不存在失败后数据文件不应变化：before=%s after=%s", before, after)
	}
}
