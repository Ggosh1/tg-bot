package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	stateAwaitTopupAmount = "await_topup_amount"
	stateAwaitBidAmount   = "await_bid_amount"

	auctionStatusActive = "active"
	auctionStatusClosed = "closed"

	minTopupKopeks    = int64(10000)
	minBidKopeks      = int64(10000)
	minBidRaiseKopeks = int64(10000)
	signupBonusKopeks = int64(100000)
	auctionTTL        = 90 * time.Second
)

type Amount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type Confirmation struct {
	Type      string `json:"type"`
	ReturnURL string `json:"return_url"`
}

type PaymentRequest struct {
	Amount       Amount       `json:"amount"`
	Capture      bool         `json:"capture"`
	Confirmation Confirmation `json:"confirmation"`
	Description  string       `json:"description"`
}

type PaymentResponse struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Amount       Amount `json:"amount"`
	Confirmation struct {
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`
}

type YooKassaWebhook struct {
	Event  string          `json:"event"`
	Object PaymentResponse `json:"object"`
}

type LeadPayload struct {
	Text         string `json:"text"`
	Category     string `json:"category"`
	Subcategory  string `json:"subcategory,omitempty"`
	AIClass      string `json:"ai_class,omitempty"`
	AIReason     string `json:"ai_reason,omitempty"`
	ContactURL   string `json:"contact_url,omitempty"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	UserID       int64  `json:"user_id"`
	Date         int    `json:"date"`
	MessageID    int    `json:"message_id"`
	ChatID       int64  `json:"chat_id"`
	ChatUsername string `json:"chat_username,omitempty"`
}

type User struct {
	ChatID             int64 `gorm:"primaryKey;autoIncrement:false"`
	TrialAlertsUsed    int   `gorm:"not null;default:0"`
	TrialEndedNotified bool  `gorm:"not null;default:false"`
	BalanceKopeks      int64 `gorm:"not null;default:0"`
	HoldKopeks         int64 `gorm:"not null;default:0"`
	State              string
	StatePayload       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Subscriptions      []Subscription `gorm:"foreignKey:ChatID"`
}

type Subscription struct {
	ChatID     int64  `gorm:"primaryKey;autoIncrement:false"`
	CategoryID string `gorm:"primaryKey"`
	CreatedAt  time.Time
}

type PendingPayment struct {
	PaymentID    string `gorm:"primaryKey"`
	ChatID       int64  `gorm:"index"`
	AmountKopeks int64
	Purpose      string
	Status       string
	CreditedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Lead struct {
	ID           uint `gorm:"primaryKey"`
	ChatID       int64
	MessageID    int
	ChatUsername string
	CategoryID   string
	CategoryName string
	Subcategory  string
	TextFull     string `gorm:"type:text"`
	TextMasked   string `gorm:"type:text"`
	AIReason     string `gorm:"type:text"`
	ContactURL   string
	FirstName    string
	LastName     string
	Username     string
	UserID       int64
	Date         int
	SourceURL    string
	CreatedAt    time.Time
}

type Auction struct {
	ID                  uint `gorm:"primaryKey"`
	LeadID              uint `gorm:"uniqueIndex"`
	Status              string
	CurrentBidKopeks    int64
	CurrentBidderChatID int64
	ExpiresAt           *time.Time `gorm:"index"`
	StartedAt           *time.Time
	ClosedAt            *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type AuctionBid struct {
	ID        uint  `gorm:"primaryKey"`
	AuctionID uint  `gorm:"index"`
	ChatID    int64 `gorm:"index"`
	BidKopeks int64
	CreatedAt time.Time
}

type AuctionHold struct {
	ID         uint  `gorm:"primaryKey"`
	AuctionID  uint  `gorm:"uniqueIndex:idx_auction_hold"`
	ChatID     int64 `gorm:"uniqueIndex:idx_auction_hold"`
	HoldKopeks int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type BalanceLedger struct {
	ID           uint   `gorm:"primaryKey"`
	ChatID       int64  `gorm:"index"`
	AuctionID    *uint  `gorm:"index"`
	PaymentID    string `gorm:"index"`
	Kind         string
	AmountKopeks int64
	Meta         string
	CreatedAt    time.Time
}

type AppMeta struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

type bidPlacementResult struct {
	AuctionID    uint
	CurrentBid   int64
	ExpiresAt    time.Time
	Participants []int64
	LeadID       uint
	LeadSummary  string
}

type closeAuctionResult struct {
	Closed       bool
	Lead         Lead
	WinnerChatID int64
	WinningBid   int64
	LoserChatIDs []int64
	LeadSummary  string
}

type creditResult struct {
	Credited   bool
	Already    bool
	ChatID     int64
	Amount     int64
	NewBalance int64
}

var (
	ykShopID, ykSecretKey string
	ykClient              *http.Client
	DB                    *gorm.DB

	botMu       sync.RWMutex
	botInstance *tgbotapi.BotAPI

	categoryOrder = []string{
		"cat_it",
		"cat_design",
		"cat_marketplace",
		"cat_marketing",
	}

	catNames = map[string]string{
		"cat_it":          "Разработка / Деплой / Поддержка",
		"cat_design":      "Дизайн",
		"cat_marketplace": "Маркетплейсы (ведение)",
		"cat_marketing":   "Маркетинг и SMM",
	}

	subcategoryToCatID = map[string]string{
		"development":     "cat_it",
		"design":          "cat_design",
		"marketplace_ops": "cat_marketplace",
		"marketing_smm":   "cat_marketing",
	}

	reUsername = regexp.MustCompile(`(?i)@[\p{L}\d_]{4,}`)
	reURL      = regexp.MustCompile(`(?i)(https?://\S+|tg://\S+|t\.me/\S+)`)
	rePhone    = regexp.MustCompile(`(?i)\+?\d[\d\-\s\(\)]{8,}\d`)
	reEmail    = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	mskLoc     = time.FixedZone("MSK", 3*60*60)
)

func initDB() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "bot.db"
	}

	var err error
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Panic("db open failed:", err)
	}

	err = DB.AutoMigrate(
		&User{},
		&Subscription{},
		&PendingPayment{},
		&Lead{},
		&Auction{},
		&AuctionBid{},
		&AuctionHold{},
		&BalanceLedger{},
		&AppMeta{},
	)
	if err != nil {
		log.Panic("db migration failed:", err)
	}

	if err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_lead_chat_msg ON leads(chat_id, message_id);").Error; err != nil {
		log.Panic("create lead unique index failed:", err)
	}

	if err := runV2DataMigration(); err != nil {
		log.Panic("v2 data migration failed:", err)
	}
	if err := runSignupBonusMigration(); err != nil {
		log.Panic("signup bonus migration failed:", err)
	}

	log.Println("db connected:", dbPath)
}

