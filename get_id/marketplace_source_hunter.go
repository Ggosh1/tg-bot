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

type mpCandidate struct {
	Username     string
	Title        string
	Participants int
	Queries      map[string]bool
}

type mpVerdict struct {
	Username     string
	Title        string
	Participants int
	Messages14d  int
	LastMessage  time.Time
	ProjectLeads int
	DesignLeads  int
	OpsLeads     int
	AdsLeads     int
	Jobs         int
	SelfPromo    int
	Trash        int
	Score        int
	Samples      []string
	Queries      []string
	Err          string
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

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	var verdicts []mpVerdict
	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		joinedUsernames, joinedIDs, err := mpCurrentDialogs(ctx, api)
		if err != nil {
			return err
		}

		candidates := map[string]*mpCandidate{}
		for _, q := range mpSourceQueries() {
			found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: 100})
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
				if joinedIDs[ch.ID] || joinedUsernames[strings.ToLower(ch.Username)] || mpBadSource(ch.Title, ch.Username) {
					continue
				}
				mpAddCandidate(candidates, ch, "src:"+q)
			}
			time.Sleep(850 * time.Millisecond)
		}

		minDate := int(time.Now().AddDate(0, 0, -45).Unix())
		for _, q := range mpMessageQueries() {
			found, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
				GroupsOnly: true,
				Q:          q,
				Filter:     &tg.InputMessagesFilterEmpty{},
				MinDate:    minDate,
				OffsetPeer: &tg.InputPeerEmpty{},
				Limit:      100,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "message query %q failed: %v\n", q, err)
				time.Sleep(3 * time.Second)
				continue
			}
			chats, messages := mpUnpackHistory(found)
			chatByID := map[int64]*tg.Channel{}
			for _, cc := range chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if joinedIDs[ch.ID] || joinedUsernames[strings.ToLower(ch.Username)] || mpBadSource(ch.Title, ch.Username) {
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
				mpAddCandidate(candidates, ch, "msg:"+q)
			}
			time.Sleep(1100 * time.Millisecond)
		}

		var list []*mpCandidate
		for _, c := range candidates {
			if c.Participants >= 150 {
				list = append(list, c)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].Queries) == len(list[j].Queries) {
				return list[i].Participants > list[j].Participants
			}
			return len(list[i].Queries) > len(list[j].Queries)
		})
		if len(list) > 220 {
			list = list[:220]
		}

		for _, c := range list {
			v := mpInspect(ctx, api, c)
			verdicts = append(verdicts, v)
			time.Sleep(600 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	mpWriteReports(verdicts)
	mpPrint(verdicts)
}

func mpSourceQueries() []string {
	return []string{
		"wildberries чат", "wildberries поставщики", "wildberries селлеры", "wildberries поставщик чат",
		"wb чат", "wb поставщики", "wb селлеры", "вб поставщики", "вб селлеры",
		"ozon чат", "ozon селлеры", "ozon поставщики", "озон селлеры", "озон поставщики",
		"маркетплейсы чат", "маркетплейсы поставщики", "маркетплейсы селлеры", "селлеры чат",
		"инфографика маркетплейсы", "инфографика wildberries", "карточки wildberries",
		"дизайнеры карточек", "чат поставщиков", "фулфилмент чат",
	}
}

func mpMessageQueries() []string {
	return []string{
		"кто умеет делать инфографику",
		"кто может помочь с инфографикой",
		"ищу дизайнера карточек",
		"нужен дизайнер карточек",
		"нужна инфографика",
		"сделать карточки wildberries",
		"сделать карточки wb",
		"переделать инфографику",
		"аудит карточки wb",
		"аудит карточки ozon",
		"настроить рекламу wb",
		"настроить рекламу wildberries",
		"настроить рекламу ozon",
		"seo карточки wb",
		"вывести карточку в топ",
		"нужен менеджер маркетплейсов",
		"настроить кабинет ozon",
		"исправить ошибки в карточках",
	}
}

func mpInspect(ctx context.Context, api *tg.Client, c *mpCandidate) mpVerdict {
	v := mpVerdict{
		Username:     c.Username,
		Title:        c.Title,
		Participants: c.Participants,
		Queries:      mpSortedKeys(c.Queries),
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: c.Username})
	if err != nil {
		v.Err = err.Error()
		return v
	}
	var ch *tg.Channel
	for _, cc := range resolved.Chats {
		if x, ok := cc.(*tg.Channel); ok && strings.EqualFold(x.Username, c.Username) {
			ch = x
			break
		}
	}
	if ch == nil || !ch.Megagroup {
		v.Err = "not accessible megagroup"
		return v
	}

	cutoff := int(time.Now().AddDate(0, 0, -14).Unix())
	offsetID := 0
	for page := 0; page < 10; page++ {
		h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			v.Err = err.Error()
			return v
		}
		_, messages := mpUnpackHistory(h)
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
			v.Messages14d++
			msgTime := time.Unix(int64(msg.Date), 0)
			if msgTime.After(v.LastMessage) {
				v.LastMessage = msgTime
			}
			text := msg.Message
			switch {
			case mpIsJob(text):
				v.Jobs++
				v.Trash++
			case mpIsSelfPromo(text):
				v.SelfPromo++
				v.Trash++
			case mpIsHardTrash(text):
				v.Trash++
			case mpIsProjectLead(text):
				v.ProjectLeads++
				switch mpLeadKind(text) {
				case "ads":
					v.AdsLeads++
				case "ops":
					v.OpsLeads++
				default:
					v.DesignLeads++
				}
				if len(v.Samples) < 5 {
					v.Samples = append(v.Samples, mpCompact(text, 260))
				}
			}
		}
		if oldest < cutoff || offsetID == 0 {
			break
		}
		time.Sleep(130 * time.Millisecond)
	}
	v.Score = v.ProjectLeads*20 + v.DesignLeads*3 + v.AdsLeads*4 + v.OpsLeads*2 - v.SelfPromo*2 - v.Jobs*3 - (v.Trash-v.SelfPromo-v.Jobs)*2
	if v.Messages14d >= 100 {
		v.Score += 8
	}
	if time.Since(v.LastMessage) <= 72*time.Hour {
		v.Score += 10
	}
	return v
}

