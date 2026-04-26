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

type source struct {
	Username string
	Group    string
}

type chatStat struct {
	Username     string
	Title        string
	Kind         string
	Group        string
	Messages     int
	LeadCount    int
	CategoryHits map[string]int
	DayHits      map[string]map[string]int
	Samples      []leadSample
	AllLeads     []leadSample
	Err          string
}

type leadSample struct {
	Date     string
	Category string
	Text     string
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}

	sources := buildSources()
	cutoff := time.Now().AddDate(0, 0, -3).Unix()

	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	stats := []*chatStat{}
	byDay := map[string]map[string]int{}
	byCat := map[string]int{}
	bySourceGroup := map[string]int{}
	errors := 0

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		seen := map[string]bool{}
		for _, src := range sources {
			u := cleanUsername(src.Username)
			if u == "" || seen[strings.ToLower(u)] {
				continue
			}
			seen[strings.ToLower(u)] = true
			st := inspectSource(ctx, api, u, src.Group, int(cutoff))
			stats = append(stats, st)
			if st.Err != "" {
				errors++
			}
			for cat, n := range st.CategoryHits {
				byCat[cat] += n
				bySourceGroup[st.Group] += n
			}
			for day, cats := range st.DayHits {
				if byDay[day] == nil {
					byDay[day] = map[string]int{}
				}
				for cat, n := range cats {
					byDay[day][cat] += n
				}
			}
			time.Sleep(450 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	days := sortedDays(byDay)
	cats := []string{"development", "design", "marketplace_design", "marketing_smm", "marketplace_ops", "business_services", "other_project"}

	fmt.Println("=== Daily category counts (last 3 days, user messages only where possible) ===")
	for _, day := range days {
		fmt.Printf("%s", day)
		total := 0
		for _, cat := range cats {
			total += byDay[day][cat]
		}
		fmt.Printf("\ttotal=%d", total)
		for _, cat := range cats {
			if byDay[day][cat] > 0 {
				fmt.Printf("\t%s=%d", cat, byDay[day][cat])
			}
		}
		fmt.Println()
	}

	fmt.Println("\n=== Average per day ===")
	dayCount := len(days)
	if dayCount == 0 {
		dayCount = 1
	}
	totalAll := 0
	for _, cat := range cats {
		totalAll += byCat[cat]
	}
	fmt.Printf("total\t%d\tavg=%.2f\n", totalAll, float64(totalAll)/float64(dayCount))
	for _, cat := range cats {
		fmt.Printf("%s\t%d\tavg=%.2f\n", cat, byCat[cat], float64(byCat[cat])/float64(dayCount))
	}

	fmt.Println("\n=== By old/new source group ===")
	for _, g := range []string{"new_core", "new_business", "new_niche", "new_marketplace", "old"} {
		fmt.Printf("%s\t%d\tavg=%.2f\n", g, bySourceGroup[g], float64(bySourceGroup[g])/float64(dayCount))
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].LeadCount == stats[j].LeadCount {
			return stats[i].Messages > stats[j].Messages
		}
		return stats[i].LeadCount > stats[j].LeadCount
	})

	fmt.Println("\n=== Top chats by lead-like messages ===")
	for _, st := range stats {
		if st.LeadCount == 0 {
			continue
		}
		fmt.Printf("@%s\t%s\t%s\tmessages=%d\tleads=%d", st.Username, st.Group, st.Title, st.Messages, st.LeadCount)
		for _, cat := range cats {
			if st.CategoryHits[cat] > 0 {
				fmt.Printf("\t%s=%d", cat, st.CategoryHits[cat])
			}
		}
		fmt.Println()
		for _, s := range st.Samples {
			fmt.Printf("  - %s [%s] %s\n", s.Date, s.Category, s.Text)
		}
	}

	fmt.Println("\n=== Errors / no access ===")
	fmt.Printf("errors=%d\n", errors)
	for _, st := range stats {
		if st.Err != "" {
			fmt.Printf("@%s\t%s\n", st.Username, st.Err)
		}
	}

	var report strings.Builder
	report.WriteString("date\tgroup\tusername\ttitle\tcategory\ttext\n")
	for _, st := range stats {
		for _, s := range st.AllLeads {
			report.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\n",
				s.Date,
				st.Group,
				st.Username,
				strings.ReplaceAll(st.Title, "\t", " "),
				s.Category,
				strings.ReplaceAll(s.Text, "\t", " "),
			))
		}
	}
	_ = os.WriteFile("../lidohod/data/reports/estimate_candidate_leads.tsv", []byte(report.String()), 0644)
}

