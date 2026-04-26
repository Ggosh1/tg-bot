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

type nicheSpec struct {
	Name           string
	SourceQueries  []string
	MessageQueries []string
	DomainWords    []string
}

type nicheCandidate struct {
	Username     string
	Title        string
	Participants int
	Niches       map[string]bool
	Queries      map[string]bool
}

type nicheChatResult struct {
	Niche        string
	Username     string
	Title        string
	Participants int
	Messages14d  int
	ProjectLeads int
	Jobs         int
	SelfPromo    int
	Trash        int
	LastMessage  time.Time
	Samples      []string
	Err          string
}

type nicheTotals struct {
	Chats        int
	Accessible   int
	Messages14d  int
	ProjectLeads int
	Jobs         int
	SelfPromo    int
	Trash        int
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}
	apiHash := os.Getenv("API_HASH")
	if apiHash == "" {
		log.Fatal("API_HASH is empty")
	}

	specs := benchmarkNiches()
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	var results []nicheChatResult
	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		candidates := map[string]*nicheCandidate{}
		for _, spec := range specs {
			for _, q := range spec.SourceQueries {
				found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: 60})
				if err != nil {
					fmt.Fprintf(os.Stderr, "contacts query %q failed: %v\n", q, err)
					time.Sleep(2 * time.Second)
					continue
				}
				for _, cc := range found.Chats {
					ch, ok := cc.(*tg.Channel)
					if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
						continue
					}
					if badBenchmarkSource(ch.Title, ch.Username) {
						continue
					}
					addNicheCandidate(candidates, ch, spec.Name, "src:"+q)
				}
				time.Sleep(900 * time.Millisecond)
			}
		}

		minDate := int(time.Now().AddDate(0, 0, -30).Unix())
		for _, spec := range specs {
			for _, q := range spec.MessageQueries {
				found, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
					GroupsOnly: true,
					Q:          q,
					Filter:     &tg.InputMessagesFilterEmpty{},
					MinDate:    minDate,
					OffsetPeer: &tg.InputPeerEmpty{},
					Limit:      60,
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "message query %q failed: %v\n", q, err)
					time.Sleep(3 * time.Second)
					continue
				}
				chats, messages := unpackBenchmarkHistory(found)
				chatByID := map[int64]*tg.Channel{}
				for _, cc := range chats {
					ch, ok := cc.(*tg.Channel)
					if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
						continue
					}
					if badBenchmarkSource(ch.Title, ch.Username) {
						continue
					}
					chatByID[ch.ID] = ch
				}
				for _, mc := range messages {
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
					addNicheCandidate(candidates, ch, spec.Name, "msg:"+q)
				}
				time.Sleep(1200 * time.Millisecond)
			}
		}

		var list []*nicheCandidate
		for _, c := range candidates {
			if c.Participants >= 100 {
				list = append(list, c)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].Niches) == len(list[j].Niches) {
				return list[i].Participants > list[j].Participants
			}
			return len(list[i].Niches) > len(list[j].Niches)
		})
		if len(list) > 180 {
			list = list[:180]
		}

		for _, c := range list {
			for niche := range c.Niches {
				spec := findNicheSpec(specs, niche)
				if spec == nil {
					continue
				}
				res := inspectBenchmarkChat(ctx, api, c, *spec)
				results = append(results, res)
				time.Sleep(650 * time.Millisecond)
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	writeBenchmarkReports(results)
	printBenchmarkSummary(results)
}

func benchmarkNiches() []nicheSpec {
	return []nicheSpec{
		{
			Name: "marketplaces",
			SourceQueries: []string{
				"wildberries чат", "ozon чат", "селлеры чат", "маркетплейсы чат", "поставщики wildberries",
				"wb ozon чат", "карточки товаров чат", "инфографика маркетплейсы",
			},
			MessageQueries: []string{
				"нужен дизайнер карточек", "нужна инфографика", "кто сделает карточки wb",
				"нужен менеджер маркетплейсов", "нужен аудит карточки", "нужно настроить рекламу wb",
			},
			DomainWords: []string{
				"wildberries", "wb", "вайлдбер", "ozon", "озон", "маркетплейс", "карточк", "инфограф", "селлер",
				"личный кабинет", "лк", "seo карточ", "реклама wb", "реклама вб",
			},
		},
		{
			Name: "beauty",
			SourceQueries: []string{
				"бьюти чат", "салон красоты чат", "мастера красоты чат", "бьюти бизнес",
				"косметологи чат", "парикмахеры чат", "маникюр чат", "beauty business",
			},
			MessageQueries: []string{
				"нужен сайт салон красоты", "нужен smm салон красоты", "настроить рекламу салон красоты",
				"нужен бот для записи", "нужна crm салон красоты", "ищу таргетолога бьюти",
			},
			DomainWords: []string{
				"бьюти", "салон", "салона", "салону", "косметолог", "маникюр", "парикмах", "барбер",
				"запись клиентов", "онлайн запись", "yclients", "юclients", "beauty",
			},
		},
		{
			Name: "realtors",
			SourceQueries: []string{
				"риэлторы чат", "недвижимость чат", "агенты недвижимости", "риэлторский чат",
				"агентство недвижимости чат", "инвесторы недвижимость чат", "аренда недвижимость чат",
			},
			MessageQueries: []string{
				"нужен сайт риэлтор", "нужен сайт недвижимости", "настроить рекламу недвижимость",
				"нужна crm недвижимость", "нужен бот недвижимость", "ищу таргетолога недвижимость",
			},
			DomainWords: []string{
				"риэлтор", "недвижим", "агентство недвижимости", "застройщик", "объект", "квартира",
				"аренда", "продажа квартир", "ипотек",
			},
		},
		{
			Name: "legal",
			SourceQueries: []string{
				"юристы чат", "адвокаты чат", "юридический чат", "юридический бизнес",
				"юридические услуги чат", "правовой чат", "юристы предприниматели",
			},
			MessageQueries: []string{
				"нужен сайт юрист", "нужна реклама юрист", "настроить рекламу юрист",
				"нужен сайт адвокат", "нужна crm юрист", "нужен бот юридическая консультация",
			},
			DomainWords: []string{
				"юрист", "адвокат", "юридич", "правов", "закон", "договор", "банкротств",
				"консультац", "суд", "иск",
			},
		},
		{
			Name: "medical",
			SourceQueries: []string{
				"медицинский бизнес чат", "клиники чат", "врачи чат", "стоматологи чат",
				"частная медицина чат", "косметология клиника чат",
			},
			MessageQueries: []string{
				"нужен сайт клиника", "настроить рекламу клиника", "нужен сайт стоматология",
				"нужна crm клиника", "нужен бот запись клиника", "ищу smm клиника",
			},
			DomainWords: []string{
				"клиник", "врач", "стоматолог", "медицин", "пациент", "запись на прием",
				"запись на приём", "косметолог", "медицина",
			},
		},
		{
			Name: "local_business",
			SourceQueries: []string{
				"малый бизнес чат", "предприниматели чат", "бизнес чат", "общепит бизнес",
				"ресторанный бизнес чат", "кафе бизнес чат", "локальный бизнес чат",
			},
			MessageQueries: []string{
				"нужен сайт бизнес", "нужен сайт кафе", "настроить рекламу ресторан",
				"нужна crm бизнес", "нужен бот для записи", "ищу smm кафе",
			},
			DomainWords: []string{
				"бизнес", "предприним", "кафе", "ресторан", "общепит", "услуги", "клиенты",
				"заявки", "продажи", "локальный",
			},
		},
	}
}

func addNicheCandidate(candidates map[string]*nicheCandidate, ch *tg.Channel, niche, query string) {
	key := strings.ToLower(ch.Username)
	c := candidates[key]
	if c == nil {
		c = &nicheCandidate{
			Username:     ch.Username,
			Title:        ch.Title,
			Participants: ch.ParticipantsCount,
			Niches:       map[string]bool{},
			Queries:      map[string]bool{},
		}
		candidates[key] = c
	}
	c.Niches[niche] = true
	c.Queries[query] = true
	if ch.ParticipantsCount > c.Participants {
		c.Participants = ch.ParticipantsCount
	}
}

func inspectBenchmarkChat(ctx context.Context, api *tg.Client, c *nicheCandidate, spec nicheSpec) nicheChatResult {
	res := nicheChatResult{
		Niche:        spec.Name,
		Username:     c.Username,
		Title:        c.Title,
		Participants: c.Participants,
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: c.Username})
	if err != nil {
		res.Err = err.Error()
		return res
	}
	var ch *tg.Channel
	for _, cc := range resolved.Chats {
		if x, ok := cc.(*tg.Channel); ok && strings.EqualFold(x.Username, c.Username) {
			ch = x
			break
		}
	}
	if ch == nil || !ch.Megagroup {
		res.Err = "not accessible megagroup"
		return res
	}

	cutoff := int(time.Now().AddDate(0, 0, -14).Unix())
	offsetID := 0
	for page := 0; page < 8; page++ {
		h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			res.Err = err.Error()
			return res
		}
		_, messages := unpackBenchmarkHistory(h)
		if len(messages) == 0 {
			break
		}
		oldest := int(time.Now().Unix())
		for _, mc := range messages {
			msg, ok := mc.(*tg.Message)
			if !ok || msg.Message == "" || msg.Post {
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
			res.Messages14d++
			msgTime := time.Unix(int64(msg.Date), 0)
			if msgTime.After(res.LastMessage) {
				res.LastMessage = msgTime
			}
			text := msg.Message
			switch {
			case isBenchmarkJob(text):
				res.Jobs++
				res.Trash++
			case isBenchmarkSelfPromo(text):
				res.SelfPromo++
				res.Trash++
			case isBenchmarkHardTrash(text):
				res.Trash++
			case isBenchmarkProjectLead(text, spec):
				res.ProjectLeads++
				if len(res.Samples) < 4 {
					res.Samples = append(res.Samples, compactBenchmark(text, 260))
				}
			}
		}
		if oldest < cutoff || offsetID == 0 {
			break
		}
		time.Sleep(120 * time.Millisecond)
	}
	return res
}