func runV2DataMigration() error {
	const key = "monetization_v2_migrated"
	return DB.Transaction(func(tx *gorm.DB) error {
		var meta AppMeta
		err := tx.First(&meta, "key = ?", key).Error
		if err == nil && meta.Value == "1" {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if err := tx.Model(&User{}).Where("1 = 1").Updates(map[string]interface{}{
			"trial_alerts_used":    0,
			"trial_ended_notified": false,
			"balance_kopeks":       0,
			"hold_kopeks":          0,
			"state":                "",
			"state_payload":        "",
			"updated_at":           time.Now(),
		}).Error; err != nil {
			return err
		}

		if err := tx.Where("1 = 1").Delete(&PendingPayment{}).Error; err != nil {
			return err
		}

		meta = AppMeta{Key: key, Value: "1"}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&meta).Error; err != nil {
			return err
		}
		return nil
	})
}

func runSignupBonusMigration() error {
	const key = "signup_bonus_1000_migrated"
	return DB.Transaction(func(tx *gorm.DB) error {
		var meta AppMeta
		err := tx.First(&meta, "key = ?", key).Error
		if err == nil && meta.Value == "1" {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var users []User
		if err := tx.Find(&users).Error; err != nil {
			return err
		}

		now := time.Now()
		for _, u := range users {
			newBalance := u.BalanceKopeks + signupBonusKopeks
			if err := tx.Model(&User{}).
				Where("chat_id = ?", u.ChatID).
				Updates(map[string]interface{}{
					"balance_kopeks":       newBalance,
					"trial_alerts_used":    0,
					"trial_ended_notified": false,
					"updated_at":           now,
				}).Error; err != nil {
				return err
			}
			if err := addLedger(tx, u.ChatID, nil, "", "bonus_grant", signupBonusKopeks, "signup bonus 1000 RUB"); err != nil {
				return err
			}
		}

		meta = AppMeta{Key: key, Value: "1"}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&meta).Error; err != nil {
			return err
		}
		return nil
	})
}

func setBotInstance(bot *tgbotapi.BotAPI) {
	botMu.Lock()
	defer botMu.Unlock()
	botInstance = bot
}

func getBotInstance() *tgbotapi.BotAPI {
	botMu.RLock()
	defer botMu.RUnlock()
	return botInstance
}

func getUserProfile(chatID int64) (User, error) {
	var user User
	err := DB.Preload("Subscriptions").First(&user, "chat_id = ?", chatID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user = User{ChatID: chatID, BalanceKopeks: signupBonusKopeks}
		if err := DB.Create(&user).Error; err != nil {
			return User{}, err
		}
		_ = DB.Create(&BalanceLedger{
			ChatID:       chatID,
			Kind:         "bonus_grant",
			AmountKopeks: signupBonusKopeks,
			Meta:         "signup bonus 1000 RUB",
		}).Error
		err = DB.Preload("Subscriptions").First(&user, "chat_id = ?", chatID).Error
	}
	return user, err
}

func ensureUser(chatID int64) error {
	_, err := getUserProfile(chatID)
	return err
}

func resolveLeadCategoryID(lead LeadPayload) string {
	sub := strings.ToLower(strings.TrimSpace(lead.Subcategory))
	if catID, ok := subcategoryToCatID[sub]; ok {
		return catID
	}

	for id, name := range catNames {
		if name == lead.Category {
			return id
		}
	}

	raw := strings.ToLower(strings.TrimSpace(lead.Category))
	switch {
	case strings.Contains(raw, "it"), strings.Contains(raw, "разработ"):
		return "cat_it"
	case strings.Contains(raw, "дизайн"):
		return "cat_design"
	case strings.Contains(raw, "маркетплейс"):
		return "cat_marketplace"
	case strings.Contains(raw, "маркетинг"), strings.Contains(raw, "smm"):
		return "cat_marketing"
	}
	return ""
}

func categoryIcon(catID string) string {
	switch catID {
	case "cat_it":
		return "💻"
	case "cat_design":
		return "🎨"
	case "cat_marketplace":
		return "🛍"
	case "cat_marketing":
		return "📣"
	default:
		return "📌"
	}
}

func categoryButtonLabel(catID string, enabled bool) string {
	prefix := "⬜"
	if enabled {
		prefix = "✅"
	}
	return fmt.Sprintf("%s %s %s", prefix, categoryIcon(catID), catNames[catID])
}

func buildSourceMessageURL(chatUsername string, chatID int64, messageID int) string {
	if messageID <= 0 {
		return ""
	}
	chatUsername = strings.TrimPrefix(strings.TrimSpace(chatUsername), "@")
	if chatUsername != "" {
		return fmt.Sprintf("https://t.me/%s/%d", chatUsername, messageID)
	}
	if chatID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://t.me/c/%d/%d", telegramInternalChatID(chatID), messageID)
}

func telegramInternalChatID(chatID int64) int64 {
	if chatID < 0 {
		chatID = -chatID
	}
	const channelPrefix = int64(1000000000000)
	if chatID >= channelPrefix {
		return chatID - channelPrefix
	}
	return chatID
}

func maskLeadText(raw string) string {
	out := raw
	out = reURL.ReplaceAllString(out, "[ссылка скрыта]")
	out = reEmail.ReplaceAllString(out, "[email скрыт]")
	out = rePhone.ReplaceAllString(out, "[телефон скрыт]")
	out = reUsername.ReplaceAllString(out, "[контакт скрыт]")
	return strings.TrimSpace(out)
}

func saveLead(payload LeadPayload, catID string) (Lead, bool, error) {
	record := Lead{
		ChatID:       payload.ChatID,
		MessageID:    payload.MessageID,
		CategoryID:   catID,
		CategoryName: payload.Category,
		Subcategory:  payload.Subcategory,
		TextFull:     payload.Text,
		TextMasked:   maskLeadText(payload.Text),
		AIReason:     payload.AIReason,
		ContactURL:   payload.ContactURL,
		FirstName:    payload.FirstName,
		LastName:     payload.LastName,
		Username:     payload.Username,
		UserID:       payload.UserID,
		Date:         payload.Date,
		ChatUsername: payload.ChatUsername,
		SourceURL:    buildSourceMessageURL(payload.ChatUsername, payload.ChatID, payload.MessageID),
	}

	res := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "chat_id"}, {Name: "message_id"}},
		DoNothing: true,
	}).Create(&record)
	if res.Error != nil {
		return Lead{}, false, res.Error
	}

	if res.RowsAffected == 0 {
		var existing Lead
		if err := DB.First(&existing, "chat_id = ? AND message_id = ?", payload.ChatID, payload.MessageID).Error; err != nil {
			return Lead{}, false, err
		}
		return existing, false, nil
	}
	return record, true, nil
}

