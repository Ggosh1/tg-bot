package parser

import (
	"bufio"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

type AuditRecord struct {
	EventTime        time.Time
	MessageTime      time.Time
	ChatID           int64
	MessageID        int
	Username         string
	UserID           int64
	SourceCategory   string
	DeliveryCategory string
	Subcategory      string
	Decision         string
	Reason           string
	AIClass          string
	AICategory       string
	AIReason         string
	Text             string
}

type AuditStore struct {
	logger             *zap.Logger
	reportsDir         string
	statsLoc           *time.Location
	eventsPath         string
	acceptedPath       string
	rejectedPath       string
	dailyStatsPath     string
	rejectReasonsPath  string
	dashboardPath      string
	mu                 sync.Mutex
	dailyAccepted      map[string]int
	dailyRejected      map[string]int
	dailyByCategory    map[string]map[string]int
	dailyRejectReasons map[string]map[string]int
	recentAccepted     []AuditRecord
	recentRejected     []AuditRecord
	maxRecent          int
}

func NewAuditStore(logger *zap.Logger, reportsDir string) *AuditStore {
	if reportsDir == "" {
		reportsDir = "/app/data/reports"
	}
	_ = os.MkdirAll(reportsDir, 0755)

	statsTZ := strings.TrimSpace(os.Getenv("REPORTS_TZ"))
	if statsTZ == "" {
		statsTZ = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(statsTZ)
	if err != nil {
		logger.Warn("invalid REPORTS_TZ, fallback to Europe/Moscow", zap.String("reports_tz", statsTZ), zap.Error(err))
		loc = time.FixedZone("MSK", 3*60*60)
	}

	store := &AuditStore{
		logger:             logger,
		reportsDir:         reportsDir,
		statsLoc:           loc,
		eventsPath:         filepath.Join(reportsDir, "lead_events.tsv"),
		acceptedPath:       filepath.Join(reportsDir, "accepted_leads.tsv"),
		rejectedPath:       filepath.Join(reportsDir, "rejected_leads.tsv"),
		dailyStatsPath:     filepath.Join(reportsDir, "lead_stats_daily.tsv"),
		rejectReasonsPath:  filepath.Join(reportsDir, "lead_reject_reasons_daily.tsv"),
		dashboardPath:      filepath.Join(reportsDir, "lead_audit_dashboard.html"),
		dailyAccepted:      map[string]int{},
		dailyRejected:      map[string]int{},
		dailyByCategory:    map[string]map[string]int{},
		dailyRejectReasons: map[string]map[string]int{},
		maxRecent:          250,
	}

	store.ensureHeaders()
	store.loadEvents()
	store.writeDailyStatsLocked()
	store.writeRejectReasonsLocked()
	store.writeDashboardLocked()
	return store
}

func (s *AuditStore) Record(record AuditRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if record.EventTime.IsZero() {
		record.EventTime = time.Now()
	}
	if record.MessageTime.IsZero() {
		record.MessageTime = record.EventTime
	}

	date := s.dayKey(record.MessageTime)
	if strings.EqualFold(record.Decision, "ACCEPTED") {
		s.dailyAccepted[date]++
		cat := normalizeStatCategory(record.Subcategory)
		if _, ok := s.dailyByCategory[date]; !ok {
			s.dailyByCategory[date] = map[string]int{}
		}
		s.dailyByCategory[date][cat]++
	} else {
		s.dailyRejected[date]++
		if _, ok := s.dailyRejectReasons[date]; !ok {
			s.dailyRejectReasons[date] = map[string]int{}
		}
		s.dailyRejectReasons[date][record.Reason]++
	}

	s.pushRecent(record)

	s.appendTSVLine(s.eventsPath, []string{
		record.EventTime.Format(time.RFC3339),
		record.MessageTime.Format(time.RFC3339),
		strconv.FormatInt(record.ChatID, 10),
		strconv.Itoa(record.MessageID),
		record.Username,
		strconv.FormatInt(record.UserID, 10),
		record.SourceCategory,
		record.DeliveryCategory,
		record.Subcategory,
		record.Decision,
		record.Reason,
		record.AIClass,
		record.AICategory,
		record.AIReason,
		record.Text,
	})

	if strings.EqualFold(record.Decision, "ACCEPTED") {
		s.appendTSVLine(s.acceptedPath, []string{
			record.EventTime.Format(time.RFC3339),
			record.MessageTime.Format(time.RFC3339),
			strconv.FormatInt(record.ChatID, 10),
			strconv.Itoa(record.MessageID),
			record.Username,
			record.DeliveryCategory,
			record.Subcategory,
			record.AIReason,
			record.Text,
		})
	} else {
		s.appendTSVLine(s.rejectedPath, []string{
			record.EventTime.Format(time.RFC3339),
			record.MessageTime.Format(time.RFC3339),
			strconv.FormatInt(record.ChatID, 10),
			strconv.Itoa(record.MessageID),
			record.Username,
			record.SourceCategory,
			record.Reason,
			record.AIClass,
			record.AIReason,
			record.Text,
		})
	}

	s.writeDailyStatsLocked()
	s.writeRejectReasonsLocked()
	s.writeDashboardLocked()
}

func (s *AuditStore) ensureHeaders() {
	s.ensureHeader(s.eventsPath, "event_time\tmessage_time\tchat_id\tmessage_id\tusername\tuser_id\tsource_category\tdelivery_category\tsubcategory\tdecision\treason\tai_class\tai_category\tai_reason\ttext\n")
	s.ensureHeader(s.acceptedPath, "event_time\tmessage_time\tchat_id\tmessage_id\tusername\tdelivery_category\tsubcategory\tai_reason\ttext\n")
	s.ensureHeader(s.rejectedPath, "event_time\tmessage_time\tchat_id\tmessage_id\tusername\tsource_category\treason\tai_class\tai_reason\ttext\n")
}

func (s *AuditStore) ensureHeader(path, header string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	_ = os.WriteFile(path, []byte(header), 0644)
}

func (s *AuditStore) appendTSVLine(path string, fields []string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		s.logger.Error("audit append open failed", zap.String("path", path), zap.Error(err))
		return
	}
	defer f.Close()

	for i := range fields {
		fields[i] = sanitizeTSV(fields[i])
	}
	line := strings.Join(fields, "\t") + "\n"
	if _, err := f.WriteString(line); err != nil {
		s.logger.Error("audit append write failed", zap.String("path", path), zap.Error(err))
	}
}

func (s *AuditStore) loadEvents() {
	file, err := os.Open(s.eventsPath)
	if err != nil {
		return
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 15 {
			continue
		}
		eventTime, err := time.Parse(time.RFC3339, parts[0])
		if err != nil {
			continue
		}
		msgTime, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}
		record := AuditRecord{
			EventTime:        eventTime,
			MessageTime:      msgTime,
			ChatID:           parseInt64Safe(parts[2]),
			MessageID:        parseIntSafe(parts[3]),
			Username:         parts[4],
			UserID:           parseInt64Safe(parts[5]),
			SourceCategory:   parts[6],
			DeliveryCategory: parts[7],
			Subcategory:      parts[8],
			Decision:         parts[9],
			Reason:           parts[10],
			AIClass:          parts[11],
			AICategory:       parts[12],
			AIReason:         parts[13],
			Text:             parts[14],
		}

		date := s.dayKey(msgTime)
		if strings.EqualFold(record.Decision, "ACCEPTED") {
			s.dailyAccepted[date]++
			cat := normalizeStatCategory(record.Subcategory)
			if _, ok := s.dailyByCategory[date]; !ok {
				s.dailyByCategory[date] = map[string]int{}
			}
			s.dailyByCategory[date][cat]++
		} else {
			s.dailyRejected[date]++
			if _, ok := s.dailyRejectReasons[date]; !ok {
				s.dailyRejectReasons[date] = map[string]int{}
			}
			s.dailyRejectReasons[date][record.Reason]++
		}

		s.pushRecent(record)
	}
}

