package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件从用户实际使用命令行的角度，为“建立班次（shift-add，新班次同时与
// 同岗位两班重叠并填写说明）在本地数据保存阶段失败”建立端到端回归保障：
// 已有同岗位两班 08:00-12:00 与 12:00-16:00（彼此仅端点相接），新班次
// 10:00-14:00 与两班都实际重叠、说明非空、班次信息合法，而数据文件的临时
// 写入路径被占用（保存必然失败）。此时：
//   - 命令必须非零退出、错误输出明确是保存失败，正常输出为空——不显示
//     “已建立班次”的成功提示，也不展示尚未保存的班次编号；错误输出不能把
//     保存失败说成缺少说明或时间不合法；
//   - 数据文件一个字节不变；shift-list、note-list 都仍是失败前的全部信息：
//     没有新班次，两个已有同岗位班次没有这次尝试产生的说明，其他岗位班次
//     间的既有说明保持原样；
//   - 恢复保存后用同样的建立信息重试应成功，新班次取得 S005、两条说明取得
//     N002/N003（失败尝试不占用编号），说明只关联这次成功建立的新班次。

// setupOverlappingShiftsForAdd 按用户真实步骤建立场景并返回数据文件路径：
// S001（调度 张三 08:00-12:00）、S002（调度 李四 12:00-16:00，与 S001 仅端点
// 相接）；S003（巡检 赵六 08:00-12:00）、S004（巡检 钱七 09:00-13:00）是同
// 时间段的其他岗位重叠班次，其间已保存既有说明 N001。
func setupOverlappingShiftsForAdd(t *testing.T) string {
	t.Helper()
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	must := func(what string, r cliResult) {
		t.Helper()
		r.ok(t, what)
	}

	must("建立同岗位第一班", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "张三",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T12:00:00+08:00"))
	must("建立同岗位第二班", runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "李四",
		"--start", "2026-10-02T12:00:00+08:00", "--end", "2026-10-02T16:00:00+08:00"))
	must("建立其他岗位班次一", runCLI(t, dataPath,
		"shift-add", "--position", "巡检", "--owner", "赵六",
		"--start", "2026-10-02T08:00:00+08:00", "--end", "2026-10-02T12:00:00+08:00"))
	must("建立其他岗位班次二（留下既有说明）", runCLI(t, dataPath,
		"shift-add", "--position", "巡检", "--owner", "钱七",
		"--start", "2026-10-02T09:00:00+08:00", "--end", "2026-10-02T13:00:00+08:00",
		"--note", "巡检既有重叠说明"))
	return dataPath
}

// shiftAddArgs 是本次建立操作的完整参数：新班次 10:00-14:00 与同岗位两班都
// 实际重叠，说明非空。
func shiftAddArgs() []string {
	return []string{
		"shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00",
		"--note", "抢修期间两班并行交接",
	}
}

// assertCLIShiftAddRolledBack 逐项核对：失败的建立没有在任何查询视图中留下
// 痕迹——班次列表仍是失败前的四个班次，两个已有同岗位班次没有说明，其他
// 岗位班次的既有说明保持原样。
func assertCLIShiftAddRolledBack(t *testing.T, dataPath string) {
	t.Helper()

	// 班次列表：仍是失败前的四个班次，没有新班次。
	list := runCLI(t, dataPath, "shift-list").ok(t, "失败后列出班次")
	for _, want := range []string{"S001", "S002", "S003", "S004"} {
		if !strings.Contains(list, want) {
			t.Fatalf("失败后班次列表应保留失败前的班次 %q，got:\n%s", want, list)
		}
	}
	if strings.Contains(list, "S005") || strings.Contains(list, "王五") {
		t.Fatalf("失败后班次列表不应出现新班次，got:\n%s", list)
	}

	// 两个已有同岗位班次：没有这次尝试产生的说明（它们彼此端点相接，
	// 本来就没有说明）。
	for _, id := range []string{"S001", "S002"} {
		notes := runCLI(t, dataPath, "note-list", "--id", id).ok(t, "失败后查看 "+id+" 的说明")
		if !strings.Contains(notes, "（无重叠说明）") {
			t.Fatalf("失败后 %s 应没有重叠说明，got:\n%s", id, notes)
		}
	}

	// 其他岗位班次的既有说明保持原样，且没有收到本次说明。
	notes := runCLI(t, dataPath, "note-list", "--id", "S003").ok(t, "失败后查看 S003 的说明")
	if !strings.Contains(notes, "N001") || !strings.Contains(notes, "巡检既有重叠说明") {
		t.Fatalf("失败后 S003 的既有说明应保持原样，got:\n%s", notes)
	}
	if strings.Contains(notes, "N002") || strings.Contains(notes, "抢修期间两班并行交接") {
		t.Fatalf("失败后其他岗位班次不应收到本次说明，got:\n%s", notes)
	}
}

