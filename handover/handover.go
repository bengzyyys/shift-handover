package handover

import (
	"fmt"
	"time"
)

// CreateHandover 发起交班班次到接班班次的交接。
//
// 规则：
//   - 交班班次必须已经结束；接班班次必须存在且尚未结束；
//   - 接班班次与交班班次必须同岗位，且接班班次开始时间不得早于交班班次开始时间；
//   - 双方时间重叠时必须已有重叠说明；
//   - 不能交给自身；
//   - 一个交班班次只能指定一个接班对象：重复发起返回已有记录（不复制事项），
//     改换对象则报错。
//
// 交接清单为交班班次当前持有的全部未关闭事项；空清单直接完成。
// 返回交接记录；already 为 true 表示返回的是此前已建立的记录。
func (s *Store) CreateHandover(fromShiftID, toShiftID int) (h *Handover, already bool, err error) {
	from, err := s.GetShift(fromShiftID)
	if err != nil {
		return nil, false, err
	}
	to, err := s.GetShift(toShiftID)
	if err != nil {
		return nil, false, err
	}
	if from.ID == to.ID {
		return nil, false, &ValidationError{Msg: "不能将班次交接给自身"}
	}
	if from.Post != to.Post {
		return nil, false, &ValidationError{Msg: fmt.Sprintf(
			"岗位不一致：交班班次为「%s」，接班班次为「%s」，只能交接给同岗位班次", from.Post, to.Post)}
	}
	if !from.Ended {
		return nil, false, &StateError{Msg: fmt.Sprintf(
			"交班班次 #%d 尚未结束，不能发起交接", fromShiftID)}
	}
	if to.Ended {
		return nil, false, &StateError{Msg: fmt.Sprintf(
			"接班班次 #%d 已经结束，不能作为接班对象", toShiftID)}
	}
	if to.Start.Before(from.Start) {
		return nil, false, &ValidationError{Msg: fmt.Sprintf(
			"接班班次开始时间 %s 早于交班班次开始时间 %s",
			to.Start.Format("2006-01-02 15:04 -07:00"),
			from.Start.Format("2006-01-02 15:04 -07:00"))}
	}
	if overlaps(from, to) && !s.hasOverlapNote(from, to) {
		return nil, false, &ValidationError{Msg: fmt.Sprintf(
			"班次 #%d 与 #%d 时间重叠但没有重叠说明，不能交接", from.ID, to.ID)}
	}

	// 一个交班班次只能指定一个接班对象。
	if existing := s.HandoverByFromShift(fromShiftID); existing != nil {
		if existing.ToShiftID == toShiftID {
			return existing, true, nil
		}
		return nil, false, &StateError{Msg: fmt.Sprintf(
			"交班班次 #%d 已指定接班班次 #%d，不能改换为班次 #%d",
			fromShiftID, existing.ToShiftID, toShiftID)}
	}

	var items []*HandoverItem
	for _, it := range s.data.Items {
		if it.CurrentShiftID == fromShiftID && !it.Closed {
			items = append(items, &HandoverItem{ItemID: it.ID, Result: ResultPending})
		}
	}

	h = &Handover{
		ID:          s.data.NextHandoverID,
		FromShiftID: fromShiftID,
		ToShiftID:   toShiftID,
		Items:       items,
		CreatedAt:   now(),
	}
	h.recompute() // 空清单直接完成
	s.data.NextHandoverID++
	s.data.Handovers = append(s.data.Handovers, h)
	if err := s.save(); err != nil {
		return nil, false, err
	}
	return h, false, nil
}

// withHandoverItem 加载交接记录与事项并执行状态变更，
// 成功后重新计算完成状态并保存；失败时数据不落盘。
// 各动作自行设置 HandledAt（待处理项为零值）。
func (s *Store) withHandoverItem(handoverID, itemID int, fn func(h *Handover, hi *HandoverItem, it *Item) error) error {
	h, err := s.GetHandover(handoverID)
	if err != nil {
		return err
	}
	hi := h.FindItem(itemID)
	if hi == nil {
		return &NotFoundError{What: fmt.Sprintf("交接记录 #%d 中的事项", handoverID), ID: itemID}
	}
	it, err := s.GetItem(itemID)
	if err != nil {
		return err
	}
	if err := fn(h, hi, it); err != nil {
		return err
	}
	h.recompute()
	return s.save()
}

