package domain

import "context"

type Ctx struct {
	Prompt   string
	Provider Provider
	Tools    map[string]Tool
	Context  context.Context
}
