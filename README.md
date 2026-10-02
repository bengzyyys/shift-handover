# 本地班次交接与未关闭事项

在本机运行的本地班次交接与未关闭事项管理工具。数据持久化在本地 JSON 文件中，
**退出后重新打开，数据与处理进度仍在**；写入采用临时文件 + 原子改名，业务校验失败或
写盘失败都会回滚，失败前已保存的数据保持不变。

## 构建与测试

```bash
go build ./...
go test ./...
go run ./cmd/handover help
```

## 数据文件

默认使用当前目录 `handover-data.json`，可用全局参数 `--data /路径/文件.json` 或环境变量
`HANDOVER_DATA` 指定。时间一律使用带时区偏移的 RFC3339，例如
`2026-10-02T08:00:00+08:00`。

## 命令一览

```bash
# 班次
handover shift-add   --position 岗位 --owner 负责人 --start 开始 --end 结束 [--note 重叠说明]
handover shift-list
handover shift-close --id S001
handover shift-show  --id S001      # 按班次查询完整报告

# 重叠说明（重叠建立班次时必填说明，也可事后追加）
handover note-add  --a S001 --b S003 --note "抢修并行一小时"
handover note-list --id S001

# 事项
handover item-add    --shift S001 --content 内容 --severity normal|important|urgent \
                     [--constraints 限制条件] --follow 后续负责人
handover item-update --id I001 --content 内容 --severity normal|important|urgent \
                     [--constraints 限制条件] --follow 后续负责人
handover item-close  --id I001 --operator 操作人
handover item-show   --id I001

# 交接
handover handover-create   --from S001 --to S002
handover handover-list
handover handover-show     --id H001
handover handover-process  --id H001 --item I001 --action confirm|return|track \
                           --operator 操作人 [--reason 退回原因] \
                           [--note 跟踪说明 --follow 后续负责人]
handover handover-resubmit --id H001 --item I001 --operator 操作人 --supplement 补充说明
```

严重程度与操作同时接受英文标识和中文（普通/重要/紧急，确认接收/退回/继续跟踪）。

## 业务规则

- **班次**：岗位、负责人、带时区起止时间必填，结束必须晚于开始；编号稳定（S001…）。
  同一岗位按实际时刻比较区间，前班结束恰好等于后班开始不算重叠；重叠时必须填写说明，
  说明与涉及的班次可查询。不同岗位互不影响。
- **事项**：仅未结束班次可新增；内容、严重程度（普通/重要/紧急）、后续负责人必填，
  限制条件可空；编号稳定（I001…），结束前可修改、关闭。班次结束后，未关闭事项成为
  待交接清单，已关闭事项保留；已结束班次的事项内容与当时关闭状态不可改、不可删。
  空清单也能结束班次。
- **发起交接**：交给同岗位、尚未结束且开始时间不早于交班开始的班次；不能交给自身或
  其他岗位；区间重叠但已有说明时允许。一个交班班次只能指定一个接班对象——重复发起
  返回已有记录且不复制事项，改换对象报错。
- **逐项处理**：确认接收 / 退回（必填原因）/ 继续跟踪（视为已接收，必填跟踪说明与
  后续负责人），每次处理必填操作人。未处理与退回项使交接保持未完成，全部确认或继续
  跟踪才完成；空清单直接完成。已接收项不能再次退回。
- **退回与重新提交**：交班人可对退回项追加非空补充说明并在同一交接记录上重新提交，
  仅该项恢复待处理，既有接收结果、原文、退回原因均不覆盖；历史逐轮保留。
- **流转**：确认与继续跟踪的事项进入接班班次未关闭清单，可在该班结束前关闭，否则随
  该班再次交接，始终保留原事项编号与此前历史。接班班次仍有未处理或退回交接项时不能
  结束。
- **查询**：`shift-show` 展示完整事项、关闭情况、接班对象、每项当前结果，以及操作人、
  处理时间和历次退回与补充说明。不存在的编号、缺少必填信息、不允许的状态操作都返回
  明确错误。

## 代码结构

```
handover/            领域模型、业务规则、JSON 原子持久化与展示
  model.go           班次/事项/交接/重叠说明等结构
  errors.go          领域错误
  store.go           本地 JSON 存储（快照回滚 + 原子写盘）
  service.go         全部业务规则
  render.go          中文命令行展示
cmd/handover/        命令行入口
```
