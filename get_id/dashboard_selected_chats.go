package main

import (
	"context"
	"crypto/sha1"
	"fmt"
	"html"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

type targetChat struct {
	Username string
	Status   string
}

type dashboardLead struct {
	Date     time.Time
	Username string
	Title    string
	Category string
	Strength string
	Text     string
}

type dashboardChat struct {
	Username string
	Status   string
	Title    string
	Kind     string
	Messages int
	Leads    int
	Weak     int
	Error    string
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}

	targets := selectedTargets()
	reportPrefix := "selected_chats"
	if strings.EqualFold(os.Getenv("DASHBOARD_TARGETS"), "candidates") {
		targets = candidateTargets()
		reportPrefix = "candidate_chats"
	}

	cutoff := time.Now().Add(-48 * time.Hour)
	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	var leads []dashboardLead
	var chats []dashboardChat
	seenLead := map[string]bool{}

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		for _, target := range targets {
			chat, foundLeads := inspectDashboardChat(ctx, api, target, cutoff)
			for _, lead := range foundLeads {
				key := fingerprint(lead.Username + "|" + lead.Text)
				if seenLead[key] {
					continue
				}
				seenLead[key] = true
				leads = append(leads, lead)
			}
			chats = append(chats, chat)
			time.Sleep(350 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	sort.Slice(leads, func(i, j int) bool {
		return leads[i].Date.After(leads[j].Date)
	})
	sort.Slice(chats, func(i, j int) bool {
		if chats[i].Leads == chats[j].Leads {
			return chats[i].Messages > chats[j].Messages
		}
		return chats[i].Leads > chats[j].Leads
	})

	writeTSV(leads, chats, reportPrefix)
	writeHTML(leads, chats, cutoff, reportPrefix)
	printSummary(leads, chats, cutoff)
}

func selectedTargets() []targetChat {
	return []targetChat{
		{"biznes_chat", "?"},
		{"smm_talking", "+"},
		{"tilda_community", "+"},
		{"tilda_chat", "+"},
		{"tildaformschat", "?"},
		{"bitrix24help", "+"},
		{"MP_partner", "+"},
		{"marketplaces_chat", "+"},
		{"BiznesKontakti", "+"},
		{"chat_biznes1", "+"},
		{"BussinesChat_INF", "?"},
		{"freelansechat", "?"},
		{"startupschoolsk", "+"},
		{"vckitchen_chat", "+"},
		{"wildberries_business", "+"},
		{"httpsWBselllls", "?"},
		{"sellery_wb_ozon", "+"},
	}
}

func candidateTargets() []targetChat {
	return []targetChat{
		{"clewell", "candidate"},
		{"EntrepreneursBusinessChat", "candidate"},
		{"biznes_gq", "candidate"},
		{"biznes_kzn", "candidate"},
		{"bisneschatmoskva", "candidate"},
		{"networkers_moscow", "candidate"},
		{"usbiznetwork", "candidate"},
		{"ai_community_chat", "candidate"},
		{"chatsellermarketplace", "candidate"},
		{"sellery_chat_ozon_wb", "candidate"},
		{"marketplaces_chats", "candidate"},
		{"mpgo_chat", "candidate"},
		{"mybiz64_chat", "candidate"},
		{"FromBerek", "candidate"},
		{"chat_blogers", "candidate"},
		{"barter_blogger", "candidate"},
		{"youtube_blogeri_chat", "candidate"},
		{"chatusemarketru", "candidate"},
	}
}

func inspectDashboardChat(ctx context.Context, api *tg.Client, target targetChat, cutoff time.Time) (dashboardChat, []dashboardLead) {
	out := dashboardChat{Username: target.Username, Status: target.Status}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: target.Username})
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	var ch *tg.Channel
	for _, cc := range resolved.Chats {
		if c, ok := cc.(*tg.Channel); ok && strings.EqualFold(c.Username, target.Username) {
			ch = c
			break
		}
	}
	if ch == nil {
		out.Error = "not resolved"
		return out, nil
	}
	out.Title = ch.Title
	if ch.Megagroup {
		out.Kind = "group"
	} else if ch.Broadcast {
		out.Kind = "channel"
	} else {
		out.Kind = "unknown"
	}
	if !ch.Megagroup {
		out.Error = "not a chat/supergroup"
		return out, nil
	}

	var leads []dashboardLead
	offsetID := 0
	for page := 0; page < 12; page++ {
		h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			out.Error = err.Error()
			return out, leads
		}
		_, messages := unpackDashboardMessages(h)
		if len(messages) == 0 {
			break
		}
		oldest := time.Now()
		for _, mc := range messages {
			msg, ok := mc.(*tg.Message)
			if !ok || msg.Message == "" || msg.Post {
				continue
			}
			if msg.ID > 0 {
				offsetID = msg.ID
			}
			msgTime := time.Unix(int64(msg.Date), 0)
			if msgTime.Before(oldest) {
				oldest = msgTime
			}
			if msgTime.Before(cutoff) {
				continue
			}
			out.Messages++
			category, strength, ok := classifyDashboardLead(msg.Message)
			if !ok {
				continue
			}
			if strength == "weak" {
				out.Weak++
				continue
			}
			out.Leads++
			leads = append(leads, dashboardLead{
				Date:     msgTime,
				Username: target.Username,
				Title:    ch.Title,
				Category: category,
				Strength: strength,
				Text:     compact(msg.Message, 900),
			})
		}
		if oldest.Before(cutoff) || offsetID == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return out, leads
}

