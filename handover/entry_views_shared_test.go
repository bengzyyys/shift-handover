package handover

import (
	"testing"
	"time"
)

// TestEntryViewsSharedBetweenItemAndShiftQueries 固定本次整理的核心约定：
// “各次交接当前结果”的汇总在 item-show 与 shift-show 两处共用同一实现，
// 同一（事项、交接）在两个入口得到的事实与排列次序必须一致。
func TestEntryViewsSharedBetweenItemAndShiftQueries(t *testing.T) {
	f := newFixture(t)

	mustShift(t, f, "岗A", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	mustShift(t, f, "岗A", "李四", tsDay(2, 16, 0), tsDay(3, 0, 0), "")
	mustShift(t, f, "岗A", "王五", tsDay(3, 0, 0), tsDay(3, 8, 0), "")
	i1, err := f.svc.AddItem("S001", "事项甲", SeverityUrgent, "限制一", "张三")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	if _, err := f.svc.AddItem("S001", "事项乙", SeverityNormal, "", "张三"); err != nil {
		t.Fatalf("add item2: %v", err)
	}
	if _, err := f.svc.CloseShift("S001"); err != nil {
		t.Fatalf("close S001: %v", err)
	}
	if _, err := f.svc.CreateHandover("S001", "S002"); err != nil {
		t.Fatalf("H001: %v", err)
	}
	// I001 先退回，补充后重新提交，再确认接收：退回历史必须随当前结果保留。
	if _, err := f.svc.ProcessEntry("H001", i1.ID, ActionReturn, "李四", "资料不全", "", ""); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := f.svc.ResubmitReturned("H001", i1.ID, "张三", "已补资料"); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if _, err := f.svc.ProcessEntry("H001", i1.ID, ActionConfirm, "李四", "", "", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.svc.ProcessEntry("H001", "I002", ActionTrack, "李四", "", "持续观察", "王五"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := f.svc.CloseShift("S002"); err != nil {
		t.Fatalf("close S002: %v", err)
	}
	if _, err := f.svc.CreateHandover("S002", "S003"); err != nil {
		t.Fatalf("H002: %v", err)
	}
	// 事项已随交接流到后面的班次（在 H002 中等待 S003 处理）：item-show
	// 仍须列出它参与的全部交接，不能只留最近一条。

	j, err := f.svc.ItemJourney(i1.ID)
	if err != nil {
		t.Fatalf("journey: %v", err)
	}
	if !j.HasHandovers || len(j.Results) != 2 {
		t.Fatalf("按事项查询必须列出全部两次交接，got %+v", j.Results)
	}
	if j.Results[0].HandoverID != "H001" || j.Results[1].HandoverID != "H002" {
		t.Fatalf("事项查询结果须按交接编号排列：%s, %s",
			j.Results[0].HandoverID, j.Results[1].HandoverID)
	}
	if j.Results[0].Entry.Status != EntryConfirmed || j.Results[1].Entry.Status != EntryPending {
		t.Fatalf("各次交接当前结果状态不正确：%+v", j.Results)
	}
	if len(j.Results[0].Entry.Rounds) != 1 ||
		j.Results[0].Entry.Rounds[0].Reason != "资料不全" ||
		j.Results[0].Entry.Rounds[0].Supplement != "已补资料" {
		t.Fatalf("确认接收后上一轮退回与补充仍须保留在当前结果：%+v", j.Results[0].Entry.Rounds)
	}

	// 班次查询只汇总与所查班次有直接关系的交接；S002 对同一事项既交入又交出，
	// H001 与 H002 两条分别保留。
	rep, err := f.svc.ShiftReport("S002")
	if err != nil {
		t.Fatalf("report S002: %v", err)
	}
	rows := rep.Results[i1.ID]
	if len(rows) != 2 {
		t.Fatalf("同一事项既交入又交出时两条结果分别保留，got %d", len(rows))
	}
	if rows[0].HandoverID != "H001" || rows[1].HandoverID != "H002" {
		t.Fatalf("班次报告同事项结果须按交接编号排列：%s, %s",
			rows[0].HandoverID, rows[1].HandoverID)
	}

	// 两个入口对同一（事项、交接）的事实必须逐项一致。
	repFrom, err := f.svc.ShiftReport("S001")
	if err != nil {
		t.Fatalf("report S001: %v", err)
	}
	repTo, err := f.svc.ShiftReport("S003")
	if err != nil {
		t.Fatalf("report S003: %v", err)
	}
	cases := []struct {
		name string
		want EntryView
		got  EntryView
	}{
		{"S001/H001", j.Results[0], repFrom.Results[i1.ID][0]},
		{"S002/H001", j.Results[0], rows[0]},
		{"S002/H002", j.Results[1], rows[1]},
		{"S003/H002", j.Results[1], repTo.Results[i1.ID][0]},
	}
	for _, c := range cases {
		if !sameEntryView(c.want, c.got) {
			t.Fatalf("%s 两个入口的事实不一致：item=%+v shift=%+v",
				c.name, c.want, c.got)
		}
	}

	// 班次报告不得混入该事项在其他班次之间的交接：S001 只见 H001，S003 只见 H002。
	if repFrom.Results["I002"][0].HandoverID != "H001" || len(repFrom.Results[i1.ID]) != 1 {
		t.Fatalf("S001 只应直接关联 H001：%+v", repFrom.Results)
	}
	if len(repTo.Results[i1.ID]) != 1 || repTo.Outgoing != nil {
		t.Fatalf("S003 只应直接关联交入的 H002：%+v", repTo.Results)
	}
}

func sameEntryView(a, b EntryView) bool {
	if a.HandoverID != b.HandoverID || a.FromShift != b.FromShift || a.ToShift != b.ToShift {
		return false
	}
	ea, eb := a.Entry, b.Entry
	if ea.ItemID != eb.ItemID || ea.Status != eb.Status || ea.Operator != eb.Operator ||
		ea.FollowOwner != eb.FollowOwner || ea.TrackingNote != eb.TrackingNote ||
		!timePtrEqual(ea.ProcessedAt, eb.ProcessedAt) || len(ea.Rounds) != len(eb.Rounds) {
		return false
	}
	for i := range ea.Rounds {
		ra, rb := ea.Rounds[i], eb.Rounds[i]
		if ra.Seq != rb.Seq || ra.Reason != rb.Reason || ra.Supplement != rb.Supplement ||
			ra.ReturnOperator != rb.ReturnOperator || ra.SupplementOperator != rb.SupplementOperator ||
			!ra.ReturnedAt.Equal(rb.ReturnedAt) ||
			!timePtrEqual(ra.SupplementAt, rb.SupplementAt) ||
			!timePtrEqual(ra.ResubmittedAt, rb.ResubmittedAt) {
			return false
		}
	}
	return true
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