func isBenchmarkProjectLead(s string, spec nicheSpec) bool {
	t := normalizeBenchmark(s)
	if isBenchmarkJob(t) || isBenchmarkSelfPromo(t) || isBenchmarkHardTrash(t) {
		return false
	}
	offerWords := []string{
		"сайт", "лендинг", "бот", "чат-бот", "чатбот", "парсер", "скрипт", "автоматизац",
		"crm", "amocrm", "amo", "битрикс", "bitrix", "интеграц", "форма", "заявки",
		"дизайн", "дизайнер", "логотип", "баннер", "инфограф", "карточк", "презентац",
		"реклам", "директ", "таргет", "smm", "смм", "seo", "маркетолог", "контент",
		"аналитик", "аналитика", "авито", "тильда", "tilda", "wordpress", "webflow",
	}
	if !hasAnyBenchmark(t, offerWords) {
		return false
	}
	nicheHit := hasAnyBenchmark(t, spec.DomainWords)

	intents := []string{
		"кто может", "кто сможет", "кто сделает", "кто умеет", "кто возьмется", "кто возьмётся",
		"нужно сделать", "надо сделать", "нужно создать", "надо создать", "нужно настроить",
		"надо настроить", "нужно доработать", "надо доработать", "нужен специалист",
		"нужен подрядчик", "нужен исполнитель", "ищу подрядчика", "ищу исполнителя",
		"ищу специалиста", "ищу дизайнера", "ищу маркетолога", "ищу таргетолога",
		"посоветуйте специалиста", "порекомендуйте специалиста", "есть задача", "есть проект",
		"нужна настройка", "нужна доработка", "нужна помощь с", "ищем подрядчика",
	}
	if !hasAnyBenchmark(t, intents) {
		return false
	}
	return nicheHit || spec.Name == "marketplaces" || spec.Name == "local_business"
}

