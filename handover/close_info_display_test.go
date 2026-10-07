package handover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件的测试把“关闭信息在查询中的展示规则”变成可重复执行的检查：
// 保存的关闭状态为已关闭时，即使关闭人或关闭时间缺失（旧数据），也继续显示
// 已关闭并分别指出哪一项未记录；姓名与时间各自独立判断，缺一个不隐藏另一个，
// 也不以班次负责人或处理经过中的人名、时间补齐；保存状态为未关闭时残留的
// 关闭人或时间不被解释成一次关闭。覆盖 item-show 最新状态、shift-show 的
// 当前事项（进行中班次）、结束时记录与最新状态对照；查询只读，不补写缺失信息。

// openLegacy 把一份旧数据 JSON 落盘并打开，返回存储与服务。
func openLegacy(t *testing.T, raw string) (*Store, *Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	return store, NewService(store)
}

// dataSnapshot 序列化当前保存的全部数据，用于核对查询是否只读。
func dataSnapshot(t *testing.T, store *Store) string {
	t.Helper()
	raw, err := json.Marshal(store.data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Fatalf("展示缺少 %q：\n%s", w, text)
		}
	}
}

func mustNotContain(t *testing.T, text string, bads ...string) {
	t.Helper()
	for _, b := range bads {
		if strings.Contains(text, b) {
			t.Fatalf("展示不应出现 %q：\n%s", b, text)
		}
	}
}

