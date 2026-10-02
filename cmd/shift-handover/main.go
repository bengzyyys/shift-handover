package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bengzyyys/shift-handover/handover"
)

// 时间解析支持带时区的格式：
//   - RFC3339：2006-01-02T15:04:00+08:00
//   - 2006-01-02 15:04 -07:00
//   - 2006-01-02 15:04:05 -07:00
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("时间不能为空")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04 -07:00",
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04 MST",
	}
	var firstErr error
	for _, l := range layouts {
		t, err := time.Parse(l, s)
		if err == nil {
			return t, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q（请带时区，例如 2026-10-02 08:00 +08:00 或 2026-10-02T08:00:00+08:00）", s)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04 -07:00")
}

func openStore(dataPath string) (*handover.Store, error) {
	return handover.Open(dataPath)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}

func requireID(name string, args []string) int {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "错误: 缺少%s编号\n", name)
		os.Exit(1)
	}
	id, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || id <= 0 {
		fmt.Fprintf(os.Stderr, "错误: %s编号必须是正整数，得到 %q\n", name, args[0])
		os.Exit(1)
	}
	return id
}

func main() {
	dataPath := handover.DefaultPath()
	flag.StringVar(&dataPath, "data", dataPath, "数据文件路径（也可用环境变量 SHIFTHANDOVER_DATA）")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}

	store, err := openStore(dataPath)
	if err != nil {
		fail(err)
	}

	switch args[0] {
	case "shift":
		cmdShift(store, args[1:])
	case "item":
		cmdItem(store, args[1:])
	case "handover":
		cmdHandover(store, args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知命令 %q\n", args[0])
		usage()
	}
}

func usage() {
	fmt.Print(`本地班次交接工具

用法:
  shift-handover [--data 数据文件] <命令> [参数]

班次命令:
  shift add   --post 岗位 --owner 负责人 --start "时间" --end "时间" [--note 重叠说明]
  shift list
  shift show  <班次编号>
  shift end   <班次编号>

事项命令:
  item add    --shift 班次编号 --content "内容" [--severity 普通|重要|紧急] [--constraints "限制条件"] --follow-owner "后续负责人"
  item edit   <事项编号> [--content "内容"] [--severity 普通|重要|紧急] [--constraints "限制条件"] [--follow-owner "负责人"]
  item close  <事项编号>
  item show   <事项编号>

交接命令:
  handover create    --from 交班班次编号 --to 接班班次编号
  handover list
  handover show      <交接记录编号>
  handover receive   --handover 编号 --item 事项编号 --operator "操作人"
  handover return    --handover 编号 --item 事项编号 --operator "操作人" --reason "退回原因"
  handover track     --handover 编号 --item 事项编号 --operator "操作人" --note "跟踪说明" --follow-owner "后续负责人"
  handover resubmit  --handover 编号 --item 事项编号 --operator "交班人" --note "补充说明"

时间格式: 2026-10-02 08:00 +08:00 或 2026-10-02T08:00:00+08:00（必须带时区）
`)
	os.Exit(0)
}

// ---------- 班次 ----------

