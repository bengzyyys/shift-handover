package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengzyyys/shift-handover/handover"
)

// 本文件从用户实际使用命令行的角度，为“事项关闭信息查询”补端到端回归保障，
// 重点是旧版本留下的数据：关闭状态为已关闭、却缺少关闭人或关闭时间时，
// 用户通过 item-show 与 shift-show 仍能准确区分“已经关闭”和“关闭信息未
// 记录”；保存状态为未关闭却残留关闭字段时按未关闭展示；结束时记录与最新
// 状态各用各的关闭信息。所有检查都针对真实子进程输出的中文文本，查询入口、
// 中文表达与数据含义保持不变，且查询只读、不改数据文件。

// writeLegacyDataFile 直接把旧版本可能写出的 JSON 落到数据文件，绕过当前
// 版本的正常业务流程——这些缺项、null、零值与残留字段只有旧数据才会出现。
func writeLegacyDataFile(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建数据目录：%v", err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("写入旧数据文件：%v", err)
	}
}

// legacyCloseInfoJSON 构造一份旧数据：
//   - S001（班次负责人，已结束，带结束时记录）-> S002（李四，进行中）；
//   - I001 已关闭、关闭人/时间字段全缺；I002 已关闭、两字段为 null；
//   - I003 已关闭、有姓名、时间为 0001-01-01T00:00:00Z；
//   - I004 未关闭却残留关闭人与真实时间（结束时记录里同样残留）；
//   - I005 结束时未关闭，接班后才关闭，当前只有真实时间（另一时区）无姓名。
const legacyCloseInfoJSON = `{
  "shift_seq": 2,
  "item_seq": 5,
  "handover_seq": 0,
  "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"班次负责人",
     "start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00",
     "created_at":"2026-10-02T08:00:00+08:00","closed":true,
     "closed_at":"2026-10-02T16:00:00+08:00",
     "close_record":{"items":[
       {"item_id":"I001","content":"字段全缺","severity":"important","follow_owner":"李四","closed":true},
       {"item_id":"I002","content":"字段为 null","severity":"normal","follow_owner":"李四","closed":true,
        "close_operator":null,"closed_at":null},
       {"item_id":"I003","content":"有姓名零值时间","severity":"normal","follow_owner":"李四","closed":true,
        "close_operator":"旧关闭人","closed_at":"0001-01-01T00:00:00Z"},
       {"item_id":"I004","content":"未关闭残留字段","severity":"normal","follow_owner":"李四","closed":false,
        "close_operator":"残留人","closed_at":"2026-10-02T14:00:00+08:00"},
       {"item_id":"I005","content":"接班后才关闭","severity":"normal","follow_owner":"李四","closed":false}
     ]}},
    {"id":"S002","position":"调度","owner":"李四",
     "start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00",
     "created_at":"2026-10-02T16:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"字段全缺","severity":"important","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00","closed":true},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"字段为 null","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00","closed":true,
     "close_operator":null,"closed_at":null},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"有姓名零值时间","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00","closed":true,
     "close_operator":"旧关闭人","closed_at":"0001-01-01T00:00:00Z"},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"未关闭残留字段","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:30:00+08:00","closed":false,
     "close_operator":"残留人","closed_at":"2026-10-02T14:00:00+08:00"},
    {"id":"I005","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"接班后才关闭","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:40:00+08:00","closed":true,
     "closed_at":"2026-10-02T15:30:00+09:00"}
  ],
  "handovers": [],
  "notes": []
}`

