package handover

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// 合并接收记录场景共用的旧数据：H001 由甲班 S001 交给乙班 S002，接班人李四
// 在 2026-10-02 11:00+08:00 选择继续跟踪；事项后来负责人已改为钱七，但交接
// 快照里当时的跟踪负责人仍是王五。事项历史除建立事件外，接收事件由用例注入。
// 另有一个丙班 S003（旧说明可能指向它，但不存在对应交接清单）。
func journeyMergeRaw(receivedEventsJSON, processedAtJSON string) string {
	return fmt.Sprintf(`{
  "shift_seq": 3, "item_seq": 1, "handover_seq": 1, "note_seq": 0,
  "shifts": [
    {"id":"S001","position":"调度","owner":"张三","start":"2026-10-02T08:00:00+08:00","end":"2026-10-02T16:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true,"closed_at":"2026-10-02T16:00:00+08:00"},
    {"id":"S002","position":"调度","owner":"李四","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:00:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":false},
    {"id":"S003","position":"调度","owner":"赵六","start":"2026-10-02T16:00:00+08:00","end":"2026-10-02T23:30:00+08:00","created_at":"2026-10-02T08:00:00+08:00","closed":true,"closed_at":"2026-10-02T23:30:00+08:00"}
  ],
  "items": [
    {"id":"I001","origin_shift_id":"S001","shift_ids":["S001","S002"],"current_shift_id":"S002",
     "content":"泵房压力异常","severity":"important","constraints":"夜间禁动","follow_owner":"钱七",
     "created_at":"2026-10-02T09:00:00+08:00",
     "events":[
       {"at":"2026-10-02T09:00:00+08:00","kind":"created","detail":"事项建立"}%s
     ]}
  ],
  "handovers": [
    {"id":"H001","position":"调度","from_shift_id":"S001","to_shift_id":"S002",
     "created_at":"2026-10-02T10:00:00+08:00","completed_at":%s,
     "entries":[
       {"item_id":"I001","content":"泵房压力异常","severity":"important","constraints":"夜间禁动",
        "follow_owner":"王五","status":"tracking","operator":"李四","processed_at":%s,
        "tracking_note":"每两小时记录压力"}
     ]}
  ],
  "notes": []
}`, receivedEventsJSON, processedAtJSON, processedAtJSON)
}