func cmdShift(s *handover.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: shift 需要子命令 add/list/show/end")
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("shift add", flag.ExitOnError)
		post := fs.String("post", "", "岗位")
		owner := fs.String("owner", "", "负责人")
		startS := fs.String("start", "", "开始时间（带时区）")
		endS := fs.String("end", "", "结束时间（带时区）")
		note := fs.String("note", "", "与同岗位班次重叠时的说明")
		_ = fs.Parse(args[1:])
		start, err := parseTime(*startS)
		if err != nil {
			fail(err)
		}
		end, err := parseTime(*endS)
		if err != nil {
			fail(err)
		}
		sh, err := s.AddShift(*post, *owner, start, end, *note)
		if err != nil {
			fail(err)
		}
		fmt.Printf("已建立班次 #%d：%s 岗位，负责人 %s，%s ~ %s\n",
			sh.ID, sh.Post, sh.Owner, fmtTime(sh.Start), fmtTime(sh.End))
		if sh.OverlapNote != "" {
			fmt.Printf("重叠说明：%s（涉及班次 %v）\n", sh.OverlapNote, sh.OverlapIDs)
		}
	case "list":
		for _, sh := range s.ListShifts() {
			status := "进行中"
			if sh.Ended {
				status = "已结束"
			}
			fmt.Printf("班次 #%d  岗位:%s  负责人:%s  %s ~ %s  [%s]\n",
				sh.ID, sh.Post, sh.Owner, fmtTime(sh.Start), fmtTime(sh.End), status)
		}
	case "show":
		sh, err := s.GetShift(requireID("班次", args[1:]))
		if err != nil {
			fail(err)
		}
		printShift(s, sh)
	case "end":
		id := requireID("班次", args[1:])
		if err := s.EndShift(id); err != nil {
			fail(err)
		}
		fmt.Printf("班次 #%d 已结束\n", id)
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 shift 子命令 %q\n", args[0])
		os.Exit(1)
	}
}

func printShift(s *handover.Store, sh *handover.Shift) {
	status := "进行中"
	if sh.Ended {
		status = "已结束"
	}
	fmt.Printf("班次 #%d\n", sh.ID)
	fmt.Printf("  岗位: %s\n", sh.Post)
	fmt.Printf("  负责人: %s\n", sh.Owner)
	fmt.Printf("  开始: %s\n", fmtTime(sh.Start))
	fmt.Printf("  结束: %s\n", fmtTime(sh.End))
	fmt.Printf("  状态: %s\n", status)
	if sh.OverlapNote != "" {
		fmt.Printf("  重叠说明: %s\n", sh.OverlapNote)
		fmt.Printf("  涉及班次: %v\n", sh.OverlapIDs)
	}

	// 事项清单
	items := s.ShiftItems(sh.ID)
	fmt.Printf("  事项（%d 项）:\n", len(items))
	if len(items) == 0 {
		fmt.Println("    （空）")
	}
	for _, it := range items {
		closed := "未关闭"
		if it.Closed {
			closed = "已关闭"
		}
		fmt.Printf("    #%d [%s][%s] %s\n", it.ID, it.Severity, closed, it.Content)
		if it.Constraints != "" {
			fmt.Printf("        限制条件: %s\n", it.Constraints)
		}
		fmt.Printf("        后续负责人: %s\n", it.FollowOwner)
	}

	// 接班对象（本班次作为交班方）
	if h := s.HandoverByFromShift(sh.ID); h != nil {
		to, _ := s.GetShift(h.ToShiftID)
		toLabel := fmt.Sprintf("#%d", h.ToShiftID)
		if to != nil {
			toLabel = fmt.Sprintf("#%d（%s，负责人 %s）", h.ToShiftID, to.Post, to.Owner)
		}
		fmt.Printf("  接班对象: 班次 %s\n", toLabel)
		fmt.Printf("  交接记录 #%d: %s\n", h.ID, map[bool]string{true: "已完成", false: "未完成"}[h.Complete])
		for _, hi := range h.Items {
			it, _ := s.GetItem(hi.ItemID)
			content := ""
			if it != nil {
				content = it.Content
			}
			fmt.Printf("    事项 #%d [%s] %s\n", hi.ItemID, hi.Result, content)
			if hi.Operator != "" {
				fmt.Printf("        操作人: %s  处理时间: %s\n", hi.Operator, fmtTime(hi.HandledAt))
			}
			if hi.TrackNote != "" {
				fmt.Printf("        跟踪说明: %s\n", hi.TrackNote)
				fmt.Printf("        跟踪后续负责人: %s\n", hi.TrackOwner)
			}
			if hi.ReturnReason != "" {
				fmt.Printf("        退回原因（保留）: %s\n", hi.ReturnReason)
			}
			for _, n := range hi.Notes {
				fmt.Printf("        补充说明（%s %s）: %s\n", n.Operator, fmtTime(n.Time), n.Text)
			}
		}
	}

	// 本班次作为接班方的交接
	var incoming []*handover.Handover
	for _, h := range s.ListHandovers() {
		if h.ToShiftID == sh.ID {
			incoming = append(incoming, h)
		}
	}
	if len(incoming) > 0 {
		fmt.Printf("  作为接班方的交接:\n")
		for _, h := range incoming {
			from, _ := s.GetShift(h.FromShiftID)
			fromLabel := fmt.Sprintf("#%d", h.FromShiftID)
			if from != nil {
				fromLabel = fmt.Sprintf("#%d（%s，负责人 %s）", h.FromShiftID, from.Post, from.Owner)
			}
			fmt.Printf("    交接记录 #%d: 来自班次 %s，%s\n",
				h.ID, fromLabel, map[bool]string{true: "已完成", false: "未完成"}[h.Complete])
			for _, hi := range h.Items {
				it, _ := s.GetItem(hi.ItemID)
				content := ""
				if it != nil {
					content = it.Content
				}
				fmt.Printf("      事项 #%d [%s] %s\n", hi.ItemID, hi.Result, content)
				if hi.Operator != "" {
					fmt.Printf("          操作人: %s  处理时间: %s\n", hi.Operator, fmtTime(hi.HandledAt))
				}
				if hi.TrackNote != "" {
					fmt.Printf("          跟踪说明: %s\n", hi.TrackNote)
				fmt.Printf("          跟踪后续负责人: %s\n", hi.TrackOwner)
				}
				if hi.ReturnReason != "" {
					fmt.Printf("          退回原因（保留）: %s\n", hi.ReturnReason)
				}
				for _, n := range hi.Notes {
					fmt.Printf("          补充说明（%s %s）: %s\n", n.Operator, fmtTime(n.Time), n.Text)
				}
			}
		}
	}
}

