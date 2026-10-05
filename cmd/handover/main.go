// Command handover 是本地班次交接与未关闭事项的命令行入口。
//
// 数据默认保存在当前目录 handover-data.json，可用全局参数 --data 或环境变量
// HANDOVER_DATA 指定。时间统一使用带时区偏移的 RFC3339，如
// 2026-10-02T08:00:00+08:00。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bengzyyys/shift-handover/handover"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usageText = `用法：handover [--data 文件] <命令> [参数]

数据持久化在本地 JSON 文件中，退出后重新打开数据仍在。
时间使用带时区的 RFC3339，例如 2026-10-02T08:00:00+08:00。

班次：
  shift-add      建立班次        --position 岗位 --owner 负责人 --start 开始 --end 结束
                                 [--note 重叠说明（与同岗位已存班次区间重叠时必填）]
  shift-list     列出全部班次
  shift-close    结束班次        --id 班次编号
  shift-show     按班次查询      --id 班次编号（完整事项/交接/结果）

重叠说明：
  note-add       为重叠班次填说明 --a 班次 --b 班次 --note 说明
  note-list      查看班次涉及的重叠说明 --id 班次

事项：
  item-add       新增事项        --shift 班次 --content 内容 \
                                 --severity normal|important|urgent \
                                 [--constraints 限制条件] --follow 后续负责人
  item-update    修改事项        --id 事项编号 --content 内容 \
                                 --severity normal|important|urgent \
                                 [--constraints 限制条件] --follow 后续负责人
  item-close     关闭事项        --id 事项编号 --operator 操作人
  item-show      查询事项        --id 事项编号（最新状态、完整处理经过与各次交接当前结果）

交接：
  handover-create   发起交接      --from 交班班次 --to 接班班次
  handover-list     列出全部交接
  handover-show     查看交接      --id 交接编号
  handover-process  接班人处理单项 --id 交接编号 --item 事项编号 \
                                 --action confirm|return|track --operator 操作人 \
                                 [--reason 退回原因] [--note 跟踪说明] [--follow 后续负责人]
  handover-resubmit 交班人补充并重新提交退回项 \
                                 --id 交接编号 --item 事项编号 \
                                 --operator 操作人 --supplement 补充说明
