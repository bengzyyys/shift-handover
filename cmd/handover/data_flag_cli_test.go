package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“显式指定 --data 却没给出有效路径”
// 建立端到端回归保障：此时必须明确失败（退出码 1、标准错误说明路径问题、
// 标准输出不展示业务数据或成功提示），不能按未指定参数处理而回退到
// HANDOVER_DATA 或默认 handover-data.json，也不能创建或改写任何数据文件。
// 路径填写正常时的既有选择规则（显式 --data 优先、其次 HANDOVER_DATA、
// 最后默认文件）与带空格文件名保持不变，一并在此覆盖。

// runRawCLI 以独立进程运行真实程序，参数原样传入（不自动附加 --data），
// 工作目录设为 dir，可附带环境变量。
func runRawCLI(t *testing.T, dir string, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
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

// TestCLIEmptyDataPathFails 覆盖空路径的全部写法：独立空字符串（命令前后）、
// --data= 后无内容（命令前后）、末尾独立 --data 后没有值。统一要求退出码 1、
// stderr 提示路径问题、stdout 为空，且不创建默认数据文件。
func TestCLIEmptyDataPathFails(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"命令前 --data 空字符串", []string{"--data", "", "shift-list"}, "数据文件路径不能为空"},
		{"命令后 --data 空字符串", []string{"shift-list", "--data", ""}, "数据文件路径不能为空"},
		{"命令前 --data= 无内容", []string{"--data=", "shift-list"}, "数据文件路径不能为空"},
		{"命令后 --data= 无内容", []string{"shift-list", "--data="}, "数据文件路径不能为空"},
		{"末尾独立 --data 缺少路径", []string{"shift-list", "--data"}, "缺少数据文件路径"},
		{"写入命令同样拒绝空路径", []string{"--data", "", "shift-add", "--position", "调度", "--owner", "张三",
			"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"}, "数据文件路径不能为空"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := runRawCLI(t, dir, nil, tc.args...)
			if r.exitCode != 1 {
				t.Fatalf("期望退出码 1，got %d；stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
			}
			if r.stdout != "" {
				t.Fatalf("失败时标准输出不应展示班次/事项/交接数据或成功提示，got %q", r.stdout)
			}
			if !strings.Contains(r.stderr, tc.wantErr) {
				t.Fatalf("错误输出应包含 %q，got %q", tc.wantErr, r.stderr)
			}
			// 不能因回退默认文件而创建 handover-data.json。
			if _, err := os.Stat(filepath.Join(dir, "handover-data.json")); !os.IsNotExist(err) {
				t.Fatalf("空路径失败不应创建默认数据文件，stat err=%v", err)
			}
		})
	}
}

// TestCLIEmptyDataPathDoesNotFallBack 即使默认文件已有记录、HANDOVER_DATA
// 指向另一个可用文件，显式空路径也必须失败，且两份文件都保持原样。
func TestCLIEmptyDataPathDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()

	// 默认文件与环境变量文件都先建立真实记录。
	defaultPath := filepath.Join(dir, "handover-data.json")
	envPath := filepath.Join(dir, "env.json")
	for _, p := range []string{defaultPath, envPath} {
		runCLI(t, p,
			"shift-add", "--position", "调度", "--owner", "张三",
			"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00").ok(t, "预置班次")
	}
	defaultHash := hashFile(t, defaultPath)
	envHash := hashFile(t, envPath)

	env := []string{"HANDOVER_DATA=" + envPath}
	for _, args := range [][]string{
		{"--data", "", "shift-list"},
		{"shift-list", "--data="},
		{"shift-list", "--data"},
	} {
		r := runRawCLI(t, dir, env, args...)
		if r.exitCode != 1 {
			t.Fatalf("%v：期望退出码 1，got %d；stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
		if r.stdout != "" {
			t.Fatalf("%v：不应转向默认文件或环境变量文件展示班次，got %q", args, r.stdout)
		}
		if !strings.Contains(r.stderr, "数据文件路径") {
			t.Fatalf("%v：错误输出应说明路径问题，got %q", args, r.stderr)
		}
	}
	if h := hashFile(t, defaultPath); h != defaultHash {
		t.Fatalf("默认数据文件不应被改写")
	}
	if h := hashFile(t, envPath); h != envHash {
		t.Fatalf("环境变量指向的数据文件不应被改写")
	}
}

// TestCLIEmptyDataPathWithCorruptedFallbacks 默认文件与环境变量文件本身损坏时，
// 仍应报告本次路径参数的问题，不能以读取/解析错误掩盖输入错误。
func TestCLIEmptyDataPathWithCorruptedFallbacks(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.json")
	for _, p := range []string{filepath.Join(dir, "handover-data.json"), envPath} {
		if err := os.WriteFile(p, []byte("{不是合法json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := runRawCLI(t, dir, []string{"HANDOVER_DATA=" + envPath}, "--data", "", "shift-list")
	if r.exitCode != 1 {
		t.Fatalf("期望退出码 1，got %d；stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stderr, "数据文件路径不能为空") {
		t.Fatalf("应报告路径参数问题而非文件损坏，got %q", r.stderr)
	}
	if strings.Contains(r.stderr, "损坏") || strings.Contains(r.stderr, "读取数据文件失败") {
		t.Fatalf("不能用读取错误掩盖输入错误，got %q", r.stderr)
	}
}

// TestCLIDataPathSelectionRulesUnchanged 路径填写正常时沿用既有选择规则：
// 显式 --data 优先于 HANDOVER_DATA，都未提供时用默认文件；带空格的非空
// 文件名按完整路径使用。
func TestCLIDataPathSelectionRulesUnchanged(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.json")
	envPath := filepath.Join(dir, "env.json")
	spaced := filepath.Join(dir, "我的 班次 数据.json")
	env := []string{"HANDOVER_DATA=" + envPath}
	addShift := func(args ...string) string {
		base := []string{"shift-add", "--position", "调度", "--owner", "张三",
			"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"}
		return runRawCLI(t, dir, env, append(base, args...)...).ok(t, "建立班次")
	}

	// 显式 --data（命令后）优先于环境变量。
	addShift("--data", explicit)
	if _, err := os.Stat(explicit); err != nil {
		t.Fatalf("显式 --data 的文件应被写入：%v", err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("显式指定时不应写环境变量文件")
	}

	// 未显式指定时使用 HANDOVER_DATA。
	addShift()
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("未显式指定时应写环境变量文件：%v", err)
	}

	// 环境变量也没有时使用默认 handover-data.json。
	r := runRawCLI(t, dir, nil, "shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00")
	r.ok(t, "默认文件建立班次")
	if _, err := os.Stat(filepath.Join(dir, "handover-data.json")); err != nil {
		t.Fatalf("无环境变量时应写默认文件：%v", err)
	}

	// 带空格的非空文件名按完整路径使用。
	addShift("--data", spaced)
	out := runRawCLI(t, dir, nil, "--data", spaced, "shift-list").ok(t, "带空格文件名列出班次")
	if !strings.Contains(out, "S001") {
		t.Fatalf("带空格文件应作为完整路径读写，got:\n%s", out)
	}
	if _, err := os.Stat(spaced); err != nil {
		t.Fatalf("带空格的文件名不应被拆开或改名：%v", err)
	}
}
