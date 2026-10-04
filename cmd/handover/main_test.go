package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengzyyys/shift-handover/handover"
)

// runCLI 以给定的数据文件环境变量调用命令行入口，返回标准输出、标准错误和退出码。
// envPath 为空表示不设置 HANDOVER_DATA（只能靠 --data 或默认文件）。
func runCLI(t *testing.T, envPath string, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("HANDOVER_DATA", envPath)
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func setupShift(t *testing.T, dataPath string) {
	t.Helper()
	_, errOut, code := runCLI(t, dataPath, "shift-add",
		"--position", "岗A", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00",
		"--end", "2026-10-02T16:00:00+08:00")
	if code != 0 {
		t.Fatalf("建立班次失败：%s", errOut)
	}
}

func readData(t *testing.T, path string) handover.Data {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取数据文件 %s 失败：%v", path, err)
	}
	var d handover.Data
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("解析数据文件 %s 失败：%v", path, err)
	}
	return d
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s 不应被读取、创建或改写，但文件存在", path)
	}
}

// 事项内容恰为 “--data=另一个文件名” 时，它只是正文：操作必须落在明确
// 指定的甲文件，乙文件不能被创建。
func TestItemAddContentDataEqualsFormIsBody(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "jia.json")
	b := filepath.Join(dir, "yi.json")
	setupShift(t, a)

	// 全局 --data 用 --data=路径 形式放在命令前，正文是 --data=乙文件。
	_, errOut, code := runCLI(t, "", "item-add", "--data="+a,
		"--shift", "S001", "--content", "--data="+b,
		"--severity", "normal", "--follow", "李四")
	if code != 0 {
		t.Fatalf("新增事项失败：%s", errOut)
	}

	d := readData(t, a)
	if len(d.Items) != 1 {
		t.Fatalf("甲文件应有 1 个事项，实际 %d", len(d.Items))
	}
	it := d.Items[0]
	if it.Content != "--data="+b {
		t.Fatalf("事项内容应完整保存为 --data=%s，实际 %q", b, it.Content)
	}
	if it.CurrentShiftID != "S001" || it.OriginShiftID != "S001" {
		t.Fatalf("事项应属于班次 S001，实际 当前=%s 原始=%s", it.CurrentShiftID, it.OriginShiftID)
	}
	if it.FollowOwner != "李四" {
		t.Fatalf("后续负责人应为李四，实际 %q", it.FollowOwner)
	}
	assertNotExists(t, b)
}

// 内容恰为 “--data” 时，它后面用于严重程度、限制条件、后续负责人的
// 独立参数仍按原意生效，不能被正文取走。
func TestItemAddContentDataTokenIsBody(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	setupShift(t, a)

	// 全局 --data 单独成词并放在命令之后；正文恰为 --data。
	_, errOut, code := runCLI(t, "", "item-add",
		"--shift", "S001", "--content", "--data",
		"--severity", "urgent", "--constraints", "只能白天操作",
		"--follow", "王五", "--data", a)
	if code != 0 {
		t.Fatalf("新增事项失败：%s", errOut)
	}

	d := readData(t, a)
	if len(d.Items) != 1 {
		t.Fatalf("应有 1 个事项，实际 %d", len(d.Items))
	}
	it := d.Items[0]
	if it.Content != "--data" {
		t.Fatalf("事项内容应为字面量 --data，实际 %q", it.Content)
	}
	if it.Severity != handover.SeverityUrgent {
		t.Fatalf("严重程度应为紧急，实际 %q", it.Severity)
	}
	if it.Constraints != "只能白天操作" {
		t.Fatalf("限制条件应按独立参数保存，实际 %q", it.Constraints)
	}
	if it.FollowOwner != "王五" {
		t.Fatalf("后续负责人应为王五，实际 %q", it.FollowOwner)
	}
}

