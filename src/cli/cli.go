package main

import (
	"bufio"
	"fmt"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"os"
	"strings"
)

// Run 启动命令行循环，逐行读取标准输入。
func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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

		core.Loop(domain.Ctx{
			Prompt: input,
		})
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "读取输入失败", err)
	}
}
