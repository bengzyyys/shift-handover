package handover

import (
	"errors"
	"testing"
	"time"
)

// cst 返回东八区时间。
func cst(day, hour int) time.Time {
	return time.Date(2026, 10, day, hour, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	return s
}

func isNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

func isValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func isState(err error) bool {
	var st *StateError
	return errors.As(err, &st)
}

// ---------- 班次 ----------

func TestShiftAddOK(t *testing.T) {
	s := newTestStore(t)
	sh, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	if err != nil {
		t.Fatalf("建立班次失败: %v", err)
	}
	if sh.ID != 1 {
		t.Fatalf("首个班次编号应为 1，得到 %d", sh.ID)
	}
	if sh.Ended {
		t.Fatal("新班次不应处于结束状态")
	}
}

func TestShiftAddMissingFields(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddShift("", "张三", cst(2, 8), cst(2, 16), ""); !isValidation(err) {
		t.Fatalf("空岗位应返回参数错误，得到 %v", err)
	}
	if _, err := s.AddShift("值班", "  ", cst(2, 8), cst(2, 16), ""); !isValidation(err) {
		t.Fatalf("空负责人应返回参数错误，得到 %v", err)
	}
}

func TestShiftEndMustBeAfterStart(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddShift("值班", "张三", cst(2, 16), cst(2, 8), ""); !isValidation(err) {
		t.Fatalf("结束早于开始应报错，得到 %v", err)
	}
	if _, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 8), ""); !isValidation(err) {
		t.Fatalf("结束等于开始应报错，得到 %v", err)
	}
}

func TestShiftOverlapRequiresNote(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), ""); err != nil {
		t.Fatal(err)
	}
	// 重叠但无说明 -> 报错
	if _, err := s.AddShift("值班", "李四", cst(2, 12), cst(2, 20), ""); !isValidation(err) {
		t.Fatalf("同岗位重叠且无说明应报错，得到 %v", err)
	}
	// 重叠且有说明 -> 成功，说明和涉及班次可查
	sh2, err := s.AddShift("值班", "李四", cst(2, 12), cst(2, 20), "交接班重叠半小时，已当面确认")
	if err != nil {
		t.Fatalf("有说明时重叠应允许: %v", err)
	}
	if sh2.OverlapNote == "" || len(sh2.OverlapIDs) != 1 || sh2.OverlapIDs[0] != 1 {
		t.Fatalf("重叠说明/涉及班次记录不正确: note=%q ids=%v", sh2.OverlapNote, sh2.OverlapIDs)
	}
}

func TestShiftTouchingIsNotOverlap(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), ""); err != nil {
		t.Fatal(err)
	}
	// 前班结束 16:00，后班开始 16:00，首尾相接不算重叠，无需说明
	if _, err := s.AddShift("值班", "李四", cst(2, 16), cst(3, 0), ""); err != nil {
		t.Fatalf("首尾相接不应算重叠: %v", err)
	}
}

func TestShiftDifferentPostsIndependent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), ""); err != nil {
		t.Fatal(err)
	}
	// 不同岗位，时间完全重叠也无需说明
	if _, err := s.AddShift("安保", "王五", cst(2, 8), cst(2, 16), ""); err != nil {
		t.Fatalf("不同岗位应互不影响: %v", err)
	}
}

func TestShiftEndAndDoubleEnd(t *testing.T) {
	s := newTestStore(t)
	sh, err := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndShift(sh.ID); err != nil {
		t.Fatalf("结束班次失败: %v", err)
	}
	if err := s.EndShift(sh.ID); !isState(err) {
		t.Fatalf("重复结束应返回状态错误，得到 %v", err)
	}
	if _, err := s.GetShift(99); !isNotFound(err) {
		t.Fatalf("查询不存在的班次应返回 NotFoundError，得到 %v", err)
	}
}

// ---------- 事项 ----------