func buildSources() []source {
	newCore := []string{
		"biznes_chat", "smm_talking", "tilda_community", "tilda_chat", "tildaformschat",
		"bitrix24help", "MP_partner", "marketplaces_chat", "chat_marketpleysy", "wildberries_design_mp",
	}
	newBusiness := []string{
		"BiznesKontakti", "biznes_club_russia", "chat_biznes1", "BussinesChat_INF",
		"predprinimateli_chat", "predprinimateli_rus", "frilans_chat3", "frilans_chat",
		"frelance_chat1", "freelansechat", "uslugi_chat", "malii_bizn", "klub_biznes",
		"biznetworkchat", "predprinimateli_chat1",
	}
	newNiche := []string{
		"shopifypro", "avito_biznes", "ownerbeautybussines", "biznes_chat_stroitely",
		"club_np", "oporarb", "startupschoolsk", "vckitchen_chat",
	}
	newMarketplace := []string{
		"wildberries_business", "wildberries_postavshchikii_top", "httpsWBselllls",
		"seller_wb_ozon", "sellery_wb_ozon", "ip_biznes_predprinimatel", "marketpleysy3",
	}
	old := []string{
		"digital_partners_chat", "bizneschats", "teletoloka", "frilans_uslugi_vakansii",
		"tgjob", "myjobit", "bitrix_work", "php_jobs", "javascript_jobs", "nodejs_jobs",
		"mobile_jobs", "devops_jobs", "uiux_jobs", "products_jobs", "INFOGRAPHIKAQ",
		"bitrixfordevelopers", "bit24dev", "laravelrus", "react_native_jobs", "react_js",
		"angular_ru", "nodejs_ru", "devops_ru", "kubernetes_ru", "qa_jobs", "qa_automation",
		"uiux_ru", "reactnative_ru", "agile_ru", "webflowvacancy",
	}
	var out []source
	add := func(group string, list []string) {
		for _, u := range list {
			out = append(out, source{Username: u, Group: group})
		}
	}
	add("new_core", newCore)
	add("new_business", newBusiness)
	add("new_niche", newNiche)
	add("new_marketplace", newMarketplace)
	add("old", old)
	return out
}

func inspectSource(ctx context.Context, api *tg.Client, username, group string, cutoff int) *chatStat {
	st := &chatStat{
		Username:     username,
		Group:        group,
		CategoryHits: map[string]int{},
		DayHits:      map[string]map[string]int{},
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		st.Err = err.Error()
		return st
	}
	var ch *tg.Channel
	for _, cc := range resolved.Chats {
		if c, ok := cc.(*tg.Channel); ok && strings.EqualFold(c.Username, username) {
			ch = c
			break
		}
	}
	if ch == nil {
		st.Err = "not resolved as channel/supergroup"
		return st
	}
	st.Title = ch.Title
	if ch.Megagroup {
		st.Kind = "group"
	} else if ch.Broadcast {
		st.Kind = "channel"
	} else {
		st.Kind = "unknown"
	}

	offsetID := 0
	for page := 0; page < 10; page++ {
		h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			st.Err = err.Error()
			return st
		}
		_, messages := unpackMessages(h)
		if len(messages) == 0 {
			break
		}
		oldest := int(time.Now().Unix())
		for _, mc := range messages {
			msg, ok := mc.(*tg.Message)
			if !ok || msg.Message == "" {
				continue
			}
			if msg.ID > 0 {
				offsetID = msg.ID
			}
			if msg.Date < oldest {
				oldest = msg.Date
			}
			if msg.Date < cutoff {
				continue
			}
			if msg.Post || !ch.Megagroup {
				continue
			}
			st.Messages++
			cat, ok := classifyLead(msg.Message)
			if !ok {
				continue
			}
			st.LeadCount++
			st.CategoryHits[cat]++
			day := time.Unix(int64(msg.Date), 0).Format("2006-01-02")
			if st.DayHits[day] == nil {
				st.DayHits[day] = map[string]int{}
			}
			st.DayHits[day][cat]++
			if len(st.Samples) < 4 {
				st.Samples = append(st.Samples, leadSample{
					Date:     day,
					Category: cat,
					Text:     compact(msg.Message, 180),
				})
			}
			st.AllLeads = append(st.AllLeads, leadSample{
				Date:     day,
				Category: cat,
				Text:     compact(msg.Message, 600),
			})
		}
		if oldest < cutoff || offsetID == 0 {
			break
		}
		time.Sleep(130 * time.Millisecond)
	}
	return st
}

