package main

import (
	"bufio"
	"context"
	"fmt"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider"
	"hamusuta/agent-dev/src/tool"
	"hamusuta/agent-dev/src/util"
	"os"
	"os/signal"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	configFile := "config.yaml"

	if _, err := util.EnsureFile(configFile, ""); err != nil {
		fmt.Fprintln(os.Stderr, "创建 config.yaml 失败", err)
		return
	}

	data, err := os.ReadFile(configFile)

	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置文件失败", err)
		return
	}

	var cfg config.GlobalConfig
	err = yaml.Unmarshal(data, &cfg)

	if err != nil {
		fmt.Fprintln(os.Stderr, "解析配置文件失败", err)
		return
	}

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			fmt.Println()
			break
		}

		input := strings.TrimSpace(scanner.Text())
		switch input {
		case "":
			continue
		case "exit", "quit":
			return
		}

		// 获取provider
		var probe config.ProbeConfig
		if err = cfg.Provider.Decode(&probe); err != nil {
			fmt.Fprintln(os.Stderr, "provider 解析为 ProbeConfig 报错", err)
			return
		}

		var p domain.Provider
		switch probe.Type {
		case "DeepSeek":
			var dc config.DeepSeekConfig
			if err := cfg.Provider.Decode(&dc); err != nil {
				fmt.Fprintln(os.Stderr, "provider 解析为 DeepSeekConfig 报错", err)
				return
			}
			if p, err = provider.NewDeepSeek(dc); err != nil {
				fmt.Fprintln(os.Stderr, "创建 DeepSeek Provider 失败", err)
				return
			}
		default:
			fmt.Fprintln(os.Stderr, "未支持的 provider 类型: "+probe.Type)
			return
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)

		core.Loop(domain.Ctx{
			Prompt:   input,
			Provider: p,
			Tools:    tool.Tools,
			Context:  ctx,
		})

		stop()
	}

	if err = scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "读取输入失败", err)
	}
}
