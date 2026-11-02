package tool

import (
	"hamusuta/agent-dev/src/domain"
)

var readTool ReadTool
var bashTool BashTool
var editTool EditTool

var Tools map[string]domain.Tool = map[string]domain.Tool{
	readTool.Name(): readTool,
	bashTool.Name(): bashTool,
	editTool.Name(): editTool,
}
