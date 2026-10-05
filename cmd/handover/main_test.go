package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitDataFlag(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		env      string
		wantPath string
		wantRest []string
	}{
		{
			name:     "内容恰为 --data=文件 时仍写入显式指定的数据文件",
			args:     []string{"--data", "甲.json", "item-add", "--shift", "S001", "--content", "--data=乙.json", "--severity", "normal", "--follow", "李四"},
			wantPath: "甲.json",
			wantRest: []string{"item-add", "--shift", "S001", "--content", "--data=乙.json", "--severity", "normal", "--follow", "李四"},
		},
		{
			name:     "内容恰为 --data 时后续独立参数仍生效",
			args:     []string{"item-add", "--shift", "S001", "--content", "--data", "--severity", "urgent", "--follow", "王五"},
			wantPath: "",
			wantRest: []string{"item-add", "--shift", "S001", "--content", "--data", "--severity", "urgent", "--follow", "王五"},
		},
		{
			name:     "限制条件恰为 --data= 开头的说明",
			args:     []string{"item-update", "--id", "I001", "--content", "正文", "--constraints", "--data=丙.json 只是说明", "--severity", "normal", "--follow", "赵六"},
			wantPath: "",
			wantRest: []string{"item-update", "--id", "I001", "--content", "正文", "--constraints", "--data=丙.json 只是说明", "--severity", "normal", "--follow", "赵六"},
		},
		{
			name:     "命令前的 --data 路径",
			args:     []string{"--data", "a.json", "shift-list"},
			wantPath: "a.json",
			wantRest: []string{"shift-list"},
		},
		{
			name:     "命令后的 --data 路径",
			args:     []string{"shift-list", "--data", "a.json"},
			wantPath: "a.json",
			wantRest: []string{"shift-list"},
		},
		{
			name:     "--data=路径 形式",
			args:     []string{"item-show", "--id", "I001", "--data=a.json"},
			wantPath: "a.json",
			wantRest: []string{"item-show", "--id", "I001"},
		},
		{
			name:     "未指定时沿用环境变量",
			args:     []string{"shift-list"},
			env:      "env.json",
			wantPath: "env.json",
			wantRest: []string{"shift-list"},
		},
		{
			name:     "显式 --data 优先于环境变量",
			args:     []string{"--data", "explicit.json", "shift-list"},
			env:      "env.json",
			wantPath: "explicit.json",
			wantRest: []string{"shift-list"},
		},
		{
			name:     "--name=value 形式的参数值不受影响",
			args:     []string{"item-add", "--shift=S001", "--content=--data=乙.json", "--severity=normal", "--follow=李四"},
			wantPath: "",
			wantRest: []string{"item-add", "--shift=S001", "--content=--data=乙.json", "--severity=normal", "--follow=李四"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, rest, err := splitDataFlag(tc.args, tc.env)
			if err != nil {
				t.Fatalf("splitDataFlag 返回意外错误：%v", err)
			}
			if path != tc.wantPath {
				t.Errorf("dataPath = %q，期望 %q", path, tc.wantPath)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %q，期望 %q", rest, tc.wantRest)
			}
		})
	}
}

func TestSplitDataFlagRejectsEmptyOrMissingPath(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{
			name: "命令前 --data 后为空字符串",
			args: []string{"--data", "", "shift-list"},
			want: "不能为空",
		},
		{
			name: "命令后 --data 后为空字符串",
			args: []string{"shift-list", "--data", ""},
			want: "不能为空",
		},
		{
			name: "--data= 后没有内容",
			args: []string{"shift-list", "--data="},
			want: "不能为空",
		},
		{
			name: "命令末尾只有独立 --data",
			args: []string{"shift-list", "--data"},
			want: "缺少数据文件路径",
		},
		{
			name: "空路径不能回落到环境变量",
			args: []string{"--data", "", "shift-list"},
			env:  "env.json",
			want: "不能为空",
		},
		{
			name: "空路径不能回落到环境变量（--data= 形式）",
			args: []string{"--data=", "shift-list"},
			env:  "env.json",
			want: "不能为空",
		},
		{
			name: "缺少路径不能回落到环境变量",
			args: []string{"shift-list", "--data"},
			env:  "env.json",
			want: "缺少数据文件路径",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, rest, err := splitDataFlag(tc.args, tc.env)
			if err == nil {
				t.Fatalf("期望报错，实际成功：path=%q rest=%q", path, rest)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，got %q", tc.want, err.Error())
			}
		})
	}
}