func unpackMessages(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func classifyLead(text string) (string, bool) {
	t := strings.ToLower(text)
	if isBadLead(t) || !hasIntent(t) {
		return "", false
	}

	marketplace := hasAny(t, []string{"wildberries", "вайлдбер", "wb", "ozon", "озон", "маркетплейс", "селлер", "карточк", "инфографик"})
	design := hasAny(t, []string{"дизайн", "дизайнер", "логотип", "баннер", "визуал", "фигма", "figma", "ui", "ux", "обложк", "презентац", "креатив", "карточк", "инфографик"})
	dev := hasAny(t, []string{"сайт", "лендинг", "бот", "парсер", "скрипт", "разработ", "доработ", "программист", "кодер", "backend", "frontend", "php", "python", "javascript", "js", "node", "react", "vue", "laravel", "bitrix", "битрикс", "wordpress", "вордпресс", "tilda", "тильда", "shopify", "opencart", "api", "crm", "интеграц", "автоматизац", "приложен", "android", "ios", "kotlin", "server", "сервер", "devops", "vpn"})
	marketing := hasAny(t, []string{"smm", "смм", "таргет", "таргетолог", "реклама", "директ", "яндекс", "vk", "вк", "telegram ads", "маркетинг", "контент", "копирайт", "seo", "рилс", "reels", "монтаж", "видео"})
	business := hasAny(t, []string{"юрист", "бухгалтер", "продаж", "воронк", "консульт", "аналитик", "упаковк", "бренд", "отдел продаж", "рекрутер", "подбор персонала"})

	if !(marketplace || design || dev || marketing || business) {
		return "", false
	}

	switch {
	case marketplace && design:
		return "marketplace_design", true
	case dev:
		return "development", true
	case design:
		return "design", true
	case marketing:
		return "marketing_smm", true
	case marketplace:
		return "marketplace_ops", true
	case business:
		return "business_services", true
	default:
		return "other_project", true
	}
}

func hasIntent(t string) bool {
	return hasAny(t, []string{
		"нужен", "нужна", "нужно", "надо", "ищу", "ищем", "требуется", "кто может",
		"кто сможет", "кто сделает", "кто поможет", "есть задача", "есть проект",
		"нужна помощь", "нужен человек", "готов оплатить", "бюджет",
	})
}

func isBadLead(t string) bool {
	return hasAny(t, []string{
		"ищу работу", "ищу заказчиков", "ищу клиентов", "резюме", "#резюме",
		"предлагаю услуги", "оказываю услуги", "#помогу", "помогу с", "мои услуги",
		"портфолио", "беру заказы", "я дизайнер", "я разработчик", "я таргетолог",
		"вакансия", "в штат", "офис", "полная занятость", "full-time", "fulltime",
		"usdt", "кэш", "кэшем", "отзывы", "накрут", "пф", "муж на час",
		"подработка", "ежедневный доход", "заработок", "интим", "ставки",
		"в команду", "ищем в команду", "зарплата", "зп:", "удаленная работа",
		"удалённая работа", "грузчик", "грузчиков", "кредит", "кредитование",
		"банки в наличии", "продам ботов", "готова помочь", "готов помочь",
		"помогаем бизнес", "если кому интересно", "создай свою", "подписывай pdf",
		"наш ассортимент", "наши услуги", "мы специализируемся", "специализируемся",
		"ищу партнеров для поиска клиентов", "кому нужен", "кто хочет повысить чек",
	})
}

func hasAny(t string, words []string) bool {
	for _, w := range words {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func sortedDays(byDay map[string]map[string]int) []string {
	var days []string
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	return days
}

func cleanUsername(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimPrefix(u, "https://t.me/")
	u = strings.TrimPrefix(u, "http://t.me/")
	u = strings.TrimPrefix(u, "t.me/")
	u = strings.TrimPrefix(u, "@")
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	return u
}

func compact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
