package handover

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Service 在 Store 之上实现班次交接的全部业务规则。
type Service struct {
	store *Store
	now   func() time.Time
}

// NewService 创建业务服务。
func NewService(store *Store) *Service {
	return &Service{store: store, now: time.Now}
}

// nowAt 供测试固定处理时间。
func (svc *Service) nowAt(fn func() time.Time) { svc.now = fn }

func clean(s string) string { return strings.TrimSpace(s) }

func requireNonEmpty(field, v string) error {
	if clean(v) == "" {
		return fmt.Errorf("%w：%s不能为空", ErrInvalidInput, field)
	}
	return nil
}

func intervalsOverlap(s1, e1, s2, e2 time.Time) bool {
	// 前班结束恰好等于后班开始（端点相等）不算重叠。
	return s1.Before(e2) && s2.Before(e1)
}

// notePair 返回无序班次编号对，作为重叠说明的稳定键。
func notePair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

func findShift(d *Data, id string) (*Shift, int) {
	for i := range d.Shifts {
		if d.Shifts[i].ID == id {
			return &d.Shifts[i], i
		}
	}
	return nil, -1
}

func findItem(d *Data, id string) (*Item, int) {
	for i := range d.Items {
		if d.Items[i].ID == id {
			return &d.Items[i], i
		}
	}
	return nil, -1
}

func findHandover(d *Data, id string) (*Handover, int) {
	for i := range d.Handovers {
		if d.Handovers[i].ID == id {
			return &d.Handovers[i], i
		}
	}
	return nil, -1
}

func findEntry(h *Handover, itemID string) (*HandoverEntry, int) {
	for i := range h.Entries {
		if h.Entries[i].ItemID == itemID {
			return &h.Entries[i], i
		}
	}
	return nil, -1
}

func findNote(d *Data, a, b string) *OverlapNote {
	x, y := notePair(a, b)
	for i := range d.Notes {
		if d.Notes[i].ShiftA == x && d.Notes[i].ShiftB == y {
			return &d.Notes[i]
		}
	}
	return nil
}