func TestItemAddOK(t *testing.T) {
	s := newTestStore(t)
	sh, _ := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	it, err := s.AddItem(sh.ID, "巡查机房", SeverityImportant, "需双人同行", "李四")
	if err != nil {
		t.Fatalf("新增事项失败: %v", err)
	}
	if it.ID != 1 || it.OriginShiftID != sh.ID || it.CurrentShiftID != sh.ID || it.Closed {
		t.Fatalf("事项初始状态不正确: %+v", it)
	}
	// 限制条件可以为空
	if _, err := s.AddItem(sh.ID, "记录日志", SeverityNormal, "", "李四"); err != nil {
		t.Fatalf("空限制条件应允许: %v", err)
	}
}

func TestItemAddMissingFields(t *testing.T) {
	s := newTestStore(t)
	sh, _ := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	if _, err := s.AddItem(sh.ID, "  ", SeverityNormal, "", "李四"); !isValidation(err) {
		t.Fatalf("空内容应报错，得到 %v", err)
	}
	if _, err := s.AddItem(sh.ID, "巡查", SeverityNormal, "", "  "); !isValidation(err) {
		t.Fatalf("空后续负责人应报错，得到 %v", err)
	}
}

func TestItemAddToEndedShift(t *testing.T) {
	s := newTestStore(t)
	sh, _ := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	if err := s.EndShift(sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem(sh.ID, "巡查", SeverityNormal, "", "李四"); !isState(err) {
		t.Fatalf("已结束班次不能新增事项，得到 %v", err)
	}
}

func TestItemEditAndClose(t *testing.T) {
	s := newTestStore(t)
	sh, _ := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	it, _ := s.AddItem(sh.ID, "巡查机房", SeverityNormal, "", "李四")

	newContent := "巡查机房并签字"
	newSev := SeverityUrgent
	newOwner := "王五"
	if err := s.EditItem(it.ID, ItemEdit{
		Content:  &newContent,
		Severity: &newSev,
		FollowOwner: &newOwner,
	}); err != nil {
		t.Fatalf("修改事项失败: %v", err)
	}
	got, _ := s.GetItem(it.ID)
	if got.Content != newContent || got.Severity != SeverityUrgent || got.FollowOwner != newOwner {
		t.Fatalf("修改未生效: %+v", got)
	}

	// 必填字段不允许改为空
	empty := "   "
	if err := s.EditItem(it.ID, ItemEdit{Content: &empty}); !isValidation(err) {
		t.Fatalf("内容改为空应报错，得到 %v", err)
	}

	if err := s.CloseItem(it.ID); err != nil {
		t.Fatalf("关闭事项失败: %v", err)
	}
	if err := s.CloseItem(it.ID); !isState(err) {
		t.Fatalf("重复关闭应报错，得到 %v", err)
	}
}

func TestItemFrozenAfterShiftEnd(t *testing.T) {
	s := newTestStore(t)
	sh, _ := s.AddShift("值班", "张三", cst(2, 8), cst(2, 16), "")
	it, _ := s.AddItem(sh.ID, "巡查机房", SeverityNormal, "", "李四")
	if err := s.EndShift(sh.ID); err != nil {
		t.Fatal(err)
	}
	newContent := "事后改写"
	if err := s.EditItem(it.ID, ItemEdit{Content: &newContent}); !isState(err) {
		t.Fatalf("已结束班次的事项不能修改，得到 %v", err)
	}
	if err := s.CloseItem(it.ID); !isState(err) {
		t.Fatalf("已结束班次的事项关闭状态不能修改，得到 %v", err)
	}
}

// ---------- 交接：建立规则 ----------

func makeShift(t *testing.T, s *Store, post, owner string, start, end time.Time, note string) *Shift {
	t.Helper()
	sh, err := s.AddShift(post, owner, start, end, note)
	if err != nil {
		t.Fatalf("建立班次失败: %v", err)
	}
	return sh
}

func TestCreateHandoverRequiresEndedFromShift(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	to := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	if _, _, err := s.CreateHandover(from.ID, to.ID); !isState(err) {
		t.Fatalf("交班班次未结束应报错，得到 %v", err)
	}
}

func TestCreateHandoverRules(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	if _, err := s.AddItem(from.ID, "巡查机房", SeverityNormal, "", "李四"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}

	// 交给自身
	if _, _, err := s.CreateHandover(from.ID, from.ID); !isValidation(err) {
		t.Fatalf("交给自身应报错，得到 %v", err)
	}

	// 不同岗位
	otherPost := makeShift(t, s, "安保", "王五", cst(2, 16), cst(3, 0), "")
	if _, _, err := s.CreateHandover(from.ID, otherPost.ID); !isValidation(err) {
		t.Fatalf("不同岗位应报错，得到 %v", err)
	}

	// 接班班次已结束
	ended := makeShift(t, s, "值班", "赵六", cst(2, 16), cst(3, 0), "")
	if err := s.EndShift(ended.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateHandover(from.ID, ended.ID); !isState(err) {
		t.Fatalf("接班班次已结束应报错，得到 %v", err)
	}

	// 接班开始时间早于交班开始时间（首尾相接，不构成重叠）
	early := makeShift(t, s, "值班", "钱七", cst(2, 7), cst(2, 8), "")
	if _, _, err := s.CreateHandover(from.ID, early.ID); !isValidation(err) {
		t.Fatalf("接班开始早于交班开始应报错，得到 %v", err)
	}

	// 重叠但无说明（重叠说明在建班时强制填写，此处直接构造数据以覆盖交接校验）
	overlapNoNote := &Shift{
		ID: s.data.NextShiftID, Post: "值班", Owner: "孙八",
		Start: cst(2, 15), End: cst(2, 23), CreatedAt: time.Now(),
	}
	s.data.NextShiftID++
	s.data.Shifts = append(s.data.Shifts, overlapNoNote)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateHandover(from.ID, overlapNoNote.ID); !isValidation(err) {
		t.Fatalf("重叠无说明应报错，得到 %v", err)
	}

	// 重叠且有说明 -> 允许（15:00-16:00 只与 #1 重叠一小时，与已结束班次首尾相接）
	overlapOK := makeShift(t, s, "值班", "孙八", cst(2, 15), cst(2, 16), "与班次 #1 重叠一小时，已确认")
	h, already, err := s.CreateHandover(from.ID, overlapOK.ID)
	if err != nil {
		t.Fatalf("有重叠说明应允许交接: %v", err)
	}
	if already {
		t.Fatal("首次建立不应返回 already=true")
	}
	if h.Complete {
		t.Fatal("有待交接事项时不应直接完成")
	}

	// 重复发起返回已有记录，不复制事项
	h2, already2, err := s.CreateHandover(from.ID, overlapOK.ID)
	if err != nil {
		t.Fatalf("重复发起应返回已有记录: %v", err)
	}
	if !already2 || h2.ID != h.ID || len(h2.Items) != len(h.Items) {
		t.Fatalf("重复发起应返回同一记录且不复制事项: already=%v id=%d items=%d", already2, h2.ID, len(h2.Items))
	}

	// 改换对象报错
	another := makeShift(t, s, "值班", "周九", cst(3, 0), cst(3, 8), "")
	if _, _, err := s.CreateHandover(from.ID, another.ID); !isState(err) {
		t.Fatalf("改换接班对象应报错，得到 %v", err)
	}
}

func TestEmptyHandoverCompletesImmediately(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	// 空清单结束班次
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	to := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	h, _, err := s.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Complete {
		t.Fatal("空清单交接应直接完成")
	}
}

func TestHandoverOnlyIncludesOpenItems(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	open1, _ := s.AddItem(from.ID, "未关闭事项", SeverityNormal, "", "李四")
	closed1, _ := s.AddItem(from.ID, "已关闭事项", SeverityNormal, "", "李四")
	if err := s.CloseItem(closed1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	to := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	h, _, err := s.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Items) != 1 || h.Items[0].ItemID != open1.ID {
		t.Fatalf("交接清单应只含未关闭事项，得到 %+v", h.Items)
	}
}

// ---------- 交接：处理状态机 ----------

func setupHandover(t *testing.T) (*Store, *Shift, *Shift, *Handover, *Item) {
	t.Helper()
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	it, _ := s.AddItem(from.ID, "巡查机房", SeverityImportant, "需双人", "李四")
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	to := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	h, _, err := s.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, from, to, h, it
}

func TestReceiveMovesItemAndCompletes(t *testing.T) {
	s, _, to, h, it := setupHandover(t)
	if err := s.HandoverReceive(h.ID, it.ID, "王五"); err != nil {
		t.Fatalf("接收失败: %v", err)
	}
	got, _ := s.GetItem(it.ID)
	if got.CurrentShiftID != to.ID {
		t.Fatalf("接收后事项应进入接班班次，得到班次 #%d", got.CurrentShiftID)
	}
	h2, _ := s.GetHandover(h.ID)
	if !h2.Complete {
		t.Fatal("全部接收后交接应完成")
	}
	hi := h2.FindItem(it.ID)
	if hi.Result != ResultReceived || hi.Operator != "王五" || hi.HandledAt.IsZero() {
		t.Fatalf("接收结果记录不正确: %+v", hi)
	}
	// 已接收项不能再次退回
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "又想退"); !isState(err) {
		t.Fatalf("已接收项退回应报错，得到 %v", err)
	}
}

func TestReceiveRequiresOperator(t *testing.T) {
	s, _, _, h, it := setupHandover(t)
	if err := s.HandoverReceive(h.ID, it.ID, "   "); !isValidation(err) {
		t.Fatalf("缺少操作人应报错，得到 %v", err)
	}
}

func TestReturnRequiresReasonAndKeepsItem(t *testing.T) {
	s, from, _, h, it := setupHandover(t)
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "情况不符"); err != nil {
		t.Fatalf("退回失败: %v", err)
	}
	got, _ := s.GetItem(it.ID)
	if got.CurrentShiftID != from.ID {
		t.Fatal("退回后事项应仍属于交班班次")
	}
	h2, _ := s.GetHandover(h.ID)
	if h2.Complete {
		t.Fatal("退回项存在时交接不应完成")
	}
	hi := h2.FindItem(it.ID)
	if hi.Result != ResultReturned || hi.ReturnReason != "情况不符" {
		t.Fatalf("退回结果记录不正确: %+v", hi)
	}
	// 退回原因必填
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "  "); !isValidation(err) {
		t.Fatalf("缺少退回原因应报错，得到 %v", err)
	}
}

