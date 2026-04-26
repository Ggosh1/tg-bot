package domain

type Category struct {
	Name     string
	Chats    map[int64]bool
	Keywords []string
}

func NewCategory(name string, chatIDs []int64, keywords []string) *Category {
	chatsMap := make(map[int64]bool)
	for _, id := range chatIDs {
		chatsMap[id] = true
	}
	return &Category{
		Name:     name,
		Chats:    chatsMap,
		Keywords: keywords,
	}
}