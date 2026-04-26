package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
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

type currentLead struct {
	At       time.Time
	Chat     string
	Category string
	Text     string
}

type currentChat struct {
	ID       int64
	Title    string
	Messages int
	Leads    int
}

func main() {
	_ = godotenv.Load()
	apiID, err := strconv.Atoi(os.Getenv("API_ID"))
	if err != nil {
		log.Fatalf("bad API_ID: %v", err)
	}
	parserIDs, err := readParserIDs("../lidohod/tg-parser/main.go")
	if err != nil {
		log.Fatal(err)
	}
	for _, id := range []int64{1816041970, 1071816041970} {
		delete(parserIDs, id)
	}

	cutoff48 := time.Now().Add(-48 * time.Hour)
	cutoff24 := time.Now().Add(-24 * time.Hour)
	client := telegram.NewClient(apiID, os.Getenv("API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: "./data/session.json"},
	})

	var leads []currentLead
	var chats []currentChat
	seen := map[string]bool{}

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()
		dialogs, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      500,
		})
		if err != nil {
			return err
		}
		for _, cc := range unpackDialogs(dialogs) {
			ch, ok := cc.(*tg.Channel)
			if !ok || !ch.Megagroup || !parserIDs[ch.ID] {
				continue
			}
			chat, found := inspectCurrentChat(ctx, api, ch, cutoff48, seen)
			chats = append(chats, chat)
			leads = append(leads, found...)
			time.Sleep(250 * time.Millisecond)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	byCat48 := map[string]int{}
	byCat24 := map[string]int{}
	byDay := map[string]map[string]int{}
	for _, lead := range leads {
		byCat48[lead.Category]++
		if lead.At.After(cutoff24) {
			byCat24[lead.Category]++
		}
		day := lead.At.In(time.FixedZone("MSK", 3*60*60)).Format("2006-01-02")
		if byDay[day] == nil {
			byDay[day] = map[string]int{}
		}
		byDay[day][lead.Category]++
	}

	cats := []string{"development", "design", "marketing_smm", "marketplace_ops", "other_project"}
	fmt.Println("=== Estimate for current parser chats ===")
	fmt.Printf("tracked_joined_chats=%d unique_leads_48h=%d projected_per_day=%.1f unique_leads_24h=%d\n",
		len(chats), len(leads), float64(len(leads))/2.0, sumMap(byCat24))
	fmt.Println("\n48h_by_category")
	for _, cat := range cats {
		fmt.Printf("%s\t%d\tper_day=%.1f\n", cat, byCat48[cat], float64(byCat48[cat])/2.0)
	}
	fmt.Println("\n24h_by_category")
	for _, cat := range cats {
		fmt.Printf("%s\t%d\n", cat, byCat24[cat])
	}
	fmt.Println("\nby_calendar_day_msk")
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		fmt.Printf("%s\ttotal=%d", day, sumMap(byDay[day]))
		for _, cat := range cats {
			if byDay[day][cat] > 0 {
				fmt.Printf("\t%s=%d", cat, byDay[day][cat])
			}
		}
		fmt.Println()
	}

	sort.Slice(chats, func(i, j int) bool {
		if chats[i].Leads == chats[j].Leads {
			return chats[i].Messages > chats[j].Messages
		}
		return chats[i].Leads > chats[j].Leads
	})
	fmt.Println("\ntop_chats_48h")
	for _, chat := range chats {
		if chat.Leads == 0 {
			continue
		}
		fmt.Printf("%d\tleads=%d\tmessages=%d\t%s\n", chat.ID, chat.Leads, chat.Messages, chat.Title)
	}

	sort.Slice(leads, func(i, j int) bool { return leads[i].At.After(leads[j].At) })
	var report strings.Builder
	report.WriteString("datetime_msk\tchat\tcategory\ttext\n")
	for _, lead := range leads {
		report.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n",
			lead.At.In(time.FixedZone("MSK", 3*60*60)).Format("2006-01-02 15:04"),
			safeTSV(lead.Chat),
			lead.Category,
			safeTSV(lead.Text),
		))
	}
	_ = os.WriteFile("../lidohod/data/reports/current_parser_estimate_48h.tsv", []byte(report.String()), 0644)
}