func formatRub(kopeks int64) string {
	sign := ""
	if kopeks < 0 {
		sign = "-"
		kopeks = -kopeks
	}
	rub := kopeks / 100
	kop := kopeks % 100
	return fmt.Sprintf("%s%d.%02d ₽", sign, rub, kop)
}

func maxBidFloor(currentBid int64) int64 {
	if currentBid > 0 {
		return currentBid + minBidRaiseKopeks
	}
	return minBidKopeks
}

func availableBalance(user User) int64 {
	avail := user.BalanceKopeks - user.HoldKopeks
	if avail < 0 {
		return 0
	}
	return avail
}

func sendMessage(chatID int64, text string, markup interface{}) error {
	bot := getBotInstance()
	if bot == nil {
		return errors.New("bot not initialized")
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	msg.DisableWebPagePreview = true
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	_, err := bot.Send(msg)
	return err
}

func sendTrialEndedCTA(chatID int64) {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💰 Пополнить баланс", "balance_topup_start"),
		),
	)
	_ = sendMessage(chatID, "Пробные 10 уведомлений использованы. Продолжайте участвовать в аукционах после пополнения баланса от 100 ₽.", markup)
}

func consumeTrialAlert(chatID int64) (bool, error) {
	var shouldNotify bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.First(&user, "chat_id = ?", chatID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				user = User{ChatID: chatID, BalanceKopeks: signupBonusKopeks}
				if err := tx.Create(&user).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}

		if user.TrialAlertsUsed >= 10 {
			return nil
		}
		user.TrialAlertsUsed++
		if user.TrialAlertsUsed >= 10 && !user.TrialEndedNotified {
			user.TrialEndedNotified = true
			shouldNotify = true
		}
		return tx.Model(&user).Updates(map[string]interface{}{
			"trial_alerts_used":    user.TrialAlertsUsed,
			"trial_ended_notified": user.TrialEndedNotified,
			"updated_at":           time.Now(),
		}).Error
	})
	return shouldNotify, err
}

func leadProfileURL(lead Lead) string {
	if lead.Username != "" {
		return fmt.Sprintf("https://t.me/%s", lead.Username)
	}
	return ""
}

func isHTTPURL(raw string) bool {
	return strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")
}

func formatLeadDateMSK(unixTs int) string {
	return time.Unix(int64(unixTs), 0).In(mskLoc).Format("15:04 02.01.2006 МСК")
}

func leadSummary(lead Lead) string {
	displayCategory := catNames[lead.CategoryID]
	if displayCategory == "" {
		displayCategory = lead.CategoryName
	}
	text := strings.Join(strings.Fields(lead.TextMasked), " ")
	runes := []rune(text)
	if len(runes) > 84 {
		text = string(runes[:84]) + "..."
	}
	if text == "" {
		text = "без текста"
	}
	return fmt.Sprintf("#%d [%s] %s", lead.ID, displayCategory, text)
}

func buildMaskedLeadMessage(lead Lead) string {
	displayCategory := catNames[lead.CategoryID]
	if displayCategory == "" {
		displayCategory = lead.CategoryName
	}
	timeStr := formatLeadDateMSK(lead.Date)
	text := fmt.Sprintf("🔥 <b>Новая заявка</b>\n\n📂 <b>Категория:</b> %s", html.EscapeString(displayCategory))
	if lead.Subcategory != "" {
		text += fmt.Sprintf("\n🏷 <b>Подкатегория:</b> %s", html.EscapeString(lead.Subcategory))
	}
	text += fmt.Sprintf("\n\n%s\n\n🔒 <b>Контакты и источник скрыты до завершения аукциона.</b>\n🕒 <i>%s</i>", html.EscapeString(lead.TextMasked), timeStr)
	if lead.AIReason != "" {
		text += fmt.Sprintf("\n\n🧠 <i>AI: %s</i>", html.EscapeString(lead.AIReason))
	}
	return text
}

func buildFullLeadMessage(lead Lead, winningBid int64) (string, string, string, string) {
	displayCategory := catNames[lead.CategoryID]
	if displayCategory == "" {
		displayCategory = lead.CategoryName
	}
	fullName := strings.TrimSpace(strings.TrimSpace(lead.FirstName + " " + lead.LastName))
	if fullName == "" {
		fullName = "Заказчик"
	}
	timeStr := formatLeadDateMSK(lead.Date)
	profileURL := leadProfileURL(lead)

	contactBlock := fmt.Sprintf("👤 <b>Контакт:</b>\nИмя: %s", html.EscapeString(fullName))
	if lead.Username != "" {
		contactBlock += fmt.Sprintf("\nUsername: @%s", html.EscapeString(lead.Username))
	} else if lead.UserID != 0 {
		contactBlock += fmt.Sprintf("\nUser ID: <code>%d</code>", lead.UserID)
	}
	if profileURL != "" {
		if isHTTPURL(profileURL) {
			contactBlock += fmt.Sprintf("\nПрофиль: <a href=\"%s\">Открыть профиль</a>", html.EscapeString(profileURL))
		} else {
			contactBlock += fmt.Sprintf("\nПрофиль: <code>%s</code>", html.EscapeString(profileURL))
		}
	}
	if lead.ContactURL != "" {
		if isHTTPURL(lead.ContactURL) {
			contactBlock += fmt.Sprintf("\nКонтакт из кнопки: <a href=\"%s\">Открыть контакт</a>", html.EscapeString(lead.ContactURL))
		} else {
			contactBlock += fmt.Sprintf("\nКонтакт из кнопки: <code>%s</code>", html.EscapeString(lead.ContactURL))
		}
	}
	if lead.SourceURL != "" {
		if isHTTPURL(lead.SourceURL) {
			contactBlock += fmt.Sprintf("\nИсточник: <a href=\"%s\">Открыть сообщение в чате</a>", html.EscapeString(lead.SourceURL))
		} else {
			contactBlock += fmt.Sprintf("\nИсточник: <code>%s</code>", html.EscapeString(lead.SourceURL))
		}
	}

	text := fmt.Sprintf(
		"🏆 <b>Аукцион выигран!</b>\n\n💸 Списано: <b>%s</b>\n\n📂 <b>Категория:</b> %s",
		formatRub(winningBid),
		html.EscapeString(displayCategory),
	)
	if lead.Subcategory != "" {
		text += fmt.Sprintf("\n🏷 <b>Подкатегория:</b> %s", html.EscapeString(lead.Subcategory))
	}
	text += fmt.Sprintf("\n\n%s\n\n%s\n\n🕒 <i>%s</i>", html.EscapeString(lead.TextFull), contactBlock, timeStr)
	return text, profileURL, lead.SourceURL, lead.ContactURL
}