func TestResubmitAfterReturn(t *testing.T) {
	s, _, _, h, it := setupHandover(t)
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "情况不符"); err != nil {
		t.Fatal(err)
	}
	// 交班人追加说明重新提交
	if err := s.HandoverResubmit(h.ID, it.ID, "张三", "已核实，情况属实，请接收"); err != nil {
		t.Fatalf("重新提交失败: %v", err)
	}
	h2, _ := s.GetHandover(h.ID)
	hi := h2.FindItem(it.ID)
	if hi.Result != ResultPending {
		t.Fatalf("重新提交后应恢复待处理，得到 %s", hi.Result)
	}
	if hi.Operator != "" || !hi.HandledAt.IsZero() {
		t.Fatalf("重新提交后应清空上次退回的处理人/时间，得到 operator=%q handledAt=%v", hi.Operator, hi.HandledAt)
	}
	if hi.ReturnReason != "情况不符" {
		t.Fatalf("退回原因必须保留，得到 %q", hi.ReturnReason)
	}
	if len(hi.Notes) != 1 || hi.Notes[0].Text != "已核实，情况属实，请接收" || hi.Notes[0].Operator != "张三" {
		t.Fatalf("补充说明记录不正确: %+v", hi.Notes)
	}
	if h2.Complete {
		t.Fatal("恢复待处理后交接不应完成")
	}
	// 非退回状态不能重新提交
	if err := s.HandoverResubmit(h.ID, it.ID, "张三", "再来一次"); !isState(err) {
		t.Fatalf("待处理项重新提交应报错，得到 %v", err)
	}
	// 补充说明必填
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "还是不符"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverResubmit(h.ID, it.ID, "张三", "  "); !isValidation(err) {
		t.Fatalf("缺少补充说明应报错，得到 %v", err)
	}
}

