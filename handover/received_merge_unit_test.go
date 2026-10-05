package handover

import (
	"testing"
	"time"
)

// TestParseReceivedDetail：只有当前版本的完整固定格式（交接编号、交班、接班
// 班次与接收方式四项齐全）才可用于合并；旧版本格式、自由文字与残缺说明都
// 返回 ok=false，不解析出可用于合并的归属，也不凭空补齐。
func TestParseReceivedDetail(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		ok     bool
		want   receivedAttribution
	}{
		{
			name:   "新格式完整记载",
			detail: "交接 H001（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：记录压力；后续负责人：王五",
			ok:     true,
			want:   receivedAttribution{handoverID: "H001", fromShift: "S001", toShift: "S002", kind: "track"},
		},
		{
			name:   "新格式确认接收",
			detail: "交接 H002（S002 -> S003）接班班次接收：确认接收",
			ok:     true,
			want:   receivedAttribution{handoverID: "H002", fromShift: "S002", toShift: "S003", kind: "confirm"},
		},
		// 旧格式只记载接班班次与方式，归属信息不完整：不能用于合并，
		// 但也不是错误，调用方把它作为独立接收历史原样保留。
		{
			name:   "旧格式归属不完整",
			detail: "接班班次 S002 接收：继续跟踪；跟踪说明：记录压力；后续负责人：王五",
			ok:     false,
			want:   receivedAttribution{},
		},
		{"自由文字", "当时口头打过招呼，先收下来", false, receivedAttribution{}},
		{"残缺新格式缺接班班次", "交接 H001（S001 -> ）接班班次接收：确认接收", false, receivedAttribution{}},
		{"残缺新格式缺交接编号", "交接 （S001 -> S002）接班班次接收：确认接收", false, receivedAttribution{}},
		{"残缺新格式缺交班班次", "交接 H001（ -> S002）接班班次接收：确认接收", false, receivedAttribution{}},
		{"残缺旧格式缺方式", "接班班次 S002 接收：", false, receivedAttribution{}},
		{"无法识别的接收方式", "交接 H001（S001 -> S002）接班班次接收：待定", false, receivedAttribution{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseReceivedDetail(tc.detail)
			if ok != tc.ok {
				t.Fatalf("ok=%v, want %v（%+v）", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("归属=%+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSameReceivedEvent 逐条验证合并依据：操作人与实际时刻相同只是前提；说明
// 还必须完整记载交接编号、交班、接班班次与接收方式且逐项一致才合并。旧格式
// （归属不完整，即使接班班次与方式都吻合）、自由文字一律不合并；明确指向
// 其他交接、班次或方式即冲突；不同时区表示同一时刻视为相同时刻。
func TestSameReceivedEvent(t *testing.T) {
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.FixedZone("+08", 8*3600))
	rcpt := receivedReceipt{
		handoverID: "H001", fromShift: "S001", toShift: "S002",
		kind: "track", operator: "李四", at: at, timeKnown: true,
	}
	ev := func(at time.Time, operator, detail string) ItemEvent {
		return ItemEvent{At: at, Kind: "received", Operator: operator, Detail: detail}
	}
	canonical := "交接 H001（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：x；后续负责人：王五"
	cases := []struct {
		name string
		ev   ItemEvent
		want bool
	}{
		{"新格式全部一致", ev(at, "李四", canonical), true},
		{"旧格式即使接班班次与方式一致也不合并", ev(at, "李四", "接班班次 S002 接收：继续跟踪；跟踪说明：x；后续负责人：王五"), false},
		{"不同时区同一时刻",
			ev(time.Date(2026, 10, 2, 10, 0, 0, 0, time.FixedZone("+07", 7*3600)), "李四", canonical), true},
		{"操作人不同", ev(at, "张三", canonical), false},
		{"实际时刻不同", ev(at.Add(time.Minute), "李四", canonical), false},
		{"交接编号冲突", ev(at, "李四", "交接 H999（S001 -> S002）接班班次接收：继续跟踪"), false},
		{"交班班次冲突", ev(at, "李四", "交接 H001（S009 -> S002）接班班次接收：继续跟踪"), false},
		{"接班班次冲突", ev(at, "李四", "交接 H001（S001 -> S003）接班班次接收：继续跟踪"), false},
		{"接收方式冲突", ev(at, "李四", "交接 H001（S001 -> S002）接班班次接收：确认接收"), false},
		{"旧格式接班班次不同", ev(at, "李四", "接班班次 S003 接收：继续跟踪"), false},
		{"旧格式方式不同", ev(at, "李四", "接班班次 S002 接收：确认接收"), false},
		{"自由文字", ev(at, "李四", "口头收的"), false},
		{"非 received 事件", ItemEvent{At: at, Kind: "created", Operator: "李四", Detail: canonical}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameReceivedEvent(tc.ev, rcpt); got != tc.want {
				t.Fatalf("sameReceivedEvent=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestReceivedMergeOrderIndependent：调换两条事项历史的保存顺序不改变哪一条
// 被合并；一条事项事件与一次交接接收各自至多匹配一次。
func TestReceivedMergeOrderIndependent(t *testing.T) {
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	evToB := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "交接 H001（S001 -> S002）接班班次接收：继续跟踪"}
	evToC := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "接班班次 S003 接收：确认接收"}
	rcptB := receivedReceipt{
		handoverID: "H001", fromShift: "S001", toShift: "S002",
		kind: "track", operator: "李四", at: at, timeKnown: true,
	}
	for _, tc := range []struct {
		name   string
		events []ItemEvent
	}{
		{"乙班完整说明在前", []ItemEvent{evToB, evToC}},
		{"乙班完整说明在后", []ItemEvent{evToC, evToB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newReceivedMerge(tc.events)
			m.consider(rcptB)
			used := 0
			for i := range tc.events {
				if m.isUsed(i) {
					used++
					if tc.events[i].Detail != evToB.Detail {
						t.Fatalf("被合并的必须是指向乙班 H001 的完整说明：%s", tc.events[i].Detail)
					}
				}
			}
			if used != 1 {
				t.Fatalf("应恰好合并一条，got %d", used)
			}
		})
	}
}

// TestReceivedMergeKeepsLegacyAlongsideFullMatch：一条旧说明（只记载接班班次
// 与接收方式）与一条完整说明都与同一次交接接收部分/完全吻合时，只合并完整
// 对应的那一条；旧说明归属信息不完整，永远独立保留。交换两条说明的保存
// 顺序，被合并的集合与条数必须相同。
func TestReceivedMergeKeepsLegacyAlongsideFullMatch(t *testing.T) {
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	full := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "交接 H001（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：x；后续负责人：王五"}
	legacy := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "接班班次 S002 接收：继续跟踪；跟踪说明：x；后续负责人：王五"}
	rcpt := receivedReceipt{
		handoverID: "H001", fromShift: "S001", toShift: "S002",
		kind: "track", operator: "李四", at: at, timeKnown: true,
	}
	for _, tc := range []struct {
		name   string
		events []ItemEvent
	}{
		{"旧说明在前", []ItemEvent{legacy, full}},
		{"旧说明在后", []ItemEvent{full, legacy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newReceivedMerge(tc.events)
			m.consider(rcpt)
			used := 0
			for i := range tc.events {
				switch tc.events[i].Detail {
				case full.Detail:
					if !m.isUsed(i) {
						t.Fatalf("完整对应的说明应被合并")
					}
					used++
				case legacy.Detail:
					if m.isUsed(i) {
						t.Fatalf("归属不完整的旧说明必须独立保留，不能被合并")
					}
				}
			}
			if used != 1 {
				t.Fatalf("应恰好合并完整说明一条，got %d", used)
			}
		})
	}
}

// TestReceivedMergeSkipsUnknownTime：处理时间缺失或零值的接收凭据不参与合并，
// 也不能拿任何其他时间代替；未匹配的事项事件原样保留。
func TestReceivedMergeSkipsUnknownTime(t *testing.T) {
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	events := []ItemEvent{{At: at, Kind: "received", Operator: "李四",
		Detail: "交接 H001（S001 -> S002）接班班次接收：确认接收"}}
	m := newReceivedMerge(events)
	m.consider(receivedReceipt{handoverID: "H001", fromShift: "S001", toShift: "S002",
		kind: "confirm", operator: "李四"}) // timeKnown=false
	if m.isUsed(0) {
		t.Fatalf("未记录处理时间的接收不应合并事项事件")
	}
}