// TestCLICloseInfoLegacyDataItemShow：用户通过 item-show 查看旧数据事项的
// 最新状态：已关闭结论不被缺项改成未关闭，姓名与时间分别标出未记录，
// 残留字段不算关闭，全程不出现公元元年日期，也不拿班次负责人补齐。
func TestCLICloseInfoLegacyDataItemShow(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	writeLegacyDataFile(t, dataPath, legacyCloseInfoJSON)

	// 关闭人与关闭时间都缺失（字段缺省与 null 两种旧数据形态）。
	for _, id := range []string{"I001", "I002"} {
		out := runCLI(t, dataPath, "item-show", "--id", id).ok(t, "item-show "+id)
		for _, want := range []string{
			"[已关闭（关闭人未记录，关闭时间未记录）]",
			"当前班次=S002",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s 输出应包含 %q：\n%s", id, want, out)
			}
		}
		if strings.Contains(out, "0001-01-01") || strings.Contains(out, "班次负责人") {
			t.Fatalf("%s 不得出现公元元年日期或拿班次负责人补齐：\n%s", id, out)
		}
	}

	// 有姓名无时间（零值时间同样算未记录）。
	out3 := runCLI(t, dataPath, "item-show", "--id", "I003").ok(t, "item-show I003")
	if !strings.Contains(out3, "[已关闭（关闭人 旧关闭人，关闭时间未记录）]") {
		t.Fatalf("I003 应保留姓名并指出时间未记录：\n%s", out3)
	}
	if strings.Contains(out3, "0001-01-01") {
		t.Fatalf("I003 不得显示公元元年日期：\n%s", out3)
	}

	// 有真实时间（另一时区）无姓名：保留带时区的实际时间。
	out5 := runCLI(t, dataPath, "item-show", "--id", "I005").ok(t, "item-show I005")
	if !strings.Contains(out5, "[已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）]") {
		t.Fatalf("I005 应保留带时区的实际时间并指出关闭人未记录：\n%s", out5)
	}

	// 保存状态为未关闭：残留关闭人与时间一律不展示为关闭。
	out4 := runCLI(t, dataPath, "item-show", "--id", "I004").ok(t, "item-show I004")
	if !strings.Contains(strings.SplitN(out4, "\n", 2)[0], "[未关闭]") {
		t.Fatalf("I004 标题应显示未关闭：\n%s", out4)
	}
	if strings.Contains(out4, "残留人") || strings.Contains(out4, "2026-10-02 14:00:00 +08:00") {
		t.Fatalf("I004 残留关闭字段不得被解释成一次关闭：\n%s", out4)
	}
}

// TestCLICloseInfoLegacyDataShiftShow：用户通过 shift-show 查看进行中班次的
// 当前事项，以及已结束班次的结束时记录与最新状态对照。进行中班次同样适用
// 缺项规则，不能只保障已结束班次。
func TestCLICloseInfoLegacyDataShiftShow(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	writeLegacyDataFile(t, dataPath, legacyCloseInfoJSON)

	// 进行中班次：当前事项区，缺项已关闭与残留未关闭并存。
	ongoing := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "shift-show S002")
	if strings.Contains(ongoing, "结束时记录") || strings.Contains(ongoing, "历史记录不完整") {
		t.Fatalf("进行中班次应展示当前事项，不应出现结束时记录标题：\n%s", ongoing)
	}
	for _, want := range []string{
		"[已关闭（关闭人未记录，关闭时间未记录）]",
		"[已关闭（关闭人 旧关闭人，关闭时间未记录）]",
		"[已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）]",
		"[未关闭]",
	} {
		if !strings.Contains(ongoing, want) {
			t.Fatalf("进行中班次当前事项缺少 %q：\n%s", want, ongoing)
		}
	}
	// 未关闭事项的残留字段在整个班次报告中都不得出现。
	if strings.Contains(ongoing, "残留人") || strings.Contains(ongoing, "2026-10-02 14:00:00 +08:00") {
		t.Fatalf("进行中班次报告不得展示未关闭事项的残留关闭字段：\n%s", ongoing)
	}
	if strings.Contains(ongoing, "0001-01-01") {
		t.Fatalf("进行中班次报告不得出现公元元年日期：\n%s", ongoing)
	}

	// 已结束班次：结束时记录与最新状态对照各自使用自己的关闭信息。
	closed := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "shift-show S001")
	if !strings.Contains(closed, "以下为结束时记录") {
		t.Fatalf("已结束班次应明确标注结束时记录：\n%s", closed)
	}
	// I001 字段缺省、I002 null：两条结束时缺项记录都保留。
	if n := strings.Count(closed, "结束时已关闭（关闭人未记录，关闭时间未记录）"); n != 2 {
		t.Fatalf("I001 与 I002 结束时缺项记录都应保留，期望2处，got %d：\n%s", n, closed)
	}
	if !strings.Contains(closed, "结束时已关闭（关闭人 旧关闭人，关闭时间未记录）") {
		t.Fatalf("I003 结束时记录应保留姓名并指出时间未记录：\n%s", closed)
	}
	// I004：结束时未关闭，残留字段不展示；最新状态仍未关闭。
	if !strings.Contains(closed, "结束时未关闭") || !strings.Contains(closed, "最新关闭情况=未关闭") {
		t.Fatalf("I004 结束时与最新状态都应是未关闭：\n%s", closed)
	}
	if strings.Contains(closed, "残留人") || strings.Contains(closed, "2026-10-02 14:00:00 +08:00") {
		t.Fatalf("残留关闭人/时间不得出现在结束时报告：\n%s", closed)
	}
	// I005：原班结束时未关闭，最新状态对照才显示接班后的缺项关闭信息。
	if !strings.Contains(closed, "最新关闭情况=已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）") {
		t.Fatalf("I005 最新状态对照应显示接班后的缺项关闭信息：\n%s", closed)
	}
	if strings.Contains(closed, "0001-01-01") {
		t.Fatalf("结束时报告不得出现公元元年日期：\n%s", closed)
	}
}