func notifyBidRaised(participants []int64, leadID uint, bid int64, leadSummaryText string, raisedBy int64) {
	if len(participants) == 0 {
		return
	}
	text := fmt.Sprintf("📈 Ставка по лиду <b>%s</b> повышена до <b>%s</b>. Таймер перезапущен: 1:30.", html.EscapeString(leadSummaryText), formatRub(bid))
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬆️ Повысить ставку", fmt.Sprintf("auction_join:%d", leadID)),
		),
	)
	for _, chatID := range participants {
		if chatID == raisedBy {
			continue
		}
		_ = sendMessage(chatID, text, markup)
	}
}

func addLedger(tx *gorm.DB, chatID int64, auctionID *uint, paymentID, kind string, amount int64, meta string) error {
	entry := BalanceLedger{
		ChatID:       chatID,
		AuctionID:    auctionID,
		PaymentID:    paymentID,
		Kind:         kind,
		AmountKopeks: amount,
		Meta:         meta,
	}
	return tx.Create(&entry).Error
}

func placeBid(chatID int64, leadID uint, bidKopeks int64) (bidPlacementResult, error) {
	result := bidPlacementResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var lead Lead
		if err := tx.First(&lead, "id = ?", leadID).Error; err != nil {
			return errors.New("лид не найден")
		}

		var auction Auction
		err := tx.First(&auction, "lead_id = ?", leadID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			now := time.Now()
			expires := now.Add(auctionTTL)
			started := now
			auction = Auction{
				LeadID:    leadID,
				Status:    auctionStatusActive,
				ExpiresAt: &expires,
				StartedAt: &started,
			}
			if err := tx.Create(&auction).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		if auction.Status != auctionStatusActive {
			return errors.New("аукцион уже завершен")
		}
		if auction.ExpiresAt != nil && auction.ExpiresAt.Before(time.Now()) {
			return errors.New("аукцион уже завершен")
		}
		if bidKopeks < minBidKopeks {
			return fmt.Errorf("минимальная ставка %s", formatRub(minBidKopeks))
		}
		nextMinBid := minBidKopeks
		if auction.CurrentBidKopeks > 0 {
			nextMinBid = auction.CurrentBidKopeks + minBidRaiseKopeks
		}
		if bidKopeks < nextMinBid {
			return fmt.Errorf("минимально допустимая ставка сейчас %s (текущая %s, шаг %s)", formatRub(nextMinBid), formatRub(auction.CurrentBidKopeks), formatRub(minBidRaiseKopeks))
		}

		var bidder User
		if err := tx.First(&bidder, "chat_id = ?", chatID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				bidder = User{ChatID: chatID, BalanceKopeks: signupBonusKopeks}
				if err := tx.Create(&bidder).Error; err != nil {
					return err
				}
				if err := addLedger(tx, chatID, nil, "", "bonus_grant", signupBonusKopeks, "signup bonus 1000 RUB"); err != nil {
					return err
				}
			} else {
				return err
			}
		}

		var bidderHold AuctionHold
		oldHold := int64(0)
		if err := tx.First(&bidderHold, "auction_id = ? AND chat_id = ?", auction.ID, chatID).Error; err == nil {
			oldHold = bidderHold.HoldKopeks
		}

		additional := bidKopeks - oldHold
		if additional < 0 {
			additional = 0
		}
		avail := bidder.BalanceKopeks - bidder.HoldKopeks
		if avail < additional {
			return fmt.Errorf("недостаточно средств. Доступно %s", formatRub(avail))
		}

		if oldHold == 0 {
			bidderHold = AuctionHold{
				AuctionID:  auction.ID,
				ChatID:     chatID,
				HoldKopeks: bidKopeks,
			}
			if err := tx.Create(&bidderHold).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&bidderHold).Update("hold_kopeks", bidKopeks).Error; err != nil {
				return err
			}
		}

		if additional > 0 {
			bidder.HoldKopeks += additional
			if err := tx.Model(&bidder).Updates(map[string]interface{}{
				"hold_kopeks": bidder.HoldKopeks,
				"updated_at":  time.Now(),
			}).Error; err != nil {
				return err
			}
			if err := addLedger(tx, bidder.ChatID, &auction.ID, "", "hold_place", additional, "bid placed"); err != nil {
				return err
			}
		}

		prevBidder := auction.CurrentBidderChatID
		if prevBidder != 0 && prevBidder != chatID {
			var prevHold AuctionHold
			if err := tx.First(&prevHold, "auction_id = ? AND chat_id = ?", auction.ID, prevBidder).Error; err == nil {
				var prevUser User
				if err := tx.First(&prevUser, "chat_id = ?", prevBidder).Error; err == nil {
					prevUser.HoldKopeks -= prevHold.HoldKopeks
					if prevUser.HoldKopeks < 0 {
						prevUser.HoldKopeks = 0
					}
					if err := tx.Model(&prevUser).Updates(map[string]interface{}{
						"hold_kopeks": prevUser.HoldKopeks,
						"updated_at":  time.Now(),
					}).Error; err != nil {
						return err
					}
				}
				if err := tx.Delete(&prevHold).Error; err != nil {
					return err
				}
				if err := addLedger(tx, prevBidder, &auction.ID, "", "hold_release", prevHold.HoldKopeks, "outbid"); err != nil {
					return err
				}
			}
		}

		now := time.Now()
		expires := now.Add(auctionTTL)
		update := map[string]interface{}{
			"status":                 auctionStatusActive,
			"current_bid_kopeks":     bidKopeks,
			"current_bidder_chat_id": chatID,
			"expires_at":             expires,
			"updated_at":             now,
		}
		if auction.StartedAt == nil {
			update["started_at"] = now
		}
		if err := tx.Model(&auction).Updates(update).Error; err != nil {
			return err
		}

		bid := AuctionBid{
			AuctionID: auction.ID,
			ChatID:    chatID,
			BidKopeks: bidKopeks,
		}
		if err := tx.Create(&bid).Error; err != nil {
			return err
		}

		var participants []int64
		if err := tx.Model(&AuctionBid{}).Distinct("chat_id").Where("auction_id = ?", auction.ID).Pluck("chat_id", &participants).Error; err != nil {
			return err
		}

		result = bidPlacementResult{
			AuctionID:    auction.ID,
			CurrentBid:   bidKopeks,
			ExpiresAt:    expires,
			Participants: participants,
			LeadID:       lead.ID,
			LeadSummary:  leadSummary(lead),
		}
		return nil
	})
	return result, err
}

