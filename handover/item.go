package handover

import (
	"fmt"
	"strings"
)

// ItemEdit 描述对事项的修改；指针非 nil 表示修改该字段。
// 内容和后续负责人不允许改为空字符串。
type ItemEdit struct {
	Content     *string
	Severity    *Severity
	Constraints *string
	FollowOwner *string
}

// AddItem 在指定班次下新增一条事项。
//
// 班次必须存在且尚未结束；内容、后续负责人必填，严重程度必填（缺省为普通），
// 限制条件可以为空。事项拥有全局稳定编号。
func (s *Store) AddItem(shiftID int, content string, severity Severity, constraints, followOwner string) (*Item, error) {
	sh, err := s.GetShift(shiftID)
	if err != nil {
		return nil, err
	}
	if sh.Ended {
		return nil, &StateError{Msg: fmt.Sprintf("班次 #%d 已结束，不能新增事项", shiftID)}
	}
	content = trim(content)
	followOwner = trim(followOwner)
	if content == "" {
		return nil, &ValidationError{Msg: "事项内容不能为空"}
	}
	if followOwner == "" {
		return nil, &ValidationError{Msg: "后续负责人不能为空"}
	}

	it := &Item{
		ID:             s.data.NextItemID,
		Content:        content,
		Severity:       severity,
		Constraints:    trim(constraints),
		FollowOwner:    followOwner,
		OriginShiftID:  sh.ID,
		CurrentShiftID: sh.ID,
		CreatedAt:      now(),
		History: []Event{{
			Time:   now(),
			Type:   "建立",
			Detail: fmt.Sprintf("班次 #%d", sh.ID),
		}},
	}
	s.data.NextItemID++
	s.data.Items = append(s.data.Items, it)
	if err := s.save(); err != nil {
		return nil, err
	}
	return it, nil
}

// EditItem 修改事项。
//
// 事项必须存在，且当前所在班次尚未结束；已结束班次的事项内容不能修改。
// 必填字段（内容、后续负责人）不允许改为空。
func (s *Store) EditItem(itemID int, edit ItemEdit) error {
	it, err := s.GetItem(itemID)
	if err != nil {
		return err
	}
	cur, err := s.GetShift(it.CurrentShiftID)
	if err != nil {
		return err
	}
	if cur.Ended {
		return &StateError{Msg: fmt.Sprintf(
			"事项 #%d 当前所在班次 #%d 已结束，事项内容不能修改", itemID, cur.ID)}
	}

	var changes []string
	if edit.Content != nil {
		v := trim(*edit.Content)
		if v == "" {
			return &ValidationError{Msg: "事项内容不能为空"}
		}
		if v != it.Content {
			it.Content = v
			changes = append(changes, "内容")
		}
	}
	if edit.Severity != nil && *edit.Severity != it.Severity {
		it.Severity = *edit.Severity
		changes = append(changes, "严重程度")
	}
	if edit.Constraints != nil {
		v := trim(*edit.Constraints)
		if v != it.Constraints {
			it.Constraints = v
			changes = append(changes, "限制条件")
		}
	}
	if edit.FollowOwner != nil {
		v := trim(*edit.FollowOwner)
		if v == "" {
			return &ValidationError{Msg: "后续负责人不能为空"}
		}
		if v != it.FollowOwner {
			it.FollowOwner = v
			changes = append(changes, "后续负责人")
		}
	}

	if len(changes) > 0 {
		it.History = append(it.History, Event{
			Time:   now(),
			Type:   "修改",
			Detail: strings.Join(changes, "、"),
		})
	}
	return s.save()
}

// CloseItem 关闭事项。
//
// 事项必须存在、未关闭，且当前所在班次尚未结束；
// 已结束班次的事项关闭状态冻结，不能再关闭或重新打开。
func (s *Store) CloseItem(itemID int) error {
	it, err := s.GetItem(itemID)
	if err != nil {
		return err
	}
	if it.Closed {
		return &StateError{Msg: fmt.Sprintf("事项 #%d 已经关闭，不能重复关闭", itemID)}
	}
	cur, err := s.GetShift(it.CurrentShiftID)
	if err != nil {
		return err
	}
	if cur.Ended {
		return &StateError{Msg: fmt.Sprintf(
			"事项 #%d 当前所在班次 #%d 已结束，关闭状态不能修改", itemID, cur.ID)}
	}
	it.Closed = true
	it.History = append(it.History, Event{Time: now(), Type: "关闭"})
	return s.save()
}