func isBenchmarkSelfPromo(s string) bool {
	t := normalizeBenchmark(s)
	return hasAnyBenchmark(t, []string{
		"#помогу", "#услуги", "#резюме", "предлагаю услуги", "оказываю услуги", "помогу вам",
		"беру проекты", "беру заказы", "ищу клиентов", "ищу заказчиков", "мои кейсы", "портфолио",
		"я дизайнер", "я маркетолог", "я таргетолог", "я разработчик", "создаю сайты", "делаю сайты",
		"настрою рекламу", "разрабатываю", "наша команда", "мы команда", "студия", "агентство",
		"помогаем бизнес", "если есть задача", "кому нужно", "кому еще нужно", "кому ещё нужно",
		"ищу того, кому нужно", "окошко появилось", "мои работы", "пишите кому актуально",
	})
}

func isBenchmarkJob(s string) bool {
	t := normalizeBenchmark(s)
	return hasAnyBenchmark(t, []string{
		"вакансия", "ищем в команду", "ищем сотрудника", "в штат", "полная занятость",
		"full-time", "full time", "оклад", "зарплата", "зп от", "резюме", "без опыта",
		"всему обучим", "график 5/2", "hr", "recruiter", "релокация", "оформление по тк",
		"на постоянной основе", "в команду", "оплата за", "за каждый просмотр", "kpi",
		"кандидат", "кандидатам", "от 18", "старше 18", "строго от 18",
	})
}

