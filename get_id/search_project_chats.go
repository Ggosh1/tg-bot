package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

type candidate struct {
	ID           int64
	Title        string
	Username     string
	Participants int
	Hits         int
	Queries      map[string]bool
	Samples      []string
}

func main() {
	_ = godotenv.Load()

	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}
	apiHash := os.Getenv("API_HASH")

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	queries := []string{
		"нужен сайт", "нужен лендинг", "нужен бот", "нужен парсер",
		"нужен разработчик", "ищу разработчика", "ищу программиста",
		"нужно доработать сайт", "нужно сделать сайт", "нужно сделать бот",
		"ищу дизайнера", "нужен дизайнер", "нужна инфографика",
		"нужно сделать логотип", "нужен smm", "ищу таргетолога",
		"нужен монтажер", "нужен копирайтер", "ищу специалиста",
	}

	existing := map[string]bool{}
	for _, u := range []string{
		"digital_partners_chat", "bizneschats", "teletoloka", "frilans_uslugi_vakansii",
		"tgjob", "myjobit", "bitrix_work", "php_jobs", "javascript_jobs", "nodejs_jobs",
		"mobile_jobs", "devops_jobs", "uiux_jobs", "products_jobs", "infografikaq",
		"bitrixfordevelopers", "bit24dev", "laravelrus", "react_native_jobs", "react_js",
		"angular_ru", "nodejs_ru", "devops_ru", "kubernetes_ru", "qa_jobs", "qa_automation",
		"uiux_ru", "reactnative_ru", "agile_ru", "webflowvacancy", "freelance_dev_work",
	} {
		existing[strings.ToLower(u)] = true
	}

	candidates := map[int64]*candidate{}
	minDate := int(time.Now().AddDate(0, 0, -45).Unix())

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()

		for _, q := range queries {
			res, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
				GroupsOnly:  true,
				Q:           q,
				Filter:      &tg.InputMessagesFilterEmpty{},
				MinDate:     minDate,
				OffsetPeer:  &tg.InputPeerEmpty{},
				OffsetID:    0,
				OffsetRate:  0,
				Limit:       40,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "query %q failed: %v\n", q, err)
				time.Sleep(2 * time.Second)
				continue
			}

			chats, messages := unpack(res)
			chatByID := map[int64]*tg.Channel{}
			for _, chat := range chats {
				ch, ok := chat.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if existing[strings.ToLower(ch.Username)] {
					continue
				}
				if looksBadSource(ch.Title, ch.Username) {
					continue
				}
				chatByID[ch.ID] = ch
			}

			for _, mc := range messages {
				msg, ok := mc.(*tg.Message)
				if !ok || msg.Post || msg.Message == "" || looksBadMessage(msg.Message) {
					continue
				}
				peer, ok := msg.PeerID.(*tg.PeerChannel)
				if !ok {
					continue
				}
				ch := chatByID[peer.ChannelID]
				if ch == nil {
					continue
				}
				c := candidates[ch.ID]
				if c == nil {
					c = &candidate{
						ID:           ch.ID,
						Title:        ch.Title,
						Username:     ch.Username,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{},
					}
					candidates[ch.ID] = c
				}
				c.Hits++
				c.Queries[q] = true
				if len(c.Samples) < 3 {
					c.Samples = append(c.Samples, compact(msg.Message, 170))
				}
			}

			time.Sleep(900 * time.Millisecond)
		}

		var list []*candidate
		for _, c := range candidates {
			if c.Participants >= 300 && c.Hits >= 1 {
				list = append(list, c)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Hits == list[j].Hits {
				return list[i].Participants > list[j].Participants
			}
			return list[i].Hits > list[j].Hits
		})

		for _, c := range list {
			var qs []string
			for q := range c.Queries {
				qs = append(qs, q)
			}
			sort.Strings(qs)
			fmt.Printf("@%s\t%d\t%d\t%s\tqueries=%s\n", c.Username, c.Participants, c.Hits, c.Title, strings.Join(qs, ", "))
			for _, s := range c.Samples {
				fmt.Printf("  - %s\n", s)
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

func unpack(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func looksBadSource(title, username string) bool {
	s := strings.ToLower(title + " " + username)
	bad := []string{"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "resume", "работа в штат"}
	for _, b := range bad {
		if strings.Contains(s, b) {
			return true
		}
	}
	return false
}

func looksBadMessage(message string) bool {
	s := strings.ToLower(message)
	bad := []string{
		"kwork", "кворк", "вакансия", "в штат", "офис", "резюме", "cv",
		"ищу работу", "ищу заказчиков", "предлагаю услуги", "оказываю услуги",
		"usdt", "муж на час", "подработка", "приглашаем в команду",
	}
	for _, b := range bad {
		if strings.Contains(s, b) {
			return true
		}
	}
	return false
}

func compact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
