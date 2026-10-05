package handover

import (
	"testing"
	"time"
)

// TestParseReceivedDetail：只认本工具写入的新旧固定格式；自由文字与残缺说明
// 一律视为没有明确记载，不解析出任何归属。
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
		{
			name:   "旧格式只记载接班班次与方式",
			detail: "接班班次 S002 接收：继续跟踪；跟踪说明：记录压力；后续负责人：王五",
			ok:     true,
			want:   receivedAttribution{toShift: "S002", kind: "track"},
		},
		{"自由文字", "当时口头打过招呼，先收下来", false, receivedAttribution{}},
		{"残缺新格式缺接班班次", "交接 H001（S001 -> ）接班班次接收：确认接收", false, receivedAttribution{}},
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

// TestSameReceivedEvent 逐条验证合并依据：操作人与实际时刻相同是前提，说明
// 必须完整记载交接编号、交班、接班班次与接收方式且逐项一致才合并；旧格式
// 缺少交接编号与交班班次，即使其余信息吻合也不合并；指向其他交接、班次或
// 方式即冲突；不同时区表示同一时刻视为相同时刻；自由文字不合并。
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
		{"不同时区同一时刻",
			ev(time.Date(2026, 10, 2, 10, 0, 0, 0, time.FixedZone("+07", 7*3600)), "李四", canonical), true},
		{"旧格式缺少交接编号与交班班次不合并",
			ev(at, "李四", "接班班次 S002 接收：继续跟踪；跟踪说明：x；后续负责人：王五"), false},
		{"操作人不同", ev(at, "张三", canonical), false},
		{"实际时刻不同", ev(at.Add(time.Minute), "李四", canonical), false},
		{"交接编号冲突", ev(at, "李四", "交接 H999（S001 -> S002）接班班次接收：继续跟踪"), false},
		{"交班班次冲突", ev(at, "李四", "交接 H001（S009 -> S002）接班班次接收：继续跟踪"), false},
		{"接班班次冲突", ev(at, "李四", "交接 H001（S001 -> S003）接班班次接收：继续跟踪"), false},
		{"接收方式冲突", ev(at, "李四", "交接 H001（S001 -> S002）接班班次接收：确认接收"), false},
		{"旧格式接班班次冲突", ev(at, "李四", "接班班次 S003 接收：继续跟踪"), false},
		{"旧格式方式冲突", ev(at, "李四", "接班班次 S002 接收：确认接收"), false},
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
// 被合并；一条事项事件与一次交接接收各自至多匹配一次。与清单记录部分吻合的
// 旧说明（只记载接班班次与方式）永远不被认领，被合并的始终是完整对应的那条。
func TestReceivedMergeOrderIndependent(t *testing.T) {
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	evToB := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "交接 H001（S001 -> S002）接班班次接收：继续跟踪"}
	evToC := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "接班班次 S003 接收：确认接收"}
	evLegacyB := ItemEvent{At: at, Kind: "received", Operator: "李四",
		Detail: "接班班次 S002 接收：继续跟踪；跟踪说明：x；后续负责人：王五"}
	rcptB := receivedReceipt{
		handoverID: "H001", fromShift: "S001", toShift: "S002",
		kind: "track", operator: "李四", at: at, timeKnown: true,
	}
	for _, tc := range []struct {
		name   string
		events []ItemEvent
	}{
		{"乙班说明在前", []ItemEvent{evToB, evToC}},
		{"乙班说明在后", []ItemEvent{evToC, evToB}},
		{"部分吻合的旧说明在前", []ItemEvent{evLegacyB, evToB}},
		{"部分吻合的旧说明在后", []ItemEvent{evToB, evLegacyB}},
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