// ---------- 事项 ----------

func cmdItem(s *handover.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: item 需要子命令 add/edit/close/show")
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("item add", flag.ExitOnError)
		shiftID := fs.Int("shift", 0, "班次编号")
		content := fs.String("content", "", "事项内容")
		severityS := fs.String("severity", "普通", "严重程度")
		constraints := fs.String("constraints", "", "限制条件（可空）")
		followOwner := fs.String("follow-owner", "", "后续负责人")
		_ = fs.Parse(args[1:])
		if *shiftID <= 0 {
			fail(&handover.ValidationError{Msg: "必须通过 --shift 指定班次编号"})
		}
		sev, err := handover.ParseSeverity(*severityS)
		if err != nil {
			fail(err)
		}
		it, err := s.AddItem(*shiftID, *content, sev, *constraints, *followOwner)
		if err != nil {
			fail(err)
		}
		fmt.Printf("已建立事项 #%d（班次 #%d）：[%s] %s\n", it.ID, it.CurrentShiftID, it.Severity, it.Content)
	case "edit":
		fs := flag.NewFlagSet("item edit", flag.ExitOnError)
		content := fs.String("content", "", "事项内容")
		severityS := fs.String("severity", "", "严重程度")
		constraints := fs.String("constraints", "", "限制条件")
		followOwner := fs.String("follow-owner", "", "后续负责人")
		_ = fs.Parse(args[1:])
		id := requireID("事项", fs.Args())
		edit := handover.ItemEdit{}
		// 仅当参数确实出现时才修改对应字段
		seen := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
		if seen["content"] {
			edit.Content = content
		}
		if seen["severity"] {
			sev, err := handover.ParseSeverity(*severityS)
			if err != nil {
				fail(err)
			}
			edit.Severity = &sev
		}
		if seen["constraints"] {
			edit.Constraints = constraints
		}
		if seen["follow-owner"] {
			edit.FollowOwner = followOwner
		}
		if err := s.EditItem(id, edit); err != nil {
			fail(err)
		}
		fmt.Printf("事项 #%d 已修改\n", id)
	case "close":
		id := requireID("事项", args[1:])
		if err := s.CloseItem(id); err != nil {
			fail(err)
		}
		fmt.Printf("事项 #%d 已关闭\n", id)
	case "show":
		it, err := s.GetItem(requireID("事项", args[1:]))
		if err != nil {
			fail(err)
		}
		printItem(s, it)
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 item 子命令 %q\n", args[0])
		os.Exit(1)
	}
}

