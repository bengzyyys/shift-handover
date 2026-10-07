package handover

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件针对“发起交接时重叠说明是否有效”的判定：本地数据中只保存了关联两班
// 的重叠说明记录并不等于已经说明，必须以实际保存的正文为准——去掉首尾空白
// （空格、制表符、换行等）后仍有内容的记录才算有效说明。以下用例直接在本地
// 数据中构造正文为空白的说明记录（旧数据可能如此），验证 handover-create 的
// 放行、拒绝、补救与历史记录保留行为。

// blankExistingNotes 把已保存的重叠说明正文全部替换为指定空白内容，模拟本地
// 数据里“记录存在、正文缺失或全是空白”的旧数据；记录的编号与班次关联保留。
func blankExistingNotes(f *fixture, note string) {
	for i := range f.store.data.Notes {
		f.store.data.Notes[i].Note = note
	}
}

// appendRawNote 直接在本地数据中追加一条重叠说明记录（不经过业务接口），
// 用于构造同一对班次的多条说明（含空记录）或只关联一个班次的说明。
func appendRawNote(f *fixture, id, position, a, b, note string) {
	x, y := notePair(a, b)
	f.store.data.Notes = append(f.store.data.Notes, OverlapNote{
		ID:        id,
		Position:  position,
		ShiftA:    x,
		ShiftB:    y,
		Note:      note,
		CreatedAt: f.clock,
	})
}

// setupOverlappingPair 建立一对同岗位、实际重叠的班次：S001 08:00-16:00、
// S002 15:00-22:00（重叠一小时），建立 S002 时会保存一条正文有效的说明，
// 调用方可按需改写或追加记录；交班班次保持进行中，由调用方自行结束。
func setupOverlappingPair(t *testing.T, f *fixture) (Shift, Shift) {
	t.Helper()
	from := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	to := mustShift(t, f, "调度", "李四", tsDay(2, 15, 0), tsDay(2, 22, 0), "建立班次时填写的重叠说明")
	return from, to
}

// assertNoHandoverCreated 确认失败的发起没有留下任何交接，也没有占用编号。
func assertNoHandoverCreated(t *testing.T, f *fixture) {
	t.Helper()
	if hs := f.svc.ListHandovers(); len(hs) != 0 {
		t.Fatalf("失败的发起不得留下交接记录，got %+v", hs)
	}
	if f.store.data.HandoverSeq != 0 {
		t.Fatalf("失败的发起不应占用交接编号，HandoverSeq=%d", f.store.data.HandoverSeq)
	}
}

// TestHandoverOverlapBlankNoteRejected：正文缺失、为空或全是空格/制表符/换行
// 的说明记录不能作为已经说明的依据。交班已结束、接班未结束且其他接班条件
// 满足时，实际重叠的两班首次发起交接必须明确失败：错误带 ErrOverlap 并指出
// 两班编号、提示先填写重叠说明；不留下新交接，事项归属不变。
func TestHandoverOverlapBlankNoteRejected(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t", "\n", " \t\r\n  "} {
		t.Run("空白正文", func(t *testing.T) {
			f := newFixture(t)
			from, to := setupOverlappingPair(t, f)
			item, err := f.svc.AddItem(from.ID, "待交接事项", SeverityNormal, "", "李四")
			if err != nil {
				t.Fatalf("add item: %v", err)
			}
			if _, err := f.svc.CloseShift(from.ID); err != nil {
				t.Fatalf("结束交班班次：%v", err)
			}
			// 建立时保存的有效说明被旧数据式空白记录取代：记录仍在、正文无效。
			blankExistingNotes(f, blank)

			h, err := f.svc.CreateHandover(from.ID, to.ID)
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("只有空白说明时应报 ErrOverlap，got %v（交接 %+v）", err, h)
			}
			if !strings.Contains(err.Error(), from.ID) || !strings.Contains(err.Error(), to.ID) {
				t.Fatalf("错误应指出两班编号 %s 与 %s，got %v", from.ID, to.ID, err)
			}
			if !strings.Contains(err.Error(), "重叠说明") {
				t.Fatalf("错误应提示先填写重叠说明，got %v", err)
			}
			assertNoHandoverCreated(t, f)

			// 事项仍属于交班班次，失败不得改变事项归属。
			got, err := f.svc.GetItem(item.ID)
			if err != nil {
				t.Fatalf("get item: %v", err)
			}
			if got.CurrentShiftID != from.ID {
				t.Fatalf("失败后事项应仍属于交班班次 %s，got %s", from.ID, got.CurrentShiftID)
			}

			// 接班班次报告中也不应被悄悄指定接班对象。
			rep, err := f.svc.ShiftReport(to.ID)
			if err != nil {
				t.Fatalf("report: %v", err)
			}
			if len(rep.Incoming) != 0 {
				t.Fatalf("失败后接班班次不应出现接班关系，got %+v", rep.Incoming)
			}
		})
	}
}

