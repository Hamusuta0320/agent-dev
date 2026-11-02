package domain

type SessionMessage struct {
}

type Session struct {
	Id       string           `json:"id"`
	Messages []SessionMessage `json:"messages"`
}