`

func run(argv []string, stdout, stderr io.Writer) int {
	args := append([]string(nil), argv...)
	dataPath, rest, err := splitDataFlag(args, os.Getenv("HANDOVER_DATA"))
	if err != nil {
		// 显式指定了数据文件却没给出有效路径：明确失败，不回落到
		// 环境变量或默认文件，也不展示帮助或任何业务数据。
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	if dataPath == "" {
		dataPath = "handover-data.json"
	}

	if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help" {
		fmt.Fprint(stdout, usageText)
		return 0
	}

	cmd, cmdArgs := rest[0], rest[1:]
	store, err := handover.Open(dataPath)
	if err != nil {
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	svc := handover.NewService(store)

	out, err := dispatch(svc, cmd, cmdArgs)
	if err != nil {
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	if out != "" {
		fmt.Fprint(stdout, out)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
	}
	return 0
}

// valueFlags 是各子命令会带一个值的字符串参数。提取全局 --data 时，
// 这些参数后面的一个 token 是它的值：即使恰好写成 --data 或 --data=...，
// 也只是参数值（如事项内容、限制条件），不能当作数据文件参数取走。
var valueFlags = map[string]bool{
	"id": true, "position": true, "owner": true, "start": true, "end": true,
	"a": true, "b": true, "note": true, "shift": true, "content": true,
	"severity": true, "constraints": true, "follow": true, "operator": true,
	"from": true, "to": true, "item": true, "action": true, "reason": true,
	"supplement": true,
}

// splitDataFlag 从全部输入中抽出全局 --data（--data 路径 与 --data=路径 两种
// 形式，命令前后均可），返回数据文件路径与其余参数。已知参数的 --name value
// 形式中，value 原样保留，不参与 --data 判定；env 为未显式指定时的回退值。
// 显式给出 --data 却没有有效路径（--data 后为空字符串、--data= 后没有内容、
// 或命令末尾只有独立的 --data）时返回错误：这是输入错误，不能按未指定参数
// 回落到 env 或默认文件。
func splitDataFlag(args []string, env string) (dataPath string, rest []string, err error) {
	dataPath = env
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if name, ok := flagName(arg); ok && name != "data" && valueFlags[name] {
			// 其他参数的 --name value 形式：下一个 token 是它的值，原样保留。
			rest = append(rest, arg)
			if i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		switch {
		case arg == "--data":
			if i+1 >= len(args) {
				return "", nil, errors.New("--data 后缺少数据文件路径")
			}
			dataPath = args[i+1]
			i++
			if dataPath == "" {
				return "", nil, errors.New("--data 的数据文件路径不能为空")
			}
		case strings.HasPrefix(arg, "--data="):
			dataPath = arg[len("--data="):]
			if dataPath == "" {
				return "", nil, errors.New("--data 的数据文件路径不能为空")
			}
		default:
			rest = append(rest, arg)
		}
	}
	return dataPath, rest, nil
}

// flagName 解析 -name / --name 形式的参数名；带 = 的形式值已内联，返回 false。
func flagName(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
		return "", false
	}
	name := strings.TrimLeft(arg, "-")
	return name, name != ""
}

func dispatch(svc *handover.Service, cmd string, args []string) (string, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		id, position, owner, startS, endS              string
		a, b                                           string
		shift, content, severityS, constraints, follow string
		operator, from, to                             string
		item, actionS, reason, note, supplement        string
	)
	fs.StringVar(&id, "id", "", "记录编号")
	fs.StringVar(&position, "position", "", "岗位")
	fs.StringVar(&owner, "owner", "", "负责人")
	fs.StringVar(&startS, "start", "", "开始时间 RFC3339")
	fs.StringVar(&endS, "end", "", "结束时间 RFC3339")
	fs.StringVar(&a, "a", "", "班次编号 A")
	fs.StringVar(&b, "b", "", "班次编号 B")
	// note-add 时为重叠说明；handover-process --action track 时为跟踪说明。
	fs.StringVar(&note, "note", "", "重叠说明 / 跟踪说明")
	fs.StringVar(&shift, "shift", "", "班次编号")
	fs.StringVar(&content, "content", "", "事项内容")
	fs.StringVar(&severityS, "severity", "", "严重程度 normal|important|urgent")
	fs.StringVar(&constraints, "constraints", "", "限制条件（可空）")
	// follow 同时用于后续负责人（新增/修改事项）与跟踪后续负责人（track）。
	fs.StringVar(&follow, "follow", "", "后续负责人")
	fs.StringVar(&operator, "operator", "", "操作人")
	fs.StringVar(&from, "from", "", "交班班次编号")
	fs.StringVar(&to, "to", "", "接班班次编号")
	fs.StringVar(&item, "item", "", "事项编号")
	fs.StringVar(&actionS, "action", "", "confirm|return|track")
	fs.StringVar(&reason, "reason", "", "退回原因")
	fs.StringVar(&supplement, "supplement", "", "补充说明")
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	switch cmd {
	case "shift-add":
		start, err := parseTime("--start", startS)
		if err != nil {
			return "", err
		}
		end, err := parseTime("--end", endS)
		if err != nil {
			return "", err
		}
		sh, err := svc.CreateShift(position, owner, start, end, note)
		if err != nil {
			return "", err
		}
		return "已建立班次 " + handover.FormatShift(sh), nil

	case "shift-list":
		shifts := svc.ListShifts()
		if len(shifts) == 0 {
			return "（暂无班次）", nil
		}
		out := ""
		for _, sh := range shifts {
			out += handover.FormatShift(sh) + "\n"
		}
		return out, nil

	case "shift-close":
		sh, err := svc.CloseShift(id)
		if err != nil {
			return "", err
		}
		return "已结束班次 " + handover.FormatShift(sh), nil

	case "shift-show":
		rep, err := svc.ShiftReport(id)
		if err != nil {
			return "", err
		}
		return handover.FormatReport(rep), nil

	case "note-add":
		n, err := svc.AddOverlapNote(a, b, note)
		if err != nil {
			return "", err
		}
		return "已保存重叠说明 " + handover.FormatOverlapNote(n), nil

	case "note-list":
		notes := svc.OverlapNotes(id)
		if len(notes) == 0 {
			return "（无重叠说明）", nil
		}
		out := ""
		for _, n := range notes {
			out += handover.FormatOverlapNote(n) + "\n\n"
		}
		return out, nil

	case "item-add":
		sev, err := handover.ParseSeverity(severityS)
		if err != nil {
			return "", err
		}
		it, err := svc.AddItem(shift, content, sev, constraints, follow)
		if err != nil {
			return "", err
		}
		return "已新增事项\n" + handover.FormatItem(it), nil

	case "item-update":
		sev, err := handover.ParseSeverity(severityS)
		if err != nil {
			return "", err
		}
		it, err := svc.UpdateItem(id, content, sev, constraints, follow)
		if err != nil {
			return "", err
		}
		return "已修改事项\n" + handover.FormatItem(it), nil

	case "item-close":
		it, err := svc.CloseItem(id, operator)
		if err != nil {
			return "", err
		}
		return "已关闭事项\n" + handover.FormatItem(it), nil

	case "item-show":
		j, err := svc.ItemJourney(id)
		if err != nil {
			return "", err
		}
		return handover.FormatItemJourney(j), nil

	case "handover-create":
		h, err := svc.CreateHandover(from, to)
		if err != nil {
			if errors.Is(err, handover.ErrHandoverExists) {
				// 重复发起返回已有记录，不视为失败。
				return "已存在交接记录（重复发起，返回已有记录）\n" + handover.FormatHandover(h), nil
			}
			return "", err
		}
		return "已发起交接\n" + handover.FormatHandover(h), nil

	case "handover-list":
		hs := svc.ListHandovers()
		if len(hs) == 0 {
			return "（暂无交接记录）", nil
		}
		out := ""
		for _, h := range hs {
			out += handover.FormatHandover(h) + "\n"
		}
		return out, nil

	case "handover-show":
		h, err := svc.GetHandover(id)
		if err != nil {
			return "", err
		}
		return handover.FormatHandover(h), nil

	case "handover-process":
		action, err := handover.ParseAction(actionS)
		if err != nil {
			return "", err
		}
		h, err := svc.ProcessEntry(id, item, action, operator, reason, note, follow)
		if err != nil {
			return "", err
		}
		return "已处理\n" + handover.FormatHandover(h), nil

	case "handover-resubmit":
		h, err := svc.ResubmitReturned(id, item, operator, supplement)
		if err != nil {
			return "", err
		}
		return "已补充说明并重新提交\n" + handover.FormatHandover(h), nil

	default:
		return "", fmt.Errorf("未知命令 %q，运行 handover help 查看用法", cmd)
	}
}

func parseTime(name, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("%s 不能为空，格式为带时区的 RFC3339（如 2026-10-02T08:00:00+08:00）", name)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 时间格式无效：%w（应为 RFC3339，如 2026-10-02T08:00:00+08:00）", name, err)
	}
	return t, nil
}
