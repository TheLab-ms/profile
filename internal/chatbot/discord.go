package chatbot

import "context"

type Discord struct {
}

func NewDiscord() *Discord {
	return &Discord{}
}

func (d *Discord) NotifyNewMember(ctx context.Context, email string) {
	// TODO
}
