package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
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

type leadSourceCandidate struct {
	ID              int64
	AccessHash      int64
	Username        string
	Title           string
	Participants    int
	SearchHits      int
	BuyerHits       int
	DevHits         int
	DesignHits      int
	MarketingHits   int
	MarketplaceHits int
	NoiseHits       int
	HistoryMessages int
	HistoryLeads    int
	HistoryNoise    int
	QuerySet        map[string]bool
	Samples         []string
	HistorySamples  []string
	Recommendation  string
	Reason          string
}

type messageSearchResult struct {
	chats    []tg.ChatClass
	messages []tg.MessageClass
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
		"нужно доработать сайт",
		"нужны правки на сайт",
		"нужен разработчик",
		"ищу разработчика",
		"ищу программиста",
		"нужен программист",
		"нужен верстальщик",
		"нужен wordpress",
		"нужен битрикс",
		"правки битрикс",
		"настроить битрикс24",
		"настроить amoCRM",
		"интеграция CRM",
		"нужно сделать бот",
		"нужен телеграм бот",
		"сделать телеграм бота",
		"нужен парсер",
		"настроить оплату на сайте",
		"нужен технический специалист",
		"нужен техспец",
		"настроить getcourse",
		"нужна автоматизация",
		"нужен сайт на tilda",
		"нужны правки tilda",
		"нужен webflow",
		"нужно собрать лендинг",
		"нужен дизайнер карточек",
		"нужна инфографика wildberries",
		"нужен smm специалист",
		"нужен таргетолог",
	}

	existing := existingUsernames()
	minDate := int(time.Now().AddDate(0, 0, -30).Unix())
	candidates := map[int64]*leadSourceCandidate{}

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		for _, q := range sourceQueries() {
			found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: 80})
			if err != nil {
				fmt.Fprintf(os.Stderr, "source query %q failed: %v\n", q, err)
				time.Sleep(time.Second)
				continue
			}
			addChatsFromSearch(candidates, found.Chats, existing)
			time.Sleep(650 * time.Millisecond)
		}
		for _, username := range seedUsernames() {
			resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
			if err != nil {
				fmt.Fprintf(os.Stderr, "seed @%s failed: %v\n", username, err)
				time.Sleep(400 * time.Millisecond)
				continue
			}
			addChatsFromSearch(candidates, resolved.Chats, existing)
			time.Sleep(400 * time.Millisecond)
		}

		for _, q := range queries {
			res, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
				GroupsOnly: true,
				Q:          q,
				Filter:     &tg.InputMessagesFilterEmpty{},
				MinDate:    minDate,
				OffsetPeer: &tg.InputPeerEmpty{},
				OffsetID:   0,
				OffsetRate: 0,
				Limit:      80,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "query %q failed: %v\n", q, err)
				time.Sleep(2 * time.Second)
				continue
			}

			searchResult := unpackSearchMessages(res)
			chatByID := map[int64]*tg.Channel{}
			for _, cc := range searchResult.chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if existing[strings.ToLower(ch.Username)] {
					continue
				}
				if badSourceTitle(ch.Title, ch.Username) {
					continue
				}
				chatByID[ch.ID] = ch
			}

			for _, mc := range searchResult.messages {
				msg, ok := mc.(*tg.Message)
				if !ok || msg.Post || strings.TrimSpace(msg.Message) == "" {
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
					c = &leadSourceCandidate{
						ID:           ch.ID,
						AccessHash:   ch.AccessHash,
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						QuerySet:     map[string]bool{},
					}
					candidates[ch.ID] = c
				}
				c.SearchHits++
				c.QuerySet[q] = true
				kind := classifyLeadSourceMessage(msg.Message)
				switch kind {
				case "dev":
					c.BuyerHits++
					c.DevHits++
					addSample(&c.Samples, msg.Message)
				case "design":
					c.BuyerHits++
					c.DesignHits++
					addSample(&c.Samples, msg.Message)
				case "marketing":
					c.BuyerHits++
					c.MarketingHits++
					addSample(&c.Samples, msg.Message)
				case "marketplace":
					c.BuyerHits++
					c.MarketplaceHits++
					addSample(&c.Samples, msg.Message)
				case "noise":
					c.NoiseHits++
				}
			}
			time.Sleep(900 * time.Millisecond)
		}

		for _, c := range candidates {
			inspectHistory(ctx, api, c)
			classifyHistorySamples(c)
			scoreCandidate(c)
			time.Sleep(650 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	list := make([]*leadSourceCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Participants >= 300 && (c.BuyerHits > 0 || c.HistoryLeads > 0) {
			list = append(list, c)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		si, sj := sourceScore(list[i]), sourceScore(list[j])
		if si == sj {
			return list[i].Participants > list[j].Participants
		}
		return si > sj
	})

	if err := writeDiscoveryReports(list); err != nil {
		log.Fatal(err)
	}

	for i, c := range list {
		if i >= 40 {
			break
		}
		fmt.Printf("%s\t@%s\t%d\tdev=%d design=%d marketing=%d mp=%d history=%d/%d noise=%d\t%s\n",
			c.Recommendation, c.Username, c.Participants, c.DevHits, c.DesignHits, c.MarketingHits,
			c.MarketplaceHits, c.HistoryLeads, c.HistoryMessages, c.HistoryNoise, c.Title)
		for _, s := range append(c.Samples, c.HistorySamples...) {
			fmt.Printf("  - %s\n", compactDiscovery(s, 220))
		}
	}
}

func sourceQueries() []string {
	return []string{
		"n8n чат", "make чат", "zapier чат", "albato чат", "автоматизация чат",
		"ai community чат", "нейросети бизнес чат", "чат gpt бизнес", "ии предприниматели чат",
		"getcourse чат", "геткурс чат", "salebot чат", "chatbot chat", "чатботы чат",
		"amoCRM чат", "amocrm чат", "битрикс24 чат", "bitrix24 чат", "crm чат",
		"tilda чат", "webflow чат", "wordpress чат", "shopify чат",
		"стартап чат", "стартапы чат", "предприниматели чат", "бизнес чат",
		"маркетплейсы чат", "селлеры чат", "wildberries чат", "ozon чат",
	}
}

func seedUsernames() []string {
	return []string{
		"n8n_community",
		"ai_community_chat",
		"ai_community_camp",
		"oqode",
		"nocode_jobs",
		"zerocoderdotru",
		"salebot_chat",
		"amocrm_chat",
		"amocrm_ru",
		"getcourse_chat",
		"getcourse_update",
		"wp_russia",
		"wordpress_ru",
		"webflow_ru",
		"tilda_official_chat",
	}
}

func addChatsFromSearch(candidates map[int64]*leadSourceCandidate, chats []tg.ChatClass, existing map[string]bool) {
	for _, cc := range chats {
		ch, ok := cc.(*tg.Channel)
		if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
			continue
		}
		if existing[strings.ToLower(ch.Username)] {
			continue
		}
		if badSourceTitle(ch.Title, ch.Username) {
			continue
		}
		if candidates[ch.ID] == nil {
			candidates[ch.ID] = &leadSourceCandidate{
				ID:           ch.ID,
				AccessHash:   ch.AccessHash,
				Username:     ch.Username,
				Title:        ch.Title,
				Participants: ch.ParticipantsCount,
				QuerySet:     map[string]bool{},
			}
		}
	}
}