func (s *AuditStore) writeDailyStatsLocked() {
	dates := sortedDates(s.dailyAccepted, s.dailyRejected)
	var b strings.Builder
	b.WriteString("date\taccepted_total\trejected_total\tdevelopment\tdesign\tmarketing_smm\tmarketplace_ops\tother\n")
	for _, date := range dates {
		cats := s.dailyByCategory[date]
		b.WriteString(fmt.Sprintf("%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			date,
			s.dailyAccepted[date],
			s.dailyRejected[date],
			cats["development"],
			cats["design"],
			cats["marketing_smm"],
			cats["marketplace_ops"],
			cats["other"],
		))
	}
	_ = os.WriteFile(s.dailyStatsPath, []byte(b.String()), 0644)
}

func (s *AuditStore) writeRejectReasonsLocked() {
	dates := make([]string, 0, len(s.dailyRejectReasons))
	for date := range s.dailyRejectReasons {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	var b strings.Builder
	b.WriteString("date\treason\tcount\n")
	for _, date := range dates {
		reasons := s.dailyRejectReasons[date]
		keys := make([]string, 0, len(reasons))
		for reason := range reasons {
			keys = append(keys, reason)
		}
		sort.Strings(keys)
		for _, reason := range keys {
			b.WriteString(fmt.Sprintf("%s\t%s\t%d\n", date, sanitizeTSV(reason), reasons[reason]))
		}
	}
	_ = os.WriteFile(s.rejectReasonsPath, []byte(b.String()), 0644)
}

func sortedDates(m1, m2 map[string]int) []string {
	set := map[string]bool{}
	for date := range m1 {
		set[date] = true
	}
	for date := range m2 {
		set[date] = true
	}
	out := make([]string, 0, len(set))
	for date := range set {
		out = append(out, date)
	}
	sort.Strings(out)
	return out
}

func normalizeStatCategory(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "development":
		return "development"
	case "design":
		return "design"
	case "marketing_smm":
		return "marketing_smm"
	case "marketplace_ops":
		return "marketplace_ops"
	case "business_services":
		return "other"
	default:
		return "other"
	}
}