func TestTrackRequiresFieldsAndMovesItem(t *testing.T) {
	s, _, to, h, it := setupHandover(t)
	if err := s.HandoverTrack(h.ID, it.ID, "王五", "继续跟进整改进度", "赵六"); err != nil {
		t.Fatalf("继续跟踪失败: %v", err)
	}
	got, _ := s.GetItem(it.ID)
	if got.CurrentShiftID != to.ID {
		t.Fatal("继续跟踪事项应进入接班班次")
	}
	h2, _ := s.GetHandover(h.ID)
	if !h2.Complete {
		t.Fatal("全部继续跟踪后交接应完成")
	}
	hi := h2.FindItem(it.ID)
	if hi.Result != ResultTracking || hi.TrackNote != "继续跟进整改进度" || hi.TrackOwner != "赵六" {
		t.Fatalf("跟踪结果记录不正确: %+v", hi)
	}
	// 必填校验
	if err := s.HandoverTrack(h.ID, it.ID, "王五", "  ", "赵六"); !isValidation(err) {
		t.Fatalf("缺少跟踪说明应报错，得到 %v", err)
	}
	if err := s.HandoverTrack(h.ID, it.ID, "王五", "说明", "  "); !isValidation(err) {
		t.Fatalf("缺少后续负责人应报错，得到 %v", err)
	}
}

