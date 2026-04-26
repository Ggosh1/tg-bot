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

type groupHit struct {
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

	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	queries := []string{
		"фриланс чат", "фриланс заказчики", "фриланс услуги", "услуги чат",
		"заказы фриланс", "заказы дизайн", "дизайн заказчики", "дизайнеры заказы",
		"инфографика заказы", "маркетинг чат", "smm чат", "таргет чат",
		"предприниматели чат", "бизнес чат", "малый бизнес чат",
		"tilda чат", "wordpress чат", "битрикс чат", "авито чат",
		"маркетплейсы чат", "wildberries чат", "ozon чат",
	}

	existing := map[string]bool{}
	for _, u := range []string{
		"digital_partners_chat", "bizneschats", "teletoloka", "frilans_uslugi_vakansii",
		"tgjob", "myjobit", "bitrix_work", "php_jobs", "javascript_jobs", "nodejs_jobs",
		"mobile_jobs", "devops_jobs", "uiux_jobs", "products_jobs", "infografikaq",
		"bitrixfordevelopers", "bit24dev", "laravelrus", "react_native_jobs", "react_js",
		"angular_ru", "nodejs_ru", "devops_ru", "kubernetes_ru", "qa_jobs", "qa_automation",
		"uiux_ru", "reactnative_ru", "agile_ru", "webflowvacancy", "freelance_dev_work",
		"biznes_chat", "smm_talking", "tilda_community", "tilda_chat", "tildaformschat",
		"bitrix24help", "mp_partner", "marketplaces_chat", "bizneskontakti", "chat_biznes1",
		"bussineschat_inf", "freelansechat", "startupschoolsk", "vckitchen_chat",
		"wildberries_business", "httpswbselllls", "sellery_wb_ozon",
		"getclient", "designs_squad", "zakaz_design", "zakazi_designers",
		"chatusemarketru", "marketplaces_chats", "chat_wildberries1", "job_for_bots",
		"bitrix24_work", "unityjobs_pub", "mobileadvrussia", "telemetr_chat",
		"easy_wrk_chat", "frwork3", "freelancetaverna", "n8n_community",
		"webflow_club", "webflowshcoolchat", "webfrl", "workk_on",
		"biznes_club_russia", "predprinimateli_ip", "integratorycrm",
		"shopify_chat", "chatggecom", "getcourse_online", "angar_rus",
		"smmlancer", "talentedpeoples", "mskeventjob", "spbeventjob",
		"spbeventjobtalk", "rueventjob", "eventhuntertalk",
	} {
		existing[strings.ToLower(u)] = true
	}

	hits := map[string]*groupHit{}

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
				if existing[u] || badGroup(ch.Title, ch.Username) {
					continue
				}
				g := hits[u]
				if g == nil {
					g = &groupHit{
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{},
					}
					hits[u] = g
				}
				g.Queries[q] = true
				if ch.ParticipantsCount > g.Participants {
					g.Participants = ch.ParticipantsCount
				}
			}
			time.Sleep(700 * time.Millisecond)
		}

		var list []*groupHit
		for _, h := range hits {
			if h.Participants >= 500 {
				list = append(list, h)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].Queries) == len(list[j].Queries) {
				return list[i].Participants > list[j].Participants
			}
			return len(list[i].Queries) > len(list[j].Queries)
		})

		for _, h := range list {
			var qs []string
			for q := range h.Queries {
				qs = append(qs, q)
			}
			sort.Strings(qs)
			fmt.Printf("@%s\t%d\t%s\tqueries=%s\n", h.Username, h.Participants, h.Title, strings.Join(qs, ", "))
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

func badGroup(title, username string) bool {
	s := strings.ToLower(title + " " + username)
	bad := []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "взаим", "пиар",
		"подпис", "crypto", "крипто", "биржа труда", "hh", "работа москва",
	}
	for _, b := range bad {
		if strings.Contains(s, b) {
			return true
		}
	}
	return false
}
