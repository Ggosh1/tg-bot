package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"lidohod/tg-parser/auth"
	"lidohod/tg-parser/domain"
	"lidohod/tg-parser/parser"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	tgAuth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	_ = godotenv.Load() // Загружаем .env если есть

	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	apiID, _ := strconv.Atoi(os.Getenv("API_ID"))
	apiHash := os.Getenv("API_HASH")

	// 1. Категория IT и Разработка
	itCategory := domain.NewCategory(
		"IT и Разработка",
		[]int64{
			1263233484, 407691819, 1333124183, 398845062, 1051806860, 124131585, 1141178844, 215723024, 1030317489, 110766624, 1521324970, 471996724, 1280723191, 257514029, 1032883247, 140305002, 1816041970, 1244373622, 459928466, 3914183709, 991941764, 1050008285, 124753320, 1054900891, 127664551, 1858515631, 860663832, 1187147299, 261724117, 1104353296, 215799035, 1057165592, 155197761, 1088399568, 174081609, 1054966032, 120027236, 1088564643, 170865693, 1041204341, 120512458, 1063506265, 139603438, 1096347276, 169608170, 1039787275, 144428865, 1086700901, 150634611, 1364217388, 1392392805, 247938208, 1807308973, 1816041970, 1071816041970,
			2299912906, 1393051235, 456439528, 1922313292, 1582839642, 814185964, 1335732935, 493819448, 1608905017, 661608023, 1250419634, 358908108, 1138359962, 324943742, 1328530014, 474691271, 1368529433, 400356967, 2166683742, 4278157370,
			1512388330, 1691644751, 583394275, 779184473,
			1162626517, 292207673, 1371121321, 277001393, 1363136000, 1481574785, 440921754, 1490371259, 382884562, 1277906860, 345551828, 1207713578, 251679040,
			1753540763, 2050611379, 4767626711,
			1854790549, 1246241839, 1071246241839, 1799919407, 1071799919407, 1199102856, 2028292875, 1297680727, 1071297680727, 1445279619, 1204182905, 1283023213, 1071283023213, 1329809676, 1031532699, 1071031532699, 1407813055,
			1674990228, 1303819569, 2086181858, 1072086181858, 1421781535,
			1288289978, 113694506, 2144362787, 4108123954, 1500037611, 767541062,
			1821576538, 792047439, 2707281180, 4169480545, 1843220369, 956584814,
			2237080465, 1664358983, 1364916039, 1363838533, 1439525443, 2219233982,
			1229737185, 1556108066, 1297002353, 1929308649, 1672790110, 1235778918,
			2515705224, 1000735083, 1718248552, 1235599339, 1071000735083, 1389774378,
			1671908637, 1072515705224, 1256746617, 1178187637, 1428955589, 1181128527,
			2328733474, 1406991134, 1071235599339, 1580600683, 1242873641, 1105900580,
			1659273106, 1838041568, 1448120171,
			1072095179199, 1418651811, 1460377127, 1522267176, 1594936697, 1669984427,
			1708378722, 1794625297, 1819426678, 1846105912, 1985622470, 2012777805,
			2095179199, 2224481084, 2270251777, 2539144105, 3899937065,
		},
		[]string{
			"разработка", "программист", "создать сайт", "сайт", "кодер", "разработчик",
			"верстальщик", "бекэнд", "фронтэнд", "node", "react", "laravel", "qa",
			"тестировщик", "backend", "frontend", "php", "python", "golang", "java",
			"js", "javascript", "vue", "angular", "приложение", "ios", "android",
			"devops", "парсер", "бот", "telegram бот", "тг бот", "скрипт",
			"автоматизация", "доработать", "сверстать", "интеграция", "api",
			"битрикс", "bitrix", "1c", "1с", "sql", "база данных", "html", "css", "тильда", "tilda", "лендинг",
			"интернет-магазин", "доработка", "верстка", "правки", "баг", "фикс", "фича", "поддержка", "автоматизация",
			"wordpress", "shopify", "webflow", "реакт", "vue", "nodejs", "django",
			"фронтенд", "frontend", "бэкэнд", "фуллстек", "fullstack", "разработчик",
			"сделать", "создать", "разработать", "переделать", "доработать", "исправить", "настроить",
			"оптимизировать", "нужен", "ищу", "требуется", "кто сделает", "кто может", "есть задача",
			"есть проект", "возьмется",
			"вебмастер", "веб-мастер", "вебспециалист", "веб-специалист", "вебразработчик", "сайтолог",
			"техспец", "технический специалист", "чат-бот", "чатбот", "getcourse", "геткурс", "taplink",
			"таплинк", "amo", "amocrm", "amo crm", "автоворонка", "форма", "формы", "оплата", "платежка",
			"маркетплейс", "wb", "wildberries", "ozon", "карточки", "инфографика", "селлер", "менеджер маркетплейсов",
		},
	)

	// 2. Категория Дизайн
	designCategory := domain.NewCategory(
		"Дизайн",
		[]int64{
			1086700901, 1364217388,
			1263233484, 1858515631, 1244373622, 1333124183, // Общие чаты фриланса
			1512388330, 1691644751, 583394275, 779184473, 2299912906, 1922313292, 1582839642,
			1373050413, 1071373050413, 2200628415, 1835848655, 2052394155, 1821576538, 792047439,
			2237080465, 1664358983, 1364916039, 1363838533, 1439525443, 2219233982, 1229737185,
			1418651811, 1460377127, 1522267176, 1594936697, 1669984427, 1708378722, 1794625297,
			1846105912, 2224481084, 2270251777, 2539144105, 3899937065,
		},
		[]string{
			"логотип", "баннер", "дизайн", "оформить", "фигма", "figma",
			"презентацию", "иллюстратор", "карточки", "wb", "ui", "ux", "webflow",
			"дизайнер", "креатив", "инфографика", "tilda", "тильда", "макет",
			"лендинг", "landing", "оформление", "обложка", "превью", "3d", "ozon", "wildberries", "маркетплейс",
		},
	)

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:8080/new_lead"
	}
	apiKey := os.Getenv("AITUNNEL_API_KEY")
	reportsDir := os.Getenv("REPORTS_DIR")
	if reportsDir == "" {
		reportsDir = "/app/data/reports"
	}
	excludedChatIDs := parseInt64CSV(os.Getenv("EXCLUDED_CHAT_IDS"))
	if len(excludedChatIDs) == 0 {
		excludedChatIDs = []int64{1816041970, 1071816041970}
	}
	dedupWindowMinutes := 360
	if raw := strings.TrimSpace(os.Getenv("DEDUP_WINDOW_MINUTES")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			dedupWindowMinutes = v
		}
	}
	parserService := parser.NewService(
		logger,
		[]*domain.Category{itCategory, designCategory},
		frontendURL,
		apiKey,
		reportsDir,
		excludedChatIDs,
		time.Duration(dedupWindowMinutes)*time.Minute,
	)

	ctx := context.Background()
	dispatcher := tg.NewUpdateDispatcher()
	gaps := updates.New(updates.Config{Handler: dispatcher})

	// Обработчики сообщений
	handler := func(ctx context.Context, e tg.Entities, msg *tg.Message) error {
		if msg.Message != "" {
			parserService.ProcessMessage(msg, e)
		}
		return nil
	}

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		if m, ok := u.Message.(*tg.Message); ok {
			return handler(ctx, e, m)
		}
		return nil
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		if m, ok := u.Message.(*tg.Message); ok {
			return handler(ctx, e, m)
		}
		return nil
	})

	sessionStorage := &session.FileStorage{Path: "/app/data/session.json"}
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: sessionStorage,
		UpdateHandler:  gaps,
		Logger:         logger.Named("tg"),
	})

	logger.Info("🚀 Запуск Юзербота-парсера...")
	err := client.Run(ctx, func(ctx context.Context) error {
		flow := tgAuth.NewFlow(auth.Terminal{}, tgAuth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		user, _ := client.Self(ctx)
		return gaps.Run(ctx, client.API(), user.ID, updates.AuthOptions{IsBot: false})
	})
	if err != nil {
		logger.Fatal("❌ Критическая ошибка", zap.Error(err))
	}
}

func parseInt64CSV(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil {
			out = append(out, id)
		}
	}
	return out
}