func TestMixedResultsCompleteOnlyWhenAllDone(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	i1, _ := s.AddItem(from.ID, "事项一", SeverityNormal, "", "李四")
	i2, _ := s.AddItem(from.ID, "事项二", SeverityNormal, "", "李四")
	i3, _ := s.AddItem(from.ID, "事项三", SeverityNormal, "", "李四")
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	to := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	h, _, err := s.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverReceive(h.ID, i1.ID, "王五"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverReturn(h.ID, i2.ID, "王五", "原因"); err != nil {
		t.Fatal(err)
	}
	h2, _ := s.GetHandover(h.ID)
	if h2.Complete {
		t.Fatal("存在待处理和退回项时不应完成")
	}
	// i2 重新提交后接收
	if err := s.HandoverResubmit(h.ID, i2.ID, "张三", "已确认"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverTrack(h.ID, i2.ID, "王五", "跟踪", "赵六"); err != nil {
		t.Fatal(err)
	}
	h2, _ = s.GetHandover(h.ID)
	if h2.Complete {
		t.Fatal("仍有待处理项时不应完成")
	}
	if err := s.HandoverReceive(h.ID, i3.ID, "王五"); err != nil {
		t.Fatal(err)
	}
	h2, _ = s.GetHandover(h.ID)
	if !h2.Complete {
		t.Fatal("全部事项接收或继续跟踪后应完成")
	}
}

// ---------- 接班班次结束限制与再次交接 ----------

func TestSuccessorCannotEndWithOpenHandoverItems(t *testing.T) {
	s, _, to, h, it := setupHandover(t)
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "原因"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndShift(to.ID); !isState(err) {
		t.Fatalf("接班班次有退回项时不能结束，得到 %v", err)
	}
	// 重新提交并接收后可以结束
	if err := s.HandoverResubmit(h.ID, it.ID, "张三", "已确认"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverReceive(h.ID, it.ID, "王五"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndShift(to.ID); err != nil {
		t.Fatalf("交接完成后接班班次应可以结束: %v", err)
	}
}

func TestRehandoverKeepsItemNumberAndHistory(t *testing.T) {
	s := newTestStore(t)
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "")
	it, _ := s.AddItem(from.ID, "巡查机房", SeverityNormal, "", "李四")
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	mid := makeShift(t, s, "值班", "李四", cst(2, 16), cst(3, 0), "")
	h1, _, err := s.CreateHandover(from.ID, mid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverTrack(h1.ID, it.ID, "王五", "继续跟进", "赵六"); err != nil {
		t.Fatal(err)
	}
	// mid 结束前新增一条未关闭事项
	if _, err := s.AddItem(mid.ID, "记录台账", SeverityNormal, "", "赵六"); err != nil {
		t.Fatalf("接班班次结束前应可新增事项: %v", err)
	}
	if err := s.EndShift(mid.ID); err != nil {
		t.Fatal(err)
	}
	to := makeShift(t, s, "值班", "王五", cst(3, 0), cst(3, 8), "")
	h2, _, err := s.CreateHandover(mid.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.Items) != 2 {
		t.Fatalf("再次交接应包含两条未关闭事项，得到 %d 条", len(h2.Items))
	}
	// 原事项编号保留
	found := false
	for _, hi := range h2.Items {
		if hi.ItemID == it.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("再次交接应保留原事项编号")
	}
	if err := s.HandoverReceive(h2.ID, it.ID, "钱七"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetItem(it.ID)
	if got.CurrentShiftID != to.ID {
		t.Fatal("事项应进入新接班班次")
	}
	// 历史记录保留此前交接
	hasTrack := false
	for _, e := range got.History {
		if e.Type == "继续跟踪" {
			hasTrack = true
		}
	}
	if !hasTrack {
		t.Fatal("事项历史应保留此前交接记录")
	}
}

// ---------- 持久化 ----------

func TestPersistenceReload(t *testing.T) {
	path := t.TempDir() + "/sub/data.json"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	from := makeShift(t, s, "值班", "张三", cst(2, 8), cst(2, 16), "与 #2 重叠，已确认")
	to := makeShift(t, s, "值班", "李四", cst(2, 15), cst(2, 23), "与 #1 重叠，已确认")
	it, _ := s.AddItem(from.ID, "巡查机房", SeverityUrgent, "双人", "李四")
	if err := s.EndShift(from.ID); err != nil {
		t.Fatal(err)
	}
	h, _, err := s.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverReturn(h.ID, it.ID, "王五", "情况不符"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandoverResubmit(h.ID, it.ID, "张三", "已核实"); err != nil {
		t.Fatal(err)
	}

	// 重新打开
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开存储失败: %v", err)
	}
	// 重叠说明由后建立的班次 #2 填写，记录在 #2 上并涉及 #1
	to2, err := s2.GetShift(to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if to2.OverlapNote == "" || len(to2.OverlapIDs) != 1 || to2.OverlapIDs[0] != from.ID {
		t.Fatalf("重叠说明在重载后丢失: note=%q ids=%v", to2.OverlapNote, to2.OverlapIDs)
	}
	it2, err := s2.GetItem(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if it2.Content != "巡查机房" || it2.Severity != SeverityUrgent || it2.FollowOwner != "李四" {
		t.Fatal("事项数据在重载后丢失")
	}
	h2, err := s2.GetHandover(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	hi := h2.FindItem(it.ID)
	if hi.Result != ResultPending || hi.ReturnReason != "情况不符" || len(hi.Notes) != 1 {
		t.Fatalf("交接处理进度在重载后丢失: %+v", hi)
	}
}

func TestNotFoundErrors(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetShift(1); !isNotFound(err) {
		t.Fatalf("不存在的班次应返回 NotFoundError，得到 %v", err)
	}
	if _, err := s.GetItem(1); !isNotFound(err) {
		t.Fatalf("不存在的事项应返回 NotFoundError，得到 %v", err)
	}
	if _, err := s.GetHandover(1); !isNotFound(err) {
		t.Fatalf("不存在的交接记录应返回 NotFoundError，得到 %v", err)
	}
	if err := s.EndShift(1); !isNotFound(err) {
		t.Fatalf("结束不存在的班次应返回 NotFoundError，得到 %v", err)
	}
	if _, _, err := s.CreateHandover(1, 2); !isNotFound(err) {
		t.Fatalf("不存在的班次发起交接应返回 NotFoundError，得到 %v", err)
	}
}
