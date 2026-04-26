package parser

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"lidohod/tg-parser/domain"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

const (
	AITunnelURL = "https://api.aitunnel.ru/v1/chat/completions"
)

var (
	reContactUsername = regexp.MustCompile(`(?i)@[\p{L}\d_]{4,}`)
	reContactURL      = regexp.MustCompile(`(?i)(https?://\S+|tg://\S+|t\.me/\S+)`)
	reContactPhone    = regexp.MustCompile(`(?i)\+?\d[\d\-\s\(\)]{8,}\d`)
	reContactEmail    = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
)

type GPTRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GPTResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type AIClassification struct {
	Class    string `json:"class"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
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

type Service struct {
	logger        *zap.Logger
	categories    []*domain.Category
	backendURL    string
	apiKey        string
	httpClient    *http.Client
	audit         *AuditStore
	excludedChats map[int64]bool
	dedupWindow   time.Duration
	dedupMu       sync.Mutex
	dedupSeen     map[string]time.Time
}

func NewService(
	logger *zap.Logger,
	categories []*domain.Category,
	backendURL string,
	apiKey string,
	reportsDir string,
	excludedChatIDs []int64,
	dedupWindow time.Duration,
) *Service {
	excluded := make(map[int64]bool, len(excludedChatIDs))
	for _, id := range excludedChatIDs {
		excluded[id] = true
	}
	if dedupWindow <= 0 {
		dedupWindow = 6 * time.Hour
	}

	return &Service{
		logger:        logger,
		categories:    categories,
		backendURL:    backendURL,
		apiKey:        apiKey,
		httpClient:    &http.Client{Timeout: 20 * time.Second},
		audit:         NewAuditStore(logger, reportsDir),
		excludedChats: excluded,
		dedupWindow:   dedupWindow,
		dedupSeen:     map[string]time.Time{},
	}
}

func (s *Service) ProcessMessage(msg *tg.Message, e tg.Entities) {
	rawText := strings.TrimSpace(msg.Message)
	buttonContactURL := firstContactButtonURL(msg.ReplyMarkup)
	if buttonContactURL != "" && !strings.Contains(rawText, buttonContactURL) {
		rawText = strings.TrimSpace(rawText + "\n\nКонтакт из кнопки: " + buttonContactURL)
	}
	if rawText == "" {
		return
	}

	chatID := s.extractID(msg.PeerID)
	if s.excludedChats[chatID] {
		return
	}

	lowerText := strings.ToLower(rawText)
	userMeta := s.resolveAuthor(msg, e)
	if userMeta.IsBot {
		return
	}

	category := s.findMatchedCategory(chatID, lowerText)
	if category == nil {
		return
	}

	recordBase := AuditRecord{
		EventTime:      time.Now(),
		MessageTime:    time.Unix(int64(msg.Date), 0),
		ChatID:         chatID,
		MessageID:      msg.ID,
		Username:       userMeta.Username,
		UserID:         userMeta.UserID,
		SourceCategory: category.Name,
		Text:           rawText,
	}

	if userMeta.UserID == 0 && userMeta.Username == "" && !hasContactInText(rawText) {
		s.audit.Record(withReject(recordBase, "CHANNEL_POST_WITHOUT_CONTACT", "", ""))
		return
	}

	if s.isDuplicateLead(rawText, recordBase.MessageTime) {
		s.audit.Record(withReject(recordBase, "DUPLICATE_LEAD_TEXT", "", ""))
		return
	}

	if s.isObviousSpam(lowerText) {
		s.audit.Record(withReject(recordBase, "PREFILTER_SPAM_OR_SELF_PROMO", "", ""))
		return
	}

	classification := s.classifyMessage(rawText)
	if classification.Class != "BUYER_LEAD" && s.isTransientAIError(classification.Reason) {
		if s.isSafeFallbackBuyerLead(lowerText) {
			classification = AIClassification{
				Class:    "BUYER_LEAD",
				Category: s.inferFallbackCategory(lowerText),
				Reason:   "fallback: buyer intent while AI unavailable",
			}
		}
	}
	if classification.Class == "DISCUSSION" && s.looksLikeDirectOrder(lowerText) {
		classification = AIClassification{
			Class:    "BUYER_LEAD",
			Category: s.inferFallbackCategory(lowerText),
			Reason:   "fallback: direct order pattern",
		}
	}
	recordBase.AIClass = classification.Class
	recordBase.AICategory = classification.Category
	recordBase.AIReason = classification.Reason

	if classification.Class != "BUYER_LEAD" {
		reason := "AI_" + classification.Class
		s.audit.Record(withReject(recordBase, reason, classification.Class, classification.Reason))
		return
	}

	subcategory := normalizeAICategory(classification.Category)
	payload := LeadPayload{
		Text:         rawText,
		Category:     category.Name,
		Subcategory:  subcategory,
		AIClass:      classification.Class,
		AIReason:     classification.Reason,
		FirstName:    userMeta.FirstName,
		LastName:     userMeta.LastName,
		Username:     userMeta.Username,
		UserID:       userMeta.UserID,
		Date:         msg.Date,
		MessageID:    msg.ID,
		ChatID:       chatID,
		ChatUsername: s.resolveChatUsername(chatID, e),
		ContactURL:   buttonContactURL,
	}

	if err := s.sendLead(payload); err != nil {
		s.logger.Error("send lead failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("message_id", msg.ID))
		s.audit.Record(withReject(recordBase, "FRONTEND_DELIVERY_ERROR", classification.Class, err.Error()))
		return
	}

	recordBase.Decision = "ACCEPTED"
	recordBase.Reason = "ACCEPTED"
	recordBase.DeliveryCategory = category.Name
	recordBase.Subcategory = subcategory
	s.audit.Record(recordBase)

	s.logger.Info("✅ buyer lead accepted",
		zap.String("category", category.Name),
		zap.String("subcategory", subcategory),
		zap.String("ai_reason", truncate(classification.Reason, 120)),
		zap.Int64("chat_id", chatID),
		zap.Int("message_id", msg.ID),
		zap.String("preview", truncate(lowerText, 140)),
	)
}

type authorMeta struct {
	FirstName string
	LastName  string
	Username  string
	UserID    int64
	IsBot     bool
}

func (s *Service) resolveAuthor(msg *tg.Message, e tg.Entities) authorMeta {
	meta := authorMeta{}
	peerUser, ok := msg.FromID.(*tg.PeerUser)
	if !ok {
		return meta
	}

	meta.UserID = peerUser.UserID
	user, found := e.Users[peerUser.UserID]
	if !found {
		return meta
	}
	meta.FirstName = user.FirstName
	meta.LastName = user.LastName
	meta.Username = user.Username
	meta.IsBot = user.Bot || strings.Contains(strings.ToLower(user.Username), "bot")
	return meta
}

func (s *Service) findMatchedCategory(chatID int64, lowerText string) *domain.Category {
	for _, category := range s.categories {
		if !category.Chats[chatID] {
			continue
		}
		if !s.containsKeyword(lowerText, category.Keywords) {
			continue
		}
		return category
	}
	return nil
}

func (s *Service) hasBuyerIntent(text string) bool {
	intents := []string{
		"нужен", "нужна", "нужно", "надо",
		"ищу", "ищем", "мы ищем", "требуется",
		"кто сделает", "кто сможет сделать",
		"кто может сделать", "кто может помочь",
		"кто знает", "кто то знает", "кто-то знает",
		"посоветуйте", "порекомендуйте",
		"кто может настроить", "кто поможет настроить",
		"кто возьмется", "кто возьмёт",
		"есть задача", "есть проект",
		"нужно сделать", "надо сделать",
		"нужно доработать", "надо доработать",
		"нужно разработать", "надо разработать",
		"нужно настроить", "надо настроить",
		"нужно внедрить", "нужно интегрировать",
		"готов заплатить", "готов оплатить",
		"внесу предоплату", "с предоплатой",
		"есть бюджет", "бюджет",
		"ищу исполнителя", "ищу подрядчика",
		"нужен разработчик", "нужен программист",
		"нужен дизайнер", "нужен маркетолог", "нужен smm",
		"нужен бот", "нужен парсер", "нужен сайт",
		"нужен специалист", "нужен человек",
		"ищу агенство", "ищу агентство", "агентства рассматриваем",
	}
	return hasAnyIntentPhrase(text, intents)
}

func (s *Service) isSafeFallbackBuyerLead(text string) bool {
	if s.isObviousSpam(text) {
		return false
	}
	if s.isLikelyHiring(text) {
		return false
	}
	if s.isLikelyPerformerProfile(text) {
		return false
	}
	if !s.looksLikeDirectOrder(text) {
		return false
	}
	return true
}

func (s *Service) isTransientAIError(reason string) bool {
	r := strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(r, "network error") || strings.Contains(r, "api error") || strings.Contains(r, "parse error")
}

func (s *Service) inferFallbackCategory(text string) string {
	switch {
	case hasAny(text, []string{"дизайн", "дизайнер", "баннер", "логотип", "креатив", "инфографик", "презентац"}):
		return "design"
	case hasAny(text, []string{"wb", "wildberries", "ozon", "маркетплейс", "кабинет", "поставк", "остатк", "выручк", "юнит-эконом"}):
		return "marketplace_ops"
	case hasAny(text, []string{"smm", "смм", "таргет", "маркетинг", "реклама", "контент"}):
		return "marketing_smm"
	default:
		return "development"
	}
}

func (s *Service) isObviousSpam(lowerText string) bool {
	systemPhrases := []string{
		"правила чата", "антиспам", "подмена символов", "antispambot",
		"добро пожаловать в чат", "присоединился к группе", "вступил в группу",
	}

	selfPromo := []string{
		"#помогу", "#услуги", "#резюме",
		"предлагаю свои услуги", "предлагаю услуги", "оказываю услуги",
		"помогаю", "помогу", "работаю удаленно", "работаю удалённо", "беру проекты", "беру заказы",
		"ищу клиентов", "ищу заказчиков", "ищу проект", "ищу работу",
		"мои кейсы", "мои работы",
		"индивидуальный подход", "всегда на связи", "доступные цены", "срок выполнения",
		"напишите +", "я дизайнер", "я разработчик", "я тестировщик", "я бизнес-ассистент",
		"мы команда", "наша команда", "студия", "агентство полного цикла",
	}

	hiringMarkers := []string{
		"в штат", "полная занятость", "неполная занятость",
		"без опыта", "всему обучим", "оклад", "зп от", "зарплата",
		"с 18 лет", "строго от 18", "только от 18",
	}
	quickMoneySpam := []string{
		"быстро заработать", "лёгкий заработок", "легкий заработок", "заработок за",
		"5-10 мин", "5–10 мин", "10 мин времени", "выплачиваю сразу",
		"максимально белая работа", "возраст 14+", "14+", "15+", "16+", "17+",
	}

	for _, phrase := range systemPhrases {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	for _, phrase := range selfPromo {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	for _, phrase := range hiringMarkers {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	for _, phrase := range quickMoneySpam {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	return false
}

func (s *Service) looksLikeDirectOrder(text string) bool {
	intents := []string{
		"нужен", "нужна", "нужно", "надо",
		"ищу", "ищем", "требуется",
		"кто сделает", "кто сможет", "кто может",
		"кто знает", "кто то знает", "кто-то знает",
		"посоветуйте", "порекомендуйте",
	}
	taskWords := []string{
		"правк", "доработ", "разработ", "настро", "интеграц",
		"сайт", "лендинг", "бот", "парсер", "crm", "api", "деплой", "сервер", "поддержк",
		"битрикс", "bitrix", "wordpress", "tilda", "веб",
		"дизайн", "дизайнер", "инфограф", "карточк",
	}
	return hasAnyIntentPhrase(text, intents) && hasAny(text, taskWords) && !s.isLikelyHiring(text)
}

func (s *Service) isLikelyHiring(text string) bool {
	hiringMarkers := []string{
		"вакансия", "в штат", "полная занятость", "неполная занятость",
		"работа на дому",
		"без опыта", "всему обучим", "оклад", "зп от", "зарплата",
		"с 18 лет", "строго от 18", "только от 18", "старше 18", "достигшие 18",
		"берем строго от 18", "берём строго от 18", "идет набор", "идёт набор",
		"full-time", "фуллтайм", "полный рабочий день",
	}
	return hasAnyIntentPhrase(text, hiringMarkers)
}

func (s *Service) isLikelyPerformerProfile(text string) bool {
	profileMarkers := []string{
		"#ищу", "#резюме", "#cv", "full-time / проектная", "full time / проектная",
		"опыт ", "лет опыта", "мой стек", "стек:", "формат:", "занятость:", "ожидания:",
		"разрабатываю", "разработчик", "developer", "ui kit", "swiftui", "mvvm",
		"портфолио", "коммерческой разработке", "контакт:",
	}
	return hasAny(text, profileMarkers)
}

func (s *Service) classifyMessage(text string) AIClassification {
	reqBody := GPTRequest{
		Model:     "gpt-4o-mini",
		MaxTokens: 120,
		Messages: []Message{
			{
				Role: "system",
				Content: `Ты фильтр лидов в Telegram.
Верни строго JSON-объект без markdown:
{"class":"BUYER_LEAD|JOB|SELF_PROMO|DISCUSSION","category":"development|design|marketing_smm|marketplace_ops|other","reason":"короткая причина до 12 слов"}

Определения:
- BUYER_LEAD: заказчик ищет подрядчика под конкретную задачу/проект, готов обсуждать оплату.
- JOB: найм сотрудника (в штат/на постоянку/удаленка/оклад/вакансия/без опыта/анкеты).
- SELF_PROMO: автор продает свои услуги, портфолио, кейсы, рекламу себя.
- DISCUSSION: разговор, комментарий, вопрос без заказа.

Категория ставится только для BUYER_LEAD:
- development: разработка, боты, сайты, интеграции, код, деплой, devops, техподдержка.
- design: графика, презентации, креативы, общий дизайн, карточки/инфографика для WB/Ozon.
- marketing_smm: таргет, SMM, контент, реклама.
- marketplace_ops: только операционка маркетплейсов (ведение кабинета, реклама в кабинете, аналитика продаж, поставки, остатки).
- other: если не подходит.

Правила:
- Если есть признаки вакансии или регулярного найма, class=JOB.
- Если человек предлагает себя, class=SELF_PROMO.
- Если есть запрос типа "кто знает/посоветуйте специалиста для правок/доработки", class=BUYER_LEAD.
- Если это пост канала/агрегатора с прямым контактом и конкретной проектной задачей, class=BUYER_LEAD.
- Фразы "присылайте портфолио/кейсы/прайс" в объявлении заказчика не являются SELF_PROMO.
- Если задача про дизайн/инфографику/баннеры карточек WB/Ozon, category=design.
- При сомнении class=DISCUSSION.`,
			},
			{Role: "user", Content: text},
		},
		Temperature: 0,
	}

	jsonData, _ := json.Marshal(reqBody)

	var resp *http.Response
	var err error
	var body []byte
	for attempt := 1; attempt <= 2; attempt++ {
		req, _ := http.NewRequest("POST", AITunnelURL, bytes.NewBuffer(jsonData))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+s.apiKey)

		resp, err = s.httpClient.Do(req)
		if err != nil {
			if attempt < 2 {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			s.logger.Error("GPT network error", zap.Error(err))
			return AIClassification{Class: "DISCUSSION", Category: "other", Reason: "network error"}
		}

		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			break
		}
		if resp.StatusCode >= 500 && attempt < 2 {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		s.logger.Error("GPT api error", zap.Int("status", resp.StatusCode), zap.String("response", truncate(string(body), 500)))
		return AIClassification{Class: "DISCUSSION", Category: "other", Reason: "api error"}
	}

	var gptResp GPTResponse
	if err := json.Unmarshal(body, &gptResp); err != nil || len(gptResp.Choices) == 0 {
		s.logger.Error("GPT parse error", zap.Error(err), zap.String("response", truncate(string(body), 500)))
		return AIClassification{Class: "DISCUSSION", Category: "other", Reason: "parse error"}
	}

	content := strings.TrimSpace(gptResp.Choices[0].Message.Content)
	return parseAIClassification(content)
}

func parseAIClassification(content string) AIClassification {
	result := AIClassification{Class: "DISCUSSION", Category: "other", Reason: "fallback"}

	raw := extractJSONObject(content)
	if raw != "" {
		var parsed AIClassification
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			result.Class = normalizeAIClass(parsed.Class)
			result.Category = normalizeAICategory(parsed.Category)
			result.Reason = strings.TrimSpace(parsed.Reason)
			if result.Reason == "" {
				result.Reason = "ok"
			}
			return result
		}
	}

	upper := strings.ToUpper(content)
	switch {
	case strings.Contains(upper, "BUYER_LEAD"):
		result.Class = "BUYER_LEAD"
	case strings.Contains(upper, "SELF_PROMO"):
		result.Class = "SELF_PROMO"
	case strings.Contains(upper, "JOB"):
		result.Class = "JOB"
	default:
		result.Class = "DISCUSSION"
	}
	result.Reason = truncate(content, 80)
	return result
}

func normalizeAIClass(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "BUYER_LEAD":
		return "BUYER_LEAD"
	case "JOB":
		return "JOB"
	case "SELF_PROMO":
		return "SELF_PROMO"
	default:
		return "DISCUSSION"
	}
}

func normalizeAICategory(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "development":
		return "development"
	case "design":
		return "design"
	case "marketing_smm":
		return "marketing_smm"
	case "marketplace_ops":
		return "marketplace_ops"
	default:
		return "other"
	}
}

func hasAny(text string, words []string) bool {
	for _, w := range words {
		if smartContains(text, strings.ToLower(strings.TrimSpace(w))) {
			return true
		}
	}
	return false
}

func hasContactInText(text string) bool {
	return reContactUsername.MatchString(text) ||
		reContactURL.MatchString(text) ||
		reContactPhone.MatchString(text) ||
		reContactEmail.MatchString(text)
}

func firstContactButtonURL(markup tg.ReplyMarkupClass) string {
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || inline == nil {
		return ""
	}
	for _, row := range inline.Rows {
		for _, button := range row.Buttons {
			label, url := buttonContactTarget(button)
			if url == "" || !isContactButtonLabel(label) {
				continue
			}
			return url
		}
	}
	return ""
}

func buttonContactTarget(button tg.KeyboardButtonClass) (string, string) {
	switch b := button.(type) {
	case *tg.KeyboardButtonURL:
		return b.Text, strings.TrimSpace(b.URL)
	case *tg.KeyboardButtonURLAuth:
		return b.Text, strings.TrimSpace(b.URL)
	case *tg.KeyboardButtonUserProfile:
		if b.UserID <= 0 {
			return b.Text, ""
		}
		return b.Text, "tg://user?id=" + strconv.FormatInt(b.UserID, 10)
	default:
		return "", ""
	}
}

func isContactButtonLabel(label string) bool {
	lower := strings.ToLower(label)
	contactWords := []string{
		"связаться", "контакт", "автор", "отклик", "откликнуться",
		"написать", "личк", "обсудить", "apply",
	}
	for _, word := range contactWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func hasAnyIntentPhrase(text string, words []string) bool {
	for _, w := range words {
		kw := strings.ToLower(strings.TrimSpace(w))
		if kw == "" {
			continue
		}
		if strings.Contains(kw, " ") {
			if strings.Contains(text, kw) {
				return true
			}
			continue
		}
		if containsWholeToken(text, kw) {
			return true
		}
	}
	return false
}

func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func (s *Service) containsKeyword(text string, keywords []string) bool {
	for _, kw := range keywords {
		if smartContains(text, strings.ToLower(strings.TrimSpace(kw))) {
			return true
		}
	}
	return false
}

func smartContains(text, kw string) bool {
	if kw == "" {
		return false
	}
	if strings.Contains(kw, " ") {
		return strings.Contains(text, kw)
	}
	if shortTokenKeyword(kw) {
		return containsWholeToken(text, kw)
	}
	return strings.Contains(text, kw)
}

func shortTokenKeyword(kw string) bool {
	runes := []rune(kw)
	if len(runes) > 3 {
		return false
	}
	for _, r := range runes {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func containsWholeToken(text, token string) bool {
	from := 0
	for {
		idx := strings.Index(text[from:], token)
		if idx == -1 {
			return false
		}
		start := from + idx
		end := start + len(token)

		leftOK := start == 0
		if !leftOK {
			r, _ := utf8.DecodeLastRuneInString(text[:start])
			leftOK = !isTokenRune(r)
		}

		rightOK := end == len(text)
		if !rightOK {
			r, _ := utf8.DecodeRuneInString(text[end:])
			rightOK = !isTokenRune(r)
		}

		if leftOK && rightOK {
			return true
		}
		from = end
		if from >= len(text) {
			return false
		}
	}
}

func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func (s *Service) isDuplicateLead(text string, msgTime time.Time) bool {
	key := normalizeDedupText(text)
	if key == "" {
		return false
	}

	cutoff := msgTime.Add(-s.dedupWindow)
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()

	for k, t := range s.dedupSeen {
		if t.Before(cutoff) {
			delete(s.dedupSeen, k)
		}
	}

	if prev, ok := s.dedupSeen[key]; ok && !prev.Before(cutoff) {
		return true
	}
	s.dedupSeen[key] = msgTime
	return false
}

func normalizeDedupText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (s *Service) sendLead(payload LeadPayload) error {
	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", s.backendURL, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return &httpError{StatusCode: resp.StatusCode, Body: truncate(string(body), 200)}
	}
	return nil
}

type httpError struct {
	StatusCode int
	Body       string
}

func (e *httpError) Error() string {
	return "http status " + strconv.Itoa(e.StatusCode) + ": " + e.Body
}

func (s *Service) extractID(peer tg.PeerClass) int64 {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return p.ChatID
	case *tg.PeerChannel:
		return p.ChannelID
	}
	return 0
}

func (s *Service) resolveChatUsername(chatID int64, e tg.Entities) string {
	if chatID == 0 {
		return ""
	}
	if e.Channels != nil {
		if ch, ok := e.Channels[chatID]; ok && ch != nil {
			return strings.TrimSpace(ch.Username)
		}
	}
	return ""
}

func withReject(base AuditRecord, reason, aiClass, aiReason string) AuditRecord {
	base.Decision = "REJECTED"
	base.Reason = reason
	if aiClass != "" {
		base.AIClass = aiClass
	}
	if aiReason != "" {
		base.AIReason = aiReason
	}
	return base
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
