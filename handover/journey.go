package handover

import (
	"fmt"
	"sort"
)

// 本文件只承载 item-show 查询结果（ItemJourney）的组装。组装拆成四个相互
// 独立的阶段，各自只依赖已保存数据，互不穿插：
//
//   - 各次交接当前结果的整理：item-show 与 shift-show 共用同一份汇总规则
//     （见 entry_results.go）：本处只负责选出该事项参与的全部交接
//     （itemHandovers，按交接编号排列），逐条视图的组装（交接编号、交班与
//     接班班次、单项深拷贝及排列次序）统一调用 entryViewsForItem，与处理
//     经过的生成无关，也不再在两处分别维护；
//   - 退回轮次与交接经过的展开：expandHandoverJourney 把一次交接中该事项的
//     经过展开为事件序列（发起、逐轮退回、补充后重新提交、确认接收或继续
//     跟踪），不关心其他交接，也不关心事项自身历史；
//   - 接收记录的合并：receivedMerge（见 received_merge.go）判定事项自身
//     received 事件与交接清单接收是否同一次，只读事项历史与接收凭据；
//   - 经过的排列：journeyAssembler 只负责排序键与最终次序，不关心事件来源。
//
// 四个阶段可以各自调整而不牵动其他部分；命令、数据格式与展示行为不变。
// 整个查询只读，不改变事项、交接进度或班次结束时记录。

// ItemJourney 凭事项编号汇总它从建立到当前的处理经过：保留事项自身的
// 建立、修改、关闭记录，并加入它参与的各次交接（发起交接、逐轮退回、补充后
// 重新提交、确认接收或继续跟踪），无须先知道涉及哪些交接编号。
// 当前保存的处理结果是退回、却没有任何退回轮次记录时（旧数据缺失），仍在时间线
// 中呈现这次已发生的退回，处理人与处理时间各自使用该项当前保存的信息（缺失分别
// 标为未记录），并标明退回历史不完整、原因未记录；不编造轮次序号、原因、补充或
// 重新提交经过，也不拿交接发起时间补齐处理时间。待处理、确认接收、继续跟踪、
// 结果缺失或无法识别的记录不据残留的处理人与时间认定发生退回。
// 经过按实际发生时刻排序（带不同时区的时间按同一实际时刻比较）；同一时刻下
// 同一交接内保持发起、该轮退回、该轮重新提交、后续处理的先后，不同交接按
// 交接编号排列。同一次接收若已出现在事项历史里只展示一次：除操作人与实际
// 时刻相同外，还须接收说明明确记载的交接编号、交班、接班班次与接收方式都与
// 交接清单一致；任一记载不一致，或说明缺少明确记载（旧数据、没有对应交接
// 清单的接收历史）时，事项历史原样保留，不凭空补交接编号。只读查询，
// 不改变事项、交接进度或班次结束时记录。
// 返回的整份结果是查询当时的独立副本，与班次报告同一约定：事项最新信息
// （含流经班次、历史事件与关闭时间）、处理经过（含各轮退回原因、补充说明与
// 补充时间）和各次交接当前结果（含处理时间与历次退回/补充记录）全部深拷贝，
// 处理经过与交接当前结果中同一次补充的时间也各是一份副本。调用方改动结果
// 不影响系统保存的数据与其他已取得的结果，系统随后的正常处理（补充后重新
// 提交、接收、修改、关闭等）也不会改写这份结果；再次查询才反映最新内容。
func (svc *Service) ItemJourney(itemID string) (ItemJourney, error) {
	itemID = clean(itemID)
	d := &svc.store.data
	it, _ := findItem(d, itemID)
	if it == nil {
		return ItemJourney{}, fmt.Errorf("%w：事项 %s", ErrNotFound, itemID)
	}
	hs := itemHandovers(d, itemID)
	j := ItemJourney{
		Item:         cloneItem(*it),
		HasHandovers: len(hs) > 0,
		Results:      entryViewsForItem(hs, itemID),
		Events:       assembleJourneyEvents(it, hs, itemID),
	}
	return j, nil
}