// TestFormatClosedStateCombinations：关闭信息展示的统一规则——姓名与时间
// 分别判断，四种组合各自的中文表达固定；零值时间（0001-01-01T00:00:00Z）
// 按未记录处理，不出现公元元年日期。
func TestFormatClosedStateCombinations(t *testing.T) {
	at := time.Date(2026, 10, 2, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	zero := time.Time{} // 0001-01-01T00:00:00Z
	cases := []struct {
		name   string
		prefix string
		op     string
		at     *time.Time
		want   string
	}{
		{"姓名时间都有", "已关闭", "李四", &at, "已关闭（李四 于 2026-10-02 10:30:00 +08:00）"},
		{"有姓名无时间", "已关闭", "钱七", nil, "已关闭（关闭人 钱七，关闭时间未记录）"},
		{"有姓名零值时间", "已关闭", "钱七", &zero, "已关闭（关闭人 钱七，关闭时间未记录）"},
		{"有时间无姓名", "已关闭", "", &at, "已关闭（关闭人未记录，关闭时间 2026-10-02 10:30:00 +08:00）"},
		{"都缺失", "已关闭", "", nil, "已关闭（关闭人未记录，关闭时间未记录）"},
		{"都缺失零值时间", "已关闭", "", &zero, "已关闭（关闭人未记录，关闭时间未记录）"},
		{"结束时前缀同样规则", "结束时已关闭", "", &at, "结束时已关闭（关闭人未记录，关闭时间 2026-10-02 10:30:00 +08:00）"},
	}
	for _, c := range cases {
		if got := formatClosedState(c.prefix, c.op, c.at); got != c.want {
			t.Fatalf("%s：want %q，got %q", c.name, c.want, got)
		}
	}
}

// 旧数据：进行中班次 S001（负责人张三）里六个事项，关闭信息各有残缺；
// I001 的处理经过里另有一条带操作人与时间的关闭事件，用来验证展示不拿
// 处理经过或班次负责人补齐关闭人、关闭时间。
const legacyCloseInfoRaw = `{
  "shift_seq": 1, "item_seq": 6, "handover_seq": 0, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"关闭人与时间都缺","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00","closed":true,
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"},
               {"at":"2026-10-02T09:30:00+08:00","kind":"closed","operator":"周九"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"有姓名时间为null","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:05:00+08:00","closed":true,
     "close_operator":"钱七","closed_at":null,
     "events":[{"at":"2026-10-02T09:05:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"姓名为空有时间","severity":"important","follow_owner":"李四",
     "created_at":"2026-10-02T09:10:00+08:00","closed":true,
     "close_operator":"","closed_at":"2026-10-02T10:30:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"有姓名时间零值","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:15:00+08:00","closed":true,
     "close_operator":"孙八","closed_at":"0001-01-01T00:00:00Z",
     "events":[{"at":"2026-10-02T09:15:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I005","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"姓名时间都全","severity":"urgent","follow_owner":"李四",
     "created_at":"2026-10-02T09:20:00+08:00","closed":true,
     "close_operator":"李四","closed_at":"2026-10-02T11:00:00+08:00",
     "events":[{"at":"2026-10-02T09:20:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I006","origin_shift_id":"S001","shift_ids":["S001"],"current_shift_id":"S001",
     "content":"未关闭却有残留","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:25:00+08:00","closed":false,
     "close_operator":"残留人","closed_at":"2026-10-02T12:00:00+08:00",
     "events":[{"at":"2026-10-02T09:25:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [],
  "notes": []
}`

// 各事项在 item-show 最新状态与 shift-show 当前事项中应展示的关闭情况
// （表头行 […] 部分）。
var legacyCloseInfoHeaders = map[string]string{
	"I001": "[已关闭（关闭人未记录，关闭时间未记录）]",
	"I002": "[已关闭（关闭人 钱七，关闭时间未记录）]",
	"I003": "[已关闭（关闭人未记录，关闭时间 2026-10-02 10:30:00 +08:00）]",
	"I004": "[已关闭（关闭人 孙八，关闭时间未记录）]",
	"I005": "[已关闭（李四 于 2026-10-02 11:00:00 +08:00）]",
	"I006": "[未关闭]",
}

// TestItemShowCloseInfoWithMissingFields：item-show 凭事项编号查看最新状态时，
// 保存状态为已关闭即显示已关闭，关闭人、关闭时间缺失分别标为未记录；缺失
// （字段不存在）、null 与零值时间都按未记录处理；缺一项不隐藏另一项；不拿
// 班次负责人或处理经过中的人名、时间补齐；未关闭事项的残留字段不被解释成
// 一次关闭。查询只读，不补写缺失信息。
func TestItemShowCloseInfoWithMissingFields(t *testing.T) {
	store, svc := openLegacy(t, legacyCloseInfoRaw)
	before := dataSnapshot(t, store)

	for _, id := range []string{"I001", "I002", "I003", "I004", "I005", "I006"} {
		j, err := svc.ItemJourney(id)
		if err != nil {
			t.Fatalf("journey %s: %v", id, err)
		}
		text := FormatItemJourney(j)
		want := id + "  严重程度=" + severityLabelOf(id) + "  当前班次=S001  原始班次=S001  " + legacyCloseInfoHeaders[id]
		if !strings.Contains(text, want) {
			t.Fatalf("item-show %s 最新状态应展示 %q：\n%s", id, want, text)
		}
		mustNotContain(t, text, "0001-01-01")
	}

	// I001：处理经过里虽有关闭事件（操作人周九、时间真实），最新状态的关闭人与
	// 关闭时间仍按事项保存的字段判断，各自标为未记录；经过本身原样保留。
	j1, err := svc.ItemJourney("I001")
	if err != nil {
		t.Fatalf("journey I001: %v", err)
	}
	text1 := FormatItemJourney(j1)
	mustContain(t, text1, "2026-10-02 09:30:00 +08:00 关闭 操作人=周九")
	mustNotContain(t, text1, "关闭人 周九", "周九 于")

	// 不以班次负责人张三补齐任何一项的关闭人。
	for _, id := range []string{"I001", "I003"} {
		j, err := svc.ItemJourney(id)
		if err != nil {
			t.Fatalf("journey %s: %v", id, err)
		}
		mustNotContain(t, FormatItemJourney(j), "关闭人 张三", "张三 于")
	}

	// I006：保存状态为未关闭，残留的关闭人与关闭时间不被解释成一次关闭。
	j6, err := svc.ItemJourney("I006")
	if err != nil {
		t.Fatalf("journey I006: %v", err)
	}
	mustNotContain(t, FormatItemJourney(j6), "残留人")

	// 查询只读：事项与班次的关闭状态、人名、时间都保持原样，不补写缺失信息。
	if after := dataSnapshot(t, store); after != before {
		t.Fatalf("查询不得改写保存的数据\nbefore %s\nafter  %s", before, after)
	}
}

// severityLabelOf 返回 legacyCloseInfoRaw 中各事项的严重程度中文名。
func severityLabelOf(id string) string {
	switch id {
	case "I003":
		return "重要"
	case "I005":
		return "紧急"
	default:
		return "普通"
	}
}

// TestShiftShowCurrentItemsCloseInfo：进行中班次的 shift-show 当前事项与
// item-show 最新状态按同一条规则展示关闭信息。
func TestShiftShowCurrentItemsCloseInfo(t *testing.T) {
	store, svc := openLegacy(t, legacyCloseInfoRaw)
	before := dataSnapshot(t, store)

	rep, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.ItemsAtClose || rep.HistoryIncomplete {
		t.Fatalf("进行中班次应展示当前事项：%+v", rep)
	}
	text := FormatReport(rep)
	for _, id := range []string{"I001", "I002", "I003", "I004", "I005", "I006"} {
		want := id + "  严重程度=" + severityLabelOf(id) + "  当前班次=S001  原始班次=S001  " + legacyCloseInfoHeaders[id]
		if !strings.Contains(text, want) {
			t.Fatalf("shift-show 当前事项 %s 应展示 %q：\n%s", id, want, text)
		}
	}
	mustNotContain(t, text, "0001-01-01", "残留人", "关闭人 张三", "张三 于")

	if after := dataSnapshot(t, store); after != before {
		t.Fatalf("查询不得改写保存的数据\nbefore %s\nafter  %s", before, after)
	}
}

// 旧数据：S001 已结束并留有结束时记录，四项事项的结束时关闭信息各有残缺；
// 事项最新信息（当前在 S002）的关闭情况与结束时记录各自不同，用来验证
// 结束时记录与最新状态对照各用各的关闭信息。
const legacyCloseRecordRaw = `{
  "shift_seq": 2, "item_seq": 4, "handover_seq": 0, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00",
     "closed":true,"closed_at":"2026-10-02T16:00:00+08:00",
     "close_record":{"items":[
       {"item_id":"I001","content":"事项甲","severity":"normal","follow_owner":"李四","closed":true},
       {"item_id":"I002","content":"事项乙","severity":"normal","follow_owner":"李四","closed":false},
       {"item_id":"I003","content":"事项丙","severity":"important","follow_owner":"王五","closed":true,"close_operator":"赵六"},
       {"item_id":"I004","content":"事项丁","severity":"normal","follow_owner":"李四","closed":false,
        "close_operator":"残留人","closed_at":"2026-10-02T15:00:00+08:00"}
     ]}},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"事项甲","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:00:00+08:00","closed":true,
     "close_operator":"李四","closed_at":"2026-10-02T17:00:00+08:00",
     "events":[{"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I002","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"事项乙","severity":"normal","follow_owner":"王五",
     "created_at":"2026-10-02T09:05:00+08:00","closed":true,
     "close_operator":"王五",
     "events":[{"at":"2026-10-02T09:05:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I003","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"事项丙","severity":"important","follow_owner":"王五",
     "created_at":"2026-10-02T09:10:00+08:00","closed":true,
     "closed_at":"2026-10-02T18:30:00+08:00",
     "events":[{"at":"2026-10-02T09:10:00+08:00","kind":"created","detail":"事项建立"}]},
    {"id":"I004","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"事项丁","severity":"normal","follow_owner":"李四",
     "created_at":"2026-10-02T09:15:00+08:00","closed":false,
     "events":[{"at":"2026-10-02T09:15:00+08:00","kind":"created","detail":"事项建立"}]}
  ],
  "handovers": [],
  "notes": []
}`

// TestShiftShowCloseRecordAndLatestUseOwnCloseInfo：已结束班次的 shift-show
// 中，结束时记录与最新状态对照各自使用自己的关闭信息——结束时的缺项只在
// 结束时信息中标出，不从事项最新值补齐；结束时未关闭、后班才关闭的事项
// 原班仍显示结束时未关闭，最新状态对照与 item-show 显示当前关闭情况
// （含缺项）；未关闭事项的残留字段不被解释成一次关闭。查询只读。
func TestShiftShowCloseRecordAndLatestUseOwnCloseInfo(t *testing.T) {
	store, svc := openLegacy(t, legacyCloseRecordRaw)
	before := dataSnapshot(t, store)

	rep, err := svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.ItemsAtClose {
		t.Fatalf("已结束班次应展示结束时记录：%+v", rep)
	}
	text := FormatReport(rep)

	// I001：结束时已关闭但关闭人、关闭时间都缺——结束时部分保留缺项提示，
	// 不从事项最新值（李四 + 真实时间）补齐；最新状态对照展示最新的完整信息。
	mustContain(t, text,
		"I001  严重程度=普通  结束时已关闭（关闭人未记录，关闭时间未记录）",
		"最新状态：当前所在班次=S002  最新负责人=李四  最新关闭情况=已关闭（李四 于 2026-10-02 17:00:00 +08:00）",
	)

	// I002：结束时未关闭、接班后才关闭——原班仍显示结束时未关闭，最新状态
	// 对照显示当前关闭情况；当前关闭时间缺失同样标为未记录。
	mustContain(t, text,
		"I002  严重程度=普通  结束时未关闭",
		"最新状态：当前所在班次=S002  最新负责人=王五  最新关闭情况=已关闭（关闭人 王五，关闭时间未记录）",
	)

	// I003：结束时有姓名无时间，最新有时间无姓名——两处各自保留自己记录的
	// 那一项，互不补齐。
	mustContain(t, text,
		"I003  严重程度=重要  结束时已关闭（关闭人 赵六，关闭时间未记录）",
		"最新状态：当前所在班次=S002  最新负责人=王五  最新关闭情况=已关闭（关闭人未记录，关闭时间 2026-10-02 18:30:00 +08:00）",
	)

	// I004：结束时记录里残留关闭人与关闭时间，但保存状态为未关闭——仍按
	// 结束时未关闭展示，残留字段不被解释成一次关闭；最新状态同样未关闭。
	mustContain(t, text,
		"I004  严重程度=普通  结束时未关闭",
		"最新状态：当前所在班次=S002  最新负责人=李四  最新关闭情况=未关闭",
	)
	mustNotContain(t, text, "残留人", "0001-01-01", "关闭人 张三", "张三 于")

	// item-show 与最新状态对照展示同一份当前关闭信息。
	j2, err := svc.ItemJourney("I002")
	if err != nil {
		t.Fatalf("journey I002: %v", err)
	}
	mustContain(t, FormatItemJourney(j2),
		"I002  严重程度=普通  当前班次=S002  原始班次=S001  [已关闭（关闭人 王五，关闭时间未记录）]")
	j3, err := svc.ItemJourney("I003")
	if err != nil {
		t.Fatalf("journey I003: %v", err)
	}
	mustContain(t, FormatItemJourney(j3),
		"I003  严重程度=重要  当前班次=S002  原始班次=S001  [已关闭（关闭人未记录，关闭时间 2026-10-02 18:30:00 +08:00）]")

	// 进行中班次 S002 的当前事项同样按这条规则展示。
	rep2, err := svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	if rep2.ItemsAtClose || rep2.HistoryIncomplete {
		t.Fatalf("S002 进行中，应展示当前事项：%+v", rep2)
	}
	text2 := FormatReport(rep2)
	mustContain(t, text2,
		"I001  严重程度=普通  当前班次=S002  原始班次=S001  [已关闭（李四 于 2026-10-02 17:00:00 +08:00）]",
		"I002  严重程度=普通  当前班次=S002  原始班次=S001  [已关闭（关闭人 王五，关闭时间未记录）]",
		"I003  严重程度=重要  当前班次=S002  原始班次=S001  [已关闭（关闭人未记录，关闭时间 2026-10-02 18:30:00 +08:00）]",
		"I004  严重程度=普通  当前班次=S002  原始班次=S001  [未关闭]",
	)
	mustNotContain(t, text2, "0001-01-01")

	// 查询只读：原事项与班次结束时记录中的关闭状态、人名和时间都保持原样。
	if after := dataSnapshot(t, store); after != before {
		t.Fatalf("查询不得改写保存的数据\nbefore %s\nafter  %s", before, after)
	}
}
