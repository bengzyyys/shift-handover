package handover

import (
	"strings"
	"testing"
	"time"
)

// legacyCloseInfoRaw 构造一份旧数据，覆盖关闭信息的各种保存情况：
//   - I001：已关闭，关闭人与关闭时间都在；
//   - I002：已关闭，有关闭人、关闭时间未保存；
//   - I003：已关闭，无关闭人、有关闭时间；
//   - I004：已关闭，关闭人未保存、关闭时间是旧数据零值；
//   - I005：已关闭，有关闭人、关闭时间是旧数据零值；
//   - I006：S001 结束时未关闭，后来在接班的旧班次 S002 关闭，关闭时未保存关闭人；
//   - I007：S001 结束时未关闭，至今仍未关闭；
//   - I008：S001 结束时已关闭、有关闭人但缺关闭时间；事项最新值后来补有真实时间
//     （旧数据），结束时记录仍只能保留自己的缺项；
//   - I009：状态为未关闭，却残留关闭人与关闭时间，残留值不算一次有效关闭。
//
// S001 已结束且留有完整结束时记录；S002 是没有结束时记录的旧班次（历史不完整），
// 只展示当前信息；S003 仍在进行。
const legacyCloseInfoRaw = `{
  "shift_seq": 3, "item_seq": 9, "handover_seq": 0, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true,"closed_at":"2026-10-02T16:00:00+08:00",
     "close_record":{"items":[
       {"item_id":"I001","content":"人机时齐全","severity":"normal","follow_owner":"李四",
        "closed":true,"closed_at":"2026-10-02T09:30:00+08:00","close_operator":"张三"},
       {"item_id":"I002","content":"有人没时间","severity":"important","follow_owner":"李四",
        "closed":true,"close_operator":"李四"},
       {"item_id":"I003","content":"有时间没人","severity":"normal","follow_owner":"王五",
        "closed":true,"closed_at":"2026-10-02T10:15:00+08:00"},
       {"item_id":"I006","content":"接班班次才关闭","severity":"normal","follow_owner":"孙九",
        "closed":false},
       {"item_id":"I007","content":"一直未关闭","severity":"normal","follow_owner":"孙九",
        "closed":false},
       {"item_id":"I008","content":"结束时缺时间","severity":"urgent","follow_owner":"周八",
        "closed":true,"close_operator":"孙九"}
     ]}},
    {"id":"S002","position":"调度","owner":"孙九","start":"2026-10-02T16:00:00+08:00","end":"2026-10-04T16:00:00+08:00","created_at":"2026-10-02T16:00:00+08:00","closed":true,"closed_at":"2026-10-04T16:00:00+08:00"},
    {"id":"S003","position":"调度","owner":"周八","start":"2026-10-04T16:00:00+08:00","end":"2026-10-04T23:00:00+08:00","created_at":"2026-10-04T16:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"人机时齐全","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00","closed":true,
     "closed_at":"2026-10-02T09:30:00+08:00","close_operator":"张三",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"有人没时间","severity":"important","follow_owner":"李四",
     "created_at":"2026-10-02T09:05:00+08:00","closed":true,"close_operator":"李四",
     "events":[{"at":"2026-10-02T09:05:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"有时间没人","severity":"normal","follow_owner":"王五",
     "created_at":"2026-10-02T09:10:00+08:00","closed":true,
     "closed_at":"2026-10-02T10:15:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I004","origin_shift_id":"S003","shift_ids":["S003"],"current_shift_id":"S003",
     "content":"人机时皆缺","severity":"normal","follow_owner":"周八",
     "created_at":"2026-10-04T16:30:00+08:00","closed":true,
     "closed_at":"0001-01-01T00:00:00Z",
     "events":[{"at":"2026-10-04T16:30:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I005","origin_shift_id":"S003","shift_ids":["S003"],"current_shift_id":"S003",
     "content":"有人零值时间","severity":"important","follow_owner":"周八",
     "created_at":"2026-10-04T16:40:00+08:00","closed":true,"close_operator":"王五",
     "closed_at":"0001-01-01T00:00:00Z",
     "events":[{"at":"2026-10-04T16:40:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I006","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"接班班次才关闭","severity":"normal","follow_owner":"孙九",
     "created_at":"2026-10-02T10:30:00+08:00","closed":true,
     "closed_at":"2026-10-04T09:05:00+08:00",
     "events":[{"at":"2026-10-02T10:30:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I007","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"一直未关闭","severity":"normal","follow_owner":"孙九",
     "created_at":"2026-10-02T10:40:00+08:00","closed":false,
     "events":[{"at":"2026-10-02T10:40:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I008","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"结束时缺时间","severity":"urgent","follow_owner":"周八",
     "created_at":"2026-10-02T11:00:00+08:00","closed":true,
     "closed_at":"2026-10-02T11:20:00+08:00","close_operator":"孙九",
     "events":[{"at":"2026-10-02T11:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I009","origin_shift_id":"S003","shift_ids":["S003"],"current_shift_id":"S003",
     "content":"未关闭却残留关闭信息","severity":"normal","follow_owner":"周八",
     "created_at":"2026-10-04T17:00:00+08:00","closed":false,
     "closed_at":"2026-10-02T08:45:00+08:00","close_operator":"赵六",
     "events":[{"at":"2026-10-04T17:00:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [],
  "notes": []
}`