func closeAuction(auctionID uint) (closeAuctionResult, error) {
	result := closeAuctionResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var auction Auction
		if err := tx.First(&auction, "id = ?", auctionID).Error; err != nil {
			return err
		}
		if auction.Status != auctionStatusActive {
			return nil
		}
		if auction.ExpiresAt == nil || auction.ExpiresAt.After(time.Now()) {
			return nil
		}

		var lead Lead
		if err := tx.First(&lead, "id = ?", auction.LeadID).Error; err != nil {
			return err
		}

		now := time.Now()
		if err := tx.Model(&auction).Updates(map[string]interface{}{
			"status":     auctionStatusClosed,
			"closed_at":  now,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}

		var participants []int64
		if err := tx.Model(&AuctionBid{}).Distinct("chat_id").Where("auction_id = ?", auction.ID).Pluck("chat_id", &participants).Error; err != nil {
			return err
		}

		var holds []AuctionHold
		if err := tx.Where("auction_id = ?", auction.ID).Find(&holds).Error; err != nil {
			return err
		}

		winner := auction.CurrentBidderChatID
		winningBid := auction.CurrentBidKopeks
		winnerCaptured := false
		for _, hold := range holds {
			var user User
			if err := tx.First(&user, "chat_id = ?", hold.ChatID).Error; err != nil {
				continue
			}

			if hold.ChatID == winner {
				capture := winningBid
				if capture > hold.HoldKopeks {
					capture = hold.HoldKopeks
				}
				releaseExtra := hold.HoldKopeks - capture

				user.HoldKopeks -= hold.HoldKopeks
				if user.HoldKopeks < 0 {
					user.HoldKopeks = 0
				}
				user.BalanceKopeks -= capture
				if user.BalanceKopeks < 0 {
					user.BalanceKopeks = 0
				}
				if err := tx.Model(&user).Updates(map[string]interface{}{
					"hold_kopeks":    user.HoldKopeks,
					"balance_kopeks": user.BalanceKopeks,
					"updated_at":     time.Now(),
				}).Error; err != nil {
					return err
				}
				if capture > 0 {
					winnerCaptured = true
					if err := addLedger(tx, user.ChatID, &auction.ID, "", "hold_capture", capture, "auction won"); err != nil {
						return err
					}
				}
				if releaseExtra > 0 {
					if err := addLedger(tx, user.ChatID, &auction.ID, "", "hold_release", releaseExtra, "winner extra release"); err != nil {
						return err
					}
				}
			} else {
				user.HoldKopeks -= hold.HoldKopeks
				if user.HoldKopeks < 0 {
					user.HoldKopeks = 0
				}
				if err := tx.Model(&user).Updates(map[string]interface{}{
					"hold_kopeks": user.HoldKopeks,
					"updated_at":  time.Now(),
				}).Error; err != nil {
					return err
				}
				if hold.HoldKopeks > 0 {
					if err := addLedger(tx, user.ChatID, &auction.ID, "", "hold_release", hold.HoldKopeks, "auction lost"); err != nil {
						return err
					}
				}
			}
			if err := tx.Delete(&hold).Error; err != nil {
				return err
			}
		}

		if winner != 0 && winningBid > 0 && !winnerCaptured {
			var user User
			if err := tx.First(&user, "chat_id = ?", winner).Error; err == nil {
				capture := winningBid
				if capture > user.BalanceKopeks {
					capture = user.BalanceKopeks
				}
				user.BalanceKopeks -= capture
				if user.BalanceKopeks < 0 {
					user.BalanceKopeks = 0
				}
				if err := tx.Model(&user).Updates(map[string]interface{}{
					"balance_kopeks": user.BalanceKopeks,
					"updated_at":     time.Now(),
				}).Error; err != nil {
					return err
				}
				if capture > 0 {
					if err := addLedger(tx, user.ChatID, &auction.ID, "", "hold_capture", capture, "auction won fallback"); err != nil {
						return err
					}
				}
			}
		}

		losers := make([]int64, 0, len(participants))
		for _, chatID := range participants {
			if chatID != winner {
				losers = append(losers, chatID)
			}
		}

		result = closeAuctionResult{
			Closed:       true,
			Lead:         lead,
			WinnerChatID: winner,
			WinningBid:   winningBid,
			LoserChatIDs: losers,
			LeadSummary:  leadSummary(lead),
		}
		return nil
	})
	return result, err
}

func runAuctionCloser() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var auctionIDs []uint
		if err := DB.Model(&Auction{}).
			Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?", auctionStatusActive, time.Now()).
			Limit(20).
			Pluck("id", &auctionIDs).Error; err != nil {
			continue
		}
		for _, auctionID := range auctionIDs {
			res, err := closeAuction(auctionID)
			if err != nil || !res.Closed {
				continue
			}

			if res.WinnerChatID != 0 && res.WinningBid > 0 {
				text, profileURL, sourceURL, contactURL := buildFullLeadMessage(res.Lead, res.WinningBid)
				var buttons []tgbotapi.InlineKeyboardButton
				if isHTTPURL(profileURL) {
					buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonURL("👤 Профиль", profileURL))
				}
				if isHTTPURL(contactURL) {
					buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonURL("👤 Контакт", contactURL))
				}
				if isHTTPURL(sourceURL) {
					buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonURL("📨 Источник", sourceURL))
				}
				var markup interface{}
				if len(buttons) > 0 {
					markup = tgbotapi.NewInlineKeyboardMarkup(buttons)
				}
				if err := sendMessage(res.WinnerChatID, text, markup); err != nil {
					log.Printf("winner full lead send failed (with markup), chat=%d auction=%d lead=%d err=%v", res.WinnerChatID, auctionID, res.Lead.ID, err)
					if err2 := sendMessage(res.WinnerChatID, text, nil); err2 != nil {
						log.Printf("winner full lead send failed (plain), chat=%d auction=%d lead=%d err=%v", res.WinnerChatID, auctionID, res.Lead.ID, err2)
						fallback := fmt.Sprintf("🏆 Аукцион по лиду <b>%s</b> завершен. Списано: <b>%s</b>. Если полный текст не пришел, напишите /start и повторите попытку.",
							html.EscapeString(res.LeadSummary), formatRub(res.WinningBid))
						_ = sendMessage(res.WinnerChatID, fallback, nil)
					}
				}
			}

			for _, loserID := range res.LoserChatIDs {
				_ = sendMessage(loserID, fmt.Sprintf("⏱ Аукцион по лиду <b>%s</b> завершен. Победившая ставка: <b>%s</b>. Ваш холд разблокирован.", html.EscapeString(res.LeadSummary), formatRub(res.WinningBid)), nil)
			}
		}
	}
}