func (s *AuditStore) dayKey(t time.Time) string {
	if s.statsLoc == nil {
		return t.Format("2006-01-02")
	}
	return t.In(s.statsLoc).Format("2006-01-02")
}

func (s *AuditStore) pushRecent(record AuditRecord) {
	if strings.EqualFold(record.Decision, "ACCEPTED") {
		s.recentAccepted = append(s.recentAccepted, record)
		if len(s.recentAccepted) > s.maxRecent {
			s.recentAccepted = s.recentAccepted[len(s.recentAccepted)-s.maxRecent:]
		}
		return
	}
	s.recentRejected = append(s.recentRejected, record)
	if len(s.recentRejected) > s.maxRecent {
		s.recentRejected = s.recentRejected[len(s.recentRejected)-s.maxRecent:]
	}
}

func (s *AuditStore) writeDashboardLocked() {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Lead Audit Dashboard</title><style>
body{font-family:Arial,sans-serif;margin:24px;background:#f8f8f6;color:#111}h1{margin:0 0 10px}h2{margin:28px 0 10px}
.muted{color:#666;font-size:13px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:10px;margin:12px 0 18px}
.card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:10px}.num{font-size:26px;font-weight:700}
table{width:100%;border-collapse:collapse;background:#fff}th,td{border:1px solid #ddd;padding:6px 8px;text-align:left;vertical-align:top}
.accepted{color:#0b6b30}.rejected{color:#9b2020}.text{white-space:pre-wrap;line-height:1.3}
</style></head><body>`)
	b.WriteString("<h1>Lead Audit Dashboard</h1>")
	now := time.Now()
	if s.statsLoc != nil {
		now = now.In(s.statsLoc)
	}
	b.WriteString(fmt.Sprintf("<div class=\"muted\">Обновлено: %s</div>", html.EscapeString(now.Format("2006-01-02 15:04:05"))))

	lastDate := ""
	dates := sortedDates(s.dailyAccepted, s.dailyRejected)
	if len(dates) > 0 {
		lastDate = dates[len(dates)-1]
	}
	if lastDate != "" {
		cats := s.dailyByCategory[lastDate]
		b.WriteString("<div class=\"grid\">")
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Дата</div><div class=\"num\">%s</div></div>", html.EscapeString(lastDate)))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Принято</div><div class=\"num accepted\">%d</div></div>", s.dailyAccepted[lastDate]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Отклонено</div><div class=\"num rejected\">%d</div></div>", s.dailyRejected[lastDate]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Development</div><div class=\"num\">%d</div></div>", cats["development"]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Design</div><div class=\"num\">%d</div></div>", cats["design"]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Marketing/SMM</div><div class=\"num\">%d</div></div>", cats["marketing_smm"]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Marketplace Ops</div><div class=\"num\">%d</div></div>", cats["marketplace_ops"]))
		b.WriteString(fmt.Sprintf("<div class=\"card\"><div class=\"muted\">Other</div><div class=\"num\">%d</div></div>", cats["other"]))
		b.WriteString("</div>")
	}

	b.WriteString("<h2>Последние принятые</h2><table><tr><th>Время</th><th>Чат</th><th>Категория</th><th>Подкатегория</th><th>Причина AI</th><th>Текст</th></tr>")
	for i := len(s.recentAccepted) - 1; i >= 0 && i >= len(s.recentAccepted)-120; i-- {
		r := s.recentAccepted[i]
		b.WriteString("<tr>")
		msgTime := r.MessageTime
		if s.statsLoc != nil {
			msgTime = msgTime.In(s.statsLoc)
		}
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(msgTime.Format("2006-01-02 15:04"))))
		b.WriteString(fmt.Sprintf("<td>%d</td>", r.ChatID))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.DeliveryCategory)))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.Subcategory)))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.AIReason)))
		b.WriteString(fmt.Sprintf("<td class=\"text\">%s</td>", html.EscapeString(r.Text)))
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")

	b.WriteString("<h2>Последние отклоненные</h2><table><tr><th>Время</th><th>Чат</th><th>Причина</th><th>AI класс</th><th>AI причина</th><th>Текст</th></tr>")
	for i := len(s.recentRejected) - 1; i >= 0 && i >= len(s.recentRejected)-120; i-- {
		r := s.recentRejected[i]
		b.WriteString("<tr>")
		msgTime := r.MessageTime
		if s.statsLoc != nil {
			msgTime = msgTime.In(s.statsLoc)
		}
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(msgTime.Format("2006-01-02 15:04"))))
		b.WriteString(fmt.Sprintf("<td>%d</td>", r.ChatID))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.Reason)))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.AIClass)))
		b.WriteString(fmt.Sprintf("<td>%s</td>", html.EscapeString(r.AIReason)))
		b.WriteString(fmt.Sprintf("<td class=\"text\">%s</td>", html.EscapeString(r.Text)))
		b.WriteString("</tr>")
	}
	b.WriteString("</table></body></html>")
	_ = os.WriteFile(s.dashboardPath, []byte(b.String()), 0644)
}

func parseIntSafe(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}

func parseInt64Safe(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func sanitizeTSV(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}
