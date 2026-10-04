package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从命令行公开行为出发，对“一次建立的新班次同时与同岗位多个已有班次
// 重叠”的场景做端到端回归：走 run() 与本地 JSON 文件，覆盖建立、按班次查询
// 说明、报错指出全部冲突班次、失败不跳号、不同岗位不收说明以及跨时区按实际
// 时刻判断。

// runCLI 在给定数据文件上执行一条命令，返回退出码与标准输出、标准错误。
func runCLI(t *testing.T, dataPath string, args ...string) (int, string, string) {
	t.Helper()
	argv := append([]string{"--data", dataPath}, args...)
	var out, errOut bytes.Buffer
	code := run(argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func cliDataPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "handover-data.json")
}

// TestCLIShiftSpanningTwoShiftsNotesAndQueries：命令行建立 08:00-12:00 与
// 12:00-16:00 两班后，新班次 10:00-14:00 一次跨过两班。
func TestCLIShiftSpanningTwoShiftsNotesAndQueries(t *testing.T) {
	path := cliDataPath(t)

	must := func(args ...string) string {
		t.Helper()
		code, out, errOut := runCLI(t, path, args...)
		if code != 0 {
			t.Fatalf("命令 %v 应成功，exit=%d stderr=%s", args, code, errOut)
		}
		return out
	}
	mustFail := func(args ...string) string {
		t.Helper()
		code, _, errOut := runCLI(t, path, args...)
		if code != 1 {
			t.Fatalf("命令 %v 应以退出码1失败，got %d", args, code)
		}
		return errOut
	}

	must("shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T12:00:00+08:00")
	must("shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T12:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00")

	// 不填说明：拒绝，错误必须同时列出两个冲突班次。
	errOut := mustFail("shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00")
	if !strings.Contains(errOut, "S001") || !strings.Contains(errOut, "S002") {
		t.Fatalf("重叠错误应同时指出两个冲突班次 S001、S002：%s", errOut)
	}
	// 纯空白说明同样拒绝。
	if code, _, _ := runCLI(t, path, "shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00",
		"--note", "   "); code != 1 {
		t.Fatalf("纯空白说明应被拒绝")
	}
	// 失败不跳号：列表里仍只有两班。
	listOut := must("shift-list")
	if strings.Contains(listOut, "S003") {
		t.Fatalf("失败后不应出现 S003：\n%s", listOut)
	}

	// 补齐有效说明再次建立：编号从原来的下一个 S003 继续。
	const noteText = "交接班高峰，跨两班并行"
	addOut := must("shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00",
		"--note", noteText)
	if !strings.Contains(addOut, "已建立班次 S003") {
		t.Fatalf("失败不应跳号，重试应建立 S003：\n%s", addOut)
	}

	// 从新班次查看：两条说明，分别关联 S001、S002，内容沿用输入。
	spanNotes := must("note-list", "--id", "S003")
	if !strings.Contains(spanNotes, "班次 S001 <-> S003") ||
		!strings.Contains(spanNotes, "班次 S002 <-> S003") {
		t.Fatalf("新班次应看到与两班的两条说明：\n%s", spanNotes)
	}
	if c := strings.Count(spanNotes, "说明："+noteText); c != 2 {
		t.Fatalf("两条说明都应沿用用户输入，期望出现2次，got %d：\n%s", c, spanNotes)
	}

	// 从任一已有班次查看：只看到涉及自己的那一条，不夹带另一班。
	firstNotes := must("note-list", "--id", "S001")
	if !strings.Contains(firstNotes, "班次 S001 <-> S003") {
		t.Fatalf("第一班应看到涉及自己的说明：\n%s", firstNotes)
	}
	if strings.Contains(firstNotes, "S002") {
		t.Fatalf("第一班不应看到涉及第二班的关系：\n%s", firstNotes)
	}
	if c := strings.Count(firstNotes, "说明："); c != 1 {
		t.Fatalf("第一班应只有一条说明，got %d：\n%s", c, firstNotes)
	}
	secondNotes := must("note-list", "--id", "S002")
	if !strings.Contains(secondNotes, "班次 S002 <-> S003") || strings.Contains(secondNotes, "S001") {
		t.Fatalf("第二班只应看到涉及自己的一条：\n%s", secondNotes)
	}

	// 同时间段但不同岗位的班次：无需说明即可建立，也收不到这段说明。
	otherOut := must("shift-add", "--position", "巡检", "--owner", "赵六",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00")
	if !strings.Contains(otherOut, "已建立班次 S004") {
		t.Fatalf("不同岗位班次应正常建立并得到 S004：\n%s", otherOut)
	}
	otherNotes := must("note-list", "--id", "S004")
	if !strings.Contains(otherNotes, "（无重叠说明）") {
		t.Fatalf("不同岗位班次不应收到任何重叠说明：\n%s", otherNotes)
	}
}

// TestCLIShiftSpanningAcrossTimeZones：命令行传入不同时区偏移的起止时间，
// 换算后同一时段应得到相同的两条重叠关系；不填说明时报错要同时指出两班。
func TestCLIShiftSpanningAcrossTimeZones(t *testing.T) {
	path := cliDataPath(t)

	must := func(args ...string) {
		t.Helper()
		code, _, errOut := runCLI(t, path, args...)
		if code != 0 {
			t.Fatalf("命令 %v 应成功：%s", args, errOut)
		}
	}
	must("shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-05T08:00:00+08:00", "--end", "2026-10-05T12:00:00+08:00")
	must("shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-05T12:00:00+08:00", "--end", "2026-10-05T16:00:00+08:00")

	// 10:00-14:00 +08 用不同偏移表达：起点 11:00 +09，终点 13:00 +07。
	code, _, errOut := runCLI(t, path, "shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-05T11:00:00+09:00", "--end", "2026-10-05T13:00:00+07:00")
	if code != 1 || !strings.Contains(errOut, "S001") || !strings.Contains(errOut, "S002") {
		t.Fatalf("换算后与两班重叠、未填说明应拒绝并指出 S001、S002，code=%d：%s", code, errOut)
	}

	code, out, _ := runCLI(t, path, "shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-05T11:00:00+09:00", "--end", "2026-10-05T13:00:00+07:00",
		"--note", "跨时区同一时段")
	if code != 0 || !strings.Contains(out, "已建立班次 S003") {
		t.Fatalf("跨时区换算后同一时段、已填说明应成功建立 S003，code=%d：%s", code, out)
	}
	_, notes, _ := runCLI(t, path, "note-list", "--id", "S003")
	if c := strings.Count(notes, "说明：跨时区同一时段"); c != 2 ||
		!strings.Contains(notes, "S001 <-> S003") || !strings.Contains(notes, "S002 <-> S003") {
		t.Fatalf("跨时区也应得到与两班的两条说明：\n%s", notes)
	}
}
