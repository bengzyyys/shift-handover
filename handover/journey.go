package handover

import "sort"

// 本文件只承载 item-show 查询结果（ItemJourney）的组装，即“这份查询结果”
// 范围内的只读逻辑。组装被拆成顺序固定、互不穿插的五个阶段，使历史展示的
// 各部分各自只有一个改动点：
//
//	阶段一  当前结果：逐次交接克隆该事项的当前处理结果（EntryView），
//	        退回未补充/已重新提交等待处理等展示全部只取决于这份保存结果；
//	阶段二  轮次展开：把每次交接的发起、逐轮退回、补充后重新提交、
//	        缺轮次的退回旧数据与最终接收展开为时间线事件，每轮的原因与
//	        补充只取自该轮自己的记录，不会串到其他轮次；
//	阶段三  接收合并：标记与交接清单接收同属一次的事项自身 received 事件
//	        （判定全部在 received_merge.go），被标记的事件在阶段四跳过；
//	阶段四  自身经过：建立、修改、关闭与未被合并的接收历史原样展开；
//	阶段五  排列：按实际发生时刻（不同时区按同一实际时刻）稳定排列，
//	        同一时刻同一交接保持生成顺序，不同交接按编号排列。
//
// 组装全程只读：不写存储、不移动事项、不改交接完成情况。返回结果中的事项、
// 事件与各项当前结果都是独立深拷贝（克隆发生在阶段一与入口处），改动这份
// 结果不影响保存数据，之后的正常处理也不会改写已取得的结果。
//
// 改变其中任何一种展示（如轮次如何展开、接收如何合并、经过如何排列）只需
// 调整对应阶段，不必在一个大循环里同时兼顾其他阶段。

// journeyTimelineEntry 是排列前的一条经过：ev 是事件本身，group 是它所属的
// 交接编号（事项自身事件为 ""），order 是生成序号，只在“同一实际时刻、同一
// 分组”内决定先后，保证同一交接保持发起、各轮退回与重新提交、后续接收的顺序。
type journeyTimelineEntry struct {
	ev    JourneyEvent
	group string
	order int
}

// buildItemJourney 组装一份凭事项编号查询的完整结果：it 是系统当前保存的事项，
// hs 是系统当前保存的全部交接。调用方（Service.ItemJourney）负责只读快照与
// 编号查找；本函数不碰存储，只做克隆与组装，同一份输入始终得到相同的结果。
func buildItemJourney(it Item, hs []Handover) ItemJourney {
	j := ItemJourney{Item: cloneItem(it)}

	merge := newReceivedMerge(it.Events)
	var timeline []journeyTimelineEntry

	// 阶段一+二+三（交接部分）：交接按编号排列；每一次交接先汇总当前结果，
	// 再展开它自己的经过并登记本次接收的合并标记。不同交接的展开互不影响。
	for _, h := range sortedHandoversByID(hs) {
		e, _ := findEntry(&h, it.ID)
		if e == nil {
			continue
		}
		j.HasHandovers = true
		j.Results = append(j.Results, EntryView{
			HandoverID: h.ID,
			FromShift:  h.FromShiftID,
			ToShift:    h.ToShiftID,
			Entry:      cloneEntry(*e),
		})
		timeline = append(timeline, handoverJourneyEvents(&h, e, merge)...)
	}

	// 阶段四：事项自身的建立、修改、关闭，以及未被合并掉的接收历史原样展开。
	timeline = append(timeline, itemOwnJourneyEvents(it, merge)...)

	// 阶段五：按实际发生时刻稳定排列。
	j.Events = orderJourneyEvents(timeline)
	return j
}