func mustDisplayTime(t *testing.T, rfc3339 string) string {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc3339, err)
	}
	return fmtTime(tm)
}

// TestLegacyCloseInfoInItemShow：item-show 的最新状态必须按已保存的关闭状态
// 展示关闭情况：已关闭不因人/时缺失降级；关闭人未保存或为空、关闭时间未保存
// 或为零值时分别标为未记录，保留已有的另一项；两项都在沿用原句式。未关闭
// 事项即使残留关闭人与时间也只显示未关闭。全程不出现公元元年日期与空白横线。
func TestLegacyCloseInfoInItemShow(t *testing.T) {
	svc := openLegacyService(t, legacyCloseInfoRaw)

	t1 := mustDisplayTime(t, "2026-10-02T09:30:00+08:00")
	t3 := mustDisplayTime(t, "2026-10-02T10:15:00+08:00")

	cases := []struct {
		itemID string
		want   string
	}{
		{"I001", "[已关闭（张三 于 " + t1 + "）]"},
		{"I002", "[已关闭（李四，关闭时间未记录）]"},
		{"I003", "[已关闭（关闭人未记录，于 " + t3 + "）]"},
		{"I004", "[已关闭（关闭人未记录，关闭时间未记录）]"},
		{"I005", "[已关闭（王五，关闭时间未记录）]"},
		{"I006", "[已关闭（关闭人未记录，于 " + mustDisplayTime(t, "2026-10-04T09:05:00+08:00") + "）]"},
		{"I007", "[未关闭]"},
		{"I008", "[已关闭（孙九 于 " + mustDisplayTime(t, "2026-10-02T11:20:00+08:00") + "）]"},
		{"I009", "[未关闭]"},
	}
	for _, c := range cases {
		j, err := svc.ItemJourney(c.itemID)
		if err != nil {
			t.Fatalf("journey %s: %v", c.itemID, err)
		}
		text := FormatItemJourney(j)
		if !strings.Contains(text, c.want) {
			t.Fatalf("item-show %s 缺少 %q：\n%s", c.itemID, c.want, text)
		}
		if strings.Contains(text, "0001-01-01") || strings.Contains(text, "于 -") {
			t.Fatalf("item-show %s 不能出现公元元年日期或空白横线：\n%s", c.itemID, text)
		}
	}

	// I009 状态为未关闭：残留的关闭人与关闭时间不能出现在最新状态里。
	j9, err := svc.ItemJourney("I009")
	if err != nil {
		t.Fatalf("journey I009: %v", err)
	}
	text9 := FormatItemJourney(j9)
	if strings.Contains(text9, "赵六") || strings.Contains(text9, mustDisplayTime(t, "2026-10-02T08:45:00+08:00")) {
		t.Fatalf("未关闭事项不能展示残留关闭人与关闭时间：\n%s", text9)
	}
	if strings.Contains(text9, "已关闭") {
		t.Fatalf("未关闭事项不能因残留值显示成已关闭：\n%s", text9)
	}
}

