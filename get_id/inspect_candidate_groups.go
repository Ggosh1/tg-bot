package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

type verdict struct {
	Username string
	Title    string
	Count    int
	Leads    int
	Trash    int
	Samples  []string
	Err      string
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}
	usernames := []string{
		"wildberries_professional", "ozonhelpchat", "wildberries_supply", "wildberries_chat_mp",
		"mp_multiply", "fullfilment_topchat", "marketplace_network", "chat_marketpleysy",
		"chat_ozone", "wildberries_design_mp", "frilans_chat3", "freelansechat",
		"search_cpa", "burzh_target", "predprinimateli_chat", "biznes_chat_omsk",
		"uslugi_chat", "OZON_int", "ffprofit_chat", "chat_affiliates",
	}

	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		for _, u := range usernames {
			v := inspect(ctx, api, u)
			fmt.Printf("@%s\tlead_like=%d/%d\ttrash=%d\t%s\n", v.Username, v.Leads, v.Count, v.Trash, v.Title)
			if v.Err != "" {
				fmt.Printf("  error: %s\n", v.Err)
				continue
			}
			for _, s := range v.Samples {
				fmt.Printf("  - %s\n", s)
			}
			time.Sleep(600 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

func inspect(ctx context.Context, api *tg.Client, username string) verdict {
	v := verdict{Username: username}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		v.Err = err.Error()
		return v
	}
	var ch *tg.Channel
	for _, cc := range resolved.Chats {
		if c, ok := cc.(*tg.Channel); ok && strings.EqualFold(c.Username, username) {
			ch = c
			break
		}
	}
	if ch == nil {
		v.Err = "not a channel/supergroup"
		return v
	}
	v.Title = ch.Title
	h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
		Limit: 80,
	})
	if err != nil {
		v.Err = err.Error()
		return v
	}
	_, messages := unpackHistory(h)
	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.Message == "" || msg.Post {
			continue
		}
		v.Count++
		if isTrash(msg.Message) {
			v.Trash++
			continue
		}
		if isLeadLike(msg.Message) {
			v.Leads++
			if len(v.Samples) < 4 {
				v.Samples = append(v.Samples, compact2(msg.Message, 190))
			}
		}
	}
	return v
}

func unpackHistory(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
	switch r := res.(type) {
	case *tg.MessagesMessages:
		return r.Chats, r.Messages
	case *tg.MessagesMessagesSlice:
		return r.Chats, r.Messages
	case *tg.MessagesChannelMessages:
		return r.Chats, r.Messages
	default:
		return nil, nil
	}
}

func isLeadLike(s string) bool {
	t := strings.ToLower(s)
	intents := []string{"нужен", "нужна", "нужно", "ищу", "требуется", "кто сделает", "кто может", "есть задача", "заказ", "проект", "пишите в лс"}
	domains := []string{"сайт", "лендинг", "бот", "дизайн", "логотип", "карточ", "инфограф", "smm", "таргет", "реклама", "монтаж", "копирайт", "тильд", "wordpress", "битрикс", "маркетплейс", "wb", "ozon", "авито", "парсер"}
	hasIntent, hasDomain := false, false
	for _, x := range intents {
		if strings.Contains(t, x) {
			hasIntent = true
			break
		}
	}
	for _, x := range domains {
		if strings.Contains(t, x) {
			hasDomain = true
			break
		}
	}
	return hasIntent && hasDomain
}

func isTrash(s string) bool {
	t := strings.ToLower(s)
	bad := []string{"ищу работу", "ищу заказчиков", "резюме", "вакансия", "в штат", "usdt", "отзывы", "накрут", "пф", "подработка", "интим", "займ"}
	for _, x := range bad {
		if strings.Contains(t, x) {
			return true
		}
	}
	return false
}

func compact2(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