// HandoverReceive 接班人确认接收某项事项。
// 事项必须处于待处理状态；已接收项不能再次退回。
// 接收后事项进入接班班次的未关闭清单。
func (s *Store) HandoverReceive(handoverID, itemID int, operator string) error {
	operator = trim(operator)
	if operator == "" {
		return &ValidationError{Msg: "操作人不能为空"}
	}
	return s.withHandoverItem(handoverID, itemID, func(h *Handover, hi *HandoverItem, it *Item) error {
		if hi.Result != ResultPending {
			return &StateError{Msg: fmt.Sprintf(
				"事项 #%d 当前为「%s」状态，只有待处理项可以接收", itemID, hi.Result)}
		}
		hi.Result = ResultReceived
		hi.Operator = operator
		hi.HandledAt = now()
		it.CurrentShiftID = h.ToShiftID
		it.History = append(it.History, Event{
			Time: now(), Type: "接收", Operator: operator,
			Detail: fmt.Sprintf("交接 #%d：由接班班次 #%d 接收", h.ID, h.ToShiftID),
		})
		return nil
	})
}

// HandoverReturn 接班人退回某项事项，必须填写退回原因。
// 事项退回后仍属于交班班次清单；未处理项和退回项都使交接保持未完成。
func (s *Store) HandoverReturn(handoverID, itemID int, operator, reason string) error {
	operator = trim(operator)
	reason = trim(reason)
	if operator == "" {
		return &ValidationError{Msg: "操作人不能为空"}
	}
	if reason == "" {
		return &ValidationError{Msg: "退回原因不能为空"}
	}
	return s.withHandoverItem(handoverID, itemID, func(h *Handover, hi *HandoverItem, it *Item) error {
		if hi.Result != ResultPending {
			return &StateError{Msg: fmt.Sprintf(
				"事项 #%d 当前为「%s」状态，只有待处理项可以退回（已接收项不能退回）", itemID, hi.Result)}
		}
		hi.Result = ResultReturned
		hi.Operator = operator
		hi.HandledAt = now()
		hi.ReturnReason = reason
		it.History = append(it.History, Event{
			Time: now(), Type: "退回", Operator: operator,
			Detail: fmt.Sprintf("交接 #%d：%s", h.ID, reason),
		})
		return nil
	})
}

// HandoverTrack 接班人继续跟踪某项事项，表示已接收，
// 必须填写跟踪说明和后续负责人。接收后事项进入接班班次未关闭清单。
func (s *Store) HandoverTrack(handoverID, itemID int, operator, trackNote, trackOwner string) error {
	operator = trim(operator)
	trackNote = trim(trackNote)
	trackOwner = trim(trackOwner)
	if operator == "" {
		return &ValidationError{Msg: "操作人不能为空"}
	}
	if trackNote == "" {
		return &ValidationError{Msg: "跟踪说明不能为空"}
	}
	if trackOwner == "" {
		return &ValidationError{Msg: "后续负责人不能为空"}
	}
	return s.withHandoverItem(handoverID, itemID, func(h *Handover, hi *HandoverItem, it *Item) error {
		if hi.Result != ResultPending {
			return &StateError{Msg: fmt.Sprintf(
				"事项 #%d 当前为「%s」状态，只有待处理项可以继续跟踪", itemID, hi.Result)}
		}
		hi.Result = ResultTracking
		hi.Operator = operator
		hi.HandledAt = now()
		hi.TrackNote = trackNote
		hi.TrackOwner = trackOwner
		it.CurrentShiftID = h.ToShiftID
		it.History = append(it.History, Event{
			Time: now(), Type: "继续跟踪", Operator: operator,
			Detail: fmt.Sprintf("交接 #%d：%s（后续负责人：%s）", h.ID, trackNote, trackOwner),
		})
		return nil
	})
}
// 只有该项恢复待处理；既有接收结果不变，原文和退回原因不得覆盖。
func (s *Store) HandoverResubmit(handoverID, itemID int, operator, noteText string) error {
	operator = trim(operator)
	noteText = trim(noteText)
	if operator == "" {
		return &ValidationError{Msg: "操作人不能为空"}
	}
	if noteText == "" {
		return &ValidationError{Msg: "补充说明不能为空"}
	}
	return s.withHandoverItem(handoverID, itemID, func(h *Handover, hi *HandoverItem, it *Item) error {
		if hi.Result != ResultReturned {
			return &StateError{Msg: fmt.Sprintf(
				"事项 #%d 当前为「%s」状态，只有已退回项可以重新提交", itemID, hi.Result)}
		}
		hi.Result = ResultPending
		// 清空上次退回的处理人/时间，避免待处理项显示陈旧的处理信息；
		// 退回原因保留不覆盖，补充说明与事项历史中已记录操作人。
		hi.Operator = ""
		hi.HandledAt = time.Time{}
		hi.Notes = append(hi.Notes, Note{
			Time: now(), Operator: operator, Text: noteText,
		})
		it.History = append(it.History, Event{
			Time: now(), Type: "补充说明", Operator: operator,
			Detail: fmt.Sprintf("交接 #%d：%s", h.ID, noteText),
		})
		return nil
	})
}

// HandoverResubmit 退回后交班人追加非空说明，在同一交接记录上重新提交该项。