func unpackDashboardMessages(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func classifyDashboardLead(text string) (string, string, bool) {
	t := strings.ToLower(text)
	if isRejectedDashboard(t) {
		return "", "", false
	}
	if !hasLeadIntent(t) {
		return "", "", false
	}

	marketplace := hasAny(t, []string{"wildberries", "вайлдбер", " wb", "ozon", "озон", "маркетплейс", "селлер", "карточк", "инфографик"})
	design := hasAny(t, []string{"дизайн", "дизайнер", "логотип", "баннер", "визуал", "figma", "фигма", "ui", "ux", "обложк", "презентац", "креатив", "карточк", "инфографик"})
	dev := hasAny(t, []string{"сайт", "лендинг", " бот ", "бота", "ботов", "боту", "чат-бот", "телеграм бот", "тг бот", "парсер", "скрипт", "разработ", "доработ", "программист", "кодер", "backend", "frontend", "php", "python", "javascript", "node", "react", "vue", "bitrix", "битрикс", "wordpress", "вордпресс", "tilda", "тильда", "shopify", "opencart", "api", "crm", "интеграц", "автоматизац", "приложен", "android", "ios", "сервер", "devops", "vpn", "бизнес процесс", "бизнес-процесс"})
	marketing := hasAny(t, []string{"smm", "смм", "таргет", "таргетолог", "реклама", "директ", "маркетинг", "контент", "копирайт", "seo", "reels", "рилс", "монтаж", "видео", "креативы"})
	business := hasAny(t, []string{"бухгалтер", "юрист", "продаж", "воронк", "консульт", "аналитик", "упаковк", "бренд", "отдел продаж"})

	if !(marketplace || design || dev || marketing || business) {
		return "", "", false
	}

	category := "business_services"
	switch {
	case marketplace && design:
		category = "marketplace_design"
	case dev:
		category = "development"
	case design:
		category = "design"
	case marketing:
		category = "marketing_smm"
	case marketplace:
		category = "marketplace_ops"
	case business:
		category = "business_services"
	}

	strength := "weak"
	if hasAny(t, []string{
		"кто может", "кто сможет", "кто сделает", "кто поможет",
		"нужно сделать", "надо сделать", "нужно доработать", "надо доработать",
		"нужно разработать", "надо разработать", "нужно настроить", "надо настроить",
		"ищу специалист", "ищу исполнител", "ищу подрядчик", "ищу дизайнер", "ищу разработчик",
		"ищу таргетолог", "ищу монтаж", "ищу smm", "ищу смм", "ищу копирайтер",
		"нужен специалист", "нужен исполнитель", "нужен подрядчик", "нужен дизайнер", "нужен разработчик",
		"нужен таргетолог", "нужен монтаж", "нужен smm", "нужен смм", "нужен копирайтер", "нужен бухгалтер", "нужен юрист",
		"нужна инфограф", "нужны карточк", "требуется специалист", "есть задача", "есть проект", "готов оплатить",
	}) {
		strength = "strong"
	}
	if strength == "weak" && (strings.Contains(t, "ищу ") || strings.Contains(t, "нужен ") || strings.Contains(t, "нужна ") || strings.Contains(t, "нужны ")) && (dev || design || marketing || marketplace) {
		strength = "strong"
	}
	return category, strength, true
}

func hasLeadIntent(t string) bool {
	return hasAny(t, []string{
		"нужен", "нужна", "нужно", "надо", "ищу", "ищем", "требуется",
		"кто может", "кто сможет", "кто сделает", "кто поможет",
		"есть задача", "есть проект", "готов оплатить",
	})
}

func isRejectedDashboard(t string) bool {
	return hasAny(t, []string{
		"ищу работу", "ищу заказчиков", "ищу клиентов", "#резюме", "резюме",
		"предлагаю услуги", "оказываю услуги", "#помогу", "помогу с", "мои услуги",
		"портфолио", "беру заказы", "я дизайнер", "я разработчик", "я таргетолог", "помогаю",
		"я занимаюсь", "занимаюсь разработкой", "что я делаю", "делаю под ключ", "создаю сайты", "создаю ботов", "что я предлагаю", "наши услуги",
		"если вам нужен", "если вам нужна", "если нужен", "если нужна", "если кому интересно",
		"тебе нужна лучшая реклама",
		"меня зовут", "основатель", "реальный кейс", "подключить sirena", "sirena ai",
		"читать далее", "ключевые изменения", "ключевые моменты", "форменная одежда",
		"вакансия", "в штат", "офис", "полная занятость", "full-time", "fulltime",
		"ищем в команду", "приглашаем в команду", "удаленная работа", "удалённая работа",
		"удаленка", "удалёнка", "работа на дому", "постоянная работа", "свободный график",
		"без опыта", "нет опыта", "всему обуч", "совмещать с учёбой", "совмещать с учебой",
		"зп от", "зп ", "оклад", "анкету для заполнения", "только от 18", "строго от 18", "с 18 лет", "от 18 лет", "старше 18",
		"ищу помощника", "ищу помощницу", "ищу ассистента", "ищу менеджера для работы", "менеджеры, у меня для вас",
		"нужны курьеры", "курьеры наличных", "наличных денег", "криптообменник", "дроповод",
		"ищу продажников", "ищем продажников", "ищу партнёров", "ищу партнеров", "поиска клиентов",
		"ищу инвестора", "ищу партнера", "ищу партнёра", "бизнес-девелопмент партнера", "бизнес-девелопмент партнёра",
		"нужна ли кому-то разработка", "нужен ли кому-то", "кому-то разработка сайта",
		"ищем скаут", "ищу скаут", "ищем рекрутер", "ищу рекрутер",
		"usdt", "кэш", "кэшем", "отзывы", "накрут", "пф", "муж на час", "грузчик",
		"подработка", "ежедневный доход", "заработок", "интим", "ставки", "кредит",
		"продам ботов", "куплю аккаунт", "продажа аккаунтов",
	})
}

func hasAny(t string, words []string) bool {
	for _, word := range words {
		if strings.Contains(t, word) {
			return true
		}
	}
	return false
}

func writeTSV(leads []dashboardLead, chats []dashboardChat, reportPrefix string) {
	var b strings.Builder
	b.WriteString("datetime\tdate\tusername\ttitle\tcategory\tstrength\ttext\n")
	for _, lead := range leads {
		b.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			lead.Date.Format("2006-01-02 15:04"),
			lead.Date.Format("2006-01-02"),
			lead.Username,
			sanitizeTSV(lead.Title),
			lead.Category,
			lead.Strength,
			sanitizeTSV(lead.Text),
		))
	}
	_ = os.WriteFile(fmt.Sprintf("../lidohod/data/reports/%s_leads_last_48h.tsv", reportPrefix), []byte(b.String()), 0644)

	b.Reset()
	b.WriteString("username\tstatus\ttitle\tkind\tmessages\tstrong_leads\tweak_candidates\terror\n")
	for _, chat := range chats {
		b.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			chat.Username,
			chat.Status,
			sanitizeTSV(chat.Title),
			chat.Kind,
			chat.Messages,
			chat.Leads,
			chat.Weak,
			sanitizeTSV(chat.Error),
		))
	}
	_ = os.WriteFile(fmt.Sprintf("../lidohod/data/reports/%s_stats_last_48h.tsv", reportPrefix), []byte(b.String()), 0644)
}