func unpackSearchMessages(res tg.MessagesMessagesClass) messageSearchResult {
	switch r := res.(type) {
	case *tg.MessagesMessages:
		return messageSearchResult{chats: r.Chats, messages: r.Messages}
	case *tg.MessagesMessagesSlice:
		return messageSearchResult{chats: r.Chats, messages: r.Messages}
	case *tg.MessagesChannelMessages:
		return messageSearchResult{chats: r.Chats, messages: r.Messages}
	default:
		return messageSearchResult{}
	}
}

func inspectHistory(ctx context.Context, api *tg.Client, c *leadSourceCandidate) {
	h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash},
		Limit: 120,
	})
	if err != nil {
		return
	}
	_, messages := unpackDiscoveryHistory(h)
	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.Post || strings.TrimSpace(msg.Message) == "" {
			continue
		}
		c.HistoryMessages++
		kind := classifyLeadSourceMessage(msg.Message)
		switch kind {
		case "dev", "design", "marketing", "marketplace":
			c.HistoryLeads++
			addSample(&c.HistorySamples, msg.Message)
		case "noise":
			c.HistoryNoise++
		}
	}
}

func classifyHistorySamples(c *leadSourceCandidate) {
	for _, sample := range c.HistorySamples {
		switch classifyLeadSourceMessage(sample) {
		case "dev":
			c.DevHits++
		case "design":
			c.DesignHits++
		case "marketing":
			c.MarketingHits++
		case "marketplace":
			c.MarketplaceHits++
		}
	}
}