// sortedHandoversByID 返回按交接编号排列的交接副本（浅拷贝切片，只读遍历用）。
// 不同交接在同一实际时刻的先后由编号决定。
func sortedHandoversByID(hs []Handover) []Handover {
	out := append([]Handover(nil), hs...)
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// handoverJourneyEvents 展开一次交接在处理经过中的事件，顺序即交接内的实际
// 发生顺序：发起、各轮退回（每轮后跟该轮的重新提交）、旧数据缺轮次的退回，
// 以及最终的确认接收或继续跟踪。同一次交接的所有事件使用同一 group（交接
// 编号），因此在同一实际时刻仍保持这里的生成顺序。
//
// 每轮退回的原因、该轮随后的补充说明都只取自该轮记录：前一轮的补充不会成为
// 后一轮的说明。merge 只为本次最终接收登记与事项自身 received 事件的合并
// 标记；处理时间未记录的接收不产生可合并凭据，也不拿发起时间代替。
func handoverJourneyEvents(h *Handover, e *HandoverEntry, merge *receivedMerge) []journeyTimelineEntry {
	group := h.ID
	var out []journeyTimelineEntry
	add := func(ev JourneyEvent) {
		out = append(out, journeyTimelineEntry{ev: ev, group: group, order: len(out)})
	}

	// 发起交接：交接记录本身不记操作人，明确显示未记录，不以班次负责人代替。
	add(JourneyEvent{
		At: h.CreatedAt, TimeKnown: !h.CreatedAt.IsZero(),
		Kind: "handover-init", HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
	})

	for _, r := range e.Rounds {
		add(JourneyEvent{
			At: r.ReturnedAt, TimeKnown: !r.ReturnedAt.IsZero(),
			Kind: "return", Operator: r.ReturnOperator,
			HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
			RoundSeq: r.Seq, Reason: r.Reason,
		})
		if r.ResubmittedAt != nil {
			add(JourneyEvent{
				At: *r.ResubmittedAt, TimeKnown: !r.ResubmittedAt.IsZero(),
				Kind: "resubmit", Operator: r.SupplementOperator,
				HandoverID: h.ID, FromShift: h.FromShiftID, ToShift: h.ToShiftID,
				RoundSeq: r.Seq, Supplement: r.Supplement,
				// 补充时间复制一份：处理经过与交接当前结果中同一次补充
				// 各是一份副本，改动其中一处不影响另一处与存储记录。
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
		add(ev)
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
		// 处理时间缺失或为旧数据零值时仍保留该处理事件，时间标为未记录，
		// 排在有真实时间的事件之后；这种凭据 timeKnown 为 false，consider
		// 不会合并任何事项事件（也不拿发起时间代替）。
		if rcpt.timeKnown {
			ev.At, ev.TimeKnown = rcpt.at, true
		}
		add(ev)
		// 标记与本次接收同属一次的事项 received 事件；合并依据全部在
		// sameReceivedEvent 中：操作人与实际时刻相同，且说明完整记载
		// 交接编号、交班、接班班次与接收方式并逐项一致。缺归属信息的
		// 旧说明、残缺或自由文字说明一律不合并，原样作为独立接收历史。
		merge.consider(rcpt)
	}
	return out
}

// itemOwnJourneyEvents 展开事项自身保存的历史：建立、修改、关闭原样保留；
// 已被阶段三标记为与某次交接接收同属一次的 received 事件跳过（只以信息更全
// 的交接事件展示一次），未被标记的 received 事件（归属不完整的旧说明、指向
// 其他交接/班次/方式的说明、自由文字等）按原文、原操作人与原时间独立保留，
// 不凭空补交接编号或交班/接班班次。
func itemOwnJourneyEvents(it Item, merge *receivedMerge) []journeyTimelineEntry {
	var out []journeyTimelineEntry
	for i, iev := range it.Events {
		if iev.Kind == "received" && merge.isUsed(i) {
			continue
		}
		ev := JourneyEvent{
			At: iev.At, TimeKnown: !iev.At.IsZero(),
			Kind: iev.Kind, Operator: iev.Operator, Detail: iev.Detail,
		}
		// 事项自身事件统一属于 group ""：同一实际时刻下排在所有交接事件之前；
		// 它们彼此之间按保存顺序排列。
		out = append(out, journeyTimelineEntry{ev: ev, group: "", order: len(out)})
	}
	return out
}

// orderJourneyEvents 按约定排列展开后的经过：
//   - 有真实时间的事件在前、缺时间的在后（零值时间也算未记录）；
//   - 都有时间时按实际发生时刻排列，time.Time.Equal 按同一实际时刻比较，
//     不同时区写法表示同一时刻仍视为同一时刻；
//   - 同一实际时刻下，事项自身事件在前，其后按交接编号分组，不同交接按
//     编号排列，同一交接内按生成顺序（发起、各轮退回与重新提交、后续接收）。
//
// 使用稳定排序，未被比较器区分先后的事件保持生成顺序。
func orderJourneyEvents(entries []journeyTimelineEntry) []JourneyEvent {
	sorted := append([]journeyTimelineEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, k int) bool {
		a, b := sorted[i], sorted[k]
		if a.ev.TimeKnown != b.ev.TimeKnown {
			return a.ev.TimeKnown
		}
		if a.ev.TimeKnown && !a.ev.At.Equal(b.ev.At) {
			return a.ev.At.Before(b.ev.At)
		}
		if a.group != b.group {
			return a.group < b.group
		}
		return a.order < b.order
	})
	out := make([]JourneyEvent, len(sorted))
	for i, ke := range sorted {
		out[i] = ke.ev
	}
	return out
}