// currentItemsOfShift 返回与某班次相关的当前事项，按编号排列、每项只列一次。
// 用于进行中班次的当前信息展示，以及缺少结束时记录的旧数据班次。
func currentItemsOfShift(d *Data, shiftID string) []Item {
	out := []Item{}
	seen := map[string]bool{}
	for _, it := range d.Items {
		if seen[it.ID] {
			continue
		}
		if it.OriginShiftID == shiftID || it.CurrentShiftID == shiftID || contains(it.ShiftIDs, shiftID) {
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func hasNote(d *Data, a, b string) bool { return findNote(d, a, b) != nil }

// CreateShift 建立班次。岗位、负责人必填，结束时间必须晚于开始时间。
// 同一岗位按实际时刻比较区间：前班结束恰好等于后班开始不算重叠；
// 与同岗位已存班次存在重叠时 overlapNote 必须非空，说明会一并保存。
// 不同岗位互不影响。
func (svc *Service) CreateShift(position, owner string, start, end time.Time, overlapNote string) (Shift, error) {
	position = clean(position)
	owner = clean(owner)
	overlapNote = clean(overlapNote)
	if err := requireNonEmpty("岗位", position); err != nil {
		return Shift{}, err
	}
	if err := requireNonEmpty("负责人", owner); err != nil {
		return Shift{}, err
	}
	if !end.After(start) {
		return Shift{}, fmt.Errorf("%w：结束时间必须晚于开始时间（开始 %s，结束 %s）",
			ErrInvalidInput, start.Format(time.RFC3339), end.Format(time.RFC3339))
	}

	var created Shift
	err := svc.store.mutate(func(d *Data) error {
		var overlapping []Shift
		for _, sh := range d.Shifts {
			if sh.Position == position && intervalsOverlap(start, end, sh.Start, sh.End) {
				overlapping = append(overlapping, sh)
			}
		}
		if len(overlapping) > 0 && overlapNote == "" {
			ids := make([]string, len(overlapping))
			for i, sh := range overlapping {
				ids[i] = fmt.Sprintf("%s（%s ~ %s）", sh.ID, sh.Start.Format(time.RFC3339), sh.End.Format(time.RFC3339))
			}
			return fmt.Errorf("%w：与同岗位班次 %s 区间重叠，必须填写重叠说明",
				ErrOverlap, strings.Join(ids, "、"))
		}

		d.ShiftSeq++
		now := svc.now()
		created = Shift{
			ID:        fmt.Sprintf("S%03d", d.ShiftSeq),
			Position:  position,
			Owner:     owner,
			Start:     start,
			End:       end,
			CreatedAt: now,
		}
		d.Shifts = append(d.Shifts, created)

		// 为每个尚未登记说明的重叠班次保存说明；已有说明的不重复保存。
		for _, sh := range overlapping {
			if findNote(d, created.ID, sh.ID) != nil {
				continue
			}
			d.NoteSeq++
			x, y := notePair(created.ID, sh.ID)
			d.Notes = append(d.Notes, OverlapNote{
				ID:        fmt.Sprintf("N%03d", d.NoteSeq),
				Position:  position,
				ShiftA:    x,
				ShiftB:    y,
				Note:      overlapNote,
				CreatedAt: now,
			})
		}
		return nil
	})
	return created, err
}

// AddOverlapNote 为同岗位两个重叠班次保存重叠说明。
func (svc *Service) AddOverlapNote(shiftA, shiftB, note string) (OverlapNote, error) {
	note = clean(note)
	if err := requireNonEmpty("重叠说明", note); err != nil {
		return OverlapNote{}, err
	}
	if clean(shiftA) == clean(shiftB) {
		return OverlapNote{}, fmt.Errorf("%w：重叠说明需要两个不同的班次", ErrInvalidInput)
	}

	var saved OverlapNote
	err := svc.store.mutate(func(d *Data) error {
		sa, _ := findShift(d, clean(shiftA))
		sb, _ := findShift(d, clean(shiftB))
		if sa == nil || sb == nil {
			return fmt.Errorf("%w：班次 %s 或 %s 不存在", ErrNotFound, shiftA, shiftB)
		}
		if sa.Position != sb.Position {
			return fmt.Errorf("%w：班次 %s（%s）与 %s（%s）岗位不同，无需重叠说明",
				ErrPositionMismatch, sa.ID, sa.Position, sb.ID, sb.Position)
		}
		if !intervalsOverlap(sa.Start, sa.End, sb.Start, sb.End) {
			return fmt.Errorf("%w：班次 %s 与 %s 的时间区间不重叠（端点相接不算重叠）",
				ErrInvalidInput, sa.ID, sb.ID)
		}
		d.NoteSeq++
		x, y := notePair(sa.ID, sb.ID)
		saved = OverlapNote{
			ID:        fmt.Sprintf("N%03d", d.NoteSeq),
			Position:  sa.Position,
			ShiftA:    x,
			ShiftB:    y,
			Note:      note,
			CreatedAt: svc.now(),
		}
		d.Notes = append(d.Notes, saved)
		return nil
	})
	return saved, err
}

// AddItem 向未结束班次新增事项。内容、严重程度、后续负责人必填，限制条件可空。
func (svc *Service) AddItem(shiftID, content string, severity Severity, constraints, followOwner string) (Item, error) {
	shiftID = clean(shiftID)
	content = clean(content)
	constraints = clean(constraints)
	followOwner = clean(followOwner)
	if err := requireNonEmpty("事项内容", content); err != nil {
		return Item{}, err
	}
	if err := requireNonEmpty("后续负责人", followOwner); err != nil {
		return Item{}, err
	}
	if !severity.valid() {
		return Item{}, fmt.Errorf("%w：严重程度无效", ErrInvalidInput)
	}

	var created Item
	err := svc.store.mutate(func(d *Data) error {
		sh, _ := findShift(d, shiftID)
		if sh == nil {
			return fmt.Errorf("%w：班次 %s", ErrNotFound, shiftID)
		}
		if sh.Closed {
			return fmt.Errorf("%w：班次 %s 已结束，不能新增事项", ErrShiftClosed, sh.ID)
		}
		d.ItemSeq++
		now := svc.now()
		created = Item{
			ID:             fmt.Sprintf("I%03d", d.ItemSeq),
			OriginShiftID:  sh.ID,
			ShiftIDs:       []string{sh.ID},
			CurrentShiftID: sh.ID,
			Content:        content,
			Severity:       severity,
			Constraints:    constraints,
			FollowOwner:    followOwner,
			CreatedAt:      now,
			Events: []ItemEvent{
				{At: now, Kind: "created", Detail: "事项建立"},
			},
		}
		d.Items = append(d.Items, created)
		return nil
	})
	return created, err
}

// UpdateItem 在事项所在班次结束前修改其内容、严重程度、限制条件和后续负责人。
func (svc *Service) UpdateItem(itemID, content string, severity Severity, constraints, followOwner string) (Item, error) {
	itemID = clean(itemID)
	content = clean(content)
	constraints = clean(constraints)
	followOwner = clean(followOwner)
	if err := requireNonEmpty("事项内容", content); err != nil {
		return Item{}, err
	}
	if err := requireNonEmpty("后续负责人", followOwner); err != nil {
		return Item{}, err
	}
	if !severity.valid() {
		return Item{}, fmt.Errorf("%w：严重程度无效", ErrInvalidInput)
	}

	var updated Item
	err := svc.store.mutate(func(d *Data) error {
		it, _ := findItem(d, itemID)
		if it == nil {
			return fmt.Errorf("%w：事项 %s", ErrNotFound, itemID)
		}
		sh, _ := findShift(d, it.CurrentShiftID)
		if sh == nil || sh.Closed {
			return fmt.Errorf("%w：事项 %s 所属班次 %s 已结束，内容不能修改",
				ErrShiftClosed, itemID, it.CurrentShiftID)
		}
		changes := []string{}
		if it.Content != content {
			changes = append(changes, "内容")
		}
		if it.Severity != severity {
			changes = append(changes, "严重程度")
		}
		if it.Constraints != constraints {
			changes = append(changes, "限制条件")
		}
		if it.FollowOwner != followOwner {
			changes = append(changes, "后续负责人")
		}
		it.Content = content
		it.Severity = severity
		it.Constraints = constraints
		it.FollowOwner = followOwner
		it.Events = append(it.Events, ItemEvent{
			At:     svc.now(),
			Kind:   "updated",
			Detail: "修改字段：" + joinOr(changes, "无变更"),
		})
		updated = *it
		return nil
	})
	return updated, err
}

func joinOr(xs []string, or string) string {
	if len(xs) == 0 {
		return or
	}
	return strings.Join(xs, "、")
}

// CloseItem 在事项所在班次结束前关闭该事项，须填写操作人。
func (svc *Service) CloseItem(itemID, operator string) (Item, error) {
	itemID = clean(itemID)
	operator = clean(operator)
	if err := requireNonEmpty("操作人", operator); err != nil {
		return Item{}, err
	}

	var closed Item
	err := svc.store.mutate(func(d *Data) error {
		it, _ := findItem(d, itemID)
		if it == nil {
			return fmt.Errorf("%w：事项 %s", ErrNotFound, itemID)
		}
		if it.Closed {
			return fmt.Errorf("%w：事项 %s 已关闭，不能重复关闭", ErrHandoverState, itemID)
		}
		sh, _ := findShift(d, it.CurrentShiftID)
		if sh == nil || sh.Closed {
			return fmt.Errorf("%w：事项 %s 所属班次 %s 已结束，关闭状态不能修改",
				ErrShiftClosed, itemID, it.CurrentShiftID)
		}
		now := svc.now()
		it.Closed = true
		it.ClosedAt = &now
		it.CloseOperator = operator
		it.Events = append(it.Events, ItemEvent{At: now, Kind: "closed", Operator: operator})
		closed = *it
		return nil
	})
	return closed, err
}

// CloseShift 结束班次。接班交接仍有待处理或退回项时不允许结束；空清单也可以结束。
// 结束成功时把在班事项（本班新增与已接收，含结束前已关闭者）冻结为结束时记录；
// 校验或保存失败时不留下任何记录，班次保持进行中。
func (svc *Service) CloseShift(shiftID string) (Shift, error) {
	shiftID = clean(shiftID)
	var result Shift
	err := svc.store.mutate(func(d *Data) error {
		sh, _ := findShift(d, shiftID)
		if sh == nil {
			return fmt.Errorf("%w：班次 %s", ErrNotFound, shiftID)
		}
		if sh.Closed {
			return fmt.Errorf("%w：班次 %s 已结束", ErrShiftClosed, sh.ID)
		}
		for i := range d.Handovers {
			h := &d.Handovers[i]
			if h.ToShiftID == sh.ID && !h.Completed() {
				return fmt.Errorf("%w：接班交接 %s 仍有未处理或退回事项，班次不能结束",
					ErrHandoverState, h.ID)
			}
		}
		now := svc.now()
		sh.Closed = true
		sh.ClosedAt = &now

		// 冻结结束时在班的全部事项。退回或未确认的交接事项当前班次仍在交班
		// 班次，不会被快照；结束前已关闭的事项保留关闭人与关闭时间。
		record := &ShiftCloseRecord{Items: []CloseItemSnapshot{}}
		for i := range d.Items {
			it := &d.Items[i]
			if it.CurrentShiftID != sh.ID {
				continue
			}
			record.Items = append(record.Items, CloseItemSnapshot{
				ItemID:        it.ID,
				Content:       it.Content,
				Severity:      it.Severity,
				Constraints:   it.Constraints,
				FollowOwner:   it.FollowOwner,
				Closed:        it.Closed,
				ClosedAt:      it.ClosedAt,
				CloseOperator: it.CloseOperator,
			})
		}
		sort.Slice(record.Items, func(i, j int) bool { return record.Items[i].ItemID < record.Items[j].ItemID })
		sh.CloseRecord = record

		result = *sh
		return nil
	})
	return result, err
}

// CreateHandover 由交班班次向同岗位接班班次发起交接。
// 重复向同一接班班次发起返回已有记录（ErrHandoverExists）；该承诺在接班班次
// 随后结束、事项已在后续班次关闭或继续流转后仍然成立，返回保存的原交接内容，
// 不重新挑选清单、不重新接收或移动事项、不追加经过。改换接班对象报
// ErrHandoverTarget 并指出原接班班次，无论新对象是否结束都不产生第二条交接。
// 不存在的班次编号与交给自身始终按各自错误拒绝；同岗位及接班班次尚未结束等
// 要求仅对首次发起生效。
func (svc *Service) CreateHandover(fromShiftID, toShiftID string) (Handover, error) {
	fromShiftID = clean(fromShiftID)
	toShiftID = clean(toShiftID)

	var result Handover
	err := svc.store.mutate(func(d *Data) error {
		from, _ := findShift(d, fromShiftID)
		to, _ := findShift(d, toShiftID)
		if from == nil {
			return fmt.Errorf("%w：交班班次 %s", ErrNotFound, fromShiftID)
		}
		if to == nil {
			return fmt.Errorf("%w：接班班次 %s", ErrNotFound, toShiftID)
		}
		if from.ID == to.ID {
			return fmt.Errorf("%w：交班班次与接班班次不能都是 %s", ErrSameShift, from.ID)
		}
		// 同一交班班次只能指定一个接班对象。重复发起（包括接班班次随后已
		// 结束、事项已在后续班次关闭或继续流转的情况）一律返回保存的原记录，
		// 不重新接收事项、不追加经过；改换为另一个确实存在的接班对象（无论其
		// 是否结束、是否同岗位）都按改换对象报错并指出原接班班次，且不能生成
		// 第二条交接。该判定先于同岗位、接班班次状态等首次发起校验；编号不
		// 存在与交给自身的校验仍在其前。
		for i := range d.Handovers {
			if d.Handovers[i].FromShiftID == from.ID {
				existing := &d.Handovers[i]
				result = *existing
				if existing.ToShiftID != to.ID {
					return fmt.Errorf("%w：交班班次 %s 已指定接班班次 %s，不能改换为 %s",
						ErrHandoverTarget, from.ID, existing.ToShiftID, to.ID)
				}
				return fmt.Errorf("%w：交接 %s", ErrHandoverExists, existing.ID)
			}
		}

		if from.Position != to.Position {
			return fmt.Errorf("%w：班次 %s（%s）不能交给其他岗位 %s（%s）",
				ErrPositionMismatch, from.ID, from.Position, to.ID, to.Position)
		}

		// 以下为首次发起的要求：接班班次尚未结束、开始时间不早于交班班次、
		// 交班班次已经结束，重叠区间须有说明。
		if to.Closed {
			return fmt.Errorf("%w：接班班次 %s 已结束，不能交接", ErrShiftClosed, to.ID)
		}
		if to.Start.Before(from.Start) {
			return fmt.Errorf("%w：接班班次 %s 开始时间 %s 早于交班班次 %s 开始时间 %s",
				ErrInvalidInput, to.ID, to.Start.Format(time.RFC3339),
				from.ID, from.Start.Format(time.RFC3339))
		}
		if !from.Closed {
			return fmt.Errorf("%w：交班班次 %s 尚未结束，结束班次后未关闭事项才成为交接清单",
				ErrShiftNotClosed, from.ID)
		}
		if intervalsOverlap(from.Start, from.End, to.Start, to.End) && !hasNote(d, from.ID, to.ID) {
			return fmt.Errorf("%w：交班班次 %s 与接班班次 %s 时间区间重叠，须先填写重叠说明",
				ErrOverlap, from.ID, to.ID)
		}

		entries := []HandoverEntry{}
		for i := range d.Items {
			it := &d.Items[i]
			if it.CurrentShiftID == from.ID && !it.Closed {
				entries = append(entries, HandoverEntry{
					ItemID:      it.ID,
					Content:     it.Content,
					Severity:    it.Severity,
					Constraints: it.Constraints,
					FollowOwner: it.FollowOwner,
					Status:      EntryPending,
				})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ItemID < entries[j].ItemID })

		d.HandoverSeq++
		now := svc.now()
		h := Handover{
			ID:          fmt.Sprintf("H%03d", d.HandoverSeq),
			Position:    from.Position,
			FromShiftID: from.ID,
			ToShiftID:   to.ID,
			CreatedAt:   now,
			Entries:     entries,
		}
		if len(entries) == 0 {
			// 空清单直接完成。
			h.CompletedAt = &now
		}
		d.Handovers = append(d.Handovers, h)
		result = h
		return nil
	})
	return result, err
}

// ProcessEntry 由接班人逐项处理交接事项，须填写操作人。
// action 为 confirm（确认接收）、return（退回，须填原因）或 track（继续跟踪，须填跟踪说明和后续负责人）。
// 已退回项表示等待交班人补充，只有在原交接记录上成功重新提交、恢复待处理后才能再次
// 确认、继续跟踪或退回；未重新提交前直接处理一律返回状态错误，且不改变任何数据。
func (svc *Service) ProcessEntry(handoverID, itemID string, action EntryAction, operator, reason, trackingNote, nextFollowOwner string) (Handover, error) {
	handoverID = clean(handoverID)
	itemID = clean(itemID)
	operator = clean(operator)
	reason = clean(reason)
	trackingNote = clean(trackingNote)
	nextFollowOwner = clean(nextFollowOwner)

	if action != ActionConfirm && action != ActionReturn && action != ActionTrack {
		return Handover{}, fmt.Errorf("%w：未知操作 %q", ErrInvalidInput, action)
	}
	if err := requireNonEmpty("操作人", operator); err != nil {
		return Handover{}, err
	}

	var result Handover
	err := svc.store.mutate(func(d *Data) error {
		h, _ := findHandover(d, handoverID)
		if h == nil {
			return fmt.Errorf("%w：交接 %s", ErrNotFound, handoverID)
		}
		e, _ := findEntry(h, itemID)
		if e == nil {
			return fmt.Errorf("%w：交接 %s 中没有事项 %s", ErrNotFound, h.ID, itemID)
		}
		if e.Status.Received() {
			return fmt.Errorf("%w：事项 %s 已%s，不能重复处理或退回",
				ErrHandoverState, e.ItemID, e.Status.Label())
		}
		if e.Status == EntryReturned {
			// 退回表示等待交班人补充：必须先在原交接记录上补充说明并重新提交，
			// 该项恢复待处理后才能再次确认、继续跟踪或退回。即使本次填写了
			// 操作人、退回原因或跟踪说明，也不能跳过这一步。
			return fmt.Errorf("%w：事项 %s 已退回，正等待交班人补充说明并在原交接记录 %s 上重新提交；请先完成补充并重新提交后再处理",
				ErrHandoverState, e.ItemID, h.ID)
		}
		switch action {
		case ActionReturn:
			if err := requireNonEmpty("退回原因", reason); err != nil {
				return err
			}
		case ActionTrack:
			if err := requireNonEmpty("跟踪说明", trackingNote); err != nil {
				return err
			}
			if err := requireNonEmpty("后续负责人", nextFollowOwner); err != nil {
				return err
			}
		}

		now := svc.now()
		switch action {
		case ActionConfirm:
			e.Status = EntryConfirmed
		case ActionTrack:
			e.Status = EntryTracking
			e.TrackingNote = trackingNote
			e.FollowOwner = nextFollowOwner
		case ActionReturn:
			e.Status = EntryReturned
			e.Rounds = append(e.Rounds, ReturnRound{
				Seq:            len(e.Rounds) + 1,
				ReturnedAt:     now,
				ReturnOperator: operator,
				Reason:         reason,
			})
		}
		e.Operator = operator
		e.ProcessedAt = &now

		if action != ActionReturn {
			// 确认与继续跟踪都进入接班班次的未关闭清单，保留原编号与历史。
			if it, _ := findItem(d, e.ItemID); it != nil && it.CurrentShiftID == h.FromShiftID {
				it.CurrentShiftID = h.ToShiftID
				if !contains(it.ShiftIDs, h.ToShiftID) {
					it.ShiftIDs = append(it.ShiftIDs, h.ToShiftID)
				}
				detail := "接班班次 " + h.ToShiftID + " 接收：" + e.Status.Label()
				if action == ActionTrack {
					// 继续跟踪指定新的后续负责人，回写到事项并保留跟踪说明。
					it.FollowOwner = nextFollowOwner
					detail += "；跟踪说明：" + trackingNote + "；后续负责人：" + nextFollowOwner
				}
				it.Events = append(it.Events, ItemEvent{At: now, Kind: "received", Operator: operator, Detail: detail})
			}
		}

		if h.Completed() && h.CompletedAt == nil {
			h.CompletedAt = &now
		}
		result = *h
		return nil
	})
	return result, err
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ResubmitReturned 由交班人对退回项追加非空说明并在同一交接记录上重新提交；
// 补充写入最近一次退回记录（保留该轮原因、退回人和退回时间，并记录补充人、
// 补充时间与重新提交时间），以前各轮说明不覆盖。只有该项恢复待处理，当前结果的
// 接班处理人与处理时间显示为尚未处理；原文、严重程度、限制条件不变，事项仍留在
// 交班班次，同一交接的其他事项不受影响。重新提交本身不表示接收。
// 对待处理、已确认或继续跟踪的事项重新提交报状态错误，不写入补充说明。
func (svc *Service) ResubmitReturned(handoverID, itemID, operator, supplement string) (Handover, error) {
	handoverID = clean(handoverID)
	itemID = clean(itemID)
	operator = clean(operator)
	supplement = clean(supplement)
	if err := requireNonEmpty("操作人", operator); err != nil {
		return Handover{}, err
	}
	if err := requireNonEmpty("补充说明", supplement); err != nil {
		return Handover{}, err
	}

	var result Handover
	err := svc.store.mutate(func(d *Data) error {
		h, _ := findHandover(d, handoverID)
		if h == nil {
			return fmt.Errorf("%w：交接 %s", ErrNotFound, handoverID)
		}
		e, _ := findEntry(h, itemID)
		if e == nil {
			return fmt.Errorf("%w：交接 %s 中没有事项 %s", ErrNotFound, h.ID, itemID)
		}
		if e.Status != EntryReturned {
			return fmt.Errorf("%w：事项 %s 当前状态为 %s，只有退回项可以补充说明后重新提交",
				ErrHandoverState, e.ItemID, e.Status.Label())
		}
		if len(e.Rounds) == 0 {
			return fmt.Errorf("%w：事项 %s 缺少退回记录", ErrHandoverState, e.ItemID)
		}
		round := &e.Rounds[len(e.Rounds)-1]
		if round.ResubmittedAt != nil {
			return fmt.Errorf("%w：事项 %s 已重新提交，等待接班人处理", ErrHandoverState, e.ItemID)
		}
		now := svc.now()
		round.Supplement = supplement
		round.SupplementOperator = operator
		round.SupplementAt = &now
		round.ResubmittedAt = &now
		// 仅这一个事项恢复待处理：当前结果的接班处理人与处理时间显示为
		// 尚未处理；上一轮退回信息仍完整保留在 Rounds 历史中。
		e.Status = EntryPending
		e.Operator = ""
		e.ProcessedAt = nil
		result = *h
		return nil
	})
	return result, err
}

// GetShift 按编号查询班次。
func (svc *Service) GetShift(id string) (Shift, error) {
	sh, _ := findShift(&svc.store.data, clean(id))
	if sh == nil {
		return Shift{}, fmt.Errorf("%w：班次 %s", ErrNotFound, id)
	}
	return *sh, nil
}

// ListShifts 返回全部班次，按编号排序。
func (svc *Service) ListShifts() []Shift {
	out := append([]Shift(nil), svc.store.data.Shifts...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetItem 按稳定编号查询事项。
func (svc *Service) GetItem(id string) (Item, error) {
	it, _ := findItem(&svc.store.data, clean(id))
	if it == nil {
		return Item{}, fmt.Errorf("%w：事项 %s", ErrNotFound, id)
	}
	return *it, nil
}

// ItemJourney 凭事项编号汇总它从建立到当前的处理经过：保留事项自身的
// 建立、修改、关闭记录，并加入它参与的各次交接（发起交接、逐轮退回、补充后
// 重新提交、确认接收或继续跟踪），无须先知道涉及哪些交接编号。
// 经过按实际发生时刻排序（带不同时区的时间按同一实际时刻比较）；同一时刻下
// 同一交接内保持发起、该轮退回、该轮重新提交、后续处理的先后，不同交接按
// 交接编号排列。同一次接收若已出现在事项历史里只展示一次。只读查询，
// 不改变事项、交接进度或班次结束时记录。
func (svc *Service) ItemJourney(itemID string) (ItemJourney, error) {
	itemID = clean(itemID)
	d := &svc.store.data
	it, _ := findItem(d, itemID)
	if it == nil {
		return ItemJourney{}, fmt.Errorf("%w：事项 %s", ErrNotFound, itemID)
	}
	j := ItemJourney{Item: *it}

	// 排序键：先按实际发生时刻（未记录时间的排最后），同一时刻下事项自身
	// 事件在前，其后按交接编号分组，组内保持生成顺序（发起、逐轮退回、
	// 该轮重新提交、后续处理）。
	type keyedEvent struct {
		ev    JourneyEvent
		group string // "" 表示事项自身事件，否则为交接编号
		ord   int    // 同一（时刻、分组）内的先后
	}
	var events []keyedEvent
	seq := 0
	add := func(ev JourneyEvent, group string) {
		events = append(events, keyedEvent{ev: ev, group: group, ord: seq})
		seq++
	}

	// received 事件与交接中的接收处理是同一次接收，只展示一次（以交接事件
	// 展示，信息更全）；匹配不上的旧数据 received 事件仍原样保留。
	receivedUsed := make([]bool, len(it.Events))

	hs := append([]Handover(nil), d.Handovers...)
	sort.Slice(hs, func(i, k int) bool { return hs[i].ID < hs[k].ID })
	for k := range hs {
		h := &hs[k]
		e, _ := findEntry(h, itemID)
		if e == nil {
			continue
		}
		j.HasHandovers = true
		j.Results = append(j.Results, EntryView{
			HandoverID: h.ID,
			FromShift:  h.FromShiftID,
			ToShift:    h.ToShiftID,
			Entry:      *e,
		})

		// 发起交接：交接记录本身不记操作人，明确显示未记录，不以班次负责人代替。
		add(JourneyEvent{
			At: h.CreatedAt, TimeKnown: !h.CreatedAt.IsZero(),
			Kind: "handover-init", HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
		}, h.ID)
		for _, r := range e.Rounds {
			add(JourneyEvent{
				At: r.ReturnedAt, TimeKnown: !r.ReturnedAt.IsZero(),
				Kind: "return", Operator: r.ReturnOperator,
				HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
				RoundSeq: r.Seq, Reason: r.Reason,
			}, h.ID)
			if r.ResubmittedAt != nil {
				add(JourneyEvent{
					At: *r.ResubmittedAt, TimeKnown: !r.ResubmittedAt.IsZero(),
					Kind: "resubmit", Operator: r.SupplementOperator,
					HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
					RoundSeq: r.Seq, Supplement: r.Supplement,
					SupplementOperator: r.SupplementOperator, SupplementAt: r.SupplementAt,
				}, h.ID)
			}
		}
		if e.Status.Received() {
			kind := "confirm"
			if e.Status == EntryTracking {
				kind = "track"
			}
			ev := JourneyEvent{
				Kind: kind, Operator: e.Operator,
				HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
				TrackingNote: e.TrackingNote,
				// 继续跟踪当时指定的后续负责人取自交接记录快照，
				// 之后修改事项负责人不改变这里的历史值。
				FollowOwner: e.FollowOwner,
			}
			// 处理时间缺失或为零值（0001-01-01T00:00:00Z）都视为未记录：
			// 事件保留并排在有真实时间的事件之后，不用其他时间推测补齐。
			if pt := validProcessedAt(e.ProcessedAt); pt != nil {
				ev.At, ev.TimeKnown = *pt, true
			}
			add(ev, h.ID)
			if pt := validProcessedAt(e.ProcessedAt); pt != nil {
				for i := range it.Events {
					iev := &it.Events[i]
					if iev.Kind == "received" && !receivedUsed[i] &&
						iev.At.Equal(*pt) && iev.Operator == e.Operator {
						receivedUsed[i] = true
						break
					}
				}
			}
		}
	}

	// 事项自身历史：建立、修改、关闭原样保留；未被交接接收事件覆盖的
	// received 事件（旧数据）也保留。
	for i, iev := range it.Events {
		if iev.Kind == "received" && receivedUsed[i] {
			continue
		}
		add(JourneyEvent{
			At: iev.At, TimeKnown: !iev.At.IsZero(),
			Kind: iev.Kind, Operator: iev.Operator, Detail: iev.Detail,
		}, "")
	}

	sort.SliceStable(events, func(i, k int) bool {
		a, b := events[i], events[k]
		if a.ev.TimeKnown != b.ev.TimeKnown {
			return a.ev.TimeKnown
		}
		if a.ev.TimeKnown && !a.ev.At.Equal(b.ev.At) {
			return a.ev.At.Before(b.ev.At)
		}
		if a.group != b.group {
			return a.group < b.group
		}
		return a.ord < b.ord
	})
	for _, ke := range events {
		j.Events = append(j.Events, ke.ev)
	}
	return j, nil
}

// GetHandover 按编号查询交接。
func (svc *Service) GetHandover(id string) (Handover, error) {
	h, _ := findHandover(&svc.store.data, clean(id))
	if h == nil {
		return Handover{}, fmt.Errorf("%w：交接 %s", ErrNotFound, id)
	}
	return *h, nil
}

// ListHandovers 返回全部交接记录，按编号排序。
func (svc *Service) ListHandovers() []Handover {
	out := append([]Handover(nil), svc.store.data.Handovers...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OverlapNotes 返回涉及指定班次的全部重叠说明。
func (svc *Service) OverlapNotes(shiftID string) []OverlapNote {
	shiftID = clean(shiftID)
	out := []OverlapNote{}
	for _, n := range svc.store.data.Notes {
		if n.ShiftA == shiftID || n.ShiftB == shiftID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ShiftReport 按班次汇总完整事项、关闭情况、接班对象、每项交接当前结果与历次退回/补充说明。
// 进行中的班次展示当前事项；已结束班次展示结束时冻结的事项记录，
// 旧数据中缺少结束时记录的班次只展示当前事项并标明历史不完整。
func (svc *Service) ShiftReport(shiftID string) (ShiftReport, error) {
	shiftID = clean(shiftID)
	d := &svc.store.data
	rep := ShiftReport{Results: map[string][]EntryView{}, LatestItems: map[string]Item{}}
	sh, _ := findShift(d, shiftID)
	if sh == nil {
		return ShiftReport{}, fmt.Errorf("%w：班次 %s", ErrNotFound, shiftID)
	}
	rep.Shift = *sh

	for _, n := range d.Notes {
		if n.ShiftA == sh.ID || n.ShiftB == sh.ID {
			rep.OverlapNotes = append(rep.OverlapNotes, n)
		}
	}
	sort.Slice(rep.OverlapNotes, func(i, j int) bool { return rep.OverlapNotes[i].ID < rep.OverlapNotes[j].ID })

	switch {
	case !sh.Closed:
		// 进行中的班次：事项可继续修改、关闭，展示当前信息。
		rep.Items = currentItemsOfShift(d, sh.ID)
	case sh.CloseRecord != nil:
		// 已结束且有结束时记录：展示冻结的结束时信息，并对照最新状态。
		rep.ItemsAtClose = true
		rep.CloseItems = append([]CloseItemSnapshot(nil), sh.CloseRecord.Items...)
		sort.Slice(rep.CloseItems, func(i, j int) bool { return rep.CloseItems[i].ItemID < rep.CloseItems[j].ItemID })
		for _, s := range rep.CloseItems {
			if it, _ := findItem(d, s.ItemID); it != nil {
				rep.LatestItems[s.ItemID] = *it
			}
		}
	default:
		// 旧数据：结束时未留下记录，只能展示当前信息，不能宣称是结束时事实。
		rep.HistoryIncomplete = true
		rep.Items = currentItemsOfShift(d, sh.ID)
	}
	sort.Slice(rep.Items, func(i, j int) bool { return rep.Items[i].ID < rep.Items[j].ID })

	for i := range d.Handovers {
		h := &d.Handovers[i]
		involved := h.FromShiftID == sh.ID || h.ToShiftID == sh.ID
		if !involved {
			continue
		}
		if h.FromShiftID == sh.ID {
			cp := *h
			rep.Outgoing = &cp
		}
		if h.ToShiftID == sh.ID {
			rep.Incoming = append(rep.Incoming, *h)
		}
		for j := range h.Entries {
			rep.Results[h.Entries[j].ItemID] = append(rep.Results[h.Entries[j].ItemID], EntryView{
				HandoverID: h.ID,
				FromShift:  h.FromShiftID,
				ToShift:    h.ToShiftID,
				Entry:      h.Entries[j],
			})
		}
	}
	sort.Slice(rep.Incoming, func(i, j int) bool { return rep.Incoming[i].ID < rep.Incoming[j].ID })
	for k := range rep.Results {
		sort.Slice(rep.Results[k], func(i, j int) bool { return rep.Results[k][i].HandoverID < rep.Results[k][j].HandoverID })
	}
	return rep, nil
}
