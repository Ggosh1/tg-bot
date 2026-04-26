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

type qualityCandidate struct {
	Username     string
	Title        string
	Participants int
	Queries      map[string]bool
}

type qualityVerdict struct {
	Username     string
	Title        string
	Participants int
	Messages     int
	LastMessage  time.Time
	ProjectLeads int
	Trash        int
	SelfPromo    int
	Jobs         int
	BotGate      int
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

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		existingUsernames, existingIDs, err := currentDialogs(ctx, api)
		if err != nil {
			return err
		}

		candidates := map[string]*qualityCandidate{}
		for _, q := range qualityQueries() {
			found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: 100})
			if err != nil {
				fmt.Fprintf(os.Stderr, "query %q failed: %v\n", q, err)
				time.Sleep(time.Second)
				continue
			}
			for _, cc := range found.Chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if existingIDs[ch.ID] || existingUsernames[strings.ToLower(ch.Username)] {
					continue
				}
				if badSourceByName(ch.Title, ch.Username) {
					continue
				}
				key := strings.ToLower(ch.Username)
				c := candidates[key]
				if c == nil {
					c = &qualityCandidate{
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{},
					}
					candidates[key] = c
				}
				c.Queries[q] = true
				if ch.ParticipantsCount > c.Participants {
					c.Participants = ch.ParticipantsCount
				}
			}
			time.Sleep(650 * time.Millisecond)
		}
		minDate := int(time.Now().AddDate(0, 0, -45).Unix())
		for _, q := range hotMessageQueries() {
			res, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
				GroupsOnly: true,
				Q:          q,
				Filter:     &tg.InputMessagesFilterEmpty{},
				MinDate:    minDate,
				OffsetPeer: &tg.InputPeerEmpty{},
				Limit:      100,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "message query %q failed: %v\n", q, err)
				time.Sleep(2 * time.Second)
				continue
			}
			chats, messages := unpackQualityHistory(res)
			chatByID := map[int64]*tg.Channel{}
			for _, cc := range chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if existingIDs[ch.ID] || existingUsernames[strings.ToLower(ch.Username)] {
					continue
				}
				if badSourceByName(ch.Title, ch.Username) {
					continue
				}
				chatByID[ch.ID] = ch
			}
			for _, mc := range messages {
				msg, ok := mc.(*tg.Message)
				if !ok || msg.Post || strings.TrimSpace(msg.Message) == "" || !isProjectLead(msg.Message) {
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
				key := strings.ToLower(ch.Username)
				c := candidates[key]
				if c == nil {
					c = &qualityCandidate{
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{},
					}
					candidates[key] = c
				}
				c.Queries["msg:"+q] = true
				if ch.ParticipantsCount > c.Participants {
					c.Participants = ch.ParticipantsCount
				}
			}
			time.Sleep(900 * time.Millisecond)
		}
		for _, username := range qualitySeedUsernames() {
			if existingUsernames[strings.ToLower(username)] {
				continue
			}
			resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
			if err != nil {
				fmt.Fprintf(os.Stderr, "seed @%s failed: %v\n", username, err)
				time.Sleep(450 * time.Millisecond)
				continue
			}
			for _, cc := range resolved.Chats {
				ch, ok := cc.(*tg.Channel)
				if !ok || !ch.Megagroup || ch.Broadcast || ch.Username == "" || ch.Scam || ch.Fake {
					continue
				}
				if existingIDs[ch.ID] || existingUsernames[strings.ToLower(ch.Username)] || badSourceByName(ch.Title, ch.Username) {
					continue
				}
				key := strings.ToLower(ch.Username)
				if candidates[key] == nil {
					candidates[key] = &qualityCandidate{
						Username:     ch.Username,
						Title:        ch.Title,
						Participants: ch.ParticipantsCount,
						Queries:      map[string]bool{"seed": true},
					}
				}
			}
			time.Sleep(450 * time.Millisecond)
		}

		var verdicts []qualityVerdict
		for _, c := range candidates {
			v := inspectQuality(ctx, api, c)
			if v.Err == "" && qualityPass(v) {
				verdicts = append(verdicts, v)
			}
			time.Sleep(450 * time.Millisecond)
		}

		sort.Slice(verdicts, func(i, j int) bool {
			if verdicts[i].Score == verdicts[j].Score {
				if verdicts[i].ProjectLeads == verdicts[j].ProjectLeads {
					return verdicts[i].Participants > verdicts[j].Participants
				}
				return verdicts[i].ProjectLeads > verdicts[j].ProjectLeads
			}
			return verdicts[i].Score > verdicts[j].Score
		})

		for _, v := range verdicts {
			fmt.Printf("@%s\tscore=%d\tproject=%d/%d\ttrash=%d\tself=%d\tjobs=%d\tbotgate=%d\tparticipants=%d\tlast=%s\t%s\tqueries=%s\n",
				v.Username, v.Score, v.ProjectLeads, v.Messages, v.Trash, v.SelfPromo, v.Jobs, v.BotGate,
				v.Participants, v.LastMessage.Format("2006-01-02"), v.Title, strings.Join(v.Queries, ", "))
			for _, sample := range v.Samples {
				fmt.Printf("  - %s\n", sample)
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

func currentDialogs(ctx context.Context, api *tg.Client) (map[string]bool, map[int64]bool, error) {
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

func qualityQueries() []string {
	return []string{
		"кто может сделать сайт", "кто сделает сайт", "нужен сайт", "нужен лендинг",
		"кто настроит рекламу", "нужен таргетолог", "нужен маркетолог", "нужен smm",
		"нужен seo", "seo чат", "контекстная реклама чат", "яндекс директ чат",
		"нужен программист", "нужен разработчик", "нужен бот", "нужен парсер",
		"нужен дизайнер", "нужна инфографика", "дизайн заказчики",
		"предприниматели чат", "бизнес чат", "стартап чат", "нетворкинг предприниматели",
		"риэлторы чат", "бьюти бизнес чат", "строительный бизнес чат", "медицинский маркетинг чат",
		"tilda чат", "wordpress чат", "битрикс чат", "crm чат", "getcourse чат", "amocrm чат",
		"wildberries чат", "ozon чат", "маркетплейсы чат",
	}
}

func hotMessageQueries() []string {
	return []string{
		"кто может сделать сайт", "кто сделает сайт", "нужно сделать сайт",
		"нужен разработчик", "ищу разработчика", "нужен программист", "ищу программиста",
		"нужен фронтенд", "нужен backend", "нужен верстальщик",
		"нужен wordpress", "нужен битрикс", "нужен tilda", "нужен webflow",
		"нужен телеграм бот", "сделать телеграм бота", "нужен парсер",
		"нужно настроить crm", "настроить amoCRM", "настроить битрикс24",
		"нужен дизайнер", "ищу дизайнера", "нужна инфографика",
		"нужен таргетолог", "ищу таргетолога", "настроить директ", "настроить рекламу",
		"нужен seo", "ищу seo", "нужен маркетолог", "нужен smm",
		"ищу подрядчика", "ищу исполнителя", "есть задача сайт", "есть задача бот",
	}
}

func qualitySeedUsernames() []string {
	return []string{
		"getguru", "b24_chat", "b24help", "b24chat", "wpchat", "pressword_all",
		"chatbotyAI", "ai_hub_chat", "N8N_AI_Chat", "youtube_automatisation",
		"webflow_pro_chat", "webflow_club", "shopify_chat", "wildberries_service",
		"marketplace_network", "WBchat_postavshikov", "getcourse_online",
	}
}

func inspectQuality(ctx context.Context, api *tg.Client, c *qualityCandidate) qualityVerdict {
	v := qualityVerdict{
		Username:     c.Username,
		Title:        c.Title,
		Participants: c.Participants,
		Queries:      sortedQualityKeys(c.Queries),
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
	if ch == nil {
		v.Err = "not found"
		return v
	}
	if !ch.Megagroup {
		v.Err = "skip broadcast channel"
		return v
	}
	h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
		Limit: 160,
	})
	if err != nil {
		v.Err = err.Error()
		return v
	}
	_, messages := unpackQualityHistory(h)
	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.Message == "" || msg.Post {
			continue
		}
		v.Messages++
		msgTime := time.Unix(int64(msg.Date), 0)
		if msgTime.After(v.LastMessage) {
			v.LastMessage = msgTime
		}
		text := msg.Message
		switch {
		case isBotGate(text):
			v.BotGate++
			v.Trash++
		case isJob(text):
			v.Jobs++
			v.Trash++
		case isSelfPromo(text):
			v.SelfPromo++
			v.Trash++
		case isHardTrash(text):
			v.Trash++
		case isProjectLead(text):
			v.ProjectLeads++
			if len(v.Samples) < 5 {
				v.Samples = append(v.Samples, compactQuality(text, 240))
			}
		}
	}
	v.Score = qualityScore(v)
	return v
}

func unpackQualityHistory(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func qualityPass(v qualityVerdict) bool {
	if v.Messages < 35 || v.ProjectLeads < 2 {
		return false
	}
	if time.Since(v.LastMessage) > 14*24*time.Hour {
		return false
	}
	trashRatio := float64(v.Trash) / float64(v.Messages)
	selfRatio := float64(v.SelfPromo) / float64(v.Messages)
	if trashRatio > 0.32 || selfRatio > 0.18 || v.BotGate > 2 {
		return false
	}
	return v.Score > 0
}

func qualityScore(v qualityVerdict) int {
	score := v.ProjectLeads*12 - v.SelfPromo*5 - v.Jobs*7 - v.BotGate*12 - (v.Trash-v.SelfPromo-v.Jobs-v.BotGate)*3
	if v.Messages >= 80 {
		score += 10
	}
	if time.Since(v.LastMessage) <= 72*time.Hour {
		score += 10
	}
	if v.Participants >= 3000 {
		score += 6
	}
	return score
}

func isProjectLead(s string) bool {
	t := normalizeQuality(s)
	if isJob(t) || isSelfPromo(t) || isHardTrash(t) || isBotGate(t) {
		return false
	}
	if isNonLeadContent(t) {
		return false
	}
	domains := []string{
		"сайт", "лендинг", "интернет магазин", "интернет-магазин", "бот", "парсер", "скрипт",
		"разработ", "программист", "верст", "frontend", "backend", "api", "интеграц",
		"crm", "amocrm", "битрикс", "bitrix", "wordpress", "tilda", "getcourse", "геткурс",
		"дизайн", "дизайнер", "логотип", "баннер", "инфограф", "карточк", "презентац",
		"реклам", "директ", "таргет", "smm", "смм", "seo", "маркетолог", "контент",
		"маркетплейс", "wildberries", "wb", "ozon", "авито", "аналитик",
	}
	if !hasAnyQuality(t, domains) {
		return false
	}

	directBuyer := []string{
		"кто может", "кто сможет", "кто сделает", "кто умеет", "кто возьмется", "кто возьмётся",
		"есть задача", "есть проект", "ищу подрядчика", "ищу исполнителя", "нужен подрядчик",
		"нужен исполнитель", "посоветуйте специалиста", "порекомендуйте специалиста",
	}
	if hasAnyQuality(t, directBuyer) {
		return true
	}

	needPerson := []string{
		"нужен разработчик", "нужен программист", "нужен дизайнер", "нужен маркетолог",
		"нужен таргетолог", "нужен smm", "нужен seo", "нужен специалист",
		"нужна команда", "нужна помощь", "ищу разработчика", "ищу программиста",
		"ищу дизайнера", "ищу маркетолога", "ищу таргетолога", "ищу специалиста",
	}
	if hasAnyQuality(t, needPerson) {
		return true
	}

	needAction := []string{
		"нужно сделать", "надо сделать", "нужно создать", "надо создать",
		"нужно разработать", "надо разработать", "нужно настроить", "надо настроить",
		"нужно доработать", "надо доработать", "нужно перенести", "надо перенести",
		"нужно сверстать", "надо сверстать", "нужно интегрировать", "надо интегрировать",
	}
	return hasAnyQuality(t, needAction)
}

func isSelfPromo(s string) bool {
	t := normalizeQuality(s)
	phrases := []string{
		"#помогу", "#услуги", "#продам", "предлагаю свои услуги", "предлагаю услуги",
		"оказываю услуги", "помогаю бизнесу", "помогу вам", "беру проекты", "беру заказы",
		"ищу клиентов", "ищу заказчиков", "мои кейсы", "мои работы", "портфолио",
		"я дизайнер", "я маркетолог", "я таргетолог", "я разработчик", "я программист",
		"мы команда", "наша команда", "студия", "агентство полного цикла",
		"создаю сайты", "делаю сайты", "настрою рекламу", "разрабатываю",
		"возьму на smm", "возьму на ведение", "буду рада рассказать",
		"помогаем бизнесам", "готов помочь", "ищу удалённую подработку", "ищу удаленную подработку",
	}
	return hasAnyQuality(t, phrases)
}

func isJob(s string) bool {
	t := normalizeQuality(s)
	phrases := []string{
		"вакансия", "ищем в команду", "ищем сотрудника", "в штат", "полная занятость",
		"full-time", "full time", "оклад", "зарплата", "зп от", "резюме", "#резюме",
		"удаленная работа", "удалённая работа", "без опыта", "всему обучим",
		"график", "hr", "recruiter", "релокация",
	}
	return hasAnyQuality(t, phrases)
}

func isBotGate(s string) bool {
	t := normalizeQuality(s)
	phrases := []string{
		"купить контакт", "покупать контакты", "оплатить контакт", "через бота",
		"участвовать в аукционе", "сделать ставку", "кворк", "kwork",
	}
	return hasAnyQuality(t, phrases)
}

func isHardTrash(s string) bool {
	t := normalizeQuality(s)
	phrases := []string{
		"накрут", "отзывы", "пф", "usdt", "crypto", "крипто", "займ", "ставки",
		"казино", "casino", "интим", "эскорт", "быстрый заработок", "лёгкий заработок",
		"легкий заработок", "подработка для школьников", "возраст 14", "возраст 15",
		"авито отзывы", "удалённая работа для девушек", "удаленная работа для девушек",
		"добро пожаловать", "не нужно быть гением", "вокруг полно небольших бизнес-идей",
		"прайс на рекламу", "telegram media group", "подписаться на канал", "пришлите админу",
	}
	return hasAnyQuality(t, phrases)
}

func isNonLeadContent(t string) bool {
	phrases := []string{
		"дайджест чата", "ленивые новости", "что произошло", "на онлайн-конференции",
		"в прямом эфире", "разберем какие", "идея для видео", "почему это работает",
		"смотреть:", "youtube.com", "youtu.be", "собрали небольшую подборку",
		"вы когда-нибудь", "а вы знали", "по данным", "рынок растёт", "рынок растет",
		"ознакомьтесь с правилами", "если вам нужна помощь разработчиков",
	}
	return hasAnyQuality(t, phrases)
}

func badSourceByName(title, username string) bool {
	s := normalizeQuality(title + " " + username)
	bad := []string{
		"kwork", "кворк", "ваканс", "jobs", "job", "резюме", "hh", "работа москва",
		"crypto", "крипто", "отзывы", "накрут", "пф", "займ", "заработок",
	}
	return hasAnyQuality(s, bad)
}

func normalizeQuality(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func hasAnyQuality(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func sortedQualityKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compactQuality(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