// TestHandoverOverlapBlankNoteRejectsEmptyList：重叠班次即使没有未关闭事项，
// 首次发起空清单交接也必须满足同一条有效说明要求。
func TestHandoverOverlapBlankNoteRejectsEmptyList(t *testing.T) {
	f := newFixture(t)
	from, to := setupOverlappingPair(t, f)
	if _, err := f.svc.CloseShift(from.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	blankExistingNotes(f, "   ")

	h, err := f.svc.CreateHandover(from.ID, to.ID)
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("空清单交接只有空白说明时也应报 ErrOverlap，got %v（交接 %+v）", err, h)
	}
	if !strings.Contains(err.Error(), from.ID) || !strings.Contains(err.Error(), to.ID) {
		t.Fatalf("错误应指出两班编号 %s 与 %s，got %v", from.ID, to.ID, err)
	}
	assertNoHandoverCreated(t, f)
}

// TestHandoverOverlapMultipleNotesOrderIndependent：同一对班次保存了多条说明，
// 既有空记录也有真正写明原因的记录时，只要存在有效正文就允许首次交接，与记录
// 的存放次序无关：较早的空记录挡不住后来追加的有效说明；较晚的空记录也不能
// 使已有说明失效。
func TestHandoverOverlapMultipleNotesOrderIndependent(t *testing.T) {
	// 先有空记录，随后通过 note-add 追加有效说明：应放行，空记录保留可查。
	t.Run("空记录在前有效说明在后", func(t *testing.T) {
		f := newFixture(t)
		from, to := setupOverlappingPair(t, f)
		if _, err := f.svc.CloseShift(from.ID); err != nil {
			t.Fatalf("close: %v", err)
		}
		blankExistingNotes(f, "   ") // 最早一条是空记录 N001。

		if _, err := f.svc.CreateHandover(from.ID, to.ID); !errors.Is(err, ErrOverlap) {
			t.Fatalf("仅有空记录时应拒绝，got %v", err)
		}
		assertNoHandoverCreated(t, f)

		// 用户用现有 note-add 为这两个班次追加非空说明。
		added, err := f.svc.AddOverlapNote(from.ID, to.ID, "抢修并行一小时")
		if err != nil {
			t.Fatalf("note-add: %v", err)
		}
		h, err := f.svc.CreateHandover(from.ID, to.ID)
		if err != nil {
			t.Fatalf("追加有效说明后应能首次发起交接：%v", err)
		}
		if h.ID != "H001" {
			t.Fatalf("失败尝试不应占用交接编号，got %s", h.ID)
		}

		// 原来的空记录与新说明都保留可查，没有删除或覆盖历史记录。
		notes := f.svc.OverlapNotes(from.ID)
		if len(notes) != 2 {
			t.Fatalf("空记录与新说明都应保留，got %+v", notes)
		}
		var sawBlank, sawValid bool
		for _, n := range notes {
			if [2]string{n.ShiftA, n.ShiftB} != notePairKey(from.ID, to.ID) {
				t.Fatalf("说明应只关联这两个班次，got %+v", n)
			}
			if strings.TrimSpace(n.Note) == "" {
				sawBlank = true
			}
			if n.ID == added.ID && n.Note == "抢修并行一小时" {
				sawValid = true
			}
		}
		if !sawBlank || !sawValid {
			t.Fatalf("空记录与有效说明应同时保留，got %+v", notes)
		}
	})

	// 先有有效说明，后来又混入空记录：已有说明不因此失效。
	t.Run("有效说明在前空记录在后", func(t *testing.T) {
		f := newFixture(t)
		from, to := setupOverlappingPair(t, f) // 建立时已留下有效说明 N001。
		if _, err := f.svc.CloseShift(from.ID); err != nil {
			t.Fatalf("close: %v", err)
		}
		appendRawNote(f, "N900", "调度", from.ID, to.ID, " \n\t ")

		h, err := f.svc.CreateHandover(from.ID, to.ID)
		if err != nil {
			t.Fatalf("已有有效说明时后来的空记录不应使交接失败：%v", err)
		}
		if h.ID != "H001" {
			t.Fatalf("交接编号应为 H001，got %s", h.ID)
		}
	})
}