func printItem(s *handover.Store, it *handover.Item) {
	closed := "未关闭"
	if it.Closed {
		closed = "已关闭"
	}
	fmt.Printf("事项 #%d [%s][%s]\n", it.ID, it.Severity, closed)
	fmt.Printf("  内容: %s\n", it.Content)
	fmt.Printf("  限制条件: %s\n", dashIfEmpty(it.Constraints))
	fmt.Printf("  后续负责人: %s\n", it.FollowOwner)
	fmt.Printf("  来源班次: #%d\n", it.OriginShiftID)
	fmt.Printf("  当前班次: #%d\n", it.CurrentShiftID)
	fmt.Println("  历史:")
	for _, e := range it.History {
		line := fmt.Sprintf("    %s %s", fmtTime(e.Time), e.Type)
		if e.Operator != "" {
			line += " 操作人:" + e.Operator
		}
		if e.Detail != "" {
			line += " 详情:" + e.Detail
		}
		fmt.Println(line)
	}
}

func dashIfEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

// ---------- 交接 ----------

func cmdHandover(s *handover.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: handover 需要子命令 create/list/show/receive/return/track/resubmit")
		os.Exit(1)
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("handover create", flag.ExitOnError)
		from := fs.Int("from", 0, "交班班次编号")
		to := fs.Int("to", 0, "接班班次编号")
		_ = fs.Parse(args[1:])
		if *from <= 0 || *to <= 0 {
			fail(&handover.ValidationError{Msg: "必须通过 --from 和 --to 指定交班、接班班次编号"})
		}
		h, already, err := s.CreateHandover(*from, *to)
		if err != nil {
			fail(err)
		}
		if already {
			fmt.Printf("交班班次 #%d 与接班班次 #%d 的交接记录已存在：交接记录 #%d（不复制事项）\n",
				h.FromShiftID, h.ToShiftID, h.ID)
		} else {
			fmt.Printf("已建立交接记录 #%d：班次 #%d → 班次 #%d，共 %d 项待交接\n",
				h.ID, h.FromShiftID, h.ToShiftID, len(h.Items))
		}
		fmt.Printf("当前状态: %s\n", map[bool]string{true: "已完成", false: "未完成"}[h.Complete])
	case "list":
		for _, h := range s.ListHandovers() {
			fmt.Printf("交接记录 #%d  班次 #%d → 班次 #%d  事项 %d 项  [%s]\n",
				h.ID, h.FromShiftID, h.ToShiftID, len(h.Items),
				map[bool]string{true: "已完成", false: "未完成"}[h.Complete])
		}
	case "show":
		h, err := s.GetHandover(requireID("交接记录", args[1:]))
		if err != nil {
			fail(err)
		}
		printHandover(s, h)
	case "receive":
		fs := flag.NewFlagSet("handover receive", flag.ExitOnError)
		hid := fs.Int("handover", 0, "交接记录编号")
		iid := fs.Int("item", 0, "事项编号")
		op := fs.String("operator", "", "操作人")
		_ = fs.Parse(args[1:])
		if err := s.HandoverReceive(*hid, *iid, *op); err != nil {
			fail(err)
		}
		fmt.Printf("交接记录 #%d 事项 #%d 已接收\n", *hid, *iid)
		printHandoverByID(s, *hid)
	case "return":
		fs := flag.NewFlagSet("handover return", flag.ExitOnError)
		hid := fs.Int("handover", 0, "交接记录编号")
		iid := fs.Int("item", 0, "事项编号")
		op := fs.String("operator", "", "操作人")
		reason := fs.String("reason", "", "退回原因")
		_ = fs.Parse(args[1:])
		if err := s.HandoverReturn(*hid, *iid, *op, *reason); err != nil {
			fail(err)
		}
		fmt.Printf("交接记录 #%d 事项 #%d 已退回\n", *hid, *iid)
		printHandoverByID(s, *hid)
	case "track":
		fs := flag.NewFlagSet("handover track", flag.ExitOnError)
		hid := fs.Int("handover", 0, "交接记录编号")
		iid := fs.Int("item", 0, "事项编号")
		op := fs.String("operator", "", "操作人")
		note := fs.String("note", "", "跟踪说明")
		owner := fs.String("follow-owner", "", "后续负责人")
		_ = fs.Parse(args[1:])
		if err := s.HandoverTrack(*hid, *iid, *op, *note, *owner); err != nil {
			fail(err)
		}
		fmt.Printf("交接记录 #%d 事项 #%d 已转为继续跟踪\n", *hid, *iid)
		printHandoverByID(s, *hid)
	case "resubmit":
		fs := flag.NewFlagSet("handover resubmit", flag.ExitOnError)
		hid := fs.Int("handover", 0, "交接记录编号")
		iid := fs.Int("item", 0, "事项编号")
		op := fs.String("operator", "", "交班人")
		note := fs.String("note", "", "补充说明")
		_ = fs.Parse(args[1:])
		if err := s.HandoverResubmit(*hid, *iid, *op, *note); err != nil {
			fail(err)
		}
		fmt.Printf("交接记录 #%d 事项 #%d 已重新提交（恢复待处理）\n", *hid, *iid)
		printHandoverByID(s, *hid)
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 handover 子命令 %q\n", args[0])
		os.Exit(1)
	}
}

