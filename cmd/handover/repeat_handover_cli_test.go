package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件围绕“用户实际使用命令行”的结果建立回归保障：每条命令都作为独立
// 子进程执行（复用已编译的测试二进制，见 TestMain），与真实使用方式一致地
// 重新打开数据文件，断言真实退出码、标准输出与错误输出，并核对业务记录是否
// 被悄悄改动。
//
// 场景：一次非空交接已完成（清单中既有确认接收、也有继续跟踪，且保留一轮
// 退回/补充经过），接班班次结束、事项后来改了负责人或被关闭后，用原交班、
// 接班编号重复发起 handover-create，仍应退出码为 0、错误输出为空，正常输出
// 说明已有交接记录并完整展示保存的原记录；改换接班对象或填写不存在的编号则
// 必须以非零状态明确失败，且不产生新记录、不改动任何业务数据。

// cliHelperEnv 置位时，测试二进制不再跑测试，而是以 main 的同一入口执行
// 一条 handover 命令：真实进程、真实标准输出/错误输出、真实退出码，与用户
// 在命令行直接运行完全一致（连直接写 os.Stderr 的旁路输出也能观察到）。
const cliHelperEnv = "HANDOVER_CLI_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(cliHelperEnv) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// cliResult 是一次命令行调用的可观察结果。
type cliResult struct {
	stdout string
	stderr string
	code   int
}

// runCLI 以独立命令行进程执行一条命令（带 --data 指向临时数据文件），
// 每次调用都像真实使用一样重新打开并落盘数据文件。
func runCLI(t *testing.T, dataPath string, args ...string) cliResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制: %v", err)
	}
	argv := append([]string{"--data", dataPath}, args...)
	cmd := exec.Command(exe, argv...)
	cmd.Env = append(os.Environ(), cliHelperEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("执行命令 %v 失败: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func mustCLI(t *testing.T, r cliResult, step string) cliResult {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("%s 应成功（退出码0），got 退出码=%d，stderr=%s，stdout=%s",
			step, r.code, r.stderr, r.stdout)
	}
	if r.stderr != "" {
		t.Fatalf("%s 错误输出应为空，got %q", step, r.stderr)
	}
	return r
}

// readData 读取数据文件当前字节，用于核对记录是否被改动。
func readData(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取数据文件: %v", err)
	}
	return string(b)
}