func readParserIDs(path string) (map[int64]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\b\d{6,}\b`)
	out := map[int64]bool{}
	for _, s := range re.FindAllString(string(raw), -1) {
		id, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			out[id] = true
		}
	}
	return out, nil
}

func unpackDialogs(dialogs tg.MessagesDialogsClass) []tg.ChatClass {
	switch d := dialogs.(type) {
	case *tg.MessagesDialogs:
		return d.Chats
	case *tg.MessagesDialogsSlice:
		return d.Chats
	default:
		return nil
	}
}

func inspectCurrentChat(ctx context.Context, api *tg.Client, ch *tg.Channel, cutoff time.Time, seen map[string]bool) (currentChat, []currentLead) {
	out := currentChat{ID: ch.ID, Title: ch.Title}
	var leads []currentLead
	offsetID := 0
	for page := 0; page < 12; page++ {
		h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			return out, leads
		}
		_, messages := unpackHistoryCurrent(h)
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
			at := time.Unix(int64(msg.Date), 0)
			if at.Before(oldest) {
				oldest = at
			}
			if at.Before(cutoff) {
				continue
			}
			out.Messages++
			cat, ok := classifyCurrent(msg.Message)
			if !ok {
				continue
			}
			key := fingerprintCurrent(cat + "|" + normalizeForDedup(msg.Message))
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Leads++
			leads = append(leads, currentLead{
				At:       at,
				Chat:     ch.Title,
				Category: cat,
				Text:     compactCurrent(msg.Message, 700),
			})
		}
		if offsetID == 0 || oldest.Before(cutoff) {
			break
		}
	}
	return out, leads
}

func unpackHistoryCurrent(res tg.MessagesMessagesClass) ([]tg.ChatClass, []tg.MessageClass) {
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

func classifyCurrent(text string) (string, bool) {
	t := strings.ToLower(text)
	if isRejectedCurrent(t) || !hasIntentCurrent(t) {
		return "", false
	}
	if hasAnyCurrent(t, []string{"дизайн", "дизайнер", "инфограф", "карточ", "логотип", "баннер", "figma", "фигма", "макет", "обложк", "превью"}) {
		return "design", true
	}
	if hasAnyCurrent(t, []string{"smm", "смм", "таргет", "реклама", "маркетолог", "seo", "директ", "контекст", "копирайт", "рилс", "reels"}) {
		return "marketing_smm", true
	}
	if hasAnyCurrent(t, []string{"менеджер wb", "менеджер вб", "менеджер озон", "ведение кабинета", "личного кабинета wb", "личного кабинета вб", "маркетплейс", "селлер"}) {
		return "marketplace_ops", true
	}
	if hasAnyCurrent(t, []string{"сайт", "лендинг", "бот", "парсер", "скрипт", "разработ", "доработ", "программист", "верстальщик", "backend", "frontend", "php", "python", "javascript", "react", "node", "bitrix", "битрикс", "wordpress", "вордпресс", "tilda", "тильд", "webflow", "shopify", "api", "crm", "интеграц", "автоматизац", "getcourse", "геткурс", "сервер", "деплой", "оплат"}) {
		return "development", true
	}
	if hasAnyCurrent(t, []string{"специалист", "помощник", "аналитик", "настроить", "сделать", "провести анализ"}) {
		return "other_project", true
	}
	return "", false
}

func hasIntentCurrent(t string) bool {
	return hasAnyCurrent(t, []string{"нужен", "нужна", "нужно", "ищу", "требуется", "кто может", "кто сделает", "посоветуйте", "подскажите", "есть задача", "есть проект", "надо", "помогите"})
}

func isRejectedCurrent(t string) bool {
	return hasAnyCurrent(t, []string{
		"ищу работу", "ищу заказы", "ищу заказчиков", "предлагаю услуги", "оказываю услуги",
		"помогу вам", "могу помочь", "создаю", "разработаю", "делаю сайты", "делаю ботов",
		"портфолио", "резюме", "cv", "вакансия", "в штат", "полный день", "офис", "зарплата",
		"зп от", "удаленная работа", "подработка", "быстро заработать", "отзывы есть",
		"без опыта", "анкета для заполнения", "строго от 18", "только от 18", "от 18 лет",
		"14+", "usdt", "p2p", "крипто", "казино", "ставки", "самовыкуп", "накрут",
	})
}

func hasAnyCurrent(t string, arr []string) bool {
	for _, x := range arr {
		if strings.Contains(t, x) {
			return true
		}
	}
	return false
}

func normalizeForDedup(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func fingerprintCurrent(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func compactCurrent(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func safeTSV(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
