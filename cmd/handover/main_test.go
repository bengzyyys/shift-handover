package main

import (
	"reflect"
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
			path, rest := splitDataFlag(tc.args, tc.env)
			if path != tc.wantPath {
				t.Errorf("dataPath = %q，期望 %q", path, tc.wantPath)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %q，期望 %q", rest, tc.wantRest)
			}
		})
	}
}