func printHandoverByID(s *handover.Store, id int) {
	h, err := s.GetHandover(id)
	if err != nil {
		fail(err)
	}
	printHandover(s, h)
}

func printHandover(s *handover.Store, h *handover.Handover) {
	fmt.Printf("交接记录 #%d\n", h.ID)
	from, _ := s.GetShift(h.FromShiftID)
	to, _ := s.GetShift(h.ToShiftID)
	fmt.Printf("  交班班次: #%d", h.FromShiftID)
	if from != nil {
		fmt.Printf("（%s 岗位，负责人 %s）", from.Post, from.Owner)
	}
	fmt.Println()
	fmt.Printf("  接班班次: #%d", h.ToShiftID)
	if to != nil {
		fmt.Printf("（%s 岗位，负责人 %s）", to.Post, to.Owner)
	}
	fmt.Println()
	fmt.Printf("  状态: %s\n", map[bool]string{true: "已完成", false: "未完成"}[h.Complete])
	fmt.Printf("  事项（%d 项）:\n", len(h.Items))
	for _, hi := range h.Items {
		it, _ := s.GetItem(hi.ItemID)
		content := ""
		if it != nil {
			content = it.Content
		}
		fmt.Printf("    事项 #%d [%s] %s\n", hi.ItemID, hi.Result, content)
		if hi.Operator != "" {
			fmt.Printf("        操作人: %s  处理时间: %s\n", hi.Operator, fmtTime(hi.HandledAt))
		}
		if hi.TrackNote != "" {
			fmt.Printf("        跟踪说明: %s\n", hi.TrackNote)
			fmt.Printf("        跟踪后续负责人: %s\n", hi.TrackOwner)
		}
		if hi.ReturnReason != "" {
			fmt.Printf("        退回原因（保留，不覆盖）: %s\n", hi.ReturnReason)
		}
		for _, n := range hi.Notes {
			fmt.Printf("        补充说明（操作人 %s，%s）: %s\n", n.Operator, fmtTime(n.Time), n.Text)
		}
	}
}
