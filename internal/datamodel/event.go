package datamodel

type Event struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	MembersOnly bool   `json:"membersOnly"`
}