func mpIsProjectLead(s string) bool {
	t := mpNormalize(s)
	if mpIsJob(t) || mpIsSelfPromo(t) || mpIsHardTrash(t) {
		return false
	}
	domain := []string{
		"wildberries", "wb", "вб", "вайлдбер", "ozon", "озон", "маркетплейс", "селлер",
		"карточк", "инфограф", "первый слайд", "обложк", "фбо", "фбс", "личный кабинет",
		"кабинет", "реклама", "продвиж", "seo", "сео", "позици", "выдач", "отзыв", "контент",
	}
	if !mpHasAny(t, domain) {
		return false
	}
	intents := []string{
		"кто может", "кто сможет", "кто умеет", "кто сделает", "кто поможет", "есть специалист",
		"нужен", "нужна", "нужно", "надо", "ищу", "ищем", "требуется",
		"помогите сделать", "помочь с", "настроить", "исправить", "переделать",
		"сделать", "создать", "аудит", "проконсультировать",
	}
	return mpHasAny(t, intents)
}

func mpLeadKind(s string) string {
	t := mpNormalize(s)
	if mpHasAny(t, []string{"реклама", "продвиж", "seo", "сео", "позици", "выдач", "топ"}) {
		return "ads"
	}
	if mpHasAny(t, []string{"менеджер", "кабинет", "лк", "отзыв", "фбо", "фбс", "поставка", "ошибк"}) {
		return "ops"
	}
	return "design"
}

func mpIsSelfPromo(s string) bool {
	t := mpNormalize(s)
	return mpHasAny(t, []string{
		"#помогу", "#услуги", "#резюме", "предлагаю услуги", "оказываю услуги", "помогу вам",
		"беру проекты", "беру заказы", "ищу клиентов", "ищу заказчиков", "мои кейсы", "портфолио",
		"мои работы", "я дизайнер", "я маркетолог", "я менеджер", "я селлер", "создаю карточки",
		"делаю инфографику", "оформлю карточки", "настрою рекламу", "готов помочь", "кому нужно",
		"кому еще нужно", "кому ещё нужно", "ищу того, кому нужно", "окошко появилось",
	})
}

func mpIsJob(s string) bool {
	t := mpNormalize(s)
	return mpHasAny(t, []string{
		"вакансия", "ищем в команду", "ищем сотрудника", "в штат", "полная занятость",
		"full-time", "full time", "оклад", "зарплата", "зп от", "резюме", "без опыта",
		"всему обучим", "на постоянной основе", "кандидат", "кандидатам", "от 18",
		"старше 18", "строго от 18", "график 5/2", "оформление по тк",
	})
}

func mpIsHardTrash(s string) bool {
	t := mpNormalize(s)
	return mpHasAny(t, []string{
		"kwork", "кворк", "купить контакт", "покупать контакт", "аукцион", "сделать ставку",
		"накрут", "отзывы авито", "usdt", "crypto", "крипто", "займ", "ставки", "казино",
		"подписаться на канал", "пришлите админу", "правила чата", "добро пожаловать",
		"дайджест чата", "подскажите пожалуйста", "кто знает", "как сделать", "как настроить",
	})
}

func mpBadSource(title, username string) bool {
	t := mpNormalize(title + " " + username)
	return mpHasAny(t, []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "crypto", "крипто", "заработок",
		"накрут", "ставки", "казино", "находки", "курьер", "доставка",
	})
}