func unpackDiscoveryHistory(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func classifyLeadSourceMessage(message string) string {
	s := normalizeDiscovery(message)
	if isNoiseDiscovery(s) {
		return "noise"
	}
	hasBuyer := containsAnyDiscovery(s, []string{
		"нужен", "нужна", "нужно", "ищу", "требуется", "кто может", "кто сделает",
		"посоветуйте", "подскажите", "есть задача", "есть проект", "надо сделать",
		"помогите сделать", "нужны правки", "надо настроить", "нужно настроить",
	})
	if !hasBuyer {
		return ""
	}

	if containsAnyDiscovery(s, []string{
		"разработчик", "программист", "верстальщик", "сайт", "лендинг", "бот", "парсер",
		"скрипт", "api", "интеграц", "crm", "битрикс", "bitrix", "wordpress", "вордпресс",
		"tilda", "тильд", "webflow", "оплат", "getcourse", "геткурс", "деплой", "сервер",
		"хостинг", "баг", "правки", "доработ", "автоматизац", "код", "yii", "react", "php",
	}) {
		return "dev"
	}
	if containsAnyDiscovery(s, []string{
		"дизайн", "дизайнер", "логотип", "баннер", "figma", "фигма", "карточ", "инфограф",
		"презентац", "обложк", "макет", "ui", "ux",
	}) {
		return "design"
	}
	if containsAnyDiscovery(s, []string{
		"smm", "смм", "таргет", "директ", "маркетолог", "seo", "реклама", "контекст",
		"воронк", "копирайт", "рилс", "reels",
	}) {
		return "marketing"
	}
	if containsAnyDiscovery(s, []string{
		"wildberries", "вайлдбер", "wb", "ozon", "озон", "маркетплейс", "селлер",
		"карточки товара", "ведение кабинета", "менеджер вб",
	}) {
		return "marketplace"
	}
	return ""
}

func isNoiseDiscovery(s string) bool {
	return containsAnyDiscovery(s, []string{
		"ищу работу", "ищу заказы", "ищу заказчиков", "предлагаю услуги", "оказываю услуги",
		"могу помочь", "помогу вам", "разработаю", "создаю сайты", "делаю сайты", "делаю ботов",
		"портфолио", "резюме", "cv", "вакансия", "в штат", "полный день", "офис", "зп от",
		"зарплата", "удаленная работа", "подработка", "быстро заработать", "отзывы есть",
		"без опыта", "анкета для заполнения", "только от 18", "строго от 18", "от 18 лет",
		"14+", "ставки", "казино", "usdt", "крипто", "p2p", "авито отзывы", "самовыкуп",
	})
}

func badSourceTitle(title, username string) bool {
	s := normalizeDiscovery(title + " " + username)
	return containsAnyDiscovery(s, []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "resume", "работа в штат",
		"отзывы", "накрут", "пф", "crypto", "крипто", "ставки", "займ",
	})
}

