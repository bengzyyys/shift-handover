package handover

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件为“建立班次（新班次同时与同岗位两班重叠并填写说明）在本地数据保存
// 阶段失败”的可重复回归保障：已有同岗位两班 08:00-12:00 与 12:00-16:00（彼此
// 仅端点相接），新班次 10:00-14:00 与两班都实际重叠，重叠说明非空，岗位、
// 负责人、起止时间均合法——业务校验全部通过、真正进入保存过程后本地写盘失败。
// 此时建立操作必须返回原保存错误与零值班次结果，不能把尚未保存的编号、岗位、
// 负责人、起止时间或建立时间当作建立结果；系统内不留下只有班次或只有说明的
// 部分结果，失败尝试不占用班次与说明编号。沿用既有保存失败测试的同一套故障
// 注入方式（在 .tmp 路径放目录使写入必然失败）。
//
// 场景中另有一对同时间段的其他岗位（巡检）重叠班次，其间已保存一条既有说明
// N001：既验证同时间段的其他岗位班次收不到本次说明，也验证失败前已保存的
// 班次与说明保持原内容、编号与关联关系。

// sameShift 报告两个班次是否表示同一事实：时间按实际时刻比较，与保存/回滚
// 过程中时区表示的变化无关。
func sameShift(a, b Shift) bool {
	return a.ID == b.ID && a.Position == b.Position && a.Owner == b.Owner &&
		a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.CreatedAt.Equal(b.CreatedAt) &&
		a.Closed == b.Closed && a.ClosedAt == nil && b.ClosedAt == nil &&
		a.CloseRecord == nil && b.CloseRecord == nil
}

// sameNote 报告两条说明是否表示同一事实（含编号、岗位、关联班次、内容与建立时间）。
func sameNote(a, b OverlapNote) bool {
	return a.ID == b.ID && a.Position == b.Position &&
		a.ShiftA == b.ShiftA && a.ShiftB == b.ShiftB &&
		a.Note == b.Note && a.CreatedAt.Equal(b.CreatedAt)
}

// assertShiftCreateRolledBack 用当前打开的数据核对：失败的建立没有留下任何
// 班次或说明事实——班次列表与查询仍是失败前的内容，失败尝试不占用的班次
// 编号查不到，两个已有同岗位班次没有这次尝试产生的说明，既有说明保持原
// 内容、编号与关联关系，失败尝试时刻不出现在任何说明中。
func assertShiftCreateRolledBack(t *testing.T, f *fixture, shiftsBefore []Shift, notesBefore []OverlapNote, failAt time.Time) {
	t.Helper()

	// 班次列表仍是失败前的全部班次，内容、编号与顺序完全一致。
	shiftsAfter := f.svc.ListShifts()
	if len(shiftsAfter) != len(shiftsBefore) {
		t.Fatalf("保存失败后的班次数量应与失败前一致：\n失败前 %+v\n失败后 %+v", shiftsBefore, shiftsAfter)
	}
	for i := range shiftsBefore {
		if !sameShift(shiftsAfter[i], shiftsBefore[i]) {
			t.Fatalf("保存失败后的班次应与失败前完全一致：\n失败前 %+v\n失败后 %+v", shiftsBefore[i], shiftsAfter[i])
		}
	}
	// 失败尝试不占用的班次编号查不到班次，也查不到班次报告。
	if _, err := f.svc.GetShift("S005"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败尝试不占用的班次编号应查不到记录，got %v", err)
	}
	if _, err := f.svc.ShiftReport("S005"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败尝试不占用的班次编号应查不到报告，got %v", err)
	}

	// 两个已有同岗位班次查不到这次尝试产生的说明（它们彼此端点相接，
	// 本来就没有说明，也不能因这次操作多出它们之间的说明）。
	for _, id := range []string{"S001", "S002"} {
		if ns := f.svc.OverlapNotes(id); len(ns) != 0 {
			t.Fatalf("班次 %s 不应出现失败尝试产生的说明：%+v", id, ns)
		}
		rep, err := f.svc.ShiftReport(id)
		if err != nil {
			t.Fatalf("report %s: %v", id, err)
		}
		if len(rep.OverlapNotes) != 0 {
			t.Fatalf("班次 %s 的报告中不应出现失败尝试产生的说明：%+v", id, rep.OverlapNotes)
		}
	}

	// 失败前已保存的说明保持原内容、编号与关联关系：其他岗位两班各自
	// 查询到的仍是那条既有说明，存储中也只有这一条。
	for _, id := range []string{"S003", "S004"} {
		got := f.svc.OverlapNotes(id)
		if len(got) != len(notesBefore) {
			t.Fatalf("%s 的既有说明应保持原样：\n失败前 %+v\n失败后 %+v", id, notesBefore, got)
		}
		for i := range notesBefore {
			if !sameNote(got[i], notesBefore[i]) {
				t.Fatalf("%s 的既有说明应保持原样：\n失败前 %+v\n失败后 %+v", id, notesBefore[i], got[i])
			}
		}
	}
	stored := f.store.data.Notes
	if len(stored) != len(notesBefore) {
		t.Fatalf("存储中的说明应保持失败前内容：\n失败前 %+v\n失败后 %+v", notesBefore, stored)
	}
	for i := range notesBefore {
		if !sameNote(stored[i], notesBefore[i]) {
			t.Fatalf("存储中的说明应保持失败前内容：\n失败前 %+v\n失败后 %+v", notesBefore[i], stored[i])
		}
	}
	for _, n := range f.store.data.Notes {
		if n.CreatedAt.Equal(failAt) {
			t.Fatalf("失败尝试时刻 %s 不应出现在任何说明中：%+v", failAt, n)
		}
	}
}