// TestHandoverOverlapNoteForOtherPairCannotBorrow：只关联其中一个班次、实际
// 属于另一对班次的有效说明不能借给这对重叠班次使用。
func TestHandoverOverlapNoteForOtherPairCannotBorrow(t *testing.T) {
	f := newFixture(t)
	// S001 08:00-16:00、S002 15:00-22:00 重叠一小时；S003 09:00-23:00 与
	// 两班都重叠。建立时按规则各自留下了说明：N001(S001,S002)、
	// N002(S001,S003)、N003(S002,S003)。
	from := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	to := mustShift(t, f, "调度", "李四", tsDay(2, 15, 0), tsDay(2, 22, 0), "S001 与 S002 建立时的说明")
	mustShift(t, f, "调度", "王五", tsDay(2, 9, 0), tsDay(2, 23, 0), "与前两班并行")
	if _, err := f.svc.CloseShift(from.ID); err != nil {
		t.Fatalf("close from: %v", err)
	}

	// 删掉 (S001,S002) 之间的全部说明，只保留涉及 S003 的说明；只关联其中
	// 一个班次的有效说明不能借给这一对。
	kept := f.store.data.Notes[:0]
	for _, n := range f.store.data.Notes {
		if [2]string{n.ShiftA, n.ShiftB} == notePairKey(from.ID, to.ID) {
			continue
		}
		kept = append(kept, n)
	}
	f.store.data.Notes = kept

	h, err := f.svc.CreateHandover(from.ID, to.ID)
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("属于另一对班次的说明不能借用，应报 ErrOverlap，got %v（交接 %+v）", err, h)
	}
	if !strings.Contains(err.Error(), from.ID) || !strings.Contains(err.Error(), to.ID) {
		t.Fatalf("错误应指出两班编号，got %v", err)
	}
	assertNoHandoverCreated(t, f)

	// 为这两个班次补填说明后即可发起，涉及其他班次的说明原样保留。
	if _, err := f.svc.AddOverlapNote(from.ID, to.ID, "两班重叠一小时"); err != nil {
		t.Fatalf("note-add: %v", err)
	}
	if _, err := f.svc.CreateHandover(from.ID, to.ID); err != nil {
		t.Fatalf("补填本对班次的说明后应能发起：%v", err)
	}
	if notes := f.svc.OverlapNotes("S003"); len(notes) != 2 {
		t.Fatalf("涉及其他班次的原有说明应保留，got %+v", notes)
	}
}