// 两类接收说明文本。
const (
	// 新版本格式：明确记载交接编号、两班与接收方式，与 H001 一致。
	canonicalTrackToB = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"交接 H001（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：每两小时记录压力；后续负责人：王五"}`
	// 旧格式：同操作人同时刻，但明确记载交给丙班 S003、接收方式为确认接收；
	// 没有对应的交接清单，也不带任何交接编号。
	legacyConfirmToC = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"接班班次 S003 接收：确认接收"}`
	// 新格式，但两班与交接编号都对、接收方式却记载为确认接收。
	canonicalConfirmToB = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"交接 H001（S001 -> S002）接班班次接收：确认接收"}`
	// 没有任何明确归属信息的自由文本说明。
	freeTextReceive = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"当时口头打过招呼，先收下来"}`
	// 新格式但记载了另一个交接编号 H999（无此交接）。
	otherHandoverTrack = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"交接 H999（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：每两小时记录压力；后续负责人：王五"}`
	// 旧版本格式，记载的接班班次与接收方式都与 H001 一致（无交接编号）。
	legacyTrackToB = `,{"at":"2026-10-02T11:00:00+08:00","kind":"received","operator":"李四","detail":"接班班次 S002 接收：继续跟踪；跟踪说明：每两小时记录压力；后续负责人：王五"}`
	// 新格式且与 H001 一致，但时间用另一时区表示同一实际时刻（11:00+08:00 == 10:00+07:00）。
	canonicalTrackToBTZ = `,{"at":"2026-10-02T10:00:00+07:00","kind":"received","operator":"李四","detail":"交接 H001（S001 -> S002）接班班次接收：继续跟踪；跟踪说明：每两小时记录压力；后续负责人：王五"}`
)

func countKind(events []JourneyEvent, kind string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// TestItemJourneyMergeReceptionSameOperatorSameTime：操作人与实际时刻相同并不
// 足以判定同一次接收。交给丙班的旧确认接收说明必须完整保留，H001 交给乙班的
// 继续跟踪只展示一次；两条历史在事项事件里的存放先后不能决定哪一条被隐藏；
// 旧说明没有对应交接清单时，既不能丢掉它，也不能凭空补上交接编号。
func TestItemJourneyMergeReceptionSameOperatorSameTime(t *testing.T) {
	for _, order := range []struct{ name, events string }{
		{"旧丙班说明在前", legacyConfirmToC + canonicalTrackToB},
		{"旧丙班说明在后", canonicalTrackToB + legacyConfirmToC},
	} {
		t.Run(order.name, func(t *testing.T) {
			svc := openLegacyService(t, journeyMergeRaw(order.events, `"2026-10-02T11:00:00+08:00"`))
			j, err := svc.ItemJourney("I001")
			if err != nil {
				t.Fatalf("journey: %v", err)
			}
			if countKind(j.Events, "track") != 1 {
				t.Fatalf("交给乙班的继续跟踪应只展示一次：%v", journeyKinds(j))
			}
			if countKind(j.Events, "received") != 1 {
				t.Fatalf("交给丙班的旧确认接收说明应原样保留一条：%v", journeyKinds(j))
			}
			if countKind(j.Events, "confirm") != 0 {
				t.Fatalf("H001 中不存在确认接收事件，丙班说明不应被改写成交接确认：%v", journeyKinds(j))
			}
			var standalone *JourneyEvent
			for i := range j.Events {
				if j.Events[i].Kind == "received" {
					standalone = &j.Events[i]
				}
			}
			if standalone == nil || !strings.Contains(standalone.Detail, "接班班次 S003 接收：确认接收") {
				t.Fatalf("保留的应是交给丙班的原说明：%+v", standalone)
			}
			if standalone.HandoverID != "" || standalone.ToShift != "" {
				t.Fatalf("无交接清单的旧说明不能凭空补交接编号或接班班次字段：%+v", standalone)
			}

			text := FormatItemJourney(j)
			if !strings.Contains(text, "接班班次 S003 接收：确认接收") {
				t.Fatalf("展示应完整保留交给丙班的原说明：\n%s", text)
			}
			if strings.Count(text, "交接 H001（S001 -> S002）继续跟踪") != 1 {
				t.Fatalf("交给乙班的继续跟踪应只出现一次：\n%s", text)
			}
			if !strings.Contains(text, "跟踪说明=每两小时记录压力") ||
				!strings.Contains(text, "后续负责人=王五") {
				t.Fatalf("继续跟踪应带出当时的跟踪说明与后续负责人：\n%s", text)
			}
			// 事项后来修改的负责人只体现在开头最新状态，不替换当时记录。
			if !strings.Contains(text, "后续负责人：钱七") {
				t.Fatalf("查询开头仍应展示事项最新负责人钱七：\n%s", text)
			}
			if len(j.Results) != 1 || j.Results[0].HandoverID != "H001" ||
				j.Results[0].Entry.Status != EntryTracking {
				t.Fatalf("各次交接当前结果应保持不变：%+v", j.Results)
			}
		})
	}
}

// TestItemJourneyMergeReceptionDistinguishingFacts：记载的接收方式、交接编号
// 不同，或说明根本没有明确归属信息时，事项历史各自保留；记载一致（含旧版本
// 格式、不同时区表示同一时刻）时才合并为同一次接收。
func TestItemJourneyMergeReceptionDistinguishingFacts(t *testing.T) {
	cases := []struct {
		name           string
		events         string
		processedAt    string
		wantStandalone int // 期望保留的事项自身 received 事件条数
	}{
		{"记载接收方式不同", canonicalConfirmToB, `"2026-10-02T11:00:00+08:00"`, 1},
		{"说明无明确归属信息", freeTextReceive, `"2026-10-02T11:00:00+08:00"`, 1},
		{"记载另一个交接编号", otherHandoverTrack, `"2026-10-02T11:00:00+08:00"`, 1},
		{"旧格式记载一致应合并", legacyTrackToB, `"2026-10-02T11:00:00+08:00"`, 0},
		{"新格式记载一致应合并", canonicalTrackToB, `"2026-10-02T11:00:00+08:00"`, 0},
		{"不同时区同一时刻应合并", canonicalTrackToBTZ, `"2026-10-02T11:00:00+08:00"`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := openLegacyService(t, journeyMergeRaw(tc.events, tc.processedAt))
			j, err := svc.ItemJourney("I001")
			if err != nil {
				t.Fatalf("journey: %v", err)
			}
			if countKind(j.Events, "track") != 1 {
				t.Fatalf("H001 的继续跟踪应始终展示一次：%v", journeyKinds(j))
			}
			if got := countKind(j.Events, "received"); got != tc.wantStandalone {
				t.Fatalf("事项自身接收记录保留条数 want %d, got %d：%v", tc.wantStandalone, got, journeyKinds(j))
			}
		})
	}
}

// TestItemJourneyMergeReceptionDoesNotRewrite：单纯查询不改写任何历史、不移动
// 事项、不改变交接完成情况。
func TestItemJourneyMergeReceptionDoesNotRewrite(t *testing.T) {
	raw := journeyMergeRaw(legacyConfirmToC+canonicalTrackToB, `"2026-10-02T11:00:00+08:00"`)
	svc := openLegacyService(t, raw)
	if _, err := svc.ItemJourney("I001"); err != nil {
		t.Fatalf("journey: %v", err)
	}
	// 重新打开同一文件，落盘内容必须与查询前逐字节一致。
	path := svc.store.Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != raw {
		t.Fatalf("单纯查询不应重写数据文件")
	}
	it, err := svc.GetItem("I001")
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.CurrentShiftID != "S002" || it.FollowOwner != "钱七" {
		t.Fatalf("查询不应移动事项或替换负责人：%+v", it)
	}
	h, err := svc.GetHandover("H001")
	if err != nil || !h.Completed() {
		t.Fatalf("交接完成情况不应改变：%+v err=%v", h, err)
	}
}