func isBenchmarkHardTrash(s string) bool {
	t := normalizeBenchmark(s)
	return hasAnyBenchmark(t, []string{
		"kwork", "кворк", "купить контакт", "покупать контакт", "аукцион", "сделать ставку",
		"накрут", "отзывы", "usdt", "crypto", "крипто", "займ", "ставки", "казино", "интим",
		"быстрый заработок", "легкий заработок", "лёгкий заработок", "подработка для школьников",
		"подписаться на канал", "пришлите админу", "правила чата", "добро пожаловать",
		"дайджест чата", "подскажите", "кто знает", "как сделать", "как настроить",
	})
}

func badBenchmarkSource(title, username string) bool {
	t := normalizeBenchmark(title + " " + username)
	return hasAnyBenchmark(t, []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "crypto", "крипто",
		"заработок", "накрут", "отзывы", "ставки", "казино", "знакомства",
	})
}

func writeBenchmarkReports(results []nicheChatResult) {
	now := time.Now().Format("2006-01-02")
	var detail strings.Builder
	detail.WriteString("niche\tusername\ttitle\tparticipants\tmessages_14d\tproject_leads\tleads_per_day\tjobs\tself_promo\ttrash\tlast_message\terror\tsamples\n")
	for _, r := range results {
		leadPerDay := float64(r.ProjectLeads) / 14.0
		samples := strings.ReplaceAll(strings.Join(r.Samples, " | "), "\t", " ")
		detail.WriteString(fmt.Sprintf("%s\t%s\t%s\t%d\t%d\t%d\t%.3f\t%d\t%d\t%d\t%s\t%s\t%s\n",
			r.Niche, r.Username, strings.ReplaceAll(r.Title, "\t", " "), r.Participants,
			r.Messages14d, r.ProjectLeads, leadPerDay, r.Jobs, r.SelfPromo, r.Trash,
			formatBenchmarkTime(r.LastMessage), strings.ReplaceAll(r.Err, "\t", " "), samples))
	}
	_ = os.WriteFile("../lidohod/data/reports/niche_benchmark_"+now+".tsv", []byte(detail.String()), 0644)

	totals := calculateBenchmarkTotals(results)
	var summary strings.Builder
	summary.WriteString("niche\tchats\taccessible\tmessages_14d\tproject_leads\tclean_leads_per_day\tclean_leads_per_100_chats_day\tjobs\tself_promo\ttrash\n")
	var niches []string
	for niche := range totals {
		niches = append(niches, niche)
	}
	sort.Strings(niches)
	for _, niche := range niches {
		t := totals[niche]
		perDay := float64(t.ProjectLeads) / 14.0
		per100 := 0.0
		if t.Accessible > 0 {
			per100 = perDay / float64(t.Accessible) * 100.0
		}
		summary.WriteString(fmt.Sprintf("%s\t%d\t%d\t%d\t%d\t%.2f\t%.2f\t%d\t%d\t%d\n",
			niche, t.Chats, t.Accessible, t.Messages14d, t.ProjectLeads, perDay, per100,
			t.Jobs, t.SelfPromo, t.Trash))
	}
	_ = os.WriteFile("../lidohod/data/reports/niche_benchmark_summary_"+now+".tsv", []byte(summary.String()), 0644)
}

