package tool

import (
	"hamusuta/agent-dev/src/domain"
)

var readTool ReadTool
var bashTool BashTool
var editTool EditTool
var writeTool WriteTool
var grepTool GrepTool

var Tools map[string]domain.Tool = map[string]domain.Tool{
	readTool.Name():  readTool,
	writeTool.Name(): writeTool,
	editTool.Name():  editTool,
	grepTool.Name():  grepTool,
	bashTool.Name():  bashTool,
}