// itemHandovers 返回清单中包含该事项的全部交接，按交接编号排列。
// 返回的是指向存储记录的只读视图，排序只作用于指针切片，不改变存储顺序。
// 该事项后来转到后面的班次或已关闭，不影响把它参与过的交接全部列出——
// “只留最近一条”由这里的选取范围明确排除。
func itemHandovers(d *Data, itemID string) []*Handover {
	hs := []*Handover{}
	for i := range d.Handovers {
		if e, _ := findEntry(&d.Handovers[i], itemID); e != nil {
			hs = append(hs, &d.Handovers[i])
		}
	}
	sort.Slice(hs, func(i, k int) bool { return hs[i].ID < hs[k].ID })
	return hs
}

// assembleJourneyEvents 汇总完整处理经过：先逐次交接展开经过并合并接收记录，
// 再补入事项自身历史中未被合并的事件，最后统一排列。
func assembleJourneyEvents(it *Item, hs []*Handover, itemID string) []JourneyEvent {
	asm := &journeyAssembler{}

	// 接收记录的合并判定集中在 receivedMerge（见 received_merge.go）：逐次
	// 交接展开时把清单中的接收凭据交给它标记同一次接收的事项 received 事件；
	// 未标记的 received 事件（缺交接编号或交班班次的旧说明、指向其他交接/
	// 班次/方式的说明、自由文字或残缺说明等）随后原样保留。
	merge := newReceivedMerge(it.Events)

	for _, h := range hs {
		e, _ := findEntry(h, itemID)
		asm.addAll(expandHandoverJourney(h, e), h.ID)
		if e.Status.Received() {
			// 标记与本次接收同属一次的事项 received 事件；合并依据全部在
			// sameReceivedEvent 中：操作人与实际时刻相同，且说明完整记载
			// 交接编号、交班、接班班次与接收方式并逐项一致。缺归属信息的
			// 旧说明、残缺或自由文字说明一律不合并，原样作为独立接收历史。
			merge.consider(receiptFromEntry(h, e))
		}
	}

	// 事项自身历史：建立、修改、关闭原样保留；未被合并掉的 received 事件
	// （旧数据、指向其他归属的说明、自由文字等）也原样保留。
	for i, iev := range it.Events {
		if iev.Kind == "received" && merge.isUsed(i) {
			continue
		}
		asm.add(JourneyEvent{
			At: iev.At, TimeKnown: !iev.At.IsZero(),
			Kind: iev.Kind, Operator: iev.Operator, Detail: iev.Detail,
		}, "")
	}

	return asm.sorted()
}

