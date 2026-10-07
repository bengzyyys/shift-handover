package handover

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// 本文件为“建立重叠班次（新班次同时与同岗位两班重叠、已填写有效说明）在本地
// 数据保存阶段失败”的可重复回归保障：已有两班 08:00-12:00 与 12:00-16:00（同岗位、
// 端点相接），新班次 10:00-14:00 与两班都实际重叠并填写了非空重叠说明，业务校验
// 全部通过、真正进入保存过程后写盘失败。此时建立操作必须返回原保存错误与零值班次
// 结果，不能把尚未保存的新班次编号、岗位、负责人、起止时间或建立时间当作建立结果；
// 系统内不留下这次建立的任何一部分——没有只有班次或只有说明的部分结果，失败尝试
// 不占用班次编号与说明编号。沿用既有保存失败测试的同一套故障注入方式（在 .tmp
// 路径放目录使写入必然失败）。

// failedOverlapCreateExpectations 汇总失败尝试发生前已保存的业务事实，供 reopen
// 前后用同一组期望核对。
type failedOverlapCreateExpectations struct {
	firstID, secondID string // 同岗位既有两班（端点相接）
	otherPosID        string // 同时间段的其他岗位班次
}

// assertNoOverlapCreateAfterFailure 用当前打开的数据核对：失败的建立没有留下
// 任何部分结果——班次列表与查询中都没有新班次，两个已有班次与同时间段的其他
// 岗位班次都查不到这次尝试产生的说明，既有班次与说明保持失败前的内容、编号与
// 关联关系。
func assertNoOverlapCreateAfterFailure(t *testing.T, f *fixture, w failedOverlapCreateExpectations, dataBefore string) {
	t.Helper()

	// 班次列表中不应出现新班次：仍只有失败前的三个既有班次。
	shifts := f.svc.ListShifts()
	if len(shifts) != 3 {
		t.Fatalf("保存失败后不应留下新班次，got %d 个班次：%+v", len(shifts), shifts)
	}
	// 失败尝试不占用的下一个班次编号应查不到记录。
	if _, err := f.svc.GetShift("S004"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败尝试不占用的班次编号 S004 应查不到记录，got %v", err)
	}

	// 两个已有班次都查不到这次尝试产生的说明；它们之间只是端点相接，
	// 本就不该有说明，失败尝试也不能改变这一点。
	for _, id := range []string{w.firstID, w.secondID} {
		if got := f.svc.OverlapNotes(id); len(got) != 0 {
			t.Fatalf("已有班次 %s 不应查到失败尝试产生的说明：%+v", id, got)
		}
		rep, err := f.svc.ShiftReport(id)
		if err != nil {
			t.Fatalf("report %s: %v", id, err)
		}
		if len(rep.OverlapNotes) != 0 {
			t.Fatalf("已有班次 %s 的报告不应出现失败尝试产生的说明：%+v", id, rep.OverlapNotes)
		}
	}
	// 同时间段的其他岗位班次同样不应收到说明。
	if got := f.svc.OverlapNotes(w.otherPosID); len(got) != 0 {
		t.Fatalf("其他岗位班次 %s 不应收到说明：%+v", w.otherPosID, got)
	}

	// 操作前已经保存的班次与说明仍保持原内容、编号和关联关系：
	// 内存中的完整数据与失败前逐字节一致。
	raw, err := json.Marshal(f.store.data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != dataBefore {
		t.Fatalf("保存失败不得改变任何已保存数据\nbefore %s\nafter  %s", dataBefore, raw)
	}
}

// TestCreateShiftSpanningTwoShiftsSaveFailureAtomicRollback：新班次同时与同岗位
// 两班重叠、说明与班次信息均合法，实际进入保存阶段后写盘失败——建立操作必须
// 明确返回保存错误（不能改报成缺少说明、时间不合法或重叠未获说明等业务拒绝）
// 与零值班次结果；内存与磁盘都回到失败前，不留下只有班次或只有说明的部分结果，
// 失败尝试不占用班次与说明编号。保存恢复后用同样的建立信息重试应正常成功，
// 新班次与两条说明从失败前各自的下一个编号继续，说明只关联这次成功建立的
// 新班次与每个实际重叠的已有班次。
func TestCreateShiftSpanningTwoShiftsSaveFailureAtomicRollback(t *testing.T) {
	f := newFixture(t)
	first := mustShift(t, f, "调度", "张三", ts(8, 0), ts(12, 0), "")
	second := mustShift(t, f, "调度", "李四", ts(12, 0), ts(16, 0), "")
	// 同时间段但岗位不同：不应参与本次重叠说明。
	otherPos := mustShift(t, f, "巡检", "赵六", ts(10, 0), ts(14, 0), "")

	want := failedOverlapCreateExpectations{
		firstID: first.ID, secondID: second.ID, otherPosID: otherPos.ID,
	}

	// 记录失败前已落盘的文件内容与内存数据快照。
	rawBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	dataRaw, err := json.Marshal(f.store.data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dataBefore := string(dataRaw)

	// 使下一次写盘在原子保存阶段失败。
	breakSaving(t, f)

	// 保存条件故障期间，原有业务拒绝仍按业务错误报告：同样跨两班但缺少说明
	// 时报 ErrOverlap 而不是保存错误。这说明本保障区分保存失败与业务拒绝，
	// 不是只要报错就算覆盖保存失败。
	if _, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "  "); !errors.Is(err, ErrOverlap) {
		t.Fatalf("缺少说明应仍报 ErrOverlap 而非保存错误，got %v", err)
	}

	// 填写了有效说明、班次信息也合法：业务校验全部通过，失败只能发生在保存
	// 阶段，必须明确返回保存错误。
	got, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修期间两班并行交接")
	if err == nil {
		t.Fatalf("保存失败时建立班次应明确返回错误，不能返回新班次")
	}
	if !strings.Contains(err.Error(), "写入数据文件失败") {
		t.Fatalf("应明确返回保存阶段的错误并保留原错误信息，got %v", err)
	}
	// 不能把保存失败说成缺少说明、时间不合法、班次重叠未获说明或其他业务拒绝。
	for _, sentinel := range []error{
		ErrInvalidInput, ErrOverlap, ErrNotFound, ErrShiftClosed, ErrShiftNotClosed,
		ErrHandoverExists, ErrHandoverTarget, ErrHandoverState,
		ErrPositionMismatch, ErrSameShift,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("保存失败不应被报告为业务校验错误 %v，got %v", sentinel, err)
		}
	}

	// 建立结果不能带出尚未保存的新班次：编号、岗位、负责人、起止时间与建立
	// 时间都必须是零值。
	if got.ID != "" || got.Position != "" || got.Owner != "" {
		t.Fatalf("保存失败不得返回未保存的编号、岗位或负责人：%+v", got)
	}
	if !got.Start.IsZero() || !got.End.IsZero() || !got.CreatedAt.IsZero() {
		t.Fatalf("保存失败不得返回未保存的起止时间或建立时间：%+v", got)
	}
	if !reflect.DeepEqual(got, Shift{}) {
		t.Fatalf("保存失败应返回零值班次结果：%+v", got)
	}

	// 当前打开的数据上查看，应看到失败前的事实，而不是只保证文件没变、
	// 查询却显示已经建立班次或说明。
	assertNoOverlapCreateAfterFailure(t, f, want, dataBefore)

	// 失败前已保存的数据文件一个字节都不应改变（原子改名未发生）。
	rawAfter, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatalf("read data file after failure: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("保存失败不得改动既有数据文件，失败的建立不能成为其中的业务事实")
	}

	// 退出后重新打开：仍没有新班次，也没有任何说明。
	f.reopen(t)
	assertNoOverlapCreateAfterFailure(t, f, want, dataBefore)

	// 保存恢复正常后，用同样的建立信息重试：按现有功能成功，失败尝试不占用
	// 班次编号与说明编号。
	restoreSaving(t, f)
	created, err := f.svc.CreateShift("调度", "王五", ts(10, 0), ts(14, 0), "抢修期间两班并行交接")
	if err != nil {
		t.Fatalf("恢复后用同样信息建立应成功：%v", err)
	}
	if created.ID != "S004" {
		t.Fatalf("失败尝试不应占用班次编号，成功建立应取得 S004，got %s", created.ID)
	}
	if created.Position != "调度" || created.Owner != "王五" ||
		!created.Start.Equal(ts(10, 0)) || !created.End.Equal(ts(14, 0)) {
		t.Fatalf("成功建立的班次信息应与提交一致：%+v", created)
	}
	if created.Closed || created.ClosedAt != nil {
		t.Fatalf("新建立的班次应为进行中：%+v", created)
	}

	// 全部说明恰好两条：只关联这次成功建立的新班次与两个实际重叠的已有班次，
	// 不重复留下失败时的关系，既有班次间也不因这次操作多出说明。
	if all := f.store.data.Notes; len(all) != 2 {
		t.Fatalf("应恰好保存两条说明，失败尝试不能留下重复关系，got %d：%+v", len(all), all)
	}
	pairs := notePairs(f.store.data.Notes)
	p1, ok1 := pairs[notePairKey(created.ID, first.ID)]
	p2, ok2 := pairs[notePairKey(created.ID, second.ID)]
	if !ok1 || !ok2 {
		t.Fatalf("说明应分别关联新班次与两个重叠班次，got %+v", f.store.data.Notes)
	}
	if _, ok := pairs[notePairKey(first.ID, second.ID)]; ok {
		t.Fatalf("既有班次间只是端点相接，不应出现它们之间的说明：%+v", f.store.data.Notes)
	}
	if p1.ID != "N001" || p2.ID != "N002" {
		t.Fatalf("失败尝试不应占用说明编号，说明应从 N001/N002 继续，got %s %s", p1.ID, p2.ID)
	}
	for _, n := range []OverlapNote{p1, p2} {
		if n.Note != "抢修期间两班并行交接" || n.Position != "调度" {
			t.Fatalf("说明内容与岗位应与本次成功提交一致：%+v", n)
		}
	}

	// 查询面：从新班次看到两条说明，从每个已有班次只看到涉及自己的那条，
	// 其他岗位班次收不到这段说明。
	if got := f.svc.OverlapNotes(created.ID); len(got) != 2 {
		t.Fatalf("从新班次应看到两条说明，got %+v", got)
	}
	for _, sh := range []Shift{first, second} {
		got := f.svc.OverlapNotes(sh.ID)
		if len(got) != 1 {
			t.Fatalf("从 %s 应只看到涉及自己的一条说明，got %+v", sh.ID, got)
		}
		if got[0].ShiftA != sh.ID && got[0].ShiftB != sh.ID {
			t.Fatalf("说明应涉及班次 %s，got %+v", sh.ID, got[0])
		}
	}
	if got := f.svc.OverlapNotes(otherPos.ID); len(got) != 0 {
		t.Fatalf("不同岗位班次不应收到说明，got %+v", got)
	}
}
