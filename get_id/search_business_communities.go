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

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

type bizGroup struct {
	Username     string
	Title        string
	Participants int
	Queries      map[string]bool
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}

	queries := []string{
		"предприниматели", "предприниматели чат", "бизнес чат", "бизнес клуб",
		"бизнес сообщество", "нетворкинг", "бизнес нетворкинг", "клуб предпринимателей",
		"малый бизнес", "средний бизнес", "стартап чат", "стартапы",
		"маркетинг для бизнеса", "продажи бизнес", "владельцы бизнеса",
		"селлеры", "маркетплейсы", "wildberries селлеры", "ozon селлеры",
		"ecommerce", "интернет магазин", "авито бизнес",
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

	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	foundGroups := map[string]*bizGroup{}
	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		for _, q := range queries {
			found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: 100})
			if err != nil {
				fmt.Fprintf(os.Stderr, "query %q failed: %v\n", q, err)
				time.Sleep(time.Second)
				continue
			}
			for _, cc := range found.Chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				u := strings.ToLower(ch.Username)
				if existing[u] || badBizGroup(ch.Title, ch.Username) {
					continue
				}
				g := foundGroups[u]
				if g == nil {
					g = &bizGroup{
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{},
					}
					foundGroups[u] = g
				}
				g.Queries[q] = true
				if ch.ParticipantsCount > g.Participants {
					g.Participants = ch.ParticipantsCount
				}
			}
			time.Sleep(650 * time.Millisecond)
		}

		var list []*bizGroup
		for _, g := range foundGroups {
			if g.Participants >= 800 {
				list = append(list, g)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].Queries) == len(list[j].Queries) {
				return list[i].Participants > list[j].Participants
			}
			return len(list[i].Queries) > len(list[j].Queries)
		})
		for _, g := range list {
			var qs []string
			for q := range g.Queries {
				qs = append(qs, q)
			}
			sort.Strings(qs)
			fmt.Printf("@%s\t%d\t%s\tqueries=%s\n", g.Username, g.Participants, g.Title, strings.Join(qs, ", "))
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

func badBizGroup(title, username string) bool {
	s := strings.ToLower(title + " " + username)
	bad := []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "crypto", "крипто",
		"инвест", "трейд", "форекс", "казино", "ставки", "знакомств", "доска",
		"отзывы", "накрут", "арбитраж", "affiliate", "курьер", "работа в",
	}
	for _, b := range bad {
		if strings.Contains(s, b) {
			return true
		}
	}
	return false
}
