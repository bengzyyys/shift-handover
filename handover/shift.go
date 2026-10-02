package handover

import (
	"fmt"
	"time"
)

// AddShift 建立一个班次。
//
// 岗位、负责人必填；结束时间必须晚于开始时间；时间必须带时区。
// 同一岗位按实际时刻比较区间：与已有班次重叠时必须填写 overlapNote（重叠说明），
// 保存后可在班次信息中查看说明及涉及的班次。不同岗位互不影响。
// 返回新建的班次。
func (s *Store) AddShift(post, owner string, start, end time.Time, overlapNote string) (*Shift, error) {
	post = trim(post)
	owner = trim(owner)
	if post == "" {
		return nil, &ValidationError{Msg: "岗位不能为空"}
	}
	if owner == "" {
		return nil, &ValidationError{Msg: "负责人不能为空"}
	}
	if !end.After(start) {
		return nil, &ValidationError{Msg: fmt.Sprintf("结束时间 %s 必须晚于开始时间 %s",
			end.Format("2006-01-02 15:04 -07:00"), start.Format("2006-01-02 15:04 -07:00"))}
	}

	note := trim(overlapNote)
	var overlapIDs []int
	for _, sh := range s.data.Shifts {
		if sh.Post != post {
			continue
		}
		if overlaps(sh, &Shift{Start: start, End: end}) {
			overlapIDs = append(overlapIDs, sh.ID)
		}
	}
	if len(overlapIDs) > 0 && note == "" {
		return nil, &ValidationError{Msg: fmt.Sprintf(
			"与同岗位已有班次存在时间重叠（涉及班次编号 %v），必须填写重叠说明", overlapIDs)}
	}

	sh := &Shift{
		ID:          s.data.NextShiftID,
		Post:        post,
		Owner:       owner,
		Start:       start,
		End:         end,
		OverlapNote: note,
		OverlapIDs:  overlapIDs,
		CreatedAt:   now(),
	}
	s.data.NextShiftID++
	s.data.Shifts = append(s.data.Shifts, sh)
	if err := s.save(); err != nil {
		return nil, err
	}
	return sh, nil
}

// EndShift 结束一个班次。
//
// 已结束班次的事项内容及关闭状态冻结，不能再修改或删除；未关闭事项成为待交接清单。
// 若该班次作为接班班次仍有未处理或退回的交接项，则不允许结束。
// 空清单也可以结束班次。
func (s *Store) EndShift(id int) error {
	sh, err := s.GetShift(id)
	if err != nil {
		return err
	}
	if sh.Ended {
		return &StateError{Msg: fmt.Sprintf("班次 #%d 已经结束，不能重复结束", id)}
	}
	if open := s.OpenHandoversToShift(id); len(open) > 0 {
		h := open[0]
		return &StateError{Msg: fmt.Sprintf(
			"接班班次仍有未处理或退回的交接项（交接记录 #%d，来自班次 #%d），不能结束",
			h.ID, h.FromShiftID)}
	}
	sh.Ended = true
	return s.save()
}