// TestCreateShiftSaveFailureAtomicRollback：新班次 10:00-14:00 同时与同岗位
// 两班（08:00-12:00、12:00-16:00）实际重叠，说明非空、班次信息合法，实际进入
// 保存阶段后写盘失败——建立操作必须返回原保存错误与零值班次结果；内存与磁盘
// 都回到失败前：没有新班次、没有这次尝试产生的说明，既有班次与说明不变，
// 失败尝试不占用班次与说明编号。保存恢复后用同样的建立信息重试应成功，
// 新班次取得 S005、两条说明取得 N002/N003（均从失败前的下一个编号继续），
// 说明只关联这次成功建立的新班次。
func TestCreateShiftSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")  // S001
	mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "") // S002，与 S001 仅端点相接
	// 同时间段但岗位不同的两个重叠班次，其间已保存既有说明 N001。
	mustShift(t, f, "巡检", "赵六", ts(8, 0), ts(12, 0), "")               // S003
	mustShift(t, f, "巡检", "钱七", ts(9, 0), ts(13, 0), "巡检既有重叠说明") // S004，留下 N001(S003,S004)

	shiftsBefore := f.svc.ListShifts()
	notesBefore := f.svc.OverlapNotes("S003")
	if len(notesBefore) != 1 || notesBefore[0].ID != "N001" {
		t.Fatalf("前置：巡检两班间应已有说明 N001，got %+v", notesBefore)
	}

	// 记录失败前已落盘的文件内容，并固定失败尝试的建立时刻。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	failAt := tsDay(2, 18, 0)
	f.svc.nowAt(func() time.Time { return failAt })

	// 说明非空、班次信息合法；使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)

	// 区分保存失败与原有业务拒绝：同样的保存故障下，缺少说明仍按业务规则
	// 拒绝（ErrOverlap），说明故障注入没有掩盖业务校验——本用例针对的是
	// 输入与班次信息都合法、真正进入保存阶段后的失败，而不是一次无效输入。
	if _, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "   "); !errors.Is(err, ErrOverlap) {
		t.Fatalf("保存故障下缺少说明仍应报 ErrOverlap，got %v", err)
	}

	got, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修期间两班并行交接")
	if err == nil {
		t.Fatalf("保存失败时建立班次应明确返回错误，不能返回新班次")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误并保留原错误信息，got %v", err)
	}
	// 说明与班次信息均合法，不能改报成缺少说明、时间不合法或其他业务拒绝。
	for _, sentinel := range []error{
		ErrInvalidInput, ErrOverlap, ErrNotFound, ErrPositionMismatch,
		ErrShiftClosed, ErrShiftNotClosed, ErrHandoverState,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("保存失败不应被报告为业务校验错误 %v，got %v", sentinel, err)
		}
	}
	// 调用方不应取得看似已经建立的班次：编号、岗位、负责人、起止时间与
	// 建立时间都必须是零值。
	if got.ID != "" || got.Position != "" || got.Owner != "" {
		t.Fatalf("保存失败不得返回未保存的编号、岗位或负责人：%+v", got)
	}
	if !got.Start.IsZero() || !got.End.IsZero() || !got.CreatedAt.IsZero() {
		t.Fatalf("保存失败不得返回未保存的起止时间或建立时间：%+v", got)
	}
	if got.Closed || got.ClosedAt != nil || got.CloseRecord != nil {
		t.Fatalf("保存失败不得返回任何结束状态或结束时记录：%+v", got)
	}
	if !reflect.DeepEqual(got, Shift{}) {
		t.Fatalf("保存失败应返回零值班次结果：%+v", got)
	}

	// 当前打开的数据上查看，应看到失败前的事实，而不是只保证文件没变、
	// 查询却显示已经建立班次或说明。
	assertShiftCreateRolledBack(t, f, shiftsBefore, notesBefore, failAt)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的建立不能成为其中的业务事实")
	}

	// 退出后重新打开：仍没有新班次，也没有这次尝试产生的说明。
	f.reopen(t)
	assertShiftCreateRolledBack(t, f, shiftsBefore, notesBefore, failAt)

	// 保存恢复正常后用同样的建立信息重试：按现有功能成功，失败尝试不占用
	// 班次与说明编号。
	restoreSaving(t, f)
	successAt := tsDay(2, 19, 0)
	f.svc.nowAt(func() time.Time { return successAt })
	created, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修期间两班并行交接")
	if err != nil {
		t.Fatalf("恢复后建立班次应成功：%v", err)
	}
	if created.ID != "S005" {
		t.Fatalf("失败尝试不应占用班次编号，成功建立应取得 S005，got %s", created.ID)
	}
	if created.Position != "调度" || created.Owner != "王五" ||
		!created.Start.Equal(ts(10, 0)) || !created.End.Equal(ts(14, 0)) {
		t.Fatalf("成功建立的岗位、负责人与起止时间不正确：%+v", created)
	}
	if !created.CreatedAt.Equal(successAt) || created.Closed || created.ClosedAt != nil {
		t.Fatalf("建立时间应以本次成功建立为准，班次进行中：%+v", created)
	}

	// 说明恰好三条：既有 N001 原样保留，本次成功建立产生 N002/N003（均从
	// 失败前的下一个编号继续），只关联这次成功建立的新班次，不重复留下
	// 失败时的关系；两个已有班次只是端点相接，不能多出它们之间的说明。
	all := f.store.data.Notes
	if len(all) != 3 {
		t.Fatalf("应恰好保存三条说明（既有一条加本次两条），got %d：%+v", len(all), all)
	}
	pairs := notePairs(all)
	if got := pairs[notePairKey("S003", "S004")]; !sameNote(got, notesBefore[0]) {
		t.Fatalf("既有说明应保持原内容、编号与关联：\n失败前 %+v\n成功后 %+v", notesBefore[0], got)
	}
	if _, ok := pairs[notePairKey("S001", "S002")]; ok {
		t.Fatalf("两个已有班次只是端点相接，不应出现它们之间的说明：%+v", all)
	}
	for id, pair := range map[string][2]string{"N002": {"S001", "S005"}, "N003": {"S002", "S005"}} {
		n, ok := pairs[notePairKey(pair[0], pair[1])]
		if !ok || n.ID != id {
			t.Fatalf("说明 %s 应关联 %s 与 %s，got %+v", id, pair[0], pair[1], all)
		}
		if n.Note != "抢修期间两班并行交接" || n.Position != "调度" {
			t.Fatalf("说明内容与岗位应沿用本次成功建立的输入：%+v", n)
		}
		if !n.CreatedAt.Equal(successAt) {
			t.Fatalf("说明建立时间应以本次成功建立为准：%+v", n)
		}
	}

	// 查询面：新班次看到两条说明，每个已有同岗位班次只看到涉及自己的那条，
	// 同时间段的其他岗位班次仍只有既有说明。
	if ns := f.svc.OverlapNotes("S005"); len(ns) != 2 || ns[0].ID != "N002" || ns[1].ID != "N003" {
		t.Fatalf("从新班次应看到 N002/N003 两条说明，got %+v", ns)
	}
	if ns := f.svc.OverlapNotes("S001"); len(ns) != 1 || ns[0].ID != "N002" {
		t.Fatalf("S001 应只看到涉及自己的 N002，got %+v", ns)
	}
	if ns := f.svc.OverlapNotes("S002"); len(ns) != 1 || ns[0].ID != "N003" {
		t.Fatalf("S002 应只看到涉及自己的 N003，got %+v", ns)
	}
	if ns := f.svc.OverlapNotes("S003"); len(ns) != 1 || !sameNote(ns[0], notesBefore[0]) {
		t.Fatalf("其他岗位班次不应收到本次说明，got %+v", ns)
	}

	// 建立返回内容应与随后的查询一致。
	queried, err := f.svc.GetShift(created.ID)
	if err != nil {
		t.Fatalf("get shift after success: %v", err)
	}
	if !reflect.DeepEqual(created, queried) {
		t.Fatalf("建立返回内容应与随后查询一致：\n返回 %+v\n查询 %+v", created, queried)
	}
}
