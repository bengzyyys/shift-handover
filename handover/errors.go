package handover

import "fmt"

// NotFoundError 表示查询的对象不存在。
type NotFoundError struct {
	What string // 对象名称，如“班次”“事项”“交接记录”
	ID   int
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s编号 %d 不存在", e.What, e.ID)
}

// ValidationError 表示缺少必填信息或参数不合法。
type ValidationError struct {
	Msg string
}

func (e *ValidationError) Error() string { return e.Msg }

// StateError 表示当前状态不允许该操作。
type StateError struct {
	Msg string
}

func (e *StateError) Error() string { return e.Msg }