// TestCLIRepeatHandoverAfterReceivingShiftClosed：已完成的非空交接在原接班
// 班次结束后重复发起，必须以退出状态零结束、错误输出为空，输出说明已有交接
// 记录并展示原编号、交班/接班关系、原发起与完成时间以及完整事项清单（确认
// 接收与继续跟踪各自的处理结果、操作人、处理时间、跟踪说明与后续负责人、
// 退回补充经过一律沿用保存值）；重复发起不得重新生成清单、不得重新接收或
// 移动事项、不得改变班次结束时记录，保存的交接数量也不增加。
func TestCLIRepeatHandoverAfterReceivingShiftClosed(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "handover-data.json")

	// 建立交班、接班两个同岗位班次。
	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"), "建立 S001")
	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T16:00:00+08:00", "--end", "2026-10-02T23:00:00+08:00"), "建立 S002")

	// 非空清单：一项重要事项带限制条件，一项普通事项。
	mustCLI(t, runCLI(t, dataPath, "item-add",
		"--shift", "S001", "--content", "事项甲", "--severity", "important",
		"--constraints", "需停电窗口", "--follow", "李四"), "新增 I001")
	mustCLI(t, runCLI(t, dataPath, "item-add",
		"--shift", "S001", "--content", "事项乙", "--severity", "normal",
		"--follow", "王五"), "新增 I002")

	// 交班班次结束后发起交接。
	mustCLI(t, runCLI(t, dataPath, "shift-close", "--id", "S001"), "结束 S001")
	first := mustCLI(t, runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"), "首次发起交接")
	if !strings.Contains(first.stdout, "已发起交接") || !strings.Contains(first.stdout, "H001") {
		t.Fatalf("首次发起应输出新交接 H001：\n%s", first.stdout)
	}

	// I001 确认接收；I002 先退回、交班人补充并重新提交后再继续跟踪，
	// 保留一轮退回/补充经过与当时的跟踪说明、后续负责人。
	mustCLI(t, runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I001", "--action", "confirm", "--operator", "李四"), "I001 确认接收")
	mustCLI(t, runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I002", "--action", "return",
		"--operator", "李四", "--reason", "需要补充细节"), "I002 退回")
	mustCLI(t, runCLI(t, dataPath, "handover-resubmit",
		"--id", "H001", "--item", "I002",
		"--operator", "张三", "--supplement", "补充说明如下"), "I002 补充并重新提交")
	mustCLI(t, runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I002", "--action", "track",
		"--operator", "李四", "--note", "继续盯压力", "--follow", "赵六"), "I002 继续跟踪")

	// 接班班次结束前，事项被修改负责人或关闭：只影响事项最新状态，不应影响
	// 交接时保存的原清单。
	mustCLI(t, runCLI(t, dataPath, "item-update",
		"--id", "I001", "--content", "事项甲", "--severity", "important",
		"--constraints", "需停电窗口", "--follow", "新负责人"), "I001 接班后改负责人")
	mustCLI(t, runCLI(t, dataPath, "item-close", "--id", "I002", "--operator", "李四"), "I002 在接班班次关闭")
	mustCLI(t, runCLI(t, dataPath, "shift-close", "--id", "S002"), "结束 S002")

	// 重复发起前保存全部可观察结果：此时原交班班次名下已无未关闭事项，
	// 有缺陷的实现可能按“当前还留下多少事项”重新生成清单，甚至只回一行成功。
	handoverBefore := mustCLI(t, runCLI(t, dataPath, "handover-show", "--id", "H001"), "查看原交接")
	item1Before := mustCLI(t, runCLI(t, dataPath, "item-show", "--id", "I001"), "查看 I001")
	item2Before := mustCLI(t, runCLI(t, dataPath, "item-show", "--id", "I002"), "查看 I002")
	shift1Before := mustCLI(t, runCLI(t, dataPath, "shift-show", "--id", "S001"), "查看 S001")
	shift2Before := mustCLI(t, runCLI(t, dataPath, "shift-show", "--id", "S002"), "查看 S002")
	listBefore := mustCLI(t, runCLI(t, dataPath, "handover-list"), "交接清单")
	if strings.Count(listBefore.stdout, "H00") != 1 || !strings.Contains(listBefore.stdout, "H001") {
		t.Fatalf("重复发起前应只有1条交接 H001：\n%s", listBefore.stdout)
	}
	// 接收历史在重复发起前只有一次确认接收。
	if c := strings.Count(item1Before.stdout, "交接 H001（S001 -> S002）确认接收"); c != 1 {
		t.Fatalf("重复发起前 I001 应有且仅有一次确认接收，got %d 次：\n%s", c, item1Before.stdout)
	}
	bytesBefore := readData(t, dataPath)

	// 用原来的交班、接班编号重复发起：必须退出码 0、stderr 为空。
	for i := 0; i < 2; i++ {
		r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002")
		if r.code != 0 {
			t.Fatalf("第%d次重复发起应以退出状态0结束，got %d，stderr=%s", i+1, r.code, r.stderr)
		}
		if r.stderr != "" {
			t.Fatalf("第%d次重复发起错误输出应为空，不能把记录已存在或接班班次已结束报成失败，got %q",
				i+1, r.stderr)
		}
		// 输出要明确说明已有交接记录，且完整展示原记录，不能只有一行成功提示。
		if !strings.HasPrefix(r.stdout, "已存在交接记录（重复发起，返回已有记录）\n") {
			t.Fatalf("第%d次重复发起应说明已有交接记录，got：\n%s", i+1, r.stdout)
		}
		// 原编号、交班与接班关系、完整事项清单与当时那次交接完全一致：
		// 通知行之后的内容必须与 handover-show 的保存记录逐字节相同，
		// 原发起时间、完成时间、各项处理结果/操作人/处理时间都由此锁定。
		want := "已存在交接记录（重复发起，返回已有记录）\n" + handoverBefore.stdout
		if r.stdout != want {
			t.Fatalf("第%d次重复发起应完整返回保存的原记录\nwant:\n%s\ngot:\n%s", i+1, want, r.stdout)
		}
		for _, frag := range []string{
			"H001  岗位=调度  S001 -> S002",
			"共2项",
			"I001  [确认接收]  严重程度=重要  后续负责人=李四",
			"最后处理：处理人=李四；处理时间=",
			"I002  [继续跟踪]  严重程度=普通  后续负责人=赵六",
			"跟踪说明：继续盯压力",
			"跟踪后续负责人：赵六",
			"第1次退回",
			"原因=需要补充细节",
			"补充说明：补充说明如下",
			"补充人=张三",
		} {
			if !strings.Contains(r.stdout, frag) {
				t.Fatalf("第%d次重复发起输出应包含原交接内容 %q：\n%s", i+1, frag, r.stdout)
			}
		}
		// 不能按交班班次当前剩下的事项重新生成清单（此时 S001 已无未关闭事项），
		// 也不能把接班班次已结束当成失败理由写进输出。
		if !strings.Contains(r.stdout, "共2项") || strings.Contains(r.stdout, "（空清单，直接完成）") {
			t.Fatalf("重复发起必须展示接收时的原2项清单，不能重新生成：\n%s", r.stdout)
		}
		if strings.Contains(r.stdout, "已结束，不能交接") {
			t.Fatalf("重复发起不能把接班班次已结束报成失败：\n%s", r.stdout)
		}
	}

	// 成功输出正确还不够：业务记录必须一个字节都没改——交接数量、事项所在
	// 班次与处理经过、班次结束时记录、退回补充经过全部保持一致。
	if got := readData(t, dataPath); got != bytesBefore {
		t.Fatalf("重复发起不得写入任何业务数据\nbefore: %s\nafter:  %s", bytesBefore, got)
	}
	listAfter := mustCLI(t, runCLI(t, dataPath, "handover-list"), "重复后交接清单")
	if listAfter.stdout != listBefore.stdout || strings.Count(listAfter.stdout, "H00") != 1 {
		t.Fatalf("重复发起后保存的交接数量应保持为1\nbefore:\n%s\nafter:\n%s",
			listBefore.stdout, listAfter.stdout)
	}
	if r := mustCLI(t, runCLI(t, dataPath, "handover-show", "--id", "H001"), "重复后查看交接"); r.stdout != handoverBefore.stdout {
		t.Fatalf("重复发起后保存的原交接记录不得变化\nbefore:\n%s\nafter:\n%s", handoverBefore.stdout, r.stdout)
	}
	// 事项历史不能多出一次接收：item-show 全文不变，且接收事件仍只有一条。
	item1After := mustCLI(t, runCLI(t, dataPath, "item-show", "--id", "I001"), "重复后查看 I001")
	if item1After.stdout != item1Before.stdout {
		t.Fatalf("重复发起后 I001 处理经过不得变化\nbefore:\n%s\nafter:\n%s", item1Before.stdout, item1After.stdout)
	}
	if c := strings.Count(item1After.stdout, "交接 H001（S001 -> S002）确认接收"); c != 1 {
		t.Fatalf("重复发起后 I001 仍应只有一次接收，不能多出一次，got %d 次：\n%s", c, item1After.stdout)
	}
	if r := mustCLI(t, runCLI(t, dataPath, "item-show", "--id", "I002"), "重复后查看 I002"); r.stdout != item2Before.stdout {
		t.Fatalf("重复发起后 I002 处理经过不得变化\nbefore:\n%s\nafter:\n%s", item2Before.stdout, r.stdout)
	}
	if r := mustCLI(t, runCLI(t, dataPath, "shift-show", "--id", "S001"), "重复后查看 S001"); r.stdout != shift1Before.stdout {
		t.Fatalf("重复发起不得改变交班班次已留下的结束时记录\nbefore:\n%s\nafter:\n%s", shift1Before.stdout, r.stdout)
	}
	if r := mustCLI(t, runCLI(t, dataPath, "shift-show", "--id", "S002"), "重复后查看 S002"); r.stdout != shift2Before.stdout {
		t.Fatalf("重复发起不得改变接班班次已留下的结束时记录\nbefore:\n%s\nafter:\n%s", shift2Before.stdout, r.stdout)
	}
}