// TestLegacyCloseInfoInShiftShow：shift-show 的结束时记录与最新状态对照对同一份
// 关闭信息解释一致，但各自只解读自己保存的值：结束时记录的缺项只在结束时信息
// 中标出，不从事项最新值补齐；结束时未关闭、后来关闭的事项原班记录仍是结束时
// 未关闭，最新状态对照展示当前关闭情况（含缺项）。没有结束时记录的旧班次继续
// 使用历史不完整提示，只展示当前关闭情况。
func TestLegacyCloseInfoInShiftShow(t *testing.T) {
	svc := openLegacyService(t, legacyCloseInfoRaw)

	t1 := mustDisplayTime(t, "2026-10-02T09:30:00+08:00")
	t3 := mustDisplayTime(t, "2026-10-02T10:15:00+08:00")
	t6 := mustDisplayTime(t, "2026-10-04T09:05:00+08:00")
	t8 := mustDisplayTime(t, "2026-10-02T11:20:00+08:00")

	rep1, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report S001: %v", err)
	}
	text := FormatReport(rep1)

	wantFacts := []string{
		"以下为结束时记录",
		// 两项都有：沿用原句式，结束时记录与最新状态一致。
		"结束时已关闭（张三 于 " + t1 + "）",
		"最新关闭情况=已关闭（张三 于 " + t1 + "）",
		// 有姓名无时间：姓名保留，明确指出关闭时间未记录。
		"结束时已关闭（李四，关闭时间未记录）",
		"最新关闭情况=已关闭（李四，关闭时间未记录）",
		// 有时间无姓名：带时区实际时间保留，明确指出关闭人未记录。
		"结束时已关闭（关闭人未记录，于 " + t3 + "）",
		// 结束时未关闭、接班班次才关闭（且当前关闭信息缺关闭人）：
		// 原班记录仍是结束时未关闭，最新对照展示当前关闭情况与缺项。
		"结束时未关闭",
		"最新关闭情况=已关闭（关闭人未记录，于 " + t6 + "）",
		// 至今未关闭：两处都未关闭。
		"最新关闭情况=未关闭",
		// I008：结束时记录缺时间，即使最新值已有真实时间也不能补齐。
		"结束时已关闭（孙九，关闭时间未记录）",
		"最新关闭情况=已关闭（孙九 于 " + t8 + "）",
	}
	for _, want := range wantFacts {
		if !strings.Contains(text, want) {
			t.Fatalf("shift-show S001 缺少 %q：\n%s", want, text)
		}
	}

	// I006 这一项的结束时记录只能显示结束时未关闭，不能带出后来的关闭时间；
	// 当前关闭情况单独在最新状态对照行展示。
	block6 := itemBlock(text, "I006")
	if !strings.Contains(block6, "结束时未关闭") ||
		!strings.Contains(block6, "最新关闭情况=已关闭（关闭人未记录，于 "+t6+"）") {
		t.Fatalf("I006 应显示结束时未关闭并在最新对照中展示当前关闭情况：\n%s", block6)
	}
	if atClosePart := strings.SplitN(block6, "最新状态", 2)[0]; strings.Contains(atClosePart, "已关闭") ||
		strings.Contains(atClosePart, t6) {
		t.Fatalf("I006 结束时记录不能显示成已关闭或带出后来的关闭时间：\n%s", block6)
	}
	// I008 这一项的结束时记录不能被最新时间补齐。
	if block := itemBlock(text, "I008"); strings.Contains(strings.SplitN(block, "最新状态", 2)[0], t8) {
		t.Fatalf("I008 结束时记录不能从事项最新值补齐关闭时间：\n%s", block)
	}

	for _, bad := range []string{"0001-01-01", "于 -", "已关闭（ 于"} {
		if strings.Contains(text, bad) {
			t.Fatalf("shift-show S001 不能出现 %q：\n%s", bad, text)
		}
	}

	// S002：没有结束时记录的旧班次，继续使用历史不完整提示，只展示当前信息；
	// 当前已关闭但缺关闭人的事项按当前关闭情况展示，不包装成结束时记录。
	rep2, err := svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	text2 := FormatReport(rep2)
	for _, want := range []string{
		"历史记录不完整",
		"[已关闭（关闭人未记录，于 " + t6 + "）]",
		"[未关闭]",
	} {
		if !strings.Contains(text2, want) {
			t.Fatalf("shift-show S002 缺少 %q：\n%s", want, text2)
		}
	}
	for _, bad := range []string{"结束时已关闭", "结束时未关闭", "0001-01-01", "于 -"} {
		if strings.Contains(text2, bad) {
			t.Fatalf("旧班次报告不能把当前信息当成结束时记录或出现 %q：\n%s", bad, text2)
		}
	}

	// S003：进行中班次的当前事项同样使用一致的关闭信息解读；
	// 未关闭事项的残留关闭人/时间不显示。
	rep3, err := svc.ShiftReport("S003")
	if err != nil {
		t.Fatalf("report S003: %v", err)
	}
	text3 := FormatReport(rep3)
	for _, want := range []string{
		"[已关闭（关闭人未记录，关闭时间未记录）]",
		"[已关闭（王五，关闭时间未记录）]",
		"[未关闭]",
	} {
		if !strings.Contains(text3, want) {
			t.Fatalf("shift-show S003 缺少 %q：\n%s", want, text3)
		}
	}
	if strings.Contains(text3, "0001-01-01") || strings.Contains(text3, "赵六") ||
		strings.Contains(text3, mustDisplayTime(t, "2026-10-02T08:45:00+08:00")) {
		t.Fatalf("进行中班次报告不能出现零值日期或未关闭事项的残留关闭信息：\n%s", text3)
	}
}

// itemBlock 从报告文本中截取某一事项的展示块（该事项编号行起到下一个事项
// 编号行止），用于断言结束时记录与最新状态对照各自的内容不互相补齐。
func itemBlock(report, itemID string) string {
	lines := strings.Split(report, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), itemID+"  ") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if len(trimmed) >= 4 && trimmed[0] == 'I' && strings.Contains(trimmed, "  严重程度=") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
