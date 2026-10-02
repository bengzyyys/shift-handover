# 本地班次交接与未关闭事项

这是一个在本机运行的班次交接与未关闭事项管理工具，数据保存在本地 JSON 文件中，退出后重新打开数据和处理进度仍然保留。

## 构建

```bash
go build -o shift-handover ./cmd/shift-handover
```

也可以直接 `go run ./cmd/shift-handover`。

## 数据文件位置

默认保存在用户主目录下的 `.shift-handover/data.json`，可通过环境变量或参数覆盖：

```bash
export SHIFTHANDOVER_DATA=/path/to/data.json
# 或
shift-handover --data /path/to/data.json shift list
```

## 时间格式

所有时间必须带时区，支持以下格式：

- `2026-10-02 08:00 +08:00`
- `2026-10-02 08:00:05 +08:00`
- `2026-10-02T08:00:00+08:00`（RFC3339）

## 命令

### 班次

```bash
# 建立班次（岗位、负责人必填；结束时间必须晚于开始时间）
shift-handover shift add --post 值班 --owner 张三 \
  --start "2026-10-02 08:00 +08:00" --end "2026-10-02 16:00 +08:00"

# 与同岗位已有班次重叠时，必须填写重叠说明
shift-handover shift add --post 值班 --owner 李四 \
  --start "2026-10-02 15:00 +08:00" --end "2026-10-02 23:00 +08:00" \
  --note "与班次 #1 重叠一小时，已当面确认"

shift-handover shift list
shift-handover shift show <班次编号>
shift-handover shift end <班次编号>
```

规则：

- 同一岗位按实际时刻比较区间：前班结束恰好等于后班开始不算重叠；存在重叠时必须填写说明，保存后可查看说明和涉及的班次。
- 不同岗位互不影响。
- 接班班次仍有未处理或退回的交接项时不能结束。
- 空清单也可以结束班次；结束后事项内容及关闭状态冻结，不能修改或删除。

### 事项

```bash
# 新增事项（内容、后续负责人必填；严重程度可选 普通/重要/紧急，限制条件可空）
shift-handover item add --shift 1 --content "巡查机房设备" \
  --severity 重要 --constraints "需双人同行" --follow-owner 李四

shift-handover item edit <事项编号> [--content "..."] [--severity ...] \
  [--constraints "..."] [--follow-owner "..."]
shift-handover item close <事项编号>
shift-handover item show <事项编号>
```

规则：

- 每项有稳定编号；已结束班次的事项不能修改或关闭。
- 已接收/继续跟踪的事项进入接班班次的未关闭清单，可在该班次结束前关闭；否则随该班次再次交接，保留原编号和历史。

### 交接

```bash
# 发起交接：交班班次必须已结束；接班班次必须同岗位、未结束、
# 开始时间不早于交班班次开始时间；重叠时必须已有重叠说明；不能交给自身
shift-handover handover create --from 1 --to 2

# 重复发起返回已有记录（不复制事项）；改换对象报错

# 接班人逐项处理（每次处理须填写操作人）
shift-handover handover receive  --handover 1 --item 1 --operator 李四
shift-handover handover return   --handover 1 --item 3 --operator 李四 --reason "告警已处理完毕"
shift-handover handover track    --handover 1 --item 3 --operator 李四 \
  --note "继续跟进" --follow-owner 王五

# 退回后交班人追加非空说明，在同一交接记录上重新提交该项（只有该项恢复待处理）
shift-handover handover resubmit --handover 1 --item 3 --operator 张三 \
  --note "已核实，请继续跟进"

shift-handover handover list
shift-handover handover show <交接记录编号>
```

规则：

- 未处理项和退回项使交接保持未完成；全部事项确认接收或继续跟踪后才完成；空清单直接完成。
- 已接收项不能再次退回。
- 继续跟踪表示已接收，须填写跟踪说明和后续负责人。
- 退回必须填写原因；重新提交不覆盖原文和退回原因，历次补充说明保留。
- 不存在的编号、缺少必填信息或不允许的状态操作都会给出明确错误；失败前已保存的数据保持不变。

## 测试

```bash
go test ./...
```