// TestCLIRepeatHandoverRejectsChangedOrMissingTarget：已有交接只允许原接班
// 对象。接班对象改成另一个确实存在的班次（无论该班次是否结束）都应以非零
// 状态结束，错误输出说明不能改换并指出原接班班次，正常输出不宣称成功；
// 填写不存在的班次编号应明确报不存在。两种失败均保留原交接与事项信息，
// 不产生新的交接记录。
func TestCLIRepeatHandoverRejectsChangedOrMissingTarget(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "handover-data.json")

	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"), "建立 S001")
	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T16:00:00+08:00", "--end", "2026-10-02T23:00:00+08:00"), "建立 S002")
	mustCLI(t, runCLI(t, dataPath, "item-add",
		"--shift", "S001", "--content", "事项甲", "--severity", "normal", "--follow", "李四"), "新增 I001")
	mustCLI(t, runCLI(t, dataPath, "shift-close", "--id", "S001"), "结束 S001")
	mustCLI(t, runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"), "首次发起交接")
	mustCLI(t, runCLI(t, dataPath, "handover-process",
		"--id", "H001", "--item", "I001", "--action", "confirm", "--operator", "李四"), "确认接收")
	mustCLI(t, runCLI(t, dataPath, "shift-close", "--id", "S002"), "结束原接班班次 S002")

	// 另两个确实存在的班次：一个进行中、一个已结束。
	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "钱七",
		"--start", "2026-10-03T16:00:00+08:00", "--end", "2026-10-04T00:00:00+08:00"), "建立 S003")
	mustCLI(t, runCLI(t, dataPath, "shift-add",
		"--position", "调度", "--owner", "周八",
		"--start", "2026-10-03T08:00:00+08:00", "--end", "2026-10-03T16:00:00+08:00"), "建立 S004")
	mustCLI(t, runCLI(t, dataPath, "shift-close", "--id", "S004"), "结束 S004")

	handoverBefore := mustCLI(t, runCLI(t, dataPath, "handover-show", "--id", "H001"), "失败前查看原交接")
	itemBefore := mustCLI(t, runCLI(t, dataPath, "item-show", "--id", "I001"), "失败前查看事项")
	bytesBefore := readData(t, dataPath)

	assertRejected := func(step string, args ...string) {
		t.Helper()
		r := runCLI(t, dataPath, args...)
		if r.code == 0 {
			t.Fatalf("%s 应以非零状态结束，got 0，stdout=%s", step, r.stdout)
		}
		if r.stdout != "" {
			t.Fatalf("%s 失败时正常输出不应宣称成功，got stdout=%q", step, r.stdout)
		}
		if r.stderr == "" {
			t.Fatalf("%s 应在错误输出中明确说明原因", step)
		}
		// 任何失败尝试后数据与原记录都必须原样保留。
		if got := readData(t, dataPath); got != bytesBefore {
			t.Fatalf("%s 不得改动业务数据\nbefore: %s\nafter:  %s", step, bytesBefore, got)
		}
		if r := runCLI(t, dataPath, "handover-list"); r.code != 0 ||
			strings.Count(r.stdout, "H00") != 1 || !strings.Contains(r.stdout, "H001") {
			t.Fatalf("%s 后不应产生新的交接记录，list=%s", step, r.stdout)
		}
		if r := runCLI(t, dataPath, "handover-show", "--id", "H001"); r.stdout != handoverBefore.stdout {
			t.Fatalf("%s 后原交接记录应保留\nbefore:\n%s\nafter:\n%s", step, handoverBefore.stdout, r.stdout)
		}
		if r := runCLI(t, dataPath, "item-show", "--id", "I001"); r.stdout != itemBefore.stdout {
			t.Fatalf("%s 后事项信息应保留\nbefore:\n%s\nafter:\n%s", step, itemBefore.stdout, r.stdout)
		}
	}

	// 改换为尚未结束的其他班次：明确拒绝并指出原接班班次。
	r := runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S003")
	if r.code == 0 || r.stdout != "" {
		t.Fatalf("改换为进行中班次应失败且无正常输出，code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "不能改换接班对象") || !strings.Contains(r.stderr, "S002") {
		t.Fatalf("错误输出应说明不能改换并指出原接班班次 S002，got %q", r.stderr)
	}
	assertRejected("改换为进行中班次 S003", "handover-create", "--from", "S001", "--to", "S003")

	// 改换为已结束的班次：仍按改换对象拒绝，不能误报“接班班次已结束”。
	r = runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S004")
	if r.code == 0 || r.stdout != "" {
		t.Fatalf("改换为已结束班次应失败且无正常输出，code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "不能改换接班对象") || !strings.Contains(r.stderr, "S002") {
		t.Fatalf("错误输出应说明不能改换并指出原接班班次 S002，got %q", r.stderr)
	}
	if strings.Contains(r.stderr, "已结束") {
		t.Fatalf("改换对象即使已结束也应报不能改换，不能报接班班次已结束，got %q", r.stderr)
	}
	assertRejected("改换为已结束班次 S004", "handover-create", "--from", "S001", "--to", "S004")

	// 接班编号不存在：明确报不存在，不能被已有记录掩盖。
	r = runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S999")
	if r.code == 0 || r.stdout != "" {
		t.Fatalf("接班编号不存在应失败且无正常输出，code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "不存在") || !strings.Contains(r.stderr, "S999") {
		t.Fatalf("错误输出应明确报接班班次不存在并带出编号，got %q", r.stderr)
	}
	assertRejected("接班编号不存在 S999", "handover-create", "--from", "S001", "--to", "S999")

	// 交班编号不存在同样明确报错。
	r = runCLI(t, dataPath, "handover-create", "--from", "S998", "--to", "S002")
	if r.code == 0 || r.stdout != "" {
		t.Fatalf("交班编号不存在应失败且无正常输出，code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "不存在") || !strings.Contains(r.stderr, "S998") {
		t.Fatalf("错误输出应明确报交班班次不存在并带出编号，got %q", r.stderr)
	}
	assertRejected("交班编号不存在 S998", "handover-create", "--from", "S998", "--to", "S002")
}