// expandHandoverJourney 把一次交接中该事项的经过展开为事件序列，按交接内部
// 的先后返回：发起交接、逐轮退回与该轮重新提交、（旧数据）无轮次记录的退回、
// 确认接收或继续跟踪。展开只看这一份交接记录与该事项的清单项，不读取事项
// 自身历史，也不受其他交接影响。
func expandHandoverJourney(h *Handover, e *HandoverEntry) []JourneyEvent {
	var out []JourneyEvent

	// 发起交接：交接记录本身不记操作人，明确显示未记录，不以班次负责人代替。
	out = append(out, JourneyEvent{
		At: h.CreatedAt, TimeKnown: !h.CreatedAt.IsZero(),
		Kind: "handover-init", HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
	})

	// 逐轮退回与补充后重新提交：每轮原因与补充说明对应原来的退回，
	// 前一轮的补充不会变成后一轮的说明。
	for _, r := range e.Rounds {
		out = append(out, JourneyEvent{
			At: r.ReturnedAt, TimeKnown: !r.ReturnedAt.IsZero(),
			Kind: "return", Operator: r.ReturnOperator,
			HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
			RoundSeq: r.Seq, Reason: r.Reason,
		})
		if r.ResubmittedAt != nil {
			out = append(out, JourneyEvent{
				At: *r.ResubmittedAt, TimeKnown: !r.ResubmittedAt.IsZero(),
				Kind: "resubmit", Operator: r.SupplementOperator,
				HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
				RoundSeq: r.Seq, Supplement: r.Supplement,
				// 补充时间复制一份：处理经过与交接当前结果中同一次补充
				// 各属各的副本，改动其中一处不影响另一处与存储记录。
				SupplementOperator: r.SupplementOperator, SupplementAt: cloneTimePtr(r.SupplementAt),
			})
		}
	}

	if e.Status == EntryReturned && len(e.Rounds) == 0 {
		// 当前保存的结果是退回，却没有任何退回轮次记录（旧数据缺失）：
		// 退回确实发生过，处理经过必须呈现这一事实，不能像没发生过退回一样
		// 只列发起交接；但原因、轮次序号、补充与重新提交经过都没有可靠记录，
		// 只能使用该项当前保存的处理人与处理时间，并标明退回历史不完整。
		// 不编造第几次退回或退回原因，也不拿交接发起时间补齐处理时间。
		ev := JourneyEvent{
			Kind: "return-incomplete", Operator: e.Operator,
			HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
			HistoryIncomplete: true,
		}
		if validProcessedAt(e.ProcessedAt) {
			ev.At, ev.TimeKnown = *e.ProcessedAt, true
		}
		out = append(out, ev)
	}

	if e.Status.Received() {
		rcpt := receiptFromEntry(h, e)
		ev := JourneyEvent{
			Kind: rcpt.kind, Operator: e.Operator,
			HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
			TrackingNote: e.TrackingNote,
			// 继续跟踪当时指定的后续负责人取自交接记录快照，
			// 之后修改事项负责人不改变这里的历史值。
			FollowOwner: e.FollowOwner,
		}
		// 处理时间缺失或为旧数据零值时仍保留该处理事件，
		// 时间标为未记录，排在有真实时间的事件之后；这种凭据 timeKnown
		// 为 false，consider 不会合并任何事项事件（也不拿发起时间代替）。
		if rcpt.timeKnown {
			ev.At, ev.TimeKnown = rcpt.at, true
		}
		out = append(out, ev)
	}

	return out
}

// journeyEvent 是处理经过的候选项：事件本身加上排列用的排序键。
type journeyEvent struct {
	ev    JourneyEvent
	group string // "" 表示事项自身事件，否则为交接编号
	ord   int    // 同一（时刻、分组）内的先后
}

// journeyAssembler 累积处理经过候选项并给出最终排列。它只负责排序键与
// 次序：先按实际发生时刻（未记录时间的排最后），同一时刻下事项自身
// 事件在前，其后按交接编号分组，组内保持生成顺序（发起、逐轮退回、
// 该轮重新提交、后续处理）。事件的来源与内容由调用方决定。
type journeyAssembler struct {
	events []journeyEvent
}

func (a *journeyAssembler) add(ev JourneyEvent, group string) {
	a.events = append(a.events, journeyEvent{ev: ev, group: group, ord: len(a.events)})
}

func (a *journeyAssembler) addAll(evs []JourneyEvent, group string) {
	for _, ev := range evs {
		a.add(ev, group)
	}
}

// sorted 返回排列后的处理经过，不改动候选项本身。
func (a *journeyAssembler) sorted() []JourneyEvent {
	sort.SliceStable(a.events, func(i, k int) bool {
		x, y := a.events[i], a.events[k]
		if x.ev.TimeKnown != y.ev.TimeKnown {
			return x.ev.TimeKnown
		}
		if x.ev.TimeKnown && !x.ev.At.Equal(y.ev.At) {
			return x.ev.At.Before(y.ev.At)
		}
		if x.group != y.group {
			return x.group < y.group
		}
		return x.ord < y.ord
	})
	out := make([]JourneyEvent, len(a.events))
	for i, ke := range a.events {
		out[i] = ke.ev
	}
	return out
}