func mpAddCandidate(candidates map[string]*mpCandidate, ch *tg.Channel, query string) {
	key := strings.ToLower(ch.Username)
	c := candidates[key]
	if c == nil {
		c = &mpCandidate{
			Username:     ch.Username,
			Title:        ch.Title,
			Participants: ch.ParticipantsCount,
			Queries:      map[string]bool{},
		}
		candidates[key] = c
	}
	c.Queries[query] = true
	if ch.ParticipantsCount > c.Participants {
		c.Participants = ch.ParticipantsCount
	}
}

func mpCurrentDialogs(ctx context.Context, api *tg.Client) (map[string]bool, map[int64]bool, error) {
	dialogs, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      500,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("get dialogs: %w", err)
	}
	usernames := map[string]bool{}
	ids := map[int64]bool{}
	var chats []tg.ChatClass
	switch d := dialogs.(type) {
	case *tg.MessagesDialogs:
		chats = d.Chats
	case *tg.MessagesDialogsSlice:
		chats = d.Chats
	default:
		return nil, nil, fmt.Errorf("unexpected dialogs type %T", d)
	}
	for _, cc := range chats {
		switch c := cc.(type) {
		case *tg.Chat:
			ids[c.ID] = true
		case *tg.Channel:
			ids[c.ID] = true
			if c.Username != "" {
				usernames[strings.ToLower(c.Username)] = true
			}
		case *tg.ChatForbidden:
			ids[c.ID] = true
		case *tg.ChannelForbidden:
			ids[c.ID] = true
		}
	}
	return usernames, ids, nil
}

func mpWriteReports(verdicts []mpVerdict) {
	now := time.Now().Format("2006-01-02")
	var b strings.Builder
	b.WriteString("recommendation\tusername\ttitle\tparticipants\tmessages_14d\tproject_leads\tleads_day\tdesign_leads\tops_leads\tads_leads\tjobs\tself_promo\ttrash\tlast_message\tscore\terror\tsamples\tqueries\n")
	for _, v := range verdicts {
		rec := "SKIP"
		if v.Err == "" && v.ProjectLeads >= 4 && v.Score > 0 {
			rec = "JOIN_NOW"
		} else if v.Err == "" && v.ProjectLeads >= 1 && v.Score > -20 {
			rec = "TEST"
		}
		samples := strings.ReplaceAll(strings.Join(v.Samples, " | "), "\t", " ")
		b.WriteString(fmt.Sprintf("%s\t%s\t%s\t%d\t%d\t%d\t%.3f\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%d\t%s\t%s\t%s\n",
			rec, v.Username, strings.ReplaceAll(v.Title, "\t", " "), v.Participants,
			v.Messages14d, v.ProjectLeads, float64(v.ProjectLeads)/14.0,
			v.DesignLeads, v.OpsLeads, v.AdsLeads, v.Jobs, v.SelfPromo, v.Trash,
			mpFormatTime(v.LastMessage), v.Score, strings.ReplaceAll(v.Err, "\t", " "), samples, strings.Join(v.Queries, ", ")))
	}
	_ = os.WriteFile("../lidohod/data/reports/marketplace_source_hunter_"+now+".tsv", []byte(b.String()), 0644)
}

func mpPrint(verdicts []mpVerdict) {
	sort.Slice(verdicts, func(i, j int) bool {
		if verdicts[i].Score == verdicts[j].Score {
			return verdicts[i].ProjectLeads > verdicts[j].ProjectLeads
		}
		return verdicts[i].Score > verdicts[j].Score
	})
	totalLeads := 0
	joinNow := 0
	test := 0
	for _, v := range verdicts {
		if v.Err != "" {
			continue
		}
		totalLeads += v.ProjectLeads
		if v.ProjectLeads >= 4 && v.Score > 0 {
			joinNow++
		} else if v.ProjectLeads >= 1 && v.Score > -20 {
			test++
		}
	}
	fmt.Printf("candidates=%d\tjoin_now=%d\ttest=%d\tproject14d=%d\tproject_day=%.2f\n\n", len(verdicts), joinNow, test, totalLeads, float64(totalLeads)/14.0)
	for _, v := range verdicts {
		if v.Err != "" || v.ProjectLeads == 0 {
			continue
		}
		rec := "SKIP"
		if v.ProjectLeads >= 4 && v.Score > 0 {
			rec = "JOIN_NOW"
		} else if v.ProjectLeads >= 1 && v.Score > -20 {
			rec = "TEST"
		}
		fmt.Printf("%s\t@%s\tleads14d=%d\tday=%.2f\tscore=%d\tmessages=%d\tself=%d\tjobs=%d\ttrash=%d\t%s\n",
			rec, v.Username, v.ProjectLeads, float64(v.ProjectLeads)/14.0, v.Score, v.Messages14d, v.SelfPromo, v.Jobs, v.Trash, v.Title)
		for _, s := range v.Samples {
			fmt.Printf("  - %s\n", s)
		}
	}
}

func mpUnpackHistory(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func mpSortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mpNormalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func mpHasAny(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

func mpCompact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func mpFormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