// TestCLICloseInfoQueriesDoNotModifyFile：连续通过 item-show 与 shift-show
// 查询旧数据，数据文件必须保持原样——查询只是呈现已有事实，不补写关闭人、
// 关闭时间或结束时记录。
func TestCLICloseInfoQueriesDoNotModifyFile(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "handover-data.json")
	writeLegacyDataFile(t, dataPath, legacyCloseInfoJSON)
	before := hashFile(t, dataPath)

	for _, args := range [][]string{
		{"item-show", "--id", "I001"},
		{"item-show", "--id", "I002"},
		{"item-show", "--id", "I003"},
		{"item-show", "--id", "I004"},
		{"item-show", "--id", "I005"},
		{"shift-show", "--id", "S001"},
		{"shift-show", "--id", "S002"},
		// 再查一轮，重复查询同样不写入。
		{"item-show", "--id", "I001"},
		{"shift-show", "--id", "S001"},
		{"shift-show", "--id", "S002"},
	} {
		runCLI(t, dataPath, args...).ok(t, strings.Join(args, " "))
		if got := hashFile(t, dataPath); got != before {
			t.Fatalf("查询 %v 改写了数据文件：before=%s after=%s", args, before, got)
		}
	}
}

// TestCLICloseRecordAndLatestDiverge：按用户真实步骤完成“交班结束 -> 接班
// 接收”，再把数据改成旧版本会留下的缺项形态，验证结束时记录与最新状态
// 各用各的关闭信息：
//   - I001 原班结束时记录已关闭但关闭人缺失，事项最新信息有完整姓名和时间：
//     原班结束时部分保留缺项提示，最新状态对照展示最新完整信息；
//   - I002 原班结束时未关闭，接班后才关闭且关闭人缺失：原班仍显示结束时
//     未关闭，最新状态对照与进行中班次当前事项显示当前缺项关闭信息。
func TestCLICloseRecordAndLatestDiverge(t *testing.T) {
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
	must("新增 I001", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "结束时已关闭缺人的事项",
		"--severity", "important", "--constraints", "原限制", "--follow", "李四"))
	must("新增 I002", runCLI(t, dataPath,
		"item-add", "--shift", "S001", "--content", "接班后才关闭的事项",
		"--severity", "normal", "--constraints", "", "--follow", "李四"))
	must("结束交班班次", runCLI(t, dataPath, "shift-close", "--id", "S001"))
	must("发起交接", runCLI(t, dataPath, "handover-create", "--from", "S001", "--to", "S002"))
	must("确认接收 I001", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I001",
		"--action", "confirm", "--operator", "李四"))
	must("确认接收 I002", runCLI(t, dataPath,
		"handover-process", "--id", "H001", "--item", "I002",
		"--action", "confirm", "--operator", "李四"))

	// 模拟旧版本数据：直接改 JSON（领域类型在命令行包同样可见）。
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("读取数据文件：%v", err)
	}
	var data handover.Data
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析数据文件：%v", err)
	}
	latestClosed := time.Date(2026, 10, 2, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	otherZoneClosed := time.Date(2026, 10, 2, 15, 30, 0, 0, time.FixedZone("JST", 9*3600))
	tampered := false
	for i := range data.Items {
		switch data.Items[i].ID {
		case "I001":
			// 最新信息完整。
			data.Items[i].Closed = true
			data.Items[i].CloseOperator = "李四"
			data.Items[i].ClosedAt = &latestClosed
		case "I002":
			// 接班后关闭，缺关闭人、有真实时间。
			data.Items[i].Closed = true
			data.Items[i].CloseOperator = ""
			data.Items[i].ClosedAt = &otherZoneClosed
		}
	}
	for i := range data.Shifts {
		if data.Shifts[i].ID != "S001" || data.Shifts[i].CloseRecord == nil {
			continue
		}
		for j := range data.Shifts[i].CloseRecord.Items {
			snap := &data.Shifts[i].CloseRecord.Items[j]
			if snap.ItemID == "I001" {
				// 结束时记录为已关闭但关闭人与时间都缺失。
				snap.Closed = true
				snap.CloseOperator = ""
				snap.ClosedAt = nil
				tampered = true
			}
			// I002 结束时本就未关闭，快照保持原状。
		}
	}
	if !tampered {
		t.Fatalf("前置：未找到 I001 的结束时快照")
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化旧数据：%v", err)
	}
	if err := os.WriteFile(dataPath, out, 0o644); err != nil {
		t.Fatalf("写回旧数据：%v", err)
	}

	// item-show：看最新状态。I001 完整、I002 缺关闭人。
	j1 := runCLI(t, dataPath, "item-show", "--id", "I001").ok(t, "item-show I001")
	if !strings.Contains(j1, "[已关闭（李四 于 2026-10-02 20:00:00 +08:00）]") {
		t.Fatalf("I001 最新状态应显示完整关闭信息：\n%s", j1)
	}
	j2 := runCLI(t, dataPath, "item-show", "--id", "I002").ok(t, "item-show I002")
	if !strings.Contains(j2, "[已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）]") {
		t.Fatalf("I002 最新状态应保留带时区时间并指出关闭人未记录：\n%s", j2)
	}

	// 原班结束时记录：I001 保留缺项提示，最新状态对照才是完整信息；
	// I002 结束时未关闭，最新状态对照显示接班后的缺项关闭。
	repA := runCLI(t, dataPath, "shift-show", "--id", "S001").ok(t, "shift-show S001")
	if !strings.Contains(repA, "结束时已关闭（关闭人未记录，关闭时间未记录）") {
		t.Fatalf("I001 结束时部分应保留缺项提示，不被最新值补齐：\n%s", repA)
	}
	if !strings.Contains(repA, "最新关闭情况=已关闭（李四 于 2026-10-02 20:00:00 +08:00）") {
		t.Fatalf("I001 最新状态对照应显示完整信息：\n%s", repA)
	}
	if !strings.Contains(repA, "结束时未关闭") ||
		!strings.Contains(repA, "最新关闭情况=已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）") {
		t.Fatalf("I002 原班结束时未关闭、最新状态显示接班后缺项关闭：\n%s", repA)
	}

	// 进行中的接班班次：当前事项展示当前关闭信息（含缺项）。
	repB := runCLI(t, dataPath, "shift-show", "--id", "S002").ok(t, "shift-show S002")
	if strings.Contains(repB, "结束时记录") {
		t.Fatalf("接班班次仍在进行，应展示当前事项：\n%s", repB)
	}
	if !strings.Contains(repB, "[已关闭（李四 于 2026-10-02 20:00:00 +08:00）]") ||
		!strings.Contains(repB, "[已关闭（关闭人未记录，关闭时间 2026-10-02 15:30:00 +09:00）]") {
		t.Fatalf("接班班次当前事项应显示两项各自的当前关闭信息：\n%s", repB)
	}

	// 查询只读：多次查看后文件保持为篡改后的旧数据原样。
	before := hashFile(t, dataPath)
	for _, args := range [][]string{
		{"item-show", "--id", "I001"},
		{"item-show", "--id", "I002"},
		{"shift-show", "--id", "S001"},
		{"shift-show", "--id", "S002"},
	} {
		runCLI(t, dataPath, args...).ok(t, strings.Join(args, " "))
	}
	if got := hashFile(t, dataPath); got != before {
		t.Fatalf("查询不得补写关闭信息或结束时记录：before=%s after=%s", before, got)
	}
}