func setUserState(chatID int64, state, payload string) error {
	return DB.Model(&User{}).Where("chat_id = ?", chatID).Updates(map[string]interface{}{
		"state":         state,
		"state_payload": payload,
		"updated_at":    time.Now(),
	}).Error
}

func clearUserState(chatID int64) error {
	return setUserState(chatID, "", "")
}

func parseRubInput(raw string) (int64, error) {
	txt := strings.TrimSpace(raw)
	txt = strings.ReplaceAll(txt, "₽", "")
	txt = strings.ReplaceAll(txt, " ", "")
	if txt == "" {
		return 0, errors.New("пустая сумма")
	}
	n, err := strconv.ParseInt(txt, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * 100, nil
}

func createPayment(amountKopeks int64, description string) (string, string, error) {
	reqBody := PaymentRequest{
		Amount: Amount{
			Value:    fmt.Sprintf("%.2f", float64(amountKopeks)/100.0),
			Currency: "RUB",
		},
		Capture: true,
		Confirmation: Confirmation{
			Type:      "redirect",
			ReturnURL: os.Getenv("APP_RETURN_URL"),
		},
		Description: description,
	}
	jsonData, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "https://api.yookassa.ru/v3/payments", bytes.NewBuffer(jsonData))
	req.SetBasicAuth(ykShopID, ykSecretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotence-Key", fmt.Sprintf("topup-%d", time.Now().UnixNano()))

	resp, err := ykClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("yookassa status %d", resp.StatusCode)
	}

	var payResp PaymentResponse
	if err := json.Unmarshal(body, &payResp); err != nil {
		return "", "", err
	}
	if payResp.ID == "" || payResp.Confirmation.ConfirmationURL == "" {
		return "", "", errors.New("empty payment response")
	}
	return payResp.Confirmation.ConfirmationURL, payResp.ID, nil
}

func checkPaymentStatus(paymentID string) (PaymentResponse, error) {
	req, _ := http.NewRequest("GET", "https://api.yookassa.ru/v3/payments/"+paymentID, nil)
	req.SetBasicAuth(ykShopID, ykSecretKey)
	resp, err := ykClient.Do(req)
	if err != nil {
		return PaymentResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return PaymentResponse{}, fmt.Errorf("yookassa status %d", resp.StatusCode)
	}
	var payResp PaymentResponse
	if err := json.Unmarshal(body, &payResp); err != nil {
		return PaymentResponse{}, err
	}
	return payResp, nil
}

func creditTopupIfNeeded(paymentID string) (creditResult, error) {
	res := creditResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var pending PendingPayment
		if err := tx.First(&pending, "payment_id = ?", paymentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				res.Already = true
				return nil
			}
			return err
		}
		res.ChatID = pending.ChatID
		res.Amount = pending.AmountKopeks

		if pending.CreditedAt != nil {
			res.Already = true
			var user User
			if err := tx.First(&user, "chat_id = ?", pending.ChatID).Error; err == nil {
				res.NewBalance = user.BalanceKopeks
			}
			return nil
		}
		if pending.Purpose != "topup" {
			return errors.New("unsupported payment purpose")
		}

		var user User
		if err := tx.First(&user, "chat_id = ?", pending.ChatID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				user = User{ChatID: pending.ChatID, BalanceKopeks: signupBonusKopeks}
				if err := tx.Create(&user).Error; err != nil {
					return err
				}
				if err := addLedger(tx, pending.ChatID, nil, "", "bonus_grant", signupBonusKopeks, "signup bonus 1000 RUB"); err != nil {
					return err
				}
			} else {
				return err
			}
		}

		user.BalanceKopeks += pending.AmountKopeks
		if err := tx.Model(&user).Updates(map[string]interface{}{
			"balance_kopeks": user.BalanceKopeks,
			"updated_at":     time.Now(),
		}).Error; err != nil {
			return err
		}

		if err := addLedger(tx, user.ChatID, nil, pending.PaymentID, "topup", pending.AmountKopeks, "payment succeeded"); err != nil {
			return err
		}

		now := time.Now()
		if err := tx.Model(&pending).Updates(map[string]interface{}{
			"status":      "succeeded",
			"credited_at": &now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}

		res.Credited = true
		res.NewBalance = user.BalanceKopeks
		return nil
	})
	return res, err
}

func processTopupAmount(chatID int64, text string) {
	amountKopeks, err := parseRubInput(text)
	if err != nil || amountKopeks < minTopupKopeks {
		_ = sendMessage(chatID, "Введите сумму в рублях (целое число) не меньше 100.", nil)
		return
	}

	payURL, payID, err := createPayment(amountKopeks, "Пополнение баланса FindLead AI")
	if err != nil {
		_ = sendMessage(chatID, "Не удалось создать счет. Попробуйте позже.", nil)
		return
	}

	pending := PendingPayment{
		PaymentID:    payID,
		ChatID:       chatID,
		AmountKopeks: amountKopeks,
		Purpose:      "topup",
		Status:       "pending",
	}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&pending).Error; err != nil {
		_ = sendMessage(chatID, "Не удалось сохранить счет. Попробуйте позже.", nil)
		return
	}

	_ = clearUserState(chatID)
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("💳 Оплатить через ЮKassa", payURL)),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔄 Проверить статус оплаты", "check_pay:"+payID)),
	)
	_ = sendMessage(chatID, fmt.Sprintf("Счет создан на <b>%s</b>.", formatRub(amountKopeks)), markup)
}

func processBidAmount(chatID int64, user User, text string) {
	leadID64, err := strconv.ParseUint(strings.TrimSpace(user.StatePayload), 10, 64)
	if err != nil || leadID64 == 0 {
		_ = clearUserState(chatID)
		_ = sendMessage(chatID, "Не удалось определить аукцион. Нажмите «Участвовать в аукционе» снова.", nil)
		return
	}

	bidKopeks, err := parseRubInput(text)
	if err != nil || bidKopeks < minBidKopeks {
		_ = sendMessage(chatID, "Введите ставку целым числом в рублях. Минимум 100 ₽, шаг повышения 100 ₽.", nil)
		return
	}

	placement, err := placeBid(chatID, uint(leadID64), bidKopeks)
	if err != nil {
		_ = sendMessage(chatID, fmt.Sprintf("Ставка не принята: %s", html.EscapeString(err.Error())), nil)
		return
	}

	_ = clearUserState(chatID)
	_ = sendMessage(chatID, fmt.Sprintf("✅ Ставка <b>%s</b> принята по лиду <b>%s</b>. Таймер: <b>1:30</b>.", formatRub(placement.CurrentBid), html.EscapeString(placement.LeadSummary)), nil)
	notifyBidRaised(placement.Participants, placement.LeadID, placement.CurrentBid, placement.LeadSummary, chatID)
}