// TestHandoverOverlapEndpointStillNeedsNoNote：前班结束恰好等于后班开始不算
// 重叠，即使没有任何说明记录也能交接，本次检查不改变端点规则。
func TestHandoverOverlapEndpointStillNeedsNoNote(t *testing.T) {
	f := newFixture(t)
	from := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	to := mustShift(t, f, "调度", "李四", tsDay(2, 16, 0), tsDay(2, 23, 0), "")
	if _, err := f.svc.AddItem(from.ID, "事项", SeverityNormal, "", "李四"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.CloseShift(from.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(f.svc.OverlapNotes(from.ID)) != 0 {
		t.Fatalf("端点相接不应有说明记录")
	}
	h, err := f.svc.CreateHandover(from.ID, to.ID)
	if err != nil {
		t.Fatalf("端点相接无需重叠说明，应能交接：%v", err)
	}
	if h.ID != "H001" || len(h.Entries) != 1 {
		t.Fatalf("应正常建立非空交接，got %+v", h)
	}
}

// TestHandoverOverlapByInstantAcrossZonesBlankNote：重叠继续按实际时刻判断，
// 接班班次用时区写法表示同一段重叠时刻时，空白说明仍要拒绝；补填有效说明后
// 按同一实际时刻判定仍重叠、但有有效说明，允许交接。
func TestHandoverOverlapByInstantAcrossZonesBlankNote(t *testing.T) {
	// S002 用 UTC 表示 15:00-22:00 +08:00，即 07:00-14:00 UTC。
	utcRange := func(h1, h2 int) (time.Time, time.Time) {
		return time.Date(2026, 10, 2, h1, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 2, h2, 0, 0, 0, time.UTC)
	}
	f := newFixture(t)
	from := mustShift(t, f, "调度", "张三", tsDay(2, 8, 0), tsDay(2, 16, 0), "")
	s2, e2 := utcRange(7, 14)
	to, err := f.svc.CreateShift("调度", "李四", s2, e2, "跨时区填写的说明")
	if err != nil {
		t.Fatalf("create to: %v", err)
	}
	if _, err := f.svc.CloseShift(from.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	blankExistingNotes(f, "\t\n")

	if _, err := f.svc.CreateHandover(from.ID, to.ID); !errors.Is(err, ErrOverlap) {
		t.Fatalf("跨时区写法不改变重叠判定，空白说明仍应拒绝，got %v", err)
	}
	if _, err := f.svc.AddOverlapNote(from.ID, to.ID, "实际重叠一小时"); err != nil {
		t.Fatalf("note-add: %v", err)
	}
	if _, err := f.svc.CreateHandover(from.ID, to.ID); err != nil {
		t.Fatalf("补填有效说明后应按实际时刻放行：%v", err)
	}
}

// TestRepeatHandoverWithOnlyBlankNotesReturnsSavedRecord：已有交接的重复发起
// 仍返回原记录，不因新检查改变历史交接的结果——即使本地数据中该对班次现在
// 只剩空白说明（旧交接当年已发起），重复发起返回原记录，不重新做重叠校验、
// 不产生新交接、不改变事项归属。
func TestRepeatHandoverWithOnlyBlankNotesReturnsSavedRecord(t *testing.T) {
	f := newFixture(t)
	from, to := setupOverlappingPair(t, f)
	if _, err := f.svc.CloseShift(from.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 构造旧数据：一条已完成的已保存空清单交接 + 一个仍在交班班次的事项，
	// 该对班次的说明只剩空白正文。
	now := f.clock
	f.store.data.Handovers = append(f.store.data.Handovers, Handover{
		ID:          "H001",
		Position:    "调度",
		FromShiftID: from.ID,
		ToShiftID:   to.ID,
		CreatedAt:   now,
		CompletedAt: &now,
		Entries:     []HandoverEntry{},
	})
	f.store.data.HandoverSeq = 1
	f.store.data.Items = append(f.store.data.Items, Item{
		ID:             "I001",
		OriginShiftID:  from.ID,
		ShiftIDs:       []string{from.ID},
		CurrentShiftID: from.ID,
		Content:        "旧事项",
		Severity:       SeverityNormal,
		FollowOwner:    "李四",
		CreatedAt:      now,
	})
	blankExistingNotes(f, "   ")

	again, err := f.svc.CreateHandover(from.ID, to.ID)
	if !errors.Is(err, ErrHandoverExists) {
		t.Fatalf("重复发起应返回原记录（ErrHandoverExists），got %v", err)
	}
	if again.ID != "H001" || again.FromShiftID != from.ID || again.ToShiftID != to.ID {
		t.Fatalf("应返回原交接 H001 与原两班关系，got %+v", again)
	}
	if hs := f.svc.ListHandovers(); len(hs) != 1 || hs[0].ID != "H001" {
		t.Fatalf("重复发起不得产生第二条交接，got %+v", hs)
	}
	// 重复发起不移动事项。
	got, _ := f.svc.GetItem("I001")
	if got.CurrentShiftID != from.ID {
		t.Fatalf("重复发起不得改变事项归属，got %s", got.CurrentShiftID)
	}

	// 改换对象仍优先按改换对象拒绝，重叠说明检查不改变该承诺。
	other := mustShift(t, f, "调度", "赵六", tsDay(3, 8, 0), tsDay(3, 16, 0), "")
	_, err = f.svc.CreateHandover(from.ID, other.ID)
	if !errors.Is(err, ErrHandoverTarget) {
		t.Fatalf("重复发起改换对象应报 ErrHandoverTarget，got %v", err)
	}
	if !strings.Contains(err.Error(), to.ID) {
		t.Fatalf("改换对象错误应指出原接班班次 %s，got %v", to.ID, err)
	}
}