// --name=value 形式的正文同样原样保存。
func TestItemAddContentEqualsDataEqualsForm(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	setupShift(t, a)

	_, errOut, code := runCLI(t, "", "item-add", "--data", a,
		"--shift", "S001", "--content=--data="+b,
		"--severity=normal", "--follow=李四")
	if code != 0 {
		t.Fatalf("新增事项失败：%s", errOut)
	}
	it := readData(t, a).Items[0]
	if it.Content != "--data="+b {
		t.Fatalf("事项内容应为 --data=%s，实际 %q", b, it.Content)
	}
	assertNotExists(t, b)
}

// item-show 能按现有首尾空白处理规则看到完整正文，参数样式片段不丢失。
func TestItemShowDataLikeContentVisible(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	setupShift(t, a)

	body := "--data=" + filepath.Join(dir, "yi.json") + "  以 --data= 开头的整段说明"
	_, errOut, code := runCLI(t, "", "item-add", "--data", a,
		"--shift", "S001", "--content", "  "+body+"  ",
		"--severity", "normal", "--constraints", " ", "--follow", "李四")
	if code != 0 {
		t.Fatalf("新增事项失败：%s", errOut)
	}

	out, errOut, code := runCLI(t, "", "item-show", "--id", "I001", "--data", a)
	if code != 0 {
		t.Fatalf("查询事项失败：%s", errOut)
	}
	if !strings.Contains(out, "内容："+body) {
		t.Fatalf("item-show 应展示去掉首尾空白后的完整正文 %q，实际输出：\n%s", body, out)
	}
	if !strings.Contains(out, "限制条件：-") {
		t.Fatalf("可选限制条件为空时应显示 -，实际输出：\n%s", out)
	}
}

// 修改事项时正文与限制条件都可以恰为 --data / --data=乙文件；
// 原事项编号和所属班次不变，且乙文件即使已有同编号记录也不受影响。
func TestItemUpdateDataLikeFields(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	setupShift(t, a)

	// 乙文件已有自己的 S001/I001 记录，内容不同。
	setupShift(t, b)
	_, errOut, code := runCLI(t, b, "item-add",
		"--shift", "S001", "--content", "乙文件原事项",
		"--severity", "normal", "--follow", "乙负责人")
	if code != 0 {
		t.Fatalf("乙文件准备数据失败：%s", errOut)
	}
	bBefore, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("读取乙文件失败：%v", err)
	}

	// 甲文件先新增 I001。
	_, errOut, code = runCLI(t, a, "item-add",
		"--shift", "S001", "--content", "原内容",
		"--severity", "normal", "--follow", "李四")
	if code != 0 {
		t.Fatalf("甲文件新增事项失败：%s", errOut)
	}

	// 修改：内容恰为 --data=乙文件，限制条件恰为 --data。
	_, errOut, code = runCLI(t, "", "item-update", "--data="+a,
		"--id", "I001", "--content", "--data="+b,
		"--severity", "important", "--constraints", "--data",
		"--follow", "赵六")
	if code != 0 {
		t.Fatalf("修改事项失败：%s", errOut)
	}

	d := readData(t, a)
	if len(d.Items) != 1 {
		t.Fatalf("甲文件仍应只有 1 个事项，实际 %d", len(d.Items))
	}
	it := d.Items[0]
	if it.ID != "I001" {
		t.Fatalf("修改后事项编号应保持 I001，实际 %q", it.ID)
	}
	if it.CurrentShiftID != "S001" || it.OriginShiftID != "S001" {
		t.Fatalf("修改后所属班次应保持 S001，实际 当前=%s 原始=%s", it.CurrentShiftID, it.OriginShiftID)
	}
	if it.Content != "--data="+b {
		t.Fatalf("内容应体现本次输入 --data=%s，实际 %q", b, it.Content)
	}
	if it.Constraints != "--data" {
		t.Fatalf("限制条件应恰为 --data，实际 %q", it.Constraints)
	}
	if it.Severity != handover.SeverityImportant || it.FollowOwner != "赵六" {
		t.Fatalf("严重程度与后续负责人应体现本次输入，实际 %q %q", it.Severity, it.FollowOwner)
	}

	bAfter, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("乙文件应保持存在且可读：%v", err)
	}
	if !bytes.Equal(bBefore, bAfter) {
		t.Fatalf("乙文件不能因甲文件的修改被改写")
	}
	if got := readData(t, b).Items[0].Content; got != "乙文件原事项" {
		t.Fatalf("乙文件 I001 内容应保持不变，实际 %q", got)
	}
}