func clearCallbackInlineKeyboard(query *tgbotapi.CallbackQuery) {
	bot := getBotInstance()
	if bot == nil || query == nil || query.Message == nil {
		return
	}
	empty := tgbotapi.InlineKeyboardMarkup{InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{}}
	edit := tgbotapi.NewEditMessageReplyMarkup(query.Message.Chat.ID, query.Message.MessageID, empty)
	_, _ = bot.Request(edit)
}

func showCategories(chatID int64) {
	user, err := getUserProfile(chatID)
	if err != nil {
		_ = sendMessage(chatID, "Ошибка загрузки категорий.", nil)
		return
	}
	subs := map[string]bool{}
	for _, sub := range user.Subscriptions {
		subs[sub.CategoryID] = true
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(categoryOrder))
	for _, catID := range categoryOrder {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(categoryButtonLabel(catID, subs[catID]), "cat_toggle:"+catID),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("💰 Баланс", "balance_show"),
	))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	_ = sendMessage(chatID, "📋 Выберите категории для получения masked-заявок:", markup)
}

func showMyCategories(chatID int64) {
	user, err := getUserProfile(chatID)
	if err != nil {
		_ = sendMessage(chatID, "Ошибка загрузки профиля.", nil)
		return
	}
	subs := map[string]bool{}
	for _, sub := range user.Subscriptions {
		subs[sub.CategoryID] = true
	}

	lines := []string{"⭐ <b>Мои категории:</b>"}
	enabled := 0
	for _, catID := range categoryOrder {
		if subs[catID] {
			lines = append(lines, fmt.Sprintf("✅ %s", catNames[catID]))
			enabled++
		}
	}
	if enabled == 0 {
		lines = append(lines, "Пока ничего не выбрано.")
	}

	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("💰 Доступно: <b>%s</b>", formatRub(availableBalance(user))))
	_ = sendMessage(chatID, strings.Join(lines, "\n"), nil)
}

func showBalance(chatID int64) {
	user, err := getUserProfile(chatID)
	if err != nil {
		_ = sendMessage(chatID, "Ошибка загрузки баланса.", nil)
		return
	}
	text := fmt.Sprintf(
		"💰 <b>Баланс</b>\n\nДоступно: <b>%s</b>\nВ холде: <b>%s</b>\nИтого: <b>%s</b>",
		formatRub(availableBalance(user)),
		formatRub(user.HoldKopeks),
		formatRub(user.BalanceKopeks),
	)
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Пополнить", "balance_topup_start"),
		),
	)
	_ = sendMessage(chatID, text, markup)
}

func isMainMenuAction(text string) bool {
	switch text {
	case "🗂 Категории", "⭐ Мои категории", "💰 Баланс", "ℹ️ Помощь":
		return true
	default:
		return false
	}
}

func handlePrivateMessage(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	if err := ensureUser(chatID); err != nil {
		return
	}

	if message.IsCommand() && message.Command() == "start" {
		keyboard := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("🗂 Категории"),
				tgbotapi.NewKeyboardButton("⭐ Мои категории"),
			),
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("💰 Баланс"),
				tgbotapi.NewKeyboardButton("ℹ️ Помощь"),
			),
		)
		keyboard.ResizeKeyboard = true
		_ = sendMessage(chatID, fmt.Sprintf("🚀 <b>FindLead AI</b>\nПолучайте masked-заявки и участвуйте в аукционе за контакты.\n\n🎁 Стартовый баланс: <b>%s</b>.", formatRub(signupBonusKopeks)), keyboard)
		return
	}

	user, err := getUserProfile(chatID)
	if err == nil {
		if user.State != "" && isMainMenuAction(message.Text) {
			_ = clearUserState(chatID)
		}
		switch user.State {
		case stateAwaitTopupAmount:
			if isMainMenuAction(message.Text) {
				break
			}
			processTopupAmount(chatID, message.Text)
			return
		case stateAwaitBidAmount:
			if isMainMenuAction(message.Text) {
				break
			}
			processBidAmount(chatID, user, message.Text)
			return
		}
	}

	switch message.Text {
	case "🗂 Категории":
		showCategories(chatID)
	case "⭐ Мои категории":
		showMyCategories(chatID)
	case "💰 Баланс":
		showBalance(chatID)
	case "ℹ️ Помощь":
		helpText := "🔍 <b>Как работает новая модель</b>\n\n" +
			"1. Вы выбираете категории бесплатно.\n" +
			"2. Получаете masked-заявки (контакты скрыты).\n" +
			"3. Нажимаете «Участвовать в аукционе», ставите сумму от 100 ₽.\n" +
			"4. Если 1:30 никто не перебил, вы получаете полный контакт и источник.\n\n" +
			fmt.Sprintf("🎁 Стартовый баланс: <b>%s</b>.\n", formatRub(signupBonusKopeks)) +
			"💳 Пополнение: от 100 ₽ через ЮKassa."
		_ = sendMessage(chatID, helpText, nil)
	}
}

func handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	_ = ensureUser(chatID)
	bot := getBotInstance()
	if bot != nil {
		_, _ = bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}

	switch {
	case strings.HasPrefix(query.Data, "cat_toggle:"):
		catID := strings.TrimPrefix(query.Data, "cat_toggle:")
		if catNames[catID] == "" {
			return
		}
		var existing Subscription
		err := DB.First(&existing, "chat_id = ? AND category_id = ?", chatID, catID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = DB.Create(&Subscription{ChatID: chatID, CategoryID: catID}).Error
			_ = sendMessage(chatID, fmt.Sprintf("✅ Подписка включена: %s", catNames[catID]), nil)
		} else if err == nil {
			_ = DB.Delete(&existing).Error
			_ = sendMessage(chatID, fmt.Sprintf("❌ Подписка отключена: %s", catNames[catID]), nil)
		}
		showCategories(chatID)

	case query.Data == "balance_show":
		showBalance(chatID)

	case query.Data == "balance_topup_start":
		_ = setUserState(chatID, stateAwaitTopupAmount, "")
		_ = sendMessage(chatID, "Введите сумму пополнения в рублях (целое число), минимум 100.", nil)

	case strings.HasPrefix(query.Data, "check_pay:"):
		paymentID := strings.TrimPrefix(query.Data, "check_pay:")
		var pending PendingPayment
		if err := DB.First(&pending, "payment_id = ? AND chat_id = ?", paymentID, chatID).Error; err != nil {
			_ = sendMessage(chatID, "Платеж не найден.", nil)
			return
		}
		payResp, err := checkPaymentStatus(paymentID)
		if err != nil {
			_ = sendMessage(chatID, "Ошибка проверки платежа.", nil)
			return
		}
		_ = DB.Model(&pending).Update("status", payResp.Status).Error

		if payResp.Status == "succeeded" {
			credit, err := creditTopupIfNeeded(paymentID)
			if err != nil {
				_ = sendMessage(chatID, "Ошибка зачисления платежа.", nil)
				return
			}
			if credit.Credited {
				_ = sendMessage(chatID, fmt.Sprintf("🎉 Оплата прошла успешно. Зачислено: <b>%s</b>\nНовый баланс: <b>%s</b>.", formatRub(credit.Amount), formatRub(credit.NewBalance)), nil)
			} else {
				_ = sendMessage(chatID, "Платеж уже зачислен.", nil)
			}
			return
		}
		_ = sendMessage(chatID, fmt.Sprintf("Платеж пока в статусе: <b>%s</b>.", html.EscapeString(payResp.Status)), nil)

	case strings.HasPrefix(query.Data, "auction_join:"):
		leadIDRaw := strings.TrimPrefix(query.Data, "auction_join:")
		leadID64, err := strconv.ParseUint(leadIDRaw, 10, 64)
		if err != nil || leadID64 == 0 {
			_ = sendMessage(chatID, "Ошибка: лид не найден.", nil)
			return
		}

		var lead Lead
		if err := DB.First(&lead, "id = ?", uint(leadID64)).Error; err != nil {
			_ = sendMessage(chatID, "Этот лид недоступен.", nil)
			return
		}

		var auction Auction
		current := int64(0)
		if err := DB.First(&auction, "lead_id = ?", lead.ID).Error; err == nil {
			if auction.Status != auctionStatusActive {
				clearCallbackInlineKeyboard(query)
				_ = clearUserState(chatID)
				_ = sendMessage(chatID, fmt.Sprintf("✅ Аукцион по лиду <b>%s</b> уже завершен. Кнопка отключена.", html.EscapeString(leadSummary(lead))), nil)
				return
			}
			if auction.ExpiresAt != nil && !auction.ExpiresAt.After(time.Now()) {
				_, _ = closeAuction(auction.ID)
				clearCallbackInlineKeyboard(query)
				_ = clearUserState(chatID)
				_ = sendMessage(chatID, fmt.Sprintf("✅ Аукцион по лиду <b>%s</b> уже завершен. Кнопка отключена.", html.EscapeString(leadSummary(lead))), nil)
				return
			}
			current = auction.CurrentBidKopeks
		}

		user, err := getUserProfile(chatID)
		if err != nil {
			_ = sendMessage(chatID, "Ошибка профиля.", nil)
			return
		}

		_ = setUserState(chatID, stateAwaitBidAmount, strconv.FormatUint(leadID64, 10))
		text := fmt.Sprintf(
			"💸 <b>Аукцион по лиду %s</b>\nТекущая ставка: <b>%s</b>\nСледующая допустимая ставка: <b>%s</b>\nДоступно: <b>%s</b>\n\nВведите вашу ставку в рублях.",
			html.EscapeString(leadSummary(lead)),
			formatRub(current),
			formatRub(maxBidFloor(current)),
			formatRub(availableBalance(user)),
		)
		_ = sendMessage(chatID, text, nil)
	}
}

func handleNewLeadHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var payload LeadPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	catID := resolveLeadCategoryID(payload)
	if catID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	lead, created, err := saveLead(payload, catID)
	if err != nil {
		http.Error(w, "lead save failed", http.StatusInternalServerError)
		return
	}
	if !created {
		w.WriteHeader(http.StatusOK)
		return
	}

	var targetIDs []int64
	if err := DB.Model(&Subscription{}).Where("category_id = ?", catID).Pluck("chat_id", &targetIDs).Error; err != nil {
		http.Error(w, "targets query failed", http.StatusInternalServerError)
		return
	}
	if len(targetIDs) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	sort.Slice(targetIDs, func(i, j int) bool { return targetIDs[i] < targetIDs[j] })

	for _, chatID := range targetIDs {
		msgText := buildMaskedLeadMessage(lead)
		markup := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💸 Участвовать в аукционе", fmt.Sprintf("auction_join:%d", lead.ID)),
			),
		)
		if err := sendMessage(chatID, msgText, markup); err != nil {
			continue
		}
	}

	w.WriteHeader(http.StatusOK)
}

func handleYooKassaWebhookHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	defer r.Body.Close()

	var hook YooKassaWebhook
	if err := json.Unmarshal(body, &hook); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if hook.Object.ID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if hook.Event != "payment.succeeded" && hook.Object.Status != "succeeded" {
		w.WriteHeader(http.StatusOK)
		return
	}

	res, err := creditTopupIfNeeded(hook.Object.ID)
	if err == nil && res.Credited {
		_ = sendMessage(res.ChatID, fmt.Sprintf("🎉 Оплата прошла успешно. Зачислено: <b>%s</b>\nНовый баланс: <b>%s</b>.", formatRub(res.Amount), formatRub(res.NewBalance)), nil)
	}

	w.WriteHeader(http.StatusOK)
}

func startHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/new_lead", handleNewLeadHTTP)
	mux.HandleFunc("/payments/yookassa/webhook", handleYooKassaWebhookHTTP)

	log.Println("http server started on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Println("http server stopped:", err)
	}
}

func main() {
	_ = godotenv.Load()
	initDB()

	ykShopID = os.Getenv("YK_SHOP_ID")
	ykSecretKey = os.Getenv("YK_SECRET_KEY")
	ykInsecure := os.Getenv("YK_INSECURE_SSL") == "1"

	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	if ykInsecure {
		customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	ykClient = &http.Client{Transport: customTransport, Timeout: 12 * time.Second}

	botToken := os.Getenv("BOT_TOKEN")
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}
	setBotInstance(bot)
	log.Printf("bot authorized: %s", bot.Self.UserName)

	go startHTTPServer()
	go runAuctionCloser()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery != nil {
			handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message != nil && update.Message.Chat.IsPrivate() {
			handlePrivateMessage(update.Message)
		}
	}
}
