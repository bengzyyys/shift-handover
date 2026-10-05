package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“显式指定数据文件却没给出有效路径”
// 建立端到端回归保障：--data 后为空字符串、--data= 后没有内容、命令末尾只有
// 独立 --data，都必须明确失败（退出码 1、标准错误说明路径问题、标准输出不
// 展示数据或成功信息），不能回落到 HANDOVER_DATA 或默认 handover-data.json
// 继续执行，也不能创建或改写任何数据文件。

// runCLIRaw 以独立进程运行真实程序，不自动附加 --data，可控制环境变量，
// 返回退出码与两路输出。workDir 为命令工作目录。
func runCLIRaw(t *testing.T, workDir string, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Dir = workDir
	cmd.Env = env
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

// baseEnv 构造不携带 HANDOVER_DATA 的环境，需要时由用例自行追加。
func baseEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HANDOVER_DATA=") {
			env = append(env, kv)
		}
	}
	return env
}

// TestCLIEmptyOrMissingDataPathFails 覆盖三种无效路径写法（命令前后位置均
// 包含）：空字符串、--data= 空、末尾独立 --data，都必须以退出码 1 失败，
// 标准错误提示路径问题，标准输出为空，且不创建默认数据文件。
func TestCLIEmptyOrMissingDataPathFails(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"命令前 --data 空字符串", []string{"--data", "", "shift-list"}, "不能为空"},
		{"命令后 --data 空字符串", []string{"shift-add", "--position", "调度", "--owner", "张三", "--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00", "--data", ""}, "不能为空"},
		{"--data= 后没有内容", []string{"shift-list", "--data="}, "不能为空"},
		{"命令末尾独立 --data", []string{"shift-list", "--data"}, "缺少数据文件路径"},
		{"只有独立 --data 没有命令", []string{"--data"}, "缺少数据文件路径"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			res := runCLIRaw(t, workDir, baseEnv(), tc.args...)
			if res.exitCode != 1 {
				t.Fatalf("期望退出码 1，got %d；stdout=%q stderr=%q", res.exitCode, res.stdout, res.stderr)
			}
			if res.stdout != "" {
				t.Fatalf("失败时标准输出应为空（不展示数据/成功/帮助），got %q", res.stdout)
			}
			if !strings.Contains(res.stderr, tc.want) {
				t.Fatalf("标准错误应提示 %q，got %q", tc.want, res.stderr)
			}
			// 不能因本次失败命令创建默认数据文件。
			if _, err := os.Stat(filepath.Join(workDir, "handover-data.json")); !os.IsNotExist(err) {
				t.Fatalf("失败命令不得创建默认数据文件，stat err=%v", err)
			}
		})
	}
}

// TestCLIEmptyDataPathDoesNotFallBack 验证：即使 HANDOVER_DATA 指向可用文件、
// 默认文件也已有记录，空路径仍然失败，不借它们完成操作、不改写它们。
func TestCLIEmptyDataPathDoesNotFallBack(t *testing.T) {
	workDir := t.TempDir()

	// 准备一份已有记录的默认文件，并让 HANDOVER_DATA 指向另一份可用文件。
	defaultPath := filepath.Join(workDir, "handover-data.json")
	envPath := filepath.Join(workDir, "env.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}
	must("默认文件建立班次", runCLI(t, defaultPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	must("环境变量文件建立班次", runCLI(t, envPath,
		"shift-add", "--position", "巡检", "--owner", "李四",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	defaultBefore := hashFile(t, defaultPath)
	envBefore := hashFile(t, envPath)

	env := append(baseEnv(), "HANDOVER_DATA="+envPath)
	// 空路径 + 写入命令：若回落到环境变量或默认文件，这里会真的建班。
	res := runCLIRaw(t, workDir, env,
		"shift-add", "--position", "抢修", "--owner", "王五",
		"--start", "2026-10-03T08:00:00+08:00", "--end", "2026-10-03T16:00:00+08:00",
		"--data", "")
	if res.exitCode != 1 {
		t.Fatalf("空路径应失败（退出码 1），got %d；stdout=%q stderr=%q", res.exitCode, res.stdout, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("失败时标准输出应为空，got %q", res.stdout)
	}
	if !strings.Contains(res.stderr, "不能为空") {
		t.Fatalf("标准错误应提示路径不能为空，got %q", res.stderr)
	}
	if after := hashFile(t, defaultPath); after != defaultBefore {
		t.Fatalf("空路径失败不得改写默认数据文件")
	}
	if after := hashFile(t, envPath); after != envBefore {
		t.Fatalf("空路径失败不得改写 HANDOVER_DATA 指向的文件")
	}
}

// TestCLIEmptyDataPathNotMaskedByCorruptFallback 验证：默认文件与环境变量
// 文件本身损坏时，仍报告这次路径参数的问题，不能以读取错误掩盖输入错误。
func TestCLIEmptyDataPathNotMaskedByCorruptFallback(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "handover-data.json"), []byte("{损坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(baseEnv(), "HANDOVER_DATA="+filepath.Join(workDir, "handover-data.json"))

	res := runCLIRaw(t, workDir, env, "shift-list", "--data=")
	if res.exitCode != 1 {
		t.Fatalf("期望退出码 1，got %d；stderr=%q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stderr, "不能为空") {
		t.Fatalf("应报告路径参数问题（不能为空），而不是损坏文件的读取错误，got %q", res.stderr)
	}
}

// TestCLIBusinessValueThatLooksLikeDataFlag 验证：事项正文等业务参数的值
// 即使恰好是 --data、--data= 或 --data=乙.json，也原样作为业务文字使用，
// 不触发路径错误、不切换数据文件，后续参数照常生效。
func TestCLIBusinessValueThatLooksLikeDataFlag(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "real.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}
	must("建立班次", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))

	for _, content := range []string{"--data", "--data=", "--data=乙.json"} {
		out := runCLI(t, dataPath,
			"item-add", "--shift", "S001", "--content", content,
			"--severity", "urgent", "--follow", "李四").ok(t, "正文为 "+content)
		if !strings.Contains(out, content) || !strings.Contains(out, "紧急") {
			t.Fatalf("正文 %q 应原样写入且后续参数（严重程度等）生效，got:\n%s", content, out)
		}
	}
	// 全部写进显式指定的 real.json；工作目录不应出现 乙.json 等其他数据文件。
	t.Cleanup(func() { os.Remove("乙.json") })
	if _, err := os.Stat("乙.json"); !os.IsNotExist(err) {
		t.Fatalf("不应把正文中的 --data=乙.json 当成数据文件，stat err=%v", err)
	}
}
