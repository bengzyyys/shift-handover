package handover

import "errors"

// 领域错误。命令行与调用方可以用 errors.Is 判别。
var (
	ErrNotFound         = errors.New("记录不存在")
	ErrShiftClosed      = errors.New("班次已结束，不允许该操作")
	ErrShiftNotClosed   = errors.New("班次尚未结束")
	ErrInvalidInput     = errors.New("输入不合法")
	ErrOverlap          = errors.New("班次时间区间重叠")
	ErrHandoverExists   = errors.New("该交班班次已发起交接，重复发起返回已有记录")
	ErrHandoverTarget   = errors.New("不能改换接班对象")
	ErrHandoverState    = errors.New("当前交接状态不允许该操作")
	ErrPositionMismatch = errors.New("岗位不一致")
	ErrSameShift        = errors.New("不能交给自身班次")
)