func printBenchmarkSummary(results []nicheChatResult) {
	totals := calculateBenchmarkTotals(results)
	type row struct {
		Niche  string
		Total  nicheTotals
		PerDay float64
		Per100 float64
	}
	var rows []row
	for niche, total := range totals {
		perDay := float64(total.ProjectLeads) / 14.0
		per100 := 0.0
		if total.Accessible > 0 {
			per100 = perDay / float64(total.Accessible) * 100.0
		}
		rows = append(rows, row{Niche: niche, Total: total, PerDay: perDay, Per100: per100})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Per100 > rows[j].Per100 })
	for _, r := range rows {
		fmt.Printf("%s\tchats=%d\taccessible=%d\tmessages14d=%d\tproject=%d\tper_day=%.2f\tper_100_chat_day=%.2f\tjobs=%d\tself=%d\ttrash=%d\n",
			r.Niche, r.Total.Chats, r.Total.Accessible, r.Total.Messages14d, r.Total.ProjectLeads,
			r.PerDay, r.Per100, r.Total.Jobs, r.Total.SelfPromo, r.Total.Trash)
	}

	fmt.Println("\nTop chats:")
	sort.Slice(results, func(i, j int) bool {
		if results[i].ProjectLeads == results[j].ProjectLeads {
			return results[i].Messages14d > results[j].Messages14d
		}
		return results[i].ProjectLeads > results[j].ProjectLeads
	})
	for _, r := range results {
		if r.ProjectLeads == 0 || r.Err != "" {
			continue
		}
		fmt.Printf("%s\t@%s\tproject=%d\tmessages=%d\t%s\n", r.Niche, r.Username, r.ProjectLeads, r.Messages14d, r.Title)
		for _, s := range r.Samples {
			fmt.Printf("  - %s\n", s)
		}
	}
}

func calculateBenchmarkTotals(results []nicheChatResult) map[string]nicheTotals {
	totals := map[string]nicheTotals{}
	for _, r := range results {
		t := totals[r.Niche]
		t.Chats++
		if r.Err == "" {
			t.Accessible++
		}
		t.Messages14d += r.Messages14d
		t.ProjectLeads += r.ProjectLeads
		t.Jobs += r.Jobs
		t.SelfPromo += r.SelfPromo
		t.Trash += r.Trash
		totals[r.Niche] = t
	}
	return totals
}

func findNicheSpec(specs []nicheSpec, name string) *nicheSpec {
	for i := range specs {
		if specs[i].Name == name {
			return &specs[i]
		}
	}
	return nil
}

func unpackBenchmarkHistory(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func normalizeBenchmark(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func hasAnyBenchmark(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

func compactBenchmark(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func formatBenchmarkTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