// 单独提供的 --data 路径（两种形式、命令前后）仍决定数据文件；
// 显式指定优先于 HANDOVER_DATA，未指定时沿用环境变量。
func TestGlobalDataSelection(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	env := filepath.Join(dir, "env.json")
	notUsed := filepath.Join(dir, "should-not-exist.json")
	setupShift(t, a)
	setupShift(t, env)

	// 仅环境变量：事项落到环境变量文件。
	_, errOut, code := runCLI(t, env, "item-add",
		"--shift", "S001", "--content", "环境文件事项",
		"--severity", "normal", "--follow", "李四")
	if code != 0 {
		t.Fatalf("按环境变量新增事项失败：%s", errOut)
	}
	if len(readData(t, env).Items) != 1 {
		t.Fatal("事项应写入 HANDOVER_DATA 指定的文件")
	}

	// 显式 --data 优先于环境变量：环境变量给一个不存在的路径，
	// 操作仍应落在甲文件，环境变量路径不能被创建。
	_, errOut, code = runCLI(t, notUsed, "item-add",
		"--shift", "S001", "--content", "显式文件事项",
		"--severity", "normal", "--follow", "李四", "--data", a)
	if code != 0 {
		t.Fatalf("按显式 --data 新增事项失败：%s", errOut)
	}
	if len(readData(t, a).Items) != 1 {
		t.Fatal("显式 --data 应优先于 HANDOVER_DATA")
	}
	assertNotExists(t, notUsed)
}

// 必填校验与状态限制保持不变：空白内容失败、编号不存在失败，且不产生变更；
// 正文 --data 后面缺少独立参数时仍按缺参处理。
func TestDataLikeBodyStillValidated(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	setupShift(t, a)

	// 内容只有空白：明确失败。
	_, _, code := runCLI(t, a, "item-add",
		"--shift", "S001", "--content", "   ",
		"--severity", "normal", "--follow", "李四")
	if code != 1 {
		t.Fatalf("空白内容应失败，实际退出码 %d", code)
	}
	if len(readData(t, a).Items) != 0 {
		t.Fatal("空白内容失败时不能产生事项")
	}

	// 正文恰为 --data，但缺少后续负责人：仍按缺少必填项失败，
	// --data 不会被当成数据文件参数（它后面没有路径，也不应取走其它参数）。
	_, errOut, code := runCLI(t, "", "item-add", "--data", a,
		"--shift", "S001", "--content", "--data", "--severity", "normal")
	if code != 1 {
		t.Fatalf("缺少后续负责人应失败，实际退出码 %d", code)
	}
	if !strings.Contains(errOut, "后续负责人") {
		t.Fatalf("应提示后续负责人不能为空，实际：%s", errOut)
	}
	if len(readData(t, a).Items) != 0 {
		t.Fatal("校验失败时不能产生事项")
	}

	// 修改选定文件中不存在的编号：明确失败，不产生变更。
	_, errOut, code = runCLI(t, a, "item-update",
		"--id", "I999", "--content", "--data=其它.json",
		"--severity", "normal", "--follow", "李四")
	if code != 1 {
		t.Fatalf("不存在的事项编号应失败，实际退出码 %d", code)
	}
	if !strings.Contains(errOut, "I999") {
		t.Fatalf("应指出不存在的编号，实际：%s", errOut)
	}
	if len(readData(t, a).Items) != 0 {
		t.Fatal("编号不存在时不能产生事项变更")
	}
}