func writeHTML(leads []dashboardLead, chats []dashboardChat, cutoff time.Time, reportPrefix string) {
	catCounts := map[string]int{}
	dayCounts := map[string]int{}
	for _, lead := range leads {
		catCounts[lead.Category]++
		dayCounts[lead.Date.Format("2006-01-02")]++
	}
	cats := []string{"development", "marketplace_design", "design", "marketing_smm", "marketplace_ops", "business_services"}
	sort.Slice(chats, func(i, j int) bool {
		if chats[i].Leads == chats[j].Leads {
			return chats[i].Messages > chats[j].Messages
		}
		return chats[i].Leads > chats[j].Leads
	})

	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Lead dashboard</title><style>
body{font-family:Arial,sans-serif;margin:24px;background:#f7f7f4;color:#171717}h1{margin:0 0 6px} .muted{color:#666}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:10px;margin:18px 0}.card{background:white;border:1px solid #ddd;border-radius:8px;padding:12px}.num{font-size:28px;font-weight:700}.lead{background:white;border:1px solid #ddd;border-radius:8px;padding:12px;margin:10px 0}.meta{font-size:13px;color:#555;margin-bottom:8px}.cat{display:inline-block;background:#111;color:#fff;border-radius:999px;padding:2px 8px;font-size:12px}.chat-table{width:100%;border-collapse:collapse;background:white;margin:14px 0}.chat-table td,.chat-table th{border:1px solid #ddd;padding:6px 8px;text-align:left}.text{white-space:pre-wrap;line-height:1.35}.err{color:#a33}
</style></head><body>`)
	b.WriteString(fmt.Sprintf("<h1>Заявки за последние 48 часов</h1><div class=\"muted\">Срез с %s. Только сильные кандидаты: без self-promo, вакансий и явного мусора.</div>", html.EscapeString(cutoff.Format("2006-01-02 15:04"))))
	b.WriteString("<div class=\"grid\">")
	b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Всего заявок</div><div class=\"num\">%d</div></div>", len(leads)))
	for _, cat := range cats {
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">%s</div><div class=\"num\">%d</div></div>", html.EscapeString(cat), catCounts[cat]))
	}
	b.WriteString("</div>")

	b.WriteString("<h2>Чаты</h2><table class=\"chat-table\"><tr><th>чат</th><th>статус</th><th>сообщений</th><th>заявок</th><th>weak</th><th>ошибка</th></tr>")
	for _, chat := range chats {
		b.WriteString(fmt.Sprintf("<tr><td>@%s<br><span class=\"muted\">%s</span></td><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td class=\"err\">%s</td></tr>",
			html.EscapeString(chat.Username),
			html.EscapeString(chat.Title),
			html.EscapeString(chat.Status),
			chat.Messages,
			chat.Leads,
			chat.Weak,
			html.EscapeString(chat.Error),
		))
	}
	b.WriteString("</table>")

	b.WriteString("<h2>Тексты заявок</h2>")
	for _, lead := range leads {
		b.WriteString("<div class=\"lead\">")
		b.WriteString(fmt.Sprintf("<div class=\"meta\">%s · @%s · %s · <span class=\"cat\">%s</span></div>",
			html.EscapeString(lead.Date.Format("2006-01-02 15:04")),
			html.EscapeString(lead.Username),
			html.EscapeString(lead.Title),
			html.EscapeString(lead.Category),
		))
		b.WriteString(fmt.Sprintf("<div class=\"text\">%s</div>", html.EscapeString(lead.Text)))
		b.WriteString("</div>")
	}
	b.WriteString("</body></html>")
	_ = os.WriteFile(fmt.Sprintf("../lidohod/data/reports/%s_dashboard_last_48h.html", reportPrefix), []byte(b.String()), 0644)
}

func printSummary(leads []dashboardLead, chats []dashboardChat, cutoff time.Time) {
	catCounts := map[string]int{}
	chatCounts := map[string]int{}
	for _, lead := range leads {
		catCounts[lead.Category]++
		chatCounts[lead.Username]++
	}
	fmt.Printf("cutoff=%s\n", cutoff.Format("2006-01-02 15:04"))
	fmt.Printf("strong_leads=%d\n", len(leads))
	for _, cat := range []string{"development", "marketplace_design", "design", "marketing_smm", "marketplace_ops", "business_services"} {
		fmt.Printf("%s=%d\n", cat, catCounts[cat])
	}
	fmt.Println("top_chats:")
	type kv struct {
		K string
		V int
	}
	var top []kv
	for k, v := range chatCounts {
		top = append(top, kv{k, v})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].V > top[j].V })
	for _, item := range top {
		fmt.Printf("@%s=%d\n", item.K, item.V)
	}
	fmt.Println("errors:")
	for _, chat := range chats {
		if chat.Error != "" {
			fmt.Printf("@%s %s\n", chat.Username, chat.Error)
		}
	}
}

func sanitizeTSV(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func fingerprint(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	sum := sha1.Sum([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:])
}

func compact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