func normalizeDiscovery(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func containsAnyDiscovery(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func addSample(samples *[]string, message string) {
	if len(*samples) >= 5 {
		return
	}
	compacted := compactDiscovery(message, 260)
	for _, existing := range *samples {
		if existing == compacted {
			return
		}
	}
	*samples = append(*samples, compacted)
}

func scoreCandidate(c *leadSourceCandidate) {
	noiseRatio := 0.0
	if c.HistoryMessages > 0 {
		noiseRatio = float64(c.HistoryNoise) / float64(c.HistoryMessages)
	}
	switch {
	case c.DevHits >= 2 && c.HistoryLeads >= 2 && noiseRatio <= 0.35:
		c.Recommendation = "JOIN_TOP"
		c.Reason = "несколько dev-сигналов и в свежей истории есть заявки"
	case c.DevHits >= 1 && c.HistoryLeads >= 1 && noiseRatio <= 0.45:
		c.Recommendation = "JOIN_TEST"
		c.Reason = "есть dev-сигнал, надо прогнать в теневом режиме"
	case c.BuyerHits >= 3 && c.HistoryLeads >= 2 && noiseRatio <= 0.45:
		c.Recommendation = "JOIN_NICHE"
		c.Reason = "заявки есть, но это скорее дизайн/маркетинг/маркетплейсы"
	case c.BuyerHits >= 1:
		c.Recommendation = "WATCH"
		c.Reason = "слабый сигнал, смотреть без приоритета"
	default:
		c.Recommendation = "SKIP"
		c.Reason = "мало живых покупательских заявок"
	}
}

func sourceScore(c *leadSourceCandidate) int {
	score := c.DevHits*8 + c.HistoryLeads*5 + c.DesignHits*3 + c.MarketingHits*3 + c.MarketplaceHits*2 + c.SearchHits
	score -= c.NoiseHits*3 + c.HistoryNoise
	if c.Recommendation == "JOIN_TOP" {
		score += 30
	}
	if c.Recommendation == "JOIN_TEST" {
		score += 15
	}
	return score
}

func writeDiscoveryReports(list []*leadSourceCandidate) error {
	reportDir := filepath.Join("..", "lidohod", "data", "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}
	date := time.Now().Format("2006-01-02")
	tsvPath := filepath.Join(reportDir, "lead_source_discovery_"+date+".tsv")
	htmlPath := filepath.Join(reportDir, "lead_source_discovery_"+date+".html")

	tsvFile, err := os.Create(tsvPath)
	if err != nil {
		return err
	}
	defer tsvFile.Close()
	w := csv.NewWriter(tsvFile)
	w.Comma = '\t'
	_ = w.Write([]string{
		"recommendation", "telegram_url", "title", "participants", "score", "dev_hits",
		"design_hits", "marketing_hits", "marketplace_hits", "history_leads",
		"history_messages", "history_noise", "queries", "reason", "samples",
	})
	for _, c := range list {
		_ = w.Write([]string{
			c.Recommendation,
			"https://t.me/" + c.Username,
			c.Title,
			strconv.Itoa(c.Participants),
			strconv.Itoa(sourceScore(c)),
			strconv.Itoa(c.DevHits),
			strconv.Itoa(c.DesignHits),
			strconv.Itoa(c.MarketingHits),
			strconv.Itoa(c.MarketplaceHits),
			strconv.Itoa(c.HistoryLeads),
			strconv.Itoa(c.HistoryMessages),
			strconv.Itoa(c.HistoryNoise),
			strings.Join(sortedKeys(c.QuerySet), ", "),
			c.Reason,
			strings.Join(append(c.Samples, c.HistorySamples...), " | "),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>Lead source discovery</title>")
	b.WriteString("<style>body{font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f6f7f9;color:#1f2937;margin:24px}.grid{display:grid;gap:14px}.card{background:white;border:1px solid #e5e7eb;border-radius:8px;padding:14px}.top{border-left:5px solid #16a34a}.test{border-left:5px solid #2563eb}.niche{border-left:5px solid #9333ea}.watch{border-left:5px solid #f59e0b}.skip{border-left:5px solid #9ca3af}code{background:#eef2ff;padding:2px 5px;border-radius:4px}.muted{color:#6b7280}.sample{white-space:pre-wrap;background:#f9fafb;padding:8px;border-radius:6px;margin-top:8px}</style>")
	b.WriteString("<h1>Discovery источников лидов " + html.EscapeString(date) + "</h1><div class=\"grid\">")
	for _, c := range list {
		class := strings.ToLower(strings.TrimPrefix(c.Recommendation, "JOIN_"))
		if c.Recommendation == "JOIN_TOP" {
			class = "top"
		} else if c.Recommendation == "JOIN_TEST" {
			class = "test"
		} else if c.Recommendation == "JOIN_NICHE" {
			class = "niche"
		}
		b.WriteString("<section class=\"card " + class + "\">")
		b.WriteString("<h2><a href=\"https://t.me/" + html.EscapeString(c.Username) + "\">@" + html.EscapeString(c.Username) + "</a> — " + html.EscapeString(c.Title) + "</h2>")
		b.WriteString("<p><b>" + html.EscapeString(c.Recommendation) + "</b> · " + strconv.Itoa(c.Participants) + " участников · score " + strconv.Itoa(sourceScore(c)) + "</p>")
		b.WriteString("<p class=\"muted\">dev " + strconv.Itoa(c.DevHits) + " · design " + strconv.Itoa(c.DesignHits) + " · marketing " + strconv.Itoa(c.MarketingHits) + " · marketplace " + strconv.Itoa(c.MarketplaceHits) + " · history " + strconv.Itoa(c.HistoryLeads) + "/" + strconv.Itoa(c.HistoryMessages) + " · noise " + strconv.Itoa(c.HistoryNoise) + "</p>")
		b.WriteString("<p>" + html.EscapeString(c.Reason) + "</p>")
		b.WriteString("<p class=\"muted\">queries: " + html.EscapeString(strings.Join(sortedKeys(c.QuerySet), ", ")) + "</p>")
		for _, sample := range append(c.Samples, c.HistorySamples...) {
			b.WriteString("<div class=\"sample\">" + html.EscapeString(sample) + "</div>")
		}
		b.WriteString("</section>")
	}
	b.WriteString("</div>")
	return os.WriteFile(htmlPath, []byte(b.String()), 0o644)
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compactDiscovery(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func existingUsernames() map[string]bool {
	names := []string{
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
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[strings.ToLower(name)] = true
	}
	return out
}