// TestCLIShiftAddSaveFailureAtomicRollback 是核心保障：保存失败时命令明确
// 报错、不宣称成功、不展示尚未保存的新班次，系统里不留下只有班次或只有
// 说明的部分结果；恢复保存后用同样的建立信息重试成功并取得 S005 与
// N002/N003，说明只关联这次成功建立的新班次。
func TestCLIShiftAddSaveFailureAtomicRollback(t *testing.T) {
	dataPath := setupOverlappingShiftsForAdd(t)

	before := hashFile(t, dataPath)
	breakCLISaving(t, dataPath)

	// 区分保存失败与原有业务拒绝：同样的保存故障下，缺少说明仍按业务规则
	// 拒绝——本用例针对的是说明非空、班次信息合法、真正进入保存阶段后的失败。
	missing := runCLI(t, dataPath,
		"shift-add", "--position", "调度", "--owner", "王五",
		"--start", "2026-10-02T10:00:00+08:00", "--end", "2026-10-02T14:00:00+08:00")
	missing.failed(t, "保存故障下缺少说明", "必须填写重叠说明")

	// 说明非空、班次信息合法；本地数据保存失败。
	r := runCLI(t, dataPath, shiftAddArgs()...)
	r.failed(t, "保存失败时建立班次", "写入数据文件失败")
	// 不显示“已建立班次”的成功提示，正常输出与错误输出都不能展示尚未保存的
	// 班次编号，也不能把保存失败说成缺少说明或时间不合法。
	for _, unwanted := range []string{"已建立班次", "S005", "必须填写重叠说明", "结束时间必须晚于开始时间"} {
		if strings.Contains(r.stdout, unwanted) || strings.Contains(r.stderr, unwanted) {
			t.Fatalf("保存失败不得显示成功提示、未保存内容或业务拒绝信息 %q：stdout=%q stderr=%q",
				unwanted, r.stdout, r.stderr)
		}
	}

	// 数据文件一个字节都不应改变（原子改名未发生）。
	if after := hashFile(t, dataPath); after != before {
		t.Fatalf("保存失败不得改动数据文件：before=%s after=%s", before, after)
	}

	// 每次查询都是独立进程重新打开同一数据文件，仍应看到失败前的全部信息。
	assertCLIShiftAddRolledBack(t, dataPath)

	// 恢复正常保存后用同样的建立信息重试：按现有功能成功，失败尝试不占用
	// 班次与说明编号。
	restoreCLISaving(t, dataPath)
	out := runCLI(t, dataPath, shiftAddArgs()...).ok(t, "恢复保存后建立班次")
	for _, want := range []string{"已建立班次", "S005", "岗位=调度", "负责人=王五", "进行中"} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功建立应展示完整班次信息 %q，got:\n%s", want, out)
		}
	}

	// 班次列表出现新班次；新班次看到 N002/N003 两条说明，分别关联两个
	// 已有同岗位班次。
	list := runCLI(t, dataPath, "shift-list").ok(t, "成功后列出班次")
	if !strings.Contains(list, "S005") {
		t.Fatalf("成功后班次列表应包含新班次 S005，got:\n%s", list)
	}
	notesNew := runCLI(t, dataPath, "note-list", "--id", "S005").ok(t, "成功后查看新班次的说明")
	for _, want := range []string{"N002", "N003", "S001 <-> S005", "S002 <-> S005", "抢修期间两班并行交接"} {
		if !strings.Contains(notesNew, want) {
			t.Fatalf("新班次应看到分别关联两个已有班次的说明 %q，got:\n%s", want, notesNew)
		}
	}

	// 每个已有同岗位班次只看到涉及自己的那条；两个已有班次之间没有新增说明。
	notesFirst := runCLI(t, dataPath, "note-list", "--id", "S001").ok(t, "成功后查看 S001 的说明")
	if !strings.Contains(notesFirst, "N002") || strings.Contains(notesFirst, "N003") {
		t.Fatalf("S001 应只看到 N002，got:\n%s", notesFirst)
	}
	notesSecond := runCLI(t, dataPath, "note-list", "--id", "S002").ok(t, "成功后查看 S002 的说明")
	if !strings.Contains(notesSecond, "N003") || strings.Contains(notesSecond, "N002") {
		t.Fatalf("S002 应只看到 N003，got:\n%s", notesSecond)
	}

	// 同时间段的其他岗位班次仍只有既有说明，不收到这段说明。
	notesOther := runCLI(t, dataPath, "note-list", "--id", "S003").ok(t, "成功后查看 S003 的说明")
	if !strings.Contains(notesOther, "N001") || strings.Contains(notesOther, "N002") ||
		strings.Contains(notesOther, "N003") {
		t.Fatalf("其他岗位班次应只有既有说明 N001，got:\n%s", notesOther)
	}
}
